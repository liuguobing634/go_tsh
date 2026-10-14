package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
)

// errMalformed 表示 payload 无法解析。
var errMalformed = errors.New("wal: 记录内容格式错误")

// 字段类型在记录里的编码。
//
// **刻意用裸 uint8 而不是 index.FieldKind**：wal 包不理解记录内容的业务含义，
// 引入 index 会让它从「通用追加日志」变成「文档日志」。
//
// 代价是这里的取值必须与 index.FieldKind 一致。这条约束由 pkg/tsh 的一个
// 测试守着（把两边的枚举逐个比对），因此不会悄悄漂移。
const (
	KindFieldText    uint8 = 0
	KindFieldKeyword uint8 = 1
	KindFieldNumber  uint8 = 2
	KindFieldDate    uint8 = 3
)

// appendString 写入「uvarint 长度 + 字节」。
func appendString(dst []byte, s string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(s)))
	return append(dst, s...)
}

// readString 从 off 处读出一个字符串，返回新偏移。
func readString(src []byte, off int) (string, int, error) {
	if off < 0 || off > len(src) {
		return "", 0, errMalformed
	}

	n, m := binary.Uvarint(src[off:])
	if m <= 0 {
		return "", 0, errMalformed
	}
	off += m

	// n 来自不可信输入：先与剩余长度比较再转 int，
	// 否则一个巨大的数字会让下面越界或骗出一次巨额分配。
	if n > uint64(len(src)-off) {
		return "", 0, errMalformed
	}
	return string(src[off : off+int(n)]), off + int(n), nil
}

// EncodeUpsert 把一篇文档编码成 upsert 记录的 payload（**v2 格式**）。
//
// 字段按名字排序后写入：同样的输入永远产生同样的字节。
// 这既让日志可以逐字节比较（排查问题时很有用），
// 也让测试能直接断言编码结果，不必依赖 map 的遍历顺序。
//
// kinds 给出每个字段的类型，未列出的按 text。
//
// **类型必须落盘**：动态映射是「首次出现的类型即为该字段类型」，
// 若日志只存文本，重启重放时数字会退化成文本——字段类型变了，
// 范围查询随之失效，而症状是「重启后查询报字段未声明」，很难联想到根因。
func EncodeUpsert(external string, fields map[string]string, kinds map[string]uint8, dst []byte) []byte {
	dst = appendString(dst, external)
	dst = binary.AppendUvarint(dst, uint64(len(fields)))

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		dst = append(dst, kinds[name]) // 未声明即零值 = text
		dst = appendString(dst, name)
		dst = appendString(dst, fields[name])
	}
	return dst
}

// EncodeDelete 把一次删除编码成 delete 记录的 payload。
func EncodeDelete(external string, dst []byte) []byte {
	return appendString(dst, external)
}

// EncodeDeclare 把一组字段类型声明编码成 declare 记录的 payload。
//
// 与 EncodeUpsert 一样按字段名排序：map 的遍历顺序是随机的，
// 不排序会让同样的声明产生不同的字节，日志就没法逐字节比较了。
func EncodeDeclare(kinds map[string]uint8, dst []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(kinds)))

	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		dst = append(dst, kinds[name])
		dst = appendString(dst, name)
	}
	return dst
}

// DecodeDeclare 解析 declare 记录的 payload。
func DecodeDeclare(payload []byte) (map[string]uint8, error) {
	count, m := binary.Uvarint(payload)
	if m <= 0 {
		return nil, errMalformed
	}
	off := m

	// 字段数不可信：每个字段至少要占几个字节，先跟剩余长度比一次。
	if count > uint64(len(payload)-off) {
		return nil, fmt.Errorf("%w: 声明字段数 %d 超出剩余数据所能表达的上限",
			errMalformed, count)
	}

	out := make(map[string]uint8, count)
	for i := uint64(0); i < count; i++ {
		if off >= len(payload) {
			return nil, errMalformed
		}
		kind := payload[off]
		off++

		name, next, err := readString(payload, off)
		if err != nil {
			return nil, err
		}
		off = next

		if name == "" {
			return nil, fmt.Errorf("%w: 声明的字段名不能为空", errMalformed)
		}
		out[name] = kind
	}

	if off != len(payload) {
		return nil, fmt.Errorf("%w: declare 记录尾部多了 %d 字节",
			errMalformed, len(payload)-off)
	}
	return out, nil
}

// Document 是一条 upsert 记录解码后的内容。
type Document struct {
	External string
	Fields   map[string]string

	// Kinds 只包含**非 text** 的字段：text 是默认值，不必逐个记录。
	Kinds map[string]uint8
}

