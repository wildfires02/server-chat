// Package drafty 包含 Drafty 到纯文本转换的工具函数。
package drafty

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

const (
	// 预览中样式载荷的最大大小（字节）。
	maxDataSize = 128
	// 预览中载荷字段的最大数量。
	maxDataCount = 8
)

var (
	// errUnrecognizedContent 保存errUnrecognized正文的共享实例或运行状态。
	errUnrecognizedContent = errors.New("content unrecognized")
	// errInvalidContent 保存errInvalid正文的共享实例或运行状态。
	errInvalidContent = errors.New("invalid format")
)

// ContentInfo 是服务端对 Drafty 消息的可信分类结果。客户端提供的 MIME、
// kind 和附件列表不能直接作为消息语义使用，必须由 Drafty 实体重新推导。
type ContentInfo struct {
	// Kind 是从正文实体推导出的 text、drafty、image、video、voice、audio 或 file。
	Kind string
	// Attachments 是媒体实体中去重后的带外文件引用。
	Attachments []string
	// MediaCount 是文档引用的媒体实体数量。
	MediaCount int
}

// supportedStyles 是服务器接受的 Drafty 样式白名单。
var supportedStyles = map[string]bool{
	"BR": true, "BQ": true, "CO": true, "DL": true, "EM": true,
	"FM": true, "HD": true, "HL": true, "PRE": true, "QQ": true,
	"RW": true, "SP": true, "ST": true, "UN": true,
}

// supportedEntities 是服务器接受的 Drafty 实体白名单。
var supportedEntities = map[string]bool{
	"AU": true, "BN": true, "CE": true, "EX": true, "FM": true,
	"HT": true, "IM": true, "LN": true, "MN": true, "VC": true,
	"VD": true,
}

// Analyze 校验 Drafty 的范围、实体类型和媒体元数据，并返回服务端推导的
// 消息类型及带外附件。字符串被识别为 text；Drafty 文档在没有媒体实体时
// 被识别为 drafty。
func Analyze(content any) (*ContentInfo, error) {
	doc, err := decodeAsDrafty(content)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errInvalidContent
	}
	if _, err = toTree(doc); err != nil {
		return nil, err
	}

	info := &ContentInfo{Kind: "drafty"}
	if text, ok := content.(string); ok {
		if text == "" {
			return nil, errInvalidContent
		}
		info.Kind = "text"
		return info, nil
	}

	refs := make(map[string]struct{})
	referencedEntities := make(map[int]bool)
	mediaKind := ""
	for _, st := range doc.Fmt {
		// 带 Tp 的格式项是样式；不带 Tp 的格式项通过 Key 引用实体。
		if st.Tp != "" && !supportedStyles[st.Tp] {
			return nil, fmt.Errorf("%w: unsupported style %q", errInvalidContent, st.Tp)
		}
		if st.Tp == "" {
			referencedEntities[st.Key] = true
		}
	}
	for index, ent := range doc.Ent {
		if !supportedEntities[ent.Tp] {
			return nil, fmt.Errorf("%w: unsupported entity %q", errInvalidContent, ent.Tp)
		}
		if !referencedEntities[index] {
			return nil, fmt.Errorf("%w: unreferenced entity %d", errInvalidContent, index)
		}
		if err = validateEntity(ent); err != nil {
			return nil, err
		}

		kind := ""
		switch ent.Tp {
		case "IM":
			kind = "image"
		case "VD":
			kind = "video"
		case "AU":
			kind = "audio"
			if voice, _ := ent.Data["voice"].(bool); voice {
				kind = "voice"
			}
		case "EX":
			kind = "file"
		}
		if kind == "" {
			continue
		}
		// 单一媒体实体可提升为对应消息类型；混合媒体仍归类为 Drafty。
		info.MediaCount++
		if mediaKind == "" {
			mediaKind = kind
		} else if mediaKind != kind {
			mediaKind = "drafty"
		}
		if ref, _ := ent.Data["ref"].(string); ref != "" {
			if _, found := refs[ref]; !found {
				refs[ref] = struct{}{}
				info.Attachments = append(info.Attachments, ref)
			}
		}
	}
	if info.MediaCount == 1 {
		info.Kind = mediaKind
	}
	if info.MediaCount == 0 && doc.Txt == "" {
		return nil, errInvalidContent
	}
	return info, nil
}

