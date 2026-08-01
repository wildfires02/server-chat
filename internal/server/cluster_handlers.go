package server

import (
	"errors"
	"fmt"

	"chat/server/logs"
	"chat/server/push"
	"chat/server/store/types"
)

// TopicMaster 是接收代理 Topic 发给主节点 Topic 请求的 RPC 端点。
func (c *Cluster) TopicMaster(msg *ClusterReq, rejected *bool) error {
	*rejected = false

	node := c.nodes[msg.Node]
	if node == nil {
		logs.Warn.Println("cluster TopicMaster: 收到来自未知节点的请求", msg.Node)
		return nil
	}

	// Master 节点为每个节点的每个代理 Topic 维护一个多路复用会话。
	// Channel Topic 特例：
	// * 一个多路复用会话用于频道订阅。
	// * 一个多路复用会话用于群组订阅。
	var msid string
	if msg.CliMsg != nil && types.IsChannel(msg.CliMsg.Original) {
		// 若为 Channel 请求，使用 Channel 原始名称。
		msid = msg.CliMsg.Original
	} else {
		msid = msg.RcptTo
	}
	// 拼接节点名称。
	msid += "-" + msg.Node
	msess := globals.sessionStore.Get(msid)

	if msg.Gone {
		// 代理 Topic 已销毁。清理本地辅助会话。
		// 若这是最后一个会话，主 Topic 也将相应关闭。
		node.stopMultiplexingSession(msess)

		if t := globals.hub.topicGet(msg.RcptTo); t != nil && t.isChan {
			// 若为 Channel Topic，同时清理 "chnX-" 本地辅助会话。
			msidChn := types.GrpToChn(t.name) + "-" + msg.Node
			node.stopMultiplexingSession(globals.sessionStore.Get(msidChn))
		}

		return nil
	}

	if msg.Signature != c.ringSignature() {
		logs.Warn.Println("cluster TopicMaster: 会话签名不匹配", msg.RcptTo)
		*rejected = true
		return nil
	}

	// 若会话不存在，创建新的多路复用会话。
	if msess == nil {
		var count int
		msess, count = globals.sessionStore.NewSession(node, msid)
		node.lock.Lock()
		node.msess[msid] = struct{}{}
		node.lock.Unlock()

		logs.Info.Println("cluster: 多路复用会话已启动", msid, count)
		msess.proxiedTopic = msg.RcptTo
	}

	// 这是远端 Session 的本地副本
	var sess *Session
	// 对于用户代理变更和延迟在线通知请求，Sess 为 nil
	if msg.Sess != nil {
		// 只需要一些 Session 信息，无需复制所有内容
		sess = &Session{
			proto: PROXY,
			// 多路复用 Session，实际处理通信
			multi: msess,
			// 此 Session 特有的本地参数
			sid:         msg.Sess.Sid,
			userAgent:   msg.Sess.UserAgent,
			remoteAddr:  msg.Sess.RemoteAddr,
			lang:        msg.Sess.Lang,
			countryCode: msg.Sess.CountryCode,
			proxyReq:    msg.ReqType,
			background:  msg.Sess.Background,
			uid:         msg.Sess.Uid,
		}
	}

	if msg.CliMsg != nil {
		msg.CliMsg.sess = sess
		msg.CliMsg.init = true
	}

	switch msg.ReqType {
	case ProxyReqJoin:
		select {
		case globals.hub.join <- msg.CliMsg:
		default:
			// 向用户回复 500 错误
			sess.queueOut(ErrUnknownReply(msg.CliMsg, msg.CliMsg.Timestamp))
			logs.Warn.Println("cluster: join req failed - hub.join queue full, topic ", msg.CliMsg.RcptTo,
				"; orig sid ", sess.sid)
		}

	case ProxyReqLeave:
		if t := globals.hub.topicGet(msg.RcptTo); t != nil {
			t.unreg <- msg.CliMsg
		} else {
			logs.Warn.Println("cluster: leave request for unknown topic", msg.RcptTo)
		}

	case ProxyReqMeta:
		if t := globals.hub.topicGet(msg.RcptTo); t != nil {
			select {
			case t.meta <- msg.CliMsg:
			default:
				sess.queueOut(ErrUnknownReply(msg.CliMsg, msg.CliMsg.Timestamp))
				logs.Warn.Println("cluster: meta req failed - topic.meta queue full, topic ", msg.CliMsg.RcptTo,
					"; orig sid ", sess.sid)
			}
		} else {
			logs.Warn.Println("cluster: meta request for unknown topic", msg.RcptTo)
		}

	case ProxyReqBroadcast:
		select {
		case globals.hub.routeCli <- msg.CliMsg:
		default:
			logs.Err.Println("cluster: route req failed - hub.route queue full")
			return errors.New("cluster: 主节点业务队列已满")
		}

	case ProxyReqBgSession, ProxyReqMeUserAgent:
		//Sess可能是nil
		if t := globals.hub.topicGet(msg.RcptTo); t != nil {
			if t.supd == nil {
				logs.Err.Panicln("cluster: invalid topic category in session update", t.name, msg.ReqType)
			}
			su := &sessionUpdate{}
			if msg.ReqType == ProxyReqBgSession {
				su.sess = sess
			} else {
				su.userAgent = sess.userAgent
			}
			t.supd <- su
		} else {
			logs.Warn.Println("cluster: session update for unknown topic", msg.RcptTo, msg.ReqType)
		}

	default:
		logs.Warn.Println("cluster: unknown request type", msg.ReqType, msg.RcptTo)
		*rejected = true
	}

	return nil
}

