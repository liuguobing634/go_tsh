// Package index 实现内存倒排索引：term -> posting list。
//
// term key 采用 "field\x00term" 编码，从而在扁平 map 中实现字段隔离，
// 字段级 document frequency 与 BM25 统计天然独立，无需嵌套 map。
//
// Phase 2 将实现 AddDocument / UpdateDocument / DeleteDocument / GetDocument
// 以及并发保护与统计信息。
package index
