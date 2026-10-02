package tsh

import "github.com/liuguobing/go_tsh/internal/index"

// 本文件是 Engine 上那些「作用在默认表上」的方法。
//
// 它们存在的唯一理由是**兼容**：引入表概念之前，所有操作都直接打在引擎上。
// 保留这组方法让既有调用点、既有 HTTP 路由、既有测试一行都不用改。
//
// 新代码应当用 e.Table(name) 拿到具体表的句柄，这样表名出现在调用点上，
// 读代码的人一眼能看出操作的是哪张表——而这些方法把表名藏在了默认值里。

// Create 在**默认表**上新建文档。ID 已存在时返回 index.ErrDocumentExists。
//
// 与 Upsert 分开是有意的：让「不小心覆盖已有文档」在 HTTP 层变成一个
// 明确的 409，而不是无声无息地覆盖掉别人的数据。
//
// 检查与写入在索引的同一把写锁内完成，因此不存在并发下的 TOCTOU 窗口。
//
// 启用持久化时，日志追加与索引写入在同一把写锁内完成：返回 nil
// 表示这次写**已经进入日志**，重启后仍然存在。
func (e *Engine) Create(doc Document) error { return e.defaultTable().Create(doc) }

// Upsert 在**默认表**上写入文档：不存在则新建，存在则整体覆盖。
//
// created 报告这次是新建（true）还是覆盖（false）——
// HTTP 层据此决定返回 201 还是 200。
//
// 覆盖是**整体替换**：旧版本独有的词条会被完整摘除，不会留下幽灵命中。
func (e *Engine) Upsert(doc Document) (created bool, err error) {
	return e.defaultTable().Upsert(doc)
}

// Delete 从**默认表**删除文档。
//
// 文档不存在时返回 index.ErrDocumentNotFound。
func (e *Engine) Delete(id string) error { return e.defaultTable().Delete(id) }

// GetDocument 从**默认表**取回文档；第二个返回值表示是否存在。
//
// 字段会按 schema 还原成各自的类型：写进去是数字的，取出来还是数字。
func (e *Engine) GetDocument(id string) (Document, bool) {
	return e.defaultTable().GetDocument(id)
}

// Search 在**默认表**上检索。
func (e *Engine) Search(req SearchRequest) (SearchResult, error) {
	return e.defaultTable().Search(req)
}

// ParseDocument 按**默认表**的字段类型表把 JSON 原生值转成文档。
func (e *Engine) ParseDocument(id string, values map[string]any) (Document, error) {
	return e.defaultTable().ParseDocument(id, values)
}

// Fields 返回**默认表**中出现过的字段名，已排序。
//
// 注意它只列出建了倒排索引的字段；数值与时间字段不进倒排，
// 因此不在这里。完整的字段类型表用 Schema。
func (e *Engine) Fields() []string { return e.defaultTable().Fields() }

// Schema 返回**默认表**的字段名到类型的映射。
func (e *Engine) Schema() map[string]FieldKind { return e.defaultTable().Schema() }

// Index 返回**默认表**的底层倒排索引。
func (e *Engine) Index() *index.InvertedIndex { return e.defaultTable().Index() }

// Stats 返回**默认表**的统计。
//
// 要看全部表的规模请用 TableStats。
func (e *Engine) Stats() Stats { return e.defaultTable().Stats() }