// DecodeUpsert 解析 upsert 记录的 payload。
//
// version 决定 payload 的布局：
//   - 2 及更高：每个字段前有一个类型字节
//   - 1：只有「名字 + 值」，全部按 text 处理
//
// **按「布局世代」而不是「精确版本」判断**：v3、v4 都只是多了记录类型，
// upsert 的布局与 v2 完全一样。写成 `case 1, 2` 会在升级到 v3 的那一刻
// 让所有文档重放集体失败——而空表（只有 declare 记录）测不出来，
// 这个坑真的踩过一次。
//
// 版本区间用 FormatVersion 而不是硬编码列表：每加一种记录类型都要回来
// 改这个开关，迟早会漏。文件头那关（Open）已经把超出区间的文件挡住了。
func DecodeUpsert(version uint8, payload []byte) (Document, error) {
	if version < 1 || version > FormatVersion {
		return Document{}, fmt.Errorf("%w: 不支持的重放版本 %d", errMalformed, version)
	}
	// v1 是唯一没有类型字节的世代。
	hasKinds := version >= 2

	external, off, err := readString(payload, 0)
	if err != nil {
		return Document{}, err
	}
	if external == "" {
		return Document{}, fmt.Errorf("%w: 外部 ID 为空", errMalformed)
	}

	count, m := binary.Uvarint(payload[off:])
	if m <= 0 {
		return Document{}, errMalformed
	}
	off += m

	// 字段数同样不可信。每个字段至少要占几个字节，
	// 先跟剩余长度比一次，避免被一个巨大的数字骗去分配内存。
	if count > uint64(len(payload)-off) {
		return Document{}, fmt.Errorf("%w: 字段数 %d 超出剩余数据所能表达的上限",
			errMalformed, count)
	}

	doc := Document{
		External: external,
		Fields:   make(map[string]string, count),
	}

	for i := uint64(0); i < count; i++ {
		var kind uint8
		if hasKinds {
			if off >= len(payload) {
				return Document{}, errMalformed
			}
			kind = payload[off]
			off++
		}

		name, next, err := readString(payload, off)
		if err != nil {
			return Document{}, err
		}
		off = next

		value, next, err := readString(payload, off)
		if err != nil {
			return Document{}, err
		}
		off = next

		doc.Fields[name] = value

		if kind != KindFieldText {
			if doc.Kinds == nil {
				doc.Kinds = make(map[string]uint8, 2)
			}
			doc.Kinds[name] = kind
		}
	}

	if off != len(payload) {
		return Document{}, fmt.Errorf("%w: upsert 记录尾部多了 %d 字节",
			errMalformed, len(payload)-off)
	}
	return doc, nil
}

// DecodeDelete 解析 delete 记录的 payload。
func DecodeDelete(payload []byte) (string, error) {
	external, off, err := readString(payload, 0)
	if err != nil {
		return "", err
	}
	if external == "" {
		return "", fmt.Errorf("%w: 外部 ID 为空", errMalformed)
	}
	if off != len(payload) {
		return "", fmt.Errorf("%w: delete 记录尾部多了 %d 字节",
			errMalformed, len(payload)-off)
	}
	return external, nil
}

// ImportRecord 是一条 import-done 记录的内容。
//
// Generate 也记下来是为了能发现「启动参数变了」：用户从
// `-import a/` 换成 `-import b/` 时，如果只看「有没有标记」，
// 就会静默地什么都不导入——那是很难自己发现的一类意外。
type ImportRecord struct {
	// Dir 是导入的语料目录；只有 -generate 时为空。
	Dir string

	// Files 与 Bytes 是当时导入的规模，只给人看。
	Files int
	Bytes int64

	// Generate 是当时 -generate 指定的篇数。
	Generate int
}

// EncodeImportDone 把一次「语料导入已完成」编码成 import-done 记录的 payload。
func EncodeImportDone(rec ImportRecord, dst []byte) []byte {
	dst = appendString(dst, rec.Dir)
	dst = binary.AppendUvarint(dst, uint64(rec.Files))
	dst = binary.AppendUvarint(dst, uint64(rec.Bytes))
	dst = binary.AppendUvarint(dst, uint64(rec.Generate))
	return dst
}

// DecodeImportDone 解析 import-done 记录的 payload。
func DecodeImportDone(payload []byte) (ImportRecord, error) {
	var rec ImportRecord

	dir, off, err := readString(payload, 0)
	if err != nil {
		return rec, err
	}
	rec.Dir = dir

	// Dir 为空是合法的：只用了 -generate 时就没有目录。
	// 但「目录和生成篇数都为空」说明这条记录没有意义。
	nums := make([]uint64, 0, 3)
	for range 3 {
		v, m := binary.Uvarint(payload[off:])
		if m <= 0 {
			return ImportRecord{}, errMalformed
		}
		off += m
		nums = append(nums, v)
	}

	if off != len(payload) {
		return ImportRecord{}, fmt.Errorf("%w: import-done 记录尾部多了 %d 字节",
			errMalformed, len(payload)-off)
	}
	if rec.Dir == "" && nums[2] == 0 {
		return ImportRecord{}, fmt.Errorf("%w: import-done 记录既没有目录也没有生成篇数",
			errMalformed)
	}

	// 饱和转换：这几个数只用于展示，但直接 int() 溢出后会变成负数，
	// 日志里就会出现「导入了 -1 个文件」这种莫名其妙的话。
	rec.Files = saturateInt(nums[0])
	rec.Bytes = saturateInt64(nums[1])
	rec.Generate = saturateInt(nums[2])
	return rec, nil
}

func saturateInt(v uint64) int {
	maxInt := uint64(^uint(0) >> 1)
	if v > maxInt {
		return int(maxInt)
	}
	return int(v)
}

func saturateInt64(v uint64) int64 {
	maxInt := uint64(^uint64(0) >> 1)
	if v > maxInt {
		return int64(maxInt)
	}
	return int64(v)
}
