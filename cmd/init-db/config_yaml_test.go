package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"chat/internal/configutil"
)

// TestExampleYAMLConfig 验证初始化工具的最小 YAML 配置可以完整加载。
func TestExampleYAMLConfig(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "init-db.yaml")
	var config configType
	if err := configutil.DecodeFile(configPath, &config); err != nil {
		t.Fatal(err)
	}
	var storeConfig map[string]any
	if err := json.Unmarshal(config.StoreConfig, &storeConfig); err != nil {
		t.Fatalf("store_config 转换后的 JSON 无效：%v", err)
	}
	if _, ok := storeConfig["adapters"]; !ok {
		t.Fatal("初始化 YAML 缺少数据库 adapters")
	}
}
