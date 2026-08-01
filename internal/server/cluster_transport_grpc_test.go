package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"chat/api/pbx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// recordedClusterFrame 是测试服务端保存的最小协议观测值。
type recordedClusterFrame struct {
	// lane 是客户端选择的有序分片。
	lane uint32
	// sequence 是当前流内的单调序号。
	sequence uint64
	// requestID 是请求关联标识。
	requestID string
}

// recordingClusterLaneServer 记录真实 gRPC 流收到的请求并返回成功响应。
type recordingClusterLaneServer struct {
	pbx.UnimplementedClusterTransportServer

	// closeAfterResponse 用于模拟服务端在每个响应后主动断开流。
	closeAfterResponse bool
	// dropFirstResponse 用于模拟请求已到达但首次响应在网络中丢失。
	dropFirstResponse bool
	// lock 保护测试记录，允许多个 Lane 并发写入。
	lock sync.Mutex
	// frames 保存服务端实际收到的帧。
	frames []recordedClusterFrame
	// firstResponseDropped 确保整个测试只丢弃一次响应。
	firstResponseDropped bool
}

type pipelinedClusterLaneServer struct {
	pbx.UnimplementedClusterTransportServer
	expected int
}

func (server *pipelinedClusterLaneServer) Lane(stream pbx.ClusterTransport_LaneServer) error {
	frames := make([]*pbx.ClusterFrame, 0, server.expected)
	for len(frames) < server.expected {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		frames = append(frames, frame)
	}
	payload, err := encodeClusterPayload(false)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if err = stream.Send(&pbx.ClusterFrame{
			ClusterId: frame.ClusterId, SourceNode: "node-b", SourceInstance: 2,
			ProtocolVersion: clusterProtocolVersion, MinProtocolVersion: clusterMinProtocolVersion,
			Lane: frame.Lane, Sequence: frame.Sequence, RequestId: frame.RequestId,
			Kind: frame.Kind, Payload: payload, Response: true,
			ClusterEpoch: frame.ClusterEpoch, RingSignature: frame.RingSignature,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Lane 验证每条真实 gRPC 流从 1 开始严格递增，并回显关联字段。
func (server *recordingClusterLaneServer) Lane(stream pbx.ClusterTransport_LaneServer) error {
	var expectedSequence uint64 = 1
	for {
		frame, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if frame.Sequence != expectedSequence {
			return fmt.Errorf(
				"测试服务端收到 sequence=%d，期望 %d",
				frame.Sequence,
				expectedSequence,
			)
		}
		server.lock.Lock()
		server.frames = append(server.frames, recordedClusterFrame{
			lane:      frame.Lane,
			sequence:  frame.Sequence,
			requestID: frame.RequestId,
		})
		shouldDropResponse := server.dropFirstResponse && !server.firstResponseDropped
		if shouldDropResponse {
			server.firstResponseDropped = true
		}
		server.lock.Unlock()
		if shouldDropResponse {
			return nil
		}

		payload, encodeErr := encodeClusterPayload(false)
		if encodeErr != nil {
			return encodeErr
		}
		if err = stream.Send(&pbx.ClusterFrame{
			ClusterId:          frame.ClusterId,
			SourceNode:         "node-b",
			SourceInstance:     2,
			ProtocolVersion:    clusterProtocolVersion,
			MinProtocolVersion: clusterMinProtocolVersion,
			Lane:               frame.Lane,
			Sequence:           frame.Sequence,
			RequestId:          frame.RequestId,
			Kind:               frame.Kind,
			Payload:            payload,
			Response:           true,
			ClusterEpoch:       frame.ClusterEpoch,
			RingSignature:      frame.RingSignature,
		}); err != nil {
			return err
		}
		if server.closeAfterResponse {
			return nil
		}
		expectedSequence++
	}
}

// snapshot 返回并发安全的帧记录副本。
func (server *recordingClusterLaneServer) snapshot() []recordedClusterFrame {
	server.lock.Lock()
	defer server.lock.Unlock()
	return append([]recordedClusterFrame(nil), server.frames...)
}

// startRecordingClusterServer 启动仅供当前测试使用的真实本地 gRPC 服务。
func startRecordingClusterServer(
	t *testing.T,
	server pbx.ClusterTransportServer,
) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	pbx.RegisterClusterTransportServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	return func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}
}

// newClusterTransportTestPeer 创建带有一致 Ring 的测试客户端节点。
func newClusterTransportTestPeer(
	t *testing.T,
	dialer func(context.Context, string) (net.Conn, error),
	start bool,
) (*Cluster, *clusterGRPCPeer) {
	t.Helper()
	node := &ClusterNode{
		name:    "node-b",
		address: "passthrough:///cluster-test",
		done:    make(chan bool, 1),
	}
	cluster := &Cluster{
		clusterID:        "cluster-test",
		expectedReplicas: 2,
		thisNodeName:     "node-a",
		fingerprint:      1,
		nodes: map[string]*ClusterNode{
			node.name: node,
		},
	}
	cluster.rehash([]string{"node-a", "node-b"})
	config := normalizedClusterTransport{
		LaneCount:              4,
		ReliableQueueCapacity:  16,
		EphemeralQueueCapacity: 16,
		DialTimeout:            time.Second,
		RequestTimeout:         2 * time.Second,
	}
	peer, err := newClusterGRPCPeerWithDialOptions(
		cluster,
		node,
		config,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatal(err)
	}
	if start {
		peer.Start()
	}
	t.Cleanup(peer.Close)
	return cluster, peer
}

// TestClusterGRPCPeerPreservesLaneOrder 验证同一 Topic 固定到同一真实流且严格有序。
func TestClusterGRPCPeerPreservesLaneOrder(t *testing.T) {
	recordingServer := &recordingClusterLaneServer{}
	dialer := startRecordingClusterServer(t, recordingServer)
	_, peer := newClusterTransportTestPeer(t, dialer, true)

	const topic = "grp-order"
	const requestCount = 24
	expectedLane := uint32(clusterLaneIndex(topic, len(peer.lanes)))
	for index := range requestCount {
		var rejected bool
		err := peer.Call(
			"Cluster.TopicMaster",
			&ClusterReq{RcptTo: topic},
			&rejected,
		)
		if err != nil {
			t.Fatalf("第 %d 次调用失败：%v", index+1, err)
		}
	}

	frames := recordingServer.snapshot()
	if len(frames) != requestCount {
		t.Fatalf("收到 %d 个帧，期望 %d", len(frames), requestCount)
	}
	requestIDs := make(map[string]struct{}, requestCount)
	for index, frame := range frames {
		if frame.lane != expectedLane {
			t.Fatalf("帧 %d 位于 Lane %d，期望 %d", index, frame.lane, expectedLane)
		}
		if frame.sequence != uint64(index+1) {
			t.Fatalf("帧 %d sequence=%d，期望 %d", index, frame.sequence, index+1)
		}
		if _, duplicated := requestIDs[frame.requestID]; duplicated {
			t.Fatalf("request_id %q 重复", frame.requestID)
		}
		requestIDs[frame.requestID] = struct{}{}
	}
}

func TestClusterGRPCPeerPipelinesMultipleRequestsPerLane(t *testing.T) {
	const requestCount = 8
	dialer := startRecordingClusterServer(t, &pipelinedClusterLaneServer{expected: requestCount})
	_, peer := newClusterTransportTestPeer(t, dialer, true)
	peer.config.PipelineWindow = requestCount

	errorsFound := make(chan error, requestCount)
	var calls sync.WaitGroup
	for range requestCount {
		calls.Add(1)
		go func() {
			defer calls.Done()
			var rejected bool
			errorsFound <- peer.Call(
				"Cluster.TopicMaster",
				&ClusterReq{RcptTo: "grp-pipeline"},
				&rejected,
			)
		}()
	}
	calls.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("流水线调用失败: %v", err)
		}
	}
}

