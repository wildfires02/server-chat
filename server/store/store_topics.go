package store

import "chat/server/store/types"

// TopicsPersistenceInterface 定义 Topic 持久化存储的方法接口。
type TopicsPersistenceInterface interface {
	Create(topic *types.Topic, owner types.Uid, private any) error
	CreateP2P(initiator, invited *types.Subscription) error
	Get(topic string) (*types.Topic, error)
	GetUsers(topic string, opts *types.QueryOpt) ([]types.Subscription, error)
	GetUsersAny(topic string, opts *types.QueryOpt) ([]types.Subscription, error)
	GetSubs(topic string, opts *types.QueryOpt) ([]types.Subscription, error)
	GetSubsAny(topic string, opts *types.QueryOpt) ([]types.Subscription, error)
	Update(topic string, update map[string]any) error
	UpdateSubCnt(topic string) error
	OwnerChange(topic string, newOwner types.Uid) error
	Delete(topic string, isChan, hard bool) error
}

// topicsMapper 是实现 TopicsPersistenceInterface 的具体类型。
type topicsMapper struct{}

// Topics 是导出 TopicsPersistenceInterface 方法的单例锚对象。
var Topics TopicsPersistenceInterface

// Create 创建 Topic 并创建所有者的订阅。
func (topicsMapper) Create(topic *types.Topic, owner types.Uid, private any) error {

	topic.InitTimes()
	topic.TouchedAt = topic.CreatedAt
	topic.Owner = owner.String()

	err := adp.TopicCreate(topic)
	if err != nil {
		return err
	}

	if !owner.IsZero() {
		err = Subs.Create(&types.Subscription{
			ObjHeader: types.ObjHeader{CreatedAt: topic.CreatedAt},
			User:      owner.String(),
			Topic:     topic.Id,
			ModeGiven: types.ModeCFull,
			ModeWant:  topic.GetAccess(owner),
			Private:   private})
	}

	return err
}

// CreateP2P 通过生成两个用户的相互订阅来创建 P2P Topic。
func (topicsMapper) CreateP2P(initiator, invited *types.Subscription) error {
	initiator.InitTimes()
	initiator.SetTouchedAt(initiator.CreatedAt)
	invited.InitTimes()
	invited.SetTouchedAt(invited.CreatedAt)

	return adp.TopicCreateP2P(initiator, invited)
}

// Get 获取单个 Topic，并将相关用户反规范化到其中
func (topicsMapper) Get(topic string) (*types.Topic, error) {
	return adp.TopicGet(topic)
}

// GetUsers 加载 Topic 的订阅，同时加载用户.Public+Trusted。
// 不加载已删除的订阅。
func (topicsMapper) GetUsers(topic string, opts *types.QueryOpt) ([]types.Subscription, error) {
	return adp.UsersForTopic(topic, false, opts)
}

// GetUsersAny 加载 Topic 的订阅，同时加载用户.Public+Trusted。与 GetUsers 相同，
// 但也会加载已删除的订阅。
func (topicsMapper) GetUsersAny(topic string, opts *types.QueryOpt) ([]types.Subscription, error) {
	return adp.UsersForTopic(topic, true, opts)
}

// GetSubs 加载给定 Topic 的订阅列表，不加载用户.Public+Trusted 和已删除的订阅。
// 已暂停的订阅会被加载。
func (topicsMapper) GetSubs(topic string, opts *types.QueryOpt) ([]types.Subscription, error) {
	return adp.SubsForTopic(topic, false, opts)
}

// GetSubsAny 加载给定 Topic 的订阅列表，包括已删除的订阅。
// 不加载用户.Public/Trusted
func (topicsMapper) GetSubsAny(topic string, opts *types.QueryOpt) ([]types.Subscription, error) {
	return adp.SubsForTopic(topic, true, opts)
}

// UpdateSubCnt 刷新 Topic 中反规范化的订阅者计数。
func (topicsMapper) UpdateSubCnt(topic string) error {
	return adp.TopicUpdateSubCnt(topic)
}

// Update 是通用 Topic 更新。
func (topicsMapper) Update(topic string, update map[string]any) error {
	if _, ok := update["UpdatedAt"]; !ok {
		update["UpdatedAt"] = types.TimeNow()
	}
	return adp.TopicUpdate(topic, update)
}

// OwnerChange 将旧 Topic 所有者替换为新所有者。
func (topicsMapper) OwnerChange(topic string, newOwner types.Uid) error {
	return adp.TopicOwnerChange(topic, newOwner)
}

// Delete 删除 Topic、消息、附件和订阅。
func (topicsMapper) Delete(topic string, isChan, hard bool) error {
	return adp.TopicDelete(topic, isChan, hard)
}
