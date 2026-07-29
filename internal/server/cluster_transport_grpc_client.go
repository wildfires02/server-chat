package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"chat/api/pbx"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// newClusterGRPCPeer 创建到一个远端节点的共享 ClientConn 和有序 Lane。
func newClusterGRPCPeer(
	cluster *Cluster,
	node *ClusterNode,
	config normalizedClusterTransport,
) (*clusterGRPCPeer, error) {
	transportCredentials := credentials.TransportCredentials(insecure.NewCredentials())
	if cluster.tlsMaterial != nil {
		transportCredentials = cluster.tlsMaterial.clientCredentials(node.name)
	}
	return newClusterGRPCPeerWithDialOptions(
		cluster,
		node,
		config,
		grpc.WithTransportCredentials(transportCredentials),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  clusterDefaultReconnectTime,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   clusterNetworkTimeout,
			},
			MinConnectTimeout: config.DialTimeout,
		}),
	)
}

// newClusterGRPCPeerWithDialOptions 创建可注入连接器的 Peer，供生产连接和 bufconn 测试共用。
func newClusterGRPCPeerWithDialOptions(
	cluster *Cluster,
	node *ClusterNode,
	config normalizedClusterTransport,
	options ...grpc.DialOption,
) (*clusterGRPCPeer, error) {
	connection, err := grpc.NewClient(node.address, options...)
	if err != nil {
		return nil, err
	}
	peerContext, cancel := context.WithCancel(context.Background())
	peer := &clusterGRPCPeer{
		cluster:    cluster,
		node:       node,
		config:     config,
		connection: connection,
		client:     pbx.NewClusterTransportClient(connection),
		context:    peerContext,
		cancel:     cancel,
		lanes:      make([]*clusterGRPCLane, config.LaneCount),
	}
	for index := range config.LaneCount {
		peer.lanes[index] = &clusterGRPCLane{
			peer:           peer,
			index:          uint32(index),
			reliableQueue:  make(chan *clusterLaneRequest, config.ReliableQueueCapacity),
			ephemeralQueue: make(chan *clusterLaneRequest, config.EphemeralQueueCapacity),
		}
	}
	return peer, nil
}

// Start 启动当前远端节点的所有 Lane 发送协程。
func (peer *clusterGRPCPeer) Start() {
	for _, lane := range peer.lanes {
		peer.waitGroup.Add(1)
		go lane.run()
	}
}

// Close 取消所有请求、等待 Lane 退出并关闭 ClientConn。
func (peer *clusterGRPCPeer) Close() {
	if peer == nil {
		return
	}
	peer.closeOnce.Do(func() {
		peer.cancel()
		peer.waitGroup.Wait()
		_ = peer.connection.Close()
		peer.node.connected.Store(false)
	})
}

// Call 编码一次内部 RPC，并在 Topic 对应的固定 Lane 中等待响应。
func (peer *clusterGRPCPeer) Call(proc string, request, response any) error {
	kind, err := clusterFrameKindForProcedure(proc)
	if err != nil {
		return err
	}
	payload, err := encodeClusterPayload(request)
	if err != nil {
		return err
	}
	requestNumber := peer.requestCounter.Add(1)
	requestID := fmt.Sprintf(
		"%s-%d-%d",
		peer.cluster.thisNodeName,
		peer.cluster.fingerprint,
		requestNumber,
	)
	laneIndex := clusterLaneIndex(clusterRoutingKey(request, requestID), len(peer.lanes))
	requestContext, cancel := context.WithTimeout(peer.context, peer.config.RequestTimeout)
	defer cancel()
	laneRequest := &clusterLaneRequest{
		context:   requestContext,
		requestID: requestID,
		kind:      kind,
		payload:   payload,
		reliable:  clusterRequestIsReliable(request),
		result:    make(chan clusterLaneResult, 1),
	}

	lane := peer.lanes[laneIndex]
	if laneRequest.reliable {
		statsInc("ClusterLaneQueued", 1)
		select {
		case lane.reliableQueue <- laneRequest:
		case <-requestContext.Done():
			statsInc("ClusterLaneQueued", -1)
			return fmt.Errorf("cluster: 请求进入可靠 Lane 超时: %w", requestContext.Err())
		default:
			statsInc("ClusterLaneQueued", -1)
			statsInc("ClusterLaneQueueRejected", 1)
			return errClusterLaneQueueFull
		}
	} else {
		statsInc("ClusterEphemeralQueued", 1)
		select {
		case lane.ephemeralQueue <- laneRequest:
		case <-requestContext.Done():
			statsInc("ClusterEphemeralQueued", -1)
			statsInc("ClusterEphemeralDropped", 1)
			return nil
		default:
			statsInc("ClusterEphemeralQueued", -1)
			statsInc("ClusterEphemeralDropped", 1)
			return nil
		}
	}

	select {
	case result := <-laneRequest.result:
		if result.err != nil {
			return result.err
		}
		if response == nil || len(result.payload) == 0 {
			return nil
		}
		return decodeClusterPayload(result.payload, response)
	case <-requestContext.Done():
		return fmt.Errorf("cluster: Lane 请求超时: %w", requestContext.Err())
	}
}

