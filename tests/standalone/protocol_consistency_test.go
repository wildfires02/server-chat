// Package standalone_test 提供依赖真实单机进程的黑盒回归测试。
package standalone_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"chat/api/pbx"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// defaultStandaloneAPIKey 是 configs/im.standalone.yaml 示例盐对应的开发 API Key。
	defaultStandaloneAPIKey = "AQEAAAABAAD_rAp4DJh05a1HAwFT3A6K"
	// protocolVersion 是当前服务端测试使用的客户端协议版本。
	protocolVersion = "0.29"
)

// integrationConfig 保存真实单机进程测试所需的入口地址和开发凭据。
type integrationConfig struct {
	// httpBase 是 WebSocket 和 Long Polling 共用的 HTTP 根地址。
	httpBase string
	// grpcAddr 是客户端 gRPC 服务地址。
	grpcAddr string
	// apiKey 是 JSON 协议入口要求的开发 API Key。
	apiKey string
	// username 和 password 是初始化测试数据中的 basic 凭据。
	username string
	password string
}

// wireServerMessage 是 WebSocket 和 Long Polling 共用的最小服务端信封。
type wireServerMessage struct {
	// Ctrl 保存控制响应；测试只比较跨协议稳定的控制语义。
	Ctrl *wireControl `json:"ctrl"`
	// Data 保存实时投递或历史查询返回的消息。
	Data *wireData `json:"data"`
}

// wireData 保存 JSON 协议中消息生命周期测试所需的服务端数据字段。
type wireData struct {
	// Topic 是消息所属的会话。
	Topic string `json:"topic"`
	// From 是消息发送者的公开用户 ID。
	From string `json:"from"`
	// SeqID 是服务端为 Topic 分配的单调序列号。
	SeqID int `json:"seq"`
	// ClientID 是客户端发布时提供的持久化幂等键。
	ClientID string `json:"cid"`
	// Kind 是服务端根据正文推导出的可信消息类型。
	Kind string `json:"kind"`
	// Content 保存原始 JSON 正文，便于验证历史内容未被传输层改写。
	Content json.RawMessage `json:"content"`
}

// wireControl 保存 JSON 协议中的控制响应。
type wireControl struct {
	// ID 关联客户端请求。
	ID string `json:"id"`
	// Topic 是请求处理后解析出的真实 Topic 名称。
	Topic string `json:"topic"`
	// Code 是 HTTP 风格的业务状态码。
	Code int `json:"code"`
	// Text 是状态码对应的稳定文本。
	Text string `json:"text"`
	// Params 保存登录用户、令牌等附加参数。
	Params map[string]json.RawMessage `json:"params"`
}

// observedControl 是屏蔽传输编码差异后的统一控制响应。
type observedControl struct {
	// Code 是 HTTP 风格的业务状态码。
	Code int
	// Text 是跨传输保持一致的状态文本。
	Text string
	// ParamKeys 只保存参数名，不比较每次请求都会变化的令牌值。
	ParamKeys []string
}

// runtimeSnapshot 保存 expvar 中与连接容量直接相关的运行时指标。
type runtimeSnapshot struct {
	// LiveSessions 是采样时服务端保存的活动 Session 数。
	LiveSessions int64 `json:"LiveSessions"`
	// NumGoroutines 是采样时 Go 运行时的协程数。
	NumGoroutines int `json:"NumGoroutines"`
	// Memstats 保存 Go 堆和垃圾回收器的容量指标。
	Memstats struct {
		// Alloc 是仍然存活的堆对象字节数。
		Alloc uint64 `json:"Alloc"`
		// HeapInuse 是当前已分配给堆跨度的字节数，受单次 GC 影响小于 Alloc。
		HeapInuse uint64 `json:"HeapInuse"`
		// NumGC 是进程启动以来完成的 GC 周期数。
		NumGC uint32 `json:"NumGC"`
		// PauseTotalNs 是进程启动以来全部 STW 暂停的累计纳秒数。
		PauseTotalNs uint64 `json:"PauseTotalNs"`
		// PauseNs 保存最近 256 次 GC 的 STW 暂停时间。
		PauseNs [256]uint64 `json:"PauseNs"`
	} `json:"memstats"`
}

