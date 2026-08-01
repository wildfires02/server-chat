/******************************************************************************
 *
 *  描述 :
 *
 *  Web 服务器初始化和关闭。
 *
 *****************************************************************************/

// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"chat/server/logs"
	"chat/server/store/types"
)

// listenAndServe 完成listenAndServe所需的内部处理。
func listenAndServe(addr string, mux *http.ServeMux, tlfConf *tls.Config, stop <-chan bool) error {
	globals.shuttingDown = false

	httpdone := make(chan bool)

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		MaxHeaderBytes:    1 << 14,
	}

	server.TLSConfig = tlfConf

	go func() {
		var err error
		if server.TLSConfig != nil {
			// 如果未指定端口，使用默认 HTTPS 端口（443），
			// 否则默认为 80
			if addr == "" {
				addr = ":https"
			}

			if globals.tlsRedirectHTTP != "" {
				// 从 Unix Socket 提供服务或重定向到 Unix Socket 没有意义
				if isUnixAddr(globals.tlsRedirectHTTP) || isUnixAddr(addr) {
					err = errors.New("HTTP to HTTPS redirect: unix sockets not supported")
				} else {
					logs.Info.Printf("Redirecting connections from HTTP at [%s] to HTTPS at [%s]",
						globals.tlsRedirectHTTP, addr)

					// 这是监听在不同端口的第二个 HTTP 服务器
					go func() {
						if err := http.ListenAndServe(globals.tlsRedirectHTTP, tlsRedirect(addr)); err != nil && err != http.ErrServerClosed {
							logs.Info.Println("HTTP redirect failed:", err)
						}
					}()
				}
			}

			if err == nil {
				logs.Info.Printf("Listening for client HTTPS connections on [%s]", addr)
				var lis net.Listener
				lis, err = netListener(addr)
				if err == nil {
					err = server.ServeTLS(lis, "", "")
				}
			}
		} else {
			logs.Info.Printf("Listening for client HTTP connections on [%s]", addr)
			var lis net.Listener
			lis, err = netListener(addr)
			if err == nil {
				err = server.Serve(lis)
			}
		}

		if err != nil {
			if globals.shuttingDown {
				logs.Info.Println("HTTP server: stopped")
			} else {
				logs.Err.Println("HTTP server: failed", err)
			}
		}
		httpdone <- true
	}()

	// 等待终止信号或错误
Loop:
	for {
		select {
		case <-stop:
			// 先摘除 Readiness 并拒绝新的连接/写请求，再排空可靠集群请求。
			if globals.health != nil {
				globals.health.BeginDrain()
			}
			drainContext, drainCancel := context.WithTimeout(
				context.Background(),
				globals.health.DrainTimeout(),
			)
			if err := globals.cluster.beginDrain(drainContext); err != nil {
				logs.Warn.Printf("Cluster Owner drain failed: %v", err)
			}
			if !globals.cluster.waitForReliableDrain(drainContext) {
				logs.Warn.Println("Cluster reliable queue drain timed out")
			}
			drainCancel()

			// 设置关闭标志并关闭 Accept 套接字，这样就不会有新连接。
			globals.shuttingDown = true
			// 给服务器 2 秒时间关闭
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := server.Shutdown(ctx); err != nil {
				// 优雅关闭失败或超时
				logs.Err.Println("HTTP server failed to terminate gracefully", err)
			}

			// 服务器关闭时，终止所有 Session
			globals.sessionStore.Shutdown()

			// 等待 HTTP 服务器停止 Accept 连接
			<-httpdone
			cancel()

			// 关闭本地集群节点（如果是集群的一部分）
			globals.cluster.shutdown()

			// 终止插件连接
			pluginsShutdown()

			// 关闭 gRPC 服务器（如果配置了）
			if globals.grpcServer != nil {
				// GracefulStop 不会终止 ServerStream，必须使用 Stop()
				globals.grpcServer.Stop()
			}

			// 停止发布统计信息
			statsShutdown()

			// 关闭 Hub。Hub 会关闭所有 Topic
			hubdone := make(chan bool)
			globals.hub.shutdown <- hubdone

			// 等待 Hub 完成关闭
			<-hubdone

			// 停止更新用户缓存
			usersShutdown()
			if globals.health != nil {
				globals.health.Stop()
			}

			break Loop

		case <-httpdone:
			break Loop
		}
	}
	return nil
}

