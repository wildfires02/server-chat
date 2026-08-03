package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/store"
	"chat/server/store/types"
)

// officialManagerTestState 是官方 Topic 管理器测试使用的内存状态。
type officialManagerTestState struct {
	topics map[string]*types.Topic
	subs   map[string]*types.Subscription
	users  map[types.Uid]*types.User
}

// officialManagerTopicStore 实现 Topic 持久层测试替身。
type officialManagerTopicStore struct {
	state *officialManagerTestState
}

func (fake officialManagerTopicStore) Create(topic *types.Topic, owner types.Uid, private any) error {
	if fake.state.topics[topic.Id] != nil {
		return types.ErrDuplicate
	}
	cloned := *topic
	cloned.Owner = owner.String()
	fake.state.topics[topic.Id] = &cloned
	fake.state.subs[topic.Id+":"+owner.String()] = &types.Subscription{
		User: owner.String(), Topic: topic.Id,
		ModeWant: types.ModeCFull, ModeGiven: types.ModeCFull, Private: private,
	}
	return nil
}

func (fake officialManagerTopicStore) Get(topic string) (*types.Topic, error) {
	return fake.state.topics[topic], nil
}

func (fake officialManagerTopicStore) Update(topic string, update map[string]any) error {
	current := fake.state.topics[topic]
	if current == nil {
		return types.ErrNotFound
	}
	if aux, ok := update["Aux"].(map[string]any); ok {
		current.Aux = aux
	}
	if public, ok := update["Public"]; ok {
		current.Public = public
	}
	if access, ok := update["Access"].(types.DefaultAccess); ok {
		current.Access = access
	}
	return nil
}

func (fake officialManagerTopicStore) Delete(topic string, _ bool, _ bool) error {
	delete(fake.state.topics, topic)
	for key, sub := range fake.state.subs {
		if sub.Topic == topic || sub.Topic == types.GrpToChn(topic) {
			delete(fake.state.subs, key)
		}
	}
	return nil
}

// officialManagerSubStore 实现订阅持久层测试替身。
type officialManagerSubStore struct {
	state *officialManagerTestState
}

func (fake officialManagerSubStore) Create(subs ...*types.Subscription) error {
	for _, sub := range subs {
		key := sub.Topic + ":" + sub.User
		cloned := *sub
		cloned.DeletedAt = nil
		fake.state.subs[key] = &cloned
	}
	return nil
}

func (fake officialManagerSubStore) Get(topic string, user types.Uid,
	keepDeleted bool) (*types.Subscription, error) {
	sub := fake.state.subs[topic+":"+user.String()]
	if sub == nil || (!keepDeleted && sub.DeletedAt != nil) {
		return nil, nil
	}
	cloned := *sub
	return &cloned, nil
}

func (fake officialManagerSubStore) Update(topic string, user types.Uid, update map[string]any) error {
	sub := fake.state.subs[topic+":"+user.String()]
	if sub == nil {
		return types.ErrNotFound
	}
	if value, ok := update["ModeWant"].(types.AccessMode); ok {
		sub.ModeWant = value
	}
	if value, ok := update["ModeGiven"].(types.AccessMode); ok {
		sub.ModeGiven = value
	}
	if value, ok := update["DeletedAt"]; ok {
		if value == nil {
			sub.DeletedAt = nil
		} else if deletedAt, valid := value.(*time.Time); valid {
			sub.DeletedAt = deletedAt
		}
	}
	return nil
}

func (fake officialManagerSubStore) Delete(topic string, user types.Uid) error {
	sub := fake.state.subs[topic+":"+user.String()]
	if sub == nil {
		return types.ErrNotFound
	}
	now := types.TimeNow()
	sub.DeletedAt = &now
	return nil
}

// officialManagerUserStore 实现用户持久层测试替身。
type officialManagerUserStore struct {
	state *officialManagerTestState
}

func (fake officialManagerUserStore) Get(uid types.Uid) (*types.User, error) {
	return fake.state.users[uid], nil
}

// officialManagerCache 实现治理状态持久缓存测试替身。
type officialManagerCache struct {
	values map[string]string
}

func (cache *officialManagerCache) Get(key string) (string, error) {
	value, ok := cache.values[key]
	if !ok {
		return "", types.ErrNotFound
	}
	return value, nil
}

func (cache *officialManagerCache) Upsert(key, value string, failOnDuplicate bool) error {
	if _, exists := cache.values[key]; failOnDuplicate && exists {
		return types.ErrDuplicate
	}
	cache.values[key] = value
	return nil
}

func (cache *officialManagerCache) Delete(key string) error {
	if _, ok := cache.values[key]; !ok {
		return types.ErrNotFound
	}
	delete(cache.values, key)
	return nil
}

func (cache *officialManagerCache) Expire(string, time.Time) error {
	return nil
}

func (cache *officialManagerCache) List(prefix string, limit int) (map[string]string, error) {
	out := make(map[string]string)
	for key, value := range cache.values {
		if len(out) >= limit {
			break
		}
		if strings.HasPrefix(key, prefix) {
			out[key] = value
		}
	}
	return out, nil
}

func (cache *officialManagerCache) CompareAndSwap(key, oldValue, newValue string) (bool, error) {
	if cache.values[key] != oldValue {
		return false, nil
	}
	cache.values[key] = newValue
	return true, nil
}

