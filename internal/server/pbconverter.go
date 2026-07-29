// 在 protobuf 结构体和 Go 数据包表示之间转换

// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"time"

	"chat/api/pbx"
	"chat/server/logs"
)

// pbServCtrlSerializeBasic 完成pbServCtrlSerializeBasic所需的内部处理。
func pbServCtrlSerializeBasic(ctrl *MsgServerCtrl) *pbx.ServerCtrl {
	var params map[string][]byte
	if ctrl.Params != nil {
		if in, ok := ctrl.Params.(map[string]any); ok {
			params = interfaceMapToByteMap(in)
		}
	}

	return &pbx.ServerCtrl{
		Id:     ctrl.Id,
		Topic:  ctrl.Topic,
		Code:   int32(ctrl.Code),
		Text:   ctrl.Text,
		Params: params,
	}
}

// pbServCtrlSerialize 完成pbServCtrlSerialize所需的内部处理。
func pbServCtrlSerialize(ctrl *MsgServerCtrl) *pbx.ServerMsg_Ctrl {
	return &pbx.ServerMsg_Ctrl{
		Ctrl: pbServCtrlSerializeBasic(ctrl),
	}
}

// pbServDataSerialize 将包含回复、转发、相册、编辑和反应元数据的 data 包编码为 Protobuf。
func pbServDataSerialize(data *MsgServerData) *pbx.ServerMsg_Data {
	var forwarded *pbx.ForwardedMessage
	if data.Forwarded != nil {
		forwarded = &pbx.ForwardedMessage{
			Topic:      data.Forwarded.Topic,
			SeqId:      int32(data.Forwarded.SeqId),
			FromUserId: data.Forwarded.From,
			Timestamp:  timeToInt64(&data.Forwarded.Timestamp),
		}
	}
	// Protobuf 只承载反应标识和聚合计数，不传输服务端用户明细。
	reactions := make([]*pbx.Reaction, 0, len(data.Reactions))
	for _, reaction := range data.Reactions {
		reactions = append(reactions, &pbx.Reaction{
			Reaction: reaction.Reaction,
			Count:    int32(reaction.Count),
		})
	}
	return &pbx.ServerMsg_Data{
		Data: &pbx.ServerData{
			Topic:      data.Topic,
			FromUserId: data.From,
			Timestamp:  timeToInt64(&data.Timestamp),
			EditedAt:   timeToInt64(data.EditedAt),
			DeletedAt:  timeToInt64(data.DeletedAt),
			SeqId:      int32(data.SeqId),
			Head:       interfaceMapToByteMap(data.Head),
			Content:    interfaceToBytes(data.Content),
			ClientId:   data.ClientId,
			Kind:       data.Kind,
			ReplyTo:    int32(data.ReplyTo),
			Forwarded:  forwarded,
			GroupId:    data.GroupId,
			Reactions:  reactions,
		},
	}
}

// pbServPresSerialize 完成pbServPresSerialize所需的内部处理。
func pbServPresSerialize(pres *MsgServerPres) *pbx.ServerMsg_Pres {
	var what pbx.ServerPres_What
	switch pres.What {
	case "on":
		what = pbx.ServerPres_ON
	case "off":
		what = pbx.ServerPres_OFF
	case "ua":
		what = pbx.ServerPres_UA
	case "upd":
		what = pbx.ServerPres_UPD
	case "gone":
		what = pbx.ServerPres_GONE
	case "acs":
		what = pbx.ServerPres_ACS
	case "term":
		what = pbx.ServerPres_TERM
	case "msg":
		what = pbx.ServerPres_MSG
	case "read":
		what = pbx.ServerPres_READ
	case "recv":
		what = pbx.ServerPres_RECV
	case "del":
		what = pbx.ServerPres_DEL
	case "tags":
		what = pbx.ServerPres_TAGS
	case "aux":
		what = pbx.ServerPres_AUX
	default:
		logs.Info.Println("Unknown pres.what value", pres.What)
	}
	return &pbx.ServerMsg_Pres{
		Pres: &pbx.ServerPres{
			Topic:        pres.Topic,
			Src:          pres.Src,
			What:         what,
			UserAgent:    pres.UserAgent,
			SeqId:        int32(pres.SeqId),
			DelId:        int32(pres.DelId),
			DelSeq:       pbDelQuerySerialize(pres.DelSeq),
			TargetUserId: pres.AcsTarget,
			ActorUserId:  pres.AcsActor,
			Acs:          pbAccessModeSerialize(pres.Acs),
		},
	}
}

