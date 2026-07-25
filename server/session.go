/******************************************************************************
 *
 *  描述 :
 *
 *  处理用户会话/网络连接。单用户可拥有多个 session。
 *  每个 session 可同时处理多个 Topic 通信。
 *
 *****************************************************************************/

package main

import (
	"container/list"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"chat/pbx"
	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"

	"golang.org/x/text/language"
)

// 发送队列最大积压数，超过后 Session 将被判定为死连接并移除
const sendQueueLimit = 128

// HTTP 响应状态码分类的统计变量名称预计算数组。
// 按状态码/100 索引，有效范围 [2..5]。
var ctrlCodeStatNames = [6]string{
	"", "",
	"CtrlCodesTotal2xx",
	"CtrlCodesTotal3xx",
	"CtrlCodesTotal4xx",
	"CtrlCodesTotal5xx",
}

// 后台 Session 断开延迟发送 Presence 状态通知的超时时间
const deferredNotificationsTimeout = time.Second * 5

var minSupportedVersionValue = parseVersion(minSupportedVersion)

// SessionProto 表示底层网络传输协议类型。
type SessionProto int

// 各类底层网络传输协议的常量定义。
const (
	// NONE 表示未定义/未设置。
	NONE SessionProto = iota
	// WEBSOCK 表示 WebSocket 连接。
	WEBSOCK
	// LPOLL 表示 HTTP 长轮询 (Long Polling) 连接。
	LPOLL
	// GRPC 表示 gRPC 连接。
	GRPC
	// PROXY 表示在主节点用作代理的临时会话。
	PROXY
	// MULTIPLEX 表示从代理 Topic 到主节点的 multiplexing 多路复用会话。
	MULTIPLEX
)

// Session 表示单个 WebSocket 连接或长轮询会话。一个用户可拥有多个会话。
type Session struct {
	// 传输协议 - NONE (未设置), WEBSOCK, LPOLL, GRPC, PROXY, MULTIPLEX
	proto SessionProto

	// 会话 ID (Sid)
	sid string

	// Websocket 连接句柄，仅对于 WebSocket 会话设置。
	ws *websocket.Conn

	// sessionStore 中长轮询记录的指针，仅对于长轮询会话设置。
	lpTracker *list.Element

	// gRPC 节点句柄，仅对于 gRPC 客户端设置。
	grpcnode pbx.Node_MessageLoopServer

	// 产生该会话的集群节点引用，仅对于集群 RPC 会话设置。
	clnode *ClusterNode

	// 代理多路复用会话的引用，仅对于代理会话设置。
	multi        *Session
	proxiedTopic string

	// 客户端 IP 地址。对于长轮询为上次轮询时的 IP。
	remoteAddr string

	// 用户 User-Agent（认证客户端在 {login} 数据包中提供的标识）。
	userAgent string

	// 客户端协议版本号: ((major & 0xff) << 8) | (minor & 0xff)。
	ver int

	// 客户端设备 ID
	deviceID string
	// 设备平台: web, ios, android
	platf string
	// 客户端语言
	lang string
	// 客户端国家代码
	countryCode string

	// 当前用户 ID (Uid)。若会话未认证或为多路复用会话则可能为 0。
	uid types.Uid

	// 身份认证级别 - NONE (未设置), ANON, AUTH, ROOT。
	authLvl auth.Level

	// 长轮询会话上次刷新的时间
	lastTouched time.Time

	// 会话收到客户端任何数据包的时间戳
	lastAction int64

	// 定时器：在指定秒数后将后台会话标记为前台会话
	bkgTimer *time.Timer

	// 正在处理中的订阅/退订请求数计数器
	inflightReqs *boundedWaitGroup
	// 在集群模式下同步访问 Session 存储的互斥锁：
	// 订阅/退订响应是异步处理的。
	sessionStoreLock sync.Mutex
	// 标识该 Session 是否正在处于终止过程。
	// 一旦该标志翻转为 true，不得再向该 Session 的 send 管道写入任何数据。
	// 原子读写。0 = false, 1 = true
	terminating int32

	// 后台会话标识：订阅 presence 在线状态与通知将延迟发送。
	background bool

	// 待发送的下行消息缓冲管道。
	// 内容必须序列化为适合当前 Session 格式的数据。
	send chan any

	// 用于关闭终止 Session 的管道，缓冲大小为 1。
	stop chan any

	// detach - 用于从 Topic 解绑 Session 的缓冲管道。
	// 内容为要解绑的 Topic 名称。
	detach chan string

	// 当前 Session 订阅的 Topic 映射表，按 Topic 名称索引。
	// 切勿直接访问，请使用 Getter/Setter。
	subs map[string]*Subscription
	// 访问 subs 映射表读写锁：Topic 协程与网络协程会并发访问 subs。
	subsLock sync.RWMutex

	// 长轮询与 gRPC 模式下专用的互斥锁。
	lock sync.Mutex

	// 仅在集群模式下由 Topic 主节点使用的字段。

	// 正在处理的代理发给主节点请求的类型。
	proxyReq ProxyReqType
}

// Subscription 是 Session 到 Topic 之间映射关系的记录。
type Subscription struct {
	// 用于与 Topic 通信的管道，即 Topic.clientMsg 的副本
	broadcast chan<- *ClientComMessage

	// 当 Session 从 Topic 退订时发信号通知 Topic 的管道
	// 即 Topic.unreg 的副本
	done chan<- *ClientComMessage

	// 用于发送 {meta} 请求的管道，即 Topic.meta 的副本
	meta chan<- *ClientComMessage

	// 用于将 Session 状态更新 Ping 给 Topic 的管道，即 Topic.supd 的副本
	supd chan<- *sessionUpdate
}

