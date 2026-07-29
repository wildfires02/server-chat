// 通用数据操作工具函数。

// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"

	"maps"

	"golang.org/x/crypto/acme/autocert"
)

// 带前缀的标签：
// * 前缀以 ASCII 字母开头，包含 ASCII 字母和数字，2 到 16 个字符
// * 标签主体可包含 Unicode 字母和数字，以及以下符号：+-.!?#@_
// 标签主体最长可达 maxTagLength (96) 个字符
var prefixedTagRegexp = regexp.MustCompile(`^([a-z]\w{1,15}):([-_+.!?#@\pL\pN]{1,96})$`)

// 通用标签：与标签主体相同的限制
var tagRegexp = regexp.MustCompile(`^[-_+.!?#@\pL\pN]{1,96}$`)

// nullValue 指定null值。
const nullValue = "\u2421"

// boundedWaitGroup 是限制最大并发数限制的 WaitGroup 包装结构体。
type boundedWaitGroup struct {
	// wg 保存wg。
	wg sync.WaitGroup
	// sem 传递sem相关的异步事件。
	sem chan struct{}
}

// newBoundedWaitGroup 创建并初始化BoundedWaitGroup。
func newBoundedWaitGroup(cap int) *boundedWaitGroup {
	return &boundedWaitGroup{
		sem: make(chan struct{}, cap),
	}
}

// Add 创建或追加 Add 对应的数据。
func (b *boundedWaitGroup) Add(delta int) {
	if b == nil {
		return
	}
	for i := 0; i < delta; i++ {
		b.sem <- struct{}{}
		b.wg.Add(1)
	}
}

// Done 完成Done所需的内部处理。
func (b *boundedWaitGroup) Done() {
	if b == nil {
		return
	}
	select {
	case <-b.sem:
	default:
	}
	b.wg.Done()
}

// Wait 完成Wait所需的内部处理。
func (b *boundedWaitGroup) Wait() {
	if b == nil {
		return
	}
	b.wg.Wait()
}

// 将数据库范围转换为协议范围
func rangeDeserialize(in []types.Range) []MsgRange {
	if len(in) == 0 {
		return nil
	}

	out := make([]MsgRange, 0, len(in))
	for _, r := range in {
		out = append(out, MsgRange{LowId: r.Low, HiId: r.Hi})
	}

	return out
}

// 将协议范围转换为数据库范围
func rangeSerialize(in []MsgRange) []types.Range {
	if len(in) == 0 {
		return nil
	}

	out := make([]types.Range, 0, len(in))
	for _, r := range in {
		out = append(out, types.Range{Low: r.LowId, Hi: r.HiId})
	}

	return out
}

// stringSliceDelta 从两个切片中提取添加和移除的字符串切片：
//
//	added := newSlice - (oldSlice & newSlice) -- 存在于新但缺失于旧
//	removed := oldSlice - (oldSlice & newSlice) -- 存在于旧但缺失于新
//	intersection := oldSlice & newSlice -- 同时存在于旧和新
func stringSliceDelta(rold, rnew []string) (added, removed, intersection []string) {
	if len(rold) == 0 && len(rnew) == 0 {
		return nil, nil, nil
	}
	if len(rold) == 0 {
		return rnew, nil, nil
	}
	if len(rnew) == 0 {
		return nil, rold, nil
	}

	sort.Strings(rold)
	sort.Strings(rnew)

	// 将旧切片与新切片匹配，分离移除和添加的字符串
	o, n := 0, 0
	lold, lnew := len(rold), len(rnew)
	for o < lold || n < lnew {
		if o == lold || (n < lnew && rold[o] > rnew[n]) {
			// 存在于新，缺失于旧：已添加
			added = append(added, rnew[n])
			n++

		} else if n == lnew || rold[o] < rnew[n] {
			// 存在于旧，缺失于新：已移除
			removed = append(removed, rold[o])
			o++

		} else {
			// 两者都存在
			intersection = append(intersection, rold[o])
			if o < lold {
				o++
			}
			if n < lnew {
				n++
			}
		}
	}
	return added, removed, intersection
}