func TestOfficialLargeGroupCreateAndModeration(t *testing.T) {
	control, err := admincontrol.NewControlPlane(&adminHTTPMemoryRepository{})
	if err != nil {
		t.Fatal(err)
	}
	owner := types.Uid(1001)
	adminUID := types.Uid(1002)
	memberUID := types.Uid(1003)
	state := &officialManagerTestState{
		topics: make(map[string]*types.Topic),
		subs:   make(map[string]*types.Subscription),
		users: map[types.Uid]*types.User{
			owner:     {State: types.StateOK},
			adminUID:  {State: types.StateOK},
			memberUID: {State: types.StateOK},
		},
	}
	manager := &officialTopicManager{
		control: control, topics: officialManagerTopicStore{state},
		subs: officialManagerSubStore{state}, users: officialManagerUserStore{state},
		newTopic: func() string { return "grpOfficialLarge01" },
	}
	actor := admincontrol.Actor{Subject: "platform-admin", RequestID: "request-large-1"}
	created, err := manager.create(1, officialTopicCreateInput{
		OrganizationID: "org-main", Owner: owner.UserId(), ScaleClass: "large",
		Admins: []string{adminUID.UserId()}, JoinPolicy: "open",
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if created.Topic.ScaleClass != "large" || created.Topic.MemberLimit != 0 ||
		state.topics[created.Topic.Topic].UseBt {
		t.Fatalf("官方大群策略不正确：%+v", created)
	}
	adminSub := state.subs[created.Topic.Topic+":"+adminUID.String()]
	if adminSub == nil || !(adminSub.ModeWant & adminSub.ModeGiven).IsAdmin() {
		t.Fatalf("平台管理员未写入订阅 ACL：%+v", adminSub)
	}
	allMuted := true
	patched, err := manager.patch(created.Version, created.Topic.Topic,
		officialTopicPatchInput{AllMuted: &allMuted}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if !patched.Topic.AllMuted {
		t.Fatalf("全员禁言策略未持久化：%+v", patched.Topic)
	}

	memberMode := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
	if err = manager.subs.Create(&types.Subscription{
		User: memberUID.String(), Topic: created.Topic.Topic,
		ModeWant: memberMode, ModeGiven: memberMode,
	}); err != nil {
		t.Fatal(err)
	}
	previousCache := store.PCache
	cache := &officialManagerCache{values: make(map[string]string)}
	store.PCache = cache
	t.Cleanup(func() { store.PCache = previousCache })

	muted, err := manager.mute(patched.Version, created.Topic.Topic, officialMuteInput{
		User: memberUID.UserId(), Scope: "message", ReasonCode: "spam", Minutes: 30,
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if muted.Action != "mute" || len(cache.values) != 1 {
		t.Fatalf("禁言状态未持久化：%+v cache=%+v", muted, cache.values)
	}
	audit := control.OfficialTopicAudit(created.Topic.Topic, 10)
	if len(audit) < 2 || audit[0].Action != "official_topic.member.mute" {
		t.Fatalf("禁言审计缺失：%+v", audit)
	}

	banned, err := manager.ban(muted.Version, created.Topic.Topic, officialBanInput{
		User: memberUID.UserId(), ReasonCode: "abuse",
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	bannedSub := state.subs[created.Topic.Topic+":"+memberUID.String()]
	if banned.Action != "ban" || bannedSub.ModeWant != types.ModeNone ||
		bannedSub.ModeGiven != types.ModeNone {
		t.Fatalf("封禁 ACL 不正确：%+v sub=%+v", banned, bannedSub)
	}

	unbanned, err := manager.unban(banned.Version, created.Topic.Topic, memberUID.UserId(), actor)
	if err != nil {
		t.Fatal(err)
	}
	if unbanned.Action != "unban" ||
		!(bannedSub.ModeWant & bannedSub.ModeGiven).IsWriter() {
		t.Fatalf("解封后未恢复成员 ACL：%+v sub=%+v", unbanned, bannedSub)
	}
}

func TestOfficialLargeGroupPublishPolicy(t *testing.T) {
	now := types.TimeNow()
	memberUID := types.Uid(2001)
	adminUID := types.Uid(2002)
	memberMode := types.ModeJoin | types.ModeRead | types.ModeWrite | types.ModePres
	adminMode := types.ModeCFull &^ types.ModeOwner
	topic := &Topic{
		name: "grpOfficialLarge02",
		official: &officialTopicPolicy{
			Official: true, OfficialStatus: "verified", ScaleClass: "large", AllMuted: true,
		},
		officialRefreshedAt: now,
		officialMemberRefreshed: map[types.Uid]time.Time{
			memberUID: now, adminUID: now,
		},
		perUser: map[types.Uid]perUserData{
			memberUID: {modeWant: memberMode, modeGiven: memberMode},
			adminUID:  {modeWant: adminMode, modeGiven: adminMode},
		},
		moderationCache: map[types.Uid]officialModerationCache{
			adminUID: {loadedAt: now},
		},
	}
	if err := topic.checkOfficialPublish(memberUID, "message", now); !errors.Is(err, types.ErrPermissionDenied) {
		t.Fatalf("全员禁言应拒绝普通成员，得到 %v", err)
	}
	if err := topic.checkOfficialPublish(adminUID, "message", now); err != nil {
		t.Fatalf("全员禁言不应阻止管理员，得到 %v", err)
	}
	topic.official.AllMuted = false
	expiresAt := now.Add(time.Minute)
	topic.moderationCache = map[types.Uid]officialModerationCache{
		memberUID: {
			state: &officialModerationState{
				Action: "mute", Scope: "message", StartsAt: now.Add(-time.Minute),
				ExpiresAt: &expiresAt,
			},
			loadedAt: now,
		},
	}
	if err := topic.checkOfficialPublish(memberUID, "message", now); !errors.Is(err, types.ErrPermissionDenied) {
		t.Fatalf("单人禁言应拒绝消息写入，得到 %v", err)
	}
	if err := topic.checkOfficialPublish(memberUID, "call", now); err != nil {
		t.Fatalf("message 范围禁言不应阻止通话，得到 %v", err)
	}
}
