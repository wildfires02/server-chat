//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	"strconv"
	"time"

	"chat/server/db/common"
	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// TopicCreate 从模板创建 Topic
func (a *adapter) TopicCreate(topic *t.Topic) error {
	_, err := rdb.DB(a.dbName).Table("topics").Insert(&topic).RunWrite(a.conn)
	return err
}

// TopicCreateP2P 通过两个用户创建 p2p Topic
func (a *adapter) TopicCreateP2P(initiator, invited *t.Subscription) error {
	initiator.Id = initiator.Topic + ":" + initiator.User
	// 不关心发起者是否更改了自己的订阅
	_, err := rdb.DB(a.dbName).Table("subscriptions").Insert(initiator, rdb.InsertOpts{Conflict: "replace"}).
		RunWrite(a.conn)
	if err != nil {
		return err
	}

	// 如果第二个订阅已存在，不覆盖。确保它未被删除。
	invited.Id = invited.Topic + ":" + invited.User
	_, err = rdb.DB(a.dbName).Table("subscriptions").Insert(invited, rdb.InsertOpts{Conflict: "error"}).
		RunWrite(a.conn)
	if err != nil {
		// 这是重复的订阅吗？
		if !rdb.IsConflictErr(err) {
			// 这是真正的数据库错误
			return err
		}
		// 如果存在则恢复第二个订阅：删除 DeletedAt，更新 CreatedAt 和 UpdatedAt，
		// 更新 ModeGiven。
		_, err = rdb.DB(a.dbName).Table("subscriptions").
			Get(invited.Id).Replace(
			rdb.Row.Without("DeletedAt").
				Merge(map[string]any{
					"CreatedAt": invited.CreatedAt,
					"UpdatedAt": invited.UpdatedAt,
					"ModeGiven": invited.ModeGiven})).
			RunWrite(a.conn)
		if err != nil {
			return err
		}
	}

	topic := &t.Topic{ObjHeader: t.ObjHeader{Id: initiator.Topic}}
	topic.ObjHeader.MergeTimes(&initiator.ObjHeader)
	topic.TouchedAt = initiator.GetTouchedAt()
	return a.TopicCreate(topic)
}

// TopicGet 按名称加载单个 Topic（如果存在）。如果 Topic 不存在则返回 (nil, nil)
func (a *adapter) TopicGet(topic string) (*t.Topic, error) {
	// 按名称获取 Topic
	cursor, err := rdb.DB(a.dbName).Table("topics").Get(topic).Run(a.conn)
	if err != nil {
		return nil, err
	}

	var tt = new(t.Topic)
	if err = cursor.One(tt); err != nil {
		if err == rdb.ErrEmptyResult {
			err = nil // Topic 未找到时无错误。
		}
		return nil, err
	}

	// cursor.One 执行时会自动关闭游标。

	// RethinkDB 不支持跨文档事务。以消息日志为权威来源修复崩溃窗口中的游标偏差。
	latestCursor, err := rdb.DB(a.dbName).Table("messages").
		Between([]any{topic, rdb.MinVal}, []any{topic, rdb.MaxVal},
			rdb.BetweenOpts{Index: "Topic_SeqId"}).
		OrderBy(rdb.OrderByOpts{Index: rdb.Desc("Topic_SeqId")}).
		Limit(1).Field("SeqId").Run(a.conn)
	if err != nil {
		return nil, err
	}
	var latestSeq int
	if err = latestCursor.One(&latestSeq); err == rdb.ErrEmptyResult {
		latestSeq = 0
	} else if err != nil {
		return nil, err
	}
	if latestSeq != tt.SeqId {
		tt.SeqId = latestSeq
		if _, err = rdb.DB(a.dbName).Table("topics").Get(topic).
			Update(map[string]any{"SeqId": tt.SeqId}).RunWrite(a.conn); err != nil {
			return nil, err
		}
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// Topic 已找到，获取订阅数。尝试 Topic 和 Channel 名称。
		if cursor, err = rdb.DB(a.dbName).Table("subscriptions").
			GetAllByIndex("Topic", topic, t.GrpToChn(topic)).
			Filter(rdb.Row.HasFields("DeletedAt").Not()).
			Count().Run(a.conn); err != nil {
			return nil, err
		}
		subCnt := 0
		if err = cursor.One(&subCnt); err != nil {
			return nil, err
		}
		// 无需关闭游标。

		if subCnt != tt.SubCnt {
			// 用正确的订阅数更新 Topic。
			tt.SubCnt = subCnt
			if _, err = rdb.DB(a.dbName).Table("topics").Get(topic).
				Update(map[string]any{"SubCnt": subCnt}).RunWrite(a.conn); err != nil {
				return nil, err
			}
		}
	}
	// RethinkDB Go 驱动错误地将 UTC 时区转换为 +0000
	tt.CreatedAt = tt.CreatedAt.UTC()
	tt.UpdatedAt = tt.UpdatedAt.UTC()
	tt.TouchedAt = tt.TouchedAt.UTC()
	if tt.StateAt != nil {
		stateAt := tt.StateAt.UTC()
		tt.StateAt = &stateAt
	}

	return tt, nil
}

