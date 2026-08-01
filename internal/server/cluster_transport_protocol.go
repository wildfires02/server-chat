package server

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"chat/api/pbx"
	"chat/server/auth"
	"chat/server/push"
	"chat/server/store/types"

	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

// clusterFrameKindForProcedure 将旧调用名映射为稳定的 Protobuf 枚举。
func clusterFrameKindForProcedure(proc string) (pbx.ClusterFrameKind, error) {
	switch proc {
	case "Cluster.Ping":
		return pbx.ClusterFrameKind_CLUSTER_FRAME_PING, nil
	case "Cluster.TopicMaster":
		return pbx.ClusterFrameKind_CLUSTER_FRAME_TOPIC_MASTER, nil
	case "Cluster.TopicProxy":
		return pbx.ClusterFrameKind_CLUSTER_FRAME_TOPIC_PROXY, nil
	case "Cluster.Route":
		return pbx.ClusterFrameKind_CLUSTER_FRAME_ROUTE, nil
	case "Cluster.UserCacheUpdate":
		return pbx.ClusterFrameKind_CLUSTER_FRAME_USER_CACHE, nil
	default:
		return pbx.ClusterFrameKind_CLUSTER_FRAME_UNSPECIFIED,
			fmt.Errorf("cluster: 未知内部调用 %q", proc)
	}
}

// clusterRoutingKey 返回用于固定 Lane 的 Topic 或用户路由键。
func clusterRoutingKey(request any, fallback string) string {
	switch value := request.(type) {
	case *ClusterReq:
		return value.RcptTo
	case *ClusterResp:
		return value.RcptTo
	case *ClusterRoute:
		if value.SrvMsg != nil && value.SrvMsg.RcptTo != "" {
			return value.SrvMsg.RcptTo
		}
	case *UserCacheReq:
		if !value.UserId.IsZero() {
			return value.UserId.String()
		}
		if len(value.UserIdList) > 0 {
			return value.UserIdList[0].String()
		}
	case *ClusterPing:
		return value.Node
	}
	return fallback
}

// clusterRequestIsReliable 区分必须重试的业务请求与允许丢弃的瞬态事件。
func clusterRequestIsReliable(request any) bool {
	switch value := request.(type) {
	case *ClusterReq:
		return value.CliMsg == nil ||
			value.CliMsg.Note == nil ||
			value.CliMsg.Note.What != "kp"
	case *ClusterResp:
		return !clusterServerMessageIsEphemeral(value.SrvMsg)
	case *ClusterRoute:
		return !clusterServerMessageIsEphemeral(value.SrvMsg)
	default:
		return true
	}
}

// clusterServerMessageIsEphemeral 仅允许丢弃可由后续状态自然覆盖的下行事件。
func clusterServerMessageIsEphemeral(message *ServerComMessage) bool {
	if message == nil {
		return false
	}
	if message.Info != nil && message.Info.What == "kp" {
		return true
	}
	if message.Pres == nil {
		return false
	}
	switch message.Pres.What {
	case "on", "off", "ua":
		return true
	default:
		// acs、gone、term、msg、read、recv 等状态具有业务意义，必须可靠投递。
		return false
	}
}

// isRetryableClusterTransportError 判断错误是否发生在远端业务执行之前或响应确认之前。
func isRetryableClusterTransportError(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errClusterLaneClosed) ||
		errors.Is(err, errClusterLaneQueueFull) {
		return false
	}
	var remoteError *clusterRemoteCallError
	if errors.As(err, &remoteError) {
		return false
	}
	var protocolError *clusterProtocolError
	return !errors.As(err, &protocolError)
}

// normalizedClusterMinProtocol 兼容尚未发送 min_protocol_version 的旧节点。
func normalizedClusterMinProtocol(minimum, maximum int32) int32 {
	if minimum <= 0 {
		return maximum
	}
	return minimum
}

