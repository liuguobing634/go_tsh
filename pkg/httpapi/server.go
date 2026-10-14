// Package httpapi 把 tsh 引擎暴露为 HTTP JSON 接口。
//
// # 两种用法
//
// 独立服务（本仓库的 cmd/tshd 就是这么用的）：
//
//	srv := httpapi.New(httpapi.DefaultOptions(), engine, logger)
//	srv.ListenAndServe()
//
// 挂进调用方自己的进程：
//
//	api := httpapi.New(opts, engine, logger)
//	mux := http.NewServeMux()
//	api.Register(mux)                        // 路由进自己的 mux
//	http.ListenAndServe(addr, api.Wrap(mux)) // 中间件套在最外层
//
// 这个包**不依赖任何命令行/配置解析**：它的全部可调项都在 Options 里。
// 库不该知道什么是 flag、什么是环境变量。
//
// # 路由清单
//
// 表管理：
//
//	GET    /api/v1/tables              列出所有表
//	PUT    /api/v1/tables/{table}      建表（可带 schema）
//	GET    /api/v1/tables/{table}      表详情（schema + 统计）
//	DELETE /api/v1/tables/{table}      删表
//
// 表级文档与检索：
//
//	POST   /api/v1/tables/{table}/documents        新建文档
//	PUT    /api/v1/tables/{table}/documents/{id}   覆盖更新
//	GET    /api/v1/tables/{table}/documents/{id}   取原文
//	DELETE /api/v1/tables/{table}/documents/{id}   删除
//	GET    /api/v1/tables/{table}/search           检索
//
// 旧的扁平路由**保持不变**，等价于对默认表（default）的操作：
//
//	POST   /api/v1/documents
//	PUT    /api/v1/documents/{id}
//	GET    /api/v1/documents/{id}
//	DELETE /api/v1/documents/{id}
//	GET    /api/v1/search
//
// 其余：
//
//	GET    /api/v1/stats            全局统计（含每表明细）
//	GET    /healthz                 存活探针
package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/liuguobing/go_tsh/internal/tablename"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// Options 是 HTTP 层的全部可调项。
//
// 零值不可用（Addr 为空、超时为零会让连接立刻超时），
// 请用 DefaultOptions() 起手再改。
type Options struct {
	// Addr 是监听地址，例如 ":8080" 或 "127.0.0.1:8080"。
	Addr string

	// MaxBodyBytes 是单次请求体的字节上限。
	//
	// 它是**硬上限**：超限时读取会返回 *http.MaxBytesError，
	// 由各 handler 映射成 413，而不是把内存吃光。
	MaxBodyBytes int64

	// ReadTimeout 是读请求（含请求体）的超时。
	ReadTimeout time.Duration

	// WriteTimeout 是写响应的超时。
	//
	// 注意它覆盖整个 handler 的执行时间：设得比最慢的检索还短，
	// 慢查询会被直接掐断。
	WriteTimeout time.Duration

	// IdleTimeout 是 keep-alive 空闲连接的超时。
	IdleTimeout time.Duration
}

