package main

import (
	"encoding/gob"
	"encoding/json"
	"errors"
	"net"
	"net/rpc"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"chat/server/auth"
	"chat/server/concurrency"
	"chat/server/logs"
	"chat/server/push"
	rh "chat/server/ringhash"
	"chat/server/store/types"
)

const (
	// 网络连接超时时间。
	clusterNetworkTimeout = 3 * time.Second
	// 重连集群节点的默认等待时间。
	clusterDefaultReconnectTime = 200 * time.Millisecond
	// 一致性哈希环 (RingHash) 中的虚拟节点副本数。
	clusterHashReplicas = 20
	// 代理节点 (Proxy) 向主节点 (Master) 发送请求的缓冲队列大小。
	clusterProxyToMasterBuffer = 64
	// 在基础 3 节点集群配置之上，每增加一个节点扩展的缓冲区大小。
	clusterProxyToMasterBufferPerNode = 16
	// 当缓冲队列已满时，尝试入队 Proxy-to-Master 请求的超时时间。
	clusterP2MTimeout = 20 * time.Millisecond
	// 每个节点接收来自其他节点 RPC 响应的缓冲队列大小。
	clusterRpcCompletionBuffer = 64
)

// ProxyReqType 表示代理请求的类型。
type ProxyReqType int

// 各类具体的代理请求类型。
const (
	ProxyReqNone      ProxyReqType = iota
	ProxyReqJoin                   // {sub} 订阅事件
	ProxyReqLeave                  // {leave} 退订事件
	ProxyReqMeta                   // {meta set|get} 元数据操作
	ProxyReqBroadcast              // {pub}, {note} 广播消息
	ProxyReqBgSession
	ProxyReqMeUserAgent
	ProxyReqCall // 用于视频通话代理 Session 中路由通话事件
)

type clusterNodeConfig struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
}

type clusterConfig struct {
	// 集群中所有节点的列表（包含当前节点自身）
	Nodes []clusterNodeConfig `json:"nodes"`
	// 当前集群节点的名称
	ThisName string `json:"self"`
	// 已废弃：此字段不再使用
	NumProxyEventGoRoutines int `json:"-"`
	// 故障转移 (Failover) 配置
	Failover *clusterFailoverConfig
}

// ClusterNode 表示客户端与其他节点建立的 RPC 连接对象。
type ClusterNode struct {
	lock sync.Mutex

	// RPC 端点
	endpoint *rpc.Client
	// 端点是否处于已连接状态
	connected atomic.Bool
	// 是否有后台协程正在尝试重新连接该节点
	reconnecting bool
	// TCP 地址格式 host:port
	address string
	// 节点名称
	name string
	// 节点指纹：节点每次重启时变化的随机唯一标识
	fingerprint int64

	// 该节点连续失败的次数
	failCount int

	// 终止节点的管道；带缓冲，大小为 1
	done chan bool

	// 属于该节点的代理多路复用会话 (Multiplexing Session) ID 集合
	msess map[string]struct{}

	// 接收该节点发起的 RPC 调用响应的默认管道
	// 带缓冲，大小为 clusterRpcCompletionBuffer * 节点数
	rpcDone chan *rpc.Call

	// 发送 Proxy to Master 请求的管道；带缓冲，大小为 clusterProxyToMasterBuffer
	p2mSender chan *ClusterReq
}

func (n *ClusterNode) asyncRpcLoop() {
	for call := range n.rpcDone {
		n.handleRpcResponse(call)
	}
}

func (n *ClusterNode) p2mSenderLoop() {
	for req := range n.p2mSender {
		if req == nil {
			// 退出循环
			return
		}

		if err := n.proxyToMaster(req); err != nil {
			logs.Warn.Println("p2mSenderLoop: 调用失败", n.name, err)
		}
	}
}

// ClusterSess 包含创建消息的远端 Session 的基础信息。
type ClusterSess struct {
	// 客户端 IP 地址。长轮询模式下为上次轮询的 IP
	RemoteAddr string

	// 用户 User-Agent（认证客户端在 {login} 包中提供的标识）
	UserAgent string

	// 当前用户 ID (Uid)，未认证为 0
	Uid types.Uid

	// 用户的身份认证级别
	AuthLvl auth.Level

	// 客户端协议版本号: ((major & 0xff) << 8) | (minor & 0xff)
	Ver int

	// 客户端语言
	Lang string
	// 客户端国家代码
	CountryCode string

	// 设备 ID
	DeviceID string

	// 设备平台类型: "web", "ios", "android"
	Platform string

	// 会话 ID (Sid)
	Sid string

	// 是否为后台会话
	Background bool
}

