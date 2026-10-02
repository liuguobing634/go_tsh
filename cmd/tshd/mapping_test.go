package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/liuguobing/go_tsh/internal/config"
)

// 回归：mapping 文件里的**字段类型名**绝不能被当成表名。
//
// 端到端跑的时候表列表里多出了一张叫 `text` 的表——那是我 mapping 文件里
// 用到的类型名，不是表名。多出一张表不只是难看：用户对着
// /api/v1/tables 会以为自己的数据被写到了别处。
func TestMappingFileDoesNotCreateTablesFromTypeNames(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "mapping.json")

	content := `{
  "products": {"name": "text", "price": "number", "created": "date", "sku": "keyword"},
  "articles": {"title": "text", "price": "keyword", "published": "date"}
}`
	if err := os.WriteFile(mapFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MappingFile = mapFile

	engine, err := newEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	got := engine.TableNames()
	want := []string{"articles", "default", "products"}
	if !slices.Equal(got, want) {
		t.Fatalf("表列表 = %v, want %v\n"+
			"（出现 text/number/keyword/date 说明类型名被当成了表名）", got, want)
	}
}

// mapping 文件里的 schema 必须真的生效。
func TestMappingFileSchemasAreApplied(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "mapping.json")

	content := `{"products": {"price": "number", "created": "date"}}`
	if err := os.WriteFile(mapFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MappingFile = mapFile

	engine, err := newEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	tb, err := engine.Table("products")
	if err != nil {
		t.Fatal(err)
	}

	schema := tb.Schema()
	if schema["price"] != "number" {
		t.Errorf("products.price = %v, want number（schema = %v）", schema["price"], schema)
	}
	if schema["created"] != "date" {
		t.Errorf("products.created = %v, want date", schema["created"])
	}
}
