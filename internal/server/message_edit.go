// Package server 实现已发送消息的编辑流程。
package server

import (
	"time"

	"chat/server/drafty"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// editMessage 原地替换指定消息的正文和客户端自定义头，并广播完整消息。
//
// 普通成员只能编辑自己的消息，管理员可以编辑 Topic 内任意消息。
func (t *Topic) editMessage(msg *ClientComMessage, asUid types.Uid) error {
	pub := msg.Pub
	if pub.ReplaceSeq <= 0 || pub.Forward != nil || pub.ReplyTo != 0 ||
		pub.GroupId != "" || pub.ScheduleAt != nil {
		return types.ErrMalformed
	}
	target, err := store.Messages.Get(t.name, pub.ReplaceSeq)
	if err != nil {
		return err
	}
	if target == nil {
		return types.ErrNotFound
	}

	userData := t.perUser[asUid]
	mode := userData.modeGiven & userData.modeWant
	if userData.isChan ||
		(!mode.IsAdmin() && (types.ParseUid(target.From) != asUid || !mode.IsWriter())) {
		return types.ErrPermissionDenied
	}
	if err = t.checkOfficialPublish(asUid, "message", types.TimeNow()); err != nil {
		return err
	}
	info, err := validateMessageContent(pub.Kind, pub.Content)
	if err != nil {
		return types.ErrMalformed
	}
	attachments, err := verifiedAttachmentURLs(info.Attachments, msg.Extra)
	if err != nil {
		return err
	}
	previousInfo, _ := drafty.Analyze(target.Content)
	hadAttachmentRefs := previousInfo != nil && len(previousInfo.Attachments) > 0

	// 保留原消息的回复、转发、反应等服务端元数据，仅合并客户端可修改的头。
	head := cloneHead(target.Head)
	if head == nil {
		head = map[string]any{}
	}
	for key, value := range stripServerMessageHead(pub.Head) {
		head[key] = value
	}
	head[headMessageKind] = info.Kind
	// 编辑后的消息类型重新执行权限校验，不能把普通文本改成无权发送的文档。
	if err = t.authorizeBusinessAction(
		asUid, businessPolicyAction(head, pub.Content, attachments),
	); err != nil {
		return types.ErrPolicy
	}
	now := types.TimeNow()
	head[headEditedAt] = now.Format(time.RFC3339Nano)
	target.Head = head
	target.Content = pub.Content
	target.UpdatedAt = now
	if err = store.Messages.Update(target); err != nil {
		return err
	}
	if t.cat == types.TopicCatP2P && globals.businessPolicy != nil {
		if err = globals.businessPolicy.archiveTextMessage(target, t.p2pOtherUser(asUid)); err != nil {
			logs.Warn.Printf("topic[%s]: 更新文字审计投影失败: %v", t.name, err)
		}
	}

	if store.Files != nil && (len(attachments) > 0 || hadAttachmentRefs) {
		// 编辑可能增加或移除附件，需要重建消息与文件的完整关联集合。
		if err = store.Files.LinkAttachments("", target.Uid(), attachments); err != nil {
			logs.Warn.Printf("topic[%s]: 更新已编辑消息的附件关联失败: %v", t.name, err)
		}
	}
	if msg.Id != "" && msg.sess != nil {
		msg.sess.queueOut(NoErrParamsReply(msg, now, map[string]any{
			"seq": pub.ReplaceSeq, "edited": now,
		}))
	}

	data := &ServerComMessage{
		Data:      serverDataFromStored(msg.Original, types.ParseUid(target.From).UserId(), target),
		RcptTo:    msg.RcptTo,
		AsUser:    msg.AsUser,
		Timestamp: now,
		sess:      msg.sess,
	}
	if pub.NoEcho && msg.sess != nil {
		data.SkipSid = msg.sess.sid
	}
	t.broadcastToSessions(data)
	return nil
}
