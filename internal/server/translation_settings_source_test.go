package server

import (
	"strings"
	"sync"
	"testing"
	"time"

	"chat/server/store"
	"chat/server/store/types"
)

type translationSettingsCache struct {
	mu     sync.Mutex
	values map[string]string
}

func (cache *translationSettingsCache) Get(key string) (string, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.values[key]
	if !ok {
		return "", types.ErrNotFound
	}
	return value, nil
}

func (cache *translationSettingsCache) Upsert(key, value string, _ bool) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.values[key] = value
	return nil
}

func (cache *translationSettingsCache) Delete(key string) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.values, key)
	return nil
}

func (*translationSettingsCache) Expire(string, time.Time) error {
	return nil
}

func (cache *translationSettingsCache) List(prefix string, limit int) (map[string]string, error) {
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

func (cache *translationSettingsCache) CompareAndSwap(key, oldValue, newValue string) (bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.values[key] != oldValue {
		return false, nil
	}
	cache.values[key] = newValue
	return true, nil
}

func TestPersistentTranslationSettingsSourceRefreshesAdminPolicy(t *testing.T) {
	cache := &translationSettingsCache{values: make(map[string]string)}
	previous := store.PCache
	store.PCache = cache
	t.Cleanup(func() { store.PCache = previous })

	source := newPersistentTranslationSettingsSource(time.Hour)
	fallback, err := source.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Version != 0 || !fallback.Settings.Enabled ||
		fallback.Settings.FailurePolicy != "hold" {
		t.Fatalf("unexpected fail-closed fallback: %#v", fallback)
	}

	cache.values[adminDocumentKey] = `{
		"version": 1,
		"settings": {
			"translation": {
				"enabled": false,
				"staff_language": "zh-CN",
				"customer_language": "en",
				"keep_original": true,
				"failure_policy": "hold",
				"default_timeout_ms": 1500,
				"max_attempts": 3,
				"providers": [],
				"routes": []
			}
		}
	}`
	source.nextRefresh = time.Time{}
	disabled, err := source.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Version != 1 || disabled.Settings.Enabled {
		t.Fatalf("admin policy was not loaded: %#v", disabled)
	}

	cache.values[adminDocumentKey] = `{"version":2,"settings":{"translation":{"enabled":true}}}`
	source.nextRefresh = time.Time{}
	retained, err := source.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if retained.Version != 1 || retained.Settings.Enabled {
		t.Fatalf("invalid update replaced the last valid policy: %#v", retained)
	}
}
