// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"net"
	"net/rpc"
	"sync"
	"sync/atomic"
	"time"

	"chat/server/auth"
	"chat/server/concurrency"
	rh "chat/server/ringhash"
	"chat/server/store/types"
)

const (
	// clusterProtocolVersion 是节点间控制面和数据面协议的当前版本。
	// 版本 3 使用强类型 Protobuf Lane 负载和多请求流水线；版本 2 的
	// Lane payload 使用 Gob，线格式不兼容，不能混合集群运行。
	clusterProtocolVersion = 3
	// clusterMinProtocolVersion 是滚动升级期间仍可协商的最早协议版本。
	// 当前数据面没有 v2/v3 双栈解码，因此升级时必须先停止旧节点。
	clusterMinProtocolVersion = 3
	// 网络连接超时时间。
	clusterNetworkTimeout = 3 * time.Second
	// 重连集群节点的默认等待时间。
	clusterDefaultReconnectTime = 200 * time.Millisecond
	// 一致性哈希环 (RingHash) 中的虚拟节点副本数。256 个虚拟节点在
	// 3/5 节点生产拓扑下显著降低 Topic Owner 倾斜，同时环本身仍很小。
	// 修改此值会改变 Owner 映射，必须同步提升集群协议版本。
	clusterHashReplicas = 256
	// 代理节点 (Proxy) 向主节点 (Master) 发送请求的缓冲队列大小。
	clusterProxyToMasterBuffer = 64
	// 在基础 3 节点集群配置之上，每增加一个节点扩展的缓冲区大小。
	clusterProxyToMasterBufferPerNode = 16
	// 当缓冲队列已满时，尝试入队 Proxy-to-Master 请求的超时时间。
	clusterP2MTimeout = 20 * time.Millisecond
	// 每个节点接收来自其他节点 RPC 响应的缓冲队列大小。
	clusterRpcCompletionBuffer = 64
)

var (
	// clusterStatsOnce 保证同一进程只发布一次集群指标；这也允许测试重复
	// 创建显式单机运行时而不会重复注册 expvar。
	clusterStatsOnce sync.Once
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

// clusterNodeConfig 保存集群节点配置的数据和运行状态。
type clusterNodeConfig struct {
	// Name 保存名称。
	Name string `json:"name"`
	// Addr 保存Addr。
	Addr string `json:"addr"`
}

// clusterConfig 保存集群配置的数据和运行状态。
type clusterConfig struct {
	// ClusterID 标识节点所属的逻辑集群，防止不同环境的节点混用。
	ClusterID string `json:"cluster_id"`
	// ExpectedReplicas 是首次启动时进入活动拓扑的 IM 节点总数。
	ExpectedReplicas int `json:"expected_replicas"`
	// InitialMembers 是首次创建 etcd 拓扑时启用的节点名称。
	// Nodes 可以预先声明更多候选节点，以支持无需重启现有节点的 3→5 扩容。
	InitialMembers []string `json:"initial_members"`
	// AdvertiseAddr 是其他节点连接当前节点时使用的广播地址。
	AdvertiseAddr string `json:"advertise_addr"`
	// ControlPlane 保存 etcd 成员租约和集群视图配置。
	ControlPlane *clusterControlPlaneConfig `json:"control_plane"`
	// Transport 保存节点间 gRPC 有序 Lane 配置；为空时仅保留开发版 net/rpc 兼容路径。
	Transport *clusterTransportConfig `json:"transport"`
	// TLS 保存节点间双向 TLS 证书配置。
	TLS *clusterTLSConfig `json:"tls"`
	// 集群中所有节点的列表（包含当前节点自身）
	Nodes []clusterNodeConfig `json:"nodes"`
	// 当前集群节点的名称
	ThisName string `json:"self"`
	// 已废弃：此字段不再使用
	NumProxyEventGoRoutines int `json:"-"`
	// 故障转移 (Failover) 配置
	Failover *clusterFailoverConfig `json:"failover"`
}

// ClusterNode 表示客户端与其他节点建立的 RPC 连接对象。
type ClusterNode struct {
	// lock 保护集群节点的并发读写。
	lock sync.Mutex

	// RPC 端点
	endpoint *rpc.Client
	// grpcPeer 非空时使用多 Lane gRPC 数据面，不再走 net/rpc endpoint。
	grpcPeer *clusterGRPCPeer
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
	// asyncCalls 等待通过 gRPC 兼容 callAsync 发起的后台请求退出。
	asyncCalls sync.WaitGroup
	// workers 等待响应消费和 Proxy-to-Master 发送协程退出。
	workers sync.WaitGroup
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

// Cluster 表示集群
type Cluster struct {
	// clusterID 标识当前节点所属的逻辑集群。
	clusterID string
	// expectedReplicas 是首次启动时进入活动拓扑的 IM 节点总数。
	expectedReplicas int
	// initialMembers 保存首次创建 etcd 拓扑时启用的节点名称。
	initialMembers []string
	// 集群节点及其 RPC 端点（不包含当前节点）
	nodes map[string]*ClusterNode
	// 本地节点名称
	thisNodeName string
	// 本地节点的指纹标识
	fingerprint int64

	// 监听解析后的地址
	listenOn string
	// advertiseAddr 是写入控制面的节点间广播地址。
	advertiseAddr string

	// 用于接收入站连接的 Socket
	inbound *net.TCPListener
	// ring 原子保存用于将 Topic 映射到节点的不可变一致性哈希环。
	ring atomic.Pointer[rh.Ring]

	// controlPlaneConfig 保存尚未连接的 etcd 控制面配置。
	controlPlaneConfig *clusterControlPlaneConfig
	// controlPlane 维护节点租约和单调递增的 Cluster View。
	controlPlane clusterControlPlane
	// viewEpoch 保存已经同时提交到数据库 fence 和本地 Ring 的 Cluster View Revision。
	viewEpoch atomic.Int64
	// grpcTransport 是启用 transport 配置后使用的节点间有序 Lane 数据面。
	grpcTransport *clusterGRPCTransport
	// transportConfig 保存尚未标准化的 gRPC Lane 配置。
	transportConfig *clusterTransportConfig
	// tlsConfig 保存尚未加载的节点间双向 TLS 文件配置。
	tlsConfig *clusterTLSConfig
	// tlsMaterial 保存运行时 CA 和可热加载的节点证书。
	tlsMaterial *clusterTLSMaterial

	// 故障转移 (Failover) 参数。若未开启故障转移则为 nil
	fo *clusterFailover

	// 用于运行代理会话写事件处理逻辑的协程池。
	// 代理会话的数量随着 (Topic 数量 x 集群节点数) 呈 O(N*M) 增长。
	// 在大型部署场景下（数万 Topic、数十节点），为每个代理会话开启独立协程会导致巨大的内存消耗与上下文切换开销。
	proxyEventQueue *concurrency.GoRoutinePool
}