func (s *Session) addSub(topic string, sub *Subscription) {
	if s.multi != nil {
		s.multi.addSub(topic, sub)
		return
	}
	s.subsLock.Lock()

	// 代理 Topic 与主节点连接介质的代理 Session 只能拥有一个订阅（即指向主 Topic）。
	// 普通 Session 可订阅多个 Topic。

	if !s.isMultiplex() || s.countSub() == 0 {
		s.subs[topic] = sub
	}
	s.subsLock.Unlock()
}

func (s *Session) getSub(topic string) *Subscription {
	s.subsLock.RLock()
	defer s.subsLock.RUnlock()

	return s.subs[topic]
}

func (s *Session) delSub(topic string) {
	if s.multi != nil {
		s.multi.delSub(topic)
		return
	}
	s.subsLock.Lock()
	delete(s.subs, topic)
	s.subsLock.Unlock()
}

func (s *Session) countSub() int {
	if s.multi != nil {
		return s.multi.countSub()
	}
	return len(s.subs)
}

// unsubAll 通知所有关联的 Topic 该 Session 正在被终止。
func (s *Session) unsubAll() {
	s.subsLock.RLock()
	defer s.subsLock.RUnlock()

	for _, sub := range s.subs {
		sub.done <- &ClientComMessage{sess: s, init: false}
	}
}

// isMultiplex 标识该 Session 是否为远端代理 Topic 的本地接口（多路复用多个 Session）。
func (s *Session) isMultiplex() bool {
	return s.proto == MULTIPLEX
}

// isProxy 标识该 Session 是否为远端 Session 的短生存期代理。
func (s *Session) isProxy() bool {
	return s.proto == PROXY
}

// isCluster 判断是否为集群 Session（代理 Session 或多路复用 Session）。
func (s *Session) isCluster() bool {
	return s.isProxy() || s.isMultiplex()
}

func (s *Session) scheduleClusterWriteLoop() {
	if globals.cluster != nil && globals.cluster.proxyEventQueue != nil {
		globals.cluster.proxyEventQueue.Schedule(
			func() { s.clusterWriteLoop(s.proxiedTopic) })
	}
}

func (s *Session) supportsMessageBatching() bool {
	switch s.proto {
	case WEBSOCK:
		return true
	case GRPC:
		return true
	default:
		return false
	}
}

// queueOutBatch 尝试将一批 ServerComMessage 消息发送给 Session 写循环。若发送缓冲区已满则返回 false。
func (s *Session) queueOutBatch(msgs []*ServerComMessage) bool {
	if s == nil {
		return true
	}
	if atomic.LoadInt32(&s.terminating) > 0 {
		return true
	}

	if s.multi != nil {
		// 集群模式下需传递实际 Session 的副本。
		for i := range msgs {
			msgs[i].sess = s
		}
		if s.multi.queueOutBatch(msgs) {
			s.multi.scheduleClusterWriteLoop()
			return true
		}
		return false
	}

	if s.supportsMessageBatching() {
		select {
		case s.send <- msgs:
		default:
			logs.Err.Println("s.queueOut: 会话发送队列已满", s.sid)
			return false
		}
		if s.isMultiplex() {
			s.scheduleClusterWriteLoop()
		}
	} else {
		for _, msg := range msgs {
			s.queueOut(msg)
		}
	}

	return true
}

// queueOut 尝试将单条 ServerComMessage 发送到 Session 写循环。若发送缓冲区已满则返回 false。
func (s *Session) queueOut(msg *ServerComMessage) bool {
	if s == nil {
		return true
	}
	if atomic.LoadInt32(&s.terminating) > 0 {
		return true
	}

	if s.multi != nil {
		msg.sess = s
		if s.multi.queueOut(msg) {
			s.multi.scheduleClusterWriteLoop()
			return true
		}
		return false
	}

	// 仅对 {ctrl} 消息与终端用户 Session 记录延迟时间。
	if msg.Ctrl != nil && msg.Id != "" {
		if !msg.Ctrl.Timestamp.IsZero() && !s.isCluster() {
			duration := time.Since(msg.Ctrl.Timestamp).Milliseconds()
			statsAddHistSample("RequestLatency", float64(duration))
		}
		if idx := msg.Ctrl.Code / 100; 2 <= idx && idx <= 5 {
			statsInc(ctrlCodeStatNames[idx], 1)
		} else {
			logs.Warn.Println("无效的响应码: ", msg.Ctrl.Code)
		}
	}

	select {
	case s.send <- msg:
	default:
		logs.Err.Println("s.queueOut: 会话发送队列已满", s.sid)
		return false
	}
	if s.isMultiplex() {
		s.scheduleClusterWriteLoop()
	}
	return true
}

// queueOutBytes 尝试发送已序列化为 []byte 的 ServerComMessage。若缓冲区已满则返回 false。
func (s *Session) queueOutBytes(data []byte) bool {
	if s == nil || atomic.LoadInt32(&s.terminating) > 0 {
		return true
	}

	select {
	case s.send <- data:
	default:
		logs.Err.Println("s.queueOutBytes: 会话发送队列已满", s.sid)
		return false
	}
	if s.isMultiplex() {
		s.scheduleClusterWriteLoop()
	}
	return true
}

