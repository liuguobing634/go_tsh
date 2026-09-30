package tsh

// Document 是对外的文档视图。
type Document struct {
	// ID 是调用方指定的业务 ID，也是唯一的文档标识。
	ID string `json:"id"`

	// Fields 是文档的各个字段，例如 title / body。
	Fields map[string]string `json:"fields"`
}

// Create 新建文档。ID 已存在时返回 index.ErrDocumentExists。
//
// 与 Upsert 分开是有意的：让「不小心覆盖已有文档」在 HTTP 层变成一个
// 明确的 409，而不是无声无息地覆盖掉别人的数据。
//
// 检查与写入在索引的同一把写锁内完成，因此不存在并发下的 TOCTOU 窗口。
func (e *Engine) Create(doc Document) error {
	_, err := e.idx.Add(doc.ID, doc.Fields)
	return err
}

// Upsert 写入文档：不存在则新建，存在则整体覆盖。
//
// created 报告这次是新建（true）还是覆盖（false）——
// HTTP 层据此决定返回 201 还是 200。
//
// 覆盖是**整体替换**：旧版本独有的词条会被完整摘除，不会留下幽灵命中。
func (e *Engine) Upsert(doc Document) (created bool, err error) {
	_, created, err = e.idx.Upsert(doc.ID, doc.Fields)
	return created, err
}

// Delete 删除文档。
//
// 文档不存在时返回 index.ErrDocumentNotFound。
func (e *Engine) Delete(id string) error {
	return e.idx.Delete(id)
}

// GetDocument 取回文档；第二个返回值表示是否存在。
//
// 返回的是副本，调用方随便改都不会影响索引内部状态。
func (e *Engine) GetDocument(id string) (Document, bool) {
	d, ok := e.idx.Get(id)
	if !ok {
		return Document{}, false
	}
	return Document{ID: d.External, Fields: d.Fields}, true
}

// Fields 返回索引中出现过的字段名，已排序。
func (e *Engine) Fields() []string { return e.idx.Fields() }
