//go:build mongodb

package mongodb

import (
	"sort"
	"time"

	"chat/server/db/common"
	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/v2/bson"
	mdb "go.mongodb.org/mongo-driver/v2/mongo"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// TopicCreate 创建 Topic
func (a *adapter) TopicCreate(topic *t.Topic) error {
	_, err := a.db.Collection("topics").InsertOne(a.ctx, &topic)
	return err
}

// TopicCreateP2P 创建 P2P Topic。
func (a *adapter) TopicCreateP2P(initiator, invited *t.Subscription) error {
	initiator.Id = initiator.Topic + ":" + initiator.User
	// Don't care if the initiator changes own 订阅
	replOpts := mdbopts.Replace().SetUpsert(true)
	_, err := a.db.Collection("subscriptions").ReplaceOne(a.ctx, b.M{"_id": initiator.Id}, initiator, replOpts)
	if err != nil {
		return err
	}

	// If the second 订阅 exists, don't overwrite it. Just make sure it's not deleted.
	invited.Id = invited.Topic + ":" + invited.User
	_, err = a.db.Collection("subscriptions").InsertOne(a.ctx, invited)
	if err != nil {
		// Is this a duplicate 订阅?
		if !isDuplicateErr(err) {
			// It's a genuine DB 错误
			return err
		}
		// 恢复第二个订阅（如果存在）：移除 DeletedAt，更新 CreatedAt 和 UpdatedAt，
		// 更新 ModeGiven。
		err = a.undeleteSubscription(invited)
		if err != nil {
			return err
		}
	}

	topic := &t.Topic{
		ObjHeader: t.ObjHeader{Id: initiator.Topic},
		TouchedAt: initiator.GetTouchedAt(),
	}
	topic.ObjHeader.MergeTimes(&initiator.ObjHeader)
	return a.TopicCreate(topic)
}

// TopicGet 按名称加载单个 Topic（如果存在）。若 Topic 不存在则返回 (nil, nil)
func (a *adapter) TopicGet(topic string) (*t.Topic, error) {
	var tt = new(t.Topic)
	if err := a.db.Collection("topics").FindOne(a.ctx, b.M{"_id": topic}).Decode(tt); err != nil {
		if err == mdb.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	// 独立 MongoDB 实例不支持跨文档事务。以消息日志为权威来源修复崩溃窗口中的游标偏差。
	var latest t.Message
	err := a.db.Collection("messages").FindOne(a.ctx, b.M{"topic": topic},
		mdbopts.FindOne().SetSort(b.D{{Key: "seqid", Value: -1}}).SetProjection(b.M{"seqid": 1})).Decode(&latest)
	if err != nil && err != mdb.ErrNoDocuments {
		return nil, err
	}
	if latest.SeqId != tt.SeqId {
		tt.SeqId = latest.SeqId
		if err = a.topicUpdate(topic, b.M{"seqid": tt.SeqId}); err != nil {
			return nil, err
		}
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// 已找到 Topic，获取订阅计数。
		subCnt, err := a.subscriptionCount(topic)
		if err != nil {
			return nil, err
		}

		if int(subCnt) != tt.SubCnt {
			// Update the Topic with the correct 订阅 count.
			tt.SubCnt = int(subCnt)
			err = a.topicUpdate(topic, b.M{"subcnt": tt.SubCnt})
			if err != nil {
				return nil, err
			}
		}
	}

	tt.Public = unmarshalBsonD(tt.Public)
	tt.Trusted = unmarshalBsonD(tt.Trusted)

	return tt, nil
}

// TopicsForUser 加载用户的联系人列表：P2P 和群组 Topic，不包括 'me' 和 'fnd' 订阅。
// 读取并反规范化 Public 和 Trusted 值。
func (a *adapter) TopicsForUser(uid t.Uid, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	// 获取用户的所有订阅。
	filter := b.M{"user": uid.String()}
	if !keepDeleted {
		// 过滤掉已定义 deletedat 的行
		filter["deletedat"] = b.M{"$exists": false}
	}

	limit := 0
	ims := time.Time{}
	if opts != nil {
		if opts.Topic != "" {
			filter["topic"] = opts.Topic
		}

		// 仅在客户端不管理缓存（或冷启动）时应用限制。
		// 否则需要获取所有订阅并与用户/Topic 手动连接。
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

	findOpts := mdbopts.Find()
	if limit > 0 {
		findOpts = mdbopts.Find().SetLimit(int64(limit))
	}

	cur, err := a.db.Collection("subscriptions").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	// 必须手动关闭游标，因为我们将重用它们。

	// Fetch 订阅. Two queries are needed: 用户 table (me & p2p) and Topic table (p2p and grp).
	// Prepare a list of Separate 订阅 to 用户 vs Topic
	join := make(map[string]t.Subscription) // Keeping these to make a join with table for .private and .access
	topq := make([]string, 0, 16)
	usrq := make([]string, 0, 16)
	for cur.Next(a.ctx) {
		var sub t.Subscription
		if err = cur.Decode(&sub); err != nil {
			break
		}
		tname := sub.Topic
		sub.User = uid.String()
		tcat := t.GetTopicCat(tname)

		if tcat == t.TopicCatMe || tcat == t.TopicCatFnd {
			// Skip 'me' or 'fnd' 订阅. Don't skip 'sys'.
			continue
		} else if tcat == t.TopicCatP2P {
			// P2P 订阅, find the other 用户 to get 用户.Public
			uid1, uid2, _ := t.ParseP2P(sub.Topic)
			if uid1 == uid {
				usrq = append(usrq, uid2.String())
				sub.SetWith(uid2.UserId())
			} else {
				usrq = append(usrq, uid1.String())
				sub.SetWith(uid1.UserId())
			}
			topq = append(topq, tname)
		} else if tcat == t.TopicCatGrp {
			// 可能将 Channel 名称转换为 Topic 名称。
			tname = t.ChnToGrp(tname)
		}
		// 'slf'、'sys' 订阅无需特殊处理。

		topq = append(topq, tname)
		sub.Private = unmarshalBsonD(sub.Private)
		join[tname] = sub
	}
	cur.Close(a.ctx)
	if err != nil {
		return nil, err
	}

	var subs []t.Subscription
	if len(join) == 0 {
		return subs, nil
	}

	if len(topq) > 0 {
		// 获取群组和 P2P Topic
		filter = b.M{"_id": b.M{"$in": topq}}

		if !keepDeleted {
			filter["state"] = b.M{"$ne": t.StateDeleted}
		}

		if !ims.IsZero() {
			// 如果提供了缓存时间戳：仅获取较新的条目。
			filter["touchedat"] = b.M{"$gt": ims}

			findOpts = mdbopts.Find()
			if limit > 0 && limit < len(topq) {
				// 没有意义获取超过请求限制的数量。
				findOpts = mdbopts.Find().SetSort(b.D{{Key: "touchedat", Value: 1}}).SetLimit(int64(limit))
			}
		}

		cur, err = a.db.Collection("topics").Find(a.ctx, filter, findOpts)
		if err != nil {
			return nil, err
		}

		for cur.Next(a.ctx) {
			var top t.Topic
			if err = cur.Decode(&top); err != nil {
				break
			}
			sub := join[top.Id]
			// 检查 sub.UpdatedAt 是否需要调整为更早或更晚的时间。
			sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, top.UpdatedAt)
			sub.SetState(top.State)
			sub.SetTouchedAt(top.TouchedAt)
			sub.SetSeqId(top.SeqId)
			if t.GetTopicCat(sub.Topic) == t.TopicCatGrp {
				sub.SetSubCnt(top.SubCnt)
				sub.SetPublic(unmarshalBsonD(top.Public))
				sub.SetTrusted(unmarshalBsonD(top.Trusted))
			}
			// 放回 P2P 订阅的更新值，将在下面进一步处理
			join[top.Id] = sub
		}
		cur.Close(a.ctx)

		if err != nil {
			return nil, err
		}
	}

	// 获取 P2P 用户并连接到 P2P 表
	if len(usrq) > 0 {
		filter = b.M{"_id": b.M{"$in": usrq}}
		if !keepDeleted {
			filter["state"] = b.M{"$ne": t.StateDeleted}
		}

		// 忽略 ims：我们需要所有用户来获取 LastSeen 和 UserAgent。

		cur, err = a.db.Collection("users").Find(a.ctx, filter, findOpts)
		if err != nil {
			return nil, err
		}

		for cur.Next(a.ctx) {
			var usr2 t.User
			if err = cur.Decode(&usr2); err != nil {
				break
			}

			joinOn := uid.P2PName(t.ParseUid(usr2.Id))
			if sub, ok := join[joinOn]; ok {
				sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, usr2.UpdatedAt)
				sub.SetState(usr2.State)
				sub.SetPublic(unmarshalBsonD(usr2.Public))
				sub.SetTrusted(unmarshalBsonD(usr2.Trusted))
				sub.SetDefaultAccess(usr2.Access.Auth, usr2.Access.Anon)
				sub.SetLastSeenAndUA(usr2.LastSeen, usr2.UserAgent)
				join[joinOn] = sub
			}
		}
		cur.Close(a.ctx)

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

// UsersForTopic 加载指定 Topic 的用户订阅（不包括 Channel 读者）。
// Public 和 Trusted 已加载。
func (a *adapter) UsersForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	tcat := t.GetTopicCat(topic)

	// 获取所有已订阅用户。用户数量不大。
	filter := b.M{"topic": topic}
	if !keepDeleted && tcat != t.TopicCatP2P {
		// 过滤掉 DeletedAt 非空的行。
		// P2P Topic 必须加载所有订阅，否则无法交换 Public 值。
		filter["deletedat"] = b.M{"$exists": false}
	}

	limit := a.maxResults
	var oneUser t.Uid
	if opts != nil {
		// 忽略 IfModifiedSince - 我们必须返回所有条目
		// 未修改的条目将被去除 Public、Trusted 和 Private。

		if !opts.User.IsZero() {
			if tcat != t.TopicCatP2P {
				filter["user"] = opts.User.String()
			}
			oneUser = opts.User
		} else if !opts.Cursor.IsZero() && tcat != t.TopicCatP2P {
			filter["user"] = b.M{"$gt": opts.Cursor.String()}
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}

	cur, err := a.db.Collection("subscriptions").Find(a.ctx, filter,
		mdbopts.Find().SetSort(b.D{{Key: "user", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}

	// Fetch 订阅.
	var subs []t.Subscription
	join := make(map[string]t.Subscription)
	usrq := make([]any, 0, 16)
	for cur.Next(a.ctx) {
		var sub t.Subscription
		if err = cur.Decode(&sub); err != nil {
			break
		}
		join[sub.User] = sub
		usrq = append(usrq, sub.User)
	}
	cur.Close(a.ctx)
	if err != nil {
		return nil, err
	}

	// Fetch 用户 by a list of 订阅.
	if len(usrq) > 0 {
		subs = make([]t.Subscription, 0, len(usrq))
		cur, err = a.db.Collection("users").Find(a.ctx, b.M{
			"_id":   b.M{"$in": usrq},
			"state": b.M{"$ne": t.StateDeleted}})
		if err != nil {
			return nil, err
		}

		for cur.Next(a.ctx) {
			var usr2 t.User
			if err = cur.Decode(&usr2); err != nil {
				break
			}
			if sub, ok := join[usr2.Id]; ok {
				sub.ObjHeader.MergeTimes(&usr2.ObjHeader)
				sub.Private = unmarshalBsonD(sub.Private)
				sub.SetPublic(unmarshalBsonD(usr2.Public))
				sub.SetTrusted(unmarshalBsonD(usr2.Trusted))
				sub.SetLastSeenAndUA(usr2.LastSeen, usr2.UserAgent)
				subs = append(subs, sub)
			}
		}
		cur.Close(a.ctx)
		if err != nil {
			return nil, err
		}
	}

	if t.GetTopicCat(topic) == t.TopicCatP2P && len(subs) > 0 {
		// 按预期交换 P2P Topic 的 public 值和 lastSeen。
		if len(subs) == 1 {
			// 用户已删除。无法处理。
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
	if tcat != t.TopicCatP2P {
		sort.Slice(subs, func(i, j int) bool { return subs[i].User < subs[j].User })
	}

	return subs, nil
}

// topicNamesForUser 从 'collection' 的 'field' 中使用 'filter' 读取 Topic 名称。
// 如果 includeChan 为 true，对于群组 Topic 还会添加相应的 Channel 名称。
func (a *adapter) topicNamesForUser(collection string, filter b.M, field string, includeChan bool) ([]string, error) {
	cur, err := a.db.Collection(collection).Find(a.ctx, filter,
		mdbopts.Find().SetProjection(b.M{field: 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var names []string
	for cur.Next(a.ctx) {
		var res map[string]string
		if err = cur.Decode(&res); err != nil {
			break
		}
		names = append(names, res[field])
		// 如果名称是群组 Topic，且请求时还添加 Channel 名称。
		if includeChan {
			if channel := t.GrpToChn(res[field]); channel != "" {
				names = append(names, channel)
			}
		}
	}

	return names, err
}

// p2pTopicsForUser 完成p2pTopicsFor用户所需的内部处理。
func (a *adapter) p2pTopicsForUser(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("subscriptions",
		b.M{
			"user":      uid.String(),
			"deletedat": b.M{"$exists": false},
			"topic":     b.M{"$regex": b.Regex{Pattern: "^p2p"}}},
		"topic", false)
}

// OwnTopics loads a slice of Topic names where the 用户 is the owner.
func (a *adapter) OwnTopics(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("topics",
		b.M{"owner": uid.String(), "state": b.M{"$ne": t.StateDeleted}},
		"_id", false)
}

// ChannelsForUser loads a slice of Topic names where the 用户 is a Channel reader and notifications (P) are enabled.
func (a *adapter) ChannelsForUser(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("subscriptions",
		b.M{
			"user":      uid.String(),
			"deletedat": b.M{"$exists": false},
			"topic":     b.M{"$regex": b.Regex{Pattern: "^chn"}},
			"modewant":  b.M{"$bitsAllSet": b.A{t.ModePres}},
			"modegiven": b.M{"$bitsAllSet": b.A{t.ModePres}}},
		"topic", false)
}

// TopicShare creates Topic 订阅.
func (a *adapter) TopicShare(topic string, shares []*t.Subscription) error {
	// 分配 Id。
	for _, sub := range shares {
		sub.Id = sub.Topic + ":" + sub.User
	}

	// 订阅 could have been marked as deleted (DeletedAt != nil). If it's marked
	// as deleted, unmark by clearing the DeletedAt field of the old 订阅 and
	// 更新时间和 ModeGiven。
	for _, sub := range shares {
		_, err := a.db.Collection("subscriptions").InsertOne(a.ctx, sub)
		if err != nil {
			if isDuplicateErr(err) {
				if err = a.undeleteSubscription(sub); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	}

	if topic != "" {
		// Update Topic's 订阅 count.
		// The 错误 is ignored because the 订阅 have been created already.
		a.db.Collection("topics").UpdateOne(a.ctx,
			b.M{"_id": topic},
			b.M{"$inc": b.M{"subcnt": len(shares)}})
	}

	return nil
}

// TopicDelete deletes Topic, 订阅, 消息.
func (a *adapter) TopicDelete(topic string, isChan, hard bool) error {
	filter := b.M{}
	if isChan {
		// If the Topic is a Channel, must try to delete 订阅 under both grpXXX and chnXXX names.
		filter["$or"] = b.A{
			b.M{"topic": topic},
			b.M{"topic": t.GrpToChn(topic)},
		}
	} else {
		filter["topic"] = topic
	}
	err := a.subsDelete(a.ctx, filter, hard)
	if err != nil {
		return err
	}

	filter = b.M{"_id": topic}
	if hard {
		if err = a.decFileUseCounter(a.ctx, "topics", filter); err != nil {
			return err
		}
		if err = a.MessageDeleteList(topic, nil); err != nil {
			return err
		}
		scheduledFilter := b.M{"topic": topic}
		if err = a.decFileUseCounter(a.ctx, "scheduledmessages", scheduledFilter); err != nil {
			return err
		}
		if _, err = a.db.Collection("scheduledmessages").DeleteMany(a.ctx, scheduledFilter); err != nil {
			return err
		}
		_, err = a.db.Collection("topics").DeleteOne(a.ctx, filter)
	} else {
		_, err = a.db.Collection("topics").UpdateOne(a.ctx, filter, b.M{"$set": b.M{
			"state":   t.StateDeleted,
			"stateat": t.TimeNow(),
		}})
	}

	return err
}

// TopicUpdateOnMessage increments Topic's or 用户's SeqId value and updates TouchedAt timestamp.
func (a *adapter) TopicUpdateOnMessage(topic string, msg *t.Message) error {
	return a.topicUpdate(topic, b.M{"seqid": msg.SeqId, "touchedat": msg.CreatedAt})
}

// subscriptionCount 完成订阅数量所需的内部处理。
func (a *adapter) subscriptionCount(topic string) (int64, error) {
	// 获取 Topic 的非已删除订阅数。
	return a.db.Collection("subscriptions").CountDocuments(a.ctx, b.M{
		"topic":     b.M{"$in": b.A{topic, t.GrpToChn(topic)}},
		"deletedat": b.M{"$exists": false},
	})
}

// TopicUpdateSubCnt 更新 Topic 中反规范化的订阅者计数。
func (a *adapter) TopicUpdateSubCnt(topic string) error {
	// 获取 Topic 的非已删除订阅数。
	// UPDATE ... SET=(SELECT ...) 在 MongoDB 中不支持，所以必须分两个查询完成。
	count, err := a.subscriptionCount(topic)
	if err != nil {
		return err
	}
	return a.topicUpdate(topic, b.M{"subcnt": count})
}

// TopicUpdate 更新 Topic 记录。
func (a *adapter) TopicUpdate(topic string, update map[string]any) error {
	if t, u := update["TouchedAt"], update["UpdatedAt"]; t == nil && u != nil {
		update["TouchedAt"] = u
	}
	return a.topicUpdate(topic, normalizeUpdateMap(update))
}

// TopicOwnerChange 更新 Topic 的所有者
func (a *adapter) TopicOwnerChange(topic string, newOwner t.Uid) error {
	return a.topicUpdate(topic, map[string]any{"owner": newOwner.String()})
}

// topicUpdate 将输入编码为picUpdate。
func (a *adapter) topicUpdate(topic string, update map[string]any) error {
	_, err := a.db.Collection("topics").UpdateOne(a.ctx,
		b.M{"_id": topic},
		b.M{"$set": update})

	return err
}

// Topic 订阅
