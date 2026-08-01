package types

import "time"

//InternalPinKind标识了三个仅限员工的针脚级别之一。
type InternalPinKind string

const (
	InternalPinCustomer     InternalPinKind = "customer"
	InternalPinConversation InternalPinKind = "conversation"
	InternalPinMessage      InternalPinKind = "message"
)

//InternalPinState是持久的，因此可以删除同步到其他设备。
type InternalPinState string

const (
	InternalPinActive  InternalPinState = "active"
	InternalPinDeleted InternalPinState = "deleted"
)

//InternalPinMutationOp选择应用于私有工作区的突变。
type InternalPinMutationOp string

const (
	InternalPinUpsert InternalPinMutationOp = "upsert"
	InternalPinDelete InternalPinMutationOp = "delete"
)

//InternalPin是一个私人的、每个员工的参考。 它从未包含客户可见的
// 主题元数据或消息内容。
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

//InternalPinMutation是面向员工工作区写入的乐观并发写入。
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

//InternalPinQuery要求在Since之后进行所有更改。 自=0返回完整的快照。
type InternalPinQuery struct {
	Since uint64
	Limit int
}

//InternalPinSnapshot适用于持久的多设备同步。 什么时候
// 重置为真，客户端必须用Pins替换其本地工作区。
type InternalPinSnapshot struct {
	Version   uint64        `json:"version"`
	NextSince uint64        `json:"next_since"`
	Reset     bool          `json:"reset"`
	HasMore   bool          `json:"has_more"`
	Pins      []InternalPin `json:"pins"`
}
