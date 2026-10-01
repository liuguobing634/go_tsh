package index

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func mustDeclare(t *testing.T, ix *InvertedIndex, field string, kind FieldKind) {
	t.Helper()
	if err := ix.DeclareField(field, kind); err != nil {
		t.Fatalf("声明字段 %q 为 %s 失败: %v", field, kind, err)
	}
}

// docFreqOf 在一致的读快照里取某个 (字段, 词条) 的文档频率。
func docFreqOf(t *testing.T, ix *InvertedIndex, field, term string) uint32 {
	t.Helper()

	var df uint32
	ix.View(func(v *View) { df = v.DocFreq(field, term) })
	return df
}

func mustAdd(t *testing.T, ix *InvertedIndex, id string, fields map[string]string) {
	t.Helper()
	if _, err := ix.Add(id, fields); err != nil {
		t.Fatalf("写入 %q 失败: %v", id, err)
	}
}

// rangeIDs 在 View 里跑一次范围查询，返回命中的 DocID。
func rangeIDs(t *testing.T, ix *InvertedIndex, field string, lo, hi float64, incLo, incHi bool) []DocID {
	t.Helper()

	var (
		got []DocID
		err error
	)
	ix.View(func(v *View) {
		got, err = v.NumericRange(field, lo, hi, incLo, incHi, nil)
	})
	if err != nil {
		t.Fatalf("范围查询失败: %v", err)
	}
	return got
}

func TestFieldKindDeclaration(t *testing.T) {
	ix := New(Options{})

	if _, ok := ix.FieldKindOf("price"); ok {
		t.Error("未声明的字段不该有类型")
	}

	mustDeclare(t, ix, "price", FieldNumber)

	// 幂等：每次写入都会重新声明一次，重复声明同一类型必须允许
	if err := ix.DeclareField("price", FieldNumber); err != nil {
		t.Errorf("重复声明同一类型应当允许: %v", err)
	}

	// 冲突必须报错，而不是静默改掉
	err := ix.DeclareField("price", FieldText)
	if !errors.Is(err, ErrFieldKindConflict) {
		t.Errorf("类型冲突应当返回 ErrFieldKindConflict，实际: %v", err)
	}
	t.Logf("冲突信息: %v", err)

	// 冲突之后原类型不能被改动
	if k, _ := ix.FieldKindOf("price"); k != FieldNumber {
		t.Errorf("冲突后类型被改成了 %s", k)
	}

	if err := ix.DeclareField("  ", FieldText); !errors.Is(err, ErrEmptyFieldName) {
		t.Errorf("空字段名应当报错，实际: %v", err)
	}
}

func TestParseFieldKind(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want FieldKind
	}{
		{"text", FieldText},
		{"  KEYWORD ", FieldKeyword},
		{"Number", FieldNumber},
		{"date", FieldDate},
	} {
		got, err := ParseFieldKind(tc.in)
		if err != nil {
			t.Errorf("ParseFieldKind(%q) 报错: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseFieldKind(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}

	if _, err := ParseFieldKind("martian"); !errors.Is(err, ErrInvalidFieldKind) {
		t.Errorf("未知类型应当报错，实际: %v", err)
	}
}

func TestNumericFieldRangeQuery(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "price", FieldNumber)

	prices := []string{"10", "20", "30", "40", "50"}
	for i, p := range prices {
		mustAdd(t, ix, fmt.Sprintf("d%d", i), map[string]string{"price": p})
	}

	cases := []struct {
		name       string
		lo, hi     float64
		incLo      bool
		incHi      bool
		wantDocIDs []DocID
	}{
		{"闭区间", 20, 40, true, true, []DocID{2, 3, 4}},
		{"左开", 20, 40, false, true, []DocID{3, 4}},
		{"右开", 20, 40, true, false, []DocID{2, 3}},
		{"两端开", 20, 40, false, false, []DocID{3}},
		{"单点命中", 30, 30, true, true, []DocID{3}},
		{"单点开区间为空", 30, 30, false, false, nil},
		{"空区间", 21, 29, true, true, nil},
		{"下界不设", -1e18, 20, true, true, []DocID{1, 2}},
		{"上界不设", 40, 1e18, true, true, []DocID{4, 5}},
		{"全部", -1e18, 1e18, true, true, []DocID{1, 2, 3, 4, 5}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rangeIDs(t, ix, "price", tc.lo, tc.hi, tc.incLo, tc.incHi)
			if len(got) != len(tc.wantDocIDs) {
				t.Fatalf("命中 %v, want %v", got, tc.wantDocIDs)
			}
			for i := range tc.wantDocIDs {
				if got[i] != tc.wantDocIDs[i] {
					t.Fatalf("命中 %v, want %v", got, tc.wantDocIDs)
				}
			}
		})
	}
}