// 处理凭证的正确性：移除重复和未知方法
// 重复方法仅保留第一个满足 valueRequired 的
// 如果 valueRequired 为 true，仅保留 Value 非空的
func normalizeCredentials(creds []MsgCredClient, valueRequired bool) []MsgCredClient {
	if len(creds) == 0 {
		return nil
	}

	index := make(map[string]*MsgCredClient)
	for i := range creds {
		c := &creds[i]
		if _, ok := globals.validators[c.Method]; ok && (!valueRequired || c.Value != "") {
			index[c.Method] = c
		}
	}
	creds = make([]MsgCredClient, 0, len(index))
	for _, c := range index {
		creds = append(creds, *c)
	}
	return creds
}

// 获取凭证方法的字符串切片
func credentialMethods(creds []MsgCredClient) []string {
	out := make([]string, len(creds))
	for i := range creds {
		out[i] = creds[i].Method
	}
	return out
}

// 接收 MsgClientGet 查询参数，返回数据库查询参数
func msgOpts2storeOpts(req *MsgGetOpts) *types.QueryOpt {
	var opts *types.QueryOpt
	if req != nil {
		opts = &types.QueryOpt{
			User:            types.ParseUserId(req.User),
			Topic:           req.Topic,
			IfModifiedSince: req.IfModifiedSince,
			Limit:           req.Limit,
			Since:           req.SinceId,
			Before:          req.BeforeId,
			IdRanges:        rangeSerialize(req.IdRanges),
			Forward:         req.Forward,
		}
	}
	return opts
}

// 检查是否接口包含一个仅含单个 Unicode Del 控制字符的字符串
func isNullValue(i any) bool {
	if str, ok := i.(string); ok {
		return str == nullValue
	}
	return false
}

// decodeStoreError 将输入解析为存储错误。
func decodeStoreError(err error, id string, ts time.Time, params map[string]any) *ServerComMessage {
	return decodeStoreErrorExplicitTs(err, id, "", ts, ts, params)
}

// decodeStoreErrorExplicitTs 将输入解析为存储错误ExplicitTs。
func decodeStoreErrorExplicitTs(err error, id, topic string, serverTs, incomingReqTs time.Time,
	params map[string]any) *ServerComMessage {

	var errmsg *ServerComMessage

	if err == nil {
		errmsg = NoErrExplicitTs(id, topic, serverTs, incomingReqTs)
	} else if storeErr, ok := err.(types.StoreError); !ok {
		errmsg = ErrUnknownExplicitTs(id, topic, serverTs, incomingReqTs)
	} else {
		switch storeErr {
		case types.ErrInternal:
			errmsg = ErrUnknownExplicitTs(id, topic, serverTs, incomingReqTs)
		case types.ErrMalformed:
			errmsg = ErrMalformedExplicitTs(id, topic, serverTs, incomingReqTs)
		case types.ErrFailed:
			errmsg = ErrAuthFailed(id, topic, serverTs, incomingReqTs)
		case types.ErrPermissionDenied:
			errmsg = ErrPermissionDeniedExplicitTs(id, topic, serverTs, incomingReqTs)
		case types.ErrDuplicate:
			errmsg = ErrDuplicateCredential(id, topic, serverTs, incomingReqTs)
		case types.ErrUnsupported:
			errmsg = ErrNotImplemented(id, topic, serverTs, incomingReqTs)
		case types.ErrExpired:
			errmsg = ErrAuthFailed(id, topic, serverTs, incomingReqTs)
		case types.ErrPolicy:
			errmsg = ErrPolicyExplicitTs(id, topic, serverTs, incomingReqTs)
		case types.ErrCredentials:
			errmsg = InfoValidateCredentialsExplicitTs(id, serverTs, incomingReqTs)
		case types.ErrUserNotFound:
			errmsg = ErrUserNotFound(id, topic, serverTs, incomingReqTs)
		case types.ErrTopicNotFound:
			errmsg = ErrTopicNotFound(id, topic, serverTs, incomingReqTs)
		case types.ErrNotFound:
			errmsg = ErrNotFoundExplicitTs(id, topic, serverTs, incomingReqTs)
		case types.ErrInvalidResponse:
			errmsg = ErrInvalidResponse(id, topic, serverTs, incomingReqTs)
		case types.ErrRedirected:
			errmsg = InfoUseOther(id, topic, params["topic"].(string), serverTs, incomingReqTs)
		default:
			errmsg = ErrUnknownExplicitTs(id, topic, serverTs, incomingReqTs)
		}
	}

	if params != nil {
		errmsg.Ctrl.Params = params
	}

	return errmsg
}

