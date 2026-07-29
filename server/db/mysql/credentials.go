//go:build mysql
// +build mysql

package mysql

import (
	"database/sql"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	"github.com/jmoiron/sqlx"
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
	var err error

	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	now := t.TimeNow()
	userId := common.DecodeUidString(cred.User)

	// 强制唯一性：如果凭据已确认，“method:value”必须唯一。
	// 如果凭据尚未确认，“userid:method:value”是唯一的。
	synth := cred.Method + ":" + cred.Value

	if !cred.Done {
		// 检查此凭据是否已被验证。
		var done bool
		err = tx.Get(&done, "SELECT done FROM credentials WHERE synthetic=?", synth)
		if err == nil {
			// 赋值 err 以确保事务关闭。
			err = t.ErrDuplicate
			return false, err
		}
		if err != sql.ErrNoRows {
			return false, err
		}
		// 我们将插入新记录。
		synth = cred.User + ":" + synth

		// Adding new unvalidated credential. Deactivate all unvalidated records of this 用户 and method.
		_, err = tx.Exec("UPDATE credentials SET deletedat=? WHERE userid=? AND method=? AND done=FALSE",
			now, userId, cred.Method)
		if err != nil {
			return false, err
		}
		// Assume that the record exists and try to update it: undelete, update timestamp and 响应 value.
		res, err := tx.Exec("UPDATE credentials SET updatedat=?,deletedat=NULL,resp=?,done=FALSE WHERE synthetic=?",
			cred.UpdatedAt, cred.Resp, synth)
		if err != nil {
			return false, err
		}
		// 如果记录已更新，则一切正常。
		if numrows, _ := res.RowsAffected(); numrows > 0 {
			return false, tx.Commit()
		}
	} else {
		// 硬删除未确认的记录（如果存在）。
		_, err = tx.Exec("DELETE FROM credentials WHERE synthetic=?", cred.User+":"+synth)
		if err != nil {
			return false, err
		}
	}
	// 添加新记录。
	_, err = tx.Exec("INSERT INTO credentials(createdat,updatedat,method,value,synthetic,userid,resp,done) "+
		"VALUES(?,?,?,?,?,?,?,?)",
		cred.CreatedAt, cred.UpdatedAt, cred.Method, cred.Value, synth, userId, cred.Resp, cred.Done)
	if err != nil {
		if isDupe(err) {
			return true, t.ErrDuplicate
		}
		return true, err
	}
	return true, tx.Commit()
}

// credDel 删除给定用户的指定验证方法或所有方法。
// 1. 如果用户正在被删除，硬删除所有记录（method == ""）
// 2. 如果删除单个值：
// 2.1 如果已验证或没有验证尝试则删除它
// （否则可能被用来规避验证尝试次数限制）。
// 2.2 否则标记为软删除。
func credDel(tx *sqlx.Tx, uid t.Uid, method, value string) error {
	constraints := " WHERE userid=?"
	args := []any{store.DecodeUid(uid)}

	if method != "" {
		constraints += " AND method=?"
		args = append(args, method)

		if value != "" {
			constraints += " AND value=?"
			args = append(args, value)
		}
	}

	var err error
	var res sql.Result
	if method == "" {
		// 情况 1
		res, err = tx.Exec("DELETE FROM credentials"+constraints, args...)
		if err == nil {
			if count, _ := res.RowsAffected(); count == 0 {
				err = t.ErrNotFound
			}
		}
		return err
	}

	// 情况 2.1
	res, err = tx.Exec("DELETE FROM credentials"+constraints+" AND (done=TRUE OR retries=0)", args...)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count > 0 {
		return nil
	}

	// 情况 2.2
	args = append([]any{t.TimeNow()}, args...)
	res, err = tx.Exec("UPDATE credentials SET deletedat=?"+constraints, args...)
	if err == nil {
		if count, _ := res.RowsAffected(); count >= 0 {
			err = t.ErrNotFound
		}
	}

	return err
}

// CredDel 删除给定用户的凭据。如果方法为空，所有
// 凭据被移除。如果值为空，给定方法的所有凭据被移除。
func (a *adapter) CredDel(uid t.Uid, method, value string) error {
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

	err = credDel(tx, uid, method, value)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// CredConfirm 将指定的凭据方法标记为已确认。
func (a *adapter) CredConfirm(uid t.Uid, method string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	res, err := a.db.ExecContext(
		ctx,
		"UPDATE credentials SET updatedat=?,done=TRUE,synthetic=CONCAT(method,':',value) "+
			"WHERE userid=? AND method=? AND deletedat IS NULL AND done=FALSE",
		t.TimeNow(), store.DecodeUid(uid), method)
	if err != nil {
		if isDupe(err) {
			return t.ErrDuplicate
		}
		return err
	}
	if numrows, _ := res.RowsAffected(); numrows < 1 {
		return t.ErrNotFound
	}
	return nil
}

// CredFail 增加指定验证方法的失败计数。
func (a *adapter) CredFail(uid t.Uid, method string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx, "UPDATE credentials SET updatedat=?,retries=retries+1 WHERE userid=? AND method=? AND done=FALSE",
		t.TimeNow(), store.DecodeUid(uid), method)
	return err
}

// CredGetActive 返回指定用户和方法当前活跃的未验证凭据。
func (a *adapter) CredGetActive(uid t.Uid, method string) (*t.Credential, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var cred t.Credential
	err := a.db.GetContext(ctx, &cred, "SELECT createdat,updatedat,method,value,resp,done,retries "+
		"FROM credentials WHERE userid=? AND deletedat IS NULL AND method=? AND done=FALSE",
		store.DecodeUid(uid), method)
	if err != nil {
		if err == sql.ErrNoRows {
			err = nil
		}
		return nil, err
	}
	cred.User = uid.String()

	return &cred, nil
}

// CredGetAll 返回指定用户和方法的凭据记录，可选仅已验证或全部。
func (a *adapter) CredGetAll(uid t.Uid, method string, validatedOnly bool) ([]t.Credential, error) {
	query := "SELECT createdat,updatedat,method,value,resp,done,retries FROM credentials WHERE userid=? AND deletedat IS NULL"
	args := []any{store.DecodeUid(uid)}
	if method != "" {
		query += " AND method=?"
		args = append(args, method)
	}
	if validatedOnly {
		query += " AND done=TRUE"
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var credentials []t.Credential
	err := a.db.SelectContext(ctx, &credentials, query, args...)
	if err != nil {
		return nil, err
	}

	user := uid.String()
	for i := range credentials {
		credentials[i].User = user
	}

	return credentials, err
}

// 文件上传
