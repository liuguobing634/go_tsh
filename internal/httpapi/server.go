// Package httpapi 把 tsh 引擎暴露为 HTTP JSON 接口。
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
	"log/slog"
	"net/http"
	"strings"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/internal/tablename"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// Server 组合 HTTP 服务器、配置、引擎与日志器。
//
// 内嵌 *http.Server，调用方可直接使用 ListenAndServe / Shutdown / Handler。
type Server struct {
	*http.Server

	cfg    config.Config
	engine *tsh.Engine
	log    *slog.Logger
}

// New 构造服务器并注册全部路由与中间件。
func New(cfg config.Config, engine *tsh.Engine, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if engine == nil {
		engine = tsh.New()
	}

	s := &Server{cfg: cfg, engine: engine, log: log}

	mux := http.NewServeMux()
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

	s.Server = &http.Server{
		Addr:         cfg.Addr,
		Handler:      chain(mux, s.withRecover, s.withRequestLog, s.withBodyLimit),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	return s
}

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
