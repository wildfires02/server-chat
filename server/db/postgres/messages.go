//go:build postgres
// +build postgres

package postgres

import (
	"context"
	"errors"
	pgx "github.com/jackc/pgx/v5"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"
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
	var id int
	var clientID any
	if msg.ClientId != "" {
		clientID = msg.ClientId
	}
	err := a.db.QueryRow(ctx,
		`INSERT INTO messages(createdAt,updatedAt,seqid,topic,"from",clientid,clientkey,head,content,searchtext)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		msg.CreatedAt, msg.UpdatedAt, msg.SeqId, msg.Topic,
		store.DecodeUid(t.ParseUid(msg.From)), clientID, common.NullableString(msg.ClientKey), msg.Head,
		common.ToJSON(msg.Content), msg.SearchText).Scan(&id)
	if err == nil {
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
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if msg.HasClusterFence() {
		// FOR SHARE 与 ClusterFenceAdvance 的行更新互斥，保证旧 epoch 事务不能越过新 fence 提交。
		var fenceEpoch int64
		if err = tx.QueryRow(ctx,
			`SELECT CAST("value" AS BIGINT) FROM kvmeta WHERE "key"=$1 FOR SHARE`,
			t.ClusterFenceKey(msg.ClusterId)).Scan(&fenceEpoch); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return t.ErrClusterFenced
			}
			return err
		}
		if fenceEpoch != msg.ClusterEpoch {
			return t.ErrClusterFenced
		}
		commandTag, updateErr := tx.Exec(ctx,
			`UPDATE topics SET seqid=$1,touchedat=$2,clusterowner=$3,clusterepoch=$4
			 WHERE name=$5 AND
			 (clusterepoch<$4 OR (clusterepoch=$4 AND clusterowner=$3))`,
			msg.SeqId, msg.CreatedAt, msg.ClusterOwner, msg.ClusterEpoch, msg.Topic)
		if updateErr != nil {
			return updateErr
		}
		if commandTag.RowsAffected() != 1 {
			return t.ErrClusterFenced
		}
	} else {
		if _, err = tx.Exec(ctx,
			"UPDATE topics SET seqid=$1,touchedat=$2 WHERE name=$3",
			msg.SeqId, msg.CreatedAt, msg.Topic); err != nil {
			return err
		}
	}
	var clientID any
	if msg.ClientId != "" {
		clientID = msg.ClientId
	}
	var id int
	err = tx.QueryRow(ctx,
		`INSERT INTO messages(createdAt,updatedAt,seqid,topic,"from",clientid,clientkey,head,content,searchtext)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		msg.CreatedAt, msg.UpdatedAt, msg.SeqId, msg.Topic,
		store.DecodeUid(t.ParseUid(msg.From)), clientID, common.NullableString(msg.ClientKey), msg.Head,
		common.ToJSON(msg.Content), msg.SearchText).Scan(&id)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
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
	var committedEpoch int64
	err := a.db.QueryRow(ctx, `INSERT INTO kvmeta("key",createdat,"value") VALUES($1,$2,$3)
		ON CONFLICT ("key") DO UPDATE SET
			createdat=CASE
				WHEN CAST(kvmeta."value" AS BIGINT)<CAST(EXCLUDED."value" AS BIGINT)
				THEN EXCLUDED.createdat ELSE kvmeta.createdat END,
			"value"=CASE
				WHEN CAST(kvmeta."value" AS BIGINT)<CAST(EXCLUDED."value" AS BIGINT)
				THEN EXCLUDED."value" ELSE kvmeta."value" END
		RETURNING CAST("value" AS BIGINT)`,
		t.ClusterFenceKey(clusterID), t.TimeNow(), strconv.FormatInt(epoch, 10)).Scan(&committedEpoch)
	if err != nil {
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
	var fromID int64
	err := a.db.QueryRow(ctx,
		`SELECT createdat,updatedat,deletedat,delid,seqid,topic,"from",clientid,head,content
		 FROM messages WHERE topic=$1 AND clientkey=$2 LIMIT 1`,
		topic, t.MessageClientKey(from, clientID)).Scan(
		&msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId, &msg.SeqId,
		&msg.Topic, &fromID, &msg.ClientId, &msg.Head, &msg.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.From = store.EncodeUid(fromID).String()
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
	var id, fromID int64
	err := a.db.QueryRow(ctx,
		`SELECT id,createdat,updatedat,deletedat,delid,seqid,topic,"from",COALESCE(clientid,''),head,content
		 FROM messages WHERE topic=$1 AND seqid=$2 AND delid=0 LIMIT 1`,
		topic, seqID).Scan(
		&id, &msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId, &msg.SeqId,
		&msg.Topic, &fromID, &msg.ClientId, &msg.Head, &msg.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.SetUid(t.Uid(id))
	msg.From = store.EncodeUid(fromID).String()
	msg.Content = common.FromJSON(msg.Content)
	return &msg, nil
}

// MessageUpdate 更新现存消息的正文、消息头和修改时间。
func (a *adapter) MessageUpdate(msg *t.Message) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	tag, err := a.db.Exec(ctx,
		`UPDATE messages SET updatedat=$1,head=$2,content=$3,searchtext=$4 WHERE topic=$5 AND seqid=$6 AND delid=0`,
		msg.UpdatedAt, msg.Head, common.ToJSON(msg.Content), msg.SearchText, msg.Topic, msg.SeqId)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
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
	_, err := a.db.Exec(ctx,
		`INSERT INTO scheduledmessages(id,createdat,updatedat,publishat,topic,"from",clientid,noecho,head,content,attachmenturls,attachments)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
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
	err := a.db.QueryRow(ctx,
		`SELECT id,createdat,updatedat,publishat,topic,"from",clientid,noecho,head,content,attachmenturls,attachments
		 FROM scheduledmessages WHERE topic=$1 AND "from"=$2 AND clientid=$3 LIMIT 1`,
		topic, store.DecodeUid(from), clientID).Scan(
		&id, &msg.CreatedAt, &msg.UpdatedAt, &msg.PublishAt, &msg.Topic, &fromID,
		&msg.ClientId, &msg.NoEcho, &msg.Head, &msg.Content, &msg.AttachmentURLs, &msg.Attachments)
	if errors.Is(err, pgx.ErrNoRows) {
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
	rows, err := a.db.Query(ctx,
		`SELECT id,createdat,updatedat,publishat,topic,"from",clientid,noecho,head,content,attachmenturls,attachments
		 FROM scheduledmessages WHERE publishat<=$1 ORDER BY publishat LIMIT $2`, now, limit)
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
	_, err := a.db.Exec(ctx, `DELETE FROM scheduledmessages WHERE id=$1 AND topic=$2 AND "from"=$3`,
		store.DecodeUid(t.ParseUid(id)), topic, store.DecodeUid(from))
	return err
}

// MessageGetAll 完成消息GetAll所需的内部处理。
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
			if opts.Before > 0 {
				// BETWEEN 是包含两端的，IM API 要求包含起始不包含结束，因此 -1
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

	query, args := expandQuery(`SELECT m.createdat,m.updatedat,m.deletedat,m.delid,m.seqid,m.topic,m."from",COALESCE(m.clientid,''),m.head,m.content`+
		" FROM messages AS m LEFT JOIN dellog AS d"+
		" ON d.topic=m.topic AND m.seqid BETWEEN d.low AND d.hi-1 AND d.deletedfor=?"+
		" WHERE m.delid=0 AND m.topic=? "+seqIdConstraint+modifiedConstraint+" AND d.deletedfor IS NULL"+
		" ORDER BY "+orderBy+" LIMIT ?", args...)
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs := make([]t.Message, 0, limit)
	for rows.Next() {
		var msg t.Message
		var from int64
		if err = rows.Scan(&msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId, &msg.SeqId,
			&msg.Topic, &from, &msg.ClientId, &msg.Head, &msg.Content); err != nil {
			break
		}
		msg.From = store.EncodeUid(from).String()
		msgs = append(msgs, msg)
	}
	if err == nil {
		err = rows.Err()
	}

	return msgs, err
}

// MessageGetLatest returns the latest message visible to forUser for each topic in one query.
func (a *adapter) MessageGetLatest(topics []string, forUser t.Uid) ([]t.Message, error) {
	if len(topics) == 0 {
		return nil, nil
	}
	query, args := expandQuery(`SELECT DISTINCT ON (m.topic)
		m.createdat,m.updatedat,m.deletedat,m.delid,m.seqid,m.topic,m."from",COALESCE(m.clientid,''),m.head,m.content
		FROM messages AS m LEFT JOIN dellog AS d
		ON d.topic=m.topic AND m.seqid BETWEEN d.low AND d.hi-1 AND d.deletedfor=?
		WHERE m.delid=0 AND m.topic IN (?) AND d.deletedfor IS NULL
		ORDER BY m.topic,m.seqid DESC`, store.DecodeUid(forUser), topics)
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs := make([]t.Message, 0, len(topics))
	for rows.Next() {
		var msg t.Message
		var from int64
		if err = rows.Scan(&msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId, &msg.SeqId,
			&msg.Topic, &from, &msg.ClientId, &msg.Head, &msg.Content); err != nil {
			return nil, err
		}
		msg.From = store.EncodeUid(from).String()
		msgs = append(msgs, msg)
	}
	return msgs, rows.Err()
}

// MessageSearch 在单个 Topic 内按规范化正文搜索消息，并排除调用者已删除的消息。
func (a *adapter) MessageSearch(topic string, forUser t.Uid, search *t.MessageSearchQuery) ([]t.Message, error) {
	if search == nil || search.Query == "" {
		return nil, nil
	}
	limit := search.Limit
	if limit <= 0 || limit > a.maxMessageResults {
		limit = a.maxMessageResults
	}

	// deletedFor 参数位于 JOIN 中，因此必须排在 Topic 和正文条件之前。
	args := []any{store.DecodeUid(forUser), topic, "%" + common.EscapeLike(strings.ToLower(search.Query)) + "%"}
	where := "m.delid=0 AND m.topic=? AND LOWER(COALESCE(m.searchtext,'')) LIKE ? ESCAPE '!' AND d.deletedfor IS NULL"
	if !search.From.IsZero() {
		where += ` AND m."from"=?`
		args = append(args, store.DecodeUid(search.From))
	}
	if len(search.Kinds) > 0 {
		where += " AND COALESCE(m.head::jsonb->>'x-kind','') IN (?" + strings.Repeat(",?", len(search.Kinds)-1) + ")"
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

	query, args := expandQuery(
		`SELECT m.createdat,m.updatedat,m.deletedat,m.delid,m.seqid,m.topic,m."from",`+
			`COALESCE(m.clientid,''),m.head,m.content,m.searchtext`+
			` FROM messages AS m LEFT JOIN dellog AS d`+
			` ON d.topic=m.topic AND m.seqid BETWEEN d.low AND d.hi-1 AND d.deletedfor=?`+
			` WHERE `+where+` ORDER BY m.seqid DESC LIMIT ?`, args...)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]t.Message, 0, limit)
	for rows.Next() {
		var msg t.Message
		var from int64
		if err = rows.Scan(&msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId,
			&msg.SeqId, &msg.Topic, &from, &msg.ClientId, &msg.Head, &msg.Content,
			&msg.SearchText); err != nil {
			return nil, err
		}
		msg.From = store.EncodeUid(from).String()
		msg.Content = common.FromJSON(msg.Content)
		messages = append(messages, msg)
	}
	return messages, rows.Err()
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
	rows, err := a.db.Query(ctx, "SELECT topic,deletedfor,delid,low,hi FROM dellog WHERE topic=$1 AND delid BETWEEN $2 AND $3"+
		" AND (deletedFor=0 OR deletedFor=$4) ORDER BY delid LIMIT $5",
		topic, lower, upper, store.DecodeUid(forUser), limit)
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
		if err = rows.Scan(&dellog.Topic, &dellog.Deletedfor, &dellog.Delid, &dellog.Low, &dellog.Hi); err != nil {
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
func messageDeleteList(ctx context.Context, tx pgx.Tx, topic string, toDel *t.DelMessage) error {
	var err error

	if toDel == nil {
		// Whole Topic is being deleted, thus also deleting all 消息.
		_, err = tx.Exec(ctx, "DELETE FROM dellog WHERE topic=$1", topic)
		if err == nil {
			_, err = tx.Exec(ctx, "DELETE FROM messages WHERE topic=$1", topic)
		}
		// filemsglinks 将因 ON DELETE CASCADE 而被删除
		return err
	}

	// Only some 消息 are being deleted

	delRanges := toDel.SeqIdRanges

	if toDel.DeletedFor == "" {
		// Hard-deleting 消息 requires updates to the 消息 table.
		where := "m.topic=? "
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
		query, newargs := expandQuery("SELECT seqid FROM messages AS m WHERE "+where, args)
		rows, err := tx.Query(ctx, query, newargs...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var seqID int
			if err := rows.Scan(&seqID); err != nil {
				return err
			}
			seqIDs = append(seqIDs, seqID)
		}
		if err = rows.Err(); err != nil {
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

		query, newargs = expandQuery("DELETE FROM filemsglinks AS fml USING messages AS m WHERE m.id=fml.msgid AND "+
			where, args...)
		_, err = tx.Exec(ctx, query, newargs...)
		if err != nil {
			return err
		}

		query, newargs = expandQuery(`UPDATE messages AS m SET deletedat=?,delid=?,"from"=0,head=NULL,content=NULL WHERE `+
			where, t.TimeNow(), toDel.DelId, args)
		_, err = tx.Exec(ctx, query, newargs...)
		if err != nil {
			return err
		}
	}

	// 现在记录日志。硬删除和软删除都需要。

	// 不需要预处理语句，因为驱动在首次使用时准备语句并缓存。
	forUser := common.DecodeUidString(toDel.DeletedFor)
	for _, rng := range toDel.SeqIdRanges {
		if rng.Hi == 0 {
			// Dellog 必须包含有效的 Low 和 *Hi*。
			rng.Hi = rng.Low + 1
		}

		if _, err = tx.Exec(ctx, "INSERT INTO dellog(topic,deletedfor,delid,low,hi) VALUES($1,$2,$3,$4,$5)",
			topic, forUser, toDel.DelId, rng.Low, rng.Hi); err != nil {
			break
		}
	}

	if err != nil {
		return err
	}

	if toDel.DelId > 0 {
		if _, err = tx.Exec(ctx, "UPDATE topics SET delid=$1 WHERE name=$2", toDel.DelId, topic); err != nil {
			return err
		}
		if forUser == 0 {
			_, err = tx.Exec(ctx, "UPDATE subscriptions SET delid=$1 WHERE topic=$2", toDel.DelId, topic)
		} else {
			_, err = tx.Exec(
				ctx,
				"UPDATE subscriptions SET delid=$1 WHERE topic=$2 AND userid=$3",
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
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	if err = messageDeleteList(ctx, tx, topic, toDel); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// MessageRetireExpired 分批清除超过保留期的消息正文和附件关联。
func (a *adapter) MessageRetireExpired(cutoff time.Time, limit int) (messageIDs []t.Uid, err error) {
	if limit <= 0 {
		return nil, nil
	}
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
			_ = tx.Rollback(ctx)
		}
	}()
	rows, err := tx.Query(ctx,
		`SELECT id FROM messages WHERE createdat<$1 AND deletedat IS NULL
		 ORDER BY createdat,id LIMIT $2 FOR UPDATE SKIP LOCKED`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	var rawIDs []int32
	for rows.Next() {
		var id int32
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		rawIDs = append(rawIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(rawIDs) == 0 {
		return nil, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM filemsglinks WHERE msgid=ANY($1)", rawIDs); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx,
		`UPDATE messages SET deletedat=$1,delid=-1,"from"=0,clientid=NULL,clientkey=NULL,
		 head=NULL,content=NULL,searchtext=NULL WHERE id=ANY($2)`, t.TimeNow(), rawIDs); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	messageIDs = make([]t.Uid, 0, len(rawIDs))
	for _, id := range rawIDs {
		messageIDs = append(messageIDs, t.Uid(id))
	}
	return messageIDs, nil
}
