// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/push"
	"chat/server/store"
	"chat/server/store/types"
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
