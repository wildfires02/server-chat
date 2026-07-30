package types

import "time"

// InternalPinKind identifies one of the three employee-only pin levels.
type InternalPinKind string

const (
	InternalPinCustomer     InternalPinKind = "customer"
	InternalPinConversation InternalPinKind = "conversation"
	InternalPinMessage      InternalPinKind = "message"
)

// InternalPinState is persisted so deletions can be synchronized to other devices.
type InternalPinState string

const (
	InternalPinActive  InternalPinState = "active"
	InternalPinDeleted InternalPinState = "deleted"
)

// InternalPinMutationOp selects the mutation applied to a private workspace.
type InternalPinMutationOp string

const (
	InternalPinUpsert InternalPinMutationOp = "upsert"
	InternalPinDelete InternalPinMutationOp = "delete"
)

// InternalPin is a private, per-employee reference. It never contains customer-visible
// topic metadata or message content.
type InternalPin struct {
	TargetKey   string           `json:"target_key"`
	Kind        InternalPinKind  `json:"kind"`
	CustomerUID string           `json:"customer_uid,omitempty"`
	Topic       string           `json:"topic,omitempty"`
	SeqID       int              `json:"seq_id,omitempty"`
	Rank        int64            `json:"rank"`
	State       InternalPinState `json:"state"`
	Version     uint64           `json:"version"`
	PinnedAt    time.Time        `json:"pinned_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	DeletedAt   *time.Time       `json:"deleted_at,omitempty"`
	Actor       string           `json:"actor,omitempty"`
	RequestID   string           `json:"request_id,omitempty"`
}

// InternalPinMutation is an optimistic-concurrency write to an employee workspace.
type InternalPinMutation struct {
	Op              InternalPinMutationOp
	Kind            InternalPinKind
	CustomerUID     string
	Topic           string
	SeqID           int
	Rank            int64
	ExpectedVersion uint64
	Actor           string
	RequestID       string
}

// InternalPinQuery asks for all changes after Since. Since=0 returns a full snapshot.
type InternalPinQuery struct {
	Since uint64
	Limit int
}

// InternalPinSnapshot is suitable for durable multi-device synchronization. When
// Reset is true, the client must replace its local workspace with Pins.
type InternalPinSnapshot struct {
	Version   uint64        `json:"version"`
	NextSince uint64        `json:"next_since"`
	Reset     bool          `json:"reset"`
	HasMore   bool          `json:"has_more"`
	Pins      []InternalPin `json:"pins"`
}
