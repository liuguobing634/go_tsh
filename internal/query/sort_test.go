package query

import (
	"errors"
	"strings"
	"testing"

	"github.com/liuguobing/go_tsh/internal/index"
)

func TestParseSort(t *testing.T) {
	cases := []struct {
		spec string
		want []SortKey
	}{
		{"", nil},
		{"   ", nil},
		{"price", []SortKey{{Field: "price"}}},
		{"price:asc", []SortKey{{Field: "price"}}},
		{"price:desc", []SortKey{{Field: "price", Desc: true}}},
		{"price:DESC", []SortKey{{Field: "price", Desc: true}}},
		{"price:", []SortKey{{Field: "price"}}},
		{" price : desc ", []SortKey{{Field: "price", Desc: true}}},
		{"created:desc,price:asc", []SortKey{
			{Field: "created", Desc: true},
			{Field: "price"},
		}},
		{"a:desc,b:desc,c:asc", []SortKey{
			{Field: "a", Desc: true},
			{Field: "b", Desc: true},
			{Field: "c"},
		}},
		// 末尾多余的逗号忽略
		{"price:desc,", []SortKey{{Field: "price", Desc: true}}},
	}

	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := ParseSort(tc.spec)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("得到 %d 个键，want %d（%+v）", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("第 %d 个键 = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseSortRejects(t *testing.T) {
	cases := []struct {
		spec string
		why  string
	}{
		{":desc", "没有字段名"},
		{"price:up", "方向无法识别"},
		{"price:descending", "方向无法识别"},
		{"a:asc,b:asc,c:asc,d:asc", "超过 3 个键"},
	}

	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			_, err := ParseSort(tc.spec)
			if err == nil {
				t.Fatalf("ParseSort(%q) 应当报错", tc.spec)
			}
			if !errors.Is(err, ErrBadSortField) && !errors.Is(err, ErrTooManySortKeys) {
				t.Errorf("错误类型不对: %v", err)
			}
			t.Logf("%v", err)
		})
	}
}

// planSort 要按 schema 判断字段能不能排序。必须在 View 内调用。
func TestPlanSortValidation(t *testing.T) {
	ix := index.New(index.Options{})
	if err := ix.DeclareField("price", index.FieldNumber); err != nil {
		t.Fatal(err)
	}
	if err := ix.DeclareField("created", index.FieldDate); err != nil {
		t.Fatal(err)
	}
	if err := ix.DeclareField("sku", index.FieldKeyword); err != nil {
		t.Fatal(err)
	}
	if err := ix.DeclareField("body", index.FieldText); err != nil {
		t.Fatal(err)
	}

	ix.View(func(v *index.View) {
		t.Run("可排序的三种类型", func(t *testing.T) {
			for _, field := range []string{"price", "created", "sku"} {
				if _, err := planSort(v, []SortKey{{Field: field}}); err != nil {
					t.Errorf("字段 %q 应当可排序: %v", field, err)
				}
			}
		})

		t.Run("text 不能排序", func(t *testing.T) {
			_, err := planSort(v, []SortKey{{Field: "body"}})
			if !errors.Is(err, ErrUnsortableField) {
				t.Errorf("应当返回 ErrUnsortableField，实际: %v", err)
			}
			// 错误里要说清楚为什么——否则用户只会觉得「凭什么不行」
			if !strings.Contains(err.Error(), "分词") {
				t.Errorf("错误里应当说明原因：%v", err)
			}
			t.Logf("%v", err)
		})

		t.Run("字段不存在", func(t *testing.T) {
			if _, err := planSort(v, []SortKey{{Field: "nope"}}); !errors.Is(err, ErrBadSortField) {
				t.Errorf("应当返回 ErrBadSortField，实际: %v", err)
			}
		})

		t.Run("空字段名", func(t *testing.T) {
			if _, err := planSort(v, []SortKey{{Field: "  "}}); !errors.Is(err, ErrBadSortField) {
				t.Errorf("应当返回 ErrBadSortField，实际: %v", err)
			}
		})

		t.Run("键太多", func(t *testing.T) {
			_, err := planSort(v, []SortKey{{Field: "a"}, {Field: "b"}, {Field: "c"}, {Field: "d"}})
			if !errors.Is(err, ErrTooManySortKeys) {
				t.Errorf("应当返回 ErrTooManySortKeys，实际: %v", err)
			}
		})

		t.Run("数值键按数值比", func(t *testing.T) {
			p, err := planSort(v, []SortKey{{Field: "price"}, {Field: "sku"}})
			if err != nil {
				t.Fatal(err)
			}
			if !p.numeric[0] {
				t.Error("price 应当按数值比")
			}
			if p.numeric[1] {
				t.Error("sku 是关键字，应当按字符串比")
			}
		})
	})
}

