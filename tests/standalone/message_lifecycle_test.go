// Package standalone_test 提供依赖真实单机进程的消息生命周期与热点回归。
package standalone_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// standaloneMessageCase 定义一种需要经过真实协议、业务层和数据库的消息。
type standaloneMessageCase struct {
	// name 用于生成稳定且可读的请求 ID。
	name string
	// kind 是客户端声明、服务端必须重新推导并校验的消息类型。
	kind string
	// content 是待持久化的文本或 Drafty 正文。
	content any
}

// persistenceState 保存跨进程重启验证所需的最小持久化定位信息。
type persistenceState struct {
	// Topic 是第一阶段创建并持久化的群组。
	Topic string `json:"topic"`
	// ClientID 是已获得 ACK 的消息幂等键。
	ClientID string `json:"client_id"`
	// SeqID 是 ACK 返回的服务端序列号。
	SeqID int `json:"seq_id"`
	// Content 是重启后必须能够读回的文本正文。
	Content string `json:"content"`
}

// TestStandaloneMessageLifecycle 通过真实 WebSocket 进程验证文本、Drafty、
// 图片、视频、语音、音频和文件的发布、历史读取与物理删除。
func TestStandaloneMessageLifecycle(t *testing.T) {
	cfg := loadIntegrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	conn := openAuthenticatedWebSocket(t, ctx, cfg, "lifecycle")
	defer conn.Close()

	if err := conn.WriteJSON(subMessage("lifecycle-sub")); err != nil {
		t.Fatalf("创建消息生命周期 Topic 失败: %v", err)
	}
	sub, err := readWebSocketControl(conn, "lifecycle-sub")
	if err != nil {
		t.Fatalf("读取消息生命周期建群响应失败: %v", err)
	}
	if sub.Code != http.StatusOK {
		t.Fatalf("创建消息生命周期 Topic 状态码=%d，期望 200", sub.Code)
	}
	topic := requireCreatedTopic(t, "消息生命周期", sub.Topic)

	cases := standaloneMessageCases()
	seqByName := make(map[string]int, len(cases))
	for index, testCase := range cases {
		requestID := "lifecycle-pub-" + testCase.name
		clientID := fmt.Sprintf("lifecycle-%d-%s", time.Now().UnixNano(), testCase.name)
		if err := conn.WriteJSON(pubContentMessage(
			requestID,
			topic,
			clientID,
			testCase.kind,
			testCase.content,
		)); err != nil {
			t.Fatalf("发布 %s 消息失败: %v", testCase.name, err)
		}
		ctrl, err := readWebSocketControl(conn, requestID)
		if err != nil {
			t.Fatalf("读取 %s 消息 ACK 失败: %v", testCase.name, err)
		}
		if ctrl.Code != http.StatusAccepted {
			t.Fatalf(
				"发布 %s 消息状态码=%d，响应=%s，期望 202",
				testCase.name,
				ctrl.Code,
				ctrl.Text,
			)
		}
		seq := controlIntParam(t, ctrl, "seq")
		if index > 0 && seq <= seqByName[cases[index-1].name] {
			t.Fatalf(
				"%s 消息 seq=%d，没有大于前一条 seq=%d",
				testCase.name,
				seq,
				seqByName[cases[index-1].name],
			)
		}
		seqByName[testCase.name] = seq
	}

	history := readWebSocketHistory(t, conn, topic, "lifecycle-history", 1, 100)
	if len(history) != len(cases) {
		t.Fatalf("历史消息数量=%d，期望=%d", len(history), len(cases))
	}
	for index, message := range history {
		testCase := cases[index]
		if message.Kind != testCase.kind {
			t.Errorf(
				"第 %d 条历史消息 kind=%q，期望=%q",
				index,
				message.Kind,
				testCase.kind,
			)
		}
		if message.SeqID != seqByName[testCase.name] {
			t.Errorf(
				"第 %d 条历史消息 seq=%d，期望=%d",
				index,
				message.SeqID,
				seqByName[testCase.name],
			)
		}
		if len(message.Content) == 0 || string(message.Content) == "null" {
			t.Errorf("第 %d 条历史消息正文为空", index)
		}
	}

	fileSeq := seqByName["file"]
	if err := conn.WriteJSON(deleteMessage("lifecycle-delete", topic, fileSeq)); err != nil {
		t.Fatalf("发送文件消息删除请求失败: %v", err)
	}
	deleted, err := readWebSocketControl(conn, "lifecycle-delete")
	if err != nil {
		t.Fatalf("读取文件消息删除响应失败: %v", err)
	}
	if deleted.Code != http.StatusOK {
		t.Fatalf("删除文件消息状态码=%d，响应=%s，期望 200", deleted.Code, deleted.Text)
	}

	remaining := readWebSocketHistory(t, conn, topic, "lifecycle-history-after-delete", 1, 100)
	if len(remaining) != len(cases)-1 {
		t.Fatalf("删除后历史消息数量=%d，期望=%d", len(remaining), len(cases)-1)
	}
	for _, message := range remaining {
		if message.SeqID == fileSeq || message.Kind == "file" {
			t.Fatalf("被物理删除的文件消息仍出现在历史中: seq=%d", message.SeqID)
		}
	}
}

