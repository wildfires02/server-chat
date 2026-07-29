// Package basic 提供基于“用户名+密码”组合的传统身份认证提供者实现。
package basic

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"

	"golang.org/x/crypto/bcrypt"
)

// 定义用户名和密码的默认长度约束限制
const (
	defaultMinLoginLength = 2
	defaultMaxLoginLength = 32

	defaultMinPasswordLength = 3
)

// loginPattern 定义有效的用户名正则匹配规则：
// 以 Unicode 字母或数字开头和结尾，中间可包含 Unicode 字母、数字、点号 (.) 和下划线 (_)。
var loginPattern = regexp.MustCompile(`^[\pL\pN][_.\pL\pN]*[\pL\pN]+$`)

// authenticator 基本账号密码认证提供者实现结构体。
type authenticator struct {
	// name 保存名称。
	name string
	// addToTags 保存addToTags。
	addToTags bool

	// minPasswordLength 保存min密码Length。
	minPasswordLength int
	// minLoginLength 保存min登录Length。
	minLoginLength int
}

// checkLoginPolicy 检查用户名是否符合策略规则（长度限制与正则匹配）。
func (a *authenticator) checkLoginPolicy(uname string) error {
	rlogin := []rune(uname)
	if len(rlogin) < a.minLoginLength || len(rlogin) > defaultMaxLoginLength || !loginPattern.MatchString(uname) {
		return types.ErrPolicy
	}

	return nil
}

// checkPasswordPolicy 检查密码是否符合策略规则（最小长度约束）。
func (a *authenticator) checkPasswordPolicy(password string) error {
	if len([]rune(password)) < a.minPasswordLength {
		return types.ErrPolicy
	}

	return nil
}

// parseSecret 解析客户端传入的凭据字节切片 ("login:password")。
func parseSecret(bsecret []byte) (uname, password string, err error) {
	secret := string(bsecret)

	splitAt := strings.Index(secret, ":")
	if splitAt < 0 {
		err = types.ErrMalformed
		return
	}

	uname = strings.ToLower(secret[:splitAt])
	password = secret[splitAt+1:]
	return
}

// Init 初始化 basic 认证提供者配置。
func (a *authenticator) Init(jsonconf json.RawMessage, name string) error {
	if name == "" {
		return errors.New("auth_basic: 认证器名称不能为空")
	}

	if a.name != "" {
		return errors.New("auth_basic: 已经初始化为 " + a.name + "; " + name)
	}

	type configType struct {
		// AddToTags 表示是否将用户名自动添加为可被搜索的标签
		AddToTags         bool `json:"add_to_tags"`
		MinPasswordLength int  `json:"min_password_length"`
		MinLoginLength    int  `json:"min_login_length"`
	}

	var config configType
	if err := json.Unmarshal(jsonconf, &config); err != nil {
		return errors.New("auth_basic: 解析配置失败: " + err.Error() + "(" + string(jsonconf) + ")")
	}
	a.name = name
	a.addToTags = config.AddToTags
	a.minPasswordLength = config.MinPasswordLength
	if a.minPasswordLength <= 0 {
		a.minPasswordLength = defaultMinPasswordLength
	}
	a.minLoginLength = config.MinLoginLength
	if a.minLoginLength > defaultMaxLoginLength {
		return errors.New("auth_basic: min_login_length 超过最大限制")
	}
	if a.minLoginLength <= 0 {
		a.minLoginLength = defaultMinLoginLength
	}

	return nil
}

// IsInitialized 返回认证处理器是否已完成初始化。
func (a *authenticator) IsInitialized() bool {
	return a.name != ""
}

// AddRecord 向数据库添加新的账号密码认证记录（密码经 bcrypt 哈希加密后存储）。
func (a *authenticator) AddRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	uname, password, err := parseSecret(secret)
	if err != nil {
		return nil, err
	}

	if err = a.checkLoginPolicy(uname); err != nil {
		return nil, err
	}

	if err = a.checkPasswordPolicy(password); err != nil {
		return nil, err
	}

	passhash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	var expires time.Time
	if rec.Lifetime > 0 {
		expires = time.Now().Add(time.Duration(rec.Lifetime)).UTC().Round(time.Millisecond)
	}

	authLevel := rec.AuthLevel
	if authLevel == auth.LevelNone {
		authLevel = auth.LevelAuth
	}

	err = store.Users.AddAuthRecord(rec.Uid, authLevel, a.name, uname, passhash, expires)
	if err != nil {
		return nil, err
	}

	rec.AuthLevel = authLevel
	if a.addToTags {
		rec.Tags = append(rec.Tags, a.name+":"+uname)
	}
	return rec, nil
}

