// 在 protobuf 结构体和 Go 数据包表示之间转换

// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"encoding/json"
	"time"

	"chat/pbx"
	"chat/server/logs"
	"chat/server/store/types"
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

// pbCliSerialize 将客户端消息及新增消息语义完整转换为 pbx.ClientMsg。
func pbCliSerialize(msg *ClientComMessage) *pbx.ClientMsg {
	var pkt pbx.ClientMsg

	switch {
	case msg.Hi != nil:
		pkt.Message = &pbx.ClientMsg_Hi{
			Hi: &pbx.ClientHi{
				Id:         msg.Hi.Id,
				UserAgent:  msg.Hi.UserAgent,
				Ver:        msg.Hi.Version,
				DeviceId:   msg.Hi.DeviceID,
				Platform:   msg.Hi.Platform,
				Lang:       msg.Hi.Lang,
				Background: msg.Hi.Background,
			},
		}
	case msg.Acc != nil:
		var authLevel pbx.AuthLevel
		switch msg.Acc.AuthLevel {
		case "NONE", "none", "":
			authLevel = pbx.AuthLevel_NONE
		case "ANON", "anon":
			authLevel = pbx.AuthLevel_ANON
		case "AUTH", "auth":
			authLevel = pbx.AuthLevel_AUTH
		case "ROOT", "root":
			// 这里不支持 ROOT。
			authLevel = pbx.AuthLevel_NONE
		}
		pkt.Message = &pbx.ClientMsg_Acc{
			Acc: &pbx.ClientAcc{
				Id:        msg.Acc.Id,
				UserId:    msg.Acc.User,
				State:     msg.Acc.State,
				TmpScheme: msg.Acc.TmpScheme,
				TmpSecret: msg.Acc.TmpSecret,
				AuthLevel: authLevel,
				Scheme:    msg.Acc.Scheme,
				Secret:    msg.Acc.Secret,
				Login:     msg.Acc.Login,
				Tags:      msg.Acc.Tags,
				Cred:      pbClientCredsSerialize(msg.Acc.Cred),
				Desc:      pbSetDescSerialize(msg.Acc.Desc),
			},
		}
	case msg.Login != nil:
		pkt.Message = &pbx.ClientMsg_Login{
			Login: &pbx.ClientLogin{
				Id:     msg.Login.Id,
				Scheme: msg.Login.Scheme,
				Secret: msg.Login.Secret,
				Cred:   pbClientCredsSerialize(msg.Login.Cred),
			},
		}
	case msg.Sub != nil:
		pkt.Message = &pbx.ClientMsg_Sub{
			Sub: &pbx.ClientSub{
				Id:       msg.Sub.Id,
				Topic:    msg.Sub.Topic,
				SetQuery: pbSetQuerySerialize(msg.Sub.Set),
				GetQuery: pbGetQuerySerialize(msg.Sub.Get),
			},
		}
	case msg.Leave != nil:
		pkt.Message = &pbx.ClientMsg_Leave{
			Leave: &pbx.ClientLeave{
				Id:    msg.Leave.Id,
				Topic: msg.Leave.Topic,
				Unsub: msg.Leave.Unsub,
			},
		}
	case msg.Pub != nil:
		var forward *pbx.MessageRef
		if msg.Pub.Forward != nil {
			forward = &pbx.MessageRef{
				Topic: msg.Pub.Forward.Topic,
				SeqId: int32(msg.Pub.Forward.SeqId),
			}
		}
		pkt.Message = &pbx.ClientMsg_Pub{
			Pub: &pbx.ClientPub{
				Id:         msg.Pub.Id,
				Topic:      msg.Pub.Topic,
				ClientId:   msg.Pub.ClientId,
				NoEcho:     msg.Pub.NoEcho,
				Kind:       msg.Pub.Kind,
				ReplyTo:    int32(msg.Pub.ReplyTo),
				ReplaceSeq: int32(msg.Pub.ReplaceSeq),
				Forward:    forward,
				GroupId:    msg.Pub.GroupId,
				ScheduleAt: timeToInt64(msg.Pub.ScheduleAt),
				Head:       interfaceMapToByteMap(msg.Pub.Head),
				Content:    interfaceToBytes(msg.Pub.Content),
			},
		}
	case msg.Get != nil:
		pkt.Message = &pbx.ClientMsg_Get{
			Get: &pbx.ClientGet{
				Id:    msg.Get.Id,
				Topic: msg.Get.Topic,
				Query: pbGetQuerySerialize(&msg.Get.MsgGetQuery),
			},
		}
	case msg.Set != nil:
		pkt.Message = &pbx.ClientMsg_Set{
			Set: &pbx.ClientSet{
				Id:    msg.Set.Id,
				Topic: msg.Set.Topic,
				Query: pbSetQuerySerialize(&msg.Set.MsgSetQuery),
			},
		}
	case msg.Del != nil:
		var what pbx.ClientDel_What
		switch msg.Del.What {
		case "msg":
			what = pbx.ClientDel_MSG
		case "topic":
			what = pbx.ClientDel_TOPIC
		case "sub":
			what = pbx.ClientDel_SUB
		case "user":
			what = pbx.ClientDel_USER
		case "cred":
			what = pbx.ClientDel_CRED
		case "sched":
			what = pbx.ClientDel_SCHEDULED
		}
		pkt.Message = &pbx.ClientMsg_Del{
			Del: &pbx.ClientDel{
				Id:          msg.Del.Id,
				Topic:       msg.Del.Topic,
				What:        what,
				DelSeq:      pbDelQuerySerialize(msg.Del.DelSeq),
				UserId:      msg.Del.User,
				Cred:        pbClientCredSerialize(msg.Del.Cred),
				Hard:        msg.Del.Hard,
				ScheduledId: msg.Del.ScheduledId,
			},
		}
	case msg.Note != nil:
		pkt.Message = &pbx.ClientMsg_Note{
			Note: &pbx.ClientNote{
				Id:       msg.Note.Id,
				Topic:    msg.Note.Topic,
				What:     pbInfoNoteWhatSerialize(msg.Note.What),
				SeqId:    int32(msg.Note.SeqId),
				Unread:   int32(msg.Note.Unread),
				Event:    pbCallEventSerialize(msg.Note.Event),
				Payload:  msg.Note.Payload,
				Reaction: msg.Note.Reaction,
				Remove:   msg.Note.Remove,
			},
		}
	}

	if pkt.Message == nil {
		return nil
	}

	if msg.Extra != nil {
		pkt.Extra = &pbx.ClientExtra{
			Attachments: msg.Extra.Attachments,
			OnBehalfOf:  msg.Extra.AsUser,
			AuthLevel:   pbx.AuthLevel(msg.AuthLvl),
		}
	}

	return &pkt
}

