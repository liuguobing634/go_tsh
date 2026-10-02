package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// searchResponse 是 GET /api/v1/search 的响应体。
type searchResponse struct {
	// TookMS 是本服务端处理这次检索的耗时，单位毫秒。
	TookMS float64        `json:"took_ms"`
	Total  int            `json:"total"`
	Hits   []searchHitDTO `json:"hits"`
}

// searchHitDTO 是一条命中结果。
type searchHitDTO struct {
	ID         string            `json:"id"`
	Score      float64           `json:"score"`
	Fields     map[string]string `json:"fields,omitempty"`
	Highlights map[string]string `json:"highlights,omitempty"`
}

// handleSearch 处理 GET /api/v1/search。
//
// 查询参数：
//
//	q          查询串（必填）
//	limit      返回条数，默认 10，上限 100
//	offset     跳过条数，默认 0
//	field      限定字段，可重复出现；不传表示全部字段
//	highlight  是否返回高亮片段，默认 false
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	tb, err := s.tableFor(r)
	if err != nil {
		s.writeMappedError(w, err)
		return
	}

	params := r.URL.Query()

	queryStr := strings.TrimSpace(params.Get("q"))
	if queryStr == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidQuery, "缺少查询参数 q")
		return
	}

	limit, err := intParam(params.Get("limit"), 10, "limit")
	if err != nil {
		s.writeMappedError(w, err)
		return
	}

	offset, err := intParam(params.Get("offset"), 0, "offset")
	if err != nil {
		s.writeMappedError(w, err)
		return
	}

	highlight, err := boolParam(params.Get("highlight"), false, "highlight")
	if err != nil {
		s.writeMappedError(w, err)
		return
	}

	var fields []string
	for _, f := range params["field"] {
		if f = strings.TrimSpace(f); f != "" {
			fields = append(fields, f)
		}
	}

	start := time.Now()
	res, err := tb.Search(tsh.SearchRequest{
		Query:     queryStr,
		Fields:    fields,
		Limit:     limit,
		Offset:    offset,
		Highlight: highlight,
	})
	if err != nil {
		s.writeMappedError(w, err)
		return
	}
	took := time.Since(start)

	hits := make([]searchHitDTO, 0, len(res.Hits))
	for _, h := range res.Hits {
		hits = append(hits, searchHitDTO{
			ID:         h.ID,
			Score:      h.Score,
			Fields:     h.Fields,
			Highlights: h.Highlights,
		})
	}

	writeJSON(w, http.StatusOK, searchResponse{
		TookMS: float64(took.Microseconds()) / 1000,
		Total:  res.Total,
		Hits:   hits,
	})
}

// intParam 解析整数查询参数；未提供时返回 def。
func intParam(raw string, def int, name string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}

	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %s 不是合法整数: %q", errBadParam, name, raw)
	}
	return n, nil
}

// boolParam 解析布尔查询参数；未提供时返回 def。
//
// 只认显式的 true/false/1/0，不做「非空即真」的宽松解释——
// ?highlight=no 被当成 true 是最容易让人踩坑的那类接口。
func boolParam(raw string, def bool, name string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}

	switch strings.ToLower(raw) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("%w: %s 只接受 true/false/1/0，收到 %q", errBadParam, name, raw)
	}
}
