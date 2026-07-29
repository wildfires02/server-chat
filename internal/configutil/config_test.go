package configutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDecodeFileYAML 验证 YAML、键名归一化和环境变量覆盖流程。
func TestDecodeFileYAML(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "test.yaml")
	if err := os.WriteFile(configPath, []byte(`
# 配置键大小写由 Viper 统一处理。
Enabled: true
Nested:
  Value: 7
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IM_NESTED__VALUE", "9")
	t.Setenv("IM_ENABLED", "false")

	var config struct {
		Enabled bool `json:"enabled"`
		Nested  struct {
			Value int `json:"value"`
		} `json:"nested"`
	}
	if err := DecodeFile(configPath, &config); err != nil {
		t.Fatal(err)
	}
	if config.Enabled || config.Nested.Value != 9 {
		t.Fatalf("DecodeFile() = %+v, 期望环境变量覆盖后的 YAML 配置", config)
	}
}

// TestDecodeFileRejectsNonYAML 验证配置加载器会拒绝旧格式。
func TestDecodeFileRejectsNonYAML(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "legacy.json")
	if err := os.WriteFile(configPath, []byte(`{"enabled": true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var config struct {
		Enabled bool `json:"enabled"`
	}
	if err := DecodeFile(configPath, &config); err == nil {
		t.Fatal("DecodeFile() 应拒绝非 YAML 配置")
	}
}

// TestDecodeYAMLObjectRoot 验证独立配置也使用 Viper 支持的对象根节点。
func TestDecodeYAMLObjectRoot(t *testing.T) {
	var config struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	data := []byte("entries:\n  - name: first\n  - name: second\n")
	if err := DecodeYAML(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Entries) != 2 || config.Entries[1].Name != "second" {
		t.Fatalf("DecodeYAML() = %+v，期望对象中的两个列表项", config)
	}
}

// TestDecodeYAMLRejectsRootList 验证所有配置必须采用 Viper 支持的对象根节点。
func TestDecodeYAMLRejectsRootList(t *testing.T) {
	var entries []struct {
		Name string `json:"name"`
	}
	if err := DecodeYAML([]byte("- name: first\n"), &entries); err == nil {
		t.Fatal("DecodeYAML() 应拒绝 YAML 顶层列表")
	}
}