// pbCliDeserialize 将 pbx.ClientMsg 还原为与 JSON/WebSocket 入口一致的内部消息。
func pbCliDeserialize(pkt *pbx.ClientMsg) *ClientComMessage {
	var msg ClientComMessage
	if hi := pkt.GetHi(); hi != nil {
		msg.Hi = &MsgClientHi{
			Id:         hi.GetId(),
			UserAgent:  hi.GetUserAgent(),
			Version:    hi.GetVer(),
			DeviceID:   hi.GetDeviceId(),
			Platform:   hi.GetPlatform(),
			Lang:       hi.GetLang(),
			Background: hi.GetBackground(),
		}
	} else if acc := pkt.GetAcc(); acc != nil {
		msg.Acc = &MsgClientAcc{
			Id:        acc.GetId(),
			User:      acc.GetUserId(),
			State:     acc.GetState(),
			TmpScheme: acc.GetTmpScheme(),
			TmpSecret: acc.GetTmpSecret(),
			AuthLevel: acc.GetAuthLevel().String(),
			Scheme:    acc.GetScheme(),
			Secret:    acc.GetSecret(),
			Login:     acc.GetLogin(),
			Tags:      acc.GetTags(),
			Desc:      pbSetDescDeserialize(acc.GetDesc()),
			Cred:      pbClientCredsDeserialize(acc.GetCred()),
		}
	} else if login := pkt.GetLogin(); login != nil {
		msg.Login = &MsgClientLogin{
			Id:     login.GetId(),
			Scheme: login.GetScheme(),
			Secret: login.GetSecret(),
			Cred:   pbClientCredsDeserialize(login.GetCred()),
		}
	} else if sub := pkt.GetSub(); sub != nil {
		msg.Sub = &MsgClientSub{
			Id:    sub.GetId(),
			Topic: sub.GetTopic(),
			Get:   pbGetQueryDeserialize(sub.GetGetQuery()),
			Set:   pbSetQueryDeserialize(sub.GetSetQuery()),
		}
	} else if leave := pkt.GetLeave(); leave != nil {
		msg.Leave = &MsgClientLeave{
			Id:    leave.GetId(),
			Topic: leave.GetTopic(),
			Unsub: leave.GetUnsub(),
		}
	} else if pub := pkt.GetPub(); pub != nil {
		var forward *MsgMessageRef
		if fwd := pub.GetForward(); fwd != nil {
			forward = &MsgMessageRef{Topic: fwd.GetTopic(), SeqId: int(fwd.GetSeqId())}
		}
		msg.Pub = &MsgClientPub{
			Id:         pub.GetId(),
			Topic:      pub.GetTopic(),
			ClientId:   pub.GetClientId(),
			NoEcho:     pub.GetNoEcho(),
			Kind:       pub.GetKind(),
			ReplyTo:    int(pub.GetReplyTo()),
			ReplaceSeq: int(pub.GetReplaceSeq()),
			Forward:    forward,
			GroupId:    pub.GetGroupId(),
			ScheduleAt: int64ToTime(pub.GetScheduleAt()),
			Head:       byteMapToInterfaceMap(pub.GetHead()),
			Content:    bytesToInterface(pub.GetContent()),
		}
	} else if get := pkt.GetGet(); get != nil {
		msg.Get = &MsgClientGet{
			Id:    get.GetId(),
			Topic: get.GetTopic(),
		}
		if gq := get.GetQuery(); gq != nil {
			msg.Get.MsgGetQuery = *pbGetQueryDeserialize(gq)
		}
	} else if set := pkt.GetSet(); set != nil {
		msg.Set = &MsgClientSet{
			Id:    set.GetId(),
			Topic: set.GetTopic(),
		}
		if sq := set.GetQuery(); sq != nil {
			msg.Set.MsgSetQuery = *pbSetQueryDeserialize(sq)
		}
	} else if del := pkt.GetDel(); del != nil {
		msg.Del = &MsgClientDel{
			Id:          del.GetId(),
			Topic:       del.GetTopic(),
			DelSeq:      pbDelQueryDeserialize(del.GetDelSeq()),
			User:        del.GetUserId(),
			Cred:        pbClientCredDeserialize(del.GetCred()),
			Hard:        del.GetHard(),
			ScheduledId: del.GetScheduledId(),
		}
		switch del.GetWhat() {
		case pbx.ClientDel_MSG:
			msg.Del.What = "msg"
		case pbx.ClientDel_TOPIC:
			msg.Del.What = "topic"
		case pbx.ClientDel_SUB:
			msg.Del.What = "sub"
		case pbx.ClientDel_USER:
			msg.Del.What = "user"
		case pbx.ClientDel_CRED:
			msg.Del.What = "cred"
		case pbx.ClientDel_SCHEDULED:
			msg.Del.What = "sched"
		}
	} else if note := pkt.GetNote(); note != nil {
		msg.Note = &MsgClientNote{
			Id:       note.GetId(),
			Topic:    note.GetTopic(),
			SeqId:    int(note.GetSeqId()),
			What:     pbInfoNoteWhatDeserialize(note.GetWhat()),
			Unread:   int(note.GetUnread()),
			Event:    pbCallEventDeserialize(note.GetEvent()),
			Payload:  note.GetPayload(),
			Reaction: note.GetReaction(),
			Remove:   note.GetRemove(),
		}
	}

	if extra := pkt.GetExtra(); extra != nil {
		msg.Extra = &MsgClientExtra{
			Attachments: extra.GetAttachments(),
			AsUser:      extra.GetOnBehalfOf(),
			AuthLevel:   extra.GetAuthLevel().String(),
		}
	}

	return &msg
}