// run 串行发送当前 Lane 的请求；gRPC 流和此单协程共同保证顺序。
func (lane *clusterGRPCLane) run() {
	defer lane.peer.waitGroup.Done()
	defer lane.closeStream()
	for {
		select {
		case <-lane.peer.context.Done():
			lane.failQueued(errClusterLaneClosed)
			return
		default:
		}
		// 可靠请求优先，避免瞬态事件持续涌入时挤压消息写入。
		select {
		case request := <-lane.reliableQueue:
			lane.handleQueuedRequest(request, true)
			continue
		default:
		}
		select {
		case <-lane.peer.context.Done():
			lane.failQueued(errClusterLaneClosed)
			return
		case request := <-lane.reliableQueue:
			lane.handleQueuedRequest(request, true)
		case request := <-lane.ephemeralQueue:
			lane.handleQueuedRequest(request, false)
		}
	}
}

// handleQueuedRequest 更新队列指标并执行一个已出队请求。
func (lane *clusterGRPCLane) handleQueuedRequest(request *clusterLaneRequest, reliable bool) {
	if request == nil {
		return
	}
	if reliable {
		statsInc("ClusterLaneQueued", -1)
	} else {
		statsInc("ClusterEphemeralQueued", -1)
	}
	lane.inFlight.Add(1)
	defer lane.inFlight.Add(-1)
	result := lane.executeWithRetry(request)
	select {
	case request.result <- result:
	default:
	}
}

// pendingReliableRequests 返回所有 Peer 中排队或正在处理的可靠请求数。
func (transport *clusterGRPCTransport) pendingReliableRequests() int {
	if transport == nil {
		return 0
	}
	total := 0
	for _, peer := range transport.peers {
		for _, lane := range peer.lanes {
			total += len(lane.reliableQueue) + int(lane.inFlight.Load())
		}
	}
	return total
}

// maxReliableQueueUtilization 返回所有可靠 Lane 中最高的队列使用率。
func (transport *clusterGRPCTransport) maxReliableQueueUtilization() float64 {
	if transport == nil {
		return 0
	}
	var maximum float64
	for _, peer := range transport.peers {
		for _, lane := range peer.lanes {
			capacity := cap(lane.reliableQueue)
			if capacity == 0 {
				continue
			}
			utilization := float64(len(lane.reliableQueue)) / float64(capacity)
			if utilization > maximum {
				maximum = utilization
			}
		}
	}
	return maximum
}

// executeWithRetry 对可靠请求执行有限指数退避重试，并保持 Request ID 不变。
func (lane *clusterGRPCLane) executeWithRetry(request *clusterLaneRequest) clusterLaneResult {
	for attempt := 0; ; attempt++ {
		result := lane.execute(request)
		if result.err == nil ||
			!request.reliable ||
			attempt >= lane.peer.config.MaxRetries ||
			!isRetryableClusterTransportError(result.err) {
			return result
		}
		statsInc("ClusterLaneRetries", 1)
		delay := lane.peer.config.RetryBackoff * time.Duration(1<<attempt)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-request.context.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return clusterLaneResult{err: request.context.Err()}
		}
	}
}

// execute 在当前流中发送一个请求并读取其对应响应。
func (lane *clusterGRPCLane) execute(request *clusterLaneRequest) clusterLaneResult {
	if err := request.context.Err(); err != nil {
		return clusterLaneResult{err: err}
	}
	if err := lane.ensureStream(); err != nil {
		return clusterLaneResult{err: err}
	}

	lane.sequence++
	frame := &pbx.ClusterFrame{
		ClusterId:          lane.peer.cluster.clusterID,
		SourceNode:         lane.peer.cluster.thisNodeName,
		SourceInstance:     lane.peer.cluster.fingerprint,
		ProtocolVersion:    clusterProtocolVersion,
		MinProtocolVersion: clusterMinProtocolVersion,
		Lane:               lane.index,
		Sequence:           lane.sequence,
		RequestId:          request.requestID,
		Kind:               request.kind,
		Payload:            request.payload,
		ClusterEpoch:       lane.peer.cluster.viewEpoch.Load(),
		RingSignature:      lane.peer.cluster.ringSignature(),
	}
	stream := lane.stream
	sendResult := make(chan error, 1)
	go func() {
		sendResult <- stream.Send(frame)
	}()
	select {
	case err := <-sendResult:
		if err != nil {
			lane.closeStream()
			statsInc("ClusterLaneFailures", 1)
			return clusterLaneResult{err: fmt.Errorf("cluster: Lane 发送失败: %w", err)}
		}
		lane.markActive(true)
	case <-request.context.Done():
		lane.closeStream()
		statsInc("ClusterLaneFailures", 1)
		return clusterLaneResult{err: request.context.Err()}
	}

	receiveResult := make(chan clusterLaneResult, 1)
	go func() {
		response, err := stream.Recv()
		if err != nil {
			receiveResult <- clusterLaneResult{err: err}
			return
		}
		if validationErr := lane.validateResponse(response, frame); validationErr != nil {
			receiveResult <- clusterLaneResult{err: validationErr}
			return
		}
		if response.Error != "" {
			receiveResult <- clusterLaneResult{
				err: &clusterRemoteCallError{message: response.Error},
			}
			return
		}
		receiveResult <- clusterLaneResult{payload: response.Payload}
	}()
	select {
	case result := <-receiveResult:
		if result.err != nil {
			var remoteError *clusterRemoteCallError
			if errors.As(result.err, &remoteError) {
				statsInc("ClusterLaneRequests", 1)
				return result
			}
			lane.closeStream()
			statsInc("ClusterLaneFailures", 1)
			return clusterLaneResult{err: fmt.Errorf("cluster: Lane 接收失败: %w", result.err)}
		}
		statsInc("ClusterLaneRequests", 1)
		return result
	case <-request.context.Done():
		lane.closeStream()
		statsInc("ClusterLaneFailures", 1)
		return clusterLaneResult{err: request.context.Err()}
	}
}

