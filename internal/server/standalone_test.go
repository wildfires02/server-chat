package server

import (
	"chat/api/pbx"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"chat/server/store/types"
	"google.golang.org/protobuf/proto"
)

// TestStandaloneClusterInitSkipsClusterResources 验证显式单机模式在解析
// cluster_config 之前返回，即使输入包含无效集群配置也不会创建集群对象。
func TestStandaloneClusterInitSkipsClusterResources(t *testing.T) {
	originalCluster := globals.cluster
	globals.cluster = nil
	t.Cleanup(func() {
		globals.cluster = originalCluster
	})

	self := "must-not-be-used"
	workerID := clusterInit(
		json.RawMessage(`{invalid-cluster-config`),
		&self,
		deploymentModeStandalone,
	)
	if workerID != 1 {
		t.Fatalf("单机 Snowflake worker ID=%d，期望 1", workerID)
	}
	if globals.cluster != nil {
		t.Fatal("显式单机模式创建了 Cluster 运行时")
	}
}

// TestStandaloneResolverAlwaysUsesLocalNode 验证 nil Cluster 在单机模式下
// 始终把 Topic 留在本地，不会尝试建立代理或节点间路由。
func TestStandaloneResolverAlwaysUsesLocalNode(t *testing.T) {
	var cluster *Cluster
	for _, topic := range []string{
		"sys",
		"me",
		"grpStandalone",
		"chnStandalone",
		"usrStandalone",
	} {
		if cluster.isRemoteTopic(topic) {
			t.Fatalf("单机 Resolver 把 Topic %q 判断为远端", topic)
		}
	}
}

// TestStandaloneSessionQueueBackpressure 验证慢客户端填满本地发送队列后
// 只拒绝该 Session 的新消息，不产生无界缓冲。
func TestStandaloneSessionQueueBackpressure(t *testing.T) {
	session := &Session{
		proto: WEBSOCK,
		sid:   "standalone-backpressure",
		send:  make(chan any, 1),
	}
	if !session.queueOutBytes([]byte("first")) {
		t.Fatal("空闲单机 Session 队列拒绝了首条消息")
	}
	if session.queueOutBytes([]byte("second")) {
		t.Fatal("已满单机 Session 队列没有执行背压")
	}
	if queued := len(session.send); queued != 1 {
		t.Fatalf("背压后队列长度=%d，期望 1", queued)
	}
}

func TestStandaloneSessionQueueBackpressureCountsBytes(t *testing.T) {
	session := &Session{proto: WEBSOCK, sid: "byte-backpressure", send: make(chan any, 4)}
	first := make([]byte, 5<<20)
	second := make([]byte, 4<<20)
	if !session.queueOutBytes(first) {
		t.Fatal("byte queue rejected the first frame")
	}
	if session.queueOutBytes(second) {
		t.Fatal("byte queue accepted frames beyond the 8 MiB limit")
	}
	queued := <-session.send
	session.releaseOutbound(queued)
	if pending := session.sendPendingBytes.Load(); pending != 0 {
		t.Fatalf("pending bytes after drain=%d, want 0", pending)
	}
}

func TestProtobufQueueAccountingUsesActualBatchPayloadSize(t *testing.T) {
	session := &Session{
		proto:    WEBSOCK,
		wsBinary: true,
		ver:      parseVersion("0.33"),
	}
	messages := []*ServerComMessage{
		standaloneBenchmarkMessage(),
		standaloneBenchmarkMessage(),
	}
	expectedBatch := &pbx.ServerBatch{Messages: []*pbx.ServerMsg{
		messages[0].serializedProto(),
		messages[1].serializedProto(),
	}}
	if got, want := session.outboundQueueSize(messages), int64(proto.Size(expectedBatch)); got != want {
		t.Fatalf("protobuf queue bytes=%d, want actual payload size %d", got, want)
	}
}

