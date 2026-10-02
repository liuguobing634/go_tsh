package tsh

import (
	"strconv"
	"time"

	"github.com/liuguobing/go_tsh/internal/index"
)

// Document 是对外的文档视图。
//
// # 为什么字段分成几张表
//
// 类型必须由调用方给出，而不是从值里猜。同一个 `"123"` 既可能是
// 数字也可能是编号，猜错了要么查不出来、要么查出一堆无关的东西。
//
// 分成几张表让类型成为**声明式**的：放进哪个表就是什么类型，
// 不需要额外的映射配置，也不存在「首次写入决定了类型」这种顺序相关的行为。
//
// 唯一的约束是字段名不能同时出现在两张表里——那是自相矛盾的，
// 会在写入前被拒绝。
//
// 如果手里只有 `map[string]any`（例如刚解出来的 JSON），
// 用 DocumentFromValues 按值的 Go/JSON 类型自动分流。
type Document struct {
	// ID 是调用方指定的业务 ID，也是唯一的文档标识。
	ID string `json:"id"`

	// Fields 是**文本**字段：经分析器切分后建倒排索引，
	// 支持词条查询与短语查询。不填类型的字段默认走这里。
	Fields map[string]string `json:"fields"`

	// Keywords 是**精确匹配**字段：不做分词，整个值作为一个词条。
	//
	// 适合 ID、标签、枚举——需要精确匹配、不能容忍分词副作用的值。
	// 与文本字段不同，它**不做停用词与最短长度过滤**：
	// "the" 或单字符标签作为精确值是合法的。
	Keywords map[string]string `json:"keywords,omitempty"`

	// Numbers 是数值字段：支持等值查询与范围查询。
	Numbers map[string]float64 `json:"numbers,omitempty"`

	// Dates 是时间字段：同样支持范围查询，内部按 **UTC epoch 毫秒** 存储。
	//
	// 内部精度是**毫秒**，比它更细的部分会被截断。
	Dates map[string]time.Time `json:"dates,omitempty"`
}

// Create 新建文档。ID 已存在时返回 index.ErrDocumentExists。
//
// 与 Upsert 分开是有意的：让「不小心覆盖已有文档」在 HTTP 层变成一个
// 明确的 409，而不是无声无息地覆盖掉别人的数据。
//
// 检查与写入在索引的同一把写锁内完成，因此不存在并发下的 TOCTOU 窗口。
//
// 启用持久化时，日志追加与索引写入在同一把写锁内完成：返回 nil
// 表示这次写**已经进入日志**，重启后仍然存在。
func (t *Table) Create(doc Document) error {
	if err := t.persist.check(); err != nil {
		return err
	}

	fields, err := t.normalize(doc)
	if err != nil {
		return err
	}
	_, err = t.idx.Add(doc.ID, fields)
	return err
}

// Upsert 写入文档：不存在则新建，存在则整体覆盖。
//
// created 报告这次是新建（true）还是覆盖（false）——
// HTTP 层据此决定返回 201 还是 200。
//
// 覆盖是**整体替换**：旧版本独有的词条会被完整摘除，不会留下幽灵命中。
func (t *Table) Upsert(doc Document) (created bool, err error) {
	if err := t.persist.check(); err != nil {
		return false, err
	}

	fields, err := t.normalize(doc)
	if err != nil {
		return false, err
	}
	_, created, err = t.idx.Upsert(doc.ID, fields)
	return created, err
}

// Delete 删除文档。
//
// 文档不存在时返回 index.ErrDocumentNotFound。
func (t *Table) Delete(id string) error {
	if err := t.persist.check(); err != nil {
		return err
	}
	return t.idx.Delete(id)
}

// GetDocument 取回文档；第二个返回值表示是否存在。
//
// 返回的是副本，调用方随便改都不会影响索引内部状态。
//
// 字段会按 schema **还原成各自的类型**：写进去是数字的，取出来还是数字，
// 而不是变成字符串 "42"。存进去什么样、取出来什么样，调用方才有
// 可靠的往返保证。
func (t *Table) GetDocument(id string) (Document, bool) {
	d, ok := t.idx.Get(id)
	if !ok {
		return Document{}, false
	}
	return t.toTypedDocument(d), true
}

// toTypedDocument 按 schema 把内部的文本形式还原成类型化文档。
//
// 遇到解析不了的值就**按文本原样给出**，不丢数据：
// 类型表与内容理论上不会不一致，但真出现时宁可给出原始值，
// 也不要静默把它变成零值。
func (t *Table) toTypedDocument(d *index.Document) Document {
	doc := Document{ID: d.External}
	schema := t.idx.Schema()

	for name, raw := range d.Fields {
		switch schema[name] {
		case index.FieldKeyword:
			doc.putKeyword(name, raw)

		case index.FieldNumber:
			if v, err := strconv.ParseFloat(raw, 64); err == nil {
				doc.putNumber(name, v)
				continue
			}
			doc.putText(name, raw)

		case index.FieldDate:
			if ms, err := index.ParseDate(raw); err == nil {
				doc.putDate(name, time.UnixMilli(ms).UTC())
				continue
			}
			doc.putText(name, raw)

		default:
			doc.putText(name, raw)
		}
	}
	return doc
}

// Fields 返回索引中出现过的字段名，已排序。
//
// 注意它只列出**建了倒排索引**的字段；数值与时间字段不进倒排，
// 因此不在这里。完整的字段类型表用 Schema。
func (t *Table) Fields() []string { return t.idx.Fields() }

// Schema 返回这张表的字段名到类型的映射。
//
// 返回的是对外的 FieldKind（字符串形式），不是内部的整数枚举——
// 调用方不该需要知道内部编码，更不该依赖它。
func (t *Table) Schema() map[string]FieldKind {
	internal := t.idx.Schema()
	out := make(map[string]FieldKind, len(internal))
	for name, kind := range internal {
		out[name] = toPublicKind(kind)
	}
	return out
}
