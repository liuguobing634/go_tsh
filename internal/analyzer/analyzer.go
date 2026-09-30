// Package analyzer 负责把原始文本转换成带位置信息的 token 序列。
//
// 关键约束：索引写入与查询解析必须共用**同一个** Analyzer 实例或同一套配置，
// 否则会出现「文档明明存在却检索不到」的归一化不一致问题。
//
// 当前实现为 StandardAnalyzer：按 unicode 分类切分、CJK 逐字切分、
// 全角折叠、统一小写、过滤停用词与过短词条，并保留被过滤词条占用的位置。
package analyzer

// Token 是分析器输出的最小检索单元。
type Token struct {
	// Term 是归一化后的词条，索引侧与查询侧形态完全一致。
	Term string `json:"term"`

	// Position 是该词条在原始 token 流中的序号（从 0 开始）。
	//
	// 注意：被停用词或长度规则过滤掉的词条**同样占用一个位置**，
	// 因此保留下来的词条之间的位置差与原文一致。短语查询依赖这一性质：
	// 在 "quick the brown" 中 quick 与 brown 的位置差为 2，
	// 因而不会误命中短语 "quick brown"。
	Position uint32 `json:"position"`
}

// Analyzer 把原始文本转换成有序的 token 序列。
//
// 契约：
//   - 返回的 Position 严格递增；
//   - 纯空白或纯标点输入返回空切片（可为 nil）；
//   - 同一输入必须永远产生相同输出（无隐藏状态）；
//   - 实现必须并发安全，因为同一个实例会同时服务多个请求。
type Analyzer interface {
	Analyze(text string) []Token
}