// TestClusterGRPCPeerResetsSequenceAfterReconnect 验证新建流从 sequence=1 开始。
func TestClusterGRPCPeerResetsSequenceAfterReconnect(t *testing.T) {
	recordingServer := &recordingClusterLaneServer{closeAfterResponse: true}
	dialer := startRecordingClusterServer(t, recordingServer)
	_, peer := newClusterTransportTestPeer(t, dialer, true)

	var rejected bool
	if err := peer.Call(
		"Cluster.TopicMaster",
		&ClusterReq{RcptTo: "grp-reconnect"},
		&rejected,
	); err != nil {
		t.Fatalf("首次调用失败：%v", err)
	}

	// 客户端必须先观察到旧流断开，下一次调用才会创建新流。
	sawDisconnect := false
	secondSuccess := false
	for range 6 {
		err := peer.Call(
			"Cluster.TopicMaster",
			&ClusterReq{RcptTo: "grp-reconnect"},
			&rejected,
		)
		if err != nil {
			sawDisconnect = true
			continue
		}
		secondSuccess = true
		break
	}
	if !sawDisconnect {
		t.Fatal("未观察到服务端主动断流")
	}
	if !secondSuccess {
		t.Fatal("断流后未能创建新的可用 Lane")
	}

	for index, frame := range recordingServer.snapshot() {
		if frame.sequence != 1 {
			t.Fatalf("第 %d 条新流首帧 sequence=%d，期望 1", index+1, frame.sequence)
		}
	}
}

