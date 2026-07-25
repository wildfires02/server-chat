package types

import (
	"database/sql/driver"
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
	ObjHeader `bson:",inline"`

	// Topic 的状态：正常 (ok)、暂停、已删除
	State   ObjState
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
	// 如果消息被删除，删除它们的最后一次操作的顺序 id
	DelId int

	// Topic 订阅者数量。
	SubCnt int

	Public  any
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
	User  string
	DelId int
}

// 消息是存储的 {data} 消息
type Message struct {
	ObjHeader `bson:",inline"`
	DeletedAt *time.Time `json:"DeletedAt,omitempty" bson:",omitempty"`

	// 硬删除操作的 ID
	DelId int `json:"DelId,omitempty" bson:",omitempty"`
	// 将此消息标记为软删除的用户列表
	DeletedFor []SoftDelete `json:"DeletedFor,omitempty" bson:",omitempty"`
	SeqId      int
	Topic      string
	// 发送者的用户 ID（字符串形式，无 'usr' 前缀），可能为空。
	From    string
	Head    KVMap `json:"Head,omitempty" bson:",omitempty"`
	Content any
}

// Range 是消息 SeqID 的范围。低端为包含（闭区间），高端为不包含（开区间）：[Low, Hi)。
// 如果范围只包含一个 ID，则 Hi 设为 0
type Range struct {
	Low int
	Hi  int `json:"Hi,omitempty" bson:",omitempty"`
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
	ObjHeader   `bson:",inline"`
	Topic       string
	DeletedFor  string
	DelId       int
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
	User            Uid
	Topic           string
	IfModifiedSince *time.Time
	// 基于 ID 的查询参数：消息
	Since  int
	Before int
	// 通用参数
	Limit int
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


