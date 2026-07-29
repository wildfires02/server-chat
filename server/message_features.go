// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"chat/server/drafty"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	// 以下 x-* 头字段只能由服务端写入，客户端提交的同名字段会在入库前被移除。
	headMessageKind = "x-kind"
	// headReplyTo 指定消息头ReplyTo。
	headReplyTo = "x-reply"
	// headForwarded 指定消息头Forwarded。
	headForwarded = "x-forward"
	// headGroupID 指定消息头Group标识。
	headGroupID = "x-group"
	// headEditedAt 指定消息头EditedAt。
	headEditedAt = "x-edited"
	// headReactions 指定消息头Reactions。
	headReactions = "x-reactions"
	// topicPinsKey 指定TopicPins键。
	topicPinsKey = "pins"

	// maxReactionLength 指定max反应Length。
	maxReactionLength = 64
	// maxReactionKinds 指定max反应Kinds。
	maxReactionKinds = 20
	// maxPinnedMessages 指定maxPinnedMessages。
	maxPinnedMessages = 100
	// minScheduleDelay 指定minScheduleDelay。
	minScheduleDelay = 10 * time.Second
	// maxScheduleAhead 指定maxScheduleAhead。
	maxScheduleAhead = 366 * 24 * time.Hour
)

// validMessageKinds 是服务端能够从消息正文中可信推导出的消息类型集合。
var validMessageKinds = map[string]bool{
	"text": true, "drafty": true, "image": true, "video": true,
	"voice": true, "audio": true, "file": true,
}

// jsonForward 是保存在消息头中的原始消息引用。
type jsonForward struct {
	// Topic 在 P2P 会话中为空，避免向客户端泄露内部 Topic 名称。
	Topic string `json:"topic,omitempty"`
	// SeqId 保存序列号标识。
	SeqId int `json:"seq"`
	// From 保存From。
	From string `json:"from,omitempty"`
	// Timestamp 保存Timestamp。
	Timestamp time.Time `json:"ts"`
}

// storedReaction 是消息头中的反应明细；用户列表仅在服务端存储，不下发。
type storedReaction struct {
	// Reaction 保存反应。
	Reaction string `json:"reaction"`
	// Users 指示是否启用或满足Users。
	Users []string `json:"users"`
}

// cloneHead 浅复制消息头，避免修改调用方或已入库消息持有的 map。
func cloneHead(head map[string]any) map[string]any {
	if len(head) == 0 {
		return nil
	}
	out := make(map[string]any, len(head))
	for key, value := range head {
		out[key] = value
	}
	return out
}

