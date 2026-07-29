package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chat/api/pbx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// clusterTestCA 保存测试 CA 的证书和签名私钥。
type clusterTestCA struct {
	// certificate 是已解析的 CA 证书。
	certificate *x509.Certificate
	// privateKey 是 CA 的 Ed25519 私钥。
	privateKey ed25519.PrivateKey
	// pem 是写入各节点 ca_file 的 PEM 内容。
	pem []byte
}

// newClusterTestCA 创建仅在当前测试中有效的自签名 CA。
func newClusterTestCA(t *testing.T) clusterTestCA {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cluster-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	rawCertificate, err := x509.CreateCertificate(
		rand.Reader,
		template,
		template,
		publicKey,
		privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(rawCertificate)
	if err != nil {
		t.Fatal(err)
	}
	return clusterTestCA{
		certificate: certificate,
		privateKey:  privateKey,
		pem: pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: rawCertificate,
		}),
	}
}

// writeClusterTestNodeCertificate 写入具备服务端和客户端用途的节点证书。
func writeClusterTestNodeCertificate(
	t *testing.T,
	ca clusterTestCA,
	directory string,
	nodeName string,
	serial int64,
) normalizedClusterTLSConfig {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: nodeName},
		DNSNames:     []string{nodeName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}
	rawCertificate, err := x509.CreateCertificate(
		rand.Reader,
		template,
		ca.certificate,
		publicKey,
		ca.privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	rawPrivateKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := normalizedClusterTLSConfig{
		CAFile:   filepath.Join(directory, "ca.pem"),
		CertFile: filepath.Join(directory, "node.pem"),
		KeyFile:  filepath.Join(directory, "node-key.pem"),
	}
	if err = os.WriteFile(config.CAFile, ca.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(config.CertFile, pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: rawCertificate,
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(config.KeyFile, pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: rawPrivateKey,
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	return config
}

// TestClusterTLSMaterialAndCertificateRotation 验证节点身份和握手时证书热加载。
func TestClusterTLSMaterialAndCertificateRotation(t *testing.T) {
	ca := newClusterTestCA(t)
	directory := t.TempDir()
	config := writeClusterTestNodeCertificate(t, ca, directory, "node-a", 2)
	material, err := loadClusterTLSMaterial(config, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	firstCertificate, err := material.loadCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if firstCertificate.Leaf.SerialNumber.Int64() != 2 {
		t.Fatalf("首次证书序列号 = %s", firstCertificate.Leaf.SerialNumber)
	}

	// 使用相同路径写入同 CA 签发的新证书，下一次握手读取新序列号。
	writeClusterTestNodeCertificate(t, ca, directory, "node-a", 3)
	rotatedCertificate, err := material.loadCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if rotatedCertificate.Leaf.SerialNumber.Int64() != 3 {
		t.Fatalf("轮换后证书序列号 = %s，期望 3", rotatedCertificate.Leaf.SerialNumber)
	}

	if _, err = loadClusterTLSMaterial(config, "node-b"); err == nil ||
		!strings.Contains(err.Error(), "DNS SAN") {
		t.Fatalf("错误节点身份校验结果 = %v", err)
	}
}

// TestClusterGRPCMutualTLS 验证真实 gRPC 双向流必须完成双方证书校验。
func TestClusterGRPCMutualTLS(t *testing.T) {
	ca := newClusterTestCA(t)
	clientDirectory := t.TempDir()
	serverDirectory := t.TempDir()
	clientConfig := writeClusterTestNodeCertificate(t, ca, clientDirectory, "node-a", 10)
	serverConfig := writeClusterTestNodeCertificate(t, ca, serverDirectory, "node-b", 11)
	clientMaterial, err := loadClusterTLSMaterial(clientConfig, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	serverMaterial, err := loadClusterTLSMaterial(serverConfig, "node-b")
	if err != nil {
		t.Fatal(err)
	}

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer(grpc.Creds(serverMaterial.serverCredentials()))
	recordingServer := &recordingClusterLaneServer{}
	pbx.RegisterClusterTransportServer(grpcServer, recordingServer)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	node := &ClusterNode{name: "node-b", address: "passthrough:///cluster-tls"}
	cluster := &Cluster{
		clusterID:    "cluster-tls",
		thisNodeName: "node-a",
		fingerprint:  1,
		tlsMaterial:  clientMaterial,
		nodes: map[string]*ClusterNode{
			node.name: node,
		},
	}
	cluster.rehash([]string{"node-a", "node-b"})
	peer, err := newClusterGRPCPeerWithDialOptions(
		cluster,
		node,
		normalizedClusterTransport{
			LaneCount:              1,
			ReliableQueueCapacity:  16,
			EphemeralQueueCapacity: 16,
			DialTimeout:            time.Second,
			RequestTimeout:         2 * time.Second,
		},
		grpc.WithTransportCredentials(clientMaterial.clientCredentials("node-b")),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	peer.Start()
	t.Cleanup(peer.Close)

	var rejected bool
	if err = peer.Call(
		"Cluster.TopicMaster",
		&ClusterReq{RcptTo: "grp-mtls"},
		&rejected,
	); err != nil {
		t.Fatalf("mTLS Lane 调用失败：%v", err)
	}
}

// TestClusterLaneRejectsCertificateNodeMismatch 验证帧来源不能冒充证书外的节点。
func TestClusterLaneRejectsCertificateNodeMismatch(t *testing.T) {
	ca := newClusterTestCA(t)
	config := writeClusterTestNodeCertificate(t, ca, t.TempDir(), "node-a", 20)
	material, err := loadClusterTLSMaterial(config, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := material.loadCertificate()
	if err != nil {
		t.Fatal(err)
	}
	cluster := &Cluster{
		clusterID:    "cluster-tls",
		thisNodeName: "node-server",
		nodes: map[string]*ClusterNode{
			"node-b": {name: "node-b"},
		},
	}
	cluster.rehash([]string{"node-server", "node-b"})
	server := &clusterLaneServer{
		cluster:     cluster,
		config:      normalizedClusterTransport{LaneCount: 1},
		tlsRequired: true,
	}
	frame := &pbx.ClusterFrame{
		ClusterId:          cluster.clusterID,
		SourceNode:         "node-b",
		SourceInstance:     1,
		ProtocolVersion:    clusterProtocolVersion,
		MinProtocolVersion: clusterMinProtocolVersion,
		Lane:               0,
		Sequence:           1,
		RequestId:          "request-mismatch",
		Kind:               pbx.ClusterFrameKind_CLUSTER_FRAME_PING,
	}
	err = server.validateFrame(frame, 1, 0, "", 0, certificate.Leaf)
	if err == nil || !strings.Contains(err.Error(), "证书身份") {
		t.Fatalf("证书节点冒充校验结果 = %v", err)
	}
}
