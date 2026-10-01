package query

import (
	"errors"
	"testing"

	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/scoring"
)

// typedIndex 造一个带类型字段的索引：数值、时间、关键词、文本各一个。
func typedIndex(t *testing.T) (*Searcher, *index.InvertedIndex) {
	t.Helper()

	ix := index.New(index.Options{Analyzer: keepAll()})

	for field, kind := range map[string]index.FieldKind{
		"title":   index.FieldText,
		"sku":     index.FieldKeyword,
		"price":   index.FieldNumber,
		"created": index.FieldDate,
	} {
		if err := ix.DeclareField(field, kind); err != nil {
			t.Fatal(err)
		}
	}

	docs := []struct {
		id     string
		fields map[string]string
	}{
		{"d1", map[string]string{"title": "cheap laptop", "sku": "LAP-1", "price": "500", "created": "2024-01-15"}},
		{"d2", map[string]string{"title": "mid laptop", "sku": "LAP-2", "price": "1500", "created": "2024-06-15"}},
		{"d3", map[string]string{"title": "expensive desktop", "sku": "DSK-1", "price": "3000", "created": "2024-12-15"}},
		{"d4", map[string]string{"title": "cheap mouse", "sku": "MOU-1", "price": "50", "created": "2023-05-01"}},
	}
	for _, d := range docs {
		if _, err := ix.Add(d.id, d.fields); err != nil {
			t.Fatal(err)
		}
	}

	return NewSearcher(ix, scoring.DefaultBM25()), ix
}

// typedDocNames 把内部 DocID 映射回文档名。
//
// DocID 按插入顺序从 1 开始分配，而 typedIndex 的插入顺序是固定的，
// 因此这个映射是确定的。查询层拿不到外部 ID（那是 index 的事），
// 测试里这样转换能让断言保持可读。
var typedDocNames = map[index.DocID]string{1: "d1", 2: "d2", 3: "d3", 4: "d4"}

// searchIDs 执行一次查询并返回命中的文档名（按结果顺序）。
func searchIDs(t *testing.T, s *Searcher, q string) []string {
	t.Helper()

	res, err := s.Search(mustParse(t, q, Options{}), SearchOptions{Limit: 50})
	if err != nil {
		t.Fatalf("Search(%q) 报错: %v", q, err)
	}

	ids := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		name, ok := typedDocNames[h.ID]
		if !ok {
			t.Fatalf("结果里出现了未知的 DocID %d", h.ID)
		}
		ids = append(ids, name)
	}
	return ids
}

// ---------------------------------------------------------------- 语法

func TestParseFieldSyntax(t *testing.T) {
	cases := []struct {
		query string
		want  string // 节点的 String() 形式
	}{
		{"title:hello", "title:hello"},
		{"price:500", "price:500"},
		{"price:[10 TO 100]", "price:[10 TO 100]"},
		{"price:{10 TO 100}", "price:{10 TO 100}"},
		{"price:[10 TO 100}", "price:[10 TO 100}"},
		{"price:{10 TO 100]", "price:{10 TO 100]"},
		{"price:[10 TO *]", "price:[10 TO *]"},
		{"price:[* TO 100]", "price:[* TO 100]"},
		{"created:[2024-01-01 TO 2024-12-31]", "created:[2024-01-01 TO 2024-12-31]"},
		{"price:[10 TO 100] AND title:laptop", "MUST(price:[10 TO 100] title:laptop)"},
		{"-price:[10 TO 100]", "MUSTNOT(price:[10 TO 100])"},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got := mustParse(t, tc.query, Options{}).String()
			if got != tc.want {
				t.Errorf("Parse(%q) = %s, want %s", tc.query, got, tc.want)
			}
		})
	}
}

// ⚠️ 兼容性：引入字段语法之前，`a:b` 是一个普通词。
// 现在它变成「字段 a 里的 b」。这是**破坏性变更**，必须被记录并测到。
func TestFieldSyntaxIsABreakingChange(t *testing.T) {
	n := mustParse(t, "title:hello", Options{})

	ft, ok := n.(*FieldTerm)
	if !ok {
		t.Fatalf("title:hello 现在应当解析成 FieldTerm，实际 %T", n)
	}
	if ft.Field != "title" || ft.Text != "hello" {
		t.Errorf("解析结果 = %+v", ft)
	}
}

