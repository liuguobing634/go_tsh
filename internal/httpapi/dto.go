package httpapi

import (
	"encoding/json"
	"net/http"
)

// healthResponse 是 GET /healthz 的响应体。
type healthResponse struct {
	Status string `json:"status"`
}

// statsResponse 是 GET /api/v1/stats 的响应体。
type statsResponse struct {
	Docs       int     `json:"docs"`
	Terms      int     `json:"terms"`
	AvgDocLen  float64 `json:"avg_doc_len"`
	IndexBytes int64   `json:"index_bytes"`
}

// writeJSON 先完成序列化再写响应，
// 避免序列化中途失败时已经发出了 200 状态头而无法纠正。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "响应序列化失败")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
