// Package analyzer 负责把原始文本转换成带位置信息的 token 序列。
//
// 关键约束：索引写入与查询解析必须共用同一个 Analyzer 实例，
// 否则会出现「文档明明存在却检索不到」的归一化不一致问题。
//
// Phase 1 将实现 StandardAnalyzer：按 unicode 字母/数字切分、
// 统一小写、过滤英文停用词与过短 token。
package analyzer