// TestStandaloneProtocolConsistency 验证真实单机进程的 WebSocket、
// Long Polling 和 gRPC 对握手、登录、建群和发布返回一致的业务语义。
func TestStandaloneProtocolConsistency(t *testing.T) {
	cfg := loadIntegrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wsHi, wsLogin, wsSub, wsPub, wsRetry := runWebSocketFlow(t, ctx, cfg)
	lpHi, lpLogin, lpSub, lpPub, lpRetry := runLongPollFlow(t, ctx, cfg)
	grpcHi, grpcLogin, grpcSub, grpcPub, grpcRetry := runGRPCFlow(t, ctx, cfg)

	assertSuccessfulControl(t, "WebSocket hi", wsHi, http.StatusCreated)
	assertSuccessfulControl(t, "Long Polling hi", lpHi, http.StatusCreated)
	assertSuccessfulControl(t, "gRPC hi", grpcHi, http.StatusCreated)
	assertSameControl(t, "hi", wsHi, lpHi, grpcHi)

	assertSuccessfulLogin(t, "WebSocket login", wsLogin)
	assertSuccessfulLogin(t, "Long Polling login", lpLogin)
	assertSuccessfulLogin(t, "gRPC login", grpcLogin)
	assertSameControl(t, "login", wsLogin, lpLogin, grpcLogin)

	assertSuccessfulControl(t, "WebSocket sub", wsSub, http.StatusOK)
	assertSuccessfulControl(t, "Long Polling sub", lpSub, http.StatusOK)
	assertSuccessfulControl(t, "gRPC sub", grpcSub, http.StatusOK)
	assertSameControl(t, "sub", wsSub, lpSub, grpcSub)

	assertSuccessfulControl(t, "WebSocket pub", wsPub, http.StatusAccepted)
	assertSuccessfulControl(t, "Long Polling pub", lpPub, http.StatusAccepted)
	assertSuccessfulControl(t, "gRPC pub", grpcPub, http.StatusAccepted)
	assertSameControl(t, "pub", wsPub, lpPub, grpcPub)

	assertSuccessfulControl(t, "WebSocket retry", wsRetry, http.StatusAlreadyReported)
	assertSuccessfulControl(t, "Long Polling retry", lpRetry, http.StatusAlreadyReported)
	assertSuccessfulControl(t, "gRPC retry", grpcRetry, http.StatusAlreadyReported)
	assertSameControl(t, "idempotent retry", wsRetry, lpRetry, grpcRetry)
}

// TestStandaloneWebSocketReconnectBurst 模拟客户端同时断线重连，验证握手阶段
// 没有错误响应、连接泄漏或因单个连接失败导致的级联失败。
func TestStandaloneWebSocketReconnectBurst(t *testing.T) {
	cfg := loadIntegrationConfig(t)
	connectionCount := positiveEnvInt(t, "IM_TEST_STANDALONE_RECONNECTS", 64)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	started := time.Now()
	durations := make(chan time.Duration, connectionCount)
	errorsFound := make(chan error, connectionCount)
	var workers sync.WaitGroup
	for index := 0; index < connectionCount; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			connectionStarted := time.Now()
			id := fmt.Sprintf("reconnect-%d", worker)
			conn, _, err := websocket.DefaultDialer.DialContext(ctx, websocketURL(cfg), nil)
			if err != nil {
				errorsFound <- fmt.Errorf("连接 %d 建立失败: %w", worker, err)
				return
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(hiMessage(id)); err != nil {
				errorsFound <- fmt.Errorf("连接 %d 发送 hi 失败: %w", worker, err)
				return
			}
			ctrl, err := readWebSocketControl(conn, id)
			if err != nil {
				errorsFound <- fmt.Errorf("连接 %d 读取 hi 失败: %w", worker, err)
				return
			}
			if ctrl.Code != http.StatusCreated {
				errorsFound <- fmt.Errorf("连接 %d hi 状态码=%d，期望 201", worker, ctrl.Code)
				return
			}
			durations <- time.Since(connectionStarted)
		}(index)
	}
	workers.Wait()
	close(errorsFound)
	close(durations)

	for err := range errorsFound {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	observed := collectSortedDurations(durations)
	t.Logf(
		"重连突发完成：连接=%d，总耗时=%s，p95=%s，最大=%s",
		connectionCount,
		time.Since(started),
		percentile(observed, 0.95),
		observed[len(observed)-1],
	)
}

