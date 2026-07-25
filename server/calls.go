/******************************************************************************
 *
 *  描述 :
 *    音视频通话处理模块（呼叫建立、WebRTC 媒体协商、状态广播及通话挂断）。
 *
 *****************************************************************************/
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"chat/server/logs"
	"chat/server/store/types"

	"github.com/spf13/viper"
)

// 音视频通话常量。
const (
	// 用户 A 与 用户 B 之间的通话事件。
	//
	// 接收方 B 收到呼叫，振铃中（尚未接听）。
	constCallEventRinging = "ringing"
	// 接收方 B 已接听呼叫。
	constCallEventAccept = "accept"
	// WebRTC SDP 协商与 ICE 候选者数据交换事件。
	constCallEventOffer        = "offer"
	constCallEventAnswer       = "answer"
	constCallEventIceCandidate = "ice-candidate"
	// 任何一方挂断或服务器超时终止通话。
	constCallEventHangUp = "hang-up"

	// 表示通话状态的消息头 (Message Headers)。
	// 通话已建立。
	constCallMsgAccepted = "accepted"
	// 此前的通话已正常结束。
	constCallMsgFinished = "finished"
	// 通话中断（如网络异常）。
	constCallMsgDisconnected = "disconnected"
	// 呼叫未接听（被呼叫方超时未接听）。
	constCallMsgMissed = "missed"
	// 呼叫被拒绝（被呼叫方在接听前主动挂断）。
	constCallMsgDeclined = "declined"
)

type callConfig struct {
	// 是否开启音视频通话功能。
	Enabled bool `json:"enabled"`
	// 通话建立超时时间（秒），超时未接听将自动挂断。
	CallEstablishmentTimeout int `json:"call_establishment_timeout"`
	// ICE 服务器配置列表。
	ICEServers []iceServer `json:"ice_servers"`
	// 独立外部配置文件路径。
	ICEServersFile string `json:"ice_servers_file"`
}

// ICE 服务器配置结构。
type iceServer struct {
	Username       string   `json:"username,omitempty"`
	Credential     string   `json:"credential,omitempty"`
	CredentialType string   `json:"credential_type,omitempty"`
	Urls           []string `json:"urls,omitempty"`
}

// callPartyData 描述音视频通话参与者信息。
type callPartyData struct {
	// 通话参与者的用户 ID (Uid)。
	uid types.Uid
	// 是否为本次呼叫的发起方 (Caller)。
	isOriginator bool
	// 参与者对应的会话 Session。
	sess *Session
}

// videoCall 描述正在建立中或正在进行中的音视频通话。
type videoCall struct {
	// 通话参与者映射表 (Session sid -> callPartyData)。
	parties map[string]callPartyData
	// 通话关联的富文本消息序列号 (seq ID)。
	seq int
	// 通话消息的内容。
	content any
	// 通话消息内容的 MIME 类型。
	contentMime any
	// 接听时间。
	acceptedAt time.Time
}

// callPartySession 返回保存在通话参与者数据中的 Session 实例。
func callPartySession(sess *Session) *Session {
	if sess.isProxy() {
		// 当前在主题宿主节点上，深拷贝一份代理会话。
		callSess := &Session{
			proto: PROXY,
			// 实际处理通信的多路复用会话。
			multi: sess.multi,
			// 当前会话的特定局部参数。
			sid:         sess.sid,
			userAgent:   sess.userAgent,
			remoteAddr:  sess.remoteAddr,
			lang:        sess.lang,
			countryCode: sess.countryCode,
			proxyReq:    ProxyReqCall,
			background:  sess.background,
			uid:         sess.uid,
		}
		return callSess
	}
	return sess
}

