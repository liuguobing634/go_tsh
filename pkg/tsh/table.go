package tsh

import (
	"fmt"

	"github.com/liuguobing/go_tsh/internal/analyzer"
	"github.com/liuguobing/go_tsh/internal/highlight"
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/query"
)

// Table 是一张表：一套独立的索引、检索器、高亮器与持久化日志。
//
// # 表之间完全隔离
//
// 各自的 schema、文档、词条、数值列与日志文件，互不影响。
//
// 这是**刻意**的：schema 是索引级的（同一个字段名在一份索引里只能有一种
// 类型），把多张表塞进一个索引会让「商品表的 price 是数字、资讯表的 price
// 是字符串编号」这种再正常不过的需求直接冲突。
//
// 顺带的好处是查询只扫自己的表——查商品不会去扫资讯的 posting。
//
// 并发安全：所有状态由内部 InvertedIndex 的 RWMutex 保护。
type Table struct {
	name string

	idx    *index.InvertedIndex
	search *query.Searcher
	hl     *highlight.Highlighter
	popts  query.Options

	// persist 为 nil 表示纯内存模式。
	persist *persistState
}

// Name 返回表名。
func (t *Table) Name() string { return t.name }

// newTable 构造一张表。
//
// persist 为 nil 表示纯内存。schema 会先于重放生效：重放出来的文档要按它
// 校验，类型对不上时当场报错，而不是先按推断写入再被覆盖。
//
// 失败时**不关闭 persist**，由调用方统一处理——这里关了会让调用方
// 无从判断句柄是否还在。
func newTable(
	name string,
	opts Options,
	schema map[string]FieldKind,
	persist *persistState,
) (*Table, error) {
	idxOpts := opts.Index

	if idxOpts.Analyzer == nil {
		switch opts.Analyzer {
		case "", AnalyzerStandard:
			// 留空，交给 index.New 使用默认分析器。
		case AnalyzerChinese:
			a, err := analyzer.NewChinese(analyzer.ChineseOptions{
				DictPath:   opts.DictPath,
				NoSubWords: opts.NoSubWords,
			})
			if err != nil {
				return nil, fmt.Errorf("表 %q: %w", name, err)
			}
			idxOpts.Analyzer = a
		default:
			return nil, fmt.Errorf("tsh: 未知的分析器 %q，可选 %s|%s",
				opts.Analyzer, AnalyzerStandard, AnalyzerChinese)
		}
	} else if opts.DictPath != "" {
		return nil, fmt.Errorf("tsh: DictPath 与 Index.Analyzer 不能同时指定")
	}

	if persist != nil {
		if idxOpts.OnApply != nil {
			return nil, fmt.Errorf("tsh: 启用持久化时不能自行指定 Index.OnApply")
		}
		idxOpts.OnApply = persist.apply
	}

	idx := index.New(idxOpts)

	hlOpts := opts.Highlight
	if hlOpts.MaxLen <= 0 {
		hlOpts.MaxLen = defaultSnippetBytes
	}

	t := &Table{
		name:   name,
		idx:    idx,
		search: query.NewSearcher(idx, opts.BM25),
		// 高亮必须复用索引的分析器，否则标注的位置会和检索命中的位置对不上。
		hl:      highlight.New(idx.Analyzer(), hlOpts),
		popts:   opts.Parser,
		persist: persist,
	}

	// 预声明的类型必须在重放**之前**生效。
	if err := declareSchema(idx, schema); err != nil {
		return nil, fmt.Errorf("表 %q: %w", name, err)
	}

	// 重放必须在钩子生效的前提下进行——钩子内部会检查 replaying 标志，
	// 因此重放期间不会把读到的记录又写回日志。
	if persist != nil {
		if err := persist.replay(idx, opts.Logger); err != nil {
			return nil, fmt.Errorf("表 %q: %w", name, err)
		}
	}

	return t, nil
}

// close 停止后台刷盘、做最后一次 fsync 并关闭持久化日志。
func (t *Table) close() error {
	if t.persist == nil || t.persist.log == nil {
		return nil
	}
	return t.persist.log.Close()
}

// Close 停止这张表的持久化并做最后一次 fsync。可重复调用。
//
// 没有启用持久化时它什么都不做。关闭之后的写请求会失败
// （日志返回 ErrClosed），但读请求仍然可用。
func (t *Table) Close() error { return t.close() }

// Index 返回底层倒排索引，供需要更细粒度控制的调用方使用。
func (t *Table) Index() *index.InvertedIndex { return t.idx }

// Persisted 报告这张表是否启用了持久化。
func (t *Table) Persisted() bool { return t.persist != nil }

// Stats 返回这张表的统计。
func (t *Table) Stats() Stats {
	s := t.idx.Stats()
	return Stats{
		Docs:       s.Docs,
		Terms:      s.Terms,
		Fields:     s.Fields,
		AvgDocLen:  s.AvgDocLen,
		IndexBytes: s.IndexBytes,
	}
}
