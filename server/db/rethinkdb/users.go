//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	"errors"
	"strconv"
	"time"

	"chat/server/auth"
	"chat/server/db/common"
	"chat/server/logs"
	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// UserCreate 创建新用户。返回错误，如果是重复用户名则错误为 true，
// 其他错误为 false
func (a *adapter) UserCreate(user *t.User) error {
	_, err := rdb.DB(a.dbName).Table("users").Insert(&user).RunWrite(a.conn)
	return err
}

// AuthAddRecord 添加用户的认证记录
func (a *adapter) AuthAddRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {

	_, err := rdb.DB(a.dbName).Table("auth").Insert(
		&common.AuthRecord{
			Unique:  unique,
			UserId:  uid.String(),
			Scheme:  scheme,
			AuthLvl: authLvl,
			Secret:  secret,
			Expires: expires}).RunWrite(a.conn)
	if err != nil {
		if rdb.IsConflictErr(err) {
			return t.ErrDuplicate
		}
		return err
	}
	return nil
}

// AuthDelScheme 删除用户的现有认证方案。
func (a *adapter) AuthDelScheme(uid t.Uid, scheme string) error {
	_, err := rdb.DB(a.dbName).Table("auth").
		GetAllByIndex("userid", uid.String()).
		Filter(map[string]any{"scheme": scheme}).
		Delete().RunWrite(a.conn)
	return err
}

// AuthDelAllRecords 删除用户的所有认证记录
func (a *adapter) AuthDelAllRecords(uid t.Uid) (int, error) {
	res, err := rdb.DB(a.dbName).Table("auth").GetAllByIndex("userid", uid.String()).Delete().RunWrite(a.conn)
	return res.Deleted, err
}

// AuthUpdRecord 更新用户的认证密钥。
func (a *adapter) AuthUpdRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {
	// 'unique' 用作主键（RethinkDB 中确保唯一性的唯一方式）。
	// 主键不可变。如果 'unique' 已更改，必须用新记录替换旧记录：
	// 1. 检查 'unique' 是否已更改。
	// 2. 如果没有，通过 'unique' 执行更新
	// 3. 如果是，先插入新记录（可能因 'unique' 重复而失败），然后删除旧记录。

	// 获取旧的 'unique'
	cursor, err := rdb.DB(a.dbName).Table("auth").GetAllByIndex("userid", uid.String()).
		Filter(map[string]any{"scheme": scheme}).
		Pluck("unique").Default(nil).Run(a.conn)
	if err != nil {
		if isNoResults(err) {
			return t.ErrNotFound
		}
		return err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		// 如果记录未找到，不更新
		return t.ErrNotFound
	}

	var record common.AuthRecord
	if err = cursor.One(&record); err != nil {
		return err
	}
	if record.Unique == unique {
		// Unique 未更改
		upd := map[string]any{
			"authLvl": authLvl,
		}
		if len(secret) > 0 {
			upd["secret"] = secret
		}
		if !expires.IsZero() {
			upd["expires"] = expires
		}
		_, err = rdb.DB(a.dbName).Table("auth").Get(unique).Update(upd).RunWrite(a.conn)
	} else {
		// Unique 已更改。插入-删除。
		// 不支持事务 :(
		if len(secret) == 0 {
			secret = record.Secret
		}
		if expires.IsZero() {
			expires = record.Expires
		}
		err = a.AuthAddRecord(uid, scheme, unique, authLvl, secret, expires)
		if err == nil {
			// 这里对错误无能为力。
			rdb.DB(a.dbName).Table("auth").Get(record.Unique).Delete().RunWrite(a.conn)
		}
	}
	return err
}

// AuthGetRecord 通过用户 ID 和方案检索用户的认证记录。
func (a *adapter) AuthGetRecord(uid t.Uid, scheme string) (string, auth.Level, []byte, time.Time, error) {
	// Default() 用于防止 Pluck 返回错误
	cursor, err := rdb.DB(a.dbName).Table("auth").GetAllByIndex("userid", uid.String()).
		Filter(map[string]any{"scheme": scheme}).
		Pluck("unique", "secret", "expires", "authLvl").Default(nil).Run(a.conn)
	if err != nil {
		return "", 0, nil, time.Time{}, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return "", 0, nil, time.Time{}, t.ErrNotFound
	}

	var record struct {
		Unique  string     `json:"unique"`
		AuthLvl auth.Level `json:"authLvl"`
		Secret  []byte     `json:"secret"`
		Expires time.Time  `json:"expires"`
	}

	if err = cursor.One(&record); err != nil {
		return "", 0, nil, time.Time{}, err
	}
	// 转换为 UTC（gorethink 的 bug？）
	record.Expires = record.Expires.UTC()
	return record.Unique, record.AuthLvl, record.Secret, record.Expires, nil
}

