// Package config 负责解析服务配置。
//
// 优先级：命令行参数 > 环境变量 > 默认值。
// 环境变量统一使用 TSH_ 前缀，参数名中的 '-' 转为 '_' 并大写，
// 例如 -query-timeout 对应 TSH_QUERY_TIMEOUT。
package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/liuguobing/go_tsh/internal/tablename"
)

// Config 汇总服务的全部可调参数。
type Config struct {
	Addr     string // HTTP 监听地址
	LogLevel string // debug | info | warn | error

	// ImportDir 是启动时导入的语料目录；为空表示不导入。
	//
	// 这是给本地演示与压测用的**启动动作**，不是运行时接口。
	ImportDir string

	// GenerateDocs 是启动时合成的文档数；<= 0 表示不合成。
	//
	// 合成语料是为压测准备的：比起手工准备几十万篇文档，
	// 一个开关就能得到可复现、且词频不均匀的语料。
	GenerateDocs int

	MaxBodyBytes  int64         // 单请求体字节上限
	MaxDocFields  int           // 单文档字段数上限
	MaxDocTokens  int           // 单文档 token 数上限
	MaxQueryTerms int           // 单次查询 term 数上限
	QueryTimeout  time.Duration // 单次查询超时
	ReadTimeout   time.Duration // HTTP 读超时
	WriteTimeout  time.Duration // HTTP 写超时
	IdleTimeout   time.Duration // HTTP 空闲连接超时
	ShutdownGrace time.Duration // 优雅关闭等待上限

	// Analyzer 选择文本分析器："standard"（默认，英文/数字）或 "chinese"
	// （汉字走词典分词，英文部分仍用 standard 的规则）。
	Analyzer string

	// DictPath 是中文分析器追加的自定义词典文件；analyzer 为 chinese 时生效。
	//
	// 用于补充内嵌词典没有收录的领域词，否则它们会被切碎。
	DictPath string

	// NoSubWords 关闭中文的子词扩展：索引更小、写入更快，但只能整词匹配。
	// 仅在 analyzer 为 chinese 时生效。
	NoSubWords bool

	// DataDir 是持久化目录；为空表示不持久化（纯内存，重启即丢）。
	//
	// 目录下会有一个只追加的 documents.wal，启动时重放它来重建索引。
	DataDir string

	// SyncInterval 是批量 fsync 的间隔，必须为正。
	//
	// 写入返回后数据已经交给操作系统（进程崩溃不丢），
	// 断电最多丢这个间隔内的写。调小更安全但更慢。
	SyncInterval time.Duration

	// Mapping 预先声明**默认表**的字段类型，格式为 "字段:类型,字段:类型"，
	// 例如 "created:date,sku:keyword"。
	//
	// 数值与布尔可以从 JSON 原生类型直接推断，但**日期与关键字不行**：
	// JSON 里没有日期类型，`"2024-01-15"` 只是一个字符串。
	// 靠猜格式是错的——版本号 "2024-01-01" 会被当成日期，
	// 而这是个很难被发现的静默错误。
	Mapping string

	// MappingFile 是按表声明字段类型的 JSON 文件：
	//
	//	{
	//	  "products": {"price": "number", "created": "date"},
	//	  "articles": {"published": "date", "tag": "keyword"}
	//	}
	//
	// 文件里列出的表会在启动时被创建（已存在则沿用）。
	MappingFile string

	// ImportTable 是 -import / -generate 的目标表，为空表示默认表。
	ImportTable string
}

// 支持的字段类型名，与 pkg/tsh 的 FieldKind 对应。
var fieldKindNames = map[string]struct{}{
	"text": {}, "keyword": {}, "number": {}, "date": {},
}

