/******************************************************************************
 *
 *  描述：
 *
 *    Topic 初始化例程。
 *
 *****************************************************************************/

// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"strings"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// topicInit 从数据库读取现有 Topic 或创建新 Topic
func topicInit(t *Topic, join *ClientComMessage, h *Hub) {
	var subscribeReqIssued bool
	defer func() {
		if !subscribeReqIssued && join.Sub != nil && join.sess.inflightReqs != nil {
			// 如果是客户端发起的订阅请求且我们失败了。
			join.sess.inflightReqs.Done()
		}
	}()

	timestamp := types.TimeNow()

	var err error
	switch {
	case t.xoriginal == "me":
		// 请求加载 'me' Topic。Topic 始终存在，订阅永远不是新的。
		err = initTopicMe(t, join)
	case t.xoriginal == "fnd":
		// 请求加载 'find' Topic。Topic 始终存在，订阅永远不是新的。
		err = initTopicFnd(t, join)
	case strings.HasPrefix(t.xoriginal, "usr") || strings.HasPrefix(t.xoriginal, "p2p"):
		// 请求加载现有或创建新的 p2p Topic，然后挂载到它。
		err = initTopicP2P(t, join)
	case strings.HasPrefix(t.xoriginal, "new"):
		// 处理创建新群组 Topic 的请求。
		err = initTopicNewGrp(t, join, false)
	case strings.HasPrefix(t.xoriginal, "nch"):
		// 处理创建新 Channel 的请求。
		err = initTopicNewGrp(t, join, true)
	case strings.HasPrefix(t.xoriginal, "grp") || strings.HasPrefix(t.xoriginal, "chn"):
		// 加载现有群组 Topic（或 Channel）。
		err = initTopicGrp(t, join)
	case t.xoriginal == "sys":
		// 初始化系统 Topic。
		err = initTopicSys(t)
	case t.xoriginal == "slf":
		// 初始化自记（笔记和保存的消息）Topic。
		err = initTopicSlf(t, join)
	default:
		// 无法识别的 Topic 名称
		err = types.ErrTopicNotFound
	}

	// 失败：创建或加载 Topic。
	if err != nil {
		// 从缓存中移除 Topic 以防止 hub 向其转发更多消息。
		h.topicDel(join.RcptTo)

		logs.Err.Println("init_topic: failed to load or create topic:", join.RcptTo, err)
		if join.sess != nil {
			join.sess.queueOut(decodeStoreErrorExplicitTs(err, join.Id, t.xoriginal, timestamp, join.Timestamp, nil))
		}

		// 重新排队待加入 Topic 的请求。
		for len(t.reg) > 0 {
			h.join <- (<-t.reg)
		}

		// 拒绝所有其它待处理请求
		for len(t.clientMsg) > 0 {
			msg := <-t.clientMsg
			if msg.init && msg.sess != nil {
				msg.sess.queueOut(ErrLockedExplicitTs(msg.Id, t.xoriginal, timestamp, join.Timestamp))
			}
		}
		for len(t.unreg) > 0 {
			msg := <-t.unreg
			if msg.sess != nil && msg.sess.inflightReqs != nil {
				msg.sess.inflightReqs.Done()
			}
			if msg.init && msg.sess != nil {
				msg.sess.queueOut(ErrLockedReply(msg, timestamp))
			}
		}
		for len(t.meta) > 0 {
			msg := <-t.meta
			if msg.init && msg.sess != nil {
				msg.sess.queueOut(ErrLockedReply(msg, timestamp))
			}
		}
		if len(t.exit) > 0 {
			msg := <-t.exit
			msg.done <- true
		}

		return
	}

	t.computePerUserAcsUnion()

	// 防止新初始化的 Topic 在关停进行中上线
	if globals.shuttingDown {
		h.topicDel(join.RcptTo)
		return
	}

	if t.isDeleted() {
		// 在我们尝试创建 Topic 时有人删除了它。
		return
	}

	statsInc("LiveTopics", 1)
	statsInc("TotalTopics", 1)
	usersRegisterTopic(t, true)

	// Topic 将检查访问权限，向 p2p 用户发送邀请，向发起者 Session 发送 {ctrl} 消息
	if join.Sub != nil {
		subscribeReqIssued = true
		t.reg <- join
	}

	t.markPaused(false)
	if t.cat == types.TopicCatFnd || t.cat == types.TopicCatSys {
		t.markLoaded()
	}

	go t.run(h)
}