// AuthGetUniqueRecord 通过唯一值（如登录名）检索用户的认证记录。
func (a *adapter) AuthGetUniqueRecord(unique string) (t.Uid, auth.Level, []byte, time.Time, error) {
	// Default() 用于防止 Pluck 返回错误
	cursor, err := rdb.DB(a.dbName).Table("auth").Get(unique).Pluck(
		"userid", "secret", "expires", "authLvl").Default(nil).Run(a.conn)
	if err != nil {
		return t.ZeroUid, 0, nil, time.Time{}, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return t.ZeroUid, 0, nil, time.Time{}, nil
	}

	var record struct {
		Userid  string     `json:"userid"`
		AuthLvl auth.Level `json:"authLvl"`
		Secret  []byte     `json:"secret"`
		Expires time.Time  `json:"expires"`
	}

	if err = cursor.One(&record); err != nil {
		return t.ZeroUid, 0, nil, time.Time{}, err
	}

	return t.ParseUid(record.Userid), record.AuthLvl, record.Secret, record.Expires.UTC(), nil
}

// UserGet 通过用户 ID 获取单个用户。如果用户不存在则返回 (nil, nil)
func (a *adapter) UserGet(uid t.Uid) (*t.User, error) {
	cursor, err := rdb.DB(a.dbName).Table("users").GetAll(uid.String()).
		Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var user t.User
	if err = cursor.One(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

// UserGetAll 通过 UID 获取多条用户记录。
func (a *adapter) UserGetAll(ids ...t.Uid) ([]t.User, error) {
	uids := make([]any, len(ids))
	for i, id := range ids {
		uids[i] = id.String()
	}

	users := []t.User{}
	cursor, err := rdb.DB(a.dbName).Table("users").GetAll(uids...).
		Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var user t.User
	for cursor.Next(&user) {
		// 将时间戳转换为 UTC（gorethink 返回的是 +0000 格式）
		user.CreatedAt = user.CreatedAt.UTC()
		user.UpdatedAt = user.UpdatedAt.UTC()
		if user.StateAt != nil {
			stateAt := user.StateAt.UTC()
			user.StateAt = &stateAt
		}
		users = append(users, user)
	}

	return users, cursor.Err()
}

// UserDelete 删除用户记录。
func (a *adapter) UserDelete(uid t.Uid, hard bool) error {
	// 获取用户拥有的 Topic 名称列表（'grp' 和 'chn'）。
	ownTopics, err := a.topicNamesForUser(rdb.DB(a.dbName).Table("topics").
		GetAllByIndex("Owner", uid.String()).Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).
		Field("Id"), true)
	if err != nil {
		logs.Err.Println("UserDelete: cannot get user's own topics:", err)
		return err
	}

	scheduledQuery := rdb.DB(a.dbName).Table("scheduledmessages").
		Filter(func(row rdb.Term) any {
			return row.Field("From").Eq(uid.String()).
				Or(rdb.Expr(ownTopics).Contains(row.Field("Topic")))
		})
	if err = a.decFileUseCounter(scheduledQuery); err != nil {
		return err
	}
	if _, err = scheduledQuery.Delete().RunWrite(a.conn); err != nil {
		return err
	}

	if hard {
		// 用户的设备存储在用户记录中，没有单独的表。
		// 删除用户在所有 Topic 中的订阅。
		if err = a.subsDelForUser(uid, true); err != nil {
			return err
		}

		// 删除用户在所有 Topic 中软删除的消息记录
		// 以及 dellog 条目。
		if err = a.clearUserDellog(uid, nil); err != nil {
			return err
		}

		// 不能删除用户在所有 Topic 中的消息，因为无法通知 Topic 此类删除。
		// 只保留消息标记为由“未找到”用户发送。

		// 删除用户作为所有者的 Topic：

		if len(ownTopics) > 0 {
			// 1. 删除 dellog
			// 2. 减少 fileuploads 的使用计数：Topic 本身和消息。
			// 3. 删除所有消息。
			// 4. 删除订阅。
			if _, err = rdb.DB(a.dbName).Table("topics").GetAll(ownTopics...).ForEach(
				func(topic rdb.Term) rdb.Term {
					return rdb.Expr([]any{
						// 删除 dellog
						rdb.DB(a.dbName).Table("dellog").Between(
							[]any{topic.Field("Id"), rdb.MinVal},
							[]any{topic.Field("Id"), rdb.MaxVal},
							rdb.BetweenOpts{Index: "Topic_DelId"}).Delete(),
						// 减少 Topic 附件的 UseCounter
						rdb.DB(a.dbName).Table("fileuploads").GetAll(topic.Field("Attachments")).
							Update(func(fu rdb.Term) any {
								return map[string]any{"UseCount": fu.Field("UseCount").Default(1).Sub(1)}
							}),
						// 减少消息附件的 UseCounter
						rdb.DB(a.dbName).Table("fileuploads").GetAll(
							rdb.Args(
								rdb.DB(a.dbName).Table("messages").Between(
									[]any{topic.Field("Id"), rdb.MinVal},
									[]any{topic.Field("Id"), rdb.MaxVal},
									rdb.BetweenOpts{Index: "Topic_SeqId"}).
									// 仅获取有附件的消息
									Filter(func(msg rdb.Term) rdb.Term {
										return msg.HasFields("Attachments")
									}).
									// 扁平化数组
									ConcatMap(func(row rdb.Term) any { return row.Field("Attachments") }).
									CoerceTo("array"))).
							Update(func(fu rdb.Term) any {
								return map[string]any{"UseCount": fu.Field("UseCount").Default(1).Sub(1)}
							}),
						// 删除消息
						rdb.DB(a.dbName).Table("messages").Between(
							[]any{topic.Field("Id"), rdb.MinVal},
							[]any{topic.Field("Id"), rdb.MaxVal},
							rdb.BetweenOpts{Index: "Topic_SeqId"}).Delete(),
						// 删除订阅
						rdb.DB(a.dbName).Table("subscriptions").
							GetAllByIndex("Topic", topic.Field("Id")).Delete(),
					})
				}).RunWrite(a.conn); err != nil {
				return err
			}

			// 最后删除 Topic。
			if _, err = rdb.DB(a.dbName).Table("topics").GetAllByIndex("Owner", uid.String()).
				Delete().RunWrite(a.conn); err != nil {
				return err
			}
		}

		// 删除用户的认证记录。
		if _, err = a.AuthDelAllRecords(uid); err != nil {
			return err
		}

		// 删除凭据。
		if err = a.CredDel(uid, "", ""); err != nil && err != t.ErrNotFound {
			return err
		}

		// 必须使用 GetAll 以产生 decFileUseCounter 期望的数组结果。
		q := rdb.DB(a.dbName).Table("users").GetAll(uid.String())

		// 取消关联用户的附件。
		if err = a.decFileUseCounter(q); err != nil {
			return err
		}

		// 最后删除用户。
		_, err = q.Delete().RunWrite(a.conn)
	} else {
		// 禁用用户的订阅。
		if err = a.subsDelForUser(uid, false); err != nil {
			logs.Err.Println("UserDelete: subsDelForUser:", err)
			return err
		}

		now := t.TimeNow()
		disable := map[string]any{
			"UpdatedAt": now,
			"State":     t.StateDeleted,
			"StateAt":   now,
		}
		disableSub := map[string]any{
			"UpdatedAt": now,
			"DeletedAt": now,
		}
		if len(ownTopics) > 0 {
			// 禁用用户作为所有者的 Topic 中的所有订阅。
			if _, err = rdb.DB(a.dbName).Table("subscriptions").
				GetAllByIndex("Topic", ownTopics...).
				Update(disableSub).
				RunWrite(a.conn); err != nil {
				return err
			}

			// 禁用用户作为所有者的 Topic。
			if _, err = rdb.DB(a.dbName).Table("topics").
				GetAll(ownTopics...).
				Update(disable).
				RunWrite(a.conn); err != nil {
				return err
			}
		}

		// 禁用与该用户的 p2p Topic。
		p2pTopics, err := a.p2pTopicsForUser(uid)
		if err != nil {
			logs.Err.Println("UserDelete: p2pTopics:", err)
			return err
		}
		if len(p2pTopics) > 0 {
			// 禁用与该用户的 p2p Topic 中的所有订阅。
			if _, err = rdb.DB(a.dbName).Table("subscriptions").
				GetAllByIndex("Topic", p2pTopics...).
				Update(disableSub).
				RunWrite(a.conn); err != nil {
				return err
			}
			// 禁用与该用户的 p2p Topic。
			if _, err = rdb.DB(a.dbName).Table("topics").
				GetAll(p2pTopics...).
				Update(disable).
				RunWrite(a.conn); err != nil {
				return err
			}
		}

		// 禁用用户（与 Topic 相同的字段）。
		_, err = rdb.DB(a.dbName).Table("users").Get(uid.String()).
			Update(disable).RunWrite(a.conn)
	}
	return err
}

// 删除用户在所有 Topic 中软删除的消息记录。
func (a *adapter) clearUserDellog(uid t.Uid, topics []any) error {
	var err error
	forUser := uid.String()
	if topics == nil {
		// 获取用户有订阅的所有 Topic 列表。
		topics, err = a.topicNamesForUser(rdb.DB(a.dbName).
			Table("subscriptions").
			GetAllByIndex("User", forUser).
			Field("Topic"), false)
		if err != nil {
			return err
		}
	}

	// 无需转换 Channel 名称为 group 名称：
	// Channel 读者不能删除消息。

	// 从消息的软删除列表中移除当前用户
	// （在用户有订阅的所有 Topic 中）。
	_, err = rdb.DB(a.dbName).Table("topics").GetAll(topics...).
		ForEach(func(topic rdb.Term) rdb.Term {
			return rdb.DB(a.dbName).Table("messages").Between(
				[]any{topic.Field("Id"), forUser, rdb.MinVal},
				[]any{topic.Field("Id"), forUser, rdb.MaxVal},
				rdb.BetweenOpts{Index: "Topic_DeletedFor"}).
				Update(map[string]any{
					// 取 DeletedFor 数组，减去所有包含当前用户 ID 的值。
					"DeletedFor": func(msg rdb.Term) rdb.Term {
						return msg.Field("DeletedFor").
							SetDifference(msg.Field("DeletedFor").Filter(map[string]any{"User": forUser}))
					},
				})
		}).RunWrite(a.conn)
	if err != nil {
		return err
	}

	// 删除 dellog 中该用户在所有有订阅的 Topic 中的条目。
	_, err = rdb.DB(a.dbName).Table("topics").GetAll(topics...).
		ForEach(func(topic rdb.Term) rdb.Term {
			return rdb.DB(a.dbName).Table("dellog").
				// 选择给定表的所有日志条目。
				Between(
					[]any{topic.Field("Id"), rdb.MinVal},
					[]any{topic.Field("Id"), rdb.MaxVal},
					rdb.BetweenOpts{Index: "Topic_DelId"}).
				// 仅保留为当前用户软删除的条目以待删除。
				Filter(func(dle rdb.Term) rdb.Term { return dle.Field("DeletedFor").Eq(forUser) }).
				// 删除它们。
				Delete()
		}).RunWrite(a.conn)

	return err
}

// topicNamesForUser 通过查询返回 Topic 名称列表。
func (a *adapter) topicNamesForUser(query rdb.Term, includeChan bool) ([]any, error) {
	cursor, err := query.Run(a.conn)
	if err != nil {
		if isNoResults(err) {
			return nil, nil
		}
		return nil, err
	}
	defer cursor.Close()

	var result []string
	if err = cursor.All(&result); err != nil {
		return nil, err
	}

	var args []any
	for _, name := range result {
		args = append(args, name)
		if includeChan {
			// 为每个 'grp' 名称追加 'chn' Topic 名称。
			if channel := t.GrpToChn(name); channel != "" {
				args = append(args, channel)
			}
		}
	}
	return args, nil
}

// p2pTopicsForUser 完成p2pTopicsFor用户所需的内部处理。
func (a *adapter) p2pTopicsForUser(uid t.Uid) ([]any, error) {
	return a.topicNamesForUser(rdb.DB(a.dbName).Table("subscriptions").
		GetAllByIndex("User", uid.String()).
		Field("Topic").
		Filter(rdb.Row.Field("Topic").Match("^p2p")), false)
}

// topicStateForUser 由 UserUpdate 在更新包含状态更改时调用。
func (a *adapter) topicStateForUser(uid t.Uid, now time.Time, update any) error {
	state, ok := update.(t.ObjState)
	if !ok {
		return t.ErrMalformed
	}

	if now.IsZero() {
		now = t.TimeNow()
	}

	// 更改用户作为所有者的所有 Topic 的状态。
	if _, err := rdb.DB(a.dbName).Table("topics").
		GetAllByIndex("Owner", uid.String()).
		Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).
		Update(map[string]any{
			"State":   state,
			"StateAt": now,
		}).RunWrite(a.conn); err != nil {
		return err
	}

	// 更改与该用户的 p2p Topic 的状态（p2p Topic 的 owner 为空）
	/*
		r.db('im').table('topics').getAll(
			r.args(
				r.db("im").table("subscriptions").getAll('S8VFqRpXw5M', {index: 'User'})('Topic').coerceTo('array')
			)
		).update(...)
	*/
	if _, err := rdb.DB(a.dbName).Table("topics").
		GetAll(rdb.Args(
			rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", uid.String()).
				Field("Topic").CoerceTo("array"))).
		Filter(rdb.Row.Field("Owner").Eq("").And(rdb.Row.Field("State").Eq(t.StateDeleted).Not())).
		Update(map[string]any{
			"State":   state,
			"StateAt": now,
		}).RunWrite(a.conn); err != nil {
		return err
	}

	// 订阅不需要更新：
	// 已禁用用户的订阅不会被禁用，仍然可以操作。

	return nil
}

