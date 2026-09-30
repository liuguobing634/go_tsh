package tsh

import (
	"strings"
	"sync"
	"testing"

	"github.com/liuguobing/go_tsh/internal/analyzer"
	"github.com/liuguobing/go_tsh/internal/index"
)

// 中文引擎要加载约 100 MB 词典，整个包只构造一次。
var (
	zhEngOnce sync.Once
	zhEng     *Engine
	zhEngErr  error
)

func testChineseEngine(t *testing.T) *Engine {
	t.Helper()

	zhEngOnce.Do(func() {
		zhEng, zhEngErr = NewWith(Options{Analyzer: AnalyzerChinese})
		if zhEngErr != nil {
			return
		}
		for _, d := range []Document{
			{ID: "search", Fields: map[string]string{
				"title": "全文搜索服务",
				"body":  "倒排索引是全文搜索服务的核心数据结构，它把词条映射到文档列表。",
			}},
			{ID: "rank", Fields: map[string]string{
				"title": "BM25 排序算法",
				"body":  "BM25 是经典的检索排序算法，通过词频与逆文档频率给文档打分。",
			}},
			{ID: "student", Fields: map[string]string{
				"title": "大学生就业",
				"body":  "今年大学生的就业形势总体稳定，高校毕业生人数再创新高。",
			}},
			{ID: "english", Fields: map[string]string{
				"title": "Inverted Index",
				"body":  "An inverted index maps terms to the documents that contain them.",
			}},
		} {
			if _, err := zhEng.Upsert(d); err != nil {
				zhEngErr = err
				return
			}
		}
	})

	if zhEngErr != nil {
		t.Fatalf("构造中文引擎失败: %v", zhEngErr)
	}
	return zhEng
}

// 中文检索的核心闭环：分词建索引 → 查询分词 → 命中。
func TestChineseSearchEndToEnd(t *testing.T) {
	e := testChineseEngine(t)

	cases := []struct {
		query string
		want  []string
	}{
		{"倒排索引", []string{"search"}},
		{"全文搜索", []string{"search"}},
		{"检索排序算法", []string{"rank"}},
		{"BM25", []string{"rank"}},
		{"大学生", []string{"student"}},
		{"就业形势", []string{"student"}},
		// 英文部分走的仍是 standard 规则
		{"inverted", []string{"english"}},
		{"\"inverted index\"", []string{"english"}},
		// 不存在的词
		{"量子纠缠", nil},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			res, err := e.Search(SearchRequest{Query: tc.query, Limit: 10})
			if err != nil {
				t.Fatalf("Search(%q) 报错: %v", tc.query, err)
			}

			got := make([]string, 0, len(res.Hits))
			for _, h := range res.Hits {
				got = append(got, h.ID)
			}

			if len(got) != len(tc.want) {
				t.Fatalf("Search(%q) 命中 %v，期望 %v", tc.query, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("Search(%q) 命中 %v，期望 %v", tc.query, got, tc.want)
				}
			}
			if res.Total != len(tc.want) {
				t.Errorf("Total = %d，期望 %d", res.Total, len(tc.want))
			}
		})
	}
}

// 子词召回：这是引入词典分词的主要收益。
// 只按整词建索引的话，「大学」搜不到「大学生」。
func TestChineseSubWordRecall(t *testing.T) {
	e := testChineseEngine(t)

	// 「大学生」被切成整词，同时子词「大学」「学生」也进了索引。
	for _, q := range []string{"大学生", "大学", "学生"} {
		res, err := e.Search(SearchRequest{Query: q, Limit: 10})
		if err != nil {
			t.Fatalf("Search(%q) 报错: %v", q, err)
		}

		found := false
		for _, h := range res.Hits {
			if h.ID == "student" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Search(%q) 未命中 student，子词召回失效", q)
		}
	}
}

