//go:build postgres
// +build postgres

package postgres

import (
	"context"
	"strings"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	pgx "github.com/jackc/pgx/v5"
	"github.com/jmoiron/sqlx"
)

// Get a 订阅 of a 用户 to a Topic.
func (a *adapter) SubscriptionGet(topic string, user t.Uid, keepDeleted bool) (*t.Subscription, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	query := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,readhistory,modewant,modegiven,private FROM subscriptions WHERE topic=$1 AND userid=$2`
	if !keepDeleted {
		query += " AND deletedat IS NULL"
	}
	var sub t.Subscription
	var userId int64
	var modeWant, modeGiven []byte
	err := a.db.QueryRow(ctx, query, topic, store.DecodeUid(user)).Scan(&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt, &userId,
		&sub.Topic, &sub.DelId, &sub.RecvSeqId, &sub.ReadSeqId, &sub.ReadHistory, &modeWant, &modeGiven, &sub.Private)

	if err != nil {
		if err == pgx.ErrNoRows {
			// Nothing found - clear the 错误
			err = nil
		}
		return nil, err
	}

	sub.User = store.EncodeUid(userId).String()
	sub.ModeWant.Scan(modeWant)
	sub.ModeGiven.Scan(modeGiven)

	return &sub, nil
}

// SubsForUser loads all 用户's 订阅. Does NOT load Public or Private values and does
// not load deleted 订阅.
func (a *adapter) SubsForUser(forUser t.Uid) ([]t.Subscription, error) {
	q := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,readhistory,modewant,modegiven FROM subscriptions WHERE userid=$1 AND deletedat IS NULL`
	args := []any{store.DecodeUid(forUser)}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []t.Subscription
	var sub t.Subscription
	var userId int64
	var modeWant, modeGiven []byte
	for rows.Next() {
		if err = rows.Scan(&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt, &userId, &sub.Topic, &sub.DelId,
			&sub.RecvSeqId, &sub.ReadSeqId, &sub.ReadHistory, &modeWant, &modeGiven); err != nil {
			break
		}

		sub.User = store.EncodeUid(userId).String()
		sub.ModeWant.Scan(modeWant)
		sub.ModeGiven.Scan(modeGiven)
		subs = append(subs, sub)
	}
	if err == nil {
		err = rows.Err()
	}

	return subs, err
}

// SubsForTopic 获取 Topic 的所有订阅。不加载 Public 值。
// UsersForTopic 与 SubsForTopic 的区别在于前者加载用户的 public+trusted，
// 后者不加载。
func (a *adapter) SubsForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	q := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,readhistory,modewant,modegiven,private FROM subscriptions WHERE topic=?`

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
		if !opts.Cursor.IsZero() {
			q += " AND userid>?"
			args = append(args, store.DecodeUid(opts.Cursor))
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}

	q += " ORDER BY userid ASC LIMIT ?"
	args = append(args, limit)
	q, args = expandQuery(q, args...)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []t.Subscription
	var sub t.Subscription
	var userId int64
	var modeWant, modeGiven []byte
	for rows.Next() {
		if err = rows.Scan(&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt, &userId, &sub.Topic, &sub.DelId,
			&sub.RecvSeqId, &sub.ReadSeqId, &sub.ReadHistory, &modeWant, &modeGiven, &sub.Private); err != nil {
			break
		}

		sub.User = store.EncodeUid(userId).String()
		sub.ModeWant.Scan(modeWant)
		sub.ModeGiven.Scan(modeGiven)
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
	q := "UPDATE subscriptions SET " + strings.Join(cols, ",") + " WHERE topic=?"
	args = append(args, topic)
	if !user.IsZero() {
		// Update just one Topic 订阅
		q += " AND userid=?"
		args = append(args, store.DecodeUid(user))
	}
	q, args = expandQuery(q, args...)

	if _, err = tx.Exec(ctx, q, args...); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SubsDelete marks at most one 订阅 as deleted.
func (a *adapter) SubsDelete(topic string, user t.Uid) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	decoded_id := store.DecodeUid(user)
	now := t.TimeNow()
	res, err := tx.Exec(ctx,
		"UPDATE subscriptions SET updatedat=$1,deletedat=$2 WHERE topic=$3 AND userid=$4 AND deletedat IS NULL",
		now, now, topic, decoded_id)
	if err != nil {
		return err
	}

	affected := res.RowsAffected()
	if affected == 0 {
		// 确保上面的 tx.Rollback() 被执行
		err = t.ErrNotFound
		return err
	}

	// Channel readers cannot delete 消息.
	if !t.IsChannel(topic) {
		// Remove records of 消息 soft-deleted by this 用户.
		_, err = tx.Exec(ctx, "DELETE FROM dellog WHERE topic=$1 AND deletedfor=$2", topic, decoded_id)
		if err != nil {
			return err
		}
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// Decrement Topic 订阅 count (only one 订阅 is	deleted).
		_, err = tx.Exec(ctx, "UPDATE topics SET subcnt=subcnt-1 WHERE name=$1", t.ChnToGrp(topic))
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// subsDelForUser marks 用户's 订阅 as deleted.
func subsDelForUser(ctx context.Context, tx pgx.Tx, decoded_uid int64, hard bool) error {
	// Decrement 订阅 count for all Topic the 用户 is subscribed to.
	rows, err := tx.Query(ctx, "SELECT topic FROM subscriptions WHERE userid=$1 AND deletedat IS NULL", decoded_uid)
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
		sql, args, _ := sqlx.In("UPDATE topics SET subcnt=subcnt-1 WHERE name IN (?)", topics)
		_, err = tx.Exec(ctx, sqlx.Rebind(sqlx.DOLLAR, sql), args...)
		if err != nil {
			return err
		}
	}

	if hard {
		// Hard delete: remove all 订阅 for the 用户.
		_, err = tx.Exec(ctx, "DELETE FROM subscriptions WHERE userid=$1", decoded_uid)
	} else {
		now := t.TimeNow()
		_, err = tx.Exec(ctx, "UPDATE subscriptions SET updatedat=$1,deletedat=$2 WHERE userid=$3 AND deletedat IS NULL;",
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

	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	if err = subsDelForUser(ctx, tx, store.DecodeUid(user), hard); err != nil {
		return err
	}

	return tx.Commit(ctx)

}
