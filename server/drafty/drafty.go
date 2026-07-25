// Package drafty 包含 Drafty 到纯文本转换的工具函数。
package drafty

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

const (
	// 预览中样式载荷的最大大小（字节）。
	maxDataSize = 128
	// 预览中载荷字段的最大数量。
	maxDataCount = 8
)

var (
	errUnrecognizedContent = errors.New("content unrecognized")
	errInvalidContent      = errors.New("invalid format")
)

type style struct {
	Tp     string `json:"tp,omitempty"`
	At     int    `json:"at,omitempty"`
	Length int    `json:"len,omitempty"`
	Key    int    `json:"key,omitempty"`
}

type entity struct {
	Tp   string         `json:"tp,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

type document struct {
	Txt string   `json:"txt,omitempty"`
	Fmt []style  `json:"fmt,omitempty"`
	Ent []entity `json:"ent,omitempty"`

	// 解析出的字素簇。
	gc *graphemes
}

type span struct {
	tp   string
	at   int
	end  int
	key  int
	data map[string]any
}

type node struct {
	gc       *graphemes
	sp       *span
	children []*node
}

type previewState struct {
	drafty    *document
	maxLength int
	keymap    map[int]int
}

// Preview 将 Drafty 缩短到指定长度（以字素为单位），移除引用文本、前导换行符，
// 并压缩实体中的大内容，使其适合单行预览，
// 例如用于推送通知显示。
// 返回值为编码为 JSON 字符串的 Drafty 文档。
func Preview(content any, length int) (string, error) {
	doc, err := decodeAsDrafty(content)
	if err != nil {
		return "", err
	}
	if doc == nil {
		return "", nil
	}

	tree, err := toTree(doc)
	if err != nil {
		return "", err
	}
	if tree == nil {
		return "", nil
	}

	state := previewState{
		drafty: &document{
			Fmt: make([]style, 0, len(doc.Fmt)),
			Ent: make([]entity, 0, len(doc.Ent)),
		},
		maxLength: length,
		keymap:    make(map[int]int),
	}

	if err = previewFormatter(tree, &state); err != nil {
		return "", err
	}

	state.drafty.Txt = state.drafty.gc.string()
	data, err := json.Marshal(state.drafty)
	return string(data), err
}

type plainTextState struct {
	txt string
}

// PlainText 将 drafty 文档转换为带有一些基本 markdown 风格格式的纯文本。
// 已弃用：新开发请使用 Preview。
func PlainText(content any) (string, error) {
	doc, err := decodeAsDrafty(content)
	if err != nil {
		return "", err
	}
	if doc == nil {
		return "", nil
	}

	tree, err := toTree(doc)
	if err != nil {
		return "", err
	}

	state := plainTextState{}

	err = plainTextFormatter(tree, &state)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(state.txt)), nil
}

// styleToSpan 将 Drafty 样式转换为内部表示。
func (s *span) styleToSpan(in *style) error {
	s.tp = in.Tp
	s.at = in.At
	s.end = in.Length
	if s.end < 0 {
		return errInvalidContent
	}
	s.end += s.at

	if s.tp == "" {
		s.key = in.Key
		if s.key < 0 {
			return errInvalidContent
		}
	}

	return nil
}

type spanfmt struct {
	dec    string
	isVoid bool
}

// Drafty 标签的纯文本格式。仅需列出非空标签。
var tags = map[string]spanfmt{
	"BR": {"\n", true},
	"CO": {"`", false},
	"DL": {"~", false},
	"EM": {"_", false},
	"EX": {"", true},
	"ST": {"*", false},
}

// 应用于树节点的格式化器类型。
type formatter func(n *node, state any) error

// toTree 将 drafty 文档转换为格式化片段的树。
// 树的每个节点都统一格式化。
func toTree(drafty *document) (*node, error) {
	if len(drafty.Fmt) == 0 {
		return &node{gc: drafty.gc}, nil
	}

	textLen := drafty.gc.length()

	var spans []*span
	for i := range drafty.Fmt {
		s := span{}
		if err := s.styleToSpan(&drafty.Fmt[i]); err != nil {
			return nil, err
		}
		if s.at < -1 || s.end > textLen {
			return nil, errInvalidContent
		}

		// 将实体反归一化为片段。
		if s.tp == "" && len(drafty.Ent) > 0 {
			if s.key < 0 || s.key >= len(drafty.Ent) {
				return nil, errInvalidContent
			}

			s.data = drafty.Ent[s.key].Data
			s.tp = drafty.Ent[s.key].Tp
		}
		if s.tp == "" && s.at == 0 && s.end == 0 && s.key == 0 {
			return nil, errUnrecognizedContent
		}
		spans = append(spans, &s)
	}

	// 先按起始索引升序排序，再按长度降序排序。
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].at == spans[j].at {
			// 较长的排在前面
			return spans[i].end > spans[j].end
		}
		return spans[i].at < spans[j].at
	})

	// 当片段重叠时丢弃第二个格式，如 '_first *second_ third*'。
	var filtered []*span
	end := -2
	for _, span := range spans {
		if span.at < end && span.end > end {
			continue
		}
		filtered = append(filtered, span)
		if span.end > end {
			end = span.end
		}
	}

	// 遍历片段数组。
	children, err := forEach(drafty.gc, 0, textLen, filtered)
	if err != nil {
		return nil, err
	}

	return &node{children: children}, nil
}

