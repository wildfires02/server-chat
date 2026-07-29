//go:build postgres
// +build postgres

package postgres

import (
	"context"
	pgx "github.com/jackc/pgx/v5"
	"hash/fnv"
	"strconv"
	"time"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	"github.com/jackc/pgx/v5/pgconn"
)

// deviceHasher 完成设备Hasher所需的内部处理。
func deviceHasher(deviceID string) string {
	// 生成自定义键作为设备 ID 的 64 位哈希，以确保
	// 键的长度可预测
	hasher := fnv.New64()
	hasher.Write([]byte(deviceID))
	return strconv.FormatUint(uint64(hasher.Sum64()), 16)
}

// 设备管理（用于推送通知）
func (a *adapter) DeviceUpsert(uid t.Uid, def *t.DeviceDef) error {
	hash := deviceHasher(def.DeviceId)

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

	// 确保设备 ID 的唯一性：删除该设备 ID 的所有记录
	_, err = tx.Exec(ctx, "DELETE FROM devices WHERE hash=$1", hash)
	if err != nil {
		return err
	}

	// Actually add/update DeviceId for the new 用户
	_, err = tx.Exec(ctx, "INSERT INTO devices(userid, hash, deviceId, platform, lastseen, lang) VALUES($1,$2,$3,$4,$5,$6)",
		store.DecodeUid(uid), hash, def.DeviceId, def.Platform, def.LastSeen, def.Lang)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// DeviceGetAll 完成设备GetAll所需的内部处理。
func (a *adapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
	unums := common.DecodeUidSlice(uids)

	query, unums := expandQuery("SELECT userid,deviceid,platform,lastseen,lang FROM devices WHERE userid IN (?)", unums)
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, query, unums...)
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
		if err = rows.Scan(&device.Userid, &device.Deviceid, &device.Platform, &device.Lastseen, &device.Lang); err != nil {
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
func deviceDelete(ctx context.Context, tx pgx.Tx, uid t.Uid, deviceID string) error {
	var err error
	var res pgconn.CommandTag
	if deviceID == "" {
		res, err = tx.Exec(ctx, "DELETE FROM devices WHERE userid=$1", store.DecodeUid(uid))
	} else {
		res, err = tx.Exec(ctx, "DELETE FROM devices WHERE userid=$1 AND hash=$2", store.DecodeUid(uid), deviceHasher(deviceID))
	}

	if err == nil {
		if count := res.RowsAffected(); count == 0 {
			err = t.ErrNotFound
		}
	}

	return err
}

// DeviceDelete 完成设备删除所需的内部处理。
func (a *adapter) DeviceDelete(uid t.Uid, deviceID string) error {
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

	err = deviceDelete(ctx, tx, uid, deviceID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// 凭据管理
