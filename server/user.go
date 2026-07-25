package main

import (
	"container/heap"
	"errors"
	"math/rand"
	"time"

	"slices"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/push"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	// 未读计数器更新返回码
	// 计数器未初始化，IO 挂起
	unreadUpdateIOPending = -1
	// 计数器初始化错误
	unreadUpdateError = -2
)

// replyCreateUser 处理新账号请求
func replyCreateUser(s *Session, msg *ClientComMessage, rec *auth.Rec) {
	// Session 无法使用新账号认证，因为它已经认证了
	if msg.Acc.Login && (!s.uid.IsZero() || rec != nil) {
		s.queueOut(ErrAlreadyAuthenticated(msg.Id, "", msg.Timestamp))
		logs.Warn.Println("create user: login requested while authenticated, sid=", s.sid)
		return
	}

	// 查找请求方案对应的认证器
	authhdl := store.Store.GetLogicalAuthHandler(msg.Acc.Scheme)
	if authhdl == nil {
		// 新账号必须有认证方案
		s.queueOut(ErrMalformed(msg.Id, "", msg.Timestamp))
		logs.Warn.Println("create user: unknown auth handler, sid=", s.sid)
		return
	}

	// 检查登录名是否唯一以及是否符合策略（不太长也不太短）
	if ok, err := authhdl.IsUnique(msg.Acc.Secret, s.remoteAddr); !ok {
		logs.Warn.Println("create user: auth secret is not compliant", err, "sid=", s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp,
			map[string]any{"what": "auth"}))
		return
	}

	var user types.User
	var private any

	// 如果要设置账号状态，确保发送者是 root 用户
	if msg.Acc.State != "" {
		if auth.Level(msg.AuthLvl) != auth.LevelRoot {
			logs.Warn.Println("create user: attempt to set account state by non-root, sid=", s.sid)
			msg := ErrPermissionDenied(msg.Id, "", msg.Timestamp)
			msg.Ctrl.Params = map[string]any{"what": "state"}
			s.queueOut(msg)
			return
		}

		state, err := types.NewObjState(msg.Acc.State)
		if err != nil || state == types.StateUndefined || state == types.StateDeleted {
			logs.Warn.Println("create user: invalid account state", err, "sid=", s.sid)
			s.queueOut(ErrMalformed(msg.Id, "", msg.Timestamp))
			return
		}
		user.State = state
	}

	// 确保标签唯一且不受限制
	if tags := normalizeTags(msg.Acc.Tags, globals.maxTagCount); tags != nil {
		if !restrictedTagsEqual(tags, nil, globals.immutableTagNS) {
			logs.Warn.Println("create user: attempt to directly assign restricted tags, sid=", s.sid)
			msg := ErrPermissionDenied(msg.Id, "", msg.Timestamp)
			msg.Ctrl.Params = map[string]any{"what": "tags"}
			s.queueOut(msg)
			return
		}
		user.Tags = tags
	}

	// 预检查凭证有效性。此时不知道用户的访问级别
	// 因此无法检查是否存在必需凭证，必须稍后检查
	creds := normalizeCredentials(msg.Acc.Cred, true)
	for i := range creds {
		cr := &creds[i]
		vld := store.Store.GetValidator(cr.Method)
		if _, err := vld.PreCheck(cr.Value, cr.Params); err != nil {
			logs.Warn.Println("create user: failed credential pre-check", cr, err, "sid=", s.sid)
			s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp,
				map[string]any{"what": cr.Method}))
			return
		}
	}

	// 分配默认访问权限，以防账号创建者未提供
	user.Access.Auth = getDefaultAccess(types.TopicCatP2P, true, false) |
		getDefaultAccess(types.TopicCatGrp, true, false)
	user.Access.Anon = getDefaultAccess(types.TopicCatP2P, false, false) |
		getDefaultAccess(types.TopicCatGrp, false, false)

	// 分配实际访问权限、公开和私有信息
	if msg.Acc.Desc != nil {
		if msg.Acc.Desc.DefaultAcs != nil {
			if msg.Acc.Desc.DefaultAcs.Auth != "" {
				user.Access.Auth.UnmarshalText([]byte(msg.Acc.Desc.DefaultAcs.Auth))
				user.Access.Auth &= globals.typesModeCP2P
				if user.Access.Auth != types.ModeNone {
					user.Access.Auth |= types.ModeApprove
				}
			}
			if msg.Acc.Desc.DefaultAcs.Anon != "" {
				user.Access.Anon.UnmarshalText([]byte(msg.Acc.Desc.DefaultAcs.Anon))
				user.Access.Anon &= globals.typesModeCP2P
				if user.Access.Anon != types.ModeNone {
					user.Access.Anon |= types.ModeApprove
				}
			}
		}
		if !isNullValue(msg.Acc.Desc.Public) {
			user.Public = msg.Acc.Desc.Public
		}
		if !isNullValue(msg.Acc.Desc.Private) {
			private = msg.Acc.Desc.Private
		}
	}

	// 在数据库中创建用户记录
	if _, err := store.Users.Create(&user, private); err != nil {
		logs.Warn.Println("create user: failed to create user", err, "sid=", s.sid)
		s.queueOut(ErrUnknown(msg.Id, "", msg.Timestamp))
		return
	}

	// 添加认证记录，authhdl.AddRecord 可能会更改标签
	rec, err := authhdl.AddRecord(&auth.Rec{Uid: user.Uid(), Tags: user.Tags}, msg.Acc.Secret, s.remoteAddr)
	if err != nil {
		logs.Warn.Println("create user: add auth record failed", err, "sid=", s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))

		// 尝试删除不完整的用户记录
		if err = store.Users.Delete(user.Uid(), true); err != nil {
			logs.Warn.Println("create user: failed to delete incomplete user record", err, "sid=", s.sid)
		}
		return
	}

	// 创建账号时，用户必须提供所有必需凭证
	// 如果有任何缺失，拒绝请求
	if len(creds) < len(globals.authValidators[rec.AuthLevel]) {
		logs.Warn.Println("create user: missing credentials; have:", creds, "want:",
			globals.authValidators[rec.AuthLevel], s.sid)
		_, missing, _ := stringSliceDelta(globals.authValidators[rec.AuthLevel], credentialMethods(creds))
		s.queueOut(decodeStoreError(types.ErrPolicy, msg.Id, msg.Timestamp,
			map[string]any{"creds": missing}))

		// 尝试删除不完整的用户记录
		if err = store.Users.Delete(user.Uid(), true); err != nil {
			logs.Warn.Println("create user: failed to delete incomplete user record", err, "sid=", s.sid)
		}
		return
	}

	// 保存凭证，必要时更新标签
	tmpToken, _, _ := store.Store.GetLogicalAuthHandler("token").GenSecret(&auth.Rec{
		Uid:       user.Uid(),
		AuthLevel: auth.LevelAuth,
		Lifetime:  auth.Duration(time.Hour * 24),
	})
	validated, _, err := addCreds(user.Uid(), creds, rec.Tags, s.lang, tmpToken)
	if err != nil {
		logs.Warn.Println("create user: failed to save or validate credential", err, "sid=", s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))

		// 删除不完整的用户记录
		if err = store.Users.Delete(user.Uid(), true); err != nil {
			logs.Warn.Println("create user: failed to delete incomplete user record", err, "sid=", s.sid)
		}
		return
	}

	if msg.Extra != nil && len(msg.Extra.Attachments) > 0 {
		if err := store.Files.LinkAttachments(user.Uid().UserId(), types.ZeroUid, msg.Extra.Attachments); err != nil {
			logs.Warn.Println("create user: failed to link avatar attachment", err, "sid=", s.sid)
			// 这不是严重错误，继续执行
		}
	}

	var reply *ServerComMessage
	if msg.Acc.Login {
		// 处理用户登录请求
		_, missing, _ := stringSliceDelta(globals.authValidators[rec.AuthLevel], validated)
		reply = s.onLogin(msg.Id, msg.Timestamp, rec, missing)
	} else {
		// 不使用新账号登录
		reply = NoErrCreated(msg.Id, "", msg.Timestamp)
		reply.Ctrl.Params = map[string]any{
			"user":    user.Uid().UserId(),
			"authlvl": rec.AuthLevel.String(),
		}
	}

	params := reply.Ctrl.Params.(map[string]any)
	params["desc"] = &MsgTopicDesc{
		CreatedAt: &user.CreatedAt,
		UpdatedAt: &user.UpdatedAt,
		DefaultAcs: &MsgDefaultAcsMode{
			Auth: user.Access.Auth.String(),
			Anon: user.Access.Anon.String(),
		},
		Public:  user.Public,
		Private: private,
	}

	s.queueOut(reply)

	pluginAccount(&user, plgActCreate)
}

