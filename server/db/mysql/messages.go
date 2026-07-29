//go:build mysql
// +build mysql

package mysql

import (
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	"github.com/jmoiron/sqlx"
)

// 消息
func (a *adapter) MessageSave(msg *t.Message) error {
	msg.InitClientKey()
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	// 存储 assignes 消息 ID, but we don't use it. 消息 IDs are not used anywhere.
	// Using a sequential ID provided by the 数据库.
	var clientID any
	if msg.ClientId != "" {
		clientID = msg.ClientId
	}
	res, err := a.db.ExecContext(ctx,
		"INSERT INTO messages(createdAt,updatedAt,seqid,topic,`from`,clientid,clientkey,head,content,searchtext) VALUES(?,?,?,?,?,?,?,?,?,?)",
		msg.CreatedAt, msg.UpdatedAt, msg.SeqId, msg.Topic,
		store.DecodeUid(t.ParseUid(msg.From)), clientID, common.NullableString(msg.ClientKey), msg.Head,
		common.ToJSON(msg.Content), msg.SearchText)
	if err == nil {
		id, _ := res.LastInsertId()
		// Replacing ID given by 存储 by ID given by the DB.
		msg.SetUid(t.Uid(id))
	}
	return err
}

// MessageSaveAtomic 在同一事务中校验集群 fence、推进 Topic 游标并保存消息。
func (a *adapter) MessageSaveAtomic(msg *t.Message) error {
	msg.InitClientKey()
	if msg.HasAnyClusterFenceField() && !msg.HasClusterFence() {
		return t.ErrMalformed
	}
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if msg.HasClusterFence() {
		// FOR SHARE 与 ClusterFenceAdvance 的行更新互斥，保证旧 epoch 事务不能越过新 fence 提交。
		var fenceEpoch int64
		if err = tx.QueryRowContext(ctx,
			"SELECT CAST(`value` AS SIGNED) FROM kvmeta WHERE `key`=? FOR SHARE",
			t.ClusterFenceKey(msg.ClusterId)).Scan(&fenceEpoch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return t.ErrClusterFenced
			}
			return err
		}
		if fenceEpoch != msg.ClusterEpoch {
			return t.ErrClusterFenced
		}
		result, updateErr := tx.ExecContext(ctx,
			`UPDATE topics SET seqid=?,touchedat=?,clusterowner=?,clusterepoch=?
			 WHERE name=? AND
			 (clusterepoch<? OR (clusterepoch=? AND clusterowner=?))`,
			msg.SeqId, msg.CreatedAt, msg.ClusterOwner, msg.ClusterEpoch, msg.Topic,
			msg.ClusterEpoch, msg.ClusterEpoch, msg.ClusterOwner)
		if updateErr != nil {
			return updateErr
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
			return rowsErr
		} else if affected != 1 {
			return t.ErrClusterFenced
		}
	} else {
		if _, err = tx.ExecContext(ctx,
			"UPDATE topics SET seqid=?,touchedat=? WHERE name=?",
			msg.SeqId, msg.CreatedAt, msg.Topic); err != nil {
			return err
		}
	}
	var clientID any
	if msg.ClientId != "" {
		clientID = msg.ClientId
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO messages(createdAt,updatedAt,seqid,topic,`from`,clientid,clientkey,head,content,searchtext) VALUES(?,?,?,?,?,?,?,?,?,?)",
		msg.CreatedAt, msg.UpdatedAt, msg.SeqId, msg.Topic,
		store.DecodeUid(t.ParseUid(msg.From)), clientID, common.NullableString(msg.ClientKey), msg.Head,
		common.ToJSON(msg.Content), msg.SearchText)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	msg.SetUid(t.Uid(id))
	return nil
}

