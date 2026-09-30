package index

// View 是一次一致性只读快照的访问入口。
//
// # 为什么需要它
//
// Scan 这类零拷贝接口各自持锁。在同一个 goroutine 里反复调用并不会死锁
// （每次调用返回前都已释放锁），但两次调用之间可能有写操作插进来，
// 于是整次检索看到的是**不同时刻**的索引状态。对 AND 查询而言，
// 这会产生「文档 A 在前一个词条的 posting 里、却不在后一个里」
// 这种无法向用户解释的结果。
//
// View 把整次检索放进同一把读锁：既保证快照一致，又保持零拷贝。
//
// # 代价
//
// fn 执行期间写操作会被阻塞。所以 fn 里只应做检索本身，
// 不要在其中做 IO、高亮渲染等耗时工作——把那些挪到 View 之外，
// 只把命中的少量文档带出去即可。
type View struct {
	ix *InvertedIndex
}

// View 在读锁保护下执行 fn，fn 内看到一致的索引状态。
//
// fn 内**绝不可**调用索引的写方法（Add / Update / Upsert / Delete），
// 否则会死锁（RWMutex 不可重入）。
func (ix *InvertedIndex) View(fn func(v *View)) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	fn(&View{ix: ix})
}

// DocCount 返回快照中的文档数。
func (v *View) DocCount() int { return len(v.ix.docs) }

// Fields 返回快照中出现过的全部字段名，已排序。
func (v *View) Fields() []string { return v.ix.fieldsLocked() }

// FieldStats 返回某字段的统计量；字段不存在时返回 false。
//
// BM25 的 IDF 必须按字段计算：同一个词在 title 与 body 中的稀有程度
// 完全不同，混在一起算会让短字段的高信息量被长字段稀释。
func (v *View) FieldStats(field string) (FieldStats, bool) {
	return v.ix.fieldStatsLocked(field)
}

// DocFreq 返回 (field, term) 在快照中的文档频率。
func (v *View) DocFreq(field, term string) uint32 {
	return v.ix.docFreqLocked(field, term)
}

// DocLength 返回某文档某字段的 token 数。
func (v *View) DocLength(id DocID, field string) int {
	return v.ix.docLengthLocked(id, field)
}

// Document 返回文档的深拷贝，可安全带出 View 之外。
func (v *View) Document(id DocID) (*Document, bool) {
	doc, ok := v.ix.docs[id]
	if !ok {
		return nil, false
	}
	return doc.Clone(), true
}

// EachDocument 以不确定的顺序遍历快照中的全部文档。
//
// 用于「只有否定子句」的查询（例如 -foo）：没有任何正向子句时，
// 只能从全量文档出发再做排除。fn 返回 false 时提前结束。
//
// 在 View 内部调用 fn 不会重入加锁，因此是安全的。
func (v *View) EachDocument(fn func(id DocID) bool) {
	for id := range v.ix.docs {
		if !fn(id) {
			return
		}
	}
}

// Scan 遍历 (field, term) 的 posting，零拷贝。
//
// fn 返回 false 时提前结束遍历。
//
// positions 是索引内部切片的**别名**，只在 fn 执行期间有效：
// 不得保存、不得在 fn 返回后使用、不得修改。
// 这一点与 Postings 返回深拷贝的语义正好相反，
// 换取的是整条检索路径上零分配。
func (v *View) Scan(field, term string, fn func(id DocID, tf uint32, positions []uint32) bool) {
	pl := v.ix.terms[TermKey(field, term)]
	if pl == nil {
		return
	}

	for i := range pl.Postings {
		p := &pl.Postings[i]
		if !fn(p.DocID, p.TF, p.Positions) {
			return
		}
	}
}

// ScanIDs 是 Scan 的轻量版本，只暴露 DocID 与 TF。
//
// 普通词条查询不需要位置信息，用这个可以明确表达意图，
// 也避免调用方无意中持有 positions 的别名。
func (v *View) ScanIDs(field, term string, fn func(id DocID, tf uint32) bool) {
	v.Scan(field, term, func(id DocID, tf uint32, _ []uint32) bool {
		return fn(id, tf)
	})
}
