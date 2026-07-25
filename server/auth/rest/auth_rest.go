// Package rest 提供通过远程 REST API（JSON RPC）进行外部身份认证的提供者实现。
package rest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// authenticator 映射 REST 外部认证方法的结构体。
type authenticator struct {
	// 该认证器的逻辑名称
	name string
	// 远程认证服务器的基准 URL
	serverUrl string
	// 是否允许认证服务在本地数据库中创建新账号
	allowNewAccounts bool
	// 是否使用独立 Endpoint，即发送请求时在 serverUrl 路径后追加端点名称
	useSeparateEndpoints bool
	// 受限标签前缀（命名空间）的本地缓存
	rTagNS []string
	// 校验搜索 Token 的可选正则表达式
	reToken *regexp.Regexp
}

// request 发送到远程认证服务器的请求体载荷。
type request struct {
	Endpoint   string    `json:"endpoint"`
	Name       string    `json:"name"`
	Record     *auth.Rec `json:"rec,omitempty"`
	Secret     []byte    `json:"secret,omitempty"`
	RemoteAddr string    `json:"addr,omitempty"`
}

// newAccount 创建新用户账号时的用户初始化数据结构。
type newAccount struct {
	// 默认访问权限模式
	Auth string `json:"auth,omitempty"`
	Anon string `json:"anon,omitempty"`
	// 用户的公开数据 (Public Data)
	Public any `json:"public,omitempty"`
	// 用户的可信数据 (Trusted Data)
	Trusted any `json:"trusted,omitempty"`
	// 针对每个订阅的私有数据 (Private Data)
	Private any `json:"private,omitempty"`
}

// response 远程认证服务器返回的响应体结构。
type response struct {
	// 发生错误时的错误消息
	Err string `json:"err,omitempty"`
	// 可选的认证记录
	Record *auth.Rec `json:"rec,omitempty"`
	// 可选的字节切片返回值
	ByteVal []byte `json:"byteval,omitempty"`
	// 可选的时间戳返回值
	TimeVal time.Time `json:"ts,omitempty"`
	// 布尔类型返回值
	BoolVal bool `json:"boolval,omitempty"`
	// 字符串切片返回值
	StrSliceVal []string `json:"strarr,omitempty"`
	// 创建账号的数据
	NewAcc *newAccount `json:"newacc,omitempty"`
}

// Init 初始化 REST 认证处理器配置。
func (a *authenticator) Init(jsonconf json.RawMessage, name string) error {
	if name == "" {
		return errors.New("auth_rest: 认证器名称不能为空")
	}

	if a.name != "" {
		return errors.New("auth_rest: 已经初始化为 " + a.name + "; " + name)
	}

	type configType struct {
		// ServerUrl 为调用的远程认证服务器 URL
		ServerUrl string `json:"server_url"`
		// 认证服务器是否允许创建新账号
		AllowNewAccounts bool `json:"allow_new_accounts"`
		// 是否针对不同操作使用独立的 API 端点路径
		UseSeparateEndpoints bool `json:"use_separate_endpoints"`
	}

	var config configType
	err := json.Unmarshal(jsonconf, &config)
	if err != nil {
		return errors.New("auth_rest: 解析配置失败: " + err.Error() + "(" + string(jsonconf) + ")")
	}

	serverUrl, err := url.Parse(config.ServerUrl)
	if err != nil || !serverUrl.IsAbs() {
		return errors.New("auth_rest: 无效的 server_url '" + string(jsonconf) + "'")
	}

	if !strings.HasSuffix(serverUrl.Path, "/") {
		serverUrl.Path += "/"
	}

	a.name = name
	a.serverUrl = serverUrl.String()
	a.allowNewAccounts = config.AllowNewAccounts
	a.useSeparateEndpoints = config.UseSeparateEndpoints

	return nil
}

// IsInitialized 返回认证处理器是否已完成初始化。
func (a *authenticator) IsInitialized() bool {
	return a.name != ""
}

// callEndpoint 向指定端点的远程服务器发送 HTTP POST 请求。
func (a *authenticator) callEndpoint(endpoint string, rec *auth.Rec, secret []byte, remoteAddr string) (*response, error) {
	// 序列化请求数据为 JSON
	req := &request{Endpoint: endpoint, Name: a.name, Record: rec, Secret: secret, RemoteAddr: remoteAddr}
	content, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	urlToCall := a.serverUrl
	if a.useSeparateEndpoints {
		epUrl, _ := url.Parse(a.serverUrl)
		epUrl.Path += endpoint
		urlToCall = epUrl.String()
	}

	// 使用默认 HTTP 客户端发送 POST 请求
	post, err := http.Post(urlToCall, "application/json", bytes.NewBuffer(content))
	if err != nil {
		return nil, err
	}
	defer post.Body.Close()

	// 检查 HTTP 状态码（必须为 2xx 成功状态码）
	if post.StatusCode < http.StatusOK || post.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("非预期的 HTTP 响应状态 " + post.Status)
	}

	// 读取响应体
	body, err := io.ReadAll(post.Body)
	if err != nil {
		return nil, err
	}

	// 解析响应 JSON
	var resp response
	err = json.Unmarshal(body, &resp)
	if err != nil {
		return nil, err
	}

	if resp.Err != "" {
		return nil, types.StoreError(resp.Err)
	}

	return &resp, nil
}

