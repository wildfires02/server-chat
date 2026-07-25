package store

import (
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/store/types"
)

// UsersPersistenceInterface 定义用户记录持久化存储的方法接口。
type UsersPersistenceInterface interface {
	Create(user *types.User, private any) (*types.User, error)
	GetAuthRecord(user types.Uid, scheme string) (string, auth.Level, []byte, time.Time, error)
	GetAuthUniqueRecord(scheme, unique string) (types.Uid, auth.Level, []byte, time.Time, error)
	AddAuthRecord(uid types.Uid, authLvl auth.Level, scheme, unique string, secret []byte, expires time.Time) error
	UpdateAuthRecord(uid types.Uid, authLvl auth.Level, scheme, unique string, secret []byte, expires time.Time) error
	DelAuthRecords(uid types.Uid, scheme string) error
	Get(uid types.Uid) (*types.User, error)
	GetAll(uid ...types.Uid) ([]types.User, error)
	GetByCred(method, value string) (types.Uid, error)
	Delete(id types.Uid, hard bool) error
	UpdateLastSeen(uid types.Uid, userAgent string, when time.Time) error
	Update(uid types.Uid, update map[string]any) error
	UpdateTags(uid types.Uid, add, remove, reset []string) ([]string, error)
	UpdateState(uid types.Uid, state types.ObjState) error
	GetSubs(id types.Uid) ([]types.Subscription, error)
	FindSubs(caller types.Uid, prefPrefix string, required [][]string, optional []string, activeOnly bool) ([]types.Subscription, error)
	FindOne(tag string) (string, error)
	GetTopics(id types.Uid, opts *types.QueryOpt) ([]types.Subscription, error)
	GetTopicsAny(id types.Uid, opts *types.QueryOpt) ([]types.Subscription, error)
	GetOwnTopics(id types.Uid) ([]string, error)
	GetChannels(id types.Uid) ([]string, error)
	UpsertCred(cred *types.Credential) (bool, error)
	ConfirmCred(id types.Uid, method string) error
	FailCred(id types.Uid, method string) error
	GetActiveCred(id types.Uid, method string) (*types.Credential, error)
	GetAllCreds(id types.Uid, method string, validatedOnly bool) ([]types.Credential, error)
	DelCred(id types.Uid, method, value string) error
	GetUnreadCount(ids ...types.Uid) (map[types.Uid]int, error)
	GetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]types.Uid, error)
}

// usersMapper 是实现 UsersPersistenceInterface 的具体类型。
type usersMapper struct{}

// Users 是导出 UsersPersistenceInterface 方法的单例锚对象。
var Users UsersPersistenceInterface

// Create 将用户对象插入数据库，更新创建时间并分配 UID
func (usersMapper) Create(user *types.User, private any) (*types.User, error) {

	user.SetUid(Store.GetUid())
	user.InitTimes()

	err := adp.UserCreate(user)
	if err != nil {
		return nil, err
	}

	// 创建用户的 'me' 和 'fnd' 订阅。这些 Topic 是临时的，不需要插入 Topic 对象。
	err = Subs.Create(
		&types.Subscription{
			ObjHeader: types.ObjHeader{CreatedAt: user.CreatedAt},
			User:      user.Id,
			Topic:     user.Uid().UserId(),
			ModeWant:  types.ModeCMeFnd,
			ModeGiven: types.ModeCMeFnd,
			Private:   private,
		},
		&types.Subscription{
			ObjHeader: types.ObjHeader{CreatedAt: user.CreatedAt},
			User:      user.Id,
			Topic:     user.Uid().FndName(),
			ModeWant:  types.ModeCMeFnd,
			ModeGiven: types.ModeCMeFnd,
			Private:   nil,
		})
	if err != nil {
		// 尽力删除不完整的用户记录。孤立的用户记录不是问题，只是占用空间。
		adp.UserDelete(user.Uid(), true)
		return nil, err
	}

	return user, nil
}

// GetAuthRecord 接受用户 ID 和认证方案名称，获取唯一的方案相关标识符和认证密钥。
func (usersMapper) GetAuthRecord(user types.Uid, scheme string) (string, auth.Level, []byte, time.Time, error) {
	unique, authLvl, secret, expires, err := adp.AuthGetRecord(user, scheme)
	if err == nil {
		parts := strings.Split(unique, ":")
		if len(parts) > 1 {
			unique = parts[1]
		} else {
			err = types.ErrInternal
		}
	}

	return unique, authLvl, secret, expires, err
}

// GetAuthUniqueRecord 接受唯一标识符和认证方案名称，获取用户 ID 和认证密钥。
func (usersMapper) GetAuthUniqueRecord(scheme, unique string) (types.Uid, auth.Level, []byte, time.Time, error) {
	return adp.AuthGetUniqueRecord(scheme + ":" + unique)
}

// AddAuthRecord 为给定用户创建新的认证记录。
func (usersMapper) AddAuthRecord(uid types.Uid, authLvl auth.Level, scheme, unique string, secret []byte,
	expires time.Time) error {

	return adp.AuthAddRecord(uid, scheme, scheme+":"+unique, authLvl, secret, expires)
}

// UpdateAuthRecord 用新密钥和过期时间更新认证记录。
func (usersMapper) UpdateAuthRecord(uid types.Uid, authLvl auth.Level, scheme, unique string,
	secret []byte, expires time.Time) error {

	return adp.AuthUpdRecord(uid, scheme, scheme+":"+unique, authLvl, secret, expires)
}

// DelAuthRecords 删除给定方案的用户认证记录。
func (usersMapper) DelAuthRecords(uid types.Uid, scheme string) error {
	return adp.AuthDelScheme(uid, scheme)
}

