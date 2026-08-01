/******************************************************************************
 *
 *  描述 :
 *
 *    WebSocket 连接处理器。另见 hdl_longpoll.go（长轮询）
 *    和 hdl_grpc.go（gRPC）。
 *
 *****************************************************************************/

// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"encoding/json"
	"net/http"
	"time"

	"chat/api/pbx"
	"chat/server/logs"
	"chat/server/store/types"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

const (
	// 允许向对端写入消息的时间。
	writeWait = 10 * time.Second

	// 允许从对端读取下一条 pong 消息的时间。
	pongWait = idleSessionTimeout

	// 向对端发送 ping 的周期。必须小于 pongWait。
	pingPeriod = (pongWait * 9) / 10
)

// closeWS 停止WS并释放相关资源。
func (sess *Session) closeWS() {
	if sess.proto == WEBSOCK {
		sess.ws.Close()
	}
}

// readLoop 查询并返回Loop。
func (sess *Session) readLoop() {
	defer func() {
		sess.closeWS()
		sess.cleanUp(false)
	}()

	sess.ws.SetReadLimit(globals.maxMessageSize)
	sess.ws.SetReadDeadline(time.Now().Add(pongWait))
	sess.ws.SetPongHandler(func(string) error {
		sess.ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		// 读取 ClientComMessage
		messageType, raw, err := sess.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure,
				websocket.CloseNormalClosure) {
				logs.Err.Println("ws: readLoop", sess.sid, err)
			}
			return
		}
		statsInc("IncomingMessagesWebsockTotal", 1)
		if sess.wsBinary {
			if messageType != websocket.BinaryMessage {
				logs.Warn.Println("ws: protobuf session received non-binary frame", sess.sid)
				return
			}
			var batch pbx.ClientBatch
			if err = proto.Unmarshal(raw, &batch); err != nil ||
				len(batch.Messages) == 0 || len(batch.Messages) > maxClientBatchMessages {
				logs.Warn.Println("ws: malformed protobuf batch", sess.sid, err)
				sess.queueOut(ErrMalformed("", "", types.TimeNow()))
				continue
			}
			if sess.ver == 0 && (len(batch.Messages) != 1 || batch.Messages[0].GetHi() == nil) {
				sess.queueOut(ErrCommandOutOfSequence("", "", types.TimeNow()))
				continue
			}
			if len(batch.Messages) > 1 {
				statsInc("IncomingWebsockBatchFramesTotal", 1)
				statsInc("IncomingWebsockBatchedMessagesTotal", len(batch.Messages))
			}
			for _, packet := range batch.Messages {
				sess.dispatch(pbCliDeserialize(packet))
			}
		} else {
			if messageType != websocket.TextMessage {
				logs.Warn.Println("ws: json session received non-text frame", sess.sid)
				return
			}
			sess.dispatchRaw(raw)
		}
	}
}

// sendMessage 处理消息消息或事件。
func (sess *Session) sendMessage(msg any) bool {
	if len(sess.send) > sendQueueLimit {
		logs.Err.Println("ws: outbound queue limit exceeded", sess.sid)
		return false
	}

	statsInc("OutgoingMessagesWebsockTotal", 1)
	if err := wsWrite(sess.ws, sess.wsDataMessageType(), msg); err != nil {
		if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure,
			websocket.CloseNormalClosure) {
			logs.Err.Println("ws: writeLoop", sess.sid, err)
		}
		return false
	}
	return true
}

func (sess *Session) wsDataMessageType() int {
	if sess.wsBinary {
		return websocket.BinaryMessage
	}
	return websocket.TextMessage
}

// sendQueuedBatch 把当前已经排队的连续服务端消息合并发送。它不等待新消息，
// 因此空闲连接的实时消息不会增加人为延迟；突发查询的 data/meta/ctrl 可以共享帧。
func (sess *Session) sendQueuedBatch(initial []*ServerComMessage) bool {
	messages := initial
	flush := func() bool {
		if len(messages) == 0 {
			return true
		}
		frames := sess.serializeBatchAndUpdateStats(messages)
		statsInc("OutgoingWebsockBatchFramesTotal", len(frames))
		statsInc("OutgoingWebsockBatchedMessagesTotal", len(messages))
		for _, frame := range frames {
			if !sess.sendMessage(frame) {
				return false
			}
		}
		messages = nil
		return true
	}

	for len(sess.send) > 0 {
		next, ok := <-sess.send
		if !ok {
			return flush()
		}
		sess.releaseOutbound(next)
		switch value := next.(type) {
		case *ServerComMessage:
			messages = append(messages, value)
		case []*ServerComMessage:
			messages = append(messages, value...)
		default:
			// 保持网络探针等原始帧与业务消息的严格队列顺序。
			if !flush() || !sess.sendMessage(value) {
				return false
			}
		}
	}
	return flush()
}

