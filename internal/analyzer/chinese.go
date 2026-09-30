package analyzer

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-ego/gse"
)

// embeddedChineseDict 是内嵌的简体中文词典标识。
//
// 用 "zh_s" 而不是 "zh"：实测 "zh"（简+繁）加载要 1.475 秒、约 170 MB 堆，
// 而 "zh_s" 只要 732 毫秒、约 108 MB。繁体场景不在当前目标内，
// 没必要为此付出双倍的内存基线。
const embeddedChineseDict = "zh_s"

// customWordFreq 是追加词条的默认词频。
//
// gse 用词频计算最短路径上的代价，词频越高越倾向于被当作一个整词。
// 取一个偏大的值，让调用方显式加进来的词优先成词。
const customWordFreq = 1e6

// ChineseOptions 配置中文分析器；零值即为一套合理默认。
type ChineseOptions struct {
	// Standard 复用英文侧的规则（最小词长、停用词、全角折叠、词内撇号）。
	Standard StandardOptions

	// Dict 是追加的自定义词典内容：按空白或换行分隔，'#' 起注释。
	//
	// 每行可以写「词 词频 词性」，只取第一列——这样 gse 格式的词典文件
	// 内容也能直接贴进来。
	Dict string

	// DictPath 是追加的自定义词典文件路径；为空表示不加载。
	DictPath string

	// NoSubWords 关闭子词扩展。
	//
	// 默认开启：长词的子词也会进索引，「大学」能命中「大学生」。
	// 关闭后索引更小、写入更快，但只能整词匹配。
	NoSubWords bool
}

// ChineseAnalyzer 是中英文混合分析器。
//
// 分流规则：
//   - 连续汉字交给 gse 做词典分词；
//   - 其余部分（拉丁字母、数字、假名、标点）整块交给 StandardAnalyzer。
//
// 这样英文侧已有的行为——小写化、词内撇号、全角折叠、停用词过滤——
// 全部原样保留，只有中文部分换了算法。
//
// 加载词典约需 0.7 秒与 100 MB 上下的堆，因此**应当只构造一次并复用**。
// 构造完成后状态只读，Analyze 可并发调用（gse 的 Segment/CutSearch
// 经 -race 验证为并发安全）。
type ChineseAnalyzer struct {
	std        *StandardAnalyzer
	seg        gse.Segmenter
	noSubWords bool
}

var _ Analyzer = (*ChineseAnalyzer)(nil)

// NewChinese 创建中文分析器。
//
// 内嵌词典与自定义词条都在这里一次性加载完毕：gse 的词典在并发读取期间
// 不能被修改，所有 AddToken 必须发生在任何 Analyze 之前。
func NewChinese(opts ChineseOptions) (*ChineseAnalyzer, error) {
	seg, err := gse.NewEmbed(embeddedChineseDict)
	if err != nil {
		return nil, fmt.Errorf("analyzer: 加载内嵌中文词典失败: %w", err)
	}

	// 加载词典会打日志，交给调用方的日志系统更合适。
	seg.SkipLog = true

	custom := splitDictWords(opts.Dict)
	for _, w := range custom {
		if err := seg.AddToken(w, customWordFreq); err != nil {
			return nil, fmt.Errorf("analyzer: 追加词条 %q 失败: %w", w, err)
		}
	}

	switch {
	case opts.DictPath != "":
		// LoadDict 结尾自己会做一次 CalcToken，顺带把上面加的内联词条
		// 一起算进去，因此不需要再补。
		if err := seg.LoadDict(opts.DictPath); err != nil {
			return nil, fmt.Errorf("analyzer: 加载词典文件 %q 失败: %w", opts.DictPath, err)
		}

	case len(custom) > 0:
		// 关键一步：AddToken 只往 trie 里写，**不会**更新最短路径所需的
		// 词频统计与子词缓存，少了 CalcToken 新词根本不会被选中。
		//
		// CalcToken 是 O(词典规模)，还会重算全部词条的子词缓存，
		// 因此只能在所有词条加完之后调用一次，绝不能放进循环里。
		seg.CalcToken()
	}

	return &ChineseAnalyzer{
		std:        NewStandardWith(opts.Standard),
		seg:        seg,
		noSubWords: opts.NoSubWords,
	}, nil
}

