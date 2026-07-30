//go:build mysql || (!postgres && !mongodb && !rethinkdb)
// +build mysql !postgres,!mongodb,!rethinkdb

package mysql

import (
	"database/sql"
	"strings"
	"time"

	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	"github.com/jmoiron/sqlx"
)

// TopicCreate 将 Topic 对象保存到数据库。
func (a *adapter) TopicCreate(topic *t.Topic) error {
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

	err = a.topicCreate(tx, topic)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// If undelete = true - update 订阅 on duplicate key, otherwise ignore the duplicate.
func createSubscription(tx *sqlx.Tx, sub *t.Subscription, undelete bool) error {

	isOwner := (sub.ModeGiven & sub.ModeWant).IsOwner()

	jpriv := common.ToJSON(sub.Private)
	decoded_uid := store.DecodeUid(t.ParseUid(sub.User))
	_, err := tx.Exec(
		"INSERT INTO subscriptions(createdat,updatedat,deletedat,userid,topic,modeWant,modeGiven,private) "+
			"VALUES(?,?,NULL,?,?,?,?,?)",
		sub.CreatedAt, sub.UpdatedAt, decoded_uid, sub.Topic, sub.ModeWant.String(), sub.ModeGiven.String(), jpriv)

	if err != nil && isDupe(err) {
		if undelete {
			_, err = tx.Exec("UPDATE subscriptions SET createdat=?,updatedat=?,deletedat=NULL,modeWant=?,modeGiven=?,"+
				"delid=0,recvseqid=0,readseqid=0 WHERE topic=? AND userid=?",
				sub.CreatedAt, sub.UpdatedAt, sub.ModeWant.String(), sub.ModeGiven.String(), sub.Topic, decoded_uid)
		} else {
			_, err = tx.Exec("UPDATE subscriptions SET createdat=?,updatedat=?,deletedat=NULL,modeWant=?,modeGiven=?,"+
				"delid=0,recvseqid=0,readseqid=0,private=? WHERE topic=? AND userid=?",
				sub.CreatedAt, sub.UpdatedAt, sub.ModeWant.String(), sub.ModeGiven.String(), jpriv,
				sub.Topic, decoded_uid)
		}
	}

	if err == nil && isOwner {
		// Update Topic owner if the 订阅 is with owner rights.
		// 不要在此处递增订阅者计数 - 在 TopicShare 中批量完成。
		_, err = tx.Exec("UPDATE topics SET owner=? WHERE name=?", decoded_uid, sub.Topic)
	}
	return err
}

// TopicCreateP2P given two 用户 creates a p2p Topic.
func (a *adapter) TopicCreateP2P(initiator, invited *t.Subscription) error {
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

	err = createSubscription(tx, initiator, false)
	if err != nil {
		return err
	}

	// If the second 订阅 exists, don't overwrite it. Just make sure it's not deleted.
	err = createSubscription(tx, invited, true)
	if err != nil {
		return err
	}

	topic := &t.Topic{ObjHeader: t.ObjHeader{Id: initiator.Topic}}
	topic.ObjHeader.MergeTimes(&initiator.ObjHeader)
	topic.TouchedAt = initiator.GetTouchedAt()
	err = a.topicCreate(tx, topic)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// TopicGet 按名称加载单个 Topic（如果存在）。如果 Topic 不存在返回 (nil, nil)
func (a *adapter) TopicGet(topic string) (*t.Topic, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// 按名称获取 Topic
	var tt = new(t.Topic)
	if err := a.db.GetContext(ctx, tt,
		"SELECT createdat,updatedat,state,stateat,touchedat,name AS id,usebt,access,owner,seqid,delid,subcnt,public,trusted,tags,aux "+
			"FROM topics WHERE name=?", topic); err != nil {
		if err == sql.ErrNoRows {
			// 未找到 - 清除错误
			err = nil
		}
		return nil, err
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// Topic 已找到，获取订阅数（忽略 Topic.subcnt 中设置的值）。同时尝试 Topic 和 Channel 名称。
		var subCnt int
		if err := a.db.GetContext(ctx, &subCnt,
			"SELECT COUNT(*) FROM subscriptions WHERE topic IN (?,?) AND deletedat IS NULL", topic, t.GrpToChn(topic)); err != nil {
			return nil, err
		}

		if subCnt != tt.SubCnt {
			// Update the Topic with the correct 订阅 count.
			tt.SubCnt = subCnt
			if _, err := a.db.ExecContext(ctx, "UPDATE topics SET subcnt=? WHERE name=?", subCnt, topic); err != nil {
				return nil, err
			}
		}
	}

	tt.Owner = common.EncodeUidString(tt.Owner).String()
	tt.Public = common.FromJSON(tt.Public)
	tt.Trusted = common.FromJSON(tt.Trusted)

	return tt, nil
}

// TopicsForUser loads 用户's contact list: p2p and grp Topic, except for 'me' & 'fnd' 订阅.
// 读取并反归一化 Public 和 Trusted 值。
func (a *adapter) TopicsForUser(uid t.Uid, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	// Fetch ALL 用户's 订阅, even those which has not been modified recently.
	// We are going to use these 订阅 to fetch Topic and 用户 which may have been modified recently.
	q := `SELECT createdat,updatedat,deletedat,topic,delid,recvseqid,
		readseqid,modewant,modegiven,private FROM subscriptions WHERE userid=?`
	args := []any{store.DecodeUid(uid)}
	if !keepDeleted {
		// 过滤已删除的行。
		q += " AND deletedat IS NULL"
	}

	limit := 0
	ims := time.Time{}
	if opts != nil {
		if opts.Topic != "" {
			q += " AND topic=?"
			args = append(args, opts.Topic)
		}

		// 仅在客户端不管理缓存（或冷启动）时应用限制。
		// Otherwise have to get all 订阅 and do a manual join with 用户/Topic.
		if opts.IfModifiedSince == nil {
			if opts.Limit > 0 && opts.Limit < a.maxResults {
				limit = opts.Limit
			} else {
				limit = a.maxResults
			}
		} else {
			ims = *opts.IfModifiedSince
		}
	} else {
		limit = a.maxResults
	}

	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	// 必须手动关闭 rows，因为我们将重用它们。

	// Fetch 订阅. Two queries are needed: 用户 table (p2p) and Topic table (grp).
	// Prepare a list of separate 订阅 to 用户 vs Topic
	join := make(map[string]t.Subscription) // Keeping these to make a join with table for .private and .access
	topq := make([]any, 0, 16)
	usrq := make([]any, 0, 16)
	for rows.Next() {
		var sub t.Subscription
		if err = rows.StructScan(&sub); err != nil {
			break
		}
		tname := sub.Topic
		sub.User = uid.String()
		tcat := t.GetTopicCat(tname)

		if tcat == t.TopicCatMe || tcat == t.TopicCatFnd {
			// One of 'me', 'fnd' 订阅, skip.
			// Don't skip 'sys' 订阅.
			continue
		} else if tcat == t.TopicCatP2P {
			// P2P 订阅, find the other 用户 to get 用户.Public and 用户.Trusted.
			uid1, uid2, _ := t.ParseP2P(tname)
			if uid1 == uid {
				usrq = append(usrq, store.DecodeUid(uid2))
				sub.SetWith(uid2.UserId())
			} else {
				usrq = append(usrq, store.DecodeUid(uid1))
				sub.SetWith(uid1.UserId())
			}
		} else if tcat == t.TopicCatGrp {
			// 可能将 Channel 名称转换为群组 Topic 名称。
			tname = t.ChnToGrp(tname)
		}
		// No special handling needed for 'slf', 'sys' 订阅.

		topq = append(topq, tname)
		sub.Private = common.FromJSON(sub.Private)
		join[tname] = sub
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()

	if err != nil {
		return nil, err
	}

	var subs []t.Subscription
	if len(join) == 0 {
		return subs, nil
	}

	// Fetch grp Topic and join to 订阅.
	if len(topq) > 0 {
		q = "SELECT updatedat,state,touchedat,name AS id,usebt,access,seqid,delid,subcnt,public,trusted " +
			"FROM topics WHERE name IN (?)"
		q, args, _ = sqlx.In(q, topq)

		if !keepDeleted {
			// 可选跳过已删除的 Topic。
			q += " AND state!=?"
			args = append(args, t.StateDeleted)
		}

		if !ims.IsZero() {
			// 如果提供了缓存时间戳：仅获取较新的条目。
			q += " AND touchedat>?"
			args = append(args, ims)

			if limit > 0 && limit < len(topq) {
				// 没有意义获取超过请求的限制。
				q += " ORDER BY touchedat LIMIT ?"
				args = append(args, limit)
			}
		}

		ctx2, cancel2 := a.getContext()
		if cancel2 != nil {
			defer cancel2()
		}
		rows, err = a.db.QueryxContext(ctx2, a.db.Rebind(q), args...)
		if err != nil {
			return nil, err
		}

		var top t.Topic
		for rows.Next() {
			if err = rows.StructScan(&top); err != nil {
				break
			}
			sub := join[top.Id]
			// 检查是否 sub.UpdatedAt needs to be adjusted to earlier or later time.
			sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, top.UpdatedAt)
			sub.SetState(top.State)
			sub.SetTouchedAt(top.TouchedAt)
			sub.SetSeqId(top.SeqId)
			if t.GetTopicCat(sub.Topic) == t.TopicCatGrp {
				sub.SetSubCnt(top.SubCnt)
				sub.SetPublic(common.FromJSON(top.Public))
				sub.SetTrusted(common.FromJSON(top.Trusted))
			}
			// 放回订阅的更新值，将在下面进一步处理
			join[top.Id] = sub
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()

		if err != nil {
			return nil, err
		}
	}

	// Fetch p2p 用户 and join to p2p 订阅.
	if len(usrq) > 0 {
		q = "SELECT id,updatedat,state,access,lastseen,useragent,public,trusted " +
			"FROM users WHERE id IN (?)"
		q, args, _ = sqlx.In(q, usrq)
		if !keepDeleted {
			// Optionally skip deleted 用户.
			q += " AND state!=?"
			args = append(args, t.StateDeleted)
		}

		// Ignoring ims: we need all 用户 to get LastSeen and UserAgent.

		ctx3, cancel3 := a.getContext()
		if cancel3 != nil {
			defer cancel3()
		}
		rows, err = a.db.QueryxContext(ctx3, a.db.Rebind(q), args...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var usr2 t.User
			if err = rows.StructScan(&usr2); err != nil {
				break
			}

			joinOn := uid.P2PName(common.EncodeUidString(usr2.Id))
			if sub, ok := join[joinOn]; ok {
				sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, usr2.UpdatedAt)
				sub.SetState(usr2.State)
				sub.SetPublic(common.FromJSON(usr2.Public))
				sub.SetTrusted(common.FromJSON(usr2.Trusted))
				sub.SetDefaultAccess(usr2.Access.Auth, usr2.Access.Anon)
				sub.SetLastSeenAndUA(usr2.LastSeen, usr2.UserAgent)
				join[joinOn] = sub
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()

		if err != nil {
			return nil, err
		}
	}

	subs = make([]t.Subscription, 0, len(join))
	for _, sub := range join {
		subs = append(subs, sub)
	}

	return common.SelectEarliestUpdatedSubs(subs, opts, a.maxResults), nil
}

// UsersForTopic loads 用户 subscribed to the given Topic (not Channel readers).
// The difference between UsersForTopic vs SubsForTopic is that the former loads 用户.Public,
// 后者不加载。
func (a *adapter) UsersForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	tcat := t.GetTopicCat(topic)

	// Fetch all subscribed 用户. The number of 用户 is not large.
	q := `SELECT s.createdat,s.updatedat,s.deletedat,s.userid,s.topic,s.delid,s.recvseqid,
		s.readseqid,s.modewant,s.modegiven,u.public,u.trusted,u.lastseen,u.useragent,s.private
		FROM subscriptions AS s JOIN users AS u ON s.userid=u.id
		WHERE s.topic=?`
	args := []any{topic}
	if !keepDeleted {
		// Filter out rows with 用户 deleted
		q += " AND u.state!=?"
		args = append(args, t.StateDeleted)

		// For p2p Topic we must load all 订阅 including deleted.
		// 否则将无法交换 Public 值。
		if tcat != t.TopicCatP2P {
			// Filter out deleted 订阅.
			q += " AND s.deletedat IS NULL"
		}
	}

	limit := a.maxResults
	var oneUser t.Uid
	if opts != nil {
		// 忽略 IfModifiedSince：加载所有条目，因为 Topic 不会有太多订阅者。
		// 未修改的将去除 Public 和 Private。

		if !opts.User.IsZero() {
			// For p2p Topic we have to fetch both 用户 otherwise public cannot be swapped.
			if tcat != t.TopicCatP2P {
				q += " AND s.userid=?"
				args = append(args, store.DecodeUid(opts.User))
			}
			oneUser = opts.User
		}
		if !opts.Cursor.IsZero() && tcat != t.TopicCatP2P {
			q += " AND s.userid>?"
			args = append(args, store.DecodeUid(opts.Cursor))
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	q += " ORDER BY s.userid ASC LIMIT ?"
	args = append(args, limit)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Fetch 订阅.
	var sub t.Subscription
	var subs []t.Subscription
	var lastSeen sql.NullTime
	var userAgent string
	var public, trusted any
	for rows.Next() {
		if err = rows.Scan(
			&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt,
			&sub.User, &sub.Topic, &sub.DelId, &sub.RecvSeqId,
			&sub.ReadSeqId, &sub.ModeWant, &sub.ModeGiven,
			&public, &trusted, &lastSeen, &userAgent, &sub.Private); err != nil {
			break
		}

		sub.User = common.EncodeUidString(sub.User).String()
		sub.Private = common.FromJSON(sub.Private)
		sub.SetPublic(common.FromJSON(public))
		sub.SetTrusted(common.FromJSON(trusted))
		sub.SetLastSeenAndUA(&lastSeen.Time, userAgent)
		subs = append(subs, sub)
	}
	if err == nil {
		err = rows.Err()
	}

	if err == nil && tcat == t.TopicCatP2P && len(subs) > 0 {
		// 按预期交换 P2P Topic 的 public 和 lastSeen 值。
		if len(subs) == 1 {
			// The other 用户 is deleted, nothing we can do.
			subs[0].SetPublic(nil)
			subs[0].SetTrusted(nil)
			subs[0].SetLastSeenAndUA(nil, "")
		} else {
			tmp := subs[0].GetPublic()
			subs[0].SetPublic(subs[1].GetPublic())
			subs[1].SetPublic(tmp)

			tmp = subs[0].GetTrusted()
			subs[0].SetTrusted(subs[1].GetTrusted())
			subs[1].SetTrusted(tmp)

			lastSeen := subs[0].GetLastSeen()
			userAgent = subs[0].GetUserAgent()
			subs[0].SetLastSeenAndUA(subs[1].GetLastSeen(), subs[1].GetUserAgent())
			subs[1].SetLastSeenAndUA(lastSeen, userAgent)
		}

		// Remove deleted and unneeded 订阅
		if !keepDeleted || !oneUser.IsZero() {
			var xsubs []t.Subscription
			for i := range subs {
				if (subs[i].DeletedAt != nil && !keepDeleted) || (!oneUser.IsZero() && subs[i].Uid() != oneUser) {
					continue
				}
				xsubs = append(xsubs, subs[i])
			}
			subs = xsubs
		}
	}

	return subs, err
}

// topicNamesForUser 使用提供的查询读取字符串切片。
// 如果 includeChan 为 true，查询将同时添加 Channel 名称和群组 Topic 名称。
func (a *adapter) topicNamesForUser(sqlQuery string, includeChan bool, args ...any) ([]string, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		names = append(names, name)
		// 如果名称是群组 Topic，同时添加 Channel 名称（如果请求）。
		if includeChan {
			if channel := t.GrpToChn(name); channel != "" {
				names = append(names, channel)
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}

	return names, err
}

// OwnTopics loads a slice of Topic names where the 用户 is the owner.
func (a *adapter) OwnTopics(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("SELECT name FROM topics WHERE owner=? AND state!=?",
		false, store.DecodeUid(uid), t.StateDeleted)
}

// ChannelsForUser loads a slice of Topic names where the 用户 is a Channel reader and notifications (P) are enabled.
func (a *adapter) ChannelsForUser(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("SELECT topic FROM subscriptions WHERE userid=? AND topic LIKE 'chn%' "+
		"AND INSTR(modewant,'P')>0 AND INSTR(modegiven,'P')>0 AND deletedat IS NULL",
		false, store.DecodeUid(uid))
}

// TopicShare adds 订阅 to a Topic and increments the Topic's subcnt.
func (a *adapter) TopicShare(topic string, shares []*t.Subscription) error {
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

	for _, sub := range shares {
		err = createSubscription(tx, sub, true)
		if err != nil {
			return err
		}
	}

	if topic != "" {
		// Update Topic's 订阅 count.
		if _, err = tx.Exec("UPDATE topics SET subcnt=subcnt+? WHERE name=?", len(shares), topic); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// TopicDelete deletes Topic, 订阅, 消息.
func (a *adapter) TopicDelete(topic string, isChan, hard bool) error {
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

	// If the Topic is a Channel, must try to delete 订阅 under both grpXXX and chnXXX names.
	args := []any{topic}
	if isChan {
		args = append(args, t.GrpToChn(topic))
	}

	if hard {
		// Delete 订阅. If this is a Channel, delete both group 订阅 and Channel 订阅.
		q, args, _ := sqlx.In("DELETE FROM subscriptions WHERE topic IN (?)", args)
		if _, err = tx.Exec(tx.Rebind(q), args...); err != nil {
			return err
		}

		if err = messageDeleteList(tx, topic, nil); err != nil {
			return err
		}

		if _, err = tx.Exec("DELETE FROM topictags WHERE topic=?", topic); err != nil {
			return err
		}

		if _, err = tx.Exec("DELETE FROM topics WHERE name=?", topic); err != nil {
			return err
		}
	} else {
		now := t.TimeNow()

		q, args, _ := sqlx.In("UPDATE subscriptions SET updatedat=?,deletedat=? WHERE topic IN (?)", now, now, args)
		if _, err = tx.Exec(tx.Rebind(q), args...); err != nil {
			return err
		}

		if _, err = tx.Exec("UPDATE topics SET updatedat=?,touchedat=?,state=?,stateat=? WHERE name=?",
			now, now, t.StateDeleted, now, topic); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TopicUpdateOnMessage updates Topic's seqid and touchedat when a new 消息 is posted.
func (a *adapter) TopicUpdateOnMessage(topic string, msg *t.Message) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx, "UPDATE topics SET seqid=?,touchedat=? WHERE name=?", msg.SeqId, msg.CreatedAt, topic)

	return err
}

// TopicUpdateSubCnt 更新 Topic 中反归一化的订阅者计数。
func (a *adapter) TopicUpdateSubCnt(topic string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx,
		"UPDATE topics SET subcnt=(SELECT COUNT(*) FROM subscriptions WHERE topic IN (?,?) AND deletedat IS NULL) WHERE name=?",
		topic, t.GrpToChn(topic), topic)
	return err
}

// TopicUpdate 更新 Topic 中给定更新映射的字段。
// 如果更新包含 UpdatedAt 但不包含 TouchedAt，则 TouchedAt 设置为 UpdatedAt
func (a *adapter) TopicUpdate(topic string, update map[string]any) error {
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

	if t, u := update["TouchedAt"], update["UpdatedAt"]; t == nil && u != nil {
		update["TouchedAt"] = u
	}
	cols, args := common.UpdateByMap(update)
	args = append(args, topic)
	_, err = tx.Exec("UPDATE topics SET "+strings.Join(cols, ",")+" WHERE name=?", args...)
	if err != nil {
		return err
	}

	// 标签也存储在单独的表中
	if tags := common.ExtractTags(update); tags != nil {
		// 首先删除所有用户标签
		_, err = tx.Exec("DELETE FROM topictags WHERE topic=?", topic)
		if err != nil {
			return err
		}
		// 现在插入新标签
		err = addTags(tx, "topictags", "topic", topic, tags, false)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// TopicOwnerChange 将输入编码为picOwnerChange。
func (a *adapter) TopicOwnerChange(topic string, newOwner t.Uid) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx, "UPDATE topics SET owner=? WHERE name=?", store.DecodeUid(newOwner), topic)
	return err
}
