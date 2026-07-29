// Package types 提供搜索查询在协议层、业务层与数据库适配器之间共享的领域模型。
package types

import "time"

const (
	// SearchScopePeers 表示搜索可发现的用户、群组和广播频道。
	SearchScopePeers = "peers"
	// SearchScopeTopic 表示仅搜索当前已订阅 Topic 中的消息。
	SearchScopeTopic = "topic"
)

// PeerSearchQuery 描述用户、群组和频道的关键词发现条件。
type PeerSearchQuery struct {
	// Query 是已经去除首尾空白但尚未进行数据库转义的用户输入。
	Query string
	// AliasPrefix 是用于标识公开用户名或 Topic 别名的 Tag 命名空间。
	AliasPrefix string
	// Offset 是稳定排序结果中的起始偏移，仅由服务端游标解码产生。
	Offset int
	// Limit 是本页最多返回的结果数量。
	Limit int
	// ActiveOnly 为 true 时过滤暂停和软删除的用户及 Topic。
	ActiveOnly bool
}

// MessageSearchQuery 描述单个 Topic 内的消息全文搜索条件。
type MessageSearchQuery struct {
	// Query 是需要在 SearchText 中匹配的普通文本。
	Query string
	// From 仅返回指定发送者的消息；零值表示不限制发送者。
	From Uid
	// Kinds 仅返回这些由服务端推导的消息类型；空切片表示不限类型。
	Kinds []string
	// MinDate 仅返回创建时间不早于该值的消息。
	MinDate *time.Time
	// MaxDate 仅返回创建时间早于该值的消息。
	MaxDate *time.Time
	// BeforeSeq 仅返回 SeqId 小于该值的消息；零值表示从最新消息开始。
	BeforeSeq int
	// Limit 是本页最多返回的结果数量。
	Limit int
}
