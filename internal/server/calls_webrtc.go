/******************************************************************************
 *
 *  描述 :
 *    WebRTC 点对点通话的接听、媒体信令转发和挂断处理。
 *
 *****************************************************************************/
package server

import (
	"time"

	"chat/server/logs"
	"chat/server/store/types"
)

// handleWebRTCCallEvent 处理 WebRTC 点对点通话事件。
func (t *Topic) handleWebRTCCallEvent(msg *ClientComMessage, asUid types.Uid) {
	call := msg.Note
	switch call.Event {
	case constCallEventRinging, constCallEventAccept:
		t.handleWebRTCCallAnswer(msg, asUid)
	case constCallEventOffer, constCallEventAnswer, constCallEventIceCandidate:
		t.forwardWebRTCSignal(msg)
	case constCallEventHangUp:
		if t.canHangUpWebRTCCall(msg, asUid) {
			t.maybeEndCallInProgress(msg.AsUser, msg, false)
		}
	default:
		logs.Warn.Printf("topic[%s]: WebRTC 通话 seq %d 收到未知事件 %q",
			t.name, t.currentCall.seq, call.Event)
	}
}

// handleWebRTCCallAnswer 处理振铃和接听，并将事件转发给主叫方。
func (t *Topic) handleWebRTCCallAnswer(msg *ClientComMessage, asUid types.Uid) {
	if len(t.currentCall.parties) != 1 {
		return
	}

	originatorUid, originator := t.getCallOriginator()
	if originator == nil {
		t.terminateCallInProgress(false)
		return
	}
	if originator.sid == msg.sess.sid || originatorUid == asUid {
		return
	}

	forward := t.currentCall.infoMessage(msg.Note.Event)
	forward.Info.From = msg.AsUser
	forward.Info.Topic = t.original(originatorUid)
	if msg.Note.Event == constCallEventAccept {
		if err := t.persistCallState(msg, constCallMsgAccepted, 0); err != nil {
			return
		}
		t.currentCall.parties[msg.sess.sid] = callPartyData{
			uid:  asUid,
			sess: callPartySession(msg.sess),
		}
		t.currentCall.acceptedAt = time.Now()
		t.infoCallSubsOffline(
			msg.AsUser,
			asUid,
			msg.Note.Event,
			t.currentCall.seq,
			msg.Note.Payload,
			msg.sess.sid,
			false,
		)
		t.callEstablishmentTimer.Stop()
	}
	originator.queueOut(forward)
}

// forwardWebRTCSignal 将 SDP 或 ICE 数据转发给另一个通话参与者。
func (t *Topic) forwardWebRTCSignal(msg *ClientComMessage) {
	if len(t.currentCall.parties) != 2 {
		logs.Warn.Printf("topic[%s]: WebRTC 通话参与者预期 2 人，实际 %d 人",
			t.name, len(t.currentCall.parties))
		return
	}
	if _, found := t.currentCall.parties[msg.sess.sid]; !found {
		logs.Warn.Printf("topic[%s]: WebRTC 信令来自非参与者会话 %s",
			t.name, msg.sess.sid)
		return
	}

	var targetUid types.Uid
	var targetSession *Session
	for sessionID, party := range t.currentCall.parties {
		if sessionID != msg.sess.sid {
			targetUid = party.uid
			targetSession = party.sess
			break
		}
	}
	if targetSession == nil {
		logs.Warn.Printf("topic[%s]: 未找到会话 %s 的 WebRTC 对端",
			t.name, msg.sess.sid)
		return
	}

	forward := t.currentCall.infoMessage(msg.Note.Event)
	forward.Info.From = msg.AsUser
	forward.Info.Topic = t.original(targetUid)
	forward.Info.Payload = msg.Note.Payload
	targetSession.queueOut(forward)
}

// canHangUpWebRTCCall 校验挂断请求是否来自合法参与者。
func (t *Topic) canHangUpWebRTCCall(msg *ClientComMessage, asUid types.Uid) bool {
	switch len(t.currentCall.parties) {
	case 2:
		_, found := t.currentCall.parties[msg.sess.sid]
		return found
	case 1:
		originatorUid, originator := t.getCallOriginator()
		if originator == nil {
			return false
		}
		return asUid != originatorUid || originator.sid == msg.sess.sid
	default:
		return true
	}
}