// TopicsForUser 加载用户的联系人列表：p2p 和 grp Topic，不包括 'me' 和 'fnd' 订阅。
// 读取并反规范化 Public 值。
func (a *adapter) TopicsForUser(uid t.Uid, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	// 获取用户的所有订阅，即使最近未修改的。
	// 我们将使用这些订阅来获取可能最近修改过的 Topic 和用户。
	q := rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", uid.String())
	if !keepDeleted {
		// 过滤出已定义 DeletedAt 的行
		q = q.Filter(rdb.Row.HasFields("DeletedAt").Not())
	}

	limit := 0
	ims := time.Time{}
	if opts != nil {
		if opts.Topic != "" {
			q = q.Filter(rdb.Row.Field("Topic").Eq(opts.Topic))
		}

		// 仅在客户端不管理缓存（或冷启动）时应用限制。
		// 否则必须获取所有订阅并与用户/Topic 手动连接。
		if opts.IfModifiedSince == nil {
			if opts.Limit > 0 && opts.Limit < a.maxResults {
				limit = opts.Limit
			} else {
				limit = a.maxResults
			}
		} else {
			ims = *opts.IfModifiedSince
		}
	} else {
		limit = a.maxResults
	}

	if limit > 0 {
		q = q.Limit(limit)
	}

	cursor, err := q.Run(a.conn)
	if err != nil {
		return nil, err
	}

	// 获取订阅。需要两个查询：用户表（me 和 p2p）和 Topic 表（p2p 和 grp）。
	// 准备单独订阅列表以区分用户 vs Topic
	var sub t.Subscription
	join := make(map[string]t.Subscription) // 保留这些以便与表连接获取 .private 和 .access
	topq := make([]any, 0, 16)
	usrq := make([]any, 0, 16)
	for cursor.Next(&sub) {
		tname := sub.Topic
		sub.User = uid.String()
		tcat := t.GetTopicCat(tname)

		if tcat == t.TopicCatMe || tcat == t.TopicCatFnd {
			// 'me' 或 'fnd' 订阅，跳过。不跳过 'sys'。
			continue
		} else if tcat == t.TopicCatP2P {
			// P2P 订阅，找到另一个用户以获取用户.Public
			uid1, uid2, _ := t.ParseP2P(sub.Topic)
			if uid1 == uid {
				usrq = append(usrq, uid2.String())
				sub.SetWith(uid2.UserId())
			} else {
				usrq = append(usrq, uid1.String())
				sub.SetWith(uid1.UserId())
			}
		} else if tcat == t.TopicCatGrp {
			// 可能将 Channel 名称转换为 Topic 名称。
			tname = t.ChnToGrp(tname)
		}
		// 'slf'、'sys' 订阅无需特殊处理。

		topq = append(topq, tname)
		join[tname] = sub
	}
	err = cursor.Err()
	cursor.Close()

	if err != nil {
		return nil, err
	}

	var subs []t.Subscription
	if len(join) == 0 {
		return subs, nil
	}

	if len(topq) > 0 {
		// 获取 grp 和 p2p Topic
		q = rdb.DB(a.dbName).Table("topics").GetAll(topq...)
		if !keepDeleted {
			q = q.Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not())
		}

		if !ims.IsZero() {
			// 如果提供了缓存时间戳：仅获取更新的条目。
			q = q.Filter(rdb.Row.Field("TouchedAt").Gt(ims))

			if limit > 0 && limit < len(topq) {
				// 获取超过请求限制的数量没有意义。
				q = q.OrderBy("TouchedAt").Limit(limit)
			}
		}

		cursor, err = q.Run(a.conn)
		if err != nil {
			return nil, err
		}

		var top t.Topic
		for cursor.Next(&top) {
			sub = join[top.Id]
			// 检查是否需要调整 sub.UpdatedAt 到更早或更晚的时间。
			// 如果 IMS 非零，top.UpdatedAt 保证在 IMS 之后。
			sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, top.UpdatedAt)
			sub.SetState(top.State)
			sub.SetTouchedAt(top.TouchedAt)
			sub.SetSeqId(top.SeqId)
			if t.GetTopicCat(sub.Topic) == t.TopicCatGrp {
				sub.SetSubCnt(top.SubCnt)
				sub.SetPublic(top.Public)
				sub.SetTrusted(top.Trusted)
			}
			// 放回更新后的订阅值，将在下方继续处理。
			join[top.Id] = sub
		}
		err = cursor.Err()
		cursor.Close()

		if err != nil {
			return nil, err
		}
	}

	// 获取 p2p 用户并连接到 p2p 订阅。
	if len(usrq) > 0 {
		q = rdb.DB(a.dbName).Table("users").GetAll(usrq...)
		if !keepDeleted {
			// 可选地跳过已删除的用户。
			q = q.Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not())
		}

		// 忽略 ims：我们需要所有用户以获取 LastSeen 和 UserAgent。

		cursor, err = q.Run(a.conn)
		if err != nil {
			return nil, err
		}

		var usr2 t.User
		for cursor.Next(&usr2) {
			joinOn := uid.P2PName(t.ParseUid(usr2.Id))
			if sub, ok := join[joinOn]; ok {
				sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, usr2.UpdatedAt)
				sub.SetState(usr2.State)
				sub.SetPublic(usr2.Public)
				sub.SetTrusted(usr2.Trusted)
				sub.SetDefaultAccess(usr2.Access.Auth, usr2.Access.Anon)
				sub.SetLastSeenAndUA(usr2.LastSeen, usr2.UserAgent)
				join[joinOn] = sub
			}
		}
		err = cursor.Err()
		cursor.Close()

		if err != nil {
			return nil, err
		}
	}

	subs = make([]t.Subscription, 0, len(join))
	for _, sub := range join {
		subs = append(subs, sub)
	}

	return common.SelectEarliestUpdatedSubs(subs, opts, a.maxResults), nil
}

