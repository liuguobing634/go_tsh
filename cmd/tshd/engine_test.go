package main

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// 中文引擎要加载约 100 MB 词典（约 0.7 秒），因此**只读用途**共用一份。
//
// 会写索引的测试必须用 newChineseEngine 拿一份全新的引擎：
// 共用可变状态会让测试互相污染，而且症状是"换个顺序就挂"这种最难查的形态。
var (
	zhOnce sync.Once
	zhEng  *tsh.Engine
	zhErr  error
)

// sharedChineseEngine 只用于分析器行为这类不写索引的测试。
func sharedChineseEngine(t *testing.T) *tsh.Engine {
	t.Helper()

	zhOnce.Do(func() {
		cfg := config.Default()
		cfg.Analyzer = config.AnalyzerChinese
		zhEng, zhErr = newEngine(cfg)
	})

	if zhErr != nil {
		t.Fatalf("构造中文引擎失败: %v", zhErr)
	}
	return zhEng
}

// newChineseEngine 返回一份全新的中文引擎，测试之间互不影响。
func newChineseEngine(t *testing.T) *tsh.Engine {
	t.Helper()

	cfg := config.Default()
	cfg.Analyzer = config.AnalyzerChinese

	e, err := newEngine(cfg)
	if err != nil {
		t.Fatalf("构造中文引擎失败: %v", err)
	}
	return e
}

// termsOf 取出引擎分析器对 text 产出的词条。
func termsOf(e *tsh.Engine, text string) []string {
	tokens := e.Index().Analyzer().Analyze(text)
	out := make([]string, 0, len(tokens))
	for _, tk := range tokens {
		out = append(out, tk.Term)
	}
	return out
}

// wordsDoc 造一篇含 n 个英文词的文档，用于触发 token 数上限。
func wordsDoc(n int) tsh.Document {
	words := make([]string, n)
	for i := range words {
		words[i] = fmt.Sprintf("word%d", i)
	}
	return tsh.Document{ID: "d", Fields: map[string]string{"body": strings.Join(words, " ")}}
}

// 这三个上限曾经只被解析与校验，从未接进引擎。
// 这条测试确保它们真的生效，而不是又一次"配置写了但没人用"。
func TestConfiguredLimitsAreActuallyApplied(t *testing.T) {
	t.Run("max-doc-tokens", func(t *testing.T) {
		cfg := config.Default()
		cfg.MaxDocTokens = 3

		e, err := newEngine(cfg)
		if err != nil {
			t.Fatal(err)
		}

		if err := e.Create(wordsDoc(10)); err == nil {
			t.Fatal("超出 max-doc-tokens 的文档应当被拒绝")
		} else {
			t.Logf("超限报错: %v", err)
		}

		// 上限之内的文档必须通过，否则测的就不是"上限"而是"全拒"
		if err := e.Create(wordsDoc(2)); err != nil {
			t.Errorf("未超限的文档不应被拒绝: %v", err)
		}
	})

	t.Run("max-doc-fields", func(t *testing.T) {
		cfg := config.Default()
		cfg.MaxDocFields = 1

		e, err := newEngine(cfg)
		if err != nil {
			t.Fatal(err)
		}

		doc := wordsDoc(1)
		doc.Fields["extra"] = "another field"

		if err := e.Create(doc); err == nil {
			t.Fatal("超出 max-doc-fields 的文档应当被拒绝")
		}
	})

	t.Run("max-query-terms", func(t *testing.T) {
		cfg := config.Default()
		cfg.MaxQueryTerms = 3

		e, err := newEngine(cfg)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := e.Search(tsh.SearchRequest{Query: "alpha beta gamma delta epsilon"}); err == nil {
			t.Fatal("超出 max-query-terms 的查询应当被拒绝")
		}

		if _, err := e.Search(tsh.SearchRequest{Query: "alpha beta"}); err != nil {
			t.Errorf("未超限的查询不应被拒绝: %v", err)
		}
	})
}

// analyzer 配置必须真的换掉分析器，而不是被忽略。
func TestAnalyzerConfigIsApplied(t *testing.T) {
	t.Run("chinese 走词典分词", func(t *testing.T) {
		e := sharedChineseEngine(t)

		terms := termsOf(e, "全文搜索")
		t.Logf("chinese: %v", terms)

		if !slices.Contains(terms, "全文") || !slices.Contains(terms, "搜索") {
			t.Errorf("中文分析器应当分出「全文」「搜索」，实际 %v", terms)
		}
		if slices.Contains(terms, "全") {
			t.Errorf("中文分析器不应再输出单字「全」，实际 %v", terms)
		}
	})

	// 标准分析器对汉字是**逐字**索引的。
	//
	// 注意 isUnigramScript 分支直接 append，绕过了 MinTokenLen 过滤——
	// 这是有意的：汉字单字本身就有意义。也就是说中文检索在引入
	// 中文分析器之前就能用，只是粒度停在单字。
	t.Run("standard 对汉字逐字索引", func(t *testing.T) {
		e, err := newEngine(config.Default())
		if err != nil {
			t.Fatal(err)
		}

		terms := termsOf(e, "全文搜索服务")
		t.Logf("standard: %v", terms)

		want := []string{"全", "文", "搜", "索", "服", "务"}
		if len(terms) != len(want) {
			t.Fatalf("standard 应当逐字切分，实际 %v", terms)
		}
		for i := range want {
			if terms[i] != want[i] {
				t.Fatalf("standard 应当逐字切分，实际 %v", terms)
			}
		}
	})
}

