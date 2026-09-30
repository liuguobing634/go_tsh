package analyzer

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// defaultMinTokenLen 是保留一个词条所需的最小 rune 数。
const defaultMinTokenLen = 2

// StandardOptions 配置 StandardAnalyzer；零值即为一套合理默认。
type StandardOptions struct {
	// MinTokenLen 是保留一个词条所需的最小 rune 数，<= 0 时取 2。
	//
	// 该限制不作用于 CJK 单字——单字本身就是中文的最小语义单位。
	MinTokenLen int

	// Stopwords 是自定义停用词表；为 nil 时使用内置英文表，
	// 传空切片则表示不加载任何停用词。
	Stopwords []string

	// KeepStopwords 为 true 时完全跳过停用词过滤，优先级高于 Stopwords。
	KeepStopwords bool
}

// StandardAnalyzer 是一套零第三方依赖的通用文本分析器。
//
// 处理流程：
//  1. 按 unicode 分类切分：字母与数字构成词条，其余字符视为分隔符；
//  2. Han / Hiragana / Katakana 逐字切分为单字 token（无词典的朴素回退）；
//  3. 词内撇号保留，don't 不会被拆成 don 与 t；
//  4. 全角 ASCII 折叠为半角，统一转小写；
//  5. 过滤停用词与过短词条，但保留它们占用的位置。
//
// 构造完成后内部状态只读，因此 Analyze 可被任意多个 goroutine 并发调用。
type StandardAnalyzer struct {
	minTokenLen int
	stopwords   map[string]struct{}
}

// 编译期确认接口实现。
var _ Analyzer = (*StandardAnalyzer)(nil)

// NewStandard 返回使用内置英文停用词表的默认分析器。
func NewStandard() *StandardAnalyzer { return NewStandardWith(StandardOptions{}) }

// NewStandardWith 按 opts 构造分析器。
func NewStandardWith(opts StandardOptions) *StandardAnalyzer {
	minLen := opts.MinTokenLen
	if minLen <= 0 {
		minLen = defaultMinTokenLen
	}

	a := &StandardAnalyzer{minTokenLen: minLen}

	if opts.KeepStopwords {
		return a
	}

	words := opts.Stopwords
	if words == nil {
		words = parseStopwords(defaultStopwordsRaw)
	}

	a.stopwords = make(map[string]struct{}, len(words))
	for _, w := range words {
		// 停用词表本身也要过一遍同样的归一化，否则大小写或全角写法对不上。
		w = normalizeTerm(w)
		if w != "" {
			a.stopwords[w] = struct{}{}
		}
	}

	return a
}

// Analyze 实现 Analyzer。
func (a *StandardAnalyzer) Analyze(text string) []Token {
	tokens, _ := a.analyzeInto(text, 0, nil, 0)
	return tokens
}

