package server

import (
	"chat/api/pbx"
)

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
