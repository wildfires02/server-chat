//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	"strings"
	"time"

	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// PCacheGet 完成P缓存Get所需的内部处理。
func (a *adapter) PCacheGet(key string) (string, error) {
	cursor, err := rdb.DB(a.dbName).Table("kvmeta").Get(key).Run(a.conn)
	if err != nil {
		return "", err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return "", t.ErrNotFound
	}

	var result map[string]string
	if err = cursor.One(&result); err != nil {
		return "", err
	}

	return result["value"], nil
}

// PCacheUpsert 创建或更新持久缓存条目。
func (a *adapter) PCacheUpsert(key string, value string, failOnDuplicate bool) error {
	if strings.Contains(key, "^") {
		// 不允许键中包含 ^：它会干扰 Match() 查询。
		return t.ErrMalformed
	}

	doc := map[string]any{
		"key":   key,
		"value": value,
	}

	var action string
	if failOnDuplicate {
		action = "error"
		doc["CreatedAt"] = t.TimeNow()
	} else {
		action = "update"
	}

	_, err := rdb.DB(a.dbName).Table("kvmeta").Insert(doc, rdb.InsertOpts{Conflict: action}).RunWrite(a.conn)
	if rdb.IsConflictErr(err) {
		return t.ErrDuplicate
	}

	return err
}

// PCacheDelete 删除一个持久缓存条目。
func (a *adapter) PCacheDelete(key string) error {
	_, err := rdb.DB(a.dbName).Table("kvmeta").Get(key).Delete().RunWrite(a.conn)
	return err
}

// PCacheExpire 使具有给定键前缀的旧条目过期。
func (a *adapter) PCacheExpire(keyPrefix string, olderThan time.Time) error {
	if keyPrefix == "" {
		return t.ErrMalformed
	}

	_, err := rdb.DB(a.dbName).Table("kvmeta").
		Filter(rdb.Row.Field("CreatedAt").Lt(olderThan).And(rdb.Row.Field("key").Match("^" + keyPrefix))).
		Delete().
		RunWrite(a.conn)

	return err
}

// PCacheList 按键前缀返回持久缓存条目。
func (a *adapter) PCacheList(keyPrefix string, limit int) (map[string]string, error) {
	if keyPrefix == "" || strings.Contains(keyPrefix, "^") || limit <= 0 {
		return nil, t.ErrMalformed
	}
	cursor, err := rdb.DB(a.dbName).Table("kvmeta").
		Filter(rdb.Row.Field("key").Match("^" + keyPrefix)).
		OrderBy("CreatedAt").Limit(limit).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()
	var entries []map[string]any
	if err = cursor.All(&entries); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, keyOK := entry["key"].(string)
		value, valueOK := entry["value"].(string)
		if keyOK && valueOK {
			result[key] = value
		}
	}
	return result, nil
}

// PCacheCompareAndSwap 在数据库中原子更新匹配的条目。
func (a *adapter) PCacheCompareAndSwap(key, oldValue, newValue string) (bool, error) {
	result, err := rdb.DB(a.dbName).Table("kvmeta").GetAll(key).
		Filter(rdb.Row.Field("value").Eq(oldValue)).
		Update(map[string]any{"value": newValue, "CreatedAt": t.TimeNow()}).
		RunWrite(a.conn)
	if err != nil {
		return false, err
	}
	return result.Replaced == 1, nil
}
