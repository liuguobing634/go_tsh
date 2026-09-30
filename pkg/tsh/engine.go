// Package tsh 提供 go_tsh 搜索引擎的门面，可被其他 Go 程序直接嵌入使用。
//
// Phase 0 仅提供统计查询；索引读写与检索能力在 Phase 2 / Phase 3 补齐。
package tsh

// Engine 是全文搜索引擎的对外句柄，内部持有倒排索引与文档存储。
//
// 它是并发安全的：所有读写路径最终都由 InvertedIndex 的 RWMutex 串行化。
type Engine struct{}

// Stats 描述当前索引的规模，用于 /api/v1/stats 与容量观测。
type Stats struct {
	Docs       int     // 未删除文档数
	Terms      int     // 去重后的 term 数（含字段前缀）
	AvgDocLen  float64 // 平均文档长度（token 数），BM25 归一化用
	IndexBytes int64   // 索引占用的近似内存字节数
}

// New 创建一个空引擎。
func New() *Engine { return &Engine{} }

// Stats 返回当前索引统计。
func (e *Engine) Stats() Stats { return Stats{} }
