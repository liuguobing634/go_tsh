package analyzer

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// 加载词典要 0.7 秒、约 100 MB 堆，整个测试包只做一次。
var (
	zhOnce sync.Once
	zhSeg  *ChineseAnalyzer
	zhErr  error
)

func testChinese(t *testing.T) *ChineseAnalyzer {
	t.Helper()

	zhOnce.Do(func() { zhSeg, zhErr = NewChinese(ChineseOptions{}) })
	if zhErr != nil {
		t.Fatalf("创建中文分析器失败: %v", zhErr)
	}
	return zhSeg
}

// render 把 token 序列渲染成 "词@位置" 便于阅读与断言。
//
// 中文会有多个 token 落在同一位置（词与子词），因此顺序必须保留，
// 不能像英文那样排序后比较。
func render(tokens []Token) string {
	parts := make([]string, len(tokens))
	for i, tk := range tokens {
		parts[i] = fmt.Sprintf("%s@%d", tk.Term, tk.Position)
	}
	return strings.Join(parts, " ")
}

func TestChineseAnalyze(t *testing.T) {
	a := testChinese(t)

	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "基本分词",
			text: "全文搜索服务",
			want: "全文@0 搜索@1 服务@2",
		},
		{
			name: "长词带子词",
			text: "大学生",
			want: "大学生@0 大学@0 学生@0",
		},
		{
			name: "中英混排",
			text: "Go语言search",
			want: "go@0 语言@1 search@2",
		},
		{
			name: "英文规则不受影响",
			text: "Hello 全文 World",
			want: "hello@0 全文@1 world@2",
		},
		{
			name: "标点作分隔",
			text: "全文，搜索。服务",
			want: "全文@0 搜索@1 服务@2",
		},
		{
			// 「第」「年」都是单字词，被 MinTokenLen=2 过滤掉，
			// 但仍各占一个位置，因此 2024 落在位置 1。
			name: "单字词被过滤但占位",
			text: "第 2024 年",
			want: "2024@1",
		},
		{
			name: "空串",
			text: "",
			want: "",
		},
		{
			name: "纯标点",
			text: "，。！？",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(a.Analyze(tc.text)); got != tc.want {
				t.Fatalf("Analyze(%q)\n got: %s\nwant: %s", tc.text, got, tc.want)
			}
		})
	}
}

// 位置必须严格递增，且在混排文本上保持连续——
// 短语查询依赖这一点。
func TestChinesePositionsAreIncreasing(t *testing.T) {
	a := testChinese(t)

	texts := []string{
		"全文搜索服务需要考虑倒排索引",
		"Go语言search2024年12月",
		"北京大学生前来应聘，他说 don't stop",
		"中华人民共和国万岁",
	}

	for _, text := range texts {
		tokens := a.Analyze(text)
		if len(tokens) == 0 {
			t.Fatalf("Analyze(%q) 返回空", text)
		}

		for i := 1; i < len(tokens); i++ {
			if tokens[i].Position < tokens[i-1].Position {
				t.Fatalf("位置必须非递减：Analyze(%q)\n %v", text, tokens)
			}
		}
		if tokens[0].Position != 0 {
			t.Errorf("首个 token 位置应为 0，实际 %d（%q）", tokens[0].Position, text)
		}
	}
}

// 强不变式：text[Start:End] 就是该词条在原文中的区间。
// 这条一旦破坏，高亮就会指错地方，而且错得很隐蔽。
func TestChineseOffsetsPointAtTheTerm(t *testing.T) {
	a := testChinese(t)

	texts := []string{
		"全文搜索服务需要考虑倒排索引",
		"Go语言search2024年12月",
		"北京大学生前来应聘",
		"中华人民共和国万岁",
		"don't stop 全文搜索",
	}

	for _, text := range texts {
		for _, tok := range a.Analyze(text) {
			if tok.Start < 0 || tok.End > len(text) || tok.Start >= tok.End {
				t.Fatalf("区间非法 [%d,%d)，文本 %q", tok.Start, tok.End, text)
			}

			raw := text[tok.Start:tok.End]
			if strings.ToLower(raw) != tok.Term {
				t.Errorf("text[%d:%d] = %q，与词条 %q 不一致（文本 %q）",
					tok.Start, tok.End, raw, tok.Term, text)
			}
		}
	}
}

// 子词与父词必须共享同一个位置，且子词的原文区间落在父词区间之内。
func TestChineseSubWordsShareParentPosition(t *testing.T) {
	a := testChinese(t)

	tokens := a.Analyze("大学生前来应聘")

	// 找出父词 大学生
	var parent *Token
	for i := range tokens {
		if tokens[i].Term == "大学生" {
			parent = &tokens[i]
			break
		}
	}
	if parent == nil {
		t.Fatalf("没有分出「大学生」这个词：%s", render(tokens))
	}

	subs := map[string]Token{}
	for _, tok := range tokens {
		if tok.Position == parent.Position && tok.Term != parent.Term {
			subs[tok.Term] = tok
		}
	}

	for _, want := range []string{"大学", "学生"} {
		sub, ok := subs[want]
		if !ok {
			t.Fatalf("缺少子词 %q：%s", want, render(tokens))
		}
		if sub.Start < parent.Start || sub.End > parent.End {
			t.Errorf("子词 %q 的区间 [%d,%d) 超出父词 [%d,%d)",
				want, sub.Start, sub.End, parent.Start, parent.End)
		}
	}
}