// TestStandaloneIdleWebSocketConnections 建立一批真实空闲连接并保持读取循环，
// 验证服务端 Ping、客户端 Pong、Session 存活以及 GC 暂停基线。
func TestStandaloneIdleWebSocketConnections(t *testing.T) {
	cfg := loadIntegrationConfig(t)
	connectionCount := positiveEnvInt(t, "IM_TEST_STANDALONE_IDLE_CONNECTIONS", 128)
	holdDuration := positiveEnvDuration(t, "IM_TEST_STANDALONE_IDLE_HOLD", 500*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second+holdDuration)
	defer cancel()
	before := readRuntimeSnapshot(t, ctx, cfg)

	connections := make([]*websocket.Conn, 0, connectionCount)
	var readers sync.WaitGroup
	t.Cleanup(func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
		readers.Wait()
	})

	started := time.Now()
	for index := 0; index < connectionCount; index++ {
		id := fmt.Sprintf("idle-%d", index)
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, websocketURL(cfg), nil)
		if err != nil {
			t.Fatalf("第 %d 个空闲连接建立失败: %v", index, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteJSON(hiMessage(id)); err != nil {
			_ = conn.Close()
			t.Fatalf("第 %d 个空闲连接发送 hi 失败: %v", index, err)
		}
		ctrl, err := readWebSocketControl(conn, id)
		if err != nil {
			_ = conn.Close()
			t.Fatalf("第 %d 个空闲连接读取 hi 失败: %v", index, err)
		}
		if ctrl.Code != http.StatusCreated {
			_ = conn.Close()
			t.Fatalf("第 %d 个空闲连接 hi 状态码=%d，期望 201", index, ctrl.Code)
		}
		_ = conn.SetReadDeadline(time.Time{})
		connections = append(connections, conn)
	}

	setupDuration := time.Since(started)
	earlyDisconnects := make(chan error, connectionCount)
	for index, conn := range connections {
		readers.Add(1)
		go func(connectionIndex int, connection *websocket.Conn) {
			defer readers.Done()
			for {
				if _, _, err := connection.ReadMessage(); err != nil {
					// 测试结束主动关闭连接会使读取返回错误，只记录保持窗口内的提前断开。
					select {
					case <-ctx.Done():
					default:
						earlyDisconnects <- fmt.Errorf(
							"第 %d 个空闲连接在保持窗口内断开: %w",
							connectionIndex,
							err,
						)
					}
					return
				}
			}
		}(index, conn)
	}

	select {
	case <-time.After(holdDuration):
	case <-ctx.Done():
		t.Fatalf("保持空闲连接时超时: %v", ctx.Err())
	}

	after := readRuntimeSnapshot(t, ctx, cfg)
	sessionDelta := after.LiveSessions - before.LiveSessions
	if sessionDelta < int64(connectionCount) {
		t.Errorf(
			"保持窗口结束后的活动 Session 增量=%d，小于新建连接数=%d",
			sessionDelta,
			connectionCount,
		)
	}
	select {
	case err := <-earlyDisconnects:
		t.Error(err)
	default:
	}

	allocDelta := int64(after.Memstats.Alloc) - int64(before.Memstats.Alloc)
	heapInuseDelta := int64(after.Memstats.HeapInuse) - int64(before.Memstats.HeapInuse)
	heapInusePerConnection := heapInuseDelta / int64(connectionCount)
	gcCount, gcP99, gcMax := gcPauseBaseline(before, after)
	if os.Getenv("IM_TEST_STANDALONE_REQUIRE_GC") == "1" && gcCount == 0 {
		t.Error("空闲连接测试窗口内没有发生 GC，无法形成 GC 暂停基线")
	}
	t.Logf(
		"空闲连接完成：连接=%d，建立耗时=%s，保持时长=%s，"+
			"Session 增量=%d，goroutine 增量=%d，Alloc 增量=%d B，"+
			"HeapInuse 增量=%d B，约=%d B/连接，GC=%d，GC p99=%s，GC max=%s",
		connectionCount,
		setupDuration,
		holdDuration,
		sessionDelta,
		after.NumGoroutines-before.NumGoroutines,
		allocDelta,
		heapInuseDelta,
		heapInusePerConnection,
		gcCount,
		gcP99,
		gcMax,
	)
}

