package tsh

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liuguobing/go_tsh/internal/query"
)

// sortFixture 造一张带数值/时间/关键字字段的表。
//
// 商品：p00 最便宜、p09 最贵；created 与序号同向；sku 是字符串，
// 用来验证关键字的字典序；half 那一半**故意不给 price**，
// 用来验证缺值规则。
func sortFixture(t *testing.T) (*Engine, *Table) {
	t.Helper()

	e, err := NewWith(Options{Schema: map[string]FieldKind{
		"name":    FieldText,
		"price":   FieldNumber,
		"created": FieldDate,
		"sku":     FieldKeyword,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })

	tb, err := e.Table(DefaultTable)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 20 {
		doc := Document{
			ID:      fmt.Sprintf("p%02d", i),
			Fields:  map[string]string{"name": "widget common"},
			Numbers: map[string]float64{"price": float64(i * 100)},
			Dates:   map[string]time.Time{"created": base.AddDate(0, 0, i)},
		}
		switch {
		case i < 10:
			// 关键字按字典序：p00..p09 里 "p09" < "p00"? 不——
			// 这里用固定宽度的编号，字典序与数值序一致
			doc.Keywords = map[string]string{"sku": fmt.Sprintf("S%02d", i)}
		default:
			// 后一半故意不给 price，用来验证缺值排最后
			delete(doc.Numbers, "price")
			doc.Keywords = map[string]string{"sku": fmt.Sprintf("S%02d", i)}
		}
		if _, err := tb.Upsert(doc); err != nil {
			t.Fatal(err)
		}
	}
	return e, tb
}

// sortedIDs 按给定排序键检索并返回命中的 ID。
func sortedIDs(t *testing.T, tb *Table, sortSpec string) []string {
	t.Helper()

	keys, err := ParseSort(sortSpec)
	if err != nil {
		t.Fatalf("解析排序 %q 失败: %v", sortSpec, err)
	}

	res, err := tb.Search(SearchRequest{Query: "name:common", Sort: keys, Limit: 100})
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}

	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.ID)
	}
	return out
}

func TestSortByNumber(t *testing.T) {
	_, tb := sortFixture(t)

	// 升序：前一半有 price（0,100,...,900），后一半缺值排最后
	asc := sortedIDs(t, tb, "price:asc")
	if len(asc) != 20 {
		t.Fatalf("命中 %d 条，want 20", len(asc))
	}
	if asc[0] != "p00" {
		t.Errorf("升序第一条 = %q, want p00", asc[0])
	}
	if asc[9] != "p09" {
		t.Errorf("升序第 10 条 = %q, want p09（有值的最后一条）", asc[9])
	}
	// 后 10 条都是缺值的，按 DocID 升序
	for i := 10; i < 20; i++ {
		want := fmt.Sprintf("p%02d", i)
		if asc[i] != want {
			t.Errorf("缺值区第 %d 条 = %q, want %q", i-10, asc[i], want)
		}
	}

	// 降序：有值的按价格从大到小，缺值**仍然在最后**
	desc := sortedIDs(t, tb, "price:desc")
	if desc[0] != "p09" {
		t.Errorf("降序第一条 = %q, want p09", desc[0])
	}
	if desc[9] != "p00" {
		t.Errorf("降序第 10 条 = %q, want p00", desc[9])
	}
	for i := 10; i < 20; i++ {
		if !strings.HasPrefix(desc[i], "p1") {
			t.Errorf("降序里缺值文档应当仍在最后，第 %d 条是 %q", i, desc[i])
		}
	}
}

func TestSortByDate(t *testing.T) {
	_, tb := sortFixture(t)

	desc := sortedIDs(t, tb, "created:desc")
	if desc[0] != "p19" {
		t.Errorf("按时间降序第一条 = %q, want p19（最新的）", desc[0])
	}
	if desc[19] != "p00" {
		t.Errorf("最后一条 = %q, want p00", desc[19])
	}

	asc := sortedIDs(t, tb, "created:asc")
	if asc[0] != "p00" || asc[19] != "p19" {
		t.Errorf("按时间升序 = %q ... %q, want p00 ... p19", asc[0], asc[19])
	}
}

