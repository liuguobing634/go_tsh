// Package config 负责解析服务配置。
//
// 优先级：命令行参数 > 环境变量 > 默认值。
// 环境变量统一使用 TSH_ 前缀，参数名中的 '-' 转为 '_' 并大写，
// 例如 -query-timeout 对应 TSH_QUERY_TIMEOUT。
package config

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
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
	}
}

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
