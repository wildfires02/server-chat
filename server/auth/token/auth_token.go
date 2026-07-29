// Package token 实现基于 HMAC-SHA256 签名的安全令牌（Token）认证提供者。
package token

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"
)

// authenticator 令牌认证提供者的结构体定义。
type authenticator struct {
	// name 保存名称。
	name string
	// hmacSalt 保存hmacSalt列表。
	hmacSalt []byte
	// lifetime 保存lifetime时间。
	lifetime time.Duration
	// serialNumber 保存serialNumber。
	serialNumber int
}

// tokenLayout 定义 Token 二进制数据结构中各个字段的字节布局：
// [8字节:UID][4字节:过期时间戳][2字节:认证级别][2字节:序列号][2字节:特性位图][32字节:HMAC签名] = 总计 50 字节
type tokenLayout struct {
	// 用户 ID
	Uid uint64
	// Token 过期时间戳 (UNIX 秒)
	Expires uint32
	// 用户认证级别
	AuthLevel uint16
	// 序列号 - 用于必要时一次性作废所有发出的令牌
	SerialNumber uint16
	// 特性标志位图
	Features uint16
}

// Init 初始化 Token 认证提供者：解析 JSON 配置，设置 HMAC 密钥、序列号与生命周期。
func (ta *authenticator) Init(jsonconf json.RawMessage, name string) error {
	if name == "" {
		return errors.New("auth_token: 认证器名称不能为空")
	}

	if ta.name != "" {
		return errors.New("auth_token: 已经初始化为 " + ta.name + "; " + name)
	}

	type configType struct {
		// 用于签名 Token 的密钥
		Key []byte `json:"key"`
		// 数据库或服务器序列号，用于批量失效之前发出的所有令牌
		SerialNum int `json:"serial_num"`
		// Token 过期时间（单位: 秒）
		ExpireIn int `json:"expire_in"`
	}
	var config configType
	if err := json.Unmarshal(jsonconf, &config); err != nil {
		return errors.New("auth_token: 解析配置失败: " + err.Error() + "(" + string(jsonconf) + ")")
	}

	if len(config.Key) < sha256.Size {
		return errors.New("auth_token: 密钥缺失或长度过短")
	}
	if config.ExpireIn <= 0 {
		return errors.New("auth_token: 无效的过期时间段")
	}

	ta.name = name
	ta.hmacSalt = config.Key
	ta.lifetime = time.Duration(config.ExpireIn) * time.Second
	ta.serialNumber = config.SerialNum

	return nil
}

// IsInitialized 返回认证处理器是否已完成初始化。
func (ta *authenticator) IsInitialized() bool {
	return ta.name != ""
}

// AddRecord Token 认证不支持直接添加记录。
func (authenticator) AddRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	return nil, types.ErrUnsupported
}

// UpdateRecord Token 认证不支持直接更新记录。
func (authenticator) UpdateRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	return nil, types.ErrUnsupported
}

// Authenticate 校验传入的二进制 Token 是否合规且未过期，检查 HMAC 签名与序列号。
func (ta *authenticator) Authenticate(token []byte, remoteAddr string) (*auth.Rec, []byte, error) {
	var tl tokenLayout
	dataSize := binary.Size(&tl)
	if len(token) < dataSize+sha256.Size {
		// Token 长度过短
		return nil, nil, types.ErrMalformed
	}

	buf := bytes.NewBuffer(token)
	err := binary.Read(buf, binary.LittleEndian, &tl)
	if err != nil {
		return nil, nil, types.ErrMalformed
	}

	hbuf := new(bytes.Buffer)
	binary.Write(hbuf, binary.LittleEndian, &tl)

	// 校验 HMAC 签名
	hasher := hmac.New(sha256.New, ta.hmacSalt)
	hasher.Write(hbuf.Bytes())
	if !hmac.Equal(token[dataSize:dataSize+sha256.Size], hasher.Sum(nil)) {
		return nil, nil, types.ErrFailed
	}

	// 校验认证级别合法性
	if auth.Level(tl.AuthLevel) > auth.LevelRoot {
		return nil, nil, types.ErrMalformed
	}

	// 校验序列号（若序列号变动则令牌失效）
	if int(tl.SerialNumber) != ta.serialNumber {
		return nil, nil, types.ErrFailed
	}

	// 校验令牌是否过期
	expires := time.Unix(int64(tl.Expires), 0).UTC()
	if expires.Before(time.Now().Add(1 * time.Second)) {
		return nil, nil, types.ErrExpired
	}

	return &auth.Rec{
		Uid:       types.Uid(tl.Uid),
		AuthLevel: auth.Level(tl.AuthLevel),
		Lifetime:  auth.Duration(time.Until(expires)),
		Features:  auth.Feature(tl.Features),
		State:     types.StateUndefined}, nil, nil
}

// GenSecret 生成一个新的带有 HMAC 签名的二进制 Token。
func (ta *authenticator) GenSecret(rec *auth.Rec) ([]byte, time.Time, error) {

	if rec.Lifetime == 0 {
		rec.Lifetime = auth.Duration(ta.lifetime)
	} else if rec.Lifetime < 0 {
		return nil, time.Time{}, types.ErrExpired
	}
	expires := time.Now().Add(time.Duration(rec.Lifetime)).UTC().Round(time.Millisecond)

	tl := tokenLayout{
		Uid:          uint64(rec.Uid),
		Expires:      uint32(expires.Unix()),
		AuthLevel:    uint16(rec.AuthLevel),
		SerialNumber: uint16(ta.serialNumber),
		Features:     uint16(rec.Features),
	}
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, &tl)
	hasher := hmac.New(sha256.New, ta.hmacSalt)
	hasher.Write(buf.Bytes())
	binary.Write(buf, binary.LittleEndian, hasher.Sum(nil))

	return buf.Bytes(), expires, nil
}

// AsTag Token 认证不支持转换为搜索标签。
func (authenticator) AsTag(token string) string {
	return ""
}

// IsUnique Token 认证不支持唯一性校验。
func (authenticator) IsUnique(token []byte, remoteAddr string) (bool, error) {
	return false, types.ErrUnsupported
}

// DelRecords Token 认证删除记录（空操作）。
func (authenticator) DelRecords(uid types.Uid) error {
	return nil
}

// RestrictedTags 返回受此认证器限制的标签前缀（Token 认证无限制）。
func (authenticator) RestrictedTags() ([]string, error) {
	return nil, nil
}

// GetResetParams 返回重置密码处理器所需的参数（Token 认证无参数）。
func (authenticator) GetResetParams(uid types.Uid) (map[string]any, error) {
	return nil, nil
}

// realName 指定real名称。
const realName = "token"

// GetRealName 返回认证器的硬编码内部名称 ("token")。
func (authenticator) GetRealName() string {
	return realName
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	store.RegisterAuthScheme(realName, &authenticator{})
}
