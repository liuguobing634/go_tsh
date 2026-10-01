package index

import (
	"fmt"
	"slices"
)

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

// FieldKind 返回字段在快照中的类型；字段未声明时返回 false。
func (v *View) FieldKind(field string) (FieldKind, bool) {
	k, ok := v.ix.schema[field]
	return k, ok
}

// NumericRange 把某数值/时间字段上落在区间内的 DocID 追加到 dst。
//
// 闭区间由 includeLo / includeHi 控制；单边范围传 math.Inf 即可。
//
// **返回的 DocID 天然升序**（按 DocID 顺序扫描列），
// 因此可以直接当作求值器的命中列表使用，不需要排序或归并。
//
// 字段不存在或不是数值/时间类型时返回 ErrNotNumericField——
// 这类错误必须显式报出来。静默返回空结果会让用户以为「没搜到」，
// 而真正的原因是查询写错了字段类型。
func (v *View) NumericRange(field string, lo, hi float64, includeLo, includeHi bool, dst []DocID) ([]DocID, error) {
	kind, ok := v.ix.schema[field]
	if !ok {
		return dst, fmt.Errorf("%w: 字段 %q 未被声明为任何类型", ErrNotNumericField, field)
	}
	if !kind.Numeric() {
		return dst, fmt.Errorf("%w: 字段 %q 是 %s 类型", ErrNotNumericField, field, kind)
	}

	col := v.ix.numColumns[field]
	if col == nil {
		// 声明了数值类型但还没有任何文档写入过这个字段。
		return dst, nil
	}
	return col.rangeScan(lo, hi, includeLo, includeHi, dst), nil
}

// NumericValue 返回某文档在某数值字段上的值；无值或类型不符时返回 false。
func (v *View) NumericValue(id DocID, field string) (float64, bool) {
	col := v.ix.numColumns[field]
	if col == nil || int(id) >= len(col.values) || !col.present[id] {
		return 0, false
	}
	return col.values[id], true
}

// FieldLens 返回某字段按 DocID 下标的 token 数切片。
//
// 返回的是内部切片的**别名**（零拷贝），只在 View 的 fn 执行期间有效。
// 下标越界（已删除的文档，或该字段尚未覆盖到的 DocID）读到的是 0。
//
// 这是给检索热路径准备的：把字段维度提到循环外取一次，循环内就只剩
// 一次顺序友好的切片索引，省掉每条 posting 一次随机 map 查找。
// 10 万篇规模的 pprof 显示那正是当时的头号瓶颈（占 37% 累计耗时）。
func (v *View) FieldLens(field string) []int32 {
	return v.ix.docFieldLens[field]
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
// SortedDocIDs 返回快照中的全部 DocID，按升序排列。
//
// 用于「只有否定子句」的查询（例如 -foo）：没有任何正向子句时，
// 只能从全量文档出发再做排除。
//
// 刻意返回有序切片而不是用回调遍历 map：检索结果的归并依赖
// DocID 有序，map 的遍历顺序是随机的，拿它当起点会让后续
// 双指针归并全部失效。
func (v *View) SortedDocIDs() []DocID {
	out := make([]DocID, 0, len(v.ix.docs))
	for id := range v.ix.docs {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
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

// PostingFor 返回某文档在 (field, term) 下的词频与位置。
//
// 二分查找而非线性扫描。适合「已知文档、反查某词条位置」的零散查询；
// 若要在一个文档集合上做归并，请改用 Cursor——那才是顺序访问。
//
// positions 与 Scan 的约定一致：是索引内部切片的别名，
// 只在 View 的 fn 执行期间有效，不得保存或修改。
func (v *View) PostingFor(field, term string, id DocID) (tf uint32, positions []uint32, ok bool) {
	pl := v.ix.terms[TermKey(field, term)]
	if pl == nil {
		return 0, nil, false
	}

	i, found := pl.indexOf(id)
	if !found {
		return 0, nil, false
	}

	p := &pl.Postings[i]
	return p.TF, p.Positions, true
}

// PostingCursor 在一条有序 posting 列表上**单调前进**。
//
// 只能前进、不能回退，正是归并求交/求并需要的访问模式：
// 每个 posting 最多被访问一次，摊还 O(1)，而且是顺序访问、对缓存友好。
//
// 这正是 PostingFor 的反面：二分查找每次要在几万条记录里随机跳约 15 次，
// 实测是短语查询的首要瓶颈（pprof 显示 BinarySearchFunc 占 13.7%、
// 伴随的字符串 map 查找占 11.2%）。
type PostingCursor struct {
	list []Posting
	at   int
}

// Cursor 返回 (field, term) 上的游标。
// 词条不存在时返回一个空游标（Done 恒为 true）。
func (v *View) Cursor(field, term string) PostingCursor {
	pl := v.ix.terms[TermKey(field, term)]
	if pl == nil {
		return PostingCursor{}
	}
	return PostingCursor{list: pl.Postings}
}

// Done 报告游标是否已越界。
func (c *PostingCursor) Done() bool { return c.at >= len(c.list) }

// DocID 返回当前文档编号；越界时返回 InvalidDocID。
func (c *PostingCursor) DocID() DocID {
	if c.Done() {
		return InvalidDocID
	}
	return c.list[c.at].DocID
}

// TF 返回当前文档的词频；越界时为 0。
func (c *PostingCursor) TF() uint32 {
	if c.Done() {
		return 0
	}
	return c.list[c.at].TF
}

// Positions 返回当前位置切片；越界时为 nil。
//
// 与 Scan 一致，是索引内部切片的别名，只在 View 的 fn 执行期间有效。
func (c *PostingCursor) Positions() []uint32 {
	if c.Done() {
		return nil
	}
	return c.list[c.at].Positions
}

// Next 前进到下一个文档。
func (c *PostingCursor) Next() { c.at++ }

// Seek 单调前进直到 DocID >= id，返回是否**恰好**停在 id 上。
//
// 已越过 id 时不会回退，因此调用方必须按 DocID 递增的顺序使用它。
func (c *PostingCursor) Seek(id DocID) bool {
	for c.at < len(c.list) && c.list[c.at].DocID < id {
		c.at++
	}
	return c.at < len(c.list) && c.list[c.at].DocID == id
}
