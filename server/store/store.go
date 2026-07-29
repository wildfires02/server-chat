// Package store 提供注册与访问持久化数据库适配器（Database Adapter）及存储的核心抽象接口。
package store

import (
	"encoding/json"
	"errors"
	"strings"

	"chat/server/auth"
	adapter "chat/server/db"
	"chat/server/media"
	"chat/server/store/types"
	"chat/server/validate"
)

// adp 保存adp的共享实例或运行状态。
var adp adapter.Adapter

// availableAdapters 保存availableAdapters的共享实例或运行状态。
var availableAdapters = make(map[string]adapter.Adapter)

// mediaHandler 保存媒体处理器的共享实例或运行状态。
var mediaHandler media.Handler

// 唯一 ID (UID) 生成器实例（基于 Snowflake + XTEA 加密）
var uGen types.UidGenerator

// configType 保存配置Type的数据和运行状态。
type configType struct {
	// 用于 XTEA 加密的 16 字节密钥，用于初始化 types.UidGenerator
	UidKey []byte `json:"uid_key"`
	// 数据库适配器返回的最大查询结果数量限制
	MaxResults int `json:"max_results"`
	// 要使用的数据库适配器名称，应为 `Adapters` 中配置的一员
	UseAdapter string `json:"use_adapter"`
	// 各个适配器的具体配置参数
	Adapters map[string]json.RawMessage `json:"adapters"`
}

// openAdapter 根据节点 workerId 和 JSON 配置初始化并打开指定的数据库适配器。
func openAdapter(workerId int, jsonconf json.RawMessage) error {
	var config configType
	if err := json.Unmarshal(jsonconf, &config); err != nil {
		return errors.New("store: 解析配置失败: " + err.Error() + "(" + string(jsonconf) + ")")
	}

	if adp == nil {
		if len(config.UseAdapter) > 0 {
			// 显式指定了适配器名称
			if ad, ok := availableAdapters[config.UseAdapter]; ok {
				adp = ad
			} else {
				return errors.New("store: " + config.UseAdapter + " 数据库适配器在当前二进制中不可用")
			}
		} else if len(availableAdapters) == 1 {
			// 默认使用唯一可用的适配器
			for _, v := range availableAdapters {
				adp = v
			}
		} else {
			return errors.New("store: 未指定数据库适配器，请在 `im.conf` 中设置 `store_config.use_adapter`")
		}
	}

	if adp.IsOpen() {
		return errors.New("store: 数据库连接已经建立")
	}

	// 初始化 Snowflake 算法
	if workerId < 0 || workerId > 1023 {
		return errors.New("store: 无效的 worker ID")
	}

	if err := uGen.Init(uint(workerId), config.UidKey); err != nil {
		return errors.New("store: 初始化 Snowflake 算法失败: " + err.Error())
	}

	if err := adp.SetMaxResults(config.MaxResults); err != nil {
		return err
	}

	var adapterConfig json.RawMessage
	if config.Adapters != nil {
		adapterConfig = config.Adapters[adp.GetName()]
	}

	return adp.Open(adapterConfig)
}

// PersistentStorageInterface 定义与持久化存储交互的方法。
type PersistentStorageInterface interface {
	// Open 完成Open所需的内部处理。
	Open(workerId int, jsonconf json.RawMessage) error
	// Close 停止Close并释放相关资源。
	Close() error
	// IsOpen 判断是否满足Open条件。
	IsOpen() bool
	// GetAdapter 查询并返回Adapter。
	GetAdapter() adapter.Adapter
	// GetAdapterName 查询并返回Adapter名称。
	GetAdapterName() string
	// GetAdapterVersion 查询并返回Adapter版本。
	GetAdapterVersion() int
	// GetDbVersion 查询并返回数据库版本。
	GetDbVersion() int
	// InitDb 完成Init数据库所需的内部处理。
	InitDb(jsonconf json.RawMessage, reset bool) error
	// UpgradeDb 完成Upgrade数据库所需的内部处理。
	UpgradeDb(jsonconf json.RawMessage) error
	// GetUid 查询并返回用户标识。
	GetUid() types.Uid
	// GetUidString 查询并返回用户标识String。
	GetUidString() string
	// DbStats 完成数据库Stats所需的内部处理。
	DbStats() func() any
	// GetAuthNames 查询并返回认证Names。
	GetAuthNames() []string
	// GetAuthHandler 查询并返回认证处理器。
	GetAuthHandler(name string) auth.AuthHandler
	// GetLogicalAuthHandler 查询并返回Logical认证处理器。
	GetLogicalAuthHandler(name string) auth.AuthHandler
	// GetValidator 查询并返回校验器。
	GetValidator(name string) validate.Validator
	// GetMediaHandler 查询并返回媒体处理器。
	GetMediaHandler() media.Handler
	// UseMediaHandler 完成Use媒体处理器所需的内部处理。
	UseMediaHandler(name, config string) error
}

