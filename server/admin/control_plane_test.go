package admin

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type memoryRepository struct {
	document *Document
}

func (repo *memoryRepository) Load() (*Document, error) {
	if repo.document == nil {
		return nil, ErrNotFound
	}
	raw, _ := json.Marshal(repo.document)
	var cloned Document
	_ = json.Unmarshal(raw, &cloned)
	return &cloned, nil
}

func (repo *memoryRepository) Save(document *Document) error {
	raw, _ := json.Marshal(document)
	var cloned Document
	_ = json.Unmarshal(raw, &cloned)
	repo.document = &cloned
	return nil
}

func TestControlPlaneDefaultsAndCasbinEvaluation(t *testing.T) {
	control, err := NewControlPlane(&memoryRepository{})
	if err != nil {
		t.Fatal(err)
	}
	initial := control.Snapshot()
	if initial.Version != 1 || len(initial.Permissions) == 0 || len(initial.Roles) != 5 {
		t.Fatalf("unexpected initial snapshot: version=%d permissions=%d roles=%d: %+v",
			initial.Version, len(initial.Permissions), len(initial.Roles), initial)
	}

	role := Role{
		ID: "support_lead", Name: "客服主管",
		Permissions: []string{"moderation.mute", "official_topics.read"},
	}
	updated, err := control.UpsertRole(initial.Version, role, Actor{Subject: "test-admin"})
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{
		ID: "support-lead-1", Subject: "local:operator:7",
		RoleID: "support_lead", Domain: "channel:12",
	}
	updated, err = control.UpsertBinding(updated.Version, binding, Actor{Subject: "test-admin"})
	if err != nil {
		t.Fatal(err)
	}

	allowed, err := control.Evaluate("local:operator:7", "channel:12",
		"moderation.mute", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !allowed.Allowed || len(allowed.Roles) != 1 || allowed.Roles[0] != "support_lead" {
		t.Fatalf("expected Casbin allow, got %+v", allowed)
	}
	denied, err := control.Evaluate("local:operator:7", "channel:99",
		"moderation.mute", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if denied.Allowed {
		t.Fatalf("unexpected cross-domain allow: %+v", denied)
	}
}

func TestControlPlaneRejectsStaleAndProtectedChanges(t *testing.T) {
	control, err := NewControlPlane(&memoryRepository{})
	if err != nil {
		t.Fatal(err)
	}
	initial := control.Snapshot()
	_, err = control.UpsertRole(initial.Version+1,
		Role{ID: "custom_role", Name: "Custom", Permissions: []string{"assets.read"}}, Actor{})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	_, err = control.DeleteRole(initial.Version, "super_admin", Actor{})
	if !errors.Is(err, ErrProtected) {
		t.Fatalf("expected protected error, got %v", err)
	}
}

func TestEmployeeRoleScopesInternalWorkspacePermissions(t *testing.T) {
	control, err := NewControlPlane(&memoryRepository{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := control.Snapshot()
	snapshot, err = control.UpsertBinding(snapshot.Version, Binding{
		ID: "employee-1", Subject: "im:usrEmployee",
		RoleID: "employee", Domain: "channel:org-a",
	}, Actor{Subject: "test-admin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range []string{"workspace.pins.read", "workspace.pins.write"} {
		evaluation, err := control.Evaluate(
			"im:usrEmployee", "channel:org-a", permission, time.Now())
		if err != nil || !evaluation.Allowed {
			t.Fatalf("%s should be allowed: evaluation=%+v err=%v",
				permission, evaluation, err)
		}
	}
	crossOrg, err := control.Evaluate(
		"im:usrEmployee", "channel:org-b", "workspace.pins.read", time.Now())
	if err != nil || crossOrg.Allowed {
		t.Fatalf("cross-organization workspace access leaked: evaluation=%+v err=%v",
			crossOrg, err)
	}
	if snapshot.Version != 2 {
		t.Fatalf("unexpected binding version: %d", snapshot.Version)
	}
}

func TestControlPlaneAuditAndSettingsValidation(t *testing.T) {
	control, err := NewControlPlane(&memoryRepository{})
	if err != nil {
		t.Fatal(err)
	}
	initial := control.Snapshot()
	settings := initial.Settings
	settings.General.ProductName = "Global IM Operations"
	updated, err := control.UpdateSettings(initial.Version, settings,
		Actor{Subject: "tester", RequestID: "request-1", RemoteIP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	events := control.Audit(10)
	if len(events) != 1 || events[0].Version != updated.Version ||
		events[0].RequestID != "request-1" {
		t.Fatalf("unexpected audit events: %+v", events)
	}
	settings = updated.Settings
	settings.Topics.MemberListPageSize = 1
	if _, err = control.UpdateSettings(updated.Version, settings, Actor{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid settings, got %v", err)
	}
}

func TestControlPlaneOfficialTopicAndAudit(t *testing.T) {
	control, err := NewControlPlane(&memoryRepository{})
	if err != nil {
		t.Fatal(err)
	}
	initial := control.Snapshot()
	record := OfficialTopic{
		Topic: "grpOfficial01", OrganizationID: "org-main", Owner: "usrOwner01",
		Official: true, OfficialStatus: "verified", ScaleClass: "normal",
		MemberLimit: 0, JoinPolicy: "open", AdminAssignPolicy: "platform",
		DirectMessagePolicy: "disabled", CreatedBy: "bootstrap-admin",
	}
	created, err := control.UpsertOfficialTopic(initial.Version, record,
		Actor{Subject: "bootstrap-admin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.OfficialTopics) != 1 ||
		created.OfficialTopics[0].Owner != "usrOwner01" {
		t.Fatalf("官方频道快照不正确：%+v", created.OfficialTopics)
	}

	updated, err := control.RecordOfficialAction(created.Version,
		Actor{Subject: "bootstrap-admin"}, record.Topic, "official_topic.role.assign",
		"usrPublisher01", map[string]any{"role": "publisher"})
	if err != nil {
		t.Fatal(err)
	}
	audit := control.OfficialTopicAudit(record.Topic, 10)
	if updated.Version != 3 || len(audit) != 2 ||
		audit[0].Detail["role"] != "publisher" {
		t.Fatalf("官方频道审计不完整：%+v", audit)
	}

	record.Owner = "usrOtherOwner"
	record.OfficialStatus = "suspended"
	updated, err = control.UpsertOfficialTopic(updated.Version, record,
		Actor{Subject: "bootstrap-admin"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := control.OfficialTopic(record.Topic)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Owner != "usrOwner01" || stored.OfficialStatus != "suspended" {
		t.Fatalf("官方频道不可变字段或状态不正确：%+v", stored)
	}
}
