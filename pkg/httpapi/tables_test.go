package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liuguobing/go_tsh/internal/tablename"
)

// putJSON 发一个 PUT 请求，body 为原始 JSON 字符串。
func putJSON(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doRaw(t, srv, http.MethodPut, path, body)
}

// createTableViaHTTP 建一张表并断言成功。
func createTableViaHTTP(t *testing.T, srv *Server, name, schemaJSON string) {
	t.Helper()

	body := "{}"
	if schemaJSON != "" {
		body = `{"schema":` + schemaJSON + `}`
	}
	rec := putJSON(t, srv, "/api/v1/tables/"+name, body)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("建表 %q 失败: %d %s", name, rec.Code, rec.Body.String())
	}
}

// 表管理的完整生命周期。
func TestTableManagementOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	// 初始只有默认表
	rec := doJSON(t, srv, http.MethodGet, "/api/v1/tables", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列出表失败: %d", rec.Code)
	}
	var list tableListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tables) != 1 || list.Tables[0].Name != tablename.Default {
		t.Fatalf("初始表列表 = %+v, want 只有默认表", list.Tables)
	}

	// 建表带 schema
	createTableViaHTTP(t, srv, "products", `{"price":"number","created":"date"}`)

	// 详情里 schema 必须回来
	rec = doJSON(t, srv, http.MethodGet, "/api/v1/tables/products", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("取表详情失败: %d %s", rec.Code, rec.Body.String())
	}
	var info tableInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Schema["price"] != "number" || info.Schema["created"] != "date" {
		t.Errorf("schema = %v", info.Schema)
	}

	// 幂等：重复建表返回 200 而不是 409
	rec = putJSON(t, srv, "/api/v1/tables/products", `{"schema":{"price":"number"}}`)
	if rec.Code != http.StatusOK {
		t.Errorf("重复建表应当是 200（幂等），实际 %d: %s", rec.Code, rec.Body.String())
	}

	// schema 冲突要报错
	rec = putJSON(t, srv, "/api/v1/tables/products", `{"schema":{"price":"keyword"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("schema 冲突应当 400，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 删表
	rec = doJSON(t, srv, http.MethodDelete, "/api/v1/tables/products", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("删表失败: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, srv, http.MethodGet, "/api/v1/tables/products", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("删除后应当 404，实际 %d", rec.Code)
	}
}

// 表级路由与旧的扁平路由操作的是**不同的表**，数据不能串。
func TestTableScopedRoutes(t *testing.T) {
	srv := newTestServer(t)
	createTableViaHTTP(t, srv, "products", `{"name":"text"}`)

	// 往 products 表写
	rec := putJSON(t, srv, "/api/v1/tables/products/documents/p1",
		`{"fields":{"name":"笔记本电脑"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("表级写入失败: %d %s", rec.Code, rec.Body.String())
	}

	// 往默认表写
	rec = putJSON(t, srv, "/api/v1/documents/d1", `{"fields":{"name":"默认表的文档"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("默认表写入失败: %d %s", rec.Code, rec.Body.String())
	}

	// 各自搜各自的
	if got := searchIDsViaHTTP(t, srv, "name:笔记本"); len(got) != 0 {
		t.Errorf("默认表不该搜到 products 的内容，实际 %v", got)
	}
	if got := tableSearchIDs(t, srv, "products", "name:笔记本"); len(got) != 1 || got[0] != "p1" {
		t.Errorf("products 表应当搜到 p1，实际 %v", got)
	}

	// 同名 ID 在两张表里互不覆盖
	rec = putJSON(t, srv, "/api/v1/tables/products/documents/d1",
		`{"fields":{"name":"products 里的 d1"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("写同名 ID 失败: %d %s", rec.Code, rec.Body.String())
	}
	if got := tableSearchIDs(t, srv, "products", "name:products"); len(got) != 1 {
		t.Errorf("products 表里的 d1 应当是新写的，实际 %v", got)
	}
	if got := searchIDsViaHTTP(t, srv, "name:默认表"); len(got) != 1 || got[0] != "d1" {
		t.Errorf("默认表的 d1 不该被覆盖，实际 %v", got)
	}
}

// 表级 schema 让同名不同类型字段并存——整个选型的立足点。
func TestTableSchemasAreIndependentOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	createTableViaHTTP(t, srv, "products", `{"price":"number"}`)
	createTableViaHTTP(t, srv, "articles", `{"price":"keyword"}`)

	rec := putJSON(t, srv, "/api/v1/tables/products/documents/p1", `{"fields":{"price":4999}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("products 写入失败: %d %s", rec.Code, rec.Body.String())
	}
	rec = putJSON(t, srv, "/api/v1/tables/articles/documents/a1", `{"fields":{"price":"免费"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("articles 写入失败: %d %s", rec.Code, rec.Body.String())
	}

	// 商品表按范围查
	if got := tableSearchIDs(t, srv, "products", "price:[0 TO 6000]"); len(got) != 1 || got[0] != "p1" {
		t.Errorf("products 范围查询 = %v, want [p1]", got)
	}
	// 资讯表按精确匹配查
	if got := tableSearchIDs(t, srv, "articles", "price:免费"); len(got) != 1 || got[0] != "a1" {
		t.Errorf("articles 精确匹配 = %v, want [a1]", got)
	}
}

// 建表时声明的日期类型，写入时字符串应当被按日期解析。
func TestTableSchemaAffectsIngestionOverHTTP(t *testing.T) {
	srv := newTestServer(t)
	createTableViaHTTP(t, srv, "events", `{"at":"date","tag":"keyword"}`)

	rec := putJSON(t, srv, "/api/v1/tables/events/documents/e1",
		`{"fields":{"at":"2024-03-15T00:00:00Z","tag":"A-1"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("写入失败: %d %s", rec.Code, rec.Body.String())
	}

	if got := tableSearchIDs(t, srv, "events", "at:[2024-01-01 TO 2024-06-30]"); len(got) != 1 {
		t.Errorf("日期范围查询 = %v, want 命中 e1", got)
	}
	if got := tableSearchIDs(t, srv, "events", "at:[2024-07-01 TO 2024-12-31]"); len(got) != 0 {
		t.Errorf("不该命中，实际 %v", got)
	}
	// 非法日期必须被拒绝，而不是静默存成文本
	rec = putJSON(t, srv, "/api/v1/tables/events/documents/e2", `{"fields":{"at":"不是日期"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法日期应当 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_ = strings.TrimSpace
}

// 表不存在给 404，且**不会**被顺手创建出来。
func TestUnknownTableOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	rec := doJSON(t, srv, http.MethodGet, "/api/v1/tables/nope/search?q=body:x", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("对不存在的表检索应当 404，实际 %d: %s", rec.Code, rec.Body.String())
	}

	rec = putJSON(t, srv, "/api/v1/tables/nope/documents/d1", `{"fields":{"body":"hello"}}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("往不存在的表写入应当 404，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 确认没被创建
	rec = doJSON(t, srv, http.MethodGet, "/api/v1/tables", nil)
	var list tableListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	for _, tb := range list.Tables {
		if tb.Name == "nope" {
			t.Errorf("不存在表不该被写入创建出来：%+v", list.Tables)
		}
	}
}

// 非法表名一律 400（含路径穿越）。
func TestInvalidTableNameOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	for _, name := range []string{"BadName", "has%20space", "a%2Fb"} {
		rec := putJSON(t, srv, "/api/v1/tables/"+name, "{}")
		if rec.Code == http.StatusCreated || rec.Code == http.StatusOK {
			t.Errorf("非法表名 %q 不该建表成功: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// 默认表不能删——靠 Engine 直接调用的那些接口都指着它。
func TestDefaultTableCannotBeDroppedOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	rec := doJSON(t, srv, http.MethodDelete, "/api/v1/tables/"+tablename.Default, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("删默认表应当 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// 统计里要有每张表的分项。
func TestStatsIncludesTables(t *testing.T) {
	srv := newTestServer(t)
	createTableViaHTTP(t, srv, "products", "")

	rec := doJSON(t, srv, http.MethodGet, "/api/v1/stats", nil)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}

	var st statsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Tables) != 2 {
		t.Fatalf("统计里应当有 2 张表，实际 %+v", st.Tables)
	}
}

// tableSearchIDs 在指定表上检索。
func tableSearchIDs(t *testing.T, srv *Server, table, query string) []string {
	t.Helper()

	rec := doJSON(t, srv, http.MethodGet,
		"/api/v1/tables/"+table+"/search?q="+urlEscape(query)+"&limit=50", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("表 %q 检索 %q 失败: %d %s", table, query, rec.Code, rec.Body.String())
	}

	var out struct {
		Hits []struct {
			ID string `json:"id"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	ids := make([]string, 0, len(out.Hits))
	for _, h := range out.Hits {
		ids = append(ids, h.ID)
	}
	return ids
}
