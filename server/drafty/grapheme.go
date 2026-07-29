// Package drafty 实现即时通信服务端的协议、路由和业务逻辑。
package drafty

import (
	"github.com/rivo/uniseg"
)

// graphemes 是一个容器，保存字符串中各字素簇的长度。
type graphemes struct {
	// 原始字符串。
	original string

	// 原始字符串中各字素簇的大小。
	sizes []byte
}

// prepareGraphemes 返回解析后的字素簇容器，将字符串拆分为字素簇并保存其长度。
func prepareGraphemes(str string) *graphemes {
	// 将字符串拆分为字素簇并保存每个簇的大小。
	sizes := make([]byte, 0, len(str))
	for state, remaining, cluster := -1, str, ""; len(remaining) > 0; {
		cluster, remaining, _, state = uniseg.StepString(remaining, state)
		sizes = append(sizes, byte(len(cluster)))
	}

	return &graphemes{
		original: str,
		sizes:    sizes,
	}
}

// length 返回原始字符串中字素簇的数量。
func (g *graphemes) length() int {
	if g == nil {
		return 0
	}
	return len(g.sizes)
}

// string 返回创建字素簇容器时的原始字符串。
func (g *graphemes) string() string {
	if g == nil {
		return ""
	}
	return g.original
}

// slice 返回一个新的字素簇容器，包含从 'start' 到 'end' 的字素簇。
func (g *graphemes) slice(start, end int) *graphemes {

	// 将字素偏移量转换为字符串偏移量。
	s := 0
	for i := range start {
		s += int(g.sizes[i])
	}
	e := s
	for i := start; i < end; i++ {
		e += int(g.sizes[i])
	}

	return &graphemes{
		original: g.original[s:e],
		sizes:    g.sizes[start:end],
	}
}

// append 将 'other' 字素簇容器追加到 'g' 容器并返回 g。
// 如果 g 为 nil，则返回 'other'。
func (g *graphemes) append(other *graphemes) *graphemes {
	if g == nil {
		return other
	}

	g.original += other.original
	g.sizes = append(g.sizes, other.sizes...)
	return g
}
