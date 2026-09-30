package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// documentRequest 是 POST /api/v1/documents 的请求体。
type documentRequest struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}

// documentResponse 是文档类接口的统一响应体。
type documentResponse struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}

// handleCreateDocument 处理 POST /api/v1/documents。
//
// 文档已存在时返回 409：新建与覆盖是两个不同的语义，
// 混在一起会让「不小心覆盖了别人的文档」变得无声无息。
// 需要覆盖语义请用 PUT。
func (s *Server) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	var req documentRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeMappedError(w, err)
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		s.writeMappedError(w, fmt.Errorf("%w: 字段 id 不能为空", errBadBody))
		return
	}

	if err := s.engine.Create(tsh.Document{ID: req.ID, Fields: req.Fields}); err != nil {
		s.writeMappedError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, documentResponse{ID: req.ID, Fields: req.Fields})
}

// handlePutDocument 处理 PUT /api/v1/documents/{id}。
//
// 覆盖式写入：不存在则新建（201），存在则整体替换（200）。
// 用 201/200 区分这两种情况，让调用方能判断自己是创建者还是覆盖者。
func (s *Server) handlePutDocument(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "路径中的文档 id 不能为空")
		return
	}

	var req documentRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeMappedError(w, err)
		return
	}

	// 请求体里若也带了 id，必须与路径一致，否则是调用方搞错了。
	if req.ID != "" && strings.TrimSpace(req.ID) != id {
		s.writeMappedError(w, fmt.Errorf("%w: 路径 id %q 与请求体 id %q 不一致",
			errBadBody, id, req.ID))
		return
	}

	created, err := s.engine.Upsert(tsh.Document{ID: id, Fields: req.Fields})
	if err != nil {
		s.writeMappedError(w, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, documentResponse{ID: id, Fields: req.Fields})
}

// handleGetDocument 处理 GET /api/v1/documents/{id}。
func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))

	doc, ok := s.engine.GetDocument(id)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "文档不存在: "+id)
		return
	}

	writeJSON(w, http.StatusOK, documentResponse{ID: doc.ID, Fields: doc.Fields})
}

// handleDeleteDocument 处理 DELETE /api/v1/documents/{id}。
func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "路径中的文档 id 不能为空")
		return
	}

	if err := s.engine.Delete(id); err != nil {
		s.writeMappedError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON 解码请求体，拒绝未知字段与多余的尾随内容。
//
// 拒绝未知字段是刻意的：客户端若把 fields 拼错成 field，静默忽略的后果
// 是「接口返回 201，但写入了一篇没有任何字段的文档」——这种错误在
// 生产里极难排查，不如当场报 400。
func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return fmt.Errorf("%w: 缺少请求体", errBadBody)
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		// 用 %w 而不是 %v 包装内层错误：MaxBytesReader 靠
		// *http.MaxBytesError 这个具体类型来报告超限，
		// 一旦被格式化成字符串，就再也识别不出 413 了。
		return fmt.Errorf("%w: %w", errBadBody, err)
	}

	// 请求体里只能有一个 JSON 值。
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: 请求体只能包含一个 JSON 对象", errBadBody)
	}
	return nil
}