// ParseMapping 解析 "字段:类型,字段:类型" 形式的字段类型声明。
//
// 返回 nil 表示没有声明。
func ParseMapping(spec string) (map[string]string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}

	out := make(map[string]string)
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		name, kind, ok := strings.Cut(item, ":")
		name, kind = strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(kind))
		if !ok || name == "" || kind == "" {
			return nil, fmt.Errorf("mapping 项 %q 格式不对，应当写成 字段:类型", item)
		}
		if _, valid := fieldKindNames[kind]; !valid {
			return nil, fmt.Errorf("mapping 项 %q 的类型 %q 无法识别，"+
				"可选 text|keyword|number|date", item, kind)
		}
		if prev, dup := out[name]; dup {
			return nil, fmt.Errorf("mapping 里字段 %q 被声明了两次（%s 与 %s）",
				name, prev, kind)
		}
		out[name] = kind
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ParseMappingFile 读取按表声明字段类型的 JSON 文件。
//
// 格式：表名 → {字段名: 类型}。表名与类型都在这里校验，
// 配错了应当在**启动时**就知道，而不是等到第一次写入。
func ParseMappingFile(path string) (map[string]map[string]string, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 mapping 文件失败: %w", err)
	}

	// 用 map[string]map[string]string 再接一层校验，而不是直接解成
	// 目标类型：JSON 里的类型名是自由字符串，必须逐个查表。
	var raw map[string]map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("mapping 文件不是合法的 JSON: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("mapping 文件里没有任何表")
	}

	out := make(map[string]map[string]string, len(raw))
	for table, fields := range raw {
		if err := tablename.Validate(table); err != nil {
			return nil, fmt.Errorf("mapping 文件里的表名: %w", err)
		}
		if len(fields) == 0 {
			return nil, fmt.Errorf("mapping 文件里表 %q 没有声明任何字段", table)
		}

		checked := make(map[string]string, len(fields))
		for field, kind := range fields {
			field = strings.TrimSpace(field)
			kind = strings.ToLower(strings.TrimSpace(kind))
			if field == "" {
				return nil, fmt.Errorf("mapping 文件里表 %q 有空的字段名", table)
			}
			if _, ok := fieldKindNames[kind]; !ok {
				return nil, fmt.Errorf(
					"mapping 文件里表 %q 的字段 %q 类型 %q 无法识别，"+
						"可选 text|keyword|number|date", table, field, kind)
			}
			checked[field] = kind
		}
		out[table] = checked
	}
	return out, nil
}

// Default 返回一套可直接用于本地开发的默认配置。
func Default() Config {
	return Config{
		Addr:          ":8080",
		LogLevel:      "info",
		MaxBodyBytes:  8 << 20, // 8 MiB
		MaxDocFields:  32,
		MaxDocTokens:  100_000,
		MaxQueryTerms: 32,
		QueryTimeout:  5 * time.Second,
		ReadTimeout:   10 * time.Second,
		WriteTimeout:  30 * time.Second,
		IdleTimeout:   60 * time.Second,
		ShutdownGrace: 10 * time.Second,
		Analyzer:      AnalyzerStandard,
		SyncInterval:  100 * time.Millisecond,
	}
}

// 支持的分析器名称。
const (
	AnalyzerStandard = "standard"
	AnalyzerChinese  = "chinese"
)

