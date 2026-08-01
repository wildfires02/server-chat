package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

const maxReadHistoryCheckpoints = 4096

//ReadCheckpoint记录连续范围的消息被读取时。
type ReadCheckpoint struct {
	LowSeqId  int       `json:"low" bson:"low"`
	HighSeqId int       `json:"high" bson:"high"`
	ReadAt    time.Time `json:"at" bson:"at"`
}

//ReadHistory是订阅的读取检查点的滚动列表。
type ReadHistory []ReadCheckpoint

// 附加记录读取序列前进，并丢弃比截止时更早的检查点。
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

// 当订阅首次超过seqId时，TimeFor将返回。
func (history ReadHistory) TimeFor(seqId int) (time.Time, bool) {
	for _, checkpoint := range history {
		if seqId >= checkpoint.LowSeqId && seqId <= checkpoint.HighSeqId {
			return checkpoint.ReadAt, true
		}
	}
	return time.Time{}, false
}

// 值对SQL JSON列的历史记录进行序列化。
func (history ReadHistory) Value() (driver.Value, error) {
	if len(history) == 0 {
		return nil, nil
	}
	return json.Marshal(history)
}

// 扫描将SQL适配器返回的历史记录反序列化。
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
