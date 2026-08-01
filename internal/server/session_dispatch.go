package server

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store/types"
)

const maxClientBatchMessages = 64

// decodeClientWireMessages 同时解析传统单包与 0.32+ 客户端批量信封。
// hi 必须仍然单独发送：只有握手完成后 Session 才能确认批量能力。
func decodeClientWireMessages(raw []byte, allowBatch bool) ([]*ClientComMessage, error) {
	var wire struct {
		ClientComMessage
		Batch json.RawMessage `json:"batch"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	if wire.Batch == nil {
		return []*ClientComMessage{&wire.ClientComMessage}, nil
	}
	if !allowBatch {
		return nil, errors.New("client batch requires protocol 0.32")
	}

	var batch []json.RawMessage
	if err := json.Unmarshal(wire.Batch, &batch); err != nil {
		return nil, err
	}
	if len(batch) == 0 || len(batch) > maxClientBatchMessages {
		return nil, errors.New("invalid client batch size")
	}

	messages := make([]*ClientComMessage, len(batch))
	for index := range batch {
		messages[index] = new(ClientComMessage)
		if err := json.Unmarshal(batch[index], messages[index]); err != nil {
			return nil, err
		}
	}
	return messages, nil
}

// dispatchRaw 收到原始网络数据报，转换为 ClientComMessage 并分发处理。
func (s *Session) dispatchRaw(raw []byte) {
	now := types.TimeNow()

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

	messages, err := decodeClientWireMessages(raw, s.supportsMessageBatching())
	if err != nil {
		// 畸形消息
		logs.Warn.Println("s.dispatch 格式错误:", err, s.sid)
		s.queueOut(ErrMalformed("", "", now))
		return
	}
	if s.proto == WEBSOCK && len(messages) > 1 {
		statsInc("IncomingWebsockBatchFramesTotal", 1)
		statsInc("IncomingWebsockBatchedMessagesTotal", len(messages))
	}

	for _, msg := range messages {
		s.dispatch(msg)
	}
}

// dispatch 处理dispatch消息或事件。
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

	case msg.Resume != nil:
		handler = checkVers(s.resume)
		msg.Id = msg.Resume.Id

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
		msg.Id = msg.Note.Id
		msg.Original = msg.Note.Topic
		uaRefresh = true

	default:
		// 未知消息类型
		s.queueOut(ErrMalformed("", "", msg.Timestamp))
		logs.Warn.Println("s.dispatch: 未知消息", s.sid)
		return
	}

	if clientMessageRequiresWrite(msg) && !serviceAllowsWrites() {
		// Drain、数据库异常或少数派状态下 fail closed；已存在连接仍可执行安全读请求。
		s.queueOut(ErrServiceUnavailableReply(msg, msg.Timestamp))
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
	// 在进入 Topic 串行队列前拒绝超长或非法 UTF-8 的客户端标识。
	if len(msg.Pub.ClientId) > 64 || !utf8.ValidString(msg.Pub.ClientId) {
		s.queueOut(ErrMalformedReply(msg, msg.Timestamp))
		return
	}
	if msg.Pub.ReplyTo < 0 || msg.Pub.ReplaceSeq < 0 ||
		(msg.Pub.ReplaceSeq > 0 && (msg.Pub.ReplyTo > 0 || msg.Pub.Forward != nil)) ||
		len(msg.Pub.GroupId) > 64 || !utf8.ValidString(msg.Pub.GroupId) {
		s.queueOut(ErrMalformedReply(msg, msg.Timestamp))
		return
	}

	var resp *ServerComMessage
	msg.RcptTo, resp = s.expandTopicName(msg)
	if resp != nil {
		s.queueOut(resp)
		return
	}

	if msg.Pub.Forward != nil {
		// 展开转发源 Topic，并拒绝当前会话无订阅权限的跨 Topic 引用。
		if msg.Pub.Forward.SeqId <= 0 {
			s.queueOut(ErrMalformedReply(msg, msg.Timestamp))
			return
		}
		if msg.Pub.Forward.Topic == "" {
			msg.Pub.Forward.Topic = msg.RcptTo
		} else {
			sourceMsg := *msg
			sourceMsg.Original = msg.Pub.Forward.Topic
			sourceTopic, sourceResp := s.expandTopicName(&sourceMsg)
			if sourceResp != nil {
				s.queueOut(sourceResp)
				return
			}
			if sourceTopic != msg.RcptTo && s.getSub(sourceTopic) == nil {
				s.queueOut(ErrPermissionDeniedReply(msg, msg.Timestamp))
				return
			}
			msg.Pub.Forward.Topic = sourceTopic
		}
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
