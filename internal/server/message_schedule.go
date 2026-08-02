// Package server 实现消息定时投递与幂等重试。
package server

import (
	"chat/server/store"
	"chat/server/store/types"
)

// scheduleMessage 将通过校验的消息写入持久化定时队列。
//
// 定时消息在真正投递时才获取 Topic SeqId，因此不会制造同步游标空洞。
func (t *Topic) scheduleMessage(
	msg *ClientComMessage,
	asUid types.Uid,
	head map[string]any,
	content any,
	attachments []string,
) error {
	publishAt := msg.Pub.ScheduleAt
	if publishAt == nil || !publishAt.After(msg.Timestamp) ||
		publishAt.After(msg.Timestamp.Add(maxScheduleAhead)) {
		return types.ErrMalformed
	}
	if msg.Pub.ClientId == "" {
		// 持久化定时投递必须提供调用方稳定的幂等键。
		return types.ErrMalformed
	}
	userData := t.perUser[asUid]
	if t.cat != types.TopicCatSys &&
		(userData.isChan || !(userData.modeWant & userData.modeGiven).IsWriter()) {
		return types.ErrPermissionDenied
	}
	scope := businessPolicyAction(head, content, attachments)
	if err := t.checkOfficialPublish(asUid, scope, msg.Timestamp); err != nil {
		return err
	}

	// 已经投递或仍在排队的请求直接返回原结果，避免创建重复消息。
	if delivered, err := store.Messages.GetByClientId(
		t.name,
		asUid,
		msg.Pub.ClientId,
	); err != nil {
		return err
	} else if delivered != nil {
		if msg.Id != "" && msg.sess != nil {
			msg.sess.queueOut(NoErrDeliveredParams(
				msg.Id,
				t.original(asUid),
				msg.Timestamp,
				map[string]any{
					"seq":       delivered.SeqId,
					"cid":       delivered.ClientId,
					"duplicate": true,
				},
			))
		}
		return nil
	}
	if pending, err := store.Messages.GetScheduledByClientId(
		t.name,
		asUid,
		msg.Pub.ClientId,
	); err != nil {
		return err
	} else if pending != nil {
		if msg.Id != "" && msg.sess != nil {
			reply := NoErrAccepted(msg.Id, t.original(asUid), msg.Timestamp)
			reply.Ctrl.Params = map[string]any{
				"scheduled": pending.Id,
				"schedule":  pending.PublishAt,
				"cid":       pending.ClientId,
				"duplicate": true,
			}
			msg.sess.queueOut(reply)
		}
		return nil
	}

	scheduled := &types.ScheduledMessage{
		ObjHeader:      types.ObjHeader{CreatedAt: msg.Timestamp},
		Topic:          t.name,
		From:           asUid.String(),
		ClientId:       msg.Pub.ClientId,
		NoEcho:         msg.Pub.NoEcho,
		PublishAt:      publishAt.UTC(),
		Head:           head,
		Content:        content,
		AttachmentURLs: attachments,
	}
	if err := store.Messages.Schedule(scheduled); err != nil {
		return err
	}
	msg.Pub.ClientId = scheduled.ClientId
	if msg.Id != "" && msg.sess != nil {
		reply := NoErrAccepted(msg.Id, t.original(asUid), msg.Timestamp)
		reply.Ctrl.Params = map[string]any{
			"scheduled": scheduled.Id,
			"schedule":  scheduled.PublishAt,
			"cid":       scheduled.ClientId,
		}
		msg.sess.queueOut(reply)
	}
	return nil
}
