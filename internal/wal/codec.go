package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
)

// errMalformed 表示 payload 无法解析。
var errMalformed = errors.New("wal: 记录内容格式错误")

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

// EncodeUpsert 把一篇文档编码成 upsert 记录的 payload。
//
// 字段按名字排序后写入：同样的输入永远产生同样的字节。
// 这既让日志可以逐字节比较（排查问题时很有用），
// 也让测试可以直接断言编码结果而不必依赖 map 的遍历顺序。
//
// 代价是每次写多一次小切片分配。写入路径本来就在分配（分析阶段建 map），
// 这点开销不值得为它把 API 弄复杂。
func EncodeUpsert(external string, fields map[string]string, dst []byte) []byte {
	dst = appendString(dst, external)
	dst = binary.AppendUvarint(dst, uint64(len(fields)))

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		dst = appendString(dst, name)
		dst = appendString(dst, fields[name])
	}
	return dst
}

// EncodeDelete 把一次删除编码成 delete 记录的 payload。
func EncodeDelete(external string, dst []byte) []byte {
	return appendString(dst, external)
}

// DecodeUpsert 解析 upsert 记录的 payload。
func DecodeUpsert(payload []byte) (string, map[string]string, error) {
	external, off, err := readString(payload, 0)
	if err != nil {
		return "", nil, err
	}
	if external == "" {
		return "", nil, fmt.Errorf("%w: 外部 ID 为空", errMalformed)
	}

	count, m := binary.Uvarint(payload[off:])
	if m <= 0 {
		return "", nil, errMalformed
	}
	off += m

	// 字段数同样不可信。每个字段至少要占 2 个字节
	// （两个长度都是 0 的 uvarint），所以它可以先跟剩余长度比一次，
	// 避免被一个巨大的数字骗去分配内存。
	if count > uint64(len(payload)-off) {
		return "", nil, fmt.Errorf("%w: 字段数 %d 超出剩余数据所能表达的上限", errMalformed, count)
	}

	fields := make(map[string]string, count)
	for i := uint64(0); i < count; i++ {
		var name, value string

		if name, off, err = readString(payload, off); err != nil {
			return "", nil, err
		}
		if value, off, err = readString(payload, off); err != nil {
			return "", nil, err
		}
		fields[name] = value
	}

	if off != len(payload) {
		return "", nil, fmt.Errorf("%w: upsert 记录尾部多了 %d 字节", errMalformed, len(payload)-off)
	}
	return external, fields, nil
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
		return "", fmt.Errorf("%w: delete 记录尾部多了 %d 字节", errMalformed, len(payload)-off)
	}
	return external, nil
}
