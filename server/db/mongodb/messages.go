//go:build mongodb

package mongodb

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"time"

	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/v2/bson"
	mdb "go.mongodb.org/mongo-driver/v2/mongo"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MessageSave saves 消息 to 数据库
func (a *adapter) MessageSave(msg *t.Message) error {
	msg.InitClientKey()
	_, err := a.db.Collection("messages").InsertOne(a.ctx, msg)
	return err
}

// MessageSaveAtomic 在 MongoDB 事务中校验集群 fence、推进 Topic 游标并保存消息。
func (a *adapter) MessageSaveAtomic(msg *t.Message) error {
	msg.InitClientKey()
	if msg.HasAnyClusterFenceField() && !msg.HasClusterFence() {
		return t.ErrMalformed
	}
	if msg.HasClusterFence() && !a.useTransactions {
		// 独立 MongoDB 不具备跨文档事务，不能提供集群 fencing 保证。
		return t.ErrUnsupported
	}
	sess, err := a.conn.StartSession()
	if err != nil {
		return err
	}
	defer sess.EndSession(a.ctx)
	if err = a.maybeStartTransaction(sess); err != nil {
		return err
	}
	return mdb.WithSession(a.ctx, sess, func(sc context.Context) error {
		if msg.HasClusterFence() {
			// 对 fence 文档执行一次条件写，使并发 epoch 推进与本事务发生写冲突。
			fenceResult, fenceErr := a.db.Collection("kvmeta").UpdateOne(sc,
				b.M{"_id": t.ClusterFenceKey(msg.ClusterId), "value": msg.ClusterEpoch},
				b.M{"$set": b.M{"value": msg.ClusterEpoch}})
			if fenceErr != nil {
				return fenceErr
			}
			if fenceResult.MatchedCount != 1 {
				return t.ErrClusterFenced
			}
			topicFilter := b.M{
				"_id": msg.Topic,
				"$or": b.A{
					b.M{"clusterepoch": b.M{"$exists": false}},
					b.M{"clusterepoch": b.M{"$lt": msg.ClusterEpoch}},
					b.M{"clusterepoch": msg.ClusterEpoch, "clusterowner": msg.ClusterOwner},
				},
			}
			topicResult, topicErr := a.db.Collection("topics").UpdateOne(sc, topicFilter,
				b.M{"$set": b.M{
					"seqid": msg.SeqId, "touchedat": msg.CreatedAt,
					"clusterowner": msg.ClusterOwner, "clusterepoch": msg.ClusterEpoch,
				}})
			if topicErr != nil {
				return topicErr
			}
			if topicResult.MatchedCount != 1 {
				return t.ErrClusterFenced
			}
		} else if _, err := a.db.Collection("topics").UpdateOne(sc, b.M{"_id": msg.Topic},
			b.M{"$set": b.M{"seqid": msg.SeqId, "touchedat": msg.CreatedAt}}); err != nil {
			return err
		}
		if _, err := a.db.Collection("messages").InsertOne(sc, msg); err != nil {
			return err
		}
		return a.maybeCommitTransaction(sc, sess)
	})
}

// ClusterFenceAdvance 使用 MongoDB 的 $max 原子操作单调推进数据库 fencing epoch。
func (a *adapter) ClusterFenceAdvance(clusterID string, epoch int64) error {
	if clusterID == "" || epoch <= 0 {
		return t.ErrMalformed
	}
	var committed struct {
		// Value 是数据库已提交、不会回退的 fencing epoch。
		Value int64 `bson:"value"`
	}
	err := a.db.Collection("kvmeta").FindOneAndUpdate(a.ctx,
		b.M{"_id": t.ClusterFenceKey(clusterID)},
		b.M{
			"$max":         b.M{"value": epoch},
			"$setOnInsert": b.M{"createdat": t.TimeNow()},
		},
		mdbopts.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(mdbopts.After)).Decode(&committed)
	if err != nil {
		return err
	}
	if committed.Value != epoch {
		return t.ErrClusterFenced
	}
	return nil
}