// 时间戳里含冒号，但字段名不允许数字开头，因此不会被误认成字段限定。
// 少了这条规则，查询里的时间戳会被拆坏。
func TestTimestampIsNotMistakenForField(t *testing.T) {
	cases := []string{
		"2024-01-01T10:00:00Z",
		"2024-01-01T10:00:00+08:00",
	}

	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			n := mustParse(t, q, Options{})
			if _, ok := n.(*FieldTerm); ok {
				t.Fatalf("%q 被误认成了字段限定", q)
			}
			if _, ok := n.(*FieldRange); ok {
				t.Fatalf("%q 被误认成了字段范围", q)
			}
		})
	}
}

func TestParseFieldSyntaxErrors(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"范围缺少闭合", "price:[10 TO 100"},
		{"范围缺少 TO", "price:[10 100]"},
		{"范围分段过多", "price:[10 TO 100 TO 200]"},
		{"两端都是星号", "price:[* TO *]"},
		{"字段后面没有值", "price:"},
		{"字段后面直接跟连接词", "price: AND title:x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.query, Options{})
			if err == nil {
				t.Fatalf("Parse(%q) 应当报错", tc.query)
			}

			var se *SyntaxError
			if !errors.As(err, &se) {
				t.Errorf("错误应当是 *SyntaxError，实际 %T: %v", err, err)
			}
			t.Logf("%q -> %v", tc.query, err)
		})
	}
}

// ---------------------------------------------------------------- 求值

