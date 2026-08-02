package server

import (
	"net/http"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/media"
	"chat/server/store"
	"chat/server/store/types"

	"golang.org/x/text/language"
)

// hello 处理客户端 {hi} 握手包。
func (s *Session) hello(msg *ClientComMessage) {
	var params map[string]any
	var deviceIDUpdate bool

	if s.ver == 0 {
		s.ver = parseVersion(msg.Hi.Version)
		if s.ver == 0 {
			logs.Warn.Println("s.hello:", "解析版本号失败", s.sid)
			s.queueOut(ErrMalformed(msg.Id, "", msg.Timestamp))
			return
		}
		// 检查版本兼容性
		if versionCompare(s.ver, minSupportedVersionValue) < 0 {
			s.ver = 0
			s.queueOut(ErrVersionNotSupported(msg.Id, msg.Timestamp))
			logs.Warn.Println("s.hello:", "不支持的协议版本", s.sid)
			return
		}
		if s.wsBinary && versionCompare(s.ver, minProtobufWebSocketVersionValue) < 0 {
			s.ver = 0
			s.queueOut(ErrVersionNotSupported(msg.Id, msg.Timestamp))
			logs.Warn.Println("s.hello: protobuf websocket requires protocol 0.33", s.sid)
			return
		}

		params = map[string]any{
			"ver":                currentVersion,
			"build":              store.Store.GetAdapterName() + ":" + buildstamp,
			"maxMessageSize":     globals.maxMessageSize,
			"maxSubscriberCount": globals.maxSubscriberCount,
			// 0 表示平台认证官方大群没有产品人数上限；普通群仍受上面的
			// maxSubscriberCount 保护，防止任意用户创建热点巨型群。
			"officialLargeGroupMemberLimit": 0,
			"minTagLength":                  minTagLength,
			"maxTagLength":                  maxTagLength,
			"maxTagCount":                   globals.maxTagCount,
			"maxFileUploadSize":             globals.maxFileUploadSize,
			"reqCred":                       globals.validatorClientConfig,
			"msgDelAge":                     globals.msgDeleteAge.Seconds(),
		}
		mediaHandler := store.Store.GetMediaHandler()
		_, streamingUpload := mediaHandler.(media.StreamingMultipartHandler)
		directUpload := false
		if _, supported := mediaHandler.(media.MultipartHandler); supported {
			if capability, configurable := mediaHandler.(media.DirectUploadCapability); configurable {
				directUpload = capability.DirectUploadEnabled()
			} else {
				directUpload = true
			}
		}
		params["fileUploadStreaming"] = streamingUpload
		params["fileUploadDirect"] = directUpload
		if len(globals.iceServers) > 0 {
			params["iceServers"] = globals.iceServers
		}
		if globals.agora != nil {
			// 仅通告群组通话能力；App ID、频道和短期 Token 会在
			// 成员通过 ACL 校验并发送 call/join 后单独下发。
			params["groupCallProvider"] = constCallProviderAgora
		}
		if globals.callEstablishmentTimeout > 0 {
			params["callTimeout"] = globals.callEstablishmentTimeout
		}

		if s.proto == GRPC {
			params["servingAt"] = globals.servingAt
			if globals.cluster != nil {
				params["clusterSize"] = len(globals.cluster.nodes) + 1
			} else {
				params["clusterSize"] = 1
			}
		}

		// 在 Session 初始化时保存 ua 与 platform，后续不可更改。
		s.userAgent = msg.Hi.UserAgent
		s.platf = msg.Hi.Platform
		if s.platf == "" {
			s.platf = platformFromUA(msg.Hi.UserAgent)
		}
		// 后台 Session 模式，启动延迟通知定时器。
		if msg.Hi.Background {
			s.bkgTimer.Reset(deferredNotificationsTimeout)
		}
	} else if msg.Hi.Version == "" || parseVersion(msg.Hi.Version) == s.ver {
		// 保存变更的设备 ID + 语言，或删除此前指定的设备 ID。
		if !s.uid.IsZero() {
			var err error
			if msg.Hi.DeviceID == types.NullValue {
				// 用户请求删除设备 ID。
				deviceIDUpdate = true
				if s.deviceID != "" {
					err = store.Devices.Delete(s.uid, s.deviceID)
				}
			} else if msg.Hi.DeviceID != "" && s.deviceID != msg.Hi.DeviceID {
				deviceIDUpdate = true
				err = store.Devices.Update(s.uid, s.deviceID, &types.DeviceDef{
					DeviceId: msg.Hi.DeviceID,
					Platform: s.platf,
					LastSeen: msg.Timestamp,
					Lang:     msg.Hi.Lang,
				})

				userChannelsSubUnsub(s.uid, msg.Hi.DeviceID, true)
			}

			if err != nil {
				s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
				logs.Warn.Println("s.hello:", "设备 ID 更新失败", err, s.sid)
				return
			}
		} else {
			s.queueOut(ErrAuthRequiredReply(msg, msg.Timestamp))
			logs.Warn.Println("s.hello:", "设备 ID 更新需要身份验证", s.sid)
			return
		}
	} else {
		s.queueOut(ErrCommandOutOfSequence(msg.Id, "", msg.Timestamp))
		logs.Warn.Println("s.hello:", "会话中途无法更改协议版本号", s.sid)
		return
	}

	if msg.Hi.DeviceID == types.NullValue {
		msg.Hi.DeviceID = ""
	}
	s.deviceID = msg.Hi.DeviceID
	s.lang = msg.Hi.Lang

	if s.countryCode == "" {
		if tag, _ := language.Parse(s.lang); tag != language.Und {
			if region, conf := tag.Region(); region.IsCountry() && conf >= language.High {
				s.countryCode = region.String()
			}
		}
	}

	if s.countryCode == "" {
		if len(s.lang) > 2 {
			logs.Warn.Println("s.hello:", "无法解析语言区域 ", s.lang)
		}
		s.countryCode = globals.defaultCountryCode
	}

	var httpStatus int
	var httpStatusText string
	// 三种客户端传输的首次握手都创建协议 Session，统一返回 201。
	// 只有已完成握手后的设备信息更新返回 200。
	if deviceIDUpdate {
		httpStatus = http.StatusOK
		httpStatusText = "ok"
	} else {
		httpStatus = http.StatusCreated
		httpStatusText = "created"
	}

	ctrl := &MsgServerCtrl{Id: msg.Id, Code: httpStatus, Text: httpStatusText, Timestamp: msg.Timestamp}
	if len(params) > 0 {
		ctrl.Params = params
	}
	s.queueOut(&ServerComMessage{Ctrl: ctrl})
}