// 选型的立足点：列按 DocID 顺序扫描，输出**天然升序**。
// 这条一旦不成立，整个「稠密列」方案就没有意义了。
func TestNumericRangeOutputIsAscending(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "score", FieldNumber)

	// 故意让值的大小顺序与 DocID 顺序相反
	for i, v := range []string{"900", "700", "500", "300", "100"} {
		mustAdd(t, ix, fmt.Sprintf("d%d", i), map[string]string{"score": v})
	}

	got := rangeIDs(t, ix, "score", 0, 1000, true, true)
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("输出必须严格升序: %v", got)
		}
	}
	if len(got) != 5 {
		t.Fatalf("命中 %v", got)
	}
}

func TestNumericFieldDeleteClearsColumn(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "price", FieldNumber)

	mustAdd(t, ix, "d1", map[string]string{"price": "100"})
	mustAdd(t, ix, "d2", map[string]string{"price": "200"})

	if got := rangeIDs(t, ix, "price", 0, 1000, true, true); len(got) != 2 {
		t.Fatalf("删除前应当命中 2 篇，实际 %v", got)
	}

	if err := ix.Delete("d1"); err != nil {
		t.Fatal(err)
	}

	got := rangeIDs(t, ix, "price", 0, 1000, true, true)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("删除后应当只剩 d2(DocID 2)，实际 %v", got)
	}
}

func TestNumericFieldUpdateReplacesValue(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "price", FieldNumber)

	mustAdd(t, ix, "d1", map[string]string{"price": "100"})

	if _, err := ix.Update("d1", map[string]string{"price": "500"}); err != nil {
		t.Fatal(err)
	}

	if got := rangeIDs(t, ix, "price", 0, 200, true, true); len(got) != 0 {
		t.Errorf("旧值 100 应当已被清掉，实际命中 %v", got)
	}

	got := rangeIDs(t, ix, "price", 400, 600, true, true)
	if len(got) != 1 {
		t.Fatalf("新值 500 应当命中，实际 %v", got)
	}

	// Update 会分配新的 DocID，列里不该留下旧槽位的残留
	if got := rangeIDs(t, ix, "price", -1e18, 1e18, true, true); len(got) != 1 {
		t.Errorf("整列应当只有一个值，实际命中 %v", got)
	}
}

// ⚠️ 回归：keyword 字段的词条是「小写化的整值」，
// 而 Analyze 产出的是分词结果。若 removeLocked 仍直接调 Analyze，
// 两边对不上，删除就摘不干净——文档删了还能搜到，且永远搜得到。
//
// 只有引入不分词的字段类型之后才可能出现这个问题，
// 所以这条测试是跟着新功能一起加的，不是补的。
func TestKeywordFieldDeleteLeavesNoGhost(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "sku", FieldKeyword)

	// 这个值里有连字符，标准分析器会切成 sku 与 123 两个词，
	// 与 keyword 的整值词条完全不同——正好用来暴露不一致。
	mustAdd(t, ix, "d1", map[string]string{"sku": "ABC-123"})

	if got := ix.Stats().Terms; got != 1 {
		t.Fatalf("写入后词条数 = %d, want 1", got)
	}
	if df := docFreqOf(t, ix, "sku", "abc-123"); df != 1 {
		t.Fatalf("keyword 词条的 DF = %d, want 1", df)
	}

	if err := ix.Delete("d1"); err != nil {
		t.Fatal(err)
	}

	if got := ix.Stats().Terms; got != 0 {
		t.Errorf("删除后仍有 %d 个词条残留——removeLocked 与 prepare 的"+
			"分词口径不一致，留下了幽灵命中", got)
	}
	if df := docFreqOf(t, ix, "sku", "abc-123"); df != 0 {
		t.Errorf("删除后 DF = %d, want 0", df)
	}
}

// 覆盖式更新同样要摘干净旧词条。
func TestKeywordFieldUpdateLeavesNoGhost(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "sku", FieldKeyword)

	mustAdd(t, ix, "d1", map[string]string{"sku": "OLD-1"})
	if _, _, err := ix.Upsert("d1", map[string]string{"sku": "NEW-2"}); err != nil {
		t.Fatal(err)
	}

	if df := docFreqOf(t, ix, "sku", "old-1"); df != 0 {
		t.Errorf("旧值仍残留 DF=%d", df)
	}
	if df := docFreqOf(t, ix, "sku", "new-2"); df != 1 {
		t.Errorf("新值 DF = %d, want 1", df)
	}
}

// keyword 不做停用词与最短长度过滤：精确匹配字段里，
// "the" 或单字符标签必须能查到，否则用户会以为数据丢了。
func TestKeywordFieldBypassesTextFiltering(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "tag", FieldKeyword)

	mustAdd(t, ix, "d1", map[string]string{"tag": "the"})
	mustAdd(t, ix, "d2", map[string]string{"tag": "a"})

	if df := docFreqOf(t, ix, "tag", "the"); df != 1 {
		t.Errorf("停用词作为 keyword 值时不该被过滤，DF=%d", df)
	}
	if df := docFreqOf(t, ix, "tag", "a"); df != 1 {
		t.Errorf("单字符 keyword 不该被过滤，DF=%d", df)
	}
}