// 关闭子词后只保留分词结果本身。
func TestChineseNoSubWords(t *testing.T) {
	a, err := NewChinese(ChineseOptions{NoSubWords: true})
	if err != nil {
		t.Fatal(err)
	}

	got := render(a.Analyze("大学生"))

	// 逐 token 比较，不要用子串匹配：
	// "大学生@0" 里恰好含有 "学生@"，用 strings.Contains 会误判。
	terms := map[string]bool{}
	for _, tok := range a.Analyze("大学生") {
		terms[tok.Term] = true
	}
	if terms["大学"] || terms["学生"] {
		t.Fatalf("关闭子词后不应出现子词：%s", got)
	}
	if !terms["大学生"] {
		t.Fatalf("应当保留整词：%s", got)
	}
}

// 自定义词典能把领域词切成整词。
func TestChineseCustomDict(t *testing.T) {
	// 先看默认行为：没有自定义词典时「倒排索引」往往被切碎。
	base := testChinese(t)
	t.Logf("默认分词 倒排索引与跳表 -> %s", render(base.Analyze("倒排索引与跳表")))

	a, err := NewChinese(ChineseOptions{
		Dict: "倒排索引\n跳表 # 行尾注释\nbm25 排序\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := render(a.Analyze("倒排索引与跳表"))
	if !strings.Contains(got, "倒排索引@") {
		t.Errorf("自定义词典未生效，未分出「倒排索引」：%s", got)
	}
	if !strings.Contains(got, "跳表@") {
		t.Errorf("自定义词典未生效，未分出「跳表」：%s", got)
	}
}

func TestSplitDictWords(t *testing.T) {
	got := splitDictWords("倒排索引\n# 整行注释\n跳表 100 n\n\n  bm25   5  \n")
	want := []string{"倒排索引", "跳表", "bm25"}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	if splitDictWords("") != nil {
		t.Error("空输入应返回 nil")
	}
}

// 与英文侧一致：停用词与过短词条不进索引，但仍然占位。
func TestChineseFilteringKeepsPositions(t *testing.T) {
	a, err := NewChinese(ChineseOptions{
		Standard: StandardOptions{KeepStopwords: false},
	})
	if err != nil {
		t.Fatal(err)
	}

	tokens := a.Analyze("中华人民共和国the万岁")
	for _, tok := range tokens {
		if tok.Term == "the" {
			t.Errorf("停用词不应进索引：%s", render(tokens))
		}
	}
	// 位置仍然连续递增（被过滤的词占位）
	for i := 1; i < len(tokens); i++ {
		if tokens[i].Position < tokens[i-1].Position {
			t.Fatalf("位置必须非递减：%s", render(tokens))
		}
	}
}

// Analyzer 契约要求并发安全，项目跑 -race。
func TestChineseConcurrency(t *testing.T) {
	a := testChinese(t)

	const (
		workers    = 8
		iterations = 120
		text       = "全文搜索服务需要考虑倒排索引的压缩以及短语查询的召回精度 Go language BM25"
	)

	want := render(a.Analyze(text))

	var wg sync.WaitGroup
	errs := make(chan string, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if got := render(a.Analyze(text)); got != want {
					errs <- fmt.Sprintf("并发结果不一致\n got: %s\nwant: %s", got, want)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

func BenchmarkChineseAnalyzeMixed(b *testing.B) {
	a, err := NewChinese(ChineseOptions{})
	if err != nil {
		b.Fatal(err)
	}

	text := strings.Repeat("全文搜索服务需要考虑倒排索引的posting list压缩，"+
		"以及短语查询的召回精度和BM25排序效果。", 4)

	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = a.Analyze(text)
	}
}

func BenchmarkChineseAnalyzePureHan(b *testing.B) {
	a, err := NewChinese(ChineseOptions{})
	if err != nil {
		b.Fatal(err)
	}

	text := strings.Repeat("全文搜索服务需要考虑倒排索引的压缩以及短语查询的召回精度", 4)

	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = a.Analyze(text)
	}
}

// 对照组：**用与 BenchmarkAnalyzeEnglish 完全相同的文本**，
// 直接暴露混合分析器在纯英文路径上的额外开销。
func BenchmarkChineseAnalyzeLatinOnly(b *testing.B) {
	a, err := NewChinese(ChineseOptions{})
	if err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(len(benchEnglish)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = a.Analyze(benchEnglish)
	}
}
