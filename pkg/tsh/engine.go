// Package tsh 提供 go_tsh 搜索引擎的门面，可被其他 Go 程序直接嵌入使用。
//
// 门面把三块拼在一起：倒排索引（internal/index）、查询与打分
// （internal/query + internal/scoring）、高亮（internal/highlight）。
// 对外只暴露与 HTTP API 一一对应的类型，不泄漏内部结构。
package tsh

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/liuguobing/go_tsh/internal/highlight"
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/query"
	"github.com/liuguobing/go_tsh/internal/scoring"
	"github.com/liuguobing/go_tsh/internal/tablename"
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

	// Schema 预先声明**默认表**的字段类型。
	//
	// 未声明的字段按写入时的 JSON 类型动态确定。
	Schema map[string]FieldKind

	// TableSchemas 按表声明字段类型，用于默认表之外的其它表。
	//
	// 表名同样可以写 "default"，效果与 Schema 一致（两者都写时以本字段为准）。
	//
	// 只有在这里列出的表（以及数据目录里已存在的表）才会被创建；
	// 引擎**不会**自动建表。
	TableSchemas map[string]map[string]FieldKind

	// Index 配置倒排索引（分析器、字段数与 token 数上限）。
	//
	// 注意：这些配置对**所有表**生效。分析器共用是有意的——
	// 中文词典加载一次要几百毫秒与上百 MB，每个表各来一份不可接受。
	Index index.Options

	// Parser 配置查询串解析（默认操作符、子句数上限）。
	Parser query.Options

	// BM25 是打分参数；零值时使用 scoring.DefaultBM25()。
	BM25 scoring.BM25

	// Highlight 配置高亮输出。MaxLen 为零时取 160 字节。
	Highlight highlight.Options

	// DataDir 是持久化目录；**为空表示不持久化**，此时引擎是纯内存的，
	// 行为与引入持久化之前完全一致。
	//
	// 目录下会有一个 documents.wal：只追加的原文日志。
	// 进程重启时重放它来重建索引。
	//
	// 为什么存原文而不是索引：换分析器、改索引格式、修索引 bug 之后，
	// 索引都可以从原文重建；反过来只存索引就等于把数据锁死在一种格式上。
	// 索引是派生数据，原文才是权威数据。
	DataDir string

	// SyncInterval 是批量 fsync 的间隔，<= 0 时取 100ms。
	//
	// 语义：写入返回后数据已交给操作系统（**进程崩溃不丢**），
	// 断电最多丢这个间隔内的写。
	SyncInterval time.Duration

	// Logger 用于报告恢复过程中的异常，为 nil 时用 slog.Default()。
	Logger *slog.Logger
}

// declareSchema 把预声明的字段类型表装到索引上。
func declareSchema(idx *index.InvertedIndex, schema map[string]FieldKind) error {
	for name, kind := range schema {
		ik, err := toIndexKind(kind)
		if err != nil {
			return fmt.Errorf("字段 %q: %w", name, err)
		}
		if err := idx.DeclareField(name, ik); err != nil {
			return err
		}
	}
	return nil
}

// maxTables 是单进程内允许的表数量上限。
//
// 每张表都有自己的索引、切片与（启用持久化时）日志句柄，
// 固定开销不是一个可以忽略的小数。64 张表远超正常业务需要，
// 同时把「某个客户端狂建表把内存吃光」这类误用挡在门外。
const maxTables = 64

// Engine 是全文搜索引擎的对外句柄，持有一组**互相隔离的表**。
//
// 通过 Engine 直接调用的方法（Create / Upsert / Search / ...）都作用在
// **默认表**（tablename.Default）上。这是为了让引入表概念不破坏任何既有
// 调用点：老代码原样可用，新代码用 Table(name) 拿到具体表的句柄。
//
// 并发安全：tables 这张表由 mu 保护；每张表内部的状态由它自己的
// InvertedIndex 的 RWMutex 保护。两级锁互不嵌套——持有 mu 时不会去调
// 表上的方法（除了建表与取句柄），因此不会死锁。
//
// 若通过 Options.DataDir 启用了持久化，使用完毕必须调用 Close
// 停止后台刷盘并做最后一次 fsync。
type Engine struct {
	mu     sync.RWMutex
	tables map[string]*Table

	opts Options

	// closed 之后拒绝建表，但已有表仍可读写（日志本身会拒绝写）。
	closed bool
}

