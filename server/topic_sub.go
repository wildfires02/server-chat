// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"errors"
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// unregisterSession 实现收到 Topic.unreg Channel 离开请求后的处理逻辑
func (t *Topic) unregisterSession(msg *ClientComMessage) {
	if t.currentCall != nil {
		if t.currentCall.provider == constCallProviderAgora {
			// 群组通话只移除断开的 Session；其余成员继续使用 Agora
			// 频道，最后一个参与者离开时再结束整个通话。
			t.disconnectAgoraCallSessions(msg)
		} else {
			shouldTerminateCall := false
			if msg.sess.isMultiplex() {
				// 检查通话关联的 Session 是否在 msg.sess 上多路复用
				for _, p := range t.currentCall.parties {
					if p.sess.isProxy() && p.sess.multi == msg.sess {
						shouldTerminateCall = true
						break
					}
				}
			} else if _, found := t.currentCall.parties[msg.sess.sid]; found {
				// 普通 Session 从 Topic 断开，终止通话
				shouldTerminateCall = true
			}
			if shouldTerminateCall {
				t.terminateCallInProgress(false)
			}
		}
	}
	t.handleLeaveRequest(msg, msg.sess)
	if msg.init && msg.sess.inflightReqs != nil {
		// 客户端发起的请求，计数器 Done
		msg.sess.inflightReqs.Done()
	}

	// 如果不再有任何订阅，启动杀进程/销毁定时器
	if len(t.sessions) == 0 && t.cat != types.TopicCatSys {
		t.killTimer.Reset(idleMasterTopicTimeout)
	}
}

// registerSession 处理通过 Topic.reg Channel 接收到的 Session 加入（注册）请求
func (t *Topic) registerSession(msg *ClientComMessage) {
	// 请求将连接添加到此 Topic
	if t.isInactive() {
		msg.sess.queueOut(ErrLockedReply(msg, types.TimeNow()))
	} else if msg.sess.getSub(t.name) != nil {
		// Session 已订阅此 Topic
		msg.sess.queueOut(InfoAlreadySubscribed(msg.Id, msg.Original, msg.Timestamp))
	} else {
		// Topic 为活跃状态，停止杀进程定时器
		t.killTimer.Stop()
		if err := t.handleSubscription(msg); err == nil {
			if msg.Sub.Created {
				// 调用插件通知新 Topic 创建
				pluginTopic(t, plgActCreate)
			}
		} else {
			if len(t.sessions) == 0 && t.cat != types.TopicCatSys {
				// 订阅失败，Topic 仍处于未激活状态
				t.killTimer.Reset(idleMasterTopicTimeout)
			}
			logs.Warn.Printf("topic[%s] 订阅失败 %v, sid=%s", t.name, err, msg.sess.sid)
		}
	}
	if msg.sess.inflightReqs != nil {
		msg.sess.inflightReqs.Done()
	}
}

