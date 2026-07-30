//go:build mysql || (!postgres && !mongodb && !rethinkdb)
// +build mysql !postgres,!mongodb,!rethinkdb

package mysql

import (
	"strconv"
	"strings"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"
)

// Find returns a list of 用户 or group Topic who match given tags, such as "email:jdoe@example.com" or "tel:+18003287448".
func (a *adapter) Find(caller, promoPrefix string, req [][]string, opt []string, activeOnly bool) ([]t.Subscription, error) {
	var args []any
	stateConstraint := ""
	if activeOnly {
		args = append(args, t.StateOK)
		stateConstraint = "u.state=? AND "
	}
	index := make(map[string]struct{})
	allReq := t.FlattenDoubleSlice(req)
	for _, tag := range append(allReq, opt...) {
		args = append(args, tag)
		index[tag] = struct{}{}
	}

	var matcher string
	if promoPrefix != "" {
		// 最大标签数为 16。使用 20 确保一个前缀匹配大于所有非前缀匹配的总和。
		matcher = "SUM(CASE WHEN LOCATE('" + promoPrefix + "', tg.tag)=1 THEN 20 ELSE 1 END)"
	} else {
		matcher = "COUNT(*)"
	}

	query := "SELECT u.id,u.createdat,u.updatedat,0,u.access,0 AS subcnt,u.public,u.trusted,u.tags," + matcher + " AS matches " +
		"FROM users AS u JOIN usertags AS tg ON tg.userid=u.id " +
		"WHERE " + stateConstraint + "tg.tag IN (?" + strings.Repeat(",?", len(allReq)+len(opt)-1) + ") " +
		"GROUP BY u.id,u.createdat,u.updatedat,u.access,u.public,u.trusted,u.tags "
	if len(allReq) > 0 {
		q, a := common.DisjunctionSql(req, "tg.tag")
		query += q
		args = append(args, a...)
	}

	query += "UNION ALL "

	if activeOnly {
		args = append(args, t.StateOK)
		stateConstraint = "t.state=? AND "
	}
	for _, tag := range append(allReq, opt...) {
		args = append(args, tag)
	}

	query += "SELECT t.name AS topic,t.createdat,t.updatedat,t.usebt,t.access,t.subcnt,t.public,t.trusted,t.tags," + matcher + " AS matches " +
		"FROM topics AS t JOIN topictags AS tg ON t.name=tg.topic " +
		"WHERE " + stateConstraint + "tg.tag IN (?" + strings.Repeat(",?", len(allReq)+len(opt)-1) + ") " +
		"GROUP BY t.name,t.createdat,t.updatedat,t.usebt,t.access,t.subcnt,t.public,t.trusted,t.tags "
	if len(allReq) > 0 {
		q, a := common.DisjunctionSql(req, "tg.tag")
		query += q
		args = append(args, a...)
	}
	query += "ORDER BY matches DESC, subcnt DESC LIMIT ?"
	args = append(args, a.maxResults)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// Get 用户 matched by tags, sort by number of matches from high to low.
	rows, err := a.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Read results as 订阅.
	var public, trusted any
	var access t.DefaultAccess
	var subcnt int
	var setTags t.StringSlice
	var ignored int
	var isChan bool
	var sub t.Subscription
	var subs []t.Subscription
	for rows.Next() {
		if err = rows.Scan(&sub.Topic, &sub.CreatedAt, &sub.UpdatedAt, &isChan, &access, &subcnt,
			&public, &trusted, &setTags, &ignored); err != nil {
			subs = nil
			break
		}

		if id, err := strconv.ParseInt(sub.Topic, 10, 64); err == nil {
			sub.Topic = store.EncodeUid(id).UserId()
			if sub.Topic == caller {
				// 跳过调用者自身。
				continue
			}
		}

		if isChan {
			// 这是一个 Channel，将 grp 转换为 chn 名称：所有支持 Channel 的
			// Topic 在搜索结果中应显示为 Channel。
			sub.Topic = t.GrpToChn(sub.Topic)
		}

		sub.SetSubCnt(subcnt)
		sub.SetPublic(common.FromJSON(public))
		sub.SetTrusted(common.FromJSON(trusted))
		sub.SetDefaultAccess(access.Auth, access.Anon)
		// 表示模式未设置，不是 'N'。
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = common.FilterFoundTags(setTags, index)
		subs = append(subs, sub)
	}
	if err == nil {
		err = rows.Err()
	}

	return subs, err
}