// acc 处理账号创建或属性修改 {acc}。
func (s *Session) acc(msg *ClientComMessage) {
	newAcc := strings.HasPrefix(msg.Acc.User, "new")

	var rec *auth.Rec
	if !newAcc && msg.Acc.TmpScheme != "" {
		if !s.uid.IsZero() {
			s.queueOut(ErrAlreadyAuthenticated(msg.Acc.Id, "", msg.Timestamp))
			logs.Warn.Println("s.acc: 已处于认证状态时接收到临时认证参数", s.sid)
			return
		}

		authHdl := store.Store.GetLogicalAuthHandler(msg.Acc.TmpScheme)
		if authHdl == nil {
			logs.Warn.Println("s.acc: 未知的认证方案", msg.Acc.TmpScheme, s.sid)
			s.queueOut(ErrAuthUnknownScheme(msg.Id, "", msg.Timestamp))
		}

		var err error
		rec, _, err = authHdl.Authenticate(msg.Acc.TmpSecret, s.remoteAddr)
		if err != nil {
			s.queueOut(decodeStoreError(err, msg.Acc.Id, msg.Timestamp,
				map[string]any{"what": "auth"}))
			logs.Warn.Println("s.acc: 临时认证无效", err, s.sid)
			return
		}
	}

	if newAcc {
		replyCreateUser(s, msg, rec)
	} else {
		replyUpdateUser(s, msg, rec)
	}
}

