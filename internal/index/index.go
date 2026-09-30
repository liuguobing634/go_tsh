// Package index 实现内存倒排索引：term -> posting list。
//
// term key 采用 "field\x00term" 编码，在扁平 map 中实现字段隔离，
// 字段级的 document frequency 与 BM25 统计天然独立，无需嵌套 map。
//
// # 并发模型
//
// 单把 sync.RWMutex：写操作（Add/Update/Upsert/Delete）持写锁，
// 读操作持读锁。典型场景写少读多，且分析文本的开销被刻意挪到锁外，
// 因此写锁只覆盖真正的结构修改，不会因为分词慢而阻塞别的写请求。
//
// # 字段隔离与位置语义
//
// 不同字段的 posting 完全独立，位置也各自从 0 开始。
// 因此短语查询必须在单个字段内判定——跨字段拼位置会产生假阳性。
// 跨字段检索应当分别求值再把分数相加。
package index

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/liuguobing/go_tsh/internal/analyzer"
)

// Change 描述一次**已经生效**的索引变更。
//
// 它只描述结果、不描述过程：Add / Update / Upsert 产生的都是
// 「这个 ID 现在的字段是这些」，因此重放时一律按 Upsert 处理即可，
// 不需要区分新建还是覆盖。
type Change struct {
	// External 是文档的外部 ID，不会为空。
	External string

	// Fields 是文档的全部字段。Deleted 为 true 时它没有意义。
	//
	// 这是索引内部持有的那份副本（写入后不再被修改），
	// 回调可以读取与保留，但**不得修改**。
	Fields map[string]string

	// Deleted 为 true 表示这是一次删除。
	Deleted bool
}

// Options 配置倒排索引；零值即为一套合理默认。
type Options struct {
	// Analyzer 用于切分文档，为 nil 时使用 analyzer.NewStandard()。
	//
	// 查询侧必须复用同一个实例（通过 Index.Analyzer() 取到），
	// 否则会出现「文档明明存在却检索不到」的归一化不一致。
	Analyzer analyzer.Analyzer

	// MaxFields 是单文档字段数上限，<= 0 时取 32。
	MaxFields int

	// MaxTokens 是单文档 token 数上限（所有字段合计），<= 0 时取 100000。
	MaxTokens int

	// OnApply 在**写锁内、变更已经生效之后**被调用。
	//
	// 存在的理由是原子性：变更和「把变更记到外部」（例如追加持久化日志）
	// 必须在同一把写锁里完成，否则并发写会让日志顺序与索引生效顺序错开——
	// 重放出来的状态就会和实际状态不一致。
	//
	// 为什么不让调用方自己在外面加一把锁：prepare（分析）刻意跑在写锁
	// **之外**，好让并发写能并行分析。在外面套锁会把这部分也串行化掉。
	//
	// 三条约定：
	//  1. 它持写锁运行，**必须足够快**，不要在里面做耗时工作；
	//  2. 回调失败时索引变更**不会回滚**——回滚要把刚做的插入/删除反向
	//     执行一遍，代价与出错风险都不划算。语义上应理解为「本次写未提交」：
	//     调用方把错误返回给客户端，重启后这次写不存在；
	//  3. 回调失败后调用方应当进入降级状态、直接拒绝后续写，
	//     否则索引会持续领先于日志，分歧越积越大。
	OnApply func(Change) error
}

const (
	defaultMaxFields = 32
	defaultMaxTokens = 100_000
)