// ClusterSessUpdate 表示更新 Session 的请求结构。
// 例如 User-Agent 变更或后台 Session 切换至前台。
type ClusterSessUpdate struct {
	// 会话代表的用户 Uid
	Uid types.Uid
	// 会话 Sid
	Sid string
	// 会话 User-Agent
	UserAgent string
}

// ClusterReq 表示 Proxy-to-Master、TopicProxy-to-TopicMaster 或集群内部路由请求消息。
type ClusterReq struct {
	// 发送此请求的节点名称
	Node string

	// 发送此请求节点的 RingHash 签名。
	// 签名必须与接收方一致，否则说明集群处于未同步状态。
	Signature string

	// 发送此请求节点的指纹。节点重启时变化。
	Fingerprint int64

	// 请求类型
	ReqType ProxyReqType

	// 客户端消息。在 C2S 请求中设置
	CliMsg *ClientComMessage
	// 待路由的服务端消息。在集群内部路由请求中设置
	SrvMsg *ServerComMessage

	// 展开后的目标 Topic 名称
	RcptTo string
	// 源 Session 信息
	Sess *ClusterSess
	// 当 Topic Proxy 已销毁时设为 true
	Gone bool
}

// ClusterRoute 表示集群内部的路由请求消息。
type ClusterRoute struct {
	// 发送此请求的节点名称
	Node string

	// 发送此请求节点的 RingHash 签名。
	// 签名必须与接收方一致，否则说明集群处于未同步状态。
	Signature string

	// 发送此请求节点的指纹。节点重启时变化。
	Fingerprint int64

	// 待路由的服务端消息。在集群内部路由请求中设置
	SrvMsg *ServerComMessage

	// 源 Session 信息
	Sess *ClusterSess
}

// ClusterResp 表示从 Master 发往 Proxy 的响应消息。
type ClusterResp struct {
	// 包含响应的服务端消息
	SrvMsg *ServerComMessage
	// 需将响应转发至的源 Session ID
	OrigSid string
	// 展开后的目标 Topic 名称
	RcptTo string

	// Topic Master 响应 Topic Proxy 请求时返回的参数。

	// 原始请求类型
	OrigReqType ProxyReqType
}

// ClusterPing 用于检测集群节点是否发生重启。
type ClusterPing struct {
	// 发送此请求的节点名称
	Node string

	// 发送此请求节点的指纹（重启后变更）
	Fingerprint int64
}

// Handle 出站节点通信：重联及维护从 Channel 读取消息并转发到远程节点。
func (n *ClusterNode) reconnect() {
	var reconnTicker *time.Ticker

	// 避免并行重连线程
	n.lock.Lock()
	if n.reconnecting {
		n.lock.Unlock()
		return
	}
	n.reconnecting = true
	n.lock.Unlock()

	count := 0
	for {
		// 立即尝试重连
		if conn, err := net.DialTimeout("tcp", n.address, clusterNetworkTimeout); err == nil {
			if reconnTicker != nil {
				reconnTicker.Stop()
			}
			n.lock.Lock()
			n.endpoint = rpc.NewClient(conn)
			n.connected.Store(true)
			n.reconnecting = false
			n.lock.Unlock()
			statsInc("LiveClusterNodes", 1)
			logs.Info.Println("cluster: connected to", n.name)

			// 重连成功：清空断连期间在出站队列中积压的过时/失效请求，防止数据污染与风暴
			drained := 0
			for len(n.p2mSender) > 0 {
				select {
				case <-n.p2mSender:
					drained++
				default:
				}
			}
			if drained > 0 {
				logs.Info.Printf("cluster: drained %d stale outbound messages for node %s", drained, n.name)
			}

			// 向新节点发送本节点的凭证
			var unused bool
			n.call("Cluster.Ping",
				&ClusterPing{
					Node:        globals.cluster.thisNodeName,
					Fingerprint: globals.cluster.fingerprint,
				},
				&unused)
			return
		} else if count == 0 {
			reconnTicker = time.NewTicker(clusterDefaultReconnectTime)
		}

		count++

		select {
		case <-reconnTicker.C:
			// 等待定时器以重试重连。定时器未激活时不执行任何操作
		case <-n.done:
			// 正在关闭
			logs.Info.Println("cluster: shutdown started at node", n.name)
			reconnTicker.Stop()
			if n.endpoint != nil {
				n.endpoint.Close()
			}
			n.lock.Lock()
			n.connected.Store(false)
			n.reconnecting = false
			n.lock.Unlock()
			logs.Info.Println("cluster: shut down completed at node", n.name)
			return
		}
	}
}

