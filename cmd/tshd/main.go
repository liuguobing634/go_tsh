// Command tshd 是 go_tsh 全文搜索服务的守护进程入口。
//
// 用法示例：
//
//	tshd -addr :8080 -log-level info
//	tshd -import ./testdata/corpus          # 启动时导入目录里的文本文件
//	tshd -generate 100000                   # 启动时合成 10 万篇语料（压测用）
//	tshd -analyzer chinese                  # 汉字走词典分词
//	tshd -analyzer chinese -dict ./words.txt # 再追加领域词
//
// 每个命令行参数都可被同名环境变量覆盖（前缀 TSH_、中划线转下划线并大写），
// 例如 -query-timeout 对应 TSH_QUERY_TIMEOUT。
// 命令行显式指定的值优先级最高，不会被环境变量覆盖。
//
// 注意：-analyzer 会决定索引里存的是什么词条，因此**换分析器必须重建索引**。
// 内存索引在进程重启时重建，改这个参数后重启即可。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/internal/httpapi"
)

// version 由 -ldflags "-X main.version=..." 在构建时注入。
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

// run 承载全部启动逻辑并返回进程退出码。
// 把逻辑收进 run 而不是直接写在 main 里，是为了让 defer 能正常执行。
func run(args []string) int {
	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		// 此刻日志系统尚未建立，只能直接写 stderr。
		fmt.Fprintf(os.Stderr, "tshd: 配置错误: %v\n", err)
		return 2
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	// 这些上限此前只被解析与校验，从未接进引擎——用户改了
	// -max-doc-tokens / -max-doc-fields / -max-query-terms 不会有任何效果。
	engine, err := newEngine(cfg)
	if err != nil {
		logger.Error("创建引擎失败", "err", err)
		return 1
	}

	// 语料导入必须发生在监听之前：否则客户端可能在索引还没灌完时
	// 就查到一个空索引，得到「服务在跑但搜不到东西」的困惑。
	if err := bootstrap(engine, cfg, logger); err != nil {
		logger.Error("语料导入失败", "err", err)
		return 1
	}

	srv := httpapi.New(cfg, engine, logger)

	// 收到 Ctrl+C（Windows/Linux）或 SIGTERM（Linux）时取消 ctx。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("服务启动", "addr", cfg.Addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		logger.Error("监听失败", "err", err)
		return 1
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭", "grace", cfg.ShutdownGrace.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("优雅关闭失败，进程将强制退出", "err", err)
		return 1
	}

	logger.Info("已退出")
	return 0
}

// newLogger 构造结构化 JSON 日志器；级别非法时回落到 info。
func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