// interfaceMapToByteMap 完成interface映射ToByte映射所需的内部处理。
func interfaceMapToByteMap(in map[string]any) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for key, val := range in {
		if val != nil {
			out[key], _ = json.Marshal(val)
		}
	}
	return out
}

// byteMapToInterfaceMap 完成byte映射ToInterface映射所需的内部处理。
func byteMapToInterfaceMap(in map[string][]byte) map[string]any {
	out := make(map[string]any, len(in))
	for key, raw := range in {
		if val := bytesToInterface(raw); val != nil {
			out[key] = val
		}
	}
	return out
}

// interfaceToBytes 完成interfaceToBytes所需的内部处理。
func interfaceToBytes(in any) []byte {
	if in != nil {
		out, _ := json.Marshal(in)
		return out
	}
	return nil
}

// bytesToInterface 完成bytesToInterface所需的内部处理。
func bytesToInterface(in []byte) any {
	var out any
	if len(in) > 0 {
		err := json.Unmarshal(in, &out)
		if err != nil {
			logs.Warn.Println("pbx: failed to parse bytes", string(in), err)
		}
	}
	return out
}

// timeToInt64 将时间统一编码为 Epoch 毫秒；nil 编码为协议约定的 0。
func timeToInt64(ts *time.Time) int64 {
	if ts != nil {
		return ts.UnixNano() / int64(time.Millisecond)
	}
	return 0
}

// int64ToTime 从 Epoch 毫秒解码 UTC 时间；0 表示字段未设置。
func int64ToTime(ts int64) *time.Time {
	if ts > 0 {
		res := time.UnixMilli(ts).UTC()
		return &res
	}
	return nil
}