// 初始化 'me' Topic。
func initTopicMe(t *Topic, sreg *ClientComMessage) error {
	t.cat = types.TopicCatMe

	user, err := store.Users.Get(types.ParseUserId(t.name))
	if err != nil {
		// 登出 Session
		sreg.sess.uid = types.ZeroUid
		return err
	} else if user == nil {
		// 登出 Session
		sreg.sess.uid = types.ZeroUid
		return types.ErrUserNotFound
	}

	// 用户的 p2p Topic 默认访问权限
	t.accessAuth = user.Access.Auth
	t.accessAnon = user.Access.Anon

	// 分配标签
	t.tags = user.Tags

	if err = t.loadSubscribers(); err != nil {
		return err
	}

	t.public = user.Public
	t.trusted = user.Trusted

	t.created = user.CreatedAt
	t.updated = user.UpdatedAt

	// 以下值在 'me' 中明确不设置。
	// t.touched, t.lastId, t.delId

	// 'me' 没有所有者，t.owner = nil

	// 用创建 Session 的 UA 初始化用户 Agent，以便稍后汇报
	t.userAgent = sreg.sess.userAgent
	// 初始化接收用户 agent 和 Session 在线更新的 Channel。
	t.supd = make(chan *sessionUpdate, 32)

	if !t.isProxy {
		// 分配联系人存储空间。
		t.perSubs = make(map[string]perSubsData)
	}

	return nil
}

// 初始化 'fnd' Topic
func initTopicFnd(t *Topic, sreg *ClientComMessage) error {
	t.cat = types.TopicCatFnd

	uid := types.ParseUserId(sreg.AsUser)
	if uid.IsZero() {
		return types.ErrNotFound
	}

	user, err := store.Users.Get(uid)
	if err != nil {
		return err
	} else if user == nil {
		if !sreg.sess.isMultiplex() {
			sreg.sess.uid = types.ZeroUid
		}
		return types.ErrNotFound
	}

	// 确保无人能加入 Topic。
	t.accessAuth = getDefaultAccess(t.cat, true, false)
	t.accessAnon = getDefaultAccess(t.cat, false, false)

	if err = t.loadSubscribers(); err != nil {
		return err
	}

	t.created = user.CreatedAt
	t.updated = user.UpdatedAt

	// 'fnd' 没有所有者，t.owner = nil

	// 不支持向 fnd 发布
	// t.lastId = 0, t.delId = 0, t.touched = nil

	return nil
}