// UserUpdate 更新用户对象。
func (a *adapter) UserUpdate(uid t.Uid, update map[string]any) error {
	_, err := rdb.DB(a.dbName).Table("users").Get(uid.String()).Update(update).RunWrite(a.conn)
	if err != nil {
		return err
	}

	if state, ok := update["State"]; ok {
		now, _ := update["StateAt"].(time.Time)
		err = a.topicStateForUser(uid, now, state)
	}

	return err
}

// UserUpdateTags 追加或重置用户的标签
func (a *adapter) UserUpdateTags(uid t.Uid, add, remove, reset []string) ([]string, error) {
	// 与 nil 比较而不是检查零长度：零长度重置是有效的。
	if reset != nil {
		// 用新值替换 Tags
		return reset, a.UserUpdate(uid, map[string]any{"Tags": reset})
	}

	// 变更标签列表。

	newTags := rdb.Row.Field("Tags")
	if len(add) > 0 {
		newTags = newTags.SetUnion(add)
	}
	if len(remove) > 0 {
		newTags = newTags.SetDifference(remove)
	}

	q := rdb.DB(a.dbName).Table("users").Get(uid.String())
	_, err := q.Update(map[string]any{"Tags": newTags}).RunWrite(a.conn)
	if err != nil {
		return nil, err
	}

	// 获取新标签。
	// 使用 Pluck 而不是 Field，因为 https://github.com/rethinkdb/rethinkdb-go/issues/486
	cursor, err := q.Pluck("Tags").Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var tagsField struct{ Tags []string }
	err = cursor.One(&tagsField)
	if err != nil {
		return nil, err
	}
	if len(tagsField.Tags) == 0 {
		tagsField.Tags = nil
	}
	return tagsField.Tags, nil
}

