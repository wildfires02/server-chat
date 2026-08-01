package server

import (
	"context"
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

// run 在当前 Lane 上维护有界流水线；发送仍严格有序，响应通过 request_id 关联。
func (lane *clusterGRPCLane) run() {
	defer lane.peer.waitGroup.Done()
	defer lane.closeStream()
	var carry []*clusterLaneRequest
	for {
		if lane.peer.context.Err() != nil {
			lane.finishRequests(carry, errClusterLaneClosed)
			lane.failQueued(errClusterLaneClosed)
			return
		}
		if len(carry) == 0 {
			request := lane.waitForQueuedRequest()
			if request == nil {
				lane.failQueued(errClusterLaneClosed)
				return
			}
			carry = append(carry, request)
		}
		if err := lane.ensureStream(); err != nil {
			carry = lane.retryRequests(carry, err)
			lane.waitRetryBackoff(carry)
			continue
		}
		var streamErr error
		carry, streamErr = lane.runPipeline(carry)
		lane.closeStream()
		if streamErr != nil {
			carry = lane.retryRequests(carry, streamErr)
			lane.waitRetryBackoff(carry)
		}
	}
}

func (lane *clusterGRPCLane) waitForQueuedRequest() *clusterLaneRequest {
	select {
	case request := <-lane.reliableQueue:
		statsInc("ClusterLaneQueued", -1)
		return request
	default:
	}
	select {
	case <-lane.peer.context.Done():
		return nil
	case request := <-lane.reliableQueue:
		statsInc("ClusterLaneQueued", -1)
		return request
	case request := <-lane.ephemeralQueue:
		statsInc("ClusterEphemeralQueued", -1)
		return request
	}
}

func (lane *clusterGRPCLane) takeQueuedRequest() *clusterLaneRequest {
	select {
	case request := <-lane.reliableQueue:
		statsInc("ClusterLaneQueued", -1)
		return request
	default:
	}
	select {
	case request := <-lane.reliableQueue:
		statsInc("ClusterLaneQueued", -1)
		return request
	case request := <-lane.ephemeralQueue:
		statsInc("ClusterEphemeralQueued", -1)
		return request
	default:
		return nil
	}
}

func (lane *clusterGRPCLane) pipelineWindow() int {
	if lane.peer.config.PipelineWindow > 0 {
		return lane.peer.config.PipelineWindow
	}
	return defaultClusterPipelineWindow
}

func (lane *clusterGRPCLane) runPipeline(
	ready []*clusterLaneRequest,
) ([]*clusterLaneRequest, error) {
	stream := lane.stream
	receive := make(chan clusterLaneReceive, 1)
	go func() {
		for {
			frame, err := stream.Recv()
			result := clusterLaneReceive{frame: frame, err: err}
			if err != nil {
				select {
				case receive <- result:
				case <-lane.peer.context.Done():
				}
				return
			}
			select {
			case receive <- result:
			case <-stream.Context().Done():
				return
			case <-lane.peer.context.Done():
				return
			}
		}
	}()

	pending := make([]clusterLanePending, 0, lane.pipelineWindow())
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		for len(pending) < lane.pipelineWindow() {
			var request *clusterLaneRequest
			if len(ready) > 0 {
				request, ready = ready[0], ready[1:]
			} else {
				request = lane.takeQueuedRequest()
			}
			if request == nil {
				break
			}
			if err := request.context.Err(); err != nil {
				lane.finishRequest(request, err)
				continue
			}
			frame := lane.newRequestFrame(request)
			if !request.started {
				request.started = true
				lane.inFlight.Add(1)
			}
			request.attempts++
			pending = append(pending, clusterLanePending{request: request, frame: frame})
			if err := stream.Send(frame); err != nil {
				return append(lane.pendingRequests(pending), ready...),
					fmt.Errorf("cluster: Lane 发送失败: %w", err)
			}
			lane.markActive(true)
		}

		if len(pending) == 0 {
			request := lane.waitForQueuedRequest()
			if request == nil {
				return ready, errClusterLaneClosed
			}
			ready = append(ready, request)
			continue
		}

		select {
		case <-lane.peer.context.Done():
			return append(lane.pendingRequests(pending), ready...), errClusterLaneClosed
		case result := <-receive:
			if result.err != nil {
				statsInc("ClusterLaneFailures", 1)
				return append(lane.pendingRequests(pending), ready...),
					fmt.Errorf("cluster: Lane 接收失败: %w", result.err)
			}
			index := -1
			for i := range pending {
				if pending[i].request.requestID == result.frame.GetRequestId() {
					index = i
					break
				}
			}
			if index < 0 {
				return append(lane.pendingRequests(pending), ready...),
					&clusterProtocolError{message: "cluster: 收到未知 request_id 的 Lane 响应"}
			}
			current := pending[index]
			if err := lane.validateResponse(result.frame, current.frame); err != nil {
				lane.finishRequest(current.request, err)
				pending = append(pending[:index], pending[index+1:]...)
				return append(lane.pendingRequests(pending), ready...), err
			}
			var responseErr error
			if result.frame.Error != "" {
				responseErr = &clusterRemoteCallError{message: result.frame.Error}
			}
			lane.finishRequest(current.request, responseErr, result.frame.Payload...)
			pending = append(pending[:index], pending[index+1:]...)
			statsInc("ClusterLaneRequests", 1)
		case <-ticker.C:
			for _, current := range pending {
				if current.request.context.Err() != nil {
					return append(lane.pendingRequests(pending), ready...), current.request.context.Err()
				}
			}
		}
	}
}

