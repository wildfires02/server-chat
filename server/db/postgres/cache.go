//go:build postgres
// +build postgres

package postgres

import (
	pgx "github.com/jackc/pgx/v5"
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
	if err := a.db.QueryRow(ctx, `SELECT "value" FROM kvmeta WHERE "key"=$1 LIMIT 1`, key).Scan(&value); err != nil {
		if err == pgx.ErrNoRows {
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
	if !failOnDuplicate {
		action = ` ON CONFLICT ("key") DO UPDATE SET createdat=$2,"value"=$3`
	}

	_, err := a.db.Exec(ctx, `INSERT INTO kvmeta("key",createdat,"value") VALUES($1,$2,$3)`+action,
		key, t.TimeNow(), value)
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

	_, err := a.db.Exec(ctx, `DELETE FROM kvmeta WHERE "key"=$1`, key)
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

	_, err := a.db.Exec(ctx, `DELETE FROM kvmeta WHERE "key" LIKE $1 AND createdat<$2`, keyPrefix+"%", olderThan)
	return err
}