// analyzeInto 把 text 的分析结果**追加**到 tokens 上，返回新的切片与位置计数。
//
// base 是 text 在更大文本中的字节偏移，用于把 Token 的 Start/End 换算到
// 原文坐标系；pos 是起始位置计数。
//
// 这个入口是给混合分析器准备的：中文分析器需要把「非汉字段」交给这套
// 英文规则处理，同时保持位置与偏移在整段文本上连续。
//
// 返回值里的 pos 是必需的——被过滤掉的词条同样占位，
// 只看返回的 tokens 推不出到底消耗了多少个位置。
func (a *StandardAnalyzer) analyzeInto(text string, base int, tokens []Token, pos uint32) ([]Token, uint32) {
	if text == "" {
		return tokens, pos
	}

	if tokens == nil {
		tokens = make([]Token, 0, estimateTokens(len(text)))
	}

	// word 复用同一块底层数组，flush 后通过 word[:0] 归零，避免反复分配。
	var (
		word      []rune
		wordStart int // 当前词条的起始字节偏移
		wordEnd   int // 当前词条结束后的字节偏移
	)

	// flush 为一个词条收尾。无论是否保留，pos 都会前进，
	// 这样被过滤掉的词条会在位置序列里留下空隙。
	flush := func() {
		n := len(word)
		if n == 0 {
			return
		}
		term := string(word)
		start, end := wordStart, wordEnd
		word = word[:0]

		if a.keep(term, n) {
			tokens = append(tokens, Token{
				Term:     term,
				Position: pos,
				Start:    base + start,
				End:      base + end,
			})
		}
		pos++
	}

	// 用字节游标而不是先转 []rune：
	//  1. 省掉一次 O(n) 的分配（[]rune 是 4 字节/rune）；
	//  2. 字节偏移本来就是手头就有的，记录 Start/End 因此是免费的。
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])

		switch {
		case isUnigramScript(r):
			flush()
			tokens = append(tokens, Token{
				Term:     string(unicode.ToLower(r)),
				Position: pos,
				Start:    base + i,
				End:      base + i + size,
			})
			pos++
			i += size

		case isWordRune(r):
			if len(word) == 0 {
				wordStart = i
			}
			word = append(word, unicode.ToLower(foldFullWidth(r)))
			wordEnd = i + size
			i += size

		case isApostrophe(r) && len(word) > 0 && hasWordRuneAt(text, i+size):
			// 词内撇号：don't / it's 保持为单个 token。
			// 统一写成直引号，避免弯引号（U+2019）造成同一词两种形态。
			word = append(word, '\'')
			wordEnd = i + size
			i += size

		default:
			flush()
			i += size
		}
	}
	flush()

	return tokens, pos
}

// hasWordRuneAt 报告 text[i:] 处的 rune 是否能作为词条字符。
// 越界或非法 UTF-8 都返回 false。
func hasWordRuneAt(text string, i int) bool {
	if i >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[i:])
	return isWordRune(r)
}

// isStopword 报告归一化后的词条是否在停用词表中。
func (a *StandardAnalyzer) isStopword(term string) bool {
	if len(a.stopwords) == 0 {
		return false
	}
	_, ok := a.stopwords[term]
	return ok
}

// keep 报告一个已归一化的词条是否应当进入倒排索引。
//
// n 是词条的 rune 数（不是字节数——中文词按字节算会得出错误结论）。
// 混用分析器时这条规则必须只有一处实现，否则中英文两侧的过滤口径
// 迟早会走偏。
func (a *StandardAnalyzer) keep(term string, n int) bool {
	return n >= a.minTokenLen && !a.isStopword(term)
}

// isWordRune 报告 r 是否可以作为词条的组成部分。
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isApostrophe 报告 r 是否是撇号（直引号或弯引号）。
func isApostrophe(r rune) bool {
	return r == '\'' || r == '\u2019'
}

// isUnigramScript 报告某个 rune 是否属于「词间不加空格、需要分词」的文字系统。
//
// Han（汉字）、Hiragana、Katakana 书写时词与词之间不加空格，
// 在没有词典的情况下，逐字切分（unigram）是最朴素且可用的回退方案，
// 至少能保证单字命中；真正的分词留到 Phase 6。
//
// 韩文 Hangul 在现代书写中用空格分词，因此不在此列，按普通词处理。
func isUnigramScript(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r)
}

// foldFullWidth 把全角 ASCII（U+FF01..U+FF5E）折叠为对应半角字符。
//
// 这覆盖了中文输入法下常见的全角字母、数字与标点，
// 从而无需引入 golang.org/x/text 的 NFKC 归一化。
func foldFullWidth(r rune) rune {
	if r >= 0xFF01 && r <= 0xFF5E {
		return r - 0xFEE0
	}
	return r
}

// normalizeTerm 对单个词条施加与分析流程一致的归一化，供停用词表加载使用。
func normalizeTerm(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\u2019' {
			r = '\''
		}
		runes = append(runes, unicode.ToLower(foldFullWidth(r)))
	}
	return string(runes)
}

// estimateTokens 给出一个略偏高的初始容量，减少 append 期间的切片扩容。
func estimateTokens(nRunes int) int {
	if nRunes <= 0 {
		return 0
	}
	// 英文平均词长约 5 个字符，加上分隔符接近 6；CJK 逐字则密度高得多。
	// 取 /4 是两者之间的折中：宁可略微高估，也不要频繁扩容。
	return nRunes/4 + 1
}