func TestSortByKeyword(t *testing.T) {
	_, tb := sortFixture(t)

	asc := sortedIDs(t, tb, "sku:asc")
	if asc[0] != "p00" || asc[19] != "p19" {
		t.Errorf("关键字升序 = %q ... %q, want p00 ... p19", asc[0], asc[19])
	}

	desc := sortedIDs(t, tb, "sku:desc")
	if desc[0] != "p19" || desc[19] != "p00" {
		t.Errorf("关键字降序 = %q ... %q, want p19 ... p00", desc[0], desc[19])
	}
}

// 排序与分页的交互：翻页拼起来必须等于完整结果，不重不漏。
func TestSortWithPagination(t *testing.T) {
	_, tb := sortFixture(t)

	keys, _ := ParseSort("price:asc")

	full, err := tb.Search(SearchRequest{Query: "name:common", Sort: keys, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}

	var paged []string
	for offset := 0; offset < full.Total; offset += 6 {
		res, err := tb.Search(SearchRequest{
			Query: "name:common", Sort: keys, Limit: 6, Offset: offset,
		})
		if err != nil {
			t.Fatalf("offset=%d 检索失败: %v", offset, err)
		}
		// Total 不随分页变化
		if res.Total != full.Total {
			t.Errorf("offset=%d 时 Total = %d, want %d", offset, res.Total, full.Total)
		}
		for _, h := range res.Hits {
			paged = append(paged, h.ID)
		}
	}

	if len(paged) != len(full.Hits) {
		t.Fatalf("翻页拼出 %d 条，完整结果 %d 条", len(paged), len(full.Hits))
	}
	for i := range full.Hits {
		if paged[i] != full.Hits[i].ID {
			t.Fatalf("第 %d 条不一致：翻页 %q，完整 %q（分页排序不稳定）",
				i, paged[i], full.Hits[i].ID)
		}
	}
}

// 排序不影响分数的计算与返回，只是不再决定顺序。
func TestSortKeepsScores(t *testing.T) {
	_, tb := sortFixture(t)

	keys, _ := ParseSort("price:desc")
	res, err := tb.Search(SearchRequest{Query: "name:common", Sort: keys, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}

	var anyPositive bool
	for _, h := range res.Hits {
		if h.Score > 0 {
			anyPositive = true
		}
	}
	if !anyPositive {
		t.Error("排序后分数应当照常算出来，全都为 0 说明分数路径没走")
	}

	// 而且顺序确实**不是**按分数：所有文档分数相同（同一个词），
	// 此时按分数排会退化成 DocID 序，而我们要的是价格序
	if res.Hits[0].ID != "p09" {
		t.Errorf("第一条 = %q, want p09（说明排序没生效，退化成了分数序）", res.Hits[0].ID)
	}
}

// 不加排序时行为不变：仍然按分数排。
func TestNoSortKeepsScoreOrder(t *testing.T) {
	_, tb := sortFixture(t)

	plain, err := tb.Search(SearchRequest{Query: "name:common", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := tb.Search(SearchRequest{
		Query: "name:common", Limit: 100,
		Sort: []SortKey{{Field: "price", Desc: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if plain.Hits[0].ID == sorted.Hits[0].ID {
		t.Log("两者首条恰好相同，无法据此判断；改比对整体顺序")
	}
	// 至少顺序应当不同（全部同分时按 DocID 升序 vs 按价格降序）
	same := true
	for i := range plain.Hits {
		if plain.Hits[i].ID != sorted.Hits[i].ID {
			same = false
			break
		}
	}
	if same {
		t.Error("加排序前后的顺序完全一样，排序可能没生效")
	}
}

func TestSortErrors(t *testing.T) {
	_, tb := sortFixture(t)

	cases := []struct {
		name string
		sort []SortKey
		want error
	}{
		{"字段不存在", []SortKey{{Field: "nope"}}, query.ErrBadSortField},
		{"text 字段不能排序", []SortKey{{Field: "name"}}, query.ErrUnsortableField},
		{"键太多", []SortKey{
			{Field: "price"}, {Field: "created"}, {Field: "sku"}, {Field: "price"},
		}, query.ErrTooManySortKeys},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tb.Search(SearchRequest{Query: "name:common", Sort: tc.sort})
			if !errors.Is(err, tc.want) {
				t.Fatalf("应当返回 %v，实际: %v", tc.want, err)
			}
			t.Logf("%v", err)
		})
	}
}

// 排序字段写错时，**即使没有命中也要报错**。
//
// 静默返回空结果会让用户以为「就是没搜到」，
// 而真正的原因是字段名拼错了。
func TestSortErrorEvenWithoutHits(t *testing.T) {
	_, tb := sortFixture(t)

	_, err := tb.Search(SearchRequest{
		Query: "name:不存在的词",
		Sort:  []SortKey{{Field: "nope"}},
	})
	if !errors.Is(err, query.ErrBadSortField) {
		t.Fatalf("没有命中时也应当报排序字段错误，实际: %v", err)
	}
}

// 多键排序的端到端验证。
func TestSortMultiKeyEndToEnd(t *testing.T) {
	e, err := NewWith(Options{Schema: map[string]FieldKind{
		"body":  FieldText,
		"cat":   FieldKeyword,
		"price": FieldNumber,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	tb, _ := e.Table(DefaultTable)

	docs := []struct {
		id, cat string
		price   float64
	}{
		{"a1", "book", 30},
		{"a2", "book", 10},
		{"a3", "book", 20},
		{"b1", "toy", 5},
		{"b2", "toy", 50},
	}
	for _, d := range docs {
		if _, err := tb.Upsert(Document{
			ID:       d.id,
			Fields:   map[string]string{"body": "item"},
			Keywords: map[string]string{"cat": d.cat},
			Numbers:  map[string]float64{"price": d.price},
		}); err != nil {
			t.Fatal(err)
		}
	}

	keys, err := ParseSort("cat:asc,price:desc")
	if err != nil {
		t.Fatal(err)
	}
	res, err := tb.Search(SearchRequest{Query: "body:item", Sort: keys, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"a1", "a3", "a2", "b2", "b1"} // book 组价格降序，然后 toy 组
	got := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		got = append(got, h.ID)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("多键排序 = %v, want %v", got, want)
		}
	}
}

// 缺值的判定是「这个文档在这个字段上没有值」，不是「值为 0」。
func TestSortDistinguishesMissingFromZero(t *testing.T) {
	e, err := NewWith(Options{Schema: map[string]FieldKind{"price": FieldNumber, "body": FieldText}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	tb, _ := e.Table(DefaultTable)

	// zero 的 price 真的是 0，missing 完全没有 price
	if _, err := tb.Upsert(Document{
		ID: "zero", Fields: map[string]string{"body": "hello"},
		Numbers: map[string]float64{"price": 0},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.Upsert(Document{
		ID: "hundred", Fields: map[string]string{"body": "hello"},
		Numbers: map[string]float64{"price": 100},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.Upsert(Document{
		ID: "absent", Fields: map[string]string{"body": "hello"},
	}); err != nil {
		t.Fatal(err)
	}

	keys, _ := ParseSort("price:asc")
	res, err := tb.Search(SearchRequest{Query: "body:hello", Sort: keys, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		got = append(got, h.ID)
	}
	want := []string{"zero", "hundred", "absent"} // 0 < 100 < 缺值
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("= %v, want %v（0 与缺值必须是两回事）", got, want)
		}
	}
}
