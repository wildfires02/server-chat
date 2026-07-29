// Package types 提供领域模型及持久化访问层。
package types

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"time"
)

// MessageHeaders 需要附加 Scan() 方法。
type KVMap map[string]any

// Scan 实现 sql.Scanner 接口。
func (kvm *KVMap) Scan(val any) error {
	if val == nil {
		kvm = nil
		return nil
	}
	return json.Unmarshal(val.([]byte), kvm)
}

// Value 实现 sql 的 driver.Valuer 接口。
func (kvm KVMap) Value() (driver.Value, error) {
	return json.Marshal(kvm)
}

// Topic 存储在数据库中。Topic 的名称为 Id
type Topic struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	ObjHeader `bson:",inline"`

	// Topic 的状态：正常 (ok)、暂停、已删除
	State ObjState
	// StateAt 保存状态At时间。
	StateAt *time.Time `json:"StateAt,omitempty" bson:",omitempty"`

	// 最后一次消息通过 Topic 的时间戳
	TouchedAt time.Time

	// 表示该 Topic 是一个频道。
	UseBt bool

	// Topic 所有者。可能为零
	Owner string

	// Topic 的默认访问权限
	Access DefaultAccess

	// 服务器发放的顺序 ID
	SeqId int
	// ClusterOwner 是最后一次成功持久化消息时持有 Topic 写入权的集群节点。
	ClusterOwner string
	// ClusterEpoch 是 ClusterOwner 对应的全局 Cluster View Revision。
	ClusterEpoch int64
	// 如果消息被删除，删除它们的最后一次操作的顺序 id
	DelId int

	// Topic 订阅者数量。
	SubCnt int

	// Public 保存公开资料。
	Public any
	// Trusted 保存可信资料。
	Trusted any

	// 用于查找该 Topic 的索引标签。
	Tags StringSlice

	// 辅助键值对映射.
	Aux KVMap `json:"Aux,omitempty" bson:",omitempty"`

	// 反序列化的临时参数
	perUser map[Uid]*perUserData // 从订阅反序列化
}

// GiveAccess 更新指定用户的访问模式。
func (t *Topic) GiveAccess(uid Uid, want, given AccessMode) {
	if t.perUser == nil {
		t.perUser = make(map[Uid]*perUserData, 1)
	}

	pud := t.perUser[uid]
	if pud == nil {
		pud = &perUserData{}
	}

	pud.want = want
	pud.given = given

	t.perUser[uid] = pud
	if want&given&ModeOwner != 0 && t.Owner == "" {
		t.Owner = uid.String()
	}
}

// SetPrivate 更新指定用户的私有值。
func (t *Topic) SetPrivate(uid Uid, private any) {
	if t.perUser == nil {
		t.perUser = make(map[Uid]*perUserData, 1)
	}
	pud := t.perUser[uid]
	if pud == nil {
		pud = &perUserData{}
	}
	pud.private = private
	t.perUser[uid] = pud
}

// GetPrivate 返回指定用户的私有值。
func (t *Topic) GetPrivate(uid Uid) (private any) {
	if t.perUser == nil {
		return
	}
	pud := t.perUser[uid]
	if pud == nil {
		return
	}
	private = pud.private
	return
}

// GetAccess 返回指定用户的访问模式。
func (t *Topic) GetAccess(uid Uid) (mode AccessMode) {
	if t.perUser == nil {
		return
	}
	pud := t.perUser[uid]
	if pud == nil {
		return
	}
	mode = pud.given & pud.want
	return
}

// SoftDelete 是软删除的单条数据库记录。
type SoftDelete struct {
	// User 指示是否启用或满足用户。
	User string
	// DelId 保存Del标识。
	DelId int
}

// 消息是存储的 {data} 消息
type Message struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	ObjHeader `bson:",inline"`
	// DeletedAt 保存DeletedAt时间。
	DeletedAt *time.Time `json:"DeletedAt,omitempty" bson:",omitempty"`

	// 硬删除操作的 ID
	DelId int `json:"DelId,omitempty" bson:",omitempty"`
	// 将此消息标记为软删除的用户列表
	DeletedFor []SoftDelete `json:"DeletedFor,omitempty" bson:",omitempty"`
	// SeqId 保存序列号标识。
	SeqId int
	// Topic 保存Topic。
	Topic string
	// 发送者的用户 ID（字符串形式，无 'usr' 前缀），可能为空。
	From string
	// ClientId 是客户端生成、在同一 Topic 和发送者范围内稳定的发布幂等键。
	ClientId string `json:"ClientId,omitempty" bson:",omitempty"`
	// ClientKey 是由 From 和 ClientId 哈希生成的数据库唯一索引值。
	ClientKey string `json:"ClientKey,omitempty" bson:",omitempty"`
	// ClusterId 标识产生本次写入令牌的逻辑集群，仅参与服务端持久化校验。
	ClusterId string `json:"-" bson:"-"`
	// ClusterEpoch 是 etcd 线性一致 Cluster View 的 Revision，用作全局 fencing token。
	ClusterEpoch int64 `json:"-" bson:"-"`
	// ClusterOwner 是当前 Cluster View 通过一致性哈希计算出的 Topic Owner 节点。
	ClusterOwner string `json:"-" bson:"-"`
	// Head 同时保存客户端自定义头和服务端管理的 x-* 消息元数据。
	Head KVMap `json:"Head,omitempty" bson:",omitempty"`
	// Content 是纯文本字符串或经过验证的 Drafty 文档。
	Content any
	// SearchText 是从 Content 提取的规范化纯文本，仅用于服务端全文搜索。
	// 它不参与客户端协议序列化，也不能代替原始 Content。
	SearchText string `json:"SearchText,omitempty" bson:",omitempty"`
}