// 辅助函数：为给定认证级别选择访问模式
func selectAccessMode(authLvl auth.Level, anonMode, authMode, rootMode types.AccessMode) types.AccessMode {
	switch authLvl {
	case auth.LevelNone:
		return types.ModeNone
	case auth.LevelAnon:
		return anonMode
	case auth.LevelAuth:
		return authMode
	case auth.LevelRoot:
		return rootMode
	default:
		return types.ModeNone
	}
}

// 获取给定 Topic 类别的默认 modeWant
func getDefaultAccess(cat types.TopicCat, authUser, isChan bool) types.AccessMode {
	if !authUser {
		return types.ModeNone
	}

	switch cat {
	case types.TopicCatP2P:
		return globals.typesModeCP2P
	case types.TopicCatFnd:
		return types.ModeNone
	case types.TopicCatGrp:
		if isChan {
			return types.ModeCChnWriter
		}
		return types.ModeCPublic
	case types.TopicCatMe:
		return types.ModeCMeFnd
	case types.TopicCatSlf:
		return types.ModeCSelf
	default:
		panic("未知的 Topic 类别")
	}
}

// 解析 Topic 访问参数
func parseTopicAccess(acs *MsgDefaultAcsMode, defAuth, defAnon types.AccessMode) (authMode, anonMode types.AccessMode,
	err error) {

	authMode, anonMode = defAuth, defAnon

	if acs.Auth != "" {
		if err = authMode.UnmarshalText([]byte(acs.Auth)); err != nil {
			return
		}
	}
	if acs.Anon != "" {
		err = anonMode.UnmarshalText([]byte(acs.Anon))
	}

	return
}

// 解析语义版本字符串的一个部分
func parseVersionPart(vers string) int {
	end := strings.IndexFunc(vers, func(r rune) bool {
		return !unicode.IsDigit(r)
	})

	t := 0
	var err error
	if end > 0 {
		t, err = strconv.Atoi(vers[:end])
	} else if len(vers) > 0 {
		t, err = strconv.Atoi(vers)
	}
	if err != nil || t > 0x1fff || t <= 0 {
		return 0
	}
	return t
}

// 解析以下格式的语义版本字符串：
//
//	1.2, 1.2abc, 1.2.3, 1.2.3-abc, v0.12.34-rc5
//
// 无法解析的值用零替代
func parseVersion(vers string) int {
	var major, minor, patch int
	// 可能移除可选的 "v" 前缀
	vers = strings.TrimPrefix(vers, "v")

	// 我们只能处理 3 个部分
	parts := strings.SplitN(vers, ".", 3)
	count := len(parts)
	if count > 0 {
		major = parseVersionPart(parts[0])
		if count > 1 {
			minor = parseVersionPart(parts[1])
			if count > 2 {
				patch = parseVersionPart(parts[2])
			}
		}
	}

	return (major << 16) | (minor << 8) | patch
}

// 以十进制数字表示版本。用于监控
func base10Version(hex int) int64 {
	major := hex >> 16 & 0xFF
	minor := hex >> 8 & 0xFF
	trailer := hex & 0xFF
	return int64(major*10000 + minor*100 + trailer)
}

// versionToString 完成版本ToString所需的内部处理。
func versionToString(vers int) string {
	str := strconv.Itoa(vers>>16) + "." + strconv.Itoa((vers>>8)&0xff)
	if vers&0xff != 0 {
		str += "-" + strconv.Itoa(vers&0xff)
	}
	return str
}

// 标签处理