// UsersForTopic 加载订阅给定 Topic 的用户（非 Channel 读者）。
// UsersForTopic 与 SubsForTopic 的区别在于前者加载用户.Public，
// 后者不加载。
func (a *adapter) UsersForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	tcat := t.GetTopicCat(topic)

	// 获取 Topic 订阅者
	// 获取所有已订阅用户。用户数量不大
	q := rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("Topic", topic)
	if !keepDeleted && tcat != t.TopicCatP2P {
		// 过滤出 DeletedAt 不为空的行。
		// P2P Topic 必须加载所有订阅，否则无法交换 Public 值。
		q = q.Filter(rdb.Row.HasFields("DeletedAt").Not())
	}

	limit := a.maxResults
	var oneUser t.Uid
	if opts != nil {
		// 忽略 IfModifiedSince - 必须返回所有条目
		// 未修改的将去除 Public 和 Private。

		if !opts.User.IsZero() {
			if tcat != t.TopicCatP2P {
				q = q.Filter(rdb.Row.Field("User").Eq(opts.User.String()))
			}
			oneUser = opts.User
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

	// 获取订阅
	var sub t.Subscription
	var subs []t.Subscription
	join := make(map[string]t.Subscription)
	usrq := make([]any, 0, 16)
	for cursor.Next(&sub) {
		join[sub.User] = sub
		usrq = append(usrq, sub.User)
	}
	cursor.Close()

	if len(usrq) > 0 {
		subs = make([]t.Subscription, 0, len(usrq))

		// 通过订阅列表获取用户
		cursor, err = rdb.DB(a.dbName).Table("users").GetAll(usrq...).
			Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).Run(a.conn)
		if err != nil {
			return nil, err
		}

		var usr t.User
		for cursor.Next(&usr) {
			if sub, ok := join[usr.Id]; ok {
				sub.ObjHeader.MergeTimes(&usr.ObjHeader)
				sub.SetPublic(usr.Public)
				sub.SetTrusted(usr.Trusted)
				sub.SetLastSeenAndUA(usr.LastSeen, usr.UserAgent)
				subs = append(subs, sub)
			}
		}
		cursor.Close()
	}

	if t.GetTopicCat(topic) == t.TopicCatP2P && len(subs) > 0 {
		// 按预期交换 P2P Topic 的 public 值和 lastSeen。
		if len(subs) == 1 {
			// 用户已删除。无能为力。
			subs[0].SetPublic(nil)
			subs[0].SetTrusted(nil)
			subs[0].SetLastSeenAndUA(nil, "")
		} else {
			tmp := subs[0].GetPublic()
			subs[0].SetPublic(subs[1].GetPublic())
			subs[1].SetPublic(tmp)

			tmp = subs[0].GetTrusted()
			subs[0].SetTrusted(subs[1].GetTrusted())
			subs[1].SetTrusted(tmp)

			lastSeen := subs[0].GetLastSeen()
			userAgent := subs[0].GetUserAgent()
			subs[0].SetLastSeenAndUA(subs[1].GetLastSeen(), subs[1].GetUserAgent())
			subs[1].SetLastSeenAndUA(lastSeen, userAgent)
		}

		// 移除已删除和不需要的订阅
		if !keepDeleted || !oneUser.IsZero() {
			var xsubs []t.Subscription
			for i := range subs {
				if (subs[i].DeletedAt != nil && !keepDeleted) || (!oneUser.IsZero() && subs[i].Uid() != oneUser) {
					continue
				}
				xsubs = append(xsubs, subs[i])
			}
			subs = xsubs
		}
	}

	return subs, nil
}