// ScheduledMessage 是尚未进入 Topic 消息序列的持久化定时消息。
// PublishAt 到达后才分配 SeqId，因此不会在同步游标中制造空洞或乱序。
type ScheduledMessage struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	ObjHeader `bson:",inline"`

	// Topic 是目标会话的规范化名称。
	Topic string
	// From 是发送者的内部 UID 字符串。
	From string
	// ClientId 是发送者范围内的定时发布幂等键。
	ClientId string
	// NoEcho 指示投递时是否跳过原发起会话。
	NoEcho bool
	// PublishAt 是队列记录允许进入普通消息发布流程的时间。
	PublishAt time.Time
	// Head 是入队时已完成权限和语义校验的消息头快照。
	Head KVMap `json:"Head,omitempty" bson:",omitempty"`
	// Content 是入队时已完成 Drafty 校验的正文快照。
	Content any
	// AttachmentURLs 在投递普通消息时用于建立最终的消息附件关联。
	AttachmentURLs StringSlice `json:"AttachmentURLs,omitempty" bson:",omitempty"`
	// Attachments 保存文件 ID，用于保护待投递媒体不被垃圾回收。
	Attachments StringSlice `json:"Attachments,omitempty" bson:",omitempty"`
}

// MessageClientKey 生成不暴露发送者身份的持久化幂等索引键。
func MessageClientKey(from Uid, clientID string) string {
	if from.IsZero() || clientID == "" {
		return ""
	}
	raw, _ := from.MarshalBinary()
	sum := sha256.Sum256(append(raw, clientID...))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ClusterFenceKey 将逻辑集群 ID 转换为长度固定的数据库全局 fencing 键。
// 使用摘要可以让任意合法 cluster_id 都落在 SQL kvmeta 的 64 字符键长度内。
func ClusterFenceKey(clusterID string) string {
	sum := sha256.Sum256([]byte(clusterID))
	return "cluster.fence." + base64.RawURLEncoding.EncodeToString(sum[:])
}

// HasClusterFence 判断消息是否携带完整的集群写入令牌。
func (msg *Message) HasClusterFence() bool {
	return msg != nil && msg.ClusterId != "" && msg.ClusterEpoch > 0 && msg.ClusterOwner != ""
}

// HasAnyClusterFenceField 判断消息是否设置过任一集群写入令牌字段。
// 适配器用它区分 standalone 写入和缺字段的损坏集群写入。
func (msg *Message) HasAnyClusterFenceField() bool {
	return msg != nil && (msg.ClusterId != "" || msg.ClusterEpoch != 0 || msg.ClusterOwner != "")
}

// InitClientKey 在消息带 cid 时初始化持久化幂等索引键。
func (msg *Message) InitClientKey() {
	if msg.ClientKey == "" {
		msg.ClientKey = MessageClientKey(ParseUid(msg.From), msg.ClientId)
	}
}

// Range 是消息 SeqID 的范围。低端为包含（闭区间），高端为不包含（开区间）：[Low, Hi)。
// 如果范围只包含一个 ID，则 Hi 设为 0
type Range struct {
	// Low 保存Low。
	Low int
	// Hi 保存Hi。
	Hi int `json:"Hi,omitempty" bson:",omitempty"`
}

// RangeSorter 是 'sort' 包所需的辅助类型。
type RangeSorter []Range

// Len 是范围的长度。
func (rs RangeSorter) Len() int {
	return len(rs)
}

// Swap 交换切片中的两个元素。
func (rs RangeSorter) Swap(i, j int) {
	rs[i], rs[j] = rs[j], rs[i]
}

// Less 是比较器。先按 Low 升序排序，再按 Hi 降序排序
func (rs RangeSorter) Less(i, j int) bool {
	if rs[i].Low < rs[j].Low {
		return true
	}
	if rs[i].Low == rs[j].Low {
		return rs[i].Hi >= rs[j].Hi
	}
	return false
}

// 归一化范围 - 移除重叠：[1..4],[2..4],[5..7] -> [1..7]。
// 范围应已排序。
// 范围为闭区间-闭区间，即 [1..3] -> 1, 2, 3。
func (rs RangeSorter) Normalize() RangeSorter {
	if ll := rs.Len(); ll > 1 {
		prev := 0
		for i := 1; i < ll; i++ {
			if rs[prev].Low == rs[i].Low {
				// 较早的范围保证比较晚的范围更宽或相等，
				// 将两个范围合并为一个（不做任何操作）
				continue
			}
			// 检查完全或部分重叠
			if rs[prev].Hi > 0 && rs[prev].Hi+1 >= rs[i].Low {
				// 部分重叠
				if rs[prev].Hi < rs[i].Hi {
					rs[prev].Hi = rs[i].Hi
				}
				// 否则下一个范围完全在前一个范围内，不做任何操作即可消费。
				continue
			}
			// 无重叠
			prev++
		}
		rs = rs[:prev+1]
	}

	return rs
}

// 将 int 值切片转换为范围切片。
// int 切片必须按低到高排序。
func SliceToRanges(in []int) []Range {
	if len(in) == 0 {
		return nil
	}

	var out []Range
	for _, id := range in {
		size := len(out)

		if size == 0 {
			out = append(out, Range{Low: id})
			continue
		}

		prev := &out[size-1]
		if (prev.Hi == 0 && (id != prev.Low+1)) || (id > prev.Hi) {
			// 新范围。
			out = append(out, Range{Low: id})
		} else {
			// 扩展现有范围。
			prev.Hi = id + 1
		}
	}
	return out
}

// DelMessage 是已删除消息范围的日志条目。
type DelMessage struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	ObjHeader `bson:",inline"`
	// Topic 保存Topic。
	Topic string
	// DeletedFor 保存DeletedFor。
	DeletedFor string
	// DelId 保存Del标识。
	DelId int
	// SeqIdRanges 保存序列号标识Ranges列表。
	SeqIdRanges []Range

	// 删除此值之后的消息。未序列化。
	newerThan *time.Time
}