// TestClusterGRPCPeerRetriesWithSameRequestID 验证响应丢失后使用同一标识有限重试。
func TestClusterGRPCPeerRetriesWithSameRequestID(t *testing.T) {
	recordingServer := &recordingClusterLaneServer{dropFirstResponse: true}
	dialer := startRecordingClusterServer(t, recordingServer)
	_, peer := newClusterTransportTestPeer(t, dialer, true)
	peer.config.MaxRetries = 2
	peer.config.RetryBackoff = time.Millisecond

	var rejected bool
	if err := peer.Call(
		"Cluster.TopicMaster",
		&ClusterReq{RcptTo: "grp-retry"},
		&rejected,
	); err != nil {
		t.Fatalf("有限重试后仍失败：%v", err)
	}

	frames := recordingServer.snapshot()
	if len(frames) != 2 {
		t.Fatalf("服务端收到 %d 个帧，期望首次发送和一次重试", len(frames))
	}
	if frames[0].requestID != frames[1].requestID {
		t.Fatalf("重试改变了 Request ID：%q != %q", frames[0].requestID, frames[1].requestID)
	}
	if frames[0].sequence != 1 || frames[1].sequence != 1 {
		t.Fatalf("重连流没有从 sequence=1 开始：%+v", frames)
	}
}

// TestClusterLaneFrameValidation 验证错误身份、Ring 和序号会被拒绝。
func TestClusterLaneFrameValidation(t *testing.T) {
	node := &ClusterNode{name: "node-b"}
	cluster := &Cluster{
		clusterID:    "cluster-test",
		thisNodeName: "node-a",
		fingerprint:  1,
		nodes: map[string]*ClusterNode{
			node.name: node,
		},
	}
	cluster.rehash([]string{"node-a", "node-b"})
	server := &clusterLaneServer{
		cluster: cluster,
		config: normalizedClusterTransport{
			LaneCount: 4,
		},
	}
	valid := &pbx.ClusterFrame{
		ClusterId:          cluster.clusterID,
		SourceNode:         "node-b",
		SourceInstance:     2,
		ProtocolVersion:    clusterProtocolVersion,
		MinProtocolVersion: clusterMinProtocolVersion,
		Lane:               1,
		Sequence:           1,
		RequestId:          "request-1",
		Kind:               pbx.ClusterFrameKind_CLUSTER_FRAME_TOPIC_MASTER,
		ClusterEpoch:       cluster.viewEpoch.Load(),
		RingSignature:      cluster.ringSignature(),
	}
	if err := server.validateFrame(valid, 1, 0, "", 0, nil); err != nil {
		t.Fatalf("合法帧被拒绝：%v", err)
	}

	invalidRing := proto.Clone(valid).(*pbx.ClusterFrame)
	invalidRing.RingSignature = "stale-ring"
	if err := server.validateFrame(invalidRing, 1, 0, "", 0, nil); err == nil ||
		!strings.Contains(err.Error(), "ring_signature") {
		t.Fatalf("错误 Ring 的校验结果 = %v", err)
	}

	invalidInstance := proto.Clone(valid).(*pbx.ClusterFrame)
	invalidInstance.SourceInstance = 0
	if err := server.validateFrame(invalidInstance, 1, 0, "", 0, nil); err == nil ||
		!strings.Contains(err.Error(), "source_instance") {
		t.Fatalf("错误实例标识的校验结果 = %v", err)
	}

	invalidSequence := proto.Clone(valid).(*pbx.ClusterFrame)
	invalidSequence.Sequence = 3
	if err := server.validateFrame(invalidSequence, 2, 1, "node-b", 2, nil); err == nil ||
		!strings.Contains(err.Error(), "期望 2") {
		t.Fatalf("错误序号的校验结果 = %v", err)
	}
}