func TestGroupFanoutSharesProjectedWireMessage(t *testing.T) {
	topic, sessions := standaloneFanoutTopic(2)
	topic.broadcastToSessions(standaloneBenchmarkMessage())
	first := (<-sessions[0].send).(*ServerComMessage)
	second := (<-sessions[1].send).(*ServerComMessage)
	sessions[0].releaseOutbound(first)
	sessions[1].releaseOutbound(second)
	if first != second {
		t.Fatal("local group recipients did not share the projected message")
	}
	if &first.serializedJSON()[0] != &second.serializedJSON()[0] ||
		first.serializedProto() != second.serializedProto() {
		t.Fatal("shared group message did not reuse JSON and Protobuf encodings")
	}
}

func TestWebSocketBatchingRequiresProtocol032(t *testing.T) {
	legacy := &Session{proto: WEBSOCK, ver: parseVersion("0.31"), send: make(chan any, 4)}
	current := &Session{proto: WEBSOCK, ver: parseVersion("0.32"), send: make(chan any, 4)}
	grpcSession := &Session{proto: GRPC, ver: parseVersion("0.32"), send: make(chan any, 4)}

	if legacy.supportsMessageBatching() {
		t.Fatal("0.31 WebSocket session unexpectedly supports batch envelopes")
	}
	if !current.supportsMessageBatching() {
		t.Fatal("0.32 WebSocket session does not support batch envelopes")
	}
	if grpcSession.supportsMessageBatching() {
		t.Fatal("gRPC session must keep native per-message protobuf framing")
	}
}

func TestJSONBatchSerializationPreservesOrderAndBoundsFrames(t *testing.T) {
	session := &Session{proto: WEBSOCK, ver: parseVersion("0.32")}
	messages := []*ServerComMessage{
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 1, Content: "first"}},
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 2, Content: "second"}},
	}

	frames := session.serializeBatchAndUpdateStats(messages)
	if len(frames) != 1 {
		t.Fatalf("batch frame count=%d, want 1", len(frames))
	}
	var decoded struct {
		Batch []*ServerComMessage `json:"batch"`
	}
	if err := json.Unmarshal(frames[0], &decoded); err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if len(decoded.Batch) != 2 || decoded.Batch[0].Data.SeqId != 1 || decoded.Batch[1].Data.SeqId != 2 {
		t.Fatalf("unexpected batch order: %#v", decoded.Batch)
	}

	largeContent := strings.Repeat("x", maxJSONBatchFrameSize/2)
	largeMessages := []*ServerComMessage{
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 1, Content: largeContent}},
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 2, Content: largeContent}},
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 3, Content: largeContent}},
	}
	frames = session.serializeBatchAndUpdateStats(largeMessages)
	if len(frames) < 2 {
		t.Fatalf("large batch frame count=%d, want at least 2", len(frames))
	}
	for _, frame := range frames {
		if len(frame) > maxJSONBatchFrameSize {
			t.Fatalf("batch frame size=%d exceeds limit=%d", len(frame), maxJSONBatchFrameSize)
		}
	}
}

func TestProtobufWebSocketBatchSerializationPreservesOrder(t *testing.T) {
	session := &Session{proto: WEBSOCK, wsBinary: true, ver: parseVersion("0.33")}
	if !session.supportsProtobufWebSocket() {
		t.Fatal("negotiated 0.33 WebSocket session did not enable protobuf")
	}
	frames := session.serializeBatchAndUpdateStats([]*ServerComMessage{
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 1, Content: "first"}},
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 2, Content: "second"}},
	})
	if len(frames) != 1 {
		t.Fatalf("protobuf frame count=%d, want 1", len(frames))
	}
	var decoded pbx.ServerBatch
	if err := proto.Unmarshal(frames[0], &decoded); err != nil {
		t.Fatalf("decode protobuf batch: %v", err)
	}
	if len(decoded.Messages) != 2 || decoded.Messages[0].GetData().GetSeqId() != 1 ||
		decoded.Messages[1].GetData().GetSeqId() != 2 {
		t.Fatalf("unexpected protobuf batch order: %#v", decoded.Messages)
	}

	largeContent := strings.Repeat("x", maxJSONBatchFrameSize/2)
	frames = session.serializeBatchAndUpdateStats([]*ServerComMessage{
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 1, Content: largeContent}},
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 2, Content: largeContent}},
		{Data: &MsgServerData{Topic: "grpBatch", SeqId: 3, Content: largeContent}},
	})
	if len(frames) < 2 {
		t.Fatalf("large protobuf batch frame count=%d, want at least 2", len(frames))
	}
	for _, frame := range frames {
		if len(frame) > maxJSONBatchFrameSize {
			t.Fatalf("protobuf batch frame size=%d exceeds limit=%d", len(frame), maxJSONBatchFrameSize)
		}
	}
}

