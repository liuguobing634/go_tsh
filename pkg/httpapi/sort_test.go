package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// newSortableServer 造一张带数值字段的表，用于排序测试。
func newSortableServer(t *testing.T) *Server {
	t.Helper()

	engine, err := tsh.NewWith(tsh.Options{Schema: map[string]tsh.FieldKind{
		"body":  tsh.FieldText,
		"price": tsh.FieldNumber,
		"sku":   tsh.FieldKeyword,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	srv := New(DefaultOptions(), engine, slog.New(slog.NewTextHandler(io.Discard, nil)))
	createTableViaHTTP(t, srv, "shop", `{"body":"text","price":"number","sku":"keyword"}`)

	for i := range 6 {
		body := `{"fields":{"body":"widget","price":` +
			itoa(i*10) + `,"sku":"S` + itoa(i) + `"}}`
		rec := putJSON(t, srv, "/api/v1/tables/shop/documents/p"+itoa(i), body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("写入失败: %d %s", rec.Code, rec.Body.String())
		}
	}
	return srv
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

// searchSorted 检索并返回命中的 ID。
func searchSorted(t *testing.T, srv *Server, path string) []string {
	t.Helper()

	rec := doJSON(t, srv, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("检索失败: %d %s", rec.Code, rec.Body.String())
	}

	var res searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}

	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.ID)
	}
	return out
}

func TestSortOverHTTP(t *testing.T) {
	srv := newSortableServer(t)

	t.Run("数值降序", func(t *testing.T) {
		got := searchSorted(t, srv, "/api/v1/tables/shop/search?q=body:widget&sort=price:desc&limit=10")
		want := []string{"p5", "p4", "p3", "p2", "p1", "p0"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("= %v, want %v", got, want)
		}
	})

	t.Run("数值升序", func(t *testing.T) {
		got := searchSorted(t, srv, "/api/v1/tables/shop/search?q=body:widget&sort=price:asc&limit=10")
		want := []string{"p0", "p1", "p2", "p3", "p4", "p5"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("= %v, want %v", got, want)
		}
	})

	t.Run("关键字降序", func(t *testing.T) {
		got := searchSorted(t, srv, "/api/v1/tables/shop/search?q=body:widget&sort=sku:desc&limit=10")
		want := []string{"p5", "p4", "p3", "p2", "p1", "p0"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("= %v, want %v", got, want)
		}
	})

	t.Run("省略方向默认升序", func(t *testing.T) {
		got := searchSorted(t, srv, "/api/v1/tables/shop/search?q=body:widget&sort=price&limit=10")
		if got[0] != "p0" {
			t.Errorf("第一条 = %q, want p0", got[0])
		}
	})

	t.Run("不传 sort 时行为不变", func(t *testing.T) {
		rec := doJSON(t, srv, http.MethodGet, "/api/v1/tables/shop/search?q=body:widget&limit=10", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("失败: %d", rec.Code)
		}
		var res searchResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		if len(res.Hits) != 6 {
			t.Errorf("命中 %d 条，want 6", len(res.Hits))
		}
	})
}

// 排序也要能用在旧的扁平路由上（默认表）。
func TestSortOnFlatRoute(t *testing.T) {
	engine, err := tsh.NewWith(tsh.Options{Schema: map[string]tsh.FieldKind{
		"body": tsh.FieldText, "n": tsh.FieldNumber,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	srv := New(DefaultOptions(), engine, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := range 4 {
		putJSON(t, srv, "/api/v1/documents/d"+itoa(i),
			`{"fields":{"body":"hi","n":`+itoa(i*7)+`}}`)
	}

	got := searchSorted(t, srv, "/api/v1/search?q=body:hi&sort=n:desc&limit=10")
	want := []string{"d3", "d2", "d1", "d0"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("= %v, want %v", got, want)
	}
}

// 排序分页：翻页拼起来要与完整结果一致。
func TestSortPaginationOverHTTP(t *testing.T) {
	srv := newSortableServer(t)

	var paged []string
	for offset := 0; offset < 6; offset += 2 {
		got := searchSorted(t, srv,
			"/api/v1/tables/shop/search?q=body:widget&sort=price:desc&limit=2&offset="+itoa(offset))
		paged = append(paged, got...)
	}

	want := []string{"p5", "p4", "p3", "p2", "p1", "p0"}
	if strings.Join(paged, ",") != strings.Join(want, ",") {
		t.Errorf("翻页结果 = %v, want %v", paged, want)
	}
}

// 排序声明的各种错误一律 400，且错误信息要有指导性。
func TestSortErrorsOverHTTP(t *testing.T) {
	srv := newSortableServer(t)

	cases := []struct {
		name  string
		sort  string
		words []string
	}{
		{"字段不存在", "nope:desc", []string{"nope"}},
		{"text 字段不能排", "body:asc", []string{"body", "text"}},
		{"方向无法识别", "price:up", []string{"up", "asc"}},
		{"没有字段名", "%3Adesc", nil},
		{"键太多", "price:asc,sku:asc,body:asc,price:desc", []string{"3"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(t, srv, http.MethodGet,
				"/api/v1/tables/shop/search?q=body:widget&sort="+tc.sort, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应当 400，实际 %d：%s", rec.Code, rec.Body.String())
			}
			msg := rec.Body.String()
			for _, w := range tc.words {
				if !strings.Contains(msg, w) {
					t.Errorf("错误信息里应当提到 %q：%s", w, msg)
				}
			}
			t.Logf("%s -> %s", tc.sort, msg)
		})
	}
}

// 排序字段写错时，**没有命中也要报错**——不能静默返回空结果。
func TestSortErrorWithoutHitsOverHTTP(t *testing.T) {
	srv := newSortableServer(t)

	rec := doJSON(t, srv, http.MethodGet,
		"/api/v1/tables/shop/search?q=body:nothingmatches&sort=nope:desc", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("没有命中时也应当 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}