// filterTags 接收标签切片和命名空间映射，返回输入中包含的命名空间标签切片
// 参数：要过滤的标签，用作过滤器的命名空间
func filterTags(tags []string, namespaces map[string]bool) []string {
	var out []string
	if len(namespaces) == 0 {
		return out
	}

	for _, s := range tags {
		parts := prefixedTagRegexp.FindStringSubmatch(s)

		if len(parts) < 2 {
			continue
		}

		// [1] 是前缀。[0] 是整个标签
		if namespaces[parts[1]] {
			out = append(out, s)
		}
	}

	return out
}

// getCountryCodeFromHeader 尝试从 HTTP 请求标头（如 CDN / GeoIP 代理标头）解析 ISO 2位国家代码
func getCountryCodeFromHeader(req *http.Request) string {
	if req == nil {
		return ""
	}
	for _, h := range []string{"CF-IPCountry", "X-GeoIP-Country-Code", "CloudFront-Viewer-Country", "X-Country-Code"} {
		if c := req.Header.Get(h); len(c) == 2 && c != "XX" {
			return strings.ToUpper(c)
		}
	}
	return ""
}

// rewriteTag 尝试将原始令牌匹配为电子邮件或电话号码
// 标签预期为小写
// 成功时，返回包含原始标签和带前缀标签的切片。标签无效时返回空切片。
// 若 countryCode 为空，降级使用服务器配置的默认国家代码（globals.defaultCountryCode）
func rewriteTag(orig, countryCode string) []string {
	if countryCode == "" {
		countryCode = globals.defaultCountryCode
	}

	// 检查是否标签已有前缀，例如 basic:alice
	if prefixedTagRegexp.MatchString(orig) {
		return []string{orig}
	}

	// 检查是否令牌可以被任何验证器重写
	param := map[string]any{"countryCode": countryCode}
	for name, conf := range globals.validators {
		if conf.addToTags {
			val := store.Store.GetValidator(name)
			if tag, _ := val.PreCheck(orig, param); tag != "" {
				return []string{orig, tag}
			}
		}
	}

	if tagRegexp.MatchString(orig) {
		return []string{orig}
	}

	// 无效的通用标签

	return nil
}

// rewriteTagSlice 对切片的每个成员调用 rewriteTag，返回包含原始和转换值的新切片
func rewriteTagSlice(tags []string, countryCode string) []string {
	var result []string
	for _, tag := range tags {
		rewritten := rewriteTag(tag, countryCode)
		if len(rewritten) != 0 {
			result = append(result, rewritten...)
		}
	}
	return result
}

// restrictedTagsEqual 检查两个标签集是否包含相同的受限标签集：
// true - 相同，false - 不同
func restrictedTagsEqual(oldTags, newTags []string, namespaces map[string]bool) bool {
	rold := filterTags(oldTags, namespaces)
	rnew := filterTags(newTags, namespaces)

	if len(rold) != len(rnew) {
		return false
	}

	sort.Strings(rold)
	sort.Strings(rnew)

	// 将旧标签与新标签匹配
	for i := range rnew {
		if rold[i] != rnew[i] {
			return false
		}
	}

	return true
}

// 去除空白，移除过短/空的标签和重复项，转换为小写，确保
// 标签数量不超过最大值
func normalizeTags(src []string, maxTags int) types.StringSlice {
	if src == nil {
		return nil
	}

	// 确保标签数量不超过最大值
	// 技术上可能因此产生比最大值更少的标签（由于空标签和
	// 重复），但那是用户的责任
	if len(src) > maxTags {
		src = src[:maxTags]
	}

	// 去除空白并转换为小写
	for i := range src {
		src[i] = strings.ToLower(strings.TrimSpace(src[i]))
	}

	// 排序标签
	sort.Strings(src)

	// 移除过短、无效的标签并去重，保持顺序。如果稍后强制执行长度限制
	// 可能产生更多标签，但那是客户端的责任
	var prev string
	var dst []string
	for _, curr := range src {
		if isNullValue(curr) {
			// 返回非空的空数组
			return make([]string, 0, 1)
		}

		// Unicode 处理
		ucurr := []rune(curr)

		// 按字符而非字节强制执行长度限制
		if len(ucurr) < minTagLength || len(ucurr) > maxTagLength || curr == prev {
			continue
		}

		// 确保标签以字母或数字开头
		if unicode.IsLetter(ucurr[0]) || unicode.IsDigit(ucurr[0]) {
			dst = append(dst, curr)
			prev = curr
		}
	}

	return types.StringSlice(dst)
}

