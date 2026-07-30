package server

import (
	"errors"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"
)

// anotherUserSub 完成another用户订阅所需的内部处理。
func (t *Topic) anotherUserSub(sess *Session, asUid, target types.Uid, asChan bool,
	pkt *ClientComMessage) (*MsgAccessMode, error) {

	now := types.TimeNow()
	set := pkt.Set

	hostData, ok := t.perUser[asUid]
	hostMode := hostData.modeGiven & hostData.modeWant
	if !ok || !hostMode.IsSharer() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("Topic 拒绝访问；审批人无共享权限")
	}

	if asChan {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("Topic 拒绝访问：无法将读者订阅至 Channel")
	}

	if t.isReadOnly() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("Topic 已暂停")
	}

	modeGiven := types.ModeUnset
	if set.Sub.Mode != "" {
		if err := modeGiven.UnmarshalText([]byte(set.Sub.Mode)); err != nil {
			sess.queueOut(ErrMalformedReply(pkt, now))
			return nil, err
		}

		if t.cat == types.TopicCatP2P {
			modeGiven = (modeGiven & globals.typesModeCP2P) | types.ModeApprove
		}
	}

	if modeGiven != types.ModeUnset && !hostMode.IsAdmin() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("共享者无法显式设置 modeGiven")
	}

	if modeGiven.IsOwner() && t.owner != asUid {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("非群主尝试转让群主权限")
	}
	if modeGiven != types.ModeUnset && t.owner != asUid &&
		!hostMode.BetterEqual(modeGiven&^types.ModeOwner) {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("管理员无法授予自己不具备的权限")
	}

	oldWant := types.ModeUnset
	oldGiven := types.ModeUnset

	userData, existingSub := t.perUser[target]
	if !existingSub || userData.deleted {
		if t.cat == types.TopicCatGrp && !t.isOfficialLargeGroup() &&
			t.subsCount() >= globals.maxSubscriberCount {
			sess.queueOut(ErrPolicyReply(pkt, now))
			return nil, errors.New("超出最大订阅数量限制")
		}

		if modeGiven == types.ModeUnset {
			modeGiven = t.accessFor(auth.LevelAuth)
			modeGiven |= types.ModeJoin
		}

		var modeWant types.AccessMode
		sub, err := store.Subs.Get(t.name, target, true)
		if err != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}

		if sub != nil {
			modeWant = sub.ModeWant
		} else {
			if user, err := store.Users.Get(target); err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			} else if user == nil {
				sess.queueOut(ErrUserNotFoundReply(pkt, now))
				return nil, errors.New("未找到用户")
			} else if user.State != types.StateOK {
				sess.queueOut(ErrPermissionDeniedReply(pkt, now))
				return nil, errors.New("用户已禁言/封禁")
			} else {
				modeWant = user.Access.Auth & modeGiven
			}
		}

		if !modeWant.IsJoiner() {
			sess.queueOut(ErrPermissionDeniedReply(pkt, now))
			return nil, errors.New("由于权限不足邀请被拒绝")
		}

		sub = &types.Subscription{
			User:      target.String(),
			Topic:     t.name,
			ModeWant:  modeWant,
			ModeGiven: modeGiven,
		}

		if err := store.Subs.Create(sub); err != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}
		if t.cat == types.TopicCatGrp {
			t.subCnt++
		}

		userData = perUserData{
			modeGiven: sub.ModeGiven,
			modeWant:  sub.ModeWant,
			private:   nil,
		}
		t.perUser[target] = userData
		t.computePerUserAcsUnion()

		usersRegisterUser(target, true)
		pluginSubscription(sub, plgActCreate)
		sendPush(t.pushForP2PSub(asUid, target, userData.modeWant, userData.modeGiven, now))
	} else {
		oldGiven = userData.modeGiven
		oldWant = userData.modeWant

		if modeGiven == types.ModeUnset {
			modeGiven = userData.modeGiven
		} else if modeGiven != userData.modeGiven {
			if t.owner == target && (!modeGiven.IsOwner() || !modeGiven.IsJoiner()) {
				sess.queueOut(ErrPermissionDeniedReply(pkt, now))
				return nil, errors.New("无法剥夺群主身份或封禁群主")
			}

			if err := store.Subs.Update(t.name, target,
				map[string]any{"ModeGiven": modeGiven}); err != nil {
				return nil, err
			}

			userData.modeGiven = modeGiven
			t.perUser[target] = userData
		}
	}

	var modeChanged *MsgAccessMode
	if oldGiven != userData.modeGiven {
		oldReader := (oldWant & oldGiven).IsReader()
		newReader := (userData.modeWant & userData.modeGiven).IsReader()
		if oldReader && !newReader {
			usersUpdateUnread(target, userData.readID-t.lastID, true)
		} else if !oldReader && newReader {
			usersUpdateUnread(target, t.lastID-userData.readID, true)
		}
		t.notifySubChange(target, asUid, false,
			oldWant, oldGiven, userData.modeWant, userData.modeGiven, sess.sid)

		modeChanged = &MsgAccessMode{
			Given: userData.modeGiven.String(),
			Want:  userData.modeWant.String(),
			Mode:  (userData.modeGiven & userData.modeWant).String(),
			Role: topicRoleFromAccess(userData.modeGiven&userData.modeWant,
				t.isChan, userData.isChan),
		}
	}

	if !userData.modeGiven.IsJoiner() {
		t.evictUser(target, false, "")
	}
	t.evictColdSubscriber(target)

	return modeChanged, nil
}
