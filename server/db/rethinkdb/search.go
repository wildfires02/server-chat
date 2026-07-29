//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	"regexp"

	"chat/server/db/common"
	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// Find 返回匹配给定标签的用户和 Topic 列表，如 "email:jdoe@example.com" 或 "tel:+18003287448"。
func (a *adapter) Find(caller, promoPrefix string, req [][]string, opt []string, activeOnly bool) ([]t.Subscription, error) {
	index := make(map[string]struct{})
	allReq := t.FlattenDoubleSlice(req)
	var allTags []any
	for _, tag := range append(allReq, opt...) {
		allTags = append(allTags, tag)
		index[tag] = struct{}{}
	}
	// 查询以选择匹配项，其中每个组包含至少一个必需匹配（限制搜索范围为组成员）。
	/*
		r.db('im').
			table('users').
			getAll('basic:alice', 'travel', {index: "Tags"}).
			union(r.db('im').table('topics').getAll('basic:alice', 'travel', {index: "Tags"})).
			pluck('Id', 'Access', 'CreatedAt', 'UpdatedAt', 'UseBt', 'Public', 'Trusted', 'Tags').
			group('Id').
			ungroup().
			map(row => row.getField('reduction').nth(0).merge(
				{matchedCount: row.getField('reduction').
					getField('Tags').
					nth(0).
					setIntersection(['alias:aliassa', 'basic:alice', 'travel']).
					map(tag => r.branch(tag.match('^alias:'), 20, 1)).
					sum()
				})).
			filter(row => row.getField('Tags').setIntersection(['basic:alice', 'travel']).count().ne(0)).
			orderBy(r.desc('matchedCount')).
			limit(20)
	*/

	// 获取标签匹配的用户和 Topic，按匹配数从高到低排序。
	query := rdb.DB(a.dbName).
		Table("users").
		GetAllByIndex("Tags", allTags...).
		Union(rdb.DB(a.dbName).Table("topics").
			GetAllByIndex("Tags", allTags...))
	if activeOnly {
		query = query.Filter(rdb.Row.Field("State").Eq(t.StateOK))
	}
	query = query.Pluck("Id", "Access", "CreatedAt", "UpdatedAt", "UseBt", "SubCnt", "Public", "Trusted", "Tags").
		Group("Id").
		Ungroup().
		Map(func(row rdb.Term) rdb.Term {
			return row.Field("reduction").
				Nth(0).
				Merge(map[string]any{"MatchedTagsCount": row.Field("reduction").
					Field("Tags").
					Nth(0).
					SetIntersection(allTags).
					Map(func(tag rdb.Term) any {
						return rdb.Branch(
							tag.Match("^"+promoPrefix),
							20, // 如果标签匹配 promo 前缀，计为 20。
							1)  // 否则计为 1。
					}).
					Sum()})
		})

	for _, reqDisjunction := range req {
		if len(reqDisjunction) == 0 {
			continue
		}
		var reqTags []any
		for _, tag := range reqDisjunction {
			reqTags = append(reqTags, tag)
		}
		// 过滤出不匹配任何必需标签的对象。
		query = query.Filter(func(row rdb.Term) rdb.Term {
			return row.Field("Tags").SetIntersection(reqTags).Count().Ne(0)
		})
	}
	cursor, err := query.OrderBy(rdb.Desc("MatchedTagsCount")).Limit(a.maxResults).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var topic t.Topic
	var sub t.Subscription
	var subs []t.Subscription
	for cursor.Next(&topic) {
		if uid := t.ParseUid(topic.Id); !uid.IsZero() {
			topic.Id = uid.UserId()
			if topic.Id == caller {
				// 跳过调用者
				continue
			}
		}

		if topic.UseBt {
			sub.Topic = t.GrpToChn(topic.Id)
		} else {
			sub.Topic = topic.Id
		}

		sub.CreatedAt = topic.CreatedAt
		sub.UpdatedAt = topic.UpdatedAt
		sub.SetSubCnt(topic.SubCnt)
		sub.SetPublic(topic.Public)
		sub.SetTrusted(topic.Trusted)
		sub.SetDefaultAccess(topic.Access.Auth, topic.Access.Anon)
		// 表示模式未设置，不是 'N'。
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = common.FilterFoundTags(topic.Tags, index)
		subs = append(subs, sub)
	}

	return subs, cursor.Err()
}

