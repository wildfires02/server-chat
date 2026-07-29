/******************************************************************************
 *  描述 :
 *    集群中的 Proxy Topic，用作托管在另一个节点上的 Master Topic 的本地代理表示。
 *****************************************************************************/

// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"net/http"
	"time"

	"chat/server/logs"
	"chat/server/store/types"
)

// runProxy 启动并运行代理处理流程。
func (t *Topic) runProxy(hub *Hub) {
	killTimer := time.NewTimer(time.Hour)
	killTimer.Stop()

	for {
		select {
		case msg := <-t.reg:
			// 请求将连接添加到此 Topic
			if t.isInactive() {
				msg.sess.queueOut(ErrLockedReply(msg, types.TimeNow()))
			} else if err := globals.cluster.routeToTopicMaster(ProxyReqJoin, msg, t.name, msg.sess); err != nil {
				// 响应 (ctrl 消息) 将在通过 proxy Channel 接收时处理。
				logs.Warn.Printf("proxy topic[%s]: route join request from proxy to master failed - %s", t.name, err)
				msg.sess.queueOut(ErrClusterUnreachableReply(msg, types.TimeNow()))
			}
			if msg.sess.inflightReqs != nil {
				msg.sess.inflightReqs.Done()
			}

		case msg := <-t.unreg:
			if !t.handleProxyLeaveRequest(msg, killTimer) {
				sid := "nil"
				if msg.sess != nil {
					sid = msg.sess.sid
				}
				logs.Warn.Printf("proxy topic[%s]: failed to update proxy topic state for leave request - sid %s", t.name, sid)
				msg.sess.queueOut(ErrClusterUnreachableReply(msg, types.TimeNow()))
			}
			if msg.init && msg.sess.inflightReqs != nil {
				// 如果是客户端发起的请求。
				msg.sess.inflightReqs.Done()
			}

		case msg := <-t.clientMsg:
			// 拟广播给接收者的 Content 消息
			if err := globals.cluster.routeToTopicMaster(ProxyReqBroadcast, msg, t.name, msg.sess); err != nil {
				logs.Warn.Printf("topic proxy[%s]: route broadcast request from proxy to master failed - %s", t.name, err)
				msg.sess.queueOut(ErrClusterUnreachableReply(msg, types.TimeNow()))
			}

		case msg := <-t.serverMsg:
			if msg.Info != nil || msg.Pres != nil {
				globals.cluster.routeToTopicIntraCluster(t.name, msg, msg.sess)
			} else if msg.Data != nil {
				t.handleProxyBroadcast(msg)
			} else if msg.Ctrl != nil {
				t.proxyCtrlBroadcast(msg)
			} else {
				logs.Warn.Printf("topic proxy[%s]: unrecognized server-side message - %s", t.name, msg.describe())
			}

		case msg := <-t.meta:
			// 获取/设置 Topic 元数据的请求
			if err := globals.cluster.routeToTopicMaster(ProxyReqMeta, msg, t.name, msg.sess); err != nil {
				logs.Warn.Printf("proxy topic[%s]: route meta request from proxy to master failed - %s", t.name, err)
				msg.sess.queueOut(ErrClusterUnreachableReply(msg, types.TimeNow()))
			}

		case upd := <-t.supd:
			// 来自其中一个 Session 的 'me' 用户代理更新，或
			// 后台 Session 切换到前台。
			req := ProxyReqMeUserAgent
			tmpSess := &Session{userAgent: upd.userAgent}
			if upd.sess != nil {
				// 订阅的用户可能与 Session 用户不匹配。查明是谁订阅了
				pssd, ok := t.sessions[upd.sess]
				if !ok {
					logs.Warn.Printf("proxy topic[%s]: sess update request from detached session - sid %s", t.name, upd.sess.sid)
					continue
				}
				req = ProxyReqBgSession
				tmpSess.uid = pssd.uid
				tmpSess.sid = upd.sess.sid
				tmpSess.userAgent = upd.sess.userAgent
			}
			if err := globals.cluster.routeToTopicMaster(req, nil, t.name, tmpSess); err != nil {
				logs.Warn.Printf("proxy topic[%s]: route sess update request from proxy to master failed - %s", t.name, err)
			}

		case msg := <-t.proxy:
			t.proxyMasterResponse(msg, killTimer)

		case sd := <-t.exit:
			// 通知 Session 移除此 Topic
			for s := range t.sessions {
				s.detachSession(t.name)
			}

			if err := globals.cluster.topicProxyGone(t.name); err != nil {
				logs.Warn.Printf("proxy topic[%s] shutdown: failed to notify master - %s", t.name, err)
			}

			// 如果 'done' 不为 nil，向发送方报告完成状态。
			if sd.done != nil {
				sd.done <- true
			}
			return

		case <-killTimer.C:
			// Topic 超时
			hub.unreg <- &topicUnreg{rcptTo: t.name}
		}
	}
}

