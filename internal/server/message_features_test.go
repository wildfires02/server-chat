// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"testing"
	"time"

	"chat/server/store/types"
	"github.com/golang/mock/gomock"
)

// TestServerDataFromStoredUsesTrustedMetadata 验证 Server Data From Stored Uses Trusted Metadata 相关行为。
func TestServerDataFromStoredUsesTrustedMetadata(t *testing.T) {
	now := types.TimeNow()
	msg := &types.Message{
		ObjHeader: types.ObjHeader{CreatedAt: now},
		SeqId:     7,
		ClientId:  "device:7",
		Head: types.KVMap{
			"mime":          "text/x-drafty",
			headMessageKind: "image",
			headReplyTo:     3,
			headGroupID:     "album-a",
			headEditedAt:    now.Format(time.RFC3339Nano),
			headForwarded: encodeHeadValue(jsonForward{
				Topic: "grpSource", SeqId: 2, From: "usrSource", Timestamp: now,
			}),
			headReactions: encodeHeadValue([]storedReaction{
				{Reaction: "👍", Users: []string{"usrA", "usrB"}},
			}),
		},
		Content: map[string]any{"txt": "caption"},
	}
	data := serverDataFromStored("grpDest", "usrSender", msg)
	if data.Kind != "image" || data.ReplyTo != 3 || data.GroupId != "album-a" {
		t.Fatalf("trusted metadata was not decoded: %#v", data)
	}
	if data.EditedAt == nil || data.Forwarded == nil || data.Forwarded.SeqId != 2 {
		t.Fatalf("edit/forward metadata was not decoded: %#v", data)
	}
	if len(data.Reactions) != 1 || data.Reactions[0].Count != 2 {
		t.Fatalf("reaction summary was not decoded: %#v", data.Reactions)
	}
	if data.Head["mime"] != "text/x-drafty" || data.Head[headReactions] != nil {
		t.Fatalf("internal metadata leaked into public head: %#v", data.Head)
	}
}

// TestEditMessageValidatesOwnerAndBroadcasts 验证 Edit Message Validates Owner And Broadcasts 相关行为。
func TestEditMessageValidatesOwnerAndBroadcasts(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 2, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	now := types.TimeNow()
	target := &types.Message{
		ObjHeader: types.ObjHeader{CreatedAt: now},
		SeqId:     4,
		Topic:     "grpTest",
		From:      helper.uids[1].String(),
		Head:      types.KVMap{headMessageKind: "text"},
		Content:   "before",
	}
	helper.mm.EXPECT().Get("grpTest", 4).Return(target, nil)
	helper.mm.EXPECT().Update(gomock.Any()).DoAndReturn(func(updated *types.Message) error {
		if updated.Content != "after" || updated.Head[headEditedAt] == nil {
			t.Fatalf("unexpected edit: %#v", updated)
		}
		return nil
	})

	msg := &ClientComMessage{
		Id:        "edit-1",
		Original:  "grpTest",
		RcptTo:    "grpTest",
		AsUser:    helper.uids[1].UserId(),
		Timestamp: now,
		Pub:       &MsgClientPub{ReplaceSeq: 4, Content: "after"},
		sess:      helper.sessions[1],
	}
	if err := helper.topic.editMessage(msg, helper.uids[1]); err != nil {
		t.Fatal(err)
	}
	helper.finish()

	if len(helper.results[0].messages) != 1 {
		t.Fatalf("peer did not receive edited message: %#v", helper.results[0].messages)
	}
	edited := helper.results[0].messages[0].(*ServerComMessage).Data
	if edited == nil || edited.SeqId != 4 || edited.EditedAt == nil || edited.Content != "after" {
		t.Fatalf("unexpected edited broadcast: %#v", edited)
	}
}

// TestReactionIsPersistedAndBroadcast 验证 Reaction Is Persisted And Broadcast 相关行为。
func TestReactionIsPersistedAndBroadcast(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 2, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	target := &types.Message{
		SeqId: 4, Topic: "grpTest", From: helper.uids[1].String(),
		Head: types.KVMap{headMessageKind: "text"}, Content: "hello",
	}
	helper.mm.EXPECT().Get("grpTest", 4).Return(target, nil)
	helper.mm.EXPECT().Update(gomock.Any()).DoAndReturn(func(updated *types.Message) error {
		reactions := messageReactions(updated.Head)
		if len(reactions) != 1 || len(reactions[0].Users) != 1 ||
			reactions[0].Users[0] != helper.uids[0].UserId() {
			t.Fatalf("reaction was not persisted: %#v", reactions)
		}
		return nil
	})
	msg := &ClientComMessage{
		Id:        "react-1",
		Original:  "grpTest",
		RcptTo:    "grpTest",
		AsUser:    helper.uids[0].UserId(),
		Timestamp: types.TimeNow(),
		Note:      &MsgClientNote{What: "react", SeqId: 4, Reaction: "👍"},
		sess:      helper.sessions[0],
	}
	if err := helper.topic.reactToMessage(msg, helper.uids[0]); err != nil {
		t.Fatal(err)
	}
	helper.finish()

	if len(helper.results[1].messages) != 1 {
		t.Fatalf("peer did not receive reaction event: %#v", helper.results[1].messages)
	}
	info := helper.results[1].messages[0].(*ServerComMessage).Info
	if info == nil || info.What != "react" || info.Reaction != "👍" || info.SeqId != 4 {
		t.Fatalf("unexpected reaction event: %#v", info)
	}
}