// **缺值文档一律排在最后**，与升序降序无关。
//
// 这条规则必须是稳定的：否则「没有价格的商品」会随着排序方向
// 在首尾之间跳。
func TestMissingValuesAlwaysLast(t *testing.T) {
	ids := []index.DocID{1, 2, 3}
	plan := sortPlan{
		keys:    []SortKey{{Field: "price"}},
		numeric: []bool{true},
	}

	// 1 有值、2 缺、3 有值
	values := []sortValue{
		{ok: true, num: 100},
		{ok: false},
		{ok: true, num: 50},
	}

	asc := plan.rank(ids, values, 0)
	// 升序：50(3) -> 100(1) -> 缺(2)
	if got := []index.DocID{ids[asc[0]], ids[asc[1]], ids[asc[2]]}; got[0] != 3 || got[1] != 1 || got[2] != 2 {
		t.Errorf("升序 = %v, want [3 1 2]", got)
	}

	descPlan := sortPlan{
		keys:    []SortKey{{Field: "price", Desc: true}},
		numeric: []bool{true},
	}
	desc := descPlan.rank(ids, values, 0)
	// 降序：100(1) -> 50(3) -> 缺(2)
	if got := []index.DocID{ids[desc[0]], ids[desc[1]], ids[desc[2]]}; got[0] != 1 || got[1] != 3 || got[2] != 2 {
		t.Errorf("降序 = %v, want [1 3 2]", got)
	}
}

// 所有键都相等时按 DocID 兜底，保证顺序确定。
//
// 没有这条，同键文档的先后会随排序算法漂移，分页就会重复或漏项。
func TestTieBreakIsDocIDAscending(t *testing.T) {
	ids := []index.DocID{7, 3, 9, 1, 5}
	plan := sortPlan{keys: []SortKey{{Field: "price"}}, numeric: []bool{true}}

	values := make([]sortValue, len(ids))
	for i := range values {
		values[i] = sortValue{ok: true, num: 42} // 全部同值
	}

	got := plan.rank(ids, values, 0)

	// 结果里存的**位置下标**，映射回 DocID 后必须是升序
	prev := index.DocID(0)
	for _, i := range got {
		if ids[i] <= prev {
			t.Fatalf("同键时没有按 DocID 升序：%v", got)
		}
		prev = ids[i]
	}
}

// 多个排序键：前一个相等时看后一个。
func TestMultiKeySort(t *testing.T) {
	ids := []index.DocID{1, 2, 3}
	plan := sortPlan{
		keys:    []SortKey{{Field: "cat"}, {Field: "price", Desc: true}},
		numeric: []bool{false, true},
	}

	// 同一个分类下按价格降序
	values := []sortValue{
		{ok: true, str: "a"}, {ok: true, num: 10}, // 1: a/10
		{ok: true, str: "a"}, {ok: true, num: 99}, // 2: a/99
		{ok: true, str: "b"}, {ok: true, num: 50}, // 3: b/50
	}

	got := plan.rank(ids, values, 0)
	want := []index.DocID{2, 1, 3} // a 组里 99 在前，然后 b 组

	for i, pos := range got {
		if ids[pos] != want[i] {
			t.Fatalf("多键排序 = %v, want %v", got, want)
		}
	}
}

func TestRankRespectsKeep(t *testing.T) {
	ids := make([]index.DocID, 10)
	values := make([]sortValue, 10)
	for i := range ids {
		ids[i] = index.DocID(i + 1)
		values[i] = sortValue{ok: true, num: float64(i)}
	}

	plan := sortPlan{keys: []SortKey{{Field: "n", Desc: true}}, numeric: []bool{true}}

	if got := plan.rank(ids, values, 3); len(got) != 3 {
		t.Errorf("keep=3 时返回 %d 条", len(got))
	}
	// 降序，前三个应当是 10、9、8
	got := plan.rank(ids, values, 3)
	for i, want := range []index.DocID{10, 9, 8} {
		if ids[got[i]] != want {
			t.Errorf("第 %d 条 = %d, want %d", i, ids[got[i]], want)
		}
	}

	// keep 大于总数时全返回
	if got := plan.rank(ids, values, 100); len(got) != 10 {
		t.Errorf("keep 超过总数时返回 %d 条", len(got))
	}
}

func TestRankEmptyInput(t *testing.T) {
	plan := sortPlan{keys: []SortKey{{Field: "n"}}, numeric: []bool{true}}
	if got := plan.rank(nil, nil, 10); got != nil {
		t.Errorf("空输入应当返回 nil，实际 %v", got)
	}
}

// 空排序键表示不排序，planSort 应当给出空计划而不是报错。
func TestPlanSortEmptyIsNoop(t *testing.T) {
	ix := index.New(index.Options{})
	ix.View(func(v *index.View) {
		p, err := planSort(v, nil)
		if err != nil {
			t.Fatalf("空的排序键不该报错: %v", err)
		}
		if p.size() != 0 {
			t.Errorf("空计划的键数 = %d", p.size())
		}
	})
}