// 加载或创建 P2P Topic。
// 当两个用户尝试同时创建 p2p Topic 时存在竞争条件。
func initTopicP2P(t *Topic, sreg *ClientComMessage) error {
	pktsub := sreg.Sub

	// 处理以下情况：
	// 1. Topic 和订阅都不存在：创建新的 p2p Topic 和订阅。
	// 2. Topic 存在，其中一个订阅缺失：
	// 2.1 请求者的订阅缺失，重新创建它。
	// 2.2 另一个用户的订阅缺失，像用户 2 的新请求一样处理。
	// 3. Topic 存在，两个订阅都缺失：不应发生，失败。
	// 4. Topic 和两个订阅都存在：挂载到 Topic

	t.cat = types.TopicCatP2P

	// 检查是否 Topic 已存在
	stopic, err := store.Topics.Get(t.name)
	if err != nil {
		return err
	}

	// 如果 Topic 存在，加载订阅
	var subs []types.Subscription
	if stopic != nil {
		// 订阅已交换 Public
		if subs, err = store.Topics.GetUsers(t.name, nil); err != nil {
			return err
		}

		// 情况 3，失败
		if len(subs) == 0 {
			logs.Err.Println("hub: missing both subscriptions for '" + t.name + "' (SHOULD NEVER HAPPEN!)")
			return types.ErrInternal
		}

		t.created = stopic.CreatedAt
		t.updated = stopic.UpdatedAt
		if !stopic.TouchedAt.IsZero() {
			t.touched = stopic.TouchedAt
		}
		t.aux = stopic.Aux
		t.lastID = stopic.SeqId
		t.delID = stopic.DelId
	}

	// t.owner 对 p2p Topic 为空

	// P2P Topic 的默认用户访问权限未设置，因为未使用。
	// 其它用户无法加入 Topic，因为 Topic 名称的构造方式。
	// 两个参与者相互设置对方的访问权限。
	// t.accessAuth = getDefaultAccess(t.cat, true)
	// t.accessAnon = getDefaultAccess(t.cat, false)

	// t.public 和 t.trusted 不用于 p2p Topic，因为每个用户获得不同的 public/trusted。

	if stopic != nil && len(subs) == 2 {
		// 情况 4。
		for i := range 2 {
			uid := types.ParseUid(subs[i].User)
			t.perUser[uid] = perUserData{
				// 适配器已交换 state、public、defaultAccess、lastSeen 值。
				public:    subs[i].GetPublic(),
				lastSeen:  subs[i].GetLastSeen(),
				lastUA:    subs[i].GetUserAgent(),
				topicName: types.ParseUid(subs[(i+1)%2].User).UserId(),

				private:   subs[i].Private,
				modeWant:  subs[i].ModeWant,
				modeGiven: subs[i].ModeGiven,
				delID:     subs[i].DelId,
				recvID:    subs[i].RecvSeqId,
				readID:    subs[i].ReadSeqId,
			}
		}
	} else {
		// 情况 1（新 Topic），2（两个订阅之一缺失：要么是新请求
		// 要么订阅已被删除）
		var userData perUserData

		// 获取两个用户的记录。
		// 请求者。
		userID1 := types.ParseUserId(sreg.AsUser)
		// 另一个用户。
		userID2 := types.ParseUserId(t.xoriginal)

		// 用户索引：u1 - 请求者，u2 - 响应者，另一个用户
		var u1, u2 int
		users, err := store.Users.GetAll(userID1, userID2)
		if err != nil {
			return err
		}
		if len(users) != 2 {
			// 被邀请的用户不存在
			return types.ErrUserNotFound
		}

		// 用户记录未排序，确保我们知道谁是谁。
		if users[0].Uid() == userID1 {
			u1, u2 = 0, 1
		} else {
			u1, u2 = 1, 0
		}

		// 确定哪些订阅缺失：User1 的、User2 的或两者的。
		var sub1, sub2 *types.Subscription
		// 如果只需创建请求者的订阅则设为 true。
		var user1only bool
		if len(subs) == 1 {
			if subs[0].User == userID1.String() {
				// User2 的订阅缺失，user1 的存在
				sub1 = &subs[0]
			} else {
				// User1 的缺失，user2 的存在
				sub2 = &subs[0]
				user1only = true
			}
		}

		// 另一个用户（响应者）的订阅缺失
		if sub2 == nil {
			sub2 = &types.Subscription{
				User:    userID2.String(),
				Topic:   t.name,
				Private: nil,
			}

			// 基于 user1 提供的内容分配 user2 的 ModeGiven。
			// 我们不知道 user2 的访问模式，假设是 Auth。
			if pktsub.Set != nil && pktsub.Set.Desc != nil && pktsub.Set.Desc.DefaultAcs != nil {
				// 使用提供的 DefaultAcs 作为另一个用户的非默认 modeGiven。
				// 假设另一个用户的认证级别为 "Auth"。
				sub2.ModeGiven = users[u1].Access.Auth
				if err := sub2.ModeGiven.UnmarshalText([]byte(pktsub.Set.Desc.DefaultAcs.Auth)); err != nil {
					logs.Err.Println("hub: invalid access mode", t.xoriginal, pktsub.Set.Desc.DefaultAcs.Auth)
				}
			} else {
				// 使用 user1.Auth 作为另一个用户的 modeGiven
				sub2.ModeGiven = users[u1].Access.Auth
			}
			// 健全性检查
			sub2.ModeGiven = sub2.ModeGiven&globals.typesModeCP2P | types.ModeApprove

			// 交换 Public+Trusted 以匹配从存储.Topic.GetSubs 返回的订阅中交换的 Public+Trusted
			sub2.SetPublic(users[u1].Public)
			sub2.SetTrusted(users[u1].Trusted)

			// 将整个 Topic 标记为新。
			pktsub.Created = true
		}

		// 请求者的订阅缺失：
		// a. 请求者正在启动新 Topic
		// b. 请求者的订阅缺失：已删除或创建失败
		if sub1 == nil {
			// 从 user2 的默认值设置 user1 的 ModeGiven
			userData.modeGiven = selectAccessMode(auth.Level(sreg.AuthLvl),
				users[u2].Access.Anon,
				users[u2].Access.Auth,
				globals.typesModeCP2P)

			// 默认分配与 user1 给 user2 的相同 mode（可在下面更改）
			userData.modeWant = sub2.ModeGiven

			if pktsub.Set != nil {
				if pktsub.Set.Sub != nil {
					uid := userID1
					if pktsub.Set.Sub.User != "" {
						uid = types.ParseUserId(pktsub.Set.Sub.User)
					}

					if uid != userID1 {
						// 汇报错误并忽略该值
						logs.Err.Println("hub: setting mode for another user is not supported '" + t.name + "'")
					} else {
						// user1 正在设置非默认 modeWant
						if err := userData.modeWant.UnmarshalText([]byte(pktsub.Set.Sub.Mode)); err != nil {
							logs.Err.Println("hub: invalid access mode", t.xoriginal, pktsub.Set.Sub.Mode)
						}
						// 确保健全性
						userData.modeWant = userData.modeWant&globals.typesModeCP2P | types.ModeApprove
					}

					// 由于 user1 发出了 {sub} 请求，确保用户可以加入
					userData.modeWant |= types.ModeJoin
				}

				// user1 设置非默认 Private
				if pktsub.Set.Desc != nil {
					if !isNullValue(pktsub.Set.Desc.Private) {
						userData.private = pktsub.Set.Desc.Private
					}
					// Public 如果存在则被忽略
				}
			}

			sub1 = &types.Subscription{
				User:      userID1.String(),
				Topic:     t.name,
				ModeWant:  userData.modeWant,
				ModeGiven: userData.modeGiven,
				Private:   userData.private,
			}
			// 交换 Public+Trusted 以匹配从存储.Topic.GetSubs 返回的订阅中交换的 Public+Trusted
			sub1.SetPublic(users[u2].Public)
			sub1.SetTrusted(users[u2].Trusted)

			// 将此订阅标记为新
			pktsub.Newsub = true
		}

		if !user1only {
			// sub2 正在被创建，将 sub2.modeWant 分配为 user2 给 user1 的权限（sub1.modeGiven）
			sub2.ModeWant = selectAccessMode(auth.Level(sreg.AuthLvl),
				users[u2].Access.Anon,
				users[u2].Access.Auth,
				globals.typesModeCP2P)
			// 确保健全性
			sub2.ModeWant = sub2.ModeWant&globals.typesModeCP2P | types.ModeApprove
		}

		// 创建所有内容
		if stopic == nil {
			if err = store.Topics.CreateP2P(sub1, sub2); err != nil {
				return err
			}

			t.created = sub1.CreatedAt
			t.updated = sub1.UpdatedAt
			t.touched = t.updated

			// 新 Topic 的 t.lastId 未设置（默认 0）

		} else {
			// 重新创建缺失的订阅并同步更新已更改的现有订阅
			var subToMake *types.Subscription
			if user1only {
				subToMake = sub1
			} else if sub2 == nil {
				subToMake = sub2
			}

			if subToMake != nil {
				if err = store.Subs.Create(subToMake); err != nil {
					return err
				}
			}

			if sub1 != nil && subToMake != sub1 {
				updateFields := map[string]any{
					"ModeWant":  sub1.ModeWant,
					"ModeGiven": sub1.ModeGiven,
					"Private":   sub1.Private,
					"DeletedAt": nil,
				}
				if err = store.Subs.Update(sub1.Topic, userID1, updateFields); err != nil {
					logs.Warn.Println("initTopicP2P: failed to update sub1", err)
				}
			}

			if sub2 != nil && subToMake != sub2 {
				updateFields := map[string]any{
					"ModeWant":  sub2.ModeWant,
					"ModeGiven": sub2.ModeGiven,
					"Private":   sub2.Private,
					"DeletedAt": nil,
				}
				if err = store.Subs.Update(sub2.Topic, userID2, updateFields); err != nil {
					logs.Warn.Println("initTopicP2P: failed to update sub2", err)
				}
			}
		}

		// Public 和 Trusted 已交换。
		userData.public = sub1.GetPublic()
		userData.trusted = sub1.GetTrusted()
		userData.topicName = userID2.UserId()
		userData.modeWant = sub1.ModeWant
		userData.modeGiven = sub1.ModeGiven
		userData.delID = sub1.DelId
		userData.readID = sub1.ReadSeqId
		userData.recvID = sub1.RecvSeqId
		t.perUser[userID1] = userData

		t.perUser[userID2] = perUserData{
			public:    sub2.GetPublic(),
			trusted:   sub2.GetTrusted(),
			topicName: userID1.UserId(),
			modeWant:  sub2.ModeWant,
			modeGiven: sub2.ModeGiven,
			delID:     sub2.DelId,
			readID:    sub2.ReadSeqId,
			recvID:    sub2.RecvSeqId,
		}
	}

	// 清除原始 Topic 名称。
	t.xoriginal = ""

	return nil
}

