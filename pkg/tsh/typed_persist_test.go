package tsh

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/tablename"
	"github.com/liuguobing/go_tsh/internal/wal"
)

// readHeader 直接读日志文件头里的版本字节。
//
// 不通过 wal 包的接口读：那样就成了「用被测对象验证被测对象」，
// 万一 open 时把版本记错了也发现不了。
func readHeader(t *testing.T, path string) uint8 {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	buf := make([]byte, 5)
	if _, err := io.ReadFull(f, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf[:4]) != wal.Magic {
		t.Fatalf("魔数 = %q, want %q", buf[:4], wal.Magic)
	}
	return buf[4]
}

// wal 里的字段类型用裸 uint8 编码（它不该依赖 index 这个业务包），
// 代价是两边的取值必须一致。这条测试就是那个守卫：
// 任何一边改了枚举，这里立刻会红。
func TestFieldKindEncodingMatchesIndex(t *testing.T) {
	pairs := []struct {
		name string
		idx  index.FieldKind
		wal  uint8
	}{
		{"text", index.FieldText, wal.KindFieldText},
		{"keyword", index.FieldKeyword, wal.KindFieldKeyword},
		{"number", index.FieldNumber, wal.KindFieldNumber},
		{"date", index.FieldDate, wal.KindFieldDate},
	}

	for _, p := range pairs {
		if uint8(p.idx) != p.wal {
			t.Errorf("字段类型 %s 的编码不一致：index=%d, wal=%d",
				p.name, uint8(p.idx), p.wal)
		}
	}
}

// 类型化字段必须跨重启存活。
//
// 这是 WAL v2 存在的**全部理由**：动态映射是「首次出现的类型即为该字段
// 类型」，若日志只存文本，重启重放时 number 会退化成 text，
// 现象是「重启后范围查询报字段未声明」——很难联想到根因。
func TestTypedFieldsSurviveRestart(t *testing.T) {
	dir := t.TempDir()

	// 第一次：类型靠「放进哪张表」声明，不需要手动调 DeclareField
	e := openPersistent(t, dir)

	docs := []Document{
		{
			ID:       "d1",
			Fields:   map[string]string{"title": "cheap"},
			Keywords: map[string]string{"sku": "A-1"},
			Numbers:  map[string]float64{"price": 500},
			Dates:    map[string]time.Time{"created": time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)},
		},
		{
			ID:       "d2",
			Fields:   map[string]string{"title": "pricey"},
			Keywords: map[string]string{"sku": "B-2"},
			Numbers:  map[string]float64{"price": 2500},
			Dates:    map[string]time.Time{"created": time.Date(2024, 8, 15, 0, 0, 0, 0, time.UTC)},
		},
	}
	for _, d := range docs {
		if _, err := e.Upsert(d); err != nil {
			t.Fatal(err)
		}
	}

	before := numericFingerprint(t, e)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// 第二次：只重放，不做任何声明
	e2 := openPersistent(t, dir)
	defer e2.Close()

	// 类型必须原样恢复
	for _, tc := range []struct {
		field string
		want  index.FieldKind
	}{
		{"price", index.FieldNumber},
		{"created", index.FieldDate},
		{"sku", index.FieldKeyword},
	} {
		got, ok := defaultIdx(e2).FieldKindOf(tc.field)
		if !ok {
			t.Errorf("重启后字段 %q 的类型丢失了", tc.field)
			continue
		}
		if got != tc.want {
			t.Errorf("重启后字段 %q 的类型 = %s, want %s", tc.field, got, tc.want)
		}
	}

	// 范围查询必须给出与重启前完全一致的结果
	after := numericFingerprint(t, e2)
	if len(before) != len(after) {
		t.Fatalf("重启前后指纹长度不同: %d vs %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("重启后第 %d 项不一致:\n  重启前: %s\n  重启后: %s",
				i, before[i], after[i])
		}
	}
}

// numericFingerprint 用范围查询与等值查询刻画带类型字段的状态。
func numericFingerprint(t *testing.T, e *Engine) []string {
	t.Helper()

	queries := []string{
		"price:[* TO *]", // 会被解析器拒绝，只用于占位（见下）
		"price:[0 TO 1000]",
		"price:[1000 TO 3000]",
		"price:2500",
		"created:[2024-01-01 TO 2024-06-30]",
		"created:[2024-07-01 TO 2024-12-31]",
		"sku:A-1",
		"sku:B-2",
	}

	out := make([]string, 0, len(queries))
	for _, q := range queries {
		res, err := e.Search(SearchRequest{Query: q, Limit: 50})
		if err != nil {
			// [* TO *] 本来就会被拒绝，把错误也纳入指纹
			out = append(out, fmt.Sprintf("q=%q err=%v", q, err))
			continue
		}

		ids := make([]string, 0, len(res.Hits))
		for _, h := range res.Hits {
			ids = append(ids, h.ID)
		}
		out = append(out, fmt.Sprintf("q=%q total=%d hits=%v", q, res.Total, ids))
	}
	return out
}

// 日志文件必须是 v2：v1 的格式里没有类型字节。
func TestLogIsWrittenAsV2(t *testing.T) {
	dir := t.TempDir()

	e := openPersistent(t, dir)
	if _, err := e.Upsert(Document{ID: "d1", Fields: map[string]string{"body": "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// 直接读文件头的版本字节
	raw := readHeader(t, filepath.Join(dir, tablesSubdir, tablename.Default+logExt))
	if raw != wal.FormatVersion {
		t.Errorf("日志版本 = %d, want %d", raw, wal.FormatVersion)
	}
	if wal.FormatVersion != 2 {
		t.Errorf("本测试的前提是当前格式为 v2，实际 %d", wal.FormatVersion)
	}
}

// defaultIdx 取默认表的底层索引，供测试直接检查内部状态。
//
// 有了表之后 Engine 不再直接持有索引，测试得先拿到默认表。
func defaultIdx(e *Engine) *index.InvertedIndex { return e.defaultTable().idx }
