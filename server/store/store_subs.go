// Package store 提供领域模型及持久化访问层。
package store

import (
	"fmt"

	"chat/server/store/types"
)

// SubsPersistenceInterface 定义订阅持久化存储的方法接口。
type SubsPersistenceInterface interface {
	// Create 创建并初始化Create。
	Create(subs ...*types.Subscription) error
	// Get 查询并返回Get。
	Get(topic string, user types.Uid, keepDeleted bool) (*types.Subscription, error)
	// Update 更新Update。
	Update(topic string, user types.Uid, update map[string]any) error
	// Delete 删除或清理删除。
	Delete(topic string, user types.Uid) error
}

// subsMapper 是实现 SubsPersistenceInterface 的具体类型。
type subsMapper struct{}

// Subs 是导出 SubsPersistenceInterface 的单例锚对象。
var Subs SubsPersistenceInterface

// subscriptionCounterTopic 返回需要维护 SubCnt 的持久化 Topic。
// 频道读者使用 chn... 订阅键，但计数始终属于对应的 grp... Topic。
func subscriptionCounterTopic(topic string) string {
	if types.IsEphemeralTopic(topic) {
		return ""
	}
	if types.IsChannel(topic) {
		return types.ChnToGrp(topic)
	}
	return topic
}

// Create 创建多个订阅。
func (subsMapper) Create(subs ...*types.Subscription) error {
	if len(subs) == 0 {
		// 无事可做。
		return nil
	}

	subscriptionTopic := subs[0].Topic
	counterTopic := subscriptionCounterTopic(subscriptionTopic)

	for _, sub := range subs {
		sub.InitTimes()
		if subscriptionTopic != "" && sub.Topic != subscriptionTopic {
			return fmt.Errorf("all subscriptions must be for the same topic, got %s vs %s",
				sub.Topic, subscriptionTopic)
		}
	}

	return adp.TopicShare(counterTopic, subs)
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
