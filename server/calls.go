/******************************************************************************
 *
 *  描述 :
 *    音视频通话处理模块（WebRTC P2P、Agora 群组 RTC、状态广播及通话挂断）。
 *
 *****************************************************************************/
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"chat/server/agora"
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
	// Agora 群组通话参与者加入频道。
	constCallEventJoin = "join"
	// Agora 群组通话参与者离开频道。
	constCallEventLeave = "leave"
	// Agora 客户端在 Token 即将过期时申请新凭证。
	constCallEventRefresh = "refresh"

	// WebRTC 表示原有的端到端媒体协商通话提供方。
	constCallProviderWebRTC = "webrtc"
	// Agora 表示由 Agora RTC SDK 承载媒体流的群组通话提供方。
	constCallProviderAgora = "agora"

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

// callConfig 保存通话配置的数据和运行状态。
type callConfig struct {
	// 是否开启音视频通话功能。
	Enabled bool `json:"enabled"`
	// 通话建立超时时间（秒），超时未接听将自动挂断。
	CallEstablishmentTimeout int `json:"call_establishment_timeout"`
	// ICE 服务器配置列表。
	ICEServers []iceServer `json:"ice_servers"`
	// 独立外部配置文件路径。
	ICEServersFile string `json:"ice_servers_file"`
	// Agora 保存群组语音和视频通话的服务端鉴权配置。
	Agora agoraConfig `json:"agora"`
}

// agoraConfig 保存 Agora RTC 服务端接入参数。
type agoraConfig struct {
	// Enabled 控制是否允许群组 Topic 创建 Agora 通话。
	Enabled bool `json:"enabled"`
	// AppID 是 Agora Console 为项目分配的公开项目标识。
	AppID string `json:"app_id"`
	// AppCertificate 是仅可保存在服务端的 Token 签名证书。
	AppCertificate string `json:"app_certificate"`
	// TokenTTL 是 RTC Token 和媒体权限的有效秒数，最大为 24 小时。
	TokenTTL int `json:"token_ttl"`
	// ChannelPrefix 是服务端生成的 Agora 频道名前缀。
	ChannelPrefix string `json:"channel_prefix"`
	// MaxParticipants 限制单个群组通话可同时加入的 Session 数量。
	MaxParticipants int `json:"max_participants"`
}

// agoraProvider 保存已校验的 Agora 运行时配置。
type agoraProvider struct {
	// appID 是下发给客户端初始化 Agora SDK 的项目标识。
	appID string
	// appCertificate 仅用于服务端签发 AccessToken2，绝不下发客户端。
	appCertificate string
	// tokenTTL 是每个参与者 Token 的有效期。
	tokenTTL time.Duration
	// channelPrefix 是不可逆频道名的可读前缀。
	channelPrefix string
	// maxParticipants 是单次群组通话的在线 Session 上限。
	maxParticipants int
}

// ICE 服务器配置结构。
type iceServer struct {
	// Username 指示是否启用或满足Username。
	Username string `json:"username,omitempty"`
	// Credential 保存凭据。
	Credential string `json:"credential,omitempty"`
	// CredentialType 保存凭据Type。
	CredentialType string `json:"credential_type,omitempty"`
	// Urls 保存Urls列表。
	Urls []string `json:"urls,omitempty"`
}

// callPartyData 描述音视频通话参与者信息。
type callPartyData struct {
	// 通话参与者的用户 ID (Uid)。
	uid types.Uid
	// 是否为本次呼叫的发起方 (Caller)。
	isOriginator bool
	// 参与者对应的会话 Session。
	sess *Session
	// joined 表示该 Session 已取得 Agora 凭证并加入群组通话。
	joined bool
	// agoraUID 是当前 Session 在 Agora 频道中的唯一数字用户 ID。
	agoraUID uint32
	// agoraRole 是服务端依据 Topic ACL 授予的 publisher 或 subscriber 角色。
	agoraRole string
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
	// provider 区分原有 WebRTC P2P 与 Agora 群组通话。
	provider string
	// channel 是服务端为 Agora 群组通话生成的专属 RTC 频道名。
	channel string
	// originatorUid 在参与者离线后仍保留发起人身份，用于结束状态持久化。
	originatorUid types.Uid
	// originatorSess 保存发起呼叫时的 Session，供服务器超时终止流程使用。
	originatorSess *Session
}

