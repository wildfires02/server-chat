package server

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"

	"chat/api/pbx"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Lane 按接收顺序执行请求并在同一流内返回关联响应。
func (server *clusterLaneServer) Lane(stream pbx.ClusterTransport_LaneServer) error {
	peerCertificate, certificateErr := clusterPeerCertificate(
		stream.Context(),
		server.tlsRequired,
	)
	if certificateErr != nil {
		return status.Error(codes.Unauthenticated, certificateErr.Error())
	}
	var expectedSequence uint64 = 1
	var boundLane uint32
	var boundNode string
	var boundInstance int64
	for {
		frame, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		response := &pbx.ClusterFrame{
			ClusterId:          server.cluster.clusterID,
			SourceNode:         server.cluster.thisNodeName,
			SourceInstance:     server.cluster.fingerprint,
			ProtocolVersion:    negotiatedClusterProtocolVersion(frame),
			MinProtocolVersion: clusterMinProtocolVersion,
			Lane:               frame.Lane,
			Sequence:           frame.Sequence,
			RequestId:          frame.RequestId,
			Kind:               frame.Kind,
			Response:           true,
			ClusterEpoch:       server.cluster.viewEpoch.Load(),
			RingSignature:      server.cluster.ringSignature(),
		}
		validationErr := server.validateFrame(
			frame,
			expectedSequence,
			boundLane,
			boundNode,
			boundInstance,
			peerCertificate,
		)
		if validationErr != nil {
			response.Error = validationErr.Error()
		} else {
			if expectedSequence == 1 {
				boundLane = frame.Lane
				boundNode = frame.SourceNode
				boundInstance = frame.SourceInstance
			}
			dedupeKey := fmt.Sprintf(
				"%s/%d/%s",
				frame.SourceNode,
				frame.SourceInstance,
				frame.RequestId,
			)
			response.Payload, err = server.dedupe.Execute(
				stream.Context(),
				dedupeKey,
				frame.Kind,
				frame.Payload,
				func() ([]byte, error) {
					return server.dispatch(frame.Kind, frame.Payload)
				},
			)
			if err != nil {
				response.Error = err.Error()
			}
		}
		if sendErr := stream.Send(response); sendErr != nil {
			return sendErr
		}
		if validationErr != nil {
			return status.Error(codes.FailedPrecondition, validationErr.Error())
		}
		expectedSequence++
	}
}

