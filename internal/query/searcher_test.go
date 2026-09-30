package query

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/liuguobing/go_tsh/internal/analyzer"
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/scoring"
)

// keepAll 返回一个既不过滤停用词、也不过滤短词的分析器。
//
// 检索测试里大量使用 go / a / it 这类词，用默认分析器会让它们
// 悄悄消失，断言随之失效。
func keepAll() analyzer.Analyzer {
	return analyzer.NewStandardWith(analyzer.StandardOptions{
		KeepStopwords: true,
		MinTokenLen:   1,
	})
}

func newSearcherIndex(t *testing.T) *index.InvertedIndex {
	t.Helper()

	ix := index.New(index.Options{Analyzer: keepAll()})

	docs := []struct {
		id     string
		fields map[string]string
	}{
		{"d1", map[string]string{
			"title": "go search",
			"body":  "fast inverted index written in go",
		}},
		{"d2", map[string]string{
			"title": "search engines",
			"body":  "a search engine relies on an inverted index",
		}},
		{"d3", map[string]string{
			"title": "baking bread",
			"body":  "knead the dough then bake it",
		}},
	}

	for _, d := range docs {
		if _, err := ix.Add(d.id, d.fields); err != nil {
			t.Fatal(err)
		}
	}
	return ix
}

// externalIDs 把命中结果映射回外部 ID，顺序保持不变。
func externalIDs(t *testing.T, ix *index.InvertedIndex, hits []Hit) []string {
	t.Helper()

	out := make([]string, len(hits))
	ix.View(func(v *index.View) {
		for i, h := range hits {
			doc, ok := v.Document(h.ID)
			if !ok {
				t.Fatalf("DocID %d 不存在", h.ID)
			}
			out[i] = doc.External
		}
	})
	return out
}

// assertHitSet 无视顺序地比较命中文档集合。
func assertHitSet(t *testing.T, ix *index.InvertedIndex, hits []Hit, want ...string) {
	t.Helper()

	got := externalIDs(t, ix, hits)
	slices.Sort(got)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Fatalf("命中文档 = %v, want %v", got, want)
	}
}

func docID(t *testing.T, ix *index.InvertedIndex, external string) index.DocID {
	t.Helper()

	d, ok := ix.Get(external)
	if !ok {
		t.Fatalf("文档 %q 不存在", external)
	}
	return d.ID
}

// mustParse 解析查询串并要求成功。
func mustParse(t *testing.T, q string, opts Options) Node {
	t.Helper()

	n, err := Parse(q, opts)
	if err != nil {
		t.Fatalf("Parse(%q) 失败: %v", q, err)
	}
	return n
}

// ---------------------------------------------------------------- 基本检索

func TestSearchSingleTerm(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	got, err := s.Search(&Term{Text: "inverted"}, SearchOptions{})
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if got.Total != 2 {
		t.Errorf("Total = %d, want 2", got.Total)
	}
	assertHitSet(t, ix, got.Hits, "d1", "d2")
}

