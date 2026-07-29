package server

import (
	"encoding/json"
)

// serializeAndUpdateStats 将输入编码为AndUpdateStats。
func (s *Session) serializeAndUpdateStats(msg *ServerComMessage) any {
	dataSize, data := s.serialize(msg)
	if dataSize >= 0 {
		statsAddHistSample("OutgoingMessageSize", float64(dataSize))
	}
	return data
}

// serialize 将输入编码为serialize。
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
