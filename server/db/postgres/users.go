//go:build postgres
// +build postgres

package postgres

import (
	"context"
	"errors"
	pgx "github.com/jackc/pgx/v5"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	"github.com/jmoiron/sqlx"
)

// UserCreate creates a new 用户. Returns 错误 and true if 错误 is due to duplicate 用户 name,
// false for any other 错误
func (a *adapter) UserCreate(user *t.User) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	decoded_uid := store.DecodeUid(user.Uid())
	if _, err = tx.Exec(ctx,
		"INSERT INTO users(id,createdat,updatedat,state,access,public,trusted,tags) VALUES($1,$2,$3,$4,$5,$6,$7,$8);",
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

	// Save 用户's tags to a separate table to make 用户 findable.
	if err = addTags(ctx, tx, "usertags", "userid", decoded_uid, user.Tags, false); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// Add 用户's authentication record
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

	if _, err := a.db.Exec(ctx, "INSERT INTO auth(uname,userid,scheme,authLvl,secret,expires) VALUES($1,$2,$3,$4,$5,$6)",
		unique, store.DecodeUid(uid), scheme, authLvl, secret, exp); err != nil {
		if isDupe(err) {
			return t.ErrDuplicate
		}
		return err
	}
	return nil
}

// AuthDelScheme deletes an existing authentication scheme for the 用户.
func (a *adapter) AuthDelScheme(user t.Uid, scheme string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.Exec(ctx, "DELETE FROM auth WHERE userid=$1 AND scheme=$2", store.DecodeUid(user), scheme)
	return err
}

// AuthDelAllRecords deletes all authentication records for the 用户.
func (a *adapter) AuthDelAllRecords(user t.Uid) (int, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	res, err := a.db.Exec(ctx, "DELETE FROM auth WHERE userid=$1", store.DecodeUid(user))
	if err != nil {
		return 0, err
	}
	count := res.RowsAffected()

	return int(count), nil
}

// Update 用户's authentication unique, secret, auth level.
func (a *adapter) AuthUpdRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {

	parapg := []string{"authLvl=?"}
	args := []any{authLvl}
	if unique != "" {
		parapg = append(parapg, "uname=?")
		args = append(args, unique)
	}
	if len(secret) > 0 {
		parapg = append(parapg, "secret=?")
		args = append(args, secret)
	}
	if !expires.IsZero() {
		parapg = append(parapg, "expires=?")
		args = append(args, expires)
	}
	args = append(args, store.DecodeUid(uid), scheme)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	sql, args := expandQuery("UPDATE auth SET "+strings.Join(parapg, ",")+" WHERE userid=? AND scheme=?", args...)
	resp, err := a.db.Exec(ctx, sql, args...)
	if isDupe(err) {
		return t.ErrDuplicate
	}

	if count := resp.RowsAffected(); count <= 0 {
		return t.ErrNotFound
	}

	return err
}

// Retrieve 用户's authentication record
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
	if err := a.db.QueryRow(ctx, "SELECT uname,secret,expires,authlvl FROM auth WHERE userid=$1 AND scheme=$2",
		store.DecodeUid(uid), scheme).Scan(
		&record.Uname, &record.Secret, &record.Expires, &record.Authlvl); err != nil {
		if err == pgx.ErrNoRows {
			// Nothing found - use standard 错误.
			err = t.ErrNotFound
		}
		return "", 0, nil, expires, err
	}

	if record.Expires != nil {
		expires = *record.Expires
	}

	return record.Uname, record.Authlvl, record.Secret, expires, nil
}

// Retrieve 用户's authentication record
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
	if err := a.db.QueryRow(ctx, "SELECT userid,secret,expires,authlvl FROM auth WHERE uname=$1", unique).Scan(
		&record.Userid, &record.Secret, &record.Expires, &record.Authlvl); err != nil {
		if err == pgx.ErrNoRows {
			// Nothing found - clear the 错误
			err = nil
		}
		return t.ZeroUid, 0, nil, expires, err
	}

	if record.Expires != nil {
		expires = *record.Expires
	}

	return store.EncodeUid(record.Userid), record.Authlvl, record.Secret, expires, nil
}

