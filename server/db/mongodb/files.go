//go:build mongodb

package mongodb

import (
	"context"
	"time"

	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/v2/bson"
	mdb "go.mongodb.org/mongo-driver/v2/mongo"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// FileStartUpload 初始化文件上传
func (a *adapter) FileStartUpload(fd *t.FileDef) error {
	_, err := a.db.Collection("fileuploads").InsertOne(a.ctx, fd)
	return err
}

// FileFinishUpload 标记文件上传完成，无论成功与否。
func (a *adapter) FileFinishUpload(fd *t.FileDef, success bool, size int64) (*t.FileDef, error) {
	now := t.TimeNow()
	if success {
		// 标记上传完成。
		if _, err := a.db.Collection("fileuploads").UpdateOne(a.ctx,
			b.M{"_id": fd.Id},
			b.M{"$set": b.M{
				"updatedat": now,
				"status":    t.UploadCompleted,
				"size":      size,
				"etag":      fd.ETag,
				"location":  fd.Location,
			}}); err != nil {

			return nil, err
		}
		fd.Status = t.UploadCompleted
		fd.Size = size
	} else {
		// 删除记录：已无用。
		if _, err := a.db.Collection("fileuploads").DeleteOne(a.ctx, b.M{"_id": fd.Id}); err != nil {
			return nil, err
		}
		fd.Status = t.UploadFailed
		fd.Size = 0
	}

	fd.UpdatedAt = now

	return fd, nil
}

// FileGet 获取指定文件的记录
func (a *adapter) FileGet(fid string) (*t.FileDef, error) {
	var fd t.FileDef
	err := a.db.Collection("fileuploads").FindOne(a.ctx, b.M{"_id": fid}).Decode(&fd)
	if err != nil {
		if err == mdb.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	return &fd, nil
}

// FileDeleteUnused 删除 UseCount 为零的记录。若 olderThan 非零，则删除
// UpdatedAt 早于 olderThan 的未使用记录。
// 返回已删除文件记录的 FileDef.Location 数组，以便同时删除实际文件。
func (a *adapter) FileDeleteUnused(olderThan time.Time, limit int, protected func(string) bool) ([]string, error) {
	findOpts := mdbopts.Find()
	filter := b.M{"$or": b.A{
		b.M{"usecount": 0},
		b.M{"usecount": b.M{"$exists": false}}}}
	if !olderThan.IsZero() {
		filter["updatedat"] = b.M{"$lt": olderThan}
	}
	if limit > 0 {
		findOpts.SetLimit(int64(limit))
	}

	findOpts.SetProjection(b.M{"location": 1, "_id": 1})
	cur, err := a.db.Collection("fileuploads").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var locations []string
	var ids []string
	for cur.Next(a.ctx) {
		var result struct {
			Id       string `bson:"_id"`
			Location string `bson:"location"`
		}
		if err := cur.Decode(&result); err != nil {
			return nil, err
		}
		if protected != nil && protected(result.Id) {
			continue
		}
		ids = append(ids, result.Id)
		if result.Location != "" {
			locations = append(locations, result.Location)
		}
	}
	if err = cur.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return locations, nil
	}
	_, err = a.db.Collection("fileuploads").DeleteMany(a.ctx, b.M{"_id": b.M{"$in": ids}})
	return locations, err
}

// 给定针对 '消息' 集合的过滤查询，递减 'fileuploads' 表中对应的使用计数器。
func (a *adapter) decFileUseCounter(ctx context.Context, collection string, msgFilter b.M) error {
	// 复制 msgFilter
	filter := b.M{}
	for k, v := range msgFilter {
		filter[k] = v
	}
	filter["attachments"] = b.M{"$exists": true}
	cur, err := a.db.Collection(collection).Find(ctx, filter,
		mdbopts.Find().SetProjection(b.M{"attachments": 1, "_id": 0}))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)

	counts := make(map[string]int)
	for cur.Next(ctx) {
		var record struct {
			Attachments []string `bson:"attachments"`
		}
		if err = cur.Decode(&record); err != nil {
			return err
		}
		for _, id := range record.Attachments {
			counts[id]++
		}
	}
	if err = cur.Err(); err != nil {
		return err
	}
	for id, count := range counts {
		if _, err = a.db.Collection("fileuploads").UpdateOne(ctx,
			b.M{"_id": id}, b.M{"$inc": b.M{"usecount": -count}}); err != nil {
			return err
		}
	}
	return nil
}

// FileLinkAttachments connects given Topic or 消息 to the file record IDs from the list.
func (a *adapter) FileLinkAttachments(topic string, userId, msgId t.Uid, fids []string) error {
	if (topic == "" && userId.IsZero() && msgId.IsZero()) ||
		(len(fids) == 0 && msgId.IsZero()) {
		return t.ErrMalformed
	}

	now := t.TimeNow()
	var err error

	if msgId.IsZero() {
		// Only one link per 用户 or Topic is permitted.
		fids = fids[0:1]

		// Topic and 用户 and mutable. Must unlink the previous attachments first.
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
		var attachments map[string][]string
		findOpts := mdbopts.FindOne().SetProjection(b.M{"attachments": 1, "_id": 0})
		err = a.db.Collection(table).FindOne(a.ctx, b.M{"_id": linkId}, findOpts).Decode(&attachments)
		if err != nil {
			return err
		}

		if len(attachments["attachments"]) > 0 {
			// 递减旧附件的使用计数。
			if _, err = a.db.Collection("fileuploads").UpdateOne(a.ctx,
				b.M{"_id": attachments["attachments"][0]},
				b.M{
					"$set": b.M{"updatedat": now},
					"$inc": b.M{"usecount": -1},
				},
			); err != nil {
				return err
			}
		}

		_, err = a.db.Collection(table).UpdateOne(a.ctx,
			b.M{"_id": linkId},
			b.M{"$set": b.M{"updatedat": now, "attachments": fids}})
		if err != nil {
			return err
		}
	} else {
		var previous struct {
			Attachments []string `bson:"attachments"`
		}
		findOpts := mdbopts.FindOne().SetProjection(b.M{"attachments": 1, "_id": 0})
		if err = a.db.Collection("messages").FindOne(a.ctx,
			b.M{"_id": msgId.String()}, findOpts).Decode(&previous); err != nil {
			return err
		}
		if len(previous.Attachments) > 0 {
			counts := make(map[string]int)
			for _, id := range previous.Attachments {
				counts[id]++
			}
			for id, count := range counts {
				if _, err = a.db.Collection("fileuploads").UpdateOne(a.ctx,
					b.M{"_id": id}, b.M{
						"$set": b.M{"updatedat": now},
						"$inc": b.M{"usecount": -count},
					}); err != nil {
					return err
				}
			}
		}
		_, err = a.db.Collection("messages").UpdateOne(a.ctx,
			b.M{"_id": msgId.String()},
			b.M{"$set": b.M{"updatedat": now, "attachments": fids}})
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
	_, err = a.db.Collection("fileuploads").UpdateMany(a.ctx,
		b.M{"_id": b.M{"$in": ids}},
		b.M{
			"$set": b.M{"updatedat": now},
			"$inc": b.M{"usecount": 1},
		},
	)

	return err
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
	_, err := a.db.Collection("fileuploads").UpdateMany(a.ctx,
		b.M{"_id": b.M{"$in": ids}},
		b.M{
			"$set": b.M{"updatedat": t.TimeNow()},
			"$inc": b.M{"usecount": 1},
		})
	return err
}
