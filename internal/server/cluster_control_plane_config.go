package server

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// controlPlaneProviderEtcd 表示使用 etcd v3 维护集群成员租约。
	controlPlaneProviderEtcd = "etcd"
	// defaultControlPlaneLeaseTTL 是节点租约的默认有效期。
	defaultControlPlaneLeaseTTL = 10 * time.Second
	// defaultControlPlaneDialTimeout 是连接 etcd 的默认超时时间。
	defaultControlPlaneDialTimeout = 5 * time.Second
	// minimumControlPlaneLeaseTTL 防止租约过短造成频繁误摘流。
	minimumControlPlaneLeaseTTL = 5 * time.Second
	// maximumControlPlaneLeaseTTL 限制故障节点从集群视图移除的最长时间。
	maximumControlPlaneLeaseTTL = 60 * time.Second
	// defaultScheduledTaskClaimTTL 限制定时任务 Owner 切换期间的重复领取窗口。
	defaultScheduledTaskClaimTTL = 30 * time.Second
)

// clusterControlPlaneConfig 保存集群控制面的连接和租约参数。
type clusterControlPlaneConfig struct {
	Provider    string                        `json:"provider"`
	Endpoints   []string                      `json:"endpoints"`
	Namespace   string                        `json:"namespace"`
	LeaseTTL    string                        `json:"lease_ttl"`
	DialTimeout string                        `json:"dial_timeout"`
	Username    string                        `json:"username"`
	Password    string                        `json:"password"`
	TLS         *clusterControlPlaneTLSConfig `json:"tls"`
}

// clusterControlPlaneTLSConfig 保存 etcd 客户端双向 TLS 文件。
type clusterControlPlaneTLSConfig struct {
	CAFile     string `json:"ca_file"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	ServerName string `json:"server_name"`
}

// normalizeControlPlaneConfig 填充默认值并校验不依赖运行环境的 etcd 参数。
func normalizeControlPlaneConfig(
	config clusterControlPlaneConfig,
) (clusterControlPlaneConfig, error) {
	config.Provider = strings.ToLower(strings.TrimSpace(config.Provider))
	if config.Provider != controlPlaneProviderEtcd {
		return clusterControlPlaneConfig{}, fmt.Errorf("cluster_config.control_plane.provider 必须是 etcd")
	}
	config.Namespace = "/" + strings.Trim(strings.TrimSpace(config.Namespace), "/")
	if config.Namespace == "/" {
		return clusterControlPlaneConfig{}, fmt.Errorf("cluster_config.control_plane.namespace 不能为空")
	}

	endpointSet := make(map[string]struct{}, len(config.Endpoints))
	normalizedEndpoints := make([]string, 0, len(config.Endpoints))
	for index, endpoint := range config.Endpoints {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			return clusterControlPlaneConfig{}, fmt.Errorf(
				"cluster_config.control_plane.endpoints[%d] 不能为空",
				index,
			)
		}
		if _, exists := endpointSet[endpoint]; exists {
			return clusterControlPlaneConfig{}, fmt.Errorf("etcd endpoint %q 重复", endpoint)
		}
		endpointSet[endpoint] = struct{}{}
		normalizedEndpoints = append(normalizedEndpoints, endpoint)
	}
	if len(normalizedEndpoints) == 0 {
		return clusterControlPlaneConfig{}, fmt.Errorf("cluster_config.control_plane.endpoints 不能为空")
	}
	config.Endpoints = normalizedEndpoints

	if strings.TrimSpace(config.LeaseTTL) == "" {
		config.LeaseTTL = defaultControlPlaneLeaseTTL.String()
	}
	leaseTTL, err := time.ParseDuration(config.LeaseTTL)
	if err != nil || leaseTTL < minimumControlPlaneLeaseTTL || leaseTTL > maximumControlPlaneLeaseTTL {
		return clusterControlPlaneConfig{}, fmt.Errorf(
			"cluster_config.control_plane.lease_ttl 必须是 %s 到 %s 之间的时长",
			minimumControlPlaneLeaseTTL,
			maximumControlPlaneLeaseTTL,
		)
	}
	config.LeaseTTL = leaseTTL.String()

	if strings.TrimSpace(config.DialTimeout) == "" {
		config.DialTimeout = defaultControlPlaneDialTimeout.String()
	}
	dialTimeout, err := time.ParseDuration(config.DialTimeout)
	if err != nil || dialTimeout <= 0 || dialTimeout > maximumControlPlaneLeaseTTL {
		return clusterControlPlaneConfig{}, fmt.Errorf(
			"cluster_config.control_plane.dial_timeout 必须是大于 0 且不超过 %s 的时长",
			maximumControlPlaneLeaseTTL,
		)
	}
	config.DialTimeout = dialTimeout.String()
	if config.TLS != nil {
		normalizedTLS, tlsErr := normalizeControlPlaneTLSConfig(*config.TLS)
		if tlsErr != nil {
			return clusterControlPlaneConfig{}, tlsErr
		}
		config.TLS = &normalizedTLS
	}
	return config, nil
}

// normalizeControlPlaneTLSConfig 校验 etcd mTLS 文件路径。
func normalizeControlPlaneTLSConfig(
	config clusterControlPlaneTLSConfig,
) (clusterControlPlaneTLSConfig, error) {
	config.CAFile = strings.TrimSpace(config.CAFile)
	config.CertFile = strings.TrimSpace(config.CertFile)
	config.KeyFile = strings.TrimSpace(config.KeyFile)
	config.ServerName = strings.TrimSpace(config.ServerName)
	if config.CAFile == "" || config.CertFile == "" || config.KeyFile == "" {
		return clusterControlPlaneTLSConfig{}, errors.New(
			"control_plane.tls 必须配置 ca_file、cert_file 和 key_file",
		)
	}
	return config, nil
}

// loadControlPlaneTLSConfig 加载 etcd 客户端双向 TLS 凭据。
func loadControlPlaneTLSConfig(
	config clusterControlPlaneTLSConfig,
) (*tls.Config, error) {
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, fmt.Errorf("读取 control_plane tls ca_file 失败: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("control_plane tls ca_file 不包含有效 CA 证书")
	}
	certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 control_plane tls 客户端证书失败: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		Certificates: []tls.Certificate{certificate},
		ServerName:   config.ServerName,
	}, nil
}

// isValidClusterIdentifier 限制控制面键路径中可使用的集群和节点标识字符。
func isValidClusterIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
