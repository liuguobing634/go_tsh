package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// newTestServer 构造一个日志被丢弃、配置为默认值的服务器。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	return New(
		config.Default(),
		tsh.New(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

// do 发起一次内存内请求，返回记录器。
func do(t *testing.T, srv *Server, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(method, target, body))
	return rec
}

func TestHealthz(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/healthz", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}

	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (body=%q)", err, rec.Body.String())
	}
	if got.Status != "ok" {
		t.Errorf("status = %q, want %q", got.Status, "ok")
	}
}

func TestStatsOnEmptyEngine(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/api/v1/stats", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want %d", rec.Code, http.StatusOK)
	}

	var got statsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (body=%q)", err, rec.Body.String())
	}
	if got.Docs != 0 || got.Terms != 0 || got.IndexBytes != 0 {
		t.Errorf("空引擎统计应全为 0，实际 %+v", got)
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/nope", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// Go 1.22 起 ServeMux 会对「路径已注册但方法不匹配」自动返回 405。
func TestWrongMethodReturns405(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodPost, "/healthz", nil)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态码 = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestNilEngineFallsBackToEmpty(t *testing.T) {
	srv := New(config.Default(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if rec := do(t, srv, http.MethodGet, "/api/v1/stats", nil); rec.Code != http.StatusOK {
		t.Fatalf("传入 nil 引擎时不应 panic，状态码 = %d", rec.Code)
	}
}

func TestRecoverMiddlewareReturns500(t *testing.T) {
	srv := New(
		config.Default(),
		tsh.New(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	panicking := srv.withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	panicking.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 应被兜住并返回 500，实际 %d", rec.Code)
	}

	var got errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v", err)
	}
	if got.Error.Code != CodeInternal {
		t.Errorf("error.code = %q, want %q", got.Error.Code, CodeInternal)
	}
}

func TestChainOrder(t *testing.T) {
	var order []string
	tag := func(name string) middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	h := chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "handler") }),
		tag("first"), tag("second"), tag("third"),
	)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := []string{"first", "second", "third", "handler"}
	if len(order) != len(want) {
		t.Fatalf("执行顺序 = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("执行顺序 = %v, want %v", order, want)
		}
	}
}
