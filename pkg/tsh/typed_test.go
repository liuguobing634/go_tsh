package tsh

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liuguobing/go_tsh/internal/index"
)

func TestDocumentRoundTripByType(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	created := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	doc := Document{
		ID:       "d1",
		Fields:   map[string]string{"title": "hello world"},
		Keywords: map[string]string{"sku": "LAP-1"},
		Numbers:  map[string]float64{"price": 4999.5},
		Dates:    map[string]time.Time{"created": created},
	}

	if _, err := e.Upsert(doc); err != nil {
		t.Fatal(err)
	}

	got, ok := e.GetDocument("d1")
	if !ok {
		t.Fatal("文档不存在")
	}

	if got.Fields["title"] != "hello world" {
		t.Errorf("Fields = %v", got.Fields)
	}
	if got.Keywords["sku"] != "LAP-1" {
		t.Errorf("Keywords = %v", got.Keywords)
	}
	if got.Numbers["price"] != 4999.5 {
		t.Errorf("Numbers = %v", got.Numbers)
	}
	// 内部按毫秒存储，秒级时间应当精确往返
	if !got.Dates["created"].Equal(created) {
		t.Errorf("Dates = %v, want %v", got.Dates["created"], created)
	}

	// 类型表也应当被写进去。
	//
	// Schema() 返回的是**对外的** FieldKind（字符串），不是内部枚举——
	// 调用方不该需要知道内部编码。
	schema := e.Schema()
	for field, want := range map[string]FieldKind{
		"title":   FieldText,
		"sku":     FieldKeyword,
		"price":   FieldNumber,
		"created": FieldDate,
	} {
		if schema[field] != want {
			t.Errorf("字段 %q 的类型 = %v, want %v", field, schema[field], want)
		}
	}
}

// 同一字段出现在两张表里是自相矛盾的，必须在写入前拒绝。
func TestAmbiguousFieldIsRejected(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	_, err = e.Upsert(Document{
		ID:     "d1",
		Fields: map[string]string{"x": "text"},
		// 同一个名字又出现在数值表里
		Numbers: map[string]float64{"x": 1},
	})
	if !errors.Is(err, ErrAmbiguousField) {
		t.Fatalf("应当返回 ErrAmbiguousField，实际: %v", err)
	}
	t.Logf("%v", err)

	for _, want := range []string{"x", "Fields", "Numbers"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误里应当说明 %q：%v", want, err)
		}
	}
}

func TestEmptyDocumentIsRejected(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	if _, err := e.Upsert(Document{ID: "d1"}); !errors.Is(err, ErrNoFields) {
		t.Errorf("空文档应当返回 ErrNoFields，实际: %v", err)
	}
}

func hasKey[V any](m map[string]V, key string) bool {
	_, ok := m[key]
	return ok
}

func TestDocumentFromValuesRouting(t *testing.T) {
	cases := []struct {
		name  string
		value any
		check func(*testing.T, Document)
	}{
		{"字符串走文本", "hello", func(t *testing.T, d Document) {
			if d.Fields["f"] != "hello" {
				t.Errorf("Fields = %v", d.Fields)
			}
		}},
		{"浮点走数值", 1.5, func(t *testing.T, d Document) {
			if d.Numbers["f"] != 1.5 {
				t.Errorf("Numbers = %v", d.Numbers)
			}
		}},
		{"整数走数值", 42, func(t *testing.T, d Document) {
			if d.Numbers["f"] != 42 {
				t.Errorf("Numbers = %v", d.Numbers)
			}
		}},
		{"int64 走数值", int64(7), func(t *testing.T, d Document) {
			if d.Numbers["f"] != 7 {
				t.Errorf("Numbers = %v", d.Numbers)
			}
		}},
		{"布尔走关键字", true, func(t *testing.T, d Document) {
			if d.Keywords["f"] != "true" {
				t.Errorf("Keywords = %v", d.Keywords)
			}
		}},
		{"时间走日期", time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC), func(t *testing.T, d Document) {
			if d.Dates["f"].IsZero() {
				t.Errorf("Dates = %v", d.Dates)
			}
		}},
		{"null 被跳过", nil, func(t *testing.T, d Document) {
			// 断言的是 f 没进任何一张表，不是「文档没有字段」——
			// 伴生字段 keep 本来就在。
			for table, has := range map[string]bool{
				"Fields":   hasKey(d.Fields, "f"),
				"Keywords": hasKey(d.Keywords, "f"),
				"Numbers":  hasKey(d.Numbers, "f"),
				"Dates":    hasKey(d.Dates, "f"),
			} {
				if has {
					t.Errorf("null 不该进入 %s: %+v", table, d)
				}
			}
			if d.Fields["keep"] != "present" {
				t.Errorf("伴生字段应当保留: %+v", d)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 带上一个伴生字段：全 null 的文档会被 ErrNoFields 挡掉，
			// 这里要测的是「null 被跳过」，不是「空文档被拒绝」。
			doc, err := DocumentFromValues("d1", map[string]any{
				"f":    tc.value,
				"keep": "present",
			})
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			tc.check(t, doc)
		})
	}
}

