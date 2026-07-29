//go:build mongodb

package mongodb

import (
	"errors"
	"time"

	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mdb "go.mongodb.org/mongo-driver/mongo"
	mdbopts "go.mongodb.org/mongo-driver/mongo/options"
)

// UserCreate 创建用户记录
func (a *adapter) UserCreate(usr *t.User) error {
	if _, err := a.db.Collection("users").InsertOne(a.ctx, &usr); err != nil {
		return err
	}

	return nil
}

// UserGet 根据用户 ID 获取单个用户。若未找到用户则返回 (nil, nil)
func (a *adapter) UserGet(id t.Uid) (*t.User, error) {
	var user t.User

	filter := b.M{"_id": id.String(), "state": b.M{"$ne": t.StateDeleted}}
	if err := a.db.Collection("users").FindOne(a.ctx, filter).Decode(&user); err != nil {
		if err == mdb.ErrNoDocuments { // 未找到用户
			return nil, nil
		} else {
			return nil, err
		}
	}
	user.Public = unmarshalBsonD(user.Public)
	user.Trusted = unmarshalBsonD(user.Trusted)
	return &user, nil
}

// UserGetAll 根据用户 ID 列表返回用户记录
func (a *adapter) UserGetAll(ids ...t.Uid) ([]t.User, error) {
	uids := make([]any, len(ids))
	for i, id := range ids {
		uids[i] = id.String()
	}

	var users []t.User
	filter := b.M{"_id": b.M{"$in": uids}, "state": b.M{"$ne": t.StateDeleted}}
	cur, err := a.db.Collection("users").Find(a.ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	for cur.Next(a.ctx) {
		var user t.User
		if err := cur.Decode(&user); err != nil {
			return nil, err
		}
		user.Public = unmarshalBsonD(user.Public)
		user.Trusted = unmarshalBsonD(user.Trusted)

		users = append(users, user)
	}

	return users, nil
}

// UserDelete 删除指定用户：完全擦除（硬删除）或标记为已删除。
func (a *adapter) UserDelete(uid t.Uid, hard bool) error {
	ownFilter := b.M{"owner": uid.String()}
	// 硬删除时，删除所有 Topic，包括那些
	// 之前已软删除。
	if !hard {
		ownFilter["state"] = b.M{"$ne": t.StateDeleted}
	}

	forUser := uid.String()
	// 选择用户作为所有者的 Topic。
	ownTopics, err := a.topicNamesForUser("topics", ownFilter, "_id", true)
	if err != nil {
		return err
	}
	ownTopicsFilter := b.M{"topic": b.M{"$in": ownTopics}}

	var sess mdb.Session
	if sess, err = a.conn.StartSession(); err != nil {
		return err
	}
	defer sess.EndSession(a.ctx)

	if err = a.maybeStartTransaction(sess); err != nil {
		return err
	}

	if err = mdb.WithSession(a.ctx, sess, func(sc mdb.SessionContext) error {
		scheduledFilter := b.M{"from": forUser}
		if len(ownTopics) > 0 {
			scheduledFilter = b.M{"$or": b.A{
				b.M{"from": forUser},
				b.M{"topic": b.M{"$in": ownTopics}},
			}}
		}
		if err = a.decFileUseCounter(sc, "scheduledmessages", scheduledFilter); err != nil {
			return err
		}
		if _, err = a.db.Collection("scheduledmessages").DeleteMany(sc, scheduledFilter); err != nil {
			return err
		}

		if hard {
			// 无需删除用户的设备：设备存储在用户记录中，会随记录一起删除。

			// 删除用户在所有 Topic 中的订阅并递减 Topic 的 subcnt。
			if err = a.subsDelete(sc, b.M{"user": forUser}, true); err != nil {
				return err
			}

			// 删除用户在所有 Topic 中的 dellog 条目。
			err = a.clearUserDellog(sc, forUser)
			if err != nil {
				return err
			}

			// 无法删除用户在所有 Topic 中的消息，因为无法通知 Topic 此类删除。
			// 仅将消息保留，标记为由“未找到”用户发送。

			// 删除用户作为所有者的 Topic：
			if len(ownTopics) > 0 {

				// 1. 删除 dellog
				// 2. 递减 fileuploads。
				// 3. 删除所有消息。
				// 4. 删除订阅。

				// 删除用户拥有的 Topic 的 dellog。
				_, err = a.db.Collection("dellog").DeleteMany(sc, ownTopicsFilter)
				if err != nil {
					return err
				}

				// 递减 fileuploads 使用计数器
				// 首先获取在 topicIds 的 Topic 消息中使用的附件 ID 数组
				// 然后递减这些文件记录的 usecount 字段
				err = a.decFileUseCounter(sc, "messages", ownTopicsFilter)
				if err != nil {
					return err
				}

				// 递减 Topic 头像的使用计数器。
				err = a.decFileUseCounter(sc, "topics", b.M{"_id": b.M{"$in": ownTopics}})
				if err != nil {
					return err
				}

				// Delete 消息
				_, err = a.db.Collection("messages").DeleteMany(sc, ownTopicsFilter)
				if err != nil {
					return err
				}

				// 删除订阅（所有用户在该用户作为 Topic 所有者的地方）。
				_, err = a.db.Collection("subscriptions").DeleteMany(sc, ownTopicsFilter)
				if err != nil {
					return err
				}

				// 无需删除 Topic 标签：标签存储在 Topic 记录中，会随记录一起删除。

				// 最后删除 Topic。
				if _, err = a.db.Collection("topics").DeleteMany(sc, b.M{"owner": forUser}); err != nil {
					return err
				}
			}

			// 删除用户的认证记录。
			if _, err = a.authDelAllRecords(sc, uid); err != nil {
				return err
			}

			// 删除凭据。
			if err = a.credDel(sc, uid, "", ""); err != nil && err != t.ErrNotFound {
				return err
			}

			// 删除头像（递减使用计数器）。
			if err = a.decFileUseCounter(sc, "users", b.M{"_id": forUser}); err != nil {
				return err
			}

			// 无需删除用户的标签：标签存储在用户记录中，会随记录一起删除。

			// 最后删除用户。
			if _, err = a.db.Collection("users").DeleteOne(sc, b.M{"_id": forUser}); err != nil {
				return err
			}
		} else {
			// 禁用用户的订阅。
			if err = a.subsDelete(sc, b.M{"user": forUser}, false); err != nil {
				return err
			}

			now := t.TimeNow()
			disable := b.M{"$set": b.M{"updatedat": now, "state": t.StateDeleted, "stateat": now}}

			if len(ownTopics) > 0 {
				// 禁用用户作为所有者的 Topic 的订阅。
				if _, err = a.db.Collection("subscriptions").UpdateMany(sc, ownTopicsFilter, disable); err != nil {
					return err
				}

				// 禁用用户作为所有者的群组 Topic。
				if _, err = a.db.Collection("topics").UpdateMany(sc, b.M{"_id": b.M{"$in": ownTopics}},
					b.M{"$set": b.M{
						"updatedat": now, "touchedat": now, "state": t.StateDeleted, "stateat": now,
					}}); err != nil {
					return err
				}
			}

			// 禁用与该用户的 P2P Topic。
			p2pTopics, err := a.p2pTopicsForUser(uid)
			if err != nil {
				return err
			}
			if len(p2pTopics) > 0 {
				if _, err = a.db.Collection("topics").UpdateMany(sc, b.M{"_id": b.M{"$in": p2pTopics}},
					b.M{"$set": b.M{
						"updatedat": now, "touchedat": now, "state": t.StateDeleted, "stateat": now,
					}}); err != nil {
					return err
				}

				// 禁用用户已禁用的 P2P Topic 的订阅。
				if _, err = a.db.Collection("subscriptions").UpdateMany(sc,
					b.M{"topic": b.M{"$in": p2pTopics}}, disable); err != nil {
					return err
				}
			}

			// 最后禁用用户。
			if _, err = a.db.Collection("users").UpdateMany(sc, b.M{"_id": forUser}, disable); err != nil {
				return err
			}
		}

		// 最后提交所有更改
		return a.maybeCommitTransaction(sc, sess)
	}); err != nil {
		return err
	}

	return err
}

// topicStateForUser 由 UserUpdate 在更新包含状态变更时调用。
// 已软删除的 Topic 保持软删除状态。
func (a *adapter) topicStateForUser(uid t.Uid, now time.Time, update any) error {
	state, ok := update.(t.ObjState)
	if !ok {
		return t.ErrMalformed
	}

	if now.IsZero() {
		now = t.TimeNow()
	}

	// 变更用户作为所有者的所有 Topic 的状态。
	if _, err := a.db.Collection("topics").UpdateMany(a.ctx,
		b.M{"owner": uid.String(), "state": b.M{"$ne": t.StateDeleted}},
		b.M{"$set": b.M{"state": state, "stateat": now}}); err != nil {
		return err
	}

	// 变更与该用户的 P2P Topic 的状态（P2P Topic 的 owner 为空）
	// 获取与该用户的 P2P Topic 列表。
	p2pTopics, err := a.p2pTopicsForUser(uid)
	if err != nil {
		return err
	}
	if len(p2pTopics) > 0 {
		if _, err := a.db.Collection("topics").UpdateMany(a.ctx,
			b.M{"_id": b.M{"$in": p2pTopics}, "state": b.M{"$ne": t.StateDeleted}},
			b.M{"$set": b.M{"state": state, "stateat": now}}); err != nil {
			return err
		}
	}

	// 订阅无需更新：
	// 已禁用用户的订阅不会被禁用，仍可操作。
	return nil
}

// UserUpdate 更新用户记录
func (a *adapter) UserUpdate(uid t.Uid, update map[string]any) error {
	// 将字段名从 CamelCase 转换为小写。
	update = normalizeUpdateMap(update)

	_, err := a.db.Collection("users").UpdateOne(a.ctx, b.M{"_id": uid.String()}, b.M{"$set": update})
	if err != nil {
		return err
	}

	if state, ok := update["state"]; ok {
		now, _ := update["stateat"].(time.Time)
		err = a.topicStateForUser(uid, now, state)
	}

	// 标签存储在同一记录中，无需单独更新。

	return err
}

// UserUpdateTags 添加、删除或重置用户的标签。
func (a *adapter) UserUpdateTags(uid t.Uid, add, remove, reset []string) ([]string, error) {
	var newTags t.StringSlice
	// 与 nil 比较而非检查零长度：零长度重置是有效的。
	if reset != nil {
		// 用新值替换标签
		newTags = reset
	} else {
		var user t.User
		err := a.db.Collection("users").FindOne(a.ctx, b.M{"_id": uid.String()}).Decode(&user)
		if err != nil {
			return nil, err
		}

		// 变更标签列表。
		newTags = user.Tags
		if len(add) > 0 {
			newTags = union(newTags, add)
		}
		if len(remove) > 0 {
			newTags = diff(newTags, remove)
		}
	}

	return newTags, a.UserUpdate(uid, map[string]any{"tags": newTags})
}

// UserGetByCred returns 用户 ID for the given validated credential.
func (a *adapter) UserGetByCred(method, value string) (t.Uid, error) {
	var userId map[string]string
	err := a.db.Collection("credentials").FindOne(a.ctx,
		b.M{"_id": method + ":" + value},
		mdbopts.FindOne().SetProjection(b.M{"user": 1, "_id": 0}),
	).Decode(&userId)
	if err != nil {
		if err == mdb.ErrNoDocuments {
			return t.ZeroUid, nil
		}
		return t.ZeroUid, err
	}

	return t.ParseUid(userId["user"]), nil
}

// UserUnreadCount returns the total number of unread 消息 in all Topic with
// the R 权限. If read fails, the counts are still returned with the original
// 用户 IDs but with the unread count undefined and non-nil 错误.
// Does not count unread 消息 in Channel although it probably should.
func (a *adapter) UserUnreadCount(ids ...t.Uid) (map[t.Uid]int, error) {
	uids := make([]string, len(ids))
	counts := make(map[t.Uid]int, len(ids))
	for i, id := range ids {
		uids[i] = id.String()
		// 确保所有原始 uid 始终存在。
		counts[id] = 0
	}
	/*
		Query:
			db.subscriptions.aggregate([
				{ $match: { user: { $in: ["KnElfSSA21U", "0ZcCQmwI2RI"] } } },
				{ $lookup: { from: "topics", localField: "topic", foreignField: "_id", as: "fromTopics"} },
				{ $match: { fromTopics: { $not: {$size: 0}  }}},
				{ $replaceRoot: { newRoot: { $mergeObjects: [ {$arrayElemAt: [ "$fromTopics", 0 ]} , "$$ROOT" ] } } },
				{ $match: {
						deletedat: { $exists: false },
						state:     { $ne: t.StateDeleted },
						modewant:  { $bitsAllSet: [ t.ModeRead ] },
						modegiven: { $bitsAllSet: [ t.ModeRead ] }
					}
				},
				{ $project: { _id: 0, user: 1, readseqid: 1, seqid: 1} },
				{ $group: { _id: "$user", unreadCount: { $sum: { $subtract: [ "$seqid", "$readseqid" ] } } } }
			])

		Result:
			{ "_id" : "KnElfSSA21U", "unreadCount" : 0 }
			{ "_id" : "0ZcCQmwI2RI", "unreadCount" : 7 }
	*/

	pipeline := b.A{
		b.M{"$match": b.M{"user": b.M{"$in": uids}}},
		// 将 Topic 名称映射为真实的 Topic ID (如将 chn... 映射为对应的 grp... 主键) 从而支持 Channel
		b.M{"$addFields": b.M{
			"realTopicId": b.M{
				"$cond": b.A{
					b.M{"$eq": b.A{b.M{"$substrCP": b.A{"$topic", 0, 3}}, "chn"}},
					b.M{"$concat": b.A{"grp", b.M{"$substrCP": b.A{"$topic", 3, b.M{"$strLenCP": "$topic"}}}}},
					"$topic",
				},
			},
		}},
		// 从两个集合中连接文档
		b.M{"$lookup": b.M{
			"from":         "topics",
			"localField":   "realTopicId",
			"foreignField": "_id",
			"as":           "fromTopics"},
		},
		// 移除没有订阅的用户。
		b.M{"$match": b.M{"fromTopics": b.M{"$not": b.M{"$size": 0}}}},
		// 合并两个文档为一个
		b.M{"$replaceRoot": b.M{"newRoot": b.M{"$mergeObjects": b.A{b.M{"$arrayElemAt": b.A{"$fromTopics", 0}}, "$$ROOT"}}}},

		// 只保留影响结果的记录。
		b.M{"$match": b.M{
			"deletedat": b.M{"$exists": false},
			"state":     b.M{"$ne": t.StateDeleted},
			// 按访问权限过滤
			"modewant":  b.M{"$bitsAllSet": b.A{t.ModeRead}},
			"modegiven": b.M{"$bitsAllSet": b.A{t.ModeRead}}}},

		// 移除未使用的字段。
		b.M{"$project": b.M{"_id": 0, "user": 1, "readseqid": 1, "seqid": 1}},
		// 按用户分组。
		b.M{"$group": b.M{"_id": "$user", "unreadCount": b.M{"$sum": b.M{"$subtract": b.A{"$seqid", "$readseqid"}}}}},
	}
	cur, err := a.db.Collection("subscriptions").Aggregate(a.ctx, pipeline)
	if err != nil {
		return counts, err
	}
	defer cur.Close(a.ctx)

	for cur.Next(a.ctx) {
		var oneCount struct {
			Id          string `bson:"_id"`
			UnreadCount int    `bson:"unreadCount"`
		}
		cur.Decode(&oneCount)
		counts[t.ParseUid(oneCount.Id)] = oneCount.UnreadCount
	}

	return counts, nil
}

// UserGetUnvalidated 返回从未登录、没有已验证凭据且自 lastUpdatedBefore 以来未更新过的用户 ID 列表。
func (a *adapter) UserGetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]t.Uid, error) {
	/*
		Query:
		[
			// .. WHERE lastseen 为空 AND updatedat<?
			{$match: {
				$and: [
					{ lastseen: null },
					{ updatedat: {$lt: new ISODate("2022-12-09T01:26:15.819Z")} },
				],
			}},
			// JOIN credentials ON id=用户
			{$lookup: {
				from: "credentials",
				localField: "_id",
				foreignField: "user",
				as: "fcred",
			}},
			// {x: 1, y: [{a: 1}, {a: 2}]} -> [{x: 1, a: 1}, {x: 1, a: 2}]（展开数组）
		  {$unwind: {path: "$fcred"}},
			// SELECT _id, 当 done 时为 1 否则为 0
		  {$project: {
				_id: 1,
		    completed: { $cond: { if: "$fcred.done", then: 1, else: 0 } },
		  }},
			// 按 _id 分组
		  {$group: { _id: "$_id", completed: { $sum: "$completed" } } },
			// 筛选 completed=0
		  {$match: { completed: 0 }},
			// 投影 _id
		  {$project: { _id: "$_id" }},
			{$limit: 10}
		]
	*/
	pipeline := b.A{
		b.M{"$match": b.M{
			"$and": b.A{
				b.M{"lastseen": primitive.Null{}},
				b.M{"updatedat": b.M{"$lt": lastUpdatedBefore}},
			},
		}},
		b.M{"$lookup": b.D{
			{Key: "from", Value: "credentials"},
			{Key: "localField", Value: "_id"},
			{Key: "foreignField", Value: "user"},
			{Key: "as", Value: "fcred"}},
		},
		b.M{"$unwind": b.M{"path": "$fcred"}},
		b.M{"$project": b.D{
			{Key: "_id", Value: 1},
			{Key: "completed", Value: b.M{
				"$cond": b.D{{Key: "if", Value: "$fcred.done"}, {Key: "then", Value: 1}, {Key: "else", Value: 0}}},
			}}},
		b.M{"$group": b.D{{Key: "_id", Value: "$_id"}, {Key: "completed", Value: b.M{"$sum": "$completed"}}}},
		b.M{"$match": b.M{"completed": 0}},
		b.M{"$project": b.M{"_id": "$_id"}},
		b.M{"$limit": limit},
	}

	cur, err := a.db.Collection("users").Aggregate(a.ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var uids []t.Uid
	for cur.Next(a.ctx) {
		var oneUser struct {
			Id string `bson:"_id"`
		}
		if err := cur.Decode(&oneUser); err != nil {
			return nil, err
		}
		uid := t.ParseUid(oneUser.Id)
		if uid.IsZero() {
			return nil, errors.New("failed to decode user id")
		}
		uids = append(uids, uid)
	}

	return uids, err
}

// 凭据管理
