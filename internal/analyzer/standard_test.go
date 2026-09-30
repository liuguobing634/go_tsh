package analyzer

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

// tk 是构造期望 Token 的简写，让测试表更紧凑。
//
// 只设置 Term 与 Position：Start/End 是原文偏移，由
// TestTokenOffsets 专门覆盖，不该污染切分用例的期望值。
func tk(term string, pos uint32) Token { return Token{Term: term, Position: pos} }

// sameTerms 比较两个 token 序列的词条与位置，忽略原文偏移。
func sameTerms(got, want []Token) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Term != want[i].Term || got[i].Position != want[i].Position {
			return false
		}
	}
	return true
}

func TestAnalyze(t *testing.T) {
	// 绝大多数用例只想验证「切分与归一化」，因此默认关闭停用词过滤。
	// 否则用例里一个普通英文单词（例如 out）恰好命中停用词表时，
	// 断言会以难以察觉的方式失效——这正是本用例集第一版踩过的坑。
	cases := []struct {
		name          string
		text          string
		want          []Token
		withStopwords bool
	}{
		{"空串", "", nil, false},
		{"纯标点", "!!! ... ---", nil, false},
		{"纯空白", " \t\n\r ", nil, false},

		{"基本切分与位置", "Hello, WORLD!", []Token{tk("hello", 0), tk("world", 1)}, false},
		{"短词被过滤但仍占位", "  a   bb  ", []Token{tk("bb", 1)}, false},
		{"数字与字母混合", "go1.22 is out", []Token{tk("go1", 0), tk("22", 1), tk("is", 2), tk("out", 3)}, false},

		{"大小写归一", "GoLang", []Token{tk("golang", 0)}, false},
		{"全角折叠", "ＦＵＬＬＷＩＤＴＨ１２３", []Token{tk("fullwidth123", 0)}, false},
		{"全角空格作分隔符", "foo\u3000bar", []Token{tk("foo", 0), tk("bar", 1)}, false},

		{"中文逐字切分", "全文搜索", []Token{tk("全", 0), tk("文", 1), tk("搜", 2), tk("索", 3)}, false},
		{"中英混合", "Go语言search", []Token{tk("go", 0), tk("语", 1), tk("言", 2), tk("search", 3)}, false},
		{"最小长度不作用于中文单字", "a中", []Token{tk("中", 1)}, false},

		{"词内撇号保持完整", "don't stop", []Token{tk("don't", 0), tk("stop", 1)}, false},
		{"弯引号归一为直引号", "don\u2019t stop", []Token{tk("don't", 0), tk("stop", 1)}, false},
		{"撇号不粘连相邻词", "end. 'start'", []Token{tk("end", 0), tk("start", 1)}, false},

		{"连字符与下划线作分隔符", "foo_bar-baz", []Token{tk("foo", 0), tk("bar", 1), tk("baz", 2)}, false},
		{"纯数字保留", "2024", []Token{tk("2024", 0)}, false},
		{"重复词各自占位", "go go go", []Token{tk("go", 0), tk("go", 1), tk("go", 2)}, false},

		// 停用词过滤：被过滤的词条仍占用位置，
		// 于是 quick 与 brown 的位置差保持为 1，短语 "quick brown" 仍能命中。
		{"停用词留下位置间隙", "the quick brown fox", []Token{tk("quick", 1), tk("brown", 2), tk("fox", 3)}, true},
		{"停用词不影响后续词的位置", "out of the box", []Token{tk("box", 3)}, true},
	}

	tokenizer := NewStandardWith(StandardOptions{KeepStopwords: true})
	filtering := NewStandard()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := tokenizer
			if tc.withStopwords {
				a = filtering
			}
			got := a.Analyze(tc.text)
			if !sameTerms(got, tc.want) {
				t.Fatalf("Analyze(%q)\n got: %v\nwant: %v", tc.text, got, tc.want)
			}
		})
	}
}

