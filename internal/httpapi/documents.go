package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// documentRequest 是文档写入接口的请求体。
//
// Fields 用 map[string]any 而不是 map[string]string：字段类型由
// **JSON 本身**表达，与 ES 的用法一致。
//
//	{"fields": {"title": "笔记本", "price": 4999, "onSale": true}}
//
//	title → 文本    price → 数值    onSale → 关键字 "true"
//
// 注意分流依据是 JSON 类型而不是值的字面形态：带引号的 "4999" 是文本，
// 不带引号的 4999 是数值。靠猜的话，商品编号 "0755" 会被当成 755，
// 前导零就没了。
type documentRequest struct {
	ID     string         `json:"id"`
	Fields map[string]any `json:"fields"`
}

// documentResponse 是文档类接口的统一响应体。
//
// 字段按**存储时的类型**回显：写进去是数字的，取出来还是数字，
// 而不是变成字符串 "4999"。
type documentResponse struct {
	ID     string         `json:"id"`
	Fields map[string]any `json:"fields"`
}

// fieldsAsJSON 按字段类型把类型化的文档合并回一张 JSON 字段表。
func fieldsAsJSON(doc tsh.Document) map[string]any {
	out := make(map[string]any,
		len(doc.Fields)+len(doc.Keywords)+len(doc.Numbers)+len(doc.Dates))

	for name, v := range doc.Fields {
		out[name] = v
	}
	for name, v := range doc.Keywords {
		out[name] = v
	}
	for name, v := range doc.Numbers {
		out[name] = v
	}
	for name, v := range doc.Dates {
		// 与写入侧同一个格式，保证「写进去什么、取出来什么」。
		out[name] = v.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// parseDocument 把请求体转换成类型化的文档。
func parseDocument(s *Server, id string, fields map[string]any) (tsh.Document, error) {
	return s.engine.ParseDocument(id, fields)
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

	doc, err := parseDocument(s, req.ID, req.Fields)
	if err != nil {
		s.writeMappedError(w, err)
		return
	}

	if err := s.engine.Create(doc); err != nil {
		s.writeMappedError(w, err)
		return
	}

	// 回显**存储后的结果**而不是请求原文：这样响应里的类型一定与
	// 索引里的一致，调用方不必再去猜服务端把值当成了什么。
	s.writeStoredDocument(w, http.StatusCreated, req.ID)
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

	doc, err := parseDocument(s, id, req.Fields)
	if err != nil {
		s.writeMappedError(w, err)
		return
	}

	created, err := s.engine.Upsert(doc)
	if err != nil {
		s.writeMappedError(w, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeStoredDocument(w, status, id)
}

// writeStoredDocument 取回刚写入的文档并回显。
func (s *Server) writeStoredDocument(w http.ResponseWriter, status int, id string) {
	doc, ok := s.engine.GetDocument(id)
	if !ok {
		// 刚写完就取不到，说明索引内部出了问题，不能假装成功。
		s.writeMappedError(w, fmt.Errorf("写入后取回文档 %q 失败", id))
		return
	}
	writeJSON(w, status, documentResponse{ID: doc.ID, Fields: fieldsAsJSON(doc)})
}

// handleGetDocument 处理 GET /api/v1/documents/{id}。
func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))

	doc, ok := s.engine.GetDocument(id)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "文档不存在: "+id)
		return
	}

	writeJSON(w, http.StatusOK, documentResponse{ID: doc.ID, Fields: fieldsAsJSON(doc)})
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
//
// UseNumber 也是刻意的：默认会把 JSON 数字解成 float64，
// 那时 `20240101123456789` 这样的整数**精度已经丢了**，再检查也来不及。
// 保留原始文本（json.Number）才能判断它是否超出 float64 的精确范围，
// 从而给出明确的错误而不是静默改变用户的数据。
func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return fmt.Errorf("%w: 缺少请求体", errBadBody)
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	dec.UseNumber()

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
