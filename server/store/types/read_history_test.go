package types

import (
	"testing"
	"time"
)

func TestReadHistoryAppendAndLookup(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	history := ReadHistory{
		{LowSeqId: 1, HighSeqId: 2, ReadAt: now.Add(-8 * 24 * time.Hour)},
	}

	history.Append(3, 7, now, now.Add(-7*24*time.Hour))

	if len(history) != 1 {
		t.Fatalf("expected expired checkpoint to be removed, got %d", len(history))
	}
	if readAt, ok := history.TimeFor(5); !ok || !readAt.Equal(now) {
		t.Fatalf("expected seq 5 to resolve to %v, got %v, %v", now, readAt, ok)
	}
	if _, ok := history.TimeFor(2); ok {
		t.Fatal("expired sequence unexpectedly resolved")
	}
}

func TestReadHistorySQLRoundTrip(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	source := ReadHistory{{LowSeqId: 4, HighSeqId: 9, ReadAt: now}}
	value, err := source.Value()
	if err != nil {
		t.Fatal(err)
	}

	var decoded ReadHistory
	if err := decoded.Scan(value); err != nil {
		t.Fatal(err)
	}
	if readAt, ok := decoded.TimeFor(8); !ok || !readAt.Equal(now) {
		t.Fatalf("unexpected round trip result: %v, %v", readAt, ok)
	}
}

func TestReadHistoryIsBounded(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	var history ReadHistory
	for seqID := 1; seqID <= maxReadHistoryCheckpoints+10; seqID++ {
		history.Append(seqID, seqID, now, now.Add(-time.Hour))
	}
	if len(history) != maxReadHistoryCheckpoints {
		t.Fatalf("expected %d checkpoints, got %d",
			maxReadHistoryCheckpoints, len(history))
	}
	if _, ok := history.TimeFor(10); ok {
		t.Fatal("oldest checkpoint should have been evicted")
	}
	if _, ok := history.TimeFor(maxReadHistoryCheckpoints + 10); !ok {
		t.Fatal("latest checkpoint should be retained")
	}
}