// validateTag 校验标签的输入和约束。
func validateTag(tag string) (string, string) {
	// 检查是否标签已有前缀，例如 basic:alice
	if parts := prefixedTagRegexp.FindStringSubmatch(tag); len(parts) == 3 {
		// 有效的带前缀标签
		return parts[1], parts[2]
	}

	if tagRegexp.MatchString(tag) {
		// 有效的不带前缀标签（仅标签值）
		return "", tag
	}

	return "", ""
}

// hasDuplicateNamespaceTags 检查唯一命名空间标签是否重复
// 每个命名空间只能有一个标签。这不能防止跨请求的标签重复，
// 只是节省额外的数据库调用
func hasDuplicateNamespaceTags(src []string, uniqueNS string) bool {
	found := map[string]bool{}
	for _, tag := range src {
		parts := prefixedTagRegexp.FindStringSubmatch(tag)
		if len(parts) != 3 {
			// 无效标签，忽略
			continue
		}

		if uniqueNS == parts[1] && found[parts[1]] {
			return true
		}
		found[parts[1]] = true
	}
	return false
}

// 搜索查询解析器。查询可能包含非 ASCII 字符，
// 即字符串的字节长度 != 字符串的 rune 长度
// 返回
// * 必需标签：AND 标签（每个结果必须至少包含一个）
// * 可选标签
// * 错误
func parseSearchQuery(query string) ([]string, []string, error) {
	const (
		NONE = iota
		QUO  // 1
		AND  // 2
		OR   // 3
		END  // 4
		ORD  // 5
	)
	type token struct {
		op  int
		val string
	}
	type context struct {
		// 前令牌操作数
		preOp int
		// 后令牌操作数
		postOp int
		// 在引号字符串内
		quo bool
		// 当前令牌的起始位置
		start int
		// 当前令牌的结束位置
		end int
	}
	ctx := context{preOp: AND}
	var out []token
	var prev int
	query = strings.TrimSpace(query)
	// 将查询拆分为令牌
	//   i - 字符串中的字符索引
	//   pos - 字符串中的 rune 索引
	//   w - 当前 rune 的字符宽度
	for i, w, pos := 0, 0, 0; prev != END; i, pos = i+w, pos+1 {
		//
		var emit bool

		// 获取下一个 rune
		var r rune
		// 默认为普通字符
		curr := ORD
		r, w = utf8.DecodeRuneInString(query[i:])
		switch {
		case w == 0:
			// 宽度为零：字符串结束
			curr = END
		case r == '"':
			// 引号开始或结束
			curr = QUO
		case !ctx.quo:
			// 不在引号字符串内，测试控制字符
			switch r {
			case ' ', '\t':
				// 制表符或空格
				curr = AND
			case ',':
				curr = OR
			}
		}

		if curr == QUO {
			if ctx.quo {
				// 引号字符串结束。关闭引号
				ctx.quo = false
			} else {
				if prev == ORD {
					// 拒绝类似 a"b 的字符串
					return nil, nil, fmt.Errorf("missing operator at or near %d", pos)
				}
				// 引号字符串开始。打开引号
				ctx.quo = true
			}
			// 将引号字符串视为普通
			curr = ORD
		}

		// 解析器：在上下文中处理当前词法
		switch curr {
		case OR:
			if ctx.postOp == OR {
				// 多个逗号：' , ,,'
				return nil, nil, fmt.Errorf("invalid operator sequence at or near %d", pos)
			}
			// 确保上下文不是 "and"，即 ' ,' -> ',' 的情况
			ctx.postOp = OR
			if prev == ORD {
				// 关闭当前令牌
				ctx.end = i
			}
		case AND:
			if prev == ORD {
				// 关闭当前令牌
				ctx.end = i
				ctx.postOp = AND
			} else if ctx.postOp != OR {
				// "and" 不改变 "or" 上下文
				ctx.postOp = AND
			}
		case ORD:
			if prev == OR || prev == AND {
				// 逗号或空格后的普通字符：' a' 或 ',a'
				// 发出不改变操作
				emit = true
			}
		case END:
			if prev == ORD {
				// 关闭当前令牌
				ctx.end = i
			}
			emit = true
		}

		if emit {
			if ctx.quo && curr == END {
				return nil, nil, fmt.Errorf("unterminated quoted string at or near %d %#v", pos, ctx)
			}

			// 发出新令牌
			op := ctx.preOp
			if ctx.postOp == OR {
				op = OR
			}
			start, end := ctx.start, ctx.end
			if query[start] == '"' && query[end-1] == '"' {
				start++
				end--
			}
			// 添加非空令牌
			if start < end {
				out = append(out, token{val: strings.ToLower(query[start:end]), op: op})
			}
			ctx.start = i
			ctx.preOp, ctx.postOp = ctx.postOp, NONE
		}

		prev = curr
	}

	if len(out) == 0 {
		return nil, nil, nil
	}

	// 将令牌转换为两个字符串切片
	var and []string
	var or []string
	for _, t := range out {
		switch t.op {
		case AND:
			and = append(and, t.val)
		case OR:
			or = append(or, t.val)
		}
	}
	return and, or, nil
}