// UserGetByCred 返回给定已验证凭据的用户 ID。
func (a *adapter) UserGetByCred(method, value string) (t.Uid, error) {
	cursor, err := rdb.DB(a.dbName).Table("credentials").Get(method + ":" + value).Field("User").Default(nil).Run(a.conn)
	if err != nil {
		return t.ZeroUid, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return t.ZeroUid, nil
	}

	var userId string
	if err = cursor.One(&userId); err != nil {
		return t.ZeroUid, err
	}

	return t.ParseUid(userId), nil
}

// UserUnreadCount 返回所有具有 R 权限的 Topic 中未读消息的总数。
// 如果读取失败，仍会返回带有原始用户 ID 的计数，
// 但未读计数未定义且错误非 nil。
// UserUnreadCount 不统计 Channel 中的未读消息（尽管应该统计）。
func (a *adapter) UserUnreadCount(ids ...t.Uid) (map[t.Uid]int, error) {
	// 调用期望用户 ID 为纯字符串，如 "356zaYaumiU"。
	uids := make([]any, len(ids))
	counts := make(map[t.Uid]int, len(ids))
	for i, id := range ids {
		uids[i] = id.String()
		// 确保所有原始 uid 始终存在。
		counts[id] = 0
	}

	/*
		Query:
			r.db("im").table("subscriptions").getAll("356zaYaumiU", "k4cvfaq8zCQ", {index: "User"})
			  .eqJoin("Topic", r.db("im").table("topics"), {index: "Id"})
			  .filter(
			    r.not(r.row.hasFields({"left": "DeletedAt"}).or(r.row("right")("State").eq(20)))
			  )
			  .zip()
			  .pluck("User", "ReadSeqId", "ModeWant", "ModeGiven", "SeqId")
			  .filter(r.js('(function(row) {return row.ModeWant&row.ModeGiven&1 > 0;})'))
			  .group("User")
			  .sum(function(x) {return x.getField("SeqId").sub(x.getField("ReadSeqId"));})

		Result:
				[{group: "356zaYaumiU", reduction: 1}, {group: "k4cvfaq8zCQ", reduction: 0}]
	*/
	cursor, err := rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", uids...).
		EqJoin("Topic", rdb.DB(a.dbName).Table("topics"), rdb.EqJoinOpts{Index: "Id"}).
		// left: 订阅; right: Topic。
		Filter(
			rdb.Not(rdb.Row.HasFields(map[string]any{"left": "DeletedAt"}).
				Or(rdb.Row.Field("right").Field("State").Eq(t.StateDeleted)))).
		Zip().
		Pluck("User", "ReadSeqId", "ModeWant", "ModeGiven", "SeqId").
		Filter(rdb.JS("(function(row) {return (row.ModeWant & row.ModeGiven & " + strconv.Itoa(int(t.ModeRead)) + ") > 0;})")).
		Group("User").
		Sum(func(row rdb.Term) rdb.Term { return row.Field("SeqId").Sub(row.Field("ReadSeqId")) }).
		Run(a.conn)
	if err != nil {
		return counts, err
	}
	defer cursor.Close()

	var oneCount struct {
		Group     string
		Reduction int
	}
	for cursor.Next(&oneCount) {
		counts[t.ParseUid(oneCount.Group)] = oneCount.Reduction
	}
	err = cursor.Err()

	return counts, err
}