// AddRecord 通过远程 REST API 添加持久化认证记录。
func (a *authenticator) AddRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	resp, err := a.callEndpoint("add", rec, secret, remoteAddr)
	if err != nil {
		return nil, err
	}

	return resp.Record, nil
}

// UpdateRecord 通过远程 REST API 更新认证记录。
func (a *authenticator) UpdateRecord(rec *auth.Rec, secret []byte, remoteAddr string) (*auth.Rec, error) {
	_, err := a.callEndpoint("upd", rec, secret, remoteAddr)
	return rec, err
}

// Authenticate 通过远程服务器校验凭据以获取用户认证记录。
func (a *authenticator) Authenticate(secret []byte, remoteAddr string) (*auth.Rec, []byte, error) {
	resp, err := a.callEndpoint("auth", nil, secret, remoteAddr)
	if err != nil {
		return nil, nil, err
	}

	// 未能获取到认证记录
	if resp.Record == nil {
		logs.Warn.Println("rest_auth: 无效的响应: 缺失 Record 记录")
		return nil, nil, types.ErrInternal
	}

	// 检查远程服务器是否返回了 User ID。如果 UID 为零且允许创建新账号，则在本地数据库中创建账号
	if resp.Record.Uid.IsZero() && a.allowNewAccounts {
		if resp.NewAcc == nil {
			return nil, nil, types.ErrNotFound
		}

		// 创建账号，生成 UID 并将 UID 回传发给远程服务器绑定
		user := types.User{
			State:   resp.Record.State,
			Public:  resp.NewAcc.Public,
			Trusted: resp.NewAcc.Trusted,
			Tags:    resp.Record.Tags,
		}
		user.Access.Auth.UnmarshalText([]byte(resp.NewAcc.Auth))
		user.Access.Anon.UnmarshalText([]byte(resp.NewAcc.Anon))
		_, err = store.Users.Create(&user, resp.NewAcc.Private)
		if err != nil {
			return nil, nil, err
		}

		// 将新生成的 UID 回传通知远程认证服务器
		resp.Record.Uid = user.Uid()
		_, err = a.callEndpoint("link", resp.Record, secret, "")
		if err != nil {
			store.Users.Delete(resp.Record.Uid, true)
			return nil, nil, err
		}
	}

	return resp.Record, resp.ByteVal, nil
}

// AsTag 如果搜索 Token 符合正则要求，将其转换为带前缀的标签。
func (a *authenticator) AsTag(token string) string {
	if len(a.rTagNS) > 0 {
		if a.reToken != nil && !a.reToken.MatchString(token) {
			return ""
		}
		return a.rTagNS[0] + ":" + token
	}
	return ""
}

// IsUnique 调用远程服务器验证凭据的唯一性与策略合规性。
func (a *authenticator) IsUnique(secret []byte, remoteAddr string) (bool, error) {
	resp, err := a.callEndpoint("checkunique", nil, secret, remoteAddr)
	if err != nil {
		return false, err
	}

	return resp.BoolVal, err
}

// GenSecret 调用远程服务器生成新的密钥凭据。
func (a *authenticator) GenSecret(rec *auth.Rec) ([]byte, time.Time, error) {
	resp, err := a.callEndpoint("gen", rec, nil, "")
	if err != nil {
		return nil, time.Time{}, err
	}

	return resp.ByteVal, resp.TimeVal, err
}

// DelRecords 调用远程服务器删除指定用户的所有认证记录。
func (a *authenticator) DelRecords(uid types.Uid) error {
	logs.Info.Println("DelRecords, initialized=", a.name != "")
	_, err := a.callEndpoint("del", &auth.Rec{Uid: uid}, nil, "")
	return err
}

// RestrictedTags 返回受远程认证服务器限制的标签命名空间（前缀）。
func (a *authenticator) RestrictedTags() ([]string, error) {
	if a.rTagNS != nil {
		// 使用已缓存的前缀列表（返回副本以防止并发修改）
		ns := make([]string, len(a.rTagNS))
		copy(ns, a.rTagNS)
		return ns, nil
	}

	// 首次调用，向远程服务器请求前缀列表
	resp, err := a.callEndpoint("rtagns", nil, nil, "")
	if err != nil {
		return nil, err
	}

	// 保存结果到缓存
	a.rTagNS = resp.StrSliceVal
	if len(resp.ByteVal) > 0 {
		a.reToken, err = regexp.Compile(string(resp.ByteVal))
		if err != nil {
			logs.Warn.Println("rest_auth: 无效的 Token 正则表达式", string(resp.ByteVal))
		}
	}
	return resp.StrSliceVal, nil
}

// GetResetParams 返回传递给重置密码处理器的参数。
func (authenticator) GetResetParams(uid types.Uid) (map[string]any, error) {
	return nil, nil
}

const realName = "rest"

// GetRealName 返回认证器的硬编码内部名称 ("rest")。
func (authenticator) GetRealName() string {
	return realName
}

func init() {
	store.RegisterAuthScheme(realName, &authenticator{})
}
