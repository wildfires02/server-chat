package server

import (
	"sync/atomic"
	"time"

	"chat/api/pbx"
	"chat/server/logs"
	"google.golang.org/protobuf/proto"
)

func (s *Session) protobufQueueSize(messages []*ServerComMessage) int64 {
	if len(messages) == 0 {
		return 0
	}
	if s.proto == GRPC || s.isMultiplex() {
		var total int64
		for _, message := range messages {
			total += int64(proto.Size(message.serializedProto()))
		}
		return total
	}
	batch := &pbx.ServerBatch{Messages: make([]*pbx.ServerMsg, 0, len(messages))}
	for _, message := range messages {
		batch.Messages = append(batch.Messages, message.serializedProto())
	}
	return int64(proto.Size(batch))
}

func jsonBatchQueueSize(messages []*ServerComMessage, batched bool) int64 {
	if !batched {
		var total int64
		for _, message := range messages {
			total += int64(len(message.serializedJSON()))
		}
		return total
	}
	const prefixSize = int64(len(`{"batch":[`))
	const suffixSize = int64(len(`]}`))
	var total, current int64
	count := 0
	for _, message := range messages {
		size := int64(len(message.serializedJSON()))
		separator := int64(0)
		if count > 0 {
			separator = 1
		}
		if count > 0 && prefixSize+current+separator+size+suffixSize > maxJSONBatchFrameSize {
			total += prefixSize + current + suffixSize
			current = 0
			count = 0
			separator = 0
		}
		current += separator + size
		count++
	}
	if count > 0 {
		total += prefixSize + current + suffixSize
	}
	return total
}

func (s *Session) outboundQueueSize(value any) int64 {
	switch item := value.(type) {
	case nil:
		return 0
	case []byte:
		return int64(len(item))
	case string:
		return int64(len(item))
	case *ServerComMessage:
		if s.supportsProtobufWebSocket() || s.proto == GRPC || s.isMultiplex() {
			return s.protobufQueueSize([]*ServerComMessage{item})
		}
		if s.supportsMessageBatching() {
			// The writer may merge this item with the following queue entries.
			// Reserve the one-message envelope as a safe transport-exact upper bound.
			return jsonBatchQueueSize([]*ServerComMessage{item}, true)
		}
		return int64(len(item.serializedJSON()))
	case []*ServerComMessage:
		if s.supportsProtobufWebSocket() || s.proto == GRPC || s.isMultiplex() {
			return s.protobufQueueSize(item)
		}
		return jsonBatchQueueSize(item, s.supportsMessageBatching())
	default:
		// Raw probe frames are tiny; use a conservative fixed accounting value.
		return 256
	}
}

func (s *Session) reserveOutbound(value any) (int64, bool) {
	size := s.outboundQueueSize(value)
	if size <= 0 {
		size = 1
	}
	for {
		pending := s.sendPendingBytes.Load()
		if size > sendQueueByteLimit-pending {
			statsInc("OutgoingQueueByteLimitExceeded", 1)
			return size, false
		}
		if s.sendPendingBytes.CompareAndSwap(pending, pending+size) {
			return size, true
		}
	}
}

func (s *Session) releaseOutbound(value any) {
	size := s.outboundQueueSize(value)
	if size <= 0 {
		size = 1
	}
	for {
		pending := s.sendPendingBytes.Load()
		if pending <= 0 {
			return
		}
		next := pending - size
		if next < 0 {
			next = 0
		}
		if s.sendPendingBytes.CompareAndSwap(pending, next) {
			return
		}
	}
}

// scheduleClusterWriteLoop 持续运行schedule集群WriteLoop，直到输入通道关闭或收到停止信号。
func (s *Session) scheduleClusterWriteLoop() {
	if globals.cluster != nil && globals.cluster.proxyEventQueue != nil {
		if !s.clusterWriterScheduled.CompareAndSwap(false, true) {
			return
		}
		globals.cluster.proxyEventQueue.Schedule(
			func() { s.clusterWriteLoop(s.proxiedTopic) })
	}
}

// supportsMessageBatching 完成supports消息Batching所需的内部处理。
func (s *Session) supportsMessageBatching() bool {
	return s.proto == WEBSOCK && versionCompare(s.ver, minBatchVersionValue) >= 0
}

// supportsProtobufWebSocket 表示当前连接同时完成了 HTTP 子协议与 hi 版本协商。
func (s *Session) supportsProtobufWebSocket() bool {
	return s.proto == WEBSOCK && s.wsBinary &&
		versionCompare(s.ver, minProtobufWebSocketVersionValue) >= 0
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

	// WebSocket 与 gRPC 都在队列中保存切片，避免大段历史记录占满队列。
	// 各写循环再根据传输类型和协议版本决定批量编码还是逐条发送。
	if s.proto == WEBSOCK || s.proto == GRPC {
		_, reserved := s.reserveOutbound(msgs)
		if !reserved {
			logs.Err.Println("s.queueOutBatch: 会话发送字节队列已满", s.sid)
			return false
		}
		select {
		case s.send <- msgs:
		default:
			s.releaseOutbound(msgs)
			logs.Err.Println("s.queueOut: 会话发送队列已满", s.sid)
			return false
		}
		if s.isMultiplex() {
			s.scheduleClusterWriteLoop()
		}
	} else {
		for _, msg := range msgs {
			if !s.queueOut(msg) {
				return false
			}
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

	_, reserved := s.reserveOutbound(msg)
	if !reserved {
		logs.Err.Println("s.queueOut: 会话发送字节队列已满", s.sid)
		return false
	}
	select {
	case s.send <- msg:
	default:
		s.releaseOutbound(msg)
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

	_, reserved := s.reserveOutbound(data)
	if !reserved {
		logs.Err.Println("s.queueOutBytes: 会话发送字节队列已满", s.sid)
		return false
	}
	select {
	case s.send <- data:
	default:
		s.releaseOutbound(data)
		logs.Err.Println("s.queueOutBytes: 会话发送队列已满", s.sid)
		return false
	}
	if s.isMultiplex() {
		s.scheduleClusterWriteLoop()
	}
	return true
}

// maybeScheduleClusterWriteLoop 持续运行maybeSchedule集群WriteLoop，直到输入通道关闭或收到停止信号。
func (s *Session) maybeScheduleClusterWriteLoop() {
	if s.multi != nil {
		s.multi.scheduleClusterWriteLoop()
		return
	}
	if s.isMultiplex() {
		s.scheduleClusterWriteLoop()
	}
}

// detachSession 完成detach会话所需的内部处理。
func (s *Session) detachSession(fromTopic string) {
	if atomic.LoadInt32(&s.terminating) == 0 {
		s.detach <- fromTopic
		s.maybeScheduleClusterWriteLoop()
	}
}

// stopSession 停止会话并释放相关资源。
func (s *Session) stopSession(data any) {
	s.stop <- data
	s.maybeScheduleClusterWriteLoop()
}

// purgeChannels 完成purgeChannels所需的内部处理。
func (s *Session) purgeChannels() {
	for len(s.send) > 0 {
		msg := <-s.send
		s.releaseOutbound(msg)
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
