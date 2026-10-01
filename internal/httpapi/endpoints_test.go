package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// doJSON 发起一次带 JSON 请求体的请求。
func doJSON(t *testing.T, srv *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	return do(t, srv, method, target, reader)
}

// doRaw 发起一次带原始字符串请求体的请求，用于构造畸形 Body。
func doRaw(t *testing.T, srv *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, srv, method, target, strings.NewReader(body))
}

// decodeInto 把响应体解析成 T；失败即终止测试。
func decodeInto[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (body=%q)", err, rec.Body.String())
	}
	return v
}

// assertStatus 校验状态码，失败时带上响应体便于排查。
func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("状态码 = %d, want %d; body = %s", rec.Code, want, rec.Body.String())
	}
}

// search 发起一次检索并要求 200。
func search(t *testing.T, srv *Server, target string) searchResponse {
	t.Helper()

	rec := do(t, srv, http.MethodGet, target, nil)
	assertStatus(t, rec, http.StatusOK)
	return decodeInto[searchResponse](t, rec)
}

// hitIDs 提取命中结果的 ID 列表。
func hitIDs(res searchResponse) []string {
	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.ID)
	}
	return out
}

// newServerWithConfig 用指定配置构造测试服务器。
func newServerWithConfig(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	return New(cfg, tsh.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// seedDocs 写入一批固定语料。
func seedDocs(t *testing.T, srv *Server) {
	t.Helper()

	docs := []documentRequest{
		{ID: "doc-1", Fields: map[string]any{
			"title": "Inverted index",
			"body":  "a searchable inverted index written in Go",
		}},
		{ID: "doc-2", Fields: map[string]any{
			"title": "Search engines",
			"body":  "an engine relies on an inverted index",
		}},
		{ID: "doc-3", Fields: map[string]any{
			"title": "Baking bread",
			"body":  "knead the dough then bake it",
		}},
	}

	for _, d := range docs {
		assertStatus(t, doJSON(t, srv, http.MethodPost, "/api/v1/documents", d), http.StatusCreated)
	}
}

// ---------------------------------------------------------------- 文档接口

func TestCreateDocument(t *testing.T) {
	srv := newTestServer(t)

	rec := doJSON(t, srv, http.MethodPost, "/api/v1/documents", documentRequest{
		ID:     "doc-1",
		Fields: map[string]any{"title": "Hello World"},
	})
	assertStatus(t, rec, http.StatusCreated)

	got := decodeInto[documentResponse](t, rec)
	if got.ID != "doc-1" || got.Fields["title"] != "Hello World" {
		t.Fatalf("响应 = %+v", got)
	}

	// 写入后立即可读。
	rec = do(t, srv, http.MethodGet, "/api/v1/documents/doc-1", nil)
	assertStatus(t, rec, http.StatusOK)
	if read := decodeInto[documentResponse](t, rec); read.ID != "doc-1" {
		t.Fatalf("读回的文档 = %+v", read)
	}
}

// 重复新建必须 409，而不是静默覆盖。
func TestCreateDuplicateConflicts(t *testing.T) {
	srv := newTestServer(t)
	seedDocs(t, srv)

	rec := doJSON(t, srv, http.MethodPost, "/api/v1/documents", documentRequest{
		ID:     "doc-1",
		Fields: map[string]any{"title": "Overwrite attempt"},
	})
	assertStatus(t, rec, http.StatusConflict)

	body := decodeInto[errorResponse](t, rec)
	if body.Error.Code != CodeConflict {
		t.Errorf("错误码 = %q, want %q", body.Error.Code, CodeConflict)
	}

	// 原文档不能被改动。
	rec = do(t, srv, http.MethodGet, "/api/v1/documents/doc-1", nil)
	if got := decodeInto[documentResponse](t, rec); got.Fields["title"] != "Inverted index" {
		t.Errorf("冲突的写入污染了原文档: %+v", got)
	}
}

func TestCreateRejectsBadRequests(t *testing.T) {
	srv := newTestServer(t)

	cases := []struct {
		name string
		body string
	}{
		{"非法 JSON", `{"id": "x", `},
		{"缺少 id", `{"fields": {"body": "x"}}`},
		{"id 为空白", `{"id": "   ", "fields": {"body": "x"}}`},
		{"没有字段", `{"id": "x", "fields": {}}`},
		{"未知字段（拼错的键）", `{"id": "x", "field": {"body": "y"}}`},
		{"两个 JSON 对象", `{"id":"x","fields":{"b":"y"}}{"id":"z"}`},
		{"空请求体", ``},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRaw(t, srv, http.MethodPost, "/api/v1/documents", tc.body)

			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("状态码 = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPutDocumentCreatesThenOverwrites(t *testing.T) {
	srv := newTestServer(t)

	// 首次 PUT 是新建 → 201
	rec := doJSON(t, srv, http.MethodPut, "/api/v1/documents/doc-1", documentRequest{
		Fields: map[string]any{"body": "alpha beta"},
	})
	assertStatus(t, rec, http.StatusCreated)

	// 再次 PUT 是覆盖 → 200
	rec = doJSON(t, srv, http.MethodPut, "/api/v1/documents/doc-1", documentRequest{
		Fields: map[string]any{"body": "gamma"},
	})
	assertStatus(t, rec, http.StatusOK)

	// 覆盖是整体替换：旧词条必须彻底消失。
	if got := search(t, srv, "/api/v1/search?q=alpha"); got.Total != 0 {
		t.Errorf("覆盖后旧词条仍可检索到: %+v", got)
	}
	if got := search(t, srv, "/api/v1/search?q=gamma"); got.Total != 1 {
		t.Errorf("覆盖后新词条应可检索: %+v", got)
	}
}

func TestPutRejectsMismatchedID(t *testing.T) {
	srv := newTestServer(t)

	rec := doJSON(t, srv, http.MethodPut, "/api/v1/documents/path-id", documentRequest{
		ID:     "body-id",
		Fields: map[string]any{"body": "x"},
	})
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestGetAndDeleteDocument(t *testing.T) {
	srv := newTestServer(t)
	seedDocs(t, srv)

	rec := do(t, srv, http.MethodDelete, "/api/v1/documents/doc-2", nil)
	assertStatus(t, rec, http.StatusNoContent)
	if rec.Body.Len() != 0 {
		t.Errorf("204 不应有响应体，实际 %q", rec.Body.String())
	}

	rec = do(t, srv, http.MethodGet, "/api/v1/documents/doc-2", nil)
	assertStatus(t, rec, http.StatusNotFound)
	if got := decodeInto[errorResponse](t, rec); got.Error.Code != CodeNotFound {
		t.Errorf("错误码 = %q, want %q", got.Error.Code, CodeNotFound)
	}

	// 删除不存在的文档 → 404
	rec = do(t, srv, http.MethodDelete, "/api/v1/documents/doc-2", nil)
	assertStatus(t, rec, http.StatusNotFound)

	// 其余文档不受影响
	rec = do(t, srv, http.MethodGet, "/api/v1/documents/doc-1", nil)
	assertStatus(t, rec, http.StatusOK)
}

// ---------------------------------------------------------------- 检索接口

func TestSearchEndpoint(t *testing.T) {
	srv := newTestServer(t)
	seedDocs(t, srv)

	res := search(t, srv, "/api/v1/search?q=inverted")
	if res.Total != 2 {
		t.Errorf("Total = %d, want 2", res.Total)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("Hits 长度 = %d, want 2", len(res.Hits))
	}
	for _, h := range res.Hits {
		if h.ID == "" {
			t.Error("命中项缺少 id")
		}
		if h.Score <= 0 {
			t.Errorf("命中项分数应大于 0: %+v", h)
		}
		if h.Fields["title"] == "" {
			t.Errorf("默认应回显文档字段: %+v", h)
		}
	}
	if res.TookMS < 0 {
		t.Errorf("took_ms 不应为负: %v", res.TookMS)
	}
}

func TestSearchFieldRestriction(t *testing.T) {
	srv := newTestServer(t)
	seedDocs(t, srv)

	// "searchable" 只在 doc-1 的 body 里。
	if got := search(t, srv, "/api/v1/search?q=searchable&field=title"); got.Total != 0 {
		t.Errorf("限定 title 时不应命中: %+v", got)
	}
	if got := search(t, srv, "/api/v1/search?q=searchable&field=body"); got.Total != 1 {
		t.Errorf("限定 body 时应命中 1 篇: %+v", got)
	}
	if got := search(t, srv, "/api/v1/search?q=searchable"); got.Total != 1 {
		t.Errorf("不限定字段时应命中 1 篇: %+v", got)
	}

	// 重复的 field 参数都要生效。
	got := search(t, srv, "/api/v1/search?q=inverted&field=title&field=body")
	if got.Total != 2 {
		t.Errorf("多字段限定: Total = %d, want 2", got.Total)
	}
}

func TestSearchHighlight(t *testing.T) {
	srv := newTestServer(t)
	seedDocs(t, srv)

	res := search(t, srv, "/api/v1/search?q=inverted&highlight=true")
	if len(res.Hits) == 0 {
		t.Fatal("应当有命中")
	}

	found := false
	for _, h := range res.Hits {
		if len(h.Highlights) == 0 {
			continue
		}
		found = true
		for field, snippet := range h.Highlights {
			// 高亮保留原文大小写（title 里是 "Inverted"），
			// 因此比较时要忽略大小写。
			if !strings.Contains(strings.ToLower(snippet), "<em>inverted</em>") {
				t.Errorf("字段 %s 的高亮片段未标注命中词: %s", field, snippet)
			}
		}
	}
	if !found {
		t.Error("highlight=true 时应当返回高亮片段")
	}

	// 不开启时不应有 highlights 字段。
	res = search(t, srv, "/api/v1/search?q=inverted")
	for _, h := range res.Hits {
		if len(h.Highlights) != 0 {
			t.Errorf("未开启 highlight 却返回了片段: %+v", h.Highlights)
		}
	}
}

// 被排除的词条不应出现在高亮里。
func TestSearchHighlightSkipsNegatedTerms(t *testing.T) {
	srv := newTestServer(t)
	seedDocs(t, srv)

	res := search(t, srv, "/api/v1/search?q=inverted+-engine&highlight=true")
	if res.Total != 1 {
		t.Fatalf("Total = %d, want 1", res.Total)
	}
	for field, snippet := range res.Hits[0].Highlights {
		if strings.Contains(snippet, "<em>engine</em>") {
			t.Errorf("被排除的词不该高亮（字段 %s）: %s", field, snippet)
		}
	}
}

func TestSearchPagination(t *testing.T) {
	srv := newTestServer(t)
	seedDocs(t, srv)

	all := search(t, srv, "/api/v1/search?q=inverted")
	if all.Total != 2 {
		t.Fatalf("Total = %d, want 2", all.Total)
	}

	page1 := search(t, srv, "/api/v1/search?q=inverted&limit=1")
	if page1.Total != 2 || len(page1.Hits) != 1 {
		t.Fatalf("第一页 = %+v", page1)
	}

	page2 := search(t, srv, "/api/v1/search?q=inverted&limit=1&offset=1")
	if page2.Total != 2 || len(page2.Hits) != 1 {
		t.Fatalf("第二页 = %+v", page2)
	}

	if page1.Hits[0].ID == page2.Hits[0].ID {
		t.Error("分页出现重复项")
	}

	beyond := search(t, srv, "/api/v1/search?q=inverted&limit=1&offset=99")
	if beyond.Total != 2 || len(beyond.Hits) != 0 {
		t.Errorf("越界页应返回空 hits 但保留 total: %+v", beyond)
	}
}

func TestSearchRejectsBadRequests(t *testing.T) {
	srv := newTestServer(t)
	seedDocs(t, srv)

	cases := []struct {
		name     string
		target   string
		wantCode string
	}{
		{"缺少 q", "/api/v1/search", CodeInvalidQuery},
		{"q 为空白", "/api/v1/search?q=%20%20", CodeInvalidQuery},
		{"limit 非整数", "/api/v1/search?q=index&limit=abc", CodeBadRequest},
		{"offset 非整数", "/api/v1/search?q=index&offset=x", CodeBadRequest},
		{"highlight 取值非法", "/api/v1/search?q=index&highlight=yes", CodeBadRequest},
		{"引号未闭合", "/api/v1/search?q=%22index", CodeInvalidQuery},
		{"缺少右括号", "/api/v1/search?q=(a%20OR%20b", CodeInvalidQuery},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, srv, http.MethodGet, tc.target, nil)
			assertStatus(t, rec, http.StatusBadRequest)

			if got := decodeInto[errorResponse](t, rec); got.Error.Code != tc.wantCode {
				t.Errorf("错误码 = %q, want %q", got.Error.Code, tc.wantCode)
			}
		})
	}
}

func TestSearchEmptyIndex(t *testing.T) {
	srv := newTestServer(t)

	res := search(t, srv, "/api/v1/search?q=anything")
	if res.Total != 0 {
		t.Errorf("空索引 Total = %d, want 0", res.Total)
	}
	// hits 应当是空数组而不是 null，省得客户端还要判空。
	if !strings.Contains(strings.ReplaceAll(string(mustJSON(t, res)), " ", ""), `"hits":[]`) {
		t.Errorf("空结果应序列化成空数组: %s", mustJSON(t, res))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ---------------------------------------------------------------- 请求体上限

func TestRequestBodyTooLarge(t *testing.T) {
	cfg := config.Default()
	cfg.MaxBodyBytes = 64
	srv := newServerWithConfig(t, cfg)

	// 构造一个超过 64 字节的合法 JSON。
	big := strings.Repeat("x", 200)
	rec := doRaw(t, srv, http.MethodPost, "/api/v1/documents",
		`{"id":"big","fields":{"body":"`+big+`"}}`)

	assertStatus(t, rec, http.StatusRequestEntityTooLarge)
	if got := decodeInto[errorResponse](t, rec); got.Error.Code != CodePayloadTooLarge {
		t.Errorf("错误码 = %q, want %q", got.Error.Code, CodePayloadTooLarge)
	}
}

// ---------------------------------------------------------------- 端到端

// 走完「写入 → 检索 → 覆盖 → 检索 → 删除 → 检索」的完整链路。
func TestFullLifecycle(t *testing.T) {
	srv := newTestServer(t)

	step := func(name string, rec *httptest.ResponseRecorder, want int) {
		t.Helper()
		if rec.Code != want {
			t.Fatalf("[%s] 状态码 = %d, want %d; body = %s", name, rec.Code, want, rec.Body.String())
		}
	}

	// 1. 写入
	step("create", doJSON(t, srv, http.MethodPost, "/api/v1/documents", documentRequest{
		ID: "article",
		Fields: map[string]any{
			"title": "Full text search",
			"body":  "an inverted index powers full text search",
		},
	}), http.StatusCreated)

	// 2. 检索命中
	if got := search(t, srv, "/api/v1/search?q=inverted&highlight=true"); got.Total != 1 {
		t.Fatalf("写入后应能检索到, Total = %d", got.Total)
	}

	// 3. 统计反映写入
	stats := decodeInto[statsResponse](t, do(t, srv, http.MethodGet, "/api/v1/stats", nil))
	if stats.Docs != 1 {
		t.Errorf("Stats.Docs = %d, want 1", stats.Docs)
	}
	if stats.Fields != 2 {
		t.Errorf("Stats.Fields = %d, want 2", stats.Fields)
	}

	// 4. 覆盖：旧词条消失
	step("update", doJSON(t, srv, http.MethodPut, "/api/v1/documents/article", documentRequest{
		Fields: map[string]any{"body": "completely different content now"},
	}), http.StatusOK)

	if got := search(t, srv, "/api/v1/search?q=inverted"); got.Total != 0 {
		t.Errorf("覆盖后旧词条应消失, Total = %d", got.Total)
	}
	if got := search(t, srv, "/api/v1/search?q=different"); got.Total != 1 {
		t.Errorf("覆盖后新词条应命中, Total = %d", got.Total)
	}

	// 5. 删除
	step("delete", do(t, srv, http.MethodDelete, "/api/v1/documents/article", nil), http.StatusNoContent)
	if got := search(t, srv, "/api/v1/search?q=different"); got.Total != 0 {
		t.Errorf("删除后不应再命中, Total = %d", got.Total)
	}

	// 6. 统计回到零
	stats = decodeInto[statsResponse](t, do(t, srv, http.MethodGet, "/api/v1/stats", nil))
	if stats.Docs != 0 || stats.Terms != 0 {
		t.Errorf("删除后统计应归零: %+v", stats)
	}

	// 7. 同一个 ID 可以重新写入
	step("recreate", doJSON(t, srv, http.MethodPost, "/api/v1/documents", documentRequest{
		ID:     "article",
		Fields: map[string]any{"body": "fresh content"},
	}), http.StatusCreated)
}

// 并发写入与检索不应产生数据竞争（配合 -race 生效）。
func TestConcurrentHTTPTraffic(t *testing.T) {
	srv := newTestServer(t)
	seedDocs(t, srv)

	done := make(chan struct{})
	errs := make(chan string, 64)

	// 写者
	for w := 0; w < 3; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 30; i++ {
				body := map[string]any{
					"fields": map[string]any{"body": "concurrent traffic test"},
				}
				raw, _ := json.Marshal(body)
				target := "/api/v1/documents/w" + string(rune('0'+w)) + "-" + string(rune('0'+i%10))

				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(raw))
				srv.Handler.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
					errs <- "写入状态码 " + string(rune('0'+rec.Code/100)) + "xx"
					return
				}
			}
		}(w)
	}

	// 读者
	for r := 0; r < 3; r++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 30; i++ {
				rec := httptest.NewRecorder()
				srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=inverted", nil))
				if rec.Code != http.StatusOK {
					errs <- "检索状态码异常"
					return
				}
			}
		}()
	}

	for i := 0; i < 6; i++ {
		<-done
	}
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}