// signalHandler 完成signal处理器所需的内部处理。
func signalHandler() <-chan bool {
	stop := make(chan bool)

	signchan := make(chan os.Signal, 1)
	signal.Notify(signchan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		// 等待信号，不关心是哪个信号
		sig := <-signchan
		logs.Info.Printf("Signal received: '%s', shutting down", sig)
		stop <- true
	}()

	return stop
}

// 以下代码用于拦截 HTTP 错误，以便将其封装为 JSON 格式。

// errorResponseWriter 是 http.ResponseWriter 的包装器，检测状态码 400+ 并替换
// 默认错误消息为自定义消息。
type errorResponseWriter struct {
	// status 保存状态。
	status int
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	http.ResponseWriter
}

// WriteHeader 保存 Write Header 对应的数据。
func (w *errorResponseWriter) WriteHeader(status int) {
	if status >= http.StatusBadRequest {
		// charset=utf-8 是默认值，无需显式设置
		// 必须在调用 super.WriteHeader() 之前设置所有头部
		w.ResponseWriter.Header().Set("Content-Type", "application/json")
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write 保存 Write 对应的数据。
func (w *errorResponseWriter) Write(p []byte) (n int, err error) {
	if w.status >= http.StatusBadRequest {
		p, _ = json.Marshal(
			&ServerComMessage{
				Ctrl: &MsgServerCtrl{
					Timestamp: time.Now().UTC().Round(time.Millisecond),
					Code:      w.status,
					Text:      http.StatusText(w.status),
				},
			})
	}
	return w.ResponseWriter.Write(p)
}

// httpErrorHandler 用于为静态内容响应 JSON 格式的错误消息。
func httpErrorHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(&errorResponseWriter{0, w}, r)
		})
}

// Custom 404 响应.
func serve404(wrt http.ResponseWriter, req *http.Request) {
	wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
	wrt.WriteHeader(http.StatusNotFound)
	json.NewEncoder(wrt).Encode(
		&ServerComMessage{
			Ctrl: &MsgServerCtrl{
				Timestamp: time.Now().UTC().Round(time.Millisecond),
				Code:      http.StatusNotFound,
				Text:      "not found",
			},
		})
}

// 重定向 HTTP 请求到 HTTPS
func tlsRedirect(toPort string) http.HandlerFunc {
	if toPort == ":443" || toPort == ":https" {
		toPort = ""
	} else if toPort != "" && toPort[:1] == ":" {
		// 去掉前导冒号，JoinHostPort 会加回来
		toPort = toPort[1:]
	}

	return func(wrt http.ResponseWriter, req *http.Request) {
		host, _, err := net.SplitHostPort(req.Host)
		if err != nil {
			// 如果 SplitHostPort 失败，假设是因为缺少 :port 部分
			host = req.Host
		}

		target, _ := url.ParseRequestURI(req.RequestURI)
		target.Scheme = "https"

		// 确保有效的重定向目标
		if toPort != "" {
			// 替换端口号
			target.Host = net.JoinHostPort(host, toPort)
		} else {
			target.Host = host
		}

		if target.Path == "" {
			target.Path = "/"
		}

		http.Redirect(wrt, req, target.String(), http.StatusTemporaryRedirect)
	}
}

// base64URLToStdReplacer 保存base64URLToStdReplacer的共享实例或运行状态。
var base64URLToStdReplacer = strings.NewReplacer("-", "+", "_", "/")