// pbServInfoSerialize 完成pbServ通知Serialize所需的内部处理。
func pbServInfoSerialize(info *MsgServerInfo) *pbx.ServerMsg_Info {
	return &pbx.ServerMsg_Info{
		Info: &pbx.ServerInfo{
			Topic:      info.Topic,
			FromUserId: info.From,
			Src:        info.Src,
			What:       pbInfoNoteWhatSerialize(info.What),
			SeqId:      int32(info.SeqId),
			Event:      pbCallEventSerialize(info.Event),
			Payload:    info.Payload,
			Reaction:   info.Reaction,
			Remove:     info.Remove,
		},
	}
}

// pbServMetaSerialize 完成pbServ元数据Serialize所需的内部处理。
func pbServMetaSerialize(meta *MsgServerMeta) *pbx.ServerMsg_Meta {
	return &pbx.ServerMsg_Meta{
		Meta: &pbx.ServerMeta{
			Id:     meta.Id,
			Topic:  meta.Topic,
			Desc:   pbTopicDescSerialize(meta.Desc),
			Sub:    pbTopicSubSliceSerialize(meta.Sub),
			Del:    pbDelValuesSerialize(meta.Del),
			Tags:   meta.Tags,
			Cred:   pbServerCredsSerialize(meta.Cred),
			Aux:    interfaceMapToByteMap(meta.Aux),
			Search: pbSearchResultSerialize(meta.Search),
		},
	}
}

// pbSearchResultSerialize 将统一搜索结果转换为 Protobuf。
func pbSearchResultSerialize(result *MsgSearchResult) *pbx.SearchResult {
	if result == nil {
		return nil
	}
	out := &pbx.SearchResult{
		Scope: result.Scope,
		Peers: pbTopicSubSliceSerialize(result.Peers),
		Next:  result.Next,
	}
	for _, message := range result.Messages {
		if message != nil {
			out.Messages = append(out.Messages, pbServDataSerialize(message).Data)
		}
	}
	return out
}

// pbSearchResultDeserialize 将 Protobuf 搜索结果还原为内部协议结构。
func pbSearchResultDeserialize(result *pbx.SearchResult) *MsgSearchResult {
	if result == nil {
		return nil
	}
	out := &MsgSearchResult{
		Scope: result.GetScope(),
		Peers: pbTopicSubSliceDeserialize(result.GetPeers()),
		Next:  result.GetNext(),
	}
	for _, message := range result.GetMessages() {
		if message == nil {
			continue
		}
		decoded := pbServDeserialize(&pbx.ServerMsg{
			Message: &pbx.ServerMsg_Data{Data: message},
		})
		if decoded != nil && decoded.Data != nil {
			out.Messages = append(out.Messages, decoded.Data)
		}
	}
	return out
}

// 将 ServerComMessage 转换为 pbx.ServerMsg
func pbServSerialize(msg *ServerComMessage) *pbx.ServerMsg {
	var pkt pbx.ServerMsg

	switch {
	case msg.Ctrl != nil:
		pkt.Message = pbServCtrlSerialize(msg.Ctrl)
	case msg.Data != nil:
		pkt.Message = pbServDataSerialize(msg.Data)
	case msg.Pres != nil:
		pkt.Message = pbServPresSerialize(msg.Pres)
	case msg.Info != nil:
		pkt.Message = pbServInfoSerialize(msg.Info)
	case msg.Meta != nil:
		pkt.Message = pbServMetaSerialize(msg.Meta)
	}

	return &pkt
}

