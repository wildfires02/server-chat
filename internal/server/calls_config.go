/******************************************************************************
 *
 *  描述 :
 *    音视频通话配置的解析、校验和运行时初始化。
 *
 *****************************************************************************/
package server

import (
	"encoding/json"
	"errors"
	"fmt"

	"chat/internal/configutil"
	"chat/server/logs"
)

// callConfig 保存音视频通话的完整配置。
type callConfig struct {
	// Enabled 控制音视频通话功能是否启用。
	Enabled bool `json:"enabled"`
	// CallEstablishmentTimeout 是未接听自动挂断的秒数。
	CallEstablishmentTimeout int `json:"call_establishment_timeout"`
	// ICEServers 是内嵌的 WebRTC ICE 服务器列表。
	ICEServers []iceServer `json:"ice_servers"`
	// ICEServersFile 是外部 ICE 配置文件路径。
	ICEServersFile string `json:"ice_servers_file"`
	// Agora 是群组语音和视频通话的鉴权配置。
	Agora agoraConfig `json:"agora"`
}

// iceServer 描述一个下发给 WebRTC 客户端的 STUN 或 TURN 服务。
type iceServer struct {
	// Username 是 TURN 服务的用户名。
	Username string `json:"username,omitempty"`
	// Credential 是 TURN 服务的凭据。
	Credential string `json:"credential,omitempty"`
	// CredentialType 是凭据的编码类型。
	CredentialType string `json:"credential_type,omitempty"`
	// Urls 是 STUN 或 TURN 服务地址列表。
	Urls []string `json:"urls,omitempty"`
}

// iceServersFileConfig 是独立 ICE YAML 文件的对象根节点。
type iceServersFileConfig struct {
	// ICEServers 保存需要下发给客户端的 STUN 和 TURN 服务。
	ICEServers []iceServer `json:"ice_servers"`
}

// initVideoCalls 初始化音视频通话模块。
func initVideoCalls(rawConfig json.RawMessage) error {
	if len(rawConfig) == 0 {
		return nil
	}

	var config callConfig
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return fmt.Errorf("解析音视频配置失败: %w", err)
	}
	if !config.Enabled {
		logs.Info.Println("音视频通话功能已禁用")
		return nil
	}

	if err := configureICEServers(config); err != nil {
		return err
	}
	if config.Agora.Enabled {
		provider, err := newAgoraProvider(config.Agora)
		if err != nil {
			return err
		}
		globals.agora = provider
	}
	if len(globals.iceServers) == 0 && globals.agora == nil {
		return errors.New("未配置有效的 ICE 服务器或 Agora 群组通话")
	}

	globals.callEstablishmentTimeout = config.CallEstablishmentTimeout
	if globals.callEstablishmentTimeout <= 0 {
		globals.callEstablishmentTimeout = defaultCallEstablishmentTimeout
	}
	logs.Info.Printf("音视频通话功能已启用：ICE 服务器 %d 个，Agora 群组通话 %t",
		len(globals.iceServers), globals.agora != nil)
	return nil
}

// configureICEServers 从内嵌配置或外部文件加载 WebRTC ICE 服务。
func configureICEServers(config callConfig) error {
	if len(config.ICEServers) > 0 {
		globals.iceServers = config.ICEServers
		return nil
	}
	if config.ICEServersFile == "" {
		return nil
	}

	var iceConfig iceServersFileConfig
	if err := configutil.DecodeFile(config.ICEServersFile, &iceConfig); err != nil {
		return fmt.Errorf("解析 ICE 配置文件失败: %w", err)
	}
	globals.iceServers = iceConfig.ICEServers
	return nil
}
