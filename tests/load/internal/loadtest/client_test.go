package loadtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRunWorkloadAgainstWebSocketServer(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("apikey") != "test-key" {
			http.Error(writer, "接口密钥错误", http.StatusUnauthorized)
			return
		}
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Errorf("升级 WebSocket 失败: %v", err)
			return
		}
		defer connection.Close()
		serveFakeProtocol(t, connection)
	}))
	defer server.Close()

	config := validTestWorkload()
	config.WebSocketURL = "ws" + strings.TrimPrefix(server.URL, "http")
	config.Duration = 150 * time.Millisecond
	metrics := NewMetrics(config.RunID, config.WorkerID)

	if err := RunWorkload(context.Background(), config, metrics); err != nil {
		t.Fatalf("运行负载失败: %v", err)
	}
	snapshot := metrics.Snapshot(true, "")
	if snapshot.ConnectionsSucceeded != 1 ||
		snapshot.LoginsSucceeded != 1 ||
		snapshot.Subscriptions != 1 ||
		snapshot.PublishesAcknowledged != 1 ||
		snapshot.Deliveries != 1 {
		t.Fatalf("指标不符合预期: %#v", snapshot)
	}
}

func serveFakeProtocol(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	for {
		var request map[string]json.RawMessage
		if err := connection.ReadJSON(&request); err != nil {
			return
		}
		for operation, raw := range request {
			var message struct {
				ID      string          `json:"id"`
				Topic   string          `json:"topic"`
				Client  string          `json:"cid"`
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(raw, &message); err != nil {
				t.Errorf("解析 %s 请求失败: %v", operation, err)
				return
			}
			code := http.StatusOK
			params := map[string]string(nil)
			switch operation {
			case "hi":
				code = http.StatusCreated
			case "login":
				params = map[string]string{"token": "issued-token"}
			case "sub":
			case "pub":
				code = http.StatusAccepted
			default:
				t.Errorf("收到未支持的操作 %q", operation)
				return
			}
			response := map[string]any{
				"ctrl": map[string]any{
					"id":     message.ID,
					"code":   code,
					"text":   http.StatusText(code),
					"params": params,
				},
			}
			if err := connection.WriteJSON(response); err != nil {
				return
			}
			if operation == "pub" {
				if err := connection.WriteJSON(map[string]any{
					"data": map[string]any{
						"topic":   message.Topic,
						"cid":     message.Client,
						"content": json.RawMessage(message.Content),
					},
				}); err != nil {
					return
				}
			}
		}
	}
}