// InvertedIndex 是并发安全的内存倒排索引。
type InvertedIndex struct {
	analyzer  analyzer.Analyzer
	maxFields int
	maxTokens int

	// onApply 见 Options.OnApply。构造后只读，因此无需加锁。
	onApply func(Change) error

	mu sync.RWMutex

	// terms 的 key 由 TermKey 生成。
	terms map[string]*PostingList
	// docs 按内部 DocID 索引。
	docs map[DocID]*Document
	// byExternal 是外部 ID 到内部 DocID 的映射。
	byExternal map[string]DocID

	fieldSet         map[string]struct{}
	fieldTotalTokens map[string]uint64 // 每个字段的 token 总数
	fieldDocs        map[string]uint32 // 含该字段的文档数

	// docFieldLens 按 DocID 稠密存放「某篇文档在某字段上有多少 token」。
	//
	// 为什么不复用 Document.FieldLen：那是 map，检索热路径上要对每条
	// posting 做一次随机 map 查找。10 万篇规模的 pprof 显示
	// docLengthLocked 占了 37.4% 的累计耗时，是绝对瓶颈。
	//
	// 换成按 DocID 下标的切片后，字段维度可以在循环外取一次，
	// 循环内只剩一次顺序友好的切片索引。
	docFieldLens map[string][]int32

	nextID      DocID
	totalTokens uint64

	// bytes 是索引常驻内存的**增量记账**结果。
	//
	// 早先是在 Stats() 里全量遍历算一遍。10 万篇语料下每次请求要跑
	// 约 1500 万次循环，实测 /api/v1/stats 的 p50 高达 105ms、p99 401ms——
	// 一个监控端点做成 O(索引规模)，在大索引上根本不能用。
	// 改成在写入路径上加减之后，Stats 变成 O(1)。
	bytes int64
}

// New 创建一个空索引。
func New(opts Options) *InvertedIndex {
	a := opts.Analyzer
	if a == nil {
		a = analyzer.NewStandard()
	}

	maxFields := opts.MaxFields
	if maxFields <= 0 {
		maxFields = defaultMaxFields
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	return &InvertedIndex{
		analyzer:         a,
		onApply:          opts.OnApply,
		maxFields:        maxFields,
		maxTokens:        maxTokens,
		terms:            make(map[string]*PostingList),
		docs:             make(map[DocID]*Document),
		byExternal:       make(map[string]DocID),
		fieldSet:         make(map[string]struct{}),
		fieldTotalTokens: make(map[string]uint64),
		fieldDocs:        make(map[string]uint32),
		docFieldLens:     make(map[string][]int32),
		nextID:           1,
	}
}

// TermKey 把字段名与词条编码为扁平 map 的 key。
//
// 用 NUL 分隔而非 "field:term" 这类可见分隔符：
// 分析器输出的词条只包含字母、数字与撇号，永远不含 NUL，
// 因此编码不会产生歧义（"a:b"+"c" 与 "a"+"b:c" 在冒号方案下会撞车）。
func TermKey(field, term string) string {
	return field + "\x00" + term
}

// SplitTermKey 是 TermKey 的逆运算，主要供调试与测试使用。
func SplitTermKey(key string) (field, term string, ok bool) {
	i := strings.IndexByte(key, 0)
	if i < 0 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// Analyzer 返回索引使用的分析器。查询侧必须复用它。
func (ix *InvertedIndex) Analyzer() analyzer.Analyzer { return ix.analyzer }

// ---------------------------------------------------------------- 写入路径

// analyzedField 是 prepare 阶段对单个字段的分析结果。
type analyzedField struct {
	field  string
	tokens []analyzer.Token
}

// preparedDoc 是「已校验、已分析、但尚未写入索引」的文档。
//
// 把分析与写入拆开，是为了让分词这段 CPU 密集工作跑在写锁之外。
type preparedDoc struct {
	external string
	fields   map[string]string
	fieldLen map[string]int
	analyzed []analyzedField
	total    int
}

// prepare 校验参数并分析所有字段。它不接触索引状态，因此无需持锁。
func (ix *InvertedIndex) prepare(external string, fields map[string]string) (*preparedDoc, error) {
	external = strings.TrimSpace(external)
	if external == "" {
		return nil, ErrEmptyExternalID
	}
	if len(fields) == 0 {
		return nil, ErrNoFields
	}
	if len(fields) > ix.maxFields {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooManyFields, len(fields), ix.maxFields)
	}

	p := &preparedDoc{
		external: external,
		fields:   make(map[string]string, len(fields)),
		fieldLen: make(map[string]int, len(fields)),
		analyzed: make([]analyzedField, 0, len(fields)),
	}

	// 按字段名排序后处理：同样的输入永远产生同样的索引布局，
	// 出现问题时可以稳定复现。
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, ErrEmptyFieldName
		}

		tokens := ix.analyzer.Analyze(fields[name])
		p.fields[name] = fields[name]
		p.fieldLen[name] = len(tokens)
		p.total += len(tokens)

		if p.total > ix.maxTokens {
			return nil, fmt.Errorf("%w: 已超过 %d", ErrDocumentTooLarge, ix.maxTokens)
		}

		p.analyzed = append(p.analyzed, analyzedField{field: name, tokens: tokens})
	}

	return p, nil
}