// stripServerMessageHead 移除客户端无权设置的服务端消息元数据。
func stripServerMessageHead(head map[string]any) map[string]any {
	out := cloneHead(head)
	for _, key := range []string{
		headMessageKind, headReplyTo, headForwarded, headGroupID, headEditedAt, headReactions,
	} {
		delete(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// encodeHeadValue 把结构体归一化成 JSON 兼容值，保证四种数据库编码结果一致。
func encodeHeadValue(value any) any {
	raw, _ := json.Marshal(value)
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// decodeHeadValue 从 JSON 兼容值解码服务端消息头。
func decodeHeadValue(value, out any) bool {
	raw, err := json.Marshal(value)
	return err == nil && json.Unmarshal(raw, out) == nil
}

// intHeadValue 兼容 JSON、SQL 和内存存储返回的常见整数表示。
func intHeadValue(value any) int {
	switch val := value.(type) {
	case int:
		return val
	case int32:
		return int(val)
	case int64:
		return int(val)
	case float64:
		return int(val)
	default:
		return 0
	}
}

// messageKind 优先读取服务端已验证的类型；旧消息则根据正文即时推导。
func messageKind(msg *types.Message) string {
	if kind, _ := msg.Head[headMessageKind].(string); kind != "" {
		return kind
	}
	if info, err := drafty.Analyze(msg.Content); err == nil {
		return info.Kind
	}
	return ""
}

// messageReactions 解码消息头中的服务端反应明细。
func messageReactions(head map[string]any) []storedReaction {
	var reactions []storedReaction
	if !decodeHeadValue(head[headReactions], &reactions) {
		return nil
	}
	return reactions
}

// serverDataFromStored 将持久化消息还原为对客户端安全的 data 包。
// 反应只暴露聚合计数，不暴露参与反应的用户列表。
func serverDataFromStored(topic, from string, msg *types.Message) *MsgServerData {
	data := &MsgServerData{
		Topic:     topic,
		From:      from,
		ClientId:  msg.ClientId,
		Timestamp: msg.CreatedAt,
		DeletedAt: msg.DeletedAt,
		SeqId:     msg.SeqId,
		Kind:      messageKind(msg),
		ReplyTo:   intHeadValue(msg.Head[headReplyTo]),
		Head:      stripServerMessageHead(msg.Head),
		Content:   msg.Content,
	}
	if edited, _ := msg.Head[headEditedAt].(string); edited != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, edited); err == nil {
			data.EditedAt = &parsed
		}
	}
	if group, _ := msg.Head[headGroupID].(string); group != "" {
		data.GroupId = group
	}
	var fwd jsonForward
	if decodeHeadValue(msg.Head[headForwarded], &fwd) {
		data.Forwarded = &MsgForwardedMessage{
			Topic:     fwd.Topic,
			SeqId:     fwd.SeqId,
			From:      fwd.From,
			Timestamp: fwd.Timestamp,
		}
	}
	for _, reaction := range messageReactions(msg.Head) {
		if len(reaction.Users) > 0 {
			data.Reactions = append(data.Reactions, MsgReaction{
				Reaction: reaction.Reaction,
				Count:    len(reaction.Users),
			})
		}
	}
	sort.Slice(data.Reactions, func(i, j int) bool {
		if data.Reactions[i].Count == data.Reactions[j].Count {
			return data.Reactions[i].Reaction < data.Reactions[j].Reaction
		}
		return data.Reactions[i].Count > data.Reactions[j].Count
	})
	return data
}

// validateMessageContent 校验正文并验证客户端声明的 kind 是否与正文一致。
func validateMessageContent(requestedKind string, content any) (*drafty.ContentInfo, error) {
	info, err := drafty.Analyze(content)
	if err != nil {
		return nil, err
	}
	if requestedKind != "" {
		requestedKind = strings.ToLower(requestedKind)
		if !validMessageKinds[requestedKind] || requestedKind != info.Kind {
			return nil, fmt.Errorf("message kind %q does not match content kind %q", requestedKind, info.Kind)
		}
	}
	return info, nil
}

// verifiedAttachmentURLs 以 Drafty 实体提取的附件为可信来源，并验证 extra 中的声明。
func verifiedAttachmentURLs(extracted []string, extra *MsgClientExtra) ([]string, error) {
	seen := make(map[string]bool, len(extracted))
	out := make([]string, 0, len(extracted))
	for _, ref := range extracted {
		if ref != "" && !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	if extra != nil {
		for _, ref := range extra.Attachments {
			if ref == "" || !seen[ref] {
				return nil, types.ErrMalformed
			}
		}
	}
	return out, nil
}

// prepareMessagePublication 校验发布参数，并把回复、转发、相册等客户端参数
// 转换为服务端管理的消息头。返回值可直接交给普通发布或定时队列持久化。
func (t *Topic) prepareMessagePublication(msg *ClientComMessage, asUid types.Uid) (map[string]any, any, []string, error) {
	pub := msg.Pub
	head := stripServerMessageHead(pub.Head)
	content := pub.Content
	if t.cat == types.TopicCatGrp && t.perUser[asUid].isChan {
		// 广播频道订阅读者始终只读，不能通过伪造 grp... 地址绕过频道 ACL。
		return nil, nil, nil, types.ErrPermissionDenied
	}

	if pub.Forward != nil {
		// 转发只能引用发送者有读取权限的现存消息，正文始终从数据库复制。
		if pub.Forward.SeqId <= 0 || pub.ReplyTo > 0 {
			return nil, nil, nil, types.ErrMalformed
		}
		sourceTopic := pub.Forward.Topic
		if sourceTopic == "" {
			sourceTopic = t.name
		}
		if sourceTopic == t.name {
			pud := t.perUser[asUid]
			if t.cat != types.TopicCatSys && !(pud.modeWant & pud.modeGiven).IsReader() {
				return nil, nil, nil, types.ErrPermissionDenied
			}
		} else {
			sub, subErr := store.Subs.Get(sourceTopic, asUid, false)
			if subErr != nil {
				return nil, nil, nil, subErr
			}
			if sub == nil || !(sub.ModeWant & sub.ModeGiven).IsReader() {
				return nil, nil, nil, types.ErrPermissionDenied
			}
		}
		source, err := store.Messages.Get(sourceTopic, pub.Forward.SeqId)
		if err != nil {
			return nil, nil, nil, err
		}
		if source == nil {
			return nil, nil, nil, types.ErrNotFound
		}
		content = source.Content
		head = stripServerMessageHead(source.Head)
		if head == nil {
			head = map[string]any{}
		}
		displayTopic := sourceTopic
		if types.GetTopicCat(sourceTopic) == types.TopicCatP2P {
			displayTopic = ""
		}
		head[headForwarded] = encodeHeadValue(jsonForward{
			Topic:     displayTopic,
			SeqId:     source.SeqId,
			From:      types.ParseUid(source.From).UserId(),
			Timestamp: source.CreatedAt,
		})
	}

	info, err := validateMessageContent(pub.Kind, content)
	if err != nil {
		return nil, nil, nil, types.ErrMalformed
	}
	if head == nil {
		head = map[string]any{}
	}
	head[headMessageKind] = info.Kind

	if pub.ReplyTo > 0 {
		// 回复目标必须存在且发送者必须能够读取当前 Topic。
		pud := t.perUser[asUid]
		if t.cat != types.TopicCatSys && !(pud.modeWant & pud.modeGiven).IsReader() {
			return nil, nil, nil, types.ErrPermissionDenied
		}
		replied, err := store.Messages.Get(t.name, pub.ReplyTo)
		if err != nil {
			return nil, nil, nil, err
		}
		if replied == nil {
			return nil, nil, nil, types.ErrNotFound
		}
		head[headReplyTo] = pub.ReplyTo
	} else if pub.ReplyTo < 0 {
		return nil, nil, nil, types.ErrMalformed
	}

	if pub.GroupId != "" {
		// 相册只接受图片或视频，并限制为连续的最多十条消息。
		if len(pub.GroupId) > 64 || !utf8.ValidString(pub.GroupId) ||
			(info.Kind != "image" && info.Kind != "video") {
			return nil, nil, nil, types.ErrMalformed
		}
		// 将客户端相册 ID 放入发送者命名空间，防止不同用户合并到同一相册。
		groupID := types.MessageClientKey(asUid, pub.GroupId)
		groupCount := 0
		for seq := t.lastID; seq > 0 && groupCount < 10; seq-- {
			previous, getErr := store.Messages.Get(t.name, seq)
			if getErr != nil {
				return nil, nil, nil, getErr
			}
			if previous == nil {
				continue
			}
			group, _ := previous.Head[headGroupID].(string)
			if group != groupID {
				break
			}
			groupCount++
		}
		if groupCount >= 10 {
			return nil, nil, nil, types.ErrPolicy
		}
		head[headGroupID] = groupID
	}
	attachments, err := verifiedAttachmentURLs(info.Attachments, msg.Extra)
	if err != nil {
		return nil, nil, nil, err
	}
	return head, content, attachments, nil
}

// scheduleMessage 将通过校验的消息写入持久化定时队列。
// 定时消息在真正投递时才获取 Topic SeqId，因此不会制造同步游标空洞。
func (t *Topic) scheduleMessage(msg *ClientComMessage, asUid types.Uid, head map[string]any, content any, attachments []string) error {
	publishAt := msg.Pub.ScheduleAt
	if publishAt == nil || !publishAt.After(msg.Timestamp) ||
		publishAt.After(msg.Timestamp.Add(maxScheduleAhead)) {
		return types.ErrMalformed
	}
	if msg.Pub.ClientId == "" {
		// 持久化定时投递必须提供调用方稳定的幂等键，语义等同 Telegram random_id。
		return types.ErrMalformed
	}
	pud := t.perUser[asUid]
	if t.cat != types.TopicCatSys &&
		(pud.isChan || !(pud.modeWant & pud.modeGiven).IsWriter()) {
		return types.ErrPermissionDenied
	}
	if msg.Pub.ClientId != "" {
		// 先检查已经投递和仍在排队的记录，重试只返回原结果，不重复创建消息。
		if delivered, err := store.Messages.GetByClientId(t.name, asUid, msg.Pub.ClientId); err != nil {
			return err
		} else if delivered != nil {
			if msg.Id != "" && msg.sess != nil {
				msg.sess.queueOut(NoErrDeliveredParams(msg.Id, t.original(asUid), msg.Timestamp,
					map[string]any{
						"seq": delivered.SeqId, "cid": delivered.ClientId, "duplicate": true,
					}))
			}
			return nil
		}
		if pending, err := store.Messages.GetScheduledByClientId(t.name, asUid, msg.Pub.ClientId); err != nil {
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

// editMessage 原地替换指定消息的正文和客户端自定义头，并广播编辑后的完整消息。
// 普通成员只能编辑自己的消息，管理员可编辑 Topic 内任意消息。
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
	mode := t.perUser[asUid].modeGiven & t.perUser[asUid].modeWant
	if t.perUser[asUid].isChan ||
		(!mode.IsAdmin() && (types.ParseUid(target.From) != asUid || !mode.IsWriter())) {
		return types.ErrPermissionDenied
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

	// 保留原消息的回复、转发、反应等服务端元数据，仅合并允许客户端修改的头。
	head := cloneHead(target.Head)
	if head == nil {
		head = map[string]any{}
	}
	for key, value := range stripServerMessageHead(pub.Head) {
		head[key] = value
	}
	head[headMessageKind] = info.Kind
	now := types.TimeNow()
	head[headEditedAt] = now.Format(time.RFC3339Nano)
	target.Head = head
	target.Content = pub.Content
	target.UpdatedAt = now
	if err = store.Messages.Update(target); err != nil {
		return err
	}
	if store.Files != nil && (len(attachments) > 0 || hadAttachmentRefs) {
		// 编辑可能增加或移除附件，重新建立消息与文件的完整关联集合。
		if err = store.Files.LinkAttachments("", target.Uid(), attachments); err != nil {
			logs.Warn.Printf("topic[%s]: failed to link edited message attachments: %v", t.name, err)
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

// reactToMessage 幂等地添加或移除当前用户对一条消息的反应。
func (t *Topic) reactToMessage(msg *ClientComMessage, asUid types.Uid) error {
	reaction := strings.TrimSpace(msg.Note.Reaction)
	if msg.Note.SeqId <= 0 || reaction == "" || len(reaction) > maxReactionLength || !utf8.ValidString(reaction) {
		return types.ErrMalformed
	}
	target, err := store.Messages.Get(t.name, msg.Note.SeqId)
	if err != nil {
		return err
	}
	if target == nil {
		return types.ErrNotFound
	}

	uid := asUid.UserId()
	reactions := messageReactions(target.Head)
	index := -1
	for i := range reactions {
		if reactions[i].Reaction == reaction {
			index = i
			break
		}
	}
	changed := false
	if index < 0 && !msg.Note.Remove {
		// 限制单条消息的反应种类，避免消息头无限增长。
		if len(reactions) >= maxReactionKinds {
			return types.ErrPolicy
		}
		reactions = append(reactions, storedReaction{Reaction: reaction, Users: []string{uid}})
		changed = true
	} else if index >= 0 {
		userAt := -1
		for i, user := range reactions[index].Users {
			if user == uid {
				userAt = i
				break
			}
		}
		if msg.Note.Remove && userAt >= 0 {
			reactions[index].Users = append(reactions[index].Users[:userAt], reactions[index].Users[userAt+1:]...)
			changed = true
		} else if !msg.Note.Remove && userAt < 0 {
			reactions[index].Users = append(reactions[index].Users, uid)
			changed = true
		}
		if len(reactions[index].Users) == 0 {
			reactions = append(reactions[:index], reactions[index+1:]...)
		}
	}

	if changed {
		if target.Head == nil {
			target.Head = types.KVMap{}
		}
		target.Head[headReactions] = encodeHeadValue(reactions)
		target.UpdatedAt = types.TimeNow()
		if err = store.Messages.Update(target); err != nil {
			return err
		}
	}
	if msg.Id != "" && msg.sess != nil {
		msg.sess.queueOut(NoErrParamsReply(msg, msg.Timestamp, map[string]any{
			"what": "react", "seq": msg.Note.SeqId, "reaction": reaction, "duplicate": !changed,
		}))
	}
	if changed {
		t.broadcastMessageInfo(msg, "react")
	}
	return nil
}

// topicPins 从 Topic.Aux 中兼容解析置顶消息列表。
func topicPins(aux map[string]any) []int {
	var pins []int
	switch raw := aux[topicPinsKey].(type) {
	case []int:
		return append(pins, raw...)
	case []any:
		for _, value := range raw {
			if seq := intHeadValue(value); seq > 0 {
				pins = append(pins, seq)
			}
		}
	default:
		_ = decodeHeadValue(raw, &pins)
	}
	return pins
}

// pinMessage 幂等地置顶或取消置顶消息，并把最新列表持久化到 Topic.Aux。
func (t *Topic) pinMessage(msg *ClientComMessage, asUid types.Uid) error {
	pud := t.perUser[asUid]
	mode := pud.modeGiven & pud.modeWant
	if pud.isChan || (!mode.IsAdmin() && t.cat != types.TopicCatP2P) {
		return types.ErrPermissionDenied
	}
	if msg.Note.SeqId <= 0 {
		return types.ErrMalformed
	}
	target, err := store.Messages.Get(t.name, msg.Note.SeqId)
	if err != nil {
		return err
	}
	if target == nil {
		return types.ErrNotFound
	}

	pins := topicPins(t.aux)
	found := -1
	for i, seq := range pins {
		if seq == msg.Note.SeqId {
			found = i
			break
		}
	}
	changed := false
	if msg.Note.Remove && found >= 0 {
		pins = append(pins[:found], pins[found+1:]...)
		changed = true
	} else if !msg.Note.Remove && found < 0 {
		// 新置顶排在最前，并设置上限避免 Topic 元数据无限增长。
		pins = append([]int{msg.Note.SeqId}, pins...)
		if len(pins) > maxPinnedMessages {
			pins = pins[:maxPinnedMessages]
		}
		changed = true
	}
	if changed {
		aux := copyMap(t.aux)
		if aux == nil {
			aux = map[string]any{}
		}
		aux[topicPinsKey] = pins
		now := types.TimeNow()
		if err = store.Topics.Update(t.name, map[string]any{"Aux": aux, "UpdatedAt": now}); err != nil {
			return err
		}
		t.aux = aux
	}
	if msg.Id != "" && msg.sess != nil {
		msg.sess.queueOut(NoErrParamsReply(msg, msg.Timestamp, map[string]any{
			"what": "pin", "seq": msg.Note.SeqId, "remove": msg.Note.Remove, "duplicate": !changed,
		}))
	}
	if changed {
		t.broadcastMessageInfo(msg, "pin")
	}
	return nil
}

// broadcastMessageInfo 向 Topic 中的其他在线会话广播反应或置顶状态变化。
func (t *Topic) broadcastMessageInfo(msg *ClientComMessage, what string) {
	skip := ""
	if msg.sess != nil {
		skip = msg.sess.sid
	}
	t.broadcastToSessions(&ServerComMessage{
		Info: &MsgServerInfo{
			Topic:    msg.Original,
			From:     msg.AsUser,
			What:     what,
			SeqId:    msg.Note.SeqId,
			Reaction: msg.Note.Reaction,
			Remove:   msg.Note.Remove,
		},
		RcptTo:    msg.RcptTo,
		AsUser:    msg.AsUser,
		Timestamp: msg.Timestamp,
		SkipSid:   skip,
		sess:      msg.sess,
	})
}