// TopicProxy 是 Topic 代理上的 gRPC 端点，接收 Topic 主节点的响应
func (*Cluster) TopicProxy(msg *ClusterResp, unused *bool) error {
	if msg == nil || msg.SrvMsg == nil {
		return errors.New("cluster: TopicProxy 收到空投递消息")
	}
	// 本集群成员收到来自 Topic 主节点的响应，需转发给 Topic
	// 找到对应的 Topic，将消息发送给它
	if t := globals.hub.topicGet(msg.RcptTo); t != nil {
		msg.SrvMsg.uid = types.ParseUserId(msg.SrvMsg.AsUser)
		select {
		case t.proxy <- msg:
		default:
			logs.Warn.Printf("cluster: proxy channel full, topic %s", msg.RcptTo)
			return errors.New("cluster: 边缘 Topic 投递队列已满")
		}
	} else {
		logs.Warn.Println("cluster: master response for unknown topic", msg.RcptTo)
		return fmt.Errorf("cluster: 边缘 Topic %q 不存在", msg.RcptTo)
	}

	return nil
}

// Route 端点接收集群内部发往托管 Topic 节点的消息
// 由 Hub.route Channel 消费者调用，用于在不附加到 Topic 的情况下发送消息
func (c *Cluster) Route(msg *ClusterRoute, rejected *bool) error {
	logError := func(err string) {
		sid := ""
		if msg.Sess != nil {
			sid = msg.Sess.Sid
		}
		logs.Warn.Println(err, sid)
		*rejected = true
	}

	*rejected = false
	if msg.Signature != c.ringSignature() {
		logError("cluster Route: session signature mismatch")
		return nil
	}

	if msg.SrvMsg == nil {
		logError("cluster Route: nil server message")
		return errors.New("cluster Route: nil server message")
	}

	select {
	case globals.hub.routeSrv <- msg.SrvMsg:
	default:
		logError("cluster Route: server busy")
		return errors.New("cluster Route: server busy")
	}
	return nil
}

// 用户缓存和推送通知管理。这些是主节点从代理接收的调用
// 代理期望主节点不返回负载

// UserCacheUpdate 端点接收用户缓存值的更新并发送推送通知
func (c *Cluster) UserCacheUpdate(msg *UserCacheReq, rejected *bool) error {
	if msg.Gone {
		// 用户已删除。驱逐该用户的所有 Session
		globals.sessionStore.EvictUser(msg.UserId, "")

		if globals.cluster.isRemoteTopic(msg.UserId.UserId()) {
			// 如果用户是远程的，无需删除用户缓存
			return nil
		}
	}

	usersRequestFromCluster(msg)
	return nil
}

// Ping 是 gRPC 端点，接收对等节点的 ping 请求。用于检测节点重启
func (c *Cluster) Ping(ping *ClusterPing, unused *bool) error {
	node := c.nodes[ping.Node]
	if node == nil {
		logs.Warn.Println("cluster Ping from unknown node", ping.Node)
		return nil
	}

	if node.fingerprint == 0 {
		// 这是首次连接到远程节点
		node.fingerprint = ping.Fingerprint
	} else if node.fingerprint != ping.Fingerprint {
		// 远程节点已重启
		node.fingerprint = ping.Fingerprint
		c.invalidateProxySubs(ping.Node)
		c.gcProxySessionsForNode(ping.Node)
	}

	return nil
}

// 将用户缓存更新发送到用户主节点（缓存实际所在位置）
// 请求应仅包含驻留在远程节点的用户
func (c *Cluster) routeUserReq(req *UserCacheReq) error {
	// 按集群节点索引请求
	reqByNode := make(map[string]*UserCacheReq)

	if req.PushRcpt != nil {
		// 请求发送推送通知。为每个受影响的集群节点创建单独的数据包
		for uid, recipient := range req.PushRcpt.To {
			n := c.nodeForTopic(uid.UserId())
			if n == nil {
				return errors.New("attempt to update user at a non-existent node (1)")
			}
			r := reqByNode[n.name]
			if r == nil {
				r = &UserCacheReq{
					PushRcpt: &push.Receipt{
						Payload: req.PushRcpt.Payload,
						To:      make(map[types.Uid]push.Recipient),
					},
					Node: c.thisNodeName,
				}
			}
			r.PushRcpt.To[uid] = recipient
			reqByNode[n.name] = r
		}
	} else if len(req.UserIdList) > 0 {
		// 请求从缓存中添加/移除部分用户
		for _, uid := range req.UserIdList {
			n := c.nodeForTopic(uid.UserId())
			if n == nil {
				return errors.New("attempt to update user at a non-existent node (2)")
			}
			r := reqByNode[n.name]
			if r == nil {
				r = &UserCacheReq{Node: c.thisNodeName, Inc: req.Inc}
			}
			r.UserIdList = append(r.UserIdList, uid)
			reqByNode[n.name] = r
		}
	} else if req.Gone {
		// 用户已删除的消息发送到所有节点
		r := &UserCacheReq{Node: c.thisNodeName, UserIdList: req.UserIdList, Gone: true}
		for _, n := range c.nodes {
			reqByNode[n.name] = r
		}
	}

	if len(reqByNode) > 0 {
		for nodeName, r := range reqByNode {
			n := c.nodes[nodeName]
			var rejected bool
			err := n.call("Cluster.UserCacheUpdate", r, &rejected)
			if rejected {
				err = errors.New("master node out of sync")
			}
			if err != nil {
				return err
			}
		}
		return nil
	}

	// 更新缓存值
	n := c.nodeForTopic(req.UserId.UserId())
	if n == nil {
		return errors.New("attempt to update user at a non-existent node (3)")
	}
	req.Node = c.thisNodeName
	var rejected bool
	err := n.call("Cluster.UserCacheUpdate", req, &rejected)
	if rejected {
		err = errors.New("master node out of sync")
	}
	return err
}
