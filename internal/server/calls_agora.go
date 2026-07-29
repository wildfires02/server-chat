/******************************************************************************
 *
 *  描述 :
 *    Agora 群组通话配置、凭证签发和参与者生命周期。
 *
 *****************************************************************************/
package server

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
)

// agoraParticipantRole 是服务器依据 Topic ACL 授予的 RTC 角色。
type agoraParticipantRole string

const (
	// agoraRoleSubscriber 只允许接收频道中的媒体流。
	agoraRoleSubscriber agoraParticipantRole = "subscriber"
	// agoraRolePublisher 允许发送和接收频道中的媒体流。
	agoraRolePublisher agoraParticipantRole = "publisher"
)

// agoraConfig 保存 Agora RTC 服务端接入参数。
type agoraConfig struct {
	// Enabled 控制群组 Topic 是否允许创建 Agora 通话。
	Enabled bool `json:"enabled"`
	// AppID 是 Agora Console 分配的公开项目标识。
	AppID string `json:"app_id"`
	// AppCertificate 是仅可保存在服务端的 Token 签名证书。
	AppCertificate string `json:"app_certificate"`
	// TokenTTL 是 RTC Token 的有效秒数。
	TokenTTL int `json:"token_ttl"`
	// ChannelPrefix 是服务端生成频道名时使用的前缀。
	ChannelPrefix string `json:"channel_prefix"`
	// MaxParticipants 是单次群组通话的最大在线 Session 数。
	MaxParticipants int `json:"max_participants"`
}

// agoraProvider 保存通过校验的 Agora 运行时配置。
type agoraProvider struct {
	// appID 用于客户端初始化 Agora SDK。
	appID string
	// appCertificate 仅用于服务端签发 AccessToken2。
	appCertificate string
	// tokenTTL 是参与者凭证的有效期。
	tokenTTL time.Duration
	// channelPrefix 是不可逆频道名的可读前缀。
	channelPrefix string
	// maxParticipants 是单次通话的在线 Session 上限。
	maxParticipants int
}

// agoraCallData 保存一次群组通话的频道级状态。
type agoraCallData struct {
	// channel 是本次通话独占的 Agora RTC 频道。
	channel string
}

// agoraPartyData 保存一个 Session 的 Agora 媒体状态。
type agoraPartyData struct {
	// uid 是 Session 在 Agora 频道中的数字用户 ID。
	uid uint32
	// role 是服务端根据当前 ACL 授予的媒体角色。
	role agoraParticipantRole
}

// agoraCallCredentials 是客户端加入 Agora RTC 频道所需的参数。
type agoraCallCredentials struct {
	// Provider 固定为 agora。
	Provider string `json:"provider"`
	// AppID 用于初始化 Agora RTC SDK。
	AppID string `json:"app_id"`
	// Channel 是 Token 唯一授权加入的频道。
	Channel string `json:"channel"`
	// UID 是 Token 唯一授权使用的 Agora 数字用户 ID。
	UID uint32 `json:"uid"`
	// UserID 是 UID 对应的业务用户 ID。
	UserID string `json:"user_id"`
	// Token 是短期有效的 Agora AccessToken2。
	Token string `json:"token"`
	// ExpiresAt 是 Token 过期时间的 Unix 秒数。
	ExpiresAt int64 `json:"expires_at"`
	// Role 是 publisher 或 subscriber。
	Role agoraParticipantRole `json:"role"`
	// CallSeq 是通话邀请消息的序列号。
	CallSeq int `json:"call_seq"`
}

// newAgoraProvider 解析并校验 Agora 运行时配置。
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

// isAgoraHexIdentifier 校验 Agora App ID 和 App Certificate 的格式。
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

// isSafeAgoraChannelPrefix 校验频道名前缀是否可被各平台 SDK 安全处理。
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

// channelName 生成稳定且不泄露内部 Topic 名称的频道名。
func (provider *agoraProvider) channelName(topic string, seq int) string {
	digest := sha256.Sum256([]byte(topic + ":" + strconv.Itoa(seq)))
	return fmt.Sprintf("%s_%x", provider.channelPrefix, digest[:16])
}

// participantUID 为同一业务用户的每个 Session 生成稳定的非零 UID。
func (provider *agoraProvider) participantUID(topic string, seq int, userID, sessionID string) uint32 {
	digest := sha256.Sum256([]byte(
		topic + ":" + strconv.Itoa(seq) + ":" + userID + ":" + sessionID,
	))
	uid := binary.LittleEndian.Uint32(digest[:4])
	if uid == 0 {
		return 1
	}
	return uid
}

// credentials 为通过 Topic ACL 校验的 Session 签发短期 AccessToken2。
func (provider *agoraProvider) credentials(
	call *videoCall,
	userID string,
	sessionID string,
	role agoraParticipantRole,
) (*agoraCallCredentials, error) {
	if call.agora == nil || call.agora.channel == "" {
		return nil, errors.New("Agora 通话缺少频道状态")
	}

	channel := call.agora.channel
	agoraUID := provider.participantUID(channel, call.seq, userID, sessionID)
	tokenRole := agora.RoleSubscriber
	if role == agoraRolePublisher {
		tokenRole = agora.RolePublisher
	}
	ttlSeconds := uint32(provider.tokenTTL / time.Second)
	token, err := agora.BuildRTCToken(
		provider.appID,
		provider.appCertificate,
		channel,
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
		Channel:   channel,
		UID:       agoraUID,
		UserID:    userID,
		Token:     token,
		ExpiresAt: time.Now().Add(provider.tokenTTL).Unix(),
		Role:      role,
		CallSeq:   call.seq,
	}, nil
}

