// Package cluster_test 提供依赖真实多节点进程的黑盒集群认证。
package cluster_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// clusterTestAPIKey 是测试 YAML 中 API Key 盐对应的开发测试 Key。
	clusterTestAPIKey = "AQEAAAABAAD_rAp4DJh05a1HAwFT3A6K"
	// clusterTestProtocolVersion 是当前黑盒客户端使用的协议版本。
	clusterTestProtocolVersion = "0.29"
	// clusterProductionSoakDuration 是生产发布认证不可降低的最短持续时间。
	clusterProductionSoakDuration = 72 * time.Hour
)

// processTestConfig 保存真实三节点入口和跨故障阶段状态文件。
type processTestConfig struct {
	// Node0HTTP 是发布者和故障恢复探针使用的节点入口。
	Node0HTTP string
	// Node1HTTP 是跨节点订阅者使用的节点入口。
	Node1HTTP string
	// Node2HTTP 是远端 Owner 向边缘 Session 投递使用的第三个节点入口。
	Node2HTTP string
	// StatePath 保存故障前已 ACK 消息和 Owner。
	StatePath string
}

// processState 保存 SIGKILL 前后需要核对的业务状态。
type processState struct {
	// Topic 是确定由远端节点持有的群组。
	Topic string `json:"topic"`
	// Owner 是故障注入脚本需要终止的远端 Owner。
	Owner string `json:"owner"`
	// ClientID 是故障前已经持久化并 ACK 的消息幂等键。
	ClientID string `json:"client_id"`
	// SeqID 是故障前 ACK 返回的 Topic 序列号。
	SeqID int `json:"seq_id"`
	// Content 是恢复后必须从历史中读回的正文。
	Content string `json:"content"`
}

// soakReport 是 72 小时稳定性任务持续覆盖写入的审计检查点。
type soakReport struct {
	// ReleaseID 是待认证的不可变镜像 digest 或发布版本。
	ReleaseID string `json:"release_id"`
	// StartedAt 是稳定性任务启动时间。
	StartedAt time.Time `json:"started_at"`
	// UpdatedAt 是最近一次成功检查点时间。
	UpdatedAt time.Time `json:"updated_at"`
	// PlannedDuration 是要求运行的总时长。
	PlannedDuration string `json:"planned_duration"`
	// Messages 是已经完成 ACK 和在线投递核对的消息数。
	Messages int64 `json:"messages"`
	// LastSeq 是最近一次确认的 Topic seq。
	LastSeq int `json:"last_seq"`
	// AckP99 是最近窗口发布 ACK p99。
	AckP99 string `json:"ack_p99"`
	// DeliveryP99 是最近窗口在线投递 p99。
	DeliveryP99 string `json:"delivery_p99"`
	// MaxAck 是任务开始以来最大的 ACK 延迟。
	MaxAck string `json:"max_ack"`
	// MaxDelivery 是任务开始以来最大的在线投递延迟。
	MaxDelivery string `json:"max_delivery"`
	// Status 是 running、failed 或 passed。
	Status string `json:"status"`
}

// wireEnvelope 是测试关心的最小服务端 JSON 信封。
type wireEnvelope struct {
	// Ctrl 保存命令响应。
	Ctrl *wireControl `json:"ctrl"`
	// Data 保存实时投递或历史消息。
	Data *wireData `json:"data"`
}

// wireControl 保存请求关联、状态码和扩展参数。
type wireControl struct {
	// ID 是客户端请求 ID。
	ID string `json:"id"`
	// Topic 是服务端解析后的 Topic。
	Topic string `json:"topic"`
	// Code 是 HTTP 风格业务状态码。
	Code int `json:"code"`
	// Text 是稳定响应文本。
	Text string `json:"text"`
	// Params 保存 seq 等响应参数。
	Params map[string]json.RawMessage `json:"params"`
}

// wireData 保存消息恢复和跨节点投递需要核对的字段。
type wireData struct {
	// Topic 是消息所属群组。
	Topic string `json:"topic"`
	// SeqID 是 Topic 内单调序列号。
	SeqID int `json:"seq"`
	// ClientID 是客户端发布幂等键。
	ClientID string `json:"cid"`
	// Content 是原始 JSON 正文。
	Content json.RawMessage `json:"content"`
}

