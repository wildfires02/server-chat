// Package agora 实现 Agora AccessToken2 的服务端子集，用于鉴权 RTC 频道参与者。
//
// 二进制格式遵循 Agora 官方开源 Token 生成器：
// https://github.com/AgoraIO/Tools/tree/master/DynamicKey/AgoraDynamicKey/go/src
package agora

import (
	"bytes"
	"compress/zlib"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

const (
	// accessTokenVersion 是 Agora AccessToken2 使用的版本前缀。
	accessTokenVersion = "007"
	// serviceTypeRTC 标识 AccessToken2 中的 RTC 服务。
	serviceTypeRTC uint16 = 1
	// privilegeJoinChannel 授予加入指定 RTC 频道的权限。
	privilegeJoinChannel uint16 = 1
	// privilegePublishAudio 授予发布音频流的权限。
	privilegePublishAudio uint16 = 2
	// privilegePublishVideo 授予发布视频流的权限。
	privilegePublishVideo uint16 = 3
	// privilegePublishData 授予发布 RTC 数据流的权限。
	privilegePublishData uint16 = 4
)

// Role 描述参与者 Token 中编码的 RTC 权限角色。
type Role uint8

const (
	// RoleSubscriber 只授予加入频道权限，不允许发布媒体流。
	RoleSubscriber Role = iota
	// RolePublisher 授予加入频道以及发布音频、视频和数据流的权限。
	RolePublisher
)

// BuildRTCToken 创建绑定单一频道和数字 Agora 用户 ID 的生产 AccessToken2。
// ttl 以秒为单位且必须大于零。
func BuildRTCToken(appID, appCertificate, channel string, uid uint32, role Role, ttl uint32) (string, error) {
	salt, err := randomSalt(rand.Reader)
	if err != nil {
		return "", err
	}
	return buildRTCToken(appID, appCertificate, channel, uid, role, ttl, uint32(time.Now().Unix()), salt)
}

// buildRTCToken 使用显式签发时间和随机盐构建确定性 Token。
// 通过注入熵源参数，可以独立测试线格式，同时不降低生产环境的随机性。
func buildRTCToken(appID, appCertificate, channel string, uid uint32, role Role, ttl, issueAt, salt uint32) (string, error) {
	if !isAgoraIdentifier(appID) || !isAgoraIdentifier(appCertificate) {
		return "", errors.New("Agora App ID 和 App Certificate 必须是 32 位十六进制字符串")
	}
	if channel == "" || len(channel) > 64 {
		return "", errors.New("Agora 频道名长度必须为 1 到 64 字节")
	}
	if uid == 0 {
		return "", errors.New("Agora 用户 ID 不能为 0")
	}
	if ttl == 0 {
		return "", errors.New("Agora Token 有效期必须大于 0")
	}
	if role != RoleSubscriber && role != RolePublisher {
		return "", errors.New("Agora RTC 角色无效")
	}

	// 签名载荷以项目标识和 Token 有效期开始。
	payload := new(bytes.Buffer)
	if err := packString(payload, appID); err != nil {
		return "", err
	}
	if err := packUint32(payload, issueAt); err != nil {
		return "", err
	}
	if err := packUint32(payload, ttl); err != nil {
		return "", err
	}
	if err := packUint32(payload, salt); err != nil {
		return "", err
	}
	// 每个参与者 Token 只编码一个 RTC 服务。
	if err := packUint16(payload, 1); err != nil {
		return "", err
	}
	if err := packRTCService(payload, channel, uid, role, ttl); err != nil {
		return "", err
	}

	signingKey, err := deriveSigningKey(appCertificate, issueAt, salt)
	if err != nil {
		return "", err
	}
	signatureMAC := hmac.New(sha256.New, signingKey)
	if _, err = signatureMAC.Write(payload.Bytes()); err != nil {
		return "", err
	}

	tokenBody := new(bytes.Buffer)
	if err = packString(tokenBody, string(signatureMAC.Sum(nil))); err != nil {
		return "", err
	}
	if _, err = tokenBody.Write(payload.Bytes()); err != nil {
		return "", err
	}

	compressed := new(bytes.Buffer)
	compressor := zlib.NewWriter(compressed)
	if _, err = compressor.Write(tokenBody.Bytes()); err != nil {
		_ = compressor.Close()
		return "", err
	}
	if err = compressor.Close(); err != nil {
		return "", err
	}
	return accessTokenVersion + base64.StdEncoding.EncodeToString(compressed.Bytes()), nil
}

// packRTCService 序列化 RTC 服务及其角色权限。
func packRTCService(dst io.Writer, channel string, uid uint32, role Role, ttl uint32) error {
	if err := packUint16(dst, serviceTypeRTC); err != nil {
		return err
	}

	privileges := map[uint16]uint32{
		privilegeJoinChannel: ttl,
	}
	if role == RolePublisher {
		privileges[privilegePublishAudio] = ttl
		privileges[privilegePublishVideo] = ttl
		privileges[privilegePublishData] = ttl
	}
	if err := packPrivileges(dst, privileges); err != nil {
		return err
	}
	if err := packString(dst, channel); err != nil {
		return err
	}
	return packString(dst, fmt.Sprintf("%d", uid))
}

// packPrivileges 按权限编号顺序写入条目，确保 HMAC 输入不受 Go map
// 随机遍历顺序影响。
func packPrivileges(dst io.Writer, privileges map[uint16]uint32) error {
	if err := packUint16(dst, uint16(len(privileges))); err != nil {
		return err
	}
	keys := make([]int, 0, len(privileges))
	for key := range privileges {
		keys = append(keys, int(key))
	}
	sort.Ints(keys)
	for _, key := range keys {
		if err := packUint16(dst, uint16(key)); err != nil {
			return err
		}
		if err := packUint32(dst, privileges[uint16(key)]); err != nil {
			return err
		}
	}
	return nil
}

// deriveSigningKey 实现 Agora AccessToken2 规定的两阶段 HMAC 派生：
// App Certificate -> 签发时间 -> 随机盐。
func deriveSigningKey(appCertificate string, issueAt, salt uint32) ([]byte, error) {
	issueBuffer := new(bytes.Buffer)
	if err := packUint32(issueBuffer, issueAt); err != nil {
		return nil, err
	}
	issueMAC := hmac.New(sha256.New, issueBuffer.Bytes())
	if _, err := issueMAC.Write([]byte(appCertificate)); err != nil {
		return nil, err
	}

	saltBuffer := new(bytes.Buffer)
	if err := packUint32(saltBuffer, salt); err != nil {
		return nil, err
	}
	saltMAC := hmac.New(sha256.New, saltBuffer.Bytes())
	if _, err := saltMAC.Write(issueMAC.Sum(nil)); err != nil {
		return nil, err
	}
	return saltMAC.Sum(nil), nil
}

// randomSalt 返回密码学安全的非零随机盐。
func randomSalt(source io.Reader) (uint32, error) {
	var raw [4]byte
	if _, err := io.ReadFull(source, raw[:]); err != nil {
		return 0, fmt.Errorf("生成 Agora Token 随机盐失败: %w", err)
	}
	salt := binary.LittleEndian.Uint32(raw[:])
	if salt == 0 {
		salt = 1
	}
	return salt, nil
}

// isAgoraIdentifier 校验 App ID 和 App Certificate 的编码形式。
func isAgoraIdentifier(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// packUint16 按 AccessToken2 字节序写入无符号 16 位整数。
func packUint16(dst io.Writer, value uint16) error {
	return binary.Write(dst, binary.LittleEndian, value)
}

// packUint32 按 AccessToken2 字节序写入无符号 32 位整数。
func packUint32(dst io.Writer, value uint32) error {
	return binary.Write(dst, binary.LittleEndian, value)
}

// packString 按 AccessToken2 格式写入带长度前缀的字节字符串。
func packString(dst io.Writer, value string) error {
	if len(value) > int(^uint16(0)) {
		return errors.New("Agora Token 字符串字段过长")
	}
	if err := packUint16(dst, uint16(len(value))); err != nil {
		return err
	}
	_, err := io.WriteString(dst, value)
	return err
}