// 创建新群组 Topic
func initTopicNewGrp(t *Topic, sreg *ClientComMessage, isChan bool) error {
	timestamp := types.TimeNow()
	pktsub := sreg.Sub

	t.cat = types.TopicCatGrp
	t.isChan = isChan

	// 通用 Topic 的参数存储在 Topic 对象中
	t.owner = types.ParseUserId(sreg.AsUser)
	authLevel := auth.Level(sreg.AuthLvl)

	t.accessAuth = getDefaultAccess(t.cat, true, isChan)
	t.accessAnon = getDefaultAccess(t.cat, false, isChan)

	// 所有者/创建者获得 Topic 的完全访问权限。所有者可以通过 'set' 更改默认 modeWant。
	userData := perUserData{
		modeGiven: types.ModeCFull,
		modeWant:  types.ModeCFull,
	}

	if pktsub.Set != nil {
		// 用户发送了初始化参数
		if pktsub.Set.Desc != nil {
			if pktsub.Set.Desc.Trusted != nil && authLevel != auth.LevelRoot {
				logs.Err.Println("hub: attempt to assign Trusted by non-ROOT", t.name)
				return types.ErrPermissionDenied
			}

			if !isNullValue(pktsub.Set.Desc.Public) {
				t.public = pktsub.Set.Desc.Public
			}
			if !isNullValue(pktsub.Set.Desc.Trusted) {
				t.trusted = pktsub.Set.Desc.Trusted
			}
			if !isNullValue(pktsub.Set.Desc.Private) {
				userData.private = pktsub.Set.Desc.Private
			}

			// 设置默认访问权限
			if pktsub.Set.Desc.DefaultAcs != nil {
				if authMode, anonMode, err := parseTopicAccess(pktsub.Set.Desc.DefaultAcs,
					t.accessAuth, t.accessAnon); err != nil {

					// 一个或两个都无效。设为明确的 None
					if authMode.IsInvalid() {
						t.accessAuth = types.ModeNone
					} else {
						t.accessAuth = authMode
					}
					if anonMode.IsInvalid() {
						t.accessAnon = types.ModeNone
					} else {
						t.accessAnon = anonMode
					}
					logs.Err.Println("hub: invalid access mode for topic '" + t.name + "': '" + err.Error() + "'")
				} else if authMode.IsOwner() || anonMode.IsOwner() {
					logs.Err.Println("hub: OWNER default access in topic", t.name)
					t.accessAuth, t.accessAnon = authMode & ^types.ModeOwner, anonMode & ^types.ModeOwner
				} else {
					t.accessAuth, t.accessAnon = authMode, anonMode
				}
			}
		}

		// 所有者/创建者可以限制自己对 Topic 的访问
		if pktsub.Set.Sub != nil && pktsub.Set.Sub.Mode != "" {
			userData.modeWant = types.ModeCFull
			if err := userData.modeWant.UnmarshalText([]byte(pktsub.Set.Sub.Mode)); err != nil {
				logs.Err.Println("hub: invalid access mode", t.xoriginal, pktsub.Set.Sub.Mode)
			}
			// 用户不得取消设置 ModeJoin 或 Owner 标志
			userData.modeWant |= types.ModeJoin | types.ModeOwner
		}

		if tags := normalizeTags(pktsub.Set.Tags, globals.maxTagCount); len(tags) > 0 {
			if !restrictedTagsEqual(tags, nil, globals.immutableTagNS) {
				return types.ErrPermissionDenied
			}
			// 分配标签
			t.tags = tags
		}
	}

	t.perUser[t.owner] = userData

	t.created = timestamp
	t.updated = timestamp
	t.touched = timestamp

	// 新 Topic 的 t.lastId 和 t.delId 未设置

	stopic := &types.Topic{
		ObjHeader: types.ObjHeader{Id: sreg.RcptTo, CreatedAt: timestamp},
		Access:    types.DefaultAccess{Auth: t.accessAuth, Anon: t.accessAnon},
		Tags:      t.tags,
		UseBt:     isChan,
		Public:    t.public,
		Trusted:   t.trusted,
	}

	// 存储.Topic.Create 将为 Topic 创建者添加订阅记录
	stopic.GiveAccess(t.owner, userData.modeWant, userData.modeGiven)
	err := store.Topics.Create(stopic, t.owner, t.perUser[t.owner].private)
	if err != nil {
		return err
	}

	// 链接上传的头像到 Topic。
	if sreg.Extra != nil && len(sreg.Extra.Attachments) > 0 {
		if err := store.Files.LinkAttachments(t.name, types.ZeroUid, sreg.Extra.Attachments); err != nil {
			logs.Warn.Printf("topic[%s] failed to link avatar attachment: %v", t.name, err)
			// 这不是关键错误，继续执行。
		}
	}

	t.xoriginal = t.name // 保留 'new' 或 'nch' 作为原始名称对客户端无意义
	t.subCnt = 1         // 一个订阅，所有者。

	pktsub.Created = true
	pktsub.Newsub = true

	return nil
}

