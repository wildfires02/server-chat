/******************************************************************************
 *
 *  描述 :
 *
 *    长轮询客户端处理器。另见 hdl_websock.go（WebSocket）和
 *    hdl_grpc.go（gRPC）。
 *
 *****************************************************************************/

// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"chat/server/logs"
)

// sendMessageLp 处理消息Lp消息或事件。
func (sess *Session) sendMessageLp(wrt http.ResponseWriter, msg any) bool {
	if len(sess.send) > sendQueueLimit {
		logs.Err.Println("longPoll: outbound queue limit exceeded", sess.sid)
		return false
	}

	statsInc("OutgoingMessagesLongpollTotal", 1)
	if err := lpWrite(wrt, msg); err != nil {
		logs.Err.Println("longPoll: writeOnce failed", sess.sid, err)
		return false
	}

	return true
}

// writeOnce 保存Once。
func (sess *Session) writeOnce(wrt http.ResponseWriter, req *http.Request) {
	// 使用 NewTimer 而不是 time.After 以避免 for-select 循环中的定时器泄漏。
	pingTimer := time.NewTimer(pingPeriod)
	defer pingTimer.Stop()

	for {
		select {
		case msg, ok := <-sess.send:
			if !ok {
				return
			}
			switch v := msg.(type) {
			case *ServerComMessage: // 单个未序列化的消息
				w := sess.serializeAndUpdateStats(v)
				if !sess.sendMessageLp(wrt, w) {
					return
				}
			default: // 已序列化的消息
				if !sess.sendMessageLp(wrt, v) {
					return
				}
			}
			return

		case <-sess.bkgTimer.C:
			if sess.background {
				sess.background = false
				sess.onBackgroundTimer()
			}

		case msg := <-sess.stop:
			// 请求关闭 Session。使其不可可用。
			globals.sessionStore.Delete(sess)
			// 不关心 lpWrite 是否失败。
			if msg != nil {
				lpWrite(wrt, msg)
			}
			return

		case topic := <-sess.detach:
			// 请求将 Session 从 Topic 分离。
			sess.delSub(topic)
			// 不在此处 'return'：继续等待

		case <-pingTimer.C:
			// 超时时只写入空数据包
			if _, err := wrt.Write([]byte{}); err != nil {
				logs.Err.Println("longPoll: writeOnce: timout", sess.sid, err)
			}
			return

		case <-req.Context().Done():
			// HTTP 请求已取消或连接丢失。
			return
		}
	}
}

// lpWrite 完成lpWrite所需的内部处理。
func lpWrite(wrt http.ResponseWriter, msg any) error {
	// 如果 msg 不是 []byte 将会 panic。这是有意为之的。
	wrt.Write(msg.([]byte))
	return nil
}

// readOnce 查询并返回Once。
func (sess *Session) readOnce(wrt http.ResponseWriter, req *http.Request) (int, error) {
	if req.ContentLength > globals.maxMessageSize {
		return http.StatusExpectationFailed, errors.New("request too large")
	}

	req.Body = http.MaxBytesReader(wrt, req.Body, globals.maxMessageSize)
	raw, err := io.ReadAll(req.Body)
	if err == nil {
		// 加锁/解锁是必需的，因为客户端可能并行发出多个请求。
		// 不会影响性能
		sess.lock.Lock()
		statsInc("IncomingMessagesLongpollTotal", 1)
		sess.dispatchRaw(raw)
		sess.lock.Unlock()
		return 0, nil
	}

	return 0, err
}