func (s *Session) maybeScheduleClusterWriteLoop() {
	if s.multi != nil {
		s.multi.scheduleClusterWriteLoop()
		return
	}
	if s.isMultiplex() {
		s.scheduleClusterWriteLoop()
	}
}

func (s *Session) detachSession(fromTopic string) {
	if atomic.LoadInt32(&s.terminating) == 0 {
		s.detach <- fromTopic
		s.maybeScheduleClusterWriteLoop()
	}
}

func (s *Session) stopSession(data any) {
	s.stop <- data
	s.maybeScheduleClusterWriteLoop()
}

func (s *Session) purgeChannels() {
	for len(s.send) > 0 {
		<-s.send
	}
	for len(s.stop) > 0 {
		<-s.stop
	}
	for len(s.detach) > 0 {
		<-s.detach
	}
}

// cleanUp 在 Session 终止时被调用，用于执行资源清理。
func (s *Session) cleanUp(expired bool) {
	atomic.StoreInt32(&s.terminating, 1)
	s.purgeChannels()
	s.inflightReqs.Wait()
	s.inflightReqs = nil
	if !expired {
		s.sessionStoreLock.Lock()
		globals.sessionStore.Delete(s)
		s.sessionStoreLock.Unlock()
	}

	s.background = false
	s.bkgTimer.Stop()
	s.unsubAll()
	// 停止写循环。
	s.stopSession(nil)
}

// dispatchRaw 收到原始网络数据报，转换为 ClientComMessage 并分发处理。
func (s *Session) dispatchRaw(raw []byte) {
	now := types.TimeNow()
	var msg ClientComMessage

	if atomic.LoadInt32(&s.terminating) > 0 {
		logs.Warn.Println("s.dispatch: 在正在终止的会话上收到消息", s.sid)
		s.queueOut(ErrLocked("", "", now))
		return
	}

	if len(raw) == 1 && raw[0] == 0x31 {
		// 0x31 == '1'，网络探针消息。响应 '0'。
		s.queueOutBytes([]byte{0x30})
		return
	}

	toLog := raw
	truncated := ""
	if len(raw) > 512 {
		toLog = raw[:512]
		truncated = "<...>"
	}
	logs.Info.Printf("in: '%s%s' sid='%s' uid='%s'", toLog, truncated, s.sid, s.uid)

	if err := json.Unmarshal(raw, &msg); err != nil {
		// 畸形消息
		logs.Warn.Println("s.dispatch 格式错误:", err, s.sid)
		s.queueOut(ErrMalformed("", "", now))
		return
	}

	s.dispatch(&msg)
}

func (s *Session) dispatch(msg *ClientComMessage) {
	now := types.TimeNow()
	atomic.StoreInt64(&s.lastAction, now.UnixNano())

	// 插件系统优先拦截块。
	var resp *ServerComMessage
	if msg, resp = pluginFireHose(s, msg); resp != nil {
		// 插件直接提供了响应，无需进一步处理。
		s.queueOut(resp)
		return
	} else if msg == nil {
		// 插件请求静默丢弃该请求。
		return
	}

	authLvl := auth.LevelNone
	if msg.Extra != nil {
		authLvl = auth.ParseAuthLevel(msg.Extra.AuthLevel)
	}

	if msg.Extra == nil || (msg.Extra.AsUser == "" && authLvl == auth.LevelNone) {
		// 使用当前用户的 UID 和认证级别。
		msg.AsUser = s.uid.UserId()
		msg.AuthLvl = int(s.authLvl)
	} else if s.authLvl != auth.LevelRoot {
		// 仅超级管理员 (root) 用户可以替代其他用户发送消息或指定认证级别。
		s.queueOut(ErrPermissionDenied("", "", now))
		logs.Warn.Println("s.dispatch: 非 root 用户尝试指定 asUser", s.sid)
		return
	} else if fromUid := types.ParseUserId(msg.Extra.AsUser); fromUid.IsZero() {
		// 无效的 msg.Extra.AsUser。
		s.queueOut(ErrMalformed("", "", now))
		logs.Warn.Println("s.dispatch: 畸形的 asUser: ", msg.Extra.AsUser, s.sid)
		return
	} else {
		// 使用指定的 msg.Extra.AsUser
		msg.AsUser = msg.Extra.AsUser

		// 赋予指定的认证级别，如果未指定则默认为 LevelAuth。
		if authLvl == auth.LevelNone {
			msg.AuthLvl = int(auth.LevelAuth)
		} else {
			msg.AuthLvl = int(authLvl)
		}
	}

	msg.Timestamp = now

	var handler func(*ClientComMessage)
	var uaRefresh bool

	// 检查协议版本号 s.ver 是否已定义
	checkVers := func(handler func(*ClientComMessage)) func(*ClientComMessage) {
		return func(m *ClientComMessage) {
			if s.ver == 0 {
				logs.Warn.Println("s.dispatch: 缺少 {hi} 握手包", s.sid)
				s.queueOut(ErrCommandOutOfSequence(m.Id, m.Original, msg.Timestamp))
				return
			}
			handler(m)
		}
	}

	// 检查用户是否已登录
	checkUser := func(handler func(*ClientComMessage)) func(*ClientComMessage) {
		return func(m *ClientComMessage) {
			if msg.AsUser == "" {
				logs.Warn.Println("s.dispatch: 需要身份验证", s.sid)
				s.queueOut(ErrAuthRequiredReply(m, m.Timestamp))
				return
			}
			handler(m)
		}
	}

	switch {
	case msg.Pub != nil:
		handler = checkVers(checkUser(s.publish))
		msg.Id = msg.Pub.Id
		msg.Original = msg.Pub.Topic
		uaRefresh = true

	case msg.Sub != nil:
		handler = checkVers(checkUser(s.subscribe))
		msg.Id = msg.Sub.Id
		msg.Original = msg.Sub.Topic
		uaRefresh = true

	case msg.Leave != nil:
		handler = checkVers(checkUser(s.leave))
		msg.Id = msg.Leave.Id
		msg.Original = msg.Leave.Topic

	case msg.Hi != nil:
		handler = s.hello
		msg.Id = msg.Hi.Id

	case msg.Login != nil:
		handler = checkVers(s.login)
		msg.Id = msg.Login.Id

	case msg.Get != nil:
		handler = checkVers(checkUser(s.get))
		msg.Id = msg.Get.Id
		msg.Original = msg.Get.Topic
		uaRefresh = true

	case msg.Set != nil:
		handler = checkVers(checkUser(s.set))
		msg.Id = msg.Set.Id
		msg.Original = msg.Set.Topic
		uaRefresh = true

	case msg.Del != nil:
		handler = checkVers(checkUser(s.del))
		msg.Id = msg.Del.Id
		msg.Original = msg.Del.Topic

	case msg.Acc != nil:
		handler = checkVers(s.acc)
		msg.Id = msg.Acc.Id

	case msg.Note != nil:
		// 若用户未认证或版本号未设置，静默忽略 {note} 包。
		handler = s.note
		msg.Original = msg.Note.Topic
		uaRefresh = true

	default:
		// 未知消息类型
		s.queueOut(ErrMalformed("", "", msg.Timestamp))
		logs.Warn.Println("s.dispatch: 未知消息", s.sid)
		return
	}

	if globals.cluster.isPartitioned() {
		// 集群发生脑裂网络分区，当前节点处于小分区中。为了防止数据不一致，拒绝所有写请求。
		s.queueOut(ErrClusterUnreachableReply(msg, msg.Timestamp))
		return
	}

	msg.sess = s
	msg.init = true
	handler(msg)

	// 通知 'me' Topic 当前 Session 处于活跃状态。
	if uaRefresh && msg.AsUser != "" && s.userAgent != "" {
		if sub := s.getSub(msg.AsUser); sub != nil {
			sub.supd <- &sessionUpdate{userAgent: s.userAgent}
		}
	}
}