// Store 是与持久化存储交互主对象。
var Store PersistentStorageInterface

// storeObj 保存存储Obj的数据和运行状态。
type storeObj struct{}

// Open 初始化持久化系统。适配器持有一个数据库实例的连接池。
//
//	name - 配置文件中请求的适配器名称
//	jsonconf - 配置字符串
func (storeObj) Open(workerId int, jsonconf json.RawMessage) error {
	if err := openAdapter(workerId, jsonconf); err != nil {
		return err
	}

	return adp.CheckDbVersion()
}

// Close 终止与持久化存储的连接。
func (storeObj) Close() error {
	if adp.IsOpen() {
		return adp.Close()
	}

	return nil
}

// IsOpen 检查持久化存储连接是否已初始化。
func (storeObj) IsOpen() bool {
	if adp != nil {
		return adp.IsOpen()
	}

	return false
}

// GetAdapter 返回当前配置的适配器。
func (storeObj) GetAdapter() adapter.Adapter {
	return adp
}

// GetAdapterName 返回当前适配器的名称。
func (storeObj) GetAdapterName() string {
	if adp != nil {
		return adp.GetName()
	}

	return ""
}

// GetAdapterVersion 返回当前适配器的版本。
func (storeObj) GetAdapterVersion() int {
	if adp != nil {
		return adp.Version()
	}

	return -1
}

// GetDbVersion 返回底层数据库的版本。
func (storeObj) GetDbVersion() int {
	if adp != nil {
		vers, _ := adp.GetDbVersion()
		return vers
	}

	return -1
}

// InitDb 创建并配置新的数据库实例。如果 'reset' 为 true，将先尝试删除已有数据库。
// 如果 jsonconf 为 nil，则假定适配器已打开。如果非 nil 且适配器未打开，
// 将使用配置字符串先打开适配器。
func (s storeObj) InitDb(jsonconf json.RawMessage, reset bool) error {
	if !s.IsOpen() {
		if err := openAdapter(1, jsonconf); err != nil {
			return err
		}
	}
	return adp.CreateDb(reset)
}

// UpgradeDb 将数据库升级到当前适配器版本。
// 如果 jsonconf 为 nil，则假定适配器已打开。如果非 nil 且适配器未打开，
// 将使用配置字符串先打开适配器。
func (s storeObj) UpgradeDb(jsonconf json.RawMessage) error {
	if !s.IsOpen() {
		if err := openAdapter(1, jsonconf); err != nil {
			return err
		}
	}
	return adp.UpgradeDb()
}

// RegisterAdapter 使持久化适配器可用。
// 如果 Register 被调用两次或适配器为 nil，将 panic。
func RegisterAdapter(a adapter.Adapter) {
	if a == nil {
		panic("store: Register adapter is nil")
	}

	adapterName := a.GetName()
	if _, ok := availableAdapters[adapterName]; ok {
		panic("store: adapter '" + adapterName + "' is already registered")
	}
	availableAdapters[adapterName] = a
}

// GetUid 生成适合作为主键的唯一 ID。
func (storeObj) GetUid() types.Uid {
	return uGen.Get()
}

// GetUidString 生成唯一 ID 字符串。
func (storeObj) GetUidString() string {
	return uGen.GetStr()
}

// DecodeUid 接受 XTEA 加密的 Uid 并解密为 int64。
// 这是为了 SQL 兼容性。原始 int64 值由 snowflake 生成，
// 确保最高位未设置。
func DecodeUid(uid types.Uid) int64 {
	if uid.IsZero() {
		return 0
	}
	return uGen.DecodeUid(uid)
}

// EncodeUid 对 int64 值应用 XTEA 加密。是 DecodeUid 的逆操作。
func EncodeUid(id int64) types.Uid {
	if id == 0 {
		return types.ZeroUid
	}
	return uGen.EncodeInt64(id)
}

// DbStats 返回返回数据库连接统计对象的回调。
func (s storeObj) DbStats() func() any {
	if !s.IsOpen() {
		return nil
	}
	return adp.Stats
}

// 已注册的认证处理器。
var authHandlers map[string]auth.AuthHandler

// 逻辑认证处理器名称
var authHandlerNames map[string]string

// RegisterAuthScheme 注册认证方案处理器。
// 'name' 必须是硬编码名称，而非逻辑名称。
func RegisterAuthScheme(name string, handler auth.AuthHandler) {
	if name == "" {
		panic("RegisterAuthScheme: empty auth scheme name")
	}
	if handler == nil {
		panic("RegisterAuthScheme: scheme handler is nil")
	}

	name = strings.ToLower(name)
	if authHandlers == nil {
		authHandlers = make(map[string]auth.AuthHandler)
	}
	if _, dup := authHandlers[name]; dup {
		panic("RegisterAuthScheme: called twice for scheme " + name)
	}
	authHandlers[name] = handler
}