// gcPauseBaseline 从 runtime.MemStats 的环形缓冲中提取测试窗口内的 GC 暂停。
func gcPauseBaseline(before, after runtimeSnapshot) (uint32, time.Duration, time.Duration) {
	count := after.Memstats.NumGC - before.Memstats.NumGC
	sampleCount := count
	if sampleCount > uint32(len(after.Memstats.PauseNs)) {
		sampleCount = uint32(len(after.Memstats.PauseNs))
	}
	if sampleCount == 0 {
		return count, 0, 0
	}

	pauses := make([]time.Duration, 0, sampleCount)
	for offset := uint32(0); offset < sampleCount; offset++ {
		index := (after.Memstats.NumGC - 1 - offset) %
			uint32(len(after.Memstats.PauseNs))
		pauses = append(pauses, time.Duration(after.Memstats.PauseNs[index]))
	}
	sort.Slice(pauses, func(left, right int) bool {
		return pauses[left] < pauses[right]
	})
	return count, percentile(pauses, 0.99), pauses[len(pauses)-1]
}

// loadIntegrationConfig 从环境读取真实进程地址。未显式提供 HTTP 地址时
// 跳过测试，保证普通 go test 不会意外访问开发者数据库或端口。
func loadIntegrationConfig(t *testing.T) integrationConfig {
	t.Helper()
	httpBase := strings.TrimRight(os.Getenv("IM_TEST_STANDALONE_HTTP"), "/")
	if httpBase == "" {
		t.Skip("未设置 IM_TEST_STANDALONE_HTTP，跳过真实单机进程测试")
	}
	grpcAddr := os.Getenv("IM_TEST_STANDALONE_GRPC")
	if grpcAddr == "" {
		t.Fatal("已启用真实进程测试，但未设置 IM_TEST_STANDALONE_GRPC")
	}
	apiKey := os.Getenv("IM_TEST_STANDALONE_API_KEY")
	if apiKey == "" {
		apiKey = defaultStandaloneAPIKey
	}
	username := os.Getenv("IM_TEST_STANDALONE_USERNAME")
	if username == "" {
		username = "alice"
	}
	password := os.Getenv("IM_TEST_STANDALONE_PASSWORD")
	if password == "" {
		password = "alice123"
	}
	return integrationConfig{
		httpBase: httpBase,
		grpcAddr: grpcAddr,
		apiKey:   apiKey,
		username: username,
		password: password,
	}
}