// Load 解析 args（不含程序名）与环境变量，返回校验通过的最终配置。
//
// lookup 由调用方注入以便测试；生产环境传 os.Getenv。
func Load(args []string, lookup func(string) string) (Config, error) {
	cfg := Default()

	fs := flag.NewFlagSet("tshd", flag.ContinueOnError)
	// flag 默认会把用法信息打到 stderr，这里交由调用方统一处理。
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "HTTP 监听地址")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "日志级别：debug|info|warn|error")
	fs.StringVar(&cfg.ImportDir, "import", cfg.ImportDir, "启动时导入的语料目录（txt/md）")
	fs.IntVar(&cfg.GenerateDocs, "generate", cfg.GenerateDocs, "启动时合成的文档数（压测用）")
	fs.Int64Var(&cfg.MaxBodyBytes, "max-body-bytes", cfg.MaxBodyBytes, "单请求体字节上限")
	fs.IntVar(&cfg.MaxDocFields, "max-doc-fields", cfg.MaxDocFields, "单文档字段数上限")
	fs.IntVar(&cfg.MaxDocTokens, "max-doc-tokens", cfg.MaxDocTokens, "单文档 token 数上限")
	fs.IntVar(&cfg.MaxQueryTerms, "max-query-terms", cfg.MaxQueryTerms, "单次查询 term 数上限")
	fs.DurationVar(&cfg.QueryTimeout, "query-timeout", cfg.QueryTimeout, "单次查询超时")
	fs.DurationVar(&cfg.ReadTimeout, "read-timeout", cfg.ReadTimeout, "HTTP 读超时")
	fs.DurationVar(&cfg.WriteTimeout, "write-timeout", cfg.WriteTimeout, "HTTP 写超时")
	fs.DurationVar(&cfg.IdleTimeout, "idle-timeout", cfg.IdleTimeout, "HTTP 空闲连接超时")
	fs.DurationVar(&cfg.ShutdownGrace, "shutdown-grace", cfg.ShutdownGrace, "优雅关闭等待上限")
	fs.StringVar(&cfg.Analyzer, "analyzer", cfg.Analyzer, "文本分析器：standard|chinese")
	fs.StringVar(&cfg.DictPath, "dict", cfg.DictPath, "中文自定义词典文件（每行一个词）")
	fs.BoolVar(&cfg.NoSubWords, "no-sub-words", cfg.NoSubWords, "关闭中文子词扩展")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "持久化目录（留空则不持久化）")
	fs.DurationVar(&cfg.SyncInterval, "sync-interval", cfg.SyncInterval, "批量 fsync 间隔")
	fs.StringVar(&cfg.Mapping, "mapping", cfg.Mapping, `字段类型声明，如 "created:date,sku:keyword"（作用在默认表上）`)
	fs.StringVar(&cfg.MappingFile, "mapping-file", cfg.MappingFile, "按表声明字段类型的 JSON 文件")
	fs.StringVar(&cfg.ImportTable, "import-table", cfg.ImportTable, "导入与合成的目标表名，默认为 default")

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("解析命令行参数: %w", err)
	}

	// 先记录用户显式指定的参数，它们不允许被环境变量覆盖。
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	var envErr error
	fs.VisitAll(func(f *flag.Flag) {
		if explicit[f.Name] || envErr != nil {
			return
		}
		key := EnvKey(f.Name)
		raw := strings.TrimSpace(lookup(key))
		if raw == "" {
			return
		}
		if err := fs.Set(f.Name, raw); err != nil {
			envErr = fmt.Errorf("环境变量 %s=%q 无法解析: %w", key, raw, err)
		}
	})
	if envErr != nil {
		return Config{}, envErr
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// EnvKey 把 flag 名映射为环境变量名，例如 query-timeout -> TSH_QUERY_TIMEOUT。
func EnvKey(flagName string) string {
	return "TSH_" + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// Validate 检查配置是否自洽。
func (c Config) Validate() error {
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("非法的 log-level %q，可选 debug|info|warn|error", c.LogLevel)
	}

	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("addr 不能为空")
	}

	switch c.Analyzer {
	case AnalyzerStandard, AnalyzerChinese:
	default:
		return fmt.Errorf("非法的 analyzer %q，可选 %s|%s",
			c.Analyzer, AnalyzerStandard, AnalyzerChinese)
	}

	// dict 与 no-sub-words 只在中文分析器下有意义。显式指出比静默忽略要好：
	// 用户多半是忘了同时加 -analyzer chinese。
	if c.Analyzer != AnalyzerChinese {
		if c.DictPath != "" {
			return fmt.Errorf("指定了 dict 但 analyzer 是 %q，请同时设置 -analyzer %s",
				c.Analyzer, AnalyzerChinese)
		}
		if c.NoSubWords {
			return fmt.Errorf("指定了 no-sub-words 但 analyzer 是 %q，请同时设置 -analyzer %s",
				c.Analyzer, AnalyzerChinese)
		}
	}

	positiveInts := []struct {
		name string
		val  int64
	}{
		{"max-body-bytes", c.MaxBodyBytes},
		{"max-doc-fields", int64(c.MaxDocFields)},
		{"max-doc-tokens", int64(c.MaxDocTokens)},
		{"max-query-terms", int64(c.MaxQueryTerms)},
	}
	for _, p := range positiveInts {
		if p.val <= 0 {
			return fmt.Errorf("%s 必须为正数，当前为 %d", p.name, p.val)
		}
	}

	// generate 是唯一允许为 0 的数值项：0 表示不合成语料。
	if c.GenerateDocs < 0 {
		return fmt.Errorf("generate 不能为负数，当前为 %d", c.GenerateDocs)
	}

	// sync-interval 必须为正。
	//
	// 不能用 0 表示「不后台刷盘」：0 是 tsh.Options 的零值，那里把它解释为
	// 「用默认间隔」。同一个值在两处含义不同迟早出事，索性只留一种含义。
	if c.SyncInterval <= 0 {
		return fmt.Errorf("sync-interval 必须为正数，当前为 %s", c.SyncInterval)
	}

	// mapping 在启动时就校验：配错了应当立刻知道，
	// 而不是等到第一次写入才报错。
	if _, err := ParseMapping(c.Mapping); err != nil {
		return err
	}

	if c.ImportTable != "" {
		if err := tablename.Validate(c.ImportTable); err != nil {
			return fmt.Errorf("import-table: %w", err)
		}
	}

	positiveDurations := []struct {
		name string
		val  time.Duration
	}{
		{"query-timeout", c.QueryTimeout},
		{"read-timeout", c.ReadTimeout},
		{"write-timeout", c.WriteTimeout},
		{"idle-timeout", c.IdleTimeout},
		{"shutdown-grace", c.ShutdownGrace},
	}
	for _, d := range positiveDurations {
		if d.val <= 0 {
			return fmt.Errorf("%s 必须为正数，当前为 %s", d.name, d.val)
		}
	}

	return nil
}
