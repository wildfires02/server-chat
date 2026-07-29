// Package agora 测试 RTC AccessToken2 的编码、权限和签名。
package agora

import (
	"bytes"
	"compress/zlib"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"io"
	"testing"
)

// decodedToken 保存测试期间从确定性 AccessToken2 中提取的字段。
// 它只映射当前实现会生成的 RTC 子集。
type decodedToken struct {
	// signature 是压缩 Token 开头保存的 HMAC 签名。
	signature []byte
	// signingPayload 是签名覆盖的原始字节序列。
	signingPayload []byte
	// appID 标识 Token 中编码的 Agora 项目。
	appID string
	// issueAt 是密钥派生使用的 Unix 时间戳。
	issueAt uint32
	// ttl 是以秒为单位的 Token 有效期。
	ttl uint32
	// salt 是每个 Token 独立使用的随机值。
	salt uint32
	// privileges 将 RTC 权限编号映射到各自的有效期。
	privileges map[uint16]uint32
	// channel 是 Token 唯一授权的 RTC 频道。
	channel string
	// uid 是 Token 授权的十进制 Agora 用户标识。
	uid string
}

// TestBuildRTCTokenPublisher 验证主播 Token 的 AccessToken2 外层格式、
// 频道和用户绑定、角色权限以及签名。
func TestBuildRTCTokenPublisher(t *testing.T) {
	const (
		appID          = "0123456789abcdef0123456789abcdef"
		appCertificate = "abcdef0123456789abcdef0123456789"
		channel        = "im_123456"
		uid            = uint32(42)
		ttl            = uint32(3600)
		issueAt        = uint32(1_700_000_000)
		salt           = uint32(123456)
	)

	token, err := buildRTCToken(appID, appCertificate, channel, uid, RolePublisher, ttl, issueAt, salt)
	if err != nil {
		t.Fatalf("buildRTCToken() error = %v", err)
	}
	decoded := decodeTokenForTest(t, token)
	if decoded.appID != appID || decoded.channel != channel || decoded.uid != "42" {
		t.Fatalf("token binding = (%q, %q, %q), want (%q, %q, %q)",
			decoded.appID, decoded.channel, decoded.uid, appID, channel, "42")
	}
	if decoded.issueAt != issueAt || decoded.ttl != ttl || decoded.salt != salt {
		t.Fatalf("token lifetime fields = (%d, %d, %d), want (%d, %d, %d)",
			decoded.issueAt, decoded.ttl, decoded.salt, issueAt, ttl, salt)
	}
	if len(decoded.privileges) != 4 {
		t.Fatalf("publisher privilege count = %d, want 4", len(decoded.privileges))
	}
	for _, privilege := range []uint16{
		privilegeJoinChannel,
		privilegePublishAudio,
		privilegePublishVideo,
		privilegePublishData,
	} {
		if decoded.privileges[privilege] != ttl {
			t.Errorf("privilege %d lifetime = %d, want %d", privilege, decoded.privileges[privilege], ttl)
		}
	}
	verifyTokenSignatureForTest(t, decoded, appCertificate)
}

// TestBuildRTCTokenSubscriber 验证只读参与者不会获得音频、视频或数据流
// 发布权限。
func TestBuildRTCTokenSubscriber(t *testing.T) {
	token, err := buildRTCToken(
		"0123456789abcdef0123456789abcdef",
		"abcdef0123456789abcdef0123456789",
		"im_readonly",
		7,
		RoleSubscriber,
		900,
		1_700_000_000,
		9,
	)
	if err != nil {
		t.Fatalf("buildRTCToken() error = %v", err)
	}
	decoded := decodeTokenForTest(t, token)
	if len(decoded.privileges) != 1 || decoded.privileges[privilegeJoinChannel] != 900 {
		t.Fatalf("subscriber privileges = %#v, want join-only privilege", decoded.privileges)
	}
}