// runWebSocketFlow 在同一 WebSocket Session 中执行 hi 和 basic 登录。
func runWebSocketFlow(
	t *testing.T,
	ctx context.Context,
	cfg integrationConfig,
) (observedControl, observedControl, observedControl, observedControl, observedControl) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, websocketURL(cfg), nil)
	if err != nil {
		t.Fatalf("连接 WebSocket 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	if err := conn.WriteJSON(hiMessage("hi-ws")); err != nil {
		t.Fatalf("WebSocket 发送 hi 失败: %v", err)
	}
	hi, err := readWebSocketControl(conn, "hi-ws")
	if err != nil {
		t.Fatalf("WebSocket 接收 hi 失败: %v", err)
	}

	if err := conn.WriteJSON(loginMessage("login-ws", cfg)); err != nil {
		t.Fatalf("WebSocket 发送 login 失败: %v", err)
	}
	login, err := readWebSocketControl(conn, "login-ws")
	if err != nil {
		t.Fatalf("WebSocket 接收 login 失败: %v", err)
	}

	if err := conn.WriteJSON(subMessage("sub-ws")); err != nil {
		t.Fatalf("WebSocket 发送 sub 失败: %v", err)
	}
	sub, err := readWebSocketControl(conn, "sub-ws")
	if err != nil {
		t.Fatalf("WebSocket 接收 sub 失败: %v", err)
	}
	topic := requireCreatedTopic(t, "WebSocket", sub.Topic)

	if err := conn.WriteJSON(pubMessage("pub-ws", topic, "cid-ws")); err != nil {
		t.Fatalf("WebSocket 发送 pub 失败: %v", err)
	}
	pub, err := readWebSocketControl(conn, "pub-ws")
	if err != nil {
		t.Fatalf("WebSocket 接收 pub 失败: %v", err)
	}
	if err := conn.WriteJSON(pubMessage("retry-ws", topic, "cid-ws")); err != nil {
		t.Fatalf("WebSocket 发送重复 pub 失败: %v", err)
	}
	retry, err := readWebSocketControl(conn, "retry-ws")
	if err != nil {
		t.Fatalf("WebSocket 接收重复 pub 失败: %v", err)
	}
	return observeJSONControl(hi), observeJSONControl(login),
		observeJSONControl(sub), observeJSONControl(pub), observeJSONControl(retry)
}

// runLongPollFlow 在同一 Long Polling Session 中执行 hi 和 basic 登录。
func runLongPollFlow(
	t *testing.T,
	ctx context.Context,
	cfg integrationConfig,
) (observedControl, observedControl, observedControl, observedControl, observedControl) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	sid := createLongPollSession(t, ctx, client, cfg)
	hi := longPollRoundTrip(t, ctx, client, cfg, sid, "hi-lp", hiMessage("hi-lp"))
	login := longPollRoundTrip(
		t,
		ctx,
		client,
		cfg,
		sid,
		"login-lp",
		loginMessage("login-lp", cfg),
	)
	sub := longPollRoundTrip(t, ctx, client, cfg, sid, "sub-lp", subMessage("sub-lp"))
	topic := requireCreatedTopic(t, "Long Polling", sub.Topic)
	pub := longPollRoundTrip(
		t,
		ctx,
		client,
		cfg,
		sid,
		"pub-lp",
		pubMessage("pub-lp", topic, "cid-lp"),
	)
	retry := longPollRoundTrip(
		t,
		ctx,
		client,
		cfg,
		sid,
		"retry-lp",
		pubMessage("retry-lp", topic, "cid-lp"),
	)
	return observeJSONControl(hi), observeJSONControl(login),
		observeJSONControl(sub), observeJSONControl(pub), observeJSONControl(retry)
}

// runGRPCFlow 在同一双向流中执行 hi 和 basic 登录。
func runGRPCFlow(
	t *testing.T,
	ctx context.Context,
	cfg integrationConfig,
) (observedControl, observedControl, observedControl, observedControl, observedControl) {
	t.Helper()
	conn, err := grpc.NewClient(
		cfg.grpcAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("创建 gRPC 客户端失败: %v", err)
	}
	defer conn.Close()
	stream, err := pbx.NewNodeClient(conn).MessageLoop(ctx)
	if err != nil {
		t.Fatalf("建立 gRPC MessageLoop 失败: %v", err)
	}
	defer stream.CloseSend()

	if err := stream.Send(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Hi{
			Hi: &pbx.ClientHi{
				Id:        "hi-grpc",
				UserAgent: "standalone-e2e",
				Ver:       protocolVersion,
				Lang:      "zh",
			},
		},
	}); err != nil {
		t.Fatalf("gRPC 发送 hi 失败: %v", err)
	}
	hi := readGRPCControl(t, stream, "hi-grpc")

	if err := stream.Send(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Login{
			Login: &pbx.ClientLogin{
				Id:     "login-grpc",
				Scheme: "basic",
				Secret: []byte(cfg.username + ":" + cfg.password),
			},
		},
	}); err != nil {
		t.Fatalf("gRPC 发送 login 失败: %v", err)
	}
	login := readGRPCControl(t, stream, "login-grpc")

	if err := stream.Send(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Sub{
			Sub: &pbx.ClientSub{Id: "sub-grpc", Topic: "new"},
		},
	}); err != nil {
		t.Fatalf("gRPC 发送 sub 失败: %v", err)
	}
	sub := readGRPCControl(t, stream, "sub-grpc")
	topic := requireCreatedTopic(t, "gRPC", sub.GetTopic())

	if err := stream.Send(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Pub{
			Pub: &pbx.ClientPub{
				Id:       "pub-grpc",
				Topic:    topic,
				NoEcho:   true,
				ClientId: "cid-grpc",
				Kind:     "text",
				Content:  []byte(`"standalone protocol message"`),
			},
		},
	}); err != nil {
		t.Fatalf("gRPC 发送 pub 失败: %v", err)
	}
	pub := readGRPCControl(t, stream, "pub-grpc")
	if err := stream.Send(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Pub{
			Pub: &pbx.ClientPub{
				Id:       "retry-grpc",
				Topic:    topic,
				NoEcho:   true,
				ClientId: "cid-grpc",
				Kind:     "text",
				Content:  []byte(`"standalone protocol message"`),
			},
		},
	}); err != nil {
		t.Fatalf("gRPC 发送重复 pub 失败: %v", err)
	}
	retry := readGRPCControl(t, stream, "retry-grpc")
	return observeGRPCControl(hi), observeGRPCControl(login),
		observeGRPCControl(sub), observeGRPCControl(pub), observeGRPCControl(retry)
}

