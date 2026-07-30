package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"chat/internal/configutil"
)

func TestServiceProcessesRejectCommandLineArguments(t *testing.T) {
	if err := rejectServiceArguments([]string{"im-server"}); err != nil {
		t.Fatal(err)
	}
	err := rejectServiceArguments([]string{"im-server", "--listen=:7060"})
	if err == nil || !strings.Contains(err.Error(), "YAML") {
		t.Fatalf("命令行参数未被拒绝：%v", err)
	}
}

// TestExampleYAMLConfig 验证仓库提供的主配置可以完整映射到服务端配置结构。
func TestExampleYAMLConfig(t *testing.T) {
	t.Setenv("IM_LISTEN", ":7060")
	repositoryRoot := filepath.Join("..", "..")
	config, configPath, err := loadServerConfig(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(configPath) != "im.yaml" {
		t.Fatalf("加载了意外配置文件：%s", configPath)
	}
	if config.Listen != ":6060" || config.GrpcListen == "" {
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
	if config.Admin != nil {
		t.Fatal("im-server 配置不应包含独立管理服务配置")
	}
	if config.Translation == nil || !config.Translation.Enabled ||
		config.Translation.RefreshInterval != 5 {
		t.Fatalf("翻译策略消费者配置不正确：%+v", config.Translation)
	}
	assertRawConfigObject(t, "store_config", config.Store)
	assertRawConfigObject(t, "auth_config.token", config.Auth["token"])
	assertRawConfigObject(t, "webrtc", config.WebRTC)
}

func TestAdminYAMLConfig(t *testing.T) {
	t.Setenv("IM_LOG_FLAGS", "nocolor")
	t.Setenv("IM_LISTEN", ":7061")
	t.Setenv("IM_ADMIN__BOOTSTRAP_TOKEN", "environment-token")
	configPath := filepath.Join("..", "..", "configs", "admin.yaml")
	var config configType
	if err := configutil.DecodeFileConfigOnly(configPath, &config); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminServiceConfig(&config); err != nil {
		t.Fatalf("独立管理配置未通过门禁：%v", err)
	}
	if config.Admin == nil || !config.Admin.Enabled || config.Listen != ":6061" {
		t.Fatalf("独立管理配置不完整：listen=%q admin=%+v", config.Listen, config.Admin)
	}
	if config.LogFlags != "stdFlags" ||
		config.Admin.BootstrapToken != "dev-only-change-this-admin-token" {
		t.Fatalf("im-admin 不应读取环境变量覆盖：%+v", config)
	}
	if config.Translation != nil {
		t.Fatal("im-admin 配置不应启动聊天翻译消费者")
	}
	assertRawConfigObject(t, "store_config", config.Store)
}

// TestProductionClusterYAMLConfig 验证生产模板可在生成节点专属 YAML 后通过门禁。
func TestProductionClusterYAMLConfig(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "im.cluster.yaml")
	var config configType
	if err := configutil.DecodeFileConfigOnly(configPath, &config); err != nil {
		t.Fatal(err)
	}

	var cluster clusterConfig
	if err := json.Unmarshal(config.Cluster, &cluster); err != nil {
		t.Fatalf("解析生产 cluster_config 失败：%v", err)
	}
	cluster.ThisName = "im-0"
	cluster.AdvertiseAddr = "im-0.im.internal:12000"
	cluster.TLS.CertFile = "/run/secrets/im-0/cert.pem"
	cluster.TLS.KeyFile = "/run/secrets/im-0/key.pem"
	config.Cluster, _ = json.Marshal(cluster)
	if err := validateDeploymentConfig(&config, "", ""); err != nil {
		t.Fatalf("节点专属生产配置未通过部署门禁：%v", err)
	}
	if cluster.ThisName != "im-0" ||
		cluster.AdvertiseAddr != "im-0.im.internal:12000" {
		t.Fatalf("节点 YAML 配置未生效：%+v", cluster)
	}
	if cluster.TLS == nil ||
		cluster.TLS.CertFile != "/run/secrets/im-0/cert.pem" ||
		cluster.TLS.KeyFile != "/run/secrets/im-0/key.pem" {
		t.Fatalf("节点 YAML 证书配置未生效：%+v", cluster.TLS)
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