// TestBuildRTCTokenRejectsInvalidInput 确保服务端不会使用通配符或格式错误的
// 绑定意外签发凭证。
func TestBuildRTCTokenRejectsInvalidInput(t *testing.T) {
	validID := "0123456789abcdef0123456789abcdef"
	tests := []struct {
		// name 标识被拒绝的输入场景。
		name string
		// appID 是传入构建器的项目标识。
		appID string
		// certificate 是传入构建器的签名证书。
		certificate string
		// channel 是传入构建器的频道绑定。
		channel string
		// uid 是传入构建器的参与者绑定。
		uid uint32
		// ttl 是请求的有效期。
		ttl uint32
	}{
		{name: "invalid app id", appID: "bad", certificate: validID, channel: "channel", uid: 1, ttl: 1},
		{name: "invalid certificate", appID: validID, certificate: "bad", channel: "channel", uid: 1, ttl: 1},
		{name: "empty channel", appID: validID, certificate: validID, uid: 1, ttl: 1},
		{name: "zero uid", appID: validID, certificate: validID, channel: "channel", ttl: 1},
		{name: "zero ttl", appID: validID, certificate: validID, channel: "channel", uid: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildRTCToken(
				test.appID,
				test.certificate,
				test.channel,
				test.uid,
				RolePublisher,
				test.ttl,
				1,
				1,
			); err == nil {
				t.Fatal("buildRTCToken() error = nil, want validation error")
			}
		})
	}
}

// decodeTokenForTest 解析当前包生成的 RTC Token 子集。
func decodeTokenForTest(t *testing.T, token string) decodedToken {
	t.Helper()
	if len(token) < len(accessTokenVersion) || token[:len(accessTokenVersion)] != accessTokenVersion {
		t.Fatalf("token prefix = %q, want %q", token, accessTokenVersion)
	}
	compressed, err := base64.StdEncoding.DecodeString(token[len(accessTokenVersion):])
	if err != nil {
		t.Fatalf("decode token base64: %v", err)
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("open token zlib stream: %v", err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read token zlib stream: %v", err)
	}
	if err = reader.Close(); err != nil {
		t.Fatalf("close token zlib stream: %v", err)
	}

	src := bytes.NewReader(raw)
	signature := readStringForTest(t, src)
	payloadOffset := len(raw) - src.Len()
	decoded := decodedToken{
		signature:      []byte(signature),
		signingPayload: append([]byte(nil), raw[payloadOffset:]...),
		appID:          readStringForTest(t, src),
		issueAt:        readUint32ForTest(t, src),
		ttl:            readUint32ForTest(t, src),
		salt:           readUint32ForTest(t, src),
	}
	if serviceCount := readUint16ForTest(t, src); serviceCount != 1 {
		t.Fatalf("service count = %d, want 1", serviceCount)
	}
	if serviceType := readUint16ForTest(t, src); serviceType != serviceTypeRTC {
		t.Fatalf("service type = %d, want %d", serviceType, serviceTypeRTC)
	}
	decoded.privileges = make(map[uint16]uint32)
	for count := readUint16ForTest(t, src); count > 0; count-- {
		decoded.privileges[readUint16ForTest(t, src)] = readUint32ForTest(t, src)
	}
	decoded.channel = readStringForTest(t, src)
	decoded.uid = readStringForTest(t, src)
	if src.Len() != 0 {
		t.Fatalf("unparsed token bytes = %d, want 0", src.Len())
	}
	return decoded
}

// verifyTokenSignatureForTest 独立重新计算最终 HMAC。
func verifyTokenSignatureForTest(t *testing.T, token decodedToken, certificate string) {
	t.Helper()
	signingKey, err := deriveSigningKey(certificate, token.issueAt, token.salt)
	if err != nil {
		t.Fatalf("derive signing key: %v", err)
	}
	mac := hmac.New(sha256.New, signingKey)
	if _, err = mac.Write(token.signingPayload); err != nil {
		t.Fatalf("hash signing payload: %v", err)
	}
	if !hmac.Equal(token.signature, mac.Sum(nil)) {
		t.Fatal("token signature does not match signing payload")
	}
}

// readUint16ForTest 从测试 Token 中读取一个小端序 uint16。
func readUint16ForTest(t *testing.T, src io.Reader) uint16 {
	t.Helper()
	var value uint16
	if err := binary.Read(src, binary.LittleEndian, &value); err != nil {
		t.Fatalf("read uint16: %v", err)
	}
	return value
}

// readUint32ForTest 从测试 Token 中读取一个小端序 uint32。
func readUint32ForTest(t *testing.T, src io.Reader) uint32 {
	t.Helper()
	var value uint32
	if err := binary.Read(src, binary.LittleEndian, &value); err != nil {
		t.Fatalf("read uint32: %v", err)
	}
	return value
}

// readStringForTest 读取一个 AccessToken2 长度前缀字符串。
func readStringForTest(t *testing.T, src io.Reader) string {
	t.Helper()
	raw := make([]byte, readUint16ForTest(t, src))
	if _, err := io.ReadFull(src, raw); err != nil {
		t.Fatalf("read string: %v", err)
	}
	return string(raw)
}
