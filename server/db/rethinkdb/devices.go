//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// DeviceUpsert 添加或更新用户的设备 FCM 推送令牌。
func (a *adapter) DeviceUpsert(uid t.Uid, def *t.DeviceDef) error {
	hash := deviceHasher(def.DeviceId)
	user := uid.String()

	// 确保设备 ID 的唯一性
	// 查找已使用此设备 ID 的用户，忽略当前用户。
	cursor, err := rdb.DB(a.dbName).Table("users").GetAllByIndex("DeviceIds", def.DeviceId).
		// 我们只关心用户 Id
		Pluck("Id").
		// 确保过滤掉可能合法使用此设备 ID 的当前用户
		Filter(rdb.Not(rdb.Row.Field("Id").Eq(user))).
		// 将对象切片转换为字符串切片
		ConcatMap(func(row rdb.Term) any { return []any{row.Field("Id")} }).
		// 执行
		Run(a.conn)
	if err != nil {
		return err
	}
	defer cursor.Close()

	var others []any
	if err = cursor.All(&others); err != nil {
		return err
	}

	if len(others) > 0 {
		// 删除其他用户的设备 ID。
		_, err = rdb.DB(a.dbName).Table("users").GetAll(others...).Replace(rdb.Row.Without(
			map[string]string{"Devices": hash})).RunWrite(a.conn)
		if err != nil {
			return err
		}
	}

	// 实际为新用户添加/更新 DeviceId
	_, err = rdb.DB(a.dbName).Table("users").Get(user).
		Update(map[string]any{
			"Devices": map[string]*t.DeviceDef{
				hash: def,
			}}).RunWrite(a.conn)
	return err
}

// DeviceGetAll 检索用户的设备列表（推送令牌）。
func (a *adapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
	ids := make([]any, len(uids))
	for i, id := range uids {
		ids[i] = id.String()
	}

	// {Id: "userid", Devices: {"hash1": {..def1..}, "hash2": {..def2..}}
	cursor, err := rdb.DB(a.dbName).Table("users").GetAll(ids...).Pluck("Id", "Devices").
		Default(nil).Limit(a.maxResults).Run(a.conn)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close()

	var row struct {
		Id      string
		Devices map[string]*t.DeviceDef
	}

	result := make(map[t.Uid][]t.DeviceDef)
	count := 0
	var uid t.Uid
	for cursor.Next(&row) {
		if len(row.Devices) > 0 {
			if err := uid.UnmarshalText([]byte(row.Id)); err != nil {
				continue
			}

			result[uid] = make([]t.DeviceDef, len(row.Devices))
			i := 0
			for _, def := range row.Devices {
				if def != nil {
					result[uid][i] = *def
					i++
					count++
				}
			}
		}
	}

	return result, count, cursor.Err()
}

// DeviceDelete 删除用户的设备（推送令牌）。
func (a *adapter) DeviceDelete(uid t.Uid, deviceID string) error {
	var err error
	q := rdb.DB(a.dbName).Table("users").Get(uid.String())
	if deviceID == "" {
		q = q.Update(map[string]any{"Devices": nil})
	} else {
		q = q.Replace(rdb.Row.Without(map[string]string{"Devices": deviceHasher(deviceID)}))
	}
	_, err = q.RunWrite(a.conn)
	return err
}

// 凭据管理