// TestStandalonePersistenceProbe 分 write 和 verify 两阶段运行。外部脚本会在
// 两阶段之间强制终止并重新拉起服务，从而验证 ACK 后的消息能够跨进程恢复。
func TestStandalonePersistenceProbe(t *testing.T) {
	phase := strings.TrimSpace(os.Getenv("IM_TEST_STANDALONE_PERSISTENCE_PHASE"))
	if phase == "" {
		t.Skip("未设置 IM_TEST_STANDALONE_PERSISTENCE_PHASE，跳过跨进程持久化探针")
	}
	statePath := strings.TrimSpace(os.Getenv("IM_TEST_STANDALONE_PERSISTENCE_STATE"))
	if statePath == "" {
		t.Fatal("持久化探针必须设置 IM_TEST_STANDALONE_PERSISTENCE_STATE")
	}

	cfg := loadIntegrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := openAuthenticatedWebSocket(t, ctx, cfg, "persistence-"+phase)
	defer conn.Close()

	switch phase {
	case "write":
		writePersistenceProbe(t, conn, statePath)
	case "verify":
		verifyPersistenceProbe(t, conn, statePath)
	default:
		t.Fatalf("未知持久化探针阶段 %q，只支持 write 或 verify", phase)
	}
}

// TestStandaloneWebSocketHotTopic 让多条真实 WebSocket 订阅同一 Topic，
// 对每条发布验证数据库 ACK 和全部在线订阅者的网络投递。
func TestStandaloneWebSocketHotTopic(t *testing.T) {
	cfg := loadIntegrationConfig(t)
	receiverCount := positiveEnvInt(t, "IM_TEST_STANDALONE_HOT_RECEIVERS", 16)
	messageCount := positiveEnvInt(t, "IM_TEST_STANDALONE_HOT_MESSAGES", 100)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	publisher := openAuthenticatedWebSocket(t, ctx, cfg, "hot-publisher")
	defer publisher.Close()
	if err := publisher.WriteJSON(subMessage("hot-create")); err != nil {
		t.Fatalf("创建热点 Topic 失败: %v", err)
	}
	created, err := readWebSocketControl(publisher, "hot-create")
	if err != nil {
		t.Fatalf("读取热点 Topic 创建响应失败: %v", err)
	}
	if created.Code != http.StatusOK {
		t.Fatalf("创建热点 Topic 状态码=%d，期望 200", created.Code)
	}
	topic := requireCreatedTopic(t, "热点测试", created.Topic)

	receivers := make([]*websocket.Conn, 0, receiverCount)
	t.Cleanup(func() {
		for _, receiver := range receivers {
			_ = receiver.Close()
		}
	})
	for index := 0; index < receiverCount; index++ {
		receiver := openAuthenticatedWebSocket(
			t,
			ctx,
			cfg,
			fmt.Sprintf("hot-receiver-%d", index),
		)
		requestID := fmt.Sprintf("hot-sub-%d", index)
		if err := receiver.WriteJSON(subExistingMessage(requestID, topic)); err != nil {
			_ = receiver.Close()
			t.Fatalf("第 %d 个热点订阅者发送 sub 失败: %v", index, err)
		}
		sub, err := readWebSocketControl(receiver, requestID)
		if err != nil {
			_ = receiver.Close()
			t.Fatalf("第 %d 个热点订阅者读取 sub 失败: %v", index, err)
		}
		if sub.Code != http.StatusOK {
			_ = receiver.Close()
			t.Fatalf("第 %d 个热点订阅者 sub 状态码=%d，期望 200", index, sub.Code)
		}
		receivers = append(receivers, receiver)
	}

	prefix := fmt.Sprintf("hot-%d-", time.Now().UnixNano())
	results := make(chan error, receiverCount)
	deliveryDurations := make(chan time.Duration, receiverCount*messageCount)
	var sentAt sync.Map
	var readers sync.WaitGroup
	for index, receiver := range receivers {
		readers.Add(1)
		go func(receiverIndex int, connection *websocket.Conn) {
			defer readers.Done()
			_ = connection.SetReadDeadline(time.Now().Add(75 * time.Second))
			received := 0
			for received < messageCount {
				var envelope wireServerMessage
				if err := connection.ReadJSON(&envelope); err != nil {
					results <- fmt.Errorf("第 %d 个热点订阅者读取失败: %w", receiverIndex, err)
					return
				}
				if envelope.Data != nil &&
					envelope.Data.Topic == topic &&
					strings.HasPrefix(envelope.Data.ClientID, prefix) {
					if startedAt, found := sentAt.Load(envelope.Data.ClientID); found {
						deliveryDurations <- time.Since(startedAt.(time.Time))
					}
					received++
				}
			}
			results <- nil
		}(index, receiver)
	}

	started := time.Now()
	ackDurations := make([]time.Duration, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		requestID := fmt.Sprintf("hot-pub-%d", index)
		clientID := fmt.Sprintf("%s%d", prefix, index)
		ackStarted := time.Now()
		sentAt.Store(clientID, ackStarted)
		if err := publisher.WriteJSON(pubContentMessage(
			requestID,
			topic,
			clientID,
			"text",
			fmt.Sprintf("热点消息 %d", index),
		)); err != nil {
			t.Fatalf("第 %d 条热点消息发布失败: %v", index, err)
		}
		ctrl, err := readWebSocketControl(publisher, requestID)
		if err != nil {
			t.Fatalf("第 %d 条热点消息读取 ACK 失败: %v", index, err)
		}
		if ctrl.Code != http.StatusAccepted {
			t.Fatalf("第 %d 条热点消息 ACK=%d，期望 202", index, ctrl.Code)
		}
		ackDurations = append(ackDurations, time.Since(ackStarted))
	}

	for index := 0; index < receiverCount; index++ {
		select {
		case err := <-results:
			if err != nil {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Fatalf("等待第 %d 个热点订阅者完成时超时: %v", index, ctx.Err())
		}
	}
	readers.Wait()
	close(deliveryDurations)
	sorted := append([]time.Duration(nil), ackDurations...)
	sort.Slice(sorted, func(left, right int) bool {
		return sorted[left] < sorted[right]
	})
	sortedDeliveries := make([]time.Duration, 0, receiverCount*messageCount)
	for duration := range deliveryDurations {
		sortedDeliveries = append(sortedDeliveries, duration)
	}
	sort.Slice(sortedDeliveries, func(left, right int) bool {
		return sortedDeliveries[left] < sortedDeliveries[right]
	})
	if len(sortedDeliveries) != receiverCount*messageCount {
		t.Fatalf(
			"投递延迟样本=%d，期望=%d",
			len(sortedDeliveries),
			receiverCount*messageCount,
		)
	}
	ackP99 := percentile(sorted, 0.99)
	deliveryP99 := percentile(sortedDeliveries, 0.99)
	elapsed := time.Since(started)
	t.Logf(
		"真实热点 Topic 完成：订阅者=%d，消息=%d，网络投递=%d，"+
			"总耗时=%s，ACK p95=%s，ACK p99=%s，ACK max=%s，"+
			"投递 p95=%s，投递 p99=%s，投递 max=%s",
		receiverCount,
		messageCount,
		receiverCount*messageCount,
		elapsed,
		percentile(sorted, 0.95),
		ackP99,
		sorted[len(sorted)-1],
		percentile(sortedDeliveries, 0.95),
		deliveryP99,
		sortedDeliveries[len(sortedDeliveries)-1],
	)
	assertOptionalLatencySLO(t, "IM_TEST_ACK_P99_MAX", ackP99)
	assertOptionalLatencySLO(t, "IM_TEST_DELIVERY_P99_MAX", deliveryP99)
}

// assertOptionalLatencySLO 在设置环境门限时把性能基线升级为发布门禁。
func assertOptionalLatencySLO(t *testing.T, environmentName string, actual time.Duration) {
	t.Helper()
	rawLimit := strings.TrimSpace(os.Getenv(environmentName))
	if rawLimit == "" {
		return
	}
	limit, err := time.ParseDuration(rawLimit)
	if err != nil || limit <= 0 {
		t.Fatalf("%s=%q 不是正时长", environmentName, rawLimit)
	}
	if actual > limit {
		t.Fatalf("%s 实测=%s，超过门限=%s", environmentName, actual, limit)
	}
}

// standaloneMessageCases 返回覆盖当前基础消息能力的真实正文样例。
func standaloneMessageCases() []standaloneMessageCase {
	return []standaloneMessageCase{
		{name: "text", kind: "text", content: "单机真实文本消息"},
		{
			name: "drafty",
			kind: "drafty",
			content: map[string]any{
				"txt": "粗体",
				"fmt": []map[string]any{{"at": 0, "len": 2, "tp": "ST"}},
			},
		},
		{
			name: "image",
			kind: "image",
			content: draftyMediaContent("IM", map[string]any{
				"mime": "image/png", "val": "aW1hZ2U=", "width": 16, "height": 16,
			}),
		},
		{
			name: "video",
			kind: "video",
			content: draftyMediaContent("VD", map[string]any{
				"mime": "video/mp4", "val": "dmlkZW8=", "duration": 1000,
				"width": 16, "height": 16,
			}),
		},
		{
			name: "voice",
			kind: "voice",
			content: draftyMediaContent("AU", map[string]any{
				"mime": "audio/ogg", "val": "dm9pY2U=", "duration": 800, "voice": true,
			}),
		},
		{
			name: "audio",
			kind: "audio",
			content: draftyMediaContent("AU", map[string]any{
				"mime": "audio/mpeg", "val": "YXVkaW8=", "duration": 1200,
			}),
		},
		{
			name: "file",
			kind: "file",
			content: draftyMediaContent("EX", map[string]any{
				"mime": "application/pdf", "val": "ZmlsZQ==",
				"name": "standalone.pdf", "size": 4,
			}),
		},
	}
}

// draftyMediaContent 创建引用一个内联实体的最小合法 Drafty 文档。
func draftyMediaContent(entityType string, data map[string]any) map[string]any {
	return map[string]any{
		"txt": " ",
		"fmt": []map[string]any{{"at": 0, "len": 1, "key": 0}},
		"ent": []map[string]any{{"tp": entityType, "data": data}},
	}
}

// openAuthenticatedWebSocket 建立 WebSocket，完成 hi 与 basic 登录。
func openAuthenticatedWebSocket(
	t *testing.T,
	ctx context.Context,
	cfg integrationConfig,
	idPrefix string,
) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, websocketURL(cfg), nil)
	if err != nil {
		t.Fatalf("%s 建立 WebSocket 失败: %v", idPrefix, err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(hiMessage(idPrefix + "-hi")); err != nil {
		_ = conn.Close()
		t.Fatalf("%s 发送 hi 失败: %v", idPrefix, err)
	}
	hi, err := readWebSocketControl(conn, idPrefix+"-hi")
	if err != nil || hi.Code != http.StatusCreated {
		_ = conn.Close()
		t.Fatalf("%s hi 失败: ctrl=%+v err=%v", idPrefix, hi, err)
	}
	if err := conn.WriteJSON(loginMessage(idPrefix+"-login", cfg)); err != nil {
		_ = conn.Close()
		t.Fatalf("%s 发送 login 失败: %v", idPrefix, err)
	}
	login, err := readWebSocketControl(conn, idPrefix+"-login")
	if err != nil || login.Code != http.StatusOK {
		_ = conn.Close()
		t.Fatalf("%s login 失败: ctrl=%+v err=%v", idPrefix, login, err)
	}
	return conn
}

// pubContentMessage 创建可指定类型和正文、关闭发送者回显的发布请求。
func pubContentMessage(id, topic, clientID, kind string, content any) map[string]any {
	return map[string]any{
		"pub": map[string]any{
			"id": id, "topic": topic, "cid": clientID,
			"noecho": true, "kind": kind, "content": content,
		},
	}
}

// subExistingMessage 把当前 Session 挂载到已经存在的 Topic。
func subExistingMessage(id, topic string) map[string]any {
	return map[string]any{"sub": map[string]any{"id": id, "topic": topic}}
}

// deleteMessage 创建物理删除一条消息的请求。
func deleteMessage(id, topic string, seq int) map[string]any {
	return map[string]any{
		"del": map[string]any{
			"id": id, "topic": topic, "what": "msg", "hard": true,
			"delseq": []map[string]any{{"low": seq}},
		},
	}
}

// readWebSocketHistory 发起正向历史查询并读取到匹配的最终 ctrl。
func readWebSocketHistory(
	t *testing.T,
	conn *websocket.Conn,
	topic string,
	requestID string,
	since int,
	limit int,
) []*wireData {
	t.Helper()
	request := map[string]any{
		"get": map[string]any{
			"id": requestID, "topic": topic, "what": "data",
			"data": map[string]any{"since": since, "limit": limit, "forward": true},
		},
	}
	if err := conn.WriteJSON(request); err != nil {
		t.Fatalf("发送历史查询 %s 失败: %v", requestID, err)
	}

	history := make([]*wireData, 0, limit)
	for attempt := 0; attempt < limit+100; attempt++ {
		var envelope wireServerMessage
		if err := conn.ReadJSON(&envelope); err != nil {
			t.Fatalf("读取历史查询 %s 失败: %v", requestID, err)
		}
		if envelope.Data != nil && envelope.Data.Topic == topic {
			history = append(history, envelope.Data)
		}
		if envelope.Ctrl != nil && envelope.Ctrl.ID == requestID {
			if envelope.Ctrl.Code != http.StatusAlreadyReported &&
				envelope.Ctrl.Code != http.StatusNoContent {
				t.Fatalf(
					"历史查询 %s 状态码=%d，响应=%s",
					requestID,
					envelope.Ctrl.Code,
					envelope.Ctrl.Text,
				)
			}
			return history
		}
	}
	t.Fatalf("历史查询 %s 未收到最终 ctrl", requestID)
	return nil
}

// controlIntParam 从控制响应的 JSON 参数中读取整数。
func controlIntParam(t *testing.T, ctrl *wireControl, key string) int {
	t.Helper()
	raw, found := ctrl.Params[key]
	if !found {
		t.Fatalf("控制响应缺少参数 %q: %+v", key, ctrl)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("控制响应参数 %q 不是整数: %v", key, err)
	}
	return value
}

// writePersistenceProbe 创建 Topic、发布消息，并在收到 ACK 后落盘定位信息。
func writePersistenceProbe(t *testing.T, conn *websocket.Conn, statePath string) {
	t.Helper()
	if err := conn.WriteJSON(subMessage("persistence-create")); err != nil {
		t.Fatalf("持久化探针创建 Topic 失败: %v", err)
	}
	sub, err := readWebSocketControl(conn, "persistence-create")
	if err != nil || sub.Code != http.StatusOK {
		t.Fatalf("持久化探针创建 Topic 失败: ctrl=%+v err=%v", sub, err)
	}
	topic := requireCreatedTopic(t, "持久化探针", sub.Topic)
	state := persistenceState{
		Topic:    topic,
		ClientID: fmt.Sprintf("restart-%d", time.Now().UnixNano()),
		Content:  "ACK 后跨进程恢复消息",
	}
	if err := conn.WriteJSON(pubContentMessage(
		"persistence-pub",
		state.Topic,
		state.ClientID,
		"text",
		state.Content,
	)); err != nil {
		t.Fatalf("持久化探针发布消息失败: %v", err)
	}
	ack, err := readWebSocketControl(conn, "persistence-pub")
	if err != nil || ack.Code != http.StatusAccepted {
		t.Fatalf("持久化探针 ACK 失败: ctrl=%+v err=%v", ack, err)
	}
	state.SeqID = controlIntParam(t, ack, "seq")

	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("编码持久化探针状态失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("创建持久化探针状态目录失败: %v", err)
	}
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		t.Fatalf("写入持久化探针状态失败: %v", err)
	}
	t.Logf("已记录 ACK 消息：topic=%s cid=%s seq=%d", state.Topic, state.ClientID, state.SeqID)
}

