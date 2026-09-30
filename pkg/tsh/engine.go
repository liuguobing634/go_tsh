// Package tsh 提供 go_tsh 搜索引擎的门面，可被其他 Go 程序直接嵌入使用。
//
// 门面把三块拼在一起：倒排索引（internal/index）、查询与打分
// （internal/query + internal/scoring）、高亮（internal/highlight）。
// 对外只暴露与 HTTP API 一一对应的类型，不泄漏内部结构。
package tsh

import (
	"github.com/liuguobing/go_tsh/internal/highlight"
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/query"
	"github.com/liuguobing/go_tsh/internal/scoring"
)

// defaultSnippetBytes 是高亮片段的默认最大字节数。
const defaultSnippetBytes = 160

// Options 配置引擎；零值即为一套合理默认。
type Options struct {
	// Index 配置倒排索引（分析器、字段数与 token 数上限）。
	Index index.Options

	// Parser 配置查询串解析（默认操作符、子句数上限）。
	Parser query.Options

	// BM25 是打分参数；零值时使用 scoring.DefaultBM25()。
	BM25 scoring.BM25

	// Highlight 配置高亮输出。MaxLen 为零时取 160 字节。
	Highlight highlight.Options
}

// Engine 是全文搜索引擎的对外句柄。
//
// 并发安全：所有状态都由内部 InvertedIndex 的 RWMutex 保护。
type Engine struct {
	idx    *index.InvertedIndex
	search *query.Searcher
	hl     *highlight.Highlighter
	popts  query.Options
}

// Stats 描述当前索引的规模，用于 /api/v1/stats 与容量观测。
type Stats struct {
	Docs       int     `json:"docs"`        // 未删除文档数
	Terms      int     `json:"terms"`       // 去重后的 (字段, 词条) 组合数
	Fields     int     `json:"fields"`      // 出现过的字段名数量
	AvgDocLen  float64 `json:"avg_doc_len"` // 平均文档长度（token 数）
	IndexBytes int64   `json:"index_bytes"` // 索引常驻内存的粗略估算
}

// New 使用默认配置创建一个空引擎。
func New() *Engine { return NewWith(Options{}) }

// NewWith 按给定选项创建引擎。
func NewWith(opts Options) *Engine {
	idx := index.New(opts.Index)

	hlOpts := opts.Highlight
	if hlOpts.MaxLen <= 0 {
		hlOpts.MaxLen = defaultSnippetBytes
	}

	return &Engine{
		idx:    idx,
		search: query.NewSearcher(idx, opts.BM25),
		// 高亮必须复用索引的分析器，否则标注的位置会和检索命中的位置对不上。
		hl:    highlight.New(idx.Analyzer(), hlOpts),
		popts: opts.Parser,
	}
}

// Index 返回底层倒排索引，供需要更细粒度控制的调用方使用。
func (e *Engine) Index() *index.InvertedIndex { return e.idx }

// Stats 返回当前索引统计。
func (e *Engine) Stats() Stats {
	s := e.idx.Stats()
	return Stats{
		Docs:       s.Docs,
		Terms:      s.Terms,
		Fields:     s.Fields,
		AvgDocLen:  s.AvgDocLen,
		IndexBytes: s.IndexBytes,
	}
}