// validateEntity 校验媒体、文件和链接实体的关键元数据。
func validateEntity(ent entity) error {
	data := ent.Data
	switch ent.Tp {
	case "IM", "VD", "AU", "EX":
		if data == nil {
			return fmt.Errorf("%w: %s entity has no data", errInvalidContent, ent.Tp)
		}
		mimeType, _ := data["mime"].(string)
		if mimeType != "" {
			wantPrefix := map[string]string{
				"IM": "image/", "VD": "video/", "AU": "audio/",
			}[ent.Tp]
			if wantPrefix != "" && !strings.HasPrefix(strings.ToLower(mimeType), wantPrefix) {
				return fmt.Errorf("%w: %s entity has mismatched mime", errInvalidContent, ent.Tp)
			}
		}
		ref, _ := data["ref"].(string)
		_, hasInline := data["val"]
		// 媒体必须是已上传文件引用或内联载荷，不能只有客户端声明的 MIME。
		if ref == "" && !hasInline {
			return fmt.Errorf("%w: %s entity has neither ref nor val", errInvalidContent, ent.Tp)
		}
		for _, key := range []string{"size", "width", "height", "duration"} {
			if val, found := data[key]; found {
				n, convErr := intFromNumeric(val)
				if convErr != nil || n < 0 {
					return fmt.Errorf("%w: invalid %s", errInvalidContent, key)
				}
			}
		}
	case "LN":
		if url, _ := data["url"].(string); url == "" {
			return fmt.Errorf("%w: link has no url", errInvalidContent)
		}
	}
	return nil
}

// style 保存style的数据和运行状态。
type style struct {
	// Tp 保存Tp。
	Tp string `json:"tp,omitempty"`
	// At 保存At时间。
	At int `json:"at,omitempty"`
	// Length 保存Length。
	Length int `json:"len,omitempty"`
	// Key 保存键。
	Key int `json:"key,omitempty"`
}

// entity 保存entity的数据和运行状态。
type entity struct {
	// Tp 保存Tp。
	Tp string `json:"tp,omitempty"`
	// Data 按键索引数据。
	Data map[string]any `json:"data,omitempty"`
}

// document 保存document的数据和运行状态。
type document struct {
	// Txt 保存Txt。
	Txt string `json:"txt,omitempty"`
	// Fmt 保存Fmt列表。
	Fmt []style `json:"fmt,omitempty"`
	// Ent 保存Ent列表。
	Ent []entity `json:"ent,omitempty"`

	// 解析出的字素簇。
	gc *graphemes
}

// span 保存span的数据和运行状态。
type span struct {
	// tp 保存tp。
	tp string
	// at 保存at时间。
	at int
	// end 保存end。
	end int
	// key 保存键。
	key int
	// data 按键索引数据。
	data map[string]any
}

// node 保存节点的数据和运行状态。
type node struct {
	// gc 保存gc。
	gc *graphemes
	// sp 保存sp。
	sp *span
	// children 保存children列表。
	children []*node
}

// previewState 保存preview状态的数据和运行状态。
type previewState struct {
	// drafty 保存drafty。
	drafty *document
	// maxLength 保存maxLength。
	maxLength int
	// keymap 按键索引keymap。
	keymap map[int]int
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

// plainTextState 保存plainText状态的数据和运行状态。
type plainTextState struct {
	// txt 保存txt。
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

// SearchText 提取适合服务端全文搜索的稳定纯文本。
// 正文使用 Drafty 的 txt 字段；文件名和公开链接等可见实体属性作为补充关键词。
// 结果使用 NFKC 归一化以统一全角/半角字符，但不改变用户可见的语言内容。
func SearchText(content any) (string, error) {
	doc, err := decodeAsDrafty(content)
	if err != nil {
		return "", err
	}
	if doc == nil {
		return "", nil
	}
	if _, err = toTree(doc); err != nil {
		return "", err
	}

	text := doc.Txt
	if text == "" && doc.gc != nil {
		text = doc.gc.string()
	}
	parts := []string{text}
	for _, ent := range doc.Ent {
		for _, key := range []string{"name", "url"} {
			if value, ok := nullableMapGet(ent.Data, key); ok && value != "" {
				parts = append(parts, value)
			}
		}
	}

	return strings.TrimSpace(norm.NFKC.String(strings.Join(parts, " "))), nil
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

// spanfmt 保存spanfmt的数据和运行状态。
type spanfmt struct {
	// dec 保存dec。
	dec string
	// isVoid 保存isVoid。
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