// 两个分析器的差别到底在哪？
//
// 一个反直觉的事实：裸词被拆成多个 token 时，执行器按**短语**求值
// （位置必须连续）。所以标准分析器对中文做的是逐字连续匹配，
// 本质上等价于子串匹配，精度并不差——
// 「全新文档搜索系统」里 全@0 与 文@2 不相邻，不会被「全文搜索」召回。
//
// 真正的差别在**索引形态**：逐字索引会为 的/是/在 这类高频功能字
// 建立 df 接近 N 的 posting 列表，它们对打分的贡献接近 0，
// 却让查询变慢、索引变大。词级索引没有这个问题。
func TestChineseAnalyzerIndexShapeComparison(t *testing.T) {
	const corpus = "全文搜索服务的倒排索引是核心数据结构，它把词条映射到文档列表。" +
		"检索时通过BM25算法给文档打分，分数越高的文档越相关。" +
		"中文分词是中文检索的基础，好的分词能显著提升召回与精度，" +
		"因此在中文场景下应当使用合适的分析器。"

	std, err := newEngine(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	zh := newChineseEngine(t)

	for name, e := range map[string]*tsh.Engine{"standard": std, "chinese": zh} {
		if _, err := e.Upsert(tsh.Document{ID: "d1", Fields: map[string]string{"body": corpus}}); err != nil {
			t.Fatal(err)
		}
		t.Logf("%-8s 词条=%d 索引=%d 字节", name, e.Stats().Terms, e.Stats().IndexBytes)
		t.Logf("%-8s %v", name, termsOf(e, corpus))
	}

	// 逐字索引的词条数必然远多于词级索引。这是两者最稳固、最可测的差别。
	if std.Stats().Terms <= zh.Stats().Terms {
		t.Errorf("逐字索引的词条数（%d）应当远多于词级索引（%d）",
			std.Stats().Terms, zh.Stats().Terms)
	}

	// 高频功能字：标准分析器会把「的」当成词条，中文分析器会把它过滤掉。
	if !slices.Contains(termsOf(std, "的"), "的") {
		t.Error("标准分析器应当保留单字「的」（汉字逐字索引）")
	}
	if slices.Contains(termsOf(zh, "的"), "的") {
		t.Error("中文分析器应当把单字「的」过滤掉（MinTokenLen=2），它没有区分度")
	}
}

// 召回上两者是一致的：连续的汉字串，两个分析器都能命中。
// 这条测试记录的是"差别不在召回"，避免日后误以为换分析器会改变召回。
func TestBothAnalyzersRecallContiguousChinese(t *testing.T) {
	doc := tsh.Document{ID: "d1", Fields: map[string]string{
		"body": "倒排索引是全文搜索服务的核心数据结构",
	}}

	std, err := newEngine(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	zh := newChineseEngine(t)

	for _, e := range []*tsh.Engine{std, zh} {
		if _, err := e.Upsert(doc); err != nil {
			t.Fatal(err)
		}
	}

	for _, q := range []string{"全文搜索", "倒排索引", "搜索服务"} {
		for name, e := range map[string]*tsh.Engine{"standard": std, "chinese": zh} {
			res, err := e.Search(tsh.SearchRequest{Query: q, Limit: 5})
			if err != nil {
				t.Fatalf("%s Search(%q): %v", name, q, err)
			}
			if res.Total != 1 {
				t.Errorf("%s 对连续汉字查询 %q 应当命中 1 篇，实际 %d", name, q, res.Total)
			}
		}
	}
}

// 端到端：配置成中文后，中文查询必须能命中中文文档。
func TestChineseConfigEndToEnd(t *testing.T) {
	e := newChineseEngine(t)

	if err := e.Create(tsh.Document{ID: "search", Fields: map[string]string{
		"body": "倒排索引是全文搜索服务的核心数据结构",
	}}); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"全文搜索", "倒排索引", "核心数据结构"} {
		res, err := e.Search(tsh.SearchRequest{Query: q, Limit: 5})
		if err != nil {
			t.Fatalf("Search(%q) 报错: %v", q, err)
		}
		if res.Total != 1 {
			t.Errorf("Search(%q) 应当命中 1 篇，实际 %d", q, res.Total)
		}
	}
}

// 反向验证：标准分析器对中文是能用的（逐字），只是粒度不同。
// 这条测试防止日后有人误以为"引入中文分析器之前中文完全搜不到"。
func TestStandardAnalyzerCanAlsoSearchChinese(t *testing.T) {
	e, err := newEngine(config.Default())
	if err != nil {
		t.Fatal(err)
	}

	if err := e.Create(tsh.Document{ID: "search", Fields: map[string]string{
		"body": "倒排索引是全文搜索服务的核心数据结构",
	}}); err != nil {
		t.Fatal(err)
	}

	res, err := e.Search(tsh.SearchRequest{Query: "全文搜索", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Fatalf("标准分析器按单字也能搜到中文，实际命中 %d", res.Total)
	}
}
