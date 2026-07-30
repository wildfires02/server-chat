package store

import (
	"strings"
	"sync"
	"testing"
	"time"

	"chat/server/store/types"
)

type memoryPersistentCache struct {
	mu     sync.Mutex
	values map[string]string
}

func (cache *memoryPersistentCache) Get(key string) (string, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.values[key]
	if !ok {
		return "", types.ErrNotFound
	}
	return value, nil
}

func (cache *memoryPersistentCache) Upsert(key, value string, failOnDuplicate bool) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, ok := cache.values[key]; ok && failOnDuplicate {
		return types.ErrDuplicate
	}
	cache.values[key] = value
	return nil
}

func (cache *memoryPersistentCache) Delete(key string) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.values, key)
	return nil
}

func (cache *memoryPersistentCache) Expire(_ string, _ time.Time) error { return nil }

func (cache *memoryPersistentCache) List(prefix string, limit int) (map[string]string, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	result := make(map[string]string)
	for key, value := range cache.values {
		if strings.HasPrefix(key, prefix) && len(result) < limit {
			result[key] = value
		}
	}
	return result, nil
}

func (cache *memoryPersistentCache) CompareAndSwap(key, oldValue, newValue string) (bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.values[key] != oldValue {
		return false, nil
	}
	cache.values[key] = newValue
	return true, nil
}

func useMemoryPersistentCache(t *testing.T) {
	t.Helper()
	previous := PCache
	PCache = &memoryPersistentCache{values: make(map[string]string)}
	t.Cleanup(func() { PCache = previous })
}

func TestContactsCRUDAndIncrementalSync(t *testing.T) {
	useMemoryPersistentCache(t)
	mapper := &contactMapper{}
	owner := types.Uid(100)
	peer := types.Uid(200).UserId()

	if _, err := mapper.Apply(owner, types.ContactMutation{
		Op: "upsert_group",
		Group: &types.ContactGroup{
			Id:   "team",
			Name: "团队",
		},
	}); err != nil {
		t.Fatal(err)
	}
	afterGroup, err := mapper.Get(owner, types.ContactQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterGroup.Groups) != 1 || afterGroup.Groups[0].Name != "团队" {
		t.Fatalf("unexpected groups: %#v", afterGroup.Groups)
	}

	if _, err = mapper.Apply(owner, types.ContactMutation{
		Op: "upsert_contact",
		Contact: &types.AddressBookContact{
			User:   peer,
			Alias:  " 小明 ",
			Groups: []string{"team", "team"},
			Status: types.ContactAccepted,
		},
	}); err != nil {
		t.Fatal(err)
	}
	incremental, err := mapper.Get(owner, types.ContactQuery{Since: afterGroup.Version})
	if err != nil {
		t.Fatal(err)
	}
	if incremental.Reset || len(incremental.Events) != 1 ||
		incremental.Events[0].Type != "contact.upsert" ||
		len(incremental.Contacts) != 1 || incremental.Contacts[0].Alias != "小明" ||
		len(incremental.Contacts[0].Groups) != 1 {
		t.Fatalf("unexpected incremental result: %#v", incremental)
	}

	if _, err = mapper.Apply(owner, types.ContactMutation{Op: "delete_group", GroupId: "team"}); err != nil {
		t.Fatal(err)
	}
	full, err := mapper.Get(owner, types.ContactQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Groups) != 0 || len(full.Contacts) != 1 || len(full.Contacts[0].Groups) != 0 {
		t.Fatalf("group delete did not detach contacts: %#v", full)
	}
	limited, err := mapper.Get(owner, types.ContactQuery{Since: afterGroup.Version, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Events) != 1 || limited.Version != limited.Events[0].Version ||
		limited.Version >= full.Version {
		t.Fatalf("limited sync advanced beyond returned events: %#v", limited)
	}
}

func TestContactsRejectUnknownGroup(t *testing.T) {
	useMemoryPersistentCache(t)
	mapper := &contactMapper{}
	_, err := mapper.Apply(types.Uid(100), types.ContactMutation{
		Op: "upsert_contact",
		Contact: &types.AddressBookContact{
			User:   types.Uid(200).UserId(),
			Groups: []string{"missing"},
			Status: types.ContactAccepted,
		},
	})
	if err != types.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestFriendRequestAcceptanceUpdatesBothUsers(t *testing.T) {
	useMemoryPersistentCache(t)
	mapper := &contactMapper{}
	alice := types.Uid(100)
	bob := types.Uid(200)

	if _, err := mapper.Apply(alice, types.ContactMutation{
		Op: "request_friend", User: bob.UserId(),
	}); err != nil {
		t.Fatal(err)
	}
	bobPending, err := mapper.Get(bob, types.ContactQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bobPending.Contacts) != 1 || bobPending.Contacts[0].Request != "incoming" ||
		bobPending.Contacts[0].Status != types.ContactPending {
		t.Fatalf("unexpected incoming request: %#v", bobPending)
	}

	if _, err = mapper.Apply(bob, types.ContactMutation{
		Op: "accept_friend", User: alice.UserId(),
	}); err != nil {
		t.Fatal(err)
	}
	aliceAccepted, err := mapper.Get(alice, types.ContactQuery{})
	if err != nil {
		t.Fatal(err)
	}
	bobAccepted, err := mapper.Get(bob, types.ContactQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceAccepted.Contacts) != 1 || aliceAccepted.Contacts[0].Status != types.ContactAccepted ||
		len(bobAccepted.Contacts) != 1 || bobAccepted.Contacts[0].Status != types.ContactAccepted {
		t.Fatalf("friend relation not accepted: alice=%#v bob=%#v", aliceAccepted, bobAccepted)
	}
}
