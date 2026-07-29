package server

import (
	"errors"

	"chat/server/store"
	"chat/server/store/types"
)

// replyLeaveUnsub 取消订阅用户并从 Topic 解绑其所有 Session
func (t *Topic) replyLeaveUnsub(sess *Session, msg *ClientComMessage, asUid types.Uid) error {
	now := types.TimeNow()

	if asUid.IsZero() {
		panic("replyLeaveUnsub: zero asUid")
	}

	if t.owner == asUid {
		if msg.init {
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
		}
		return errors.New("replyLeaveUnsub: 群主无法取消订阅")
	}

	var err error
	var asChan bool
	if msg.init {
		asChan, err = t.verifyChannelAccess(msg.Original)
		if err != nil {
			sess.queueOut(ErrNotFoundReply(msg, now))
			return errors.New("replyLeaveUnsub: Channel 寻址错误")
		}
	}

	pud := t.perUser[asUid]
	if pud.isChan {
		err = store.Subs.Delete(types.GrpToChn(t.name), asUid)
	} else {
		err = store.Subs.Delete(t.name, asUid)
	}

	if err != nil {
		if msg.init {
			if err == types.ErrNotFound {
				sess.queueOut(InfoNoActionReply(msg, now))
				err = nil
			} else {
				sess.queueOut(ErrUnknownReply(msg, now))
			}
		}
		return err
	}

	if msg.init {
		sess.queueOut(NoErrReply(msg, now))
	}

	var oldWant types.AccessMode
	var oldGiven types.AccessMode
	if !asChan {
		if (pud.modeWant & pud.modeGiven).IsReader() {
			usersUpdateUnread(asUid, pud.readID-t.lastID, true)
		}
		oldWant, oldGiven = pud.modeWant, pud.modeGiven
	} else {
		oldWant, oldGiven = types.ModeCChnReader, types.ModeCChnReader
		t.channelSubUnsub(asUid, false)
	}

	t.notifySubChange(asUid, asUid, asChan, oldWant, oldGiven, types.ModeUnset, types.ModeUnset, sess.sid)
	t.evictUser(asUid, true, sess.sid)
	pluginSubscription(&types.Subscription{Topic: t.name, User: asUid.String()}, plgActDel)

	if t.cat == types.TopicCatGrp {
		t.subCnt--
	}

	if t.cat == types.TopicCatP2P && t.subsCount() == 0 {
		t.markPaused(true)
		globals.hub.unreg <- &topicUnreg{del: true, sess: nil, rcptTo: t.name, pkt: nil}
	}

	return nil
}

// evictUser 从 Topic 驱逐指定用户的所有 Session 并清除缓存
func (t *Topic) evictUser(uid types.Uid, unsub bool, skip string) {
	now := types.TimeNow()
	pud, ok := t.perUser[uid]
	// 在删除频道读者的 perUser 记录前保存其客户端可见 Topic 名称。
	// 否则后续 t.original 会把驱逐通知错误地写成 grp...。
	original := t.original(uid)

	if unsub {
		if t.cat == types.TopicCatP2P {
			pud.online = 0
			pud.deleted = true
			t.perUser[uid] = pud
		} else if ok {
			delete(t.perUser, uid)
			t.computePerUserAcsUnion()

			if !pud.isChan {
				usersRegisterUser(uid, false)
			}
		}
	} else if ok {
		if pud.isChan {
			delete(t.perUser, uid)
		} else {
			pud.online = 0
			t.perUser[uid] = pud
		}
	}

	msg := NoErrEvicted("", original, now)
	msg.Ctrl.Params = map[string]any{"unsub": unsub}
	msg.SkipSid = skip
	msg.uid = uid
	msg.AsUser = uid.UserId()
	for s := range t.sessions {
		if pssd, removed := t.remSession(s, uid); pssd != nil {
			if removed {
				s.detachSession(t.name)
			}
			if s.sid != skip {
				s.queueOut(msg)
			}
		}
	}
}
