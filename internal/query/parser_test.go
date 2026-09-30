package query

import (
	"errors"
	"strings"
	"testing"
)

// parseOK 解析查询串并要求成功，返回语法树的可读形式。
func parseOK(t *testing.T, q string, opts Options) string {
	t.Helper()

	node, err := Parse(q, opts)
	if err != nil {
		t.Fatalf("Parse(%q) 返回错误: %v", q, err)
	}
	return node.String()
}

func TestParseShape(t *testing.T) {
	cases := []struct {
		name string
		q    string
		opts Options
		want string
	}{
		{"单词", "hello", Options{}, "hello"},
		{"相邻默认 OR", "hello world", Options{}, "SHOULD(hello world)"},
		{"相邻可配成 AND", "hello world", Options{DefaultOp: OpAnd}, "MUST(hello world)"},
		{"显式 AND", "hello AND world", Options{}, "MUST(hello world)"},
		{"显式 OR", "hello OR world", Options{}, "SHOULD(hello world)"},
		{"关键字大小写不敏感", "a and b", Options{}, "MUST(a b)"},
		{"显式 AND 优先于默认 OR", "a AND b", Options{}, "MUST(a b)"},
		{"三词默认 OR", "a b c", Options{}, "SHOULD(a b c)"},

		{"短语", `"hello world"`, Options{}, `"hello world"`},
		{"短语与词混合", `hello "world peace"`, Options{}, `SHOULD(hello "world peace")`},

		{"前缀减号排除", "-hello", Options{}, "MUSTNOT(hello)"},
		{"NOT 排除", "NOT hello", Options{}, "MUSTNOT(hello)"},
		{"正负混合", "hello -world", Options{}, "SHOULD(hello) MUSTNOT(world)"},
		{"多个排除", "a -b -c", Options{}, "SHOULD(a) MUSTNOT(b c)"},
		{"双重取反抵消", "--hello", Options{}, "hello"},
		{"三重取反仍为排除", "---hello", Options{}, "MUSTNOT(hello)"},
		{"NOT 与减号等价", "NOT a -b", Options{}, "MUSTNOT(a b)"},

		{"括号改变优先级", "(a OR b) AND c", Options{}, "MUST(SHOULD(a b) c)"},
		{"OR 优先级低于 AND", "a OR b AND c", Options{}, "SHOULD(a MUST(b c))"},
		{"括号嵌套", "((a))", Options{}, "a"},
		{"括号内排除", "a (b -c)", Options{}, "SHOULD(a SHOULD(b) MUSTNOT(c))"},

		{"多余空白", "   a    b   ", Options{}, "SHOULD(a b)"},
		{"连字符属词内", "full-width", Options{}, "full-width"},
		{"纯标点也当词", "c++", Options{}, "c++"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseOK(t, tc.q, tc.opts); got != tc.want {
				t.Fatalf("Parse(%q)\n got: %s\nwant: %s", tc.q, got, tc.want)
			}
		})
	}
}

func TestParsePhraseEscapes(t *testing.T) {
	node, err := Parse(`"say \"hi\" now"`, Options{})
	if err != nil {
		t.Fatalf("Parse 返回错误: %v", err)
	}

	phrase, ok := node.(*Phrase)
	if !ok {
		t.Fatalf("期望 *Phrase，实际 %T", node)
	}
	if phrase.Raw != `say "hi" now` {
		t.Errorf("Raw = %q, want %q", phrase.Raw, `say "hi" now`)
	}
}