// pbGetQuerySerialize 完成pbGet查询Serialize所需的内部处理。
func pbGetQuerySerialize(in *MsgGetQuery) *pbx.GetQuery {
	if in == nil {
		return nil
	}

	out := &pbx.GetQuery{
		What: in.What,
	}

	if in.Desc != nil {
		out.Desc = &pbx.GetOpts{
			IfModifiedSince: timeToInt64(in.Desc.IfModifiedSince),
			User:            in.Desc.User,
			Topic:           in.Desc.Topic,
			Limit:           int32(in.Desc.Limit),
			Forward:         in.Desc.Forward,
		}
	}
	if in.Sub != nil {
		out.Sub = &pbx.GetOpts{
			IfModifiedSince: timeToInt64(in.Sub.IfModifiedSince),
			User:            in.Sub.User,
			Topic:           in.Sub.Topic,
			Limit:           int32(in.Sub.Limit),
			Forward:         in.Sub.Forward,
		}
	}
	if in.Data != nil {
		out.Data = &pbx.GetOpts{
			BeforeId: int32(in.Data.BeforeId),
			SinceId:  int32(in.Data.SinceId),
			Limit:    int32(in.Data.Limit),
			Forward:  in.Data.Forward,
		}

		if len(in.Data.IdRanges) > 0 {
			out.Data.Ranges = make([]*pbx.SeqRange, len(in.Data.IdRanges))
			for i, dq := range in.Data.IdRanges {
				out.Data.Ranges[i] = &pbx.SeqRange{Low: int32(dq.LowId), Hi: int32(dq.HiId)}
			}
		}
	}
	if in.Del != nil {
		out.Del = &pbx.GetOpts{
			BeforeId: int32(in.Del.BeforeId),
			SinceId:  int32(in.Del.SinceId),
			Limit:    int32(in.Del.Limit),
			Forward:  in.Del.Forward,
		}
		if len(in.Del.IdRanges) > 0 {
			out.Del.Ranges = make([]*pbx.SeqRange, len(in.Del.IdRanges))
			for i, dq := range in.Del.IdRanges {
				out.Del.Ranges[i] = &pbx.SeqRange{Low: int32(dq.LowId), Hi: int32(dq.HiId)}
			}
		}
	}
	if in.Search != nil {
		out.Search = &pbx.SearchOpts{
			Query:      in.Search.Query,
			Scope:      in.Search.Scope,
			FromUserId: in.Search.From,
			Kinds:      in.Search.Kinds,
			MinDate:    timeToInt64(in.Search.MinDate),
			MaxDate:    timeToInt64(in.Search.MaxDate),
			Cursor:     in.Search.Cursor,
			Limit:      int32(in.Search.Limit),
		}
	}
	return out
}

// pbGetQueryDeserialize 完成pbGet查询Deserialize所需的内部处理。
func pbGetQueryDeserialize(in *pbx.GetQuery) *MsgGetQuery {
	if in == nil {
		return nil
	}

	msg := MsgGetQuery{
		What: in.GetWhat(),
	}

	if desc := in.GetDesc(); desc != nil {
		msg.Desc = &MsgGetOpts{
			IfModifiedSince: int64ToTime(desc.GetIfModifiedSince()),
			User:            desc.GetUser(),
			Topic:           desc.GetTopic(),
			Limit:           int(desc.GetLimit()),
			Forward:         desc.GetForward(),
		}
	}
	if sub := in.GetSub(); sub != nil {
		msg.Sub = &MsgGetOpts{
			IfModifiedSince: int64ToTime(sub.GetIfModifiedSince()),
			User:            sub.GetUser(),
			Topic:           sub.GetTopic(),
			Limit:           int(sub.GetLimit()),
			Forward:         sub.GetForward(),
		}
	}
	if data := in.GetData(); data != nil {
		msg.Data = &MsgGetOpts{
			BeforeId: int(data.GetBeforeId()),
			SinceId:  int(data.GetSinceId()),
			Limit:    int(data.GetLimit()),
			Forward:  data.GetForward(),
		}

		if ranges := data.GetRanges(); len(ranges) > 0 {
			msg.Data.IdRanges = make([]MsgRange, len(ranges))
			for i, sr := range ranges {
				msg.Data.IdRanges[i].LowId = int(sr.GetLow())
				msg.Data.IdRanges[i].HiId = int(sr.GetHi())
			}
		}
	}
	if del := in.GetDel(); del != nil {
		msg.Del = &MsgGetOpts{
			BeforeId: int(del.GetBeforeId()),
			SinceId:  int(del.GetSinceId()),
			Limit:    int(del.GetLimit()),
			Forward:  del.GetForward(),
		}
		if ranges := del.GetRanges(); len(ranges) > 0 {
			msg.Del.IdRanges = make([]MsgRange, len(ranges))
			for i, sr := range ranges {
				msg.Del.IdRanges[i].LowId = int(sr.GetLow())
				msg.Del.IdRanges[i].HiId = int(sr.GetHi())
			}
		}
	}
	if search := in.GetSearch(); search != nil {
		msg.Search = &MsgSearchOpts{
			Query:   search.GetQuery(),
			Scope:   search.GetScope(),
			From:    search.GetFromUserId(),
			Kinds:   search.GetKinds(),
			MinDate: int64ToTime(search.GetMinDate()),
			MaxDate: int64ToTime(search.GetMaxDate()),
			Cursor:  search.GetCursor(),
			Limit:   int(search.GetLimit()),
		}
	}

	return &msg
}

// pbSetDescSerialize 完成pbSetDescSerialize所需的内部处理。
func pbSetDescSerialize(in *MsgSetDesc) *pbx.SetDesc {
	if in == nil {
		return nil
	}

	if in.DefaultAcs != nil || in.Public != nil || in.Trusted != nil || in.Private != nil {
		return &pbx.SetDesc{
			DefaultAcs: pbDefaultAcsSerialize(in.DefaultAcs),
			Public:     interfaceToBytes(in.Public),
			Trusted:    interfaceToBytes(in.Trusted),
			Private:    interfaceToBytes(in.Private),
		}
	}

	return nil
}