// 接收 Session 离开请求，将其转发给 Master Topic 并
// 相应地修改本地状态。
// 返回操作是否成功。
func (t *Topic) handleProxyLeaveRequest(msg *ClientComMessage, killTimer *time.Timer) bool {
	// 从 Topic 中解绑 Session；Session 可继续运行。
	var asUid types.Uid
	if msg.init {
		asUid = types.ParseUserId(msg.AsUser)
	}

	if asUid.IsZero() {
		if pssd, ok := t.sessions[msg.sess]; ok {
			asUid = pssd.uid
		} else {
			logs.Warn.Printf("proxy topic[%s]: leave request sent for unknown session", t.name)
			return false
		}
	}
	// 从 Topic 中移除 Session，无需等待来自 Master 节点的响应，
	// 因为当响应到达时，该 Session 可能已从 Session 存储中销毁，
	// 我们将无法通过其 sid 找到并移除它。
	pssd, result := t.remSession(msg.sess, asUid)
	if result {
		msg.sess.delSub(t.name)
	}
	if !msg.init {
		// 显式指定 uid，因为 Master 多路复用 Session 需要知道要删除其托管的
		// 多个 Session 中的哪一个。
		msg.AsUser = asUid.UserId()
		msg.Leave = &MsgClientLeave{}
		msg.init = true
	}
	// 确保当 Original 字段为空时设置它（例如 Session 完全终止时）。
	if msg.Original == "" {
		if t.cat == types.TopicCatGrp && t.isChan {
			// 这是一个 Channel Topic。原始 Topic 名称取决于订阅类型。
			if result && pssd.isChanSub {
				msg.Original = types.GrpToChn(t.xoriginal)
			} else {
				msg.Original = t.xoriginal
			}
		} else {
			msg.Original = t.original(asUid)
		}
	}

	if err := globals.cluster.routeToTopicMaster(ProxyReqLeave, msg, t.name, msg.sess); err != nil {
		logs.Warn.Printf("proxy topic[%s]: route leave request from proxy to master failed - %s", t.name, err)
	}
	if len(t.sessions) == 0 {
		// 没有更多的 Session 挂载。启动倒计时。
		killTimer.Reset(idleProxyTopicTimeout)
	}
	return result
}

