package server

import (
	"chat/api/pbx"
	"chat/server/logs"
)

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
