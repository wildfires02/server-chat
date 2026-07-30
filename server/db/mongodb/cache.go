//go:build mongodb

package mongodb

import (
	"regexp"
	"strings"
	"time"

	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/v2/bson"
	mdb "go.mongodb.org/mongo-driver/v2/mongo"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// PCacheGet 完成P缓存Get所需的内部处理。
func (a *adapter) PCacheGet(key string) (string, error) {
	var value map[string]string
	findOpts := mdbopts.FindOne().SetProjection(b.M{"value": 1, "_id": 0})
	if err := a.db.Collection("kvmeta").FindOne(a.ctx, b.M{"_id": key}, findOpts).Decode(&value); err != nil {
		if err == mdb.ErrNoDocuments {
			err = t.ErrNotFound
		}
		return "", err
	}
	return value["value"], nil
}

// PCacheUpsert 创建或更新持久缓存条目。
func (a *adapter) PCacheUpsert(key string, value string, failOnDuplicate bool) error {
	if strings.Contains(key, "^") {
		// 不允许键中包含 ^：它会干扰 $match 查询。
		return t.ErrMalformed
	}

	collection := a.db.Collection("kvmeta")
	doc := b.M{
		"value": value,
	}

	if failOnDuplicate {
		doc["_id"] = key
		doc["createdat"] = t.TimeNow()
		_, err := collection.InsertOne(a.ctx, doc)
		if mdb.IsDuplicateKeyError(err) {
			err = t.ErrDuplicate
		}
		return err
	}

	res := collection.FindOneAndUpdate(a.ctx, b.M{"_id": key}, b.M{"$set": doc},
		mdbopts.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(mdbopts.After))
	return res.Err()
}

// PCacheDelete 删除一条持久缓存条目。
func (a *adapter) PCacheDelete(key string) error {
	_, err := a.db.Collection("kvmeta").DeleteOne(a.ctx, b.M{"_id": key})
	return err
}

// PCacheExpire 使具有给定键前缀的旧条目过期。
func (a *adapter) PCacheExpire(keyPrefix string, olderThan time.Time) error {
	if keyPrefix == "" {
		return t.ErrMalformed
	}

	_, err := a.db.Collection("kvmeta").DeleteMany(a.ctx, b.M{"createdat": b.M{"$lt": olderThan},
		"_id": b.Regex{Pattern: "^" + keyPrefix}})
	return err
}

// PCacheList 按键前缀返回最早写入的条目。
func (a *adapter) PCacheList(keyPrefix string, limit int) (map[string]string, error) {
	if keyPrefix == "" || limit <= 0 {
		return nil, t.ErrMalformed
	}
	options := mdbopts.Find().
		SetProjection(b.M{"value": 1}).
		SetSort(b.D{{Key: "createdat", Value: 1}}).
		SetLimit(int64(limit))
	cursor, err := a.db.Collection("kvmeta").Find(a.ctx,
		b.M{"_id": b.Regex{Pattern: "^" + regexp.QuoteMeta(keyPrefix)}}, options)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(a.ctx)
	var entries []struct {
		Key   string `bson:"_id"`
		Value string `bson:"value"`
	}
	if err = cursor.All(a.ctx, &entries); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		result[entry.Key] = entry.Value
	}
	return result, nil
}

// PCacheCompareAndSwap 在数据库中原子更新匹配的条目。
func (a *adapter) PCacheCompareAndSwap(key, oldValue, newValue string) (bool, error) {
	result, err := a.db.Collection("kvmeta").UpdateOne(a.ctx,
		b.M{"_id": key, "value": oldValue},
		b.M{"$set": b.M{"value": newValue, "createdat": t.TimeNow()}})
	if err != nil {
		return false, err
	}
	return result.ModifiedCount == 1, nil
}