// FindByName 按公开 alias 子串发现用户，并按 alias 或 Public.fn 发现公开 Topic。
func (a *adapter) FindByName(caller string, search *t.PeerSearchQuery) ([]t.Subscription, error) {
	if search == nil || search.Query == "" {
		return nil, nil
	}
	quotedQuery := regexp.QuoteMeta(search.Query)
	aliasPattern := "(?i)a^"
	if search.AliasPrefix != "" {
		aliasPattern = "(?i)^" + regexp.QuoteMeta(search.AliasPrefix) + ":.*" + quotedQuery
	}
	namePattern := "(?i)" + quotedQuery

	userQuery := rdb.DB(a.dbName).Table("users").Filter(func(row rdb.Term) any {
		return row.Field("Tags").Default([]any{}).Contains(func(tag rdb.Term) any {
			return tag.Match(aliasPattern)
		})
	})
	if search.ActiveOnly {
		userQuery = userQuery.Filter(rdb.Row.Field("State").Eq(t.StateOK))
	}
	userCursor, err := userQuery.Limit(a.maxResults).Run(a.conn)
	if err != nil {
		return nil, err
	}

	found := make([]t.Subscription, 0)
	var user t.User
	for userCursor.Next(&user) {
		uid := t.ParseUid(user.Id)
		if uid.IsZero() {
			continue
		}
		topic := uid.UserId()
		if topic == caller {
			continue
		}
		score, matched := common.RankPeerSearch(topic, search.Query, search.AliasPrefix, user.Tags, user.Public)
		if score == 0 {
			continue
		}
		sub := t.Subscription{Topic: topic}
		sub.CreatedAt = user.CreatedAt
		sub.UpdatedAt = user.UpdatedAt
		sub.SetPublic(user.Public)
		sub.SetTrusted(user.Trusted)
		sub.SetDefaultAccess(user.Access.Auth, user.Access.Anon)
		sub.SetSearchScore(score)
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = matched
		found = append(found, sub)
	}
	userCursor.Close()
	if err = userCursor.Err(); err != nil {
		return nil, err
	}

	topicQuery := rdb.DB(a.dbName).Table("topics").Filter(func(row rdb.Term) any {
		aliasMatch := row.Field("Tags").Default([]any{}).Contains(func(tag rdb.Term) any {
			return tag.Match(aliasPattern)
		})
		nameMatch := row.Field("Public").Default(map[string]any{}).
			Field("fn").Default("").Match(namePattern)
		return rdb.Or(aliasMatch, nameMatch)
	})
	if search.ActiveOnly {
		topicQuery = topicQuery.Filter(rdb.Row.Field("State").Eq(t.StateOK))
	}
	topicCursor, err := topicQuery.Limit(a.maxResults).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer topicCursor.Close()

	var topic t.Topic
	for topicCursor.Next(&topic) {
		score, matched := common.RankPeerSearch(topic.Id, search.Query, search.AliasPrefix, topic.Tags, topic.Public)
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
		sub.SetPublic(topic.Public)
		sub.SetTrusted(topic.Trusted)
		sub.SetDefaultAccess(topic.Access.Auth, topic.Access.Anon)
		sub.SetSearchScore(score)
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = matched
		found = append(found, sub)
	}
	return found, topicCursor.Err()
}

// FindOne 返回匹配给定标签的 Topic 或用户。
func (a *adapter) FindOne(tag string) (string, error) {
	query := rdb.DB(a.dbName).
		Table("users").GetAllByIndex("Tags", tag).
		Union(rdb.DB(a.dbName).Table("topics").GetAllByIndex("Tags", tag)).
		Field("Id").
		Limit(1)
	cursor, err := query.Run(a.conn)
	if err != nil {
		return "", err
	}
	defer cursor.Close()

	var found string
	if err = cursor.One(&found); err != nil {
		if err == rdb.ErrEmptyResult {
			return "", nil
		}
		return "", err
	}

	if user := t.ParseUid(found); !user.IsZero() {
		found = user.UserId()
	}

	return found, nil
}

// 消息
