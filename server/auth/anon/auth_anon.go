// Package anon 提供无需凭据的身份认证。最适用于客户支持场景。
// 匿名认证仅在账号创建时使用。
package anon

import (
	"encoding/json"
	"errors"
	"time"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"
)

// authenticator 是匿名认证器的单例实例。
type authenticator struct {
	name string
}

// Init 是无操作，始终返回成功。
func (a *authenticator) Init(_ json.RawMessage, name string) error {
	if name == "" {
		return errors.New("auth_anonymous: authenticator name cannot be blank")
	}

	if a.name != "" {
		return errors.New("auth_anonymous: already initialized as " + a.name + "; " + name)
	}

	a.name = name
	return nil
}

// IsInitialized 返回处理器是否已初始化。
func (a *authenticator) IsInitialized() bool {
	return a.name != ""
}

// AddRecord 检查 authLevel 并分配默认的 LevelAnon。否则仅报告成功。
func (authenticator) AddRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	if rec.AuthLevel == auth.LevelNone {
		rec.AuthLevel = auth.LevelAnon
	}
	rec.State = types.StateOK
	return rec, nil
}

// UpdateRecord 是无操作。仅报告成功。
func (authenticator) UpdateRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	return rec, nil
}

// Authenticate 不受支持。此认证器仅在账号创建时使用。
func (authenticator) Authenticate(secret []byte, remoteAddr string) (*auth.Rec, []byte, error) {
	return nil, nil, types.ErrUnsupported
}

// AsTag 不受支持，将返回空字符串。
func (authenticator) AsTag(token string) string {
	return ""
}

// IsUnique 作为无操作。匿名登录不使用密钥，任何密钥都可以。
func (authenticator) IsUnique(secret []byte, remoteAddr string) (bool, error) {
	return true, nil
}

// GenSecret 始终失败。
func (authenticator) GenSecret(rec *auth.Rec) ([]byte, time.Time, error) {
	return nil, time.Time{}, types.ErrUnsupported
}

// DelRecords 是无操作，始终成功。
func (authenticator) DelRecords(uid types.Uid) error {
	return nil
}

// RestrictedTags 返回此认证器限制的标签命名空间（匿名认证无限制）。
func (authenticator) RestrictedTags() ([]string, error) {
	return nil, nil
}

// GetResetParams 返回传递给密码重置处理器的认证器参数
//（匿名认证无参数）。
func (authenticator) GetResetParams(uid types.Uid) (map[string]any, error) {
	return nil, nil
}

const realName = "anonymous"

// GetRealName 返回认证器的硬编码名称。
func (authenticator) GetRealName() string {
	return realName
}

func init() {
	store.RegisterAuthScheme(realName, &authenticator{})
}
