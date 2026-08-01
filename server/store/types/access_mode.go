// Package types 提供领域模型及持久化访问层。
package types

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
)

// AccessMode 定义访问控制模式位图标志。
type AccessMode uint

// 各类访问权限标志位常量
const (
	ModeJoin    AccessMode = 1 << iota // 用户可加入 Topic 频道 (J:1)
	ModeRead                           // 用户可接收广播消息 ({data}, {info}) (R:2)
	ModeWrite                          // 用户可发送/发布消息 {pub} (W:4)
	ModePres                           // 用户可接收在线状态/Presence 变更通知 (P:8)
	ModeApprove                        // 用户可审批新成员加入或剔除现有成员 (A:16, 0x10)
	ModeShare                          // 用户可邀请/分享给新成员 (S:32, 0x20)
	ModeDelete                         // 用户可硬删除消息 (D:64, 0x40)
	ModeOwner                          // 用户为 Topic 群主/所有者，拥有最高完全控制权限 (O:128, 0x80)
	ModeUnset                          // 非零值，表示未知或未定义的模式 (:256, 0x100)，用于区分 ModeNone

	ModeNone AccessMode = 0 // 无访问权限 (N:0)

	// 普通用户对 Topic 的默认访问权限 ("JRWPS", 47, 0x2F)
	ModeCPublic AccessMode = ModeJoin | ModeRead | ModeWrite | ModePres | ModeShare
	// 用户对 'me' 和 'fnd' Topic 的订阅权限 ("JPS", 41, 0x29)
	ModeCMeFnd AccessMode = ModeJoin | ModePres | ModeShare
	// 用户对 'slf' Topic 的订阅权限 ("JRWDO", 199, 0xC7)
	ModeCSelf = ModeJoin | ModeRead | ModeWrite | ModeDelete | ModeOwner
	// 群主所有者对普通 Topic 的完全管理权限 ("JRWPASDO", 255, 0xFF)
	ModeCFull AccessMode = ModeJoin | ModeRead | ModeWrite | ModePres | ModeApprove | ModeShare | ModeDelete | ModeOwner
	// 默认 P2P 会话访问权限 ("JRWPA", 31, 0x1F)
	ModeCP2P AccessMode = ModeJoin | ModeRead | ModeWrite | ModePres | ModeApprove
	// 允许硬删除消息时的 P2P 会话访问权限 ("JRWPAD", 95, 0x5F)
	ModeCP2PD AccessMode = ModeJoin | ModeRead | ModeWrite | ModePres | ModeApprove | ModeDelete
	// 用户默认 Auth 验证级别下的访问模式 ("JRWPAS", 63, 0x3F)
	ModeCAuth AccessMode = ModeCP2P | ModeCPublic
	// Topic 只读访问模式 ("JR", 3)
	ModeCReadOnly = ModeJoin | ModeRead
	// 超级管理员 root 用户对 'sys' Topic 的访问模式 ("JRWPD", 79, 0x4F)
	ModeCSys = ModeJoin | ModeRead | ModeWrite | ModePres | ModeDelete
	// 频道发布者访问模式：授权发布内容，仅限受邀 ("RWPD", 78, 0x4E)
	ModeCChnWriter = ModeRead | ModeWrite | ModePres | ModeShare
	// 频道读者订阅访问模式 ("JRP", 11, 0xB)
	ModeCChnReader = ModeJoin | ModeRead | ModePres

	// 管理员：可修改访问权限模式的用户 ("OA", 144, 0x90)
	ModeCAdmin = ModeOwner | ModeApprove
	// 分享者：能接收权限模式变更通知的用户标志 ("OAS", 176, 0xB0)
	ModeCSharer = ModeCAdmin | ModeShare

	// 无效模式标识
	ModeInvalid AccessMode = 0x100000

	// 所有合法掩码位的位图 (不含 ModeInvalid 与 ModeUnset) = 0xFF, 255
	ModeBitmask AccessMode = ModeJoin | ModeRead | ModeWrite | ModePres | ModeApprove | ModeShare | ModeDelete | ModeOwner
)

