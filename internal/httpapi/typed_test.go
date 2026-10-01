package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/pkg/tsh"
	"io"
	"log/slog"
	"testing"
)

// newTypedServer 构造一个预声明了字段类型的测试服务器。
//
// 日期与关键字**只能靠预声明**：JSON 里没有日期类型，
// `"2024-01-15"` 只是一个字符串，光看值无法与普通文本区分。
func newTypedServer(t *testing.T, schema map[string]tsh.FieldKind) *Server {
	t.Helper()

	engine, err := tsh.NewWith(tsh.Options{Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	return New(config.Default(), engine, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// putDoc 写入一篇文档，返回响应记录器。
func putDoc(t *testing.T, srv *Server, id string, fields map[string]any) map[string]any {
	t.Helper()

	rec := doJSON(t, srv, http.MethodPut, "/api/v1/documents/"+id, documentRequest{
		Fields: fields,
	})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("写入 %s 失败: %d %s", id, rec.Code, rec.Body.String())
	}

	var out documentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败: %v (%s)", err, rec.Body.String())
	}
	return out.Fields
}

// searchIDsViaHTTP 执行一次检索，返回命中的文档 ID。
func searchIDsViaHTTP(t *testing.T, srv *Server, query string) []string {
	t.Helper()

	rec := doJSON(t, srv, http.MethodGet, "/api/v1/search?q="+urlEscape(query)+"&limit=50", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("检索 %q 失败: %d %s", query, rec.Code, rec.Body.String())
	}

	var out struct {
		Hits []struct {
			ID string `json:"id"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析检索响应失败: %v", err)
	}

	ids := make([]string, 0, len(out.Hits))
	for _, h := range out.Hits {
		ids = append(ids, h.ID)
	}
	return ids
}

func urlEscape(s string) string {
	// 查询串里会出现 [ ] { } : 与空格，必须转义
	r := strings.NewReplacer(
		" ", "%20",
		"[", "%5B", "]", "%5D",
		"{", "%7B", "}", "%7D",
		":", "%3A",
		"*", "%2A",
		"\"", "%22",
	)
	return r.Replace(s)
}

// JSON 原生类型必须被正确分流，而且**回显时保持原类型**。
func TestTypedFieldsOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	got := putDoc(t, srv, "p1", map[string]any{
		"title":  "笔记本电脑",
		"sku":    "LAP-1",
		"price":  4999,
		"stock":  12,
		"onSale": true,
	})

	// 数字必须回显成 JSON 数字，而不是字符串 "4999"
	if v, ok := got["price"].(float64); !ok || v != 4999 {
		t.Errorf("price 回显 = %#v, want 数字 4999", got["price"])
	}
	if v, ok := got["stock"].(float64); !ok || v != 12 {
		t.Errorf("stock 回显 = %#v, want 数字 12", got["stock"])
	}
	// 布尔按关键字存放，回显为字符串 "true"
	if v, ok := got["onSale"].(string); !ok || v != "true" {
		t.Errorf("onSale 回显 = %#v, want 字符串 true", got["onSale"])
	}
	// 文本原样
	if v, ok := got["title"].(string); !ok || v != "笔记本电脑" {
		t.Errorf("title 回显 = %#v", got["title"])
	}
}

// 数值字段的等值与范围查询必须能从 HTTP 走通。
func TestNumericRangeOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	putDoc(t, srv, "cheap", map[string]any{"title": "mouse", "price": 50})
	putDoc(t, srv, "mid", map[string]any{"title": "keyboard", "price": 500})
	putDoc(t, srv, "high", map[string]any{"title": "laptop", "price": 5000})

	cases := []struct {
		query string
		want  []string
	}{
		{"price:[100 TO 1000]", []string{"mid"}},
		{"price:{50 TO 5000}", []string{"mid"}},
		{"price:[* TO 100]", []string{"cheap"}},
		{"price:[1000 TO *]", []string{"high"}},
		{"price:500", []string{"mid"}},
		{"price:[1 TO 2]", nil},
		{"title:laptop AND price:[1000 TO 9000]", []string{"high"}},
		// 范围命中恒为 0 分，而 title:mouse 命中得 BM25 正分，
		// 因此排序是「正分的 cheap 在前、0 分的 high 在后」。
		{"title:mouse OR price:[4000 TO 9000]", []string{"cheap", "high"}},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got := searchIDsViaHTTP(t, srv, tc.query)
			if len(got) != len(tc.want) {
				t.Fatalf("命中 %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("命中 %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestDateRangeOverHTTP(t *testing.T) {
	// 必须预声明，否则 "2024-01-15T00:00:00Z" 只是个普通字符串
	srv := newTypedServer(t, map[string]tsh.FieldKind{
		"created": tsh.FieldDate,
		"sku":     tsh.FieldKeyword,
	})

	got := putDoc(t, srv, "a", map[string]any{
		"title":   "early",
		"created": "2024-01-15T00:00:00Z",
		"sku":     "A-1",
	})
	// 日期必须原样回显（它与写入时是同一个格式）
	if v, ok := got["created"].(string); !ok || !strings.HasPrefix(v, "2024-01-15") {
		t.Errorf("created 回显 = %#v", got["created"])
	}

	putDoc(t, srv, "b", map[string]any{
		"title":   "late",
		"created": "2024-08-15T00:00:00Z",
		"sku":     "B-2",
	})

	cases := []struct {
		query string
		want  []string
	}{
		{"created:[2024-01-01 TO 2024-06-30]", []string{"a"}},
		{"created:[2024-07-01 TO 2024-12-31]", []string{"b"}},
		{"created:[2024-01-01 TO 2024-12-31]", []string{"a", "b"}},
		{"created:[2025-01-01 TO *]", nil},
		// 顺便验证关键字字段的精确匹配
		{"sku:A-1", []string{"a"}},
		{"sku:B-2", []string{"b"}},
		{"sku:A", nil},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got := searchIDsViaHTTP(t, srv, tc.query)
			if len(got) != len(tc.want) {
				t.Fatalf("命中 %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("命中 %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// 没有预声明时，同样的字符串就是普通文本，范围查询必须报错。
// 这条与上一条构成对照：证明预声明**确实起作用**，而不是碰巧能查。
func TestDateWithoutSchemaStaysText(t *testing.T) {
	srv := newTestServer(t)

	putDoc(t, srv, "a", map[string]any{"created": "2024-01-15T00:00:00Z"})

	rec := doJSON(t, srv, http.MethodGet,
		"/api/v1/search?q="+urlEscape("created:[2024-01-01 TO 2024-12-31]"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未声明为 date 的字段做范围查询应当 400，实际 %d：%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "created") {
		t.Errorf("错误信息里应当指明字段名：%s", rec.Body.String())
	}
}

// 声明成 number 的字段收到字符串时，能解析就接受，解析不了就报错，
// **绝不静默降级成文本**——那会让字段类型在运行时悄悄漂移。
func TestSchemaNumberAcceptsNumericString(t *testing.T) {
	srv := newTypedServer(t, map[string]tsh.FieldKind{"price": tsh.FieldNumber})

	got := putDoc(t, srv, "d1", map[string]any{"price": "4999"})
	if v, ok := got["price"].(float64); !ok || v != 4999 {
		t.Errorf("字符串形式的数字应当被接受并回显为数字，实际 %#v", got["price"])
	}

	rec := doJSON(t, srv, http.MethodPut, "/api/v1/documents/d2", documentRequest{
		Fields: map[string]any{"price": "not-a-number"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("解析不了的值应当 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// 带引号的 "4999" 是**文本**，不带引号的 4999 是**数值**。
// 靠值的字面形态猜类型，商品编号 "0755" 会被当成 755，前导零就没了。
func TestQuotedNumberStaysText(t *testing.T) {
	srv := newTestServer(t)

	putDoc(t, srv, "d1", map[string]any{"code": "0755"})
	putDoc(t, srv, "d2", map[string]any{"code": "755"})

	// 文本字段能用词条查到
	if got := searchIDsViaHTTP(t, srv, "code:0755"); len(got) != 1 || got[0] != "d1" {
		t.Errorf("文本字段应当能按原值查到，命中 %v", got)
	}

	// 它不是数值字段，范围查询必须报错而不是静默返回空
	rec := doJSON(t, srv, http.MethodGet, "/api/v1/search?q="+urlEscape("code:[1 TO 9999]"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("对文本字段做范围查询应当 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// 已声明为 number 的字段收到无法解析的值时，必须当场报 400 并指明字段与值。
//
// 这条取代了原先「同字段两种类型」的测法：走 HTTP 路径**产生不了**
// 类型冲突——ParseDocument 会按 schema 分流，字符串遇到 number 字段
// 就走数值解析，永远不会被当成文本。这是更好的用户体验，
// 也意味着「类型冲突」要到 Go API 那一层才测得到。
func TestWrongValueForDeclaredNumberOverHTTP(t *testing.T) {
	srv := newTypedServer(t, map[string]tsh.FieldKind{"amount": tsh.FieldNumber})

	putDoc(t, srv, "d1", map[string]any{"amount": 100})

	rec := doJSON(t, srv, http.MethodPut, "/api/v1/documents/d2", documentRequest{
		Fields: map[string]any{"amount": "not a number"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("应当 400，实际 %d：%s", rec.Code, rec.Body.String())
	}

	var errResp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatal(err)
	}
	t.Logf("报错: %s", errResp.Error.Message)

	for _, want := range []string{"amount", "not a number"} {
		if !strings.Contains(errResp.Error.Message, want) {
			t.Errorf("错误信息里应当包含 %q：%s", want, errResp.Error.Message)
		}
	}
}

// 类型冲突：schema 声明成 date，数据却给了布尔。
//
// 布尔没有别的合理归宿（只能当关键字），于是与 date 冲突。
// 报错里必须带上**文档 ID**：批量导入时这是定位问题的第一手信息。
func TestFieldKindConflictNamesTheDocument(t *testing.T) {
	srv := newTypedServer(t, map[string]tsh.FieldKind{"created": tsh.FieldDate})

	rec := doJSON(t, srv, http.MethodPut, "/api/v1/documents/doc-42", documentRequest{
		Fields: map[string]any{"created": true},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("类型冲突应当 400，实际 %d：%s", rec.Code, rec.Body.String())
	}

	var errResp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatal(err)
	}
	t.Logf("冲突报错: %s", errResp.Error.Message)

	for _, want := range []string{"doc-42", "created", "date"} {
		if !strings.Contains(errResp.Error.Message, want) {
			t.Errorf("错误信息里应当包含 %q：%s", want, errResp.Error.Message)
		}
	}
}

// 超过 2^53 的整数会被 float64 静默改变，必须当场拒绝而不是改掉用户的数据。
func TestLargeIntegerIsRejectedNotSilentlyChanged(t *testing.T) {
	srv := newTestServer(t)

	// 20240101123456789 超出 2^53 ≈ 9.007e15
	body := []byte(`{"fields":{"orderId":20240101123456789}}`)
	rec := doJSON(t, srv, http.MethodPut, "/api/v1/documents/o1", json.RawMessage(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超出精确范围的整数应当 400，实际 %d：%s", rec.Code, rec.Body.String())
	}

	var errResp errorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
	t.Logf("报错: %s", errResp.Error.Message)

	if !strings.Contains(errResp.Error.Message, "20240101123456789") {
		t.Errorf("错误信息里应当带上原值，便于排查：%s", errResp.Error.Message)
	}

	// 2^53 之内的整数必须正常接受
	putDoc(t, srv, "ok", map[string]any{"orderId": 9007199254740992})
}

// 对象与数组暂不支持，必须明确拒绝而不是悄悄取第一个或者转成字符串。
func TestComplexFieldValuesAreRejected(t *testing.T) {
	srv := newTestServer(t)

	for _, tc := range []struct {
		name  string
		value any
	}{
		{"数组", []any{1, 2, 3}},
		{"对象", map[string]any{"a": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(t, srv, http.MethodPut, "/api/v1/documents/x", documentRequest{
				Fields: map[string]any{"f": tc.value},
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应当 400，实际 %d：%s", rec.Code, rec.Body.String())
			}
			t.Logf("%s: %s", tc.name, rec.Body.String())
		})
	}
}

// null 视为「不提供这个字段」，与 ES 一致。
func TestNullFieldIsSkipped(t *testing.T) {
	srv := newTestServer(t)

	got := putDoc(t, srv, "d1", map[string]any{
		"title": "kept",
		"gone":  nil,
	})

	if _, exists := got["gone"]; exists {
		t.Errorf("null 字段不该出现在结果里：%#v", got)
	}
	if got["title"] != "kept" {
		t.Errorf("title = %#v", got["title"])
	}
}

// 全 null 的请求等于没有字段，应当报错而不是写一篇空文档。
func TestAllNullFieldsIsRejected(t *testing.T) {
	srv := newTestServer(t)

	rec := doJSON(t, srv, http.MethodPut, "/api/v1/documents/x", documentRequest{
		Fields: map[string]any{"a": nil},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("全是 null 应当 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// 数值字段上写了无法解析的值，必须报 400 并指明字段。
func TestBadValueForNumericFieldOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	putDoc(t, srv, "d1", map[string]any{"price": 100})

	rec := doJSON(t, srv, http.MethodGet, "/api/v1/search?q="+urlEscape("price:abc"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("应当 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "price") {
		t.Errorf("错误信息里应当指明字段名：%s", rec.Body.String())
	}
}
