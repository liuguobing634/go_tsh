package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// newQuietServer 构造一个不出声的服务器，用于嵌入方视角的测试。
func newQuietServer(t *testing.T, engine *tsh.Engine) *Server {
	t.Helper()
	return New(DefaultOptions(), engine, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// **这是「嵌入方把 HTTP 挂进自己进程」的验收测试。**
//
// 外部 module 的编译门禁（apicheck/）只能证明「类型和方法够用」；
// 真正挂上去能不能工作，必须有功能测试。
func TestRegisterOnCallersOwnMux(t *testing.T) {
	engine := tsh.New()
	defer engine.Close()

	api := newQuietServer(t, engine)

	// 嵌入方自己的路由体系：既有自己的路由，也要挂上搜索接口。
	mux := http.NewServeMux()
	api.Register(mux)
	mux.HandleFunc("GET /my/custom", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"mine":true}`))
	})

	handler := api.Wrap(mux)

	// ---- 1. 本包的路由在 ----
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", rec.Code)
	}

	// ---- 2. 调用方自己的路由也还在 ----
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/my/custom", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /my/custom = %d, want 200（自己的路由被覆盖了？）", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "mine") {
		t.Errorf("自己的路由返回了别人的内容: %s", rec.Body.String())
	}

	// ---- 3. 端到端：写入 + 检索 ----
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/documents/d1",
		strings.NewReader(`{"fields":{"body":"hello embedded world"}}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("写入失败: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=body:embedded", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("检索失败: %d %s", rec.Code, rec.Body.String())
	}

	var res searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].ID != "d1" {
		t.Errorf("命中 = %+v, want d1", res.Hits)
	}
}

// 只用 Register 时不该套上本包的中间件——调用方自己决定。
//
// 这条边界要明确：否则「我只想注册路由」的人会莫名其妙多出一层访问日志。
func TestRegisterAloneDoesNotAddMiddleware(t *testing.T) {
	engine := tsh.New()
	defer engine.Close()

	api := newQuietServer(t, engine)

	mux := http.NewServeMux()
	api.Register(mux)

	// 请求体上限没有被套上：这样超大的请求体不会在 Register 这一层被拦，
	// 而是由 handler 自己在读取时才失败。
	//
	// 这里只验证「路径能被路由到」，确认 Register 是可用的最小单元。
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("裸 mux 上的 /healthz = %d, want 200", rec.Code)
	}
}

// New 返回的 Server 自带套好中间件的 Handler，可以直接 ListenAndServe。
func TestNewServerHandlerIsWrapped(t *testing.T) {
	engine := tsh.New()
	defer engine.Close()

	api := newQuietServer(t, engine)

	if api.Handler == nil {
		t.Fatal("Handler 不该为空")
	}

	// 请求体上限生效：这是中间件确实套上了的证据。
	api.opts.MaxBodyBytes = 16

	rec := httptest.NewRecorder()
	api.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/documents/d1",
		strings.NewReader(`{"fields":{"body":"`+strings.Repeat("x", 200)+`"}}`)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("超限请求体应当 413，实际 %d（中间件没套上？）", rec.Code)
	}
}

func TestOptionsValidate(t *testing.T) {
	if err := DefaultOptions().Validate(); err != nil {
		t.Fatalf("默认配置必须合法: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"空 Addr", func(o *Options) { o.Addr = "" }},
		{"空白 Addr", func(o *Options) { o.Addr = "   " }},
		{"MaxBodyBytes 为零", func(o *Options) { o.MaxBodyBytes = 0 }},
		{"MaxBodyBytes 为负", func(o *Options) { o.MaxBodyBytes = -1 }},
		{"ReadTimeout 为零", func(o *Options) { o.ReadTimeout = 0 }},
		{"WriteTimeout 为负", func(o *Options) { o.WriteTimeout = -1 }},
		{"IdleTimeout 为零", func(o *Options) { o.IdleTimeout = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := DefaultOptions()
			tc.mutate(&o)
			if err := o.Validate(); err == nil {
				t.Error("应当报错")
			}
		})
	}
}

// 引擎与配置要能取回去，方便调用方在同一个进程里继续用。
func TestServerExposesEngineAndOptions(t *testing.T) {
	engine := tsh.New()
	defer engine.Close()

	api := newQuietServer(t, engine)
	if api.Engine() != engine {
		t.Error("Engine() 应当返回构造时传入的引擎")
	}
	if api.Options().Addr != DefaultOptions().Addr {
		t.Error("Options() 应当返回构造时的配置")
	}
}