func TestDocumentFromValuesRejects(t *testing.T) {
	t.Run("对象与数组", func(t *testing.T) {
		for _, bad := range []any{[]any{1}, map[string]any{"a": 1}} {
			if _, err := DocumentFromValues("d", map[string]any{"f": bad}); !errors.Is(err, ErrUnsupportedFieldType) {
				t.Errorf("%T 应当被拒绝，实际: %v", bad, err)
			}
		}
	})

	t.Run("空字段名", func(t *testing.T) {
		if _, err := DocumentFromValues("d", map[string]any{"  ": "x"}); err == nil {
			t.Error("空字段名应当被拒绝")
		}
	})

	t.Run("全 null", func(t *testing.T) {
		if _, err := DocumentFromValues("d", map[string]any{"a": nil}); !errors.Is(err, ErrNoFields) {
			t.Errorf("全 null 应当返回 ErrNoFields，实际: %v", err)
		}
	})
}

// json.Number 的整数路径要能识别超出 2^53 的值。
// 这条正是 decodeJSON 用 UseNumber 的理由：默认解成 float64 时精度已经没了。
func TestJSONNumberIntegerPrecision(t *testing.T) {
	t.Run("范围内接受", func(t *testing.T) {
		doc, err := DocumentFromValues("d", map[string]any{"n": json.Number("9007199254740992")})
		if err != nil {
			t.Fatal(err)
		}
		if doc.Numbers["n"] != 9007199254740992 {
			t.Errorf("Numbers = %v", doc.Numbers)
		}
	})

	t.Run("超范围拒绝", func(t *testing.T) {
		_, err := DocumentFromValues("d", map[string]any{"n": json.Number("20240101123456789")})
		if !errors.Is(err, ErrIntegerTooLarge) {
			t.Fatalf("应当返回 ErrIntegerTooLarge，实际: %v", err)
		}
		if !strings.Contains(err.Error(), "20240101123456789") {
			t.Errorf("错误里应当带上原值：%v", err)
		}
	})

	t.Run("小数正常接受", func(t *testing.T) {
		doc, err := DocumentFromValues("d", map[string]any{"n": json.Number("1.25")})
		if err != nil {
			t.Fatal(err)
		}
		if doc.Numbers["n"] != 1.25 {
			t.Errorf("Numbers = %v", doc.Numbers)
		}
	})

	t.Run("非法数字", func(t *testing.T) {
		if _, err := DocumentFromValues("d", map[string]any{"n": json.Number("abc")}); err == nil {
			t.Error("非法数字应当报错")
		}
	})
}

// Go API 直接传 int64 也要做同样的检查。
func TestGoAPIIntegerPrecision(t *testing.T) {
	if _, err := DocumentFromValues("d", map[string]any{"n": int64(20240101123456789)}); !errors.Is(err, ErrIntegerTooLarge) {
		t.Errorf("超范围 int64 应当被拒绝，实际: %v", err)
	}
	if _, err := DocumentFromValues("d", map[string]any{"n": int64(123)}); err != nil {
		t.Errorf("正常 int64 应当被接受: %v", err)
	}
}

