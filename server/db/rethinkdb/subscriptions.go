//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	"chat/server/logs"
	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// SubscriptionGet 返回用户对 Topic 的订阅
func (a *adapter) SubscriptionGet(topic string, user t.Uid, keepDeleted bool) (*t.Subscription, error) {

	cursor, err := rdb.DB(a.dbName).Table("subscriptions").Get(topic + ":" + user.String()).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var sub t.Subscription
	if err = cursor.One(&sub); err != nil {
		return nil, err
	}

	if !keepDeleted && sub.DeletedAt != nil {
		return nil, nil
	}

	return &sub, nil
}

// SubsForUser 加载用户的所有订阅。不加载 Public 或 Private 值，
// 也不加载已删除的订阅。
func (a *adapter) SubsForUser(forUser t.Uid) ([]t.Subscription, error) {
	q := rdb.DB(a.dbName).
		Table("subscriptions").
		GetAllByIndex("User", forUser.String()).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Without("Private")

	cursor, err := q.Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var subs []t.Subscription
	var ss t.Subscription
	for cursor.Next(&ss) {
		subs = append(subs, ss)
	}

	return subs, cursor.Err()
}

// SubsForTopic 获取 Topic 的所有订阅。不加载 Public 值。
func (a *adapter) SubsForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {

	q := rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("Topic", topic)
	if !keepDeleted {
		// 过滤出已定义 DeletedAt 的行
		q = q.Filter(rdb.Row.HasFields("DeletedAt").Not())
	}

	limit := a.maxResults
	if opts != nil {
		// 忽略 IfModifiedSince - 必须返回所有条目
		// 未修改的将去除 Public 和 Private。

		if !opts.User.IsZero() {
			q = q.Filter(rdb.Row.Field("User").Eq(opts.User.String()))
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	q = q.Limit(limit)

	cursor, err := q.Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var subs []t.Subscription
	var ss t.Subscription
	for cursor.Next(&ss) {
		subs = append(subs, ss)
	}

	return subs, cursor.Err()
}

// SubsUpdate 更新单个订阅。
func (a *adapter) SubsUpdate(topic string, user t.Uid, update map[string]any) error {
	q := rdb.DB(a.dbName).Table("subscriptions")
	if !user.IsZero() {
		// 更新单个 Topic 订阅
		q = q.Get(topic + ":" + user.String())
	} else {
		// 更新所有 Topic 订阅
		q = q.GetAllByIndex("Topic", topic)
	}
	_, err := q.Update(update).RunWrite(a.conn)
	return err
}

// SubsDelete 最多将一个订阅标记为已删除。
func (a *adapter) SubsDelete(topic string, user t.Uid) error {
	now := t.TimeNow()
	forUser := user.String()

	// 将订阅标记为已删除。
	res, err := rdb.DB(a.dbName).Table("subscriptions").
		Get(topic + ":" + forUser).Update(map[string]any{
		"UpdatedAt": now,
		"DeletedAt": now,
	}).RunWrite(a.conn)
	if err != nil {
		return err
	}

	if res.Replaced == 0 {
		// 未更新任何内容，无事可做。
		return t.ErrNotFound
	}

	// 频道读者的订阅键是 chn...，计数必须更新对应的 grp... Topic。
	counterTopic := topic
	if t.IsChannel(counterTopic) {
		counterTopic = t.ChnToGrp(counterTopic)
	}
	// 减少 Topic 的 SubCnt。
	_, err = rdb.DB(a.dbName).Table("topics").Get(counterTopic).
		Update(map[string]any{"SubCnt": rdb.Row.Field("SubCnt").Default(1).Sub(1)}).
		RunWrite(a.conn)
	if err != nil {
		return err
	}

	if t.IsChannel(topic) {
		// Channel 读者不能删除消息，全部完成。
		return nil
	}

	// 删除已删除消息的记录。

	// 删除当前用户的 dellog 条目。
	resp, err := rdb.DB(a.dbName).Table("dellog").
		// 选择给定表的所有日志条目。
		Between([]any{topic, rdb.MinVal}, []any{topic, rdb.MaxVal},
			rdb.BetweenOpts{Index: "Topic_DelId"}).
		// 仅保留为当前用户软删除的条目。
		Filter(rdb.Row.Field("DeletedFor").Eq(forUser)).
		// 删除它们。
		Delete().
		RunWrite(a.conn)

	if err != nil || resp.Deleted == 0 {
		// 要么是错误，要么是没有删除任何内容。对此错误无能为力。
		// 即使失败也返回 nil。
		return nil
	}

	// 从消息的软删除列表中移除当前用户。
	// 此处可能的错误将被忽略。
	rdb.DB(a.dbName).Table("messages").
		// 选择给定 Topic 中的所有消息。
		Between(
			[]any{topic, forUser, rdb.MinVal},
			[]any{topic, forUser, rdb.MaxVal},
			rdb.BetweenOpts{Index: "Topic_DeletedFor"}).
		// 更新 DeletedFor 字段：
		Update(map[string]any{
			// 取 DeletedFor 数组，减去所有包含当前用户 ID 的值。
			"DeletedFor": rdb.Row.Field("DeletedFor").
				SetDifference(
					rdb.Row.Field("DeletedFor").
						Filter(map[string]any{"User": forUser}))}).
		RunWrite(a.conn)

	return nil
}

// subsDelForTopic 将给定 Topic 的所有订阅标记为已删除。
func (a *adapter) subsDelForTopic(topic string, isChan, hard bool) error {
	var err error

	q := rdb.DB(a.dbName).Table("subscriptions")
	if isChan {
		// 如果 Topic 是 Channel，必须尝试在 grpXXX 和 chnXXX 名称下删除订阅。
		q = q.GetAllByIndex("Topic", topic, t.GrpToChn(topic))
	} else {
		q = q.GetAllByIndex("Topic", topic)
	}
	if hard {
		_, err = q.Delete().RunWrite(a.conn)
	} else {
		now := t.TimeNow()
		_, err = q.Update(map[string]any{
			"UpdatedAt": now,
			"DeletedAt": now,
		}).RunWrite(a.conn)
	}
	return err
}

// subsDelForUser 将给定用户的所有订阅标记为已删除。
func (a *adapter) subsDelForUser(user t.Uid, hard bool) error {
	var err error

	forUser := user.String()

	// 获取用户订阅的所有 Topic。Channel 保留为 Channel。
	topics, err := a.topicNamesForUser(rdb.DB(a.dbName).Table("subscriptions").
		GetAllByIndex("User", forUser).Field("Topic"), false)
	if err != nil {
		logs.Err.Println("subsDelForUser: topicNamesForUser:", err)
		return err
	}

	// 1. 减少 Topic 中的 SubCnt。
	if _, err = rdb.DB(a.dbName).Table("topics").Get(topics...).
		Update(map[string]any{"SubCnt": rdb.Row.Field("SubCnt").
			Default(1).Sub(1)}).
		RunWrite(a.conn); err != nil {
		return err
	}

	err = a.clearUserDellog(user, topics)
	if err != nil {
		logs.Err.Println("subsDelForUser: clearUserDellog:", err)
		return err
	}

	if hard {
		_, err = rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", user.String()).
			Delete().RunWrite(a.conn)
	} else {
		now := t.TimeNow()
		update := map[string]any{
			"UpdatedAt": now,
			"DeletedAt": now,
		}
		_, err = rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", user.String()).
			Update(update).RunWrite(a.conn)
	}

	return err
}
