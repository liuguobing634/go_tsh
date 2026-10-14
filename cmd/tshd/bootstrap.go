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
//
// # 只导入一次
//
// 导入完成后会在日志里留下一条标记，之后再启动看到标记就**跳过**，
// 除非显式给了 -reimport。没有这一步的话，带持久化的部署光是重启就
// 会让日志不断膨胀——实测 20 篇语料连开 4 次，日志变成语料的 4.4 倍。
func bootstrap(engine *tsh.Engine, cfg config.Config, logger *slog.Logger) error {
	if cfg.ImportDir == "" && cfg.GenerateDocs <= 0 {
		return nil // 没有启动动作
	}

	tb, err := targetTable(engine, cfg)
	if err != nil {
		return err
	}

	if skip, changed, reason := shouldSkipImport(tb, cfg); skip {
		// 参数变了要**告警**而不是 Info：用户多半以为自己导入了新语料，
		// 而实际上什么都没发生。
		if changed {
			logger.Warn("语料导入已跳过：本次参数与上次不同，但默认只导入一次",
				"table", tb.Name(), "reason", reason)
		} else {
			logger.Info("语料导入已跳过", "table", tb.Name(), "reason", reason)
		}
		return nil
	}

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

	start := time.Now()
	for _, d := range docs {
		if _, err := tb.Upsert(tsh.Document{ID: d.ID, Fields: d.Fields}); err != nil {
			return fmt.Errorf("导入文档 %q 失败: %w", d.ID, err)
		}
	}

	// 打标记必须在文档写完**之后**：它表达的是「导入已经完成」。
	// 标记与文档在同一个日志里、且在所有文档记录之后，因此共用
	// fsync 批次——标记落盘时数据必然也已落盘。
	if err := tb.MarkImported(tsh.ImportMarker{
		Dir:      cfg.ImportDir,
		Files:    len(docs),
		Generate: cfg.GenerateDocs,
	}); err != nil {
		return fmt.Errorf("记录导入标记失败: %w", err)
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

// shouldSkipImport 判断这次启动要不要跳过导入。
//
// 返回 skip、changed、reason：
//   - 显式要求重导 → 不跳
//   - 从没导入过 → 不跳
//   - 导入过 → 跳过；changed 表示这次的参数与上次不同
//
// 参数变了时**也跳过**，但要把差异和解决办法都写进 reason，
// 并由调用方以告警级别打出来：只看「有没有标记」会静默地什么都不导，
// 而用户以为导了。
func shouldSkipImport(tb *tsh.Table, cfg config.Config) (skip, changed bool, reason string) {
	if cfg.Reimport {
		return false, false, ""
	}

	mark, ok := tb.Imported()
	if !ok {
		return false, false, ""
	}

	const hint = "；要按当前参数重导请加 -reimport"

	if mark.Dir != cfg.ImportDir || mark.Generate != cfg.GenerateDocs {
		return true, true, fmt.Sprintf(
			"上次导入的参数与这次不同（上次 dir=%q generate=%d，这次 dir=%q generate=%d）%s",
			mark.Dir, mark.Generate, cfg.ImportDir, cfg.GenerateDocs, hint)
	}

	return true, false, fmt.Sprintf(
		"上次启动已经导入过（dir=%q files=%d generate=%d）%s",
		mark.Dir, mark.Files, mark.Generate, hint)
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
