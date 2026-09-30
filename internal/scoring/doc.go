// Package scoring 实现 BM25 相关性打分与 Top-K 选择。
//
// 默认参数 k1 = 1.2、b = 0.75；Top-K 使用固定容量小顶堆，
// 避免为了取前 10 条而对全部候选做排序。
//
// Phase 3 将实现 BM25 与 TopK。
package scoring