func TestNumericRangeEvaluation(t *testing.T) {
	s, _ := typedIndex(t)

	cases := []struct {
		query string
		want  []string
	}{
		{"price:[500 TO 1500]", []string{"d1", "d2"}},
		// 两端都开：500 与 1500 都被排除，而没有文档落在两者之间
		{"price:{500 TO 1500}", nil},
		// 半开：下界含、上界不含
		{"price:[500 TO 1500}", []string{"d1"}},
		// 半开：下界不含、上界含
		{"price:{500 TO 1500]", []string{"d2"}},
		// 有内容的两端开区间
		{"price:{500 TO 3000}", []string{"d2"}},
		{"price:[* TO 100]", []string{"d4"}},
		{"price:[1500 TO *]", []string{"d2", "d3"}},
		{"price:[2000 TO 4000]", []string{"d3"}},
		{"price:[600 TO 1400]", nil},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got := searchIDs(t, s, tc.query)
			if len(got) != len(tc.want) {
				t.Fatalf("命中 %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("命中 %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestNumericEqualityViaFieldTerm(t *testing.T) {
	s, _ := typedIndex(t)

	if got := searchIDs(t, s, "price:1500"); len(got) != 1 || got[0] != "d2" {
		t.Fatalf("price:1500 命中 %v, want [d2]", got)
	}
	if got := searchIDs(t, s, "price:999"); len(got) != 0 {
		t.Fatalf("price:999 命中 %v, want 空", got)
	}
}

func TestDateRangeEvaluation(t *testing.T) {
	s, _ := typedIndex(t)

	cases := []struct {
		query string
		want  []string
	}{
		{"created:[2024-01-01 TO 2024-12-31]", []string{"d1", "d2", "d3"}},
		{"created:[2024-01-01 TO 2024-06-15]", []string{"d1", "d2"}},
		{"created:[2024-06-16 TO *]", []string{"d3"}},
		{"created:[* TO 2023-12-31]", []string{"d4"}},
		{"created:[2025-01-01 TO *]", nil},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got := searchIDs(t, s, tc.query)
			if len(got) != len(tc.want) {
				t.Fatalf("命中 %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("命中 %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// 范围查询与文本子句组合：分数来自文本，范围只负责筛选。
func TestRangeCombinedWithText(t *testing.T) {
	s, _ := typedIndex(t)

	got := searchIDs(t, s, "title:laptop AND price:[1000 TO 2000]")
	if len(got) != 1 || got[0] != "d2" {
		t.Fatalf("laptop 且 1000-2000 应当只命中 d2，实际 %v", got)
	}

	got = searchIDs(t, s, "title:laptop AND price:[5000 TO 9000]")
	if len(got) != 0 {
		t.Fatalf("范围无命中时结果应为空，实际 %v", got)
	}

	// OR：范围命中的文档也在结果里
	got = searchIDs(t, s, "title:desktop OR price:[* TO 100]")
	if len(got) != 2 {
		t.Fatalf("应当命中 d3 与 d4，实际 %v", got)
	}
}

func TestNumericRangeScoreIsZero(t *testing.T) {
	s, _ := typedIndex(t)

	res, err := s.Search(mustParse(t, "price:[500 TO 1500]", Options{}), SearchOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("命中 %d 条", len(res.Hits))
	}
	for _, h := range res.Hits {
		if h.Score != 0 {
			t.Errorf("范围查询的得分应当恒为 0，实际 %s=%v", typedDocNames[h.ID], h.Score)
		}
	}
}

func TestRangeOnTextFieldFails(t *testing.T) {
	s, _ := typedIndex(t)

	// 明确声明的文本字段
	_, err := s.Search(mustParse(t, "title:[a TO z]", Options{}), SearchOptions{})
	if !errors.Is(err, index.ErrNotNumericField) {
		t.Errorf("对文本字段做范围查询应当报 ErrNotNumericField，实际: %v", err)
	}

	// 完全没声明过的字段
	_, err = s.Search(mustParse(t, "nope:[1 TO 2]", Options{}), SearchOptions{})
	if !errors.Is(err, index.ErrNotNumericField) {
		t.Errorf("未声明字段做范围查询应当报错，实际: %v", err)
	}
}

func TestNumericFieldWithBadValueFails(t *testing.T) {
	s, _ := typedIndex(t)

	for _, q := range []string{"price:abc", "price:[abc TO 100]", "price:[10 TO xyz]"} {
		_, err := s.Search(mustParse(t, q, Options{}), SearchOptions{})
		if err == nil {
			t.Errorf("查询 %q 应当报错", q)
			continue
		}
		if !errors.Is(err, index.ErrInvalidFieldValue) {
			t.Errorf("查询 %q 的错误应当是 ErrInvalidFieldValue，实际: %v", q, err)
		}
		t.Logf("%q -> %v", q, err)
	}
}

// keyword 字段是整值精确匹配，不做分词。
func TestKeywordFieldEvaluation(t *testing.T) {
	s, _ := typedIndex(t)

	// 整值命中
	if got := searchIDs(t, s, "sku:LAP-1"); len(got) != 1 || got[0] != "d1" {
		t.Fatalf("sku:LAP-1 命中 %v", got)
	}

	// 大小写不敏感（写入与查询都做小写化）
	if got := searchIDs(t, s, "sku:lap-1"); len(got) != 1 || got[0] != "d1" {
		t.Fatalf("sku:lap-1 命中 %v", got)
	}

	// 部分值不该命中：这是 keyword 与 text 的核心区别
	if got := searchIDs(t, s, "sku:LAP"); len(got) != 0 {
		t.Fatalf("keyword 字段不该被部分匹配，实际命中 %v", got)
	}
}

// 字段限定只查那一个字段，不受 SearchOptions.Fields 影响。
func TestFieldTermIgnoresGlobalFieldFilter(t *testing.T) {
	s, _ := typedIndex(t)

	n := mustParse(t, "title:laptop", Options{})
	res, err := s.Search(n, SearchOptions{Fields: []string{"sku"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	// 全局限定到 sku，但用户显式写了 title:，应当按 title 查
	if len(res.Hits) != 2 {
		t.Fatalf("显式的字段限定不该被全局 Fields 覆盖，实际命中 %d 条", len(res.Hits))
	}
}

func TestFieldRangeClauseCounting(t *testing.T) {
	// 范围子句也要计入配额，否则一个查询串就能绕过上限
	_, err := Parse("price:[1 TO 2] price:[3 TO 4] price:[5 TO 6]", Options{MaxClauses: 2})
	if !errors.Is(err, ErrTooManyClauses) {
		t.Errorf("范围子句应当计入 MaxClauses，实际: %v", err)
	}
}
