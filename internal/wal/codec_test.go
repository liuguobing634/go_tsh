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
		kinds    map[string]uint8
	}{
		{"纯文本", "doc-1", map[string]string{"body": "hello"}, nil},
		{"多字段", "doc-2", map[string]string{"title": "标题", "body": "正文"}, nil},
		{"空字段值", "doc-3", map[string]string{"empty": ""}, nil},
		{"无字段", "doc-4", map[string]string{}, nil},
		{"含换行与引号", "doc-5", map[string]string{"body": "a\nb\t\"q\" 中文"}, nil},
		{"非 ASCII 的 ID", "文档-6", map[string]string{"body": "值"}, nil},
		{
			name:     "带类型化字段",
			external: "doc-7",
			fields:   map[string]string{"title": "x", "price": "42", "created": "2024-01-01"},
			kinds: map[string]uint8{
				"price":   KindFieldNumber,
				"created": KindFieldDate,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := EncodeUpsert(tc.external, tc.fields, tc.kinds, nil)

			doc, err := DecodeUpsert(FormatVersion, payload)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if doc.External != tc.external {
				t.Errorf("外部 ID = %q, want %q", doc.External, tc.external)
			}
			if len(doc.Fields) != len(tc.fields) {
				t.Fatalf("字段数 = %d, want %d（%v）", len(doc.Fields), len(tc.fields), doc.Fields)
			}
			for k, v := range tc.fields {
				if doc.Fields[k] != v {
					t.Errorf("字段 %q = %q, want %q", k, doc.Fields[k], v)
				}
			}

			// 类型必须原样往返。text 不出现在 Kinds 里（它是默认值）。
			if len(doc.Kinds) != len(tc.kinds) {
				t.Fatalf("类型数 = %d, want %d（%v）", len(doc.Kinds), len(tc.kinds), doc.Kinds)
			}
			for k, v := range tc.kinds {
				if doc.Kinds[k] != v {
					t.Errorf("字段 %q 的类型 = %d, want %d", k, doc.Kinds[k], v)
				}
			}
		})
	}
}

// 编码必须确定：同样的输入产生同样的字节。
func TestUpsertEncodingIsDeterministic(t *testing.T) {
	fields := map[string]string{
		"zebra": "1", "alpha": "2", "中文": "3", "body": "4", "title": "5",
	}
	kinds := map[string]uint8{"zebra": KindFieldNumber}

	first := EncodeUpsert("doc", fields, kinds, nil)
	for range 20 {
		again := EncodeUpsert("doc", fields, kinds, nil)
		if string(again) != string(first) {
			t.Fatalf("同样的输入产生了不同的字节:\n%v\n%v", first, again)
		}
	}
}

// v1 的日志必须仍然读得出来。
//
// 升级格式就直接拒绝启动、逼用户删数据重来，是最差的处理方式——
// v1 的数据是完全可解析的，只是没有类型信息而已。
//
// 这里手工构造一条 v1 记录（字段前面**没有**类型字节）。
func TestDecodeLegacyV1Upsert(t *testing.T) {
	payload := appendString(nil, "legacy-doc")
	payload = binary.AppendUvarint(payload, 2)
	payload = appendString(payload, "title")
	payload = appendString(payload, "old format")
	payload = appendString(payload, "price")
	payload = appendString(payload, "42")

	doc, err := DecodeUpsert(legacyFormatVersion, payload)
	if err != nil {
		t.Fatalf("v1 记录应当能读出来: %v", err)
	}
	if doc.External != "legacy-doc" {
		t.Errorf("外部 ID = %q", doc.External)
	}
	if doc.Fields["title"] != "old format" || doc.Fields["price"] != "42" {
		t.Errorf("字段 = %v", doc.Fields)
	}
	// v1 没有类型信息，因此全部按 text
	if len(doc.Kinds) != 0 {
		t.Errorf("v1 记录不该带类型，实际 %v", doc.Kinds)
	}
}

func TestDecodeUpsertRejectsUnknownVersion(t *testing.T) {
	payload := EncodeUpsert("doc", map[string]string{"a": "b"}, nil, nil)

	for _, v := range []uint8{0, 3, 99} {
		if _, err := DecodeUpsert(v, payload); !errors.Is(err, errMalformed) {
			t.Errorf("版本 %d 应当被拒绝，实际: %v", v, err)
		}
	}
}

func TestDeleteCodecRoundTrip(t *testing.T) {
	got, err := DecodeDelete(EncodeDelete("doc-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if got != "doc-1" {
		t.Errorf("外部 ID = %q, want %q", got, "doc-1")
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	valid := EncodeUpsert("doc", map[string]string{"body": "x"}, nil, nil)

	cases := []struct {
		name    string
		payload []byte
	}{
		{"空 payload", nil},
		{"外部 ID 为空", EncodeUpsert("", map[string]string{"a": "b"}, nil, nil)},
		{"截断在长度字段中间", []byte{0xff}},
		{"字符串长度超出剩余数据", []byte{0x7f, 'a'}},
		{"声明字段数极大", func() []byte {
			b := appendString(nil, "doc")
			return binary.AppendUvarint(b, 1<<40)
		}()},
		{"尾部有多余字节", append(append([]byte{}, valid...), 0x00)},
		{"v2 记录被截断", valid[:len(valid)-1]},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeUpsert(FormatVersion, tc.payload); err == nil {
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
func TestDecodeRejectsHugeFieldCountWithoutAllocating(t *testing.T) {
	b := appendString(nil, "doc")
	b = binary.AppendUvarint(b, 1<<40)

	if _, err := DecodeUpsert(FormatVersion, b); err == nil {
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

func TestEncodeUpsertReusesBuffer(t *testing.T) {
	buf := make([]byte, 0, 256)

	first := EncodeUpsert("doc-1", map[string]string{"body": "hello"}, nil, buf)
	if &first[0] != &buf[:1][0] {
		t.Error("应当复用传入的缓冲")
	}

	second := EncodeUpsert("doc-2", map[string]string{"body": "world"}, nil, first[:0])
	doc, err := DecodeUpsert(FormatVersion, second)
	if err != nil || doc.External != "doc-2" {
		t.Errorf("复用缓冲后解析结果错误: id=%q err=%v", doc.External, err)
	}
}