// pbSetDescDeserialize 完成pbSetDescDeserialize所需的内部处理。
func pbSetDescDeserialize(in *pbx.SetDesc) *MsgSetDesc {
	if in == nil {
		return nil
	}

	defacs := pbDefaultAcsDeserialize(in.GetDefaultAcs())
	public := in.GetPublic()
	trusted := in.GetTrusted()
	private := in.GetPrivate()

	if defacs != nil || public != nil || private != nil || trusted != nil {
		return &MsgSetDesc{
			DefaultAcs: defacs,
			Public:     bytesToInterface(public),
			Trusted:    bytesToInterface(trusted),
			Private:    bytesToInterface(private),
		}
	}

	return nil
}

// pbSetQuerySerialize 完成pbSet查询Serialize所需的内部处理。
func pbSetQuerySerialize(in *MsgSetQuery) *pbx.SetQuery {
	if in == nil {
		return nil
	}

	out := &pbx.SetQuery{
		Desc: pbSetDescSerialize(in.Desc),
	}

	if in.Sub != nil {
		out.Sub = &pbx.SetSub{
			UserId: in.Sub.User,
			Mode:   in.Sub.Mode,
			Role:   in.Sub.Role,
		}
	}

	out.Tags = in.Tags

	out.Cred = pbClientCredSerialize(in.Cred)

	return out
}

// pbSetQueryDeserialize 完成pbSet查询Deserialize所需的内部处理。
func pbSetQueryDeserialize(in *pbx.SetQuery) *MsgSetQuery {
	if in == nil {
		return nil
	}

	var msg *MsgSetQuery

	if desc := in.GetDesc(); desc != nil {
		msg = &MsgSetQuery{}
		msg.Desc = pbSetDescDeserialize(desc)
	}

	if sub := in.GetSub(); sub != nil {
		user := sub.GetUserId()
		mode := sub.GetMode()
		role := sub.GetRole()

		if user != "" || mode != "" || role != "" {
			if msg == nil {
				msg = &MsgSetQuery{}
			}

			msg.Sub = &MsgSetSub{
				User: sub.GetUserId(),
				Mode: sub.GetMode(),
				Role: sub.GetRole(),
			}
		}
	}

	if tags := in.GetTags(); tags != nil {
		if msg == nil {
			msg = &MsgSetQuery{}
		}
		msg.Tags = tags
	}

	if cred := in.GetCred(); cred != nil {
		if msg == nil {
			msg = &MsgSetQuery{}
		}
		msg.Cred = pbClientCredDeserialize(cred)
	}

	return msg
}

// pbInfoNoteWhatSerialize 将内部 note 类型映射为 Protobuf 枚举。
func pbInfoNoteWhatSerialize(what string) pbx.InfoNote {
	var out pbx.InfoNote
	switch what {
	case "kp":
		out = pbx.InfoNote_KP
	case "kpa":
		out = pbx.InfoNote_KPA
	case "kpv":
		out = pbx.InfoNote_KPV
	case "read":
		out = pbx.InfoNote_READ
	case "recv":
		out = pbx.InfoNote_RECV
	case "call":
		out = pbx.InfoNote_CALL
	case "react":
		out = pbx.InfoNote_REACT
	case "pin":
		out = pbx.InfoNote_PIN
	default:
		logs.Info.Println("unknown info-note.what", what)
	}
	return out
}

// pbInfoNoteWhatDeserialize 将 Protobuf note 枚举映射回内部字符串。
func pbInfoNoteWhatDeserialize(what pbx.InfoNote) string {
	var out string
	switch what {
	case pbx.InfoNote_KP:
		out = "kp"
	case pbx.InfoNote_KPA:
		out = "kpa"
	case pbx.InfoNote_KPV:
		out = "kpv"
	case pbx.InfoNote_READ:
		out = "read"
	case pbx.InfoNote_RECV:
		out = "recv"
	case pbx.InfoNote_CALL:
		out = "call"
	case pbx.InfoNote_REACT:
		out = "react"
	case pbx.InfoNote_PIN:
		out = "pin"
	default:
	}
	return out
}

// pbCallEventSerialize 完成pb通话事件Serialize所需的内部处理。
func pbCallEventSerialize(event string) pbx.CallEvent {
	var out pbx.CallEvent
	switch event {
	case "accept":
		out = pbx.CallEvent_ACCEPT
	case "answer":
		out = pbx.CallEvent_ANSWER
	case "hang-up":
		out = pbx.CallEvent_HANG_UP
	case "ice-candidate":
		out = pbx.CallEvent_ICE_CANDIDATE
	case "invite":
		out = pbx.CallEvent_INVITE
	case "offer":
		out = pbx.CallEvent_OFFER
	case "ringing":
		out = pbx.CallEvent_RINGING
	case "join":
		out = pbx.CallEvent_JOIN
	case "leave":
		out = pbx.CallEvent_LEAVE
	case "refresh":
		out = pbx.CallEvent_REFRESH
	case "":
		out = pbx.CallEvent_X2
	default:
		logs.Info.Println("unknown call event", event)
	}
	return out
}

