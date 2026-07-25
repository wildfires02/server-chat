// Package auth 提供实现身份认证提供者（Authenticator）所需的接口和类型定义。
package auth

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"chat/server/store/types"
)

// Level 表示身份认证级别类型。
type Level int

// 身份认证级别常量定义
const (
	// LevelNone 未定义 / 未通过认证
	LevelNone Level = iota * 10
	// LevelAnon 匿名用户 / 轻量级认证
	LevelAnon
	// LevelAuth 完全认证用户
	LevelAuth
	// LevelRoot 超级管理员（目前未使用）
	LevelRoot
)

// String 实现 Stringer 接口，将数字认证级别转换为可读字符串。
func (a Level) String() string {
	s, err := a.MarshalText()
	if err != nil {
		return "unkn"
	}
	return string(s)
}

// ParseAuthLevel 从字符串解析身份认证级别。
func ParseAuthLevel(name string) Level {
	switch name {
	case "anon", "ANON":
		return LevelAnon
	case "auth", "AUTH":
		return LevelAuth
	case "root", "ROOT":
		return LevelRoot
	default:
		return LevelNone
	}
}

// MarshalText 将 Level 转换为字节切片文本表示。
func (a Level) MarshalText() ([]byte, error) {
	switch a {
	case LevelNone:
		return []byte(""), nil
	case LevelAnon:
		return []byte("anon"), nil
	case LevelAuth:
		return []byte("auth"), nil
	case LevelRoot:
		return []byte("root"), nil
	default:
		return nil, errors.New("auth.Level: 无效的认证级别数值")
	}
}

// UnmarshalText 从文本字节切片解析 Level。
func (a *Level) UnmarshalText(b []byte) error {
	switch string(b) {
	case "":
		*a = LevelNone
		return nil
	case "anon", "ANON":
		*a = LevelAnon
		return nil
	case "auth", "AUTH":
		*a = LevelAuth
		return nil
	case "root", "ROOT":
		*a = LevelRoot
		return nil
	default:
		return errors.New("auth.Level: 无法识别的认证级别")
	}
}

// MarshalJSON 将 Level 转换为带双引号的 JSON 字符串。
func (a Level) MarshalJSON() ([]byte, error) {
	res, err := a.MarshalText()
	if err != nil {
		return nil, err
	}

	return append(append([]byte{'"'}, res...), '"'), nil
}

// UnmarshalJSON 从带双引号的 JSON 字符串中解析 Level。
func (a *Level) UnmarshalJSON(b []byte) error {
	if b[0] != '"' || b[len(b)-1] != '"' {
		return errors.New("语法错误")
	}

	return a.UnmarshalText(b[1 : len(b)-1])
}

// Feature 表示认证特性的位图标识（如已验证/未验证等）。
type Feature uint16

const (
	// FeatureValidated 若用户凭据已验证（标志 'V'），则设置该位。
	FeatureValidated Feature = 1 << iota
	// FeatureNoLogin 若令牌不能用于持久化登录会话（标志 'L'），则设置该位。
	FeatureNoLogin
)

// MarshalText 将 Feature 转换为 ASCII 字节切片。
func (f Feature) MarshalText() ([]byte, error) {
	res := []byte{}
	for i, chr := range []byte{'V', 'L'} {
		if (f & (1 << uint(i))) != 0 {
			res = append(res, chr)
		}
	}
	return res, nil
}

// UnmarshalText 将 Feature 文本转换为位图标志。
func (f *Feature) UnmarshalText(b []byte) error {
	var f0 int
	var err error
	if len(b) > 0 {
		if b[0] >= '0' && b[0] <= '9' {
			f0, err = strconv.Atoi(string(b))
		} else {
		Loop:
			for i := range b {
				switch b[i] {
				case 'V', 'v':
					f0 |= int(FeatureValidated)
				case 'L', 'l':
					f0 |= int(FeatureNoLogin)
				default:
					err = errors.New("Feature: 无效的字符 '" + string(b[i]) + "'")
					break Loop
				}
			}
		}
	}

	*f = Feature(f0)

	return err
}

// String 将 Feature 转换为字符串表示。
func (f Feature) String() string {
	res, err := f.MarshalText()
	if err != nil {
		return ""
	}
	return string(res)
}