// subscribe 处理订阅 Topic 请求。
func (s *Session) subscribe(msg *ClientComMessage) {
	if strings.HasPrefix(msg.Original, "new") || strings.HasPrefix(msg.Original, "nch") {
		// 请求创建新的群组/频道 Topic。集群模式下确保新 Topic 属于当前节点。
		msg.RcptTo = globals.cluster.genLocalTopicName()
	} else {
		var resp *ServerComMessage
		msg.RcptTo, resp = s.expandTopicName(msg)
		if resp != nil {
			s.queueOut(resp)
			return
		}
	}

	s.inflightReqs.Add(1)
	// Session 一次只能代表单个用户订阅 Topic。
	if sub := s.getSub(msg.RcptTo); sub != nil {
		s.queueOut(InfoAlreadySubscribed(msg.Id, msg.Original, msg.Timestamp))
		s.inflightReqs.Done()
	} else {
		select {
		case globals.hub.join <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			s.inflightReqs.Done()
			logs.Err.Println("s.subscribe: hub.join 队列已满, topic ", msg.RcptTo, s.sid)
		}
	}
}

// leave 处理离开/退订 Topic 请求。
func (s *Session) leave(msg *ClientComMessage) {
	var resp *ServerComMessage
	msg.RcptTo, resp = s.expandTopicName(msg)
	if resp != nil {
		s.queueOut(resp)
		return
	}

	s.inflightReqs.Add(1)
	if sub := s.getSub(msg.RcptTo); sub != nil {
		if (msg.Original == "me" || msg.Original == "fnd") && msg.Leave.Unsub {
			// 用户不应退订 'me' 或 'find'，仅离开即可。
			s.queueOut(ErrPermissionDeniedReply(msg, msg.Timestamp))
			s.inflightReqs.Done()
		} else {
			// 解绑 Topic，Topic 将发送响应。
			sub.done <- msg
		}
		return
	}
	s.inflightReqs.Done()
	if !msg.Leave.Unsub {
		s.queueOut(InfoNotJoined(msg.Id, msg.Original, msg.Timestamp))
	} else {
		logs.Warn.Println("s.leave:", "必须先加入 Topic", s.sid)
		s.queueOut(ErrAttachFirst(msg, msg.Timestamp))
	}
}