// TestClusterCrossNodeRouting 创建一个由远端 Owner 持有的 Topic，并验证
// node-0 发布、远端 Owner 持久化、node-1 Session 收取的完整双向集群跳转。
func TestClusterCrossNodeRouting(t *testing.T) {
	config := loadProcessTestConfig(t)
	contextValue, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 服务端会为 "new" 反复生成名称直到 Topic 属于接入节点。因此从
	// im-1 建群可以确定 Owner=im-1，避免依赖随机 UID 碰撞远端 Ring。
	creator := openProcessWebSocket(t, contextValue, config.Node1HTTP, "creator")
	if err := creator.WriteJSON(map[string]any{
		"sub": map[string]any{"id": "create", "topic": "new"},
	}); err != nil {
		t.Fatalf("创建远端 Owner Topic 失败: %v", err)
	}
	created := requireControl(t, creator, "create")
	if created.Code != http.StatusOK || !strings.HasPrefix(created.Topic, "grp") {
		t.Fatalf("建群响应无效：%+v", created)
	}
	topic := created.Topic
	_ = creator.Close()

	publisher := openProcessWebSocket(t, contextValue, config.Node0HTTP, "publisher")
	defer publisher.Close()
	if err := publisher.WriteJSON(map[string]any{
		"sub": map[string]any{"id": "publisher-sub", "topic": topic},
	}); err != nil {
		t.Fatalf("发布边缘节点订阅失败: %v", err)
	}
	if control := requireControl(t, publisher, "publisher-sub"); control.Code != http.StatusOK {
		t.Fatalf("发布边缘节点订阅状态码=%d，响应=%s", control.Code, control.Text)
	}

	subscriber := openProcessWebSocket(t, contextValue, config.Node2HTTP, "subscriber")
	defer subscriber.Close()
	if err := subscriber.WriteJSON(map[string]any{
		"sub": map[string]any{"id": "subscribe", "topic": topic},
	}); err != nil {
		t.Fatalf("跨节点订阅失败: %v", err)
	}
	if control := requireControl(t, subscriber, "subscribe"); control.Code != http.StatusOK {
		t.Fatalf("跨节点订阅状态码=%d，响应=%s", control.Code, control.Text)
	}

	state := processState{
		Topic:    topic,
		Owner:    "im-1",
		ClientID: fmt.Sprintf("before-failure-%d", time.Now().UnixNano()),
		Content:  "故障前已 ACK 的跨节点消息",
	}
	if err := publisher.WriteJSON(map[string]any{
		"pub": map[string]any{
			"id": "publish-before-failure", "topic": topic,
			"cid": state.ClientID, "kind": "text", "content": state.Content,
			"noecho": true,
		},
	}); err != nil {
		t.Fatalf("发布故障前消息失败: %v", err)
	}
	ack := requireControl(t, publisher, "publish-before-failure")
	if ack.Code != http.StatusAccepted {
		t.Fatalf("故障前发布状态码=%d，响应=%s", ack.Code, ack.Text)
	}
	state.SeqID = requireIntParam(t, ack, "seq")
	data := requireData(t, subscriber, topic, state.ClientID)
	if data.SeqID != state.SeqID || string(data.Content) != `"`+state.Content+`"` {
		t.Fatalf("跨节点投递不一致：data=%+v state=%+v", data, state)
	}
	writeProcessState(t, config.StatePath, state)
	t.Logf("跨节点链路通过：topic=%s owner=im-1 seq=%d", topic, state.SeqID)
}