// initVideoCalls 初始化音视频通话模块配置。
func initVideoCalls(jsconfig json.RawMessage) error {
	var config callConfig

	if len(jsconfig) == 0 {
		return nil
	}

	if err := json.Unmarshal([]byte(jsconfig), &config); err != nil {
		return fmt.Errorf("解析音视频配置失败: %w", err)
	}

	if !config.Enabled {
		logs.Info.Println("音视频通话功能已禁用")
		return nil
	}

	if len(config.ICEServers) > 0 {
		globals.iceServers = config.ICEServers
	} else if config.ICEServersFile != "" {
		var iceConfig []iceServer
		v := viper.New()
		v.SetConfigFile(config.ICEServersFile)
		v.SetConfigType("json")
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("读取 ICE 配置文件失败: %w", err)
		}
		if err := v.Unmarshal(&iceConfig); err != nil {
			return fmt.Errorf("解析 ICE 配置文件失败: %w", err)
		}
		globals.iceServers = iceConfig
	}

	if len(globals.iceServers) == 0 {
		return errors.New("未配置有效的 ICE 服务器")
	}

	globals.callEstablishmentTimeout = config.CallEstablishmentTimeout
	if globals.callEstablishmentTimeout <= 0 {
		globals.callEstablishmentTimeout = defaultCallEstablishmentTimeout
	}

	logs.Info.Println("音视频通话功能已启用，可用 ICE 服务器数:", len(globals.iceServers))
	return nil
}

