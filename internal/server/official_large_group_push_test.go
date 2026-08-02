package server

import (
	"testing"

	"chat/server/store/types"
)

func TestOfficialLargeGroupPushUsesProviderTopic(t *testing.T) {
	sender := types.ParseUserId("usrSender")
	member := types.ParseUserId("usrMember")
	topic := &Topic{
		name: "grpOfficialPush",
		cat:  types.TopicCatGrp,
		official: &officialTopicPolicy{
			Official: true, OfficialStatus: "verified", ScaleClass: "large",
		},
		perUser: map[types.Uid]perUserData{
			sender: {modeWant: types.ModeCP2P, modeGiven: types.ModeCP2P, online: 1},
			member: {modeWant: types.ModeCP2P, modeGiven: types.ModeCP2P},
		},
	}
	receipt := topic.pushForData(sender, &MsgServerData{
		From: sender.UserId(), Head: map[string]any{"webrtc": "started", "aonly": true},
		Content: "incoming call",
	}, false)
	if receipt == nil {
		t.Fatal("expected push receipt")
	}
	if receipt.Channel != topic.name {
		t.Fatalf("push topic: want %s, got %s", topic.name, receipt.Channel)
	}
	if len(receipt.To) != 0 {
		t.Fatalf("official large group push must not enumerate hot members: %#v", receipt.To)
	}
	if receipt.Payload.Webrtc != "started" || !receipt.Payload.AudioOnly {
		t.Fatalf("call metadata was lost: %#v", receipt.Payload)
	}
}