// publish 广播消息给 Topic 所有订阅者。
func (s *Session) publish(msg *ClientComMessage) {
	var resp *ServerComMessage
	msg.RcptTo, resp = s.expandTopicName(msg)
	if resp != nil {
		s.queueOut(resp)
		return
	}

	// 如果代发消息，添加 "sender" 标头。
	if msg.AsUser != s.uid.UserId() {
		if msg.Pub.Head == nil {
			msg.Pub.Head = make(map[string]any)
		}
		msg.Pub.Head["sender"] = s.uid.UserId()
	} else if msg.Pub.Head != nil {
		// 清理潜在伪造的 "sender" 字段。
		delete(msg.Pub.Head, "sender")
		if len(msg.Pub.Head) == 0 {
			msg.Pub.Head = nil
		}
	}

	if sub := s.getSub(msg.RcptTo); sub != nil {
		select {
		case sub.broadcast <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.publish: sub.broadcast 管道已满, topic ", msg.RcptTo, s.sid)
		}
	} else if msg.RcptTo == "sys" {
		// 发送到 "sys" 系统 Topic 无需订阅。
		select {
		case globals.hub.routeCli <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.publish: hub.route 管道已满", s.sid)
		}
	} else {
		s.queueOut(ErrAttachFirst(msg, msg.Timestamp))
		logs.Warn.Printf("s.publish[%s]: 必须先加入 Topic %s", msg.RcptTo, s.sid)
	}
}

// hello 处理客户端 {hi} 握手包。
func (s *Session) hello(msg *ClientComMessage) {
	var params map[string]any
	var deviceIDUpdate bool

	if s.ver == 0 {
		s.ver = parseVersion(msg.Hi.Version)
		if s.ver == 0 {
			logs.Warn.Println("s.hello:", "解析版本号失败", s.sid)
			s.queueOut(ErrMalformed(msg.Id, "", msg.Timestamp))
			return
		}
		// 检查版本兼容性
		if versionCompare(s.ver, minSupportedVersionValue) < 0 {
			s.ver = 0
			s.queueOut(ErrVersionNotSupported(msg.Id, msg.Timestamp))
			logs.Warn.Println("s.hello:", "不支持的协议版本", s.sid)
			return
		}

		params = map[string]any{
			"ver":                currentVersion,
			"build":              store.Store.GetAdapterName() + ":" + buildstamp,
			"maxMessageSize":     globals.maxMessageSize,
			"maxSubscriberCount": globals.maxSubscriberCount,
			"minTagLength":       minTagLength,
			"maxTagLength":       maxTagLength,
			"maxTagCount":        globals.maxTagCount,
			"maxFileUploadSize":  globals.maxFileUploadSize,
			"reqCred":            globals.validatorClientConfig,
			"msgDelAge":          globals.msgDeleteAge.Seconds(),
		}
		if len(globals.iceServers) > 0 {
			params["iceServers"] = globals.iceServers
		}
		if globals.callEstablishmentTimeout > 0 {
			params["callTimeout"] = globals.callEstablishmentTimeout
		}

		if s.proto == GRPC {
			params["servingAt"] = globals.servingAt
			if globals.cluster != nil {
				params["clusterSize"] = len(globals.cluster.nodes) + 1
			} else {
				params["clusterSize"] = 1
			}
		}

		// 在 Session 初始化时保存 ua 与 platform，后续不可更改。
		s.userAgent = msg.Hi.UserAgent
		s.platf = msg.Hi.Platform
		if s.platf == "" {
			s.platf = platformFromUA(msg.Hi.UserAgent)
		}
		// 后台 Session 模式，启动延迟通知定时器。
		if msg.Hi.Background {
			s.bkgTimer.Reset(deferredNotificationsTimeout)
		}
	} else if msg.Hi.Version == "" || parseVersion(msg.Hi.Version) == s.ver {
		// 保存变更的设备 ID + 语言，或删除此前指定的设备 ID。
		if !s.uid.IsZero() {
			var err error
			if msg.Hi.DeviceID == types.NullValue {
				// 用户请求删除设备 ID。
				deviceIDUpdate = true
				if s.deviceID != "" {
					err = store.Devices.Delete(s.uid, s.deviceID)
				}
			} else if msg.Hi.DeviceID != "" && s.deviceID != msg.Hi.DeviceID {
				deviceIDUpdate = true
				err = store.Devices.Update(s.uid, s.deviceID, &types.DeviceDef{
					DeviceId: msg.Hi.DeviceID,
					Platform: s.platf,
					LastSeen: msg.Timestamp,
					Lang:     msg.Hi.Lang,
				})

				userChannelsSubUnsub(s.uid, msg.Hi.DeviceID, true)
			}

			if err != nil {
				s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
				logs.Warn.Println("s.hello:", "设备 ID 更新失败", err, s.sid)
				return
			}
		} else {
			s.queueOut(ErrAuthRequiredReply(msg, msg.Timestamp))
			logs.Warn.Println("s.hello:", "设备 ID 更新需要身份验证", s.sid)
			return
		}
	} else {
		s.queueOut(ErrCommandOutOfSequence(msg.Id, "", msg.Timestamp))
		logs.Warn.Println("s.hello:", "会话中途无法更改协议版本号", s.sid)
		return
	}

	if msg.Hi.DeviceID == types.NullValue {
		msg.Hi.DeviceID = ""
	}
	s.deviceID = msg.Hi.DeviceID
	s.lang = msg.Hi.Lang

	if s.countryCode == "" {
		if tag, _ := language.Parse(s.lang); tag != language.Und {
			if region, conf := tag.Region(); region.IsCountry() && conf >= language.High {
				s.countryCode = region.String()
			}
		}
	}

	if s.countryCode == "" {
		if len(s.lang) > 2 {
			logs.Warn.Println("s.hello:", "无法解析语言区域 ", s.lang)
		}
		s.countryCode = globals.defaultCountryCode
	}

	var httpStatus int
	var httpStatusText string
	if s.proto == LPOLL || deviceIDUpdate {
		httpStatus = http.StatusOK
		httpStatusText = "ok"
	} else {
		httpStatus = http.StatusCreated
		httpStatusText = "created"
	}

	ctrl := &MsgServerCtrl{Id: msg.Id, Code: httpStatus, Text: httpStatusText, Timestamp: msg.Timestamp}
	if len(params) > 0 {
		ctrl.Params = params
	}
	s.queueOut(&ServerComMessage{Ctrl: ctrl})
}