// websocketURL 构造带开发 API Key 的 WebSocket 入口地址。
func websocketURL(cfg integrationConfig) string {
	endpoint := strings.TrimPrefix(cfg.httpBase, "http://")
	scheme := "ws"
	if strings.HasPrefix(cfg.httpBase, "https://") {
		endpoint = strings.TrimPrefix(cfg.httpBase, "https://")
		scheme = "wss"
	}
	query := url.Values{"apikey": []string{cfg.apiKey}}
	return scheme + "://" + endpoint + "/v0/channels?" + query.Encode()
}

// longPollURL 构造带 Session ID 和开发 API Key 的 Long Polling 地址。
func longPollURL(cfg integrationConfig, sid string) string {
	query := url.Values{"apikey": []string{cfg.apiKey}}
	if sid != "" {
		query.Set("sid", sid)
	}
	return cfg.httpBase + "/v0/channels/lp?" + query.Encode()
}

// createLongPollSession 创建 Long Polling Session 并提取服务端分配的 sid。
func createLongPollSession(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	cfg integrationConfig,
) string {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, longPollURL(cfg, ""), nil)
	if err != nil {
		t.Fatalf("创建 Long Polling Session 请求失败: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("创建 Long Polling Session 失败: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("创建 Long Polling Session 状态码=%d，响应=%s", response.StatusCode, body)
	}

	var message wireServerMessage
	if err := json.NewDecoder(response.Body).Decode(&message); err != nil {
		t.Fatalf("解析 Long Polling Session 响应失败: %v", err)
	}
	if message.Ctrl == nil {
		t.Fatal("Long Polling Session 响应缺少 ctrl")
	}
	rawSID, found := message.Ctrl.Params["sid"]
	if !found {
		t.Fatal("Long Polling Session 响应缺少 sid")
	}
	var sid string
	if err := json.Unmarshal(rawSID, &sid); err != nil || sid == "" {
		t.Fatalf("解析 Long Polling sid 失败: %v", err)
	}
	return sid
}

// readRuntimeSnapshot 读取服务端 expvar，用于记录真实连接容量基线。
func readRuntimeSnapshot(
	t *testing.T,
	ctx context.Context,
	cfg integrationConfig,
) runtimeSnapshot {
	t.Helper()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		cfg.httpBase+"/debug/vars",
		nil,
	)
	if err != nil {
		t.Fatalf("创建 expvar 请求失败: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("读取 expvar 失败: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("读取 expvar 状态码=%d，响应=%s", response.StatusCode, body)
	}
	var snapshot runtimeSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatalf("解析 expvar 失败: %v", err)
	}
	return snapshot
}

