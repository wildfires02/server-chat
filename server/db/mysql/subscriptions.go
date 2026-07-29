//go:build mysql
// +build mysql

package mysql

import (
	"database/sql"
	"strings"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	"github.com/jmoiron/sqlx"
)

// Get a 订阅 of a 用户 to a Topic.
func (a *adapter) SubscriptionGet(topic string, user t.Uid, keepDeleted bool) (*t.Subscription, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	query := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,modewant,modegiven,private FROM subscriptions WHERE topic=? AND userid=?`
	if !keepDeleted {
		query += " AND deletedat IS NULL"
	}
	var sub t.Subscription
	err := a.db.GetContext(ctx, &sub, query, topic, store.DecodeUid(user))
	if err != nil {
		if err == sql.ErrNoRows {
			// 未找到 - 清除错误
			err = nil
		}
		return nil, err
	}

	sub.User = user.String()
	sub.Private = common.FromJSON(sub.Private)

	return &sub, nil
}

// SubsForUser loads all 用户's 订阅. Does NOT load Public or Private values and does
// not load deleted 订阅.
func (a *adapter) SubsForUser(forUser t.Uid) ([]t.Subscription, error) {
	q := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,modewant,modegiven FROM subscriptions WHERE userid=? AND deletedat IS NULL`
	args := []any{store.DecodeUid(forUser)}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []t.Subscription
	var sub t.Subscription
	for rows.Next() {
		if err = rows.StructScan(&sub); err != nil {
			break
		}
		sub.User = forUser.String()
		subs = append(subs, sub)
	}
	if err == nil {
		err = rows.Err()
	}

	return subs, err
}

// SubsForTopic 获取 Topic 的所有订阅。不加载 Public 值，也不加载 Channel 读者。
// UsersForTopic 与 SubsForTopic 的区别在于前者加载用户的 public+trusted，
// 后者不加载。
func (a *adapter) SubsForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	q := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,modewant,modegiven,private FROM subscriptions WHERE topic=?`

	args := []any{topic}
	if !keepDeleted {
		// 过滤已删除的行。
		q += " AND deletedat IS NULL"
	}
	limit := a.maxResults
	if opts != nil {
		// 忽略 IfModifiedSince - 必须返回所有条目
		// 未修改的将去除 Public 和 Private。

		if !opts.User.IsZero() {
			q += " AND userid=?"
			args = append(args, store.DecodeUid(opts.User))
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}

	q += " LIMIT ?"
	args = append(args, limit)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []t.Subscription
	var sub t.Subscription
	for rows.Next() {
		if err = rows.StructScan(&sub); err != nil {
			break
		}

		sub.User = common.EncodeUidString(sub.User).String()
		sub.Private = common.FromJSON(sub.Private)
		subs = append(subs, sub)
	}
	if err == nil {
		err = rows.Err()
	}

	return subs, err
}

// SubsUpdate updates one or multiple 订阅 to a Topic.
func (a *adapter) SubsUpdate(topic string, user t.Uid, update map[string]any) error {
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
	q := "UPDATE subscriptions SET " + strings.Join(cols, ",") + " WHERE topic=?"
	args = append(args, topic)
	if !user.IsZero() {
		// Update just one Topic 订阅
		q += " AND userid=?"
		args = append(args, store.DecodeUid(user))
	}

	if _, err = tx.Exec(q, args...); err != nil {
		return err
	}

	return tx.Commit()
}

// SubsDelete marks at most one 订阅 as deleted (soft-deleting).
func (a *adapter) SubsDelete(topic string, user t.Uid) error {
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

	decoded_id := store.DecodeUid(user)
	now := t.TimeNow()

	// Mark 订阅 as deleted.
	res, err := tx.ExecContext(ctx,
		"UPDATE subscriptions SET updatedat=?,deletedat=? WHERE topic=? AND userid=? AND deletedat IS NULL",
		now, now, topic, decoded_id)
	if err != nil {
		return err
	}

	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		// 确保上面的 tx.Rollback() 被执行
		err = t.ErrNotFound
		return err
	}

	// Channel readers cannot delete 消息.
	if !t.IsChannel(topic) {
		// Remove records of 消息 soft-deleted by this 用户.
		_, err = tx.Exec("DELETE FROM dellog WHERE topic=? AND deletedfor=?", topic, decoded_id)
		if err != nil {
			return err
		}
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// Decrement Topic 订阅 count (only one 订阅 is	deleted).
		_, err = tx.Exec("UPDATE topics SET subcnt=subcnt-1 WHERE name=?", t.ChnToGrp(topic))
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// subsDelForUser marks 用户's 订阅 as deleted.
func subsDelForUser(tx *sqlx.Tx, decoded_uid int64, hard bool) error {
	// Decrement 订阅 count for all Topic the 用户 is subscribed to.
	rows, err := tx.Query("SELECT topic FROM subscriptions WHERE userid=? AND deletedat IS NULL", decoded_uid)
	if err != nil {
		return err
	}
	var topics []any
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		if t.IsChannel(name) {
			// 将 Channel 名称转换为群组名称。
			name = t.ChnToGrp(name)
		}
		topics = append(topics, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(topics) > 0 {
		sql, args, err := sqlx.In("UPDATE topics SET subcnt=subcnt-1 WHERE name IN (?)", topics)
		_, err = tx.Exec(tx.Rebind(sql), args...)
		if err != nil {
			return err
		}
	}

	if hard {
		_, err = tx.Exec("DELETE FROM subscriptions WHERE userid=?", decoded_uid)
	} else {
		now := t.TimeNow()
		_, err = tx.Exec("UPDATE subscriptions SET updatedat=?,deletedat=? WHERE userid=? AND deletedat IS NULL",
			now, now, decoded_uid)
	}
	return err
}

// SubsDelForUser marks 用户's 订阅 as deleted.
func (a *adapter) SubsDelForUser(user t.Uid, hard bool) error {
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

	if err = subsDelForUser(tx, store.DecodeUid(user), hard); err != nil {
		return err
	}

	return tx.Commit()
}