// handleSubscription Session 订阅 Topic 处理函数
func (t *Topic) handleSubscription(msg *ClientComMessage) error {
	asUid := types.ParseUserId(msg.AsUser)
	authLevel := auth.Level(msg.AuthLvl)
	asChan, err := t.verifyChannelAccess(msg.Original)
	if err != nil {
		// 用户不应将非 Channel Topic 当作 Channel 寻址
		msg.sess.queueOut(ErrNotFoundReply(msg, types.TimeNow()))
		return err
	}

	if err := t.subscriptionReply(asChan, msg); err != nil {
		return err
	}

	msgsub := msg.Sub
	getWhat := 0
	if msgsub.Get != nil {
		getWhat = parseMsgClientMeta(msgsub.Get.What)
	}
	if getWhat&constMsgMetaDesc != 0 {
		// 作为单独的 {meta} 包发送 get.desc
		if err := t.replyGetDesc(msg.sess, asUid, asChan, msgsub.Get.Desc, msg); err != nil {
			logs.Warn.Printf("topic[%s] handleSubscription Get.Desc 失败: %v sid=%s", t.name, err, msg.sess.sid)
		}
	}

	if getWhat&constMsgMetaSub != 0 {
		// 作为单独的 {meta} 包发送 get.sub 响应
		if err := t.replyGetSub(msg.sess, asUid, authLevel, asChan, msg); err != nil {
			logs.Warn.Printf("topic[%s] handleSubscription Get.Sub 失败: %v sid=%s", t.name, err, msg.sess.sid)
		}
	}

	if getWhat&constMsgMetaTags != 0 {
		// 作为单独的 {meta} 包发送 get.tags 响应
		if err := t.replyGetTags(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] handleSubscription Get.Tags 失败: %v sid=%s", t.name, err, msg.sess.sid)
		}
	}

	if getWhat&constMsgMetaCred != 0 {
		// 发送 get.cred 响应
		if err := t.replyGetCreds(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] handleSubscription Get.Cred 失败: %v sid=%s", t.name, err, msg.sess.sid)
		}
	}

	if getWhat&constMsgMetaAux != 0 {
		// 发送 get.aux 响应
		if err := t.replyGetAux(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] handleSubscription Get.Aux 失败: %v sid=%s", t.name, err, msg.sess.sid)
		}
	}

	if getWhat&constMsgMetaData != 0 {
		// 作为 {data} 包发送 get.data 响应
		if err := t.replyGetData(msg.sess, asUid, asChan, msgsub.Get.Data, msg); err != nil {
			logs.Warn.Printf("topic[%s] handleSubscription Get.Data 失败: %v sid=%s", t.name, err, msg.sess.sid)
		}
	}

	if getWhat&constMsgMetaDel != 0 {
		// 发送 get.del 响应
		if err := t.replyGetDel(msg.sess, asUid, msgsub.Get.Del, msg); err != nil {
			logs.Warn.Printf("topic[%s] handleSubscription Get.Del 失败: %v sid=%s", t.name, err, msg.sess.sid)
		}
	}

	return nil
}

// handleLeaveRequest 处理 Session 离开请求
func (t *Topic) handleLeaveRequest(msg *ClientComMessage, sess *Session) {
	// 从 Topic 中移除连接；Session 保持运行
	now := types.TimeNow()

	var asUid types.Uid
	var asChan bool
	if msg.init {
		asUid = types.ParseUserId(msg.AsUser)
		var err error
		asChan, err = t.verifyChannelAccess(msg.Original)
		if err != nil {
			sess.queueOut(ErrNotFoundReply(msg, now))
		}
	}

	if t.isInactive() {
		if !asUid.IsZero() && msg.init {
			sess.queueOut(ErrLockedReply(msg, now))
		}
		return
	}

	// 用户希望离开并取消订阅
	if msg.init && msg.Leave.Unsub {
		if err := t.replyLeaveUnsub(sess, msg, asUid); err != nil {
			logs.Err.Println("取消订阅失败", err, sess.sid)
		}
		return
	}

	// 用户希望离开但不取消订阅
	if pssd, _ := t.remSession(sess, asUid); pssd != nil {
		if !sess.isProxy() {
			sess.delSub(t.name)
		}
		if pssd.isChanSub != asChan {
			if msg.init {
				sess.queueOut(ErrNotFoundReply(msg, now))
			}
			return
		}

		var uid types.Uid
		if sess.isProxy() {
			uid = asUid
		} else {
			uid = pssd.uid
		}

		var pud perUserData
		if !uid.IsZero() {
			pud = t.perUser[uid]
			if !sess.background {
				pud.online--
				t.perUser[uid] = pud
			}
		} else if len(pssd.muids) > 0 {
			for _, uid := range pssd.muids {
				pud := t.perUser[uid]
				pud.online--
				t.perUser[uid] = pud
			}
		} else if !sess.isCluster() {
			logs.Warn.Panic("无法确定 uid: leave req", msg, sess)
		}

		switch t.cat {
		case types.TopicCatMe:
			mrs := t.mostRecentSession()
			if mrs == nil {
				mrs = sess
			} else {
				select {
				case t.supd <- &sessionUpdate{userAgent: mrs.userAgent}:
				default:
				}
			}

			meUid := uid
			if meUid.IsZero() && len(pssd.muids) > 0 {
				meUid = pssd.muids[0]
			}
			if !meUid.IsZero() {
				if err := store.Users.UpdateLastSeen(meUid, mrs.userAgent, now); err != nil {
					logs.Warn.Println("更新用户最后在线时间失败:", err)
				}
			}
		case types.TopicCatFnd:
			t.fndRemovePublic(sess)
		case types.TopicCatGrp:
			// 订阅者在 Topic 中下线：通知当前在线的其它订阅者
			readFilter := &presFilters{filterIn: types.ModeRead}
			if !uid.IsZero() {
				if pud.online == 0 {
					if asChan {
						delete(t.perUser, uid)
					} else {
						t.presSubsOnline("off", uid.UserId(), nilPresParams, readFilter, "")
					}
				}
			} else if len(pssd.muids) > 0 {
				for _, uid := range pssd.muids {
					if t.perUser[uid].online == 0 {
						if asChan {
							delete(t.perUser, uid)
						} else {
							t.presSubsOnline("off", uid.UserId(), nilPresParams, readFilter, "")
						}
					}
				}
			}
		}

		if !uid.IsZero() {
			if msg.init {
				sess.queueOut(NoErrReply(msg, now))
			}
		}
	}
}

