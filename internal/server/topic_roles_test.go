// Package server 实现群组角色与广播频道成员管理的回归测试。
package server

import (
	"net/http"
	"testing"

	"chat/server/store/types"
	"go.uber.org/mock/gomock"
)

// TestSetAnotherUserRoleInvitesOfflineChannelSubscriber 验证管理员可直接邀请离线频道读者。
func TestSetAnotherUserRoleInvitesOfflineChannelSubscriber(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()
	helper.topic.isChan = true
	helper.topic.subCnt = 1

	target := types.Uid(100)
	helper.ss.EXPECT().Get("grpTest", target, false).Return(nil, nil)
	helper.ss.EXPECT().Get("chnTest", target, false).Return(nil, nil)
	helper.uu.EXPECT().Get(target).Return(&types.User{State: types.StateOK}, nil)
	helper.ss.EXPECT().Create(gomock.Any()).DoAndReturn(func(subs ...*types.Subscription) error {
		if len(subs) != 1 || subs[0].Topic != "chnTest" ||
			subs[0].ModeGiven != types.ModeCChnReader ||
			subs[0].ModeWant != types.ModeCChnReader {
			t.Fatalf("unexpected channel subscription: %#v", subs)
		}
		return nil
	})

	msg := &ClientComMessage{
		Id:       "role-1",
		Original: "grpTest",
		AsUser:   helper.uids[0].UserId(),
		Set: &MsgClientSet{
			Id:    "role-1",
			Topic: "grpTest",
			MsgSetQuery: MsgSetQuery{Sub: &MsgSetSub{
				User: target.UserId(),
				Role: "subscriber",
			}},
		},
		sess: helper.sessions[0],
	}
	if err := helper.topic.replySetSub(helper.sessions[0], msg, false); err != nil {
		t.Fatalf("set subscriber role failed: %v", err)
	}
	helper.finish()

	if helper.topic.subCnt != 2 {
		t.Fatalf("subscriber count: want 2, got %d", helper.topic.subCnt)
	}
	if _, cached := helper.topic.perUser[target]; cached {
		t.Fatal("offline channel subscriber must not remain in the in-memory member map")
	}
	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusOK})
}

// TestSetAnotherUserRoleMutesOrdinaryGroupMember 验证普通群角色可把成员调整为只读。
func TestSetAnotherUserRoleMutesOrdinaryGroupMember(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 2, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	target := helper.uids[1]
	memberMode := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
	targetData := helper.topic.perUser[target]
	targetData.modeWant = memberMode
	targetData.modeGiven = memberMode
	helper.topic.perUser[target] = targetData
	helper.ss.EXPECT().Update("grpTest", target, map[string]any{
		"ModeWant":  types.ModeCChnReader,
		"ModeGiven": types.ModeCChnReader,
	}).Return(nil)

	msg := &ClientComMessage{
		Id:       "mute-member",
		Original: "grpTest",
		AsUser:   helper.uids[0].UserId(),
		Set: &MsgClientSet{
			Id:    "mute-member",
			Topic: "grpTest",
			MsgSetQuery: MsgSetQuery{Sub: &MsgSetSub{
				User: target.UserId(),
				Role: "readonly",
			}},
		},
		sess: helper.sessions[0],
	}
	if err := helper.topic.replySetSub(helper.sessions[0], msg, false); err != nil {
		t.Fatalf("set readonly role failed: %v", err)
	}
	helper.finish()

	effective := helper.topic.perUser[target].modeWant & helper.topic.perUser[target].modeGiven
	if effective.IsWriter() || topicRoleFromAccess(effective, false, false) != "readonly" {
		t.Fatalf("unexpected muted member ACL: %s", effective)
	}
	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusOK})
}

// TestOnlyOwnerCanPromoteAdmin 验证普通管理员不能把其他成员提升为管理员。
func TestOnlyOwnerCanPromoteAdmin(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 2, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	actor := helper.uids[1]
	adminMode := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres | types.ModeApprove
	actorData := helper.topic.perUser[actor]
	actorData.modeWant = adminMode
	actorData.modeGiven = adminMode
	helper.topic.perUser[actor] = actorData

	msg := &ClientComMessage{
		Id:       "promote-admin",
		Original: "grpTest",
		AsUser:   actor.UserId(),
		Set: &MsgClientSet{
			Id:    "promote-admin",
			Topic: "grpTest",
			MsgSetQuery: MsgSetQuery{Sub: &MsgSetSub{
				User: types.Uid(103).UserId(),
				Role: "admin",
			}},
		},
		sess: helper.sessions[1],
	}
	if err := helper.topic.replySetSub(helper.sessions[1], msg, false); err == nil {
		t.Fatal("non-owner unexpectedly promoted an admin")
	}
	helper.finish()

	registerSessionVerifyOutputs(t, helper.results[1], []int{http.StatusForbidden})
}

// TestReplyDelSubRemovesOfflineChannelSubscriber 验证频道管理员可移除不在内存中的读者。
func TestReplyDelSubRemovesOfflineChannelSubscriber(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()
	helper.topic.isChan = true
	helper.topic.subCnt = 2

	target := types.Uid(101)
	channelSub := &types.Subscription{
		User:      target.String(),
		Topic:     "chnTest",
		ModeWant:  types.ModeCChnReader,
		ModeGiven: types.ModeCChnReader,
	}
	helper.ss.EXPECT().Get("grpTest", target, false).Return(nil, nil)
	helper.ss.EXPECT().Get("chnTest", target, false).Return(channelSub, nil)
	helper.ss.EXPECT().Delete("chnTest", target).Return(nil)

	msg := &ClientComMessage{
		Id:       "del-reader-1",
		Original: "grpTest",
		AsUser:   helper.uids[0].UserId(),
		Del: &MsgClientDel{
			Id:    "del-reader-1",
			Topic: "grpTest",
			What:  "sub",
			User:  target.UserId(),
		},
		sess: helper.sessions[0],
	}
	if err := helper.topic.replyDelSub(helper.sessions[0], helper.uids[0], msg); err != nil {
		t.Fatalf("delete channel subscriber failed: %v", err)
	}
	helper.finish()

	if helper.topic.subCnt != 1 {
		t.Fatalf("subscriber count: want 1, got %d", helper.topic.subCnt)
	}
	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusOK})
}