func TestNumericFieldRejectsBadValues(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "price", FieldNumber)

	for _, bad := range []string{"", "  ", "abc", "12abc", "NaN", "Inf", "-Inf"} {
		_, err := ix.Add("d-"+bad, map[string]string{"price": bad})
		if err == nil {
			t.Errorf("值 %q 应当被拒绝", bad)
			continue
		}
		if !errors.Is(err, ErrInvalidFieldValue) {
			t.Errorf("值 %q 的错误应当是 ErrInvalidFieldValue，实际: %v", bad, err)
		}
	}
}

func TestDateFieldRangeQuery(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "created", FieldDate)

	// 2024-01-01 / 06-01 / 12-31，混用 RFC3339 与纯日期
	mustAdd(t, ix, "a", map[string]string{"created": "2024-01-01"})
	mustAdd(t, ix, "b", map[string]string{"created": "2024-06-01T00:00:00Z"})
	mustAdd(t, ix, "c", map[string]string{"created": "2024-12-31T23:59:59Z"})

	lo := float64(mustParseDate(t, "2024-01-01"))
	hi := float64(mustParseDate(t, "2024-06-01"))

	got := rangeIDs(t, ix, "created", lo, hi, true, true)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("上半年应当命中 a 与 b，实际 %v", got)
	}
}

func mustParseDate(t *testing.T, s string) int64 {
	t.Helper()
	ms, err := ParseDate(s)
	if err != nil {
		t.Fatalf("ParseDate(%q) 失败: %v", s, err)
	}
	return ms
}

func TestParseDate(t *testing.T) {
	// 纯日期一律按 UTC 解释，不猜本地时区
	ms := mustParseDate(t, "2024-01-01")
	want := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	if ms != want {
		t.Errorf("2024-01-01 = %d, want %d", ms, want)
	}

	// 带偏移要换算到 UTC
	plus8 := mustParseDate(t, "2024-01-01T08:00:00+08:00")
	if plus8 != want {
		t.Errorf("+08:00 的 08:00 应当等于 UTC 的 00:00：%d vs %d", plus8, want)
	}

	for _, bad := range []string{"", "2024/01/01", "01-01-2024", "not-a-date"} {
		if _, err := ParseDate(bad); err == nil {
			t.Errorf("ParseDate(%q) 应当报错", bad)
		}
	}
}

// 非数值字段做范围查询必须报错，而不是静默返回空——
// 静默返回空会让用户以为「没搜到」，而实际是查询写错了类型。
func TestNumericRangeRejectsNonNumericField(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "title", FieldText)
	mustAdd(t, ix, "d1", map[string]string{"title": "hello"})

	var err error
	ix.View(func(v *View) {
		_, err = v.NumericRange("title", 0, 10, true, true, nil)
	})
	if !errors.Is(err, ErrNotNumericField) {
		t.Errorf("对文本字段做范围查询应当返回 ErrNotNumericField，实际: %v", err)
	}

	// 完全未声明的字段同样要报错
	ix.View(func(v *View) {
		_, err = v.NumericRange("nope", 0, 10, true, true, nil)
	})
	if !errors.Is(err, ErrNotNumericField) {
		t.Errorf("未声明字段应当返回 ErrNotNumericField，实际: %v", err)
	}
}

func TestNumericColumnGrowthAndStats(t *testing.T) {
	ix := New(Options{})
	mustDeclare(t, ix, "n", FieldNumber)

	const docs = 500
	for i := range docs {
		mustAdd(t, ix, fmt.Sprintf("d%d", i), map[string]string{"n": fmt.Sprint(i)})
	}

	got := rangeIDs(t, ix, "n", 0, float64(docs-1), true, true)
	if len(got) != docs {
		t.Fatalf("应当命中 %d 篇，实际 %d", docs, len(got))
	}

	// 删一半
	for i := 0; i < docs; i += 2 {
		if err := ix.Delete(fmt.Sprintf("d%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	got = rangeIDs(t, ix, "n", 0, float64(docs-1), true, true)
	if len(got) != docs/2 {
		t.Fatalf("删除一半后应当命中 %d 篇，实际 %d", docs/2, len(got))
	}
	for _, id := range got {
		if id%2 == 1 {
			t.Fatalf("命中了奇数 DocID %d，说明删除没清干净", id)
		}
	}
}

func TestLosslessAsNumber(t *testing.T) {
	if !LosslessAsNumber(0) || !LosslessAsNumber(maxExactInteger) {
		t.Error("2^53 之内应当可以精确表示")
	}
	if LosslessAsNumber(maxExactInteger + 1) {
		t.Error("2^53+1 不能被 float64 精确表示")
	}
	if LosslessAsNumber(-maxExactInteger - 1) {
		t.Error("负方向同样")
	}
}
