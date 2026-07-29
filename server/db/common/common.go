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

// AuthRecord 保存认证Record的数据和运行状态。
type AuthRecord struct {
	// Unique 保存Unique。
	Unique string `json:"unique" bson:"_id"`
	// UserId 保存用户标识。
	UserId string `json:"userid"`
	// Scheme 保存Scheme。
	Scheme string `json:"scheme"`
	// AuthLvl 保存认证Lvl。
	AuthLvl auth.Level `json:"authLvl"`
	// Secret 保存密钥列表。
	Secret []byte `json:"secret"`
	// Expires 保存Expires。
	Expires time.Time `json:"expires"`
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

// NullableString 将空字符串转换为 SQL NULL，使可选唯一索引不发生空值冲突。
func NullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
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

// RankPeerSearch 计算用户名/别名和公开名称的稳定相关性分数。
// 用户只通过显式 alias Tag 被发现；群组和频道还允许按 Public.fn 搜索。
func RankPeerSearch(topic, query, aliasPrefix string, tags t.StringSlice, public any) (int, []string) {
	query = strings.ToLower(strings.TrimSpace(query))
	aliasPrefix = strings.ToLower(strings.TrimSpace(aliasPrefix))
	if query == "" {
		return 0, nil
	}

	score := 0
	var matched []string
	prefix := aliasPrefix + ":"
	for _, tag := range tags {
		normalized := strings.ToLower(tag)
		if aliasPrefix == "" || !strings.HasPrefix(normalized, prefix) {
			continue
		}
		alias := strings.TrimPrefix(normalized, prefix)
		switch {
		case alias == query:
			if score < 100 {
				score = 100
			}
		case strings.HasPrefix(alias, query):
			if score < 80 {
				score = 80
			}
		case strings.Contains(alias, query):
			if score < 70 {
				score = 70
			}
		default:
			continue
		}
		matched = append(matched, tag)
	}

	// 用户显示名称不属于公开用户名，避免仅凭昵称枚举账号。
	if !strings.HasPrefix(topic, "usr") {
		if fields, ok := public.(map[string]any); ok {
			if name, ok := fields["fn"].(string); ok {
				name = strings.ToLower(strings.TrimSpace(name))
				switch {
				case name == query && score < 60:
					score = 60
				case strings.HasPrefix(name, query) && score < 50:
					score = 50
				case strings.Contains(name, query) && score < 40:
					score = 40
				}
			}
		}
	}
	return score, matched
}

// EscapeLike 将用户输入转义为 SQL LIKE 模式中的普通字符。
func EscapeLike(value string) string {
	// 使用显式的 ! 转义符，避免依赖 MySQL SQL_MODE 或 PostgreSQL 字符串设置。
	value = strings.ReplaceAll(value, `!`, `!!`)
	value = strings.ReplaceAll(value, `%`, `!%`)
	return strings.ReplaceAll(value, `_`, `!_`)
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
