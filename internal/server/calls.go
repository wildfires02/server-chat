/******************************************************************************
 *
 *  描述 :
 *    音视频通话的公共模型、生命周期和事件分发。
 *
 *****************************************************************************/
package server

import (
	"strconv"
	"time"

	"chat/server/logs"
	"chat/server/store/types"
)

// 音视频通话事件、提供方和持久化状态常量。
const (
	// 被呼叫方正在振铃，尚未接听。
	constCallEventRinging = "ringing"
	// 被呼叫方已接听。
	constCallEventAccept = "accept"
	// WebRTC SDP 和 ICE 协商事件。
	constCallEventOffer        = "offer"
	constCallEventAnswer       = "answer"
	constCallEventIceCandidate = "ice-candidate"
	// 任意参与者或服务器结束通话。
	constCallEventHangUp = "hang-up"
	// Agora 群组通话的加入、离开和 Token 续期事件。
	constCallEventJoin    = "join"
	constCallEventLeave   = "leave"
	constCallEventRefresh = "refresh"

	// WebRTC 表示端到端媒体协商通话提供方。
	constCallProviderWebRTC = "webrtc"
	// Agora 表示由 Agora RTC SDK 承载媒体流的群组通话提供方。
	constCallProviderAgora = "agora"

	// 通话已建立。
	constCallMsgAccepted = "accepted"
	// 通话正常结束。
	constCallMsgFinished = "finished"
	// 通话因异常中断。
	constCallMsgDisconnected = "disconnected"
	// 呼叫超时或由主叫方在接听前结束。
	constCallMsgMissed = "missed"
	// 被叫方在接听前拒绝呼叫。
	constCallMsgDeclined = "declined"
)

// callProvider 标识通话使用的媒体承载实现。
type callProvider string

// callPartyData 描述一个参与通话的客户端会话。
type callPartyData struct {
	// uid 是参与者的业务用户 ID。
	uid types.Uid
	// isOriginator 表示该参与者是否为呼叫发起方。
	isOriginator bool
	// sess 是接收通话事件的客户端会话。
	sess *Session
	// agora 仅保存 Agora 群组通话的参与者状态。
	agora *agoraPartyData
}

// videoCall 描述正在建立或进行中的一次音视频通话。
type videoCall struct {
	// parties 以 Session ID 为键保存参与者。
	parties map[string]callPartyData
	// seq 是通话邀请消息的序列号。
	seq int
	// content 是原始通话邀请消息的内容。
	content any
	// contentMime 是原始消息的 MIME 类型。
	contentMime any
	// acceptedAt 是通话首次接通的时间。
	acceptedAt time.Time
	// provider 区分 WebRTC P2P 与 Agora 群组通话。
	provider callProvider
	// agora 仅保存 Agora 群组通话的频道级状态。
	agora *agoraCallData
	// originatorUid 在发起者离线后继续保存其身份。
	originatorUid types.Uid
	// originatorSess 保存发起呼叫时的会话，供超时终止流程使用。
	originatorSess *Session
}

// callPartySession 返回适合保存在通话状态中的会话。
func callPartySession(sess *Session) *Session {
	if !sess.isProxy() {
		return sess
	}

	// Topic 宿主节点需要保存代理会话的快照，避免后续复用代理对象时
	// 改变当前通话所绑定的用户或连接。
	return &Session{
		proto:       PROXY,
		multi:       sess.multi,
		sid:         sess.sid,
		userAgent:   sess.userAgent,
		remoteAddr:  sess.remoteAddr,
		lang:        sess.lang,
		countryCode: sess.countryCode,
		proxyReq:    ProxyReqCall,
		background:  sess.background,
		uid:         sess.uid,
	}
}

// messageHead 生成替换通话邀请消息所需的消息头。
func (call *videoCall) messageHead(head map[string]any, state string, duration int) map[string]any {
	if head == nil {
		head = map[string]any{}
	}

	head["replace"] = ":" + strconv.Itoa(call.seq)
	head["webrtc"] = state
	if call.provider != "" {
		head["call-provider"] = string(call.provider)
	}
	if duration > 0 {
		head["webrtc-duration"] = duration
	} else {
		delete(head, "webrtc-duration")
	}
	if call.contentMime != nil {
		head["mime"] = call.contentMime
	}
	return head
}

// infoMessage 创建一个关联当前通话的服务器通知。
func (call *videoCall) infoMessage(event string) *ServerComMessage {
	return &ServerComMessage{
		Info: &MsgServerInfo{
			What:  "call",
			Event: event,
			SeqId: call.seq,
		},
	}
}

// getCallOriginator 返回当前通话发起人的用户和会话。
func (t *Topic) getCallOriginator() (types.Uid, *Session) {
	if t.currentCall == nil {
		return types.ZeroUid, nil
	}
	if !t.currentCall.originatorUid.IsZero() {
		return t.currentCall.originatorUid, t.currentCall.originatorSess
	}
	for _, party := range t.currentCall.parties {
		if party.isOriginator {
			return party.uid, party.sess
		}
	}
	return types.ZeroUid, nil
}

