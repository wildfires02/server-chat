package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

const maxReadHistoryCheckpoints = 4096

// ReadCheckpoint records when a contiguous range of messages became read.
type ReadCheckpoint struct {
	LowSeqId  int       `json:"low" bson:"low"`
	HighSeqId int       `json:"high" bson:"high"`
	ReadAt    time.Time `json:"at" bson:"at"`
}

// ReadHistory is a rolling list of read checkpoints for a subscription.
type ReadHistory []ReadCheckpoint

// Append records a read-sequence advance and drops checkpoints older than cutoff.
func (history *ReadHistory) Append(lowSeqId, highSeqId int, readAt, cutoff time.Time) {
	if history == nil || lowSeqId <= 0 || highSeqId < lowSeqId {
		return
	}

	filtered := (*history)[:0]
	for _, checkpoint := range *history {
		if !checkpoint.ReadAt.Before(cutoff) {
			filtered = append(filtered, checkpoint)
		}
	}
	*history = append(filtered, ReadCheckpoint{
		LowSeqId:  lowSeqId,
		HighSeqId: highSeqId,
		ReadAt:    readAt,
	})
	if len(*history) > maxReadHistoryCheckpoints {
		*history = append(ReadHistory(nil),
			(*history)[len(*history)-maxReadHistoryCheckpoints:]...)
	}
}

// TimeFor returns when the subscription first advanced past seqId.
func (history ReadHistory) TimeFor(seqId int) (time.Time, bool) {
	for _, checkpoint := range history {
		if seqId >= checkpoint.LowSeqId && seqId <= checkpoint.HighSeqId {
			return checkpoint.ReadAt, true
		}
	}
	return time.Time{}, false
}

// Value serializes the history for SQL JSON columns.
func (history ReadHistory) Value() (driver.Value, error) {
	if len(history) == 0 {
		return nil, nil
	}
	return json.Marshal(history)
}

// Scan deserializes the history returned by SQL adapters.
func (history *ReadHistory) Scan(value any) error {
	if history == nil {
		return fmt.Errorf("types.ReadHistory: Scan on nil receiver")
	}
	if value == nil {
		*history = nil
		return nil
	}

	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return fmt.Errorf("types.ReadHistory: unsupported Scan type %T", value)
	}
	if len(raw) == 0 {
		*history = nil
		return nil
	}
	return json.Unmarshal(raw, history)
}