// messageHead 给消息 Head 添加 WebRTC 相关字段，保留原 Head 中的属性。
func (call *videoCall) messageHead(head map[string]any, newState string, duration int) map[string]any {
	if head == nil {
		head = map[string]any{}
	}

	head["replace"] = ":" + strconv.Itoa(call.seq)
	head["webrtc"] = newState

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

// infoMessage 生成音视频通话事件的服务器通知消息模版。
func (call *videoCall) infoMessage(event string) *ServerComMessage {
	return &ServerComMessage{
		Info: &MsgServerInfo{
			What:  "call",
			Event: event,
			SeqId: call.seq,
		},
	}
}

// getCallOriginator 获取当前正在进行或建立中通话的发起人 Uid 和 Session。
func (t *Topic) getCallOriginator() (types.Uid, *Session) {
	if t.currentCall == nil {
		return types.ZeroUid, nil
	}
	for _, p := range t.currentCall.parties {
		if p.isOriginator {
			return p.uid, p.sess
		}
	}
	return types.ZeroUid, nil
}

// handleCallInvite 处理音视频呼叫邀请 (发起通话)
// (响应客户端 msg = {pub head=[mime: application/x-im-webrtc]})
func (t *Topic) handleCallInvite(msg *ClientComMessage, asUid types.Uid) {
	// 创建新的音视频通话对象。
	t.currentCall = &videoCall{
		parties:     make(map[string]callPartyData),
		seq:         t.lastID,
		content:     msg.Pub.Content,
		contentMime: msg.Pub.Head["mime"],
	}
	t.currentCall.parties[msg.sess.sid] = callPartyData{
		uid:          asUid,
		isOriginator: true,
		sess:         callPartySession(msg.sess),
	}
	// 开启呼叫建立超时定时器。
	t.callEstablishmentTimer.Reset(time.Duration(globals.callEstablishmentTimeout) * time.Second)
}

// handleCallEvent 处理已有音视频通话的相关事件（接听、挂断、WebRTC 媒体参数交换）
// (响应客户端 msg = {note what=call})
func (t *Topic) handleCallEvent(msg *ClientComMessage) {
	if t.currentCall == nil {
		// 通话尚未发起。
		logs.Warn.Printf("topic[%s]: 当前无正在进行的音视频通话", t.name)
		return
	}
	if t.isInactive() {
		// 主题处于非活跃或正在被删除状态。
		return
	}

	call := msg.Note
	if t.currentCall.seq != call.SeqId {
		// 消息 Sequence ID 不匹配。
		logs.Info.Printf("topic[%s]: 通话 seq id 不匹配 - 当前 (%d) vs 收到 (%d)", t.name, t.currentCall.seq, call.SeqId)
		return
	}

	asUid := types.ParseUserId(msg.AsUser)

	if _, userFound := t.perUser[asUid]; !userFound {
		// 未在该主题中找到该用户。
		logs.Warn.Printf("topic[%s]: 未找到用户 %s", t.name, asUid.UserId())
		return
	}

	switch call.Event {
	case constCallEventRinging, constCallEventAccept:
		// 条件校验：
		// 1. 通话已发起但尚未成功建立。
		if len(t.currentCall.parties) != 1 {
			return
		}
		originatorUid, originator := t.getCallOriginator()
		if originator == nil {
			// 未找到发起人 Session：终止通话。
			t.terminateCallInProgress(false)
			return
		}
		// 2. 振铃与接听事件只能来自被呼叫方。
		if originator.sid == msg.sess.sid || originatorUid == asUid {
			return
		}
		// 构造 {info} 消息转发给呼叫发起人。
		forwardMsg := t.currentCall.infoMessage(call.Event)
		forwardMsg.Info.From = msg.AsUser
		forwardMsg.Info.Topic = t.original(originatorUid)
		if call.Event == constCallEventAccept {
			// 呼叫已被接听。
			// 广播替换型的 {data} 状态更新消息至主题。
			msgCopy := *msg
			msgCopy.AsUser = originatorUid.UserId()
			replaceWith := constCallMsgAccepted
			var origHead map[string]any
			if msgCopy.Pub != nil {
				origHead = msgCopy.Pub.Head
			}
			head := t.currentCall.messageHead(origHead, replaceWith, 0)
			if err := t.saveAndBroadcastMessage(&msgCopy, originatorUid, false, nil,
				head, t.currentCall.content); err != nil {
				return
			}
			// 将被呼叫方信息加入通话对象。
			t.currentCall.parties[msg.sess.sid] = callPartyData{
				uid:          asUid,
				isOriginator: false,
				sess:         callPartySession(msg.sess),
			}
			t.currentCall.acceptedAt = time.Now()

			// 通知其他客户端呼叫已被接听。
			t.infoCallSubsOffline(msg.AsUser, asUid, call.Event, t.currentCall.seq, call.Payload, msg.sess.sid, false)
			t.callEstablishmentTimer.Stop()
		}
		originator.queueOut(forwardMsg)

	case constCallEventOffer, constCallEventAnswer, constCallEventIceCandidate:
		// 条件校验：
		// 1. 通话已被接听（有 2 位参与者）。
		if len(t.currentCall.parties) != 2 {
			logs.Warn.Printf("topic[%s]: 通话参与者预期 2 人，实际 %d 人", t.name, len(t.currentCall.parties))
			return
		}
		// 2. 事件来自通话参与者的 Session。
		if _, ok := t.currentCall.parties[msg.sess.sid]; !ok {
			logs.Warn.Printf("topic[%s]: 通话事件来自非参与者会话 %s", t.name, msg.sess.sid)
			return
		}
		// WebRTC 媒体元数据交换，转发至对方 Session。
		var otherUid types.Uid
		var otherEnd *Session
		for sid, p := range t.currentCall.parties {
			if sid != msg.sess.sid {
				otherUid = p.uid
				otherEnd = p.sess
				break
			}
		}
		if otherEnd == nil {
			logs.Warn.Printf("topic[%s]: 未能找到会话 %s 的通话对端", t.name, msg.sess.sid)
			return
		}
		// 转发 {info} 消息给对方。
		forwardMsg := t.currentCall.infoMessage(call.Event)
		forwardMsg.Info.From = msg.AsUser
		forwardMsg.Info.Topic = t.original(otherUid)
		forwardMsg.Info.Payload = call.Payload
		otherEnd.queueOut(forwardMsg)

	case constCallEventHangUp:
		switch len(t.currentCall.parties) {
		case 2:
			// 如果是进行中的通话，挂断只能由参与者 Session 发起。
			if _, ok := t.currentCall.parties[msg.sess.sid]; !ok {
				return
			}
		case 1:
			// 通话尚未建立。
			originatorUid, originator := t.getCallOriginator()
			// 挂断可由发起方 Session 或被呼叫方 Session 发起。
			if asUid == originatorUid && originator.sid != msg.sess.sid {
				return
			}
		default:
			break
		}
		t.maybeEndCallInProgress(msg.AsUser, msg, false)

	default:
		logs.Warn.Printf("topic[%s]: 音视频通话 (seq %d) 收到未知事件: %s", t.name, t.currentCall.seq, call.Event)
	}
}

// maybeEndCallInProgress 根据客户端挂断请求 (msg) 结束当前通话。
func (t *Topic) maybeEndCallInProgress(from string, msg *ClientComMessage, callDidTimeout bool) {
	if t.currentCall == nil {
		return
	}
	t.callEstablishmentTimer.Stop()
	originatorUid, _ := t.getCallOriginator()
	var replaceWith string
	var callDuration int64
	if from != "" && len(t.currentCall.parties) == 2 {
		// 进行中的通话正被正常挂断。
		replaceWith = constCallMsgFinished
		callDuration = time.Since(t.currentCall.acceptedAt).Milliseconds()
	} else {
		if from != "" {
			// 用户主动挂断。
			if from == originatorUid.UserId() {
				// 发起人/主叫方挂断：未接听。
				replaceWith = constCallMsgMissed
			} else {
				// 被叫方挂断：拒接。
				replaceWith = constCallMsgDeclined
			}
		} else {
			// 服务器触发的挂断（如超时）。
			if callDidTimeout {
				replaceWith = constCallMsgMissed
			} else {
				replaceWith = constCallMsgDisconnected
			}
		}
	}

	// 发送通话结束更新消息。
	msgCopy := *msg
	msgCopy.AsUser = originatorUid.UserId()
	var origHead map[string]any
	if msgCopy.Pub != nil {
		origHead = msgCopy.Pub.Head
	}
	head := t.currentCall.messageHead(origHead, replaceWith, int(callDuration))
	if err := t.saveAndBroadcastMessage(&msgCopy, originatorUid, false, nil, head, t.currentCall.content); err != nil {
		logs.Err.Printf("topic[%s]: 保存/广播通话结束消息失败 (seq id %d) - '%s'", t.name, t.currentCall.seq, err)
	}

	// 广播 {info} 挂断事件给在线会话。
	t.broadcastToSessions(t.currentCall.infoMessage(constCallEventHangUp))

	// 通知所有订阅者通话已结束。
	for tgt := range t.perUser {
		t.infoCallSubsOffline(from, tgt, constCallEventHangUp, t.currentCall.seq, nil, "", true)
	}
	t.currentCall = nil
}

// terminateCallInProgress 服务器主动终止通话（如超时未接听）。
func (t *Topic) terminateCallInProgress(callDidTimeout bool) {
	if t.currentCall == nil {
		return
	}
	uid, sess := t.getCallOriginator()
	if sess == nil || uid.IsZero() {
		logs.Warn.Printf("topic[%s]: 音视频通话 seq %d 缺少发起人，强制终止", t.name, t.currentCall.seq)
		t.currentCall = nil
		return
	}
	// 构造虚拟挂断请求。
	dummy := &ClientComMessage{
		Original:  t.original(uid),
		RcptTo:    uid.UserId(),
		AsUser:    uid.UserId(),
		Timestamp: types.TimeNow(),
		sess:      sess,
	}

	logs.Info.Printf("topic[%s]: 正在终止通话 seq %d, 是否超时: %t", t.name, t.currentCall.seq, callDidTimeout)
	t.maybeEndCallInProgress("", dummy, callDidTimeout)
}
