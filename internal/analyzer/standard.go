package analyzer

import (
	"strings"
	"unicode"
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
	if text == "" {
		return nil
	}

	// 先整体转成 []rune：撇号需要向后看一个字符，rune 切片让逻辑清晰得多。
	runes := []rune(text)
	tokens := make([]Token, 0, estimateTokens(len(runes)))

	// word 复用同一块底层数组，flush 后通过 word[:0] 归零，避免反复分配。
	var word []rune
	var pos uint32

	// flush 为一个词条收尾。无论是否保留，pos 都会前进，
	// 这样被过滤掉的词条会在位置序列里留下空隙。
	flush := func() {
		n := len(word)
		if n == 0 {
			return
		}
		term := string(word)
		word = word[:0]

		if n >= a.minTokenLen && !a.isStopword(term) {
			tokens = append(tokens, Token{Term: term, Position: pos})
		}
		pos++
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		switch {
		case isUnigramScript(r):
			flush()
			tokens = append(tokens, Token{Term: string(unicode.ToLower(r)), Position: pos})
			pos++

		case isWordRune(r):
			word = append(word, unicode.ToLower(foldFullWidth(r)))

		case isApostrophe(r) && len(word) > 0 && i+1 < len(runes) && isWordRune(runes[i+1]):
			// 词内撇号：don't / it's 保持为单个 token。
			// 统一写成直引号，避免弯引号（U+2019）造成同一词两种形态。
			word = append(word, '\'')

		default:
			flush()
		}
	}
	flush()

	return tokens
}

// isStopword 报告归一化后的词条是否在停用词表中。
func (a *StandardAnalyzer) isStopword(term string) bool {
	if len(a.stopwords) == 0 {
		return false
	}
	_, ok := a.stopwords[term]
	return ok
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