// forEach 递归遍历嵌套片段以形成树。
func forEach(g *graphemes, start, end int, spans []*span) ([]*node, error) {
	var result []*node

	// 处理范围，为每个范围调用迭代器。
	for i := 0; i < len(spans); i++ {
		sp := spans[i]

		if sp.at < 0 {
			// 附件
			result = append(result, &node{sp: sp})
			continue
		}
		// 添加样式片段开始前的无样式范围。
		if start < sp.at {
			result = append(result, &node{gc: g.slice(start, sp.at)})
			start = sp.at
		}

		// 获取当前片段内的所有子片段。
		var subspans []*span
		for si := i + 1; si < len(spans) && spans[si].at < sp.end; si++ {
			subspans = append(subspans, spans[si])
			i = si
		}

		if tags[sp.tp].isVoid {
			result = append(result, &node{sp: sp})
		} else {
			children, err := forEach(g, start, sp.end, subspans)
			if err != nil {
				return nil, err
			}
			result = append(result, &node{children: children, sp: sp})
		}
		start = sp.end
	}

	// 添加剩余的未格式化范围。
	if start < end {
		result = append(result, &node{gc: g.slice(start, end)})
	}

	return result, nil
}

// plainTextFormatter 将格式化片段的树转换为纯文本。
func plainTextFormatter(n *node, ctx any) error {
	if n.sp != nil && n.sp.tp == "QQ" {
		return nil
	}

	var text string
	if len(n.children) > 0 {
		state := &plainTextState{}
		for _, c := range n.children {
			if err := plainTextFormatter(c, state); err != nil {
				return err
			}
		}
		text = string(state.txt)
	} else {
		text = n.gc.string()
	}

	state := ctx.(*plainTextState)

	if n.sp == nil {
		state.txt += text
		return nil
	}

	switch n.sp.tp {
	case "ST", "EM", "DL", "CO":
		state.txt += tags[n.sp.tp].dec + text + tags[n.sp.tp].dec

	case "LN":
		if url, ok := nullableMapGet(n.sp.data, "url"); ok && url != text {
			state.txt += "[" + text + "](" + url + ")"
		} else {
			state.txt += text
		}

	case "MN", "HT":
		state.txt += text
	case "BR":
		state.txt += "\n"
	case "AU", "EX", "IM", "VD":
		name, ok := nullableMapGet(n.sp.data, "name")
		if !ok || name == "" {
			name = "?"
		}
		expand := map[string]string{"AU": "AUDIO", "EX": "FILE", "IM": "IMAGE", "VD": "VIDEO"}
		state.txt += "[" + expand[n.sp.tp] + " '" + name + "']"
	case "VC":
		state.txt += "[CALL]"
	default:
		state.txt += text
	}
	return nil
}

// previewFormatter 将格式化片段的树转换为缩短的 drafty 文档。
func previewFormatter(n *node, ctx any) error {

	state := ctx.(*previewState)
	at := state.drafty.gc.length()
	if at >= state.maxLength {
		// 已达到最大文档长度。
		return nil
	}

	if n.sp != nil {
		if n.sp.tp == "QQ" {
			// 跳过引用文本
			return nil
		}
		if n.sp.tp == "BR" && at == 0 {
			// 跳过前导换行符。
			return nil
		}
	}

	if len(n.children) > 0 {
		for _, c := range n.children {
			if err := previewFormatter(c, ctx); err != nil {
				return err
			}
		}
	} else {
		increment := n.gc.length()
		if increment > 0 {
			if at+increment > state.maxLength {
				increment = state.maxLength - at
			}
			if state.drafty.gc == nil {
				state.drafty.gc = prepareGraphemes("")
			}
			state.drafty.gc = state.drafty.gc.append(n.gc.slice(0, increment))
		}
	}

	end := state.drafty.gc.length()

	if n.sp != nil {
		fmt := style{}
		if n.sp.at < 0 {
			fmt.At = -1
		} else if at < end || tags[n.sp.tp].isVoid {
			fmt.At = at
			fmt.Length = end - at
		} else {
			return nil
		}

		if n.sp.data != nil {
			// 检查是否已经处理过此载荷。
			key, ok := state.keymap[n.sp.key]
			if !ok {
				// 未找到载荷，添加它。
				ent := entity{Tp: n.sp.tp, Data: copyLight(n.sp.data)}
				key = len(state.drafty.Ent)
				state.keymap[n.sp.key] = key
				state.drafty.Ent = append(state.drafty.Ent, ent)
			}
			fmt.Key = key
		} else {
			fmt.Tp = n.sp.tp
		}

		state.drafty.Fmt = append(state.drafty.Fmt, fmt)
	}
	return nil
}

