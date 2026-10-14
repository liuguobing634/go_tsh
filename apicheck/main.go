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
// # 它测的是什么
//
// 不是功能正确性（那由仓库内的单测负责），而是**对外 API 的可用性**：
// 类型能不能被命名、配置能不能被构造、返回值能不能用。
//
// 历史上它抓到过两个真实问题：
//   - 配置字段是 internal 类型，外部写不出 `tsh.Options{Index: ...}`
//     这样自然的复合字面量；修复方式是类型别名 + DefaultOptions。
//   - HTTP 层整个在 internal 里，外部挂不进自己的进程（见文末）。
package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/liuguobing/go_tsh/pkg/tsh"
)

func main() {
	// ---- 1. 拿一份默认值再改：新用户最需要的入口 ----
	opts := tsh.DefaultOptions()
	opts.Analyzer = tsh.AnalyzerChinese
	opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	// ---- 2. 复合字面量必须写得出来（类型名要能被外部命名）----
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
	}

	// ---- 3. 字段式赋值也要能用（两条路都通）----
	opts.Index.MaxFields = 32
	opts.BM25.K1 = 1.2

	// ---- 4. 按表声明 schema ----
	opts.TableSchemas = map[string]map[string]tsh.FieldKind{
		"products": {
			"name":    tsh.FieldText,
			"price":   tsh.FieldNumber,
			"created": tsh.FieldDate,
			"sku":     tsh.FieldKeyword,
		},
	}

	engine, err := tsh.NewWith(opts2)
	if err != nil {
		panic(err)
	}
	defer engine.Close()

	// ---- 5. 写入：文本 / 数值 / 时间 三种 ----
	if _, err := engine.Upsert(tsh.Document{
		ID:      "p1",
		Fields:  map[string]string{"name": "笔记本电脑"},
		Numbers: map[string]float64{"price": 4999},
		Dates:   map[string]time.Time{"created": time.Now()},
	}); err != nil {
		panic(err)
	}

	// ---- 6. JSON 原生值入口（HTTP 层的同款）----
	if _, err := engine.ParseDocument("p2", map[string]any{
		"name":  "机械键盘",
		"price": 499,
	}); err != nil {
		panic(err)
	}

	// ---- 7. 检索 ----
	res, err := engine.Search(tsh.SearchRequest{Query: "name:笔记本", Limit: 10})
	if err != nil {
		panic(err)
	}
	fmt.Println("命中", res.Total)

	// ---- 8. 表的生命周期 ----
	if _, err := engine.CreateTable("articles", map[string]tsh.FieldKind{
		"title": tsh.FieldText,
	}); err != nil && err != tsh.ErrTableExists {
		panic(err)
	}
	for _, name := range engine.TableNames() {
		fmt.Println("表:", name)
	}
	for field, kind := range engine.Schema() {
		fmt.Println("字段:", field, kind)
	}
	for _, st := range engine.TableStats() {
		fmt.Println("统计:", st.Name, st.Stats.Docs)
	}

	// ---- 9. 更细粒度的控制 ----
	_ = engine.Index()

	// ---- 10. 错误要能被程序化判断，而不是解析文案 ----
	_, err = engine.Table("nope")
	fmt.Println("表不存在可判定:", err == tsh.ErrTableNotFound)

	// ------------------------------------------------------------------
	// TODO(Phase 12)：外部仍然挂不上 HTTP 接口。
	//
	// 下面这段**故意保持注释**——它现在编译不过，因为 HTTP 层在
	// internal/ 里（连它依赖的 config 也在）。等把它挪到 pkg/httpapi
	// 之后，这里要打开，作为「保留对外 API」这条需求的验收点。
	//
	//	api := httpapi.New(httpapi.Options{
	//		MaxBodyBytes: 8 << 20,
	//		MaxDocFields: 32,
	//	}, engine, logger)
	//
	//	mux := http.NewServeMux()
	//	api.Register(mux)          // 挂到调用方自己的路由上
	//	http.ListenAndServe(":0", mux)
	// ------------------------------------------------------------------
}