// MessageGetByClientId 按 Topic、发送者和客户端幂等键查询已投递消息。
func (a *adapter) MessageGetByClientId(topic string, from t.Uid, clientID string) (*t.Message, error) {
	if clientID == "" {
		return nil, nil
	}
	var msg t.Message
	err := a.db.Collection("messages").FindOne(a.ctx,
		b.M{"topic": topic, "clientkey": t.MessageClientKey(from, clientID)}).Decode(&msg)
	if errors.Is(err, mdb.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.Content = unmarshalBsonD(msg.Content)
	return &msg, nil
}

// MessageGet 按 Topic 和 SeqId 查询一条未硬删除消息。
func (a *adapter) MessageGet(topic string, seqID int) (*t.Message, error) {
	var msg t.Message
	err := a.db.Collection("messages").FindOne(a.ctx,
		b.M{"topic": topic, "seqid": seqID, "delid": b.M{"$exists": false}}).Decode(&msg)
	if errors.Is(err, mdb.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.Content = unmarshalBsonD(msg.Content)
	return &msg, nil
}

// MessageUpdate 更新现存消息的正文、消息头和修改时间。
func (a *adapter) MessageUpdate(msg *t.Message) error {
	res, err := a.db.Collection("messages").UpdateOne(a.ctx,
		b.M{"topic": msg.Topic, "seqid": msg.SeqId, "delid": b.M{"$exists": false}},
		b.M{"$set": b.M{
			"updatedat":  msg.UpdatedAt,
			"head":       msg.Head,
			"content":    msg.Content,
			"searchtext": msg.SearchText,
		}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return t.ErrNotFound
	}
	return nil
}

// MessageSchedule 将消息快照写入持久化定时队列集合。
func (a *adapter) MessageSchedule(msg *t.ScheduledMessage) error {
	_, err := a.db.Collection("scheduledmessages").InsertOne(a.ctx, msg)
	return err
}

// MessageGetScheduledByClientId 按发送者范围内的幂等键查询待投递消息。
func (a *adapter) MessageGetScheduledByClientId(topic string, from t.Uid, clientID string) (*t.ScheduledMessage, error) {
	if clientID == "" {
		return nil, nil
	}
	var msg t.ScheduledMessage
	err := a.db.Collection("scheduledmessages").FindOne(a.ctx,
		b.M{"topic": topic, "from": from.String(), "clientid": clientID}).Decode(&msg)
	if errors.Is(err, mdb.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.Content = unmarshalBsonD(msg.Content)
	return &msg, nil
}

// MessageGetDueScheduled 按计划时间升序读取一批已到期消息。
func (a *adapter) MessageGetDueScheduled(now time.Time, limit int) ([]t.ScheduledMessage, error) {
	if limit <= 0 {
		limit = a.maxMessageResults
	}
	cur, err := a.db.Collection("scheduledmessages").Find(a.ctx,
		b.M{"publishat": b.M{"$lte": now}},
		mdbopts.Find().SetSort(b.D{{Key: "publishat", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)
	var out []t.ScheduledMessage
	for cur.Next(a.ctx) {
		var msg t.ScheduledMessage
		if err = cur.Decode(&msg); err != nil {
			return nil, err
		}
		msg.Content = unmarshalBsonD(msg.Content)
		out = append(out, msg)
	}
	return out, cur.Err()
}

// MessageDeleteScheduled 递减附件引用计数后删除指定发送者拥有的定时消息。
func (a *adapter) MessageDeleteScheduled(id, topic string, from t.Uid) error {
	filter := b.M{"_id": id, "topic": topic, "from": from.String()}
	if err := a.decFileUseCounter(a.ctx, "scheduledmessages", filter); err != nil {
		return err
	}
	_, err := a.db.Collection("scheduledmessages").DeleteOne(a.ctx, filter)
	return err
}

// MessageGetAll returns 消息 matching the query.
func (a *adapter) MessageGetAll(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.Message, error) {
	var limit = a.maxMessageResults
	var lower, upper int
	requester := forUser.String()
	if opts != nil {
		if opts.Since > 0 {
			lower = opts.Since
		}
		if opts.Before > 0 {
			upper = opts.Before
		}

		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	filter := b.M{
		"topic":           topic,
		"delid":           b.M{"$exists": false},
		"deletedfor.user": b.M{"$ne": requester},
	}
	if upper == 0 {
		filter["seqid"] = b.M{"$gte": lower}
	} else {
		filter["seqid"] = b.M{"$gte": lower, "$lt": upper}
	}
	if opts != nil && opts.IfModifiedSince != nil {
		filter["updatedat"] = b.M{"$gt": *opts.IfModifiedSince}
	}
	sortDirection := -1
	if opts != nil && opts.Forward {
		sortDirection = 1
	}
	sortBy := b.D{{Key: "seqid", Value: sortDirection}}
	if opts != nil && opts.IfModifiedSince != nil {
		sortBy = b.D{{Key: "updatedat", Value: sortDirection}, {Key: "seqid", Value: sortDirection}}
	}
	findOpts := mdbopts.Find().SetSort(sortBy)
	findOpts.SetLimit(int64(limit))

	cur, err := a.db.Collection("messages").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var msgs []t.Message
	for cur.Next(a.ctx) {
		var msg t.Message
		if err = cur.Decode(&msg); err != nil {
			return nil, err
		}
		msg.Content = unmarshalBsonD(msg.Content)
		msgs = append(msgs, msg)
	}

	return msgs, nil
}

// MessageGetLatest returns the latest message visible to forUser for each topic in one aggregation.
func (a *adapter) MessageGetLatest(topics []string, forUser t.Uid) ([]t.Message, error) {
	if len(topics) == 0 {
		return nil, nil
	}
	pipeline := mdb.Pipeline{
		{{Key: "$match", Value: b.M{
			"topic":           b.M{"$in": topics},
			"delid":           b.M{"$exists": false},
			"deletedfor.user": b.M{"$ne": forUser.String()},
		}}},
		{{Key: "$sort", Value: b.D{{Key: "topic", Value: 1}, {Key: "seqid", Value: -1}}}},
		{{Key: "$group", Value: b.M{"_id": "$topic", "message": b.M{"$first": "$$ROOT"}}}},
		{{Key: "$replaceRoot", Value: b.M{"newRoot": "$message"}}},
	}
	cur, err := a.db.Collection("messages").Aggregate(a.ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	msgs := make([]t.Message, 0, len(topics))
	for cur.Next(a.ctx) {
		var msg t.Message
		if err = cur.Decode(&msg); err != nil {
			return nil, err
		}
		msg.Content = unmarshalBsonD(msg.Content)
		msgs = append(msgs, msg)
	}
	return msgs, cur.Err()
}

// MessageSearch 在单个 Topic 内按规范化正文搜索消息，并排除调用者已删除的消息。
func (a *adapter) MessageSearch(topic string, forUser t.Uid, search *t.MessageSearchQuery) ([]t.Message, error) {
	if search == nil || search.Query == "" {
		return nil, nil
	}
	limit := search.Limit
	if limit <= 0 || limit > a.maxMessageResults {
		limit = a.maxMessageResults
	}

	filter := b.M{
		"topic":           topic,
		"delid":           b.M{"$exists": false},
		"deletedfor.user": b.M{"$ne": forUser.String()},
		"searchtext": b.Regex{
			Pattern: regexp.QuoteMeta(search.Query),
			Options: "i",
		},
	}
	if !search.From.IsZero() {
		filter["from"] = search.From.String()
	}
	if len(search.Kinds) > 0 {
		filter["head.x-kind"] = b.M{"$in": search.Kinds}
	}
	if search.MinDate != nil || search.MaxDate != nil {
		dateFilter := b.M{}
		if search.MinDate != nil {
			dateFilter["$gte"] = *search.MinDate
		}
		if search.MaxDate != nil {
			dateFilter["$lt"] = *search.MaxDate
		}
		filter["createdat"] = dateFilter
	}
	if search.BeforeSeq > 0 {
		filter["seqid"] = b.M{"$lt": search.BeforeSeq}
	}

	cursor, err := a.db.Collection("messages").Find(a.ctx, filter,
		mdbopts.Find().SetSort(b.D{{Key: "seqid", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(a.ctx)

	messages := make([]t.Message, 0, limit)
	for cursor.Next(a.ctx) {
		var msg t.Message
		if err = cursor.Decode(&msg); err != nil {
			return nil, err
		}
		msg.Content = unmarshalBsonD(msg.Content)
		messages = append(messages, msg)
	}
	return messages, cursor.Err()
}

// messagesHardDelete 完成messagesHard删除所需的内部处理。
func (a *adapter) messagesHardDelete(topic string) error {
	var err error

	// 扣减关联文件附件的使用计数 (decFileUseCounter 在下文执行)
	filter := b.M{"topic": topic}
	if _, err = a.db.Collection("dellog").DeleteMany(a.ctx, filter); err != nil {
		return err
	}

	if err = a.decFileUseCounter(a.ctx, "messages", filter); err != nil {
		return err
	}

	if _, err = a.db.Collection("messages").DeleteMany(a.ctx, filter); err != nil {
		return err
	}

	return err
}

// rangeToFilter 是 Mongo 中等效于 common.RangeToSql 的实现。
func rangeToFilter(delRanges []t.Range, filter b.M) b.M {
	if len(delRanges) > 1 || delRanges[0].Hi == 0 {
		rangeFilter := b.A{}
		for _, rng := range delRanges {
			if rng.Hi == 0 {
				rangeFilter = append(rangeFilter, b.M{"seqid": rng.Low})
			} else {
				rangeFilter = append(rangeFilter, b.M{"seqid": b.M{"$gte": rng.Low, "$lt": rng.Hi}})
			}
		}
		filter["$or"] = rangeFilter
	} else {
		filter["seqid"] = b.M{"$gte": delRanges[0].Low, "$lt": delRanges[0].Hi}
	}
	return filter
}

// MessageDeleteList marks 消息 as deleted.
// 软删除还是硬删除取决于 forUser 值：forUser.IsZero() == true 为硬删除。
func (a *adapter) MessageDeleteList(topic string, toDel *t.DelMessage) error {
	var err error

	if toDel == nil {
		// No filter: delete all 消息.
		return a.messagesHardDelete(topic)
	}

	// Only some 消息 are being deleted

	delRanges := toDel.SeqIdRanges
	filter := b.M{
		"topic": topic,
		// Skip already hard-deleted 消息.
		"delid": b.M{"$exists": false},
	}
	// Mongo 中等效于 common.RangeToSql
	rangeToFilter(delRanges, filter)

	if toDel.DeletedFor == "" {
		// Hard-deleting 消息 requires updates to the 消息 table.

		// We are asked to delete 消息 no older than newerThan.
		if newerThan := toDel.GetNewerThan(); newerThan != nil {
			filter["createdat"] = b.M{"$gt": newerThan}
		}

		pipeline := b.A{
			b.M{"$match": filter},
			b.M{"$project": b.M{"seqid": 1}},
		}

		// Find the actual IDs still present in the 数据库.

		cur, err := a.db.Collection("messages").Aggregate(a.ctx, pipeline)
		if err != nil {
			return err
		}
		defer cur.Close(a.ctx)

		var seqIDs []int
		for cur.Next(a.ctx) {
			var result struct {
				SeqID int `bson:"seqid"`
			}
			if err = cur.Decode(&result); err != nil {
				return err
			}
			seqIDs = append(seqIDs, result.SeqID)
		}

		if len(seqIDs) == 0 {
			// 无需删除。无需记录日志。完成。
			return nil
		}

		// 重新计算实际要删除的范围。
		sort.Ints(seqIDs)
		delRanges = t.SliceToRanges(seqIDs)

		// 用新范围组成新查询。
		filter = b.M{
			"topic": topic,
		}
		rangeToFilter(delRanges, filter)

		if err = a.decFileUseCounter(a.ctx, "messages", filter); err != nil {
			return err
		}
		// Hard-delete individual 消息. 消息 is not deleted but all fields with content
		// 被替换为 null。
		_, err = a.db.Collection("messages").UpdateMany(a.ctx, filter, b.M{"$set": b.M{
			"deletedat":   t.TimeNow(),
			"delid":       toDel.DelId,
			"from":        "",
			"head":        nil,
			"content":     nil,
			"attachments": nil}})
	} else {
		// 软删除：将 DelId 添加到 DeletedFor

		// Skip 消息 already soft-deleted for the current 用户
		filter["deletedfor.user"] = b.M{"$ne": toDel.DeletedFor}

		_, err = a.db.Collection("messages").UpdateMany(a.ctx, filter,
			b.M{"$addToSet": b.M{
				"deletedfor": &t.SoftDelete{
					User:  toDel.DeletedFor,
					DelId: toDel.DelId,
				}}})
	}

	// 记录日志。硬删除和软删除都需要。
	if _, err = a.db.Collection("dellog").InsertOne(a.ctx, toDel); err != nil {
		return err
	}

	if toDel.DelId > 0 {
		if err = a.TopicUpdate(topic, map[string]any{"DelId": toDel.DelId}); err != nil {
			return err
		}
		forUser := t.ParseUserId(toDel.DeletedFor)
		if err = a.SubsUpdate(topic, forUser, map[string]any{"DelId": toDel.DelId}); err != nil {
			return err
		}
	}

	return nil
}

// MessageRetireExpired 分批清除超过保留期的消息正文和附件引用。
func (a *adapter) MessageRetireExpired(cutoff time.Time, limit int) ([]t.Uid, error) {
	if limit <= 0 {
		return nil, nil
	}
	filter := b.M{"createdat": b.M{"$lt": cutoff}, "delid": b.M{"$exists": false}}
	cursor, err := a.db.Collection("messages").Find(a.ctx, filter,
		mdbopts.Find().SetProjection(b.M{"_id": 1}).SetSort(b.D{{Key: "createdat", Value: 1}}).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(a.ctx)
	var ids []string
	for cursor.Next(a.ctx) {
		var row struct {
			ID string `bson:"_id"`
		}
		if err = cursor.Decode(&row); err != nil {
			return nil, err
		}
		ids = append(ids, row.ID)
	}
	if err = cursor.Err(); err != nil || len(ids) == 0 {
		return nil, err
	}
	selected := b.M{"_id": b.M{"$in": ids}}
	if err = a.decFileUseCounter(a.ctx, "messages", selected); err != nil {
		return nil, err
	}
	if _, err = a.db.Collection("messages").UpdateMany(a.ctx, selected, b.M{
		"$set": b.M{
			"deletedat": t.TimeNow(), "delid": -1, "from": "", "head": nil,
			"content": nil, "searchtext": "", "attachments": nil,
		},
		"$unset": b.M{"clientid": "", "clientkey": ""},
	}); err != nil {
		return nil, err
	}
	messageIDs := make([]t.Uid, 0, len(ids))
	for _, id := range ids {
		messageIDs = append(messageIDs, t.ParseUid(id))
	}
	return messageIDs, nil
}

// MessageGetDeleted returns a list of deleted 消息 Ids.
func (a *adapter) MessageGetDeleted(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.DelMessage, error) {
	var limit = a.maxResults
	var lower, upper int
	if opts != nil {
		if opts.Since > 0 {
			lower = opts.Since
		}
		if opts.Before > 0 {
			upper = opts.Before
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	filter := b.M{
		"topic": topic,
		"$or": b.A{
			b.M{"deletedfor": forUser.String()},
			b.M{"deletedfor": ""},
		}}
	if upper == 0 {
		filter["delid"] = b.M{"$gte": lower}
	} else {
		filter["delid"] = b.M{"$gte": lower, "$lt": upper}
	}
	findOpts := mdbopts.Find().
		SetSort(b.D{{Key: "topic", Value: 1}, {Key: "delid", Value: 1}}).
		SetLimit(int64(limit))

	cur, err := a.db.Collection("dellog").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var dmsgs []t.DelMessage
	if err = cur.All(a.ctx, &dmsgs); err != nil {
		return nil, err
	}

	return dmsgs, nil
}

// 设备管理（用于推送通知）。