func TestResumeCatchupQueryUsesClientCursors(t *testing.T) {
	me := buildResumeGetQuery(MsgResumeTopic{Topic: "me"})
	if me.What != "desc sub" || me.Desc == nil || me.Sub == nil {
		t.Fatalf("unexpected me resume query: %#v", me)
	}
	active := buildResumeGetQuery(MsgResumeTopic{
		Topic: "grpResume", SeqId: 41, DelId: 6, Active: true,
	})
	if active.What != "desc sub data del aux" || active.Data == nil ||
		active.Data.SinceId != 42 || !active.Data.Forward ||
		active.Data.Limit != resumeCatchupPageSize || active.Del == nil ||
		active.Del.SinceId != 7 || !active.Del.Forward ||
		active.Del.Limit != resumeCatchupPageSize {
		t.Fatalf("unexpected active resume query: %#v", active)
	}
	inactive := buildResumeGetQuery(MsgResumeTopic{Topic: "grpIdle"})
	if inactive.What != "desc" || inactive.Desc == nil || inactive.Data != nil {
		t.Fatalf("unexpected inactive resume query: %#v", inactive)
	}
}

func TestResumeTopicsAreDispatchedBeforeWaitingForCompletion(t *testing.T) {
	previousHub := globals.hub
	globals.hub = &Hub{join: make(chan *ClientComMessage, 2)}
	t.Cleanup(func() { globals.hub = previousHub })

	session := test_makeSession(types.Uid(10))
	session.subs = make(map[string]*Subscription)
	result := make(chan []string, 1)
	go func() {
		result <- session.resumeTopics("resume-parallel", []MsgResumeTopic{
			{Topic: "grpResumeOne", Active: true},
			{Topic: "grpResumeTwo", Active: true},
		})
	}()

	requests := make([]*ClientComMessage, 0, 2)
	for len(requests) < 2 {
		select {
		case request := <-globals.hub.join:
			requests = append(requests, request)
		case <-time.After(time.Second):
			t.Fatal("resume subscriptions were serialized before entering the Topic actors")
		}
	}
	for _, request := range requests {
		session.addSub(request.RcptTo, &Subscription{})
		request.sess.inflightReqs.Done()
	}

	select {
	case restored := <-result:
		if len(restored) != 2 || restored[0] != "grpResumeOne" || restored[1] != "grpResumeTwo" {
			t.Fatalf("restored topics=%v", restored)
		}
	case <-time.After(time.Second):
		t.Fatal("parallel resume did not complete")
	}
}

func TestResumeTopicValidationRejectsInvalidOrDuplicateCursors(t *testing.T) {
	if !validateResumeTopics([]MsgResumeTopic{{Topic: "me"}, {Topic: "grpA", SeqId: 7}}) {
		t.Fatal("valid resume topic cursors were rejected")
	}
	invalid := [][]MsgResumeTopic{
		{{Topic: ""}},
		{{Topic: "grpA", SeqId: -1}},
		{{Topic: "grpA", DelId: maxResumeCursor + 1}},
		{{Topic: "grpA"}, {Topic: "grpA"}},
	}
	for _, topics := range invalid {
		if validateResumeTopics(topics) {
			t.Fatalf("invalid resume topic cursors were accepted: %#v", topics)
		}
	}
}

func TestClientBatchDecodingRequiresProtocol032(t *testing.T) {
	raw := []byte(`{"batch":[{"get":{"id":"1","topic":"grp1","what":"desc"}},{"get":{"id":"2","topic":"grp2","what":"desc"}}]}`)
	messages, err := decodeClientWireMessages(raw, true)
	if err != nil {
		t.Fatalf("decode client batch: %v", err)
	}
	if len(messages) != 2 || messages[0].Get.Id != "1" || messages[1].Get.Id != "2" {
		t.Fatalf("unexpected client batch: %#v", messages)
	}
	if _, err = decodeClientWireMessages(raw, false); err == nil {
		t.Fatal("legacy protocol unexpectedly accepted client batch")
	}
	if _, err = decodeClientWireMessages([]byte(`{"batch":[]}`), true); err == nil {
		t.Fatal("empty client batch unexpectedly accepted")
	}
}