// TestBannedChannelSubscriberCannotRejoin 验证频道封禁记录不会在重连时被默认权限覆盖。
func TestBannedChannelSubscriberCannotRejoin(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", false)
	defer helper.tearDown()
	helper.topic.isChan = true

	uid := helper.uids[0]
	delete(helper.topic.perUser, uid)
	helper.ss.EXPECT().Get("chnTest", uid, false).Return(&types.Subscription{
		User:      uid.String(),
		Topic:     "chnTest",
		ModeWant:  types.ModeCChnReader,
		ModeGiven: types.ModeNone,
	}, nil)

	msg := &ClientComMessage{
		Original: "chnTest",
		AsUser:   uid.UserId(),
		Sub: &MsgClientSub{
			Id:    "join-banned",
			Topic: "chnTest",
		},
		sess: helper.sessions[0],
	}
	helper.topic.registerSession(msg)
	helper.finish()

	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusForbidden})
}

// TestReplyGetSubListsChannelSubscribersForAdmin 验证管理员可查询频道读者列表与派生角色。
func TestReplyGetSubListsChannelSubscribersForAdmin(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()
	helper.topic.isChan = true

	target := types.Uid(102)
	helper.tt.EXPECT().GetUsers("chnTest", gomock.Any()).Return([]types.Subscription{{
		User:      target.String(),
		Topic:     "chnTest",
		ModeWant:  types.ModeCChnReader,
		ModeGiven: types.ModeCChnReader,
	}}, nil)

	msg := &ClientComMessage{
		Id:       "list-readers",
		Original: "grpTest",
		AsUser:   helper.uids[0].UserId(),
		Get: &MsgClientGet{
			Id:    "list-readers",
			Topic: "grpTest",
			MsgGetQuery: MsgGetQuery{
				What: "sub",
				Sub:  &MsgGetOpts{Topic: "chnTest"},
			},
		},
		sess: helper.sessions[0],
	}
	if err := helper.topic.replyGetSub(helper.sessions[0], helper.uids[0], 0, false, msg); err != nil {
		t.Fatalf("list channel subscribers failed: %v", err)
	}
	helper.finish()

	if len(helper.results[0].messages) != 1 {
		t.Fatalf("expected one meta response, got %d", len(helper.results[0].messages))
	}
	response := helper.results[0].messages[0].(*ServerComMessage)
	if response.Meta == nil || len(response.Meta.Sub) != 1 {
		t.Fatalf("unexpected channel subscriber response: %#v", response)
	}
	if response.Meta.Sub[0].Acs.Role != "subscriber" {
		t.Fatalf("role: want subscriber, got %q", response.Meta.Sub[0].Acs.Role)
	}
}

// TestChannelSubscriberCannotPublishWithStaleWriteBit 验证旧数据中的 W 位不能突破频道只读边界。
func TestChannelSubscriberCannotPublishWithStaleWriteBit(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()
	helper.topic.isChan = true

	uid := helper.uids[0]
	pud := helper.topic.perUser[uid]
	pud.isChan = true
	pud.modeWant = types.ModeCFull
	pud.modeGiven = types.ModeCFull
	helper.topic.perUser[uid] = pud

	msg := &ClientComMessage{
		Id:       "reader-pub",
		Original: "chnTest",
		AsUser:   uid.UserId(),
		Pub: &MsgClientPub{
			Id:      "reader-pub",
			Topic:   "chnTest",
			Content: "must be rejected",
		},
		sess: helper.sessions[0],
	}
	helper.topic.handlePubBroadcast(msg)
	helper.finish()

	registerSessionVerifyOutputs(t, helper.results[0], []int{http.StatusForbidden})
}

// TestOrdinaryGroupMemberCanPublish 验证普通群成员角色保留双向发言能力。
func TestOrdinaryGroupMemberCanPublish(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 2, types.TopicCatGrp, "grpTest", true)
	defer helper.tearDown()

	uid := helper.uids[1]
	memberMode := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
	pud := helper.topic.perUser[uid]
	pud.modeWant = memberMode
	pud.modeGiven = memberMode
	helper.topic.perUser[uid] = pud
	helper.mm.EXPECT().Save(gomock.Any(), gomock.Any(), true).Return(nil, true)

	msg := &ClientComMessage{
		Id:       "member-pub",
		Original: "grpTest",
		AsUser:   uid.UserId(),
		Pub: &MsgClientPub{
			Id:      "member-pub",
			Topic:   "grpTest",
			Content: "ordinary group message",
		},
		sess: helper.sessions[1],
	}
	helper.topic.handlePubBroadcast(msg)
	helper.finish()

	foundAccepted := false
	for _, raw := range helper.results[1].messages {
		response := raw.(*ServerComMessage)
		if response.Ctrl != nil && response.Ctrl.Code == http.StatusAccepted {
			foundAccepted = true
		}
	}
	if !foundAccepted {
		t.Fatal("ordinary group member did not receive publish acknowledgement")
	}
}
