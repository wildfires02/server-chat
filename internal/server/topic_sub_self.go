package server

import (
	"errors"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"
)

// thisUserSub 完成this用户订阅所需的内部处理。
func (t *Topic) thisUserSub(sess *Session, pkt *ClientComMessage, asUid types.Uid, asChan bool, want string,
	private any, invite string) (*MsgAccessMode, error) {

	now := types.TimeNow()
	asLvl := auth.Level(pkt.AuthLvl)

	oldWant := types.ModeNone
	oldGiven := types.ModeNone

	modeWant := types.ModeUnset
	if want != "" {
		if err := modeWant.UnmarshalText([]byte(want)); err != nil {
			sess.queueOut(ErrMalformedReply(pkt, now))
			return nil, err
		}
	}

	var err error
	userData, existingSub := t.perUser[asUid]
	hasInvite := invite != "" && t.cat == types.TopicCatGrp
	if hasInvite && !validateTopicInvite(invite, t.name, now) {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("group invitation token is invalid, expired, or belongs to another topic")
	}

	// 旧客户端的“移除成员”操作错误地写入了封禁 ACL，而不是删除订阅。
	// 有效邀请必须能恢复这类记录，否则被踢出的成员无法通过邀请重新加入。
	if existingSub && !userData.deleted && hasInvite && !userData.modeGiven.IsJoiner() {
		if !consumeTopicInvite(invite, t.name, now) {
			sess.queueOut(ErrPermissionDeniedReply(pkt, now))
			return nil, errors.New("group invitation has no remaining uses")
		}
		oldWant, oldGiven := userData.modeWant, userData.modeGiven
		memberAccess := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
		userData.modeWant = memberAccess
		userData.modeGiven = memberAccess
		if err := store.Subs.Update(t.name, asUid, map[string]any{
			"ModeWant":  userData.modeWant,
			"ModeGiven": userData.modeGiven,
		}); err != nil {
			sess.queueOut(ErrUnknownReply(pkt, now))
			return nil, err
		}
		t.perUser[asUid] = userData
		if !(oldWant & oldGiven).IsReader() {
			usersUpdateUnread(asUid, t.lastID-userData.readID, true)
		}
		t.notifySubChange(asUid, asUid, false,
			oldWant, oldGiven, userData.modeWant, userData.modeGiven, sess.sid)
		pluginSubscription(&types.Subscription{
			User: asUid.String(), Topic: t.name,
			ModeWant: userData.modeWant, ModeGiven: userData.modeGiven,
		}, plgActUpd)
		return &MsgAccessMode{
			Want:  userData.modeWant.String(),
			Given: userData.modeGiven.String(),
			Mode:  (userData.modeGiven & userData.modeWant).String(),
			Role:  topicRoleFromAccess(userData.modeGiven&userData.modeWant, t.isChan, userData.isChan),
		}, nil
	}

	if !existingSub || userData.deleted {
		if t.cat == types.TopicCatGrp {
			if err := t.checkOfficialSelfJoin(sess, pkt, asUid, hasInvite, now); err != nil {
				return nil, err
			}
			if hasInvite && !consumeTopicInvite(invite, t.name, now) {
				sess.queueOut(ErrPermissionDeniedReply(pkt, now))
				return nil, errors.New("group invitation has no remaining uses")
			}
		}
		if t.cat == types.TopicCatGrp && !asChan && !t.isOfficialLargeGroup() &&
			t.subsCount() >= globals.maxSubscriberCount {
			sess.queueOut(ErrPolicyReply(pkt, now))
			return nil, errors.New("超出最大订阅数量限制")
		}

		var sub *types.Subscription
		tname := t.name
		if t.cat == types.TopicCatP2P {
			if modeWant != types.ModeUnset {
				userData.modeWant = modeWant
			}
			userData.modeWant = (userData.modeWant & globals.typesModeCP2P) | types.ModeApprove
		} else if t.cat == types.TopicCatSys {
			if asLvl != auth.LevelRoot {
				sess.queueOut(ErrPermissionDeniedReply(pkt, now))
				return nil, errors.New("订阅 'sys' Topic 需要 Root 权限")
			}

			userData.modeWant = types.ModeCSys
			userData.modeGiven = types.ModeCSys
			if modeWant != types.ModeUnset {
				userData.modeWant = (modeWant & types.ModeCSys) | types.ModeWrite | types.ModeJoin
			}
		} else if asChan {
			userData.isChan = true

			sub, err = store.Subs.Get(pkt.Original, asUid, false)
			if err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			}

			if sub != nil {
				oldWant = sub.ModeWant
				oldGiven = sub.ModeGiven
				// 管理员可将频道读者设置为 banned。必须尊重持久化的
				// ModeGiven，不能在重新订阅时无条件恢复读权限。
				userData.modeGiven = sub.ModeGiven
			} else {
				oldWant = types.ModeCChnReader
				oldGiven = types.ModeCChnReader
				userData.modeGiven = types.ModeCChnReader
			}

			if modeWant != types.ModeUnset {
				userData.modeWant = (modeWant & types.ModeCChnReader) | types.ModeRead | types.ModeJoin
			} else {
				userData.modeWant = oldWant
			}

			tname = pkt.Original
		} else {
			if !existingSub {
				sub, err = store.Subs.Get(t.name, asUid, true)
				if err != nil {
					sess.queueOut(ErrUnknownReply(pkt, now))
					return nil, err
				}

				if sub != nil {
					userData.modeGiven = sub.ModeGiven
				} else {
					userData.modeGiven = types.ModeUnset
				}
			}

			if userData.modeGiven == types.ModeUnset {
				userData.modeGiven = t.accessFor(asLvl)
			}

			if modeWant == types.ModeUnset {
				userData.modeWant = t.accessFor(asLvl)
			} else {
				userData.modeWant = modeWant
			}

			if hasInvite {
				// 有效邀请代表管理员明确授予成员权限；它不依赖私有群的默认
				//加入权限，也不会授予管理员或群主权限。
				memberAccess := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
				userData.modeWant = memberAccess
				userData.modeGiven = memberAccess
			}
		}

		if !userData.modeGiven.IsJoiner() {
			sess.queueOut(ErrPermissionDeniedReply(pkt, now))
			return nil, errors.New("由于权限不足订阅被拒绝")
		}

		if userData.deleted {
			userData.deleted = false
			userData.delID, userData.readID, userData.recvID = 0, 0, 0
		}

		if isNullValue(private) {
			private = nil
		}
		userData.private = private

		if sub == nil || sub.DeletedAt != nil {
			sub = &types.Subscription{
				User:      asUid.String(),
				Topic:     tname,
				ModeWant:  userData.modeWant,
				ModeGiven: userData.modeGiven,
				Private:   userData.private,
			}

			if err := store.Subs.Create(sub); err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			}

		} else if asChan && userData.modeWant != oldWant {
			if err := store.Subs.Update(tname, asUid,
				map[string]any{"ModeWant": userData.modeWant}); err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			}

			t.channelSubUnsub(asUid, userData.modeWant.IsPresencer())
		}

		if asChan {
			if userData.modeWant != oldWant {
				pluginSubscription(sub, plgActCreate)
			} else {
				pluginSubscription(sub, plgActUpd)
			}
		} else {
			usersRegisterUser(asUid, true)
			pluginSubscription(sub, plgActCreate)
		}

	} else {
		if !userData.isChan && asChan {
			sess.queueOut(InfoUseOtherReply(pkt, t.name, now))
			return nil, types.ErrNotFound
		}

		oldWant = userData.modeWant
		oldGiven = userData.modeGiven

		transition, transitionErr := planExistingSelfSubscription(selfSubscriptionTransitionInput{
			category:      t.cat,
			isTopicOwner:  t.owner == asUid,
			oldWant:       oldWant,
			oldGiven:      oldGiven,
			requested:     modeWant,
			defaultAccess: t.accessFor(asLvl),
			p2pAccess:     globals.typesModeCP2P,
		})
		if transitionErr != nil {
			sess.queueOut(ErrPermissionDeniedReply(pkt, now))
			return nil, transitionErr
		}
		userData.modeWant = transition.want
		userData.modeGiven = transition.given
		ownerChange := transition.ownerChange

		sub := types.Subscription{
			User:  asUid.String(),
			Topic: t.name,
		}

		update := map[string]any{}
		if isNullValue(private) {
			update["Private"] = nil
			userData.private = nil
			sub.Private = private
		} else if private != nil {
			update["Private"] = private
			userData.private = private
			sub.Private = private
		}
		if userData.modeWant != oldWant {
			update["ModeWant"] = userData.modeWant
			sub.ModeWant = userData.modeWant
		}
		if userData.modeGiven != oldGiven {
			update["ModeGiven"] = userData.modeGiven
			sub.ModeGiven = userData.modeGiven
		}

		if len(update) > 0 {
			if err := store.Subs.Update(t.name, asUid, update); err != nil {
				sess.queueOut(ErrUnknownReply(pkt, now))
				return nil, err
			}
			pluginSubscription(&sub, plgActUpd)
		}

		if ownerChange {
			oldOwnerData := t.perUser[t.owner]
			oldOwnerOldWant, oldOwnerOldGiven := oldOwnerData.modeWant, oldOwnerData.modeGiven
			oldOwnerData.modeGiven = (oldOwnerData.modeGiven & ^types.ModeOwner)
			oldOwnerData.modeWant = (oldOwnerData.modeWant & ^types.ModeOwner)
			if err := store.Subs.Update(t.name, t.owner,
				map[string]any{
					"ModeWant":  oldOwnerData.modeWant,
					"ModeGiven": oldOwnerData.modeGiven,
				}); err != nil {
				return nil, err
			}
			if err := store.Topics.OwnerChange(t.name, asUid); err != nil {
				return nil, err
			}
			t.perUser[t.owner] = oldOwnerData
			t.notifySubChange(t.owner, asUid, false,
				oldOwnerOldWant, oldOwnerOldGiven, oldOwnerData.modeWant, oldOwnerData.modeGiven, "")
			t.owner = asUid
		}
	}

	if !asChan {
		if (oldWant & oldGiven).IsPresencer() && !(userData.modeWant & userData.modeGiven).IsPresencer() {
			if t.cat == types.TopicCatMe {
				t.presUsersOfInterest("off+dis", t.userAgent)
			} else {
				t.presSingleUserOffline(asUid, userData.modeWant&userData.modeGiven,
					"off+dis", nilPresParams, "", false)
			}
		}
	}
	t.perUser[asUid] = userData

	var modeChanged *MsgAccessMode
	if oldWant != userData.modeWant || oldGiven != userData.modeGiven {
		if !asChan {
			oldReader := (oldWant & oldGiven).IsReader()
			newReader := (userData.modeWant & userData.modeGiven).IsReader()

			if oldReader && !newReader {
				usersUpdateUnread(asUid, userData.readID-t.lastID, true)
			} else if !oldReader && newReader {
				usersUpdateUnread(asUid, t.lastID-userData.readID, true)
			}
		}

		t.notifySubChange(asUid, asUid, asChan, oldWant, oldGiven, userData.modeWant, userData.modeGiven, sess.sid)
	}

	if (pkt.Sub != nil && pkt.Sub.Newsub) || oldWant != userData.modeWant || oldGiven != userData.modeGiven {
		modeChanged = &MsgAccessMode{
			Want:  userData.modeWant.String(),
			Given: userData.modeGiven.String(),
			Mode:  (userData.modeGiven & userData.modeWant).String(),
			Role: topicRoleFromAccess(userData.modeGiven&userData.modeWant,
				t.isChan, userData.isChan),
		}
	}

	if !userData.modeWant.IsJoiner() {
		t.evictUser(asUid, false, "")
		return modeChanged, nil
	}

	if !userData.modeGiven.IsJoiner() {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return nil, errors.New("Topic 拒绝访问；用户已被被封禁")
	}

	return modeChanged, nil
}