// agoraCallCredentials 是客户端初始化并加入 Agora RTC 频道所需的完整参数。
type agoraCallCredentials struct {
	// Provider 固定为 agora，便于客户端选择正确的媒体 SDK。
	Provider string `json:"provider"`
	// AppID 用于初始化 Agora RTC SDK。
	AppID string `json:"app_id"`
	// Channel 是 Token 唯一授权加入的频道名。
	Channel string `json:"channel"`
	// UID 是 Token 唯一授权使用的 Agora 数字用户 ID。
	UID uint32 `json:"uid"`
	// UserID 是 UID 对应的业务用户 ID，供客户端渲染成员身份。
	UserID string `json:"user_id"`
	// Token 是短期有效的 Agora AccessToken2。
	Token string `json:"token"`
	// ExpiresAt 是 Token 过期时间的 Unix 秒数。
	ExpiresAt int64 `json:"expires_at"`
	// Role 是 publisher 或 subscriber，对应客户端的 RTC 角色。
	Role string `json:"role"`
	// CallSeq 是该群组通话对应的消息序列号。
	CallSeq int `json:"call_seq"`
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

	// ICE 配置只用于保留兼容的 WebRTC P2P 通话。Agora 群组通话不依赖
	// 业务服务器配置 STUN/TURN。
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

	if config.Agora.Enabled {
		provider, err := newAgoraProvider(config.Agora)
		if err != nil {
			return err
		}
		globals.agora = provider
	}

	if len(globals.iceServers) == 0 && globals.agora == nil {
		return errors.New("未配置有效的 ICE 服务器或 Agora 群组通话")
	}

	globals.callEstablishmentTimeout = config.CallEstablishmentTimeout
	if globals.callEstablishmentTimeout <= 0 {
		globals.callEstablishmentTimeout = defaultCallEstablishmentTimeout
	}

	logs.Info.Printf("音视频通话功能已启用：ICE 服务器 %d 个，Agora 群组通话 %t",
		len(globals.iceServers), globals.agora != nil)
	return nil
}

// newAgoraProvider 解析并校验 Agora 运行时配置。官方建议通过环境变量
// AGORA_APP_ID 和 AGORA_APP_CERTIFICATE 注入生产凭据，因此配置中的空值
// 会自动回退到对应环境变量。
func newAgoraProvider(config agoraConfig) (*agoraProvider, error) {
	appID := strings.TrimSpace(config.AppID)
	if appID == "" {
		appID = strings.TrimSpace(os.Getenv("AGORA_APP_ID"))
	}
	appCertificate := strings.TrimSpace(config.AppCertificate)
	if appCertificate == "" {
		appCertificate = strings.TrimSpace(os.Getenv("AGORA_APP_CERTIFICATE"))
	}
	if !isAgoraHexIdentifier(appID) {
		return nil, errors.New("Agora app_id 或 AGORA_APP_ID 必须是 32 位十六进制字符串")
	}
	if !isAgoraHexIdentifier(appCertificate) {
		return nil, errors.New("Agora app_certificate 或 AGORA_APP_CERTIFICATE 必须是 32 位十六进制字符串")
	}

	if config.TokenTTL == 0 {
		config.TokenTTL = 3600
	}
	if config.TokenTTL < 60 || config.TokenTTL > 24*60*60 {
		return nil, errors.New("Agora token_ttl 必须在 60 到 86400 秒之间")
	}
	if config.ChannelPrefix == "" {
		config.ChannelPrefix = "im"
	}
	if len(config.ChannelPrefix) > 24 || !isSafeAgoraChannelPrefix(config.ChannelPrefix) {
		return nil, errors.New("Agora channel_prefix 只能包含字母、数字、横线和下划线，且最长 24 字节")
	}
	if config.MaxParticipants == 0 {
		config.MaxParticipants = 128
	}
	if config.MaxParticipants < 2 || config.MaxParticipants > 10_000 {
		return nil, errors.New("Agora max_participants 必须在 2 到 10000 之间")
	}

	return &agoraProvider{
		appID:           appID,
		appCertificate:  appCertificate,
		tokenTTL:        time.Duration(config.TokenTTL) * time.Second,
		channelPrefix:   config.ChannelPrefix,
		maxParticipants: config.MaxParticipants,
	}, nil
}