// pbServDeserialize 将集群或 gRPC 收到的 Protobuf 服务端包还原为内部消息。
func pbServDeserialize(pkt *pbx.ServerMsg) *ServerComMessage {
	var msg ServerComMessage
	if ctrl := pkt.GetCtrl(); ctrl != nil {
		msg.Ctrl = &MsgServerCtrl{
			Id:     ctrl.GetId(),
			Topic:  ctrl.GetTopic(),
			Code:   int(ctrl.GetCode()),
			Text:   ctrl.GetText(),
			Params: byteMapToInterfaceMap(ctrl.GetParams()),
		}
	} else if data := pkt.GetData(); data != nil {
		tsptr := int64ToTime(data.GetTimestamp())
		if tsptr == nil {
			tsptr = &time.Time{}
		}
		var forwarded *MsgForwardedMessage
		if fwd := data.GetForwarded(); fwd != nil {
			fwdTs := int64ToTime(fwd.GetTimestamp())
			if fwdTs == nil {
				fwdTs = &time.Time{}
			}
			forwarded = &MsgForwardedMessage{
				Topic:     fwd.GetTopic(),
				SeqId:     int(fwd.GetSeqId()),
				From:      fwd.GetFromUserId(),
				Timestamp: *fwdTs,
			}
		}
		reactions := make([]MsgReaction, 0, len(data.GetReactions()))
		for _, reaction := range data.GetReactions() {
			reactions = append(reactions, MsgReaction{
				Reaction: reaction.GetReaction(),
				Count:    int(reaction.GetCount()),
			})
		}
		msg.Data = &MsgServerData{
			Topic:     data.GetTopic(),
			From:      data.GetFromUserId(),
			Timestamp: *tsptr,
			EditedAt:  int64ToTime(data.GetEditedAt()),
			DeletedAt: int64ToTime(data.GetDeletedAt()),
			SeqId:     int(data.GetSeqId()),
			Head:      byteMapToInterfaceMap(data.GetHead()),
			Content:   bytesToInterface(data.GetContent()),
			ClientId:  data.GetClientId(),
			Kind:      data.GetKind(),
			ReplyTo:   int(data.GetReplyTo()),
			Forwarded: forwarded,
			GroupId:   data.GetGroupId(),
			Reactions: reactions,
		}
	} else if pres := pkt.GetPres(); pres != nil {
		var what string
		switch pres.GetWhat() {
		case pbx.ServerPres_ON:
			what = "on"
		case pbx.ServerPres_OFF:
			what = "off"
		case pbx.ServerPres_UA:
			what = "ua"
		case pbx.ServerPres_UPD:
			what = "upd"
		case pbx.ServerPres_GONE:
			what = "gone"
		case pbx.ServerPres_ACS:
			what = "acs"
		case pbx.ServerPres_TERM:
			what = "term"
		case pbx.ServerPres_MSG:
			what = "msg"
		case pbx.ServerPres_READ:
			what = "read"
		case pbx.ServerPres_RECV:
			what = "recv"
		case pbx.ServerPres_DEL:
			what = "del"
		case pbx.ServerPres_TAGS:
			what = "tags"
		case pbx.ServerPres_AUX:
			what = "aux"
		}
		msg.Pres = &MsgServerPres{
			Topic:     pres.GetTopic(),
			Src:       pres.GetSrc(),
			What:      what,
			UserAgent: pres.GetUserAgent(),
			SeqId:     int(pres.GetSeqId()),
			DelId:     int(pres.GetDelId()),
			DelSeq:    pbDelQueryDeserialize(pres.GetDelSeq()),
			AcsTarget: pres.GetTargetUserId(),
			AcsActor:  pres.GetActorUserId(),
			Acs:       pbAccessModeDeserialize(pres.GetAcs()),
		}
	} else if info := pkt.GetInfo(); info != nil {
		msg.Info = &MsgServerInfo{
			Topic:    info.GetTopic(),
			Src:      info.GetSrc(),
			From:     info.GetFromUserId(),
			What:     pbInfoNoteWhatDeserialize(info.GetWhat()),
			SeqId:    int(info.GetSeqId()),
			Event:    pbCallEventDeserialize(info.GetEvent()),
			Payload:  info.GetPayload(),
			Reaction: info.GetReaction(),
			Remove:   info.GetRemove(),
		}
	} else if meta := pkt.GetMeta(); meta != nil {
		msg.Meta = &MsgServerMeta{
			Id:     meta.GetId(),
			Topic:  meta.GetTopic(),
			Desc:   pbTopicDescDeserialize(meta.GetDesc()),
			Sub:    pbTopicSubSliceDeserialize(meta.GetSub()),
			Del:    pbDelValuesDeserialize(meta.GetDel()),
			Tags:   meta.GetTags(),
			Cred:   pbServerCredsDeserialize(meta.GetCred()),
			Aux:    byteMapToInterfaceMap(meta.GetAux()),
			Search: pbSearchResultDeserialize(meta.GetSearch()),
		}
	}
	return &msg
}