// subscriptionReply 生成对订阅请求的响应
func (t *Topic) subscriptionReply(asChan bool, msg *ClientComMessage) error {
	msgsub := msg.Sub

	var now time.Time
	if msgsub.Created {
		now = t.updated
	} else {
		now = types.TimeNow()
	}

	asUid := types.ParseUserId(msg.AsUser)

	if !msgsub.Newsub && (t.cat == types.TopicCatP2P || t.cat == types.TopicCatGrp) {
		pud, found := t.perUser[asUid]
		msgsub.Newsub = !found || pud.deleted
	}

	var private any
	var mode string
	if msgsub.Set != nil {
		if msgsub.Set.Sub != nil {
			if msgsub.Set.Sub.User != "" {
				msg.sess.queueOut(ErrMalformedReply(msg, now))
				return errors.New("不得指定用户 ID")
			}
			if msgsub.Set.Sub.Role != "" {
				msg.sess.queueOut(ErrMalformedReply(msg, now))
				return errors.New("订阅请求不能修改成员角色")
			}
			mode = msgsub.Set.Sub.Mode
		}

		if msgsub.Set.Desc != nil {
			private = msgsub.Set.Desc.Private
		}
	}

	var err error
	var modeChanged *MsgAccessMode
	// 创建新订阅或修改现有订阅
	if modeChanged, err = t.thisUserSub(msg.sess, msg, asUid, asChan, mode, private); err != nil {
		return err
	}

	hasJoined := true
	if modeChanged != nil {
		if acs, err := types.ParseAcs([]byte(modeChanged.Mode)); err == nil {
			hasJoined = acs.IsJoiner()
		}
	}

	if hasJoined {
		// 订阅成功创建，链接 Topic 至 Session
		msg.sess.addSub(t.name, &Subscription{
			broadcast: t.clientMsg,
			done:      t.unreg,
			meta:      t.meta,
			supd:      t.supd,
		})
		t.addSession(msg.sess, asUid, asChan)

		if !msg.sess.background {
			userData := t.perUser[asUid]
			userData.online++
			t.perUser[asUid] = userData
		}

		if t.cat == types.TopicCatGrp && msgsub.Newsub {
			t.subCnt++
		}
	}

	params := map[string]any{}
	if modeChanged != nil {
		params["acs"] = modeChanged
	}
	toriginal := t.original(asUid)

	if msgsub.Created && msg.Original != toriginal {
		params["tmpname"] = msg.Original
		msg.Original = toriginal
	}

	if len(params) == 0 {
		msg.sess.queueOut(NoErr(msg.Id, toriginal, now))
	} else {
		msg.sess.queueOut(NoErrParams(msg.Id, toriginal, now, params))
	}

	if modeChanged != nil {
		t.sendImmediateSubNotifications(asUid, modeChanged, msg, now)
	}

	if !msg.sess.background && hasJoined {
		t.sendSubNotifications(asUid, msg.sess.sid, msg.sess.userAgent)
	}

	return nil
}