// GetAuthNames 返回所有可寻址的认证处理器名称，包括逻辑名称和硬编码名称，
// 排除已禁用的（如 "basic:"）。
func (s storeObj) GetAuthNames() []string {
	if len(authHandlers) == 0 {
		return nil
	}

	allNames := make(map[string]struct{})
	for name := range authHandlers {
		allNames[name] = struct{}{}
	}
	for name := range authHandlerNames {
		allNames[name] = struct{}{}
	}

	var names []string
	for name := range allNames {
		if s.GetLogicalAuthHandler(name) != nil {
			names = append(names, name)
		}
	}

	return names

}

// GetAuthHandler 按实际硬编码名称返回认证处理器，不考虑逻辑命名。
func (storeObj) GetAuthHandler(name string) auth.AuthHandler {
	return authHandlers[strings.ToLower(name)]
}

// GetLogicalAuthHandler 按逻辑名称返回认证处理器。如果没有该逻辑名称的处理器，
// 则尝试按硬编码名称查找。
func (storeObj) GetLogicalAuthHandler(name string) auth.AuthHandler {
	name = strings.ToLower(name)
	if len(authHandlerNames) != 0 {
		if lname, ok := authHandlerNames[name]; ok {
			return authHandlers[lname]
		}
	}
	return authHandlers[name]
}

// InitAuthLogicalNames 初始化认证映射"逻辑处理器名称":"实际处理器名称"。
// 逻辑名称不能为空，实际名称可以为空字符串。
func InitAuthLogicalNames(config json.RawMessage) error {
	if config == nil || string(config) == "null" {
		return nil
	}
	var mapping []string
	if err := json.Unmarshal(config, &mapping); err != nil {
		return errors.New("store: failed to parse logical auth names: " + err.Error() + "(" + string(config) + ")")
	}
	if len(mapping) == 0 {
		return nil
	}

	if authHandlerNames == nil {
		authHandlerNames = make(map[string]string)
	}
	for _, pair := range mapping {
		if parts := strings.Split(pair, ":"); len(parts) == 2 {
			if parts[0] == "" {
				return errors.New("store: empty logical auth name '" + pair + "'")
			}
			parts[0] = strings.ToLower(parts[0])
			if _, ok := authHandlerNames[parts[0]]; ok {
				return errors.New("store: duplicate mapping for logical auth name '" + pair + "'")
			}
			parts[1] = strings.ToLower(parts[1])
			if parts[1] != "" {
				if _, ok := authHandlers[parts[1]]; !ok {
					return errors.New("store: unknown handler for logical auth name '" + pair + "'")
				}
			}
			if parts[0] == parts[1] {
				// 跳过无用的恒等映射。
				continue
			}
			authHandlerNames[parts[0]] = parts[1]
		} else {
			return errors.New("store: invalid logical auth mapping '" + pair + "'")
		}
	}
	return nil
}

// 已注册的验证处理器。
var validators map[string]validate.Validator

// RegisterValidator 注册验证方案。
func RegisterValidator(name string, v validate.Validator) {
	name = strings.ToLower(name)
	if validators == nil {
		validators = make(map[string]validate.Validator)
	}

	if v == nil {
		panic("RegisterValidator: validator is nil")
	}
	if _, dup := validators[name]; dup {
		panic("RegisterValidator: called twice for validator " + name)
	}
	validators[name] = v
}

// GetValidator 按名称返回已注册的验证器。
func (storeObj) GetValidator(name string) validate.Validator {
	return validators[strings.ToLower(name)]
}

// 已注册的媒体/文件处理器。
var fileHandlers map[string]media.Handler

// RegisterMediaHandler 保存媒体处理器（文件上传/下载处理器）的引用。
func RegisterMediaHandler(name string, mh media.Handler) {
	if fileHandlers == nil {
		fileHandlers = make(map[string]media.Handler)
	}

	if mh == nil {
		panic("RegisterMediaHandler: handler is nil")
	}
	if _, dup := fileHandlers[name]; dup {
		panic("RegisterMediaHandler: called twice for handler " + name)
	}
	fileHandlers[name] = mh
}

// GetMediaHandler 返回默认媒体处理器。
func (storeObj) GetMediaHandler() media.Handler {
	return mediaHandler
}

// UseMediaHandler 将指定的媒体处理器设置为默认。
func (storeObj) UseMediaHandler(name, config string) error {
	mediaHandler = fileHandlers[name]
	if mediaHandler == nil {
		panic("UseMediaHandler: unknown handler '" + name + "'")
	}
	return mediaHandler.Init(config)
}

// SetTestUidGenerator 更新 Test Uid Generator。
func SetTestUidGenerator(g types.UidGenerator) {
	uGen = g
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	Store = storeObj{}
	Users = usersMapper{}
	Topics = topicsMapper{}
	Subs = subsMapper{}
	Messages = messagesMapper{}
	Devices = deviceMapper{}
	Files = fileMapper{}
	PCache = pcacheMapper{}
}