// ClusterFenceAdvance 使用单条 UPSERT 单调推进逻辑集群的数据库 fencing epoch。
func (a *adapter) ClusterFenceAdvance(clusterID string, epoch int64) error {
	if clusterID == "" || epoch <= 0 {
		return t.ErrMalformed
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	epochText := strconv.FormatInt(epoch, 10)
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO kvmeta(`+"`key`"+`,createdat,`+"`value`"+`) VALUES(?,?,?)
		 ON DUPLICATE KEY UPDATE
			createdat=IF(CAST(`+"`value`"+` AS UNSIGNED)<?,VALUES(createdat),createdat),
			`+"`value`"+`=IF(CAST(`+"`value`"+` AS UNSIGNED)<?,VALUES(`+"`value`"+`),`+"`value`"+`)`,
		t.ClusterFenceKey(clusterID), t.TimeNow(), epochText, epoch, epoch)
	if err != nil {
		return err
	}
	var committedEpoch int64
	if err = a.db.QueryRowContext(ctx,
		"SELECT CAST(`value` AS SIGNED) FROM kvmeta WHERE `key`=?",
		t.ClusterFenceKey(clusterID)).Scan(&committedEpoch); err != nil {
		return err
	}
	if committedEpoch != epoch {
		return t.ErrClusterFenced
	}
	return nil
}

// MessageGetByClientId 按 Topic、发送者和客户端幂等键查询已投递消息。
func (a *adapter) MessageGetByClientId(topic string, from t.Uid, clientID string) (*t.Message, error) {
	if clientID == "" {
		return nil, nil
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var msg t.Message
	err := a.db.GetContext(ctx, &msg,
		"SELECT createdat,updatedat,deletedat,delid,seqid,topic,`from`,clientid,head,content"+
			" FROM messages WHERE topic=? AND clientkey=? LIMIT 1",
		topic, t.MessageClientKey(from, clientID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.From = common.EncodeUidString(msg.From).String()
	msg.Content = common.FromJSON(msg.Content)
	return &msg, nil
}

// MessageGet 按 Topic 和 SeqId 查询一条未硬删除消息。
func (a *adapter) MessageGet(topic string, seqID int) (*t.Message, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var msg t.Message
	var id int64
	err := a.db.QueryRowxContext(ctx,
		"SELECT id,createdat,updatedat,deletedat,delid,seqid,topic,`from`,COALESCE(clientid,'') AS clientid,head,content"+
			" FROM messages WHERE topic=? AND seqid=? AND delid=0 LIMIT 1",
		topic, seqID).Scan(
		&id, &msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId, &msg.SeqId,
		&msg.Topic, &msg.From, &msg.ClientId, &msg.Head, &msg.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.SetUid(t.Uid(id))
	msg.From = common.EncodeUidString(msg.From).String()
	msg.Content = common.FromJSON(msg.Content)
	return &msg, nil
}

// MessageUpdate 更新现存消息的正文、消息头和修改时间。
func (a *adapter) MessageUpdate(msg *t.Message) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	res, err := a.db.ExecContext(ctx,
		"UPDATE messages SET updatedat=?,head=?,content=?,searchtext=? WHERE topic=? AND seqid=? AND delid=0",
		msg.UpdatedAt, msg.Head, common.ToJSON(msg.Content), msg.SearchText, msg.Topic, msg.SeqId)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return t.ErrNotFound
	}
	return nil
}

// MessageSchedule 将消息快照写入持久化定时队列。
func (a *adapter) MessageSchedule(msg *t.ScheduledMessage) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx,
		"INSERT INTO scheduledmessages(id,createdat,updatedat,publishat,topic,`from`,clientid,noecho,head,content,attachmenturls,attachments)"+
			" VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
		store.DecodeUid(msg.Uid()), msg.CreatedAt, msg.UpdatedAt, msg.PublishAt, msg.Topic,
		store.DecodeUid(t.ParseUid(msg.From)), msg.ClientId, msg.NoEcho, msg.Head,
		common.ToJSON(msg.Content), msg.AttachmentURLs, msg.Attachments)
	return err
}

// MessageGetScheduledByClientId 按发送者范围内的幂等键查询待投递消息。
func (a *adapter) MessageGetScheduledByClientId(topic string, from t.Uid, clientID string) (*t.ScheduledMessage, error) {
	if clientID == "" {
		return nil, nil
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var msg t.ScheduledMessage
	var id, fromID int64
	err := a.db.QueryRowxContext(ctx,
		"SELECT id,createdat,updatedat,publishat,topic,`from`,clientid,noecho,head,content,attachmenturls,attachments"+
			" FROM scheduledmessages WHERE topic=? AND `from`=? AND clientid=? LIMIT 1",
		topic, store.DecodeUid(from), clientID).Scan(
		&id, &msg.CreatedAt, &msg.UpdatedAt, &msg.PublishAt, &msg.Topic, &fromID,
		&msg.ClientId, &msg.NoEcho, &msg.Head, &msg.Content, &msg.AttachmentURLs, &msg.Attachments)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.SetUid(store.EncodeUid(id))
	msg.From = store.EncodeUid(fromID).String()
	msg.Content = common.FromJSON(msg.Content)
	return &msg, nil
}

// MessageGetDueScheduled 按计划时间升序读取一批已到期消息。
func (a *adapter) MessageGetDueScheduled(now time.Time, limit int) ([]t.ScheduledMessage, error) {
	if limit <= 0 {
		limit = a.maxMessageResults
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx,
		"SELECT id,createdat,updatedat,publishat,topic,`from`,clientid,noecho,head,content,attachmenturls,attachments"+
			" FROM scheduledmessages WHERE publishat<=? ORDER BY publishat LIMIT ?",
		now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []t.ScheduledMessage
	for rows.Next() {
		var msg t.ScheduledMessage
		var id, fromID int64
		if err = rows.Scan(&id, &msg.CreatedAt, &msg.UpdatedAt, &msg.PublishAt, &msg.Topic, &fromID,
			&msg.ClientId, &msg.NoEcho, &msg.Head, &msg.Content, &msg.AttachmentURLs, &msg.Attachments); err != nil {
			return nil, err
		}
		msg.SetUid(store.EncodeUid(id))
		msg.From = store.EncodeUid(fromID).String()
		msg.Content = common.FromJSON(msg.Content)
		out = append(out, msg)
	}
	return out, rows.Err()
}

// MessageDeleteScheduled 删除指定发送者拥有的定时消息。
func (a *adapter) MessageDeleteScheduled(id, topic string, from t.Uid) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx, "DELETE FROM scheduledmessages WHERE id=? AND topic=? AND `from`=?",
		store.DecodeUid(t.ParseUid(id)), topic, store.DecodeUid(from))
	return err
}

// MessageGetAll returns 消息 matching the query.
func (a *adapter) MessageGetAll(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.Message, error) {
	var limit = a.maxMessageResults
	order := "DESC"

	args := []any{store.DecodeUid(forUser), topic}
	seqIdConstraint := ""
	modifiedConstraint := ""
	if opts != nil {
		seqIdConstraint = "AND m.seqid "
		if len(opts.IdRanges) > 0 {
			constr, newargs := common.RangesToSql(opts.IdRanges)
			seqIdConstraint += constr
			args = append(args, newargs...)
		} else {
			seqIdConstraint += "BETWEEN ? AND ?"
			if opts.Since > 0 {
				args = append(args, opts.Since)
			} else {
				args = append(args, 0)
			}
			if opts.Before > 1 {
				// MySQL BETWEEN 是包含两端的，IM API 要求包含起始不包含结束，因此 -1
				args = append(args, opts.Before-1)
			} else {
				args = append(args, 1<<31-1)
			}
		}

		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
		if opts.Forward {
			order = "ASC"
		}
		if opts.IfModifiedSince != nil {
			modifiedConstraint = " AND m.updatedat>?"
			args = append(args, *opts.IfModifiedSince)
			order = "ASC"
		}
	}

	args = append(args, limit)
	orderBy := "m.seqid " + order
	if opts != nil && opts.IfModifiedSince != nil {
		orderBy = "m.updatedat " + order + ",m.seqid " + order
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	rows, err := a.db.QueryxContext(
		ctx,
		"SELECT m.createdat,m.updatedat,m.deletedat,m.delid,m.seqid,m.topic,m.`from`,COALESCE(m.clientid,'') AS clientid,m.head,m.content"+
			" FROM messages AS m LEFT JOIN dellog AS d"+
			" ON d.topic=m.topic AND m.seqid BETWEEN d.low AND d.hi-1 AND d.deletedfor=?"+
			" WHERE m.delid=0 AND m.topic=? "+seqIdConstraint+modifiedConstraint+" AND d.deletedfor IS NULL"+
			" ORDER BY "+orderBy+" LIMIT ?",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs := make([]t.Message, 0, limit)
	for rows.Next() {
		var msg t.Message
		if err = rows.StructScan(&msg); err != nil {
			break
		}
		msg.From = common.EncodeUidString(msg.From).String()
		msg.Content = common.FromJSON(msg.Content)
		msgs = append(msgs, msg)
	}
	if err == nil {
		err = rows.Err()
	}

	return msgs, err
}

// MessageSearch 在单个 Topic 内执行正文子串搜索并应用用户软删除过滤。
func (a *adapter) MessageSearch(topic string, forUser t.Uid, search *t.MessageSearchQuery) ([]t.Message, error) {
	if search == nil || search.Query == "" {
		return nil, nil
	}
	limit := search.Limit
	if limit <= 0 || limit > a.maxMessageResults {
		limit = a.maxMessageResults
	}
	args := []any{store.DecodeUid(forUser), topic, "%" + common.EscapeLike(strings.ToLower(search.Query)) + "%"}
	where := "m.delid=0 AND m.topic=? AND LOWER(COALESCE(m.searchtext,'')) LIKE ? ESCAPE '!' AND d.deletedfor IS NULL"
	if !search.From.IsZero() {
		where += " AND m.`from`=?"
		args = append(args, store.DecodeUid(search.From))
	}
	if len(search.Kinds) > 0 {
		where += " AND JSON_UNQUOTE(JSON_EXTRACT(m.head,'$.\"x-kind\"')) IN (?" +
			strings.Repeat(",?", len(search.Kinds)-1) + ")"
		for _, kind := range search.Kinds {
			args = append(args, kind)
		}
	}
	if search.MinDate != nil {
		where += " AND m.createdat>=?"
		args = append(args, *search.MinDate)
	}
	if search.MaxDate != nil {
		where += " AND m.createdat<?"
		args = append(args, *search.MaxDate)
	}
	if search.BeforeSeq > 0 {
		where += " AND m.seqid<?"
		args = append(args, search.BeforeSeq)
	}
	args = append(args, limit)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx,
		"SELECT m.createdat,m.updatedat,m.deletedat,m.delid,m.seqid,m.topic,m.`from`,"+
			"COALESCE(m.clientid,'') AS clientid,m.head,m.content,m.searchtext"+
			" FROM messages m LEFT JOIN dellog d"+
			" ON d.topic=m.topic AND m.seqid BETWEEN d.low AND d.hi-1 AND d.deletedfor=?"+
			" WHERE "+where+" ORDER BY m.seqid DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make([]t.Message, 0, limit)
	for rows.Next() {
		var msg t.Message
		if err = rows.StructScan(&msg); err != nil {
			break
		}
		msg.From = common.EncodeUidString(msg.From).String()
		msg.Content = common.FromJSON(msg.Content)
		found = append(found, msg)
	}
	if err == nil {
		err = rows.Err()
	}
	return found, err
}

// Get ranges of deleted 消息
func (a *adapter) MessageGetDeleted(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.DelMessage, error) {
	var limit = a.maxResults
	var lower = 0
	var upper = 1<<31 - 1

	if opts != nil {
		if opts.Since > 0 {
			lower = opts.Since
		}
		if opts.Before > 1 {
			// DelRange 是包含起始不包含结束，而 BETWEEN 是包含两端的。
			upper = opts.Before - 1
		}

		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}

	// 获取删除日志
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, "SELECT topic,deletedfor,delid,low,hi FROM dellog WHERE topic=? AND delid BETWEEN ? AND ?"+
		" AND (deletedFor=0 OR deletedFor=?)"+
		" ORDER BY delid LIMIT ?", topic, lower, upper, store.DecodeUid(forUser), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dellog struct {
		Topic      string
		Deletedfor int64
		Delid      int
		Low        int
		Hi         int
	}
	var dmsgs []t.DelMessage
	var dmsg t.DelMessage
	for rows.Next() {
		if err = rows.StructScan(&dellog); err != nil {
			dmsgs = nil
			break
		}

		if dellog.Delid != dmsg.DelId {
			if dmsg.DelId > 0 {
				dmsgs = append(dmsgs, dmsg)
			}
			dmsg.DelId = dellog.Delid
			dmsg.Topic = dellog.Topic
			if dellog.Deletedfor > 0 {
				dmsg.DeletedFor = store.EncodeUid(dellog.Deletedfor).String()
			} else {
				dmsg.DeletedFor = ""
			}
			dmsg.SeqIdRanges = nil
		}
		if dellog.Hi <= dellog.Low+1 {
			dellog.Hi = 0
		}
		dmsg.SeqIdRanges = append(dmsg.SeqIdRanges, t.Range{Low: dellog.Low, Hi: dellog.Hi})
	}
	if err == nil {
		err = rows.Err()
	}

	if err == nil {
		if dmsg.DelId > 0 {
			dmsgs = append(dmsgs, dmsg)
		}
	}

	return dmsgs, err
}

// messageDeleteList 完成消息删除List所需的内部处理。
func messageDeleteList(tx *sqlx.Tx, topic string, toDel *t.DelMessage) error {
	var err error

	if toDel == nil {
		// Whole Topic is being deleted, thus also deleting all 消息.
		_, err = tx.Exec("DELETE FROM dellog WHERE topic=?", topic)
		if err == nil {
			_, err = tx.Exec("DELETE FROM messages WHERE topic=?", topic)
		}
		// filemsglinks 将因 ON DELETE CASCADE 而被删除
		return err
	}

	// Only some 消息 are being deleted.

	delRanges := toDel.SeqIdRanges

	if toDel.DeletedFor == "" {
		// Hard-deleting 消息 requires updates to the 消息 table.
		where := "m.topic=?"
		args := []any{topic}

		if len(delRanges) > 0 {
			rSql, rArgs := common.RangesToSql(delRanges)
			where += " AND m.seqid " + rSql
			args = append(args, rArgs...)
		}

		where += " AND m.deletedat IS NULL"

		// We are asked to delete 消息 no older than newerThan.
		if newerThan := toDel.GetNewerThan(); newerThan != nil {
			where += " AND m.createdat>?"
			args = append(args, newerThan)
		}

		// Find the actual IDs still present in the 数据库.
		var seqIDs []int
		err = tx.Select(&seqIDs, "SELECT seqid FROM messages AS m WHERE "+where, args...)
		if err != nil {
			return err
		}

		if len(seqIDs) == 0 {
			// 无需删除。无需记录日志。完成。
			return nil
		}

		// 重新计算实际要删除的范围。
		sort.Ints(seqIDs)
		delRanges = t.SliceToRanges(seqIDs)

		// 用新范围组成新查询。
		where = "m.topic=?"
		args = []any{topic}
		rSql, rArgs := common.RangesToSql(delRanges)
		where += " AND m.seqid " + rSql
		args = append(args, rArgs...)

		// 无需添加其他内容：deletedat 等已被考虑。

		_, err = tx.Exec("DELETE fml.* FROM filemsglinks AS fml INNER JOIN messages AS m ON m.id=fml.msgid WHERE "+
			where, args...)
		if err != nil {
			return err
		}

		// Instead of deleting 消息, clear all content.
		_, err = tx.Exec("UPDATE messages AS m SET m.deletedat=?,m.delId=?,m.`from`=0,m.head=NULL,m.content=NULL WHERE "+
			where, append([]any{t.TimeNow(), toDel.DelId}, args...)...)
		if err != nil {
			return err
		}
	}

	// 现在记录日志。硬删除和软删除都需要。
	var insert *sql.Stmt
	if insert, err = tx.Prepare(
		"INSERT INTO dellog(topic,deletedfor,delid,low,hi) VALUES(?,?,?,?,?)"); err != nil {
		return err
	}

	forUser := common.DecodeUidString(toDel.DeletedFor)
	for _, rng := range delRanges {
		if rng.Hi == 0 {
			// Dellog 必须包含有效的 Low 和 *Hi*。
			rng.Hi = rng.Low + 1
		}
		// 每个范围一条日志记录。
		if _, err = insert.Exec(topic, forUser, toDel.DelId, rng.Low, rng.Hi); err != nil {
			break
		}
	}

	if err != nil {
		return err
	}

	if toDel.DelId > 0 {
		if _, err = tx.Exec("UPDATE topics SET delid=? WHERE name=?", toDel.DelId, topic); err != nil {
			return err
		}
		if forUser == 0 {
			_, err = tx.Exec("UPDATE subscriptions SET delid=? WHERE topic=?", toDel.DelId, topic)
		} else {
			_, err = tx.Exec(
				"UPDATE subscriptions SET delid=? WHERE topic=? AND userid=?",
				toDel.DelId,
				topic,
				forUser,
			)
		}
	}

	return err
}

// MessageDeleteList deletes 消息 in the given Topic with seqIds from the list.
func (a *adapter) MessageDeleteList(topic string, toDel *t.DelMessage) (err error) {
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

	if err = messageDeleteList(tx, topic, toDel); err != nil {
		return err
	}

	return tx.Commit()
}
