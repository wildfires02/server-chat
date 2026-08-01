package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"chat/api/pbx"
	"chat/server/logs"

	"google.golang.org/grpc"
)

var (
	// errClusterLaneQueueFull 表示可靠 Lane 已达到显式背压上限。
	errClusterLaneQueueFull = errors.New("cluster: reliable lane queue full")
	// errClusterLaneClosed 表示集群数据面正在关闭。
	errClusterLaneClosed = errors.New("cluster: gRPC lane closed")
)

// clusterGRPCTransport 管理本节点的 Lane 服务端以及到所有远端节点的客户端。
type clusterGRPCTransport struct {
	// cluster 是业务调用最终落到的本地集群实例。
	cluster *Cluster
	// config 是已经校验并补齐默认值的运行时参数。
	config normalizedClusterTransport
	// listener 只接收节点间 gRPC 数据面连接。
	listener net.Listener
	// server 承载 ClusterTransport 服务。
	server *grpc.Server
	// peers 保存按静态节点名称索引的出站连接。
	peers map[string]*clusterGRPCPeer
	// closeOnce 保证关闭过程幂等。
	closeOnce sync.Once
}

// clusterGRPCPeer 管理到单个远端节点的多个独立有序 Lane。
type clusterGRPCPeer struct {
	// cluster 保存本地节点身份和当前 Cluster View。
	cluster *Cluster
	// node 是现有路由层中的远端节点对象。
	node *ClusterNode
	// config 保存 Lane 数量、队列和超时。
	config normalizedClusterTransport
	// connection 是所有 Lane 复用的 HTTP/2 客户端连接。
	connection *grpc.ClientConn
	// client 是生成的 ClusterTransport 客户端。
	client pbx.ClusterTransportClient
	// lanes 按确定性哈希结果索引。
	lanes []*clusterGRPCLane
	// context 控制所有 Lane 的生命周期。
	context context.Context
	// cancel 关闭所有 Lane。
	cancel context.CancelFunc
	// activeLanes 保存当前至少完成过一次发送的可用 Lane 数。
	activeLanes atomic.Int32
	// requestCounter 生成进程内单调请求编号。
	requestCounter atomic.Uint64
	// waitGroup 等待所有 Lane 发送协程退出。
	waitGroup sync.WaitGroup
	// closeOnce 保证连接只关闭一次。
	closeOnce sync.Once
}

// clusterGRPCLane 使用单发送协程和单接收协程维护有界请求流水线。
// 多个 Lane 提供跨 Topic 并行度，同一 Topic 始终落入同一 Lane。
type clusterGRPCLane struct {
	// peer 是 Lane 所属的远端节点连接。
	peer *clusterGRPCPeer
	// index 是协议帧中的 Lane 编号。
	index uint32
	// sequence 仅由 run 协程递增，无需额外互斥锁。
	sequence uint64
	// active 表示该 Lane 当前持有可发送的流。
	active atomic.Bool
	// inFlight 表示已经出队但尚未获得最终响应的请求数。
	inFlight atomic.Int32
	// reliableQueue 保存必须明确成功或失败的业务请求。
	reliableQueue chan *clusterLaneRequest
	// ephemeralQueue 保存允许在过载时丢弃的输入状态和 Presence。
	ephemeralQueue chan *clusterLaneRequest
	// stream 是长期复用的双向 gRPC 流。
	stream pbx.ClusterTransport_LaneClient
	// cancelStream 在超时或网络错误时中断阻塞的 Send/Recv。
	cancelStream context.CancelFunc
}

// clusterLaneRequest 保存进入有序 Lane 的一次内部 RPC。
type clusterLaneRequest struct {
	// context 控制排队、发送和等待响应的总超时。
	context context.Context
	// requestID 是响应关联和后续去重的稳定键。
	requestID string
	// kind 标识内部业务调用类型。
	kind pbx.ClusterFrameKind
	// payload 是强类型 Protobuf 业务负载。
	payload []byte
	// reliable 表示传输错误时需要有限重试。
	reliable bool
	// result 是容量为 1 的完成通知，不阻塞 Lane 退出。
	result chan clusterLaneResult
	// attempts 保存已经写入流或尝试写入流的次数。
	attempts int
	// started 表示该请求已经计入在途指标。
	started bool
}

