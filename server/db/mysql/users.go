//go:build mysql || (!postgres && !mongodb && !rethinkdb)
// +build mysql !postgres,!mongodb,!rethinkdb

package mysql

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	"github.com/jmoiron/sqlx"
)

// UserCreate 创建新用户。如果错误是由于用户名重复导致的，返回错误和 true，
// 其他错误返回 false
func (a *adapter) UserCreate(user *t.User) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	decoded_uid := store.DecodeUid(user.Uid())
	if _, err = tx.Exec("INSERT INTO users(id,createdat,updatedat,state,access,public,trusted,tags) VALUES(?,?,?,?,?,?,?,?)",
		decoded_uid,
		user.CreatedAt,
		user.UpdatedAt,
		user.State,
		user.Access,
		common.ToJSON(user.Public),
		common.ToJSON(user.Trusted),
		user.Tags); err != nil {
		return err
	}

	// 保存用户的标签到单独的表以便用户可以被搜索到。
	if err = addTags(tx, "usertags", "userid", decoded_uid, user.Tags, false); err != nil {
		return err
	}

	return tx.Commit()
}

// 添加用户的认证记录
func (a *adapter) AuthAddRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {

	var exp *time.Time
	if !expires.IsZero() {
		exp = &expires
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	if _, err := a.db.ExecContext(ctx, "INSERT INTO auth(uname,userid,scheme,authLvl,secret,expires) VALUES(?,?,?,?,?,?)",
		unique, store.DecodeUid(uid), scheme, authLvl, secret, exp); err != nil {
		if isDupe(err) {
			return t.ErrDuplicate
		}
		return err
	}
	return nil
}

// AuthDelScheme 删除用户的现有认证方案。
func (a *adapter) AuthDelScheme(user t.Uid, scheme string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx, "DELETE FROM auth WHERE userid=? AND scheme=?", store.DecodeUid(user), scheme)
	return err
}

// AuthDelAllRecords 删除用户的所有认证记录。
func (a *adapter) AuthDelAllRecords(user t.Uid) (int, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	res, err := a.db.ExecContext(ctx, "DELETE FROM auth WHERE userid=?", store.DecodeUid(user))
	if err != nil {
		return 0, err
	}
	count, _ := res.RowsAffected()

	return int(count), nil
}

// 更新用户的认证唯一值、密钥、认证级别。
func (a *adapter) AuthUpdRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {

	params := []string{"authLvl=?"}
	args := []any{authLvl}

	if unique != "" {
		params = append(params, "uname=?")
		args = append(args, unique)
	}
	if len(secret) > 0 {
		params = append(params, "secret=?")
		args = append(args, secret)
	}
	if !expires.IsZero() {
		params = append(params, "expires=?")
		args = append(args, expires)
	}
	args = append(args, store.DecodeUid(uid), scheme)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	sql := "UPDATE auth SET " + strings.Join(params, ",") + " WHERE userid=? AND scheme=?"
	resp, err := a.db.ExecContext(ctx, sql, args...)
	if isDupe(err) {
		return t.ErrDuplicate
	}

	if count, _ := resp.RowsAffected(); count <= 0 {
		return t.ErrNotFound
	}

	return err
}

