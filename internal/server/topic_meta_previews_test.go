package server

import (
	"testing"
	"time"

	"chat/server/store"
	mockstore "chat/server/store/mock_store"
	"chat/server/store/types"
	"go.uber.org/mock/gomock"
)

func TestReplyGetPreviewsUsesOneAuthorizedBulkQuery(t *testing.T) {
	controller := gomock.NewController(t)
	users := mockstore.NewMockUsersPersistenceInterface(controller)
	messages := mockstore.NewMockMessagesPersistenceInterface(controller)
	previousUsers, previousMessages := store.Users, store.Messages
	store.Users, store.Messages = users, messages
	t.Cleanup(func() { store.Users, store.Messages = previousUsers, previousMessages })

	uid, peer := types.Uid(10), types.Uid(11)
	p2p := uid.P2PName(peer)
	mode := types.ModeJoin | types.ModeRead
	users.EXPECT().GetSubs(uid).Return([]types.Subscription{
		{User: uid.String(), Topic: p2p, ModeWant: mode, ModeGiven: mode},
		{User: uid.String(), Topic: "grpPreview", ModeWant: mode, ModeGiven: mode},
	}, nil)
	messages.EXPECT().GetLatest([]string{p2p, "grpPreview"}, uid).Return([]types.Message{
		{Topic: p2p, From: peer.String(), SeqId: 7, Content: "p2p", ObjHeader: types.ObjHeader{CreatedAt: time.Now()}},
		{Topic: "grpPreview", From: peer.String(), SeqId: 9, Content: "group", ObjHeader: types.ObjHeader{CreatedAt: time.Now()}},
	}, nil)

	session := &Session{proto: WEBSOCK, send: make(chan any, 1)}
	topic := &Topic{cat: types.TopicCatMe}
	request := &ClientComMessage{
		Id: "preview-1", Original: "me",
		Get: &MsgClientGet{MsgGetQuery: MsgGetQuery{
			Previews: &MsgPreviewQuery{Topics: []string{peer.UserId(), "grpPreview"}},
		}},
	}
	if err := topic.replyGetPreviews(session, uid, request); err != nil {
		t.Fatal(err)
	}
	queued := <-session.send
	session.releaseOutbound(queued)
	response := queued.(*ServerComMessage)
	if response.Meta == nil || len(response.Meta.Previews) != 2 ||
		response.Meta.Previews[0].Topic != peer.UserId() ||
		response.Meta.Previews[1].Topic != "grpPreview" {
		t.Fatalf("unexpected preview response: %#v", response.Meta)
	}
}
