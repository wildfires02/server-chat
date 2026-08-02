package push

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"chat/server/store"
	"chat/server/store/types"
)

type pushMemoryCache struct {
	mu     sync.Mutex
	values map[string]string
}

func (cache *pushMemoryCache) Get(key string) (string, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.values[key]
	if !ok {
		return "", types.ErrNotFound
	}
	return value, nil
}

func (cache *pushMemoryCache) Upsert(key, value string, failOnDuplicate bool) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, exists := cache.values[key]; exists && failOnDuplicate {
		return types.ErrDuplicate
	}
	cache.values[key] = value
	return nil
}

func (cache *pushMemoryCache) Delete(key string) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.values, key)
	return nil
}

func (cache *pushMemoryCache) Expire(string, time.Time) error { return nil }

func (cache *pushMemoryCache) List(prefix string, limit int) (map[string]string, error) {
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

func (cache *pushMemoryCache) CompareAndSwap(key, oldValue, newValue string) (bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.values[key] != oldValue {
		return false, nil
	}
	cache.values[key] = newValue
	return true, nil
}

func usePushMemoryCache(t *testing.T) *pushMemoryCache {
	t.Helper()
	previous := store.PCache
	cache := &pushMemoryCache{values: make(map[string]string)}
	store.PCache = cache
	t.Cleanup(func() { store.PCache = previous })
	return cache
}

func TestDurableOutboxRetriesAndSplitsRecipients(t *testing.T) {
	usePushMemoryCache(t)
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	attempts := 0
	outbox := newDurableOutbox("test", func(*Receipt) error {
		attempts++
		if attempts <= 2 {
			return errors.New("temporary outage")
		}
		return nil
	}, durableOutboxConfig{
		BatchSize: 10, RecipientChunk: 2, MaxAttempts: 3,
		PollInterval: time.Hour, RetryBase: time.Second,
		MaxRetry: time.Minute, Lease: time.Minute,
	})
	outbox.now = func() time.Time { return now }
	receipt := &Receipt{To: map[types.Uid]Recipient{
		1: {}, 2: {}, 3: {},
	}, Payload: Payload{What: ActMsg, Content: "hello"}}
	if err := outbox.Enqueue(receipt); err != nil {
		t.Fatal(err)
	}
	stats, err := GetDurableOutboxStats("test")
	if err != nil || stats.Queued != 2 {
		t.Fatalf("queued stats=%+v err=%v", stats, err)
	}
	if processed, err := outbox.processAvailable(now); err != nil || processed != 2 {
		t.Fatalf("first process=%d err=%v", processed, err)
	}
	if processed, err := outbox.processAvailable(now.Add(2 * time.Second)); err != nil || processed != 2 {
		t.Fatalf("retry process=%d err=%v", processed, err)
	}
	stats, err = GetDurableOutboxStats("test")
	if err != nil || stats.Total != 0 || attempts != 4 {
		t.Fatalf("final stats=%+v attempts=%d err=%v", stats, attempts, err)
	}
}

func TestDurableOutboxMovesPermanentFailureToDLQ(t *testing.T) {
	usePushMemoryCache(t)
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	outbox := newDurableOutbox("dead", func(*Receipt) error {
		return Permanent(errors.New("invalid credentials"))
	}, durableOutboxConfig{
		BatchSize: 10, RecipientChunk: 10, MaxAttempts: 8,
		PollInterval: time.Hour, RetryBase: time.Second,
		MaxRetry: time.Minute, Lease: time.Minute,
	})
	outbox.now = func() time.Time { return now }
	if err := outbox.Enqueue(&Receipt{
		To:      map[types.Uid]Recipient{1: {}},
		Payload: Payload{What: ActMsg},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.processAvailable(now); err != nil {
		t.Fatal(err)
	}
	stats, err := GetDurableOutboxStats("dead")
	if err != nil || stats.Total != 0 || stats.DLQ != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestDurableOutboxPersistsChannelReceiptWithoutRecipients(t *testing.T) {
	usePushMemoryCache(t)
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	delivered := ""
	outbox := newDurableOutbox("channel", func(receipt *Receipt) error {
		delivered = receipt.Channel
		return nil
	}, durableOutboxConfig{
		BatchSize: 10, RecipientChunk: 10, MaxAttempts: 3,
		PollInterval: time.Hour, RetryBase: time.Second,
		MaxRetry: time.Minute, Lease: time.Minute,
	})
	outbox.now = func() time.Time { return now }
	if err := outbox.Enqueue(&Receipt{
		To: map[types.Uid]Recipient{}, Channel: "official-news",
		Payload: Payload{What: ActMsg},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.processAvailable(now); err != nil {
		t.Fatal(err)
	}
	if delivered != "official-news" {
		t.Fatalf("delivered channel=%q", delivered)
	}
}

func TestDurableDeadLetterListReplayAndDelete(t *testing.T) {
	usePushMemoryCache(t)
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	deadOutbox := newDurableOutbox("ops", func(*Receipt) error {
		return Permanent(errors.New("provider rejected credentials"))
	}, durableOutboxConfig{
		BatchSize: 10, RecipientChunk: 10, MaxAttempts: 2,
		PollInterval: time.Hour, RetryBase: time.Second,
		MaxRetry: time.Minute, Lease: time.Minute,
	})
	deadOutbox.now = func() time.Time { return now }
	enqueueDead := func(topic string) {
		t.Helper()
		if err := deadOutbox.Enqueue(&Receipt{
			To:      map[types.Uid]Recipient{1: {}},
			Payload: Payload{What: ActMsg, Topic: topic, Content: "not exposed"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := deadOutbox.processAvailable(now); err != nil {
			t.Fatal(err)
		}
	}

	enqueueDead("usr-replay")
	letters, err := ListDurableDeadLetters("ops", 20)
	if err != nil || len(letters) != 1 {
		t.Fatalf("dead letters=%+v err=%v", letters, err)
	}
	if letters[0].Topic != "usr-replay" || letters[0].Recipients != 1 ||
		letters[0].LastError == "" {
		t.Fatalf("unexpected dead letter summary: %+v", letters[0])
	}
	if _, err = ReplayDurableDeadLetter("ops", letters[0].ID); err != nil {
		t.Fatal(err)
	}
	stats, err := GetDurableOutboxStats("ops")
	if err != nil || stats.Queued != 1 || stats.DLQ != 0 || stats.Alert != "ok" {
		t.Fatalf("replayed stats=%+v err=%v", stats, err)
	}
	successOutbox := newDurableOutbox("ops", func(*Receipt) error { return nil },
		deadOutbox.config)
	if _, err = successOutbox.processAvailable(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	enqueueDead("usr-delete")
	letters, err = ListDurableDeadLetters("ops", 20)
	if err != nil || len(letters) != 1 {
		t.Fatalf("delete candidate=%+v err=%v", letters, err)
	}
	stats, err = GetDurableOutboxStats("ops")
	if err != nil || stats.Alert != "warning" || stats.OldestDLQ == "" {
		t.Fatalf("alert stats=%+v err=%v", stats, err)
	}
	if err = DeleteDurableDeadLetter("ops", letters[0].ID); err != nil {
		t.Fatal(err)
	}
	letters, err = ListDurableDeadLetters("ops", 20)
	if err != nil || len(letters) != 0 {
		t.Fatalf("dead letters after delete=%+v err=%v", letters, err)
	}
}
