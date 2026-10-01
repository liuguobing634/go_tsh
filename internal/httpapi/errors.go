package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/query"
	"github.com/liuguobing/go_tsh/pkg/tsh"
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

// 内部哨兵错误，用于把「参数问题」与「服务端故障」区分开。
var (
	errBadParam = errors.New("查询参数非法")
	errBadBody  = errors.New("请求体非法")
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

// writeMappedError 把内部错误映射成合适的 HTTP 状态码与错误码。
//
// 映射遵循一条原则：**调用方能改的错，给 4xx 和可读的原因；
// 调用方改不了的，给 500 且不泄漏内部细节**。
func (s *Server) writeMappedError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return

	case errors.Is(err, index.ErrDocumentNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return

	case errors.Is(err, index.ErrDocumentExists):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
		return
	}

	// 请求体超限必须排在 errBadBody 之前判断：
	// 解码错误会把 MaxBytesError 包在 errBadBody 里面，
	// 顺序反了就会把「太大」误报成「格式不对」。
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
			fmt.Sprintf("请求体超过 %d 字节上限", maxBytes.Limit))
		return
	}

	switch {
	case errors.Is(err, errBadParam),
		errors.Is(err, errBadBody),
		errors.Is(err, index.ErrEmptyExternalID),
		errors.Is(err, index.ErrEmptyFieldName),
		errors.Is(err, index.ErrNoFields),
		errors.Is(err, index.ErrTooManyFields),
		errors.Is(err, index.ErrDocumentTooLarge),

		// 字段类型相关的错误一律 400：它们全都是调用方能改的
		// （换个字段名、改个值、或者用对类型的那张表）。
		// 归到 500 会让调用方以为是服务端故障而无从下手。
		errors.Is(err, index.ErrFieldKindConflict),
		errors.Is(err, index.ErrInvalidFieldKind),
		errors.Is(err, index.ErrInvalidFieldValue),
		errors.Is(err, index.ErrNotNumericField),
		errors.Is(err, tsh.ErrUnsupportedFieldType),
		errors.Is(err, tsh.ErrAmbiguousField),
		errors.Is(err, tsh.ErrIntegerTooLarge),
		errors.Is(err, tsh.ErrNoFields):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return

	case errors.Is(err, query.ErrEmptyQuery),
		errors.Is(err, query.ErrTooManyClauses),
		errors.Is(err, query.ErrNilQuery):
		writeError(w, http.StatusBadRequest, CodeInvalidQuery, err.Error())
		return
	}

	// 带位置的语法错误单独处理：它是个具体类型而不是哨兵。
	var syntaxErr *query.SyntaxError
	if errors.As(err, &syntaxErr) {
		writeError(w, http.StatusBadRequest, CodeInvalidQuery, syntaxErr.Error())
		return
	}

	// 其余一律 500，并且只记日志、不外泄细节。
	s.log.Error("请求处理失败", "err", err)
	writeError(w, http.StatusInternalServerError, CodeInternal, "服务器内部错误")
}