// v1 > v2 时返回 > 0；相等返回 0；v1 < v2 返回 < 0
// 仅比较 Major 和 Minor 部分，忽略 trailer
func versionCompare(v1, v2 int) int {
	return (v1 >> 8) - (v2 >> 8)
}

// 如果字符串过长则截断。用于日志记录
func truncateStringIfTooLong(s string) string {
	if len(s) <= 1024 {
		return s
	}

	return s[:1024] + "..."
}

// 将相对路径转换为绝对路径
func toAbsolutePath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(base, path))
}

// 从 UserAgent 字符串检测平台
func platformFromUA(ua string) string {
	ua = strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "reactnative"):
		switch {
		case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"):
			return "ios"
		case strings.Contains(ua, "android"):
			return "android"
		}
		return ""
	case strings.Contains(ua, "imjs") || strings.Contains(ua, "tinodejs"):
		return "web"
	case strings.Contains(ua, "imdroid") || strings.Contains(ua, "tindroid"):
		return "android"
	case strings.Contains(ua, "imios") || strings.Contains(ua, "tinodios"):
		return "ios"
	case strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad"):
		return "ios"
	case strings.Contains(ua, "android"):
		return "android"
	case strings.Contains(ua, "mozilla") || strings.Contains(ua, "chrome") || strings.Contains(ua, "safari") || strings.Contains(ua, "firefox"):
		return "web"
	}
	return ""
}

// parseTLSConfig 将输入解析为TLS配置。
func parseTLSConfig(tlsEnabled bool, jsconfig json.RawMessage) (*tls.Config, error) {
	type tlsAutocertConfig struct {
		// autocert 支持的域名
		Domains []string `json:"domains"`
		// 自动证书缓存目录名称，例如 /etc/letsencrypt/live/your-domain-here
		CertCache string `json:"cache"`
		// letsencrypt 的联系邮箱
		Email string `json:"email"`
	}

	type tlsConfig struct {
		// TLS 启用标志
		Enabled bool `json:"enabled"`
		// 监听此地址:端口的连接并重定向到 HTTPS 端口
		RedirectHTTP string `json:"http_redirect"`
		// 通过设置 max_age > 0 启用 Strict-Transport-Security
		StrictMaxAge int `json:"strict_max_age"`
		// ACME 自动证书配置，例如 letsencrypt.org
		Autocert *tlsAutocertConfig `json:"autocert"`
		// 如果未定义 Autocert，提供静态证书和密钥的文件名
		CertFile string `json:"cert_file"`
		KeyFile  string `json:"key_file"`
	}

	var config tlsConfig

	if jsconfig != nil {
		if err := json.Unmarshal(jsconfig, &config); err != nil {
			return nil, errors.New("http: failed to parse tls_config: " + err.Error() + "(" + string(jsconfig) + ")")
		}
	}

	if !tlsEnabled && !config.Enabled {
		return nil, nil
	}

	if config.StrictMaxAge > 0 {
		globals.tlsStrictMaxAge = strconv.Itoa(config.StrictMaxAge)
	}

	globals.tlsRedirectHTTP = config.RedirectHTTP

	// 如果提供了 autocert，使用它
	if config.Autocert != nil {
		certManager := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(config.Autocert.Domains...),
			Cache:      autocert.DirCache(config.Autocert.CertCache),
			Email:      config.Autocert.Email,
		}
		return certManager.TLSConfig(), nil
	}

	// 否则尝试使用静态密钥
	cert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return nil, err
	}

	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