// 获取用户的认证记录
func (a *adapter) AuthGetRecord(uid t.Uid, scheme string) (string, auth.Level, []byte, time.Time, error) {
	var expires time.Time

	var record struct {
		Uname   string
		Authlvl auth.Level
		Secret  []byte
		Expires *time.Time
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	if err := a.db.GetContext(ctx, &record, "SELECT uname,secret,expires,authlvl FROM auth WHERE userid=? AND scheme=?",
		store.DecodeUid(uid), scheme); err != nil {
		if err == sql.ErrNoRows {
			// 未找到 - 使用标准错误。
			err = t.ErrNotFound
		}
		return "", 0, nil, expires, err
	}

	if record.Expires != nil {
		expires = *record.Expires
	}

	return record.Uname, record.Authlvl, record.Secret, expires, nil
}

// 获取用户的认证记录
func (a *adapter) AuthGetUniqueRecord(unique string) (t.Uid, auth.Level, []byte, time.Time, error) {
	var expires time.Time

	var record struct {
		Userid  int64
		Authlvl auth.Level
		Secret  []byte
		Expires *time.Time
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	if err := a.db.GetContext(ctx, &record, "SELECT userid,secret,expires,authlvl FROM auth WHERE uname=?", unique); err != nil {
		if err == sql.ErrNoRows {
			// 未找到 - 清除错误
			err = nil
		}
		return t.ZeroUid, 0, nil, expires, err
	}

	if record.Expires != nil {
		expires = *record.Expires
	}

	return store.EncodeUid(record.Userid), record.Authlvl, record.Secret, expires, nil
}

// UserGet 按用户 ID 获取单个用户。如果用户不存在返回 (nil, nil)
func (a *adapter) UserGet(uid t.Uid) (*t.User, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var user t.User
	err := a.db.GetContext(ctx, &user, "SELECT * FROM users WHERE id=? AND state!=?", store.DecodeUid(uid), t.StateDeleted)
	if err == nil {
		user.SetUid(uid)
		user.Public = common.FromJSON(user.Public)
		user.Trusted = common.FromJSON(user.Trusted)
		return &user, nil
	}

	if err == sql.ErrNoRows {
		// 如果用户不存在或标记为软删除则清除错误。
		return nil, nil
	}

	return nil, err
}

// UserGetAll 完成用户GetAll所需的内部处理。
func (a *adapter) UserGetAll(ids ...t.Uid) ([]t.User, error) {
	uids := make([]any, len(ids))
	for i, id := range ids {
		if id.IsZero() {
			continue
		}
		uids[i] = store.DecodeUid(id)
	}

	users := []t.User{}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	q, uids, _ := sqlx.In("SELECT * FROM users WHERE id IN (?) AND state!=?", uids, t.StateDeleted)
	rows, err := a.db.QueryxContext(ctx, a.db.Rebind(q), uids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var user t.User
		if err = rows.StructScan(&user); err != nil {
			users = nil
			break
		}
		user.SetUid(common.EncodeUidString(user.Id))
		user.Public = common.FromJSON(user.Public)
		user.Trusted = common.FromJSON(user.Trusted)

		users = append(users, user)
	}
	if err == nil {
		err = rows.Err()
	}

	return users, err
}

// UserDelete 删除指定用户：完全擦除（硬删除）或标记为已删除。
func (a *adapter) UserDelete(uid t.Uid, hard bool) error {
	decoded_uid := store.DecodeUid(uid)

	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// 检查用户是否存在以及是否已被软删除
	var state t.ObjState
	if err = tx.QueryRowContext(ctx, "SELECT state FROM users WHERE id=?", decoded_uid).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return t.ErrNotFound
		}
		return err
	}
	if !hard && state == t.StateDeleted {
		return t.ErrNotFound
	}

	query := "SELECT name FROM topics WHERE owner=?"
	args := []any{decoded_uid}
	// 硬删除时，删除所有 Topic，包括之前软删除的。
	if !hard {
		query += " AND state!=?"
		args = append(args, t.StateDeleted)
	}
	// 获取用户拥有的 Topic 名称列表（'grp' 和 'chn' 格式）。
	ownTopics, err := a.topicNamesForUser(query, true, args...)
	if err != nil {
		return err
	}

	now := t.TimeNow()
	if _, err = tx.ExecContext(ctx, "DELETE FROM scheduledmessages WHERE `from`=?", decoded_uid); err != nil {
		return err
	}
	for _, topic := range ownTopics {
		if _, err = tx.ExecContext(ctx, "DELETE FROM scheduledmessages WHERE topic=?", topic); err != nil {
			return err
		}
	}

	if hard {
		// 删除用户的设备
		// t.ErrNotFound = 用户没有设备。
		if err = deviceDelete(tx, uid, ""); err != nil && err != t.ErrNotFound {
			return err
		}

		// 删除用户在所有 Topic 中的订阅。
		if err = subsDelForUser(tx, decoded_uid, true); err != nil {
			return err
		}

		// 删除用户在所有 Topic 中软删除的消息记录。
		if _, err = tx.Exec("DELETE FROM dellog WHERE deletedfor=?", decoded_uid); err != nil {
			return err
		}

		// 无法删除用户在所有 Topic 中的消息，因为无法通知 Topic 此类删除。
		// 只需将消息保留并标记为由“未找到”用户发送。

		// 删除用户作为所有者的 Topic。
		if len(ownTopics) > 0 {
			// 首先删除这些 Topic 中的所有消息。
			if _, err = tx.Exec("DELETE dellog FROM dellog JOIN topics ON topics.name=dellog.topic WHERE topics.owner=?",
				decoded_uid); err != nil {
				return err
			}

			// 消息删除将级联到 filemsglinks，进而到 fileuploads。
			if _, err = tx.Exec("DELETE messages FROM messages JOIN topics ON topics.name=messages.topic WHERE topics.owner=?",
				decoded_uid); err != nil {
				return err
			}

			// 删除用户作为 Topic 所有者的所有用户的订阅。
			sql, args, _ := sqlx.In("DELETE FROM subscriptions AS s WHERE topic IN (?)", ownTopics)
			if _, err = tx.Exec(tx.Rebind(sql), args); err != nil {
				return err
			}

			// 删除 Topic 标签。
			if _, err = tx.Exec("DELETE tt FROM topictags AS tt JOIN topics AS t ON t.name=tt.topic WHERE t.owner=?",
				decoded_uid); err != nil {
				return err
			}

			// 最后删除 Topic。
			if _, err = tx.Exec("DELETE FROM topics WHERE owner=?", decoded_uid); err != nil {
				return err
			}
		}

		// 删除用户的认证记录。
		if _, err = tx.Exec("DELETE FROM auth WHERE userid=?", decoded_uid); err != nil {
			return err
		}

		// 删除所有凭据。
		if err = credDel(tx, uid, "", ""); err != nil && err != t.ErrNotFound {
			return err
		}

		if _, err = tx.Exec("DELETE FROM usertags WHERE userid=?", decoded_uid); err != nil {
			return err
		}

		if _, err = tx.Exec("DELETE FROM users WHERE id=?", decoded_uid); err != nil {
			return err
		}
	} else {
		// 禁用用户的所有订阅。包括 p2p 订阅。无需删除它们。
		if err = subsDelForUser(tx, decoded_uid, false); err != nil {
			return err
		}

		if len(ownTopics) > 0 {
			// 禁用用户作为所有者的 Topic 的所有订阅。
			sql, args, _ := sqlx.In("UPDATE subscriptions SET updatedat=?,deletedat=? WHERE topic IN (?)", now, now, ownTopics)
			if _, err = tx.Exec(tx.Rebind(sql), args...); err != nil {
				return err
			}
		}

		// 禁用用户作为所有者的群组 Topic。
		if _, err = tx.Exec("UPDATE topics SET updatedat=?,touchedat=?,state=?,stateat=? WHERE owner=?",
			now, now, t.StateDeleted, now, decoded_uid); err != nil {
			return err
		}

		// 禁用与该用户的 p2p Topic（p2p Topic 的所有者为 0）。
		if _, err = tx.Exec("UPDATE topics AS t JOIN subscriptions AS s ON t.name=s.topic "+
			"SET t.updatedat=?,t.touchedat=?,t.state=?,t.stateat=? "+
			"WHERE t.owner=0 AND s.userid=? AND t.name LIKE 'p2p%'",
			now, now, t.StateDeleted, now, decoded_uid); err != nil {
			return err
		}

		// 禁用其他用户对已禁用 p2p Topic 的订阅。
		if _, err = tx.Exec("UPDATE subscriptions AS s_one JOIN subscriptions AS s_two "+
			"ON s_one.topic=s_two.topic "+
			"SET s_two.updatedat=?, s_two.deletedat=? WHERE s_one.userid=? AND s_one.topic LIKE 'p2p%'",
			now, now, decoded_uid); err != nil {
			return err
		}

		// 最后禁用用户。
		if _, err = tx.Exec("UPDATE users SET updatedat=?,state=?,stateat=? WHERE id=?",
			now, t.StateDeleted, now, decoded_uid); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// topicStateForUser 由 UserUpdate 在更新包含状态变更时调用。
// 软删除的 Topic 保持软删除状态。
func (a *adapter) topicStateForUser(tx *sqlx.Tx, decoded_uid int64, now time.Time, update any) error {
	var err error

	state, ok := update.(t.ObjState)
	if !ok {
		return t.ErrMalformed
	}

	if now.IsZero() {
		now = t.TimeNow()
	}

	// 变更用户作为所有者的所有 Topic 的状态。
	if _, err = tx.Exec("UPDATE topics SET state=?, stateat=? WHERE owner=? AND state!=?",
		state, now, decoded_uid, t.StateDeleted); err != nil {
		return err
	}

	// 变更与该用户的 p2p Topic 的状态（p2p Topic 的所有者为 0）
	if _, err = tx.Exec("UPDATE topics JOIN subscriptions ON topics.name=subscriptions.topic "+
		"SET topics.state=?, topics.stateat=? WHERE topics.owner=0 AND subscriptions.userid=? AND topics.state!=?",
		state, now, decoded_uid, t.StateDeleted); err != nil {
		return err
	}

	// 订阅无需更新：
	// 已禁用用户的订阅不会被禁用，仍可操作。
	return nil
}

// UserUpdate 更新用户对象。
func (a *adapter) UserUpdate(uid t.Uid, update map[string]any) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	cols, args := common.UpdateByMap(update)
	decoded_uid := store.DecodeUid(uid)
	args = append(args, decoded_uid)
	_, err = tx.Exec("UPDATE users SET "+strings.Join(cols, ",")+" WHERE id=?", args...)
	if err != nil {
		return err
	}

	if state, ok := update["State"]; ok {
		now, _ := update["StateAt"].(time.Time)
		err = a.topicStateForUser(tx, decoded_uid, now, state)
		if err != nil {
			return err
		}
	}

	// 标签也存储在单独的表中
	if tags := common.ExtractTags(update); tags != nil {
		// 首先删除所有用户标签
		_, err = tx.Exec("DELETE FROM usertags WHERE userid=?", decoded_uid)
		if err != nil {
			return err
		}
		// 现在插入新标签
		err = addTags(tx, "usertags", "userid", decoded_uid, tags, false)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// UserUpdateTags 添加、移除或重置用户的标签。
func (a *adapter) UserUpdateTags(uid t.Uid, add, remove, reset []string) ([]string, error) {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	decoded_uid := store.DecodeUid(uid)

	if reset != nil {
		// 重置时先删除所有标签。
		_, err = tx.Exec("DELETE FROM usertags WHERE userid=?", decoded_uid)
		if err != nil {
			return nil, err
		}
		add = reset
		remove = nil
	}

	// 现在插入新标签。重置时忽略重复。
	err = addTags(tx, "usertags", "userid", decoded_uid, add, reset == nil)
	if err != nil {
		return nil, err
	}

	// 删除标签。
	err = removeTags(tx, "usertags", "userid", decoded_uid, remove)
	if err != nil {
		return nil, err
	}

	var allTags []string
	err = tx.Select(&allTags, "SELECT tag FROM usertags WHERE userid=?", decoded_uid)
	if err != nil {
		return nil, err
	}

	_, err = tx.Exec("UPDATE users SET tags=? WHERE id=?", t.StringSlice(allTags), decoded_uid)
	if err != nil {
		return nil, err
	}

	return allTags, tx.Commit()
}

// UserGetByCred 返回给定已验证凭据的用户 ID。
func (a *adapter) UserGetByCred(method, value string) (t.Uid, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var decoded_uid int64
	err := a.db.GetContext(ctx, &decoded_uid, "SELECT userid FROM credentials WHERE synthetic=?", method+":"+value)
	if err == nil {
		return store.EncodeUid(decoded_uid), nil
	}

	if err == sql.ErrNoRows {
		// 如果用户不存在则清除错误
		return t.ZeroUid, nil
	}
	return t.ZeroUid, err
}

// UserUnreadCount 返回所有具有 R 权限的 Topic 中未读消息的总数。
// 如果读取失败，计数仍然返回，但带有原始用户 ID，未读计数未定义且错误非 nil。
// UserUnreadCount 不统计 Channel 中的未读消息，尽管它应该。
func (a *adapter) UserUnreadCount(ids ...t.Uid) (map[t.Uid]int, error) {
	uids := make([]any, len(ids))
	counts := make(map[t.Uid]int, len(ids))
	for i, id := range ids {
		uids[i] = store.DecodeUid(id)
		// 确保所有原始 uid 始终存在。
		counts[id] = 0
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// 联表查询未读消息数：利用 IF/CONCAT 动态支持 Channel (将 chn... 前缀映射为 topics 主表中的 grp...)
	q, args, _ := sqlx.In("SELECT s.userid, SUM(t.seqid)-SUM(s.readseqid) AS unreadcount FROM topics AS t JOIN subscriptions AS s "+
		"ON t.name = IF(LEFT(s.topic, 3) = 'chn', CONCAT('grp', SUBSTRING(s.topic, 4)), s.topic) "+
		"WHERE s.userid IN (?) AND s.deletedat IS NULL AND t.state!=? AND "+
		"INSTR(s.modewant, 'R')>0 AND INSTR(s.modegiven, 'R')>0 GROUP BY s.userid", uids, int(t.StateDeleted))
	rows, err := a.db.QueryxContext(ctx, a.db.Rebind(q), args...)
	if err != nil {
		return counts, err
	}
	defer rows.Close()

	var userId int64
	var unreadCount int
	for rows.Next() {
		if err = rows.Scan(&userId, &unreadCount); err != nil {
			break
		}
		counts[store.EncodeUid(userId)] = unreadCount
	}
	if err == nil {
		err = rows.Err()
	}

	return counts, err
}

// UserGetUnvalidated 返回从未登录、没有已验证凭据且自 lastUpdatedBefore 以来未更新过的用户 ID 列表。
func (a *adapter) UserGetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]t.Uid, error) {
	var uids []t.Uid

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	rows, err := a.db.QueryxContext(ctx,
		"SELECT u.id, IFNULL(SUM(c.done),0) AS total FROM users AS u "+
			"LEFT JOIN credentials AS c ON u.id=c.userid WHERE u.lastseen IS NULL AND u.updatedat<? "+
			"GROUP BY u.id, u.updatedat HAVING total=0 ORDER BY u.updatedat ASC LIMIT ?", lastUpdatedBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var userId int64
		var unused int
		if err = rows.Scan(&userId, &unused); err != nil {
			break
		}
		uids = append(uids, store.EncodeUid(userId))
	}
	if err == nil {
		err = rows.Err()
	}

	return uids, err
}

// topicCreate 将输入编码为picCreate。
func (a *adapter) topicCreate(tx *sqlx.Tx, topic *t.Topic) error {
	_, err := tx.Exec("INSERT INTO topics(createdat,updatedat,touchedat,state,name,usebt,owner,access,public,trusted,tags,aux) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
		topic.CreatedAt, topic.UpdatedAt, topic.TouchedAt, topic.State, topic.Id, topic.UseBt,
		store.DecodeUid(t.ParseUid(topic.Owner)), topic.Access, common.ToJSON(topic.Public), common.ToJSON(topic.Trusted),
		topic.Tags, common.ToJSON(topic.Aux))
	if err != nil {
		return err
	}

	// 保存 Topic 的标签到单独的表以便 Topic 可被搜索。
	return addTags(tx, "topictags", "topic", topic.Id, topic.Tags, false)
}
