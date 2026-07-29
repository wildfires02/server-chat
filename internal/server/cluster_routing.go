package server

import (
	"errors"
	"sync/atomic"

	"chat/server/logs"
	"chat/server/store/types"
)

// 根据 Topic 名称，找到合适的集群节点来路由消息
func (c *Cluster) nodeForTopic(topic string) *ClusterNode {
	key := c.topicOwner(topic)
	if key == c.thisNodeName {
		logs.Err.Println("cluster: request to route to self")
		// 不路由到自己
		return nil
	}

	node := c.nodes[key]
	if node == nil {
		logs.Warn.Println("cluster: no node for topic", topic, key)
	}
	return node
}

// isRemoteTopic 检查给定 Topic 是由本节点还是远程节点处理
func (c *Cluster) isRemoteTopic(topic string) bool {
	if c == nil {
		// 集群未初始化，所有 Topic 都是本地的
		return false
	}
	return c.topicOwner(topic) != c.thisNodeName
}

// genLocalTopicName 与 genTopicName() 类似，但生成的名称属于当前集群节点。
// 限制最大尝试次数上限（32 次），防止在大规模集群下产生 CPU 暴涨或无限死循环。
func (c *Cluster) genLocalTopicName() string {
	topic := genTopicName()
	if c == nil {
		// 集群未初始化，所有 Topic 都是本地的
		return topic
	}

	const maxAttempts = 32
	for i := 0; i < maxAttempts; i++ {
		if c.topicOwner(topic) == c.thisNodeName {
			return topic
		}
		topic = genTopicName()
	}

	logs.Warn.Printf("cluster: genLocalTopicName reached max attempts (%d), fallback to non-local topic '%s'",
		maxAttempts, topic)
	return topic
}

// isPartitioned 检查集群是否因网络或其他故障而分区，以及
// 当前节点是否属于较小分区
func (c *Cluster) isPartitioned() bool {
	if c == nil {
		return false
	}
	if c.controlPlane != nil {
		ready := c.controlPlane.Ready()
		statsSet("ClusterControlPlaneReady", boolToInt64(ready))
		return !ready
	}
	if c.fo == nil {
		// 集群未初始化或故障转移未启用，因此未分区
		return false
	}

	c.fo.activeNodesLock.RLock()
	result := (len(c.nodes)+1)/2 >= len(c.fo.activeNodes)
	c.fo.activeNodesLock.RUnlock()

	return result
}

// makeClusterReq 创建并初始化集群Req。
func (c *Cluster) makeClusterReq(reqType ProxyReqType, msg *ClientComMessage, topic string, sess *Session) *ClusterReq {
	req := &ClusterReq{
		Node:        c.thisNodeName,
		Signature:   c.ringSignature(),
		Fingerprint: c.fingerprint,
		ReqType:     reqType,
		RcptTo:      topic,
	}

	var uid types.Uid

	if msg != nil {
		req.CliMsg = msg
		uid = types.ParseUserId(req.CliMsg.AsUser)
	}

	if sess != nil {
		if uid.IsZero() {
			uid = sess.uid
		}

		req.Sess = &ClusterSess{
			Uid:         uid,
			AuthLvl:     sess.authLvl,
			RemoteAddr:  sess.remoteAddr,
			UserAgent:   sess.userAgent,
			Ver:         sess.ver,
			Lang:        sess.lang,
			CountryCode: sess.countryCode,
			DeviceID:    sess.deviceID,
			Platform:    sess.platf,
			Sid:         sess.sid,
			Background:  sess.background,
		}
	}
	return req
}

// 将客户端请求消息从 Topic 代理转发到 Topic 主节点（拥有该 Topic 的集群节点）
func (c *Cluster) routeToTopicMaster(reqType ProxyReqType, msg *ClientComMessage, topic string, sess *Session) error {
	if c == nil {
		// 集群可能因关闭而为 nil
		return nil
	}

	if sess != nil && reqType != ProxyReqLeave {
		if atomic.LoadInt32(&sess.terminating) > 0 {
			// Session 正在终止
			// 除 "leave" 外，不向 Topic 主节点转发任何请求
			return nil
		}
	}

	req := c.makeClusterReq(reqType, msg, topic, sess)

	// 找到拥有该 Topic 的集群节点，然后转发给它
	n := c.nodeForTopic(topic)
	if n == nil {
		return errors.New("node for topic not found")
	}
	return n.proxyToMasterAsync(req)
}

// 将服务器响应消息转发到拥有 Topic 的节点
func (c *Cluster) routeToTopicIntraCluster(topic string, msg *ServerComMessage, sess *Session) error {
	if c == nil {
		// 集群可能因关闭而为 nil
		return nil
	}

	n := c.nodeForTopic(topic)
	if n == nil {
		return errors.New("node for topic not found (intra)")
	}

	route := &ClusterRoute{
		Node:        c.thisNodeName,
		Signature:   c.ringSignature(),
		Fingerprint: c.fingerprint,
		SrvMsg:      msg,
	}

	if sess != nil {
		route.Sess = &ClusterSess{Sid: sess.sid}
	}
	return n.route(route)
}

// Topic 代理已终止。通知远程主节点该代理已失效
func (c *Cluster) topicProxyGone(topicName string) error {
	if c == nil {
		// 集群可能因关闭而为 nil
		return nil
	}

	// 找到拥有该 Topic 的集群节点，然后转发给它
	n := c.nodeForTopic(topicName)
	if n == nil {
		return errors.New("node for topic not found")
	}

	req := c.makeClusterReq(ProxyReqLeave, nil, topicName, nil)
	req.Gone = true
	return n.proxyToMasterAsync(req)
}