// 解析器必须原样保留词条，不做任何归一化——分词是执行阶段的职责，
// 且必须由索引自己的分析器完成。
func TestParseDoesNotAnalyze(t *testing.T) {
	node, err := Parse("Hello WORLD", Options{})
	if err != nil {
		t.Fatal(err)
	}

	b, ok := node.(*Bool)
	if !ok || len(b.Should) != 2 {
		t.Fatalf("期望含两个 Should 子句的 *Bool，实际 %#v", node)
	}

	first, ok := b.Should[0].(*Term)
	if !ok || first.Text != "Hello" {
		t.Errorf("词条应保持原始大小写，实际 %#v", b.Should[0])
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name     string
		q        string
		opts     Options
		wantErr  error // 非 nil 时用 errors.Is 校验
		wantText string
	}{
		{name: "空串", q: "", wantErr: ErrEmptyQuery},
		{name: "纯空白", q: "   \t\n ", wantErr: ErrEmptyQuery},
		{name: "引号未闭合", q: `"hello`, wantText: "引号未闭合"},
		{name: "缺少右括号", q: "(a OR b", wantText: "缺少右括号"},
		{name: "多余的右括号", q: "a)", wantText: "多余的内容"},
		{name: "悬空减号", q: "-", wantText: "表达式不完整"},
		{name: "尾部 AND", q: "a AND", wantText: "表达式不完整"},
		{name: "空括号", q: "()", wantText: "意外"},
		{name: "只有 OR", q: "OR", wantText: "意外"},
		{name: "括号内只有 OR", q: "(OR)", wantText: "意外"},
		{name: "子句超限", q: "a b c d e", opts: Options{MaxClauses: 3}, wantErr: ErrTooManyClauses},
		{name: "长短语也计入配额", q: `"a b c d e f"`, opts: Options{MaxClauses: 3}, wantErr: ErrTooManyClauses},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.q, tc.opts)
			if err == nil {
				t.Fatalf("Parse(%q) 期望报错，实际通过", tc.q)
			}

			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, tc.wantErr)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("err = %q，期望包含 %q", err.Error(), tc.wantText)
			}
		})
	}
}

// "a AND b" 曾经因为 startsClause 没把 AND 算作子句起始而整体解析失败。
// 这里把容易回归的形状单独钉住。
func TestParseConjunctionRegression(t *testing.T) {
	cases := []struct {
		q    string
		want string
	}{
		{"a AND b", "MUST(a b)"},
		{"a and b", "MUST(a b)"},
		{"a AND b AND c", "MUST(a b c)"},
		{"x a AND b", "MUST(x a b)"},
		{"a AND b OR c", "SHOULD(MUST(a b) c)"},
		{"a OR b AND c", "SHOULD(a MUST(b c))"},
		{"a AND b -c", "MUST(a b) MUSTNOT(c)"},
	}

	for _, tc := range cases {
		if got := parseOK(t, tc.q, Options{}); got != tc.want {
			t.Errorf("Parse(%q) = %s, want %s", tc.q, got, tc.want)
		}
	}
}

func TestSyntaxErrorReportsPosition(t *testing.T) {
	_, err := Parse("ab (cd", Options{})
	if err == nil {
		t.Fatal("期望语法错误")
	}

	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("期望 *SyntaxError，实际 %T: %v", err, err)
	}
	if se.Pos != 3 {
		t.Errorf("出错位置 = %d, want 3", se.Pos)
	}
}

func TestScanTokenizes(t *testing.T) {
	toks, err := scan(`a "b c" (d) -e AND`)
	if err != nil {
		t.Fatalf("scan 返回错误: %v", err)
	}

	want := []tokenKind{tokWord, tokPhrase, tokLParen, tokWord, tokRParen, tokNot, tokWord, tokAnd, tokEOF}
	if len(toks) != len(want) {
		t.Fatalf("token 数 = %d, want %d: %+v", len(toks), len(want), toks)
	}
	for i := range want {
		if toks[i].kind != want[i] {
			t.Errorf("tokens[%d].kind = %v, want %v", i, toks[i].kind, want[i])
		}
	}
}

func TestDefaultMaxClausesApplied(t *testing.T) {
	// 不显式设置 MaxClauses 时也必须有一道防护。
	q := strings.Repeat("w ", defaultMaxClauses+10)
	if _, err := Parse(q, Options{}); !errors.Is(err, ErrTooManyClauses) {
		t.Fatalf("err = %v, want ErrTooManyClauses", err)
	}
}

func BenchmarkParseSimple(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse("hello world -bad", Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseComplex(b *testing.B) {
	const q = `(full text OR "inverted index") AND search -slow`

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(q, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