// thisUserSub 完成this用户订阅所需的内部处理。
func (t *Topic) thisUserSub(sess *Session, pkt *ClientComMessage, asUid types.Uid, asChan bool, want string,
	private any) (*MsgAccessMode, error) {

	now := types.TimeNow()
	asLvl := auth.Level(pkt.AuthLvl)

	oldWant := types.ModeNone
	oldGiven := types.ModeNone

	modeWant := types.ModeUnset
	if want != "" {
		if err := modeWant.UnmarshalText([]byte(want)); err != nil {
			sess.queueOut(ErrMalformedReply(pkt, now))
			return nil, err
		}
	}

	var err error
	userData, existingSub := t.perUser[asUid]
	if !existingSub || userData.deleted {
		if t.cat == types.TopicCatGrp && !asChan && t.subsCount() >= globals.maxSubscriberCount {
			sess.queueOut(ErrPolicyReply(pkt, now))
			return nil, errors.New("超出最大订阅数量限制")
		}

		var sub *types.Subscription
		tname := t.name
		if t.cat == types.TopicCatP2P {
			if modeWant != types.ModeUnset {
				userData.modeWant = modeWant
			}
			userData.modeWant = (userData.modeWant & globals.typesModeCP2P) | types.ModeApprove
		} else if t.cat == types.TopicCatSys {
			if asLvl != auth.LevelRoot {
				sess.queueOut(ErrPermissionDeniedReply(pkt, now))
				return nil, errors.New("订阅 'sys' Topic 需要 Root 权限")
			}

			userData.modeWant = types.ModeCSys
			userData.modeGiven = types.ModeCSys
			if modeWant != types.ModeUnset {
				userData.modeWant = (modeWant & types.ModeCSys) | types.ModeWrite | types.ModeJoin
			}
		} else if asChan {
			userData.isChan = true

			sub, err = store.Subs.Get(pkt.Original, asUid, false)
			if err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			}

			if sub != nil {
				oldWant = sub.ModeWant
				oldGiven = sub.ModeGiven
				// 管理员可将频道读者设置为 banned。必须尊重持久化的
				// ModeGiven，不能在重新订阅时无条件恢复读权限。
				userData.modeGiven = sub.ModeGiven
			} else {
				oldWant = types.ModeCChnReader
				oldGiven = types.ModeCChnReader
				userData.modeGiven = types.ModeCChnReader
			}

			if modeWant != types.ModeUnset {
				userData.modeWant = (modeWant & types.ModeCChnReader) | types.ModeRead | types.ModeJoin
			} else {
				userData.modeWant = oldWant
			}

			tname = pkt.Original
		} else {
			if !existingSub {
				sub, err = store.Subs.Get(t.name, asUid, true)
				if err != nil {
					sess.queueOut(ErrUnknownReply(pkt, now))
					return nil, err
				}

				if sub != nil {
					userData.modeGiven = sub.ModeGiven
				} else {
					userData.modeGiven = types.ModeUnset
				}
			}

			if userData.modeGiven == types.ModeUnset {
				userData.modeGiven = t.accessFor(asLvl)
			}

			if modeWant == types.ModeUnset {
				userData.modeWant = t.accessFor(asLvl)
			} else {
				userData.modeWant = modeWant
			}
		}

		if !userData.modeGiven.IsJoiner() {
			sess.queueOut(ErrPermissionDeniedReply(pkt, now))
			return nil, errors.New("由于权限不足订阅被拒绝")
		}

		if userData.deleted {
			userData.deleted = false
			userData.delID, userData.readID, userData.recvID = 0, 0, 0
		}

		if isNullValue(private) {
			private = nil
		}
		userData.private = private

		if sub == nil || sub.DeletedAt != nil {
			sub = &types.Subscription{
				User:      asUid.String(),
				Topic:     tname,
				ModeWant:  userData.modeWant,
				ModeGiven: userData.modeGiven,
				Private:   userData.private,
			}

			if err := store.Subs.Create(sub); err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			}

		} else if asChan && userData.modeWant != oldWant {
			if err := store.Subs.Update(tname, asUid,
				map[string]any{"ModeWant": userData.modeWant}); err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			}

			t.channelSubUnsub(asUid, userData.modeWant.IsPresencer())
		}

		if asChan {
			if userData.modeWant != oldWant {
				pluginSubscription(sub, plgActCreate)
			} else {
				pluginSubscription(sub, plgActUpd)
			}
		} else {
			usersRegisterUser(asUid, true)
			pluginSubscription(sub, plgActCreate)
		}

	} else {
		if !userData.isChan && asChan {
			sess.queueOut(InfoUseOtherReply(pkt, t.name, now))
			return nil, types.ErrNotFound
		}

		var ownerChange bool

		oldWant = userData.modeWant
		oldGiven = userData.modeGiven

		if modeWant != types.ModeUnset {
			if t.owner == asUid && (!modeWant.IsOwner() || !modeWant.IsJoiner()) {
				sess.queueOut(ErrPermissionDeniedReply(pkt, now))
				return nil, errors.New("群主不可取消群主权限或禁言自身")
			}

			if userData.modeGiven.IsOwner() {
				ownerChange = modeWant.IsOwner() && !userData.modeWant.IsOwner()

				if modeWant.IsOwner() && !userData.modeGiven.BetterEqual(modeWant) {
					userData.modeGiven |= modeWant
				}
			} else if modeWant.IsOwner() {
				sess.queueOut(ErrPermissionDeniedReply(pkt, now))
				return nil, errors.New("非群主无法转让群权")
			} else if t.cat == types.TopicCatGrp && userData.modeGiven.IsAdmin() && modeWant.IsAdmin() {
				if !userData.modeGiven.BetterEqual(modeWant & ^types.ModeDelete) {
					userData.modeGiven |= (modeWant & ^types.ModeDelete)
				}
			}

			switch t.cat {
			case types.TopicCatP2P:
				modeWant = (modeWant & globals.typesModeCP2P) | types.ModeApprove
			case types.TopicCatSys:
				modeWant &= (modeWant & types.ModeCSys) | types.ModeWrite
			}
		}

		if modeWant == types.ModeUnset {
			if !oldWant.IsJoiner() {
				userData.modeWant = userData.modeGiven | t.accessFor(asLvl)
			}
		} else if userData.modeWant != modeWant {
			userData.modeWant = modeWant
		}

		sub := types.Subscription{
			User:  asUid.String(),
			Topic: t.name,
		}

		update := map[string]any{}
		if isNullValue(private) {
			update["Private"] = nil
			userData.private = nil
			sub.Private = private
		} else if private != nil {
			update["Private"] = private
			userData.private = private
			sub.Private = private
		}
		if userData.modeWant != oldWant {
			update["ModeWant"] = userData.modeWant
			sub.ModeWant = userData.modeWant
		}
		if userData.modeGiven != oldGiven {
			update["ModeGiven"] = userData.modeGiven
			sub.ModeGiven = userData.modeGiven
		}

		if len(update) > 0 {
			if err := store.Subs.Update(t.name, asUid, update); err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			}
			pluginSubscription(&sub, plgActUpd)
		}

		if ownerChange {
			oldOwnerData := t.perUser[t.owner]
			oldOwnerOldWant, oldOwnerOldGiven := oldOwnerData.modeWant, oldOwnerData.modeGiven
			oldOwnerData.modeGiven = (oldOwnerData.modeGiven & ^types.ModeOwner)
			oldOwnerData.modeWant = (oldOwnerData.modeWant & ^types.ModeOwner)
			if err := store.Subs.Update(t.name, t.owner,
				map[string]any{
					"ModeWant":  oldOwnerData.modeWant,
					"ModeGiven": oldOwnerData.modeGiven,
				}); err != nil {
				return nil, err
			}
			if err := store.Topics.OwnerChange(t.name, asUid); err != nil {
				return nil, err
			}
			t.perUser[t.owner] = oldOwnerData
			t.notifySubChange(t.owner, asUid, false,
				oldOwnerOldWant, oldOwnerOldGiven, oldOwnerData.modeWant, oldOwnerData.modeGiven, "")
			t.owner = asUid
		}
	}

	if !asChan {
		if (oldWant & oldGiven).IsPresencer() && !(userData.modeWant & userData.modeGiven).IsPresencer() {
			if t.cat == types.TopicCatMe {
				t.presUsersOfInterest("off+dis", t.userAgent)
			} else {
				t.presSingleUserOffline(asUid, userData.modeWant&userData.modeGiven,
					"off+dis", nilPresParams, "", false)
			}
		}
	}
	t.perUser[asUid] = userData

	var modeChanged *MsgAccessMode
	if oldWant != userData.modeWant || oldGiven != userData.modeGiven {
		if !asChan {
			oldReader := (oldWant & oldGiven).IsReader()
			newReader := (userData.modeWant & userData.modeGiven).IsReader()

			if oldReader && !newReader {
				usersUpdateUnread(asUid, userData.readID-t.lastID, true)
			} else if !oldReader && newReader {
				usersUpdateUnread(asUid, t.lastID-userData.readID, true)
			}
		}

		t.notifySubChange(asUid, asUid, asChan, oldWant, oldGiven, userData.modeWant, userData.modeGiven, sess.sid)
	}

	if (pkt.Sub != nil && pkt.Sub.Newsub) || oldWant != userData.modeWant || oldGiven != userData.modeGiven {
		modeChanged = &MsgAccessMode{
			Want:  userData.modeWant.String(),
			Given: userData.modeGiven.String(),
			Mode:  (userData.modeGiven & userData.modeWant).String(),
			Role: topicRoleFromAccess(userData.modeGiven&userData.modeWant,
				t.isChan, userData.isChan),
		}
	}

	if !userData.modeWant.IsJoiner() {
		t.evictUser(asUid, false, "")
		return modeChanged, nil
	}

	if !userData.modeGiven.IsJoiner() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("Topic 拒绝访问；用户已被被封禁")
	}

	return modeChanged, nil
}

