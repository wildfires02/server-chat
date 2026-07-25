// Package adapter 定义了数据库适配器需要实现的接口。
package adapter

import (
	"encoding/json"
	"time"

	"chat/server/auth"
	t "chat/server/store/types"
)

// Adapter 是数据库适配器必须实现的接口。
// 当前架构支持每种数据库类型一个连接。
type Adapter interface {
	// 通用操作
	
	// Open 打开并配置适配器。
	Open(config json.RawMessage) error
	// Close 关闭适配器。
	Close() error
	// IsOpen 检查适配器是否已准备好使用。
	IsOpen() bool
	// GetDbVersion 返回当前数据库版本。
	GetDbVersion() (int, error)
	// CheckDbVersion 检查实际数据库版本是否与适配器版本匹配。
	CheckDbVersion() error
	// GetName 返回适配器名称。
	GetName() string
	// SetMaxResults 配置单次数据库调用可返回的最大结果数。
	SetMaxResults(val int) error
	// CreateDb 创建数据库，可选先删除已有数据库。
	CreateDb(reset bool) error
	// UpgradeDb 将数据库升级到当前适配器版本。
	UpgradeDb() error
	// Version 返回适配器版本。
	Version() int
	// Stats 返回数据库连接统计对象。
	Stats() any

	// 用户管理

	// UserCreate 创建用户记录。
	UserCreate(user *t.User) error
	// UserGet 根据用户 ID 返回用户记录。
	UserGet(uid t.Uid) (*t.User, error)
	// UserGetAll 根据用户 ID 列表返回用户记录。
	UserGetAll(ids ...t.Uid) ([]t.User, error)
	// UserDelete 删除用户记录。
	UserDelete(uid t.Uid, hard bool) error
	// UserUpdate 更新用户记录。
	UserUpdate(uid t.Uid, update map[string]any) error
	// UserUpdateTags 添加、删除或重置用户的标签。
	UserUpdateTags(uid t.Uid, add, remove, reset []string) ([]string, error)
	// UserGetByCred 根据已验证的凭据返回用户 ID。
	UserGetByCred(method, value string) (t.Uid, error)
	// UserUnreadCount 返回所有具有 R 权限的 Topic 中未读消息的总数。
	// 如果读取失败，仍会返回原始用户 ID 的计数，但未读计数未定义且错误非空。
	UserUnreadCount(ids ...t.Uid) (map[t.Uid]int, error)
	// UserGetUnvalidated 返回从未登录、没有已验证凭据且自 'lastUpdatedBefore' 以来未更新过的用户 ID 列表，
	// 数量不超过 'limit'。
	UserGetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]t.Uid, error)

	// 凭据管理

	// CredUpsert 添加或更新凭据记录。返回 true 表示插入，false 表示更新。
	CredUpsert(cred *t.Credential) (bool, error)
	// CredGetActive 返回指定方法当前活跃的凭据记录。
	CredGetActive(uid t.Uid, method string) (*t.Credential, error)
	// CredGetAll 返回指定用户和方法的凭据记录，可选仅已验证或全部。
	CredGetAll(uid t.Uid, method string, validatedOnly bool) ([]t.Credential, error)
	// CredDel 删除指定方法/值的凭据。若方法为空，则删除用户的所有凭据。
	CredDel(uid t.Uid, method, value string) error
	// CredConfirm 将指定凭据标记为已验证。
	CredConfirm(uid t.Uid, method string) error
	// CredFail 增加指定凭据验证失败次数。
	CredFail(uid t.Uid, method string) error

	// 基础认证方案的认证管理

	// AuthGetUniqueRecord 根据唯一值（如登录名）返回认证记录。
	AuthGetUniqueRecord(unique string) (t.Uid, auth.Level, []byte, time.Time, error)
	// AuthGetRecord 根据用户 ID 和方法返回认证记录。
	AuthGetRecord(user t.Uid, scheme string) (string, auth.Level, []byte, time.Time, error)
	// AuthAddRecord 创建新的认证记录。
	AuthAddRecord(user t.Uid, scheme, unique string, authLvl auth.Level, secret []byte, expires time.Time) error
	// AuthDelScheme 删除用户的指定认证方案。
	AuthDelScheme(user t.Uid, scheme string) error
	// AuthDelAllRecords 删除指定用户的所有认证记录。
	AuthDelAllRecords(uid t.Uid) (int, error)
	// AuthUpdRecord 修改认证记录。仅更新非默认/非零值字段。
	AuthUpdRecord(user t.Uid, scheme, unique string, authLvl auth.Level, secret []byte, expires time.Time) error

	// Topic 管理

	// TopicCreate 创建 Topic。
	TopicCreate(topic *t.Topic) error
	// TopicCreateP2P 创建 P2P Topic。
	TopicCreateP2P(initiator, invited *t.Subscription) error
	// TopicGet 按名称加载单个 Topic（如果存在）。若 Topic 不存在则返回 (nil, nil)。
	TopicGet(topic string) (*t.Topic, error)
	// TopicsForUser 加载用户的订阅，同时读取 Public 值。
	// 当 'opts.IfModifiedSince' 查询非空时，返回 UpdatedAt > opts.IfModifiedSince 的订阅，
	// 其中 UpdatedAt 可以是订阅、Topic 或用户的更新时间戳。
	// 这是为了支持订阅分页：从最早更新到最近更新逐页获取订阅：
	// 1. 客户端已持有最新更新时间戳 X 的订阅。
	// 2. 客户端请求自 X 以来更新的 N 条订阅。服务器返回 N 条更新在 X 和 Y 之间的订阅。
	// 3. 客户端以 X := Y 回到步骤 1。
	TopicsForUser(uid t.Uid, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error)
	// UsersForTopic 加载指定 Topic 的用户订阅，同时读取 Public。
	UsersForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error)
	// OwnTopics 加载用户作为所有者的 Topic 名称列表。
	OwnTopics(uid t.Uid) ([]string, error)
	// ChannelsForUser 加载用户作为 Channel 读者且启用了通知 (P) 的 Topic 名称列表。
	ChannelsForUser(uid t.Uid) ([]string, error)
	// TopicShare 创建 Topic 订阅。
	TopicShare(topic string, subs []*t.Subscription) error
	// TopicDelete 删除 Topic、订阅和消息。
	TopicDelete(topic string, isChan, hard bool) error
	// TopicUpdateOnMessage 递增 Topic 或用户的 SeqId 值并更新 TouchedAt 时间戳。
	TopicUpdateOnMessage(topic string, msg *t.Message) error
	// TopicUpdateSubCnt 刷新反规范化的 Topic 订阅者计数。
	TopicUpdateSubCnt(topic string) error
	// TopicUpdate 更新 Topic 记录。
	TopicUpdate(topic string, update map[string]any) error
	// TopicOwnerChange 更新 Topic 的所有者。
	TopicOwnerChange(topic string, newOwner t.Uid) error

	// Topic 订阅管理

	// SubscriptionGet 读取用户对 Topic 的订阅。
	SubscriptionGet(topic string, user t.Uid, keepDeleted bool) (*t.Subscription, error)
	// SubsForUser 加载指定用户的所有订阅。不加载 Public 或 Private 值，不加载已删除的订阅。
	SubsForUser(user t.Uid) ([]t.Subscription, error)
	// SubsForTopic 获取指定 Topic 的订阅列表。不加载 Public 值。
	SubsForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error)
	// SubsUpdate 更新订阅对象的部分字段。不需要更新的字段传 nil。
	SubsUpdate(topic string, user t.Uid, update map[string]any) error
	// SubsDelete 删除单个订阅。
	SubsDelete(topic string, user t.Uid) error

	// 搜索

	// Find 根据标签列表搜索用户或 Topic。
	// - caller 是进行搜索的用户或 Topic，将从结果中跳过。
	// - prefix 如果存在，将使匹配结果排名最高。
	// - req 是必需标签集的列表。每个集是一个标签列表。搜索将返回
	//   拥有每个集中至少一个标签的所有用户/Topic。
	// - opt 是可选标签列表；如果匹配，结果排名更高。
	// - activeOnly 若为 true 则仅返回活跃的订阅。
	Find(caller, prefix string, req [][]string, opt []string, activeOnly bool) ([]t.Subscription, error)
	// FindOne 返回与给定标签匹配的 Topic 或用户。
	FindOne(tag string) (string, error)

	// 消息管理

	// MessageSave 保存消息到数据库。
	MessageSave(msg *t.Message) error
	// MessageGetAll 返回匹配查询的消息。
	MessageGetAll(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.Message, error)
	// MessageDeleteList 将消息标记为已删除。
	// 软删除或硬删除由 forUser 值决定：forUser.IsZero == true 为硬删除。
	MessageDeleteList(topic string, toDel *t.DelMessage) error
	// MessageGetDeleted 返回已删除的消息 ID 列表。
	MessageGetDeleted(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.DelMessage, error)

	// 设备管理（用于推送通知）

	// DeviceUpsert 创建或更新设备记录。
	DeviceUpsert(uid t.Uid, dev *t.DeviceDef) error
	// DeviceGetAll 返回指定用户组的所有设备。
	DeviceGetAll(uid ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error)
	// DeviceDelete 删除设备记录。
	DeviceDelete(uid t.Uid, deviceID string) error

	// 文件上传记录。文件存储在数据库之外。

	// FileStartUpload 初始化文件上传。
	FileStartUpload(fd *t.FileDef) error
	// FileFinishUpload 标记文件上传完成（成功或失败）。
	FileFinishUpload(fd *t.FileDef, success bool, size int64) (*t.FileDef, error)
	// FileGet 获取指定文件的记录。
	FileGet(fid string) (*t.FileDef, error)
	// FileDeleteUnused 删除 UseCount 为零的记录。如果 olderThan 非零，
	// 则删除 UpdatedAt 早于 olderThan 的未使用记录。
	// 返回已删除文件记录的 FileDef.Location 数组，以便同时删除实际文件。
	FileDeleteUnused(olderThan time.Time, limit int) ([]string, error)
	// FileLinkAttachments 将指定的 Topic 或消息连接到文件记录 ID 列表。
	FileLinkAttachments(topic string, userId, msgId t.Uid, fids []string) error

	// 持久缓存管理

	// PCacheGet 读取持久缓存条目。
	PCacheGet(key string) (string, error)
	// PCacheUpsert 创建或更新持久缓存条目。
	PCacheUpsert(key string, value string, failOnDuplicate bool) error
	// PCacheDelete 删除单个持久缓存条目。
	PCacheDelete(key string) error
	// PCacheExpire 过期指定键前缀的较早条目。
	PCacheExpire(keyPrefix string, olderThan time.Time) error

	// 测试

	// GetTestDB 返回当前打开的数据库连接。
	GetTestDB() any
}