// Analyze 实现 Analyzer。
func (a *ChineseAnalyzer) Analyze(text string) []Token {
	if text == "" {
		return nil
	}

	tokens := make([]Token, 0, estimateTokens(len(text)))
	var pos uint32

	for i := 0; i < len(text); {
		r, _ := utf8.DecodeRuneInString(text[i:])

		if isHan(r) {
			start := i
			for i < len(text) {
				r2, size2 := utf8.DecodeRuneInString(text[i:])
				if !isHan(r2) {
					break
				}
				i += size2
			}
			tokens, pos = a.analyzeHan(text[start:i], start, tokens, pos)
			continue
		}

		// 非汉字段整块交给英文规则。两个分支各自至少前进一个 rune，
		// 因此循环不会退化。
		start := i
		for i < len(text) {
			r2, size2 := utf8.DecodeRuneInString(text[i:])
			if isHan(r2) {
				break
			}
			i += size2
		}
		tokens, pos = a.std.analyzeInto(text[start:i], start, tokens, pos)
	}

	return tokens
}

// analyzeHan 对一段连续汉字分词，把结果追加到 tokens。
//
// 位置语义与英文侧一致：**每个词占一个位置，被过滤掉的词同样占位**，
// 这样中英混排时位置序列不会错乱。
func (a *ChineseAnalyzer) analyzeHan(text string, base int, tokens []Token, pos uint32) ([]Token, uint32) {
	// gse 的 Segment/CutSearch 只接受 []byte，这里转换一次；
	// 段内后续取词都在这块字节上做，不再重复转换。
	raw := []byte(text)
	words := a.seg.Segment(raw)

	for i := range words {
		w := &words[i]

		word := string(raw[w.Start():w.End()])
		term := strings.ToLower(word)

		if a.std.keep(term, utf8.RuneCountInString(term)) {
			tokens = append(tokens, Token{
				Term:     term,
				Position: pos,
				Start:    base + w.Start(),
				End:      base + w.End(),
			})

			if !a.noSubWords {
				tokens = a.appendSubWords(tokens, word, base+w.Start(), term, pos)
			}
		}
		pos++
	}

	return tokens, pos
}

// appendSubWords 为长词追加子词。
//
// 子词与父词**共享同一个位置**，这是 Lucene SynonymGraphFilter 的语义：
// 短语查询在词级与子词级都能正确对齐，而不会因为插入子词把后续位置整体推后。
//
// 代价是当查询把一个子词和后面的词拼成短语时，可能命中原文中并不相邻的
// 组合（例如「中华万岁」会命中「中华人民共和国万岁」）。
// 这是换取「大学」能命中「大学生」所付出的代价，属已知取舍。
func (a *ChineseAnalyzer) appendSubWords(tokens []Token, word string, base int, parent string, pos uint32) []Token {
	// jieba 的 cut_for_search 只对长度大于 2 的词做扩展，短词没有子词。
	if utf8.RuneCountInString(word) < 3 {
		return tokens
	}

	var seen []string

	for _, sub := range a.seg.CutSearch(word, true) {
		if sub == parent || slices.Contains(seen, sub) {
			continue
		}
		seen = append(seen, sub)

		term := strings.ToLower(sub)
		if !a.std.keep(term, utf8.RuneCountInString(term)) {
			continue
		}

		// 子词必然是本词的一段。找不到就跳过——宁可不加，
		// 也不能给出错误的原文区间，否则高亮会指到别的地方。
		//
		// 用最早出现的匹配：中文词很短，重复字导致的歧义影响有限，
		// 且结果确定可复现。
		idx := strings.Index(word, sub)
		if idx < 0 {
			continue
		}

		tokens = append(tokens, Token{
			Term:     term,
			Position: pos,
			Start:    base + idx,
			End:      base + idx + len(sub),
		})
	}

	return tokens
}

// isHan 报告 r 是否是汉字。
//
// 只把汉字交给 gse：假名、谚文等仍走英文侧的逐字规则，
// 因为当前加载的是中文词典，对它们没有帮助。
func isHan(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

// splitDictWords 把自定义词典文本拆成词条。
//
// 支持 '#' 行注释与 gse 词典的「词 词频 词性」格式（只取第一列）。
func splitDictWords(raw string) []string {
	if raw == "" {
		return nil
	}

	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	return out
}
