// Package main 测试音视频通话配置、Agora 绑定和传输协议转换。
package main

import (
	"strings"
	"testing"
	"time"

	"chat/pbx"
)

// TestNewAgoraProviderDefaults 验证生产安全默认值，并拒绝 Agora 不接受的
// 凭据格式和有效期。
func TestNewAgoraProviderDefaults(t *testing.T) {
	provider, err := newAgoraProvider(agoraConfig{
		Enabled:        true,
		AppID:          "0123456789abcdef0123456789abcdef",
		AppCertificate: "abcdef0123456789abcdef0123456789",
	})
	if err != nil {
		t.Fatalf("newAgoraProvider() error = %v", err)
	}
	if provider.tokenTTL != time.Hour {
		t.Errorf("token TTL = %v, want %v", provider.tokenTTL, time.Hour)
	}
	if provider.channelPrefix != "im" {
		t.Errorf("channel prefix = %q, want %q", provider.channelPrefix, "im")
	}
	if provider.maxParticipants != 128 {
		t.Errorf("max participants = %d, want 128", provider.maxParticipants)
	}

	invalid := []agoraConfig{
		{Enabled: true, AppID: "bad", AppCertificate: "abcdef0123456789abcdef0123456789"},
		{
			Enabled:        true,
			AppID:          "0123456789abcdef0123456789abcdef",
			AppCertificate: "bad",
		},
		{
			Enabled:        true,
			AppID:          "0123456789abcdef0123456789abcdef",
			AppCertificate: "abcdef0123456789abcdef0123456789",
			TokenTTL:       86401,
		},
		{
			Enabled:        true,
			AppID:          "0123456789abcdef0123456789abcdef",
			AppCertificate: "abcdef0123456789abcdef0123456789",
			ChannelPrefix:  "unsafe prefix",
		},
	}
	for index, config := range invalid {
		if _, err = newAgoraProvider(config); err == nil {
			t.Errorf("invalid config %d error = nil, want validation error", index)
		}
	}
}

// TestAgoraProviderBindings 验证生成的频道名不会泄露 Topic 标识，并确保
// 多端 Session 获得不同且稳定的 UID。
func TestAgoraProviderBindings(t *testing.T) {
	provider := &agoraProvider{channelPrefix: "im"}
	channel := provider.channelName("grpSecretTopic", 42)
	if len(channel) > 64 {
		t.Fatalf("channel length = %d, want at most 64", len(channel))
	}
	if strings.Contains(channel, "grpSecretTopic") {
		t.Fatalf("channel %q leaks internal Topic name", channel)
	}
	if duplicate := provider.channelName("grpSecretTopic", 42); duplicate != channel {
		t.Fatalf("same call channel = %q, want %q", duplicate, channel)
	}
	firstUID := provider.participantUID(channel, 42, "usr1", "sid1")
	if firstUID == 0 {
		t.Fatal("participant UID = 0, want non-zero")
	}
	if duplicate := provider.participantUID(channel, 42, "usr1", "sid1"); duplicate != firstUID {
		t.Fatalf("same session UID = %d, want %d", duplicate, firstUID)
	}
	if secondUID := provider.participantUID(channel, 42, "usr1", "sid2"); secondUID == firstUID {
		t.Fatalf("different sessions share UID %d", firstUID)
	}
}

// TestPBCallEventAgoraRoundTrip 验证 JSON 和 gRPC 传输层使用相同的 Agora
// 加入、离开和 Token 续期事件。
func TestPBCallEventAgoraRoundTrip(t *testing.T) {
	tests := []struct {
		// event 是 JSON 协议中的事件名称。
		event string
		// protobuf 是 gRPC 协议中的枚举值。
		protobuf pbx.CallEvent
	}{
		{event: constCallEventJoin, protobuf: pbx.CallEvent_JOIN},
		{event: constCallEventLeave, protobuf: pbx.CallEvent_LEAVE},
		{event: constCallEventRefresh, protobuf: pbx.CallEvent_REFRESH},
	}
	for _, test := range tests {
		if got := pbCallEventSerialize(test.event); got != test.protobuf {
			t.Errorf("pbCallEventSerialize(%q) = %v, want %v", test.event, got, test.protobuf)
		}
		if got := pbCallEventDeserialize(test.protobuf); got != test.event {
			t.Errorf("pbCallEventDeserialize(%v) = %q, want %q", test.protobuf, got, test.event)
		}
	}
}
