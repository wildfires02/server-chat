// Package configutil 提供项目内统一的配置文件解析工具。
package configutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

const envPrefix = "IM"

// DecodeFile 读取扩展名为 .yaml 或 .yml 的配置文件。
func DecodeFile(path string, target any) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
	default:
		return fmt.Errorf("配置文件 %q 必须使用 .yaml 或 .yml 扩展名", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取配置文件 %q 失败: %w", path, err)
	}
	if err = DecodeYAML(data, target); err != nil {
		return fmt.Errorf("解析配置文件 %q 失败: %w", path, err)
	}
	return nil
}

// DecodeYAML 将 YAML 配置解码到目标对象。
func DecodeYAML(data []byte, target any) error {
	return decodeYAML(data, target)
}

// decodeYAML 使用 Viper 统一处理键名、嵌套结构和环境变量覆盖。
func decodeYAML(data []byte, target any) error {
	reader := viper.New()
	reader.SetConfigType("yaml")
	if err := reader.ReadConfig(bytes.NewReader(data)); err != nil {
		return err
	}

	// 先保存 YAML 中的原始类型，再启用环境变量覆盖。环境变量本身都是
	// 字符串，需要按原始类型恢复为整数、浮点数或布尔值。
	baseSettings := reader.AllSettings()
	reader.SetEnvPrefix(envPrefix)
	reader.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	reader.AutomaticEnv()

	// AllSettings 会通过 Get 读取已知配置键，因此 IM_ 前缀的环境变量
	// 可以覆盖 YAML 中的标量值。双下划线表示配置层级。
	settings := reader.AllSettings()
	restoreScalarTypes(settings, baseSettings)
	normalized, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, target)
}

// restoreScalarTypes 按 YAML 原始值恢复环境变量覆盖后的标量类型。
func restoreScalarTypes(settings, base map[string]any) {
	for key, value := range settings {
		baseValue, exists := base[key]
		if !exists {
			continue
		}

		currentMap, currentIsMap := value.(map[string]any)
		baseMap, baseIsMap := baseValue.(map[string]any)
		if currentIsMap && baseIsMap {
			restoreScalarTypes(currentMap, baseMap)
			continue
		}

		text, overriddenByString := value.(string)
		if !overriddenByString {
			continue
		}
		switch baseValue.(type) {
		case bool:
			if converted, err := strconv.ParseBool(text); err == nil {
				settings[key] = converted
			}
		case int:
			if converted, err := strconv.Atoi(text); err == nil {
				settings[key] = converted
			}
		case int64:
			if converted, err := strconv.ParseInt(text, 10, 64); err == nil {
				settings[key] = converted
			}
		case float64:
			if converted, err := strconv.ParseFloat(text, 64); err == nil {
				settings[key] = converted
			}
		}
	}
}
