//go:build mongodb

package mongodb

import (
	"regexp"
	"slices"

	"chat/server/db/common"
	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/v2/bson"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Find 根据标签列表搜索联系人和 Topic。
func (a *adapter) Find(caller, prefPrefix string, req [][]string, opt []string, activeOnly bool) ([]t.Subscription, error) {
	/*
		// 使用 unionWith 的 MongoDB 聚合管道。
		[
			{ $match: { tags: { $in: ["basic:alice", "travel"] } } },
			{ $unionWith: {
					coll: "topics",
					pipeline: [ { $match: { tags: { $in: ["basic:alice", "travel"] } } } ]
				}
			},
			{ $project: { _id: 1, access: 1, createdat: 1, updatedat: 1, usebt: 1, public: 1, trusted: 1, tags: 1, _source: 1 } },
			{ $addFields: { matchedCount: { $sum: { $map: {
				input: { $setIntersection: [ "$tags", [ "alias:aliassa", "basic:alice", "travel" ] ] },
				as: "tag",
				in: { $cond: { if: { $regexMatch: { input: "$$tag", regex: "^alias:"} }, then: 20, else: 1 } }
			} }}}},
			{ $match: { $expr: { $ne: [ { $size: { $setIntersection: [ "$tags", ["basic:alice", "travel"] ] } }, 0 ] } } },
			{ $sort: { matchedCount: -1 } },
			{ $limit: 20 }
		]

		// 使用 $facet 的替代方法（据说）性能更好：
		[ { $facet: {
					users: [
						{ $match: { tags: { $in: [ "alias:alice", "basic:alice", "travel" ] } } },
						{ $project: { _id: 1, access: 1, createdat: 1, updatedat: 1, usebt: 1, public: 1, trusted: 1, tags: 1 } }
					],
					topics: [
						{ $lookup: {
							from: "topics",
							pipeline: [
								{ $match: { tags: { $in: [ "alias:alice", "basic:alice", "travel" ] } } },
								{ $project: { _id: 1, access: 1, createdat: 1, updatedat: 1, usebt: 1, public: 1, trusted: 1, tags: 1 } } }
							],
							as: "topicDocs"
						}},
						{ $unwind: "$topicDocs" },
						{ $replaceRoot: { newRoot: "$topicDocs" } }
					]
				}
			},
			{ $project: { combined: { $concatArrays: ["$users", "$topics"] } } },
			{ $unwind: "$combined" },
			{ $replaceRoot: { newRoot: "$combined" } },
			{ $group: { _id: "$_id", doc: { $first: "$$ROOT" } } },
			{ $replaceRoot: { newRoot: "$doc" } },
			{ $addFields: { matchedCount:
				{ $sum: { $map: { input:
					{ $setIntersection: [ "$tags", [ "alias:alice", "basic:alice", "travel" ] ] },
					as: "tag",
					in: {
					$cond: {
						if: { $regexMatch: { input: "$$tag", regex: "^alias:" } }, then: 20, else: 1 }
					}
				} }
			} } },
			{ $match: { $expr: { $ne: [
				{ $size: { $setIntersection: [ "$tags", [ "alias:alice", "basic:alice", "travel" ] ] } },
				0
			] } } },
			{ $sort: { matchedCount: -1 } },
			{ $limit: 20 }
		]
	*/

	index := make(map[string]struct{})
	allReq := t.FlattenDoubleSlice(req)
	var allTags []any
	for _, tag := range append(allReq, opt...) {
		allTags = append(allTags, tag)
		index[tag] = struct{}{}
	}

	matchOn := b.M{"tags": b.M{"$in": allTags}}
	if activeOnly {
		matchOn["state"] = b.M{"$eq": t.StateOK}
	}

	projectFields := b.M{"_id": 1, "createdat": 1, "updatedat": 1, "usebt": 1,
		"access": 1, "subcnt": 1, "public": 1, "trusted": 1, "tags": 1}

	pipeline := b.A{
		// 阶段 1：$facet
		b.M{
			"$facet": b.D{
				{Key: "users", Value: b.A{
					b.M{"$match": matchOn},
					b.M{"$project": projectFields},
				}},
				{Key: "topics", Value: b.A{
					b.M{"$lookup": b.D{
						{Key: "from", Value: "topics"},
						{Key: "pipeline", Value: b.A{
							b.M{"$match": matchOn},
							b.M{"$project": projectFields},
						}},
						{Key: "as", Value: "topicDocs"},
					}},
					b.M{"$unwind": "$topicDocs"},
					b.M{"$replaceRoot": b.M{"newRoot": "$topicDocs"}},
				}},
			},
		},
		// 阶段 2：$project
		b.M{"$project": b.M{"combined": b.M{"$concatArrays": b.A{"$users", "$topics"}}}},
		// 阶段 3：$unwind
		b.M{"$unwind": "$combined"},
		// 阶段 4：$replaceRoot
		b.M{"$replaceRoot": b.M{"newRoot": "$combined"}},
		// 阶段 5：$group
		b.M{"$group": b.D{{Key: "_id", Value: "$_id"}, {Key: "doc", Value: b.M{"$first": "$$ROOT"}}}},
		// 阶段 6：$replaceRoot
		b.M{"$replaceRoot": b.M{"newRoot": "$doc"}},
		// 阶段 7：$addFields
		b.M{"$addFields": b.M{"matchedCount": b.M{"$sum": b.M{"$map": b.D{
			{Key: "input", Value: b.M{"$setIntersection": b.A{"$tags", allTags}}},
			{Key: "as", Value: "tag"},
			{Key: "in", Value: b.D{
				{Key: "$cond", Value: b.D{
					{Key: "if", Value: b.M{"$regexMatch": b.D{
						{Key: "input", Value: "$$tag"},
						{Key: "regex", Value: "^alias:"},
					},
					}},
					{Key: "then", Value: 20},
					{Key: "else", Value: 1},
				}}}}},
		}}}},
	}

	// 确保必需标签存在。
	for _, reqDisjunction := range req {
		if len(reqDisjunction) == 0 {
			continue
		}
		var reqTags []any
		for _, tag := range reqDisjunction {
			reqTags = append(reqTags, tag)
		}
		// 过滤掉 'tags' 与 'reqTags' 交集为空数组的文档。
		pipeline = append(pipeline,
			b.M{"$match": b.M{"$expr": b.M{"$ne": b.A{b.M{"$size": b.M{"$setIntersection": b.A{"$tags", reqTags}}}, 0}}}})
	}

	pipeline = append(pipeline,
		// 阶段 9：$sort
		b.M{"$sort": b.D{{Key: "matchedCount", Value: -1}, {Key: "subcnt", Value: -1}}},
		// 阶段 10：$limit
		b.M{"$limit": a.maxResults},
	)

	cur, err := a.db.Collection("users").Aggregate(a.ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var subs []t.Subscription
	for cur.Next(a.ctx) {
		var topic t.Topic
		var sub t.Subscription
		if err = cur.Decode(&topic); err != nil {
			break
		}

		if topic.UseBt {
			// 这是一个 Channel，将 grp 转换为 chn 名称：所有支持 Channel 的
			// Topic 在搜索结果中应以 Channel 形式出现。
			sub.Topic = t.GrpToChn(topic.Id)
		} else {
			if uid := t.ParseUid(topic.Id); !uid.IsZero() {
				topic.Id = uid.UserId()
				if topic.Id == caller {
					// 跳过调用者自身。
					continue
				}
			}
			sub.Topic = topic.Id
		}

		sub.CreatedAt = topic.CreatedAt
		sub.UpdatedAt = topic.UpdatedAt
		sub.SetSubCnt(topic.SubCnt)
		sub.SetPublic(unmarshalBsonD(topic.Public))
		sub.SetTrusted(unmarshalBsonD(topic.Trusted))
		sub.SetDefaultAccess(topic.Access.Auth, topic.Access.Anon)
		// 表示模式未设置，不是 'N'。
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = common.FilterFoundTags(topic.Tags, index)
		subs = append(subs, sub)
	}
	if err == nil {
		err = cur.Err()
	}

	return subs, err
}

// FindByName 按公开 alias 子串发现用户，并按 alias 或 Public.fn 发现公开 Topic。
func (a *adapter) FindByName(caller string, search *t.PeerSearchQuery) ([]t.Subscription, error) {
	if search == nil || search.Query == "" {
		return nil, nil
	}
	quotedQuery := regexp.QuoteMeta(search.Query)
	aliasRegex := "a^"
	if search.AliasPrefix != "" {
		aliasRegex = "^" + regexp.QuoteMeta(search.AliasPrefix) + ":.*" + quotedQuery
	}
	aliasPattern := b.Regex{
		Pattern: aliasRegex,
		Options: "i",
	}
	namePattern := b.Regex{Pattern: quotedQuery, Options: "i"}

	userFilter := b.M{"tags": aliasPattern}
	if search.ActiveOnly {
		userFilter["state"] = t.StateOK
	}
	userCursor, err := a.db.Collection("users").Find(a.ctx, userFilter,
		mdbopts.Find().SetLimit(int64(a.maxResults)))
	if err != nil {
		return nil, err
	}

	found := make([]t.Subscription, 0)
	for userCursor.Next(a.ctx) {
		var user t.User
		if err = userCursor.Decode(&user); err != nil {
			break
		}
		uid := t.ParseUid(user.Id)
		if uid.IsZero() {
			continue
		}
		topic := uid.UserId()
		if topic == caller {
			continue
		}
		public := unmarshalBsonD(user.Public)
		score, matched := common.RankPeerSearch(topic, search.Query, search.AliasPrefix, user.Tags, public)
		if score == 0 {
			continue
		}
		sub := t.Subscription{Topic: topic}
		sub.CreatedAt = user.CreatedAt
		sub.UpdatedAt = user.UpdatedAt
		sub.SetPublic(public)
		sub.SetTrusted(unmarshalBsonD(user.Trusted))
		sub.SetDefaultAccess(user.Access.Auth, user.Access.Anon)
		sub.SetSearchScore(score)
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = matched
		found = append(found, sub)
	}
	if closeErr := userCursor.Close(a.ctx); err == nil {
		err = closeErr
	}
	if err == nil {
		err = userCursor.Err()
	}
	if err != nil {
		return nil, err
	}

	topicFilter := b.M{"$or": b.A{
		b.M{"tags": aliasPattern},
		b.M{"public.fn": namePattern},
	}}
	if search.ActiveOnly {
		topicFilter["state"] = t.StateOK
	}
	topicCursor, err := a.db.Collection("topics").Find(a.ctx, topicFilter,
		mdbopts.Find().SetLimit(int64(a.maxResults)))
	if err != nil {
		return nil, err
	}
	defer topicCursor.Close(a.ctx)
	for topicCursor.Next(a.ctx) {
		var topic t.Topic
		if err = topicCursor.Decode(&topic); err != nil {
			return nil, err
		}
		public := unmarshalBsonD(topic.Public)
		score, matched := common.RankPeerSearch(topic.Id, search.Query, search.AliasPrefix, topic.Tags, public)
		if score == 0 {
			continue
		}
		name := topic.Id
		if topic.UseBt {
			name = t.GrpToChn(name)
		}
		sub := t.Subscription{Topic: name}
		sub.CreatedAt = topic.CreatedAt
		sub.UpdatedAt = topic.UpdatedAt
		sub.SetSubCnt(topic.SubCnt)
		sub.SetPublic(public)
		sub.SetTrusted(unmarshalBsonD(topic.Trusted))
		sub.SetDefaultAccess(topic.Access.Auth, topic.Access.Anon)
		sub.SetSearchScore(score)
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = matched
		found = append(found, sub)
	}
	return found, topicCursor.Err()
}

// FindOne returns the first Topic or 用户 which matches the given tag.
func (a *adapter) FindOne(tag string) (string, error) {
	// Part of the pipeline identical for 用户 and Topic collections.
	commonPipe := b.A{b.M{"$match": b.M{"tags": tag}}, b.M{"$project": b.M{"_id": 1}}}

	// 必须创建 commonPipe 的副本，以便原始 commonPipe 可在 $unionWith 中不受修改地使用。
	pipeline := append(slices.Clone(commonPipe),
		b.M{"$unionWith": b.M{"coll": "topics", "pipeline": commonPipe}},
		b.M{"$limit": 1})
	cur, err := a.db.Collection("users").Aggregate(a.ctx, pipeline)
	if err != nil {
		return "", err
	}
	defer cur.Close(a.ctx)

	var found string
	if cur.Next(a.ctx) {
		entry := map[string]any{}
		if err = cur.Decode(&entry); err != nil {
			return "", err
		}

		if id, ok := entry["_id"].(string); ok {
			if user := t.ParseUid(id); !user.IsZero() {
				found = user.UserId()
			} else {
				found = id
			}
		}
	}

	return found, cur.Err()
}

// 消息