// Get 返回给定用户 ID 的用户对象，如果用户未找到则返回 nil。
func (usersMapper) Get(uid types.Uid) (*types.User, error) {
	return adp.UserGet(uid)
}

// GetAll 返回给定用户 ID 列表的用户对象切片。
func (usersMapper) GetAll(uid ...types.Uid) ([]types.User, error) {
	return adp.UserGetAll(uid...)
}

// GetByCred 返回给定已验证凭据的用户 ID。
func (usersMapper) GetByCred(method, value string) (types.Uid, error) {
	return adp.UserGetByCred(method, value)
}

// Delete 删除用户记录。
func (usersMapper) Delete(id types.Uid, hard bool) error {
	return adp.UserDelete(id, hard)
}

// UpdateLastSeen 更新 LastSeen 和 UserAgent。
func (usersMapper) UpdateLastSeen(uid types.Uid, userAgent string, when time.Time) error {
	return adp.UserUpdate(uid, map[string]any{"LastSeen": when, "UserAgent": userAgent})
}

// Update 是用户数据的通用更新。
func (usersMapper) Update(uid types.Uid, update map[string]any) error {
	if _, ok := update["UpdatedAt"]; !ok {
		update["UpdatedAt"] = types.TimeNow()
	}
	return adp.UserUpdate(uid, update)
}

// UpdateTags 添加、删除或重置标签到给定切片。
func (usersMapper) UpdateTags(uid types.Uid, add, remove, reset []string) ([]string, error) {
	return adp.UserUpdateTags(uid, add, remove, reset)
}

// UpdateState 更改用户状态及与该用户关联的部分 Topic 状态。
func (usersMapper) UpdateState(uid types.Uid, state types.ObjState) error {
	update := map[string]any{
		"State":   state,
		"StateAt": types.TimeNow()}
	return adp.UserUpdate(uid, update)
}

// GetSubs 加载给定用户的所有订阅。
// 不加载 Public/Trusted 或 Private，不加载已删除的订阅。
func (usersMapper) GetSubs(id types.Uid) ([]types.Subscription, error) {
	return adp.SubsForUser(id)
}

// FindSubs 根据给定标签查找用户和 Topic 列表。结果格式化为订阅。
// `required` 指定必需项的 AND-of-ORs：
// `required` 中每个子列表至少有一个元素必须存在于对象的标签列表中。
// `optional` 指定可选标签列表。
func (usersMapper) FindSubs(caller types.Uid, prefPrefix string, required [][]string, optional []string, activeOnly bool) ([]types.Subscription, error) {
	if len(required) == 0 && len(optional) == 0 {
		// 未指定标签，返回空列表。
		return nil, nil
	}
	return adp.Find(caller.UserId(), prefPrefix, required, optional, activeOnly)
}

// FindOne 返回匹配给定标签的 Topic 和/或用户，支持部分匹配。
func (usersMapper) FindOne(tag string) (string, error) {
	return adp.FindOne(tag)
}

// GetTopics 加载用户的订阅列表，将 Public+Trusted 字段复制到订阅
func (usersMapper) GetTopics(id types.Uid, opts *types.QueryOpt) ([]types.Subscription, error) {
	return adp.TopicsForUser(id, false, opts)
}

// GetTopicsAny 加载用户的订阅列表，将 Public+Trusted 字段复制到订阅。
// 已删除的 Topic 也会返回。
func (usersMapper) GetTopicsAny(id types.Uid, opts *types.QueryOpt) ([]types.Subscription, error) {
	return adp.TopicsForUser(id, true, opts)
}

// GetOwnTopics 返回用户作为所有者的 group Topic 名称切片。
func (usersMapper) GetOwnTopics(id types.Uid) ([]string, error) {
	return adp.OwnTopics(id)
}

// GetChannels 返回用户作为 Channel 读者的 group Topic 名称切片。
func (usersMapper) GetChannels(id types.Uid) ([]string, error) {
	return adp.ChannelsForUser(id)
}

// UpsertCred 添加或更新凭据验证请求。插入返回 true，更新返回 false。
func (usersMapper) UpsertCred(cred *types.Credential) (bool, error) {
	cred.InitTimes()
	return adp.CredUpsert(cred)
}

// ConfirmCred 将凭据方法标记为已确认。
func (usersMapper) ConfirmCred(id types.Uid, method string) error {
	return adp.CredConfirm(id, method)
}

// FailCred 增加给定凭据方法的失败计数。
func (usersMapper) FailCred(id types.Uid, method string) error {
	return adp.CredFail(id, method)
}

// GetActiveCred 获取给定用户和方法的当前活跃凭据。
func (usersMapper) GetActiveCred(id types.Uid, method string) (*types.Credential, error) {
	return adp.CredGetActive(id, method)
}

// GetAllCreds 返回给定用户的凭据，全部或仅已验证的。
func (usersMapper) GetAllCreds(id types.Uid, method string, validatedOnly bool) ([]types.Credential, error) {
	return adp.CredGetAll(id, method, validatedOnly)
}

// DelCred 删除用户的凭据。如果 method 为 ""，则删除所有凭据。
func (usersMapper) DelCred(id types.Uid, method, value string) error {
	return adp.CredDel(id, method, value)
}

// GetUnreadCount 返回用户所有具有 R 权限的 Topic 中未读消息的总数。
func (usersMapper) GetUnreadCount(ids ...types.Uid) (map[types.Uid]int, error) {
	return adp.UserUnreadCount(ids...)
}

// GetUnvalidated 返回具有未验证凭据的过期用户 ID 列表、
// 他们的认证级别以及这些凭据名称的逗号分隔列表。
func (usersMapper) GetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]types.Uid, error) {
	return adp.UserGetUnvalidated(lastUpdatedBefore, limit)
}
