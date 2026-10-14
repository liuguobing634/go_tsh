// 这是一个**模拟外部使用者**的独立 module。
//
// # 它为什么必须存在
//
// 它用一个 replace 指回本仓库，因此受 Go 的 internal 规则约束——
// 和真实用户完全一样。
//
// 仓库内部的测试**测不到这件事**：`package tsh_test` 虽然在别的包里，
// 但仍然在同一个 module 内，照样能 import internal。也就是说，
// 「外部用户拿不到这个类型」这类问题，只有跨 module 才暴露得出来。
//
// 所以这个目录是一道编译门禁：它必须始终能 `go build`。
// 由 scripts/check.ps1 执行。
//
// # 它覆盖什么
//
// **docs/EMBEDDING.md 里出现的每一个 API**。这样文档不会漂移：
// 写了不存在的函数、改了签名、删了导出符号，这里立刻编译不过。
//
// 不测功能正确性——那由仓库内的单测负责。
//
// # 它抓到过的真实问题
//
//   - 配置字段是 internal 类型，外部写不出 `tsh.Options{Index: ...}`
//     这样自然的复合字面量（修复：类型别名 + DefaultOptions）
//   - HTTP 层整个在 internal 里，外部挂不进自己的进程（修复：pkg/httpapi）
//   - 外部拿不到默认表名，只能硬编码 "default"（修复：tsh.DefaultTable）
//   - 文档级错误没转发，外部只能匹配错误文案
//     （修复：pkg/tsh/errors.go）
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/liuguobing/go_tsh/pkg/httpapi"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// ============================================================
	// 《最小可用》：零值 Options 就是一套合理默认
	// ============================================================
	simple, err := tsh.NewWith(tsh.Options{})
	if err != nil {
		panic(err)
	}
	if err := simple.Close(); err != nil {
		panic(err)
	}

	// tsh.New() 是简写，不会失败
	_ = tsh.New().Close()

	// ============================================================
	// 《字段类型》：DefaultOptions + 复合字面量 + 字段式赋值
	// ============================================================
	opts := tsh.DefaultOptions()
	opts.Analyzer = tsh.AnalyzerChinese
	opts.Logger = logger
	opts.DataDir = "" // 纯内存

	// 复合字面量必须写得出来（类型名要能被外部命名）
	opts2 := tsh.Options{
		Analyzer: tsh.AnalyzerStandard,
		Index: tsh.IndexOptions{
			MaxFields: 32,
			MaxTokens: 100_000,
		},
		Parser: tsh.ParserOptions{
			MaxClauses: 64,
		},
		BM25: tsh.BM25Options{K1: 1.2, B: 0.75},
		Highlight: tsh.HighlightOptions{
			MaxLen: 200,
		},
		SyncInterval: 100 * time.Millisecond,
	}

	// 字段式赋值也要能用
	opts.Index.MaxFields = 32
	opts.BM25.K1 = 1.2

	// 表级 schema
	opts2.TableSchemas = map[string]map[string]tsh.FieldKind{
		"products": {
			"name":    tsh.FieldText,
			"sku":     tsh.FieldKeyword,
			"price":   tsh.FieldNumber,
			"created": tsh.FieldDate,
		},
	}

	engine, err := tsh.NewWith(opts2)
	if err != nil {
		panic(err)
	}
	defer func() { _ = engine.Close() }()

	// ============================================================
	// 《写入》：四张字段表
	// ============================================================
	_, err = engine.Upsert(tsh.Document{
		ID:       "p1",
		Fields:   map[string]string{"name": "笔记本电脑", "body": "轻薄的开发本"},
		Keywords: map[string]string{"sku": "LAP-1"},
		Numbers:  map[string]float64{"price": 4999, "stock": 12},
		Dates:    map[string]time.Time{"created": time.Now().UTC()},
	})
	if err != nil {
		panic(err)
	}

	// Create 只新建；已存在返回 ErrDocumentExists
	if err := engine.Create(tsh.Document{
		ID: "p2", Fields: map[string]string{"name": "机械键盘"},
	}); err != nil && !errors.Is(err, tsh.ErrDocumentExists) {
		panic(err)
	}

	// Delete / GetDocument
	if err := engine.Delete("p2"); err != nil && !errors.Is(err, tsh.ErrDocumentNotFound) {
		panic(err)
	}
	if _, ok := engine.GetDocument("p1"); !ok {
		panic("p1 应当存在")
	}

	// 从 JSON 原生值构造（会查字段类型表）
	if _, err := engine.ParseDocument("p3", map[string]any{
		"name":    "显示器",
		"sku":     "MON-1",
		"price":   1299,
		"created": "2024-03-15T00:00:00Z",
		"gone":    nil,
	}); err != nil {
		panic(err)
	}

	// 不查类型表的版本
	if _, err := tsh.DocumentFromValues("p4", map[string]any{
		"name": "鼠标", "onSale": true,
	}); err != nil {
		panic(err)
	}

	// ============================================================
	// 《检索》：语法、排序、字段限定、高亮
	// ============================================================
	sortKeys, err := tsh.ParseSort("created:desc,price:asc")
	if err != nil {
		panic(err)
	}

	res, err := engine.Search(tsh.SearchRequest{
		Query:     `category:electronics AND price:[1000 TO 6000] AND "轻薄" AND -缺货`,
		Fields:    []string{"name", "body"},
		Sort:      sortKeys,
		Limit:     20,
		Offset:    0,
		Highlight: true,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("命中", res.Total)

	for _, h := range res.Hits {
		_, _, _, _ = h.ID, h.Score, h.Fields, h.Highlights
	}

	// 结构化构造排序键
	_, err = engine.Search(tsh.SearchRequest{
		Query: "name:笔记本",
		Sort:  []tsh.SortKey{{Field: "price", Desc: true}},
	})
	if err != nil {
		panic(err)
	}

	// ============================================================
	// 《表》：句柄、列表、建删、统计
	// ============================================================
	products, err := engine.Table("products")
	if err != nil {
		panic(err)
	}
	_ = products.Name()

	if _, err := engine.CreateTable("articles", map[string]tsh.FieldKind{
		"title": tsh.FieldText,
	}); err != nil && !errors.Is(err, tsh.ErrTableExists) {
		panic(err)
	}

	for _, name := range engine.TableNames() {
		_ = name
	}
	for _, st := range engine.TableStats() {
		_, _ = st.Name, st.Stats.Docs
	}
	for field, kind := range engine.Schema() {
		_, _ = field, kind
	}
	for _, f := range engine.Fields() {
		_ = f
	}
	_ = engine.Stats()
	_ = engine.Index()
	_ = tsh.DefaultTable

	if err := engine.DropTable("articles"); err != nil && !errors.Is(err, tsh.ErrTableNotFound) {
		panic(err)
	}

	// ============================================================
	// 《持久化》：压实、可观测性、导入标记
	// ============================================================
	tb, err := engine.Table(tsh.DefaultTable)
	if err != nil {
		panic(err)
	}

	_ = tb.Persisted()
	_ = tb.LogBytes()
	_ = tb.LogRecords()

	if err := tb.Compact(); err != nil {
		panic(err) // 纯内存时是无害的空操作
	}
	tb.MaybeCompact()

	if err := tb.MarkImported(tsh.ImportMarker{Dir: "corpus", Files: 3}); err != nil {
		panic(err)
	}
	if mark, ok := tb.Imported(); ok {
		_, _, _, _ = mark.Dir, mark.Files, mark.Bytes, mark.Generate
	}

	// Table 上的完整方法面
	if err := tb.Create(tsh.Document{ID: "t1", Fields: map[string]string{"name": "x"}}); err != nil &&
		!errors.Is(err, tsh.ErrDocumentExists) {
		panic(err)
	}
	_, _ = tb.Upsert(tsh.Document{ID: "t2", Fields: map[string]string{"name": "y"}})
	_, _ = tb.GetDocument("t2")
	_, _ = tb.Search(tsh.SearchRequest{Query: "name:y"})
	_, _ = tb.ParseDocument("t3", map[string]any{"name": "z"})
	_ = tb.Schema()
	_ = tb.Fields()
	_ = tb.Stats()
	_ = tb.Index()
	if err := tb.Delete("t2"); err != nil && !errors.Is(err, tsh.ErrDocumentNotFound) {
		panic(err)
	}
	if err := tb.Close(); err != nil && !errors.Is(err, tsh.ErrPersistenceBroken) {
		panic(err)
	}

	// ============================================================
	// 错误都要能被 errors.Is 判断（不能只靠匹配文案）
	// ============================================================
	for _, e := range []error{
		tsh.ErrEmptyExternalID, tsh.ErrEmptyFieldName,
		tsh.ErrDocumentExists, tsh.ErrDocumentNotFound,
		tsh.ErrTooManyFields, tsh.ErrDocumentTooLarge,
		tsh.ErrFieldKindConflict, tsh.ErrInvalidFieldKind,
		tsh.ErrNotNumericField, tsh.ErrInvalidFieldValue,
		tsh.ErrNoFields, tsh.ErrPersistenceBroken,
		tsh.ErrTableNotFound, tsh.ErrTableExists,
		tsh.ErrTooManyTables, tsh.ErrCannotDropDefault,
		tsh.ErrEngineClosed, tsh.ErrUnsupportedFieldType,
		tsh.ErrAmbiguousField, tsh.ErrIntegerTooLarge,
	} {
		_ = errors.Is(nil, e)
	}

	// ============================================================
	// 《接自己的 HTTP 接口》
	// ============================================================
	httpOpts := httpapi.DefaultOptions()
	httpOpts.Addr = "127.0.0.1:0"
	if err := httpOpts.Validate(); err != nil {
		panic(err)
	}
	httpOpts.MaxBodyBytes = 8 << 20 // 字段式赋值
	httpOpts2 := httpapi.Options{   // 复合字面量
		Addr:         "127.0.0.1:0",
		MaxBodyBytes: 8 << 20,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	api := httpapi.New(httpOpts2, engine, logger)
	_ = api.Engine()
	_ = api.Options()

	// 挂到自己的 mux 上：自己的路由与本包路由共存
	mux := http.NewServeMux()
	api.Register(mux)
	mux.HandleFunc("GET /my/custom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// 中间件单独套一层；不要就直接用 mux
	var _ http.Handler = api.Wrap(mux)

	// 也可以让它自己就是一个服务（内嵌 *http.Server）
	var _ http.Handler = api.Handler
}
