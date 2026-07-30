package server

import (
	"errors"
	"strings"

	"chat/server/store"
	"chat/server/store/types"
)

// replyGetContacts 返回当前用户的全量通讯录或指定版本后的增量。
func (t *Topic) replyGetContacts(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()
	if t.cat != types.TopicCatMe || msg.Get.Contacts == nil {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("contacts can only be queried on the me topic")
	}
	result, err := store.Contacts.Get(asUid, *msg.Get.Contacts)
	if err != nil {
		sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
		return err
	}
	sess.queueOut(&ServerComMessage{Meta: &MsgServerMeta{
		Id:        msg.Id,
		Topic:     t.original(asUid),
		Timestamp: &now,
		Contacts:  result,
	}})
	return nil
}

// replySetContact 原子执行一条联系人或分组变更，并通知当前用户的其它设备同步。
func (t *Topic) replySetContact(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()
	if t.cat != types.TopicCatMe || msg.Set.Contact == nil {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("contacts can only be updated on the me topic")
	}
	if strings.EqualFold(msg.Set.Contact.Op, "request_friend") {
		target := types.ParseUserId(msg.Set.Contact.User)
		if target.IsZero() && msg.Set.Contact.Contact != nil {
			target = types.ParseUserId(msg.Set.Contact.Contact.User)
		}
		if target.IsZero() {
			sess.queueOut(ErrMalformedReply(msg, now))
			return types.ErrMalformed
		}
		user, err := store.Users.Get(target)
		if err != nil {
			sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
			return err
		}
		if user == nil {
			sess.queueOut(decodeStoreErrorExplicitTs(types.ErrUserNotFound, msg.Id, msg.Original,
				now, msg.Timestamp, nil))
			return types.ErrUserNotFound
		}
	}
	result, err := store.Contacts.Apply(asUid, *msg.Set.Contact)
	if err != nil {
		sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
		return err
	}
	sess.queueOut(&ServerComMessage{Meta: &MsgServerMeta{
		Id:        msg.Id,
		Topic:     t.original(asUid),
		Timestamp: &now,
		Contacts:  result,
	}})
	t.presSubsOnline("contacts", "", nilPresParams, nilPresFilters, sess.sid)
	op := strings.ToLower(msg.Set.Contact.Op)
	if (op == "request_friend" || op == "accept_friend" || op == "remove_friend") &&
		globals.hub != nil {
		target := types.ParseUserId(msg.Set.Contact.User)
		if target.IsZero() && msg.Set.Contact.Contact != nil {
			target = types.ParseUserId(msg.Set.Contact.Contact.User)
		}
		if !target.IsZero() {
			presSingleUserOfflineOffline(target, asUid.UserId(), "contacts",
				&presParams{actor: asUid.UserId()}, "")
		}
	}
	return nil
}