// clusterProtocolVersionsOverlap 判断远端和本地协议版本范围是否存在交集。
func clusterProtocolVersionsOverlap(remoteMinimum, remoteMaximum int32) bool {
	remoteMinimum = normalizedClusterMinProtocol(remoteMinimum, remoteMaximum)
	return remoteMaximum >= clusterMinProtocolVersion &&
		remoteMinimum <= clusterProtocolVersion &&
		remoteMinimum > 0 &&
		remoteMaximum >= remoteMinimum
}

// negotiatedClusterProtocolVersion 选择双方共同支持的最高协议版本。
func negotiatedClusterProtocolVersion(frame *pbx.ClusterFrame) int32 {
	if frame != nil && frame.ProtocolVersion < clusterProtocolVersion {
		return frame.ProtocolVersion
	}
	return clusterProtocolVersion
}

// clusterPeerCertificate 从 gRPC 握手状态读取已经由 CA 验证的客户端叶子证书。
func clusterPeerCertificate(
	context context.Context,
	required bool,
) (*x509.Certificate, error) {
	if !required {
		return nil, nil
	}
	connectionPeer, ok := grpcpeer.FromContext(context)
	if !ok || connectionPeer.AuthInfo == nil {
		return nil, errors.New("cluster tls: 入站流缺少客户端认证信息")
	}
	tlsInfo, ok := connectionPeer.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, errors.New("cluster tls: 入站流缺少已验证的客户端证书")
	}
	return tlsInfo.State.PeerCertificates[0], nil
}

// clusterLaneIndex 使用稳定 FNV-1a 把同一 Topic 固定到同一 Lane。
func clusterLaneIndex(key string, laneCount int) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(key))
	return int(hash.Sum32()) & (laneCount - 1)
}

// encodeClusterPayload 把集群内部业务负载编码为对应的强类型 Protobuf 消息。
func encodeClusterPayload(value any) ([]byte, error) {
	var message proto.Message
	switch typed := value.(type) {
	case *ClusterPing:
		message = &pbx.ClusterPingPayload{Node: typed.Node, Fingerprint: typed.Fingerprint}
	case *ClusterReq:
		message = clusterRequestToProto(typed)
	case *ClusterResp:
		message = clusterResponseToProto(typed)
	case *ClusterRoute:
		message = clusterRouteToProto(typed)
	case *UserCacheReq:
		message = clusterUserCacheToProto(typed)
	case *MsgServerCtrl:
		message = clusterServerMessageToProto(&ServerComMessage{Ctrl: typed})
	case bool:
		message = &pbx.ClusterAck{Rejected: typed}
	case *bool:
		message = &pbx.ClusterAck{Rejected: typed != nil && *typed}
	default:
		return nil, fmt.Errorf("cluster: 不支持的 Protobuf Lane payload 类型 %T", value)
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("cluster: 编码 Protobuf Lane payload 失败: %w", err)
	}
	return payload, nil
}

