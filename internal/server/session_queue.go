package server

import (
	"sync/atomic"
	"time"

	"chat/server/logs"
)

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
