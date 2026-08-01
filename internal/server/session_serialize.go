package server

import (
	"bytes"
	"encoding/json"

	"chat/api/pbx"
	"google.golang.org/protobuf/proto"
)

const maxJSONBatchFrameSize = 256 << 10

func (msg *ServerComMessage) serializedJSON() []byte {
	if msg == nil {
		return nil
	}
	msg.jsonOnce.Do(func() {
		msg.jsonWire, _ = json.Marshal(msg)
	})
	return msg.jsonWire
}

func (msg *ServerComMessage) serializedProto() *pbx.ServerMsg {
	if msg == nil {
		return nil
	}
	msg.protoOnce.Do(func() {
		msg.protoWire = pbServSerialize(msg)
	})
	return msg.protoWire
}

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
		msg := msg.serializedProto()
		return -1, msg
	}

	if s.isMultiplex() {
		return -1, msg
	}
	if s.wsBinary {
		out, err := proto.Marshal(&pbx.ServerBatch{Messages: []*pbx.ServerMsg{msg.serializedProto()}})
		if err != nil {
			return 0, []byte{}
		}
		return len(out), out
	}

	out := msg.serializedJSON()
	return len(out), out
}

// serializeBatchAndUpdateStats 把一组下行消息编码为少量有界 JSON 帧。
// WebSocket 本身已经提供消息边界，因此批量信封无需额外长度前缀。
// 单条超大消息允许独占一帧，避免为了满足批量上限再次拆分业务消息。
func (s *Session) serializeBatchAndUpdateStats(msgs []*ServerComMessage) [][]byte {
	if len(msgs) == 0 {
		return nil
	}
	if s.supportsProtobufWebSocket() {
		return s.serializeProtobufBatchAndUpdateStats(msgs)
	}

	const prefix = `{"batch":[`
	const suffix = `]}`
	frames := make([][]byte, 0, (len(msgs)+31)/32)
	buffer := bytes.NewBuffer(make([]byte, 0, min(maxJSONBatchFrameSize, len(msgs)*256)))
	buffer.WriteString(prefix)
	count := 0

	flush := func() {
		if count == 0 {
			return
		}
		buffer.WriteString(suffix)
		frame := append([]byte(nil), buffer.Bytes()...)
		statsAddHistSample("OutgoingMessageSize", float64(len(frame)))
		frames = append(frames, frame)
		buffer.Reset()
		buffer.WriteString(prefix)
		count = 0
	}

	for _, msg := range msgs {
		raw := msg.serializedJSON()
		if len(raw) == 0 {
			continue
		}
		separatorSize := 0
		if count > 0 {
			separatorSize = 1
		}
		if count > 0 && buffer.Len()+separatorSize+len(raw)+len(suffix) > maxJSONBatchFrameSize {
			flush()
		}
		if count > 0 {
			buffer.WriteByte(',')
		}
		buffer.Write(raw)
		count++
	}
	flush()
	return frames
}

// serializeProtobufBatchAndUpdateStats 编码 0.33+ WebSocket ServerBatch，
// 并按实际 Protobuf wire size 拆分，避免单次发送形成超大浏览器任务。
func (s *Session) serializeProtobufBatchAndUpdateStats(msgs []*ServerComMessage) [][]byte {
	frames := make([][]byte, 0, (len(msgs)+31)/32)
	batch := &pbx.ServerBatch{}
	flush := func() {
		if len(batch.Messages) == 0 {
			return
		}
		frame, err := proto.Marshal(batch)
		if err == nil {
			statsAddHistSample("OutgoingMessageSize", float64(len(frame)))
			frames = append(frames, frame)
		}
		batch.Messages = nil
	}

	for _, msg := range msgs {
		encoded := msg.serializedProto()
		batch.Messages = append(batch.Messages, encoded)
		if len(batch.Messages) > 1 && proto.Size(batch) > maxJSONBatchFrameSize {
			batch.Messages = batch.Messages[:len(batch.Messages)-1]
			flush()
			batch.Messages = append(batch.Messages, encoded)
		}
	}
	flush()
	return frames
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