// decodeClusterPayload 解码强类型 Protobuf 业务负载。
func decodeClusterPayload(payload []byte, destination any) error {
	if len(payload) == 0 {
		return errors.New("cluster: Lane payload 不能为空")
	}
	var message proto.Message
	switch destination.(type) {
	case *ClusterPing:
		message = &pbx.ClusterPingPayload{}
	case *ClusterReq:
		message = &pbx.ClusterRequestPayload{}
	case *ClusterResp:
		message = &pbx.ClusterResponsePayload{}
	case *ClusterRoute:
		message = &pbx.ClusterRoutePayload{}
	case *UserCacheReq:
		message = &pbx.ClusterUserCachePayload{}
	case *MsgServerCtrl:
		message = &pbx.ClusterServerMessage{}
	case *bool:
		message = &pbx.ClusterAck{}
	default:
		return fmt.Errorf("cluster: 不支持的 Protobuf Lane 目标类型 %T", destination)
	}
	if err := proto.Unmarshal(payload, message); err != nil {
		return fmt.Errorf("cluster: 解码 Protobuf Lane payload 失败: %w", err)
	}
	switch output := destination.(type) {
	case *ClusterPing:
		decoded := message.(*pbx.ClusterPingPayload)
		*output = ClusterPing{Node: decoded.Node, Fingerprint: decoded.Fingerprint}
	case *ClusterReq:
		*output = *clusterRequestFromProto(message.(*pbx.ClusterRequestPayload))
	case *ClusterResp:
		*output = *clusterResponseFromProto(message.(*pbx.ClusterResponsePayload))
	case *ClusterRoute:
		*output = *clusterRouteFromProto(message.(*pbx.ClusterRoutePayload))
	case *UserCacheReq:
		*output = *clusterUserCacheFromProto(message.(*pbx.ClusterUserCachePayload))
	case *MsgServerCtrl:
		decoded := clusterServerMessageFromProto(message.(*pbx.ClusterServerMessage))
		if decoded == nil || decoded.Ctrl == nil {
			return errors.New("cluster: Protobuf Lane payload 不包含控制响应")
		}
		*output = *decoded.Ctrl
	case *bool:
		*output = message.(*pbx.ClusterAck).Rejected
	}
	return nil
}

func clusterSessionToProto(session *ClusterSess) *pbx.ClusterSession {
	if session == nil {
		return nil
	}
	return &pbx.ClusterSession{
		RemoteAddr: session.RemoteAddr, UserAgent: session.UserAgent,
		Uid: session.Uid.String(), AuthLevel: int32(session.AuthLvl),
		ProtocolVersion: int32(session.Ver), Language: session.Lang,
		CountryCode: session.CountryCode, DeviceId: session.DeviceID,
		Platform: session.Platform, Sid: session.Sid, Background: session.Background,
	}
}

func clusterSessionFromProto(session *pbx.ClusterSession) *ClusterSess {
	if session == nil {
		return nil
	}
	return &ClusterSess{
		RemoteAddr: session.RemoteAddr, UserAgent: session.UserAgent,
		Uid: types.ParseUid(session.Uid), AuthLvl: auth.Level(session.AuthLevel),
		Ver: int(session.ProtocolVersion), Lang: session.Language,
		CountryCode: session.CountryCode, DeviceID: session.DeviceId,
		Platform: session.Platform, Sid: session.Sid, Background: session.Background,
	}
}

func clusterClientMessageToProto(message *ClientComMessage) *pbx.ClusterClientMessage {
	if message == nil {
		return nil
	}
	result := &pbx.ClusterClientMessage{
		Message: pbCliSerialize(message), Id: message.Id, Original: message.Original,
		RcptTo: message.RcptTo, AsUser: message.AsUser,
		AuthLevel: int32(message.AuthLvl), MetaWhat: int32(message.MetaWhat),
	}
	if !message.Timestamp.IsZero() {
		result.TimestampUnixNano = message.Timestamp.UnixNano()
	}
	return result
}

func clusterClientMessageFromProto(message *pbx.ClusterClientMessage) *ClientComMessage {
	if message == nil {
		return nil
	}
	result := pbCliDeserialize(message.Message)
	if result == nil {
		result = &ClientComMessage{}
	}
	result.Id, result.Original, result.RcptTo = message.Id, message.Original, message.RcptTo
	result.AsUser, result.AuthLvl = message.AsUser, int(message.AuthLevel)
	result.MetaWhat = int(message.MetaWhat)
	if message.TimestampUnixNano != 0 {
		result.Timestamp = time.Unix(0, message.TimestampUnixNano).UTC()
	}
	return result
}

