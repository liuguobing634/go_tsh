package main

import (
	"log/slog"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/query"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// newEngine 按配置装配搜索引擎。
//
// 单独抽出来是为了能被测试覆盖：这些配置项光"解析成功"没有意义，
// 必须真的作用到索引上。
func newEngine(cfg config.Config) (*tsh.Engine, error) {
	return newEngineWithLogger(cfg, nil)
}

func newEngineWithLogger(cfg config.Config, logger *slog.Logger) (*tsh.Engine, error) {
	// 默认表的 schema：-mapping 的内联形式。
	inline, err := config.ParseMapping(cfg.Mapping)
	if err != nil {
		return nil, err
	}

	kinds := make(map[string]tsh.FieldKind, len(inline))
	for name, kind := range inline {
		kinds[name] = tsh.FieldKind(kind)
	}

	// 其它表的 schema：-mapping-file。
	//
	// 文件里列出的表会在构造时被创建，因此「先建表声明类型、
	// 再慢慢灌数据」在配置层面就能表达。
	fileSchemas, err := config.ParseMappingFile(cfg.MappingFile)
	if err != nil {
		return nil, err
	}

	tableSchemas := make(map[string]map[string]tsh.FieldKind, len(fileSchemas))
	for table, fields := range fileSchemas {
		m := make(map[string]tsh.FieldKind, len(fields))
		for name, kind := range fields {
			m[name] = tsh.FieldKind(kind)
		}
		tableSchemas[table] = m
	}

	return tsh.NewWith(tsh.Options{
		Analyzer:     tsh.AnalyzerKind(cfg.Analyzer),
		DictPath:     cfg.DictPath,
		NoSubWords:   cfg.NoSubWords,
		Schema:       kinds,
		TableSchemas: tableSchemas,

		DataDir:      cfg.DataDir,
		SyncInterval: cfg.SyncInterval,
		Logger:       logger,

		Index: index.Options{
			MaxFields: cfg.MaxDocFields,
			MaxTokens: cfg.MaxDocTokens,
		},
		Parser: query.Options{MaxClauses: cfg.MaxQueryTerms},
	})
}
