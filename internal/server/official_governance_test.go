package server

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/store"
	"chat/server/store/types"
)

func useOfficialGovernanceTestCache(t *testing.T) *officialManagerCache {
	t.Helper()
	previous := store.PCache
	cache := &officialManagerCache{values: make(map[string]string)}
	store.PCache = cache
	t.Cleanup(func() { store.PCache = previous })
	return cache
}

func TestManagedTopicInviteUseLimitAndRevocation(t *testing.T) {
	useOfficialGovernanceTestCache(t)
	previousSalt := globals.apiKeySalt
	globals.apiKeySalt = []byte("managed-invite-test-key")
	t.Cleanup(func() { globals.apiKeySalt = previousSalt })

	now := time.Unix(1_800_000_000, 0).UTC()
	token, invite, err := issueManagedTopicInvite(
		"grpOfficialInvite", "admin", now.Add(time.Hour), 1, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !validateTopicInvite(token, "grpOfficialInvite", now) ||
		!consumeTopicInvite(token, "grpOfficialInvite", now) {
		t.Fatal("首次使用受控邀请应成功")
	}
	if validateTopicInvite(token, "grpOfficialInvite", now) ||
		consumeTopicInvite(token, "grpOfficialInvite", now) {
		t.Fatal("达到最大使用次数后邀请必须失效")
	}

	unlimited, unlimitedState, err := issueManagedTopicInvite(
		"grpOfficialInvite", "admin", now.Add(time.Hour), 0, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = revokeTopicInvite("grpOfficialInvite", unlimitedState.ID); err != nil {
		t.Fatal(err)
	}
	if validateTopicInvite(unlimited, "grpOfficialInvite", now) {
		t.Fatal("被撤销的邀请必须立即失效")
	}
	if invite.Uses != 0 || unlimitedState.MaxUses != 0 {
		t.Fatalf("邀请初始状态异常：limited=%+v unlimited=%+v", invite, unlimitedState)
	}
}

func TestOfficialJoinRequestLifecycle(t *testing.T) {
	useOfficialGovernanceTestCache(t)
	now := time.Unix(1_800_100_000, 0).UTC()
	uid := types.Uid(7001)

	request, err := submitOfficialJoinRequest("grpApproval", uid, now)
	if err != nil || request.Status != "pending" {
		t.Fatalf("提交入群申请失败：request=%+v err=%v", request, err)
	}
	pending, err := listOfficialJoinRequests("grpApproval", "pending", 10)
	if err != nil || len(pending) != 1 || pending[0].User != uid.UserId() {
		t.Fatalf("待审批列表异常：requests=%+v err=%v", pending, err)
	}
	decided, err := decideOfficialJoinRequest(
		"grpApproval", uid, "rejected", "operator", "not eligible", now.Add(time.Minute),
	)
	if err != nil || decided.Status != "rejected" || decided.Version != 2 {
		t.Fatalf("拒绝入群申请失败：request=%+v err=%v", decided, err)
	}
	if _, err = submitOfficialJoinRequest("grpApproval", uid, now.Add(30*time.Minute)); !errors.Is(err, admincontrol.ErrProtected) {
		t.Fatalf("拒绝后一小时内重新申请应被限流，得到 %v", err)
	}
	reopened, err := submitOfficialJoinRequest("grpApproval", uid, now.Add(2*time.Hour))
	if err != nil || reopened.Status != "pending" || reopened.Version != 3 {
		t.Fatalf("冷却期后应可重新申请：request=%+v err=%v", reopened, err)
	}
}

func TestOfficialSlowModeAndAdminBypass(t *testing.T) {
	useOfficialGovernanceTestCache(t)
	now := time.Unix(1_800_200_000, 0).UTC()
	member := types.Uid(7101)
	admin := types.Uid(7102)
	memberMode := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
	adminMode := types.ModeCFull &^ types.ModeOwner
	topic := &Topic{
		name: "grpSlowMode",
		official: &officialTopicPolicy{
			Official: true, OfficialStatus: "verified", ScaleClass: "large",
			SlowModeSeconds: 30,
		},
		perUser: map[types.Uid]perUserData{
			member: {modeWant: memberMode, modeGiven: memberMode},
			admin:  {modeWant: adminMode, modeGiven: adminMode},
		},
	}
	if retry, err := topic.enforceOfficialSlowMode(member, "message", now); err != nil || retry != 0 {
		t.Fatalf("成员首次发布应成功：retry=%v err=%v", retry, err)
	}
	if retry, err := topic.enforceOfficialSlowMode(member, "message", now.Add(5*time.Second)); !errors.Is(err, errOfficialSlowMode) || retry != 25*time.Second {
		t.Fatalf("慢速模式未阻止连续发布：retry=%v err=%v", retry, err)
	}
	if retry, err := topic.enforceOfficialSlowMode(admin, "message", now); err != nil || retry != 0 {
		t.Fatalf("管理员应绕过慢速模式：retry=%v err=%v", retry, err)
	}
	if retry, err := topic.enforceOfficialSlowMode(member, "call", now); err != nil || retry != 0 {
		t.Fatalf("通话信令不应进入慢速模式：retry=%v err=%v", retry, err)
	}
}

func TestOfficialReportQuotaAndDecision(t *testing.T) {
	cache := useOfficialGovernanceTestCache(t)
	now := time.Unix(1_800_300_000, 0).UTC()
	uid := types.Uid(7201)
	for index := 0; index < 20; index++ {
		if err := consumeOfficialReportQuota("grpReports", uid, now); err != nil {
			t.Fatalf("第 %d 次举报配额不应失败：%v", index+1, err)
		}
	}
	if err := consumeOfficialReportQuota("grpReports", uid, now); !errors.Is(err, admincontrol.ErrProtected) {
		t.Fatalf("每日第 21 次举报应被限流，得到 %v", err)
	}

	report := officialMessageReport{
		ID: "report-1", Topic: "grpReports", SeqID: 99, Reporter: uid.UserId(),
		Reason: "spam", Status: "open", CreatedAt: now, Version: 1,
	}
	raw, _ := json.Marshal(report)
	cache.values[officialReportPrefix+report.Topic+":"+report.ID] = string(raw)
	decided, err := decideOfficialReport(
		report.Topic, report.ID, "resolved", "operator", "handled", now.Add(time.Minute),
	)
	if err != nil || decided.Status != "resolved" || decided.Version != 2 ||
		decided.ReviewedBy != "operator" {
		t.Fatalf("举报处置失败：report=%+v err=%v", decided, err)
	}
	if _, err = decideOfficialReport(
		report.Topic, report.ID, "dismissed", "operator", "", now.Add(2*time.Minute),
	); !errors.Is(err, admincontrol.ErrConflict) {
		t.Fatalf("已处置举报不得重复决定，得到 %v", err)
	}
}