// replyUpdateUser 处理账号更新：
// * 认证更新，即登录名/密码更改
// * 凭证更新
func replyUpdateUser(s *Session, msg *ClientComMessage, rec *auth.Rec) {
	if s.uid.IsZero() && rec == nil {
		// Session 未认证且未提供临时认证
		logs.Warn.Println("replyUpdateUser: not a new account and not authenticated", s.sid)
		s.queueOut(ErrPermissionDenied(msg.Id, "", msg.Timestamp))
		return
	} else if msg.AsUser != "" && rec != nil {
		// 两个 UID：一个来自 msg.from，一个来自临时认证，有歧义，拒绝
		logs.Warn.Println("replyUpdateUser: got both authenticated session and token", s.sid)
		s.queueOut(ErrMalformed(msg.Id, "", msg.Timestamp))
		return
	}

	userId := msg.AsUser
	authLvl := auth.Level(msg.AuthLvl)
	if rec != nil {
		userId = rec.Uid.UserId()
		authLvl = rec.AuthLevel
	}

	if msg.Acc.User != "" && msg.Acc.User != userId {
		if s.authLvl != auth.LevelRoot {
			logs.Warn.Println("replyUpdateUser: attempt to change another's account by non-root", s.sid)
			s.queueOut(ErrPermissionDenied(msg.Id, "", msg.Timestamp))
			return
		}
		// Root 正在编辑他人的账号
		userId = msg.Acc.User
		authLvl = auth.ParseAuthLevel(msg.Acc.AuthLevel)
	}

	uid := types.ParseUserId(userId)
	if uid.IsZero() {
		// msg.Acc.用户包含无效数据
		s.queueOut(ErrMalformed(msg.Id, "", msg.Timestamp))
		logs.Warn.Println("replyUpdateUser: user id is invalid or missing", s.sid)
		return
	}

	// 只有 root 可以暂停账号，包括自己的账号
	if msg.Acc.State != "" && s.authLvl != auth.LevelRoot {
		s.queueOut(ErrPermissionDenied(msg.Id, "", msg.Timestamp))
		logs.Warn.Println("replyUpdateUser: attempt to change account state by non-root", s.sid)
		return
	}

	user, err := store.Users.Get(uid)
	if user == nil && err == nil {
		err = types.ErrNotFound
	}
	if err != nil {
		logs.Warn.Println("replyUpdateUser: failed to fetch user from DB", err, s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
		return
	}

	var params map[string]any
	if msg.Acc.Scheme != "" {
		err = updateUserAuth(msg, user, rec, s.remoteAddr)
	} else if len(msg.Acc.Cred) > 0 {
		if authLvl == auth.LevelNone {
			// msg.Acc.AuthLevel 包含无效数据
			s.queueOut(ErrMalformed(msg.Id, "", msg.Timestamp))
			logs.Warn.Println("replyUpdateUser: auth level is missing", s.sid)
			return
		}
		// 处理更新凭证的请求
		tmpToken, _, _ := store.Store.GetLogicalAuthHandler("token").GenSecret(&auth.Rec{
			Uid:       uid,
			AuthLevel: auth.LevelNone,
			Lifetime:  auth.Duration(time.Hour * 24),
			Features:  auth.FeatureNoLogin,
		})
		_, _, err := addCreds(uid, msg.Acc.Cred, nil, s.lang, tmpToken)
		if err == nil {
			if allCreds, err := store.Users.GetAllCreds(uid, "", true); err == nil {
				var validated []string
				for i := range allCreds {
					validated = append(validated, allCreds[i].Method)
				}
				_, missing, _ := stringSliceDelta(globals.authValidators[authLvl], validated)
				if len(missing) > 0 {
					params = map[string]any{"cred": missing}
				}
			}
		}
	} else if msg.Acc.State != "" {
		var changed bool
		changed, err = changeUserState(s, uid, user, msg)
		if !changed && err == nil {
			s.queueOut(InfoNotModified(msg.Id, "", msg.Timestamp))
			return
		}
	} else {
		err = types.ErrMalformed
	}

	if err != nil {
		logs.Warn.Println("replyUpdateUser: failed to update user", err, s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
		return
	}

	s.queueOut(NoErrParams(msg.Id, "", msg.Timestamp, params))

	// 使用账号更新调用插件
	pluginAccount(user, plgActUpd)
}

// updateUserAuth 认证更新
func updateUserAuth(msg *ClientComMessage, user *types.User, _ *auth.Rec, remoteAddr string) error {
	authhdl := store.Store.GetLogicalAuthHandler(msg.Acc.Scheme)
	if authhdl != nil {
		rec, err := authhdl.UpdateRecord(&auth.Rec{Uid: user.Uid(), Tags: user.Tags}, msg.Acc.Secret, remoteAddr)
		if errors.Is(err, types.ErrNotFound) || err == types.ErrNotFound {
			// 若该方案在此用户上尚无记录，作为新认证方案添加
			rec, err = authhdl.AddRecord(&auth.Rec{Uid: user.Uid(), Tags: user.Tags}, msg.Acc.Secret, remoteAddr)
		}
		if err != nil {
			return err
		}

		// 标签可能已被 authhdl 更改，重置它们
		// 此处无法处理错误，仅记录日志但不返回
		if _, err = store.Users.UpdateTags(user.Uid(), nil, nil, rec.Tags); err != nil {
			logs.Warn.Println("updateUserAuth tags update failed:", err)
		}
		return nil
	}

	// 无效或未知认证方案
	return types.ErrMalformed
}

// addCreds 添加新凭证并重新发送现有凭证的验证请求
// 必要时还会添加凭证定义的标签
// 返回仅在此调用中验证的方法，返回完整的标签集
// 或当标签未变时返回 nil
func addCreds(uid types.Uid, creds []MsgCredClient, extraTags []string,
	lang string, tmpToken []byte) ([]string, []string, error) {
	var validated []string
	for i := range creds {
		cr := &creds[i]
		vld := store.Store.GetValidator(cr.Method)
		if vld == nil || !vld.IsInitialized() {
			// 忽略未知或未初始化的验证器
			continue
		}

		isNew, err := vld.Request(uid, cr.Value, lang, cr.Response, tmpToken)
		if err != nil {
			return nil, nil, err
		}

		if isNew && cr.Response != "" {
			// 如果提供了响应且 vld.Request 未返回错误，说明新请求验证成功
			validated = append(validated, cr.Method)

			// 为已确认的凭证生成标签
			if globals.validators[cr.Method].addToTags {
				extraTags = append(extraTags, cr.Method+":"+cr.Value)
			}
		}
	}

	// 保存验证器可能已更改的标签
	if len(extraTags) > 0 {
		if utags, err := store.Users.UpdateTags(uid, extraTags, nil, nil); err == nil {
			extraTags = utags
		} else {
			logs.Warn.Println("add cred tags update failed:", err)
		}
	} else {
		extraTags = nil
	}
	return validated, extraTags, nil
}

// validatedCreds 返回已验证的凭证列表，包括本次调用中验证的凭证。
// 返回所有已验证的方法（包括之前和本次验证的）。
// 返回完整的标签集，或标签未变时返回 nil。
func validatedCreds(uid types.Uid, authLvl auth.Level, creds []MsgCredClient,
	errorOnFail bool) ([]string, []string, error) {
	// 检查是否需要凭证验证
	if len(globals.authValidators[authLvl]) == 0 {
		return nil, nil, nil
	}

	// 获取所有已验证的方法
	allCreds, err := store.Users.GetAllCreds(uid, "", true)
	if err != nil {
		return nil, nil, err
	}

	methods := make(map[string]struct{})
	for i := range allCreds {
		methods[allCreds[i].Method] = struct{}{}
	}

	// 添加本次调用中验证的凭证。
	// 移除未知的验证器
	creds = normalizeCredentials(creds, false)
	var tagsToAdd []string
	for i := range creds {
		cr := &creds[i]
		if cr.Response == "" {
			// 忽略空响应
			continue
		}

		// 无需检查 nil，未知方法已在前面移除
		vld := store.Store.GetValidator(cr.Method)
		value, err := vld.Check(uid, cr.Response)

		if err != nil {
			// 检查失败
			if storeErr, ok := err.(types.StoreError); ok && storeErr == types.ErrCredentials {
				if errorOnFail {
					// 报告无效响应
					return nil, nil, types.ErrInvalidResponse
				}
				// 跳过无效响应，凭证保持未验证状态
				continue
			}
			// 实际错误，向上报告
			return nil, nil, err
		}

		// 检查未返回错误：请求验证成功
		methods[cr.Method] = struct{}{}

		// 将已验证的凭证添加到用户标签
		if globals.validators[cr.Method].addToTags {
			tagsToAdd = append(tagsToAdd, cr.Method+":"+value)
		}
	}

	var tags []string
	if len(tagsToAdd) > 0 {
		// 保存标签更新
		if utags, err := store.Users.UpdateTags(uid, tagsToAdd, nil, nil); err == nil {
			tags = utags
		} else {
			logs.Warn.Println("validated creds tags update failed:", err)
			tags = nil
		}
	} else {
		tags = nil
	}

	validated := make([]string, 0, len(methods))
	for method := range methods {
		validated = append(validated, method)
	}

	return validated, tags, nil
}

// deleteCred 删除用户凭证。
// 返回完整的剩余标签集，或标签未变时返回 nil。
func deleteCred(uid types.Uid, authLvl auth.Level, cred *MsgCredClient) ([]string, error) {
	vld := store.Store.GetValidator(cred.Method)
	if vld == nil || cred.Value == "" {
		// 拒绝无效请求：未知验证方法或缺少凭证值
		return nil, types.ErrMalformed
	}

	// 此验证级别是否要求该凭证？
	isRequired := slices.Contains(globals.authValidators[authLvl], cred.Method)

	// 如果凭证是必需的，确保删除后该方法仍有已验证的凭证
	if isRequired {
		// 同一方法可能有多个已验证凭证，因此需要获取每个方法的计数映射

		// 获取指定方法的所有凭证
		allCreds, err := store.Users.GetAllCreds(uid, cred.Method, false)
		if err != nil {
			return nil, err
		}

		// 检查是否可以安全删除：存在另一个已验证值，
		// 或者该值本身尚未验证
		var okTodelete bool
		for _, cr := range allCreds {
			if (cr.Done && cr.Value != cred.Value) || (!cr.Done && cr.Value == cred.Value) {
				okTodelete = true
				break
			}
		}

		if !okTodelete {
			// 拒绝：这是唯一已验证的凭证，必须提供
			return nil, types.ErrPolicy
		}
	}

	// 凭证非必需，或者该方法有多个已验证凭证
	err := vld.Remove(uid, cred.Value)
	if err != nil {
		if err == types.ErrNotFound {
			// 未找到凭证，无法删除
			err = nil
		}
		return nil, err
	}

	// 移除已删除凭证生成的标签
	var tags []string
	if globals.validators[cred.Method].addToTags {
		// 此错误不应返回给用户
		if utags, err := store.Users.UpdateTags(uid, nil, []string{cred.Method + ":" + cred.Value}, nil); err == nil {
			tags = utags
		} else {
			logs.Warn.Println("delete cred: failed to update tags:", err)
			tags = nil
		}
	} else {
		tags = nil
	}

	return tags, nil
}

// 更改用户状态：暂停/正常 (ok)。
// 1. 不需要 -- 禁用/启用登录（登录后检查状态）。
// 2. 如果暂停，驱逐用户的所有 Session。恢复时跳过此步骤。
// 3. 暂停/激活与该用户的 p2p Topic。
// 4. 暂停/激活该用户拥有的群组 Topic。
// 5. 更新用户数据库记录。
func changeUserState(s *Session, uid types.Uid, user *types.User, msg *ClientComMessage) (bool, error) {
	state, err := types.NewObjState(msg.Acc.State)
	if err != nil || state == types.StateUndefined {
		logs.Warn.Println("replyUpdateUser: invalid account state", s.sid)
		return false, types.ErrMalformed
	}

	// 状态未变
	if user.State == state {
		return false, nil
	}

	if state != types.StateOK {
		// 终止所有 Session
		globals.sessionStore.EvictUser(uid, "")
	}

	err = store.Users.UpdateState(uid, state)
	if err != nil {
		return false, err
	}

	// 更新内存中已加载的用户 p2p 和群组所有者 Topic 的状态
	globals.hub.userStatus <- &userStatusReq{forUser: uid, state: state}
	user.State = state

	return true, err
}

// 请求删除用户：
// 1. 禁用用户登录
// 2. 终止用户的所有 Session（当前 Session 除外）
// 3. 停止所有活跃的 Topic
// 4. 通知其他订阅者 Topic 正在被删除
// 5. 从数据库中删除用户
// 6. 报告成功或失败
// 7. 终止用户的最后一个 Session
func replyDelUser(s *Session, msg *ClientComMessage) {
	var uid types.Uid

	if msg.Del.User == "" || msg.Del.User == s.uid.UserId() {
		// 检查是否禁用了账号删除
		if globals.permanentAccounts && s.authLvl != auth.LevelRoot {
			logs.Warn.Println("replyDelUser: account deletion disabled", s.sid)
			s.queueOut(ErrPolicy(msg.Id, "", msg.Timestamp))
			return
		}

		// 删除当前用户
		uid = s.uid
	} else if s.authLvl == auth.LevelRoot {
		// 删除其他用户
		uid = types.ParseUserId(msg.Del.User)
		if uid.IsZero() {
			logs.Warn.Println("replyDelUser: invalid user ID", msg.Del.User, s.sid)
			s.queueOut(ErrMalformed(msg.Id, "", msg.Timestamp))
			return
		}
	} else {
		logs.Warn.Println("replyDelUser: illegal attempt to delete another user", msg.Del.User, s.sid)
		s.queueOut(ErrPermissionDenied(msg.Id, "", msg.Timestamp))
		return
	}

	// 禁用所有认证器
	authnames := store.Store.GetAuthNames()
	for _, name := range authnames {
		hdl := store.Store.GetLogicalAuthHandler(name)
		if !hdl.IsInitialized() {
			continue
		}
		if err := hdl.DelRecords(uid); err != nil {
			// 这可能是完全无害的，例如认证器存在但未使用
			logs.Warn.Println("replyDelUser: failed to delete auth record", uid.UserId(), name, err, s.sid)
			if storeErr, ok := err.(types.StoreError); ok && storeErr == types.ErrUnsupported {
				// 认证器拒绝删除记录：用户账号无法被删除
				s.queueOut(ErrOperationNotAllowed(msg.Id, "", msg.Timestamp))
				return
			}
		}
	}

	// 终止所有 Session。跳过当前 Session，以便请求者收到响应
	globals.sessionStore.EvictUser(uid, s.sid)
	// 从缓存中移除用户，并向集群宣布该用户已删除
	usersRemoveUser(uid)

	// 停止该用户拥有的 Topic 和 p2p Topic
	done := make(chan bool)
	globals.hub.unreg <- &topicUnreg{forUser: uid, del: msg.Del.Hard, done: done}
	<-done

	// 通知关注该用户的其他用户，该用户已下线
	if uoi, err := store.Users.GetSubs(uid); err == nil {
		presUsersOfInterestOffline(uid, uoi, "gone")
	} else {
		logs.Warn.Println("replyDelUser: failed to send notifications to users", err, s.sid)
	}

	// 通知该用户拥有的群组 Topic 的订阅者，Topic 已被删除
	if ownTopics, err := store.Users.GetOwnTopics(uid); err == nil {
		for _, topicName := range ownTopics {
			if subs, err := store.Topics.GetSubs(topicName, nil); err == nil {
				presSubsOfflineOffline(topicName, types.TopicCatGrp, subs, "gone", &presParams{}, s.sid)
			} else {
				logs.Warn.Println("replyDelUser: failed to notify topic subscribers", err, topicName, s.sid)
			}
		}
	} else {
		logs.Warn.Println("replyDelUser: failed to send notifications to owned topics", err, s.sid)
	}

	// 暂停/驱逐所有与该用户关联的 P2P Topic
	if uoi, err := store.Users.GetSubs(uid); err == nil {
		for _, sub := range uoi {
			if types.GetTopicCat(sub.Topic) == types.TopicCatP2P {
				globals.hub.unreg <- &topicUnreg{rcptTo: sub.Topic}
			}
		}
	} else {
		logs.Warn.Println("replyDelUser: failed to fetch P2P topics for user eviction", err, s.sid)
	}

	// 从数据库中删除用户记录
	if err := store.Users.Delete(uid, msg.Del.Hard); err != nil {
		logs.Warn.Println("replyDelUser: failed to delete user", err, s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
		return
	}

	s.queueOut(NoErr(msg.Id, "", msg.Timestamp))

	if s.uid == uid && s.multi == nil {
		// 如果当前 Session 属于被删除的用户，将其驱逐
		// 无需发送到多路复用 Session：远程节点会单独收到通知
		_, data := s.serialize(NoErrEvicted("", "", msg.Timestamp))
		s.stopSession(data)
	}
}

// 从数据库读取用户状态
func userGetState(uid types.Uid) (types.ObjState, error) {
	user, err := store.Users.Get(uid)
	if err != nil {
		return types.StateUndefined, err
	}
	if user == nil {
		return types.StateUndefined, types.ErrUserNotFound
	}
	return user.State, nil
}

// 为单个用户的设备订阅或取消订阅所有 FCM Topic（Channel）
func userChannelsSubUnsub(uid types.Uid, deviceID string, sub bool) {
	push.ChannelSub(&push.ChannelReq{
		Uid:      uid,
		DeviceID: deviceID,
		Unsub:    !sub,
	})
}

// UserCacheReq 包含用于变更一个或多个用户缓存条目的数据
type UserCacheReq struct {
	// 集群模式下发送此请求的节点名称，否则不设置
	Node string

	// 当单个用户的未读消息数更新或用户被删除时设置
	UserId types.Uid
	// 当 Topic 的订阅者数量更新时设置
	UserIdList []types.Uid
	// 未读计数（UserId 已设置）
	Unread int

	// 如果设置了 UserId：将 Unread 计数视为增量而非最终值
	// 如果设置了 UserIdList：增加 (Inc == true) 或减少订阅计数
	Inc bool
	// 用户正在被删除，从缓存中移除用户
	Gone bool

	// 可选的推送通知
	PushRcpt *push.Receipt
}

type userCacheEntry struct {
	unread int
	topics int
}

// 从数据库读取未读计数器时保留的更新条目
type bufferedUpdate struct {
	val int
	inc bool
}

type ioResult struct {
	counts map[types.Uid]int
	err    error
}

// 表示待处理的推送通知回执
type pendingReceipt struct {
	// 当前正在从数据库读取的未读计数器数量
	pendingIOs int
	// 索引由 update 使用，由 heap.Interface 方法维护
	index int
	// 底层回执
	rcpt *push.Receipt
}

// 待处理推送按优先级队列组织（优先级 = 待处理 IO 数量）
// 可快速发现已准备好发送的回执（待处理 IO 数为 0）
type pendingReceiptsQueue []*pendingReceipt

// Heap 接口方法
func (pq pendingReceiptsQueue) Len() int { return len(pq) }

func (pq pendingReceiptsQueue) Less(i, j int) bool {
	// 我们希望 Pop 返回最高优先级而非最低优先级，因此使用大于号
	return pq[i].pendingIOs < pq[j].pendingIOs
}

func (pq pendingReceiptsQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *pendingReceiptsQueue) Push(x any) {
	n := len(*pq)
	item := x.(*pendingReceipt)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *pendingReceiptsQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // 避免内存泄漏
	item.index = -1 // 安全起见
	*pq = old[0 : n-1]
	return item
}

func (pq *pendingReceiptsQueue) fix(index int) {
	heap.Fix(pq, index)
}

// 初始化用户缓存
func usersInit() {
	globals.usersUpdate = make(chan *UserCacheReq, 1024)

	go userUpdater()
}

// 关闭用户缓存
func usersShutdown() {
	if globals.usersUpdate != nil {
		globals.usersUpdate <- nil
	}
}

func usersUpdateUnread(uid types.Uid, val int, inc bool) {
	if globals.usersUpdate == nil || (val == 0 && inc) {
		return
	}

	upd := &UserCacheReq{UserId: uid, Unread: val, Inc: inc}
	if globals.cluster.isRemoteTopic(uid.UserId()) {
		// 发送请求到拥有该用户的远程节点
		globals.cluster.routeUserReq(upd)
	} else {
		select {
		case globals.usersUpdate <- upd:
		default:
		}
	}
}

// 开始跟踪单个用户。用于缓存管理
// 'add' 增加/减少用户订阅的 Topic 数量
func usersRegisterUser(uid types.Uid, add bool) {
	if globals.usersUpdate == nil {
		return
	}

	upd := &UserCacheReq{UserIdList: make([]types.Uid, 1), Inc: add}
	upd.UserIdList[0] = uid

	if globals.cluster.isRemoteTopic(uid.UserId()) {
		// 发送请求到拥有该用户的远程节点
		globals.cluster.routeUserReq(upd)
	} else {
		select {
		case globals.usersUpdate <- upd:
		default:
		}
	}
}

// 停止跟踪用户并从缓存中移除
func usersRemoveUser(uid types.Uid) {
	if globals.usersUpdate == nil {
		return
	}

	upd := &UserCacheReq{UserId: uid, Gone: true}
	if !globals.cluster.isRemoteTopic(uid.UserId()) {
		select {
		case globals.usersUpdate <- upd:
		default:
		}
	}

	if globals.cluster != nil {
		// 即使用户是本地的也向集群广播
		globals.cluster.routeUserReq(upd)
	}
}

// 将用户计入活跃 Topic 的成员。用于缓存管理
// 在集群模式下，此方法仅在 Topic 为本地时调用：
// globals.cluster.isRemoteTopic(t.name) == false
func usersRegisterTopic(t *Topic, add bool) {
	if globals.usersUpdate == nil {
		return
	}

	if t.cat == types.TopicCatFnd || t.cat == types.TopicCatMe {
		// 忽略 me 和 fnd Topic
		return
	}

	local := &UserCacheReq{Inc: add}

	// 在集群模式下，UID 可能是本地或远程的。在本地处理本地 UID，
	// 将远程 UID 发送到其他集群节点处理。UID 可能需要发送到多个节点
	remote := &UserCacheReq{Inc: add}
	for uid, pud := range t.perUser {
		if pud.isChan {
			// 跳过 Channel 订阅者
			continue
		}
		if globals.cluster.isRemoteTopic(uid.UserId()) {
			remote.UserIdList = append(remote.UserIdList, uid)
		} else {
			local.UserIdList = append(local.UserIdList, uid)
		}
	}

	if len(remote.UserIdList) > 0 {
		globals.cluster.routeUserReq(remote)
	}

	if len(local.UserIdList) > 0 {
		select {
		case globals.usersUpdate <- local:
		default:
			logs.Err.Println("User cache: globals.usersUpdate queue full: ", len(globals.usersUpdate))
		}
	}
}

// usersRequestFromCluster 处理来自其他集群节点的请求
func usersRequestFromCluster(req *UserCacheReq) {
	if globals.usersUpdate == nil {
		return
	}

	select {
	case globals.usersUpdate <- req:
	default:
	}
}

var usersCache map[types.Uid]userCacheEntry

// 处理用户缓存更新的协程
func userUpdater() {
	// 缓存未读计数器和用户订阅的 Topic 数量
	usersCache = make(map[types.Uid]userCacheEntry)

	// 每个用户因 IO 阻塞的未读计数器更新。IO 完成时刷新
	perUserBuffers := make(map[types.Uid][]bufferedUpdate)

	// 因 IO 阻塞的推送通知接收者（部分接收者的未读计数器
	// 正在从数据库读取），按用户分组
	perUserPendingReceipts := make(map[types.Uid][]*pendingReceipt)

	// 所有待处理推送回执按待处理 IO 数量组织的优先级队列
	receiptQueue := pendingReceiptsQueue{}

	// IO 回调队列
	ioDone := make(chan *ioResult, 1024)

	unreadUpdater := func(uids []types.Uid, vals []int, inc bool) map[types.Uid]int {
		var dbPending []types.Uid
		counts := make(map[types.Uid]int, len(uids))
		for i, uid := range uids {
			counts[uid] = 0
			uce, ok := usersCache[uid]
			if !ok {
				logs.Err.Println("ERROR: attempt to update unread count for user who has not been loaded", uid)
				counts[uid] = unreadUpdateError
				continue
			}

			val := vals[i]
			if uce.unread < 0 {
				// 未读计数器尚未初始化。是否启动数据库读取？
				if updateBuf, ioInProgress := perUserBuffers[uid]; ioInProgress {
					// 缓存此更新
					updateBuf = append(updateBuf, bufferedUpdate{val: val, inc: inc})
					perUserBuffers[uid] = updateBuf
				} else {
					// 调度从数据库读取计数器
					updateBuf = []bufferedUpdate{}
					perUserBuffers[uid] = updateBuf
					dbPending = append(dbPending, uid)
				}
				counts[uid] = unreadUpdateIOPending
				continue

			} else if inc {
				uce.unread += val
			} else {
				uce.unread = val
			}

			usersCache[uid] = uce
			counts[uid] = uce.unread
		}

		if len(dbPending) > 0 {
			go func() {
				dbUnread, err := store.Users.GetUnreadCount(dbPending...)
				if err != nil {
					logs.Warn.Println("users: failed to load unread count: ", err)
				}
				ioDone <- &ioResult{counts: dbUnread, err: err}
			}()
		}

		return counts
	}

	for {
		select {
		case io := <-ioDone:
			// 未读计数器读取完成
			for uid, count := range io.counts {
				updateBuf, ok := perUserBuffers[uid]
				// 停止缓存更新。新更新将正常处理
				delete(perUserBuffers, uid)
				if io.err != nil {
					continue
				}

				// 更新计数器
				if ok {
					for _, upd := range updateBuf {
						if upd.inc {
							count += upd.val
						} else {
							count = upd.val
						}
					}
				} else {
					logs.Warn.Println("ERROR: io didn't have an update buffer, uid", uid)
				}

				if uce, ok := usersCache[uid]; ok {
					if uce.unread >= 0 {
						logs.Warn.Println("users: unread count double initialization, uid", uid)
					}
					uce.unread = count
					usersCache[uid] = uce
				} else {
					logs.Warn.Println("users: missing users cache entry after IO completion, uid", uid)
				}

				// 未读计数器已初始化完成，处理待处理的推送通知回执
				// 减少该用户待处理推送回执的 IO 计数
				if pendingReceipts, ok := perUserPendingReceipts[uid]; ok {
					for _, pp := range pendingReceipts {
						pp.pendingIOs--
						receiptQueue.fix(pp.index)
					}
					delete(perUserPendingReceipts, uid)
				}
			}

			if io.err != nil {
				logs.Err.Println("users: failed to read unread count:", io.err)
				continue
			}

			// 发送就绪的回执
			for receiptQueue.Len() > 0 && receiptQueue[0].pendingIOs == 0 {
				rcpt := heap.Pop(&receiptQueue).(*pendingReceipt).rcpt
				for uid, rcptTo := range rcpt.To {
					if uce, ok := usersCache[uid]; ok && uce.unread >= 0 {
						rcptTo.Unread = uce.unread
						rcpt.To[uid] = rcptTo
					}
				}
				push.Push(rcpt)
			}
		case upd := <-globals.usersUpdate:
			if globals.shuttingDown {
				// 如果正在关闭，不处理任何请求
				// 忽略所有调用
				continue
			}

			// 请求关闭
			if upd == nil {
				globals.usersUpdate = nil
				// 无需关闭 Channel
				goto Exit
			}

			// 请求发送推送通知
			if upd.PushRcpt != nil {
				// 正在从数据库读取未读计数的用户 UID 列表
				pendingUsers := []types.Uid{}

				allUids := make([]types.Uid, 0, len(upd.PushRcpt.To))
				allDeltas := make([]int, 0, len(upd.PushRcpt.To))
				for uid, r := range upd.PushRcpt.To {
					allUids = append(allUids, uid)
					delta := 0
					if r.ShouldIncrementUnreadCountInCache {
						delta = 1
					}
					allDeltas = append(allDeltas, delta)
				}

				allUnread := unreadUpdater(allUids, allDeltas, true)
				for uid, unread := range allUnread {
					rcptTo := upd.PushRcpt.To[uid]
					// 处理更新
					if unread >= 0 {
						rcptTo.Unread = unread
						upd.PushRcpt.To[uid] = rcptTo
					} else if unread == unreadUpdateIOPending {
						pendingUsers = append(pendingUsers, uid)
					}
				}

				if len(pendingUsers) == 0 {
					// 所有数据都在内存中，直接发送推送
					push.Push(upd.PushRcpt)
				} else {
					// 正在等待 IO。将此回执加入队列
					pp := &pendingReceipt{
						pendingIOs: len(pendingUsers),
						rcpt:       upd.PushRcpt,
					}
					for _, uid := range pendingUsers {
						var queue []*pendingReceipt
						var ok bool
						if queue, ok = perUserPendingReceipts[uid]; !ok {
							queue = []*pendingReceipt{}
						}
						perUserPendingReceipts[uid] = append(queue, pp)
					}
					heap.Push(&receiptQueue, pp)
				}
				continue
			}

			// 请求从缓存中添加/移除用户
			if len(upd.UserIdList) > 0 {
				for _, uid := range upd.UserIdList {
					uce, ok := usersCache[uid]
					if upd.Inc {
						if !ok {
							// 这是新用户的注册
							// 此处不加载未读计数，设为 -1
							uce.unread = -1
						}
						uce.topics++
						usersCache[uid] = uce
					} else if ok {
						if uce.topics > 1 {
							uce.topics--
							usersCache[uid] = uce
						} else {
							// 从缓存中移除用户
							delete(usersCache, uid)
						}
					} else {
						// BUG!
						logs.Err.Println("ERROR: request to unregister user which has not been registered", uid)
					}
				}
				continue
			}

			if upd.Gone {
				// 用户正在被删除。不关心是否有记录
				delete(usersCache, upd.UserId)
				continue
			}

			// 请求更新单个用户的未读计数
			unreadUpdater([]types.Uid{upd.UserId}, []int{upd.Unread}, upd.Inc)
		}
	}

Exit:
	logs.Info.Println("users: shutdown")
}

// garbageCollectUsers 每隔 'period' 运行一次，最多删除 'blockSize' 个
// 过期的未验证用户账号，这些账号至少 'minAccountAgeHours' 小时未更新
// 返回可用于停止过程的 Channel
func garbageCollectUsers(period time.Duration, blockSize, minAccountAgeHours int) chan<- bool {
	// 无缓冲停止 Channel。停止 GC 的人必须等待过程完成
	stop := make(chan bool)
	go func() {
		// 为 tick 周期添加随机性以去同步集群节点的运行：
		// 0.75 * period + rand(0, 0.5) * period
		period = period - (period >> 2) + time.Duration(rand.Intn(int(period>>1)))
		gcTicker := time.Tick(period)
		logs.Info.Printf("Stale account GC started with period %s, block size %d, min account age %d hours",
			period.Round(time.Second), blockSize, minAccountAgeHours)
		staleAge := time.Hour * time.Duration(minAccountAgeHours)
		for {
			select {
			case <-gcTicker:
				if uids, err := store.Users.GetUnvalidated(time.Now().Add(-staleAge), blockSize); err == nil {
					if len(uids) > 0 {
						logs.Info.Println("Stale account GC will delete uids:", uids)
						for _, uid := range uids {
							if err = store.Users.Delete(uid, true); err != nil {
								logs.Warn.Printf("Stale account GC failed to delete %s: %+v", uid.UserId(), err)
							}
						}
					}
				} else {
					logs.Warn.Println("Stale account GC error:", err)
				}
			case <-stop:
				return
			}
		}
	}()

	return stop
}
