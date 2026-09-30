// Package tsh 提供 go_tsh 搜索引擎的门面，可被其他 Go 程序直接嵌入使用。
//
// 门面把三块拼在一起：倒排索引（internal/index）、查询与打分
// （internal/query + internal/scoring）、高亮（internal/highlight）。
// 对外只暴露与 HTTP API 一一对应的类型，不泄漏内部结构。
package tsh

import (
	"fmt"

	"github.com/liuguobing/go_tsh/internal/analyzer"
	"github.com/liuguobing/go_tsh/internal/highlight"
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/query"
	"github.com/liuguobing/go_tsh/internal/scoring"
)

// defaultSnippetBytes 是高亮片段的默认最大字节数。
const defaultSnippetBytes = 160

// AnalyzerKind 选择文本分析器。
type AnalyzerKind string

const (
	// AnalyzerStandard 是默认分析器：拉丁字母与数字按规则切分，汉字逐字。
	AnalyzerStandard AnalyzerKind = "standard"

	// AnalyzerChinese 对汉字做词典分词，其余部分沿用 AnalyzerStandard 的规则。
	//
	// 构造时要加载约 100 MB 的内嵌词典（实测约 0.7 秒、约 108 MB 堆），
	// 因此**引擎应当只创建一次**并复用。
	AnalyzerChinese AnalyzerKind = "chinese"
)

// Options 配置引擎；零值即为一套合理默认。
type Options struct {
	// Analyzer 选择分析器；空值等价于 AnalyzerStandard。
	//
	// 若 Index.Analyzer 已显式给出，则以它为准，本字段被忽略。
	Analyzer AnalyzerKind

	// DictPath 是中文分析器追加的自定义词典文件（每行一个词）。
	// 仅在 Analyzer 为 AnalyzerChinese 时生效。
	DictPath string

	// NoSubWords 关闭中文的子词扩展。
	//
	// 默认开启：长词的子词也进索引，「大学」能命中「大学生」。
	// 关闭后索引更小、写入更快，但只能整词匹配。
	NoSubWords bool

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
//
// 默认走 AnalyzerStandard，构造不会失败，因此不返回错误。
// 需要中文分词或自定义词典请用 NewWith。
func New() *Engine {
	e, err := NewWith(Options{})
	if err != nil {
		// 默认配置只会走到不会失败的分支。
		panic("tsh: 默认引擎构造失败: " + err.Error())
	}
	return e
}

// NewWith 按给定选项创建引擎。
//
// 选择 AnalyzerChinese 时会加载内嵌中文词典，可能因为内存不足或
// 词典文件不可读而失败，因此返回错误。
func NewWith(opts Options) (*Engine, error) {
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
				return nil, err
			}
			idxOpts.Analyzer = a
		default:
			return nil, fmt.Errorf("tsh: 未知的分析器 %q，可选 %s|%s",
				opts.Analyzer, AnalyzerStandard, AnalyzerChinese)
		}
	} else if opts.DictPath != "" {
		return nil, fmt.Errorf("tsh: DictPath 与 Index.Analyzer 不能同时指定")
	}

	idx := index.New(idxOpts)

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
	}, nil
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
