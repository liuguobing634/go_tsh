// Package httpapi 把 tsh 引擎暴露为 HTTP JSON 接口。
//
// 完整路由清单（Phase 0 已落地 /healthz 与 /api/v1/stats）：
//
//	POST   /api/v1/documents        新建文档
//	PUT    /api/v1/documents/{id}   覆盖更新
//	DELETE /api/v1/documents/{id}   删除
//	GET    /api/v1/documents/{id}   取原文
//	GET    /api/v1/search           检索
//	GET    /api/v1/stats            索引统计
//	GET    /healthz                 存活探针
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/liuguobing/go_tsh/internal/config"
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

	mux.HandleFunc("POST /api/v1/documents", s.handleCreateDocument)
	mux.HandleFunc("PUT /api/v1/documents/{id}", s.handlePutDocument)
	mux.HandleFunc("GET /api/v1/documents/{id}", s.handleGetDocument)
	mux.HandleFunc("DELETE /api/v1/documents/{id}", s.handleDeleteDocument)

	mux.HandleFunc("GET /api/v1/search", s.handleSearch)

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

// handleHealth 是存活探针，只要进程还能响应就返回 200。
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// handleStats 暴露索引规模，供容量观测与调试使用。
func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	st := s.engine.Stats()
	writeJSON(w, http.StatusOK, statsResponse{
		Docs:       st.Docs,
		Terms:      st.Terms,
		Fields:     st.Fields,
		AvgDocLen:  st.AvgDocLen,
		IndexBytes: st.IndexBytes,
	})
}