// clusterLaneResult 保存远端响应负载或传输错误。
type clusterLaneResult struct {
	// payload 是远端业务响应。
	payload []byte
	// err 是传输、协议或远端业务错误。
	err error
}

type clusterLanePending struct {
	request *clusterLaneRequest
	frame   *pbx.ClusterFrame
}

type clusterLaneReceive struct {
	frame *pbx.ClusterFrame
	err   error
}

// clusterLaneServer 实现生成代码要求的双向流服务。
type clusterLaneServer struct {
	pbx.UnimplementedClusterTransportServer
	// cluster 是收到帧后调用的本地业务路由对象。
	cluster *Cluster
	// config 用于校验远端发送的 Lane 编号。
	config normalizedClusterTransport
	// dedupe 防止断流后使用同一 Request ID 重试时重复执行业务。
	dedupe *clusterDedupeCache
	// tlsRequired 表示每条入站流都必须携带可映射到节点名的客户端证书。
	tlsRequired bool
}

// clusterRemoteCallError 表示远端已经接收请求并返回的业务错误。
type clusterRemoteCallError struct {
	// message 是远端返回的可读错误信息。
	message string
}

// Error 实现 error 接口。
func (err *clusterRemoteCallError) Error() string {
	return err.message
}

// clusterProtocolError 表示响应身份、版本或关联字段违反 Lane 协议。
type clusterProtocolError struct {
	// message 是拒绝当前流的具体原因。
	message string
}

// Error 实现 error 接口。
func (err *clusterProtocolError) Error() string {
	return err.message
}

// newClusterGRPCTransport 创建尚未监听端口的数据面。
func newClusterGRPCTransport(
	cluster *Cluster,
	config normalizedClusterTransport,
) *clusterGRPCTransport {
	return &clusterGRPCTransport{
		cluster: cluster,
		config:  config,
		peers:   make(map[string]*clusterGRPCPeer, len(cluster.nodes)),
	}
}

// Start 启动入站 gRPC 服务，并为每个远端节点建立多 Lane 客户端。
func (transport *clusterGRPCTransport) Start() error {
	listenAddress := transport.config.Listen
	if listenAddress == "" {
		listenAddress = transport.cluster.listenOn
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("监听集群 gRPC Lane 地址 %q 失败: %w", listenAddress, err)
	}

	serverOptions := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(int(globals.maxMessageSize)),
	}
	if transport.cluster.tlsMaterial != nil {
		serverOptions = append(
			serverOptions,
			grpc.Creds(transport.cluster.tlsMaterial.serverCredentials()),
		)
	}
	server := grpc.NewServer(serverOptions...)
	pbx.RegisterClusterTransportServer(server, &clusterLaneServer{
		cluster: transport.cluster,
		config:  transport.config,
		dedupe: newClusterDedupeCache(
			transport.config.DedupeCapacity,
			transport.config.DedupeTTL,
		),
		tlsRequired: transport.cluster.tlsMaterial != nil,
	})
	transport.listener = listener
	transport.server = server
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil &&
			!errors.Is(serveErr, grpc.ErrServerStopped) {
			logs.Err.Printf("cluster: gRPC Lane 服务退出: %v", serveErr)
		}
	}()

	for name, node := range transport.cluster.nodes {
		peer, peerErr := newClusterGRPCPeer(transport.cluster, node, transport.config)
		if peerErr != nil {
			transport.Close()
			return fmt.Errorf("初始化节点 %q 的 gRPC Lane 失败: %w", name, peerErr)
		}
		transport.peers[name] = peer
		node.grpcPeer = peer
		peer.Start()
	}
	logs.Info.Printf(
		"cluster: gRPC Lane 数据面监听 [%s]，每个远端节点 %d 条 Lane",
		listenAddress,
		transport.config.LaneCount,
	)
	return nil
}

// Close 停止所有出站 Lane 和入站 gRPC 服务。
func (transport *clusterGRPCTransport) Close() {
	if transport == nil {
		return
	}
	transport.closeOnce.Do(func() {
		for _, peer := range transport.peers {
			peer.Close()
		}
		if transport.server != nil {
			transport.server.Stop()
		}
		if transport.listener != nil {
			_ = transport.listener.Close()
		}
	})
}