// isAgoraHexIdentifier 校验 Agora App ID 和 App Certificate 的编码格式。
func isAgoraHexIdentifier(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') &&
			(char < 'a' || char > 'f') &&
			(char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

// isSafeAgoraChannelPrefix 限制频道名前缀为跨平台 SDK 都可安全处理的字符。
func isSafeAgoraChannelPrefix(value string) bool {
	for _, char := range value {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' {
			return false
		}
	}
	return value != ""
}

// channelName 为一次 Topic 通话生成稳定且不泄露内部 Topic 名称的频道名。
func (provider *agoraProvider) channelName(topic string, seq int) string {
	digest := sha256.Sum256([]byte(topic + ":" + strconv.Itoa(seq)))
	return fmt.Sprintf("%s_%x", provider.channelPrefix, digest[:16])
}

// participantUID 为同一业务用户的每个 Session 生成稳定的非零 Agora UID，
// 从而允许一个账号通过多端设备同时加入同一群组通话。
func (provider *agoraProvider) participantUID(topic string, seq int, userID, sessionID string) uint32 {
	digest := sha256.Sum256([]byte(topic + ":" + strconv.Itoa(seq) + ":" + userID + ":" + sessionID))
	uid := binary.LittleEndian.Uint32(digest[:4])
	if uid == 0 {
		uid = 1
	}
	return uid
}

// credentials 为已通过 Topic ACL 校验的 Session 签发短期 AccessToken2。
func (provider *agoraProvider) credentials(call *videoCall, userID, sessionID, role string) (*agoraCallCredentials, error) {
	agoraUID := provider.participantUID(call.channel, call.seq, userID, sessionID)
	tokenRole := agora.RoleSubscriber
	if role == "publisher" {
		tokenRole = agora.RolePublisher
	}
	ttlSeconds := uint32(provider.tokenTTL / time.Second)
	token, err := agora.BuildRTCToken(
		provider.appID,
		provider.appCertificate,
		call.channel,
		agoraUID,
		tokenRole,
		ttlSeconds,
	)
	if err != nil {
		return nil, err
	}
	return &agoraCallCredentials{
		Provider:  constCallProviderAgora,
		AppID:     provider.appID,
		Channel:   call.channel,
		UID:       agoraUID,
		UserID:    userID,
		Token:     token,
		ExpiresAt: time.Now().Add(provider.tokenTTL).Unix(),
		Role:      role,
		CallSeq:   call.seq,
	}, nil
}

// messageHead 给消息 Head 添加 WebRTC 相关字段，保留原 Head 中的属性。
func (call *videoCall) messageHead(head map[string]any, newState string, duration int) map[string]any {
	if head == nil {
		head = map[string]any{}
	}

	head["replace"] = ":" + strconv.Itoa(call.seq)
	head["webrtc"] = newState
	if call.provider != "" {
		head["call-provider"] = call.provider
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
	if !t.currentCall.originatorUid.IsZero() {
		return t.currentCall.originatorUid, t.currentCall.originatorSess
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
	provider := constCallProviderWebRTC
	channel := ""
	if t.cat == types.TopicCatGrp {
		provider = constCallProviderAgora
		channel = globals.agora.channelName(t.name, t.lastID)
	}
	originatorSession := callPartySession(msg.sess)
	// 创建新的音视频通话对象。
	t.currentCall = &videoCall{
		parties:        make(map[string]callPartyData),
		seq:            t.lastID,
		content:        msg.Pub.Content,
		contentMime:    msg.Pub.Head["mime"],
		provider:       provider,
		channel:        channel,
		originatorUid:  asUid,
		originatorSess: originatorSession,
	}
	t.currentCall.parties[msg.sess.sid] = callPartyData{
		uid:          asUid,
		isOriginator: true,
		sess:         originatorSession,
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

	if t.currentCall.provider == constCallProviderAgora {
		t.handleAgoraCallEvent(msg, asUid)
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

// handleAgoraCallEvent 处理群组成员加入、离开、续期和管理员结束 Agora
// 通话。媒体流由 Agora SDK 传输，因此不会接受 SDP 或 ICE 信令事件。
func (t *Topic) handleAgoraCallEvent(msg *ClientComMessage, asUid types.Uid) {
	call := msg.Note
	userData := t.perUser[asUid]
	mode := userData.modeGiven & userData.modeWant
	if userData.deleted || !mode.IsJoiner() || !mode.IsReader() {
		if msg.Id != "" {
			msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
		}
		return
	}

	switch call.Event {
	case constCallEventJoin, constCallEventRefresh:
		t.issueAgoraCallCredentials(msg, asUid, mode, call.Event == constCallEventRefresh)

	case constCallEventLeave:
		t.leaveAgoraCall(msg, asUid, false)

	case constCallEventHangUp:
		originatorUID, _ := t.getCallOriginator()
		// 结束整个群组通话只允许发起人或群组管理员执行。
		if asUid != originatorUID && !mode.IsAdmin() {
			if msg.Id != "" {
				msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			}
			return
		}
		t.maybeEndCallInProgress(msg.AsUser, msg, false)

	case constCallEventOffer, constCallEventAnswer, constCallEventIceCandidate,
		constCallEventRinging, constCallEventAccept:
		// Agora RTC 不使用业务 WebSocket 交换 SDP/ICE，也不使用 P2P 接听状态。
		if msg.Id != "" {
			msg.sess.queueOut(ErrOperationNotAllowedReply(msg, types.TimeNow()))
		}

	default:
		logs.Warn.Printf("topic[%s]: Agora 群组通话 (seq %d) 收到未知事件: %s",
			t.name, t.currentCall.seq, call.Event)
		if msg.Id != "" {
			msg.sess.queueOut(ErrMalformedReply(msg, types.TimeNow()))
		}
	}
}

// issueAgoraCallCredentials 根据最新 Topic ACL 签发加入或续期凭证。
func (t *Topic) issueAgoraCallCredentials(
	msg *ClientComMessage,
	asUid types.Uid,
	mode types.AccessMode,
	refresh bool,
) {
	if globals.agora == nil {
		if msg.Id != "" {
			msg.sess.queueOut(ErrNotImplementedReply(msg, types.TimeNow()))
		}
		return
	}

	party, found := t.currentCall.parties[msg.sess.sid]
	if refresh && (!found || !party.joined) {
		if msg.Id != "" {
			msg.sess.queueOut(ErrMalformedReply(msg, types.TimeNow()))
		}
		return
	}
	if (!found || !party.joined) &&
		t.agoraParticipantCount() >= globals.agora.maxParticipants {
		if msg.Id != "" {
			msg.sess.queueOut(ErrCallBusyReply(msg, types.TimeNow()))
		}
		return
	}

	role := "subscriber"
	// 频道订阅读者和只读 Topic 只能获取 join 权限。Agora Console 还应
	// 开启 Co-host Token Authentication 以在 RTC 网络侧强制此限制。
	if mode.IsWriter() && !userDataIsChannel(t.perUser[asUid]) && !t.isReadOnly() {
		role = "publisher"
	}
	credentials, err := globals.agora.credentials(
		t.currentCall,
		asUid.UserId(),
		msg.sess.sid,
		role,
	)
	if err != nil {
		logs.Err.Printf("topic[%s]: 签发 Agora Token 失败: %v", t.name, err)
		if msg.Id != "" {
			msg.sess.queueOut(ErrServiceUnavailableReply(msg, types.TimeNow()))
		}
		return
	}
	payload, err := json.Marshal(credentials)
	if err != nil {
		logs.Err.Printf("topic[%s]: 序列化 Agora 加入凭证失败: %v", t.name, err)
		if msg.Id != "" {
			msg.sess.queueOut(ErrServiceUnavailableReply(msg, types.TimeNow()))
		}
		return
	}

	if !refresh {
		if t.currentCall.acceptedAt.IsZero() {
			if err = t.markAgoraCallAccepted(msg); err != nil {
				return
			}
		}
		if !found {
			party = callPartyData{
				uid:  asUid,
				sess: callPartySession(msg.sess),
			}
		}
		party.joined = true
		party.agoraUID = credentials.UID
		party.agoraRole = role
		t.currentCall.parties[msg.sess.sid] = party
	} else {
		// ACL 可能在 Token 生命周期内发生变化，续期时同步最新角色。
		party.agoraRole = role
		party.agoraUID = credentials.UID
		t.currentCall.parties[msg.sess.sid] = party
	}

	responseEvent := constCallEventJoin
	if refresh {
		responseEvent = constCallEventRefresh
	}
	response := t.currentCall.infoMessage(responseEvent)
	response.Info.Topic = t.original(asUid)
	response.Info.From = asUid.UserId()
	response.Info.Payload = payload
	msg.sess.queueOut(response)
}

// markAgoraCallAccepted 在首位成员取得凭证时持久化 accepted 替换消息。
func (t *Topic) markAgoraCallAccepted(msg *ClientComMessage) error {
	originatorUID, _ := t.getCallOriginator()
	msgCopy := *msg
	msgCopy.AsUser = originatorUID.UserId()
	head := t.currentCall.messageHead(nil, constCallMsgAccepted, 0)
	if err := t.saveAndBroadcastMessage(
		&msgCopy,
		originatorUID,
		false,
		nil,
		head,
		t.currentCall.content,
	); err != nil {
		logs.Err.Printf("topic[%s]: 保存 Agora 通话接通状态失败: %v", t.name, err)
		return err
	}
	t.currentCall.acceptedAt = time.Now()
	t.callEstablishmentTimer.Stop()
	return nil
}

// leaveAgoraCall 从群组通话移除一个 Session，并在最后一个成员离开时
// 结束通话。fromDisconnect 仅影响日志语义，不改变客户端可见协议。
func (t *Topic) leaveAgoraCall(msg *ClientComMessage, asUid types.Uid, fromDisconnect bool) {
	party, found := t.currentCall.parties[msg.sess.sid]
	if !found || !party.joined || party.uid != asUid {
		return
	}
	t.removeAgoraParty(msg.sess.sid, fromDisconnect)

	if t.agoraParticipantCount() == 0 {
		t.maybeEndCallInProgress(msg.AsUser, msg, false)
	}
}

// removeAgoraParty 删除指定 Session 的参与状态并向其余在线成员广播离开。
// 调用方负责在批量删除结束后判断是否需要结束整个通话。
func (t *Topic) removeAgoraParty(sessionID string, fromDisconnect bool) {
	party, found := t.currentCall.parties[sessionID]
	if !found || !party.joined {
		return
	}
	delete(t.currentCall.parties, sessionID)

	publicPayload, _ := json.Marshal(map[string]any{
		"provider": constCallProviderAgora,
		"user_id":  party.uid.UserId(),
		"uid":      party.agoraUID,
	})
	notification := t.currentCall.infoMessage(constCallEventLeave)
	notification.Info.From = party.uid.UserId()
	notification.Info.Payload = publicPayload
	notification.SkipSid = sessionID
	t.broadcastToSessions(notification)

	if fromDisconnect {
		logs.Info.Printf("topic[%s]: Agora 参与者 %s 因 Session 断开离开通话",
			t.name, party.uid.UserId())
	}
}

// disconnectAgoraCallSessions 清理普通或多路复用连接持有的全部 Agora
// 参与状态，防止断线 Session 长时间占用群组通话人数配额。
func (t *Topic) disconnectAgoraCallSessions(msg *ClientComMessage) {
	if t.currentCall == nil || t.currentCall.provider != constCallProviderAgora {
		return
	}
	var lastUID types.Uid
	for sessionID, party := range t.currentCall.parties {
		matches := sessionID == msg.sess.sid
		if msg.sess.isMultiplex() && party.sess != nil {
			matches = party.sess.isProxy() && party.sess.multi == msg.sess
		}
		if matches && party.joined {
			lastUID = party.uid
			t.removeAgoraParty(sessionID, true)
		}
	}
	if t.agoraParticipantCount() == 0 && !lastUID.IsZero() {
		msgCopy := *msg
		msgCopy.AsUser = lastUID.UserId()
		t.maybeEndCallInProgress(lastUID.UserId(), &msgCopy, false)
	}
}

// agoraParticipantCount 返回已实际取得 Agora 加入凭证的 Session 数量。
func (t *Topic) agoraParticipantCount() int {
	if t.currentCall == nil {
		return 0
	}
	count := 0
	for _, party := range t.currentCall.parties {
		if party.joined {
			count++
		}
	}
	return count
}

// userDataIsChannel 安全判断一个订阅是否为广播频道的只读地址。
func userDataIsChannel(data perUserData) bool {
	return data.isChan
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
	if from != "" && !t.currentCall.acceptedAt.IsZero() {
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
