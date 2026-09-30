package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/internal/corpus"
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

	start := time.Now()
	for _, d := range docs {
		if _, err := engine.Upsert(tsh.Document{ID: d.ID, Fields: d.Fields}); err != nil {
			return fmt.Errorf("导入文档 %q 失败: %w", d.ID, err)
		}
	}

	st := engine.Stats()
	logger.Info("语料导入完成",
		"docs", st.Docs,
		"terms", st.Terms,
		"fields", st.Fields,
		"avg_doc_len", st.AvgDocLen,
		"index_mb", float64(st.IndexBytes)/(1<<20),
		"elapsed", time.Since(start).Round(time.Millisecond).String(),
	)

	return nil
}
