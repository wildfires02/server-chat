package server

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/gob"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"chat/api/pbx"

	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
)

var (
	// clusterGobTypesOnce 保证兼容载荷的 interface 具体类型只注册一次。
	clusterGobTypesOnce sync.Once
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

// encodeClusterPayload 编码迁移期内部负载；外层协议始终是版本化 Protobuf。
func encodeClusterPayload(value any) ([]byte, error) {
	registerClusterGobTypes()
	var buffer bytes.Buffer
	if err := gob.NewEncoder(&buffer).Encode(value); err != nil {
		return nil, fmt.Errorf("cluster: 编码 Lane payload 失败: %w", err)
	}
	return buffer.Bytes(), nil
}

// decodeClusterPayload 解码迁移期内部负载。
func decodeClusterPayload(payload []byte, destination any) error {
	registerClusterGobTypes()
	if len(payload) == 0 {
		return errors.New("cluster: Lane payload 不能为空")
	}
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(destination); err != nil {
		return fmt.Errorf("cluster: 解码 Lane payload 失败: %w", err)
	}
	return nil
}

// registerClusterGobTypes 注册可能出现在 interface 字段中的具体类型。
// 注册靠近编解码器，确保测试、工具和服务进程走完全相同的协议初始化。
func registerClusterGobTypes() {
	clusterGobTypesOnce.Do(func() {
		gob.Register([]any{})
		gob.Register(map[string]any{})
		gob.Register(map[string]int{})
		gob.Register(map[string]string{})
		gob.Register(MsgAccessMode{})
		gob.Register(time.Time{})
	})
}