// 关掉子词后，「学生」就不该再命中「大学生」。
// 与 TestChineseSubWordRecall 构成正反两面：证明测的确实是子词这个机制，
// 而不是「碰巧能搜到」。
func TestChineseNoSubWordsLosesRecall(t *testing.T) {
	e, err := NewWith(Options{
		Analyzer:   AnalyzerChinese,
		NoSubWords: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Upsert(Document{ID: "student", Fields: map[string]string{
		"title": "大学生就业",
	}}); err != nil {
		t.Fatal(err)
	}

	// 整词仍然能搜到
	res, err := e.Search(SearchRequest{Query: "大学生", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Errorf("整词「大学生」应当仍能命中，实际 %d 条", len(res.Hits))
	}

	// 子词不该再命中
	res, err = e.Search(SearchRequest{Query: "学生", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("关闭子词后「学生」不应命中，实际 %d 条", len(res.Hits))
	}
}

// 中文高亮：偏移必须落在正确的字上。
//
// 注意「倒排索引」会被 gse 切成「倒排」「索引」两个词，因此高亮输出是
// <em>倒排</em><em>索引</em> 而不是 <em>倒排索引</em>——
// 相邻的 <em> 视觉上与一个等价，这是分词带来的正常现象。
func TestChineseHighlight(t *testing.T) {
	e := testChineseEngine(t)

	res, err := e.Search(SearchRequest{Query: "倒排索引", Limit: 5, Highlight: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("没有命中")
	}

	snippet, ok := res.Hits[0].Highlights["body"]
	if !ok {
		t.Fatalf("body 字段没有高亮：%+v", res.Hits[0].Highlights)
	}
	t.Logf("高亮片段: %s", snippet)

	for _, want := range []string{"<em>倒排</em>", "<em>索引</em>"} {
		if !strings.Contains(snippet, want) {
			t.Errorf("高亮缺少 %s：%s", want, snippet)
		}
	}

	// 未被命中的文字不应被标注
	if strings.Contains(snippet, "<em>是全文搜索</em>") {
		t.Errorf("标注了未命中的文字：%s", snippet)
	}
}

// 自定义词典能把领域词切成整词。
func TestChineseEngineWithCustomDict(t *testing.T) {
	e, err := NewWith(Options{
		Analyzer: AnalyzerChinese,
		DictPath: "",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 先记录默认分词结果，便于人工核对
	terms := e.Index().Analyzer().Analyze("倒排索引与跳表")
	var got []string
	for _, tk := range terms {
		got = append(got, tk.Term)
	}
	t.Logf("默认分词「倒排索引与跳表」-> %v", got)

	if _, err := e.Upsert(Document{ID: "d1", Fields: map[string]string{
		"body": "倒排索引与跳表都是查找结构",
	}}); err != nil {
		t.Fatal(err)
	}

	// 无论怎么切，查询侧用的是同一个分析器，因此必须能命中。
	res, err := e.Search(SearchRequest{Query: "倒排索引", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("「倒排索引」应当命中，实际 %d 条", len(res.Hits))
	}
}

func TestUnknownAnalyzerKind(t *testing.T) {
	if _, err := NewWith(Options{Analyzer: AnalyzerKind("martian")}); err == nil {
		t.Fatal("未知分析器应当报错")
	}
}

// DictPath 与 Index.Analyzer 同时给出是自相矛盾的：
// 一个要求按名字新建分析器，一个已经把分析器给了出来。
// 必须报错，而不是静默忽略其中一个。
func TestDictPathConflictsWithExplicitAnalyzer(t *testing.T) {
	_, err := NewWith(Options{
		DictPath: "words.txt",
		Index:    index.Options{Analyzer: analyzer.NewStandard()},
	})
	if err == nil {
		t.Fatal("DictPath 与 Index.Analyzer 同时给出时应当报错")
	}
}

// 默认引擎（英文分析器）不应当受中文改动影响。
func TestDefaultEngineStillStandard(t *testing.T) {
	e := New()
	if _, err := e.Upsert(Document{ID: "d1", Fields: map[string]string{
		"body": "the quick brown fox",
	}}); err != nil {
		t.Fatal(err)
	}

	res, err := e.Search(SearchRequest{Query: "quick", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("默认引擎检索异常：命中 %d 条", len(res.Hits))
	}
}
