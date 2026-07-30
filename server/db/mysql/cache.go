//go:build mysql || (!postgres && !mongodb && !rethinkdb)
// +build mysql !postgres,!mongodb,!rethinkdb

package mysql

import (
	"database/sql"
	"strings"
	"time"

	t "chat/server/store/types"
)

// PCacheGet 完成P缓存Get所需的内部处理。
func (a *adapter) PCacheGet(key string) (string, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	var value string
	if err := a.db.GetContext(ctx, &value, "SELECT `value` FROM kvmeta WHERE `key`=? LIMIT 1", key); err != nil {
		if err == sql.ErrNoRows {
			return "", t.ErrNotFound
		}
		return "", err
	}
	return value, nil
}

// PCacheUpsert 创建或更新持久缓存条目。
func (a *adapter) PCacheUpsert(key string, value string, failOnDuplicate bool) error {
	if strings.Contains(key, "%") {
		// 不允许键中包含 %：它会干扰 LIKE 查询。
		return t.ErrMalformed
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	var action string
	if failOnDuplicate {
		action = "INSERT"
	} else {
		action = "REPLACE"
	}

	_, err := a.db.ExecContext(ctx, action+" INTO kvmeta(`key`,createdat,`value`) VALUES(?,?,?)", key, t.TimeNow(), value)
	if isDupe(err) {
		return t.ErrDuplicate
	}
	return err
}

// PCacheDelete 删除一条持久缓存条目。
func (a *adapter) PCacheDelete(key string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	_, err := a.db.ExecContext(ctx, "DELETE FROM kvmeta WHERE `key`=?", key)
	return err
}

// PCacheExpire 使具有给定键前缀的旧条目过期。
func (a *adapter) PCacheExpire(keyPrefix string, olderThan time.Time) error {
	if keyPrefix == "" {
		return t.ErrMalformed
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	_, err := a.db.ExecContext(ctx, "DELETE FROM kvmeta WHERE `key` LIKE ? AND createdat<?", keyPrefix+"%", olderThan)
	return err
}

// PCacheList 按键前缀返回最早写入的条目。
func (a *adapter) PCacheList(keyPrefix string, limit int) (map[string]string, error) {
	if keyPrefix == "" || strings.ContainsAny(keyPrefix, "%_") || limit <= 0 {
		return nil, t.ErrMalformed
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx,
		"SELECT `key`,`value` FROM kvmeta WHERE `key` LIKE ? ORDER BY createdat LIMIT ?",
		keyPrefix+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, rows.Err()
}

// PCacheCompareAndSwap 在数据库中原子更新匹配的条目。
func (a *adapter) PCacheCompareAndSwap(key, oldValue, newValue string) (bool, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	result, err := a.db.ExecContext(ctx,
		"UPDATE kvmeta SET createdat=?,`value`=? WHERE `key`=? AND `value`=?",
		t.TimeNow(), newValue, key, oldValue)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}
