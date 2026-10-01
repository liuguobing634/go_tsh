package index

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseDate 把时间文本解析成 **UTC epoch 毫秒**。
//
// 支持两种格式：
//
//	RFC3339  : 2024-01-01T10:00:00Z、2024-01-01T18:00:00+08:00
//	纯日期    : 2024-01-01
//
// 纯日期**一律按 UTC 当日 00:00:00 解释**，不猜本地时区。
// 同一份数据在不同机器上被解释成不同瞬间，是这类功能最常见、
// 也最难排查的坑；要本地时间就显式写偏移量。
func ParseDate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: 时间为空", ErrInvalidFieldValue)
	}

	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UnixMilli(), nil
	}

	return 0, fmt.Errorf("%w: %q 不是合法时间，"+
		"支持 RFC3339（2024-01-01T10:00:00Z）或纯日期（2024-01-01，按 UTC 解释）",
		ErrInvalidFieldValue, s)
}

// parseFieldNumber 把文本形式的值按声明类型解析成数值列里的 float64。
//
// 为什么不在这里直接收 float64：索引的写入 API 统一用 map[string]string，
// 文本是唯一的线格式（持久化日志也存它）。类型信息来自 schema，
// 解析在这里做一次，代价是 strconv.ParseFloat 的几十纳秒。
func parseFieldNumber(kind FieldKind, raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("%w: %s 字段 %q 的值为空",
			ErrInvalidFieldValue, kind, raw)
	}

	switch kind {
	case FieldNumber:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q 不是合法数值",
				ErrInvalidFieldValue, raw)
		}
		// NaN / ±Inf 会破坏所有范围比较（NaN 参与任何比较都是 false，
		// 会从过滤条件里漏过去），必须在入口挡掉。
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("%w: %q 不是有限数值",
				ErrInvalidFieldValue, raw)
		}
		return v, nil

	case FieldDate:
		ms, err := ParseDate(s)
		if err != nil {
			return 0, err
		}
		return float64(ms), nil

	default:
		return 0, fmt.Errorf("%w: %s", ErrNotNumericField, kind)
	}
}

// formatFieldNumber 把数值列里的值还原成文本形式。
//
// 用 'g' + -1 精度：这是能**精确往返**（parse → format → parse 结果不变）
// 的最短表示，因此回显与持久化都不会丢精度。
func formatFieldNumber(kind FieldKind, v float64) string {
	if kind == FieldDate {
		return time.UnixMilli(int64(v)).UTC().Format(time.RFC3339)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// maxExactInteger 是 float64 能精确表示的最大连续整数（2^53）。
//
// 超过它的整数放进数值列会静默丢精度——1 亿亿这个量级之前都没事，
// 但订单号一类的大整数正好会踩到，所以调用方需要显式判断。
const maxExactInteger = 1 << 53

// LosslessAsNumber 报告一个 int64 能否被 float64 精确表示。
//
// 供 DTO 层判断：不能精确表示时应当报错或转成字符串，
// 而不是静默截断。
func LosslessAsNumber(v int64) bool {
	return v <= maxExactInteger && v >= -maxExactInteger
}