// pbCallEventDeserialize 完成pb通话事件Deserialize所需的内部处理。
func pbCallEventDeserialize(event pbx.CallEvent) string {
	var out string
	switch event {
	case pbx.CallEvent_ACCEPT:
		out = "accept"
	case pbx.CallEvent_ANSWER:
		out = "answer"
	case pbx.CallEvent_HANG_UP:
		out = "hang-up"
	case pbx.CallEvent_ICE_CANDIDATE:
		out = "ice-candidate"
	case pbx.CallEvent_INVITE:
		out = "invite"
	case pbx.CallEvent_OFFER:
		out = "offer"
	case pbx.CallEvent_RINGING:
		out = "ringing"
	case pbx.CallEvent_JOIN:
		out = "join"
	case pbx.CallEvent_LEAVE:
		out = "leave"
	case pbx.CallEvent_REFRESH:
		out = "refresh"
	default:
	}
	return out
}

// pbAccessModeSerialize 完成pbAccess访问模式Serialize所需的内部处理。
func pbAccessModeSerialize(acs *MsgAccessMode) *pbx.AccessMode {
	if acs == nil {
		return nil
	}

	return &pbx.AccessMode{
		Want:  acs.Want,
		Given: acs.Given,
		Role:  acs.Role,
	}
}

// pbAccessModeDeserialize 完成pbAccess访问模式Deserialize所需的内部处理。
func pbAccessModeDeserialize(acs *pbx.AccessMode) *MsgAccessMode {
	if acs == nil {
		return nil
	}

	return &MsgAccessMode{
		Want:  acs.Want,
		Given: acs.Given,
		Role:  acs.Role,
	}
}

// pbDefaultAcsSerialize 完成pb默认AcsSerialize所需的内部处理。
func pbDefaultAcsSerialize(defacs *MsgDefaultAcsMode) *pbx.DefaultAcsMode {
	if defacs == nil {
		return nil
	}

	return &pbx.DefaultAcsMode{
		Auth: defacs.Auth,
		Anon: defacs.Anon,
	}
}

// pbDefaultAcsDeserialize 完成pb默认AcsDeserialize所需的内部处理。
func pbDefaultAcsDeserialize(defacs *pbx.DefaultAcsMode) *MsgDefaultAcsMode {
	if defacs == nil {
		return nil
	}

	auth := defacs.GetAuth()
	anon := defacs.GetAnon()

	if auth != "" || anon != "" {
		return &MsgDefaultAcsMode{
			Auth: auth,
			Anon: anon,
		}
	}
	return nil
}

// pbTopicDescSerialize 完成pbTopicDescSerialize所需的内部处理。
func pbTopicDescSerialize(desc *MsgTopicDesc) *pbx.TopicDesc {
	if desc == nil {
		return nil
	}
	out := &pbx.TopicDesc{
		CreatedAt: timeToInt64(desc.CreatedAt),
		UpdatedAt: timeToInt64(desc.UpdatedAt),
		TouchedAt: timeToInt64(desc.TouchedAt),
		State:     desc.State,
		Online:    desc.Online,
		IsChan:    desc.IsChan,
		Defacs:    pbDefaultAcsSerialize(desc.DefaultAcs),
		Acs:       pbAccessModeSerialize(desc.Acs),
		SeqId:     int32(desc.SeqId),
		ReadId:    int32(desc.ReadSeqId),
		RecvId:    int32(desc.RecvSeqId),
		DelId:     int32(desc.DelId),
		SubCount:  int32(desc.SubCnt),
		Public:    interfaceToBytes(desc.Public),
		Trusted:   interfaceToBytes(desc.Trusted),
		Private:   interfaceToBytes(desc.Private),
	}
	if desc.LastSeen != nil {
		out.LastSeenTime = timeToInt64(desc.LastSeen.When)
		out.LastSeenUserAgent = desc.LastSeen.UserAgent
	}
	return out
}

// pbTopicDescDeserialize 完成pbTopicDescDeserialize所需的内部处理。
func pbTopicDescDeserialize(desc *pbx.TopicDesc) *MsgTopicDesc {
	if desc == nil {
		return nil
	}
	out := &MsgTopicDesc{
		CreatedAt:  int64ToTime(desc.GetCreatedAt()),
		UpdatedAt:  int64ToTime(desc.GetUpdatedAt()),
		TouchedAt:  int64ToTime(desc.GetTouchedAt()),
		State:      desc.GetState(),
		Online:     desc.GetOnline(),
		IsChan:     desc.GetIsChan(),
		DefaultAcs: pbDefaultAcsDeserialize(desc.GetDefacs()),
		Acs:        pbAccessModeDeserialize(desc.GetAcs()),
		SeqId:      int(desc.SeqId),
		ReadSeqId:  int(desc.ReadId),
		RecvSeqId:  int(desc.RecvId),
		DelId:      int(desc.DelId),
		SubCnt:     int(desc.SubCount),
		Public:     bytesToInterface(desc.Public),
		Trusted:    bytesToInterface(desc.Trusted),
		Private:    bytesToInterface(desc.Private),
	}

	if desc.GetLastSeenTime() > 0 {
		out.LastSeen = &MsgLastSeenInfo{
			When:      int64ToTime(desc.GetLastSeenTime()),
			UserAgent: desc.GetLastSeenUserAgent(),
		}
	}
	return out
}