// 可选 HTTP 头部包装器：
//   - Strict-Transport-Security
//   - X-Frame-Options
//   - Referrer-Policy
func optionalHttpHeaders(handler http.Handler) http.Handler {
	strictMaxAge := globals.tlsStrictMaxAge
	xFrameOpts := globals.xFrameOptions

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "origin")
		if strictMaxAge != "" {
			w.Header().Set("Strict-Transport-Security", "max-age="+strictMaxAge)
		}
		if xFrameOpts != "-" {
			w.Header().Set("X-Frame-Options", xFrameOpts)
		}
		handler.ServeHTTP(w, r)
	})
}

// cacheControlHandler 是 http.Handler 的包装器，可选地添加 Cache-Control 头部到响应
func cacheControlHandler(maxAge int, handler http.Handler) http.Handler {
	if maxAge > 0 {
		strMaxAge := strconv.Itoa(maxAge)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "must-revalidate, public, max-age="+strMaxAge)
			handler.ServeHTTP(w, r)
		})
	}
	return handler
}

// getAPIKey 从 HTTP 请求中获取 API 密钥。
func getAPIKey(req *http.Request) string {
	// 检查头部 X-IM-APIKey
	apikey := req.Header.Get("X-IM-APIKey")
	if apikey != "" {
		return apikey
	}

	// 检查 URL 查询参数
	apikey = req.URL.Query().Get("apikey")
	if apikey != "" {
		return apikey
	}

	// 检查表单值
	apikey = req.FormValue("apikey")
	if apikey != "" {
		return apikey
	}

	// 检查 Cookie
	if c, err := req.Cookie("apikey"); err == nil {
		apikey = c.Value
	}

	return apikey
}

// getHttpAuth 从 HTTP 请求中提取授权凭证。
// 返回认证方法和密钥。
func getHttpAuth(req *http.Request) (method, secret string) {
	// 检查 X-IM-Auth 头部
	authHeader := req.Header.Get("X-IM-Auth")
	if parts := strings.Split(authHeader, " "); len(parts) == 2 {
		method, secret = parts[0], parts[1]
		return
	}

	// 检查标准 Authorization 头部
	if parts := strings.Split(req.Header.Get("Authorization"), " "); len(parts) == 2 {
		method, secret = parts[0], parts[1]
		return
	}

	// 检查 URL 查询参数
	if method = req.URL.Query().Get("auth"); method != "" {
		// 获取认证密钥
		secret = req.URL.Query().Get("secret")
		// 将 base64 URL 编码转换为标准编码
		secret = base64URLToStdReplacer.Replace(secret)
		return
	}

	// 检查表单值
	if method = req.FormValue("auth"); method != "" {
		return method, req.FormValue("secret")
	}

	// 最后检查 Cookie
	if mcookie, err := req.Cookie("auth"); err == nil {
		if scookie, err := req.Cookie("secret"); err == nil {
			method, secret = mcookie.Value, scookie.Value
		}
	}

	return
}

// getRemoteAddr 获取客户端 IP 地址。
func getRemoteAddr(req *http.Request) string {
	var addr string
	if globals.useXForwardedFor {
		addr = req.Header.Get("X-Forwarded-For")
		if !isRoutableIP(addr) {
			addr = ""
		}
	}
	if addr != "" {
		return addr
	}
	return req.RemoteAddr
}

// debugSession 是 Session 调试信息。
type debugSession struct {
	// RemoteAddr 保存RemoteAddr。
	RemoteAddr string `json:"remote_addr,omitempty"`
	// Ua 保存Ua。
	Ua string `json:"ua,omitempty"`
	// Uid 保存用户标识。
	Uid string `json:"uid,omitempty"`
	// Sid 保存Sid。
	Sid string `json:"sid,omitempty"`
	// Clnode 保存Clnode。
	Clnode string `json:"clnode,omitempty"`
	// Subs 保存Subs列表。
	Subs []string `json:"subs,omitempty"`
}

