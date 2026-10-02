package tsh

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/liuguobing/go_tsh/internal/tablename"
)

// **这是整个「表 = 独立索引」选型的立足点。**
//
// 两张表用**同名但不同类型**的字段：商品表的 price 是数字（要范围查询），
// 资讯表的 price 是关键字（免费/付费）。
//
// 如果表是索引里的一个分区（给文档打 _table 标签），这个测试会直接失败：
// schema 是索引级的，同一个索引里 price 不可能既是 number 又是 keyword。
func TestTablesHaveIndependentSchemas(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	products, err := e.CreateTable("products", map[string]FieldKind{
		"price": FieldNumber,
		"name":  FieldText,
	})
	if err != nil {
		t.Fatal(err)
	}

	articles, err := e.CreateTable("articles", map[string]FieldKind{
		"price": FieldKeyword, // ← 同一个字段名，完全不同的类型
		"title": FieldText,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 两张表各自写入，都不该报类型冲突
	if _, err := products.Upsert(Document{
		ID:      "p1",
		Fields:  map[string]string{"name": "笔记本电脑"},
		Numbers: map[string]float64{"price": 4999},
	}); err != nil {
		t.Fatalf("商品表写入失败: %v", err)
	}

	if _, err := articles.Upsert(Document{
		ID:       "a1",
		Fields:   map[string]string{"title": "如何挑选笔记本"},
		Keywords: map[string]string{"price": "免费"},
	}); err != nil {
		t.Fatalf("资讯表写入失败: %v", err)
	}

	// 商品表的 price 必须是数字，能范围查询
	got := searchIDs(t, products, "price:[0 TO 6000]")
	if len(got) != 1 || got[0] != "p1" {
		t.Errorf("商品表 price 范围查询命中 %v, want [p1]", got)
	}

	// 资讯表的 price 必须是关键字，能精确匹配
	got = searchIDs(t, articles, "price:免费")
	if len(got) != 1 || got[0] != "a1" {
		t.Errorf("资讯表 price 精确匹配命中 %v, want [a1]", got)
	}

	// 反过来必须都报**类型错误**，而不是静默返回空。
	//
	// 这比「结果为空」是更强的证据：关键字字段查 "免费" 会命中或返回空，
	// 只有数字字段才会在解析时失败。所以报错本身就证明了
	// 商品表的 price 确实是 number。
	err = searchErr(products, "price:免费")
	if err == nil {
		t.Error("在数字字段上写非数值应当报错（商品表 price 应当是 number）")
	} else if !strings.Contains(err.Error(), "price") {
		t.Errorf("错误里应当指明字段名：%v", err)
	} else {
		t.Logf("商品表 price:免费 -> %v", err)
	}

	// 资讯表的 price 是关键字，对它做范围查询必须报「不是数值字段」
	err = searchErr(articles, "price:[0 TO 6000]")
	if err == nil {
		t.Error("对关键字字段做范围查询应当报错（资讯表 price 应当是 keyword）")
	} else if !strings.Contains(err.Error(), "price") {
		t.Errorf("错误里应当指明字段名：%v", err)
	} else {
		t.Logf("资讯表 price:[0 TO 6000] -> %v", err)
	}
}

// 表之间数据不能串。
func TestTablesDoNotLeakDocuments(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	a, _ := e.CreateTable("alpha", nil)
	b, _ := e.CreateTable("beta", nil)

	if _, err := a.Upsert(Document{ID: "shared-id", Fields: map[string]string{"body": "alpha content"}}); err != nil {
		t.Fatal(err)
	}
	// 同样的外部 ID 在另一张表里是**另一篇文档**，不是覆盖
	if _, err := b.Upsert(Document{ID: "shared-id", Fields: map[string]string{"body": "beta content"}}); err != nil {
		t.Fatal(err)
	}

	if got := searchIDs(t, a, "body:alpha"); len(got) != 1 {
		t.Errorf("alpha 表应当命中自己的文档，实际 %v", got)
	}
	if got := searchIDs(t, b, "body:beta"); len(got) != 1 {
		t.Errorf("beta 表应当命中自己的文档，实际 %v", got)
	}

	// 跨表串味是这里最要防的
	if got := searchIDs(t, a, "body:beta"); len(got) != 0 {
		t.Errorf("alpha 表不该搜到 beta 的内容，却命中了 %v", got)
	}
	if got := searchIDs(t, b, "body:alpha"); len(got) != 0 {
		t.Errorf("beta 表不该搜到 alpha 的内容，却命中了 %v", got)
	}

	// 同名 ID 在两张表里各自存在
	da, ok := a.GetDocument("shared-id")
	if !ok || da.Fields["body"] != "alpha content" {
		t.Errorf("alpha 表的文档 = %+v", da)
	}
	db, ok := b.GetDocument("shared-id")
	if !ok || db.Fields["body"] != "beta content" {
		t.Errorf("beta 表的文档 = %+v", db)
	}
}

func TestTableNotFoundIsNotAutoCreated(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	// 拼错的表名必须报错，而不是静默产生一张空表——
	// 那会让数据写进去却写在别处，在导入管道里极难发现
	if _, err := e.Table("produts"); !errors.Is(err, ErrTableNotFound) {
		t.Fatalf("应当返回 ErrTableNotFound，实际: %v", err)
	}

	if names := e.TableNames(); len(names) != 1 || names[0] != tablename.Default {
		t.Errorf("查找失败的表不该被创建，现在有 %v", names)
	}
}

func TestCreateTableIsNotIdempotent(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if _, err := e.CreateTable("dup", nil); err != nil {
		t.Fatal(err)
	}
	// 「以为新建了一张表，其实在往旧表里写」是很严重的误解
	if _, err := e.CreateTable("dup", nil); !errors.Is(err, ErrTableExists) {
		t.Fatalf("重复建表应当返回 ErrTableExists，实际: %v", err)
	}
}

func TestInvalidTableNamesAreRejected(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	for _, name := range []string{
		"", "Products", "has space", "../escape", "a/b", "中文",
		strings.Repeat("x", 100),
	} {
		if _, err := e.CreateTable(name, nil); err == nil {
			t.Errorf("CreateTable(%q) 应当被拒绝", name)
		}
		if _, err := e.Table(name); err == nil {
			t.Errorf("Table(%q) 应当被拒绝", name)
		}
	}
}

func TestDefaultTableCannotBeDropped(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	// 靠 Engine 直接调用的那些方法都指着默认表，删掉会让一半接口失去目标
	if err := e.DropTable(tablename.Default); !errors.Is(err, ErrCannotDropDefault) {
		t.Fatalf("应当返回 ErrCannotDropDefault，实际: %v", err)
	}
}

func TestDropTable(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	tmp, _ := e.CreateTable("tmp", nil)
	if _, err := tmp.Upsert(Document{ID: "d1", Fields: map[string]string{"body": "x"}}); err != nil {
		t.Fatal(err)
	}

	if err := e.DropTable("tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Table("tmp"); !errors.Is(err, ErrTableNotFound) {
		t.Errorf("删除后应当查不到，实际: %v", err)
	}
	if err := e.DropTable("tmp"); !errors.Is(err, ErrTableNotFound) {
		t.Errorf("重复删除应当返回 ErrTableNotFound，实际: %v", err)
	}
}

func TestTooManyTables(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	// 默认表占一个名额
	for i := 1; i < maxTables; i++ {
		if _, err := e.CreateTable(fmt.Sprintf("t%d", i), nil); err != nil {
			t.Fatalf("建第 %d 张表失败: %v", i, err)
		}
	}
	if _, err := e.CreateTable("toomany", nil); !errors.Is(err, ErrTooManyTables) {
		t.Fatalf("超限应当返回 ErrTooManyTables，实际: %v", err)
	}
}

// 并发建表 / 删表 / 写入不该崩，也不该有数据竞争（配合 -race）。
func TestConcurrentTableLifecycle(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	var wg sync.WaitGroup

	// 建表与写
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("t%d", i)
			tb, err := e.CreateTable(name, nil)
			if err != nil {
				return
			}
			_, _ = tb.Upsert(Document{ID: "d", Fields: map[string]string{"body": "hello"}})
			_, _ = tb.Search(SearchRequest{Query: "body:hello"})
		}()
	}

	// 同时删
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.DropTable(fmt.Sprintf("t%d", i))
		}()
	}

	// 同时读表列表与统计
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.TableNames()
			_ = e.TableStats()
		}()
	}

	wg.Wait()

	// 默认表必须毫发无伤
	if _, err := e.Table(tablename.Default); err != nil {
		t.Errorf("并发操作后默认表不见了: %v", err)
	}
}