// BenchmarkStandaloneJSONSerialize 测量 WebSocket/Long Polling 共用的
// 本地消息 JSON 序列化成本。
func BenchmarkStandaloneJSONSerialize(b *testing.B) {
	session := &Session{proto: WEBSOCK}
	message := standaloneBenchmarkMessage()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = session.serialize(message)
	}
}

// BenchmarkStandaloneGRPCSerialize 测量客户端 gRPC 本地消息转换成本。
func BenchmarkStandaloneGRPCSerialize(b *testing.B) {
	session := &Session{proto: GRPC}
	message := standaloneBenchmarkMessage()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = session.serialize(message)
	}
}

// BenchmarkStandaloneSessionQueue 测量单机 Session 有界队列的入队和消费成本。
func BenchmarkStandaloneSessionQueue(b *testing.B) {
	session := &Session{
		proto: WEBSOCK,
		send:  make(chan any, 1),
	}
	message := standaloneBenchmarkMessage()
	b.ReportAllocs()
	for b.Loop() {
		if !session.queueOut(message) {
			b.Fatal("单机 Session 队列意外执行背压")
		}
		<-session.send
	}
}

// TestStandaloneTopicFanout 验证单机热点 Topic 可以把同一条消息完整投递给
// 一千个本地 Session，且每个 Session 只收到一份消息副本。
func TestStandaloneTopicFanout(t *testing.T) {
	topic, sessions := standaloneFanoutTopic(1000)
	topic.broadcastToSessions(standaloneBenchmarkMessage())
	for index, session := range sessions {
		if queued := len(session.send); queued != 1 {
			t.Fatalf("Session %d 收到 %d 条消息，期望 1", index, queued)
		}
	}
}

// BenchmarkStandaloneTopicFanout 测量单机热点 Topic 在不同订阅规模下的
// 本地广播成本，不包含数据库保存和网络写入。
func BenchmarkStandaloneTopicFanout(b *testing.B) {
	for _, subscriberCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("subscribers-%d", subscriberCount), func(b *testing.B) {
			topic, sessions := standaloneFanoutTopic(subscriberCount)
			message := standaloneBenchmarkMessage()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				topic.broadcastToSessions(message)
				for _, session := range sessions {
					queued := <-session.send
					session.releaseOutbound(queued)
				}
			}
		})
	}
}

// standaloneFanoutTopic 构造拥有固定数量可读订阅者的本地 Topic。
func standaloneFanoutTopic(subscriberCount int) (*Topic, []*Session) {
	topic := &Topic{
		name:      "grpStandalone",
		xoriginal: "grpStandalone",
		cat:       types.TopicCatGrp,
		perUser:   make(map[types.Uid]perUserData, subscriberCount),
		sessions:  make(map[*Session]perSessionData, subscriberCount),
	}
	sessions := make([]*Session, 0, subscriberCount)
	for index := 0; index < subscriberCount; index++ {
		uid := types.Uid(index + 1)
		session := &Session{
			proto: WEBSOCK,
			sid:   fmt.Sprintf("standalone-fanout-%d", index),
			send:  make(chan any, 1),
		}
		topic.perUser[uid] = perUserData{
			modeWant:  types.ModeRead,
			modeGiven: types.ModeRead,
		}
		topic.sessions[session] = perSessionData{uid: uid}
		sessions = append(sessions, session)
	}
	return topic, sessions
}

// standaloneBenchmarkMessage 创建固定的代表性文本消息，避免把测试数据
// 构造成本计入序列化基准。
func standaloneBenchmarkMessage() *ServerComMessage {
	return &ServerComMessage{
		Data: &MsgServerData{
			Topic:   "grpStandalone",
			From:    "usrStandalone",
			SeqId:   42,
			Head:    map[string]any{"content-type": "text/plain"},
			Content: "standalone benchmark message",
		},
	}
}