// debugTopic 是 Topic 调试信息。
type debugTopic struct {
	// Topic 保存Topic。
	Topic string `json:"topic,omitempty"`
	// Xorig 保存Xorig。
	Xorig string `json:"xorig,omitempty"`
	// IsProxy 指示是否启用或满足Is代理。
	IsProxy bool `json:"is_proxy,omitempty"`
	// PerUser 保存Per用户列表。
	PerUser []string `json:"per_user,omitempty"`
	// PerSubs 保存PerSubs列表。
	PerSubs []string `json:"per_subs,omitempty"`
	// Sessions 保存Sessions列表。
	Sessions []string `json:"sessions,omitempty"`
}

// debugCachedUser 是用户缓存条目调试信息。
type debugCachedUser struct {
	// Uid 保存用户标识。
	Uid string `json:"uid,omitempty"`
	// Unread 保存Unread。
	Unread int `json:"unread,omitempty"`
	// Topics 保存Topics。
	Topics int `json:"topics,omitempty"`
}

// debugDump 是服务器内部状态调试信息。
type debugDump struct {
	// Version 保存版本。
	Version string `json:"server_version,omitempty"`
	// Build 保存Build。
	Build string `json:"build_id,omitempty"`
	// Timestamp 保存Timestamp。
	Timestamp time.Time `json:"ts,omitempty"`
	// Sessions 保存Sessions列表。
	Sessions []debugSession `json:"sessions,omitempty"`
	// Topics 保存Topics列表。
	Topics []debugTopic `json:"topics,omitempty"`
	// UserCache 指示是否启用或满足用户缓存。
	UserCache []debugCachedUser `json:"user_cache,omitempty"`
}

// serveStatus 处理状态消息或事件。
func serveStatus(wrt http.ResponseWriter, req *http.Request) {
	wrt.Header().Set("Content-Type", "application/json")

	result := &debugDump{
		Version:   currentVersion,
		Build:     buildstamp,
		Timestamp: types.TimeNow(),
		Sessions:  make([]debugSession, 0, len(globals.sessionStore.sessCache)),
		Topics:    make([]debugTopic, 0, 10),
		UserCache: make([]debugCachedUser, 0, 10),
	}
	//会议。
	globals.sessionStore.Range(func(sid string, s *Session) bool {
		keys := make([]string, 0, len(s.subs))
		for tn := range s.subs {
			keys = append(keys, tn)
		}
		sort.Strings(keys)
		var clnode string
		if s.clnode != nil {
			clnode = s.clnode.name
		}
		result.Sessions = append(result.Sessions, debugSession{
			RemoteAddr: s.remoteAddr,
			Ua:         s.userAgent,
			Uid:        s.uid.String(),
			Sid:        sid,
			Clnode:     clnode,
			Subs:       keys,
		})
		return true
	})
	//主题。
	globals.hub.topics.Range(func(_, t any) bool {
		topic := t.(*Topic)
		psd := make([]string, 0, len(topic.sessions))
		for s := range topic.sessions {
			psd = append(psd, s.sid)
		}
		pud := make([]string, 0, len(topic.perUser))
		for uid := range topic.perUser {
			pud = append(pud, uid.String())
		}
		ps := make([]string, 0, len(topic.perSubs))
		for key := range topic.perSubs {
			ps = append(ps, key)
		}
		result.Topics = append(result.Topics, debugTopic{
			Topic:    topic.name,
			Xorig:    topic.xoriginal,
			IsProxy:  topic.isProxy,
			PerUser:  pud,
			PerSubs:  ps,
			Sessions: psd,
		})
		return true
	})
	for k, v := range usersCache {
		result.UserCache = append(result.UserCache, debugCachedUser{
			Uid:    k.UserId(),
			Unread: v.unread,
			Topics: v.topics,
		})
	}

	json.NewEncoder(wrt).Encode(result)
}