// TestPinIsPersistedInTopicAux 验证 Pin Is Persisted In Topic Aux 相关行为。
func TestPinIsPersistedInTopicAux(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 2, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	helper.mm.EXPECT().Get("grpTest", 4).Return(&types.Message{
		SeqId: 4, Topic: "grpTest", From: helper.uids[1].String(), Content: "hello",
	}, nil)
	helper.tt.EXPECT().Update("grpTest", gomock.Any()).DoAndReturn(func(_ string, update map[string]any) error {
		aux := update["Aux"].(map[string]any)
		pins := aux[topicPinsKey].([]int)
		if len(pins) != 1 || pins[0] != 4 {
			t.Fatalf("pin was not persisted: %#v", update)
		}
		return nil
	})
	msg := &ClientComMessage{
		Id:        "pin-1",
		Original:  "grpTest",
		RcptTo:    "grpTest",
		AsUser:    helper.uids[0].UserId(),
		Timestamp: types.TimeNow(),
		Note:      &MsgClientNote{What: "pin", SeqId: 4},
		sess:      helper.sessions[0],
	}
	if err := helper.topic.pinMessage(msg, helper.uids[0]); err != nil {
		t.Fatal(err)
	}
	helper.finish()

	if got := topicPins(helper.topic.aux); len(got) != 1 || got[0] != 4 {
		t.Fatalf("topic pin cache was not updated: %#v", helper.topic.aux)
	}
	info := helper.results[1].messages[0].(*ServerComMessage).Info
	if info == nil || info.What != "pin" || info.SeqId != 4 {
		t.Fatalf("unexpected pin broadcast: %#v", info)
	}
}

// TestScheduleMessageIsDurableAndIdempotent 验证 Schedule Message Is Durable And Idempotent 相关行为。
func TestScheduleMessageIsDurableAndIdempotent(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	now := types.TimeNow()
	helper.mm.EXPECT().GetByClientId("grpTest", helper.uids[0], "device:scheduled").Return(nil, nil)
	helper.mm.EXPECT().GetScheduledByClientId("grpTest", helper.uids[0], "device:scheduled").Return(nil, nil)
	helper.mm.EXPECT().Schedule(gomock.Any()).DoAndReturn(func(scheduled *types.ScheduledMessage) error {
		scheduled.SetUid(types.Uid(99))
		if scheduled.PublishAt.Sub(now) != time.Minute || scheduled.Topic != "grpTest" {
			t.Fatalf("unexpected scheduled message: %#v", scheduled)
		}
		return nil
	})

	msg := &ClientComMessage{
		Id:        "schedule-1",
		Original:  "grpTest",
		RcptTo:    "grpTest",
		AsUser:    helper.uids[0].UserId(),
		Timestamp: now,
		Pub: &MsgClientPub{
			ClientId: "device:scheduled",
			ScheduleAt: func() *time.Time {
				at := now.Add(time.Minute)
				return &at
			}(),
		},
		sess: helper.sessions[0],
	}
	if err := helper.topic.scheduleMessage(msg, helper.uids[0],
		map[string]any{headMessageKind: "text"}, "later", nil); err != nil {
		t.Fatal(err)
	}
	helper.finish()

	if len(helper.results[0].messages) != 1 {
		t.Fatalf("missing schedule acknowledgement: %#v", helper.results[0].messages)
	}
	ctrl := helper.results[0].messages[0].(*ServerComMessage).Ctrl
	if ctrl == nil || ctrl.Code != 202 {
		t.Fatalf("unexpected schedule acknowledgement: %#v", ctrl)
	}
}

// TestScheduleMessageRequiresClientId 验证 Schedule Message Requires Client Id 相关行为。
func TestScheduleMessageRequiresClientId(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	now := types.TimeNow()
	at := now.Add(time.Minute)
	msg := &ClientComMessage{
		Timestamp: now,
		Pub:       &MsgClientPub{ScheduleAt: &at},
	}
	if err := helper.topic.scheduleMessage(msg, helper.uids[0],
		map[string]any{headMessageKind: "text"}, "later", nil); err != types.ErrMalformed {
		t.Fatalf("schedule without cid: want malformed, got %v", err)
	}
}

// TestAlbumGroupIsNamespacedBySender 验证 Album Group Is Namespaced By Sender 相关行为。
func TestAlbumGroupIsNamespacedBySender(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	content := map[string]any{
		"fmt": []any{map[string]any{"at": float64(-1), "key": float64(0)}},
		"ent": []any{map[string]any{
			"tp": "IM",
			"data": map[string]any{
				"mime": "image/jpeg",
				"ref":  "https://cdn.example.test/image.jpg",
			},
		}},
	}
	msg := &ClientComMessage{
		Pub: &MsgClientPub{GroupId: "client-album", Content: content},
	}
	head, _, _, err := helper.topic.prepareMessagePublication(msg, helper.uids[0])
	if err != nil {
		t.Fatal(err)
	}
	group, _ := head[headGroupID].(string)
	if group == "" || group == msg.Pub.GroupId {
		t.Fatalf("album id was not namespaced: %q", group)
	}
	if group != types.MessageClientKey(helper.uids[0], msg.Pub.GroupId) {
		t.Fatalf("unexpected album id: %q", group)
	}
}
