// Package query 定义查询 AST 与查询串解析器。
//
// 支持的语法：bare term、双引号短语、"-(排除)" 前缀、AND / OR / NOT。
//
// Phase 3 将实现 parser、posting list 归并求交/求并，以及短语位置判定。
package query