// 初始化现有群组 Topic。当两个用户尝试同时加载
// 相同 Topic 时存在竞争条件。这在 hub 层被防止。
func initTopicGrp(t *Topic, join *ClientComMessage) error {
	t.cat = types.TopicCatGrp

	// 检查和验证 Topic 名称合法性
	if t.name == "" || len(t.name) <= 3 || types.GetTopicCat(t.name) != types.TopicCatGrp {
		return types.ErrMalformed
	}

	stopic, err := store.Topics.Get(t.name)
	if err != nil {
		return err
	} else if stopic == nil {
		return types.ErrTopicNotFound
	}

	t.isChan = stopic.UseBt
	t.accessAuth = stopic.Access.Auth
	t.accessAnon = stopic.Access.Anon

	// 分配标签和辅助数据。
	t.tags = stopic.Tags
	t.aux = stopic.Aux
	t.official, err = officialPolicyFromAux(t.name, t.aux)
	if err != nil {
		return err
	}
	t.officialRefreshedAt = types.TimeNow()

	if t.isOfficialLargeGroup() {
		// 官方大群不把全量成员装入 Topic Actor。这里只加载所有者和本次加入者，
		// 其它成员在进入、发言或管理操作时按需从订阅表读取。
		t.owner = types.ParseUid(stopic.Owner)
		if t.owner.IsZero() {
			return types.ErrInternal
		}
		if found, loadErr := t.loadSubscriber(t.owner); loadErr != nil || !found {
			if loadErr != nil {
				return loadErr
			}
			return types.ErrInternal
		}
		if join != nil {
			joiningUID := types.ParseUserId(join.AsUser)
			if joiningUID != t.owner {
				if _, loadErr := t.loadSubscriber(joiningUID); loadErr != nil {
					return loadErr
				}
			}
		}
	} else if err = t.loadSubscribers(); err != nil {
		return err
	}

	t.public = stopic.Public
	t.trusted = stopic.Trusted

	t.created = stopic.CreatedAt
	t.updated = stopic.UpdatedAt
	if !stopic.TouchedAt.IsZero() {
		t.touched = stopic.TouchedAt
	}
	t.lastID = stopic.SeqId
	t.delID = stopic.DelId
	t.subCnt = stopic.SubCnt

	// 初始化接收 Session 在线更新的 Channel。
	t.supd = make(chan *sessionUpdate, 32)

	t.xoriginal = t.name // Topic 可能由 Channel 读者加载；确保是 grpXXX，而不是 chnXXX。

	return nil
}

