package tsh

import (
	"github.com/liuguobing/go_tsh/internal/highlight"
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/query"
	"github.com/liuguobing/go_tsh/internal/scoring"
)

// 对外的配置类型。
//
// 它们是**类型别名**，不是新定义的类型：这样既能被外部命名与构造
// （`tsh.IndexOptions{MaxFields: 32}`），又不需要在门面里重复一遍字段、
// 再写一层转换——重复的字段声明迟早会和内部实现漂移。
//
// 别名能不能穿透 Go 的 internal 规则，是一个必须实测的假设：
// 外部用户写的是 `tsh.IndexOptions`，从不 import internal 包，
// 理论上合法。由外部 module 的编译测试守着。
type (
	// IndexOptions 配置倒排索引。
	IndexOptions = index.Options

	// ParserOptions 配置查询串解析。
	ParserOptions = query.Options

	// HighlightOptions 配置高亮输出。
	HighlightOptions = highlight.Options

	// BM25Options 是打分参数。
	BM25Options = scoring.BM25
)

// DefaultOptions 返回一份可直接使用的默认配置。
//
// 新用户的第一步几乎总是「拿一份默认值，改一两个字段」。
// 没有这个入口，他们只能对着 Options 的零值猜哪些字段必须自己填
// ——而零值其实是可以用的（每个字段都有合理默认），
// 这一点光看结构体定义看不出来。
func DefaultOptions() Options {
	return Options{
		Analyzer: AnalyzerStandard,
		Index:    IndexOptions{},
		Parser:   ParserOptions{},
		BM25:     scoring.DefaultBM25(),
		Highlight: HighlightOptions{
			MaxLen: defaultSnippetBytes,
		},
	}
}
