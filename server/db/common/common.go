// Package common 包含所有适配器使用的工具方法。
package common

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/store"
	t "chat/server/store/types"
)

type AuthRecord struct {
	Unique  string     `json:"unique" bson:"_id"`
	UserId  string     `json:"userid"`
	Scheme  string     `json:"scheme"`
	AuthLvl auth.Level `json:"authLvl"`
	Secret  []byte     `json:"secret"`
	Expires time.Time  `json:"expires"`
}

// SelectEarliestUpdatedSubs 从给定切片中选择查询条件下不超过指定数量的订阅。
// 当订阅数超过限制时，选择时间戳最早的订阅。
func SelectEarliestUpdatedSubs(subs []t.Subscription, opts *t.QueryOpt, maxResults int) []t.Subscription {
	limit := maxResults
	ims := time.Time{}
	if opts != nil {
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
		if opts.IfModifiedSince != nil {
			ims = *opts.IfModifiedSince
		}
	}

	// 无缓存管理且结果数未超过限制：返回全部。
	if ims.IsZero() && len(subs) <= limit {
		return subs
	}

	// 可能获取的订阅比需要的多，取修改时间最早的那些。
	// 按修改时间升序排序。
	sort.Slice(subs, func(i, j int) bool {
		return subs[i].LastModified().Before(subs[j].LastModified())
	})

	if !ims.IsZero() {
		// 只保留比 ims 更新的订阅。
		at := sort.Search(len(subs), func(i int) bool {
			return subs[i].LastModified().After(ims)
		})
		subs = subs[at:]
	}
	// 按限制截断切片。
	if len(subs) > limit {
		subs = subs[:limit]
	}

	return subs
}

// SelectLatestTime 从两个时间戳中选取较新的更新时戳。
func SelectLatestTime(t1, t2 time.Time) time.Time {
	if t1.Before(t2) {
		// 订阅最近未变更，使用用户的更新时间戳。
		return t2
	}

	return t1
}

// RangesToSql 将范围切片转换为 SQL BETWEEN 或 IN() 约束和参数。
func RangesToSql(in []t.Range) (string, []any) {
	if len(in) > 1 || in[0].Hi == 0 {
		var args []any
		for _, r := range in {
			if r.Hi == 0 {
				args = append(args, r.Low)
			} else {
				for i := r.Low; i < r.Hi; i++ {
					args = append(args, i)
				}
			}
		}

		return "IN (?" + strings.Repeat(",?", len(args)-1) + ")", args
	}

	// 针对单个范围 low..hi 的特殊情况进行优化。
	// SQL 的 BETWEEN 是闭区间，因此将 Hi 减 1。
	return "BETWEEN ? AND ?", []any{in[0].Low, in[0].Hi - 1}
}

// DisjunctionSql 将析取切片转换为 SQL HAVING 子句和参数。
func DisjunctionSql(req [][]string, fieldName string) (string, []any) {
	var args []any
	counts := make([]string, 0, len(req))
	for _, reqDisjunction := range req {
		// 至少必须存在一个标签。
		if len(reqDisjunction) == 0 {
			continue
		}
		counts = append(counts, "COUNT("+fieldName+" IN (?"+strings.Repeat(",?", len(reqDisjunction)-1)+") OR NULL)>=1")
		for _, tag := range reqDisjunction {
			args = append(args, tag)
		}
	}
	return "HAVING " + strings.Join(counts, " AND ") + " ", args
}

// FilterFoundTags 仅保留 setTags 中存在于索引中的标签。
func FilterFoundTags(setTags t.StringSlice, index map[string]struct{}) []string {
	foundTags := make([]string, 0, 1)
	for _, tag := range setTags {
		if _, ok := index[tag]; ok {
			foundTags = append(foundTags, tag)
		}
	}
	return foundTags
}

// ToJSON 在存储到 JSON 字段之前转换为 JSON。
func ToJSON(src any) []byte {
	if src == nil {
		return nil
	}

	jval, _ := json.Marshal(src)
	return jval
}

// FromJSON 从数据库反序列化 JSON 数据。
func FromJSON(src any) any {
	if src == nil {
		return nil
	}
	if bb, ok := src.([]byte); ok {
		var out any
		json.Unmarshal(bb, &out)
		return out
	}
	return nil
}

// UpdateByMap 将更新转换为列名和参数列表。
func UpdateByMap(update map[string]any) (cols []string, args []any) {
	for col, arg := range update {
		col = strings.ToLower(col)
		if col == "public" || col == "trusted" || col == "private" || col == "aux" {
			arg = ToJSON(arg)
		}
		cols = append(cols, col+"=?")
		args = append(args, arg)
	}
	return
}

// ExtractTags 如果 Tags 字段被更新，获取标签以便同步更新标签表。
func ExtractTags(update map[string]any) []string {
	var tags []string

	if val := update["Tags"]; val != nil {
		tags, _ = val.(t.StringSlice)
	}

	return []string(tags)
}

// EncodeUidString 将 int64 的解码字符串表示转换为 UID。
// UID 以解码的 int64 值存储。
func EncodeUidString(str string) t.Uid {
	unum, _ := strconv.ParseInt(str, 10, 64)
	return store.EncodeUid(unum)
}

// DecodeUidString 将 UID 字符串转换为 int64 表示。
// UID 以解码的 int64 值存储。
func DecodeUidString(str string) int64 {
	uid := t.ParseUid(str)
	return store.DecodeUid(uid)
}