// verifyPersistenceProbe 在新服务进程中重新订阅并核对历史和幂等重试。
func verifyPersistenceProbe(t *testing.T, conn *websocket.Conn, statePath string) {
	t.Helper()
	encoded, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("读取持久化探针状态失败: %v", err)
	}
	var state persistenceState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatalf("解析持久化探针状态失败: %v", err)
	}
	if state.Topic == "" || state.ClientID == "" || state.SeqID <= 0 {
		t.Fatalf("持久化探针状态不完整: %+v", state)
	}

	if err := conn.WriteJSON(subExistingMessage("persistence-resub", state.Topic)); err != nil {
		t.Fatalf("重启后重新订阅 Topic 失败: %v", err)
	}
	sub, err := readWebSocketControl(conn, "persistence-resub")
	if err != nil || sub.Code != http.StatusOK {
		t.Fatalf("重启后重新订阅 Topic 失败: ctrl=%+v err=%v", sub, err)
	}
	history := readWebSocketHistory(
		t,
		conn,
		state.Topic,
		"persistence-history",
		state.SeqID,
		10,
	)
	found := false
	for _, message := range history {
		if message.SeqID != state.SeqID || message.ClientID != state.ClientID {
			continue
		}
		var content string
		if err := json.Unmarshal(message.Content, &content); err != nil {
			t.Fatalf("重启后消息正文不是文本: %v", err)
		}
		if content != state.Content {
			t.Fatalf("重启后消息正文=%q，期望=%q", content, state.Content)
		}
		found = true
		break
	}
	if !found {
		t.Fatalf(
			"重启后历史中未找到 ACK 消息：topic=%s cid=%s seq=%d",
			state.Topic,
			state.ClientID,
			state.SeqID,
		)
	}

	if err := conn.WriteJSON(pubContentMessage(
		"persistence-retry",
		state.Topic,
		state.ClientID,
		"text",
		state.Content,
	)); err != nil {
		t.Fatalf("重启后发送幂等重试失败: %v", err)
	}
	retry, err := readWebSocketControl(conn, "persistence-retry")
	if err != nil || retry.Code != http.StatusAlreadyReported {
		t.Fatalf("重启后幂等重试失败: ctrl=%+v err=%v", retry, err)
	}
	if retrySeq := controlIntParam(t, retry, "seq"); retrySeq != state.SeqID {
		t.Fatalf("重启后幂等重试 seq=%d，期望=%d", retrySeq, state.SeqID)
	}
	t.Logf("ACK 消息跨进程恢复通过：topic=%s cid=%s seq=%d", state.Topic, state.ClientID, state.SeqID)
}
