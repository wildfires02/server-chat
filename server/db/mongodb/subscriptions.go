//go:build mongodb

package mongodb

import (
	"context"
	"sort"

	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/v2/bson"
	mdb "go.mongodb.org/mongo-driver/v2/mongo"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// SubscriptionGet 读取用户对 Topic 的订阅。
func (a *adapter) SubscriptionGet(topic string, user t.Uid, keepDeleted bool) (*t.Subscription, error) {
	sub := new(t.Subscription)
	filter := b.M{"_id": topic + ":" + user.String()}
	if !keepDeleted {
		filter["deletedat"] = b.M{"$exists": false}
	}
	err := a.db.Collection("subscriptions").FindOne(a.ctx, filter).Decode(sub)
	if err != nil {
		if err == mdb.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	return sub, nil
}

// SubsForUser loads all 订阅 of a given 用户. It does NOT load Public, Trusted or Private values,
// 不加载已删除的订阅。
func (a *adapter) SubsForUser(user t.Uid) ([]t.Subscription, error) {
	filter := b.M{"user": user.String(), "deletedat": b.M{"$exists": false}}

	cur, err := a.db.Collection("subscriptions").Find(a.ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var subs []t.Subscription
	for cur.Next(a.ctx) {
		var ss t.Subscription
		if err := cur.Decode(&ss); err != nil {
			return nil, err
		}
		ss.Private = nil
		subs = append(subs, ss)
	}

	return subs, cur.Err()
}

// SubsForTopic 获取 Topic 的所有订阅。不加载 Public 值，不加载 Channel 读者。
// UsersForTopic 与 SubsForTopic 的区别在于前者加载 用户.public+trusted，后者不加载。
func (a *adapter) SubsForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	filter := b.M{"topic": topic}
	if !keepDeleted {
		filter["deletedat"] = b.M{"$exists": false}
	}

	limit := a.maxResults
	if opts != nil {
		// 忽略 IfModifiedSince - 我们必须返回所有条目
		// 未修改的条目将被去除 Public、Trusted 和 Private。

		if !opts.User.IsZero() {
			filter["user"] = opts.User.String()
		} else if !opts.Cursor.IsZero() {
			filter["user"] = b.M{"$gt": opts.Cursor.String()}
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	findOpts := mdbopts.Find().
		SetSort(b.D{{Key: "user", Value: 1}}).
		SetLimit(int64(limit))

	cur, err := a.db.Collection("subscriptions").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var subs []t.Subscription
	for cur.Next(a.ctx) {
		var ss t.Subscription
		if err := cur.Decode(&ss); err != nil {
			return nil, err
		}
		ss.Private = unmarshalBsonD(ss.Private)
		subs = append(subs, ss)
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].User < subs[j].User })

	return subs, cur.Err()
}

// SubsUpdate 更新订阅对象的部分字段。不需要更新的字段传 nil
func (a *adapter) SubsUpdate(topic string, user t.Uid, update map[string]any) error {
	// 将 CamelCase 字段名转换为小写。
	update = normalizeUpdateMap(update)

	filter := b.M{}
	if !user.IsZero() {
		// 更新单个 Topic 订阅
		filter["_id"] = topic + ":" + user.String()
	} else {
		// 更新所有 Topic 订阅
		filter["topic"] = topic
	}
	_, err := a.db.Collection("subscriptions").UpdateOne(a.ctx, filter, b.M{"$set": update})
	return err
}

// SubsDelete marks at most one 订阅 as deleted (soft-deleting).
func (a *adapter) SubsDelete(topic string, user t.Uid) error {
	var sess *mdb.Session
	var err error

	if sess, err = a.conn.StartSession(); err != nil {
		return err
	}
	defer sess.EndSession(a.ctx)

	if err = a.maybeStartTransaction(sess); err != nil {
		return err
	}

	forUser := user.String()

	return mdb.WithSession(a.ctx, sess, func(sc context.Context) error {
		if err := a.subsDelete(sc, b.M{"_id": topic + ":" + forUser}, false); err != nil {
			return err
		}

		// Channel readers cannot delete 消息.
		if !t.IsChannel(topic) {

			// 删除用户的 dellog 条目。
			if _, err := a.db.Collection("dellog").DeleteMany(sc, b.M{"topic": topic, "deletedfor": forUser}); err != nil {
				return err
			}

			// Delete 用户's markings of soft-deleted 消息
			filter := b.M{"topic": topic, "deletedfor.user": forUser}
			if _, err := a.db.Collection("messages").
				UpdateMany(sc, filter, b.M{"$pull": b.M{"deletedfor": b.M{"user": forUser}}}); err != nil {
				return err
			}
		}

		// 提交更改。
		return a.maybeCommitTransaction(sc, sess)
	})
}

// clearUserDellog 删除指定用户的所有 dellog 条目和 deletedfor 标记。
func (a *adapter) clearUserDellog(sc context.Context, forUser string) error {
	var topics []string
	if err := a.db.Collection("subscriptions").Distinct(
		sc,
		"topic",
		b.M{"user": forUser, "deletedat": b.M{"$exists": false}},
	).Decode(&topics); err != nil {
		return err
	}

	// 无需将 Channel 名称转换为群组名称：
	// Channel 读者无法删除消息。

	if len(topics) > 0 {
		// 删除用户的 dellog 条目。
		if _, err := a.db.Collection("dellog").DeleteMany(sc,
			b.M{"topic": b.M{"$in": topics}, "deletedfor": forUser}); err != nil {
			return err
		}

		// Delete 用户's markings of soft-deleted 消息
		filter := b.M{"topic": b.M{"$in": topics}, "deletedfor.user": forUser}
		if _, err := a.db.Collection("messages").
			UpdateMany(sc, filter, b.M{"$pull": b.M{"deletedfor": b.M{"user": forUser}}}); err != nil {
			return err
		}
	}

	return nil
}

// 删除/标记删除订阅并递减 Topic 中的 subcnt。
func (a *adapter) subsDelete(ctx context.Context, filter b.M, hard bool) error {
	// 首先，递减所有受影响 Topic 的订阅计数。
	// 分两步完成，因为 MongoDB 不支持等效的 'UPDATE .. LEFT JOIN ...'。
	filterWithDeletedAt := copyBsonMap(filter)
	filterWithDeletedAt["deletedat"] = b.M{"$exists": false}
	cur, err := a.db.Collection("subscriptions").Find(ctx, filterWithDeletedAt,
		mdbopts.Find().SetProjection(b.D{{Key: "topic", Value: 1}, {Key: "_id", Value: 0}}))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	var topics []string
	for cur.Next(ctx) {
		var result struct {
			Topic string `bson:"topic"`
		}
		if err = cur.Decode(&result); err != nil {
			return err
		}
		if t.IsChannel(result.Topic) {
			// 将 Channel 名称转换为群组名称。
			topics = append(topics, t.ChnToGrp(result.Topic))
		}
		topics = append(topics, result.Topic)
	}

	if err = cur.Err(); err != nil {
		return err
	}

	if len(topics) > 0 {
		// Decrement 订阅 count in affected Topic.
		a.db.Collection("topics").UpdateMany(ctx,
			b.M{"_id": b.M{"$in": topics}},
			b.M{"$inc": b.M{"subcnt": -1}})
	}

	// Now delete or mark deleted the 订阅.
	if hard {
		_, err = a.db.Collection("subscriptions").DeleteMany(ctx, filter)
	} else {
		now := t.TimeNow()
		_, err = a.db.Collection("subscriptions").UpdateMany(ctx, filterWithDeletedAt,
			b.M{"$set": b.M{"updatedat": now, "deletedat": now}})
	}
	return err
}
