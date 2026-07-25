package store

import (
	"fmt"

	"chat/server/store/types"
)

// SubsPersistenceInterface 定义订阅持久化存储的方法接口。
type SubsPersistenceInterface interface {
	Create(subs ...*types.Subscription) error
	Get(topic string, user types.Uid, keepDeleted bool) (*types.Subscription, error)
	Update(topic string, user types.Uid, update map[string]any) error
	Delete(topic string, user types.Uid) error
}

// subsMapper 是实现 SubsPersistenceInterface 的具体类型。
type subsMapper struct{}

// Subs 是导出 SubsPersistenceInterface 的单例锚对象。
var Subs SubsPersistenceInterface

// Create 创建多个订阅。
func (subsMapper) Create(subs ...*types.Subscription) error {
	if len(subs) == 0 {
		// 无事可做。
		return nil
	}

	topic := subs[0].Topic
	if types.IsEphemeralTopic(topic) {
		// 临时 Topic 不持久化在 'Topic' 表中，不要尝试更新它们。
		// 不允许混合临时和真实 Topic。
		topic = ""
	}

	for _, sub := range subs {
		sub.InitTimes()
		if topic != "" && sub.Topic != topic {
			return fmt.Errorf("all subscriptions must be for the same topic, got %s vs %s", sub.Topic, topic)
		}
	}

	return adp.TopicShare(topic, subs)
}

// Get 根据 Topic 和用户 ID 获取订阅。
func (subsMapper) Get(topic string, user types.Uid, keepDeleted bool) (*types.Subscription, error) {
	return adp.SubscriptionGet(topic, user, keepDeleted)
}

// Update 更新 Topic 订阅的值。
func (subsMapper) Update(topic string, user types.Uid, update map[string]any) error {
	update["UpdatedAt"] = types.TimeNow()
	return adp.SubsUpdate(topic, user, update)
}

// Delete 删除订阅。
// 要删除 Channel 订阅，必须明确指定 Channel 名称。
func (subsMapper) Delete(topic string, user types.Uid) error {
	return adp.SubsDelete(topic, user)
}
