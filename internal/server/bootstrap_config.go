package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"chat/internal/configutil"
)

func rejectServiceArguments(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("服务进程不接受命令行参数，请修改 YAML 配置：%v", args[1:])
	}
	return nil
}

func loadServerConfig(curwd string) (configType, string, error) {
	configFile, err := findServiceConfig(curwd, "im.yaml")
	if err != nil {
		return configType{}, "", err
	}
	var topLevel map[string]json.RawMessage
	if err = configutil.DecodeFileConfigOnly(configFile, &topLevel); err != nil {
		return configType{}, "", err
	}
	if err = validateServerProviderConfigKeys(topLevel); err != nil {
		return configType{}, "", err
	}
	var config configType
	if err = configutil.DecodeFileConfigOnly(configFile, &config); err != nil {
		return configType{}, "", err
	}
	if config.LogFlags == "" {
		config.LogFlags = "stdFlags"
	}
	if err = validateDeploymentConfig(&config, "", config.PprofURL); err != nil {
		return configType{}, "", err
	}
	return config, configFile, nil
}

// validateServerProviderConfigKeys 拒绝旧的通话和推送配置，避免未知键被 YAML 解析器静默忽略。
func validateServerProviderConfigKeys(topLevel map[string]json.RawMessage) error {
	if _, found := topLevel["webrtc"]; found {
		return errors.New("configuration key 'webrtc' was removed; use the top-level 'calls' section")
	}
	if _, found := topLevel["push"]; found {
		return errors.New("configuration key 'push' was removed; use the top-level 'firebase' section")
	}
	if _, found := topLevel["calls"]; !found {
		return errors.New("required configuration section 'calls' is missing")
	}
	if _, found := topLevel["firebase"]; !found {
		return errors.New("required configuration section 'firebase' is missing")
	}
	return nil
}

func findServiceConfig(curwd, name string) (string, error) {
	candidates := []string{
		filepath.Join(curwd, "configs", name),
		filepath.Join(curwd, name),
		filepath.Join("/etc/im", name),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			absolute, absoluteErr := filepath.Abs(candidate)
			if absoluteErr != nil {
				return "", absoluteErr
			}
			return absolute, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", errors.New("找不到配置文件；已检查 configs/" + name + "、" + name + " 和 /etc/im/" + name)
}
