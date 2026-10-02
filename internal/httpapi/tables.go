package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/tablename"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// createTableRequest 是 PUT /api/v1/tables/{table} 的请求体。
//
// Schema 给出字段类型。**日期与关键字必须在这里声明**：
// JSON 里没有日期类型，`"2024-01-15"` 只是一个字符串，光看值分不出来。
type createTableRequest struct {
	Schema map[string]tsh.FieldKind `json:"schema"`
}

// tableInfo 是单张表的详情。
type tableInfo struct {
	Name       string                   `json:"name"`
	Docs       int                      `json:"docs"`
	Terms      int                      `json:"terms"`
	Fields     int                      `json:"fields"`
	AvgDocLen  float64                  `json:"avg_doc_len"`
	IndexBytes int64                    `json:"index_bytes"`
	Persisted  bool                     `json:"persisted"`
	Schema     map[string]tsh.FieldKind `json:"schema,omitempty"`
}

// tableListResponse 是 GET /api/v1/tables 的响应体。
type tableListResponse struct {
	Tables []tableInfo `json:"tables"`
}

// handleListTables 处理 GET /api/v1/tables。
func (s *Server) handleListTables(w http.ResponseWriter, _ *http.Request) {
	stats := s.engine.TableStats()

	out := make([]tableInfo, 0, len(stats))
	for _, ts := range stats {
		info := tableInfo{
			Name:       ts.Name,
			Docs:       ts.Stats.Docs,
			Terms:      ts.Stats.Terms,
			Fields:     ts.Stats.Fields,
			AvgDocLen:  ts.Stats.AvgDocLen,
			IndexBytes: ts.Stats.IndexBytes,
			Persisted:  ts.Persisted,
		}
		// schema 取不到不算错误：表的统计本身仍然有效。
		if tb, err := s.engine.Table(ts.Name); err == nil {
			info.Schema = tb.Schema()
		}
		out = append(out, info)
	}

	writeJSON(w, http.StatusOK, tableListResponse{Tables: out})
}

// handleCreateTable 处理 PUT /api/v1/tables/{table}。
//
// **幂等**：表已存在且 schema 一致时返回 200 而不是 409。
//
// 这与 tsh.CreateTable 的语义刻意不同：HTTP 的 PUT 在语义上就是幂等的，
// 而引擎层的 CreateTable 用 ErrTableExists 保护 Go 调用方不犯
// 「以为新建了、其实在往旧表写」的错误。放在这里做一次调和。
func (s *Server) handleCreateTable(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("table"))
	if err := tablename.Validate(name); err != nil {
		s.writeMappedError(w, err)
		return
	}

	// 先尝试取：已存在就走幂等分支，也顺便避免解析 body。
	existing, err := s.engine.Table(name)
	if err == nil {
		var req createTableRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				s.writeMappedError(w, err)
				return
			}
		}
		if err := checkSchemaCompatible(existing.Schema(), req.Schema); err != nil {
			s.writeMappedError(w, err)
			return
		}
		s.writeTable(w, http.StatusOK, existing)
		return
	}
	if !errors.Is(err, tsh.ErrTableNotFound) {
		s.writeMappedError(w, err)
		return
	}

	var req createTableRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			s.writeMappedError(w, err)
			return
		}
	}

	tb, err := s.engine.CreateTable(name, req.Schema)
	if err != nil {
		// 并发下可能刚好被别人建出来了，回退到幂等语义。
		if errors.Is(err, tsh.ErrTableExists) {
			if again, e2 := s.engine.Table(name); e2 == nil {
				s.writeTable(w, http.StatusOK, again)
				return
			}
		}
		s.writeMappedError(w, err)
		return
	}

	s.writeTable(w, http.StatusCreated, tb)
}

// checkSchemaCompatible 校验请求里的 schema 与已有表的 schema 是否一致。
//
// 声明一个**新**字段是允许的（动态映射本来就会长出新字段）；
// 把已有字段改成另一种类型必须报错——那正是引擎会拒绝的事，
// 在这里提前给出更清楚的说明。
func checkSchemaCompatible(existing, want map[string]tsh.FieldKind) error {
	for field, kind := range want {
		got, ok := existing[field]
		if !ok {
			continue
		}
		if got != kind {
			return fmt.Errorf("%w: 字段 %q 已经是 %s 类型，不能改成 %s",
				index.ErrFieldKindConflict, field, got, kind)
		}
	}
	return nil
}

// handleGetTable 处理 GET /api/v1/tables/{table}。
func (s *Server) handleGetTable(w http.ResponseWriter, r *http.Request) {
	tb, err := s.tableFor(r)
	if err != nil {
		s.writeMappedError(w, err)
		return
	}
	s.writeTable(w, http.StatusOK, tb)
}

// handleDropTable 处理 DELETE /api/v1/tables/{table}。
func (s *Server) handleDropTable(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("table"))
	if err := tablename.Validate(name); err != nil {
		s.writeMappedError(w, err)
		return
	}

	if err := s.engine.DropTable(name); err != nil {
		s.writeMappedError(w, err)
		return
	}

	s.log.Info("已删除表", "table", name)
	w.WriteHeader(http.StatusNoContent)
}

// writeTable 输出单张表的详情。
func (s *Server) writeTable(w http.ResponseWriter, status int, tb *tsh.Table) {
	st := tb.Stats()
	writeJSON(w, status, tableInfo{
		Name:       tb.Name(),
		Docs:       st.Docs,
		Terms:      st.Terms,
		Fields:     st.Fields,
		AvgDocLen:  st.AvgDocLen,
		IndexBytes: st.IndexBytes,
		Persisted:  tb.Persisted(),
		Schema:     tb.Schema(),
	})
}
