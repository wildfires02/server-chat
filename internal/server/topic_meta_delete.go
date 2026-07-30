package server

import (
	"errors"
	"sort"
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// replyGetDel 是对 get[what=del] 请求的响应：加载已删除消息 ID 列表，作为 {meta} 发送给
// Session
// 响应仅发送给单个 Session 而不是 Topic 中的所有 Session
func (t *Topic) replyGetDel(sess *Session, asUid types.Uid, req *MsgGetOpts, msg *ClientComMessage) error {
	now := types.TimeNow()
	toriginal := t.original(asUid)

	id := msg.Id
	incomingReqTs := msg.Timestamp

	if req != nil && (req.IfModifiedSince != nil || req.User != "" || req.Topic != "") {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("invalid MsgGetOpts query")
	}

	// 检查是否用户有权限读取 Topic 数据且请求有效。
	if userData := t.perUser[asUid]; (userData.modeGiven & userData.modeWant).IsReader() {
		high := max(t.delID, userData.delID)
		queryReq := req
		if req != nil && req.Forward {
			// 删除日志使用与消息历史相同的固定快照游标语义。
			if req.BeforeId > 0 && req.BeforeId-1 < high {
				high = req.BeforeId - 1
			}
			snapshotReq := *req
			if snapshotReq.BeforeId == 0 || snapshotReq.BeforeId > high+1 {
				snapshotReq.BeforeId = high + 1
			}
			queryReq = &snapshotReq
		}
		ranges, delID, err := store.Messages.GetDeleted(t.name, asUid, msgOpts2storeOpts(queryReq))
		if err != nil {
			sess.queueOut(ErrUnknownReply(msg, now))
			return err
		}

		if len(ranges) > 0 {
			sess.queueOut(&ServerComMessage{
				Meta: &MsgServerMeta{
					Id:    id,
					Topic: toriginal,
					Del: &MsgDelValues{
						DelId:  delID,
						DelSeq: rangeDeserialize(ranges),
					},
					Timestamp: &now,
				},
			})
			if req == nil || !req.Forward {
				return nil
			}
		}

		if req != nil && req.Forward {
			cursor := delID
			if len(ranges) == 0 {
				cursor = high
				if req.SinceId-1 > cursor {
					cursor = req.SinceId - 1
				}
			}
			params := map[string]any{
				"what":    "del",
				"count":   len(ranges),
				"low":     req.SinceId,
				"high":    high,
				"cursor":  cursor,
				"hasMore": cursor < high,
			}
			if len(ranges) == 0 {
				sess.queueOut(NoContentParams(id, toriginal, now, incomingReqTs, params))
			} else {
				sess.queueOut(NoErrDeliveredParams(id, toriginal, now, params))
			}
			return nil
		}
	} else {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("attempt to get deletions by non-reader")
	}

	sess.queueOut(NoContentParams(id, toriginal, now, incomingReqTs, map[string]string{"what": "del"}))

	return nil
}

// replyDelMsg 删除（软删除或硬删除）消息以响应 del.msg 数据包。
func (t *Topic) replyDelMsg(sess *Session, asUid types.Uid, asChan bool, msg *ClientComMessage) error {
	now := types.TimeNow()

	pud := t.perUser[asUid]
	if asChan || pud.isChan {
		// 不允许 Channel 读者删除消息。
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("channel readers cannot delete messages")
	}

	del := msg.Del

	mode := pud.modeGiven & pud.modeWant
	canDeleteAny := mode.IsDeleter()
	if !canDeleteAny {
		// 用户必须有 R 权限：如果用户无法读取消息，则
		// 无权删除它们。
		if !mode.IsReader() {
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
			return errors.New("del.msg: permission denied")
		}
	}

	var err error
	var ranges []types.Range
	if len(del.DelSeq) == 0 {
		err = errors.New("del.msg: no IDs to delete")
	} else {
		count := 0
		for _, dq := range del.DelSeq {
			if dq.LowId > t.lastID || dq.LowId < 0 || dq.HiId < 0 ||
				(dq.HiId > 0 && dq.LowId > dq.HiId) ||
				(dq.LowId == 0 && dq.HiId == 0) {
				err = errors.New("del.msg: invalid entry in list")
				break
			}

			if dq.HiId > t.lastID {
				// 范围是包含-排除 [low, hi)，
				// 要删除所有消息 hi 必须是 lastId + 1
				dq.HiId = t.lastID + 1
			} else if dq.LowId == dq.HiId || dq.LowId+1 == dq.HiId {
				dq.HiId = 0
			}

			if dq.HiId == 0 {
				count++
			} else {
				count += dq.HiId - dq.LowId
			}

			ranges = append(ranges, types.Range{Low: dq.LowId, Hi: dq.HiId})
		}

		if err == nil {
			// 按 Low 升序然后按 Hi 降序排序。
			sort.Sort(types.RangeSorter(ranges))
			// 折叠重叠的范围
			ranges = types.RangeSorter(ranges).Normalize()
		}

		if count > defaultMaxDeleteCount && len(ranges) > 1 {
			err = errors.New("del.msg: too many messages to delete")
		}
	}

	if err != nil {
		sess.queueOut(ErrMalformedReply(msg, now))
		return err
	}

	if del.Hard && !canDeleteAny {
		// 无删除管理权限的普通成员只能硬删除自己发送的消息。
		for _, seqRange := range ranges {
			upper := seqRange.Hi
			if upper == 0 {
				upper = seqRange.Low + 1
			}
			for seq := seqRange.Low; seq < upper; seq++ {
				stored, getErr := store.Messages.Get(t.name, seq)
				if getErr != nil {
					sess.queueOut(ErrUnknownReply(msg, now))
					return getErr
				}
				if stored != nil && types.ParseUid(stored.From) != asUid {
					sess.queueOut(ErrPermissionDeniedReply(msg, now))
					return errors.New("del.msg: cannot hard-delete another sender's message")
				}
			}
		}
	}

	forUser := asUid
	var age time.Duration
	if del.Hard {
		forUser = types.ZeroUid
		age = globals.msgDeleteAge
	}
	if err = store.Messages.DeleteList(t.name, t.delID+1, forUser, age, ranges); err != nil {
		sess.queueOut(ErrUnknownReply(msg, now))
		return err
	}

	// 增加删除事务 ID
	t.delID++
	dr := rangeDeserialize(ranges)
	if del.Hard {
		// 被硬删除的消息不能继续留在 Topic 置顶列表。
		if pins := topicPins(t.aux); len(pins) > 0 {
			kept := pins[:0]
			for _, pin := range pins {
				deleted := false
				for _, seqRange := range ranges {
					upper := seqRange.Hi
					if upper == 0 {
						upper = seqRange.Low + 1
					}
					if pin >= seqRange.Low && pin < upper {
						deleted = true
						break
					}
				}
				if !deleted {
					kept = append(kept, pin)
				}
			}
			if len(kept) != len(pins) {
				aux := copyMap(t.aux)
				aux[topicPinsKey] = kept
				if updateErr := store.Topics.Update(t.name, map[string]any{
					"Aux": aux, "UpdatedAt": now,
				}); updateErr != nil {
					logs.Warn.Printf("topic[%s]: failed to remove deleted pinned messages: %v", t.name, updateErr)
				} else {
					t.aux = aux
					t.presSubsOnline("aux", "", nilPresParams, nilPresFilters, sess.sid)
				}
			}
		}
		for uid, pud := range t.perUser {
			pud.delID = t.delID
			t.perUser[uid] = pud

			// 更新所有可能将这些消息作为未读的用户的未读计数器
			if (pud.modeGiven & pud.modeWant).IsReader() {
				// 计算此用户删除了多少未读消息
				unreadDeleted := calculateUnreadInRanges(pud.readID, t.lastID, ranges)
				if unreadDeleted > 0 {
					// 减少未读计数（负值）
					usersUpdateUnread(uid, -unreadDeleted, true)
				}
			}
		}

		// 将更改广播给所有在线和离线的用户，排除进行更改的 Session。
		params := &presParams{delID: t.delID, delSeq: dr, actor: asUid.UserId()}
		filters := &presFilters{filterIn: types.ModeRead}
		t.presSubsOnline("del", params.actor, params, filters, sess.sid)
		t.presSubsOffline("del", params, filters, nilPresFilters, sess.sid, true)
	} else {
		pud := t.perUser[asUid]
		pud.delID = t.delID
		t.perUser[asUid] = pud

		// 通知用户的其它 Session
		t.presPubMessageDelete(asUid, pud.modeGiven&pud.modeWant, t.delID, dr, sess.sid)
	}

	sess.queueOut(NoErrParamsReply(msg, now, map[string]int{"del": t.delID}))

	return nil
}

// 处理删除 Topic 的请求 {del what="Topic"}。
// 1. 如果请求者是所有者，则应在 hub 层处理，记录错误。
// 2. 如果请求者不是所有者，则像 {leave unsub=true} 一样处理。
func (t *Topic) replyDelTopic(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	if t.owner != asUid {
		return t.replyLeaveUnsub(sess, msg, asUid)
	}

	// 这是一个 bug 指示。
	logs.Err.Println("replyDelTopic called by owner (SHOULD NOT HAPPEN!)")
	return nil
}

// 删除凭证
func (t *Topic) replyDelCred(sess *Session, asUid types.Uid, authLvl auth.Level, msg *ClientComMessage) error {
	now := types.TimeNow()
	incomingReqTs := msg.Timestamp
	del := msg.Del

	if t.cat != types.TopicCatMe {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("del.cred: invalid topic category")
	}
	if del.Cred == nil || del.Cred.Method == "" {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("del.cred: missing method")
	}

	tags, err := deleteCred(asUid, authLvl, del.Cred)
	if tags != nil {
		// 检查是否实际删除了任何内容。
		_, removed, _ := stringSliceDelta(t.tags, tags)
		if len(removed) > 0 {
			t.tags = tags
			t.presSubsOnline("tags", "", nilPresParams, nilPresFilters, "")
		}
	} else if err == nil {
		sess.queueOut(InfoNoActionReply(msg, now))
		return nil
	}
	sess.queueOut(decodeStoreErrorExplicitTs(err, del.Id, del.Topic, now, incomingReqTs, nil))
	return err
}

// 删除订阅。
func (t *Topic) replyDelSub(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()
	del := msg.Del
	if t.isOfficialTopic() {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("官方频道成员只能通过平台管理接口移除")
	}

	asChan, err := t.verifyChannelAccess(msg.Original)
	if err != nil {
		// 用户不应能将非 Channel Topic 作为 Channel 寻址。
		sess.queueOut(ErrNotFoundReply(msg, now))
		return types.ErrNotFound
	}
	if asChan {
		// 不允许 Channel 读者删除自订阅。使用 leave-unsub 或 del-Topic。
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("channel access denied: cannot delete subscription")
	}

	// 获取受影响用户的 ID
	uid := types.ParseUserId(del.User)

	pud := t.perUser[asUid]
	if !(pud.modeGiven & pud.modeWant).IsAdmin() {
		err = errors.New("del.sub: permission denied")
	} else if uid.IsZero() || uid == asUid {
		// 不能删除自订阅。用户 [leave unsub] 或 [delete Topic]
		err = errors.New("del.sub: cannot delete self-subscription")
	} else if t.cat == types.TopicCatP2P {
		// 不要尝试删除另一个 P2P 用户
		err = errors.New("del.sub: cannot apply to a P2P topic")
	}

	if err != nil {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return err
	}

	pud, ok := t.perUser[uid]
	subTopic := t.name
	isChannelSub := ok && pud.isChan
	if isChannelSub {
		subTopic = types.GrpToChn(t.name)
	}
	if !ok {
		// 广播频道的离线读者不会常驻 perUser；依次查询发布者和读者命名空间。
		sub, getErr := store.Subs.Get(t.name, uid, false)
		if getErr == nil && sub == nil && t.isChan {
			subTopic = types.GrpToChn(t.name)
			sub, getErr = store.Subs.Get(subTopic, uid, false)
			isChannelSub = sub != nil
		}
		if getErr != nil {
			sess.queueOut(ErrUnknownReply(msg, now))
			return getErr
		}
		if sub == nil {
			sess.queueOut(InfoNoActionReply(msg, now))
			return nil
		}
		pud = perUserData{
			modeWant:  sub.ModeWant,
			modeGiven: sub.ModeGiven,
			private:   sub.Private,
			delID:     sub.DelId,
			readID:    sub.ReadSeqId,
			recvID:    sub.RecvSeqId,
			isChan:    isChannelSub,
		}
	}

	// 检查是否被驱逐的用户是所有者。
	if (pud.modeGiven & pud.modeWant).IsOwner() {
		err = errors.New("del.sub: cannot evict topic owner")
	} else if !(pud.modeWant & pud.modeGiven).IsJoiner() {
		// 如果用户已禁止 Topic，则不应删除订阅。否则用户可能被重新邀请，
		// 这就违背了禁止的目的。
		err = errors.New("del.sub: cannot delete banned subscription")
	}

	if err != nil {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return err
	}

	// 从数据库中删除用户的订阅
	if err := store.Subs.Delete(subTopic, uid); err != nil {
		if err == types.ErrNotFound {
			sess.queueOut(InfoNoActionReply(msg, now))
			return nil
		} else {
			sess.queueOut(ErrUnknownReply(msg, now))
			return err
		}
	} else {
		sess.queueOut(NoErrReply(msg, now))
	}

	// 更新缓存的未读计数：负值
	if !isChannelSub && (pud.modeWant & pud.modeGiven).IsReader() {
		usersUpdateUnread(uid, pud.readID-t.lastID, true)
	}

	// ModeUnset 表示已删除的订阅，与 ModeNone（无访问权限）相反。
	t.notifySubChange(uid, asUid, isChannelSub,
		pud.modeWant, pud.modeGiven, types.ModeUnset, types.ModeUnset, sess.sid)

	t.evictUser(uid, true, "")
	if isChannelSub {
		t.channelSubUnsub(uid, false)
	}
	if t.cat == types.TopicCatGrp && t.subCnt > 0 {
		t.subCnt--
	}

	// 通知插件。
	pluginSubscription(&types.Subscription{Topic: subTopic, User: uid.String()}, plgActDel)

	// 如果所有 P2P 用户都被删除，则挂起 Topic 以让其关闭。
	if t.cat == types.TopicCatP2P && t.subsCount() == 0 {
		t.markPaused(true)
		globals.hub.unreg <- &topicUnreg{del: true, sess: nil, rcptTo: t.name, pkt: nil}
	}

	return nil
}