// login 处理用户登录验证 {login}。
func (s *Session) login(msg *ClientComMessage) {
	if msg.Login.Scheme == "reset" {
		if err := s.authSecretReset(msg.Login.Secret); err != nil {
			s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
		} else {
			s.queueOut(InfoAuthReset(msg.Id, msg.Timestamp))
		}
		return
	}

	if !s.uid.IsZero() {
		s.queueOut(ErrAlreadyAuthenticated(msg.Id, "", msg.Timestamp))
		return
	}

	handler := store.Store.GetLogicalAuthHandler(msg.Login.Scheme)
	if handler == nil {
		logs.Warn.Println("s.login: 未知的认证方案", msg.Login.Scheme, s.sid)
		s.queueOut(ErrAuthUnknownScheme(msg.Id, "", msg.Timestamp))
		return
	}

	rec, challenge, err := handler.Authenticate(msg.Login.Secret, s.remoteAddr)
	if err != nil {
		resp := decodeStoreError(err, msg.Id, msg.Timestamp, nil)
		if resp.Ctrl.Code >= 500 {
			logs.Warn.Println("s.login 内部错误:", err, s.sid)
		}
		s.queueOut(resp)
		return
	}

	if rec.State == types.StateUndefined {
		rec.State, err = userGetState(rec.Uid)
	}
	if err == nil && rec.State != types.StateOK {
		err = types.ErrPermissionDenied
	}

	if err != nil {
		logs.Warn.Println("s.login: 用户状态检查失败", rec.Uid, err, s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
		return
	}

	if challenge != nil {
		// 多阶段认证，向客户端下发 Challenge 挑战。
		s.queueOut(InfoChallenge(msg.Id, msg.Timestamp, challenge))
		return
	}

	var missing []string
	if rec.Features&auth.FeatureValidated == 0 && len(globals.authValidators[rec.AuthLevel]) > 0 {
		var validated []string
		if validated, _, err = validatedCreds(rec.Uid, rec.AuthLevel, msg.Login.Cred, false); err == nil {
			_, missing, _ = stringSliceDelta(globals.authValidators[rec.AuthLevel], validated)
		}
	}
	if err != nil {
		logs.Warn.Println("s.login: 验证凭证失败:", err, s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
	} else {
		s.queueOut(s.onLogin(msg.Id, msg.Timestamp, rec, missing))
	}
}

// authSecretReset 重置认证密钥；
// 参数格式: "auth-method-to-reset:credential-method:credential-value",
// 例如: "basic:email:alice@example.com"。
func (s *Session) authSecretReset(params []byte) error {
	var authScheme, credMethod, credValue string
	if parts := strings.Split(string(params), ":"); len(parts) >= 3 {
		authScheme, credMethod, credValue = parts[0], parts[1], parts[2]
	} else {
		return types.ErrMalformed
	}

	auther := store.Store.GetLogicalAuthHandler(authScheme)
	if auther == nil {
		return types.ErrUnsupported
	}
	validator := store.Store.GetValidator(credMethod)
	if validator == nil {
		return types.ErrUnsupported
	}
	uid, err := store.Users.GetByCred(credMethod, credValue)
	if err != nil {
		return err
	}
	if uid.IsZero() {
		// 防止探测已有联系人：若不存在也不报错
		return nil
	}

	resetParams, err := auther.GetResetParams(uid)
	if err != nil {
		return err
	}
	tempScheme, err := validator.TempAuthScheme()
	if err != nil {
		return err
	}

	tempAuth := store.Store.GetLogicalAuthHandler(tempScheme)
	if tempAuth == nil || !tempAuth.IsInitialized() {
		logs.Err.Println("s.authSecretReset: 验证器缺失临时认证", credMethod, tempScheme, s.sid)
		return types.ErrInternal
	}

	code, _, err := tempAuth.GenSecret(&auth.Rec{
		Uid:        uid,
		AuthLevel:  auth.LevelAuth,
		Features:   auth.FeatureNoLogin,
		Credential: credMethod + ":" + credValue,
	})
	if err != nil {
		return err
	}

	return validator.ResetSecret(credValue, authScheme, s.lang, code, resetParams)
}

// onLogin 登录成功后执行的相关步骤。
func (s *Session) onLogin(msgID string, timestamp time.Time, rec *auth.Rec, missing []string) *ServerComMessage {
	var reply *ServerComMessage
	var params map[string]any

	features := rec.Features

	params = map[string]any{
		"user":    rec.Uid.UserId(),
		"authlvl": rec.AuthLevel.String(),
	}
	if len(missing) > 0 {
		// 部分凭证尚未验证，下发验证请求。
		reply = InfoValidateCredentials(msgID, timestamp)

		params["cred"] = missing
	} else {
		// 全部正常，认证该 Session。

		reply = NoErr(msgID, "", timestamp)

		if features&auth.FeatureNoLogin == 0 {
			s.uid = rec.Uid
			if globals.sessionStore != nil {
				globals.sessionStore.SetSessionUid(s, rec.Uid)
			}
			s.authLvl = rec.AuthLevel
			rec.Lifetime = 0
		}
		features |= auth.FeatureValidated

		// 记录设备信息
		if s.deviceID != "" {
			if err := store.Devices.Update(rec.Uid, "", &types.DeviceDef{
				DeviceId: s.deviceID,
				Platform: s.platf,
				LastSeen: timestamp,
				Lang:     s.lang,
			}); err != nil {
				logs.Warn.Println("更新设备记录失败:", err)
			}
		}
	}

	rec.Features = features
	params["token"], params["expires"], _ = store.Store.GetLogicalAuthHandler("token").GenSecret(rec)

	reply.Ctrl.Params = params
	return reply
}
