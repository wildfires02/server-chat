// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

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
						t.evictColdSubscriber(uid)
					}
				}
			} else if len(pssd.muids) > 0 {
				for _, uid := range pssd.muids {
					if t.perUser[uid].online == 0 {
						if asChan {
							delete(t.perUser, uid)
						} else {
							t.presSubsOnline("off", uid.UserId(), nilPresParams, readFilter, "")
							t.evictColdSubscriber(uid)
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

	if t.isOfficialLargeGroup() {
		// 冷成员可能没有内存快照；先按需读取持久订阅，避免把已存在成员误判为新成员。
		if _, err := t.loadSubscriber(asUid); err != nil {
			msg.sess.queueOut(ErrUnknownReply(msg, now))
			return err
		}
	}

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
	if modeChanged, err = t.thisUserSub(msg.sess, msg, asUid, asChan, mode, private, msgsub.Invite); err != nil {
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
		if t.isOfficialLargeGroup() && msgsub.Newsub {
			// 原生 App 通过供应商 Topic 接收官方大群离线通知。这里只提交
			// 一个异步订阅动作，不读取或遍历大群成员。
			t.pushTopicSubUnsub(asUid, true)
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
