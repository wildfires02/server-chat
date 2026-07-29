//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// CredUpsert 添加或更新验证记录。插入返回 true，更新返回 false。
// 1. 如果凭据已验证：
// 1.1 硬删除未确认的等效记录（如果存在）。
// 1.2 插入新记录。如果重复则报告错误。
// 2. 如果凭据未验证：
// 2.1 检查已验证的等效记录是否存在。如果存在，报告错误。
// 2.2 软删除同一方法的所有未验证记录。
// 2.3 恢复现有凭据。成功则返回。
// 2.4 插入新凭据记录。
func (a *adapter) CredUpsert(cred *t.Credential) (bool, error) {
	var err error
	tableCredentials := rdb.DB(a.dbName).Table("credentials")

	cred.Id = cred.Method + ":" + cred.Value

	if !cred.Done {
		// 检查相同凭据是否已验证。
		cursor, err := tableCredentials.Get(cred.Id).Run(a.conn)
		if err != nil {
			return false, err
		}
		defer cursor.Close()
		if !cursor.IsNil() {
			// 有人已经验证了此凭据。
			return false, t.ErrDuplicate
		}

		// 停用该用户和该方法的所有未验证记录。
		_, err = tableCredentials.GetAllByIndex("User", cred.User).
			Filter(map[string]any{"Method": cred.Method, "Done": false}).Update(
			map[string]any{"DeletedAt": t.TimeNow()}).RunWrite(a.conn)
		if err != nil {
			return false, err
		}

		// 如果凭据未确认，不应阻止其他人尝试验证：
		// 使索引为用户唯一而非全局唯一。
		cred.Id = cred.User + ":" + cred.Id

		// 检查此凭据是否已被用户添加。
		cursor2, err := tableCredentials.Get(cred.Id).Run(a.conn)
		if err != nil {
			return false, err
		}
		defer cursor2.Close()
		if !cursor2.IsNil() {
			_, err = tableCredentials.Get(cred.Id).
				Replace(rdb.Row.Without("DeletedAt").
					Merge(map[string]any{
						"UpdatedAt": cred.UpdatedAt,
						"Resp":      cred.Resp})).RunWrite(a.conn)
			if err != nil {
				return false, err
			}
			// 记录已更新，一切正常。
			return false, nil
		}

	} else {
		// 硬删除可能存在的未验证凭据。
		_, err = tableCredentials.Get(cred.User + ":" + cred.Id).Delete().RunWrite(a.conn)
		if err != nil {
			return false, err
		}
	}

	// 插入新记录。
	_, err = tableCredentials.Insert(cred).RunWrite(a.conn)
	if rdb.IsConflictErr(err) {
		return true, t.ErrDuplicate
	}

	return true, err
}

// CredDel 删除给定方法的凭据。如果方法为空，则删除用户的所有凭据。
func (a *adapter) CredDel(uid t.Uid, method, value string) error {
	q := rdb.DB(a.dbName).Table("credentials").
		GetAllByIndex("User", uid.String())
	if method != "" {
		q = q.Filter(map[string]any{"Method": method})
		if value != "" {
			q = q.Filter(map[string]any{"Value": value})
		}
	}

	if method == "" {
		res, err := q.Delete().RunWrite(a.conn)
		if err == nil {
			if res.Deleted == 0 {
				err = t.ErrNotFound
			}
		}
		return err
	}

	// 硬删除所有已确认的值或没有确认尝试的值。
	res, err := q.Filter(rdb.Or(rdb.Row.Field("Done").Eq(true), rdb.Row.Field("Retries").Eq(0))).Delete().RunWrite(a.conn)
	if err != nil {
		return err
	}
	if res.Deleted > 0 {
		return nil
	}

	// 软删除所有其他值。
	res, err = q.Update(map[string]any{"DeletedAt": t.TimeNow()}).RunWrite(a.conn)
	if err == nil {
		if res.Deleted == 0 {
			err = t.ErrNotFound
		}
	}
	return err
}

// credGetActive 读取当前活跃的未验证凭据
func (a *adapter) credGetActive(uid t.Uid, method string) (*t.Credential, error) {
	// 获取活跃的未确认凭据：
	cursor, err := rdb.DB(a.dbName).Table("credentials").GetAllByIndex("User", uid.String()).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Filter(map[string]any{"Method": method, "Done": false}).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var cred t.Credential
	if err = cursor.One(&cred); err != nil {
		return nil, err
	}

	return &cred, nil
}

// CredConfirm 将给定凭据标记为已验证。
func (a *adapter) CredConfirm(uid t.Uid, method string) error {

	cred, err := a.credGetActive(uid, method)
	if err != nil {
		return err
	}

	// RethinkDb 不允许更改主键（userid:method:value -> method:value）
	// 必须删除并用不同的主键重新插入。

	cred.Done = true
	cred.UpdatedAt = t.TimeNow()
	if _, err = a.CredUpsert(cred); err != nil {
		return err
	}

	rdb.DB(a.dbName).
		Table("credentials").
		Get(uid.String() + ":" + cred.Method + ":" + cred.Value).
		Delete(rdb.DeleteOpts{Durability: "soft", ReturnChanges: false}).
		RunWrite(a.conn)

	return nil
}

// CredFail 增加给定凭据的验证失败尝试计数。
func (a *adapter) CredFail(uid t.Uid, method string) error {
	_, err := rdb.DB(a.dbName).Table("credentials").
		GetAllByIndex("User", uid.String()).
		Filter(map[string]any{"Method": method, "Done": false}).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Update(map[string]any{
			"Retries":   rdb.Row.Field("Retries").Default(0).Add(1),
			"UpdatedAt": t.TimeNow(),
		}).RunWrite(a.conn)
	return err
}

// CredGetActive 返回给定方法的当前活跃凭据记录。
func (a *adapter) CredGetActive(uid t.Uid, method string) (*t.Credential, error) {
	return a.credGetActive(uid, method)
}

// CredGetAll 返回用户的给定方法的凭据记录，仅已验证或全部。
func (a *adapter) CredGetAll(uid t.Uid, method string, validatedOnly bool) ([]t.Credential, error) {
	q := rdb.DB(a.dbName).Table("credentials").GetAllByIndex("User", uid.String())
	if method != "" {
		q = q.Filter(map[string]any{"Method": method})
	}
	if validatedOnly {
		q = q.Filter(map[string]any{"Done": true})
	} else {
		q = q.Filter(rdb.Row.HasFields("DeletedAt").Not())
	}

	cursor, err := q.Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var credentials []t.Credential
	err = cursor.All(&credentials)
	return credentials, err
}

// 文件上传
