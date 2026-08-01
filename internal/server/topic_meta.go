// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// handleMetaGet 处理元数据Get消息或事件。
func (t *Topic) handleMetaGet(msg *ClientComMessage, asUid types.Uid, asChan bool, authLevel auth.Level) {
	if msg.MetaWhat&constMsgMetaDesc != 0 {
		if err := t.replyGetDesc(msg.sess, asUid, asChan, msg.Get.Desc, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Desc failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaSub != 0 {
		if err := t.replyGetSub(msg.sess, asUid, authLevel, asChan, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Sub failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaData != 0 {
		if err := t.replyGetData(msg.sess, asUid, asChan, msg.Get.Data, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Data failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaDel != 0 {
		if err := t.replyGetDel(msg.sess, asUid, msg.Get.Del, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Del failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaTags != 0 {
		if err := t.replyGetTags(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Tags failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaCred != 0 {
		if err := t.replyGetCreds(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Creds failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaAux != 0 {
		logs.Warn.Printf("topic[%s] handle getAux", t.name)
		if err := t.replyGetAux(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Aux failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaSearch != 0 {
		if err := t.replySearch(msg.sess, asUid, asChan, authLevel, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Search failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaContacts != 0 {
		if err := t.replyGetContacts(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Contacts failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaAssets != 0 {
		if err := t.replyGetAssets(msg.sess, asUid, authLevel, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Assets failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaReaders != 0 {
		if err := t.replyGetReaders(msg.sess, asUid, asChan, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Readers failed: %s", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaPreviews != 0 {
		if err := t.replyGetPreviews(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Get.Previews failed: %s", t.name, err)
		}
	}
}

// handleMetaSet 处理元数据Set消息或事件。
func (t *Topic) handleMetaSet(msg *ClientComMessage, asUid types.Uid, asChan bool, authLevel auth.Level) {
	if msg.MetaWhat&constMsgMetaInvite != 0 {
		if err := t.replySetInvite(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Invite failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaDesc != 0 {
		if err := t.replySetDesc(msg.sess, asUid, asChan, authLevel, msg); err == nil {
			// 通知插件更新
			pluginTopic(t, plgActUpd)
		} else {
			logs.Warn.Printf("topic[%s] meta.Set.Desc failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaSub != 0 {
		if err := t.replySetSub(msg.sess, msg, asChan); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Sub failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaTags != 0 {
		if err := t.replySetTags(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Tags failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaCred != 0 {
		if err := t.replySetCred(msg.sess, asUid, authLevel, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Cred failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaAux != 0 {
		if err := t.replySetAux(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Aux failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaContacts != 0 {
		if err := t.replySetContact(msg.sess, asUid, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Contact failed: %v", t.name, err)
		}
	}
	if msg.MetaWhat&constMsgMetaAssets != 0 {
		if err := t.replySetAsset(msg.sess, asUid, authLevel, msg); err != nil {
			logs.Warn.Printf("topic[%s] meta.Set.Asset failed: %v", t.name, err)
		}
	}
}

// handleMetaDel 处理元数据Del消息或事件。
func (t *Topic) handleMetaDel(msg *ClientComMessage, asUid types.Uid, asChan bool, authLevel auth.Level) {
	var err error
	if refreshErr := t.refreshOfficialChannelMember(asUid); refreshErr != nil {
		msg.sess.queueOut(ErrPermissionDeniedReply(msg, types.TimeNow()))
		return
	}
	switch msg.MetaWhat {
	case constMsgDelMsg:
		err = t.replyDelMsg(msg.sess, asUid, asChan, msg)
	case constMsgDelSub:
		err = t.replyDelSub(msg.sess, asUid, msg)
	case constMsgDelTopic:
		err = t.replyDelTopic(msg.sess, asUid, msg)
	case constMsgDelCred:
		err = t.replyDelCred(msg.sess, asUid, authLevel, msg)
	case constMsgDelScheduled:
		err = t.replyDelScheduled(msg.sess, asUid, msg)
	}

	if err != nil {
		logs.Warn.Printf("topic[%s] meta.Del failed: %v", t.name, err)
	}
}

// replyDelScheduled 校验发送权限并取消当前用户创建的持久化定时消息。
func (t *Topic) replyDelScheduled(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()
	if types.ParseUid(msg.Del.ScheduledId).IsZero() {
		sess.queueOut(ErrMalformedReply(msg, now))
		return types.ErrMalformed
	}
	mode := t.perUser[asUid].modeGiven & t.perUser[asUid].modeWant
	if !mode.IsWriter() {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return types.ErrPermissionDenied
	}
	if err := store.Messages.DeleteScheduled(msg.Del.ScheduledId, t.name, asUid); err != nil {
		sess.queueOut(ErrUnknownReply(msg, now))
		return err
	}
	sess.queueOut(NoErrParamsReply(msg, now, map[string]any{
		"what": "sched", "scheduled": msg.Del.ScheduledId,
	}))
	return nil
}

// handleMeta 实现通过 Topic.meta Channel 接收的元数据请求处理逻辑
func (t *Topic) handleMeta(msg *ClientComMessage) {
	// 请求获取/设置 Topic 元数据
	asUid := types.ParseUserId(msg.AsUser)
	authLevel := auth.Level(msg.AuthLvl)
	asChan, err := t.verifyChannelAccess(msg.Original)
	if err != nil {
		// 用户不应能将非 Channel Topic 作为 Channel 寻址。
		msg.sess.queueOut(ErrNotFoundReply(msg, types.TimeNow()))
		return
	}
	switch {
	case msg.Get != nil:
		// Get 请求
		t.handleMetaGet(msg, asUid, asChan, authLevel)

	case msg.Set != nil:
		// Set 请求
		t.handleMetaSet(msg, asUid, asChan, authLevel)

	case msg.Del != nil:
		// Del 请求
		t.handleMetaDel(msg, asUid, asChan, authLevel)
	}
}
