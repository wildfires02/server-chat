//go:build mysql || (!postgres && !mongodb && !rethinkdb)
// +build mysql !postgres,!mongodb,!rethinkdb

package mysql

import (
	"database/sql"
	"strings"
	"time"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	"github.com/jmoiron/sqlx"
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
	} else {
		user = 0
	}
	_, err := a.db.ExecContext(ctx,
		"INSERT INTO fileuploads(id,createdat,updatedat,userid,status,mimetype,size,etag,location) "+
			"VALUES(?,?,?,?,?,?,?,?,?)",
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
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	now := t.TimeNow()
	if success {
		_, err = tx.ExecContext(ctx, "UPDATE fileuploads SET updatedat=?,status=?,size=?,etag=?,location=? WHERE id=?",
			now, t.UploadCompleted, size, fd.ETag, fd.Location, store.DecodeUid(fd.Uid()))
		if err != nil {
			return nil, err
		}

		fd.Status = t.UploadCompleted
		fd.Size = size
	} else {
		// 删除记录：保留在数据库中没有意义。
		_, err = tx.ExecContext(ctx, "DELETE FROM fileuploads WHERE id=?", store.DecodeUid(fd.Uid()))
		if err != nil {
			return nil, err
		}

		fd.Status = t.UploadFailed
		fd.Size = 0
	}
	fd.UpdatedAt = now

	return fd, tx.Commit()
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
	err := a.db.GetContext(ctx, &fd, "SELECT id,createdat,updatedat,userid AS user,status,mimetype,size,IFNULL(etag,'') AS etag,location "+
		"FROM fileuploads WHERE id=?", store.DecodeUid(id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	fd.Id = common.EncodeUidString(fd.Id).String()
	fd.User = common.EncodeUidString(fd.User).String()

	return &fd, nil
}

// FileDeleteUnused 删除 UseCount 为零的记录。若 olderThan 非零，则删除
// UpdatedAt 早于 olderThan 的未使用记录。
// 返回已删除文件记录的 FileDef.Location 数组，以便同时删除实际文件。
func (a *adapter) FileDeleteUnused(olderThan time.Time, limit int, protected func(string) bool) ([]string, error) {
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

	rows, err := tx.Query(query, args...)
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
		query, ids, _ = sqlx.In("DELETE FROM fileuploads WHERE id IN (?)", ids)
		_, err = tx.Exec(query, ids...)
		if err != nil {
			return nil, err
		}
	}

	return locations, tx.Commit()
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
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Messages are editable too: replace their links instead of accumulating stale files.
	sql := "DELETE FROM filemsglinks WHERE " + linkBy + "=?"
	_, err = tx.Exec(sql, linkId)
	if err != nil {
		return err
	}

	if len(dids) == 0 {
		return tx.Commit()
	}
	sql = "INSERT INTO filemsglinks(createdat,fileid," + linkBy + ") VALUES (?,?,?)"
	_, err = tx.Exec(sql+strings.Repeat(",(?,?,?)", len(dids)-1), args...)
	if err != nil {
		return err
	}

	return tx.Commit()
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
		if _, err := a.db.ExecContext(ctx,
			"INSERT IGNORE INTO scheduledfilelinks(scheduledid,fileid) VALUES(?,?)",
			store.DecodeUid(scheduledId), store.DecodeUid(fileID)); err != nil {
			return err
		}
	}
	return nil
}
