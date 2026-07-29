// Package code 实现基于短数字验证码的临时非持久化认证方案。
package code

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// authenticator 验证码认证提供者结构体。
type authenticator struct {
	// name 保存名称。
	name string
	// codeLength 保存codeLength。
	codeLength int
	// maxCodeValue 保存maxCode值。
	maxCodeValue *big.Int
	// lifetime 保存lifetime时间。
	lifetime time.Duration
	// maxRetries 保存maxRetries。
	maxRetries int
}

// Init 初始化验证码认证提供者：解析 JSON 配置并设置内部状态。
func (ca *authenticator) Init(jsonconf json.RawMessage, name string) error {
	if name == "" {
		return errors.New("auth_code: 认证器名称不能为空")
	}

	if ca.name != "" {
		return errors.New("auth_code: 已经初始化为 " + ca.name + "; " + name)
	}

	type configType struct {
		// 验证码的长度
		CodeLength int `json:"code_length"`
		// 验证码过期时间（单位: 秒）
		ExpireIn int `json:"expire_in"`
		// 每个验证码允许的最大尝试重试次数
		MaxRetries int `json:"max_retries"`
	}
	var config configType
	if err := json.Unmarshal(jsonconf, &config); err != nil {
		return errors.New("auth_code: 解析配置失败: " + err.Error() + "(" + string(jsonconf) + ")")
	}

	if config.ExpireIn <= 0 {
		return errors.New("auth_code: 无效的过期时间段")
	}

	if config.CodeLength < 4 {
		return errors.New("auth_code: 验证码长度不能小于 4")
	}

	if config.MaxRetries < 1 {
		return errors.New("auth_code: 重试次数设置无效")
	}

	ca.name = name
	ca.codeLength = config.CodeLength
	ca.maxCodeValue = big.NewInt(0).Exp(big.NewInt(10), big.NewInt(int64(ca.codeLength)), nil)
	ca.lifetime = time.Duration(config.ExpireIn) * time.Second
	ca.maxRetries = config.MaxRetries

	return nil
}

// IsInitialized 返回认证处理器是否已完成初始化。
func (ca *authenticator) IsInitialized() bool {
	return ca.name != ""
}

// AddRecord 验证码认证不支持直接添加持久化记录。
func (authenticator) AddRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	return nil, types.ErrUnsupported
}

// UpdateRecord 验证码认证不支持直接更新记录。
func (authenticator) UpdateRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	return nil, types.ErrUnsupported
}

// Authenticate 校验输入的短数字验证码是否有效。
// 凭据密钥结构格式为 <code>:<cred_method>:<cred_value>，例如 "123456:email:alice@example.com"。
func (ca *authenticator) Authenticate(secret []byte, remoteAddr string) (*auth.Rec, []byte, error) {
	parts := strings.SplitN(string(secret), ":", 2)
	if len(parts) != 2 {
		return nil, nil, types.ErrMalformed
	}

	code, cred := parts[0], parts[1]
	key := sanitizeKey(realName + "_" + cred)

	value, err := store.PCache.Get(key)
	if err != nil {
		if err == types.ErrNotFound {
			err = types.ErrFailed
		}
		return nil, nil, err
	}

	// 缓存中的数据格式: code:count:uid
	parts = strings.Split(value, ":")
	if len(parts) != 3 {
		return nil, nil, types.ErrInternal
	}

	count, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, nil, types.ErrInternal
	}

	if count >= ca.maxRetries {
		return nil, nil, types.ErrFailed
	}

	if parts[0] != code {
		// 尝试次数加 1，若更新失败则忽略错误
		store.PCache.Upsert(key, parts[0]+":"+strconv.Itoa(count+1)+":"+parts[2], false)
		return nil, nil, types.ErrFailed
	}

	// 校验成功，删除已无用的缓存记录
	if err = store.PCache.Delete(key); err != nil {
		logs.Warn.Println("code_auth: 删除缓存 key 失败", key, err)
	}

	return &auth.Rec{
		Uid:        types.ParseUid(parts[2]),
		AuthLevel:  auth.LevelNone,
		Lifetime:   auth.Duration(ca.lifetime),
		Features:   auth.FeatureNoLogin,
		State:      types.StateUndefined,
		Credential: cred}, nil, nil
}

// GenSecret 随机生成一个新的数字验证码并存入缓存。
func (ca *authenticator) GenSecret(rec *auth.Rec) ([]byte, time.Time, error) {
	// 清理过期的验证码缓存记录
	store.PCache.Expire(realName+"_", time.Now().UTC().Add(-ca.lifetime))

	// 生成安全加密随机数验证码
	code, err := rand.Int(rand.Reader, ca.maxCodeValue)
	if err != nil {
		return nil, time.Time{}, types.ErrInternal
	}

	// 格式化为固定长度的数字字符串
	resp := strconv.FormatInt(code.Int64(), 10)
	resp = strings.Repeat("0", ca.codeLength-len(resp)) + resp

	if rec.Lifetime == 0 {
		rec.Lifetime = auth.Duration(ca.lifetime)
	} else if rec.Lifetime < 0 {
		return nil, time.Time{}, types.ErrExpired
	}

	// 保存 "code:counter:uid" 到缓存中，缓存 Key 格式为 code_<credential>
	if err = store.PCache.Upsert(sanitizeKey(realName+"_"+rec.Credential), resp+":0:"+rec.Uid.String(), true); err != nil {
		return nil, time.Time{}, err
	}

	expires := time.Now().Add(time.Duration(rec.Lifetime)).UTC().Round(time.Millisecond)

	return []byte(resp), expires, nil
}

// AsTag 验证码认证不支持转换为搜索标签。
func (authenticator) AsTag(token string) string {
	return ""
}

// IsUnique 验证码认证不支持唯一性校验。
func (authenticator) IsUnique(secret []byte, remoteAddr string) (bool, error) {
	return false, types.ErrUnsupported
}

// DelRecords 验证码认证删除记录（空操作）。
func (authenticator) DelRecords(uid types.Uid) error {
	return nil
}

// RestrictedTags 返回受此认证器限制的标签前缀（验证码认证无限制）。
func (authenticator) RestrictedTags() ([]string, error) {
	return nil, nil
}

// GetResetParams 返回重置密码处理器所需的参数（验证码认证无参数）。
func (authenticator) GetResetParams(uid types.Uid) (map[string]any, error) {
	return nil, nil
}

// sanitizeKey 替换所有的 '%' 为 '/'，防止在 SQL LIKE 查询中出现通配符解析错误。
func sanitizeKey(key string) string {
	return strings.ReplaceAll(key, "%", "/")
}

// realName 指定real名称。
const realName = "code"

// GetRealName 返回认证器的硬编码内部名称 ("code")。
func (authenticator) GetRealName() string {
	return realName
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	store.RegisterAuthScheme(realName, &authenticator{})
}