// UpdateRecord 更新现有 Basic 认证记录的用户名或密码。
func (a *authenticator) UpdateRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	uname, password, err := parseSecret(secret)
	if err != nil {
		return nil, err
	}

	login, authLevel, _, _, err := store.Users.GetAuthRecord(rec.Uid, a.name)
	if err != nil {
		return nil, err
	}
	// 用户不存在该认证记录
	if login == "" {
		return nil, types.ErrNotFound
	}

	if uname == "" || uname == login {
		// 用户仅修改密码
		uname = login
	} else if err = a.checkLoginPolicy(uname); err != nil {
		return nil, err
	} else if uid, _, _, _, err := store.Users.GetAuthUniqueRecord(a.name, uname); err != nil {
		return nil, err
	} else if !uid.IsZero() {
		// 新用户名已被占用，返回重复错误
		return nil, types.ErrDuplicate
	}

	if err = a.checkPasswordPolicy(password); err != nil {
		return nil, err
	}

	passhash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, types.ErrInternal
	}
	var expires time.Time
	if rec.Lifetime > 0 {
		expires = types.TimeNow().Add(time.Duration(rec.Lifetime))
	}
	err = store.Users.UpdateAuthRecord(rec.Uid, authLevel, a.name, uname, passhash, expires)
	if err != nil {
		return nil, err
	}

	// 移除旧标签
	oldTag := a.name + ":" + login
	for i, tag := range rec.Tags {
		if tag == oldTag {
			rec.Tags[i] = rec.Tags[len(rec.Tags)-1]
			rec.Tags = rec.Tags[:len(rec.Tags)-1]

			break
		}
	}
	// 添加新标签
	rec.Tags = append(rec.Tags, a.name+":"+uname)

	return rec, nil
}

// Authenticate 校验用户名和密码是否正确。
func (a *authenticator) Authenticate(secret []byte, remoteAddr string) (*auth.Rec, []byte, error) {
	uname, password, err := parseSecret(secret)
	if err != nil {
		return nil, nil, err
	}

	uid, authLvl, passhash, expires, err := store.Users.GetAuthUniqueRecord(a.name, uname)
	if err != nil {
		return nil, nil, err
	}
	if uid.IsZero() {
		// 无效的用户名
		return nil, nil, types.ErrFailed
	}
	if !expires.IsZero() && expires.Before(time.Now()) {
		// 认证记录已过期
		return nil, nil, types.ErrExpired
	}

	err = bcrypt.CompareHashAndPassword(passhash, []byte(password))
	if err != nil {
		// 密码错误
		return nil, nil, types.ErrFailed
	}

	var lifetime time.Duration
	if !expires.IsZero() {
		lifetime = time.Until(expires)
	}
	return &auth.Rec{
		Uid:       uid,
		AuthLevel: authLvl,
		Lifetime:  auth.Duration(lifetime),
		Features:  0,
		State:     types.StateUndefined}, nil, nil
}

// AsTag 如果配置了 addToTags 且搜索词符合规范，将其转换为带前缀的标签。
func (a *authenticator) AsTag(token string) string {
	if !a.addToTags {
		return ""
	}

	if err := a.checkLoginPolicy(token); err != nil {
		return ""
	}

	return a.name + ":" + token
}

// IsUnique 检查用户名在数据库中的唯一性及合规性。
func (a *authenticator) IsUnique(secret []byte, remoteAddr string) (bool, error) {
	uname, _, err := parseSecret(secret)
	if err != nil {
		return false, err
	}

	if err := a.checkLoginPolicy(uname); err != nil {
		return false, err
	}

	uid, _, _, _, err := store.Users.GetAuthUniqueRecord(a.name, uname)
	if err != nil {
		return false, err
	}

	if uid.IsZero() {
		return true, nil
	}
	return false, types.ErrDuplicate
}

// GenSecret Basic 认证不支持直接生成密钥。
func (authenticator) GenSecret(rec *auth.Rec) ([]byte, time.Time, error) {
	return nil, time.Time{}, types.ErrUnsupported
}

// DelRecords 删除指定用户的所有 Basic 认证记录。
func (a *authenticator) DelRecords(uid types.Uid) error {
	return store.Users.DelAuthRecords(uid, a.name)
}

// RestrictedTags 返回受此认证器限制的标签命名空间（前缀）。
func (a *authenticator) RestrictedTags() ([]string, error) {
	var prefix []string
	if a.addToTags {
		prefix = []string{a.name}
	}
	return prefix, nil
}

// GetResetParams 返回重置密码处理器所需的参数（如用户的登录名）。
func (a *authenticator) GetResetParams(uid types.Uid) (map[string]any, error) {
	login, _, _, _, err := store.Users.GetAuthRecord(uid, a.name)
	if err != nil {
		return nil, err
	}
	// 用户在此认证方案下不存在记录
	if login == "" {
		return nil, types.ErrNotFound
	}

	params := make(map[string]any)
	params["login"] = login
	return params, nil
}

// realName 指定real名称。
const realName = "basic"

// GetRealName 返回认证器的硬编码内部名称 ("basic")。
func (authenticator) GetRealName() string {
	return realName
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	store.RegisterAuthScheme(realName, &authenticator{})
}
