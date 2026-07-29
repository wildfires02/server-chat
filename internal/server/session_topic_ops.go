package server

import (
	"strings"

	"chat/server/logs"
	"chat/server/store/types"
)

// get 查询并返回get。
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

// set 更新set。
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

// del 完成del所需的内部处理。
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
	case "read", "recv", "react", "pin":
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