// TestClusterFailoverRecovery 验证远端 Owner 被 SIGKILL 后，已 ACK 消息仍可读，
// 且迁移后的新 Owner 可以继续分配更大的 seq。
func TestClusterFailoverRecovery(t *testing.T) {
	config := loadProcessTestConfig(t)
	state := readProcessState(t, config.StatePath)
	contextValue, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	connection := openProcessWebSocket(t, contextValue, config.Node0HTTP, "recovery")
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{
		"sub": map[string]any{"id": "recovery-sub", "topic": state.Topic},
	}); err != nil {
		t.Fatalf("故障后重新订阅失败: %v", err)
	}
	if control := requireControl(t, connection, "recovery-sub"); control.Code != http.StatusOK {
		t.Fatalf("故障后订阅状态码=%d，响应=%s", control.Code, control.Text)
	}

	history := readHistory(t, connection, state.Topic, "recovery-history")
	found := false
	for _, message := range history {
		if message.SeqID == state.SeqID &&
			message.ClientID == state.ClientID &&
			string(message.Content) == `"`+state.Content+`"` {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Owner SIGKILL 后历史中缺少已 ACK 消息：%+v", state)
	}

	clientID := fmt.Sprintf("after-failure-%d", time.Now().UnixNano())
	if err := connection.WriteJSON(map[string]any{
		"pub": map[string]any{
			"id": "publish-after-failure", "topic": state.Topic,
			"cid": clientID, "kind": "text", "content": "故障迁移后的消息",
			"noecho": true,
		},
	}); err != nil {
		t.Fatalf("故障迁移后发布失败: %v", err)
	}
	ack := requireControl(t, connection, "publish-after-failure")
	if ack.Code != http.StatusAccepted {
		t.Fatalf("故障迁移后发布状态码=%d，响应=%s", ack.Code, ack.Text)
	}
	if seq := requireIntParam(t, ack, "seq"); seq <= state.SeqID {
		t.Fatalf("故障迁移后 seq=%d，没有大于旧 seq=%d", seq, state.SeqID)
	}
}