// validateResponse 校验远端响应的身份、版本、Cluster View 和请求关联字段。
func (lane *clusterGRPCLane) validateResponse(
	response *pbx.ClusterFrame,
	request *pbx.ClusterFrame,
) error {
	if response == nil || !response.Response {
		return &clusterProtocolError{message: "cluster: 收到无效的 Lane 响应帧"}
	}
	if response.ClusterId != lane.peer.cluster.clusterID ||
		response.SourceNode != lane.peer.node.name ||
		response.SourceInstance <= 0 ||
		!clusterProtocolVersionsOverlap(
			response.MinProtocolVersion,
			response.ProtocolVersion,
		) {
		return &clusterProtocolError{message: "cluster: Lane 响应节点身份或协议版本不匹配"}
	}
	if response.RequestId != request.RequestId ||
		response.Sequence != request.Sequence ||
		response.Lane != request.Lane ||
		response.Kind != request.Kind {
		return &clusterProtocolError{message: "cluster: Lane 响应关联字段不匹配"}
	}
	if lane.peer.cluster.controlPlane != nil &&
		response.ClusterEpoch != lane.peer.cluster.viewEpoch.Load() {
		return &clusterProtocolError{message: "cluster: Lane 响应 Cluster View epoch 不匹配"}
	}
	if response.Kind != pbx.ClusterFrameKind_CLUSTER_FRAME_PING &&
		response.RingSignature != lane.peer.cluster.ringSignature() {
		return &clusterProtocolError{message: "cluster: Lane 响应 Ring 签名不匹配"}
	}
	return nil
}

// ensureStream 创建或复用当前 Lane 的双向流。
func (lane *clusterGRPCLane) ensureStream() error {
	if lane.stream != nil {
		return nil
	}
	streamContext, cancel := context.WithCancel(lane.peer.context)
	stream, err := lane.peer.client.Lane(streamContext, grpc.WaitForReady(true))
	if err != nil {
		cancel()
		return fmt.Errorf("cluster: 创建 Lane %d 失败: %w", lane.index, err)
	}
	lane.stream = stream
	lane.cancelStream = cancel
	// 每条新 gRPC 流都从 sequence=1 开始，服务端才能识别断线后的新会话。
	lane.sequence = 0
	return nil
}

// closeStream 中断阻塞 I/O 并更新连接统计。
func (lane *clusterGRPCLane) closeStream() {
	if lane.cancelStream != nil {
		lane.cancelStream()
	}
	lane.cancelStream = nil
	lane.stream = nil
	lane.markActive(false)
}

// markActive 维护 Lane 和远端节点的聚合连接状态。
func (lane *clusterGRPCLane) markActive(active bool) {
	if lane.active.Swap(active) == active {
		return
	}
	if active {
		statsInc("ClusterConnectedLanes", 1)
		if lane.peer.activeLanes.Add(1) == 1 {
			lane.peer.node.connected.Store(true)
			statsInc("LiveClusterNodes", 1)
		}
		return
	}
	statsInc("ClusterConnectedLanes", -1)
	if lane.peer.activeLanes.Add(-1) == 0 {
		lane.peer.node.connected.Store(false)
		statsInc("LiveClusterNodes", -1)
	}
}

// failQueued 在关闭时使所有已接收但未发送的请求明确失败。
func (lane *clusterGRPCLane) failQueued(err error) {
	lane.failQueue(lane.reliableQueue, "ClusterLaneQueued", err)
	lane.failQueue(lane.ephemeralQueue, "ClusterEphemeralQueued", err)
}

// failQueue 使指定队列中的所有等待请求明确结束。
func (lane *clusterGRPCLane) failQueue(
	queue chan *clusterLaneRequest,
	metric string,
	err error,
) {
	for {
		select {
		case request := <-queue:
			if request != nil {
				statsInc(metric, -1)
				select {
				case request.result <- clusterLaneResult{err: err}:
				default:
				}
			}
		default:
			return
		}
	}
}
