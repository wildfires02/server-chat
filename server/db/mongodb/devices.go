//go:build mongodb

package mongodb

import (
	"strings"

	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/bson"
	mdb "go.mongodb.org/mongo-driver/mongo"
	mdbopts "go.mongodb.org/mongo-driver/mongo/options"
)

// DeviceUpsert 创建或更新设备记录。
func (a *adapter) DeviceUpsert(uid t.Uid, dev *t.DeviceDef) error {
	userId := uid.String()
	var user t.User
	err := a.db.Collection("users").FindOne(a.ctx, b.M{
		"_id":              userId,
		"devices.deviceid": dev.DeviceId}).Decode(&user)

	if err == nil && user.Id != "" { // current 用户 owns this device
		// 使用 ArrayFilter 避免添加重复设备对象，而是更新该设备数据
		updOpts := mdbopts.Update().SetArrayFilters(mdbopts.ArrayFilters{
			Filters: []any{b.M{"dev.deviceid": dev.DeviceId}}})
		_, err = a.db.Collection("users").UpdateOne(a.ctx,
			b.M{"_id": userId},
			b.M{"$set": b.M{
				"devices.$[dev].platform": dev.Platform,
				"devices.$[dev].lastseen": dev.LastSeen,
				"devices.$[dev].lang":     dev.Lang}},
			updOpts)
		return err
	} else if err == mdb.ErrNoDocuments { // device is free or owned by other 用户
		err = a.deviceInsert(userId, dev)

		if isDuplicateErr(err) {
			// Other 用户 owns this device.
			// We need to delete this device from that 用户 and then insert again
			if _, err = a.db.Collection("users").UpdateOne(a.ctx,
				b.M{"devices.deviceid": dev.DeviceId},
				b.M{"$pull": b.M{"devices": b.M{"deviceid": dev.DeviceId}}}); err != nil {

				return err
			}
			return a.deviceInsert(userId, dev)
		}
		if err != nil {
			return err
		}
		return nil
	}

	return err
}

// deviceInsert adds device object to 用户.devices array
func (a *adapter) deviceInsert(userId string, dev *t.DeviceDef) error {
	filter := b.M{"_id": userId}
	_, err := a.db.Collection("users").UpdateOne(a.ctx, filter,
		b.M{"$push": b.M{"devices": dev}})

	if err != nil && strings.Contains(err.Error(), "must be an array") {
		// 'devices' 字段不是数组。将其转为以 'dev' 为首个元素的数组
		_, err = a.db.Collection("users").UpdateOne(a.ctx, filter,
			b.M{"$set": b.M{"devices": []any{dev}}})
	}

	return err
}

// DeviceGetAll returns all devices for a given set of 用户.
func (a *adapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
	ids := make([]any, len(uids))
	for i, id := range uids {
		ids[i] = id.String()
	}

	filter := b.M{"_id": b.M{"$in": ids}}
	findOpts := mdbopts.Find().SetProjection(b.M{"_id": 1, "devices": 1})
	cur, err := a.db.Collection("users").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(a.ctx)

	result := make(map[t.Uid][]t.DeviceDef)
	count := 0
	var uid t.Uid
	for cur.Next(a.ctx) {
		var row struct {
			Id      string `bson:"_id"`
			Devices []t.DeviceDef
		}
		if err = cur.Decode(&row); err != nil {
			return nil, 0, err
		}
		if len(row.Devices) > 0 {
			if err := uid.UnmarshalText([]byte(row.Id)); err != nil {
				continue
			}

			result[uid] = row.Devices
			count++
		}
	}
	return result, count, cur.Err()
}

// DeviceDelete 删除设备记录（推送令牌）。
func (a *adapter) DeviceDelete(uid t.Uid, deviceID string) error {
	var err error
	filter := b.M{"_id": uid.String()}
	update := b.M{}
	if deviceID == "" {
		update["$set"] = b.M{"devices": []any{}}
	} else {
		update["$pull"] = b.M{"devices": b.M{"deviceid": deviceID}}
	}
	_, err = a.db.Collection("users").UpdateOne(a.ctx, filter, update)
	return err
}

// 文件上传记录。文件存储在数据库之外。
