// Package highlight 把命中的词条在原文里标注出来。
//
// 它不自己切分文本，而是复用检索用的同一个 Analyzer——
// 这一点很关键：高亮依赖 token 在原文里的字节区间，
// 如果另起一套切分规则，就会出现「高亮的位置和检索命中的位置对不上」，
// 而这种错位非常隐蔽，只有肉眼看结果才会发现。
package highlight

import (
	"html"
	"strings"
	"unicode/utf8"

	"github.com/liuguobing/go_tsh/internal/analyzer"
)

// Options 配置高亮输出；零值即为一套合理默认。
type Options struct {
	// PreTag / PostTag 是包裹命中词条的标记；为空时取 <em> / </em>。
	PreTag  string
	PostTag string

	// MaxLen 是输出片段的最大**字节**长度，<= 0 表示不截断、返回全文。
	//
	// 截断时以**第一个命中词条**为中心开窗，并向前留出约 1/4 的上下文，
	// 保证用户一眼能看到命中的地方，而不是只看到一段无关的前缀。
	MaxLen int
}

// DefaultPreTag / DefaultPostTag 是默认的标注标记。
const (
	DefaultPreTag  = "<em>"
	DefaultPostTag = "</em>"
)

// Highlighter 按分析器的切分结果标注原文。
//
// 构造后状态只读，可并发使用。
type Highlighter struct {
	anz     analyzer.Analyzer
	preTag  string
	postTag string
	maxLen  int
}

// New 创建高亮器。anz 必须与检索用的是同一个实例。
func New(anz analyzer.Analyzer, opts Options) *Highlighter {
	pre, post := opts.PreTag, opts.PostTag
	if pre == "" {
		pre = DefaultPreTag
	}
	if post == "" {
		post = DefaultPostTag
	}

	return &Highlighter{
		anz:     anz,
		preTag:  pre,
		postTag: post,
		maxLen:  opts.MaxLen,
	}
}

// Highlight 返回把 terms 中的词条用标记包裹后的文本。
//
// 返回的是 **HTML 片段**：原文中的 < > & 等字符会被转义，
// 因此插入的标记是输出里仅有的原始 HTML，可以安全地直接嵌进页面。
//
// 没有任何命中时返回空串——调用方据此判断要不要带上高亮字段。
//
// terms 必须是**已归一化**的词条（即 analyzer 的输出形态）。
// 直接用用户输入的原始词是匹配不上的，例如原文里的 "Go" 归一化成 "go"。
func (h *Highlighter) Highlight(text string, terms []string) string {
	if text == "" || len(terms) == 0 {
		return ""
	}

	wanted := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		if t != "" {
			wanted[t] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return ""
	}

	// 复用检索用的切分规则，从 token 上直接拿原文区间。
	var spans []span
	for _, tok := range h.anz.Analyze(text) {
		if _, ok := wanted[tok.Term]; ok {
			spans = append(spans, span{start: tok.Start, end: tok.End})
		}
	}
	if len(spans) == 0 {
		return ""
	}

	lo, hi, truncatedHead, truncatedTail := h.window(text, spans[0])

	var sb strings.Builder
	sb.Grow(hi - lo + 32)

	if truncatedHead {
		sb.WriteString("…")
	}

	prev := lo
	for _, s := range spans {
		// 落在窗口外、或跨越窗口边界的命中直接跳过——
		// 半个词条加标记反而会误导人。
		if s.start < prev || s.end > hi {
			continue
		}

		sb.WriteString(html.EscapeString(text[prev:s.start]))
		sb.WriteString(h.preTag)
		sb.WriteString(html.EscapeString(text[s.start:s.end]))
		sb.WriteString(h.postTag)
		prev = s.end
	}
	sb.WriteString(html.EscapeString(text[prev:hi]))

	if truncatedTail {
		sb.WriteString("…")
	}

	return sb.String()
}

// span 是原文中的一个字节区间 [start, end)。
type span struct {
	start int
	end   int
}

// window 决定输出窗口，并报告首尾是否被截断。
//
// 不做截断时返回整个文本；需要截断时以第一个命中词条为中心开窗。
func (h *Highlighter) window(text string, first span) (lo, hi int, head, tail bool) {
	lo, hi = 0, len(text)

	if h.maxLen <= 0 || len(text) <= h.maxLen {
		return lo, hi, false, false
	}

	// 命中位置之前留约 1/4 的上下文，其余留给后文。
	lo = first.start - h.maxLen/4
	if lo < 0 {
		lo = 0
	}
	hi = lo + h.maxLen
	if hi > len(text) {
		hi = len(text)
		lo = hi - h.maxLen
		if lo < 0 {
			lo = 0
		}
	}

	// 对齐到 rune 边界，避免把多字节字符切成半个。
	lo = alignStart(text, lo)
	hi = alignEnd(text, hi)

	return lo, hi, lo > 0, hi < len(text)
}

// alignStart 把 i 回退到所在 rune 的起始字节。
func alignStart(text string, i int) int {
	for i > 0 && i < len(text) && !utf8.RuneStart(text[i]) {
		i--
	}
	return i
}

// alignEnd 把 i 前进到下一个 rune 的起始字节。
func alignEnd(text string, i int) int {
	for i < len(text) && !utf8.RuneStart(text[i]) {
		i++
	}
	return i
}
