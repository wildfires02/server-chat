package server

import (
	"errors"
	"sync"
	"testing"
	"time"

	"chat/server/store"
	"chat/server/store/types"
)

func useProcessingJobTestCache(t *testing.T) {
	t.Helper()
	previous := store.PCache
	store.PCache = &resumableTestCache{values: make(map[string]string)}
	t.Cleanup(func() { store.PCache = previous })
}

func testProcessingDefinition(id types.Uid) *types.FileDef {
	definition := &types.FileDef{
		ObjHeader: types.ObjHeader{Id: id.String()},
		User:      types.Uid(10).String(),
		MimeType:  "image/png",
	}
	definition.InitTimes()
	return definition
}

func TestPersistentFileProcessingJobClaimIsExclusive(t *testing.T) {
	useProcessingJobTestCache(t)
	definition := testProcessingDefinition(types.Uid(930))
	if err := enqueuePersistentFileProcessingJob(definition, "/file/"+definition.Id); err != nil {
		t.Fatal(err)
	}
	now := types.TimeNow().Add(time.Millisecond)
	var wg sync.WaitGroup
	claims := make(chan *claimedFileProcessingJob, 2)
	errs := make(chan error, 2)
	for _, owner := range []string{"node-a", "node-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			claim, err := claimPersistentFileProcessingJob(owner, now, time.Minute, 10)
			claims <- claim
			errs <- err
		}(owner)
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for claim := range claims {
		if claim != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("exclusive claims=%d, want 1", count)
	}
}

func TestPersistentFileProcessingJobRecoversExpiredLease(t *testing.T) {
	useProcessingJobTestCache(t)
	definition := testProcessingDefinition(types.Uid(931))
	if err := enqueuePersistentFileProcessingJob(definition, "/file/"+definition.Id); err != nil {
		t.Fatal(err)
	}
	now := types.TimeNow().Add(time.Millisecond)
	first, err := claimPersistentFileProcessingJob("node-a", now, time.Second, 10)
	if err != nil || first == nil {
		t.Fatalf("first claim=%v err=%v", first, err)
	}
	if next, err := claimPersistentFileProcessingJob("node-b", now.Add(500*time.Millisecond),
		time.Second, 10); err != nil || next != nil {
		t.Fatalf("claim before expiry=%v err=%v", next, err)
	}
	recovered, err := claimPersistentFileProcessingJob("node-b", now.Add(2*time.Second),
		time.Second, 10)
	if err != nil || recovered == nil {
		t.Fatalf("recovered claim=%v err=%v", recovered, err)
	}
	if recovered.job.Attempts != 2 || recovered.job.LeaseOwner != "node-b" {
		t.Fatalf("recovered job=%+v", recovered.job)
	}
}

func TestPersistentFileProcessingJobBackoffAndDeadLetter(t *testing.T) {
	useProcessingJobTestCache(t)
	definition := testProcessingDefinition(types.Uid(932))
	if err := enqueuePersistentFileProcessingJob(definition, "/file/"+definition.Id); err != nil {
		t.Fatal(err)
	}
	now := types.TimeNow().Add(time.Millisecond)
	first, err := claimPersistentFileProcessingJob("node-a", now, time.Minute, 10)
	if err != nil || first == nil {
		t.Fatalf("first claim=%v err=%v", first, err)
	}
	swapped, retry, err := retryPersistentFileProcessingJob(first, 2, time.Second,
		errors.New("temporary"))
	if err != nil || !swapped || retry.Status != "retrying" {
		t.Fatalf("first retry swapped=%v job=%+v err=%v", swapped, retry, err)
	}
	second, err := claimPersistentFileProcessingJob("node-b", retry.NextAttemptAt.Add(time.Millisecond),
		time.Minute, 10)
	if err != nil || second == nil {
		t.Fatalf("second claim=%v err=%v", second, err)
	}
	swapped, dead, err := retryPersistentFileProcessingJob(second, 2, time.Second,
		errors.New("permanent"))
	if err != nil || !swapped || dead.Status != "dead" {
		t.Fatalf("dead transition swapped=%v job=%+v err=%v", swapped, dead, err)
	}
	if claim, err := claimPersistentFileProcessingJob("node-c", time.Now().Add(time.Hour),
		time.Minute, 10); err != nil || claim != nil {
		t.Fatalf("dead job was claimed=%v err=%v", claim, err)
	}
	if err := enqueuePersistentFileProcessingJob(definition, "/file/"+definition.Id); err != nil {
		t.Fatalf("manual dead-letter retry: %v", err)
	}
	requeued, err := claimPersistentFileProcessingJob("node-c", time.Now().Add(time.Hour),
		time.Minute, 10)
	if err != nil || requeued == nil || requeued.job.Attempts != 1 {
		t.Fatalf("requeued dead job=%v err=%v", requeued, err)
	}
}