func (lane *clusterGRPCLane) newRequestFrame(request *clusterLaneRequest) *pbx.ClusterFrame {
	lane.sequence++
	return &pbx.ClusterFrame{
		ClusterId: lane.peer.cluster.clusterID, SourceNode: lane.peer.cluster.thisNodeName,
		SourceInstance: lane.peer.cluster.fingerprint, ProtocolVersion: clusterProtocolVersion,
		MinProtocolVersion: clusterMinProtocolVersion, Lane: lane.index, Sequence: lane.sequence,
		RequestId: request.requestID, Kind: request.kind, Payload: request.payload,
		ClusterEpoch: lane.peer.cluster.viewEpoch.Load(), RingSignature: lane.peer.cluster.ringSignature(),
	}
}

func (lane *clusterGRPCLane) pendingRequests(pending []clusterLanePending) []*clusterLaneRequest {
	requests := make([]*clusterLaneRequest, 0, len(pending))
	for _, current := range pending {
		requests = append(requests, current.request)
	}
	return requests
}

func (lane *clusterGRPCLane) retryRequests(
	requests []*clusterLaneRequest,
	cause error,
) []*clusterLaneRequest {
	retry := make([]*clusterLaneRequest, 0, len(requests))
	for _, request := range requests {
		if request == nil {
			continue
		}
		if err := request.context.Err(); err != nil {
			lane.finishRequest(request, err)
			continue
		}
		if !request.started {
			retry = append(retry, request)
			continue
		}
		if request.reliable && request.attempts <= lane.peer.config.MaxRetries &&
			isRetryableClusterTransportError(cause) {
			statsInc("ClusterLaneRetries", 1)
			retry = append(retry, request)
			continue
		}
		if !request.reliable {
			statsInc("ClusterEphemeralDropped", 1)
			lane.finishRequest(request, nil)
			continue
		}
		lane.finishRequest(request, cause)
	}
	return retry
}

func (lane *clusterGRPCLane) waitRetryBackoff(requests []*clusterLaneRequest) {
	if len(requests) == 0 || lane.peer.config.RetryBackoff <= 0 {
		return
	}
	maximumAttempt := 1
	for _, request := range requests {
		if request != nil && request.attempts > maximumAttempt {
			maximumAttempt = request.attempts
		}
	}
	exponent := maximumAttempt - 1
	if exponent > lane.peer.config.MaxRetries {
		exponent = lane.peer.config.MaxRetries
	}
	timer := time.NewTimer(lane.peer.config.RetryBackoff * time.Duration(1<<exponent))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-lane.peer.context.Done():
	}
}

func (lane *clusterGRPCLane) finishRequest(
	request *clusterLaneRequest,
	err error,
	payload ...byte,
) {
	if request == nil {
		return
	}
	if request.started {
		request.started = false
		lane.inFlight.Add(-1)
	}
	result := clusterLaneResult{err: err}
	if len(payload) > 0 {
		result.payload = append([]byte(nil), payload...)
	}
	select {
	case request.result <- result:
	default:
	}
}

func (lane *clusterGRPCLane) finishRequests(requests []*clusterLaneRequest, err error) {
	for _, request := range requests {
		lane.finishRequest(request, err)
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
