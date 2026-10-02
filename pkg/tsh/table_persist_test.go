package tsh

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuguobing/go_tsh/internal/tablename"
	"github.com/liuguobing/go_tsh/internal/wal"
)

// silentLogger 用于不想看到恢复日志的用例。
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openTables 打开一个启用了持久化的引擎。
func openTables(t *testing.T, dir string) *Engine {
	t.Helper()

	e, err := NewWith(Options{DataDir: dir, Logger: silentLogger(), SyncInterval: 10e6})
	if err != nil {
		t.Fatalf("打开引擎失败: %v", err)
	}
	return e
}

// 多张表的数据、schema 与表列表都要跨重启存活。
func TestTablesSurviveRestart(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)

	products, err := e.CreateTable("products", map[string]FieldKind{
		"price": FieldNumber,
		"name":  FieldText,
	})
	if err != nil {
		t.Fatal(err)
	}
	articles, err := e.CreateTable("articles", map[string]FieldKind{
		"price": FieldKeyword,
		"title": FieldText,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := products.Upsert(Document{
		ID: "p1", Fields: map[string]string{"name": "笔记本"},
		Numbers: map[string]float64{"price": 4999},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := articles.Upsert(Document{
		ID: "a1", Fields: map[string]string{"title": "导购"},
		Keywords: map[string]string{"price": "免费"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Upsert(Document{ID: "d1", Fields: map[string]string{"body": "default table"}}); err != nil {
		t.Fatal(err)
	}

	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启
	e2 := openTables(t, dir)
	defer e2.Close()

	names := e2.TableNames()
	want := []string{"articles", tablename.Default, "products"}
	if len(names) != len(want) {
		t.Fatalf("重启后的表 = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("重启后的表 = %v, want %v", names, want)
		}
	}

	// 各表数据都在
	p2, err := e2.Table("products")
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(t, p2, "name:笔记本"); len(got) != 1 || got[0] != "p1" {
		t.Errorf("products 重启后命中 %v", got)
	}
	// 数字类型必须还在——这正是 WAL v2 存在的理由
	if got := searchIDs(t, p2, "price:[0 TO 6000]"); len(got) != 1 || got[0] != "p1" {
		t.Errorf("products 的 price 范围查询重启后命中 %v", got)
	}

	a2, err := e2.Table("articles")
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(t, a2, "price:免费"); len(got) != 1 || got[0] != "a1" {
		t.Errorf("articles 重启后命中 %v", got)
	}

	// schema 也要回来
	if k := p2.Schema()["price"]; k != FieldNumber {
		t.Errorf("products.price 重启后 = %v, want number", k)
	}
	if k := a2.Schema()["price"]; k != FieldKeyword {
		t.Errorf("articles.price 重启后 = %v, want keyword", k)
	}
}

// **空表的 schema 必须跨重启存活。**
//
// 「先建表、声明好字段类型、再慢慢灌数据」是最常见的用法。
// 此时日志里一条文档记录都没有，schema 只能从别处来——
// 这正是 WAL 需要「声明字段」记录类型的原因。
func TestEmptyTableSchemaSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	_, err := e.CreateTable("empty", map[string]FieldKind{
		"created": FieldDate,
		"price":   FieldNumber,
		"sku":     FieldKeyword,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := openTables(t, dir)
	defer e2.Close()

	tb, err := e2.Table("empty")
	if err != nil {
		t.Fatalf("空表重啟后不见了: %v", err)
	}

	schema := tb.Schema()
	for field, want := range map[string]FieldKind{
		"created": FieldDate,
		"price":   FieldNumber,
		"sku":     FieldKeyword,
	} {
		if got := schema[field]; got != want {
			t.Errorf("空表字段 %q 重启后 = %v, want %v", field, got, want)
		}
	}

	// 声明过的类型必须真的生效：给 date 字段写个不合法的值应当报错
	_, err = tb.ParseDocument("d1", map[string]any{"created": "not-a-date"})
	if err == nil {
		t.Error("声明过的 date 字段应当拒绝非法日期；" +
			"若这里没报错，说明 schema 其实丢了")
	}
}

// 删掉的表重启后不该回来。
func TestDroppedTableStaysDropped(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	if _, err := e.CreateTable("gone", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.DropTable("gone"); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := openTables(t, dir)
	defer e2.Close()

	if _, err := e2.Table("gone"); !errors.Is(err, ErrTableNotFound) {
		t.Errorf("删掉的表重启后不该出现，实际: %v", err)
	}
}

// 旧布局的 data/documents.wal 要迁移成 tables/default.wal，数据不能丢。
func TestLegacyLogIsMigrated(t *testing.T) {
	dir := t.TempDir()

	// 手工造一份旧布局的日志
	oldPath := filepath.Join(dir, legacyDocumentsLogName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log, err := wal.Open(oldPath, wal.Options{SyncInterval: 10e6, Logger: silentLogger()})
	if err != nil {
		t.Fatal(err)
	}
	payload := wal.EncodeUpsert("legacy-1", map[string]string{"body": "old data"}, nil, nil)
	if err := log.Append(wal.KindUpsert, payload); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	// 打开引擎应当触发迁移
	e := openTables(t, dir)

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("旧日志应当已被移走，实际 stat 错误: %v", err)
	}
	newPath := filepath.Join(dir, tablesSubdir, tablename.Default+logExt)
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("新路径应当存在: %v", err)
	}

	// 数据必须还在
	tb, err := e.Table(tablename.Default)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(t, tb, "body:old"); len(got) != 1 || got[0] != "legacy-1" {
		t.Errorf("迁移后应当能搜到旧数据，实际 %v", got)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}

// 迁移是幂等的：再开一次不该出问题。
func TestLegacyMigrationIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	// 用正常长度的词：单字符会被分析器的最短长度过滤掉，
	// 那样测的就是「分析器按预期工作」而不是「数据还在不在」。
	if _, err := e.Upsert(Document{ID: "d1", Fields: map[string]string{"body": "hello"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		e2 := openTables(t, dir)
		if got := searchIDs(t, mustDefault(t, e2), "body:hello"); len(got) != 1 {
			t.Fatalf("反复打开后数据丢了: %v", got)
		}
		if err := e2.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// 目录里混入的非本程序文件应当被跳过，而不是让启动失败。
func TestNonMagicFilesAreSkipped(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	tablesPath := filepath.Join(dir, tablesSubdir)
	// 常见的杂物：编辑器备份、系统文件、用户随手拷来的东西
	junk := []string{"README.md", ".DS_Store", "Thumbs.db", "notes.txt", "empty.wal"}
	for _, name := range junk {
		if err := os.WriteFile(filepath.Join(tablesPath, name), []byte("not our data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 必须能正常启动
	e2 := openTables(t, dir)
	defer e2.Close()

	if names := e2.TableNames(); len(names) != 1 || names[0] != tablename.Default {
		t.Errorf("杂物不该被认成表，实际表列表: %v", names)
	}
}

// **魔数正确但表名非法 → 拒绝启动。**
//
// 那是我们的数据，只是文件名被改过。静默跳过等于无声地丢掉一整个表。
func TestMagicWithBadTableNameRefusesStart(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// 造一个魔数正确、但名字不合法的日志
	badName := "BadName.wal"
	src := filepath.Join(dir, tablesSubdir, tablename.Default+logExt)
	dst := filepath.Join(dir, tablesSubdir, badName)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = NewWith(Options{DataDir: dir, Logger: silentLogger(), SyncInterval: 10e6})
	if err == nil {
		t.Fatal("表名非法但魔数正确的文件必须让启动失败")
	}
	if !strings.Contains(err.Error(), badName) {
		t.Errorf("错误信息里应当指出是哪个文件：%v", err)
	}
	t.Logf("按预期拒绝启动: %v", err)
}

// 每张表的日志必须是独立的文件。
func TestEachTableHasItsOwnLogFile(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	for _, name := range []string{"products", "articles"} {
		if _, err := e.CreateTable(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{tablename.Default, "products", "articles"} {
		path := filepath.Join(dir, tablesSubdir, name+logExt)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("表 %q 的日志应当存在: %v", name, err)
		}
	}
}

func mustDefault(t *testing.T, e *Engine) *Table {
	t.Helper()
	tb, err := e.Table(tablename.Default)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}
