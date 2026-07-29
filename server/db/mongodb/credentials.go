//go:build mongodb

package mongodb

import (
	"context"
	"time"

	"chat/server/auth"
	"chat/server/db/common"
	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/v2/bson"
	mdb "go.mongodb.org/mongo-driver/v2/mongo"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// CredUpsert 添加或更新验证记录。返回 true 表示插入，false 表示更新。
// 1. 如果凭据已验证：
// 1.1 硬删除未确认的等效记录（如果存在）。
// 1.2 插入新记录。重复则报告错误。
// 2. 如果凭据未验证：
// 2.1 检查已验证的等效记录是否存在。若存在则报告错误。
// 2.2 软删除同一方法的所有未验证记录。
// 2.3 恢复已有凭据。成功则返回。
// 2.4 插入新凭据记录。
func (a *adapter) CredUpsert(cred *t.Credential) (bool, error) {
	credCollection := a.db.Collection("credentials")

	cred.Id = cred.Method + ":" + cred.Value

	if !cred.Done {
		// 检查是否 the same credential is already validated.
		var result1 t.Credential
		err := credCollection.FindOne(a.ctx, b.M{"_id": cred.Id}).Decode(&result1)
		if result1 != (t.Credential{}) {
			// 已有人验证了此凭据。
			return false, t.ErrDuplicate
		}
		if err != nil && err != mdb.ErrNoDocuments { // if no result -> continue
			return false, err
		}

		// 软删除此用户和方法的所有未验证记录。
		_, err = credCollection.UpdateMany(a.ctx,
			b.M{"user": cred.User, "method": cred.Method, "done": false},
			b.M{"$set": b.M{"deletedat": t.TimeNow()}})
		if err != nil {
			return false, err
		}

		// 如果凭据未确认，不应阻止其他人尝试验证：
		// 使索引为用户唯一而非全局唯一。
		cred.Id = cred.User + ":" + cred.Id

		// 检查此凭据是否已被用户添加。
		var result2 t.Credential
		err = credCollection.FindOne(a.ctx, b.M{"_id": cred.Id}).Decode(&result2)
		if result2 != (t.Credential{}) {
			_, err = credCollection.UpdateOne(a.ctx,
				b.M{"_id": cred.Id},
				b.M{
					"$unset": b.M{"deletedat": ""},
					"$set":   b.M{"updatedat": cred.UpdatedAt, "resp": cred.Resp}})
			if err != nil {
				return false, err
			}

			// 记录已更新，一切正常。
			return false, nil
		}
		if err != nil && err != mdb.ErrNoDocuments {
			return false, err
		}
	} else {
		// 硬删除可能存在的未验证凭据。
		_, err := credCollection.DeleteOne(a.ctx, b.M{"_id": cred.User + ":" + cred.Id})
		if err != nil {
			return false, err
		}
	}

	// 插入新记录。
	_, err := credCollection.InsertOne(a.ctx, cred)
	if isDuplicateErr(err) {
		return true, t.ErrDuplicate
	}

	return true, err
}

// CredGetActive 返回指定方法当前活跃的凭据记录。
func (a *adapter) CredGetActive(uid t.Uid, method string) (*t.Credential, error) {
	var cred t.Credential

	filter := b.M{
		"user":      uid.String(),
		"deletedat": b.M{"$exists": false},
		"method":    method,
		"done":      false}

	if err := a.db.Collection("credentials").FindOne(a.ctx, filter).Decode(&cred); err != nil {
		if err == mdb.ErrNoDocuments { // 未找到凭据
			err = nil
		}
		return nil, err
	}

	return &cred, nil
}

// CredGetAll 返回指定用户和方法的凭据记录，可选仅已验证或全部。
func (a *adapter) CredGetAll(uid t.Uid, method string, validatedOnly bool) ([]t.Credential, error) {
	filter := b.M{"user": uid.String()}
	if method != "" {
		filter["method"] = method
	}
	if validatedOnly {
		filter["done"] = true
	} else {
		filter["deletedat"] = b.M{"$exists": false}
	}

	cur, err := a.db.Collection("credentials").Find(a.ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var credentials []t.Credential
	if err := cur.All(a.ctx, &credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

// CredDel 删除指定方法/值的凭据。若方法为空，则删除用户的所有凭据。
func (a *adapter) credDel(ctx context.Context, uid t.Uid, method, value string) error {
	credCollection := a.db.Collection("credentials")
	filter := b.M{"user": uid.String()}
	if method != "" {
		filter["method"] = method
		if value != "" {
			filter["value"] = value
		}
	} else {
		res, err := credCollection.DeleteMany(ctx, filter)
		if err == nil {
			if res.DeletedCount == 0 {
				err = t.ErrNotFound
			}
		}
		return err
	}

	// 硬删除所有已确认的值或未尝试确认的值。
	hardDeleteFilter := copyBsonMap(filter)
	hardDeleteFilter["$or"] = b.A{
		b.M{"done": true},
		b.M{"retries": 0}}
	if res, err := credCollection.DeleteMany(ctx, hardDeleteFilter); err != nil {
		return err
	} else if res.DeletedCount > 0 {
		return nil
	}

	// 软删除所有其他值。
	res, err := credCollection.UpdateMany(ctx, filter, b.M{"$set": b.M{"deletedat": t.TimeNow()}})
	if err == nil {
		if res.ModifiedCount == 0 {
			err = t.ErrNotFound
		}
	}
	return err
}

// CredDel 完成凭据Del所需的内部处理。
func (a *adapter) CredDel(uid t.Uid, method, value string) error {
	return a.credDel(a.ctx, uid, method, value)
}

// CredConfirm 将指定凭据标记为已验证。
func (a *adapter) CredConfirm(uid t.Uid, method string) error {
	cred, err := a.CredGetActive(uid, method)
	if err != nil {
		return err
	}

	cred.Done = true
	cred.UpdatedAt = t.TimeNow()
	if _, err = a.CredUpsert(cred); err != nil {
		return err
	}

	_, _ = a.db.Collection("credentials").DeleteOne(a.ctx, b.M{"_id": uid.String() + ":" + cred.Method + ":" + cred.Value})
	return nil
}

// CredFail 增加指定凭据验证失败次数。
func (a *adapter) CredFail(uid t.Uid, method string) error {
	filter := b.M{
		"user":      uid.String(),
		"deletedat": b.M{"$exists": false},
		"method":    method,
		"done":      false}

	update := b.M{
		"$inc": b.M{"retries": 1},
		"$set": b.M{"updatedat": t.TimeNow()}}
	_, err := a.db.Collection("credentials").UpdateOne(a.ctx, filter, update)
	return err
}

// 基础认证方案的认证管理

// AuthGetUniqueRecord 根据唯一值（如登录名）返回认证记录。
func (a *adapter) AuthGetUniqueRecord(unique string) (t.Uid, auth.Level, []byte, time.Time, error) {
	var record struct {
		UserId  string
		AuthLvl auth.Level
		Secret  []byte
		Expires time.Time
	}

	filter := b.M{"_id": unique}
	findOpts := mdbopts.FindOne().SetProjection(b.M{
		"userid":  1,
		"authlvl": 1,
		"secret":  1,
		"expires": 1,
	})
	if err := a.db.Collection("auth").FindOne(a.ctx, filter, findOpts).Decode(&record); err != nil {
		if err == mdb.ErrNoDocuments {
			return t.ZeroUid, 0, nil, time.Time{}, nil
		}
		return t.ZeroUid, 0, nil, time.Time{}, err
	}

	return t.ParseUid(record.UserId), record.AuthLvl, record.Secret, record.Expires, nil
}

// AuthGetRecord returns authentication record given 用户 ID and method.
func (a *adapter) AuthGetRecord(uid t.Uid, scheme string) (string, auth.Level, []byte, time.Time, error) {
	var record struct {
		Id      string `bson:"_id"`
		AuthLvl auth.Level
		Secret  []byte
		Expires time.Time
	}

	filter := b.M{"userid": uid.String(), "scheme": scheme}
	findOpts := mdbopts.FindOne().SetProjection(b.M{
		"authlvl": 1,
		"secret":  1,
		"expires": 1,
	})
	err := a.db.Collection("auth").FindOne(a.ctx, filter, findOpts).Decode(&record)
	if err != nil {
		if err == mdb.ErrNoDocuments {
			err = t.ErrNotFound
		}
		return "", 0, nil, time.Time{}, err
	}

	return record.Id, record.AuthLvl, record.Secret, record.Expires, nil
}

// AuthAddRecord 创建新的认证记录
func (a *adapter) AuthAddRecord(uid t.Uid, scheme, unique string, authLvl auth.Level, secret []byte, expires time.Time) error {
	authRecord := b.M{
		"_id":     unique,
		"userid":  uid.String(),
		"scheme":  scheme,
		"authlvl": authLvl,
		"secret":  secret,
		"expires": expires}
	if _, err := a.db.Collection("auth").InsertOne(a.ctx, authRecord); err != nil {
		if isDuplicateErr(err) {
			return t.ErrDuplicate
		}
		return err
	}
	return nil
}

// AuthDelScheme 删除用户的指定认证方案。
func (a *adapter) AuthDelScheme(uid t.Uid, scheme string) error {
	_, err := a.db.Collection("auth").DeleteOne(a.ctx,
		b.M{
			"userid": uid.String(),
			"scheme": scheme})
	return err
}

// authDelAllRecords 完成认证DelAllRecords所需的内部处理。
func (a *adapter) authDelAllRecords(ctx context.Context, uid t.Uid) (int, error) {
	res, err := a.db.Collection("auth").DeleteMany(ctx, b.M{"userid": uid.String()})
	return int(res.DeletedCount), err
}

// AuthDelAllRecords 删除指定用户的所有认证记录。
func (a *adapter) AuthDelAllRecords(uid t.Uid) (int, error) {
	return a.authDelAllRecords(a.ctx, uid)
}

// AuthUpdRecord 修改认证记录。
func (a *adapter) AuthUpdRecord(uid t.Uid, scheme, unique string,
	authLvl auth.Level, secret []byte, expires time.Time) error {
	// 主键不可变。如果 '_id' 已变更，需要用新记录替换旧记录：
	// 1. 检查 '_id' 是否已变更。
	// 2. 若未变更，按 '_id' 执行更新。
	// 3. 若已变更，先插入新记录（可能因 '_id' 重复而失败），然后删除旧记录。

	var err error
	var record common.AuthRecord
	findOpts := mdbopts.FindOne().SetProjection(b.M{"_id": 1})
	filter := b.M{"userid": uid.String(), "scheme": scheme}
	if err = a.db.Collection("auth").FindOne(a.ctx, filter, findOpts).Decode(&record); err != nil {
		if err == mdb.ErrNoDocuments {
			err = t.ErrNotFound
		}
		return err
	}

	if record.Unique == unique {
		upd := b.M{
			"authlvl": authLvl,
		}
		if len(secret) > 0 {
			upd["secret"] = secret
		}
		if !expires.IsZero() {
			upd["expires"] = expires
		}
		_, err = a.db.Collection("auth").UpdateOne(a.ctx,
			b.M{"_id": unique},
			b.M{"$set": upd})
	} else {
		// 唯一值已变更。采用原子增加+尝试删除模式（若删除失败则原子撤销补偿，兼顾单机与副本集集群）
		if len(secret) == 0 {
			secret = record.Secret
		}
		if expires.IsZero() {
			expires = record.Expires
		}
		err = a.AuthAddRecord(uid, scheme, unique, authLvl, secret, expires)
		if err == nil {
			if _, delErr := a.db.Collection("auth").DeleteOne(a.ctx, b.M{"_id": record.Unique}); delErr != nil {
				// 删除旧记录失败：发起补偿性回滚，删除刚创建的新记录
				a.db.Collection("auth").DeleteOne(a.ctx, b.M{"_id": unique})
				err = delErr
			}
		}
	}

	return err
}

// Topic 管理

// undeleteSubscription 完成undelete订阅所需的内部处理。
func (a *adapter) undeleteSubscription(sub *t.Subscription) error {
	_, err := a.db.Collection("subscriptions").UpdateOne(a.ctx,
		b.M{"_id": sub.Id},
		b.M{
			"$unset": b.M{"deletedat": ""},
			"$set": b.M{
				"updatedat": sub.UpdatedAt,
				"createdat": sub.CreatedAt,
				"modegiven": sub.ModeGiven,
				"modewant":  sub.ModeWant,
				"delid":     0,
				"readseqid": 0,
				"recvseqid": 0}})
	return err
}