// TestClusterSoak 在真实 staging 上持续执行跨节点 ACK、投递、seq 和 cid
// 幂等核对。默认 go test 不运行，发布脚本必须显式提供持续时间和报告路径。
func TestClusterSoak(t *testing.T) {
	rawDuration := strings.TrimSpace(os.Getenv("IM_TEST_CLUSTER_SOAK_DURATION"))
	if rawDuration == "" {
		t.Skip("未设置 IM_TEST_CLUSTER_SOAK_DURATION，跳过持续稳定性认证")
	}
	duration, err := time.ParseDuration(rawDuration)
	if err != nil || duration < time.Minute {
		t.Fatalf("IM_TEST_CLUSTER_SOAK_DURATION=%q 必须是不小于 1m 的时长", rawDuration)
	}
	if os.Getenv("IM_TEST_CLUSTER_REQUIRE_PRODUCTION_DURATION") == "1" &&
		duration < clusterProductionSoakDuration {
		t.Fatalf(
			"生产认证时长=%s，小于不可降低的门禁 %s",
			duration,
			clusterProductionSoakDuration,
		)
	}
	reportPath := strings.TrimSpace(os.Getenv("IM_TEST_CLUSTER_SOAK_REPORT"))
	if reportPath == "" {
		t.Fatal("持续稳定性认证必须设置 IM_TEST_CLUSTER_SOAK_REPORT")
	}
	messagesPerSecond := positiveEnvironmentInt(t, "IM_TEST_CLUSTER_SOAK_QPS", 10)
	checkpointInterval := positiveEnvironmentDuration(
		t,
		"IM_TEST_CLUSTER_SOAK_CHECKPOINT",
		time.Minute,
	)
	ackLimit := positiveEnvironmentDuration(t, "IM_TEST_ACK_P99_MAX", 300*time.Millisecond)
	deliveryLimit := positiveEnvironmentDuration(
		t,
		"IM_TEST_DELIVERY_P99_MAX",
		500*time.Millisecond,
	)
	config := loadSoakTestConfig(t)
	contextValue, cancel := context.WithTimeout(context.Background(), duration+time.Minute)
	defer cancel()

	creator := openProcessWebSocket(t, contextValue, config.Node1HTTP, "soak-creator")
	if err = creator.WriteJSON(map[string]any{
		"sub": map[string]any{"id": "soak-create", "topic": "new"},
	}); err != nil {
		t.Fatal(err)
	}
	created := requireControl(t, creator, "soak-create")
	if created.Code != http.StatusOK || !strings.HasPrefix(created.Topic, "grp") {
		t.Fatalf("持续测试建群失败：%+v", created)
	}
	topic := created.Topic
	_ = creator.Close()

	publisher := openProcessWebSocket(t, contextValue, config.Node0HTTP, "soak-publisher")
	defer publisher.Close()
	subscriber := openProcessWebSocket(t, contextValue, config.Node2HTTP, "soak-subscriber")
	defer subscriber.Close()
	for index, connection := range []*websocket.Conn{publisher, subscriber} {
		requestID := "soak-sub-" + strconv.Itoa(index)
		if err = connection.WriteJSON(map[string]any{
			"sub": map[string]any{"id": requestID, "topic": topic},
		}); err != nil {
			t.Fatal(err)
		}
		if control := requireControl(t, connection, requestID); control.Code != http.StatusOK {
			t.Fatalf("持续测试订阅状态码=%d，响应=%s", control.Code, control.Text)
		}
	}

	startedAt := time.Now().UTC()
	deadline := time.Now().Add(duration)
	nextCheckpoint := time.Now().Add(checkpointInterval)
	interval := time.Second / time.Duration(messagesPerSecond)
	nextMessage := time.Now()
	report := soakReport{
		ReleaseID:       strings.TrimSpace(os.Getenv("IM_TEST_CLUSTER_RELEASE_ID")),
		StartedAt:       startedAt,
		UpdatedAt:       startedAt,
		PlannedDuration: duration.String(),
		Status:          "running",
	}
	if report.ReleaseID == "" {
		t.Fatal("持续稳定性认证必须设置 IM_TEST_CLUSTER_RELEASE_ID")
	}
	defer func() {
		if report.Status == "passed" {
			return
		}
		report.UpdatedAt = time.Now().UTC()
		report.Status = "failed"
		writeSoakReport(t, reportPath, report)
	}()
	writeSoakReport(t, reportPath, report)

	ackSamples := make([]time.Duration, 0, messagesPerSecond*int(checkpointInterval/time.Second))
	deliverySamples := make([]time.Duration, 0, cap(ackSamples))
	var previousSeq int
	for time.Now().Before(deadline) {
		if wait := time.Until(nextMessage); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-contextValue.Done():
				if !timer.Stop() {
					<-timer.C
				}
				t.Fatal(contextValue.Err())
			}
		}
		nextMessage = nextMessage.Add(interval)
		requestID := fmt.Sprintf("soak-pub-%d", report.Messages)
		clientID := fmt.Sprintf("soak-%d-%d", startedAt.UnixNano(), report.Messages)
		sentAt := time.Now()
		_ = publisher.SetReadDeadline(time.Now().Add(15 * time.Second))
		_ = subscriber.SetReadDeadline(time.Now().Add(15 * time.Second))
		if err = publisher.WriteJSON(map[string]any{
			"pub": map[string]any{
				"id": requestID, "topic": topic, "cid": clientID,
				"kind": "text", "content": "cluster soak", "noecho": true,
			},
		}); err != nil {
			t.Fatalf("持续测试发布第 %d 条失败: %v", report.Messages, err)
		}
		ack := requireControl(t, publisher, requestID)
		ackDuration := time.Since(sentAt)
		if ack.Code != http.StatusAccepted {
			t.Fatalf("持续测试 ACK=%d，响应=%s", ack.Code, ack.Text)
		}
		seq := requireIntParam(t, ack, "seq")
		if previousSeq > 0 && seq != previousSeq+1 {
			t.Fatalf("持续测试 seq 从 %d 跳到 %d", previousSeq, seq)
		}
		data := requireData(t, subscriber, topic, clientID)
		deliveryDuration := time.Since(sentAt)
		if data.SeqID != seq {
			t.Fatalf("持续测试 ACK seq=%d，投递 seq=%d", seq, data.SeqID)
		}
		previousSeq = seq
		report.Messages++
		report.LastSeq = seq
		ackSamples = append(ackSamples, ackDuration)
		deliverySamples = append(deliverySamples, deliveryDuration)
		if ackDuration > parseReportDuration(report.MaxAck) {
			report.MaxAck = ackDuration.String()
		}
		if deliveryDuration > parseReportDuration(report.MaxDelivery) {
			report.MaxDelivery = deliveryDuration.String()
		}

		// 每一千条使用相同 cid 重试，必须返回 208 且不能推进 seq。
		if report.Messages%1000 == 0 {
			retryID := requestID + "-retry"
			if err = publisher.WriteJSON(map[string]any{
				"pub": map[string]any{
					"id": retryID, "topic": topic, "cid": clientID,
					"kind": "text", "content": "cluster soak", "noecho": true,
				},
			}); err != nil {
				t.Fatal(err)
			}
			retry := requireControl(t, publisher, retryID)
			if retry.Code != http.StatusAlreadyReported ||
				requireIntParam(t, retry, "seq") != seq {
				t.Fatalf("持续测试 cid 幂等响应异常：%+v", retry)
			}
		}

		if !time.Now().Before(nextCheckpoint) {
			report.AckP99 = durationPercentile(ackSamples, 0.99).String()
			report.DeliveryP99 = durationPercentile(deliverySamples, 0.99).String()
			if parseReportDuration(report.AckP99) > ackLimit {
				t.Fatalf("持续测试 ACK p99=%s 超过 %s", report.AckP99, ackLimit)
			}
			if parseReportDuration(report.DeliveryP99) > deliveryLimit {
				t.Fatalf(
					"持续测试投递 p99=%s 超过 %s",
					report.DeliveryP99,
					deliveryLimit,
				)
			}
			report.UpdatedAt = time.Now().UTC()
			writeSoakReport(t, reportPath, report)
			t.Logf(
				"稳定性检查点：消息=%d seq=%d ACK p99=%s 投递 p99=%s",
				report.Messages,
				report.LastSeq,
				report.AckP99,
				report.DeliveryP99,
			)
			ackSamples = ackSamples[:0]
			deliverySamples = deliverySamples[:0]
			nextCheckpoint = time.Now().Add(checkpointInterval)
		}
	}
	if len(ackSamples) > 0 {
		report.AckP99 = durationPercentile(ackSamples, 0.99).String()
		report.DeliveryP99 = durationPercentile(deliverySamples, 0.99).String()
	}
	if parseReportDuration(report.AckP99) > ackLimit {
		t.Fatalf("最终窗口 ACK p99=%s 超过 %s", report.AckP99, ackLimit)
	}
	if parseReportDuration(report.DeliveryP99) > deliveryLimit {
		t.Fatalf("最终窗口投递 p99=%s 超过 %s", report.DeliveryP99, deliveryLimit)
	}
	report.UpdatedAt = time.Now().UTC()
	report.Status = "passed"
	writeSoakReport(t, reportPath, report)
}

