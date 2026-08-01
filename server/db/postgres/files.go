//go:build postgres
// +build postgres

package postgres

import (
	pgx "github.com/jackc/pgx/v5"
	"strings"
	"time"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"
)

// FileStartUpload 初始化文件上传
func (a *adapter) FileStartUpload(fd *t.FileDef) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var user any
	if fd.User != "" {
		user = store.DecodeUid(t.ParseUid(fd.User))
	}
	_, err := a.db.Exec(ctx,
		"INSERT INTO fileuploads(id,createdat,updatedat,userid,status,mimetype,size,etag,location) "+
			"VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)",
		store.DecodeUid(fd.Uid()), fd.CreatedAt, fd.UpdatedAt, user,
		fd.Status, fd.MimeType, fd.Size, fd.ETag, fd.Location)
	return err
}

// FileFinishUpload 标记文件上传完成，无论成功与否
func (a *adapter) FileFinishUpload(fd *t.FileDef, success bool, size int64) (*t.FileDef, error) {
	ctx, cancel := a.getContext()
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

	now := t.TimeNow()
	if success {
		_, err = tx.Exec(ctx, "UPDATE fileuploads SET updatedat=$1,status=$2,size=$3,etag=$4,location=$5 WHERE id=$6",
			now, t.UploadCompleted, size, fd.ETag, fd.Location, store.DecodeUid(fd.Uid()))
		if err != nil {
			return nil, err
		}

		fd.Status = t.UploadCompleted
		fd.Size = size
	} else {
		// 删除记录：保留在数据库中没有意义。
		_, err = tx.Exec(ctx, "DELETE FROM fileuploads WHERE id=$1", store.DecodeUid(fd.Uid()))
		if err != nil {
			return nil, err
		}

		fd.Status = t.UploadFailed
		fd.Size = 0
	}
	fd.UpdatedAt = now

	return fd, tx.Commit(ctx)
}

// FileGet 获取指定文件的记录
func (a *adapter) FileGet(fid string) (*t.FileDef, error) {
	id := t.ParseUid(fid)
	if id.IsZero() {
		return nil, t.ErrMalformed
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var fd t.FileDef
	var ID int64
	var userId int64
	err := a.db.QueryRow(ctx, "SELECT id,createdat,updatedat,userid AS user,status,mimetype,size,etag,location "+
		"FROM fileuploads WHERE id=$1", store.DecodeUid(id)).Scan(&ID, &fd.CreatedAt, &fd.UpdatedAt, &userId, &fd.Status,
		&fd.MimeType, &fd.Size, &fd.ETag, &fd.Location)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	fd.Id = common.EncodeUidString(fd.Id).String()
	fd.User = store.EncodeUid(userId).String()

	return &fd, nil
}

// FileDeleteUnused 删除文件上传记录。
func (a *adapter) FileDeleteUnused(olderThan time.Time, limit int, protected func(string) bool) ([]string, error) {
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

	// Garbage collecting entries which as either marked as deleted, or lack 消息 references, or have no 用户 assigned.
	query := "SELECT fu.id,fu.location FROM fileuploads AS fu LEFT JOIN filemsglinks AS fml ON fml.fileid=fu.id " +
		"LEFT JOIN scheduledfilelinks AS sfl ON sfl.fileid=fu.id WHERE fml.id IS NULL AND sfl.id IS NULL"
	var args []any
	if !olderThan.IsZero() {
		query += " AND fu.updatedat<?"
		args = append(args, olderThan)
	}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	query, _ = expandQuery(query, args...)

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var locations []string
	var ids []any
	for rows.Next() {
		var id int
		var loc string
		if err = rows.Scan(&id, &loc); err != nil {
			break
		}
		if protected != nil && protected(store.EncodeUid(int64(id)).String()) {
			continue
		}
		if loc != "" {
			locations = append(locations, loc)
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}

	if err != nil {
		return nil, err
	}

	if len(ids) > 0 {
		query, ids = expandQuery("DELETE FROM fileuploads WHERE id IN (?)", ids)
		_, err = tx.Exec(ctx, query, ids...)
		if err != nil {
			return nil, err
		}
	}

	return locations, tx.Commit(ctx)
}

// FileLinkAttachments connects given Topic or 消息 to the file record IDs from the list.
func (a *adapter) FileLinkAttachments(topic string, userId, msgId t.Uid, fids []string) error {
	if (topic == "" && msgId.IsZero() && userId.IsZero()) ||
		(len(fids) == 0 && msgId.IsZero()) {
		return t.ErrMalformed
	}
	now := t.TimeNow()

	var args []any
	var linkId any
	var linkBy string
	if !msgId.IsZero() {
		linkBy = "msgid"
		linkId = int64(msgId)
	} else if topic != "" {
		linkBy = "topic"
		linkId = topic
		// 目前每个 Topic 只允许一个附件。
		fids = fids[0:1]
	} else {
		linkBy = "userid"
		linkId = store.DecodeUid(userId)
		// Only one attachment per 用户 is permitted at this time.
		fids = fids[0:1]
	}

	// 已解码的 id
	var dids []any
	for _, fid := range fids {
		id := t.ParseUid(fid)
		if id.IsZero() {
			return t.ErrMalformed
		}
		dids = append(dids, store.DecodeUid(id))
	}

	for _, id := range dids {
		// 创建时间,文件ID,[消息ID|Topic|用户ID]
		args = append(args, now, id, linkId)
	}

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

	//消息也是可编辑的：替换其链接，而不是积累陈旧的文件。
	sql := "DELETE FROM filemsglinks WHERE " + linkBy + "=$1"
	_, err = tx.Exec(ctx, sql, linkId)
	if err != nil {
		return err
	}
	if len(dids) == 0 {
		return tx.Commit(ctx)
	}

	query, args := expandQuery("INSERT INTO filemsglinks(createdat,fileid,"+linkBy+") VALUES (?,?,?)"+
		strings.Repeat(",(?,?,?)", len(dids)-1), args...)
	_, err = tx.Exec(ctx, query, args...)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// FileLinkScheduled 建立定时消息与待投递文件的关联，防止文件提前回收。
func (a *adapter) FileLinkScheduled(scheduledId t.Uid, fids []string) error {
	if scheduledId.IsZero() || len(fids) == 0 {
		return t.ErrMalformed
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	for _, fid := range fids {
		fileID := t.ParseUid(fid)
		if fileID.IsZero() {
			return t.ErrMalformed
		}
		if _, err := a.db.Exec(ctx,
			`INSERT INTO scheduledfilelinks(scheduledid,fileid) VALUES($1,$2) ON CONFLICT DO NOTHING`,
			store.DecodeUid(scheduledId), store.DecodeUid(fileID)); err != nil {
			return err
		}
	}
	return nil
}