func (n *ClusterNode) call(proc string, req, resp any) error {
	if !n.connected.Load() {
		return errors.New("cluster: node '" + n.name + "' not connected")
	}

	if err := n.endpoint.Call(proc, req, resp); err != nil {
		logs.Warn.Println("cluster: call failed", n.name, err)

		n.lock.Lock()
		if n.connected.Load() {
			n.endpoint.Close()
			n.connected.Store(false)
			statsInc("LiveClusterNodes", -1)
			go n.reconnect()
		}
		n.lock.Unlock()
		return err
	}

	return nil
}

func (n *ClusterNode) handleRpcResponse(call *rpc.Call) {
	if call.Error != nil {
		logs.Warn.Printf("cluster: %s call failed: %s", call.ServiceMethod, call.Error)
		n.lock.Lock()
		if n.connected.Load() {
			n.endpoint.Close()
			n.connected.Store(false)
			statsInc("LiveClusterNodes", -1)
			go n.reconnect()
		}
		n.lock.Unlock()
	}
}

func (n *ClusterNode) callAsync(proc string, req, resp any, done chan *rpc.Call) *rpc.Call {
	if done != nil && cap(done) == 0 {
		logs.Err.Panic("cluster: RPC done channel is unbuffered")
	}

	if !n.connected.Load() {
		call := &rpc.Call{
			ServiceMethod: proc,
			Args:          req,
			Reply:         resp,
			Error:         errors.New("cluster: node '" + n.name + "' not connected"),
			Done:          done,
		}
		if done != nil {
			done <- call
		}
		return call
	}

	var responseChan chan *rpc.Call
	if done != nil {
		// 如果需要通知调用方，创建单独的响应回调
		myDone := make(chan *rpc.Call, 1)
		go func() {
			call := <-myDone
			n.handleRpcResponse(call)
			if done != nil {
				done <- call
			}
		}()
		responseChan = myDone
	} else {
		responseChan = n.rpcDone
	}

	call := n.endpoint.Go(proc, req, resp, responseChan)

	return call
}

// proxyToMaster 将请求从 Topic 代理转发到 Topic 主节点
func (n *ClusterNode) proxyToMaster(msg *ClusterReq) error {
	msg.Node = globals.cluster.thisNodeName
	var rejected bool
	err := n.call("Cluster.TopicMaster", msg, &rejected)
	if err == nil && rejected {
		err = errors.New("cluster: topic master node out of sync")
	}
	return err
}

// proxyToMaster 将请求从 Topic 代理转发到 Topic 主节点
func (n *ClusterNode) proxyToMasterAsync(msg *ClusterReq) error {
	select {
	case n.p2mSender <- msg:
		return nil
	default:
	}
	// 缓冲已满。短暂等待后放弃
	timer := time.NewTimer(clusterP2MTimeout)
	defer timer.Stop()
	select {
	case n.p2mSender <- msg:
		return nil
	case <-timer.C:
		return errors.New("cluster: load exceeded")
	}
}

// masterToProxyAsync 以即发即忘的方式将响应从 Topic 主节点转发到 Topic 代理
func (n *ClusterNode) masterToProxyAsync(msg *ClusterResp) error {
	var unused bool
	if c := n.callAsync("Cluster.TopicProxy", msg, &unused, nil); c.Error != nil {
		return c.Error
	}
	return nil
}

// route 在集群内路由服务器消息
func (n *ClusterNode) route(msg *ClusterRoute) error {
	var unused bool
	return n.call("Cluster.Route", msg, &unused)
}