// loadProcessTestConfig 从环境加载进程级测试入口；未配置时跳过普通 go test。
func loadProcessTestConfig(t *testing.T) processTestConfig {
	t.Helper()
	config := processTestConfig{
		Node0HTTP: strings.TrimRight(os.Getenv("IM_TEST_CLUSTER_NODE0_HTTP"), "/"),
		Node1HTTP: strings.TrimRight(os.Getenv("IM_TEST_CLUSTER_NODE1_HTTP"), "/"),
		Node2HTTP: strings.TrimRight(os.Getenv("IM_TEST_CLUSTER_NODE2_HTTP"), "/"),
		StatePath: strings.TrimSpace(os.Getenv("IM_TEST_CLUSTER_STATE")),
	}
	if config.Node0HTTP == "" {
		t.Skip("未设置 IM_TEST_CLUSTER_NODE0_HTTP，跳过真实集群进程测试")
	}
	if config.Node1HTTP == "" || config.Node2HTTP == "" || config.StatePath == "" {
		t.Fatal("真实集群进程测试必须设置 node-1、node-2 入口和状态文件")
	}
	return config
}

// loadSoakTestConfig 只要求三个真实 staging 入口，不依赖本地故障状态文件。
func loadSoakTestConfig(t *testing.T) processTestConfig {
	t.Helper()
	config := processTestConfig{
		Node0HTTP: strings.TrimRight(os.Getenv("IM_TEST_CLUSTER_NODE0_HTTP"), "/"),
		Node1HTTP: strings.TrimRight(os.Getenv("IM_TEST_CLUSTER_NODE1_HTTP"), "/"),
		Node2HTTP: strings.TrimRight(os.Getenv("IM_TEST_CLUSTER_NODE2_HTTP"), "/"),
	}
	if config.Node0HTTP == "" || config.Node1HTTP == "" || config.Node2HTTP == "" {
		t.Fatal("持续稳定性认证必须设置三个 staging 节点 HTTP 入口")
	}
	return config
}