func TestTableNamesAndStats(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	for _, name := range []string{"zeta", "alpha", "mid"} {
		if _, err := e.CreateTable(name, nil); err != nil {
			t.Fatal(err)
		}
	}

	// 必须排序，否则调用方拿到的顺序是随机的
	want := []string{"alpha", tablename.Default, "mid", "zeta"}
	got := e.TableNames()
	if len(got) != len(want) {
		t.Fatalf("表名 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("表名 = %v, want %v", got, want)
		}
	}

	stats := e.TableStats()
	if len(stats) != len(want) {
		t.Fatalf("统计条数 = %d, want %d", len(stats), len(want))
	}
	for i := range want {
		if stats[i].Name != want[i] {
			t.Errorf("统计第 %d 项 = %q, want %q", i, stats[i].Name, want[i])
		}
	}
}

// 引擎级方法作用在默认表上——这是引入表概念不破坏既有调用点的保证。
func TestEngineMethodsTargetDefaultTable(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if _, err := e.Upsert(Document{ID: "d1", Fields: map[string]string{"body": "hello"}}); err != nil {
		t.Fatal(err)
	}

	def, err := e.Table(tablename.Default)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(t, def, "body:hello"); len(got) != 1 {
		t.Errorf("默认表里应当能搜到，实际 %v", got)
	}

	// 引擎级的检索同样打在默认表上
	res, err := e.Search(SearchRequest{Query: "body:hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].ID != "d1" {
		t.Errorf("引擎级检索命中 %+v", res.Hits)
	}
}

// searchIDs 在指定表上执行一次检索，返回命中的文档 ID。
func searchIDs(t *testing.T, tb *Table, query string) []string {
	t.Helper()

	res, err := tb.Search(SearchRequest{Query: query, Limit: 50})
	if err != nil {
		t.Fatalf("表 %q 检索 %q 失败: %v", tb.Name(), query, err)
	}

	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.ID)
	}
	return out
}

// searchErr 执行一次检索并**只返回错误**，用于断言「应当报错」的场景。
func searchErr(tb *Table, query string) error {
	_, err := tb.Search(SearchRequest{Query: query, Limit: 50})
	return err
}