func TestSearchNoMatch(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	got, err := s.Search(&Term{Text: "nonexistent"}, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 0 || len(got.Hits) != 0 {
		t.Errorf("应无命中，实际 Total=%d Hits=%v", got.Total, got.Hits)
	}
}

func TestSearchOnEmptyIndex(t *testing.T) {
	ix := index.New(index.Options{Analyzer: keepAll()})
	s := NewSearcher(ix, scoring.BM25{})

	got, err := s.Search(&Term{Text: "anything"}, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 0 || len(got.Hits) != 0 {
		t.Errorf("空索引应无命中，实际 %+v", got)
	}
}

func TestSearchNilQuery(t *testing.T) {
	s := NewSearcher(newSearcherIndex(t), scoring.BM25{})

	if _, err := s.Search(nil, SearchOptions{}); !errors.Is(err, ErrNilQuery) {
		t.Fatalf("err = %v, want ErrNilQuery", err)
	}
}

// 查询与写入必须共用同一个分析器，否则「文档明明存在却检索不到」。
func TestSearchReusesIndexAnalyzer(t *testing.T) {
	ix := index.New(index.Options{Analyzer: keepAll()})
	if _, err := ix.Add("d1", map[string]string{"body": "Hello WORLD"}); err != nil {
		t.Fatal(err)
	}

	s := NewSearcher(ix, scoring.BM25{})

	for _, q := range []string{"hello", "HELLO", "WoRLd", "world"} {
		got, err := s.Search(mustParse(t, q, Options{}), SearchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		assertHitSet(t, ix, got.Hits, "d1")
	}
}

// 整体被停用词表吃掉的词条不应命中任何文档。
func TestSearchTermAnalyzedAway(t *testing.T) {
	ix := index.New(index.Options{}) // 默认分析器，带停用词过滤
	if _, err := ix.Add("d1", map[string]string{"body": "hello world"}); err != nil {
		t.Fatal(err)
	}

	s := NewSearcher(ix, scoring.BM25{})

	got, err := s.Search(mustParse(t, "the", Options{}), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 0 {
		t.Errorf("停用词不应命中任何文档，实际 Total=%d", got.Total)
	}
}

// ---------------------------------------------------------------- 打分

// 同一词条命中多个字段时，各字段贡献相加，
// 因此跨字段命中的文档应排在只命中一个字段的文档之前。
func TestSearchScoresSumAcrossFields(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	// "search" 在 d1 只有 title 命中，在 d2 的 title 与 body 都命中。
	got, err := s.Search(&Term{Text: "search"}, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 {
		t.Fatalf("Total = %d, want 2", got.Total)
	}

	d2 := docID(t, ix, "d2")
	if got.Hits[0].ID != d2 {
		t.Errorf("跨两个字段命中的 d2 应排第一，实际首位是 %s",
			externalIDs(t, ix, got.Hits[:1])[0])
	}
}

func TestSearchDefaultBM25WhenZero(t *testing.T) {
	ix := newSearcherIndex(t)

	zero := NewSearcher(ix, scoring.BM25{})
	def := NewSearcher(ix, scoring.DefaultBM25())

	a, err := zero.Search(&Term{Text: "inverted"}, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := def.Search(&Term{Text: "inverted"}, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(a.Hits, b.Hits) {
		t.Errorf("零值 BM25 应等价于 DefaultBM25\n got: %v\nwant: %v", a.Hits, b.Hits)
	}
}

// ---------------------------------------------------------------- 布尔语义

func TestSearchOrSemantics(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	got, err := s.Search(mustParse(t, "inverted bread", Options{}), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertHitSet(t, ix, got.Hits, "d1", "d2", "d3")
}

func TestSearchAndSemantics(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	t.Run("同时命中的文档", func(t *testing.T) {
		got, err := s.Search(mustParse(t, "inverted AND index", Options{}), SearchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		assertHitSet(t, ix, got.Hits, "d1", "d2")
	})

	t.Run("无交集则为空", func(t *testing.T) {
		got, err := s.Search(mustParse(t, "inverted AND bread", Options{}), SearchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Total != 0 {
			t.Errorf("Total = %d, want 0", got.Total)
		}
	})

	t.Run("DefaultOpAnd 让相邻词变成与", func(t *testing.T) {
		node := mustParse(t, "inverted index", Options{DefaultOp: OpAnd})
		got, err := s.Search(node, SearchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		assertHitSet(t, ix, got.Hits, "d1", "d2")
	})
}

// Should 在 Must 非空时只加分、不参与筛选。
//
// 解析器产不出这种形状（语法上 Must 与 Should 不会共存），
// 所以这里手工构造 AST——AST 本身是对外公开的。
func TestSearchShouldOnlyBoostsWhenMustPresent(t *testing.T) {
	ix := index.New(index.Options{Analyzer: keepAll()})
	if _, err := ix.Add("d1", map[string]string{"body": "alpha beta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "alpha gamma"}); err != nil {
		t.Fatal(err)
	}

	s := NewSearcher(ix, scoring.BM25{})

	node := &Bool{
		Must:   []Node{&Term{Text: "alpha"}},
		Should: []Node{&Term{Text: "beta"}},
	}

	got, err := s.Search(node, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// d2 没有 beta，但它满足 Must，仍应命中。
	assertHitSet(t, ix, got.Hits, "d1", "d2")

	// d1 因 beta 加分而应排在前面。
	if got.Hits[0].ID != docID(t, ix, "d1") {
		t.Errorf("Should 命中应加分，d1 应排第一，实际 %v", externalIDs(t, ix, got.Hits))
	}
}

// MustNot 与正向子句并存时的筛选行为。
func TestSearchMustNotWithMust(t *testing.T) {
	ix := index.New(index.Options{Analyzer: keepAll()})
	if _, err := ix.Add("d1", map[string]string{"body": "alpha beta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "alpha gamma"}); err != nil {
		t.Fatal(err)
	}

	s := NewSearcher(ix, scoring.BM25{})

	node := &Bool{
		Must:    []Node{&Term{Text: "alpha"}},
		MustNot: []Node{&Term{Text: "gamma"}},
	}

	got, err := s.Search(node, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertHitSet(t, ix, got.Hits, "d1")
}

func TestSearchNegation(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	// d2 的 body 里有 engine，应被排除。
	got, err := s.Search(mustParse(t, "search -engine", Options{}), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertHitSet(t, ix, got.Hits, "d1")
}

// 只有否定子句时，语义是「全量文档减去这些子句」。
func TestSearchOnlyNegativeClause(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	got, err := s.Search(mustParse(t, "-bread", Options{}), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 {
		t.Errorf("Total = %d, want 2", got.Total)
	}
	assertHitSet(t, ix, got.Hits, "d1", "d2")
}

// ---------------------------------------------------------------- 短语

// 短语匹配的核心语义：要求的是「位置差等于查询侧的位置差」，
// 而不是「位置差等于 1」。
//
// 这个用例用默认分析器（带停用词表），"the" 会被过滤但**位置仍被占用**，
// 于是查询 "quick the brown" 分析成 quick@0 / brown@2，位置差是 2。
// 若按朴素实现（差 1）去匹配，命中的会是 d2 —— 恰好是错的。
func TestPhraseUsesAnalyzerPositionGaps(t *testing.T) {
	ix := index.New(index.Options{}) // 默认分析器，停用词过滤开启

	if _, err := ix.Add("d1", map[string]string{"body": "quick the brown fox"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "quick brown fox"}); err != nil {
		t.Fatal(err)
	}

	s := NewSearcher(ix, scoring.BM25{})

	search := func(q string) []string {
		t.Helper()
		got, err := s.Search(mustParse(t, q, Options{}), SearchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return externalIDs(t, ix, got.Hits)
	}

	t.Run("带停用词的短语只命中保留了间隔的文档", func(t *testing.T) {
		if got := search(`"quick the brown"`); !slices.Equal(got, []string{"d1"}) {
			t.Fatalf("命中 %v，want [d1]（d2 里 quick/brown 相邻、位置差为 1，不应命中）", got)
		}
	})

	t.Run("相邻短语只命中真正相邻的文档", func(t *testing.T) {
		if got := search(`"quick brown"`); !slices.Equal(got, []string{"d2"}) {
			t.Fatalf("命中 %v，want [d2]（d1 里 quick/brown 相隔 2，不应命中）", got)
		}
	})
}

// 短语必须在**单个字段内**成立。各字段的位置都从 0 开始，
// 跨字段拼位置会造出根本不存在的短语。
func TestPhraseMustMatchWithinOneField(t *testing.T) {
	ix := index.New(index.Options{Analyzer: keepAll()})

	if _, err := ix.Add("d1", map[string]string{"title": "alpha", "body": "beta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"title": "alpha beta", "body": "gamma"}); err != nil {
		t.Fatal(err)
	}

	s := NewSearcher(ix, scoring.BM25{})

	got, err := s.Search(mustParse(t, `"alpha beta"`, Options{}), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertHitSet(t, ix, got.Hits, "d2")
}

func TestPhraseNotMatching(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	// "index written" 在 d1 里相隔 1？d1 body = fast inverted index written in go
	// → index@2, written@3，确实相邻，会命中。
	// 改用 "written fast"（顺序颠倒）验证不命中。
	got, err := s.Search(mustParse(t, `"written fast"`, Options{}), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 0 {
		t.Errorf("顺序颠倒的短语不应命中，实际 %v", externalIDs(t, ix, got.Hits))
	}
}

// 单词条被分析器切成多个 token 时按短语处理，而不是拆成 OR。
func TestSearchTermSplittingIntoPhrase(t *testing.T) {
	ix := index.New(index.Options{Analyzer: keepAll()})

	if _, err := ix.Add("d1", map[string]string{"body": "full width test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "full of width"}); err != nil {
		t.Fatal(err)
	}

	s := NewSearcher(ix, scoring.BM25{})

	got, err := s.Search(mustParse(t, "full-width", Options{}), SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertHitSet(t, ix, got.Hits, "d1")
}

// ---------------------------------------------------------------- 字段与分页

func TestSearchFieldRestriction(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	// "go" 只在 d1 出现（title 与 body 各一次）。
	node := mustParse(t, "go", Options{})

	cases := []struct {
		name   string
		fields []string
		want   []string
	}{
		{"全部字段", nil, []string{"d1"}},
		{"限定 title", []string{"title"}, []string{"d1"}},
		{"限定 body", []string{"body"}, []string{"d1"}},
		{"不存在的字段", []string{"nope"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Search(node, SearchOptions{Fields: tc.fields})
			if err != nil {
				t.Fatal(err)
			}
			assertHitSet(t, ix, got.Hits, tc.want...)
		})
	}
}

func TestSearchPagination(t *testing.T) {
	ix := newSearcherIndex(t)
	s := NewSearcher(ix, scoring.BM25{})

	node := mustParse(t, "inverted", Options{}) // d1, d2

	page1, err := s.Search(node, SearchOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page1.Total != 2 || len(page1.Hits) != 1 {
		t.Fatalf("第一页 = %+v", page1)
	}

	page2, err := s.Search(node, SearchOptions{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page2.Total != 2 || len(page2.Hits) != 1 {
		t.Fatalf("第二页 = %+v", page2)
	}

	if page1.Hits[0].ID == page2.Hits[0].ID {
		t.Error("分页出现了重复项")
	}

	beyond, err := s.Search(node, SearchOptions{Limit: 1, Offset: 99})
	if err != nil {
		t.Fatal(err)
	}
	if beyond.Total != 2 || len(beyond.Hits) != 0 {
		t.Errorf("越界分页应返回空页但保留 Total，实际 %+v", beyond)
	}
}

func TestSearchLimitClamped(t *testing.T) {
	ix := index.New(index.Options{Analyzer: keepAll()})
	for i := 0; i < 150; i++ {
		if _, err := ix.Add(fmt.Sprintf("doc-%03d", i), map[string]string{"body": "same token here"}); err != nil {
			t.Fatal(err)
		}
	}

	s := NewSearcher(ix, scoring.BM25{})

	got, err := s.Search(mustParse(t, "token", Options{}), SearchOptions{Limit: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 150 {
		t.Errorf("Total = %d, want 150", got.Total)
	}
	if len(got.Hits) != maxLimit {
		t.Errorf("返回 %d 条，应被截到 %d", len(got.Hits), maxLimit)
	}
}

// 同分时的次序必须稳定：否则分页会出现重复或漏项。
func TestSearchTieBreakIsDeterministic(t *testing.T) {
	ix := index.New(index.Options{Analyzer: keepAll()})
	for i := 0; i < 30; i++ {
		if _, err := ix.Add(fmt.Sprintf("doc-%02d", i), map[string]string{"body": "same token here"}); err != nil {
			t.Fatal(err)
		}
	}

	s := NewSearcher(ix, scoring.BM25{})
	node := mustParse(t, "token", Options{})

	first, err := s.Search(node, SearchOptions{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		again, err := s.Search(node, SearchOptions{Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(first.Hits, again.Hits) {
			t.Fatalf("第 %d 次结果与首次不一致\n got: %v\nwant: %v", i, again.Hits, first.Hits)
		}
	}

	// 全部同分，因此应当严格按 DocID 升序。
	for i := 1; i < len(first.Hits); i++ {
		if first.Hits[i].ID <= first.Hits[i-1].ID {
			t.Fatalf("同分时应按 DocID 升序，实际 %v", first.Hits)
		}
	}
}

// ---------------------------------------------------------------- 基准测试

const benchDocs = 20_000

var (
	benchResult Result

	// 语料只构建一次。
	//
	// 早先的写法是在每个基准函数里调 benchmarkIndex，虽然放在了 ResetTimer
	// 之前，但 CPU profile 覆盖的是整个测试进程——建 2 万篇文档的时间会
	// 混进 profile，把真正的热点淹掉。第一次做 pprof 时就踩了这个坑。
	benchOnce sync.Once
	benchIdx  *index.InvertedIndex
)

func benchCorpus() *index.InvertedIndex {
	benchOnce.Do(func() {
		ix := index.New(index.Options{Analyzer: keepAll()})
		for i := 0; i < benchDocs; i++ {
			if _, err := ix.Add(fmt.Sprintf("doc-%d", i), map[string]string{
				"title": "the quick brown fox",
				"body":  "the quick brown fox jumps over the lazy dog and keeps running",
			}); err != nil {
				panic(err)
			}
		}
		benchIdx = ix
	})
	return benchIdx
}

// benchSearch 是三个检索基准的公共骨架。
func benchSearch(b *testing.B, q string, opts Options, sopts SearchOptions) {
	b.Helper()

	node, err := Parse(q, opts)
	if err != nil {
		b.Fatal(err)
	}
	s := NewSearcher(benchCorpus(), scoring.BM25{})

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		got, err := s.Search(node, sopts)
		if err != nil {
			b.Fatal(err)
		}
		benchResult = got
	}
}

func BenchmarkSearchTerm(b *testing.B) {
	benchSearch(b, "quick", Options{}, SearchOptions{Limit: 10})
}

func BenchmarkSearchPhrase(b *testing.B) {
	benchSearch(b, `"quick brown"`, Options{}, SearchOptions{Limit: 10})
}

func BenchmarkSearchAnd(b *testing.B) {
	benchSearch(b, "quick brown", Options{DefaultOp: OpAnd}, SearchOptions{Limit: 10})
}

// 罕见词：候选集很小，用来区分「候选多寡」与「单候选成本」。
func BenchmarkSearchRareTerm(b *testing.B) {
	benchSearch(b, "running", Options{}, SearchOptions{Limit: 10})
}

func BenchmarkSearchMustNot(b *testing.B) {
	benchSearch(b, "quick -running", Options{}, SearchOptions{Limit: 10})
}