// longPollRoundTrip 先 POST 客户端消息，再通过 GET 等待匹配 ID 的控制响应。
func longPollRoundTrip(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	cfg integrationConfig,
	sid string,
	expectedID string,
	payload any,
) *wireControl {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("编码 Long Polling 请求失败: %v", err)
	}
	post, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		longPollURL(cfg, sid),
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("创建 Long Polling POST 请求失败: %v", err)
	}
	post.Header.Set("Content-Type", "application/json")
	response, err := client.Do(post)
	if err != nil {
		t.Fatalf("发送 Long Polling 消息失败: %v", err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		errorBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		response.Body.Close()
		t.Fatalf("Long Polling POST 状态码=%d，响应=%s", response.StatusCode, errorBody)
	}
	response.Body.Close()

	// 登录后可能穿插 Presence 等异步消息，因此按请求 ID 查找对应 ctrl。
	for attempt := 0; attempt < 10; attempt++ {
		get, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			longPollURL(cfg, sid),
			nil,
		)
		if err != nil {
			t.Fatalf("创建 Long Polling GET 请求失败: %v", err)
		}
		response, err := client.Do(get)
		if err != nil {
			t.Fatalf("读取 Long Polling 响应失败: %v", err)
		}
		var message wireServerMessage
		decodeErr := json.NewDecoder(response.Body).Decode(&message)
		response.Body.Close()
		if decodeErr != nil {
			t.Fatalf("解析 Long Polling 响应失败: %v", decodeErr)
		}
		if message.Ctrl != nil && message.Ctrl.ID == expectedID {
			return message.Ctrl
		}
	}
	t.Fatalf("Long Polling 未收到请求 %q 对应的 ctrl", expectedID)
	return nil
}

// readWebSocketControl 持续读取 WebSocket 消息，直到找到匹配请求 ID 的 ctrl。
func readWebSocketControl(conn *websocket.Conn, expectedID string) (*wireControl, error) {
	for attempt := 0; attempt < 100; attempt++ {
		var message wireServerMessage
		if err := conn.ReadJSON(&message); err != nil {
			return nil, err
		}
		if message.Ctrl != nil && message.Ctrl.ID == expectedID {
			return message.Ctrl, nil
		}
	}
	return nil, fmt.Errorf("未收到请求 %q 对应的 ctrl", expectedID)
}

// readGRPCControl 持续读取双向流，直到找到匹配请求 ID 的 ctrl。
func readGRPCControl(
	t *testing.T,
	stream pbx.Node_MessageLoopClient,
	expectedID string,
) *pbx.ServerCtrl {
	t.Helper()
	for attempt := 0; attempt < 10; attempt++ {
		message, err := stream.Recv()
		if err != nil {
			t.Fatalf("gRPC 读取响应失败: %v", err)
		}
		if ctrl := message.GetCtrl(); ctrl != nil && ctrl.GetId() == expectedID {
			return ctrl
		}
	}
	t.Fatalf("gRPC 未收到请求 %q 对应的 ctrl", expectedID)
	return nil
}

// hiMessage 创建 JSON 入口共用的握手请求。
func hiMessage(id string) map[string]any {
	return map[string]any{
		"hi": map[string]any{
			"id":   id,
			"ua":   "standalone-e2e",
			"ver":  protocolVersion,
			"lang": "zh",
		},
	}
}

// loginMessage 创建 JSON 入口共用的 basic 登录请求。
func loginMessage(id string, cfg integrationConfig) map[string]any {
	return map[string]any{
		"login": map[string]any{
			"id":     id,
			"scheme": "basic",
			// []byte 由 encoding/json 编码为协议要求的 Base64 字符串。
			"secret": []byte(cfg.username + ":" + cfg.password),
		},
	}
}

// subMessage 创建新临时名称群组并把当前 Session 挂载到真实 Topic。
func subMessage(id string) map[string]any {
	return map[string]any{
		"sub": map[string]any{
			"id":    id,
			"topic": "new",
		},
	}
}