// anotherUserSub 完成another用户订阅所需的内部处理。
func (t *Topic) anotherUserSub(sess *Session, asUid, target types.Uid, asChan bool,
	pkt *ClientComMessage) (*MsgAccessMode, error) {

	now := types.TimeNow()
	set := pkt.Set

	hostData, ok := t.perUser[asUid]
	hostMode := hostData.modeGiven & hostData.modeWant
	if !ok || !hostMode.IsSharer() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("Topic 拒绝访问；审批人无共享权限")
	}

	if asChan {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("Topic 拒绝访问：无法将读者订阅至 Channel")
	}

	if t.isReadOnly() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("Topic 已暂停")
	}

	modeGiven := types.ModeUnset
	if set.Sub.Mode != "" {
		if err := modeGiven.UnmarshalText([]byte(set.Sub.Mode)); err != nil {
			sess.queueOut(ErrMalformedReply(pkt, now))
			return nil, err
		}

		if t.cat == types.TopicCatP2P {
			modeGiven = (modeGiven & globals.typesModeCP2P) | types.ModeApprove
		}
	}

	if modeGiven != types.ModeUnset && !hostMode.IsAdmin() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("共享者无法显式设置 modeGiven")
	}

	if modeGiven.IsOwner() && t.owner != asUid {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("非群主尝试转让群主权限")
	}
	if modeGiven != types.ModeUnset && t.owner != asUid &&
		!hostMode.BetterEqual(modeGiven&^types.ModeOwner) {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("管理员无法授予自己不具备的权限")
	}

	oldWant := types.ModeUnset
	oldGiven := types.ModeUnset

	userData, existingSub := t.perUser[target]
	if !existingSub || userData.deleted {
		if t.cat == types.TopicCatGrp && t.subsCount() >= globals.maxSubscriberCount {
			sess.queueOut(ErrPolicyReply(pkt, now))
			return nil, errors.New("超出最大订阅数量限制")
		}

		if modeGiven == types.ModeUnset {
			modeGiven = t.accessFor(auth.LevelAuth)
			modeGiven |= types.ModeJoin
		}

		var modeWant types.AccessMode
		sub, err := store.Subs.Get(t.name, target, true)
		if err != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}

		if sub != nil {
			modeWant = sub.ModeWant
		} else {
			if user, err := store.Users.Get(target); err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			} else if user == nil {
				sess.queueOut(ErrUserNotFoundReply(pkt, now))
				return nil, errors.New("未找到用户")
			} else if user.State != types.StateOK {
				sess.queueOut(ErrPermissionDeniedReply(pkt, now))
				return nil, errors.New("用户已禁言/封禁")
			} else {
				modeWant = user.Access.Auth & modeGiven
			}
		}

		if !modeWant.IsJoiner() {
			sess.queueOut(ErrPermissionDeniedReply(pkt, now))
			return nil, errors.New("由于权限不足邀请被拒绝")
		}

		sub = &types.Subscription{
			User:      target.String(),
			Topic:     t.name,
			ModeWant:  modeWant,
			ModeGiven: modeGiven,
		}

		if err := store.Subs.Create(sub); err != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}
		if t.cat == types.TopicCatGrp {
			t.subCnt++
		}

		userData = perUserData{
			modeGiven: sub.ModeGiven,
			modeWant:  sub.ModeWant,
			private:   nil,
		}
		t.perUser[target] = userData
		t.computePerUserAcsUnion()

		usersRegisterUser(target, true)
		pluginSubscription(sub, plgActCreate)
		sendPush(t.pushForP2PSub(asUid, target, userData.modeWant, userData.modeGiven, now))
	} else {
		oldGiven = userData.modeGiven
		oldWant = userData.modeWant

		if modeGiven == types.ModeUnset {
			modeGiven = userData.modeGiven
		} else if modeGiven != userData.modeGiven {
			if t.owner == target && (!modeGiven.IsOwner() || !modeGiven.IsJoiner()) {
				sess.queueOut(ErrPermissionDeniedReply(pkt, now))
				return nil, errors.New("无法剥夺群主身份或封禁群主")
			}

			if err := store.Subs.Update(t.name, target,
				map[string]any{"ModeGiven": modeGiven}); err != nil {
				return nil, err
			}

			userData.modeGiven = modeGiven
			t.perUser[target] = userData
		}
	}

	var modeChanged *MsgAccessMode
	if oldGiven != userData.modeGiven {
		oldReader := (oldWant & oldGiven).IsReader()
		newReader := (userData.modeWant & userData.modeGiven).IsReader()
		if oldReader && !newReader {
			usersUpdateUnread(target, userData.readID-t.lastID, true)
		} else if !oldReader && newReader {
			usersUpdateUnread(target, t.lastID-userData.readID, true)
		}
		t.notifySubChange(target, asUid, false,
			oldWant, oldGiven, userData.modeWant, userData.modeGiven, sess.sid)

		modeChanged = &MsgAccessMode{
			Given: userData.modeGiven.String(),
			Want:  userData.modeWant.String(),
			Mode:  (userData.modeGiven & userData.modeWant).String(),
			Role: topicRoleFromAccess(userData.modeGiven&userData.modeWant,
				t.isChan, userData.isChan),
		}
	}

	if !userData.modeGiven.IsJoiner() {
		t.evictUser(target, false, "")
	}

	return modeChanged, nil
}

