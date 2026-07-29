// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"chat/server/drafty"
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

// prepareForwardedPublication 加载并校验被转发的源消息。
func (t *Topic) prepareForwardedPublication(
	pub *MsgClientPub,
	asUid types.Uid,
) (map[string]any, any, error) {
	if pub.Forward.SeqId <= 0 || pub.ReplyTo > 0 {
		return nil, nil, types.ErrMalformed
	}

	sourceTopic := pub.Forward.Topic
	if sourceTopic == "" {
		sourceTopic = t.name
	}
	if sourceTopic == t.name {
		userData := t.perUser[asUid]
		if t.cat != types.TopicCatSys &&
			!(userData.modeWant & userData.modeGiven).IsReader() {
			return nil, nil, types.ErrPermissionDenied
		}
	} else {
		subscription, err := store.Subs.Get(sourceTopic, asUid, false)
		if err != nil {
			return nil, nil, err
		}
		if subscription == nil ||
			!(subscription.ModeWant & subscription.ModeGiven).IsReader() {
			return nil, nil, types.ErrPermissionDenied
		}
	}

	source, err := store.Messages.Get(sourceTopic, pub.Forward.SeqId)
	if err != nil {
		return nil, nil, err
	}
	if source == nil {
		return nil, nil, types.ErrNotFound
	}

	head := stripServerMessageHead(source.Head)
	if head == nil {
		head = map[string]any{}
	}
	displayTopic := sourceTopic
	if types.GetTopicCat(sourceTopic) == types.TopicCatP2P {
		// P2P 内部 Topic 名称不应暴露给客户端。
		displayTopic = ""
	}
	head[headForwarded] = encodeHeadValue(jsonForward{
		Topic:     displayTopic,
		SeqId:     source.SeqId,
		From:      types.ParseUid(source.From).UserId(),
		Timestamp: source.CreatedAt,
	})
	return head, source.Content, nil
}

// validateReplyTarget 校验回复目标存在且发送者具有当前 Topic 的读取权限。
func (t *Topic) validateReplyTarget(replyTo int, asUid types.Uid) error {
	if replyTo < 0 {
		return types.ErrMalformed
	}
	if replyTo == 0 {
		return nil
	}

	userData := t.perUser[asUid]
	if t.cat != types.TopicCatSys &&
		!(userData.modeWant & userData.modeGiven).IsReader() {
		return types.ErrPermissionDenied
	}
	replied, err := store.Messages.Get(t.name, replyTo)
	if err != nil {
		return err
	}
	if replied == nil {
		return types.ErrNotFound
	}
	return nil
}

// prepareMessageGroup 校验相册并返回发送者命名空间内的相册 ID。
func (t *Topic) prepareMessageGroup(
	clientGroupID string,
	kind string,
	asUid types.Uid,
) (string, error) {
	if clientGroupID == "" {
		return "", nil
	}
	if len(clientGroupID) > 64 || !utf8.ValidString(clientGroupID) ||
		(kind != "image" && kind != "video") {
		return "", types.ErrMalformed
	}

	groupID := types.MessageClientKey(asUid, clientGroupID)
	groupCount := 0
	for seq := t.lastID; seq > 0 && groupCount < 10; seq-- {
		previous, err := store.Messages.Get(t.name, seq)
		if err != nil {
			return "", err
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
		return "", types.ErrPolicy
	}
	return groupID, nil
}

// prepareMessagePublication 校验发布参数，并把回复、转发、相册等客户端参数
// 转换为服务端管理的消息头。返回值可直接交给普通发布或定时队列持久化。
func (t *Topic) prepareMessagePublication(
	msg *ClientComMessage,
	asUid types.Uid,
) (map[string]any, any, []string, error) {
	pub := msg.Pub
	if t.cat == types.TopicCatGrp && t.perUser[asUid].isChan {
		// 广播频道订阅读者始终只读，不能通过伪造 grp 地址绕过 ACL。
		return nil, nil, nil, types.ErrPermissionDenied
	}

	head := stripServerMessageHead(pub.Head)
	content := pub.Content
	var err error
	if pub.Forward != nil {
		head, content, err = t.prepareForwardedPublication(pub, asUid)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	info, err := validateMessageContent(pub.Kind, content)
	if err != nil {
		return nil, nil, nil, types.ErrMalformed
	}
	if head == nil {
		head = map[string]any{}
	}
	head[headMessageKind] = info.Kind

	if err = t.validateReplyTarget(pub.ReplyTo, asUid); err != nil {
		return nil, nil, nil, err
	}
	if pub.ReplyTo > 0 {
		head[headReplyTo] = pub.ReplyTo
	}

	groupID, err := t.prepareMessageGroup(pub.GroupId, info.Kind, asUid)
	if err != nil {
		return nil, nil, nil, err
	}
	if groupID != "" {
		head[headGroupID] = groupID
	}

	attachments, err := verifiedAttachmentURLs(info.Attachments, msg.Extra)
	if err != nil {
		return nil, nil, nil, err
	}
	return head, content, attachments, nil
}