// OwnTopics 加载用户作为所有者的 Topic 名称切片。
func (a *adapter) OwnTopics(uid t.Uid) ([]string, error) {
	cursor, err := rdb.DB(a.dbName).Table("topics").GetAllByIndex("Owner", uid.String()).
		Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).Field("Id").Run(a.conn)
	if err != nil {
		return nil, err
	}
	var names []string
	var name string
	for cursor.Next(&name) {
		names = append(names, name)
	}
	cursor.Close()
	return names, nil
}

// ChannelsForUser 加载用户作为 Channel 读者且启用了通知 (P) 的 Topic 名称切片。
func (a *adapter) ChannelsForUser(uid t.Uid) ([]string, error) {
	cursor, err := rdb.DB(a.dbName).Table("subscriptions").
		GetAllByIndex("User", uid.String()).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Filter(rdb.Row.Field("Topic").Match("^chn")).
		Filter(rdb.JS("(function(row) {return (row.ModeWant & row.ModeGiven & " + strconv.Itoa(int(t.ModePres)) + ") > 0;})")).
		Field("Topic").Run(a.conn)

	if err != nil {
		return nil, err
	}
	var names []string
	var name string
	for cursor.Next(&name) {
		names = append(names, name)
	}
	cursor.Close()
	return names, nil
}

