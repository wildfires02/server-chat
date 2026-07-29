package server

import (
	"errors"
	"time"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"
)

// replyGetCreds 返回用户的凭证，如电子邮件和电话号码。
func (t *Topic) replyGetCreds(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()
	id := msg.Id

	if t.cat != types.TopicCatMe {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category for getting credentials")
	}

	screds, err := store.Users.GetAllCreds(asUid, "", false)
	if err != nil {
		sess.queueOut(decodeStoreErrorExplicitTs(err, id, msg.Original, now, msg.Timestamp, nil))
		return err
	}

	if len(screds) > 0 {
		creds := make([]*MsgCredServer, len(screds))
		for i, sc := range screds {
			creds[i] = &MsgCredServer{Method: sc.Method, Value: sc.Value, Done: sc.Done}
		}
		sess.queueOut(&ServerComMessage{
			Meta: &MsgServerMeta{
				Id:        id,
				Topic:     t.original(asUid),
				Timestamp: &now,
				Cred:      creds,
			},
		})
		return nil
	}

	// 通知请求者没有凭证。
	sess.queueOut(NoContentParamsReply(msg, now, map[string]string{"what": "creds"}))

	return nil
}

// replySetCred 添加或验证用户凭证，如电子邮件和电话号码。
func (t *Topic) replySetCred(sess *Session, asUid types.Uid, authLevel auth.Level, msg *ClientComMessage) error {
	now := types.TimeNow()
	set := msg.Set

	if t.cat != types.TopicCatMe {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category for updating credentials")
	}

	var err error
	var tags []string
	creds := []MsgCredClient{*set.Cred}
	if set.Cred.Response != "" {
		// 凭证正在被验证。如果响应无效则返回错误。
		_, tags, err = validatedCreds(asUid, authLevel, creds, true)
	} else {
		// 凭证正在被添加或更新。
		tmpToken, _, _ := store.Store.GetLogicalAuthHandler("token").GenSecret(&auth.Rec{
			Uid:       asUid,
			AuthLevel: auth.LevelNone,
			Lifetime:  auth.Duration(time.Hour * 24),
			Features:  auth.FeatureNoLogin,
		})
		_, tags, err = addCreds(asUid, creds, nil, sess.lang, tmpToken)
	}

	if tags != nil {
		t.tags = tags
		t.presSubsOnline("tags", "", nilPresParams, nilPresFilters, "")
	}

	sess.queueOut(decodeStoreErrorExplicitTs(err, set.Id, t.original(asUid), now, msg.Timestamp, nil))

	return err
}

// replyGetAux 返回 Topic 的辅助键值对集合。
func (t *Topic) replyGetAux(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()

	if t.cat != types.TopicCatP2P && t.cat != types.TopicCatGrp && t.cat != types.TopicCatSlf {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category to query aux")
	}

	if len(t.aux) > 0 {
		sess.queueOut(&ServerComMessage{
			Meta: &MsgServerMeta{
				Id:        msg.Id,
				Topic:     t.original(asUid),
				Timestamp: &now,
				Aux:       t.aux,
			},
		})
		return nil
	}

	// 通知请求者没有标签。
	sess.queueOut(NoContentParamsReply(msg, now, map[string]string{"what": "aux"}))

	return nil
}

// replyGetAux 返回 Topic 的辅助键值对集合。
func (t *Topic) replySetAux(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()

	if t.cat != types.TopicCatP2P && t.cat != types.TopicCatGrp && t.cat != types.TopicCatSlf {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("invalid topic category to assign aux")
	}

	if userData := t.perUser[asUid]; !(userData.modeGiven & userData.modeWant).IsAdmin() {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("aux update by non-admin")
	}

	if aux, changed := mergeMaps(copyMap(t.aux), msg.Set.Aux); changed {
		err := store.Topics.Update(t.name, map[string]any{"Aux": aux, "UpdatedAt": now})
		if err == nil {
			t.aux = aux
			t.presSubsOnline("aux", "", nilPresParams, nilPresFilters, sess.sid)
		}
		sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Set.Id, t.original(asUid), now, msg.Timestamp, nil))
		return err
	}

	sess.queueOut(InfoNotModifiedReply(msg, now))
	return nil
}
