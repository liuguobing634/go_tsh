package wal

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestUpsertCodecRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		external string
		fields   map[string]string
	}{
		{"单字段", "doc-1", map[string]string{"body": "hello"}},
		{"多字段", "doc-2", map[string]string{"title": "标题", "body": "正文内容", "tag": "x"}},
		{"空字段值", "doc-3", map[string]string{"empty": ""}},
		{"无字段", "doc-4", map[string]string{}},
		{"含换行与引号", "doc-5", map[string]string{"body": "line1\nline2\t\"quoted\" 中文"}},
		{"非 ASCII 的 ID", "文档-6", map[string]string{"body": "值"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := EncodeUpsert(tc.external, tc.fields, nil)

			gotID, gotFields, err := DecodeUpsert(payload)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if gotID != tc.external {
				t.Errorf("外部 ID = %q, want %q", gotID, tc.external)
			}
			if len(gotFields) != len(tc.fields) {
				t.Fatalf("字段数 = %d, want %d（%v）", len(gotFields), len(tc.fields), gotFields)
			}
			for k, v := range tc.fields {
				if gotFields[k] != v {
					t.Errorf("字段 %q = %q, want %q", k, gotFields[k], v)
				}
			}
		})
	}
}

// 编码必须确定：同样的输入产生同样的字节。
// 这条保证了日志可以逐字节比较，也让测试不必依赖 map 遍历顺序。
func TestUpsertEncodingIsDeterministic(t *testing.T) {
	fields := map[string]string{
		"zebra": "1", "alpha": "2", "中文": "3", "body": "4", "title": "5",
	}

	first := EncodeUpsert("doc", fields, nil)
	for range 20 {
		again := EncodeUpsert("doc", fields, nil)
		if string(again) != string(first) {
			t.Fatalf("同样的输入产生了不同的字节:\n%v\n%v", first, again)
		}
	}
}

func TestDeleteCodecRoundTrip(t *testing.T) {
	payload := EncodeDelete("doc-1", nil)

	got, err := DecodeDelete(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != "doc-1" {
		t.Errorf("外部 ID = %q, want %q", got, "doc-1")
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	valid := EncodeUpsert("doc", map[string]string{"body": "x"}, nil)

	cases := []struct {
		name    string
		payload []byte
	}{
		{"空 payload", nil},
		{"外部 ID 为空", EncodeUpsert("", map[string]string{"a": "b"}, nil)},
		{"截断在长度字段中间", []byte{0xff}},
		{"字符串长度超出剩余数据", []byte{0x7f, 'a'}},
		{"声明字段数极大", func() []byte {
			b := appendString(nil, "doc")
			return binary.AppendUvarint(b, 1<<40)
		}()},
		{"字段名后缺少字段值", func() []byte {
			b := appendString(nil, "doc")
			b = binary.AppendUvarint(b, 1)
			return appendString(b, "name")
		}()},
		{"尾部有多余字节", append(append([]byte{}, valid...), 0x00)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := DecodeUpsert(tc.payload); err == nil {
				t.Fatal("畸形输入必须被拒绝")
			} else if !errors.Is(err, errMalformed) {
				t.Errorf("错误应当是 errMalformed，实际: %v", err)
			}
		})
	}

	t.Run("delete 尾部多余字节", func(t *testing.T) {
		bad := append(EncodeDelete("doc", nil), 0x00)
		if _, err := DecodeDelete(bad); !errors.Is(err, errMalformed) {
			t.Errorf("应当返回 errMalformed，实际: %v", err)
		}
	})

	t.Run("delete 空 ID", func(t *testing.T) {
		if _, err := DecodeDelete(EncodeDelete("", nil)); !errors.Is(err, errMalformed) {
			t.Errorf("应当返回 errMalformed，实际: %v", err)
		}
	})
}

// 一个巨大的字段数不能让解析器去分配对应的内存。
// 这里只验证它被拒绝，不验证耗时——1<<40 个条目如果真去分配会直接 OOM。
func TestDecodeRejectsHugeFieldCountWithoutAllocating(t *testing.T) {
	b := appendString(nil, "doc")
	b = binary.AppendUvarint(b, 1<<40)

	if _, _, err := DecodeUpsert(b); err == nil {
		t.Fatal("巨大的字段数必须被拒绝")
	}
}

func TestKindString(t *testing.T) {
	if KindUpsert.String() != "upsert" {
		t.Errorf("KindUpsert = %q", KindUpsert.String())
	}
	if KindDelete.String() != "delete" {
		t.Errorf("KindDelete = %q", KindDelete.String())
	}
	if got := Kind(200).String(); got != "unknown(200)" {
		t.Errorf("未知 kind = %q", got)
	}
}

// EncodeUpsert 支持复用目标切片，避免每次追加都重新分配。
func TestEncodeUpsertReusesBuffer(t *testing.T) {
	buf := make([]byte, 0, 256)

	first := EncodeUpsert("doc-1", map[string]string{"body": "hello"}, buf)
	if &first[0] != &buf[:1][0] {
		t.Error("应当复用传入的缓冲")
	}

	second := EncodeUpsert("doc-2", map[string]string{"body": "world"}, first[:0])
	if id, _, err := DecodeUpsert(second); err != nil || id != "doc-2" {
		t.Errorf("复用缓冲后解析结果错误: id=%q err=%v", id, err)
	}
}
