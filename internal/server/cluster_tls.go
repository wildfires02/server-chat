package server

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc/credentials"
)

const (
	// clusterCertificateExpiryReadinessWindow 在证书过期前提前摘流并触发告警。
	clusterCertificateExpiryReadinessWindow = time.Hour
)

// clusterTLSConfig 保存节点间双向 TLS 证书路径。
type clusterTLSConfig struct {
	// CAFile 是签发全部集群节点证书的 CA Bundle。
	CAFile string `json:"ca_file"`
	// CertFile 是当前节点证书链。
	CertFile string `json:"cert_file"`
	// KeyFile 是当前节点证书私钥。
	KeyFile string `json:"key_file"`
}

// normalizedClusterTLSConfig 保存去除空白后的证书路径。
type normalizedClusterTLSConfig struct {
	// CAFile 是可信 CA Bundle 路径。
	CAFile string
	// CertFile 是节点证书链路径。
	CertFile string
	// KeyFile 是节点私钥路径。
	KeyFile string
}

// clusterTLSMaterial 保存已加载 CA，并在每次握手时重新读取节点证书和私钥。
type clusterTLSMaterial struct {
	// config 保存证书文件位置。
	config normalizedClusterTLSConfig
	// roots 是客户端验证远端服务证书使用的 CA。
	roots *x509.CertPool
	// clientCAs 是服务端验证远端客户端证书使用的 CA。
	clientCAs *x509.CertPool
	// thisNodeName 是当前证书必须精确包含的 DNS SAN。
	thisNodeName string
}

// normalizeClusterTLSConfig 校验节点间 TLS 必填路径。
func normalizeClusterTLSConfig(config clusterTLSConfig) (normalizedClusterTLSConfig, error) {
	normalized := normalizedClusterTLSConfig{
		CAFile:   strings.TrimSpace(config.CAFile),
		CertFile: strings.TrimSpace(config.CertFile),
		KeyFile:  strings.TrimSpace(config.KeyFile),
	}
	if normalized.CAFile == "" {
		return normalizedClusterTLSConfig{}, errors.New("cluster tls ca_file 不能为空")
	}
	if normalized.CertFile == "" {
		return normalizedClusterTLSConfig{}, errors.New("cluster tls cert_file 不能为空")
	}
	if normalized.KeyFile == "" {
		return normalizedClusterTLSConfig{}, errors.New("cluster tls key_file 不能为空")
	}
	return normalized, nil
}

// loadClusterTLSMaterial 加载 CA，并验证当前节点证书身份和双向用途。
func loadClusterTLSMaterial(
	config normalizedClusterTLSConfig,
	thisNodeName string,
) (*clusterTLSMaterial, error) {
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, fmt.Errorf("读取 cluster tls ca_file 失败: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("cluster tls ca_file 不包含有效 CA 证书")
	}
	material := &clusterTLSMaterial{
		config:       config,
		roots:        roots,
		clientCAs:    roots.Clone(),
		thisNodeName: thisNodeName,
	}
	if _, err = material.loadCertificate(); err != nil {
		return nil, err
	}
	return material, nil
}

// serverCredentials 创建强制校验客户端证书的 TLS 1.3 服务端凭证。
func (material *clusterTLSMaterial) serverCredentials() credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  material.clientCAs,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return material.loadCertificate()
		},
	})
}

// clientCredentials 创建校验指定远端节点 DNS SAN 的 TLS 1.3 客户端凭证。
func (material *clusterTLSMaterial) clientCredentials(
	remoteNodeName string,
) credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    material.roots,
		ServerName: remoteNodeName,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return material.loadCertificate()
		},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("cluster tls: 远端未提供节点证书")
			}
			if !certificateHasExactDNSName(state.PeerCertificates[0], remoteNodeName) {
				return fmt.Errorf(
					"cluster tls: 远端证书不包含精确节点 DNS SAN %q",
					remoteNodeName,
				)
			}
			return nil
		},
	})
}

// loadCertificate 每次新握手读取证书文件，使证书/私钥轮换无需重启进程。
func (material *clusterTLSMaterial) loadCertificate() (*tls.Certificate, error) {
	certificate, err := tls.LoadX509KeyPair(
		material.config.CertFile,
		material.config.KeyFile,
	)
	if err != nil {
		return nil, fmt.Errorf("加载 cluster tls 节点证书失败: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return nil, errors.New("cluster tls 节点证书链为空")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("解析 cluster tls 节点证书失败: %w", err)
	}
	if !certificateHasExactDNSName(leaf, material.thisNodeName) {
		return nil, fmt.Errorf(
			"cluster tls 节点证书不包含当前节点 DNS SAN %q",
			material.thisNodeName,
		)
	}
	intermediates := x509.NewCertPool()
	for _, rawCertificate := range certificate.Certificate[1:] {
		parsed, parseErr := x509.ParseCertificate(rawCertificate)
		if parseErr != nil {
			return nil, fmt.Errorf("解析 cluster tls 中间证书失败: %w", parseErr)
		}
		intermediates.AddCert(parsed)
	}
	verifyOptions := x509.VerifyOptions{
		Roots:         material.roots,
		Intermediates: intermediates,
		DNSName:       material.thisNodeName,
		KeyUsages: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}
	if _, err = leaf.Verify(verifyOptions); err != nil {
		return nil, fmt.Errorf("验证 cluster tls 服务端证书失败: %w", err)
	}
	verifyOptions.DNSName = ""
	verifyOptions.KeyUsages = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if _, err = leaf.Verify(verifyOptions); err != nil {
		return nil, fmt.Errorf("验证 cluster tls 客户端证书失败: %w", err)
	}
	certificate.Leaf = leaf
	return &certificate, nil
}

// readinessError 验证磁盘上的当前证书仍有效且没有进入过期危险窗口。
func (material *clusterTLSMaterial) readinessError() error {
	if material == nil {
		return errors.New("cluster tls 材料尚未加载")
	}
	certificate, err := material.loadCertificate()
	if err != nil {
		return err
	}
	if time.Until(certificate.Leaf.NotAfter) <= clusterCertificateExpiryReadinessWindow {
		return fmt.Errorf(
			"cluster tls 节点证书将在 %s 过期",
			certificate.Leaf.NotAfter.UTC().Format(time.RFC3339),
		)
	}
	return nil
}

// certificateHasExactDNSName 禁止通配符证书冒充多个静态节点身份。
func certificateHasExactDNSName(certificate *x509.Certificate, nodeName string) bool {
	if certificate == nil || strings.TrimSpace(nodeName) == "" {
		return false
	}
	for _, dnsName := range certificate.DNSNames {
		if strings.EqualFold(dnsName, nodeName) {
			return true
		}
	}
	return false
}
