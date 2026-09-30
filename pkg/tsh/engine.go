// Package tsh 提供 go_tsh 搜索引擎的门面，可被其他 Go 程序直接嵌入使用。
//
// 目前已经接入倒排索引与统计查询；文档读写与检索接口在 Phase 4 补齐。
package tsh

import "github.com/liuguobing/go_tsh/internal/index"

// Engine 是全文搜索引擎的对外句柄。
//
// 它是并发安全的：所有状态都由内部 InvertedIndex 的 RWMutex 保护。
type Engine struct {
	idx *index.InvertedIndex
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
func New() *Engine { return NewWith(index.Options{}) }

// NewWith 按给定选项创建引擎。
func NewWith(opts index.Options) *Engine {
	return &Engine{idx: index.New(opts)}
}

// Index 返回底层倒排索引。
//
// Phase 3 的查询层与 Phase 4 的 HTTP 层都需要直接访问它；
// 查询侧务必复用 Index().Analyzer()，保证与写入侧归一化一致。
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
