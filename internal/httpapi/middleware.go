package httpapi

import (
	"net/http"
	"runtime/debug"
	"time"
)

// middleware 是标准的 HTTP 中间件签名。
type middleware func(http.Handler) http.Handler

// chain 按声明顺序自外向内包裹 handler：
// chain(h, a, b) 的执行顺序为 a -> b -> h。
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// withRecover 兜住 handler 中的 panic，保证单个请求不会拖垮整个进程。
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("handler 发生 panic",
					"err", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, CodeInternal, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withRequestLog 记录每个请求的方法、路径、状态码、字节数与耗时。
func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		s.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", float64(time.Since(start).Microseconds())/1000.0,
			"remote", r.RemoteAddr,
		)
	})
}

// withBodyLimit 给请求体套上硬上限；
// 超限时读取会返回 *http.MaxBytesError，由各 handler 映射为 413。
func (s *Server) withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder 记录响应状态码与写出字节数，供访问日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}