// UserGet fetches a single 用户 by 用户 id. If 用户 is not found it returns (nil, nil)
func (a *adapter) UserGet(uid t.Uid) (*t.User, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	var user t.User
	var id int64
	row, err := a.db.Query(ctx, "SELECT * FROM users WHERE id=$1 AND state!=$2", store.DecodeUid(uid), t.StateDeleted)
	if err != nil {
		return nil, err
	}
	defer row.Close()

	if !row.Next() {
		// Nothing found: 用户 does not exist or marked as soft-deleted
		return nil, nil
	}

	err = row.Scan(&id, &user.CreatedAt, &user.UpdatedAt, &user.State, &user.StateAt, &user.Access, &user.LastSeen, &user.UserAgent, &user.Public, &user.Trusted, &user.Tags)
	if err == nil {
		user.SetUid(uid)
		return &user, nil
	}

	return nil, err
}

// UserGetAll 完成用户GetAll所需的内部处理。
func (a *adapter) UserGetAll(ids ...t.Uid) ([]t.User, error) {
	uids := make([]any, len(ids))
	for i, id := range ids {
		uids[i] = store.DecodeUid(id)
	}

	users := []t.User{}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	rows, err := a.db.Query(ctx, "SELECT * FROM users WHERE id = ANY ($1) AND state!=$2", uids, t.StateDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var user t.User
		var id int64
		if err = rows.Scan(&id, &user.CreatedAt, &user.UpdatedAt, &user.State, &user.StateAt, &user.Access, &user.LastSeen, &user.UserAgent, &user.Public, &user.Trusted, &user.Tags); err != nil {
			users = nil
			break
		}
		user.SetUid(store.EncodeUid(id))

		users = append(users, user)
	}
	if err == nil {
		err = rows.Err()
	}

	return users, err
}

