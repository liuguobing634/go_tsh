package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/internal/corpus"
	"github.com/liuguobing/go_tsh/internal/tablename"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// bootstrap 把启动参数指定的语料推进索引。
//
// 只为本地演示与压测服务：不带 -import / -generate 时它什么都不做，
// 正常部署路径不受影响。
//
// 用 Upsert 而不是 Create：目录里若出现同名文件（不同子目录下），
// 静默覆盖比因为一个重名就让整个服务起不来要合理。
func bootstrap(engine *tsh.Engine, cfg config.Config, logger *slog.Logger) error {
	var docs []corpus.Document

	if cfg.ImportDir != "" {
		loaded, err := corpus.Load(cfg.ImportDir)
		if err != nil {
			return err
		}
		docs = append(docs, loaded...)
		logger.Info("语料目录已读取", "dir", cfg.ImportDir, "files", len(loaded))
	}

	if cfg.GenerateDocs > 0 {
		docs = append(docs, corpus.Generate(cfg.GenerateDocs)...)
		logger.Info("合成语料已生成", "docs", cfg.GenerateDocs)
	}

	if len(docs) == 0 {
		return nil
	}

	tb, err := targetTable(engine, cfg)
	if err != nil {
		return err
	}

	start := time.Now()
	for _, d := range docs {
		if _, err := tb.Upsert(tsh.Document{ID: d.ID, Fields: d.Fields}); err != nil {
			return fmt.Errorf("导入文档 %q 失败: %w", d.ID, err)
		}
	}

	st := tb.Stats()
	logger.Info("语料导入完成",
		"table", tb.Name(),
		"docs", st.Docs,
		"terms", st.Terms,
		"fields", st.Fields,
		"avg_doc_len", st.AvgDocLen,
		"index_mb", float64(st.IndexBytes)/(1<<20),
		"elapsed", time.Since(start).Round(time.Millisecond).String(),
	)

	return nil
}

// targetTable 决定导入的目标表。
//
// 没指定 -import-table 时落到默认表上；指定了就必须**已经存在**——
// 拼错的表名如果被静默创建，数据就写到了一张谁也不知道的新表里。
func targetTable(engine *tsh.Engine, cfg config.Config) (*tsh.Table, error) {
	if cfg.ImportTable != "" {
		return engine.Table(cfg.ImportTable)
	}
	return engine.Table(tablename.Default)
}
