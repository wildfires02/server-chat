package server

import (
	"net/http"
	"testing"
	"time"

	"chat/server/store/types"
	"go.uber.org/mock/gomock"
)

func TestTopicInviteTokenValidation(t *testing.T) {
	previousSalt := globals.apiKeySalt
	globals.apiKeySalt = []byte("invite-test-signing-key")
	t.Cleanup(func() {
		globals.apiKeySalt = previousSalt
	})

	now := time.Unix(1_700_000_000, 0)
	token, err := issueTopicInvite("grpInviteTest", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("issueTopicInvite failed: %v", err)
	}

	if !validateTopicInvite(token, "grpInviteTest", now) {
		t.Fatal("expected token to validate for its topic before expiry")
	}
	if validateTopicInvite(token, "grpOther", now) {
		t.Fatal("a token must not validate for another topic")
	}
	if validateTopicInvite(token, "grpInviteTest", now.Add(time.Hour)) {
		t.Fatal("an expired token must not validate")
	}

	tampered := token[:len(token)-1] + "x"
	if validateTopicInvite(tampered, "grpInviteTest", now) {
		t.Fatal("a modified token must not validate")
	}
}

func TestTopicInviteExpiryBounds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if actual := topicInviteExpiry(nil, now); !actual.Equal(now.Add(defaultTopicInviteTTL)) {
		t.Fatalf("default expiry = %v, want %v", actual, now.Add(defaultTopicInviteTTL))
	}
	if actual := topicInviteExpiry(&MsgSetInvite{ExpiresIn: int64(maxTopicInviteTTL/time.Second) * 2}, now); !actual.Equal(now.Add(maxTopicInviteTTL)) {
		t.Fatalf("clamped expiry = %v, want %v", actual, now.Add(maxTopicInviteTTL))
	}
}

func TestInvitationJoinsPrivateGroup(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpInviteTest", true)
	defer helper.tearDown()

	previousSalt := globals.apiKeySalt
	globals.apiKeySalt = []byte("invite-test-signing-key")
	t.Cleanup(func() {
		globals.apiKeySalt = previousSalt
	})

	//删除默认的加入权限：只有邀请才能授予访问权限。
	helper.topic.accessAuth = types.ModeRead
	helper.topic.accessAnon = types.ModeNone

	guest := types.Uid(10_001)
	session, results := helper.newSession("invite-guest", guest)
	helper.sessions = append(helper.sessions, session)
	helper.results = append(helper.results, results)
	helper.ss.EXPECT().Get("grpInviteTest", guest, true).Return(nil, nil)
	helper.ss.EXPECT().Create(gomock.Any()).DoAndReturn(func(subs ...*types.Subscription) error {
		if len(subs) != 1 {
			t.Fatalf("created subscriptions = %d, want 1", len(subs))
		}
		mode := subs[0].ModeWant & subs[0].ModeGiven
		if !mode.IsJoiner() || !mode.IsReader() || !mode.IsWriter() {
			t.Fatalf("invited member access = %s, want ordinary group member access", mode)
		}
		return nil
	})

	token, err := issueTopicInvite("grpInviteTest", types.TimeNow().Add(time.Hour))
	if err != nil {
		t.Fatalf("issueTopicInvite failed: %v", err)
	}
	helper.topic.registerSession(&ClientComMessage{
		Original: "grpInviteTest",
		Sub: &MsgClientSub{
			Id:     "invite-join",
			Topic:  "grpInviteTest",
			Invite: token,
		},
		AsUser:  guest.UserId(),
		AuthLvl: int(1),
		sess:    session,
	})
	helper.finish()

	if len(session.subs) != 1 {
		t.Fatalf("guest session subscriptions = %d, want 1", len(session.subs))
	}
	mode := helper.topic.perUser[guest].modeWant & helper.topic.perUser[guest].modeGiven
	if !mode.IsJoiner() || !mode.IsReader() || !mode.IsWriter() {
		t.Fatalf("cached invited member access = %s", mode)
	}
	registerSessionVerifyOutputs(t, results, []int{http.StatusOK})
}

func TestInvitationRestoresLegacyRemovedMember(t *testing.T) {
	helper := TopicTestHelper{}
	helper.setUp(t, 1, types.TopicCatGrp, "grpInviteRestore", true)
	defer helper.tearDown()

	previousSalt := globals.apiKeySalt
	globals.apiKeySalt = []byte("invite-test-signing-key")
	t.Cleanup(func() {
		globals.apiKeySalt = previousSalt
	})

	guest := types.Uid(10_002)
	helper.topic.perUser[guest] = perUserData{
		modeWant:  types.ModeNone,
		modeGiven: types.ModeNone,
	}
	session, results := helper.newSession("invite-restore", guest)
	helper.sessions = append(helper.sessions, session)
	helper.results = append(helper.results, results)
	memberAccess := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
	helper.ss.EXPECT().Update("grpInviteRestore", guest, map[string]any{
		"ModeWant":  memberAccess,
		"ModeGiven": memberAccess,
	}).Return(nil)

	token, err := issueTopicInvite("grpInviteRestore", types.TimeNow().Add(time.Hour))
	if err != nil {
		t.Fatalf("issueTopicInvite failed: %v", err)
	}
	helper.topic.registerSession(&ClientComMessage{
		Original: "grpInviteRestore",
		Sub: &MsgClientSub{
			Id:     "invite-restore",
			Topic:  "grpInviteRestore",
			Invite: token,
		},
		AsUser:  guest.UserId(),
		AuthLvl: int(1),
		sess:    session,
	})
	helper.finish()

	if len(session.subs) != 1 {
		t.Fatalf("restored member session subscriptions = %d, want 1", len(session.subs))
	}
	mode := helper.topic.perUser[guest].modeWant & helper.topic.perUser[guest].modeGiven
	if !mode.IsJoiner() || !mode.IsReader() || !mode.IsWriter() {
		t.Fatalf("restored member access = %s", mode)
	}
	registerSessionVerifyOutputs(t, results, []int{http.StatusOK})
}