// pubMessage 创建带幂等客户端 ID 的文本消息，并关闭发送者回显。
func pubMessage(id, topic, clientID string) map[string]any {
	return map[string]any{
		"pub": map[string]any{
			"id":      id,
			"topic":   topic,
			"cid":     clientID,
			"noecho":  true,
			"kind":    "text",
			"content": "standalone protocol message",
		},
	}
}

// requireCreatedTopic 校验 new Topic 已被服务端解析为持久化群组名称。
func requireCreatedTopic(t *testing.T, transport, topic string) string {
	t.Helper()
	if !strings.HasPrefix(topic, "grp") {
		t.Fatalf("%s 创建 Topic 返回 %q，期望 grp 前缀", transport, topic)
	}
	return topic
}

// observeJSONControl 把 JSON 控制响应归一化为可跨协议比较的结构。
func observeJSONControl(ctrl *wireControl) observedControl {
	keys := make([]string, 0, len(ctrl.Params))
	for key := range ctrl.Params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return observedControl{Code: ctrl.Code, Text: ctrl.Text, ParamKeys: keys}
}

// observeGRPCControl 把 Protobuf 控制响应归一化为可跨协议比较的结构。
func observeGRPCControl(ctrl *pbx.ServerCtrl) observedControl {
	keys := make([]string, 0, len(ctrl.GetParams()))
	for key := range ctrl.GetParams() {
		// gRPC 客户端额外获得直连端点和集群规模，用于连接路由；
		// 它们属于传输元数据，不参与核心业务语义对照。
		if key == "servingAt" || key == "clusterSize" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return observedControl{Code: int(ctrl.GetCode()), Text: ctrl.GetText(), ParamKeys: keys}
}

// assertSuccessfulControl 验证控制响应成功且包含稳定文本。
func assertSuccessfulControl(
	t *testing.T,
	name string,
	control observedControl,
	expectedCode int,
) {
	t.Helper()
	if control.Code != expectedCode {
		t.Errorf("%s 状态码=%d，期望 %d", name, control.Code, expectedCode)
	}
	if control.Text == "" {
		t.Errorf("%s 缺少状态文本", name)
	}
}

// assertSuccessfulLogin 验证登录成功并返回用户和令牌参数。
func assertSuccessfulLogin(t *testing.T, name string, control observedControl) {
	t.Helper()
	assertSuccessfulControl(t, name, control, http.StatusOK)
	for _, required := range []string{"token", "user"} {
		index := sort.SearchStrings(control.ParamKeys, required)
		if index >= len(control.ParamKeys) || control.ParamKeys[index] != required {
			t.Errorf("%s 缺少参数 %q，实际参数=%v", name, required, control.ParamKeys)
		}
	}
}

// assertSameControl 验证三种传输返回相同的稳定控制语义。
func assertSameControl(
	t *testing.T,
	operation string,
	webSocket observedControl,
	longPolling observedControl,
	grpcControl observedControl,
) {
	t.Helper()
	if !reflect.DeepEqual(webSocket, longPolling) || !reflect.DeepEqual(webSocket, grpcControl) {
		t.Errorf(
			"%s 三协议语义不一致：WebSocket=%+v，Long Polling=%+v，gRPC=%+v",
			operation,
			webSocket,
			longPolling,
			grpcControl,
		)
	}
}

// positiveEnvInt 读取正整数测试参数，不存在时返回默认值。
func positiveEnvInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		t.Fatalf("%s 必须是正整数，当前值=%q", key, value)
	}
	return parsed
}

// positiveEnvDuration 读取正数 duration 测试参数，不存在时返回默认值。
func positiveEnvDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		t.Fatalf("%s 必须是正数 duration，当前值=%q", key, value)
	}
	return parsed
}

// collectSortedDurations 收集并排序并发连接耗时。
func collectSortedDurations(input <-chan time.Duration) []time.Duration {
	values := make([]time.Duration, 0)
	for value := range input {
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool {
		return values[left] < values[right]
	})
	return values
}

// percentile 使用 nearest-rank 规则返回小样本延迟分位值。
func percentile(values []time.Duration, fraction float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values))*fraction + 0.999999)
	if index < 1 {
		index = 1
	}
	if index > len(values) {
		index = len(values)
	}
	return values[index-1]
}