// MarshalText 将 AccessMode 转换为 ASCII 字节切片。
func (m AccessMode) MarshalText() ([]byte, error) {
	if m == ModeNone {
		return []byte{'N'}, nil
	}

	if m == ModeInvalid {
		return nil, errors.New("AccessMode invalid")
	}

	res := []byte{}
	modes := []byte{'J', 'R', 'W', 'P', 'A', 'S', 'D', 'O'}
	for i, chr := range modes {
		if (m & (1 << uint(i))) != 0 {
			res = append(res, chr)
		}
	}
	return res, nil
}

// ParseAcs 从字节数组解析 AccessMode。
func ParseAcs(b []byte) (AccessMode, error) {
	m0 := ModeUnset

Loop:
	for i := range b {
		switch b[i] {
		case 'J', 'j':
			m0 |= ModeJoin
		case 'R', 'r':
			m0 |= ModeRead
		case 'W', 'w':
			m0 |= ModeWrite
		case 'A', 'a':
			m0 |= ModeApprove
		case 'S', 's':
			m0 |= ModeShare
		case 'D', 'd':
			m0 |= ModeDelete
		case 'P', 'p':
			m0 |= ModePres
		case 'O', 'o':
			m0 |= ModeOwner
		case 'N', 'n':
			if m0 != ModeUnset {
				return ModeUnset, errors.New("AccessMode: access N cannot be combined with any other")
			}
			m0 = ModeNone //N表示表示式无访问权限，所有位清除
			break Loop
		default:
			return ModeUnset, errors.New("AccessMode: invalid character '" + string(b[i]) + "'")
		}
	}

	return m0, nil
}

// UnmarshalText 以字节切片形式解析访问模式字符串。
// 如果字符串为空或无效，不改变模式。
func (m *AccessMode) UnmarshalText(b []byte) error {
	m0, err := ParseAcs(b)
	if err != nil {
		return err
	}

	if m0 != ModeUnset {
		*m = (m0 & ModeBitmask)
	}
	return nil
}

// String 返回 AccessMode 的字符串表示。
func (m AccessMode) String() string {
	res, err := m.MarshalText()
	if err != nil {
		return ""
	}
	return string(res)
}

// MarshalJSON 将 AccessMode 转换为带引号的字符串。
func (m AccessMode) MarshalJSON() ([]byte, error) {
	res, err := m.MarshalText()
	if err != nil {
		return nil, err
	}

	return append(append([]byte{'"'}, res...), '"'), nil
}

// UnmarshalJSON 从带引号的字符串中读取 AccessMode。
func (m *AccessMode) UnmarshalJSON(b []byte) error {
	if b[0] != '"' || b[len(b)-1] != '"' {
		return errors.New("syntax error")
	}

	return m.UnmarshalText(b[1 : len(b)-1])
}

// Scan 是 sql.Scanner 接口的实现。它期望值为 ASCII 字符串的字节切片表示。
func (m *AccessMode) Scan(val any) error {
	if bb, ok := val.([]byte); ok {
		return m.UnmarshalText(bb)
	}
	return errors.New("scan failed: data is not a byte slice")
}

// Value 是 sql.driver.Valuer 接口的实现。
func (m AccessMode) Value() (driver.Value, error) {
	res, err := m.MarshalText()
	if err != nil {
		return "", err
	}
	return string(res), nil
}

// BetterThan checks if grant mode allows more 权限 than requested in want mode.
func (grant AccessMode) BetterThan(want AccessMode) bool {
	return ModeBitmask&grant&^want != 0
}

// BetterEqual checks if grant mode allows all 权限 requested in want mode.
func (grant AccessMode) BetterEqual(want AccessMode) bool {
	return ModeBitmask&grant&want == want
}

// Delta 计算两种模式之间的差异字符串 old.Delta(new)。JRPAS -> JRWS: "+W-PA"
// 零差异为空字符串 ""
func (o AccessMode) Delta(n AccessMode) string {
	// 被移除的位，存在于 'old' 但缺失于 'new' -> '-'
	var removed string
	if o2n := ModeBitmask & o &^ n; o2n > 0 {
		removed = o2n.String()
		if removed != "" {
			removed = "-" + removed
		}
	}

	// 新增的位，存在于 'n' 但缺失于 'o' -> '+'
	var added string
	if n2o := ModeBitmask & n &^ o; n2o > 0 {
		added = n2o.String()
		if added != "" {
			added = "+" + added
		}
	}
	return added + removed
}