// Add 新建文档。外部 ID 已存在时返回 ErrDocumentExists。
func (ix *InvertedIndex) Add(external string, fields map[string]string) (DocID, error) {
	p, err := ix.prepare(external, fields)
	if err != nil {
		return InvalidDocID, err
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	if _, exists := ix.byExternal[p.external]; exists {
		return InvalidDocID, fmt.Errorf("%w: %q", ErrDocumentExists, p.external)
	}

	id := ix.insertLocked(p)
	if err := ix.notifyApply(Change{External: p.external, Fields: p.fields}); err != nil {
		return InvalidDocID, err
	}
	return id, nil
}

// Update 覆盖式更新。文档不存在时返回 ErrDocumentNotFound。
//
// 更新等价于「先完整摘除旧版本，再插入新版本」，
// 因此旧版本独有的词条不会留下幽灵命中。
func (ix *InvertedIndex) Update(external string, fields map[string]string) (DocID, error) {
	p, err := ix.prepare(external, fields)
	if err != nil {
		return InvalidDocID, err
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	old, ok := ix.lookupLocked(p.external)
	if !ok {
		return InvalidDocID, fmt.Errorf("%w: %q", ErrDocumentNotFound, p.external)
	}

	ix.removeLocked(old)

	id := ix.insertLocked(p)
	if err := ix.notifyApply(Change{External: p.external, Fields: p.fields}); err != nil {
		return InvalidDocID, err
	}
	return id, nil
}

// Upsert 存在则覆盖、不存在则新建，created 表示是否为新建。
func (ix *InvertedIndex) Upsert(external string, fields map[string]string) (id DocID, created bool, err error) {
	p, err := ix.prepare(external, fields)
	if err != nil {
		return InvalidDocID, false, err
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	if old, ok := ix.lookupLocked(p.external); ok {
		ix.removeLocked(old)
	} else {
		created = true
	}

	id = ix.insertLocked(p)
	if err := ix.notifyApply(Change{External: p.external, Fields: p.fields}); err != nil {
		return InvalidDocID, false, err
	}
	return id, created, nil
}

// Delete 删除文档。文档不存在时返回 ErrDocumentNotFound。
func (ix *InvertedIndex) Delete(external string) error {
	external = strings.TrimSpace(external)
	if external == "" {
		return ErrEmptyExternalID
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	doc, ok := ix.lookupLocked(external)
	if !ok {
		return fmt.Errorf("%w: %q", ErrDocumentNotFound, external)
	}

	ix.removeLocked(doc)
	return ix.notifyApply(Change{External: external, Deleted: true})
}

// notifyApply 在写锁内通知外部「索引已经变更」。调用方必须持有写锁。
func (ix *InvertedIndex) notifyApply(c Change) error {
	if ix.onApply == nil {
		return nil
	}
	return ix.onApply(c)
}

// insertLocked 把已分析好的文档写入索引。调用方必须持有写锁。
func (ix *InvertedIndex) insertLocked(p *preparedDoc) DocID {
	id := ix.nextID
	ix.nextID++

	ix.docs[id] = &Document{
		ID:       id,
		External: p.external,
		Fields:   p.fields,
		FieldLen: p.fieldLen,
		TotalLen: p.total,
	}
	ix.byExternal[p.external] = id

	for _, af := range p.analyzed {
		ix.fieldSet[af.field] = struct{}{}
		ix.fieldTotalTokens[af.field] += uint64(len(af.tokens))
		ix.fieldDocs[af.field]++
		ix.setDocFieldLenLocked(af.field, id, len(af.tokens))
		ix.insertFieldLocked(af.field, id, af.tokens)
	}

	ix.totalTokens += uint64(p.total)
	ix.bytes += docBytes(ix.docs[id])
	return id
}

// setDocFieldLenLocked 写入「某文档在某字段上有多少 token」。
// 调用方必须持有写锁。
//
// 切片按 2 倍扩容。DocID 是逐个递增的，若每次都按需精确扩容，
// 每插入一篇就要重新分配并拷贝整条切片，退化成 O(n²)。
func (ix *InvertedIndex) setDocFieldLenLocked(field string, id DocID, n int) {
	lens := ix.docFieldLens[field]

	if int(id) >= len(lens) {
		size := max(len(lens)*2, int(id)+1, 64)
		grown := make([]int32, size)
		copy(grown, lens)
		lens = grown
		ix.docFieldLens[field] = lens
	}

	lens[id] = int32(n)
}

// insertFieldLocked 把一个字段的 token 流写成 posting。
// 调用方必须持有写锁。
func (ix *InvertedIndex) insertFieldLocked(field string, id DocID, tokens []analyzer.Token) {
	if len(tokens) == 0 {
		return
	}

	// 按词条排序后顺序扫描分组，避免为每个词条建一次 map 条目。
	//
	// 必须用「稳定」排序：同一词条的多个位置原本就按位置递增，
	// 稳定排序保证分组后 Positions 仍然递增——短语查询依赖这一性质。
	slices.SortStableFunc(tokens, func(a, b analyzer.Token) int {
		return strings.Compare(a.Term, b.Term)
	})

	for i := 0; i < len(tokens); {
		j := i + 1
		for j < len(tokens) && tokens[j].Term == tokens[i].Term {
			j++
		}

		positions := make([]uint32, 0, j-i)
		for k := i; k < j; k++ {
			positions = append(positions, tokens[k].Position)
		}

		key := TermKey(field, tokens[i].Term)
		pl := ix.terms[key]
		if pl == nil {
			pl = &PostingList{}
			ix.terms[key] = pl
			ix.bytes += termKeyBytes(key)
		}

		// DocID 单调递增，且删除会整条摘除，因此 append 即保持有序。
		pl.Postings = append(pl.Postings, Posting{
			DocID:     id,
			TF:        uint32(j - i),
			Positions: positions,
		})
		pl.DF++
		ix.bytes += postingBytes(len(positions))

		i = j
	}
}

// removeLocked 把文档从索引中彻底摘除。调用方必须持有写锁。
//
// 这里选择「重新分析原文」来定位需要清理的 posting，而不是在文档上
// 常驻一份 term key 列表：后者在 10 万文档量级要多吃数百 MB 常驻内存，
// 而删除是低频操作，重算一遍分词的 CPU 成本远比常驻内存划算。
func (ix *InvertedIndex) removeLocked(doc *Document) {
	for field, text := range doc.Fields {
		for _, t := range ix.analyzer.Analyze(text) {
			key := TermKey(field, t.Term)
			pl := ix.terms[key]
			if pl == nil {
				continue
			}
			// 同一词条会被多次命中。removePostingLocked 对不存在的项是
			// no-op，因此重复调用安全，DF 也只会减一次。
			ix.removePostingLocked(key, pl, doc.ID)
		}
	}

	ix.bytes -= docBytes(doc)
	delete(ix.docs, doc.ID)
	delete(ix.byExternal, doc.External)
	ix.totalTokens -= uint64(doc.TotalLen)

	for field, n := range doc.FieldLen {
		ix.fieldTotalTokens[field] -= uint64(n)
		if ix.fieldDocs[field] > 0 {
			ix.fieldDocs[field]--
		}
		// 稠密切片里的条目必须归零：DocID 不复用，但持有旧 DocID 的
		// 调用方仍可能来查，读到已删除文档的长度会很意外。
		ix.setDocFieldLenLocked(field, doc.ID, 0)
		// 字段彻底空了就回收，否则 Stats().Fields 会一直虚高。
		if ix.fieldTotalTokens[field] == 0 && ix.fieldDocs[field] == 0 {
			delete(ix.fieldTotalTokens, field)
			delete(ix.fieldDocs, field)
			delete(ix.fieldSet, field)
		}
	}
}

// removePostingLocked 从单条 posting 列表中摘除某文档。
// 调用方必须持有写锁。列表被清空时连同 key 一起回收。
func (ix *InvertedIndex) removePostingLocked(key string, pl *PostingList, id DocID) {
	i, found := pl.indexOf(id)
	if !found {
		return
	}

	ix.bytes -= postingBytes(len(pl.Postings[i].Positions))

	pl.Postings = append(pl.Postings[:i], pl.Postings[i+1:]...)
	if pl.DF > 0 {
		pl.DF--
	}

	if len(pl.Postings) == 0 {
		delete(ix.terms, key)
		ix.bytes -= termKeyBytes(key)
	}
}

// lookupLocked 按外部 ID 查找文档。调用方必须持锁（读锁或写锁）。
func (ix *InvertedIndex) lookupLocked(external string) (*Document, bool) {
	id, ok := ix.byExternal[external]
	if !ok {
		return nil, false
	}
	doc, ok := ix.docs[id]
	return doc, ok
}

// ---------------------------------------------------------------- 读取路径

// Get 返回文档的深拷贝；第二个返回值表示是否存在。
func (ix *InvertedIndex) Get(external string) (*Document, bool) {
	external = strings.TrimSpace(external)
	if external == "" {
		return nil, false
	}

	ix.mu.RLock()
	defer ix.mu.RUnlock()

	doc, ok := ix.lookupLocked(external)
	if !ok {
		return nil, false
	}
	return doc.Clone(), true
}

// Postings 返回 (field, term) 的 posting 列表副本。
//
// 返回副本而不是内部切片，调用方可以在锁外安全使用。
// 但代价不小：每条记录都会深拷贝一次 Positions。实测 1 万条命中的词条
// 要花 0.7ms / 400KB / 1 万次分配，一次 3 词查询就会击穿延迟预算。
//
// 检索热路径请改用 View + View.Scan——那是零拷贝的一致快照。
func (ix *InvertedIndex) Postings(field, term string) []Posting {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	return ix.postingsLocked(field, term)
}

// postingsLocked 是 Postings 的实现，调用方必须持锁。
func (ix *InvertedIndex) postingsLocked(field, term string) []Posting {
	pl := ix.terms[TermKey(field, term)]
	if pl == nil {
		return nil
	}

	out := make([]Posting, len(pl.Postings))
	for i, p := range pl.Postings {
		cp := p
		cp.Positions = slices.Clone(p.Positions)
		out[i] = cp
	}
	return out
}

// DocFreq 返回 (field, term) 的文档频率；不存在时为 0。
func (ix *InvertedIndex) DocFreq(field, term string) uint32 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	return ix.docFreqLocked(field, term)
}

// docFreqLocked 是 DocFreq 的实现，调用方必须持锁。
func (ix *InvertedIndex) docFreqLocked(field, term string) uint32 {
	if pl := ix.terms[TermKey(field, term)]; pl != nil {
		return pl.DF
	}
	return 0
}

// DocCount 返回当前文档数。
func (ix *InvertedIndex) DocCount() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.docs)
}

// Has 报告外部 ID 对应的文档是否存在。
func (ix *InvertedIndex) Has(external string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	_, ok := ix.lookupLocked(strings.TrimSpace(external))
	return ok
}

// Fields 返回出现过的全部字段名，已排序。
func (ix *InvertedIndex) Fields() []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	return ix.fieldsLocked()
}

