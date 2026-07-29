package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"chat/internal/configutil"
)

// TestExampleYAMLConfig 验证仓库提供的主配置可以完整映射到服务端配置结构。
func TestExampleYAMLConfig(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "im.yaml")
	var config configType
	if err := configutil.DecodeFile(configPath, &config); err != nil {
		t.Fatal(err)
	}
	if config.Listen == "" || config.GrpcListen == "" {
		t.Fatalf("YAML 网络配置不完整：listen=%q grpc_listen=%q", config.Listen, config.GrpcListen)
	}
	if config.Runtime.Environment != environmentDevelopment ||
		config.Runtime.DeploymentMode != deploymentModeStandalone {
		t.Fatalf("YAML 运行模式不正确：%+v", config.Runtime)
	}
	if err := validateDeploymentConfig(&config, "", ""); err != nil {
		t.Fatalf("开发单机配置未通过部署门禁：%v", err)
	}
	if len(config.Cluster) != 0 {
		t.Fatalf("开发单机配置意外加载了 cluster_config：%s", config.Cluster)
	}
	assertRawConfigObject(t, "store_config", config.Store)
	assertRawConfigObject(t, "auth_config.token", config.Auth["token"])
	assertRawConfigObject(t, "webrtc", config.WebRTC)
}

// TestProductionClusterYAMLConfig 验证生产模板可通过环境变量绑定节点身份和证书。
func TestProductionClusterYAMLConfig(t *testing.T) {
	t.Setenv("IM_CLUSTER_CONFIG__SELF", "im-0")
	t.Setenv("IM_CLUSTER_CONFIG__ADVERTISE_ADDR", "im-0.im.internal:12000")
	t.Setenv("IM_CLUSTER_CONFIG__TLS__CERT_FILE", "/run/secrets/im-0/cert.pem")
	t.Setenv("IM_CLUSTER_CONFIG__TLS__KEY_FILE", "/run/secrets/im-0/key.pem")

	configPath := filepath.Join("..", "..", "configs", "im.cluster.yaml")
	var config configType
	if err := configutil.DecodeFile(configPath, &config); err != nil {
		t.Fatal(err)
	}
	if err := validateDeploymentConfig(&config, "", ""); err != nil {
		t.Fatalf("生产集群模板未通过部署门禁：%v", err)
	}

	var cluster clusterConfig
	if err := json.Unmarshal(config.Cluster, &cluster); err != nil {
		t.Fatalf("解析生产 cluster_config 失败：%v", err)
	}
	if cluster.ThisName != "im-0" ||
		cluster.AdvertiseAddr != "im-0.im.internal:12000" {
		t.Fatalf("节点环境变量覆盖未生效：%+v", cluster)
	}
	if cluster.TLS == nil ||
		cluster.TLS.CertFile != "/run/secrets/im-0/cert.pem" ||
		cluster.TLS.KeyFile != "/run/secrets/im-0/key.pem" {
		t.Fatalf("节点证书环境变量覆盖未生效：%+v", cluster.TLS)
	}
}

// TestExampleICEYAMLConfig 验证独立 ICE YAML 对象可以映射到通话配置。
func TestExampleICEYAMLConfig(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "ice-servers.example.yaml")
	var config iceServersFileConfig
	if err := configutil.DecodeFile(configPath, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.ICEServers) != 2 || len(config.ICEServers[1].Urls) == 0 {
		t.Fatalf("ICE YAML 配置不完整：%+v", config.ICEServers)
	}
}

// assertRawConfigObject 验证延迟解析的配置节点仍是有效 JSON 对象。
func assertRawConfigObject(t *testing.T, name string, raw json.RawMessage) {
	t.Helper()
	var value map[string]any
	if len(raw) == 0 {
		t.Fatalf("%s 未从 YAML 加载", name)
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("%s 转换后的 JSON 无效：%v", name, err)
	}
	if len(value) == 0 {
		t.Fatalf("%s 配置为空", name)
	}
}