// validateFrame 校验集群、节点、版本、Lane 和流内序号。
func (server *clusterLaneServer) validateFrame(
	frame *pbx.ClusterFrame,
	expectedSequence uint64,
	boundLane uint32,
	boundNode string,
	boundInstance int64,
	peerCertificate *x509.Certificate,
) error {
	if frame == nil || frame.Response {
		return errors.New("cluster: 无效的 Lane 请求帧")
	}
	if frame.ClusterId != server.cluster.clusterID {
		return fmt.Errorf("cluster: cluster_id %q 不匹配", frame.ClusterId)
	}
	if !clusterProtocolVersionsOverlap(
		frame.MinProtocolVersion,
		frame.ProtocolVersion,
	) {
		return fmt.Errorf(
			"cluster: 协议范围 %d～%d 与本节点 %d～%d 不兼容",
			normalizedClusterMinProtocol(frame.MinProtocolVersion, frame.ProtocolVersion),
			frame.ProtocolVersion,
			clusterMinProtocolVersion,
			clusterProtocolVersion,
		)
	}
	if int(frame.Lane) >= server.config.LaneCount {
		return fmt.Errorf("cluster: Lane %d 超出范围", frame.Lane)
	}
	if frame.Sequence != expectedSequence {
		return fmt.Errorf(
			"cluster: Lane %d sequence=%d，期望 %d",
			frame.Lane,
			frame.Sequence,
			expectedSequence,
		)
	}
	if frame.RequestId == "" {
		return errors.New("cluster: request_id 不能为空")
	}
	if frame.Kind == pbx.ClusterFrameKind_CLUSTER_FRAME_UNSPECIFIED {
		return errors.New("cluster: 请求类型不能为空")
	}
	if frame.SourceNode == server.cluster.thisNodeName || server.cluster.nodes[frame.SourceNode] == nil {
		return fmt.Errorf("cluster: 来源节点 %q 未配置", frame.SourceNode)
	}
	if server.tlsRequired &&
		!certificateHasExactDNSName(peerCertificate, frame.SourceNode) {
		return fmt.Errorf(
			"cluster: 客户端证书身份与来源节点 %q 不匹配",
			frame.SourceNode,
		)
	}
	if frame.SourceInstance <= 0 {
		return errors.New("cluster: source_instance 必须是有效的进程实例标识")
	}
	if expectedSequence > 1 &&
		(frame.Lane != boundLane ||
			frame.SourceNode != boundNode ||
			frame.SourceInstance != boundInstance) {
		return errors.New("cluster: 同一 Lane 流不能切换来源节点、实例或 Lane 编号")
	}
	if frame.Kind != pbx.ClusterFrameKind_CLUSTER_FRAME_PING &&
		frame.RingSignature != server.cluster.ringSignature() {
		return fmt.Errorf(
			"cluster: ring_signature=%q，期望 %q",
			frame.RingSignature,
			server.cluster.ringSignature(),
		)
	}
	if server.cluster.controlPlane != nil &&
		frame.ClusterEpoch != server.cluster.viewEpoch.Load() {
		return fmt.Errorf(
			"cluster: Cluster View epoch=%d，期望 %d",
			frame.ClusterEpoch,
			server.cluster.viewEpoch.Load(),
		)
	}
	return nil
}

// dispatch 解码业务负载并调用现有集群端点，确保迁移期间业务语义不变。
func (server *clusterLaneServer) dispatch(
	kind pbx.ClusterFrameKind,
	payload []byte,
) ([]byte, error) {
	var rejected bool
	switch kind {
	case pbx.ClusterFrameKind_CLUSTER_FRAME_PING:
		var request ClusterPing
		if err := decodeClusterPayload(payload, &request); err != nil {
			return nil, err
		}
		if err := server.cluster.Ping(&request, &rejected); err != nil {
			return nil, err
		}
	case pbx.ClusterFrameKind_CLUSTER_FRAME_TOPIC_MASTER:
		var request ClusterReq
		if err := decodeClusterPayload(payload, &request); err != nil {
			return nil, err
		}
		if request.CliMsg != nil &&
			clientMessageRequiresWrite(request.CliMsg) &&
			!serviceAllowsWrites() {
			return nil, errors.New("cluster: 当前节点未就绪，拒绝远端写请求")
		}
		if err := server.cluster.TopicMaster(&request, &rejected); err != nil {
			return nil, err
		}
	case pbx.ClusterFrameKind_CLUSTER_FRAME_TOPIC_PROXY:
		var request ClusterResp
		if err := decodeClusterPayload(payload, &request); err != nil {
			return nil, err
		}
		if err := server.cluster.TopicProxy(&request, &rejected); err != nil {
			return nil, err
		}
	case pbx.ClusterFrameKind_CLUSTER_FRAME_ROUTE:
		var request ClusterRoute
		if err := decodeClusterPayload(payload, &request); err != nil {
			return nil, err
		}
		if err := server.cluster.Route(&request, &rejected); err != nil {
			return nil, err
		}
	case pbx.ClusterFrameKind_CLUSTER_FRAME_USER_CACHE:
		var request UserCacheReq
		if err := decodeClusterPayload(payload, &request); err != nil {
			return nil, err
		}
		if err := server.cluster.UserCacheUpdate(&request, &rejected); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("cluster: 不支持的帧类型 %s", kind)
	}
	return encodeClusterPayload(rejected)
}
