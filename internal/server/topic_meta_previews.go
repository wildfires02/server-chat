package server

import (
	"errors"
	"strings"

	"chat/server/store"
	"chat/server/store/types"
)

const maxTopicPreviewCount = 60

// replyGetPreviews resolves a conversation list to internal topics and loads all previews
// with one storage query. Unauthorized or non-readable topics are silently omitted.
func (t *Topic) replyGetPreviews(sess *Session, asUid types.Uid, msg *ClientComMessage) error {
	now := types.TimeNow()
	if t.cat != types.TopicCatMe || asUid.IsZero() || msg.Get == nil || msg.Get.Previews == nil {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return types.ErrPermissionDenied
	}
	requested := msg.Get.Previews.Topics
	if len(requested) == 0 || len(requested) > maxTopicPreviewCount {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("preview topic count must be between 1 and 60")
	}

	wanted := make(map[string]struct{}, len(requested))
	for _, topic := range requested {
		topic = strings.TrimSpace(topic)
		if len(topic) < 3 {
			sess.queueOut(ErrMalformedReply(msg, now))
			return errors.New("invalid preview topic")
		}
		wanted[topic] = struct{}{}
	}

	subs, err := store.Users.GetSubs(asUid)
	if err != nil {
		sess.queueOut(ErrUnknownReply(msg, now))
		return err
	}
	// A user may have both grp and chn views backed by the same message stream.
	externalByInternal := make(map[string][]string, len(wanted))
	internalTopics := make([]string, 0, len(wanted))
	seenInternal := make(map[string]struct{}, len(wanted))
	for i := range subs {
		sub := &subs[i]
		if !(sub.ModeWant & sub.ModeGiven).IsReader() || len(sub.Topic) < 3 {
			continue
		}
		internal, external := sub.Topic, sub.Topic
		switch types.GetTopicCat(sub.Topic) {
		case types.TopicCatP2P:
			external, err = types.P2PNameForUser(asUid, sub.Topic)
			if err != nil {
				continue
			}
		case types.TopicCatGrp:
			if types.IsChannel(sub.Topic) {
				internal = types.ChnToGrp(sub.Topic)
			}
		case types.TopicCatSlf:
			external = "slf"
		default:
			continue
		}
		if _, ok := wanted[external]; !ok || internal == "" {
			continue
		}
		externalByInternal[internal] = append(externalByInternal[internal], external)
		if _, ok := seenInternal[internal]; !ok {
			seenInternal[internal] = struct{}{}
			internalTopics = append(internalTopics, internal)
		}
	}

	messages, err := store.Messages.GetLatest(internalTopics, asUid)
	if err != nil {
		sess.queueOut(ErrUnknownReply(msg, now))
		return err
	}
	previews := make([]*MsgServerData, 0, len(messages))
	startTranslations := make([]func(), 0)
	for i := range messages {
		stored := &messages[i]
		for _, external := range externalByInternal[stored.Topic] {
			from := types.ParseUid(stored.From).UserId()
			if types.IsChannel(external) {
				from = ""
			}
			data := serverDataFromStored(external, from, stored)
			if types.GetTopicCat(stored.Topic) == types.TopicCatP2P && globals.translation != nil {
				var start func()
				data, start = globals.translation.projectHistoricalData(stored.Topic, data, sess, asUid)
				if start != nil {
					startTranslations = append(startTranslations, start)
				}
			}
			previews = append(previews, data)
		}
	}
	sess.queueOut(&ServerComMessage{Meta: &MsgServerMeta{
		Id: msg.Id, Topic: msg.Original, Timestamp: &now, Previews: previews,
	}})
	for _, start := range startTranslations {
		start()
	}
	return nil
}
