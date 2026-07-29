package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type wireEnvelope struct {
	Ctrl *wireControl `json:"ctrl"`
	Meta *wireMeta    `json:"meta"`
	Data *wireData    `json:"data"`
}

type wireControl struct {
	ID     string                     `json:"id"`
	Topic  string                     `json:"topic"`
	Code   int                        `json:"code"`
	Text   string                     `json:"text"`
	Params map[string]json.RawMessage `json:"params"`
}

type wireMeta struct {
	ID    string             `json:"id"`
	Topic string             `json:"topic"`
	Sub   []wireSubscription `json:"sub"`
}

type wireSubscription struct {
	Topic string `json:"topic"`
}

type wireData struct {
	Topic    string          `json:"topic"`
	ClientID string          `json:"cid"`
	Content  json.RawMessage `json:"content"`
}

type loadMessageContent struct {
	RunID  string `json:"load_run_id"`
	SentAt int64  `json:"sent_at_unix_nano"`
	Index  int    `json:"index"`
}

type tokenCache struct {
	values sync.Map
}

func (cache *tokenCache) Load(username string) string {
	value, found := cache.values.Load(username)
	if !found {
		return ""
	}
	return value.(string)
}

func (cache *tokenCache) Store(username, token string) {
	if token != "" {
		cache.values.Store(username, token)
	}
}

type websocketClient struct {
	connection     *websocket.Conn
	requestTimeout time.Duration
	requestPrefix  string
	requestSeq     atomic.Uint64
	writeLock      sync.Mutex
	pendingLock    sync.RWMutex
	pending        map[string]chan wireEnvelope
	done           chan struct{}
	closeOnce      sync.Once
	readErrorLock  sync.Mutex
	readError      error
	onDelivery     func(wireData)
}

func dialWebSocketClient(
	ctx context.Context,
	config WorkloadConfig,
	requestPrefix string,
	onDelivery func(wireData),
) (*websocketClient, error) {
	endpoint, err := url.Parse(config.WebSocketURL)
	if err != nil {
		return nil, fmt.Errorf("解析 WebSocket 地址失败: %w", err)
	}
	switch endpoint.Scheme {
	case "http":
		endpoint.Scheme = "ws"
	case "https":
		endpoint.Scheme = "wss"
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("不支持的 WebSocket 协议 %q", endpoint.Scheme)
	}
	query := endpoint.Query()
	if query.Get("apikey") == "" {
		query.Set("apikey", config.APIKey)
		endpoint.RawQuery = query.Encode()
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: config.RequestTimeout,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
	}
	connection, response, err := dialer.DialContext(ctx, endpoint.String(), nil)
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("建立 WebSocket 失败，HTTP 状态=%d: %w", response.StatusCode, err)
		}
		return nil, fmt.Errorf("建立 WebSocket 失败: %w", err)
	}
	connection.SetReadLimit(8 << 20)

	client := &websocketClient{
		connection:     connection,
		requestTimeout: config.RequestTimeout,
		requestPrefix:  requestPrefix,
		pending:        make(map[string]chan wireEnvelope),
		done:           make(chan struct{}),
		onDelivery:     onDelivery,
	}
	go client.readLoop()
	return client, nil
}

func (client *websocketClient) Close() {
	client.closeOnce.Do(func() {
		_ = client.connection.Close()
	})
}

func (client *websocketClient) nextRequestID(operation string) string {
	return client.requestPrefix + "-" + operation + "-" +
		strconv.FormatUint(client.requestSeq.Add(1), 10)
}

func (client *websocketClient) readLoop() {
	defer close(client.done)
	for {
		var envelope wireEnvelope
		if err := client.connection.ReadJSON(&envelope); err != nil {
			client.readErrorLock.Lock()
			client.readError = err
			client.readErrorLock.Unlock()
			return
		}
		if envelope.Data != nil && client.onDelivery != nil {
			client.onDelivery(*envelope.Data)
		}
		requestID := ""
		if envelope.Ctrl != nil {
			requestID = envelope.Ctrl.ID
		} else if envelope.Meta != nil {
			requestID = envelope.Meta.ID
		}
		if requestID == "" {
			continue
		}
		client.pendingLock.RLock()
		responseChannel := client.pending[requestID]
		client.pendingLock.RUnlock()
		if responseChannel != nil {
			select {
			case responseChannel <- envelope:
			default:
			}
		}
	}
}

func (client *websocketClient) registerRequest(requestID string) chan wireEnvelope {
	responseChannel := make(chan wireEnvelope, 16)
	client.pendingLock.Lock()
	client.pending[requestID] = responseChannel
	client.pendingLock.Unlock()
	return responseChannel
}

func (client *websocketClient) unregisterRequest(requestID string) {
	client.pendingLock.Lock()
	delete(client.pending, requestID)
	client.pendingLock.Unlock()
}

func (client *websocketClient) writeJSON(request any) error {
	client.writeLock.Lock()
	defer client.writeLock.Unlock()
	_ = client.connection.SetWriteDeadline(time.Now().Add(client.requestTimeout))
	return client.connection.WriteJSON(request)
}

func (client *websocketClient) requestControl(
	ctx context.Context,
	requestID string,
	request any,
) (*wireControl, error) {
	responseChannel := client.registerRequest(requestID)
	defer client.unregisterRequest(requestID)
	if err := client.writeJSON(request); err != nil {
		return nil, err
	}

	timeout := time.NewTimer(client.requestTimeout)
	defer timeout.Stop()
	for {
		select {
		case envelope := <-responseChannel:
			if envelope.Ctrl != nil {
				return envelope.Ctrl, nil
			}
		case <-client.done:
			return nil, client.connectionError()
		case <-timeout.C:
			return nil, fmt.Errorf("等待请求 %s 控制响应超时", requestID)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (client *websocketClient) requestMeta(
	ctx context.Context,
	requestID string,
	request any,
) (*wireMeta, error) {
	responseChannel := client.registerRequest(requestID)
	defer client.unregisterRequest(requestID)
	if err := client.writeJSON(request); err != nil {
		return nil, err
	}

	timeout := time.NewTimer(client.requestTimeout)
	defer timeout.Stop()
	for {
		select {
		case envelope := <-responseChannel:
			if envelope.Meta != nil {
				return envelope.Meta, nil
			}
			if envelope.Ctrl != nil {
				if envelope.Ctrl.Code == http.StatusNoContent ||
					envelope.Ctrl.Code == http.StatusNotModified {
					return &wireMeta{ID: requestID}, nil
				}
				if envelope.Ctrl.Code >= http.StatusBadRequest {
					return nil, controlError(envelope.Ctrl)
				}
			}
		case <-client.done:
			return nil, client.connectionError()
		case <-timeout.C:
			return nil, fmt.Errorf("等待请求 %s 元数据响应超时", requestID)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (client *websocketClient) connectionError() error {
	client.readErrorLock.Lock()
	defer client.readErrorLock.Unlock()
	if client.readError == nil {
		return errors.New("WebSocket 已关闭")
	}
	return client.readError
}