// Stats 描述一张表的规模，用于 /api/v1/stats 与容量观测。
type Stats struct {
	Docs       int     `json:"docs"`        // 未删除文档数
	Terms      int     `json:"terms"`       // 去重后的 (字段, 词条) 组合数
	Fields     int     `json:"fields"`      // 出现过的字段名数量
	AvgDocLen  float64 `json:"avg_doc_len"` // 平均文档长度（token 数）
	IndexBytes int64   `json:"index_bytes"` // 索引常驻内存的粗略估算
}

// TableStat 是一张表的统计，用于全局视图。
type TableStat struct {
	Name  string `json:"name"`
	Stats Stats  `json:"stats"`
	// Persisted 表示这张表是否落在磁盘上。
	Persisted bool `json:"persisted"`
}

// New 使用默认配置创建一个空引擎（含默认表）。
//
// 默认走 AnalyzerStandard，构造不会失败，因此不返回错误。
// 需要中文分词、自定义词典或指定数据目录请用 NewWith。
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
//
// 指定 Options.DataDir 时会：
//  1. 扫描 <DataDir>/tables 下的 *.wal，把每张表恢复出来；
//  2. 创建 Options.Schema / Options.TableSchemas 里声明但尚无日志的表；
//  3. 把旧的 <DataDir>/documents.wal 迁移成 default 表（若存在）。
//
// 重放失败会返回错误而不是静默跳过——静默跳过等于无声地丢数据。
// 这种情况下创建的引擎持有文件句柄，用完必须 Close。
func NewWith(opts Options) (*Engine, error) {
	e := &Engine{
		tables: make(map[string]*Table),
		opts:   opts,
	}

	schemas, err := collectSchemas(opts)
	if err != nil {
		return nil, err
	}

	// 先把默认表建起来：即使数据目录为空，引擎也总该有一张能用的表。
	if err := e.openTable(tablename.Default, schemas[tablename.Default]); err != nil {
		e.closeAll()
		return nil, err
	}
	delete(schemas, tablename.Default)

	// 数据目录里已存在的其它表。
	if opts.DataDir != "" {
		// 迁移必须排在扫描**之前**：旧日志要被认成 default 表，
		// 否则扫描时看不到它，默认表就会以空索引启动——
		// 数据还在磁盘上，但用户看到的是「数据没了」。
		if err := migrateLegacyLog(opts.DataDir, opts.Logger); err != nil {
			e.closeAll()
			return nil, err
		}

		names, err := discoverTables(opts.DataDir, opts.Logger)
		if err != nil {
			e.closeAll()
			return nil, err
		}
		for _, name := range names {
			if name == tablename.Default {
				continue
			}
			if err := e.openTable(name, schemas[name]); err != nil {
				e.closeAll()
				return nil, err
			}
			delete(schemas, name)
		}
	}

	// 配置里声明、但还没有日志文件的表：现在就把它们建出来，
	// 这样「先建表声明类型、再慢慢灌数据」的用法才算数。
	for name, schema := range schemas {
		if err := e.openTable(name, schema); err != nil {
			e.closeAll()
			return nil, err
		}
	}

	return e, nil
}

// collectSchemas 把 Options 里的两处 schema 声明合并成按表的映射。
func collectSchemas(opts Options) (map[string]map[string]FieldKind, error) {
	out := make(map[string]map[string]FieldKind, len(opts.TableSchemas)+1)

	if len(opts.Schema) > 0 {
		out[tablename.Default] = opts.Schema
	}
	// TableSchemas 优先级更高：两处都写了 default 时以它为准。
	for name, schema := range opts.TableSchemas {
		if err := tablename.Validate(name); err != nil {
			return nil, err
		}
		out[name] = schema
	}
	return out, nil
}

// openTable 新建并登记一张表，同时打开它的持久化日志。
//
// 调用方必须持有 mu 或处于构造阶段（此时还没有别的 goroutine 能看到 e）。
func (e *Engine) openTable(name string, schema map[string]FieldKind) error {
	if _, dup := e.tables[name]; dup {
		return fmt.Errorf("%w: %q", ErrTableExists, name)
	}
	if len(e.tables) >= maxTables {
		return fmt.Errorf("%w: 已达上限 %d", ErrTooManyTables, maxTables)
	}

	persist, err := openPersist(e.opts.DataDir, name, e.opts.SyncInterval, e.opts.Logger)
	if err != nil {
		return err
	}

	t, err := newTable(name, e.opts, schema, persist)
	if err != nil {
		// 构造失败就别把文件句柄漏在那。
		if persist != nil {
			_ = persist.log.Close()
		}
		return err
	}

	e.tables[name] = t
	return nil
}

