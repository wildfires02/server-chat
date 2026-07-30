//go:build mysql || (!postgres && !mongodb && !rethinkdb)
// +build mysql !postgres,!mongodb,!rethinkdb

package mysql

import (
	"database/sql"
	"hash/fnv"
	"strconv"
	"time"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	"github.com/jmoiron/sqlx"
)

// deviceHasher 完成设备Hasher所需的内部处理。
func deviceHasher(deviceID string) string {
	// 生成自定义键作为设备 ID 的 64 位哈希，以确保
	// 键的长度可预测
	hasher := fnv.New64()
	hasher.Write([]byte(deviceID))
	return strconv.FormatUint(uint64(hasher.Sum64()), 16)
}

// 设备管理（用于推送通知）。

// DeviceUpsert 创建或更新设备记录。
func (a *adapter) DeviceUpsert(uid t.Uid, def *t.DeviceDef) error {
	hash := deviceHasher(def.DeviceId)

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

	// 确保设备 ID 的唯一性：删除该设备 ID 的所有记录
	_, err = tx.Exec("DELETE FROM devices WHERE hash=?", hash)
	if err != nil {
		return err
	}

	// Actually add/update DeviceId for the new 用户
	_, err = tx.Exec("INSERT INTO devices(userid, hash, deviceId, platform, lastseen, lang) VALUES(?,?,?,?,?,?)",
		store.DecodeUid(uid), hash, def.DeviceId, def.Platform, def.LastSeen, def.Lang)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// DeviceGetAll returns all devices for a given set of 用户.
func (a *adapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
	unums := common.DecodeUidSlice(uids)

	q, unums, _ := sqlx.In("SELECT userid,deviceid,platform,lastseen,lang FROM devices WHERE userid IN (?)", unums)
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, unums...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var device struct {
		Userid   int64
		Deviceid string
		Platform string
		Lastseen time.Time
		Lang     string
	}

	result := make(map[t.Uid][]t.DeviceDef)
	count := 0
	for rows.Next() {
		if err = rows.StructScan(&device); err != nil {
			break
		}
		common.AddDeviceToMap(result, device.Userid, device.Deviceid, device.Platform, device.Lastseen, device.Lang)
		count++
	}
	if err == nil {
		err = rows.Err()
	}

	return result, count, err
}

// deviceDelete 完成设备删除所需的内部处理。
func deviceDelete(tx *sqlx.Tx, uid t.Uid, deviceID string) error {
	var err error
	var res sql.Result
	if deviceID == "" {
		res, err = tx.Exec("DELETE FROM devices WHERE userid=?", store.DecodeUid(uid))
	} else {
		res, err = tx.Exec("DELETE FROM devices WHERE userid=? AND hash=?", store.DecodeUid(uid), deviceHasher(deviceID))
	}

	if err == nil {
		if count, _ := res.RowsAffected(); count == 0 {
			err = t.ErrNotFound
		}
	}

	return err
}

// DeviceDelete 删除设备记录（推送令牌）。
func (a *adapter) DeviceDelete(uid t.Uid, deviceID string) error {
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

	err = deviceDelete(tx, uid, deviceID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// 凭据管理