// acc 处理账号创建或属性修改 {acc}。
func (s *Session) acc(msg *ClientComMessage) {
	newAcc := strings.HasPrefix(msg.Acc.User, "new")

	var rec *auth.Rec
	if !newAcc && msg.Acc.TmpScheme != "" {
		if !s.uid.IsZero() {
			s.queueOut(ErrAlreadyAuthenticated(msg.Acc.Id, "", msg.Timestamp))
			logs.Warn.Println("s.acc: 已处于认证状态时接收到临时认证参数", s.sid)
			return
		}

		authHdl := store.Store.GetLogicalAuthHandler(msg.Acc.TmpScheme)
		if authHdl == nil {
			logs.Warn.Println("s.acc: 未知的认证方案", msg.Acc.TmpScheme, s.sid)
			s.queueOut(ErrAuthUnknownScheme(msg.Id, "", msg.Timestamp))
		}

		var err error
		rec, _, err = authHdl.Authenticate(msg.Acc.TmpSecret, s.remoteAddr)
		if err != nil {
			s.queueOut(decodeStoreError(err, msg.Acc.Id, msg.Timestamp,
				map[string]any{"what": "auth"}))
			logs.Warn.Println("s.acc: 临时认证无效", err, s.sid)
			return
		}
	}

	if newAcc {
		replyCreateUser(s, msg, rec)
	} else {
		replyUpdateUser(s, msg, rec)
	}
}