// Token 的 Start/End 是原文中的**字节**区间，高亮直接依赖它。
func TestTokenOffsets(t *testing.T) {
	a := NewStandardWith(StandardOptions{KeepStopwords: true})

	cases := []struct {
		name  string
		text  string
		terms []string
		start []int
		end   []int
	}{
		{
			name:  "简单英文",
			text:  "Hello, WORLD!",
			terms: []string{"hello", "world"},
			start: []int{0, 7},
			end:   []int{5, 12},
		},
		{
			name:  "前导与连续空白",
			text:  "  alpha  beta",
			terms: []string{"alpha", "beta"},
			start: []int{2, 9},
			end:   []int{7, 13},
		},
		{
			// 每个汉字 3 字节，偏移必须按字节而不是按字算。
			name:  "中文逐字",
			text:  "全文搜索",
			terms: []string{"全", "文", "搜", "索"},
			start: []int{0, 3, 6, 9},
			end:   []int{3, 6, 9, 12},
		},
		{
			name:  "中英混合",
			text:  "Go语言search",
			terms: []string{"go", "语", "言", "search"},
			start: []int{0, 2, 5, 8},
			end:   []int{2, 5, 8, 14},
		},
		{
			// 撇号被并入词条，因此 End 应落在 t 之后。
			name:  "词内撇号",
			text:  "don't stop",
			terms: []string{"don't", "stop"},
			start: []int{0, 6},
			end:   []int{5, 10},
		},
		{
			// 全角字符 3 字节，折叠后词条变短，但区间仍是原文的。
			name:  "全角折叠",
			text:  "ＡＢ ＣＤ",
			terms: []string{"ab", "cd"},
			start: []int{0, 7},
			end:   []int{6, 13},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokens := a.Analyze(tc.text)

			if len(tokens) != len(tc.terms) {
				t.Fatalf("token 数 = %d, want %d: %+v", len(tokens), len(tc.terms), tokens)
			}
			for i, tok := range tokens {
				if tok.Term != tc.terms[i] {
					t.Errorf("tokens[%d].Term = %q, want %q", i, tok.Term, tc.terms[i])
				}
				if tok.Start != tc.start[i] || tok.End != tc.end[i] {
					t.Errorf("tokens[%d] 区间 = [%d,%d), want [%d,%d)",
						i, tok.Start, tok.End, tc.start[i], tc.end[i])
				}
			}
		})
	}
}

// 强不变式：text[Start:End] 恰好就是该词条在原文中的位置。
//
// 判定方式是把它单独再分析一遍——必须还原出同一个词条。
// 这条性质一旦破坏，高亮就会错位，而且错得很隐蔽。
func TestTokenOffsetsRoundTrip(t *testing.T) {
	a := NewStandardWith(StandardOptions{KeepStopwords: true})

	texts := []string{
		"Hello, WORLD! 全文 search don't stop",
		"  mixed　全角ＡＢ and 中文，加标点。",
		"Go语言search2024年12月",
		"it's a well-known state-of-the-art design",
	}

	for _, text := range texts {
		for _, tok := range a.Analyze(text) {
			raw := text[tok.Start:tok.End]

			again := a.Analyze(raw)
			if len(again) != 1 {
				t.Errorf("text[%d:%d] = %q 单独分析得到 %d 个 token，want 1",
					tok.Start, tok.End, raw, len(again))
				continue
			}
			if again[0].Term != tok.Term {
				t.Errorf("text[%d:%d] = %q 还原出 %q，want %q",
					tok.Start, tok.End, raw, again[0].Term, tok.Term)
			}
		}
	}
}

func TestPositionIsStrictlyIncreasing(t *testing.T) {
	// 位置单调递增是短语查询的前提，用一段混合文本做整体校验。
	a := NewStandard()
	tokens := a.Analyze("The Quick, brown 狐狸 jumps over the 懒狗！don't stop 2024.")

	if len(tokens) == 0 {
		t.Fatal("期望得到非空 token 序列")
	}
	for i := 1; i < len(tokens); i++ {
		if tokens[i].Position <= tokens[i-1].Position {
			t.Fatalf("位置必须严格递增，但 tokens[%d]=%+v, tokens[%d]=%+v",
				i-1, tokens[i-1], i, tokens[i])
		}
	}
}

func TestDefaultStopwordsLoaded(t *testing.T) {
	a := NewStandard()
	if len(a.stopwords) < 100 {
		t.Fatalf("内置停用词表过小（%d 条），//go:embed 可能失效", len(a.stopwords))
	}

	for _, w := range []string{"the", "and", "don't", "yourself"} {
		if _, ok := a.stopwords[w]; !ok {
			t.Errorf("内置停用词表应包含 %q", w)
		}
	}

	// 注释行与空行不应被解析成词条。
	for _, bad := range []string{"#", ""} {
		if _, ok := a.stopwords[bad]; ok {
			t.Errorf("停用词表不应包含 %q", bad)
		}
	}
}