// replyLeaveUnsub 取消订阅用户并从 Topic 解绑其所有 Session
func (t *Topic) replyLeaveUnsub(sess *Session, msg *ClientComMessage, asUid types.Uid) error {
	now := types.TimeNow()

	if asUid.IsZero() {
		panic("replyLeaveUnsub: zero asUid")
	}

	if t.owner == asUid {
		if msg.init {
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
		}
		return errors.New("replyLeaveUnsub: 群主无法取消订阅")
	}

	var err error
	var asChan bool
	if msg.init {
		asChan, err = t.verifyChannelAccess(msg.Original)
		if err != nil {
			sess.queueOut(ErrNotFoundReply(msg, now))
			return errors.New("replyLeaveUnsub: Channel 寻址错误")
		}
	}

	pud := t.perUser[asUid]
	if pud.isChan {
		err = store.Subs.Delete(types.GrpToChn(t.name), asUid)
	} else {
		err = store.Subs.Delete(t.name, asUid)
	}

	if err != nil {
		if msg.init {
			if err == types.ErrNotFound {
				sess.queueOut(InfoNoActionReply(msg, now))
				err = nil
			} else {
				sess.queueOut(ErrUnknownReply(msg, now))
			}
		}
		return err
	}

	if msg.init {
		sess.queueOut(NoErrReply(msg, now))
	}

	var oldWant types.AccessMode
	var oldGiven types.AccessMode
	if !asChan {
		if (pud.modeWant & pud.modeGiven).IsReader() {
			usersUpdateUnread(asUid, pud.readID-t.lastID, true)
		}
		oldWant, oldGiven = pud.modeWant, pud.modeGiven
	} else {
		oldWant, oldGiven = types.ModeCChnReader, types.ModeCChnReader
		t.channelSubUnsub(asUid, false)
	}

	t.notifySubChange(asUid, asUid, asChan, oldWant, oldGiven, types.ModeUnset, types.ModeUnset, sess.sid)
	t.evictUser(asUid, true, sess.sid)
	pluginSubscription(&types.Subscription{Topic: t.name, User: asUid.String()}, plgActDel)

	if t.cat == types.TopicCatGrp {
		t.subCnt--
	}

	if t.cat == types.TopicCatP2P && t.subsCount() == 0 {
		t.markPaused(true)
		globals.hub.unreg <- &topicUnreg{del: true, sess: nil, rcptTo: t.name, pkt: nil}
	}

	return nil
}