// UserDelete deletes specified 用户: wipes completely (hard-delete) or marks as deleted.
func (a *adapter) UserDelete(uid t.Uid, hard bool) error {
	decoded_uid := store.DecodeUid(uid)

	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	// 检查用户是否存在以及是否已被软删除
	var state t.ObjState
	if err = tx.QueryRow(ctx, "SELECT state FROM users WHERE id=$1", decoded_uid).Scan(&state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return t.ErrNotFound
		}
		return err
	}
	if !hard && state == t.StateDeleted {
		return t.ErrNotFound
	}

	query := "SELECT name FROM topics WHERE owner=$1"
	args := []any{decoded_uid}
	// 硬删除时，删除所有 Topic，包括之前软删除的。
	if !hard {
		query += " AND state!=$2"
		args = append(args, t.StateDeleted)
	}
	// Get a list of Topic names owned by the 用户 (as 'grp' and 'chn').
	ownTopics, err := a.topicNamesForUser(query, false, args...)
	if err != nil {
		return err
	}

	now := t.TimeNow()
	if _, err = tx.Exec(ctx, `DELETE FROM scheduledmessages WHERE "from"=$1`, decoded_uid); err != nil {
		return err
	}
	for _, topic := range ownTopics {
		if _, err = tx.Exec(ctx, "DELETE FROM scheduledmessages WHERE topic=$1", topic); err != nil {
			return err
		}
	}

	if hard {
		// Delete 用户's devices
		// t.ErrNotFound = 用户 has no devices.
		if err = deviceDelete(ctx, tx, uid, ""); err != nil && err != t.ErrNotFound {
			return err
		}

		// Delete 用户's 订阅 in all Topic.
		if err = subsDelForUser(ctx, tx, decoded_uid, true); err != nil {
			return err
		}

		// Delete records of 消息 soft-deleted for the 用户.
		if _, err = tx.Exec(ctx, "DELETE FROM dellog WHERE deletedfor=$1", decoded_uid); err != nil {
			return err
		}

		// Can't delete 用户's 消息 in all Topic because we cannot notify Topic of such deletion.
		// Just leave the 消息 there marked as sent by "not found" 用户.

		// Delete Topic where the 用户 is the owner.

		if len(ownTopics) > 0 {
			// First delete all 消息 in those Topic.
			if _, err = tx.Exec(ctx, "DELETE FROM dellog USING topics WHERE topics.name=dellog.topic AND topics.owner=$1",
				decoded_uid); err != nil {
				return err
			}

			// Deletion of 消息 will cascade to filemsglinks and so to fileuploads.
			if _, err = tx.Exec(ctx, "DELETE FROM messages USING topics WHERE topics.name=messages.topic AND topics.owner=$1",
				decoded_uid); err != nil {
				return err
			}
			// Delete 订阅 for all 用户 where the 用户 is the owner of the Topic.
			sql, args, _ := sqlx.In("DELETE FROM subscriptions AS s WHERE topic IN (?)", ownTopics)
			if _, err = tx.Exec(ctx, sqlx.Rebind(sqlx.DOLLAR, sql), args...); err != nil {
				return err
			}

			// 删除 Topic 标签。
			if _, err = tx.Exec(ctx, "DELETE FROM topictags USING topics WHERE topics.name=topictags.topic AND topics.owner=$1",
				decoded_uid); err != nil {
				return err
			}

			// 最后删除 Topic。
			if _, err = tx.Exec(ctx, "DELETE FROM topics WHERE owner=$1", decoded_uid); err != nil {
				return err
			}
		}

		// Delete 用户's authentication records.
		if _, err = tx.Exec(ctx, "DELETE FROM auth WHERE userid=$1", decoded_uid); err != nil {
			return err
		}

		// 删除所有凭据。
		if err = credDel(ctx, tx, uid, "", ""); err != nil && err != t.ErrNotFound {
			return err
		}

		if _, err = tx.Exec(ctx, "DELETE FROM usertags WHERE userid=$1", decoded_uid); err != nil {
			return err
		}

		if _, err = tx.Exec(ctx, "DELETE FROM users WHERE id=$1", decoded_uid); err != nil {
			return err
		}
	} else {
		// Disable all 用户's 订阅. That includes p2p 订阅. No need to delete them.
		if err = subsDelForUser(ctx, tx, decoded_uid, false); err != nil {
			return err
		}

		if len(ownTopics) > 0 {
			// Disable all 订阅 to Topic where the 用户 is the owner.
			sql, args, _ := sqlx.In("UPDATE subscriptions SET updatedat=?,deletedat=? WHERE topic IN (?)", now, now, ownTopics)
			if _, err = tx.Exec(ctx, sqlx.Rebind(sqlx.DOLLAR, sql), args...); err != nil {
				return err
			}

			// Disable group Topic where the 用户 is the owner.
			if _, err = tx.Exec(ctx, "UPDATE topics SET updatedat=$1,touchedat=$1,state=$2,stateat=$1 WHERE owner=$3",
				now, t.StateDeleted, decoded_uid); err != nil {
				return err
			}
		}

		// Disable p2p Topic with the 用户 (p2p Topic's owner is 0).
		if _, err = tx.Exec(ctx, "UPDATE topics SET updatedat=$1,touchedat=$1,state=$2,stateat=$1 "+
			"FROM subscriptions WHERE topics.name=subscriptions.topic "+
			"AND topics.owner=0 AND subscriptions.userid=$3",
			now, t.StateDeleted, decoded_uid); err != nil {
			return err
		}

		// Disable the other 用户's 订阅 to a disabled p2p Topic.
		if _, err = tx.Exec(ctx, "UPDATE subscriptions AS s_one SET updatedat=$1,deletedat=$1 "+
			"FROM subscriptions AS s_two WHERE s_one.topic=s_two.topic "+
			"AND s_two.userid=$2 AND s_two.topic LIKE 'p2p%'",
			now, decoded_uid); err != nil {
			return err
		}

		// Disable 用户.
		if _, err = tx.Exec(ctx, "UPDATE users SET updatedat=$1,state=$2,stateat=$1 WHERE id=$3",
			now, t.StateDeleted, decoded_uid); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// topicStateForUser 由 UserUpdate 在更新包含状态变更时调用。
// 软删除的 Topic 保持软删除状态。
func (a *adapter) topicStateForUser(ctx context.Context, tx pgx.Tx, decoded_uid int64, now time.Time, update any) error {
	var err error

	state, ok := update.(t.ObjState)
	if !ok {
		return t.ErrMalformed
	}

	if now.IsZero() {
		now = t.TimeNow()
	}

	// Change state of all Topic where the 用户 is the owner.
	if _, err = tx.Exec(ctx, "UPDATE topics SET state=$1, stateat=$2 WHERE owner=$3 AND state!=$4",
		state, now, decoded_uid, t.StateDeleted); err != nil {
		return err
	}

	// Change state of p2p Topic with the 用户 (p2p Topic's owner is 0)
	if _, err = tx.Exec(ctx, "UPDATE topics SET state=$1, stateat=$2 "+
		"FROM subscriptions WHERE topics.name=subscriptions.topic AND "+
		"topics.owner=0 AND subscriptions.userid=$3 AND topics.state!=$4",
		state, now, decoded_uid, t.StateDeleted); err != nil {
		return err
	}

	// 订阅 don't need to be updated:
	// 订阅 of a disabled 用户 are not disabled and still can be manipulated.

	return nil
}

// UserUpdate updates 用户 object.
func (a *adapter) UserUpdate(uid t.Uid, update map[string]any) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	cols, args := common.UpdateByMap(update)
	decoded_uid := store.DecodeUid(uid)
	args = append(args, decoded_uid)
	sql, args := expandQuery("UPDATE users SET "+strings.Join(cols, ",")+" WHERE id=?", args...)
	_, err = tx.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}

	if state, ok := update["State"]; ok {
		now, _ := update["StateAt"].(time.Time)
		err = a.topicStateForUser(ctx, tx, decoded_uid, now, state)
		if err != nil {
			return err
		}
	}

	// 标签也存储在单独的表中
	if tags := common.ExtractTags(update); tags != nil {
		// First delete all 用户 tags
		_, err = tx.Exec(ctx, "DELETE FROM usertags WHERE userid=$1", decoded_uid)
		if err != nil {
			return err
		}
		// 现在插入新标签
		err = addTags(ctx, tx, "usertags", "userid", decoded_uid, tags, false)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// tempFetchTags 完成tempFetchTags所需的内部处理。
func tempFetchTags(ctx context.Context, tx pgx.Tx, decoded_uid int64) ([]string, error) {
	var allTags []string
	rows, err := tx.Query(ctx, "SELECT tag FROM usertags WHERE userid=$1", decoded_uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var tag string
		rows.Scan(&tag)
		allTags = append(allTags, tag)
	}
	return allTags, nil
}

// UserUpdateTags adds or resets 用户's tags
func (a *adapter) UserUpdateTags(uid t.Uid, add, remove, reset []string) ([]string, error) {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	decoded_uid := store.DecodeUid(uid)

	if reset != nil {
		// 重置时先删除所有标签。
		_, err = tx.Exec(ctx, "DELETE FROM usertags WHERE userid=$1", decoded_uid)
		if err != nil {
			return nil, err
		}
		add = reset
		remove = nil
	}

	// 现在插入新标签。重置时忽略重复。
	err = addTags(ctx, tx, "usertags", "userid", decoded_uid, add, reset == nil)
	if err != nil {
		return nil, err
	}

	// 删除标签。
	err = removeTags(ctx, tx, "usertags", "userid", decoded_uid, remove)
	if err != nil {
		return nil, err
	}

	var allTags []string
	rows, err := tx.Query(ctx, "SELECT tag FROM usertags WHERE userid=$1", decoded_uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var tag string
		rows.Scan(&tag)
		allTags = append(allTags, tag)
	}

	_, err = tx.Exec(ctx, "UPDATE users SET tags=$1 WHERE id=$2", t.StringSlice(allTags), decoded_uid)
	if err != nil {
		return nil, err
	}

	return allTags, tx.Commit(ctx)
}

// UserGetByCred returns 用户 ID for the given validated credential.
func (a *adapter) UserGetByCred(method, value string) (t.Uid, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var decoded_uid int64
	err := a.db.QueryRow(ctx, "SELECT userid FROM credentials WHERE synthetic=$1", method+":"+value).Scan(&decoded_uid)
	if err == nil {
		return store.EncodeUid(decoded_uid), nil
	}

	if err == pgx.ErrNoRows {
		// Clear the 错误 if 用户 does not exist
		return t.ZeroUid, nil
	}
	return t.ZeroUid, err
}

// UserUnreadCount returns the total number of unread 消息 in all Topic with
// the R 权限. If read fails, the counts are still returned with the original
// 用户 IDs but with the unread count undefined and non-nil 错误.
// UserUnreadCount does not count unread 消息 in Channel although it should.
func (a *adapter) UserUnreadCount(ids ...t.Uid) (map[t.Uid]int, error) {
	counts, uids := common.InitUnreadCountMap(ids)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// 联表查询未读消息数：利用 CASE WHEN 动态支持 Channel (将 chn... 前缀映射为 topics 主表中的 grp...)
	query, uids := expandQuery("SELECT s.userid, SUM(t.seqid)-SUM(s.readseqid) AS unreadcount FROM topics AS t JOIN subscriptions AS s "+
		"ON t.name = CASE WHEN s.topic LIKE 'chn%' THEN 'grp' || SUBSTRING(s.topic FROM 4) ELSE s.topic END "+
		"WHERE s.userid IN (?) AND s.deletedat IS NULL AND t.state!=? AND "+
		"POSITION('R' IN s.modewant)>0 AND POSITION('R' IN s.modegiven)>0 GROUP BY s.userid", uids, t.StateDeleted)
	rows, err := a.db.Query(ctx, query, uids...)
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

	rows, err := a.db.Query(ctx,
		"SELECT u.id, COALESCE(SUM(CASE WHEN c.done THEN 1 ELSE 0 END), 0) AS total "+
			"FROM users u LEFT JOIN credentials c ON u.id = c.userid "+
			"WHERE u.lastseen IS NULL AND u.updatedat < $1 GROUP BY u.id, u.updatedat "+
			"HAVING COALESCE(SUM(CASE WHEN c.done THEN 1 ELSE 0 END), 0) = 0 ORDER BY u.updatedat ASC LIMIT $2",
		lastUpdatedBefore, limit)
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

// *****************************

// topicCreate 将输入编码为picCreate。
func (a *adapter) topicCreate(ctx context.Context, tx pgx.Tx, topic *t.Topic) error {
	_, err := tx.Exec(ctx, "INSERT INTO topics(createdat,updatedat,touchedat,state,name,usebt,owner,access,public,trusted,tags,aux) "+
		"VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)",
		topic.CreatedAt, topic.UpdatedAt, topic.TouchedAt, topic.State, topic.Id, topic.UseBt,
		store.DecodeUid(t.ParseUid(topic.Owner)), topic.Access, common.ToJSON(topic.Public), common.ToJSON(topic.Trusted),
		topic.Tags, common.ToJSON(topic.Aux))
	if err != nil {
		return err
	}

	// 保存 Topic 的标签到单独的表以便 Topic 可被搜索。
	return addTags(ctx, tx, "topictags", "topic", topic.Id, topic.Tags, false)
}
