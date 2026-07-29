//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	"hash/fnv"
	"regexp"
	"sort"
	"strconv"
	"time"

	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// MessageSave 将消息保存到数据库。
func (a *adapter) MessageSave(msg *t.Message) error {
	msg.InitClientKey()
	_, err := rdb.DB(a.dbName).Table("messages").Insert(msg).RunWrite(a.conn)
	return err
}

// MessageSaveAtomic 串行推进 Topic 游标并保存消息。
func (a *adapter) MessageSaveAtomic(msg *t.Message) error {
	msg.InitClientKey()
	if msg.HasAnyClusterFenceField() && !msg.HasClusterFence() {
		return t.ErrMalformed
	}
	if msg.HasClusterFence() {
		// RethinkDB 没有跨文档事务，无法原子校验全局 fence 并保存消息。
		return t.ErrUnsupported
	}
	// RethinkDB 不支持跨文档事务。主题由单主 goroutine 串行写入；
	// 先更新游标再写消息，加载 Topic 时再以消息日志修复崩溃窗口中的游标偏差。
	if err := a.TopicUpdateOnMessage(msg.Topic, msg); err != nil {
		return err
	}
	return a.MessageSave(msg)
}

// ClusterFenceAdvance 使用冲突合并函数单调推进数据库 fencing epoch。
// 该方法只用于记录控制面进度；RethinkDB 不提供集群消息事务认证。
func (a *adapter) ClusterFenceAdvance(clusterID string, epoch int64) error {
	if clusterID == "" || epoch <= 0 {
		return t.ErrMalformed
	}
	doc := map[string]any{
		"key":       t.ClusterFenceKey(clusterID),
		"value":     epoch,
		"CreatedAt": t.TimeNow(),
	}
	_, err := rdb.DB(a.dbName).Table("kvmeta").Insert(doc,
		rdb.InsertOpts{Conflict: func(_ rdb.Term, oldDoc, newDoc rdb.Term) any {
			return rdb.Branch(
				oldDoc.Field("value").Default(int64(0)).Lt(newDoc.Field("value")),
				oldDoc.Merge(map[string]any{
					"value":     newDoc.Field("value"),
					"CreatedAt": newDoc.Field("CreatedAt"),
				}),
				oldDoc,
			)
		}}).RunWrite(a.conn)
	if err != nil {
		return err
	}
	var committed struct {
		// Value 是数据库已提交、不会回退的 fencing epoch。
		Value int64 `gorethink:"value"`
	}
	cursor, err := rdb.DB(a.dbName).Table("kvmeta").Get(t.ClusterFenceKey(clusterID)).Run(a.conn)
	if err != nil {
		return err
	}
	defer cursor.Close()
	if err = cursor.One(&committed); err != nil {
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
	cursor, err := rdb.DB(a.dbName).Table("messages").
		GetAllByIndex("Topic_ClientKey", []any{topic, t.MessageClientKey(from, clientID)}).
		Limit(1).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()
	var msg t.Message
	if err = cursor.One(&msg); err == rdb.ErrEmptyResult {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &msg, nil
}

// MessageGet 按 Topic 和 SeqId 查询一条未硬删除消息。
func (a *adapter) MessageGet(topic string, seqID int) (*t.Message, error) {
	cursor, err := rdb.DB(a.dbName).Table("messages").
		GetAllByIndex("Topic_SeqId", []any{topic, seqID}).
		Filter(rdb.Row.HasFields("DelId").Not()).
		Limit(1).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()
	var msg t.Message
	if err = cursor.One(&msg); err == rdb.ErrEmptyResult {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &msg, nil
}

// MessageUpdate 更新现存消息的正文、消息头和修改时间。
func (a *adapter) MessageUpdate(msg *t.Message) error {
	res, err := rdb.DB(a.dbName).Table("messages").
		GetAllByIndex("Topic_SeqId", []any{msg.Topic, msg.SeqId}).
		Filter(rdb.Row.HasFields("DelId").Not()).
		Update(map[string]any{
			"UpdatedAt":  msg.UpdatedAt,
			"Head":       msg.Head,
			"Content":    msg.Content,
			"SearchText": msg.SearchText,
		}).RunWrite(a.conn)
	if err != nil {
		return err
	}
	if res.Replaced+res.Unchanged == 0 {
		return t.ErrNotFound
	}
	return nil
}

// MessageSchedule 将消息快照写入持久化定时队列表。
func (a *adapter) MessageSchedule(msg *t.ScheduledMessage) error {
	_, err := rdb.DB(a.dbName).Table("scheduledmessages").Insert(msg).RunWrite(a.conn)
	return err
}

// MessageGetScheduledByClientId 按发送者范围内的幂等键查询待投递消息。
func (a *adapter) MessageGetScheduledByClientId(topic string, from t.Uid, clientID string) (*t.ScheduledMessage, error) {
	if clientID == "" {
		return nil, nil
	}
	cursor, err := rdb.DB(a.dbName).Table("scheduledmessages").
		GetAllByIndex("Topic_From_ClientId", []any{topic, from.String(), clientID}).
		Limit(1).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()
	var msg t.ScheduledMessage
	if err = cursor.One(&msg); err == rdb.ErrEmptyResult {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &msg, nil
}

// MessageGetDueScheduled 按计划时间升序读取一批已到期消息。
func (a *adapter) MessageGetDueScheduled(now time.Time, limit int) ([]t.ScheduledMessage, error) {
	if limit <= 0 {
		limit = a.maxMessageResults
	}
	cursor, err := rdb.DB(a.dbName).Table("scheduledmessages").
		Between(rdb.MinVal, now, rdb.BetweenOpts{Index: "PublishAt", RightBound: "closed"}).
		OrderBy(rdb.OrderByOpts{Index: "PublishAt"}).
		Limit(limit).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()
	var out []t.ScheduledMessage
	if err = cursor.All(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// MessageDeleteScheduled 递减附件引用计数后删除指定发送者拥有的定时消息。
func (a *adapter) MessageDeleteScheduled(id, topic string, from t.Uid) error {
	query := rdb.DB(a.dbName).Table("scheduledmessages").GetAll(id).
		Filter(map[string]any{"Topic": topic, "From": from.String()})
	if err := a.decFileUseCounter(query); err != nil {
		return err
	}
	_, err := query.Delete().RunWrite(a.conn)
	return err
}

// MessageGetAll 检索给定用户可用的所有消息。
func (a *adapter) MessageGetAll(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.Message, error) {

	var limit = a.maxMessageResults
	var lower, upper any

	upper = rdb.MaxVal
	lower = rdb.MinVal

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

	lower = []any{topic, lower}
	upper = []any{topic, upper}

	requester := forUser.String()
	orderIndex := any(rdb.Desc("Topic_SeqId"))
	if opts != nil && opts.Forward {
		orderIndex = "Topic_SeqId"
	}
	query := rdb.DB(a.dbName).Table("messages").
		Between(lower, upper, rdb.BetweenOpts{Index: "Topic_SeqId"})
	if opts != nil && opts.IfModifiedSince != nil {
		query = query.Filter(rdb.Row.Field("UpdatedAt").Gt(*opts.IfModifiedSince)).
			OrderBy("UpdatedAt", "SeqId")
	} else {
		// 按索引排序必须在过滤之前。
		query = query.OrderBy(rdb.OrderByOpts{Index: orderIndex})
	}
	query = query.
		// 跳过硬删除的消息
		Filter(rdb.Row.HasFields("DelId").Not()).
		// 跳过为当前用户软删除的消息
		Filter(func(row rdb.Term) any {
			return rdb.Not(row.Field("DeletedFor").Default([]any{}).Contains(
				func(df rdb.Term) any {
					return df.Field("User").Eq(requester)
				}))
		})
	cursor, err := query.Limit(limit).Run(a.conn)

	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var msgs []t.Message
	if err = cursor.All(&msgs); err != nil {
		return nil, err
	}

	return msgs, nil
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

	upper := any(rdb.MaxVal)
	if search.BeforeSeq > 0 {
		upper = search.BeforeSeq
	}
	query := rdb.DB(a.dbName).Table("messages").
		Between([]any{topic, rdb.MinVal}, []any{topic, upper},
			rdb.BetweenOpts{Index: "Topic_SeqId"}).
		OrderBy(rdb.OrderByOpts{Index: rdb.Desc("Topic_SeqId")}).
		Filter(rdb.Row.HasFields("DelId").Not()).
		Filter(func(row rdb.Term) any {
			return rdb.Not(row.Field("DeletedFor").Default([]any{}).Contains(
				func(deletedFor rdb.Term) any {
					return deletedFor.Field("User").Eq(forUser.String())
				}))
		}).
		Filter(func(row rdb.Term) any {
			return row.Field("SearchText").Default("").Match(
				"(?i)" + regexp.QuoteMeta(search.Query))
		})
	if !search.From.IsZero() {
		query = query.Filter(rdb.Row.Field("From").Eq(search.From.String()))
	}
	if len(search.Kinds) > 0 {
		kinds := make([]any, len(search.Kinds))
		for i, kind := range search.Kinds {
			kinds[i] = kind
		}
		query = query.Filter(func(row rdb.Term) any {
			return rdb.Expr(kinds).Contains(
				row.Field("Head").Default(map[string]any{}).Field("x-kind").Default(""))
		})
	}
	if search.MinDate != nil {
		query = query.Filter(rdb.Row.Field("CreatedAt").Ge(*search.MinDate))
	}
	if search.MaxDate != nil {
		query = query.Filter(rdb.Row.Field("CreatedAt").Lt(*search.MaxDate))
	}

	cursor, err := query.Limit(limit).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()
	var messages []t.Message
	if err = cursor.All(&messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// MessageGetDeleted 返回已删除消息的范围。
func (a *adapter) MessageGetDeleted(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.DelMessage, error) {
	/*
		r.db('im_test')
			.table('dellog')
			.between(
				['p2p9AVDamaNCRbfKzGSh3mE0w', 1],
				['p2p9AVDamaNCRbfKzGSh3mE0w', 10],
				{index: 'Topic_DelId'}
			)
			.orderBy('Topic_DelId')
			.filter(
				row => row.getField('DeletedFor').eq('0QLrX3WPS2o').or(row.getField('DeletedFor').eq(''))
			)
	*/
	var limit = a.maxResults
	var lower, upper any

	upper = rdb.MaxVal
	lower = rdb.MinVal

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

	// 获取删除日志
	cursor, err := rdb.DB(a.dbName).Table("dellog").
		// 选择给定表和 DelId 值在两个限制之间的日志条目。
		// 默认情况下，左边界为闭区间，右边界为开区间。
		Between([]any{topic, lower}, []any{topic, upper},
			rdb.BetweenOpts{Index: "Topic_DelId"}).
		// 按 DelId 从低到高排序
		OrderBy(rdb.OrderByOpts{Index: "Topic_DelId"}).
		// 保留为当前用户软删除的条目和所有硬删除的条目。
		Filter(func(row rdb.Term) any {
			return row.Field("DeletedFor").Eq(forUser.String()).Or(row.Field("DeletedFor").Eq(""))
		}).
		Limit(limit).Run(a.conn)

	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var dmsgs []t.DelMessage
	if err = cursor.All(&dmsgs); err != nil {
		return nil, err
	}

	return dmsgs, nil
}

// messagesHardDelete 删除 Topic 中的所有消息。
func (a *adapter) messagesHardDelete(topic string) error {
	var err error

	// 扣减关联文件附件的使用计数 (decFileUseCounter 在下文执行)

	if _, err = rdb.DB(a.dbName).Table("dellog").Between(
		[]any{topic, rdb.MinVal},
		[]any{topic, rdb.MaxVal},
		rdb.BetweenOpts{Index: "Topic_DelId"}).Delete().RunWrite(a.conn); err != nil {
		return err
	}

	q := rdb.DB(a.dbName).Table("messages").Between(
		[]any{topic, rdb.MinVal},
		[]any{topic, rdb.MaxVal},
		rdb.BetweenOpts{Index: "Topic_SeqId"})

	if err = a.decFileUseCounter(q); err != nil {
		return err
	}

	_, err = q.Delete().RunWrite(a.conn)

	return err
}

// rangeToQuery 完成rangeTo查询所需的内部处理。
func rangeToQuery(delRanges []t.Range, topic string, query rdb.Term) rdb.Term {
	if len(delRanges) > 1 || delRanges[0].Hi <= delRanges[0].Low {
		var indexVals []any
		for _, rng := range delRanges {
			if rng.Hi == 0 {
				indexVals = append(indexVals, []any{topic, rng.Low})
			} else {
				for i := rng.Low; i <= rng.Hi; i++ {
					indexVals = append(indexVals, []any{topic, i})
				}
			}
		}
		query = query.GetAllByIndex("Topic_SeqId", indexVals...)
	} else {
		// 优化单个范围 low..hi 的特殊情况
		query = query.Between(
			[]any{topic, delRanges[0].Low},
			[]any{topic, delRanges[0].Hi},
			rdb.BetweenOpts{Index: "Topic_SeqId", RightBound: "closed"})
	}
	return query
}

// MessageDeleteList 删除给定 Topic 中 seqId 在列表中的消息。
func (a *adapter) MessageDeleteList(topic string, toDel *t.DelMessage) error {
	var err error

	if toDel == nil {
		// 删除所有消息。
		return a.messagesHardDelete(topic)
	}

	// 仅删除部分消息

	delRanges := toDel.SeqIdRanges
	query := rangeToQuery(delRanges, topic, rdb.DB(a.dbName).Table("messages"))
	// 跳过已硬删除的消息。
	query = query.Filter(rdb.Row.HasFields("DelId").Not())
	if toDel.DeletedFor == "" {
		// 硬删除消息需要更新消息表。

		// 要求删除不超过 newerThan 的消息。
		if newerThan := toDel.GetNewerThan(); newerThan != nil {
			query = query.Filter(rdb.Row.Field("CreatedAt").Gt(newerThan))
		}

		query = query.Field("SeqId")

		// 查找数据库中仍存在的实际 ID。
		cursor, err := query.Run(a.conn)
		if err != nil {
			return err
		}
		defer cursor.Close()

		var seqIDs []int
		if err = cursor.All(&seqIDs); err != nil {
			return err
		}

		if len(seqIDs) == 0 {
			// 无需删除。无需创建日志条目。全部完成。
			return nil
		}

		// 重新计算实际要删除的范围。
		sort.Ints(seqIDs)
		delRanges = t.SliceToRanges(seqIDs)

		// 用新范围组成新查询。
		query = rangeToQuery(delRanges, topic, rdb.DB(a.dbName).Table("messages"))

		// 首先减少附件的使用计数。
		if err = a.decFileUseCounter(query); err != nil {
			return err
		}

		// 硬删除单个消息。消息不会被删除，但所有包含个人内容的字段将被移除。
		if _, err = query.Replace(rdb.Row.Without("Head", "From", "Content", "Attachments").Merge(
			map[string]any{
				"DeletedAt": t.TimeNow(), "DelId": toDel.DelId})).
			RunWrite(a.conn); err != nil {
			return err
		}

	} else {
		// 软删除：将 DelId 添加到 DeletedFor。
		_, err = query.
			// 跳过已为当前用户软删除的消息
			Filter(func(row rdb.Term) any {
				return rdb.Not(row.Field("DeletedFor").Default([]any{}).Contains(
					func(df rdb.Term) any {
						return df.Field("User").Eq(toDel.DeletedFor)
					}))
			}).
			Update(map[string]any{"DeletedFor": rdb.Row.Field("DeletedFor").
				Default([]any{}).Append(
				&t.SoftDelete{
					User:  toDel.DeletedFor,
					DelId: toDel.DelId})}).RunWrite(a.conn)
		if err != nil {
			return err
		}
	}

	// 创建日志条目。硬删除和软删除都需要。
	if _, err = rdb.DB(a.dbName).Table("dellog").Insert(toDel).RunWrite(a.conn); err != nil {
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

// deviceHasher 完成设备Hasher所需的内部处理。
func deviceHasher(deviceID string) string {
	// 生成自定义密钥作为 [64 位设备 ID 哈希] 以确保密钥长度可预测
	hasher := fnv.New64()
	hasher.Write([]byte(deviceID))
	return strconv.FormatUint(uint64(hasher.Sum64()), 16)
}

// 推送通知的设备管理
