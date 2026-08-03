package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chat/internal/configutil"
)

// TestConfigDirectoryHasNoRemovedExamples 保证 configs 目录不再引用未部署服务或已删除的兼容配置。
func TestConfigDirectoryHasNoRemovedExamples(t *testing.T) {
	configDirectory := filepath.Join("..", "..", "configs")
	entries, err := os.ReadDir(configDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			path := filepath.Join(configDirectory, entry.Name())
			var topLevel map[string]json.RawMessage
			if err := configutil.DecodeFileConfigOnly(path, &topLevel); err != nil {
				t.Fatal(err)
			}
			for _, removed := range []string{"plugins", "push", "tls", "webrtc"} {
				if _, found := topLevel[removed]; found {
					t.Fatalf("包含已删除的顶层配置 %q", removed)
				}
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(string(content))
			for _, removed := range []string{
				"rethinkdb",
				"clamav_addr",
				"ffmpeg:",
				"libreoffice:",
			} {
				if strings.Contains(lower, removed) {
					t.Fatalf("包含未部署或已删除的配置 %q", removed)
				}
			}
		})
	}
}

func TestServiceProcessesRejectCommandLineArguments(t *testing.T) {
	if err := rejectServiceArguments([]string{"im-server"}); err != nil {
		t.Fatal(err)
	}
	err := rejectServiceArguments([]string{"im-server", "--listen=:7060"})
	if err == nil || !strings.Contains(err.Error(), "YAML") {
		t.Fatalf("命令行参数未被拒绝：%v", err)
	}
}

func TestServerProviderConfigRejectsLegacyKeys(t *testing.T) {
	valid := map[string]json.RawMessage{
		"calls":    json.RawMessage(`{}`),
		"firebase": json.RawMessage(`{}`),
	}
	if err := validateServerProviderConfigKeys(valid); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"webrtc", "push"} {
		invalid := make(map[string]json.RawMessage, len(valid)+1)
		for key, value := range valid {
			invalid[key] = value
		}
		invalid[legacy] = json.RawMessage(`{}`)
		if err := validateServerProviderConfigKeys(invalid); err == nil {
			t.Fatalf("旧配置键 %q 未被拒绝", legacy)
		}
	}
	for _, required := range []string{"calls", "firebase"} {
		invalid := make(map[string]json.RawMessage, len(valid)-1)
		for key, value := range valid {
			if key != required {
				invalid[key] = value
			}
		}
		if err := validateServerProviderConfigKeys(invalid); err == nil {
			t.Fatalf("缺少必需配置键 %q 时未报错", required)
		}
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
	if config.Listen != ":6060" {
		t.Fatalf("YAML 网络配置不完整：listen=%q", config.Listen)
	}
	if config.GrpcListen != "" {
		t.Fatalf("开发单机配置不应启动未使用的 gRPC 端口：%q", config.GrpcListen)
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
	if config.Translation == nil || config.Translation.RefreshInterval != 5 {
		t.Fatalf("翻译策略消费者配置不正确：%+v", config.Translation)
	}
	if config.Firebase.CredentialFile != "./firebase-adminsdk.json" {
		t.Fatalf("Firebase Admin 凭据路径不正确：%q", config.Firebase.CredentialFile)
	}
	if config.Firebase.Enabled || config.Firebase.TimeToLive != 3600 {
		t.Fatalf("Firebase 推送配置不正确：%+v", config.Firebase)
	}
	if !config.Calls.Enabled || config.Calls.AppID == "" || config.Calls.AppCertificate == "" {
		t.Fatalf("Agora 通话配置不正确：%+v", config.Calls)
	}
	assertRawConfigObject(t, "store_config", config.Store)
	assertRawConfigObject(t, "auth_config.token", config.Auth["token"])
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
	if config.Admin.BootstrapToken != "dev-only-change-this-admin-token" {
		t.Fatalf("im-admin 不应读取环境变量覆盖：%+v", config)
	}
	if config.Translation != nil {
		t.Fatal("im-admin 配置不应启动聊天翻译消费者")
	}
	if len(config.TLS) != 0 {
		t.Fatal("im-admin 未启用进程内 TLS 时不应保留空 tls 节点")
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
	// 生产模板中的密钥由部署系统注入，测试用固定值模拟注入。
	config.BusinessPolicy.BearerToken = "production-policy-token"
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

// TestServerYAMLConfigsUseUnifiedProviders 防止部署模板回退到旧的 Push 列表或 WebRTC/TURN 配置。
func TestServerYAMLConfigsUseUnifiedProviders(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..")
	paths := []string{
		"configs/im.yaml",
		"configs/im.cluster-dev.yaml",
		"configs/im.cluster.yaml",
		"deployments/docker/compose/im.cluster.yaml",
		"deployments/kubernetes/base/im.cluster.yaml",
		"tests/cluster/config.template.yaml",
	}
	disabledCallTemplate := "tests/cluster/config.template.yaml"
	for _, relativePath := range paths {
		t.Run(relativePath, func(t *testing.T) {
			var topLevel map[string]json.RawMessage
			path := filepath.Join(repositoryRoot, relativePath)
			if err := configutil.DecodeFileConfigOnly(path, &topLevel); err != nil {
				t.Fatal(err)
			}
			if _, found := topLevel["webrtc"]; found {
				t.Fatal("不应再配置 webrtc 节点")
			}
			if _, found := topLevel["push"]; found {
				t.Fatal("不应再配置通用 push 列表")
			}
			if _, found := topLevel["tls"]; found {
				t.Fatal("网关终止 TLS 时不应保留顶层 tls 空开关")
			}
			if _, found := topLevel["firebase"]; !found {
				t.Fatal("缺少统一 firebase 节点")
			}
			if _, found := topLevel["calls"]; !found {
				t.Fatal("缺少统一 calls 节点")
			}

			var config configType
			if err := configutil.DecodeFileConfigOnly(path, &config); err != nil {
				t.Fatal(err)
			}
			if config.Firebase.TimeToLive != 3600 {
				t.Fatalf("Firebase 过期时间不正确：%d", config.Firebase.TimeToLive)
			}
			if relativePath != disabledCallTemplate && !config.Calls.Enabled {
				t.Fatal("Agora 通话未在运行配置中启用")
			}
		})
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