func clusterServerMessageToProto(message *ServerComMessage) *pbx.ClusterServerMessage {
	if message == nil {
		return nil
	}
	result := &pbx.ClusterServerMessage{
		Message: pbServSerialize(message), Id: message.Id, RcptTo: message.RcptTo,
		AsUser: message.AsUser, SkipSid: message.SkipSid,
	}
	if message.Ctrl != nil {
		if params, ok := message.Ctrl.Params.(map[string]any); ok {
			for key, value := range params {
				if timestamp, ok := value.(time.Time); ok {
					if result.ControlTimeParams == nil {
						result.ControlTimeParams = make(map[string]int64)
					}
					result.ControlTimeParams[key] = timestamp.UnixNano()
				}
			}
		}
	}
	if !message.Timestamp.IsZero() {
		result.TimestampUnixNano = message.Timestamp.UnixNano()
	}
	return result
}

func clusterServerMessageFromProto(message *pbx.ClusterServerMessage) *ServerComMessage {
	if message == nil {
		return nil
	}
	result := pbServDeserialize(message.Message)
	if result == nil {
		result = &ServerComMessage{}
	}
	result.Id, result.RcptTo, result.AsUser = message.Id, message.RcptTo, message.AsUser
	result.SkipSid = message.SkipSid
	if message.TimestampUnixNano != 0 {
		result.Timestamp = time.Unix(0, message.TimestampUnixNano).UTC()
	}
	if result.Ctrl != nil && len(message.ControlTimeParams) > 0 {
		params, _ := result.Ctrl.Params.(map[string]any)
		if params == nil {
			params = make(map[string]any)
		}
		for key, value := range message.ControlTimeParams {
			params[key] = time.Unix(0, value).UTC()
		}
		result.Ctrl.Params = params
	}
	return result
}

func clusterRequestToProto(request *ClusterReq) *pbx.ClusterRequestPayload {
	return &pbx.ClusterRequestPayload{
		Node: request.Node, Signature: request.Signature, Fingerprint: request.Fingerprint,
		RequestType: int32(request.ReqType), ClientMessage: clusterClientMessageToProto(request.CliMsg),
		ServerMessage: clusterServerMessageToProto(request.SrvMsg), RcptTo: request.RcptTo,
		Session: clusterSessionToProto(request.Sess), Gone: request.Gone,
	}
}

func clusterRequestFromProto(request *pbx.ClusterRequestPayload) *ClusterReq {
	return &ClusterReq{
		Node: request.Node, Signature: request.Signature, Fingerprint: request.Fingerprint,
		ReqType: ProxyReqType(request.RequestType), CliMsg: clusterClientMessageFromProto(request.ClientMessage),
		SrvMsg: clusterServerMessageFromProto(request.ServerMessage), RcptTo: request.RcptTo,
		Sess: clusterSessionFromProto(request.Session), Gone: request.Gone,
	}
}

func clusterResponseToProto(response *ClusterResp) *pbx.ClusterResponsePayload {
	return &pbx.ClusterResponsePayload{
		ServerMessage: clusterServerMessageToProto(response.SrvMsg), OriginalSid: response.OrigSid,
		RcptTo: response.RcptTo, OriginalRequestType: int32(response.OrigReqType),
	}
}

func clusterResponseFromProto(response *pbx.ClusterResponsePayload) *ClusterResp {
	return &ClusterResp{
		SrvMsg: clusterServerMessageFromProto(response.ServerMessage), OrigSid: response.OriginalSid,
		RcptTo: response.RcptTo, OrigReqType: ProxyReqType(response.OriginalRequestType),
	}
}

func clusterRouteToProto(route *ClusterRoute) *pbx.ClusterRoutePayload {
	return &pbx.ClusterRoutePayload{
		Node: route.Node, Signature: route.Signature, Fingerprint: route.Fingerprint,
		ServerMessage: clusterServerMessageToProto(route.SrvMsg), Session: clusterSessionToProto(route.Sess),
	}
}

func clusterRouteFromProto(route *pbx.ClusterRoutePayload) *ClusterRoute {
	return &ClusterRoute{
		Node: route.Node, Signature: route.Signature, Fingerprint: route.Fingerprint,
		SrvMsg: clusterServerMessageFromProto(route.ServerMessage), Sess: clusterSessionFromProto(route.Session),
	}
}

