//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	"time"

	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// FileStartUpload 初始化文件上传
func (a *adapter) FileStartUpload(fd *t.FileDef) error {
	_, err := rdb.DB(a.dbName).Table("fileuploads").Insert(fd).RunWrite(a.conn)
	return err
}

// FileFinishUpload 将文件上传标记为已完成，无论成功与否
func (a *adapter) FileFinishUpload(fd *t.FileDef, success bool, size int64) (*t.FileDef, error) {
	now := t.TimeNow()
	if success {
		if _, err := rdb.DB(a.dbName).Table("fileuploads").Get(fd.Uid()).
			Update(map[string]any{
				"UpdatedAt": now,
				"Status":    t.UploadCompleted,
				"Size":      size,
				"ETag":      fd.ETag,
				"Location":  fd.Location,
			}).RunWrite(a.conn); err != nil {

			return nil, err
		}
		fd.Status = t.UploadCompleted
		fd.Size = size
	} else {
		if _, err := rdb.DB(a.dbName).Table("fileuploads").Get(fd.Uid()).Delete().RunWrite(a.conn); err != nil {
			return nil, err
		}
		fd.Status = t.UploadFailed
		fd.Size = 0
	}
	fd.UpdatedAt = now

	return fd, nil
}

// FileGet 获取特定文件的记录
func (a *adapter) FileGet(fid string) (*t.FileDef, error) {
	cursor, err := rdb.DB(a.dbName).Table("fileuploads").Get(fid).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var fd t.FileDef
	if err = cursor.One(&fd); err != nil {
		return nil, err
	}

	return &fd, nil

}

// FileLinkAttachments 将给定的 Topic 或消息连接到列表中的文件记录 ID。
func (a *adapter) FileLinkAttachments(topic string, userId, msgId t.Uid, fids []string) error {
	if (topic == "" && userId.IsZero() && msgId.IsZero()) ||
		(len(fids) == 0 && msgId.IsZero()) {
		return t.ErrMalformed
	}

	now := t.TimeNow()
	var err error

	if msgId.IsZero() {
		// 每个用户或 Topic 只允许一个链接。
		fids = fids[0:1]

		// Topic 和用户是可变的。必须先取消关联之前的附件。
		var table string
		var linkId string
		if topic != "" {
			table = "topics"
			linkId = topic
		} else {
			table = "users"
			linkId = userId.String()
		}

		// 查找旧附件。
		var cursor *rdb.Cursor
		cursor, err = rdb.DB(a.dbName).Table(table).Get(linkId).
			Field("Attachments").Default([]string{}).Run(a.conn)
		if err != nil {
			return err
		}
		defer cursor.Close()

		if !cursor.IsNil() {
			var attachments []string
			if err = cursor.One(&attachments); err != nil {
				if err != rdb.ErrEmptyResult {
					return err
				}
				err = nil
			}

			if len(attachments) > 0 {
				// 减少旧附件的使用计数。
				if _, err = rdb.DB(a.dbName).Table("fileuploads").Get(attachments[0]).
					Update(map[string]any{
						"UpdatedAt": now,
						"UseCount":  rdb.Row.Field("UseCount").Default(1).Sub(1),
					}).RunWrite(a.conn); err != nil {
					return err
				}
			}
		}

		_, err = rdb.DB(a.dbName).Table(table).Get(linkId).
			Update(map[string]any{
				"UpdatedAt":   now,
				"Attachments": fids,
			}).RunWrite(a.conn)
		if err != nil {
			return err
		}
	} else {
		var cursor *rdb.Cursor
		cursor, err = rdb.DB(a.dbName).Table("messages").Get(msgId.String()).
			Field("Attachments").Default([]string{}).Run(a.conn)
		if err != nil {
			return err
		}
		var previous []string
		if !cursor.IsNil() {
			err = cursor.One(&previous)
		}
		cursor.Close()
		if err != nil && err != rdb.ErrEmptyResult {
			return err
		}
		counts := make(map[string]int)
		for _, id := range previous {
			counts[id]++
		}
		for id, count := range counts {
			if _, err = rdb.DB(a.dbName).Table("fileuploads").Get(id).
				Update(map[string]any{
					"UpdatedAt": now,
					"UseCount":  rdb.Row.Field("UseCount").Default(count).Sub(count),
				}).RunWrite(a.conn); err != nil {
				return err
			}
		}
		_, err := rdb.DB(a.dbName).Table("messages").Get(msgId.String()).
			Update(map[string]any{
				"UpdatedAt":   now,
				"Attachments": fids,
			}).RunWrite(a.conn)
		if err != nil {
			return err
		}
	}

	if len(fids) == 0 {
		return nil
	}
	ids := make([]any, len(fids))
	for i, id := range fids {
		ids[i] = id
	}

	_, err = rdb.DB(a.dbName).Table("fileuploads").GetAll(ids...).
		Update(map[string]any{
			"UpdatedAt": now,
			"UseCount":  rdb.Row.Field("UseCount").Default(0).Add(1),
		}).RunWrite(a.conn)

	return err
}