// ApplyMutation 设置或修改访问模式：
// * 如果 `mutation` 包含 '+' 或 '-'，尝试应用增量变更到 `m`。
// * 否则，视为赋值操作。
func (m *AccessMode) ApplyMutation(mutation string) error {
	if mutation == "" {
		return nil
	}
	if strings.ContainsAny(mutation, "+-") {
		return m.ApplyDelta(mutation)
	}
	return m.UnmarshalText([]byte(mutation))
}

// ApplyDelta 将 acs 增量应用到 AccessMode。
// Delta 的格式与 AccessMode.Delta 生成的格式相同。
// 例如 JPRA.ApplyDelta(-PR+W) -> JWA。
func (m *AccessMode) ApplyDelta(delta string) error {
	if delta == "" || delta == "N" {
		// 无更新。
		return nil
	}
	m0 := *m
	for next := 0; next+1 < len(delta) && next >= 0; {
		ch := delta[next]
		end := strings.IndexAny(delta[next+1:], "+-")
		var chunk string
		if end >= 0 {
			end += next + 1
			chunk = delta[next+1 : end]
		} else {
			chunk = delta[next+1:]
		}
		next = end
		upd, err := ParseAcs([]byte(chunk))
		if err != nil {
			return err
		}
		switch ch {
		case '+':
			if upd != ModeUnset {
				m0 |= upd & ModeBitmask
			}
		case '-':
			if upd != ModeUnset {
				m0 &^= upd & ModeBitmask
			}
		default:
			return errors.New("Invalid acs delta string: '" + delta + "'")
		}
	}
	*m = m0
	return nil
}

// IsJoiner 检查 joiner 标志 J 是否已设置。
func (m AccessMode) IsJoiner() bool {
	return m&ModeJoin != 0
}

// IsOwner 检查 owner 位 O 是否已设置。
func (m AccessMode) IsOwner() bool {
	return m&ModeOwner != 0
}

// IsApprover 检查 approver A 位是否已设置。
func (m AccessMode) IsApprover() bool {
	return m&ModeApprove != 0
}

// IsAdmin 检查是否设置了 owner O 或 approver A 标志。
func (m AccessMode) IsAdmin() bool {
	return m.IsOwner() || m.IsApprover()
}

// IsSharer 检查是否设置了 approver A 或 sharer S 或 owner O 标志。
func (m AccessMode) IsSharer() bool {
	return m.IsAdmin() || (m&ModeShare != 0)
}

// IsWriter 检查是否允许发布（writer 标志 W 已设置）。
func (m AccessMode) IsWriter() bool {
	return m&ModeWrite != 0
}

// IsReader 检查 reader 标志 R 是否已设置。
func (m AccessMode) IsReader() bool {
	return m&ModeRead != 0
}

// IsPresencer 检查用户是否接收在线状态更新（P 标志已设置）。
func (m AccessMode) IsPresencer() bool {
	return m&ModePres != 0
}

// IsDeleter checks if 用户 can hard-delete 消息 (D flag is set).
func (m AccessMode) IsDeleter() bool {
	return m&ModeDelete != 0
}

// IsZero 检查是否未设置任何标志。
func (m AccessMode) IsZero() bool {
	return m == ModeNone
}

// IsInvalid 检查模式是否无效。
func (m AccessMode) IsInvalid() bool {
	return m == ModeInvalid
}

// IsDefined 检查模式是否已定义：非无效且非未设置。
// ModeNone 被视为已定义。
func (m AccessMode) IsDefined() bool {
	return m != ModeInvalid && m != ModeUnset
}

// DefaultAccess 是每个 Topic 的默认访问模式
type DefaultAccess struct {
	// Auth 保存认证。
	Auth AccessMode
	// Anon 保存Anon。
	Anon AccessMode
}

// Scan 是 Scanner 接口的实现，以便可以从 SQL 数据库读取值
// 它假定值已序列化并以 JSON 格式存储
func (da *DefaultAccess) Scan(val any) error {
	return json.Unmarshal(val.([]byte), da)
}

// Value 实现 sql 的 driver.Valuer 接口。
func (da DefaultAccess) Value() (driver.Value, error) {
	return json.Marshal(da)
}