// Cluster 表示集群
type Cluster struct {
	// 集群节点及其 RPC 端点（不包含当前节点）
	nodes map[string]*ClusterNode
	// 本地节点名称
	thisNodeName string
	// 本地节点的指纹标识
	fingerprint int64

	// 监听解析后的地址
	listenOn string

	// 用于接收入站连接的 Socket
	inbound *net.TCPListener
	// 用于将 Topic 名称映射至节点的一致性哈希环 (RingHash)
	ring *rh.Ring

	// 故障转移 (Failover) 参数。若未开启故障转移则为 nil
	fo *clusterFailover

	// 用于运行代理会话写事件处理逻辑的协程池。
	// 代理会话的数量随着 (Topic 数量 x 集群节点数) 呈 O(N*M) 增长。
	// 在大型部署场景下（数万 Topic、数十节点），为每个代理会话开启独立协程会导致巨大的内存消耗与上下文切换开销。
	proxyEventQueue *concurrency.GoRoutinePool
}

func (n *ClusterNode) stopMultiplexingSession(msess *Session) {
	if msess == nil {
		return
	}
	msess.stopSession(nil)
	n.lock.Lock()
	delete(n.msess, msess.sid)
	n.lock.Unlock()
}

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

	if msg.Signature != c.ring.Signature() {
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
		}

	case ProxyReqBgSession, ProxyReqMeUserAgent:
		// sess could be nil
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
func (Cluster) TopicProxy(msg *ClusterResp, unused *bool) error {
	// 本集群成员收到来自 Topic 主节点的响应，需转发给 Topic
	// 找到对应的 Topic，将消息发送给它
	if t := globals.hub.topicGet(msg.RcptTo); t != nil {
		msg.SrvMsg.uid = types.ParseUserId(msg.SrvMsg.AsUser)
		select {
		case t.proxy <- msg:
		default:
			logs.Warn.Printf("cluster: proxy channel full, topic %s", msg.RcptTo)
		}
	} else {
		logs.Warn.Println("cluster: master response for unknown topic", msg.RcptTo)
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
	if msg.Signature != c.ring.Signature() {
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

// 根据 Topic 名称，找到合适的集群节点来路由消息
func (c *Cluster) nodeForTopic(topic string) *ClusterNode {
	key := c.ring.Get(topic)
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
	return c.ring.Get(topic) != c.thisNodeName
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
		if c.ring.Get(topic) == c.thisNodeName {
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
	if c == nil || c.fo == nil {
		// 集群未初始化或故障转移未启用，因此未分区
		return false
	}

	c.fo.activeNodesLock.RLock()
	result := (len(c.nodes)+1)/2 >= len(c.fo.activeNodes)
	c.fo.activeNodesLock.RUnlock()

	return result
}

func (c *Cluster) makeClusterReq(reqType ProxyReqType, msg *ClientComMessage, topic string, sess *Session) *ClusterReq {
	req := &ClusterReq{
		Node:        c.thisNodeName,
		Signature:   c.ring.Signature(),
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
		Signature:   c.ring.Signature(),
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

// 返回 snowflake worker id
func clusterInit(configString json.RawMessage, self *string) int {
	if globals.cluster != nil {
		logs.Err.Fatal("Cluster already initialized.")
	}

	// 即使是独立服务器也要注册这些变量。否则监控软件会
	// 报告变量缺失

	// 如果本节点是集群 leader 则为 1，否则为 0
	statsRegisterInt("ClusterLeader")
	// 配置的节点总数
	statsRegisterInt("TotalClusterNodes")
	// 当前认为存活的节点数
	statsRegisterInt("LiveClusterNodes")

	// 这是独立服务器，不进行初始化
	if len(configString) == 0 {
		logs.Info.Println("Cluster: running as a standalone server.")
		return 1
	}

	var config clusterConfig
	if err := json.Unmarshal(configString, &config); err != nil {
		logs.Err.Fatal(err)
	}

	thisName := *self
	if thisName == "" {
		thisName = config.ThisName
	}

	// 未指定当前节点名称：集群功能已禁用
	if thisName == "" {
		logs.Info.Println("Cluster: running as a standalone server.")
		return 1
	}

	gob.Register([]any{})
	gob.Register(map[string]any{})
	gob.Register(map[string]int{})
	gob.Register(map[string]string{})
	gob.Register(MsgAccessMode{})

	if config.NumProxyEventGoRoutines != 0 {
		logs.Warn.Println("Cluster config: field num_proxy_event_goroutines is deprecated.")
	}

	globals.cluster = &Cluster{
		thisNodeName:    thisName,
		fingerprint:     time.Now().Unix(),
		nodes:           make(map[string]*ClusterNode),
		proxyEventQueue: concurrency.NewGoRoutinePool(len(config.Nodes) * 5),
	}

	var nodeNames []string
	for _, host := range config.Nodes {
		nodeNames = append(nodeNames, host.Name)

		if host.Name == thisName {
			globals.cluster.listenOn = host.Addr
			// 不为本地实例创建集群成员
			continue
		}

		globals.cluster.nodes[host.Name] = &ClusterNode{
			address: host.Addr,
			name:    host.Name,
			done:    make(chan bool, 1),
			msess:   make(map[string]struct{}),
		}
	}

	if len(globals.cluster.nodes) == 0 {
		// 集群至少需要两个节点
		logs.Err.Fatal("Cluster: invalid cluster size: 1")
	}

	if len(globals.cluster.nodes)%2 == 1 {
		// 偶数个节点（自身 + 奇数个）
		logs.Warn.Println("Cluster: use odd number of cluster nodes")
	}

	if !globals.cluster.failoverInit(config.Failover) {
		globals.cluster.rehash(nil)
	}

	sort.Strings(nodeNames)
	workerId := sort.SearchStrings(nodeNames, thisName) + 1

	statsSet("TotalClusterNodes", int64(len(globals.cluster.nodes)+1))

	return workerId
}

// 主节点上的代理 Session 正在关闭
func (sess *Session) closeRPC() {
	if sess.isMultiplex() {
		logs.Info.Println("cluster: session proxy closed", sess.sid)
	}
}

// 开始接受连接
func (c *Cluster) start() {
	addr, err := net.ResolveTCPAddr("tcp", c.listenOn)
	if err != nil {
		logs.Err.Fatal(err)
	}

	c.inbound, err = net.ListenTCP("tcp", addr)

	if err != nil {
		logs.Err.Fatal(err)
	}

	var bufferSize = clusterProxyToMasterBuffer
	if len(c.nodes) > 2 {
		// 为大型 (>3 节点) 集群扩展缓冲区
		bufferSize += clusterProxyToMasterBufferPerNode * (len(c.nodes) - 2)
	}
	for _, n := range c.nodes {
		go n.reconnect()
		n.rpcDone = make(chan *rpc.Call, len(c.nodes)*clusterRpcCompletionBuffer)
		n.p2mSender = make(chan *ClusterReq, bufferSize)
		go n.asyncRpcLoop()
		go n.p2mSenderLoop()
	}

	if c.fo != nil {
		go c.run()
	}

	err = rpc.Register(c)
	if err != nil {
		logs.Err.Fatal(err)
	}

	go rpc.Accept(c.inbound)

	logs.Info.Printf("Cluster of %d nodes initialized, node '%s' is listening on [%s]", len(globals.cluster.nodes)+1,
		globals.cluster.thisNodeName, c.listenOn)
}

func (c *Cluster) shutdown() {
	if globals.cluster == nil {
		return
	}
	for _, n := range c.nodes {
		close(n.rpcDone)
		close(n.p2mSender)
	}

	globals.cluster.proxyEventQueue.Stop()
	globals.cluster = nil

	c.inbound.Close()

	if c.fo != nil {
		c.fo.done <- true
	}

	for _, n := range c.nodes {
		n.done <- true
	}

	logs.Info.Println("Cluster shut down")
}

// rehash 使用提供的节点列表或仅使用未故障状态的节点重新计算 ring hash
// 返回用于 ring hash 的节点列表
func (c *Cluster) rehash(nodes []string) []string {
	ring := rh.New(clusterHashReplicas, nil)

	var ringKeys []string

	if nodes == nil {
		for _, node := range c.nodes {
			ringKeys = append(ringKeys, node.name)
		}
		ringKeys = append(ringKeys, c.thisNodeName)
	} else {
		ringKeys = append(ringKeys, nodes...)
	}
	ring.Add(ringKeys...)

	c.ring = ring

	return ringKeys
}

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
		newMaster := c.ring.Get(topic.name)
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
	allNodes := []string{c.thisNodeName}
	for name := range c.nodes {
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
		if terminate {
			sess.closeRPC()
			globals.sessionStore.Delete(sess)
			sess.inflightReqs = nil
			sess.unsubAll()
		}
	}()

	for {
		select {
		case msg, ok := <-sess.send:
			if !ok || sess.clnode.endpoint == nil {
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

			if err := sess.clnode.masterToProxyAsync(response); err != nil {
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
