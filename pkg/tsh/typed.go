package tsh

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/liuguobing/go_tsh/internal/index"
)

// 文档层的哨兵错误。
var (
	// ErrUnsupportedFieldType 表示字段值的类型无法映射到任何字段类型。
	ErrUnsupportedFieldType = errors.New("tsh: 不支持的字段值类型")

	// ErrAmbiguousField 表示同一个字段出现在两个类型化的字段表里。
	ErrAmbiguousField = errors.New("tsh: 同一字段被赋予了多种类型")

	// ErrIntegerTooLarge 表示整数值超出 float64 能精确表示的范围。
	//
	// 这类值放进数值列会被**静默改变**（20240101123456789 会变成
	// 20240101123456788），而症状是「存进去和取出来不一样」，
	// 极难联想到根因。宁可当场报错。
	ErrIntegerTooLarge = errors.New("tsh: 整数超出可精确表示的范围")
)

// FieldKind 是字段类型。
//
// 与索引内部的 index.FieldKind 是两个类型：对外 API 不该暴露 internal 的
// 类型（外部调用方连名字都写不出来）。转换集中在一处，由测试守着。
type FieldKind string

const (
	// FieldText 是默认类型：分词后建倒排，支持词条与短语查询。
	FieldText FieldKind = "text"

	// FieldKeyword 不分词，整个值作为一个词条，精确匹配。
	FieldKeyword FieldKind = "keyword"

	// FieldNumber 是数值，支持等值与范围查询。
	FieldNumber FieldKind = "number"

	// FieldDate 是时间，支持范围查询，内部按 UTC epoch 毫秒存储。
	FieldDate FieldKind = "date"
)

func toIndexKind(k FieldKind) (index.FieldKind, error) {
	switch k {
	case "", FieldText:
		return index.FieldText, nil
	case FieldKeyword:
		return index.FieldKeyword, nil
	case FieldNumber:
		return index.FieldNumber, nil
	case FieldDate:
		return index.FieldDate, nil
	default:
		return index.FieldText, fmt.Errorf("%w: %q，可选 text|keyword|number|date",
			index.ErrInvalidFieldKind, string(k))
	}
}

// DocumentFromValues 从「JSON 原生类型」的字段表构造文档。
//
// 这是 HTTP 层的入口，也适合任何手里只有 `map[string]any` 的调用方：
//
//	{
//	  "title":  "笔记本电脑",     → 文本
//	  "sku":    "LAP-1",         → 文本（想精确匹配请用 Keywords）
//	  "price":  4999,            → 数值
//	  "stock":  true,            → 关键字 "true"
//	  "tags":   null,            → 跳过
//	}
//
// 注意这里是**按 Go/JSON 的类型**分流，而不是按值的字面形态猜：
// `"123"`（带引号）是文本，`123`（不带引号）是数值。这一点很关键——
// 靠猜的话，商品编号 "0755" 会被当成数字 755，前导零没了。
//
// 对象与数组会被明确拒绝（多值字段是 PLAN 里的非目标），
// 而不是悄悄取第一个元素或者转成字符串。
func DocumentFromValues(id string, values map[string]any) (Document, error) {
	return documentFromValues(id, values, nil)
}

// ParseDocument 按**引擎的字段类型表**把 JSON 原生值转成文档。
//
// 与包级 DocumentFromValues 的区别只有一点：它会先查字段类型表。
//
// 这一步是必需的，因为 **JSON 里没有日期类型**——`"2024-01-15"` 只是一个
// 字符串，光看值没法知道它该是 date 还是 text。靠猜日期格式是错的：
// 版本号 "2024-01-01" 会被当成日期，而这是个很难发现的静默错误。
// 所以日期与关键字字段必须**预先声明**。
func (e *Engine) ParseDocument(id string, values map[string]any) (Document, error) {
	schema := e.idx.Schema()
	return documentFromValues(id, values, func(name string) FieldKind {
		switch schema[name] {
		case index.FieldKeyword:
			return FieldKeyword
		case index.FieldNumber:
			return FieldNumber
		case index.FieldDate:
			return FieldDate
		default:
			return FieldText
		}
	})
}