// Table 取一张表的句柄。
//
// 表不存在时返回 ErrTableNotFound——**不会**自动创建。
// 自动创建会让「表名拼错」变成静默产生一张空表，在导入管道里极难发现。
func (e *Engine) Table(name string) (*Table, error) {
	if err := tablename.Validate(name); err != nil {
		return nil, err
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	t, ok := e.tables[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrTableNotFound, name)
	}
	return t, nil
}

// defaultTable 返回默认表的句柄。
//
// 引擎构造时必定创建了默认表，因此这里不会失败。
func (e *Engine) defaultTable() *Table {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.tables[tablename.Default]
}

// TableNames 返回所有表名，已排序。
func (e *Engine) TableNames() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	names := make([]string, 0, len(e.tables))
	for name := range e.tables {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TableStats 返回所有表的统计，按表名排序。
//
// 有了表之后，全局统计的用处有限——真正要看的是各表分别多大。
func (e *Engine) TableStats() []TableStat {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]TableStat, 0, len(e.tables))
	for name, t := range e.tables {
		out = append(out, TableStat{Name: name, Stats: t.Stats(), Persisted: t.Persisted()})
	}
	slices.SortFunc(out, func(a, b TableStat) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// CreateTable 显式创建一张表。
//
// schema 可以为 nil，表示全部字段动态推断。
//
// 表已存在时返回 ErrTableExists 而不是静默成功——**这是刻意的**：
// 「我以为新建了一张表，其实在往旧表里写」是很严重的误解。
// 需要幂等语义的调用方请先查 Table。
func (e *Engine) CreateTable(name string, schema map[string]FieldKind) (*Table, error) {
	if err := tablename.Validate(name); err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return nil, ErrEngineClosed
	}
	if _, dup := e.tables[name]; dup {
		return nil, fmt.Errorf("%w: %q", ErrTableExists, name)
	}

	if err := e.openTable(name, schema); err != nil {
		return nil, err
	}
	return e.tables[name], nil
}

// DropTable 删除一张表：关闭它的日志并删除文件。
//
// 默认表**不能**删除：靠 Engine 直接调用的那些方法都指着它，
// 删掉会让一半接口失去目标。继续用 Delete 清空它的文档。
func (e *Engine) DropTable(name string) error {
	if err := tablename.Validate(name); err != nil {
		return err
	}
	if name == tablename.Default {
		return fmt.Errorf("%w: 默认表 %q 不能删除；"+
			"要清空它请逐篇 Delete", ErrCannotDropDefault, tablename.Default)
	}

	e.mu.Lock()
	t, ok := e.tables[name]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrTableNotFound, name)
	}
	// 先从可见集合里摘掉：之后到达的请求会得到 404，
	// 而不是拿到一个正在被关闭的表。
	delete(e.tables, name)
	e.mu.Unlock()

	// 关闭日志（做最后一次 fsync）之后再删文件。
	if err := t.close(); err != nil {
		return fmt.Errorf("表 %q 关闭失败: %w", name, err)
	}
	return removeTableFile(e.opts.DataDir, name)
}

// Close 停止所有表的后台刷盘、做最后一次 fsync 并关闭日志。
//
// 可重复调用。**逐个处理，不因一个失败就中断**：漏关一个文件句柄
// 比错误信息里少一条严重得多。返回的是第一个错误。
func (e *Engine) Close() error {
	e.mu.Lock()
	e.closed = true
	tables := make([]*Table, 0, len(e.tables))
	for _, t := range e.tables {
		tables = append(tables, t)
	}
	e.mu.Unlock()

	var firstErr error
	for _, t := range tables {
		if err := t.close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("表 %q: %w", t.Name(), err)
		}
	}
	return firstErr
}

// closeAll 在构造失败时尽力回收已打开的资源。
func (e *Engine) closeAll() {
	for _, t := range e.tables {
		_ = t.close()
	}
	e.tables = make(map[string]*Table)
}