// FileDeleteUnused 删除孤立的文件上传。
func (a *adapter) FileDeleteUnused(olderThan time.Time, limit int, protected func(string) bool) ([]string, error) {
	q := rdb.DB(a.dbName).Table("fileuploads").GetAllByIndex("UseCount", 0)
	if !olderThan.IsZero() {
		q = q.Filter(rdb.Row.Field("UpdatedAt").Lt(olderThan))
	}
	if limit > 0 {
		q = q.Limit(limit)
	}

	cursor, err := q.Pluck("Id", "Location").Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var locations []string
	var ids []any
	var record struct {
		Id       string
		Location string
	}
	for cursor.Next(&record) {
		if protected != nil && protected(record.Id) {
			continue
		}
		ids = append(ids, record.Id)
		if record.Location != "" {
			locations = append(locations, record.Location)
		}
	}

	if err = cursor.Err(); err != nil {
		return nil, err
	}

	if len(ids) == 0 {
		return locations, nil
	}
	_, err = rdb.DB(a.dbName).Table("fileuploads").GetAll(ids...).Delete().RunWrite(a.conn)

	return locations, err
}

// 给定选择查询，减少 'fileuploads' 表中相应的使用计数。
// 'query' 必须返回数组，即 GetAll，而非 Get。
func (a *adapter) decFileUseCounter(query rdb.Term) error {
	cursor, err := query.Filter(rdb.Row.HasFields("Attachments")).
		Pluck("Attachments").Run(a.conn)
	if err != nil {
		return err
	}
	defer cursor.Close()

	var records []struct {
		Attachments []string
	}
	if err = cursor.All(&records); err != nil {
		return err
	}
	counts := make(map[string]int)
	for _, record := range records {
		for _, id := range record.Attachments {
			counts[id]++
		}
	}
	for id, count := range counts {
		if _, err = rdb.DB(a.dbName).Table("fileuploads").Get(id).
			Update(map[string]any{
				"UseCount": rdb.Row.Field("UseCount").Default(count).Sub(count),
			}).RunWrite(a.conn); err != nil {
			return err
		}
	}
	return nil
}

// FileLinkScheduled 把文件 ID 写入定时消息并增加引用计数，防止文件提前回收。
func (a *adapter) FileLinkScheduled(scheduledId t.Uid, fids []string) error {
	if scheduledId.IsZero() || len(fids) == 0 {
		return t.ErrMalformed
	}
	ids := make([]any, len(fids))
	for i, id := range fids {
		if t.ParseUid(id).IsZero() {
			return t.ErrMalformed
		}
		ids[i] = id
	}
	_, err := rdb.DB(a.dbName).Table("fileuploads").GetAll(ids...).
		Update(map[string]any{
			"UpdatedAt": t.TimeNow(),
			"UseCount":  rdb.Row.Field("UseCount").Default(0).Add(1),
		}).RunWrite(a.conn)
	return err
}