// 初始化系统 Topic。系统 Topic 是单例，始终在内存中。
func initTopicSys(t *Topic) error {
	t.cat = types.TopicCatSys

	stopic, err := store.Topics.Get(t.name)
	if err != nil {
		return err
	} else if stopic == nil {
		return types.ErrTopicNotFound
	}

	if err = t.loadSubscribers(); err != nil {
		return err
	}

	// 没有 t.owner

	// 默认权限为 'W'
	t.accessAuth = types.ModeWrite
	t.accessAnon = types.ModeWrite

	t.public = stopic.Public
	t.trusted = stopic.Trusted

	t.created = stopic.CreatedAt
	t.updated = stopic.UpdatedAt
	if !stopic.TouchedAt.IsZero() {
		t.touched = stopic.TouchedAt
	}
	t.lastID = stopic.SeqId

	return nil
}

// 初始化或加载自 Topic 'slf'。
func initTopicSlf(t *Topic, sreg *ClientComMessage) error {
	t.cat = types.TopicCatSlf

	stopic, err := store.Topics.Get(t.name)
	if err != nil {
		return err
	}

	// 如果 Topic 存在，加载订阅
	if stopic != nil {
		if err = t.loadSubscribers(); err != nil {
			return err
		}

		// t.owner 由 loadSubscriptions 设置

		// Topic 存在但订阅缺失。失败。
		if len(t.perUser) == 0 {
			logs.Err.Println("hub: missing subscription for '" + t.name + "' (SHOULD NEVER HAPPEN!)")
			return types.ErrInternal
		}

		t.created = stopic.CreatedAt
		t.updated = stopic.UpdatedAt
		if !stopic.TouchedAt.IsZero() {
			t.touched = stopic.TouchedAt
		}
		t.aux = stopic.Aux
		t.lastID = stopic.SeqId
		t.delID = stopic.DelId

	} else {
		// 获取 Topic 所有者。
		userID := types.ParseUserId(sreg.AsUser)
		user, err := store.Users.Get(userID)
		if err != nil {
			return err
		}
		if user == nil {
			// 未找到用户。真的不应该发生。
			return types.ErrUserNotFound
		}

		t.owner = userID

		t.accessAuth = getDefaultAccess(t.cat, true, false)
		t.accessAnon = getDefaultAccess(t.cat, false, false)

		// 自所有者的默认访问权限。
		userData := perUserData{
			modeGiven: t.accessAuth,
			modeWant:  t.accessAuth,
		}

		// 将 Topic 标记为新。
		sreg.Sub.Created = true

		if sreg.Sub.Set != nil {
			// 用户设置非默认 Private
			if sreg.Sub.Set.Desc != nil {
				if !isNullValue(sreg.Sub.Set.Desc.Private) {
					userData.private = sreg.Sub.Set.Desc.Private
				}
				// Public, trusted 被忽略。
			}

			if tags := normalizeTags(sreg.Sub.Set.Tags, globals.maxTagCount); len(tags) > 0 {
				if !restrictedTagsEqual(tags, nil, globals.immutableTagNS) {
					return types.ErrPermissionDenied
				}

				// 分配标签
				t.tags = tags
			}
		}

		// 将此订阅标记为新
		sreg.Sub.Newsub = true

		t.perUser[t.owner] = userData

		timestamp := types.TimeNow()

		t.created = timestamp
		t.updated = timestamp
		t.touched = timestamp

		stopic = &types.Topic{
			ObjHeader: types.ObjHeader{Id: sreg.RcptTo, CreatedAt: timestamp},
			Access:    types.DefaultAccess{Auth: t.accessAuth, Anon: t.accessAnon},
			Tags:      t.tags,
		}

		// 存储.Topic.Create 将为 Topic 创建者添加订阅记录
		stopic.GiveAccess(t.owner, userData.modeWant, userData.modeGiven)
		err = store.Topics.Create(stopic, t.owner, t.perUser[t.owner].private)
		if err != nil {
			return err
		}

		sreg.Sub.Created = true
		sreg.Sub.Newsub = true
	}

	return nil
}

// loadSubscribers 加载 Topic 订阅者，设置 Topic 所有者。
func (t *Topic) loadSubscribers() error {
	subs, err := store.Topics.GetSubs(t.name, nil)
	if err != nil {
		return err
	}

	if subs == nil {
		return nil
	}

	for i := range subs {
		sub := &subs[i]
		uid := types.ParseUid(sub.User)
		t.perUser[uid] = perUserData{
			delID:     sub.DelId,
			readID:    sub.ReadSeqId,
			recvID:    sub.RecvSeqId,
			private:   sub.Private,
			modeWant:  sub.ModeWant,
			modeGiven: sub.ModeGiven,
		}

		if (sub.ModeGiven & sub.ModeWant).IsOwner() {
			t.owner = uid
		}
	}

	return nil
}