func TestStandardOptions(t *testing.T) {
	t.Run("默认过滤停用词", func(t *testing.T) {
		got := NewStandard().Analyze("the bar")
		want := []Token{tk("bar", 1)}
		if !sameTerms(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("KeepStopwords 完全跳过过滤", func(t *testing.T) {
		a := NewStandardWith(StandardOptions{KeepStopwords: true})
		got := a.Analyze("the bar")
		want := []Token{tk("the", 0), tk("bar", 1)}
		if !sameTerms(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("自定义停用词表替换内置表", func(t *testing.T) {
		a := NewStandardWith(StandardOptions{Stopwords: []string{"foo"}})
		got := a.Analyze("foo the bar")
		// "the" 不在自定义表里，因此被保留。
		want := []Token{tk("the", 1), tk("bar", 2)}
		if !sameTerms(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("空切片表示不加载任何停用词", func(t *testing.T) {
		a := NewStandardWith(StandardOptions{Stopwords: []string{}})
		got := a.Analyze("the bar")
		want := []Token{tk("the", 0), tk("bar", 1)}
		if !sameTerms(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("MinTokenLen 可放宽到 1", func(t *testing.T) {
		a := NewStandardWith(StandardOptions{MinTokenLen: 1, KeepStopwords: true})
		got := a.Analyze("a bb")
		want := []Token{tk("a", 0), tk("bb", 1)}
		if !sameTerms(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("停用词表条目自身会被归一化", func(t *testing.T) {
		// 自定义表里写大写与弯引号，也应匹配到归一化后的词条。
		a := NewStandardWith(StandardOptions{Stopwords: []string{"THE", "Don\u2019t"}})
		got := a.Analyze("the don't keep")
		want := []Token{tk("keep", 2)}
		if !sameTerms(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestParseStopwords(t *testing.T) {
	raw := "alpha beta\n# 注释\n\n  gamma  \ndelta # 行尾注释\n"

	got := parseStopwords(raw)
	want := []string{"alpha", "beta", "gamma", "delta"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseStopwords() = %v, want %v", got, want)
	}
}

// Analyze 必须是无副作用的纯函数：同样输入永远得到同样输出。
func TestAnalyzeIsPure(t *testing.T) {
	a := NewStandard()
	const text = "The Quick Brown Fox 中文 search don't stop"

	first := a.Analyze(text)
	for i := 0; i < 100; i++ {
		if got := a.Analyze(text); !slices.Equal(got, first) {
			t.Fatalf("第 %d 次调用结果不同\n got: %v\nwant: %v", i, got, first)
		}
	}
}

// 同一个分析器实例会被多个请求并发使用，必须并发安全。
// 注意：本机缺少 cgo/ C 编译器时 go test -race 无法运行，
// 该用例在那种情况下只能捕获 panic，捕获不到数据竞争。
func TestAnalyzeIsConcurrencySafe(t *testing.T) {
	const (
		workers    = 8
		iterations = 200
		text       = "The Quick Brown Fox 中文 search don't stop 2024"
	)

	a := NewStandard()
	want := a.Analyze(text)

	var wg sync.WaitGroup
	errs := make(chan string, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if got := a.Analyze(text); !slices.Equal(got, want) {
					errs <- fmt.Sprintf("并发结果不一致\n got: %v\nwant: %v", got, want)
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

// 基准测试语料：贴近真实文档长度（约 1 KB / 份）。
var (
	benchEnglish = strings.Repeat(
		"The quick brown fox jumps over the lazy dog while a diligent engineer "+
			"reviews a distributed full text search implementation and considers "+
			"whether the inverted index should store term positions for phrase queries. ", 6)

	benchMixed = strings.Repeat(
		"全文搜索服务需要考虑倒排索引的 posting list 压缩 and whether 位置信息 "+
			"is worth storing for phrase queries 以及短语查询的召回精度。 ", 6)
)

func BenchmarkAnalyzeEnglish(b *testing.B) {
	a := NewStandard()
	b.SetBytes(int64(len(benchEnglish)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = a.Analyze(benchEnglish)
	}
}

func BenchmarkAnalyzeMixed(b *testing.B) {
	a := NewStandard()
	b.SetBytes(int64(len(benchMixed)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = a.Analyze(benchMixed)
	}
}

func BenchmarkNewStandardWith(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = NewStandard()
	}
}
