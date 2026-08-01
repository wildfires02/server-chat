package server

import (
	"errors"
	"sort"
	"time"

	"chat/server/store"
	"chat/server/store/types"
)

const (
	messageReadersRetention  = 7 * 24 * time.Hour
	messageReadersMaxMembers = 100
)

//replyGetReaders返回已阅读符合条件的出站群组消息的成员。
func (t *Topic) replyGetReaders(sess *Session, asUid types.Uid, asChan bool, msg *ClientComMessage) error {
	now := types.TimeNow()
	if msg.Get.Readers == nil || msg.Get.Readers.SeqId <= 0 {
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("invalid message readers query")
	}
	if t.cat != types.TopicCatGrp || t.isChan || asChan {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("message readers are available for groups only")
	}
	if t.subCnt > messageReadersMaxMembers || len(t.perUser) > messageReadersMaxMembers {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("group is too large for message readers")
	}

	requester, ok := t.perUser[asUid]
	if !ok || !(requester.modeGiven & requester.modeWant).IsReader() {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("message readers requested by non-reader")
	}

	seqID := msg.Get.Readers.SeqId
	stored, err := store.Messages.Get(t.name, seqID)
	if err != nil {
		sess.queueOut(ErrUnknownReply(msg, now))
		return err
	}
	if stored == nil {
		sess.queueOut(ErrNotFoundReply(msg, now))
		return types.ErrNotFound
	}
	if types.ParseUid(stored.From) != asUid {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return errors.New("message readers are available to the sender only")
	}
	if stored.CreatedAt.Before(now.Add(-messageReadersRetention)) {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("message readers have expired")
	}

	readers := make([]MsgReadParticipant, 0, len(t.perUser)-1)
	for uid, subscriber := range t.perUser {
		if uid == asUid || subscriber.readID < seqID ||
			!(subscriber.modeGiven & subscriber.modeWant).IsReader() {
			continue
		}
		participant := MsgReadParticipant{User: uid.UserId()}
		if readAt, found := subscriber.readHistory.TimeFor(seqID); found {
			readAtCopy := readAt
			participant.Date = &readAtCopy
		}
		readers = append(readers, participant)
	}
	sort.Slice(readers, func(i, j int) bool {
		left, right := readers[i].Date, readers[j].Date
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		if left.Equal(*right) {
			return readers[i].User < readers[j].User
		}
		return left.After(*right)
	})

	sess.queueOut(&ServerComMessage{Meta: &MsgServerMeta{
		Id:        msg.Id,
		Topic:     t.original(asUid),
		Timestamp: &now,
		Readers: &MsgReadParticipants{
			SeqId: seqID,
			Users: readers,
		},
	}})
	return nil
}