func clusterPushToProto(receipt *push.Receipt) *pbx.ClusterPushReceipt {
	if receipt == nil {
		return nil
	}
	result := &pbx.ClusterPushReceipt{Channel: receipt.Channel}
	for uid, recipient := range receipt.To {
		result.Recipients = append(result.Recipients, &pbx.ClusterPushRecipient{
			Uid: uid.String(), Delivered: int32(recipient.Delivered), Devices: recipient.Devices,
			Unread: int32(recipient.Unread), IncrementUnread: recipient.ShouldIncrementUnreadCountInCache,
		})
	}
	payload := receipt.Payload
	content, _ := json.Marshal(payload.Content)
	result.Payload = &pbx.ClusterPushPayload{
		What: payload.What, Silent: payload.Silent, Topic: payload.Topic,
		From: payload.From, Sequence: int32(payload.SeqId), ContentType: payload.ContentType,
		ContentJson: content, Webrtc: payload.Webrtc, AudioOnly: payload.AudioOnly,
		Replace: payload.Replace, ModeWant: uint32(payload.ModeWant), ModeGiven: uint32(payload.ModeGiven),
	}
	if !payload.Timestamp.IsZero() {
		result.Payload.TimestampUnixNano = payload.Timestamp.UnixNano()
	}
	return result
}

func clusterPushFromProto(receipt *pbx.ClusterPushReceipt) *push.Receipt {
	if receipt == nil {
		return nil
	}
	result := &push.Receipt{To: make(map[types.Uid]push.Recipient), Channel: receipt.Channel}
	for _, recipient := range receipt.Recipients {
		uid := types.ParseUid(recipient.Uid)
		if uid.IsZero() {
			continue
		}
		result.To[uid] = push.Recipient{
			Delivered: int(recipient.Delivered), Devices: recipient.Devices, Unread: int(recipient.Unread),
			ShouldIncrementUnreadCountInCache: recipient.IncrementUnread,
		}
	}
	if payload := receipt.Payload; payload != nil {
		var content any
		if len(payload.ContentJson) > 0 && string(payload.ContentJson) != "null" {
			_ = json.Unmarshal(payload.ContentJson, &content)
		}
		result.Payload = push.Payload{
			What: payload.What, Silent: payload.Silent, Topic: payload.Topic, From: payload.From,
			SeqId: int(payload.Sequence), ContentType: payload.ContentType, Content: content,
			Webrtc: payload.Webrtc, AudioOnly: payload.AudioOnly, Replace: payload.Replace,
			ModeWant: types.AccessMode(payload.ModeWant), ModeGiven: types.AccessMode(payload.ModeGiven),
		}
		if payload.TimestampUnixNano != 0 {
			result.Payload.Timestamp = time.Unix(0, payload.TimestampUnixNano).UTC()
		}
	}
	return result
}

func clusterUserCacheToProto(request *UserCacheReq) *pbx.ClusterUserCachePayload {
	result := &pbx.ClusterUserCachePayload{
		Node: request.Node, UserId: request.UserId.String(), Unread: int32(request.Unread),
		Increment: request.Inc, Gone: request.Gone, PushReceipt: clusterPushToProto(request.PushRcpt),
	}
	for _, uid := range request.UserIdList {
		result.UserIds = append(result.UserIds, uid.String())
	}
	return result
}

func clusterUserCacheFromProto(request *pbx.ClusterUserCachePayload) *UserCacheReq {
	result := &UserCacheReq{
		Node: request.Node, UserId: types.ParseUid(request.UserId), Unread: int(request.Unread),
		Inc: request.Increment, Gone: request.Gone, PushRcpt: clusterPushFromProto(request.PushReceipt),
	}
	for _, value := range request.UserIds {
		if uid := types.ParseUid(value); !uid.IsZero() {
			result.UserIdList = append(result.UserIdList, uid)
		}
	}
	return result
}
