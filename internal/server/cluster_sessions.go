package server

import (
	"chat/server/logs"
	"chat/server/store/types"
)

// invalidateProxySubs 遍历在本节点上代理的 Session，优先代表代理 Session
// 向新的 Master 节点自动重新订阅代理 Topic。若重订阅失败，则降级发送 "{pres term}"。
// - 在 Cluster.rehash() 之后立即对所有已迁移的 Topic 调用 (forNode == "")
// - 当检测到节点重启时，对托管在特定节点的 Topic 调用
func (c *Cluster) invalidateProxySubs(forNode string) {
	sessionsToTerminate := make(map[*Session][]string)
	globals.hub.topics.Range(func(_, v any) bool {
		topic := v.(*Topic)
		if !topic.isProxy {
			// Topic 不是代理
			return true
		}
		newMaster := c.topicOwner(topic.name)
		if forNode == "" {
			if topic.masterNode == newMaster {
				// Topic 未迁移。继续
				return true
			}
		} else if topic.masterNode != forNode {
			// Topic 托管在与重启节点不同的节点上
			return true
		}

		// 更新代理 Topic 的主节点映射至新 Master 节点
		topic.masterNode = newMaster

		for s, psd := range topic.sessions {
			topName := topicNameForUser(topic.name, psd.uid, psd.isChanSub)
			if newMaster != c.thisNodeName {
				joinMsg := &ClientComMessage{
					RcptTo:   topic.name,
					Original: topName,
					AsUser:   psd.uid.UserId(),
					Sub: &MsgClientSub{
						Topic: topName,
					},
					Timestamp: types.TimeNow(),
				}

				// 尝试向新 Master 节点发起透明代理重新订阅
				if err := c.routeToTopicMaster(ProxyReqJoin, joinMsg, topic.name, s); err != nil {
					logs.Warn.Printf("cluster: auto-resubscribe for topic '%s' session '%s' to node '%s' failed: %v",
						topic.name, s.sid, newMaster, err)
					sessionsToTerminate[s] = append(sessionsToTerminate[s], topName)
				}
			} else {
				// 新 Master 是本节点，由 hub.rehash 统一转本地处理
				sessionsToTerminate[s] = append(sessionsToTerminate[s], topName)
			}
		}
		return true
	})

	for s, topicsToTerminate := range sessionsToTerminate {
		s.presTermDirect(topicsToTerminate)
	}
}

// gcProxySessions 在主节点终止所有丢失节点的孤立代理 Session（allNodes 减去 activeNodes）
// 当源节点失效时，Session 变为孤立
func (c *Cluster) gcProxySessions(activeNodes []string) {
	// 本节点从活动拓扑退出时没有对应的远端 ClusterNode，也不存在需要
	// 回收的“来自本节点的代理 Session”。这里只比较远端候选节点，避免
	// Drain 本节点时把 self 当作远端解引用。
	allNodes := make([]string, 0, len(c.nodes))
	for name, node := range c.nodes {
		if name == c.thisNodeName || node == nil {
			continue
		}
		allNodes = append(allNodes, name)
	}
	_, failedNodes, _ := stringSliceDelta(allNodes, activeNodes)
	for _, node := range failedNodes {
		// 遍历故障节点的 Session
		c.gcProxySessionsForNode(node)
	}
}

// gcProxySessionsForNode 在主节点终止指定节点的孤立代理 Session
// 例如，远程节点重启或集群在不包含该节点的情况下重新哈希
func (c *Cluster) gcProxySessionsForNode(node string) {
	n := c.nodes[node]
	if n == nil {
		return
	}
	n.lock.Lock()
	msess := n.msess
	n.msess = make(map[string]struct{})
	n.lock.Unlock()
	for sid := range msess {
		if sess := globals.sessionStore.Get(sid); sess != nil {
			sess.stop <- nil
		}
	}
}

// clusterWriteLoop 在托管主 Topic 的节点上实现多路复用（代理）Session 的写循环
// 该 Session 是多路复用 Session，即它处理来自源端多个 Session 的请求
func (sess *Session) clusterWriteLoop(forTopic string) {
	terminate := true
	defer func() {
		sess.clusterWriterScheduled.Store(false)
		if terminate {
			sess.closeRPC()
			globals.sessionStore.Delete(sess)
			sess.inflightReqs = nil
			sess.unsubAll()
		} else if len(sess.send) > 0 {
			// 清空标记后发现有新消息，重新调度以关闭“检查空队列”竞态窗口。
			sess.scheduleClusterWriteLoop()
		}
	}()

	for {
		select {
		case msg, ok := <-sess.send:
			if !ok ||
				(sess.clnode.grpcPeer == nil && sess.clnode.endpoint == nil) {
				// Channel 已关闭
				return
			}
			srvMsg := msg.(*ServerComMessage)
			response := &ClusterResp{SrvMsg: srvMsg}
			if srvMsg.sess == nil {
				response.OrigSid = "*"
			} else {
				response.OrigReqType = srvMsg.sess.proxyReq
				response.OrigSid = srvMsg.sess.sid
				srvMsg.AsUser = srvMsg.sess.uid.UserId()

				switch srvMsg.sess.proxyReq {
				case ProxyReqJoin, ProxyReqLeave, ProxyReqMeta, ProxyReqBgSession, ProxyReqMeUserAgent, ProxyReqCall:
				// 不执行任何操作
				case ProxyReqBroadcast, ProxyReqNone:
					if srvMsg.Data != nil || srvMsg.Pres != nil || srvMsg.Info != nil {
						response.OrigSid = "*"
					} else if srvMsg.Ctrl == nil {
						logs.Warn.Println("cluster: request type not set in clusterWriteLoop", sess.sid,
							srvMsg.describe(), "src_sid:", srvMsg.sess.sid)
					}
				default:
					logs.Err.Panicln("cluster: unknown request type in clusterWriteLoop", srvMsg.sess.proxyReq)
				}
			}

			srvMsg.RcptTo = forTopic
			response.RcptTo = forTopic

			var unused bool
			if err := sess.clnode.call("Cluster.TopicProxy", response, &unused); err != nil {
				logs.Warn.Printf("cluster: response to proxy failed \"%s\": %s", sess.sid, err.Error())
				return
			}
		case msg := <-sess.stop:
			if msg == nil {
				// 正在终止多路复用 Session
				return
			}
			// msg != nil 有两种情况：
			//  * 用户正在被删除
			//  * 节点关闭
			// 这两种情况下，msg 都不需要转发到代理

		case <-sess.detach:
			return
		default:
			terminate = false
			return
		}
	}
}
