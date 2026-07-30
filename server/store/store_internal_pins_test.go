package store

import (
	"errors"
	"sync"
	"testing"

	"chat/server/store/types"
)

func TestInternalPinsThreeLevelsAndIncrementalSync(t *testing.T) {
	useMemoryPersistentCache(t)
	mapper := internalPinMapper{}
	owner := types.Uid(100)
	customer := types.Uid(200).UserId()

	customerPin, changed, err := mapper.Apply("org-a", owner, types.InternalPinMutation{
		Op: types.InternalPinUpsert, Kind: types.InternalPinCustomer,
		CustomerUID: customer, Rank: 10, Actor: owner.UserId(), RequestID: "req-1",
	})
	if err != nil || !changed || customerPin.Version != 1 {
		t.Fatalf("customer pin failed: pin=%#v changed=%v err=%v", customerPin, changed, err)
	}
	conversationPin, changed, err := mapper.Apply("org-a", owner, types.InternalPinMutation{
		Op: types.InternalPinUpsert, Kind: types.InternalPinConversation,
		Topic: "grpConversation", Rank: 20,
	})
	if err != nil || !changed || conversationPin.Version != 2 {
		t.Fatalf("conversation pin failed: pin=%#v changed=%v err=%v", conversationPin, changed, err)
	}
	messagePin, changed, err := mapper.Apply("org-a", owner, types.InternalPinMutation{
		Op: types.InternalPinUpsert, Kind: types.InternalPinMessage,
		Topic: "grpConversation", SeqID: 42, Rank: 30,
	})
	if err != nil || !changed || messagePin.Version != 3 {
		t.Fatalf("message pin failed: pin=%#v changed=%v err=%v", messagePin, changed, err)
	}

	full, err := mapper.Query("org-a", owner, types.InternalPinQuery{})
	if err != nil || !full.Reset || full.Version != 3 || full.NextSince != 3 || len(full.Pins) != 3 {
		t.Fatalf("unexpected full snapshot: snapshot=%#v err=%v", full, err)
	}
	if _, _, err = mapper.Apply("org-a", owner, types.InternalPinMutation{
		Op: types.InternalPinUpsert, Kind: types.InternalPinCustomer,
		CustomerUID: customer, Rank: 11, ExpectedVersion: 999,
	}); !errors.Is(err, types.ErrVersionConflict) {
		t.Fatalf("expected stale-write conflict, got %v", err)
	}
	customerPin, changed, err = mapper.Apply("org-a", owner, types.InternalPinMutation{
		Op: types.InternalPinUpsert, Kind: types.InternalPinCustomer,
		CustomerUID: customer, Rank: 11, ExpectedVersion: customerPin.Version,
	})
	if err != nil || !changed || customerPin.Version != 4 {
		t.Fatalf("rank update failed: pin=%#v changed=%v err=%v", customerPin, changed, err)
	}
	incremental, err := mapper.Query("org-a", owner, types.InternalPinQuery{Since: 3})
	if err != nil || incremental.Reset || incremental.NextSince != 4 ||
		len(incremental.Pins) != 1 || incremental.Pins[0].Rank != 11 {
		t.Fatalf("unexpected incremental snapshot: snapshot=%#v err=%v", incremental, err)
	}

	deleted, changed, err := mapper.Apply("org-a", owner, types.InternalPinMutation{
		Op: types.InternalPinDelete, Kind: types.InternalPinCustomer,
		CustomerUID: customer, ExpectedVersion: customerPin.Version,
	})
	if err != nil || !changed || deleted.State != types.InternalPinDeleted || deleted.Version != 5 {
		t.Fatalf("delete failed: pin=%#v changed=%v err=%v", deleted, changed, err)
	}
	deletions, err := mapper.Query("org-a", owner, types.InternalPinQuery{Since: 4})
	if err != nil || len(deletions.Pins) != 1 ||
		deletions.Pins[0].State != types.InternalPinDeleted {
		t.Fatalf("delete tombstone not synchronized: snapshot=%#v err=%v", deletions, err)
	}
	full, err = mapper.Query("org-a", owner, types.InternalPinQuery{})
	if err != nil || len(full.Pins) != 2 {
		t.Fatalf("deleted pin leaked into full snapshot: snapshot=%#v err=%v", full, err)
	}
	_, changed, err = mapper.Apply("org-a", owner, types.InternalPinMutation{
		Op: types.InternalPinDelete, Kind: types.InternalPinCustomer,
		CustomerUID: customer, ExpectedVersion: 4,
	})
	if err != nil || changed {
		t.Fatalf("idempotent delete failed: changed=%v err=%v", changed, err)
	}
}

func TestInternalPinsCASAndWorkspaceIsolation(t *testing.T) {
	useMemoryPersistentCache(t)
	mapper := internalPinMapper{}
	owner := types.Uid(300)
	const count = 24
	var wait sync.WaitGroup
	errs := make(chan error, count)
	for seqID := 1; seqID <= count; seqID++ {
		wait.Add(1)
		go func(seq int) {
			defer wait.Done()
			_, _, err := mapper.Apply("org-a", owner, types.InternalPinMutation{
				Op: types.InternalPinUpsert, Kind: types.InternalPinMessage,
				Topic: "grpConcurrent", SeqID: seq, Rank: int64(seq),
			})
			errs <- err
		}(seqID)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent mutation failed: %v", err)
		}
	}
	snapshot, err := mapper.Query("org-a", owner, types.InternalPinQuery{})
	if err != nil || snapshot.Version != count || len(snapshot.Pins) != count {
		t.Fatalf("CAS lost an update: snapshot=%#v err=%v", snapshot, err)
	}
	otherOrg, err := mapper.Query("org-b", owner, types.InternalPinQuery{})
	if err != nil || otherOrg.Version != 0 || len(otherOrg.Pins) != 0 {
		t.Fatalf("organization workspace leaked: snapshot=%#v err=%v", otherOrg, err)
	}
	otherOwner, err := mapper.Query("org-a", types.Uid(301), types.InternalPinQuery{})
	if err != nil || otherOwner.Version != 0 || len(otherOwner.Pins) != 0 {
		t.Fatalf("employee workspace leaked: snapshot=%#v err=%v", otherOwner, err)
	}
}