// pbTopicSerializeToDesc 完成pbTopicSerializeToDesc所需的内部处理。
func pbTopicSerializeToDesc(topic *Topic) *pbx.TopicDesc {
	if topic == nil {
		return nil
	}
	return &pbx.TopicDesc{
		CreatedAt: timeToInt64(&topic.created),
		UpdatedAt: timeToInt64(&topic.updated),
		IsChan:    topic.isChan,
		Defacs: &pbx.DefaultAcsMode{
			Auth: topic.accessAuth.String(),
			Anon: topic.accessAnon.String(),
		},
		SeqId:    int32(topic.lastID),
		DelId:    int32(topic.delID),
		SubCount: int32(topic.subCnt),
		Public:   interfaceToBytes(topic.public),
		Trusted:  interfaceToBytes(topic.trusted),
	}
}

// pbTopicSubSliceSerialize 完成pbTopic订阅SliceSerialize所需的内部处理。
func pbTopicSubSliceSerialize(subs []MsgTopicSub) []*pbx.TopicSub {
	if len(subs) == 0 {
		return nil
	}

	out := make([]*pbx.TopicSub, len(subs))
	for i := range subs {
		out[i] = pbTopicSubSerialize(&subs[i])
	}
	return out
}

// pbTopicSubSerialize 完成pbTopic订阅Serialize所需的内部处理。
func pbTopicSubSerialize(sub *MsgTopicSub) *pbx.TopicSub {
	out := &pbx.TopicSub{
		UpdatedAt: timeToInt64(sub.UpdatedAt),
		DeletedAt: timeToInt64(sub.DeletedAt),
		Online:    sub.Online,
		Acs:       pbAccessModeSerialize(&sub.Acs),
		ReadId:    int32(sub.ReadSeqId),
		RecvId:    int32(sub.RecvSeqId),
		Public:    interfaceToBytes(sub.Public),
		Trusted:   interfaceToBytes(sub.Trusted),
		Private:   interfaceToBytes(sub.Private),
		UserId:    sub.User,
		Topic:     sub.Topic,
		TouchedAt: timeToInt64(sub.TouchedAt),
		SeqId:     int32(sub.SeqId),
		DelId:     int32(sub.DelId),
		SubCount:  int32(sub.SubCnt),
	}
	if sub.LastSeen != nil {
		out.LastSeenTime = timeToInt64(sub.LastSeen.When)
		out.LastSeenUserAgent = sub.LastSeen.UserAgent
	}
	return out
}

// pbTopicSubSliceDeserialize 完成pbTopic订阅SliceDeserialize所需的内部处理。
func pbTopicSubSliceDeserialize(subs []*pbx.TopicSub) []MsgTopicSub {
	if len(subs) == 0 {
		return nil
	}

	out := make([]MsgTopicSub, len(subs))
	for i := range subs {
		out[i] = MsgTopicSub{
			UpdatedAt: int64ToTime(subs[i].GetUpdatedAt()),
			DeletedAt: int64ToTime(subs[i].GetDeletedAt()),
			Online:    subs[i].GetOnline(),
			ReadSeqId: int(subs[i].GetReadId()),
			RecvSeqId: int(subs[i].GetRecvId()),
			Public:    bytesToInterface(subs[i].GetPublic()),
			Trusted:   bytesToInterface(subs[i].GetTrusted()),
			Private:   bytesToInterface(subs[i].GetPrivate()),
			User:      subs[i].GetUserId(),
			Topic:     subs[i].GetTopic(),
			TouchedAt: int64ToTime(subs[i].GetTouchedAt()),
			SeqId:     int(subs[i].GetSeqId()),
			DelId:     int(subs[i].GetDelId()),
			SubCnt:    int(subs[i].GetSubCount()),
		}
		if acs := subs[i].GetAcs(); acs != nil {
			out[i].Acs = *pbAccessModeDeserialize(acs)
		}
		if subs[i].GetLastSeenTime() > 0 {
			out[i].LastSeen = &MsgLastSeenInfo{
				When:      int64ToTime(subs[i].GetLastSeenTime()),
				UserAgent: subs[i].GetLastSeenUserAgent(),
			}
		}
	}
	return out
}