// writeLoop 保存Loop。
func (sess *Session) writeLoop() {
	ticker := time.NewTicker(pingPeriod)

	defer func() {
		ticker.Stop()
		//打破readLoop。
		sess.closeWS()
	}()

	for {
		select {
		case msg, ok := <-sess.send:
			if !ok {
				// Channel 已关闭。
				return
			}
			sess.releaseOutbound(msg)
			switch v := msg.(type) {
			case []*ServerComMessage: // 批量未序列化的消息
				if sess.supportsMessageBatching() {
					if !sess.sendQueuedBatch(v) {
						return
					}
				} else {
					for _, msg := range v {
						w := sess.serializeAndUpdateStats(msg)
						if !sess.sendMessage(w) {
							return
						}
					}
				}
			case *ServerComMessage: // 单个未序列化的消息
				if sess.supportsMessageBatching() && len(sess.send) > 0 {
					if !sess.sendQueuedBatch([]*ServerComMessage{v}) {
						return
					}
				} else {
					w := sess.serializeAndUpdateStats(v)
					if !sess.sendMessage(w) {
						return
					}
				}
			default: // 已序列化的消息
				if !sess.sendMessage(v) {
					return
				}
			}

		case <-sess.bkgTimer.C:
			if sess.background {
				sess.background = false
				sess.onBackgroundTimer()
			}

		case msg := <-sess.stop:
			// 请求关闭，不关心消息是否已送达
			if msg != nil {
				wsWrite(sess.ws, sess.wsDataMessageType(), msg)
			}
			return

		case topic := <-sess.detach:
			sess.delSub(topic)

		case <-ticker.C:
			if err := wsWrite(sess.ws, websocket.PingMessage, nil); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure,
					websocket.CloseNormalClosure) {
					logs.Err.Println("ws: writeLoop ping", sess.sid, err)
				}
				return
			}
		}
	}
}

// 以给定的消息类型（mt）和负载写入消息。
func wsWrite(ws *websocket.Conn, mt int, msg any) error {
	var bits []byte
	if msg != nil {
		bits = msg.([]byte)
	} else {
		bits = []byte{}
	}
	ws.SetWriteDeadline(time.Now().Add(writeWait))
	return ws.WriteMessage(mt, bits)
}

// 处理来自对端的 WebSocket 请求。
var upgrader = websocket.Upgrader{
	ReadBufferSize:    1024,
	WriteBufferSize:   1024,
	EnableCompression: globals.wsCompression,
	Subprotocols:      []string{"im.protobuf.v1"},
	// 允许来自任何 Origin 的连接
	CheckOrigin: func(r *http.Request) bool { return true },
}

// serveWebSocket 处理WebSocket消息或事件。
func serveWebSocket(wrt http.ResponseWriter, req *http.Request) {
	now := types.TimeNow()

	if !serviceAcceptingConnections() {
		wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
		wrt.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(wrt).Encode(
			ErrServiceUnavailableExplicitTs("", "", now, now),
		)
		return
	}

	if isValid, _ := checkAPIKey(getAPIKey(req)); !isValid {
		wrt.WriteHeader(http.StatusForbidden)
		json.NewEncoder(wrt).Encode(ErrAPIKeyRequired(now))
		logs.Err.Println("ws: Missing, invalid or expired API key")
		return
	}

	if req.Method != http.MethodGet {
		wrt.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(wrt).Encode(ErrOperationNotAllowed("", "", now))
		logs.Err.Println("ws: Invalid HTTP method", req.Method)
		return
	}

	ws, err := upgrader.Upgrade(wrt, req, nil)
	if _, ok := err.(websocket.HandshakeError); ok {
		logs.Err.Println("ws: Not a websocket handshake")
		return
	} else if err != nil {
		logs.Err.Println("ws: failed to Upgrade ", err)
		return
	}

	sess, count := globals.sessionStore.NewSession(ws, "")
	sess.wsBinary = ws.Subprotocol() == "im.protobuf.v1"
	sess.countryCode = getCountryCodeFromHeader(req)
	if globals.useXForwardedFor {
		sess.remoteAddr = req.Header.Get("X-Forwarded-For")
		if !isRoutableIP(sess.remoteAddr) {
			sess.remoteAddr = ""
		}
	}
	if sess.remoteAddr == "" {
		sess.remoteAddr = req.RemoteAddr
	}

	logs.Info.Println("ws: session started", sess.sid, sess.remoteAddr, count)

	// 在 goroutine 中工作以从 serveWebSocket() 返回，释放文件指针。
	// 否则会出现 "too many open files"。
	go sess.writeLoop()
	go sess.readLoop()
}