// nullableMapGet 是一个辅助方法，从可能为 nil 的 map 中获取可能不存在的字符串。
func nullableMapGet(data map[string]any, key string) (string, bool) {
	if data == nil {
		return "", false
	}
	str, ok := data[key].(string)
	return str, ok
}

// decodeAsDrafty 将字符串或 map 转换为 Drafty 文档。
func decodeAsDrafty(content any) (*document, error) {
	if content == nil {
		return nil, nil
	}

	var drafty *document

	switch tmp := content.(type) {
	case string:
		drafty = &document{gc: prepareGraphemes(tmp)}
	case map[string]any:
		drafty = &document{}
		correct := 0
		if txt, ok := tmp["txt"].(string); ok {
			drafty.Txt = txt
			drafty.gc = prepareGraphemes(txt)
			correct++
		}
		if ifmt, ok := tmp["fmt"].([]any); ok {
			for i := range ifmt {
				st, err := decodeAsStyle(ifmt[i])
				if err != nil {
					return nil, err
				}
				if st != nil {
					drafty.Fmt = append(drafty.Fmt, *st)
				}
				correct++
			}
		}
		if ient, ok := tmp["ent"].([]any); ok {
			for i := range ient {
				ent, err := decodeAsEntity(ient[i])
				if err != nil {
					return nil, err
				}
				if ent != nil {
					drafty.Ent = append(drafty.Ent, *ent)
				}
				correct++
			}
		}
		// 必须至少存在一个 drafty 元素。
		if correct == 0 {
			return nil, errUnrecognizedContent
		}
	default:
		return nil, errUnrecognizedContent
	}

	return drafty, nil
}

// decodeAsStyle 将 map 转换为样式。
func decodeAsStyle(content any) (*style, error) {
	if content == nil {
		return nil, nil
	}

	tmp, ok := content.(map[string]any)
	if !ok {
		return nil, errUnrecognizedContent
	}

	var err error
	st := &style{}
	st.Tp, _ = tmp["tp"].(string)

	st.At, err = intFromNumeric(tmp["at"])
	if err != nil {
		return nil, err
	}

	st.Length, err = intFromNumeric(tmp["len"])
	if err != nil {
		return nil, err
	}

	if st.Tp == "" {
		st.Key, err = intFromNumeric(tmp["key"])
		if err != nil {
			return nil, err
		}
		if st.Key < 0 {
			return nil, errInvalidContent
		}
	}

	return st, nil
}

// decodeAsEntity 将 map 转换为实体。
func decodeAsEntity(content any) (*entity, error) {
	if content == nil {
		return nil, nil
	}

	tmp, ok := content.(map[string]any)
	if !ok {
		return nil, errUnrecognizedContent
	}

	ent := &entity{}

	ent.Tp, _ = tmp["tp"].(string)
	if ent.Tp == "" {
		return nil, errInvalidContent
	}

	ent.Data, _ = tmp["data"].(map[string]any)

	return ent, nil
}

// 实体字段的白名单。
var lightFields = []string{"mime", "name", "width", "height", "size", "url", "ref"}

// copyLight 复制实体，仅保留白名单中的键。
// 它还确保复制的值为固定长度的基本类型或足够短的字符串/字节切片，
// 且条目数量不过多。
func copyLight(in any) map[string]any {
	data, ok := in.(map[string]any)
	if !ok {
		return nil
	}

	result := map[string]any{}
	if len(data) > 0 {
		for _, key := range lightFields {
			if val, ok := data[key]; ok {
				if isFixedLengthType(val) {
					result[key] = val
				} else if l := getVariableTypeSize(val); l >= 0 && l < maxDataSize {
					result[key] = val
				}
			}

			if len(result) > maxDataCount {
				break
			}
		}
		if len(result) == 0 {
			result = nil
		}
	}
	return result
}

// intFromNumeric 是一个辅助方法，从任意数值类型的值中获取整数。
func intFromNumeric(num any) (int, error) {
	if num == nil {
		return 0, nil
	}
	switch i := num.(type) {
	case int:
		return i, nil
	case int16:
		return int(i), nil
	case int32:
		return int(i), nil
	case int64:
		return int(i), nil
	case float32:
		return int(i), nil
	case float64:
		return int(i), nil
	default:
		return 0, errInvalidContent
	}
}

// getVariableTypeSize 检查给定字段是否为字符串或字节切片，并返回其字节大小。
func getVariableTypeSize(x any) int {
	switch val := x.(type) {
	case string:
		return len(val)
	case []byte:
		return len(val)
	default:
		return -1
	}
}

// isFixedLengthType 检查给定值是否为固定大小的类型。
func isFixedLengthType(x any) bool {
	switch x.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16,
		uint32, uint64, float32, float64, complex64, complex128:
		return true
	default:
		return false
	}
}