// FindByName 按公开 alias 子串发现用户，并按 alias 或 Public.fn 发现公开 Topic。
func (a *adapter) FindByName(caller string, search *t.PeerSearchQuery) ([]t.Subscription, error) {
	if search == nil || search.Query == "" {
		return nil, nil
	}
	needle := common.EscapeLike(strings.ToLower(search.Query))
	aliasPattern := strings.ToLower(search.AliasPrefix) + ":%" + needle + "%"
	namePattern := "%" + needle + "%"
	stateFilter := ""
	var userArgs []any
	if search.ActiveOnly {
		stateFilter = "u.state=? AND "
		userArgs = append(userArgs, t.StateOK)
	}
	aliasConstraint := "FALSE"
	if search.AliasPrefix != "" {
		aliasConstraint = "EXISTS(SELECT 1 FROM usertags ut WHERE ut.userid=u.id AND LOWER(ut.tag) LIKE ? ESCAPE '!')"
		userArgs = append(userArgs, aliasPattern)
	}
	userArgs = append(userArgs, a.maxResults)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	userRows, err := a.db.QueryxContext(ctx,
		"SELECT CAST(u.id AS CHAR) AS topic,u.createdat,u.updatedat,u.access,u.public,u.trusted,u.tags"+
			" FROM users u WHERE "+stateFilter+aliasConstraint+" LIMIT ?", userArgs...)
	if err != nil {
		return nil, err
	}
	var found []t.Subscription
	for userRows.Next() {
		var rawTopic string
		var sub t.Subscription
		var access t.DefaultAccess
		var public, trusted any
		var tags t.StringSlice
		if err = userRows.Scan(&rawTopic, &sub.CreatedAt, &sub.UpdatedAt, &access, &public, &trusted, &tags); err != nil {
			break
		}
		id, parseErr := strconv.ParseInt(rawTopic, 10, 64)
		if parseErr != nil {
			continue
		}
		sub.Topic = store.EncodeUid(id).UserId()
		if sub.Topic == caller {
			continue
		}
		decodedPublic := common.FromJSON(public)
		score, matched := common.RankPeerSearch(sub.Topic, search.Query, search.AliasPrefix, tags, decodedPublic)
		if score == 0 {
			continue
		}
		sub.SetPublic(decodedPublic)
		sub.SetTrusted(common.FromJSON(trusted))
		sub.SetDefaultAccess(access.Auth, access.Anon)
		sub.SetSearchScore(score)
		sub.Private = matched
		found = append(found, sub)
	}
	if closeErr := userRows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}

	stateFilter = ""
	var topicArgs []any
	if search.ActiveOnly {
		stateFilter = "t.state=? AND "
		topicArgs = append(topicArgs, t.StateOK)
	}
	aliasConstraint = "FALSE"
	if search.AliasPrefix != "" {
		aliasConstraint = "EXISTS(SELECT 1 FROM topictags tt WHERE tt.topic=t.name AND LOWER(tt.tag) LIKE ? ESCAPE '!')"
		topicArgs = append(topicArgs, aliasPattern)
	}
	topicArgs = append(topicArgs, namePattern, a.maxResults)
	topicRows, err := a.db.QueryxContext(ctx,
		"SELECT t.name,t.createdat,t.updatedat,t.usebt,t.access,t.subcnt,t.public,t.trusted,t.tags"+
			" FROM topics t WHERE "+stateFilter+"("+aliasConstraint+
			" OR LOWER(COALESCE(JSON_UNQUOTE(JSON_EXTRACT(t.public,'$.fn')),'')) LIKE ? ESCAPE '!') LIMIT ?",
		topicArgs...)
	if err != nil {
		return nil, err
	}
	defer topicRows.Close()
	for topicRows.Next() {
		var sub t.Subscription
		var isChan bool
		var access t.DefaultAccess
		var subCnt int
		var public, trusted any
		var tags t.StringSlice
		if err = topicRows.Scan(&sub.Topic, &sub.CreatedAt, &sub.UpdatedAt, &isChan, &access,
			&subCnt, &public, &trusted, &tags); err != nil {
			break
		}
		decodedPublic := common.FromJSON(public)
		score, matched := common.RankPeerSearch(sub.Topic, search.Query, search.AliasPrefix, tags, decodedPublic)
		if score == 0 {
			continue
		}
		if isChan {
			sub.Topic = t.GrpToChn(sub.Topic)
		}
		sub.SetSubCnt(subCnt)
		sub.SetPublic(decodedPublic)
		sub.SetTrusted(common.FromJSON(trusted))
		sub.SetDefaultAccess(access.Auth, access.Anon)
		sub.SetSearchScore(score)
		sub.Private = matched
		found = append(found, sub)
	}
	if err == nil {
		err = topicRows.Err()
	}
	return found, err
}

// FindOne returns the first Topic or 用户 which matches the given tag.
func (a *adapter) FindOne(tag string) (string, error) {
	var args []any

	query := "SELECT t.name AS topic FROM topics AS t LEFT JOIN topictags AS tt ON t.name=tt.topic " +
		"WHERE tt.tag=?"
	args = append(args, tag)

	query += " UNION ALL "

	query += "SELECT u.id AS topic FROM users AS u LEFT JOIN usertags AS ut ON ut.userid=u.id " +
		"WHERE ut.tag=?"
	args = append(args, tag)

	// LIMIT 应用于所有结果行。
	query += " LIMIT 1"

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	rows, err := a.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var found string
	if rows.Next() {
		if err = rows.Scan(&found); err != nil {
			return "", err
		}

		// 检查是否 the found value is a Topic name or a 用户 ID.
		// 用户 IDs are returned as decoded decimal strings.
		if id, err := strconv.ParseInt(found, 10, 64); err == nil {
			found = store.EncodeUid(id).UserId()
		}
	}
	if err == nil {
		err = rows.Err()
	}

	return found, err
}
