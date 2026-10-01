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
	schema, err := config.ParseMapping(cfg.Mapping)
	if err != nil {
		return nil, err
	}

	kinds := make(map[string]tsh.FieldKind, len(schema))
	for name, kind := range schema {
		kinds[name] = tsh.FieldKind(kind)
	}

	return tsh.NewWith(tsh.Options{
		Analyzer:   tsh.AnalyzerKind(cfg.Analyzer),
		DictPath:   cfg.DictPath,
		NoSubWords: cfg.NoSubWords,
		Schema:     kinds,

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