// GetNewerThan 返回 newerThan 删除查询参数。
func (dm *DelMessage) GetNewerThan() *time.Time {
	return dm.newerThan
}

// SetNewerThan 设置 newerThan 删除查询参数。
func (dm *DelMessage) SetNewerThan(t time.Time) {
	dm.newerThan = &t
}

// QueryOpt 是查询选项，[since, before] - 两端均为闭区间
type QueryOpt struct {
	// 订阅查询
	User Uid
	// Topic 保存Topic。
	Topic string
	// IfModifiedSince 保存IfModifiedSince。
	IfModifiedSince *time.Time
	// 基于 ID 的查询参数：消息
	Since int
	// Before 保存Before。
	Before int
	// 通用参数
	Limit int
	// Forward 指示按 SeqId 升序读取，用于断线同步追赶。
	Forward bool
	// ID 范围。
	IdRanges []Range
}

// TopicCat 是 Topic 类别的枚举。
type TopicCat int

const (
	// TopicCatMe 表示 'me' Topic 的值。
	TopicCatMe TopicCat = iota
	// TopicCatFnd 表示 'fnd' Topic 的值。
	TopicCatFnd
	// TopicCatP2P 表示 'p2p' Topic 的值。
	TopicCatP2P
	// TopicCatGrp 表示群组 Topic 的值。
	TopicCatGrp
	// TopicCatSys 是系统 Topic 的常量。
	TopicCatSys
	// TopicCatSlf 是 'self' Topic 的常量，即用于保存消息和笔记的 Topic。
	TopicCatSlf
)

// GetTopicCat 根据 Topic 名称返回 Topic 类别。
func GetTopicCat(name string) TopicCat {
	switch name[:3] {
	case "usr":
		return TopicCatMe
	case "p2p":
		return TopicCatP2P
	case "grp", "chn":
		return TopicCatGrp
	case "fnd":
		return TopicCatFnd
	case "sys":
		return TopicCatSys
	case "slf":
		return TopicCatSlf
	default:
		panic("invalid topic type for name '" + name + "'")
	}
}

// IsEphemeralTopic 检查 Topic 是否为临时性的，即它是对用户的引用，
// 它不存储在 'Topic' 表中，如 'me' 或 'fnd' Topic。
func IsEphemeralTopic(topic string) bool {
	cat := GetTopicCat(topic)
	return cat == TopicCatMe || cat == TopicCatFnd
}