// documentFromValues 是两种入口共用的实现。
//
// kindOf 为 nil 表示没有类型表，一切按 JSON 原生类型推断。
func documentFromValues(id string, values map[string]any, kindOf func(string) FieldKind) (Document, error) {
	doc := Document{ID: id}

	for name, raw := range values {
		name = strings.TrimSpace(name)
		if name == "" {
			return Document{}, fmt.Errorf("%w: 字段名不能为空", errEmptyFieldName)
		}

		kind := FieldText
		if kindOf != nil {
			kind = kindOf(name)
		}

		switch v := raw.(type) {
		case nil:
			// JSON null 视为「不提供这个字段」，与 ES 的行为一致。

		case string:
			if err := doc.putString(name, v, kind); err != nil {
				return Document{}, err
			}

		case bool:
			// 布尔按关键字存放：JSON 的 true/false 没有别的合理归宿，
			// 而 keyword 的精确匹配正好是它需要的语义。
			if kind == FieldText {
				kind = FieldKeyword
			}
			doc.putKeyword(name, strconv.FormatBool(v))

		case json.Number:
			if err := doc.putJSONNumber(name, v); err != nil {
				return Document{}, err
			}

		case float64:
			doc.putNumber(name, v)

		case float32:
			doc.putNumber(name, float64(v))

		case int:
			if err := doc.putInt(name, int64(v)); err != nil {
				return Document{}, err
			}
		case int64:
			if err := doc.putInt(name, v); err != nil {
				return Document{}, err
			}
		case int32:
			if err := doc.putInt(name, int64(v)); err != nil {
				return Document{}, err
			}

		case time.Time:
			doc.putDate(name, v)

		default:
			return Document{}, fmt.Errorf(
				"%w: 字段 %q 的值类型是 %T；"+
					"支持字符串、数字、布尔与时间，对象与数组暂不支持",
				ErrUnsupportedFieldType, name, raw)
		}
	}

	if len(doc.Fields)+len(doc.Keywords)+len(doc.Numbers)+len(doc.Dates) == 0 {
		return Document{}, ErrNoFields
	}
	return doc, nil
}

// putString 处理字符串值——它的归宿取决于字段类型。
func (d *Document) putString(name, v string, kind FieldKind) error {
	switch kind {
	case FieldKeyword:
		d.putKeyword(name, v)

	case FieldNumber:
		// 声明成 number 的字段收到字符串时，尝试解析而不是直接拒绝：
		// 有些客户端（尤其是把数字当字符串传的）会这样发。
		// 解析不了则报错，绝不静默降级成文本——那会让字段类型
		// 在运行时悄悄漂移。
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return fmt.Errorf("%w: 字段 %q 已声明为 number，但 %q 不是合法数值",
				index.ErrInvalidFieldValue, name, v)
		}
		d.putNumber(name, f)

	case FieldDate:
		ms, err := index.ParseDate(v)
		if err != nil {
			return fmt.Errorf("字段 %q: %w", name, err)
		}
		d.putDate(name, time.UnixMilli(ms).UTC())

	default:
		d.putText(name, v)
	}
	return nil
}

// ErrNoFields 表示文档一个字段都没有。
var ErrNoFields = errors.New("tsh: 文档至少需要一个字段")

var errEmptyFieldName = errors.New("tsh: 字段名不能为空")

func (d *Document) putText(name, v string) {
	if d.Fields == nil {
		d.Fields = make(map[string]string, 4)
	}
	d.Fields[name] = v
}

func (d *Document) putKeyword(name, v string) {
	if d.Keywords == nil {
		d.Keywords = make(map[string]string, 2)
	}
	d.Keywords[name] = v
}

func (d *Document) putNumber(name string, v float64) {
	if d.Numbers == nil {
		d.Numbers = make(map[string]float64, 2)
	}
	d.Numbers[name] = v
}