// openProcessWebSocket 建立连接并完成 hi 与 basic 登录。
func openProcessWebSocket(
	t *testing.T,
	contextValue context.Context,
	httpBase string,
	idPrefix string,
) *websocket.Conn {
	t.Helper()
	endpoint := strings.TrimPrefix(httpBase, "http://")
	webSocketScheme := "ws"
	if strings.HasPrefix(httpBase, "https://") {
		endpoint = strings.TrimPrefix(httpBase, "https://")
		webSocketScheme = "wss"
	}
	apiKey := strings.TrimSpace(os.Getenv("IM_TEST_CLUSTER_API_KEY"))
	if apiKey == "" {
		apiKey = clusterTestAPIKey
	}
	username := strings.TrimSpace(os.Getenv("IM_TEST_CLUSTER_USERNAME"))
	if username == "" {
		username = "alice"
	}
	password := os.Getenv("IM_TEST_CLUSTER_PASSWORD")
	if password == "" {
		password = "alice123"
	}
	query := url.Values{"apikey": []string{apiKey}}
	connection, _, err := websocket.DefaultDialer.DialContext(
		contextValue,
		webSocketScheme+"://"+endpoint+"/v0/channels?"+query.Encode(),
		nil,
	)
	if err != nil {
		t.Fatalf("%s 连接 WebSocket 失败: %v", idPrefix, err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err = connection.WriteJSON(map[string]any{
		"hi": map[string]any{
			"id": idPrefix + "-hi", "ua": "cluster-process-e2e",
			"ver": clusterTestProtocolVersion, "lang": "zh",
		},
	}); err != nil {
		_ = connection.Close()
		t.Fatalf("%s 发送 hi 失败: %v", idPrefix, err)
	}
	if control := requireControl(t, connection, idPrefix+"-hi"); control.Code != http.StatusCreated {
		_ = connection.Close()
		t.Fatalf("%s hi 状态码=%d", idPrefix, control.Code)
	}
	if err = connection.WriteJSON(map[string]any{
		"login": map[string]any{
			"id": idPrefix + "-login", "scheme": "basic",
			"secret": []byte(username + ":" + password),
		},
	}); err != nil {
		_ = connection.Close()
		t.Fatalf("%s 发送 login 失败: %v", idPrefix, err)
	}
	if control := requireControl(t, connection, idPrefix+"-login"); control.Code != http.StatusOK {
		_ = connection.Close()
		t.Fatalf("%s login 状态码=%d，响应=%s", idPrefix, control.Code, control.Text)
	}
	return connection
}

// requireControl 跳过异步事件，直到收到指定请求 ID 的控制响应。
func requireControl(t *testing.T, connection *websocket.Conn, requestID string) *wireControl {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		var envelope wireEnvelope
		if err := connection.ReadJSON(&envelope); err != nil {
			t.Fatalf("读取请求 %s 的控制响应失败: %v", requestID, err)
		}
		if envelope.Ctrl != nil && envelope.Ctrl.ID == requestID {
			return envelope.Ctrl
		}
	}
	t.Fatalf("未收到请求 %s 的控制响应", requestID)
	return nil
}

// requireData 等待指定 Topic 和 Client ID 的实时消息。
func requireData(
	t *testing.T,
	connection *websocket.Conn,
	topic string,
	clientID string,
) *wireData {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		var envelope wireEnvelope
		if err := connection.ReadJSON(&envelope); err != nil {
			t.Fatalf("读取跨节点实时消息失败: %v", err)
		}
		if envelope.Data != nil &&
			envelope.Data.Topic == topic &&
			envelope.Data.ClientID == clientID {
			return envelope.Data
		}
	}
	t.Fatalf("未收到 topic=%s cid=%s 的跨节点消息", topic, clientID)
	return nil
}