// pbSubSliceDeserialize 完成pb订阅SliceDeserialize所需的内部处理。
func pbSubSliceDeserialize(subs []*pbx.TopicSub) []types.Subscription {
	if len(subs) == 0 {
		return nil
	}

	out := make([]types.Subscription, len(subs))
	for i := range subs {
		out[i] = types.Subscription{
			ObjHeader: types.ObjHeader{
				UpdatedAt: *int64ToTime(subs[i].GetUpdatedAt()),
			},
			DeletedAt: int64ToTime(subs[i].GetDeletedAt()),
			User:      subs[i].GetUserId(),
			Topic:     subs[i].GetTopic(),
			DelId:     int(subs[i].GetDelId()),
			Private:   bytesToInterface(subs[i].GetPrivate()),
		}
		out[i].SetPublic(bytesToInterface(subs[i].GetPublic()))
		out[i].SetTrusted(bytesToInterface(subs[i].GetTrusted()))
		if acs := subs[i].GetAcs(); acs != nil {
			out[i].ModeGiven.UnmarshalText([]byte(acs.GetGiven()))
			out[i].ModeWant.UnmarshalText([]byte(acs.GetWant()))
		}
		if subs[i].GetLastSeenTime() > 0 {
			out[i].SetLastSeenAndUA(int64ToTime(subs[i].GetLastSeenTime()),
				subs[i].GetLastSeenUserAgent())
		}
	}
	return out
}

// pbDelQuerySerialize 完成pbDel查询Serialize所需的内部处理。
func pbDelQuerySerialize(in []MsgRange) []*pbx.SeqRange {
	if in == nil {
		return nil
	}

	out := make([]*pbx.SeqRange, len(in))
	for i, dq := range in {
		out[i] = &pbx.SeqRange{Low: int32(dq.LowId), Hi: int32(dq.HiId)}
	}

	return out
}

// pbDelQueryDeserialize 完成pbDel查询Deserialize所需的内部处理。
func pbDelQueryDeserialize(in []*pbx.SeqRange) []MsgRange {
	if in == nil {
		return nil
	}

	out := make([]MsgRange, len(in))
	for i, sr := range in {
		out[i].LowId = int(sr.GetLow())
		out[i].HiId = int(sr.GetHi())
	}

	return out
}

// pbDelValuesSerialize 完成pbDelValuesSerialize所需的内部处理。
func pbDelValuesSerialize(in *MsgDelValues) *pbx.DelValues {
	if in == nil {
		return nil
	}

	return &pbx.DelValues{
		DelId:  int32(in.DelId),
		DelSeq: pbDelQuerySerialize(in.DelSeq),
	}
}

// pbDelValuesDeserialize 完成pbDelValuesDeserialize所需的内部处理。
func pbDelValuesDeserialize(in *pbx.DelValues) *MsgDelValues {
	if in == nil {
		return nil
	}

	return &MsgDelValues{
		DelId:  int(in.GetDelId()),
		DelSeq: pbDelQueryDeserialize(in.GetDelSeq()),
	}
}

// pbClientCredSerialize 完成pb客户端凭据Serialize所需的内部处理。
func pbClientCredSerialize(in *MsgCredClient) *pbx.ClientCred {
	if in == nil {
		return nil
	}

	return &pbx.ClientCred{
		Method:   in.Method,
		Value:    in.Value,
		Response: in.Response,
		Params:   interfaceMapToByteMap(in.Params),
	}
}

// pbClientCredsSerialize 完成pb客户端CredsSerialize所需的内部处理。
func pbClientCredsSerialize(in []MsgCredClient) []*pbx.ClientCred {
	if in == nil {
		return nil
	}

	out := make([]*pbx.ClientCred, len(in))
	for i := range in {
		out[i] = pbClientCredSerialize(&in[i])
	}

	return out
}

// pbClientCredDeserialize 完成pb客户端凭据Deserialize所需的内部处理。
func pbClientCredDeserialize(in *pbx.ClientCred) *MsgCredClient {
	if in == nil {
		return nil
	}

	return &MsgCredClient{
		Method:   in.GetMethod(),
		Value:    in.GetValue(),
		Response: in.GetResponse(),
		Params:   byteMapToInterfaceMap(in.GetParams()),
	}
}

// pbClientCredsDeserialize 完成pb客户端CredsDeserialize所需的内部处理。
func pbClientCredsDeserialize(in []*pbx.ClientCred) []MsgCredClient {
	if in == nil {
		return nil
	}

	out := make([]MsgCredClient, len(in))
	for i, cr := range in {
		out[i] = *pbClientCredDeserialize(cr)
	}

	return out
}

// pbServerCredsSerialize 完成pb服务端CredsSerialize所需的内部处理。
func pbServerCredsSerialize(in []*MsgCredServer) []*pbx.ServerCred {
	if in == nil {
		return nil
	}

	out := make([]*pbx.ServerCred, len(in))
	for i, cr := range in {
		out[i] = &pbx.ServerCred{
			Method: cr.Method,
			Value:  cr.Value,
		}
	}

	return out
}

// pbServerCredsDeserialize 完成pb服务端CredsDeserialize所需的内部处理。
func pbServerCredsDeserialize(in []*pbx.ServerCred) []*MsgCredServer {
	if in == nil {
		return nil
	}

	out := make([]*MsgCredServer, len(in))
	for i, cr := range in {
		out[i] = &MsgCredServer{
			Method: cr.GetMethod(),
			Value:  cr.GetValue(),
			Done:   cr.GetDone(),
		}
	}

	return out
}