// fieldsLocked 是 Fields 的实现，调用方必须持锁。
func (ix *InvertedIndex) fieldsLocked() []string {
	out := make([]string, 0, len(ix.fieldSet))
	for f := range ix.fieldSet {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

// FieldStats 返回某个字段的统计量。字段不存在时返回 false。
func (ix *InvertedIndex) FieldStats(field string) (FieldStats, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	return ix.fieldStatsLocked(field)
}

// fieldStatsLocked 是 FieldStats 的实现，调用方必须持锁。
func (ix *InvertedIndex) fieldStatsLocked(field string) (FieldStats, bool) {
	docs := ix.fieldDocs[field]
	if docs == 0 {
		return FieldStats{}, false
	}

	total := ix.fieldTotalTokens[field]
	return FieldStats{
		Field:       field,
		Docs:        int(docs),
		TotalTokens: total,
		AvgLength:   float64(total) / float64(docs),
	}, true
}

// DocLength 返回某文档某字段的 token 数。
func (ix *InvertedIndex) DocLength(id DocID, field string) int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	return ix.docLengthLocked(id, field)
}

// docLengthLocked 是 DocLength 的实现，调用方必须持锁。
//
// 走 docFieldLens 稠密切片而不是 Document.FieldLen map：
// 后者要在检索热路径上对每条 posting 做一次随机 map 查找。
func (ix *InvertedIndex) docLengthLocked(id DocID, field string) int {
	lens, ok := ix.docFieldLens[field]
	if !ok || int(id) >= len(lens) {
		return 0
	}
	return int(lens[id])
}

// Stats 返回索引规模统计。
func (ix *InvertedIndex) Stats() Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	s := Stats{
		Docs:        len(ix.docs),
		Terms:       len(ix.terms),
		Fields:      len(ix.fieldSet),
		TotalTokens: ix.totalTokens,
	}
	if s.Docs > 0 {
		s.AvgDocLen = float64(ix.totalTokens) / float64(s.Docs)
	}
	s.IndexBytes = ix.bytes + ix.docFieldLensBytes()
	return s
}

