/******************************************************************************
 *
 *  描述 :
 *    Agora 音视频通话配置的解析、校验和运行时初始化。
 *
 *****************************************************************************/
package server

import (
	"chat/server/logs"
)

// callConfig 保存统一的 Agora 音视频通话配置。
type callConfig struct {
	// Enabled 控制语音和视频通话是否启用。
	Enabled bool `json:"enabled"`
	// CallEstablishmentTimeout 是未接听自动挂断的秒数。
	CallEstablishmentTimeout int `json:"call_establishment_timeout"`
	// AppID 是 Agora Console 分配的公开项目标识。
	AppID string `json:"app_id"`
	// AppCertificate 是服务端签发 RTC Token 使用的证书。
	AppCertificate string `json:"app_certificate"`
	// TokenTTL 是 RTC Token 的有效秒数。
	TokenTTL int `json:"token_ttl"`
	// ChannelPrefix 是服务端生成频道名时使用的前缀。
	ChannelPrefix string `json:"channel_prefix"`
	// MaxParticipants 是单次通话的最大在线 Session 数。
	MaxParticipants int `json:"max_participants"`
}

// initVideoCalls 初始化统一的 Agora 音视频通话模块。
func initVideoCalls(config callConfig) error {
	if !config.Enabled {
		logs.Info.Println("Audio and video calls are disabled")
		return nil
	}

	provider, err := newAgoraProvider(agoraConfig{
		AppID:           config.AppID,
		AppCertificate:  config.AppCertificate,
		TokenTTL:        config.TokenTTL,
		ChannelPrefix:   config.ChannelPrefix,
		MaxParticipants: config.MaxParticipants,
	})
	if err != nil {
		return err
	}
	globals.agora = provider
	globals.callEstablishmentTimeout = config.CallEstablishmentTimeout
	if globals.callEstablishmentTimeout <= 0 {
		globals.callEstablishmentTimeout = defaultCallEstablishmentTimeout
	}
	logs.Info.Println("Agora audio and video calls are enabled")
	return nil
}