// DefaultOptions 返回一套适合本地开发与中小规模部署的默认值。
func DefaultOptions() Options {
	return Options{
		Addr:         ":8080",
		MaxBodyBytes: 8 << 20, // 8 MiB
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

// Validate 检查配置是否自洽。
//
// 单独一个方法而不是塞进 New：调用方（尤其是从配置文件构造 Options 的
// cmd）应当在**拥有上下文**的地方报错，而不是在启动 HTTP 服务器时才炸。
func (o Options) Validate() error {
	if strings.TrimSpace(o.Addr) == "" {
		return fmt.Errorf("httpapi: Addr 不能为空")
	}
	if o.MaxBodyBytes <= 0 {
		return fmt.Errorf("httpapi: MaxBodyBytes 必须为正，当前为 %d", o.MaxBodyBytes)
	}
	for _, d := range []struct {
		name string
		val  time.Duration
	}{
		{"ReadTimeout", o.ReadTimeout},
		{"WriteTimeout", o.WriteTimeout},
		{"IdleTimeout", o.IdleTimeout},
	} {
		if d.val <= 0 {
			return fmt.Errorf("httpapi: %s 必须为正，当前为 %s", d.name, d.val)
		}
	}
	return nil
}

// Server 组合 HTTP 服务器、配置、引擎与日志器。
//
// 内嵌 *http.Server：调用方可以直接使用 ListenAndServe / Shutdown，
// 也可以读 Handler 字段拿到带中间件的完整处理器。
type Server struct {
	*http.Server

	opts   Options
	engine *tsh.Engine
	log    *slog.Logger
}

// New 构造服务器并注册全部路由与中间件。
//
// engine 为 nil 时会新建一个纯内存引擎（方便本地试跑）；
// log 为 nil 时用 slog.Default()。
func New(opts Options, engine *tsh.Engine, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if engine == nil {
		engine = tsh.New()
	}

	s := &Server{opts: opts, engine: engine, log: log}

	mux := http.NewServeMux()
	s.Register(mux)

	s.Server = &http.Server{
		Addr:         opts.Addr,
		Handler:      s.Wrap(mux),
		ReadTimeout:  opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout,
		IdleTimeout:  opts.IdleTimeout,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	return s
}

// Register 把全部路由注册到调用方提供的 mux 上。
//
// 它**只注册路由**，不套用本包的中间件（访问日志、panic 恢复、请求体上限）。
// 需要那些的话有两条路：
//
//   - 只用本包：用 New 返回的 Server，它的 Handler 已经套好了；
//   - 两者都要：`api.Register(mux)` 之后用 `api.Wrap(mux)` 包一层。
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/v1/stats", s.handleStats)

	// 表管理
	mux.HandleFunc("GET /api/v1/tables", s.handleListTables)
	mux.HandleFunc("PUT /api/v1/tables/{table}", s.handleCreateTable)
	mux.HandleFunc("GET /api/v1/tables/{table}", s.handleGetTable)
	mux.HandleFunc("DELETE /api/v1/tables/{table}", s.handleDropTable)

	// 文档与检索：**同一批处理器注册两次**。
	//
	// 处理器内部用 tableFor(r) 取表名，取不到就用默认表。
	// 这样旧路由与新路由共用一套实现，不会出现「改了一个忘了另一个」——
	// 那类分歧在检索语义上尤其难发现。
	for _, p := range []string{"", "/tables/{table}"} {
		mux.HandleFunc("POST /api/v1"+p+"/documents", s.handleCreateDocument)
		mux.HandleFunc("PUT /api/v1"+p+"/documents/{id}", s.handlePutDocument)
		mux.HandleFunc("GET /api/v1"+p+"/documents/{id}", s.handleGetDocument)
		mux.HandleFunc("DELETE /api/v1"+p+"/documents/{id}", s.handleDeleteDocument)
		mux.HandleFunc("GET /api/v1"+p+"/search", s.handleSearch)
	}
}

// Wrap 给任意处理器套上本包的中间件：panic 恢复、访问日志、请求体上限。
//
// 顺序是「先兜 panic，再记日志，最后限流请求体」——日志要能记到
// 被 recover 兜住的那次失败，所以它必须在 recover 内层。
func (s *Server) Wrap(next http.Handler) http.Handler {
	return chain(next, s.withRecover, s.withRequestLog, s.withBodyLimit)
}

// Engine 返回底层引擎，方便调用方自己再挂几个路由上去。
func (s *Server) Engine() *tsh.Engine { return s.engine }

// Options 返回构造时用的配置副本。
func (s *Server) Options() Options { return s.opts }

// tableFor 从请求路径里取出目标表。
//
// 路径里没有 {table} 时（走的是旧的扁平路由）落到默认表上。
//
// 表不存在返回 tsh.ErrTableNotFound，由 writeMappedError 映射成 404。
func (s *Server) tableFor(r *http.Request) (*tsh.Table, error) {
	name := strings.TrimSpace(r.PathValue("table"))
	if name == "" {
		name = tablename.Default
	}
	return s.engine.Table(name)
}

// handleHealth 是存活探针，只要进程还能响应就返回 200。
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// handleStats 暴露索引规模，供容量观测与调试使用。
//
// 有了表之后，顶层字段描述的是**默认表**（保持既有调用方的兼容），
// 各表分别多大看 tables。全局求和意义不大——真正要看的是分布。
func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	st := s.engine.Stats()
	writeJSON(w, http.StatusOK, statsResponse{
		Docs:       st.Docs,
		Terms:      st.Terms,
		Fields:     st.Fields,
		AvgDocLen:  st.AvgDocLen,
		IndexBytes: st.IndexBytes,
		Tables:     s.engine.TableStats(),
	})
}