// 预声明的 schema 让 JSON 字符串也能成为日期或关键字。
func TestParseDocumentAppliesSchema(t *testing.T) {
	e, err := NewWith(Options{Schema: map[string]FieldKind{
		"created": FieldDate,
		"sku":     FieldKeyword,
		"price":   FieldNumber,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	doc, err := e.ParseDocument("d1", map[string]any{
		"created": "2024-01-15",
		"sku":     "A-1",
		"price":   "4999", // 字符串形式的数字
		"note":    "plain text",
	})
	if err != nil {
		t.Fatal(err)
	}

	if doc.Dates["created"].IsZero() {
		t.Errorf("created 应当被解析成日期: %+v", doc)
	}
	if doc.Keywords["sku"] != "A-1" {
		t.Errorf("sku 应当是关键字: %+v", doc)
	}
	if doc.Numbers["price"] != 4999 {
		t.Errorf("price 应当是数值: %+v", doc)
	}
	if doc.Fields["note"] != "plain text" {
		t.Errorf("未声明的字段应当走文本: %+v", doc)
	}
}

func TestParseDocumentRejectsBadDate(t *testing.T) {
	e, err := NewWith(Options{Schema: map[string]FieldKind{"created": FieldDate}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	_, err = e.ParseDocument("d1", map[string]any{"created": "not-a-date"})
	if err == nil {
		t.Fatal("非法日期应当报错")
	}
	if !strings.Contains(err.Error(), "created") {
		t.Errorf("错误里应当指明字段：%v", err)
	}
}

// 日期内部精度是毫秒，比它更细的部分会被截断——这是**已知取舍**，
// 用测试钉住，免得日后有人以为能做到纳秒。
func TestDatePrecisionIsMilliseconds(t *testing.T) {
	e, err := NewWith(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	// 带微秒的时间
	original := time.Date(2024, 1, 15, 10, 30, 0, 123456000, time.UTC)
	if _, err := e.Upsert(Document{
		ID:     "d1",
		Fields: map[string]string{"title": "x"},
		Dates:  map[string]time.Time{"t": original},
	}); err != nil {
		t.Fatal(err)
	}

	got, _ := e.GetDocument("d1")
	stored := got.Dates["t"]

	if stored.Nanosecond()%int(time.Millisecond) != 0 {
		t.Errorf("存储后应当只保留到毫秒，实际 %v", stored)
	}
	if stored.UnixMilli() != original.UnixMilli() {
		t.Errorf("毫秒级应当一致：%v vs %v", stored, original)
	}
	if stored.Equal(original) {
		t.Log("微秒部分恰好相同（123.456ms 截断成 123ms 后不同才正常）")
	}
}

// schema 与数据的类型冲突必须报错，并带上文档 ID。
//
// 注意冲突是在**写入时**才被发现的，不是 ParseDocument：解析阶段只负责
// 按 schema 把值分流（布尔只能当关键字），此时还不知道关键字与预声明的
// date 已经矛盾——那要等 normalize 去声明类型时才会暴露。
func TestSchemaConflictNamesDocument(t *testing.T) {
	e, err := NewWith(Options{Schema: map[string]FieldKind{"created": FieldDate}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	doc, err := e.ParseDocument("doc-7", map[string]any{"created": true})
	if err != nil {
		t.Fatalf("ParseDocument 不该在这里失败（冲突要到写入时才发现）: %v", err)
	}

	_, err = e.Upsert(doc)
	if !errors.Is(err, index.ErrFieldKindConflict) {
		t.Fatalf("应当返回 ErrFieldKindConflict，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "doc-7") {
		t.Errorf("错误里应当带上文档 ID：%v", err)
	}
	t.Logf("%v", err)
}

func TestUnsupportedSchemaKind(t *testing.T) {
	if _, err := NewWith(Options{Schema: map[string]FieldKind{"f": "martian"}}); err == nil {
		t.Fatal("未知的字段类型应当让构造失败")
	}
}