// readHistory 读取正向历史直到服务端返回最终控制响应。
func readHistory(
	t *testing.T,
	connection *websocket.Conn,
	topic string,
	requestID string,
) []*wireData {
	t.Helper()
	if err := connection.WriteJSON(map[string]any{
		"get": map[string]any{
			"id": requestID, "topic": topic, "what": "data",
			"data": map[string]any{"since": 1, "limit": 100, "forward": true},
		},
	}); err != nil {
		t.Fatalf("发送恢复历史查询失败: %v", err)
	}
	history := make([]*wireData, 0, 16)
	for attempt := 0; attempt < 300; attempt++ {
		var envelope wireEnvelope
		if err := connection.ReadJSON(&envelope); err != nil {
			t.Fatalf("读取恢复历史失败: %v", err)
		}
		if envelope.Data != nil && envelope.Data.Topic == topic {
			history = append(history, envelope.Data)
		}
		if envelope.Ctrl != nil && envelope.Ctrl.ID == requestID {
			if envelope.Ctrl.Code != http.StatusAlreadyReported &&
				envelope.Ctrl.Code != http.StatusNoContent {
				t.Fatalf("恢复历史状态码=%d，响应=%s", envelope.Ctrl.Code, envelope.Ctrl.Text)
			}
			return history
		}
	}
	t.Fatal("恢复历史查询未收到最终控制响应")
	return nil
}

// requireIntParam 从控制响应中解析整数参数。
func requireIntParam(t *testing.T, control *wireControl, name string) int {
	t.Helper()
	payload, exists := control.Params[name]
	if !exists {
		t.Fatalf("控制响应缺少参数 %q：%+v", name, control)
	}
	var value int
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("控制响应参数 %q 不是整数: %v", name, err)
	}
	return value
}

// writeProcessState 以仅当前用户可读写权限保存故障阶段状态。
func writeProcessState(t *testing.T, statePath string, state processState) {
	t.Helper()
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(statePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

// readProcessState 加载故障前写入的业务状态。
func readProcessState(t *testing.T, statePath string) processState {
	t.Helper()
	payload, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state processState
	if err = json.Unmarshal(payload, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// positiveEnvironmentInt 读取正整数环境参数。
func positiveEnvironmentInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	rawValue := strings.TrimSpace(os.Getenv(name))
	if rawValue == "" {
		return fallback
	}
	value, err := strconv.Atoi(rawValue)
	if err != nil || value <= 0 {
		t.Fatalf("%s=%q 不是正整数", name, rawValue)
	}
	return value
}

// positiveEnvironmentDuration 读取正时长环境参数。
func positiveEnvironmentDuration(
	t *testing.T,
	name string,
	fallback time.Duration,
) time.Duration {
	t.Helper()
	rawValue := strings.TrimSpace(os.Getenv(name))
	if rawValue == "" {
		return fallback
	}
	value, err := time.ParseDuration(rawValue)
	if err != nil || value <= 0 {
		t.Fatalf("%s=%q 不是正时长", name, rawValue)
	}
	return value
}

// durationPercentile 返回排序后的最近窗口分位值。
func durationPercentile(samples []time.Duration, quantile float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sortedSamples := append([]time.Duration(nil), samples...)
	sort.Slice(sortedSamples, func(left, right int) bool {
		return sortedSamples[left] < sortedSamples[right]
	})
	index := int(float64(len(sortedSamples)-1) * quantile)
	return sortedSamples[index]
}

// parseReportDuration 把尚未写入的空报告字段视为零。
func parseReportDuration(value string) time.Duration {
	duration, _ := time.ParseDuration(value)
	return duration
}

// writeSoakReport 原子替换 JSON 检查点，避免任务中断留下半个文件。
func writeSoakReport(t *testing.T, reportPath string, report soakReport) {
	t.Helper()
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(reportPath), 0o700); err != nil {
		t.Fatal(err)
	}
	temporaryPath := reportPath + ".tmp"
	if err = os.WriteFile(temporaryPath, append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(temporaryPath, reportPath); err != nil {
		t.Fatal(err)
	}
}