// TopicShare 向 Topic 添加订阅并增加 Topic 的 subcnt。
func (a *adapter) TopicShare(topic string, shares []*t.Subscription) error {
	// 分配 Id。
	for _, sub := range shares {
		sub.Id = sub.Topic + ":" + sub.User
	}

	// 订阅可能已被标记为已删除（DeletedAt != nil）。如果已标记为已删除，
	// 通过清除旧订阅的 DeletedAt 字段并更新时间和 ModeGiven 来取消标记。
	_, err := rdb.DB(a.dbName).Table("subscriptions").
		Insert(shares, rdb.InsertOpts{Conflict: func(id, oldsub, newsub rdb.Term) any {
			return oldsub.Without("DeletedAt").Merge(map[string]any{
				"CreatedAt": newsub.Field("CreatedAt"),
				"UpdatedAt": newsub.Field("UpdatedAt"),
				"ModeGiven": newsub.Field("ModeGiven"),
				"ModeWant":  newsub.Field("ModeWant"),
				"DelId":     0,
				"ReadSeqId": 0,
				"RecvSeqId": 0})
		}}).RunWrite(a.conn)

	if err == nil && topic != "" {
		_, err = rdb.DB(a.dbName).Table("topics").
			Get(topic).
			Update(map[string]any{"SubCnt": rdb.Row.Field("SubCnt").Default(0).Add(len(shares))}).
			RunWrite(a.conn)
	}
	return err
}

// TopicDelete 删除 Topic、订阅、消息。
func (a *adapter) TopicDelete(topic string, isChan, hard bool) error {
	var err error
	if err = a.subsDelForTopic(topic, isChan, hard); err != nil {
		return err
	}

	if hard {
		if err = a.MessageDeleteList(topic, nil); err != nil {
			return err
		}
		scheduledQuery := rdb.DB(a.dbName).Table("scheduledmessages").
			Filter(map[string]any{"Topic": topic})
		if err = a.decFileUseCounter(scheduledQuery); err != nil {
			return err
		}
		if _, err = scheduledQuery.Delete().RunWrite(a.conn); err != nil {
			return err
		}
	}

	// 必须使用 GetAll 以产生 decFileUseCounter 期望的数组结果。
	q := rdb.DB(a.dbName).Table("topics").GetAll(topic)
	if hard {
		if err = a.decFileUseCounter(q); err == nil {
			_, err = q.Delete().RunWrite(a.conn)
		}
	} else {
		now := t.TimeNow()
		_, err = q.Update(map[string]any{
			"UpdatedAt": now,
			"TouchedAt": now,
			"State":     t.StateDeleted,
			"StatedAt":  now,
		}).RunWrite(a.conn)
	}
	return err
}

// TopicUpdateOnMessage 反序列化消息相关值到 Topic。
func (a *adapter) TopicUpdateOnMessage(topic string, msg *t.Message) error {
	update := struct {
		SeqId     int
		TouchedAt time.Time
	}{msg.SeqId, msg.CreatedAt}

	_, err := rdb.DB(a.dbName).Table("topics").Get(topic).
		Update(update, rdb.UpdateOpts{Durability: "soft"}).RunWrite(a.conn)

	return err
}

// TopicUpdateSubCnt 更新 Topic 中反规范化的订阅者计数。
func (a *adapter) TopicUpdateSubCnt(topic string) error {
	cursor, err := rdb.DB(a.dbName).Table("subscriptions").
		GetAllByIndex("Topic", topic, t.GrpToChn(topic)).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Count().Run(a.conn)
	if err != nil {
		return err
	}
	defer cursor.Close()

	subCnt := 0
	if !cursor.IsNil() {
		if err = cursor.One(&subCnt); err != nil {
			return err
		}
	}
	_, err = rdb.DB(a.dbName).Table("topics").
		Get(topic).
		Update(map[string]any{
			"SubCnt": subCnt,
		}).RunWrite(a.conn)
	return err
}

// TopicUpdate 执行通用 Topic 更新。
func (a *adapter) TopicUpdate(topic string, update map[string]any) error {
	if t, u := update["TouchedAt"], update["UpdatedAt"]; t == nil && u != nil {
		update["TouchedAt"] = u
	}
	_, err := rdb.DB(a.dbName).Table("topics").Get(topic).Update(update).RunWrite(a.conn)
	return err
}

// TopicOwnerChange 更改 Topic 的所有者。
func (a *adapter) TopicOwnerChange(topic string, newOwner t.Uid) error {
	_, err := rdb.DB(a.dbName).Table("topics").Get(topic).
		Update(map[string]any{"Owner": newOwner.String()}).RunWrite(a.conn)
	return err
}