// handleAgoraCallEvent 处理群组通话的加入、离开、续期和结束。
func (t *Topic) handleAgoraCallEvent(msg *ClientComMessage, asUid types.Uid) {
	userData := t.perUser[asUid]
	mode := userData.modeGiven & userData.modeWant
	if userData.deleted || !mode.IsJoiner() || !mode.IsReader() {
		if msg.Id != "" {
			msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
		}
		return
	}

	switch msg.Note.Event {
	case constCallEventJoin, constCallEventRefresh:
		t.issueAgoraCallCredentials(
			msg,
			asUid,
			mode,
			msg.Note.Event == constCallEventRefresh,
		)
	case constCallEventLeave:
		t.leaveAgoraCall(msg, asUid)
	case constCallEventHangUp:
		originatorUID, _ := t.getCallOriginator()
		if asUid != originatorUID && !mode.IsAdmin() {
			if msg.Id != "" {
				msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
			}
			return
		}
		t.maybeEndCallInProgress(msg.AsUser, msg, false)
	case constCallEventOffer, constCallEventAnswer, constCallEventIceCandidate,
		constCallEventRinging, constCallEventAccept:
		// Agora RTC 不通过业务 WebSocket 交换 SDP 或 ICE。
		if msg.Id != "" {
			msg.sess.queueOut(ErrOperationNotAllowedReply(msg, types.TimeNow()))
		}
	default:
		logs.Warn.Printf("topic[%s]: Agora 群组通话 seq %d 收到未知事件 %q",
			t.name, t.currentCall.seq, msg.Note.Event)
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
	if refresh && (!found || party.agora == nil) {
		if msg.Id != "" {
			msg.sess.queueOut(ErrMalformedReply(msg, types.TimeNow()))
		}
		return
	}
	if (!found || party.agora == nil) &&
		t.agoraParticipantCount() >= globals.agora.maxParticipants {
		if msg.Id != "" {
			msg.sess.queueOut(ErrCallBusyReply(msg, types.TimeNow()))
		}
		return
	}

	role := t.agoraRoleForParticipant(t.perUser[asUid], mode)
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
	}
	// ACL 可能在 Token 生命周期内变化，因此续期也覆盖最新角色。
	party.agora = &agoraPartyData{
		uid:  credentials.UID,
		role: role,
	}
	t.currentCall.parties[msg.sess.sid] = party

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

// agoraRoleForParticipant 根据订阅属性和有效 ACL 计算媒体角色。
func (t *Topic) agoraRoleForParticipant(
	userData perUserData,
	mode types.AccessMode,
) agoraParticipantRole {
	// 广播频道地址和只读 Topic 即使具有写权限也只能订阅媒体。
	if mode.IsWriter() && !userData.isChan && !t.isReadOnly() {
		return agoraRolePublisher
	}
	return agoraRoleSubscriber
}

// markAgoraCallAccepted 在首位成员取得凭证时持久化接通状态。
func (t *Topic) markAgoraCallAccepted(msg *ClientComMessage) error {
	if err := t.persistCallState(msg, constCallMsgAccepted, 0); err != nil {
		return err
	}
	t.currentCall.acceptedAt = time.Now()
	t.callEstablishmentTimer.Stop()
	return nil
}

// leaveAgoraCall 移除一个 Session，并在最后一位成员离开时结束通话。
func (t *Topic) leaveAgoraCall(msg *ClientComMessage, asUid types.Uid) {
	party, found := t.currentCall.parties[msg.sess.sid]
	if !found || party.agora == nil || party.uid != asUid {
		return
	}
	t.removeAgoraParty(msg.sess.sid, false)
	if t.agoraParticipantCount() == 0 {
		t.maybeEndCallInProgress(msg.AsUser, msg, false)
	}
}

// removeAgoraParty 删除一个 Session 的 Agora 状态并广播离开事件。
func (t *Topic) removeAgoraParty(sessionID string, fromDisconnect bool) {
	party, found := t.currentCall.parties[sessionID]
	if !found || party.agora == nil {
		return
	}
	delete(t.currentCall.parties, sessionID)

	publicPayload, _ := json.Marshal(map[string]any{
		"provider": constCallProviderAgora,
		"user_id":  party.uid.UserId(),
		"uid":      party.agora.uid,
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

// disconnectAgoraCallSessions 清理断开连接持有的全部 Agora 状态。
func (t *Topic) disconnectAgoraCallSessions(msg *ClientComMessage) {
	if t.currentCall == nil ||
		t.currentCall.provider != callProvider(constCallProviderAgora) {
		return
	}

	var lastUID types.Uid
	for sessionID, party := range t.currentCall.parties {
		matches := sessionID == msg.sess.sid
		if msg.sess.isMultiplex() && party.sess != nil {
			matches = party.sess.isProxy() && party.sess.multi == msg.sess
		}
		if matches && party.agora != nil {
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

// agoraParticipantCount 返回已取得有效 Agora 凭证的 Session 数。
func (t *Topic) agoraParticipantCount() int {
	if t.currentCall == nil {
		return 0
	}
	count := 0
	for _, party := range t.currentCall.parties {
		if party.agora != nil {
			count++
		}
	}
	return count
}
