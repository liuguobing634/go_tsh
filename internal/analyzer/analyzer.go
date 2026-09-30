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

	// Start 是词条在原文中的起始**字节**偏移，End 是结束后的字节偏移。
	//
	// 原文区间 [Start, End) 正是高亮要包裹的范围。索引与打分完全不用它们，
	// 但记录它们的成本只是两次 int 赋值，远比事后重新切分一遍原文划算——
	// 重新切分不仅浪费，更要命的是会引入第二套切分规则，
	// 与这里的规则一旦走偏就是「高亮结果和检索结果对不上」。
	Start int `json:"-"`
	End   int `json:"-"`
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
