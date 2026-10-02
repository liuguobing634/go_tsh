package index

import (
	"fmt"
	"strings"
)

// FieldKind 是字段的类型，决定它怎么建索引、能怎么查。
//
// 类型不由单个文档决定，而是**索引级**的：一个字段名在一份索引里
// 只能有一种类型。这与 ES 的 mapping 是同一个道理——
// 同一个字段一半是数字一半是文本，既没法建索引也没法查询。
type FieldKind uint8

const (
	// FieldText 是默认类型：经分析器切分后建倒排索引，
	// 支持词条查询与短语查询。现有的所有行为都是它。
	FieldText FieldKind = iota

	// FieldKeyword 不做分词：整个值作为一个词条。
	//
	// 适合 ID、标签、枚举这类需要精确匹配、不能容忍分词副作用的值。
	// 值会做小写化，与文本字段的口径保持一致。
	FieldKeyword

	// FieldNumber 是数值：存进数值列，支持等值查询与范围查询。
	FieldNumber

	// FieldDate 是时间：同样存进数值列，单位是 **UTC epoch 毫秒**。
	//
	// 与 FieldNumber 共用一套列与求值逻辑，区别只在写入时的解析
	// 与报错信息——这样范围查询的实现只需要一份。
	FieldDate
)

func (k FieldKind) String() string {
	switch k {
	case FieldText:
		return "text"
	case FieldKeyword:
		return "keyword"
	case FieldNumber:
		return "number"
	case FieldDate:
		return "date"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(k))
	}
}

// Numeric 报告该类型是否用数值列存储。
func (k FieldKind) Numeric() bool {
	return k == FieldNumber || k == FieldDate
}

// ParseFieldKind 解析类型名。
//
// 接受 "text" / "keyword" / "number" / "date"，大小写不敏感。
func ParseFieldKind(s string) (FieldKind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "text":
		return FieldText, nil
	case "keyword":
		return FieldKeyword, nil
	case "number":
		return FieldNumber, nil
	case "date":
		return FieldDate, nil
	default:
		return FieldText, fmt.Errorf("%w: 未知的字段类型 %q，"+
			"可选 text|keyword|number|date", ErrInvalidFieldKind, s)
	}
}

// DeclareField 声明字段的类型。
//
// 幂等：重复声明同一个类型是允许的——每次写入都会调用它。
//
// 类型冲突返回 ErrFieldKindConflict，**不做静默强转**。
// 强转看起来"更宽容"，实际会把「同一字段一半是数字一半是文本」
// 这种脏数据悄悄放进来，等到查询时才发现，那时已经很难追查了。
//
// **只有真正新增的声明才会触发 OnApply 钩子**（见 Change.Declared）。
// 这一点很关键：DeclareField 每次写入都会被调用，如果每次都记一条日志，
// 日志会被这种无变化的声明刷满。
func (ix *InvertedIndex) DeclareField(field string, kind FieldKind) error {
	field = strings.TrimSpace(field)
	if field == "" {
		return ErrEmptyFieldName
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	if existing, ok := ix.schema[field]; ok {
		if existing != kind {
			return fmt.Errorf("%w: 字段 %q 已经是 %s 类型，不能改成 %s"+
				"（同一字段在一份索引里只能有一种类型）",
				ErrFieldKindConflict, field, existing, kind)
		}
		// 已声明过同一个类型：什么都不用做，也不必记日志。
		return nil
	}

	ix.schema[field] = kind

	if kind.Numeric() && ix.numColumns[field] == nil {
		ix.numColumns[field] = &numericColumn{}
	}

	// 新增声明要落盘。
	//
	// 否则「建表声明好字段类型、还没写任何文档」的表重启后 schema 就丢了——
	// 而「先建表、再慢慢灌数据」恰恰是最常见的用法。
	//
	// 钩子在**写锁内**调用，因此声明记录与随后的文档记录顺序一致；
	// 重放时按同样的顺序重放，schema 就能原样复现。
	if ix.onApply != nil {
		if err := ix.onApply(Change{Declared: map[string]FieldKind{field: kind}}); err != nil {
			// 索引的 schema 已经改了，这里无法回滚。
			// 把错误抛出去让调用方（通常是持久化层）决定怎么办。
			return err
		}
	}
	return nil
}

// FieldKindOf 查询字段的类型。
func (ix *InvertedIndex) FieldKindOf(field string) (FieldKind, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	k, ok := ix.schema[field]
	return k, ok
}

// Schema 返回字段类型表的副本。
func (ix *InvertedIndex) Schema() map[string]FieldKind {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	out := make(map[string]FieldKind, len(ix.schema))
	for k, v := range ix.schema {
		out[k] = v
	}
	return out
}

// fieldKindsOf 返回给定字段集合的类型快照。调用方必须持锁（读或写）。
//
// 变更通知要把类型一起带出去（持久化日志需要它来复现动态映射），
// 所以每次写入都要取一次。字段数很少，直接建小 map 即可。
func (ix *InvertedIndex) fieldKindsOf(fields map[string]string) map[string]FieldKind {
	if len(fields) == 0 {
		return nil
	}

	out := make(map[string]FieldKind, len(fields))
	for name := range fields {
		out[name] = ix.schema[name] // 未声明的字段零值就是 FieldText
	}
	return out
}
