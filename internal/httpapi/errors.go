package httpapi

import (
	"encoding/json"
	"net/http"
)

// 错误码与 HTTP 状态码解耦，便于调用方做程序化判断而非解析文案。
const (
	CodeInternal        = "INTERNAL"
	CodeBadRequest      = "BAD_REQUEST"
	CodeNotFound        = "NOT_FOUND"
	CodeConflict        = "CONFLICT"
	CodePayloadTooLarge = "PAYLOAD_TOO_LARGE"
	CodeInvalidQuery    = "INVALID_QUERY"
)

// apiError 是错误响应中的错误对象。
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorResponse 是所有失败响应的统一外壳：{"error":{"code":...,"message":...}}
type errorResponse struct {
	Error apiError `json:"error"`
}

// writeError 输出统一格式的错误响应。
func writeError(w http.ResponseWriter, status int, code, message string) {
	body, err := json.Marshal(errorResponse{Error: apiError{Code: code, Message: message}})
	if err != nil {
		// 兜底：连错误对象都序列化不了时退化为纯文本，保证调用方仍能拿到 500。
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