// evictUser 从 Topic 驱逐指定用户的所有 Session 并清除缓存
func (t *Topic) evictUser(uid types.Uid, unsub bool, skip string) {
	now := types.TimeNow()
	pud, ok := t.perUser[uid]
	// 在删除频道读者的 perUser 记录前保存其客户端可见 Topic 名称。
	// 否则后续 t.original 会把驱逐通知错误地写成 grp...。
	original := t.original(uid)

	if unsub {
		if t.cat == types.TopicCatP2P {
			pud.online = 0
			pud.deleted = true
			t.perUser[uid] = pud
		} else if ok {
			delete(t.perUser, uid)
			t.computePerUserAcsUnion()

			if !pud.isChan {
				usersRegisterUser(uid, false)
			}
		}
	} else if ok {
		if pud.isChan {
			delete(t.perUser, uid)
		} else {
			pud.online = 0
			t.perUser[uid] = pud
		}
	}

	msg := NoErrEvicted("", original, now)
	msg.Ctrl.Params = map[string]any{"unsub": unsub}
	msg.SkipSid = skip
	msg.uid = uid
	msg.AsUser = uid.UserId()
	for s := range t.sessions {
		if pssd, removed := t.remSession(s, uid); pssd != nil {
			if removed {
				s.detachSession(t.name)
			}
			if s.sid != skip {
				s.queueOut(msg)
			}
		}
	}
}
