//go:build mongodb

package mongodb

import (
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