// handleCallInvite 根据 Topic 类型创建 P2P 或群组通话。
func (t *Topic) handleCallInvite(msg *ClientComMessage, asUid types.Uid) {
	provider := callProvider(constCallProviderWebRTC)
	var agoraState *agoraCallData
	if globals.agora != nil {
		provider = callProvider(constCallProviderAgora)
		agoraState = &agoraCallData{
			channel: globals.agora.channelName(t.name, t.lastID),
		}
	}

	originatorSession := callPartySession(msg.sess)
	t.currentCall = &videoCall{
		parties:        make(map[string]callPartyData),
		seq:            t.lastID,
		content:        msg.Pub.Content,
		contentMime:    msg.Pub.Head["mime"],
		provider:       provider,
		agora:          agoraState,
		originatorUid:  asUid,
		originatorSess: originatorSession,
	}
	t.currentCall.parties[msg.sess.sid] = callPartyData{
		uid:          asUid,
		isOriginator: true,
		sess:         originatorSession,
	}
	t.callEstablishmentTimer.Reset(time.Duration(globals.callEstablishmentTimeout) * time.Second)
}

// handleCallEvent 校验公共事件字段，然后交给对应媒体提供方处理。
func (t *Topic) handleCallEvent(msg *ClientComMessage) {
	if t.currentCall == nil {
		logs.Warn.Printf("topic[%s]: 当前无正在进行的音视频通话", t.name)
		return
	}
	if t.isInactive() {
		return
	}

	call := msg.Note
	if t.currentCall.seq != call.SeqId {
		logs.Info.Printf("topic[%s]: 通话 seq id 不匹配 - 当前 (%d) vs 收到 (%d)",
			t.name, t.currentCall.seq, call.SeqId)
		return
	}

	asUid := types.ParseUserId(msg.AsUser)
	if _, found := t.perUser[asUid]; !found {
		logs.Warn.Printf("topic[%s]: 未找到用户 %s", t.name, asUid.UserId())
		return
	}
	if err := t.checkOfficialPublish(asUid, "call", types.TimeNow()); err != nil {
		if msg.sess != nil {
			msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
		}
		return
	}

	switch t.currentCall.provider {
	case callProvider(constCallProviderAgora):
		t.handleAgoraCallEvent(msg, asUid)
	case "", callProvider(constCallProviderWebRTC):
		t.handleWebRTCCallEvent(msg, asUid)
	default:
		logs.Warn.Printf("topic[%s]: 通话 seq %d 使用未知提供方 %q",
			t.name, t.currentCall.seq, t.currentCall.provider)
	}
}

// persistCallState 保存并广播替换原通话邀请的状态消息。
func (t *Topic) persistCallState(msg *ClientComMessage, state string, duration int) error {
	originatorUid, _ := t.getCallOriginator()
	msgCopy := *msg
	msgCopy.AsUser = originatorUid.UserId()

	var originalHead map[string]any
	if msgCopy.Pub != nil {
		originalHead = msgCopy.Pub.Head
	}
	head := t.currentCall.messageHead(originalHead, state, duration)
	if err := t.saveAndBroadcastMessage(
		&msgCopy,
		originatorUid,
		false,
		nil,
		head,
		t.currentCall.content,
	); err != nil {
		logs.Err.Printf("topic[%s]: 保存通话状态 %q 失败 (seq %d): %v",
			t.name, state, t.currentCall.seq, err)
		return err
	}
	return nil
}

// maybeEndCallInProgress 根据挂断方和接通状态结束当前通话。
func (t *Topic) maybeEndCallInProgress(from string, msg *ClientComMessage, callDidTimeout bool) {
	if t.currentCall == nil {
		return
	}
	t.callEstablishmentTimer.Stop()

	originatorUid, _ := t.getCallOriginator()
	state := constCallMsgDisconnected
	var duration int64
	if from != "" && !t.currentCall.acceptedAt.IsZero() {
		state = constCallMsgFinished
		duration = time.Since(t.currentCall.acceptedAt).Milliseconds()
	} else if from != "" {
		if from == originatorUid.UserId() {
			state = constCallMsgMissed
		} else {
			state = constCallMsgDeclined
		}
	} else if callDidTimeout {
		state = constCallMsgMissed
	}

	if err := t.persistCallState(msg, state, int(duration)); err != nil {
		// 状态写入失败不应阻止释放内存中的通话和通知在线客户端。
		logs.Warn.Printf("topic[%s]: 通话 seq %d 将在状态保存失败后继续结束",
			t.name, t.currentCall.seq)
	}

	t.broadcastToSessions(t.currentCall.infoMessage(constCallEventHangUp))
	for target := range t.perUser {
		t.infoCallSubsOffline(from, target, constCallEventHangUp, t.currentCall.seq, nil, "", true)
	}
	t.currentCall = nil
}

// terminateCallInProgress 由服务器终止超时或失去发起人的通话。
func (t *Topic) terminateCallInProgress(callDidTimeout bool) {
	if t.currentCall == nil {
		return
	}
	uid, sess := t.getCallOriginator()
	if sess == nil || uid.IsZero() {
		logs.Warn.Printf("topic[%s]: 音视频通话 seq %d 缺少发起人，强制终止",
			t.name, t.currentCall.seq)
		t.currentCall = nil
		return
	}

	dummy := &ClientComMessage{
		Original:  t.original(uid),
		RcptTo:    uid.UserId(),
		AsUser:    uid.UserId(),
		Timestamp: types.TimeNow(),
		sess:      sess,
	}
	logs.Info.Printf("topic[%s]: 正在终止通话 seq %d, 是否超时: %t",
		t.name, t.currentCall.seq, callDidTimeout)
	t.maybeEndCallInProgress("", dummy, callDidTimeout)
}
