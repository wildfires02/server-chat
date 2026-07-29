package server

import (
	"encoding/json"
	"fmt"
	"testing"

	"chat/server/store/types"
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

// BenchmarkStandaloneJSONSerialize 测量 WebSocket/Long Polling 共用的
// 本地消息 JSON 序列化成本。
func BenchmarkStandaloneJSONSerialize(b *testing.B) {
	session := &Session{proto: WEBSOCK}
	message := standaloneBenchmarkMessage()
	b.ReportAllocs()
	for range b.N {
		_, _ = session.serialize(message)
	}
}

// BenchmarkStandaloneGRPCSerialize 测量客户端 gRPC 本地消息转换成本。
func BenchmarkStandaloneGRPCSerialize(b *testing.B) {
	session := &Session{proto: GRPC}
	message := standaloneBenchmarkMessage()
	b.ReportAllocs()
	for range b.N {
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
	for range b.N {
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
					<-session.send
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