func (d *Document) putInt(name string, v int64) error {
	if !index.LosslessAsNumber(v) {
		return fmt.Errorf(
			"%w: 字段 %q 的值 %d 超出 float64 能精确表示的范围（±2^53），"+
				"存进数值列会被静默改变；请改用字符串字段存放，或确认这个精度损失可以接受",
			ErrIntegerTooLarge, name, v)
	}
	d.putNumber(name, float64(v))
	return nil
}

// putJSONNumber 处理 json.Number。
//
// 用 json.Number 而不是默认的 float64 是关键：JSON 解码默认把数字变成
// float64，那时**精度已经丢了**，再检查也来不及。保留原始文本才能判断。
func (d *Document) putJSONNumber(name string, v json.Number) error {
	// 优先按整数解释：只有整数才需要做 2^53 的检查。
	if i, err := v.Int64(); err == nil {
		return d.putInt(name, i)
	}

	f, err := v.Float64()
	if err != nil {
		return fmt.Errorf("字段 %q 的值 %q 不是合法数字: %w",
			name, v.String(), err)
	}
	d.putNumber(name, f)
	return nil
}

func (d *Document) putDate(name string, v time.Time) {
	if d.Dates == nil {
		d.Dates = make(map[string]time.Time, 2)
	}
	d.Dates[name] = v
}

// normalize 把几张字段表合并成索引要的文本形式，并声明各字段的类型。
//
// 声明必须发生在写入之前：索引靠 schema 决定这个字段是建倒排还是写数值列。
func (e *Engine) normalize(doc Document) (map[string]string, error) {
	total := len(doc.Fields) + len(doc.Keywords) + len(doc.Numbers) + len(doc.Dates)
	if total == 0 {
		return nil, ErrNoFields
	}

	fields := make(map[string]string, total)
	seen := make(map[string]string, total) // 字段名 -> 已经占用它的那张表

	claim := func(name, table string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errEmptyFieldName
		}
		if prev, dup := seen[name]; dup {
			return fmt.Errorf(
				"%w: 字段 %q 同时出现在 %s 与 %s 里；"+
					"同一字段只能有一种类型",
				ErrAmbiguousField, name, prev, table)
		}
		seen[name] = table
		return nil
	}

	for name, v := range doc.Fields {
		if err := claim(name, "Fields"); err != nil {
			return nil, err
		}
		fields[name] = v
	}

	for name, v := range doc.Keywords {
		if err := claim(name, "Keywords"); err != nil {
			return nil, err
		}
		if err := e.declareFor(doc.ID, name, index.FieldKeyword); err != nil {
			return nil, err
		}
		fields[name] = v
	}

	for name, v := range doc.Numbers {
		if err := claim(name, "Numbers"); err != nil {
			return nil, err
		}
		if err := e.declareFor(doc.ID, name, index.FieldNumber); err != nil {
			return nil, err
		}
		// 'g' + -1 是能精确往返的最短表示。
		fields[name] = strconv.FormatFloat(v, 'g', -1, 64)
	}

	for name, v := range doc.Dates {
		if err := claim(name, "Dates"); err != nil {
			return nil, err
		}
		if err := e.declareFor(doc.ID, name, index.FieldDate); err != nil {
			return nil, err
		}
		fields[name] = v.UTC().Format(time.RFC3339Nano)
	}

	// 文本字段也要声明：这样 schema 里反映的是全部字段；
	// 更重要的是，若某个字段此前被声明成 number，这里会立刻报冲突，
	// 而不是等到查询时才发现类型对不上。
	for name := range doc.Fields {
		if err := e.declareFor(doc.ID, name, index.FieldText); err != nil {
			return nil, err
		}
	}

	return fields, nil
}

// declareFor 声明字段类型，并把冲突错误包上文档 ID。
//
// 带上文档 ID 很重要：批量导入时「哪个文档触发了类型冲突」是定位问题的
// 第一手信息，没有它就只能一条条试。
func (e *Engine) declareFor(docID, field string, kind index.FieldKind) error {
	if err := e.idx.DeclareField(field, kind); err != nil {
		return fmt.Errorf("文档 %q: %w", docID, err)
	}
	return nil
}