// UserGetUnvalidated 返回从未登录、没有已验证凭据
// 且自 lastUpdatedBefore 以来未更新过的 uid 列表。
func (a *adapter) UserGetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]t.Uid, error) {
	/*
		Query:
			r.db('im').table('users')
				.filter(r.row('LastSeen').eq(null).and(r.row('UpdatedAt').lt('Mar 31 2022 01:03:38')))
				.eqJoin('Id', r.db('im').table('credentials'), {index: 'User'}).zip()
				.pluck('User', 'Done')
				.group('User')
				.sum(function(row) {return r.branch(row('Done'), 1, 0)})
				.ungroup()
				.filter({reduction: 0})
				.pluck('group').limit(10)

		Result: [{"group": "3W1hPuHjobg"}, {"group": "Fh_skXNRhVg"}, {"group": "NqMZzq0ajWk"}]
	*/
	cursor, err := rdb.DB(a.dbName).Table("users").
		Filter(rdb.Row.Field("LastSeen").Eq(nil).And(rdb.Row.Field("UpdatedAt").Lt(lastUpdatedBefore))).
		EqJoin("Id", rdb.DB(a.dbName).Table("credentials"), rdb.EqJoinOpts{Index: "User"}).Zip().
		Pluck("User", "Done").
		Group("User").
		Sum(func(row rdb.Term) rdb.Term { return rdb.Branch(row.Field("Done"), 1, 0) }).
		Ungroup().
		Filter(rdb.Row.Field("reduction").Eq(0)).
		Pluck("group").
		Limit(limit).
		Run(a.conn)

	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var rec struct {
		Group string
	}

	var uids []t.Uid
	for cursor.Next(&rec) {
		uid := t.ParseUid(rec.Group)
		if !uid.IsZero() {
			uids = append(uids, uid)
		} else {
			return nil, errors.New("bad uid field")
		}
	}

	err = cursor.Err()

	return uids, err
}
