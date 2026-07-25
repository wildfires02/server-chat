// Package types 提供在数据库中持久化存储对象所需的核心数据类型与数据结构。
package types

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// NullValue 是一个 Unicode DEL 字符，表示该值正在被删除。
const NullValue = "\u2421"

// ObjHeader 是所有存储对象共享的头部。
type ObjHeader struct {
	// 使用 string 类型是为了解决 rethinkdb 对 uint64 的支持问题；
	// `bson:"_id"` 标签用于 mongodb 将其作为主键 '_id'。
	Id        string `bson:"_id"`
	id        Uid
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Uid 为头部字段分配 Uid。
func (h *ObjHeader) Uid() Uid {
	if h.id.IsZero() && h.Id != "" {
		h.id.UnmarshalText([]byte(h.Id))
	}
	return h.id
}

// SetUid 将给定的 Uid 分配给适当的头部字段。
func (h *ObjHeader) SetUid(uid Uid) {
	h.id = uid
	h.Id = uid.String()
}

// TimeNow 返回当前的 UTC 挂钟时间，精度到毫秒。
func TimeNow() time.Time {
	return time.Now().UTC().Round(time.Millisecond)
}

// TimeFormatRFC3339 是用于将时间戳格式化为 RFC3339 的格式字符串。
const TimeFormatRFC3339 = "2006-01-02T15:04:05.999"

// InitTimes 将头部中的 time.Time 变量初始化为当前时间。
func (h *ObjHeader) InitTimes() {
	if h.CreatedAt.IsZero() {
		h.CreatedAt = TimeNow()
	}
	h.UpdatedAt = h.CreatedAt
}

// MergeTimes 智能地将 time.Time 变量从 h2 复制到 h。
func (h *ObjHeader) MergeTimes(h2 *ObjHeader) {
	// 将创建时间设为最早的值
	if h.CreatedAt.IsZero() || (!h2.CreatedAt.IsZero() && h2.CreatedAt.Before(h.CreatedAt)) {
		h.CreatedAt = h2.CreatedAt
	}
	// 将更新时间设为最晚的值
	if h.UpdatedAt.Before(h2.UpdatedAt) {
		h.UpdatedAt = h2.UpdatedAt
	}
}

// StringSlice 的定义以便可以附加 Scanner 和 Valuer。
type StringSlice []string

// Scan 实现 sql.Scanner 接口。
func (ss *StringSlice) Scan(val any) error {
	if val == nil {
		return nil
	}
	return json.Unmarshal(val.([]byte), ss)
}

// Value 实现 sql/driver.Valuer 接口。
func (ss StringSlice) Value() (driver.Value, error) {
	return json.Marshal(ss)
}

// ObjState 表示对象状态信息，
// 如用户或 Topic 被暂停/软删除的指示。
type ObjState int

const (
	// StateOK 表示正常的用户或 Topic。
	StateOK ObjState = 0
	// StateSuspended 表示被暂停的用户或 Topic。
	StateSuspended ObjState = 10
	// StateDeleted 表示被软删除的用户或 Topic。
	StateDeleted ObjState = 20
	// StateUndefined 表示未显式设置的状态。
	StateUndefined ObjState = 30
)

// String 返回 ObjState 的字符串表示。
func (os ObjState) String() string {
	switch os {
	case StateOK:
		return "ok"
	case StateSuspended:
		return "susp"
	case StateDeleted:
		return "del"
	case StateUndefined:
		return "undef"
	}
	return ""
}

// NewObjState 将字符串解析为 ObjState。
func NewObjState(in string) (ObjState, error) {
	in = strings.ToLower(in)
	switch in {
	case "", "ok":
		return StateOK, nil
	case "susp":
		return StateSuspended, nil
	case "del":
		return StateDeleted, nil
	case "undef":
		return StateUndefined, nil
	}
	// 这是默认值。
	return StateOK, errors.New("failed to parse object state")
}

// MarshalJSON 将 ObjState 转换为带引号的字符串。
func (os ObjState) MarshalJSON() ([]byte, error) {
	return append(append([]byte{'"'}, []byte(os.String())...), '"'), nil
}

// UnmarshalJSON 从带引号的字符串中读取 ObjState。
func (os *ObjState) UnmarshalJSON(b []byte) error {
	if b[0] != '"' || b[len(b)-1] != '"' {
		return errors.New("syntax error")
	}
	state, err := NewObjState(string(b[1 : len(b)-1]))
	if err == nil {
		*os = state
	}
	return err
}

// Scan 是 sql.Scanner 接口的实现。它期望值为 ASCII 字符串的字节切片表示。
func (os *ObjState) Scan(val any) error {
	switch intval := val.(type) {
	case int64:
		*os = ObjState(intval)
		return nil
	}
	return errors.New("data is not an int64")
}

// Value 是 sql.driver.Valuer 接口的实现。
func (os ObjState) Value() (driver.Value, error) {
	return int64(os), nil
}