// login 处理用户登录验证 {login}。
func (s *Session) login(msg *ClientComMessage) {
	if msg.Login.Scheme == "reset" {
		if err := s.authSecretReset(msg.Login.Secret); err != nil {
			s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
		} else {
			s.queueOut(InfoAuthReset(msg.Id, msg.Timestamp))
		}
		return
	}

	if !s.uid.IsZero() {
		s.queueOut(ErrAlreadyAuthenticated(msg.Id, "", msg.Timestamp))
		return
	}

	handler := store.Store.GetLogicalAuthHandler(msg.Login.Scheme)
	if handler == nil {
		logs.Warn.Println("s.login: 未知的认证方案", msg.Login.Scheme, s.sid)
		s.queueOut(ErrAuthUnknownScheme(msg.Id, "", msg.Timestamp))
		return
	}

	rec, challenge, err := handler.Authenticate(msg.Login.Secret, s.remoteAddr)
	if err != nil {
		resp := decodeStoreError(err, msg.Id, msg.Timestamp, nil)
		if resp.Ctrl.Code >= 500 {
			logs.Warn.Println("s.login 内部错误:", err, s.sid)
		}
		s.queueOut(resp)
		return
	}

	if rec.State == types.StateUndefined {
		rec.State, err = userGetState(rec.Uid)
	}
	if err == nil && rec.State != types.StateOK {
		err = types.ErrPermissionDenied
	}

	if err != nil {
		logs.Warn.Println("s.login: 用户状态检查失败", rec.Uid, err, s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
		return
	}

	if challenge != nil {
		// 多阶段认证，向客户端下发 Challenge 挑战。
		s.queueOut(InfoChallenge(msg.Id, msg.Timestamp, challenge))
		return
	}

	var missing []string
	if rec.Features&auth.FeatureValidated == 0 && len(globals.authValidators[rec.AuthLevel]) > 0 {
		var validated []string
		if validated, _, err = validatedCreds(rec.Uid, rec.AuthLevel, msg.Login.Cred, false); err == nil {
			_, missing, _ = stringSliceDelta(globals.authValidators[rec.AuthLevel], validated)
		}
	}
	if err != nil {
		logs.Warn.Println("s.login: 验证凭证失败:", err, s.sid)
		s.queueOut(decodeStoreError(err, msg.Id, msg.Timestamp, nil))
	} else {
		s.queueOut(s.onLogin(msg.Id, msg.Timestamp, rec, missing))
	}
}

// authSecretReset 重置认证密钥；
// 参数格式: "auth-method-to-reset:credential-method:credential-value",
// 例如: "basic:email:alice@example.com"。
func (s *Session) authSecretReset(params []byte) error {
	var authScheme, credMethod, credValue string
	if parts := strings.Split(string(params), ":"); len(parts) >= 3 {
		authScheme, credMethod, credValue = parts[0], parts[1], parts[2]
	} else {
		return types.ErrMalformed
	}

	auther := store.Store.GetLogicalAuthHandler(authScheme)
	if auther == nil {
		return types.ErrUnsupported
	}
	validator := store.Store.GetValidator(credMethod)
	if validator == nil {
		return types.ErrUnsupported
	}
	uid, err := store.Users.GetByCred(credMethod, credValue)
	if err != nil {
		return err
	}
	if uid.IsZero() {
		// 防止探测已有联系人：若不存在也不报错
		return nil
	}

	resetParams, err := auther.GetResetParams(uid)
	if err != nil {
		return err
	}
	tempScheme, err := validator.TempAuthScheme()
	if err != nil {
		return err
	}

	tempAuth := store.Store.GetLogicalAuthHandler(tempScheme)
	if tempAuth == nil || !tempAuth.IsInitialized() {
		logs.Err.Println("s.authSecretReset: 验证器缺失临时认证", credMethod, tempScheme, s.sid)
		return types.ErrInternal
	}

	code, _, err := tempAuth.GenSecret(&auth.Rec{
		Uid:        uid,
		AuthLevel:  auth.LevelAuth,
		Features:   auth.FeatureNoLogin,
		Credential: credMethod + ":" + credValue,
	})
	if err != nil {
		return err
	}

	return validator.ResetSecret(credValue, authScheme, s.lang, code, resetParams)
}

// onLogin 登录成功后执行的相关步骤。
func (s *Session) onLogin(msgID string, timestamp time.Time, rec *auth.Rec, missing []string) *ServerComMessage {
	var reply *ServerComMessage
	var params map[string]any

	features := rec.Features

	params = map[string]any{
		"user":    rec.Uid.UserId(),
		"authlvl": rec.AuthLevel.String(),
	}
	if len(missing) > 0 {
		// 部分凭证尚未验证，下发验证请求。
		reply = InfoValidateCredentials(msgID, timestamp)

		params["cred"] = missing
	} else {
		// 全部正常，认证该 Session。

		reply = NoErr(msgID, "", timestamp)

		if features&auth.FeatureNoLogin == 0 {
			s.uid = rec.Uid
			if globals.sessionStore != nil {
				globals.sessionStore.SetSessionUid(s, rec.Uid)
			}
			s.authLvl = rec.AuthLevel
			rec.Lifetime = 0
		}
		features |= auth.FeatureValidated

		// 记录设备信息
		if s.deviceID != "" {
			if err := store.Devices.Update(rec.Uid, "", &types.DeviceDef{
				DeviceId: s.deviceID,
				Platform: s.platf,
				LastSeen: timestamp,
				Lang:     s.lang,
			}); err != nil {
				logs.Warn.Println("更新设备记录失败:", err)
			}
		}
	}

	rec.Features = features
	params["token"], params["expires"], _ = store.Store.GetLogicalAuthHandler("token").GenSecret(rec)

	reply.Ctrl.Params = params
	return reply
}

func (s *Session) get(msg *ClientComMessage) {
	var resp *ServerComMessage
	msg.RcptTo, resp = s.expandTopicName(msg)
	if resp != nil {
		s.queueOut(resp)
		return
	}

	msg.MetaWhat = parseMsgClientMeta(msg.Get.What)

	sub := s.getSub(msg.RcptTo)
	if msg.MetaWhat == 0 {
		s.queueOut(ErrMalformedReply(msg, msg.Timestamp))
		logs.Warn.Println("s.get: 无效的 Get 操作", msg.Get.What)
	} else if sub != nil {
		select {
		case sub.meta <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.get: sub.meta 管道已满, topic ", msg.RcptTo, s.sid)
		}
	} else if msg.MetaWhat&(constMsgMetaDesc|constMsgMetaSub) != 0 {
		select {
		case globals.hub.meta <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.get: hub.meta 管道已满", s.sid)
		}
	} else {
		logs.Warn.Println("s.get: 必须先订阅才能获取 get=", msg.Get.What)
		s.queueOut(ErrPermissionDeniedReply(msg, msg.Timestamp))
	}
}

func (s *Session) set(msg *ClientComMessage) {
	var resp *ServerComMessage
	msg.RcptTo, resp = s.expandTopicName(msg)
	if resp != nil {
		s.queueOut(resp)
		return
	}

	if msg.Set.Desc != nil {
		msg.MetaWhat = constMsgMetaDesc
	}
	if msg.Set.Sub != nil {
		msg.MetaWhat |= constMsgMetaSub
	}
	if msg.Set.Tags != nil {
		msg.MetaWhat |= constMsgMetaTags
	}
	if msg.Set.Cred != nil {
		msg.MetaWhat |= constMsgMetaCred
	}
	if msg.Set.Aux != nil {
		msg.MetaWhat |= constMsgMetaAux
	}

	if msg.MetaWhat == 0 {
		s.queueOut(ErrMalformedReply(msg, msg.Timestamp))
		logs.Warn.Println("s.set: Set 操作为空")
	} else if sub := s.getSub(msg.RcptTo); sub != nil {
		select {
		case sub.meta <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.set: sub.meta 管道已满, topic ", msg.RcptTo, s.sid)
		}
	} else if msg.MetaWhat&(constMsgMetaTags|constMsgMetaCred|constMsgMetaAux) != 0 {
		logs.Warn.Println("s.set: 设置标签/凭证/扩展字段仅限已订阅 Topic", msg.MetaWhat)
		s.queueOut(ErrPermissionDeniedReply(msg, msg.Timestamp))
	} else {
		select {
		case globals.hub.meta <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.set: hub.meta 管道已满", s.sid)
		}
	}
}

func (s *Session) del(msg *ClientComMessage) {
	msg.MetaWhat = parseMsgClientDel(msg.Del.What)

	if msg.MetaWhat == constMsgDelUser {
		replyDelUser(s, msg)
		return
	}

	var resp *ServerComMessage
	msg.RcptTo, resp = s.expandTopicName(msg)
	if resp != nil {
		s.queueOut(resp)
		return
	}

	if msg.MetaWhat == 0 {
		s.queueOut(ErrMalformedReply(msg, msg.Timestamp))
		logs.Warn.Println("s.del: 无效的 Del 操作", msg.Del.What, s.sid)
		return
	}

	if msg.MetaWhat == constMsgDelTopic {
		select {
		case globals.hub.unreg <- &topicUnreg{
			rcptTo: msg.RcptTo,
			pkt:    msg,
			sess:   s,
			del:    true,
		}:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.del: hub.unreg 管道已满", s.sid)
		}
	} else if sub := s.getSub(msg.RcptTo); sub != nil {
		select {
		case sub.meta <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.del: sub.meta 管道已满, topic ", msg.RcptTo, s.sid)
		}
	} else {
		s.queueOut(ErrAttachFirst(msg, msg.Timestamp))
		logs.Warn.Println("s.del: 未加入 Topic 时尝试执行 Del 操作", msg.Del.What, s.sid)
	}
}

// note 广播瞬态事件通知（如已读、输入中、通话事件）给活跃的 Topic 订阅者。不产生错误响应。
func (s *Session) note(msg *ClientComMessage) {
	if s.ver == 0 || msg.AsUser == "" {
		return
	}

	var resp *ServerComMessage
	msg.RcptTo, resp = s.expandTopicName(msg)
	if resp != nil {
		return
	}

	switch msg.Note.What {
	case "data":
		if msg.Note.Payload == nil {
			return
		}
	case "kp", "kpa", "kpv":
		if msg.Note.SeqId != 0 {
			return
		}
	case "call":
		if types.GetTopicCat(msg.RcptTo) != types.TopicCatP2P {
			return
		}
		fallthrough
	case "read", "recv":
		if msg.Note.SeqId <= 0 {
			return
		}
	default:
		return
	}

	if sub := s.getSub(msg.RcptTo); sub != nil {
		select {
		case sub.broadcast <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.note: sub.broacast 管道已满, topic ", msg.RcptTo, s.sid)
		}
	} else if msg.Note.What == "recv" || (msg.Note.What == "call" && (msg.Note.Event == "ringing" || msg.Note.Event == "hang-up" || msg.Note.Event == "accept")) {
		select {
		case globals.hub.routeCli <- msg:
		default:
			s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
			logs.Err.Println("s.note: hub.route 管道已满", s.sid)
		}
	} else {
		s.queueOut(ErrAttachFirst(msg, msg.Timestamp))
		logs.Warn.Println("s.note: 对未订阅的 Topic 发送事件通知", msg.Note.What, s.sid)
	}
}

// expandTopicName 将 Session 专属的 Topic 名称展开转换为全局可路由的名称。
// 返回:
//
//	Topic: 消息接收者可见的 Session 专属名称
//	routeTo: 全局可路由的 Topic 名称
//	err: 发生错误时返回给发送者的 *ServerComMessage 错误包
func (s *Session) expandTopicName(msg *ClientComMessage) (string, *ServerComMessage) {
	if msg.Original == "" {
		logs.Warn.Println("s.etn: Topic 名称为空", s.sid)
		return "", ErrMalformed(msg.Id, "", msg.Timestamp)
	}

	var routeTo string
	if msg.Original == "me" {
		routeTo = msg.AsUser
	} else if msg.Original == "fnd" {
		routeTo = types.ParseUserId(msg.AsUser).FndName()
	} else if msg.Original == "slf" {
		routeTo = types.ParseUserId(msg.AsUser).SlfName()
	} else if strings.HasPrefix(msg.Original, "usr") {
		// p2p Topic
		uid1 := types.ParseUserId(msg.AsUser)
		uid2 := types.ParseUserId(msg.Original)
		if uid2.IsZero() {
			logs.Warn.Println("s.etn: 解析 P2P Topic 名称失败", s.sid)
			return "", ErrMalformed(msg.Id, msg.Original, msg.Timestamp)
		} else if uid2 == uid1 {
			logs.Warn.Println("s.etn: 无效的 P2P 自呼叫订阅", s.sid)
			return "", ErrPermissionDeniedReply(msg, msg.Timestamp)
		}
		routeTo = uid1.P2PName(uid2)
	} else if tmp := types.ChnToGrp(msg.Original); tmp != "" {
		routeTo = tmp
	} else {
		routeTo = msg.Original
	}

	return routeTo, nil
}

func (s *Session) serializeAndUpdateStats(msg *ServerComMessage) any {
	dataSize, data := s.serialize(msg)
	if dataSize >= 0 {
		statsAddHistSample("OutgoingMessageSize", float64(dataSize))
	}
	return data
}

func (s *Session) serialize(msg *ServerComMessage) (int, any) {
	if s.proto == GRPC {
		msg := pbServSerialize(msg)
		return -1, msg
	}

	if s.isMultiplex() {
		return -1, msg
	}

	out, _ := json.Marshal(msg)
	return len(out), out
}

// onBackgroundTimer 定时触发，将后台 Session 标记为前台并通知订阅的 Topic。
func (s *Session) onBackgroundTimer() {
	s.subsLock.RLock()
	defer s.subsLock.RUnlock()

	update := &sessionUpdate{sess: s}
	for _, sub := range s.subs {
		if sub.supd != nil {
			sub.supd <- update
		}
	}
}