// 将源 interface{} 合并到目标 interface
// 如果值是 map，则深度合并。否则浅拷贝
// 如果 dst 值被更改则返回 dst, true
func mergeInterfaces(dst, src any) (any, bool) {
	var changed bool

	if src == nil {
		return dst, changed
	}

	vsrc := reflect.ValueOf(src)
	switch vsrc.Kind() {
	case reflect.Map:
		if xsrc, ok := src.(map[string]any); ok {
			xdst, _ := dst.(map[string]any)
			dst, changed = mergeMaps(xdst, xsrc)
		} else {
			changed = true
			dst = src
		}
	case reflect.String:
		if vsrc.String() == nullValue {
			changed = dst != nil
			dst = nil
		} else {
			changed = true
			dst = src
		}
	default:
		changed = true
		dst = src
	}
	return dst, changed
}

// 深度拷贝 map
func mergeMaps(dst, src map[string]any) (map[string]any, bool) {
	var changed bool

	if len(src) == 0 {
		return dst, changed
	}

	if dst == nil {
		dst = make(map[string]any)
	}

	for key, val := range src {
		xval := reflect.ValueOf(val)
		switch xval.Kind() {
		case reflect.Map:
			if xsrc, _ := val.(map[string]any); xsrc != nil {
				// 深度拷贝 map[string]any
				xdst, _ := dst[key].(map[string]any)
				var lchange bool
				dst[key], lchange = mergeMaps(xdst, xsrc)
				changed = changed || lchange
			} else if val != nil {
				// 如果不是 map[string]any 类型，则浅拷贝
				dst[key] = val
				changed = true
			}
		case reflect.String:
			changed = true
			if xval.String() == nullValue {
				delete(dst, key)
			} else if val != nil {
				dst[key] = val
			}
		default:
			if val != nil {
				dst[key] = val
				changed = true
			}
		}
	}

	return dst, changed
}

// 浅拷贝 map
func copyMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)
	return dst
}

// netListener 为 tcp 和 unix 域创建 net.Listener：
// 如果 addr 形如 "unix:/run/im.sock" 则为 unix socket，否则为 TCP host:port
func netListener(addr string) (net.Listener, error) {
	addrParts := strings.SplitN(addr, ":", 2)
	if len(addrParts) == 2 && addrParts[0] == "unix" {
		return net.Listen("unix", addrParts[1])
	}
	return net.Listen("tcp", addr)
}

// 检查是否指定的地址是 unix socket，如 "unix:/run/im.sock"
func isUnixAddr(addr string) bool {
	addrParts := strings.SplitN(addr, ":", 2)
	return len(addrParts) == 2 && addrParts[0] == "unix"
}

var (
	// privateIPBlocks 保存privateIPBlocks的共享实例或运行状态。
	privateIPBlocks []*net.IPNet
	// privateIPBlocksOnce 保存privateIPBlocksOnce的共享实例或运行状态。
	privateIPBlocksOnce sync.Once
)

// isRoutableIP 判断是否满足RoutableIP条件。
func isRoutableIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}

	privateIPBlocksOnce.Do(func() {
		for _, cidr := range []string{
			"10.0.0.0/8",     // RFC1918
			"172.16.0.0/12",  // RFC1918
			"192.168.0.0/16", // RFC1918
			"fc00::/7",       // RFC4193, IPv6 unique local addr
		} {
			_, block, _ := net.ParseCIDR(cidr)
			privateIPBlocks = append(privateIPBlocks, block)
		}
	})

	for _, block := range privateIPBlocks {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