// proxyMasterResponse 在 Proxy Topic 端处理 Master Topic 对早前请求的响应。
func (t *Topic) proxyMasterResponse(msg *ClusterResp, killTimer *time.Timer) {
	// 在一段时间未活动后销毁 Topic。
	keepAlive := idleProxyTopicTimeout

	if msg.SrvMsg.Pres != nil && msg.SrvMsg.Pres.What == "acs" && msg.SrvMsg.Pres.Acs != nil {
		// 如果服务器更改了此 Topic 上的 acs，则更新内部状态。
		t.updateAcsFromPresMsg(msg.SrvMsg.Pres)
	}

	if msg.OrigSid == "*" {
		// 这是一个广播消息。
		switch {
		case msg.SrvMsg.Pres != nil || msg.SrvMsg.Data != nil || msg.SrvMsg.Info != nil:
			// 普通广播。
			t.handleProxyBroadcast(msg.SrvMsg)
		case msg.SrvMsg.Ctrl != nil:
			// Ctrl 广播。例如用于用户驱逐。
			t.proxyCtrlBroadcast(msg.SrvMsg)
		default:
		}
	} else {
		sess := globals.sessionStore.Get(msg.OrigSid)
		if sess == nil {
			logs.Warn.Printf("proxy topic[%s]: session %s not found; already terminated?", t.name, msg.OrigSid)
		}
		switch msg.OrigReqType {
		case ProxyReqJoin:
			if msg.SrvMsg.Ctrl != nil {
				// 订阅结果。
				if msg.SrvMsg.Ctrl.Code < 300 {
					var session *Session
					if sess != nil {
						sess.sessionStoreLock.Lock()
						// 确保 Session 尚未销毁。
						if session = globals.sessionStore.Get(msg.OrigSid); session != nil {
							// 订阅成功。
							t.addSession(session, msg.SrvMsg.uid, types.IsChannel(msg.SrvMsg.Ctrl.Topic))
							session.addSub(t.name, &Subscription{
								broadcast: t.clientMsg,
								done:      t.unreg,
								meta:      t.meta,
								supd:      t.supd,
							})
						}
						sess.sessionStoreLock.Unlock()
					}

					if session == nil {
						// Session 在等待 Join 响应期间已销毁：Session 销毁时尚无该订阅，无法自行通知 Master。
						// 主动向 Master 发送 ProxyReqLeave 以清理 Master 节点上的失效订阅。
						leaveMsg := &ClientComMessage{
							Leave:    &MsgClientLeave{},
							Original: t.original(msg.SrvMsg.uid),
							AsUser:   msg.SrvMsg.uid.UserId(),
						}
						if err := globals.cluster.routeToTopicMaster(ProxyReqLeave, leaveMsg, t.name, nil); err != nil {
							logs.Warn.Printf("proxy topic[%s]: failed to send leave for terminated session %s - %s", t.name, msg.OrigSid, err)
						}
					}

					killTimer.Stop()
				} else if len(t.sessions) == 0 {
					killTimer.Reset(keepAlive)
				}
			}
		case ProxyReqBroadcast, ProxyReqMeta, ProxyReqCall:
			// 不做处理
		case ProxyReqLeave:
			if msg.SrvMsg != nil && msg.SrvMsg.Ctrl != nil {
				if msg.SrvMsg.Ctrl.Code < 300 {
					if sess != nil {
						t.remSession(sess, sess.uid)
					}
				}
				// 所有 Session 均已离开。启动销毁定时器。
				if len(t.sessions) == 0 {
					killTimer.Reset(keepAlive)
				}
			}

		default:
			logs.Err.Printf("proxy topic[%s] received response referencing unexpected request type %d",
				t.name, msg.OrigReqType)
		}

		if sess != nil && !sess.queueOut(msg.SrvMsg) {
			logs.Err.Printf("proxy topic[%s]: timeout in sending response - sid %s", t.name, sess.sid)
		}
	}
}

// handleProxyBroadcast 将 Data、Info 或 Pres 消息广播给挂载到此 Proxy Topic 的 Session。
func (t *Topic) handleProxyBroadcast(msg *ServerComMessage) {
	if t.isInactive() {
		// 忽略广播 - Topic 已暂停或正在被删除。
		return
	}

	if msg.Data != nil {
		t.lastID = msg.Data.SeqId
	}

	t.broadcastToSessions(msg)
}

// proxyCtrlBroadcast 将 ctrl 命令广播给挂载到此 Proxy Topic 的特定 Session。
func (t *Topic) proxyCtrlBroadcast(msg *ServerComMessage) {
	if msg.Ctrl.Code == http.StatusResetContent && msg.Ctrl.Text == "evicted" {
		// 收到用于驱逐用户的 ctrl 命令。
		if msg.uid.IsZero() {
			logs.Err.Panicf("proxy topic[%s]: proxy received evict message with empty uid", t.name)
		}
		for sess := range t.sessions {
			// Proxy Topic 仅可能包含普通 Session。此处没有多路复用或代理 Session。
			if _, removed := t.remSession(sess, msg.uid); removed {
				sess.detachSession(t.name)
				if sess.sid != msg.SkipSid {
					sess.queueOut(msg)
				}
			}
		}
	}
}

// updateAcsFromPresMsg 根据 pres 中的数据修改 Topic 的 perUser 结构体中用户的 acs 权限。
func (t *Topic) updateAcsFromPresMsg(pres *MsgServerPres) {
	uid := types.ParseUserId(pres.Src)
	if uid.IsZero() {
		if t.cat != types.TopicCatMe {
			logs.Warn.Printf("proxy topic[%s]: received acs change for invalid user id '%s'", t.name, pres.Src)
		}
		return
	}

	// 如果 t.perUser[uid] 不存在，则用空值初始化 pud；否则获取已有值。
	pud := t.perUser[uid]
	dacs := pres.Acs
	if err := pud.modeWant.ApplyMutation(dacs.Want); err != nil {
		logs.Warn.Printf("proxy topic[%s]: could not process acs change - want: %s", t.name, err)
		return
	}
	if err := pud.modeGiven.ApplyMutation(dacs.Given); err != nil {
		logs.Warn.Printf("proxy topic[%s]: could not process acs change - given: %s", t.name, err)
		return
	}
	// 更新已有记录或添加新记录。
	t.perUser[uid] = pud
}