// MarshalJSON 将 Feature 转换为带双引号的 JSON 字符串。
func (f Feature) MarshalJSON() ([]byte, error) {
	res, err := f.MarshalText()
	if err != nil {
		return nil, err
	}

	return append(append([]byte{'"'}, res...), '"'), nil
}

// UnmarshalJSON 从带双引号的 JSON 字符串或整数中解析 Feature。
func (f *Feature) UnmarshalJSON(b []byte) error {
	if b[0] == '"' && b[len(b)-1] == '"' {
		return f.UnmarshalText(b[1 : len(b)-1])
	}
	return f.UnmarshalText(b)
}

// Duration 包装 time.Duration，以便支持从 JSON 中安全反序列化。
type Duration time.Duration

// UnmarshalJSON 处理 JSON 中的时长，支持如 "5000s" 字符串或纯秒数数值。
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		*d = Duration(time.Duration(value) * time.Second)
		return nil
	case string:
		d0, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		*d = Duration(d0)
		return nil
	default:
		return errors.New("无效的时长格式")
	}
}

// Rec 身份认证记录结构体。
type Rec struct {
	// 用户 ID
	Uid types.Uid `json:"uid,omitempty"`
	// 认证级别
	AuthLevel Level `json:"authlvl,omitempty"`
	// 该认证记录的有效生命周期
	Lifetime Duration `json:"lifetime,omitempty"`
	// 认证特性位图标识（例如 已验证/未验证 等）
	Features Feature `json:"features,omitempty"`
	// 由此认证记录生成的标签
	Tags []string `json:"tags,omitempty"`
	// 认证器读取或接收到的用户账号状态
	State types.ObjState
	// 与此记录关联的凭据字符串 ("method:value")
	Credential string `json:"cred,omitempty"`

	// 认证器可请求服务器创建新账号，以下参数用于创建账号
	DefAcs  *types.DefaultAccess `json:"defacs,omitempty"`
	Public  any                  `json:"public,omitempty"`
	Private any                  `json:"private,omitempty"`
}

// AuthHandler 所有身份认证提供者（Authenticator）必须实现的接口。
type AuthHandler interface {
	// Init 初始化认证处理器，接收 JSON 配置字符串和逻辑名称作为参数。
	Init(jsonconf json.RawMessage, name string) error

	// IsInitialized 返回认证处理器是否已完成初始化。
	IsInitialized() bool

	// AddRecord 向数据库添加持久化的认证记录。
	// 返回: 更新后的认证记录, 错误信息
	AddRecord(rec *Rec, secret []byte, remoteAddr string) (*Rec, error)

	// UpdateRecord 使用新凭据更新现有的认证记录。
	// 返回: 更新后的认证记录, 错误信息
	UpdateRecord(rec *Rec, secret []byte, remoteAddr string) (*Rec, error)

	// Authenticate 给定用户提供的认证密钥（如 "login:password"）：
	// 返回用户记录（ID、密钥过期时间等）、或发起 Challenge 以进入下一步认证、或返回错误码。
	// remoteAddr（即客户端 IP 地址）可用于自定义认证器的额外校验。
	// 返回: 用户认证记录, challenge 挑战数据, 错误信息
	Authenticate(secret []byte, remoteAddr string) (*Rec, []byte, error)

	// AsTag 将搜索 Token 转换为带前缀的 Tag，若无法转换则返回空字符串。
	AsTag(token string) string

	// IsUnique 验证给定的密钥在当前认证方案中是否唯一（例如登录名是否唯一）。
	// 同时也可能校验策略合规性（如长度是否合规等）。
	IsUnique(secret []byte, remoteAddr string) (bool, error)

	// GenSecret 在适当时生成一个新的密钥。
	GenSecret(rec *Rec) ([]byte, time.Time, error)

	// DelRecords 删除（或禁用）给定用户的所有认证记录。
	DelRecords(uid types.Uid) error

	// RestrictedTags 返回受此认证器限制/保留的标签命名空间（前缀）。
	RestrictedTags() ([]string, error)

	// GetResetParams 返回传递给重置密码处理器的参数映射。
	// 返回: 参数 Map
	GetResetParams(uid types.Uid) (map[string]any, error)

	// GetRealName 返回该认证器的硬编码内部名称。
	GetRealName() string
}