// 内存估算用的粗略常量。
//
// 只求量级可信：字符串按 len 计，容器按元素数与指针宽度计，
// 不含 map 桶、分配器对齐、字符串去重等开销，因此结果偏保守。
const (
	postingSizeBytes  = 24 // DocID + TF + Positions 切片头
	positionSizeBytes = 4  // uint32
	stringHeaderBytes = 16
	docFieldLenBytes  = 8 + stringHeaderBytes // FieldLen map 的一个条目
)

// termKeyBytes 是扁平 term map 里一个 key 的占用。
func termKeyBytes(key string) int64 {
	return int64(len(key)) + stringHeaderBytes + postingSizeBytes
}

// postingBytes 是一条 posting 里 Positions 底层的占用。
// Posting 结构体本身已经算在 termKeyBytes 的 postingSizeBytes 里。
func postingBytes(positions int) int64 {
	return int64(positions) * positionSizeBytes
}

// docBytes 是一篇文档在文档表里的占用。
func docBytes(doc *Document) int64 {
	n := int64(len(doc.External)) + stringHeaderBytes

	for k, v := range doc.Fields {
		n += int64(len(k)+len(v)) + 2*stringHeaderBytes
	}
	n += int64(len(doc.FieldLen)) * docFieldLenBytes

	return n
}

// docFieldLensBytes 是稠密词长切片的总占用。
//
// 它按字段分配、随 DocID 增长，且删除时不会缩容，
// 因此只能在 Stats 里按当前长度汇总，无法完全增量维护。
func (ix *InvertedIndex) docFieldLensBytes() int64 {
	var n int64
	for _, lens := range ix.docFieldLens {
		n += int64(len(lens)) * 4
	}
	return n
}
