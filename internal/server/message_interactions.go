// Package server 实现消息反应与置顶等交互能力。
package server

import (
	"strings"
	"unicode/utf8"

	"chat/server/store"
	"chat/server/store/types"
)

// reactToMessage 幂等地添加或移除当前用户对一条消息的反应。
func (t *Topic) reactToMessage(msg *ClientComMessage, asUid types.Uid) error {
	reaction := strings.TrimSpace(msg.Note.Reaction)
	if msg.Note.SeqId <= 0 || reaction == "" ||
		len(reaction) > maxReactionLength || !utf8.ValidString(reaction) {
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
	reactionIndex := findReaction(reactions, reaction)
	changed := false
	if reactionIndex < 0 && !msg.Note.Remove {
		if len(reactions) >= maxReactionKinds {
			return types.ErrPolicy
		}
		reactions = append(reactions, storedReaction{
			Reaction: reaction,
			Users:    []string{uid},
		})
		changed = true
	} else if reactionIndex >= 0 {
		userIndex := findString(reactions[reactionIndex].Users, uid)
		if msg.Note.Remove && userIndex >= 0 {
			reactions[reactionIndex].Users = append(
				reactions[reactionIndex].Users[:userIndex],
				reactions[reactionIndex].Users[userIndex+1:]...,
			)
			changed = true
		} else if !msg.Note.Remove && userIndex < 0 {
			reactions[reactionIndex].Users = append(reactions[reactionIndex].Users, uid)
			changed = true
		}
		if len(reactions[reactionIndex].Users) == 0 {
			reactions = append(reactions[:reactionIndex], reactions[reactionIndex+1:]...)
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
			"what":      "react",
			"seq":       msg.Note.SeqId,
			"reaction":  reaction,
			"duplicate": !changed,
		}))
	}
	if changed {
		t.broadcastMessageInfo(msg, "react")
	}
	return nil
}

// findReaction 返回指定反应在持久化列表中的位置。
func findReaction(reactions []storedReaction, target string) int {
	for index := range reactions {
		if reactions[index].Reaction == target {
			return index
		}
	}
	return -1
}

// findString 返回字符串在切片中的位置。
func findString(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
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

// pinMessage 幂等地置顶或取消置顶消息，并持久化最新列表。
func (t *Topic) pinMessage(msg *ClientComMessage, asUid types.Uid) error {
	userData := t.perUser[asUid]
	mode := userData.modeGiven & userData.modeWant
	if userData.isChan || (!mode.IsAdmin() && t.cat != types.TopicCatP2P) {
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
	for index, seq := range pins {
		if seq == msg.Note.SeqId {
			found = index
			break
		}
	}
	changed := false
	if msg.Note.Remove && found >= 0 {
		pins = append(pins[:found], pins[found+1:]...)
		changed = true
	} else if !msg.Note.Remove && found < 0 {
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
		if err = store.Topics.Update(t.name, map[string]any{
			"Aux":       aux,
			"UpdatedAt": now,
		}); err != nil {
			return err
		}
		t.aux = aux
	}
	if msg.Id != "" && msg.sess != nil {
		msg.sess.queueOut(NoErrParamsReply(msg, msg.Timestamp, map[string]any{
			"what":      "pin",
			"seq":       msg.Note.SeqId,
			"remove":    msg.Note.Remove,
			"duplicate": !changed,
		}))
	}
	if changed {
		t.broadcastMessageInfo(msg, "pin")
	}
	return nil
}

// broadcastMessageInfo 向其他在线会话广播反应或置顶变化。
func (t *Topic) broadcastMessageInfo(msg *ClientComMessage, what string) {
	skipSession := ""
	if msg.sess != nil {
		skipSession = msg.sess.sid
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
		SkipSid:   skipSession,
		sess:      msg.sess,
	})
}