// serveLongPoll 处理 WebSocket 不可用时的长轮询连接
// 连接可能带 sid 也可能不带 sid：
//   - 如果 sid 为空，创建 Session，期望在同一请求中登录，响应后关闭
//   - 如果 sid 不为空且存在已初始化的 Session，payload 是可选的
//   - 如果没有 payload，执行长轮询
//   - 如果存在 payload，处理后关闭
//   - 如果 sid 不为空但不存在 Session，报告错误
func serveLongPoll(wrt http.ResponseWriter, req *http.Request) {
	now := time.Now().UTC().Round(time.Millisecond)

	// 使用最低公共标准 - 这毕竟是一个遗留处理器（否则会使用 application/json）
	wrt.Header().Set("Content-Type", "text/plain")
	if globals.tlsStrictMaxAge != "" {
		wrt.Header().Set("Strict-Transport-Security", "max-age"+globals.tlsStrictMaxAge)
	}

	enc := json.NewEncoder(wrt)

	if isValid, _ := checkAPIKey(getAPIKey(req)); !isValid {
		wrt.WriteHeader(http.StatusForbidden)
		enc.Encode(ErrAPIKeyRequired(now))
		return
	}

	// CORS 跨域响应标头配置
	if reqOrigin := req.Header.Get("Origin"); reqOrigin != "" {
		wrt.Header().Set("Access-Control-Allow-Origin", reqOrigin)
		wrt.Header().Set("Access-Control-Allow-Credentials", "true")
	} else {
		wrt.Header().Set("Access-Control-Allow-Origin", "*")
	}

	// 确保响应不被缓存
	if req.ProtoAtLeast(1, 1) {
		wrt.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate") // HTTP 1.1
	} else {
		wrt.Header().Set("Pragma", "no-cache") // HTTP 1.0
	}
	wrt.Header().Set("Expires", "0") // Proxies

	// 根据不同的 HTTP 请求方法做出对应分发处理
	switch req.Method {
	case http.MethodOptions:
		// CORS 预检请求直接响应并返回 24小时缓存
		wrt.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		wrt.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-IM-APIKey, X-IM-Auth")
		wrt.Header().Set("Access-Control-Max-Age", "86400")
		wrt.WriteHeader(http.StatusNoContent)
		return

	case http.MethodGet, http.MethodPost:
		// 合法的长轮询请求方法，放行向下处理

	default:
		// 拦截非法的 HTTP 方法
		wrt.Header().Set("Allow", "GET, POST, OPTIONS")
		wrt.WriteHeader(http.StatusMethodNotAllowed)
		enc.Encode(ErrOperationNotAllowed("", "", now))
		return
	}

	// 获取 Session id
	sid := req.FormValue("sid")
	var sess *Session
	if sid == "" {
		// 新 Session
		var count int
		sess, count = globals.sessionStore.NewSession(wrt, "")
		sess.countryCode = getCountryCodeFromHeader(req)
		sess.remoteAddr = getRemoteAddr(req)
		logs.Info.Println("longPoll: session started", sess.sid, sess.remoteAddr, count)

		wrt.WriteHeader(http.StatusCreated)
		pkt := NoErrCreated(req.FormValue("id"), "", now)
		pkt.Ctrl.Params = map[string]string{
			"sid": sess.sid,
		}
		enc.Encode(pkt)

		return
	}

	// 已存在的 Session
	sess = globals.sessionStore.Get(sid)
	if sess == nil {
		logs.Warn.Println("longPoll: invalid or expired session id", sid)
		wrt.WriteHeader(http.StatusForbidden)
		enc.Encode(ErrSessionNotFound(now))
		return
	}

	if addr := getRemoteAddr(req); sess.remoteAddr != addr {
		sess.remoteAddr = addr
		logs.Warn.Println("longPoll: remote address changed", sid, addr)
	}

	if req.ContentLength != 0 {
		// 读取 payload 并发送处理。
		if code, err := sess.readOnce(wrt, req); err != nil {
			logs.Warn.Println("longPoll: readOnce failed", sess.sid, err)
			// 失败：读取请求，报告错误（如果可能）
			if code != 0 {
				wrt.WriteHeader(code)
			} else {
				wrt.WriteHeader(http.StatusBadRequest)
			}
			enc.Encode(ErrMalformed(req.FormValue("id"), "", now))
		}
		return
	}

	sess.writeOnce(wrt, req)
}