// TestClusterTransportHelpers 验证调用映射、稳定分片和兼容负载编码。
func TestClusterTransportHelpers(t *testing.T) {
	kind, err := clusterFrameKindForProcedure("Cluster.Route")
	if err != nil || kind != pbx.ClusterFrameKind_CLUSTER_FRAME_ROUTE {
		t.Fatalf("调用映射结果 = %s, %v", kind, err)
	}
	if _, err = clusterFrameKindForProcedure("Cluster.Unknown"); err == nil {
		t.Fatal("未知调用未被拒绝")
	}
	if !clusterProtocolVersionsOverlap(
		clusterMinProtocolVersion,
		clusterProtocolVersion,
	) {
		t.Fatal("本地协议范围无法与自身协商")
	}
	if clusterProtocolVersionsOverlap(
		clusterProtocolVersion+1,
		clusterProtocolVersion+1,
	) {
		t.Fatal("不相交的未来协议范围未被拒绝")
	}
	if clusterProtocolVersionsOverlap(1, 1) {
		t.Fatal("使用旧 Ring 映射的协议版本 1 未被拒绝")
	}

	const topic = "grp-stable"
	firstLane := clusterLaneIndex(topic, 8)
	for range 100 {
		if lane := clusterLaneIndex(topic, 8); lane != firstLane {
			t.Fatalf("稳定分片发生变化：%d -> %d", firstLane, lane)
		}
	}

	input := ClusterPing{Node: "node-a", Fingerprint: 123}
	payload, err := encodeClusterPayload(&input)
	if err != nil {
		t.Fatal(err)
	}
	var output ClusterPing
	if err = decodeClusterPayload(payload, &output); err != nil {
		t.Fatal(err)
	}
	if output != input {
		t.Fatalf("负载往返结果 = %+v，期望 %+v", output, input)
	}

	// 控制响应的 Params 是 interface，历史与订阅响应会在其中携带
	// time.Time；未注册时只会在真实跨节点响应路径触发 Gob 错误。
	timestamp := time.Now().UTC().Round(time.Millisecond)
	timePayload, err := encodeClusterPayload(&MsgServerCtrl{
		Params: map[string]any{"created": timestamp},
	})
	if err != nil {
		t.Fatalf("编码带 time.Time 参数的控制响应失败: %v", err)
	}
	var timeOutput MsgServerCtrl
	if err = decodeClusterPayload(timePayload, &timeOutput); err != nil {
		t.Fatalf("解码带 time.Time 参数的控制响应失败: %v", err)
	}
	decodedTimestamp, valid := timeOutput.Params.(map[string]any)["created"].(time.Time)
	if !valid || !decodedTimestamp.Equal(timestamp) {
		t.Fatalf("time.Time 参数往返结果=%v，期望=%v", decodedTimestamp, timestamp)
	}

	if !clusterRequestIsReliable(&ClusterReq{
		CliMsg: &ClientComMessage{Pub: &MsgClientPub{}},
	}) {
		t.Fatal("消息发布被错误分类为瞬态事件")
	}
	if clusterRequestIsReliable(&ClusterReq{
		CliMsg: &ClientComMessage{Note: &MsgClientNote{What: "kp"}},
	}) {
		t.Fatal("输入状态未被分类为瞬态事件")
	}
}

// TestClusterLaneBackpressure 验证可靠队列明确拒绝、瞬态队列允许丢弃。
func TestClusterLaneBackpressure(t *testing.T) {
	recordingServer := &recordingClusterLaneServer{}
	dialer := startRecordingClusterServer(t, recordingServer)
	_, peer := newClusterTransportTestPeer(t, dialer, false)
	laneIndex := clusterLaneIndex("grp-full", len(peer.lanes))
	lane := peer.lanes[laneIndex]

	// 未启动发送协程并人工占满两个队列，确保测试不依赖调度时序。
	lane.reliableQueue = make(chan *clusterLaneRequest, 1)
	lane.ephemeralQueue = make(chan *clusterLaneRequest, 1)
	lane.reliableQueue <- &clusterLaneRequest{}
	lane.ephemeralQueue <- &clusterLaneRequest{}

	var rejected bool
	err := peer.Call(
		"Cluster.TopicMaster",
		&ClusterReq{RcptTo: "grp-full", CliMsg: &ClientComMessage{Pub: &MsgClientPub{}}},
		&rejected,
	)
	if !errors.Is(err, errClusterLaneQueueFull) {
		t.Fatalf("可靠队列满返回 %v，期望 %v", err, errClusterLaneQueueFull)
	}
	err = peer.Call(
		"Cluster.TopicMaster",
		&ClusterReq{
			RcptTo: "grp-full",
			CliMsg: &ClientComMessage{Note: &MsgClientNote{
				What: "kp",
			}},
		},
		&rejected,
	)
	if err != nil {
		t.Fatalf("瞬态队列满不应向业务返回错误：%v", err)
	}
}
