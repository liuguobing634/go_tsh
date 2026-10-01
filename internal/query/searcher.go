package query

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/liuguobing/go_tsh/internal/analyzer"
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/scoring"
)

// Hit 是一条命中结果。
type Hit = scoring.Hit[index.DocID]

// Result 是一次检索的结果。
type Result struct {
	// Total 是命中文档总数（分页之前）。
	Total int

	// Hits 是当前页结果，按「分数降序、同分 DocID 升序」排列。
	//
	// 没有这条确定性约定，同分文档的先后会随内部遍历顺序漂移，
	// 分页会出现重复或漏项。
	Hits []Hit
}

// SearchOptions 配置一次检索。
type SearchOptions struct {
	// Fields 限定检索字段；为空表示全部字段。
	//
	// 不存在的字段会被静默忽略——调用方若想区分「字段名写错」
	// 与「该字段下确实没有命中」，可以先用 Index().Fields() 校验。
	Fields []string

	// Limit 是返回条数上限，<= 0 时取 10，超过 100 会被截到 100。
	Limit int

	// Offset 是跳过的条数。
	Offset int
}

const (
	defaultLimit = 10
	maxLimit     = 100
	maxOffset    = 1_000_000
)

// ErrNilQuery 表示传入了空的语法树。
var ErrNilQuery = errors.New("query: 语法树为 nil")

// Searcher 在倒排索引上执行查询语法树。
//
// 并发安全：所有状态都是只读的，每次 Search 各自在 index.View 内工作。
// 但注意 View 会持有索引读锁，所以 Searcher 的并发度最终受索引锁约束。
type Searcher struct {
	ix  *index.InvertedIndex
	anz analyzer.Analyzer
	bm  scoring.BM25
}

// NewSearcher 创建检索器。
//
// Analyzer 一律取自索引本身（ix.Analyzer()），这是硬性要求：
// 查询侧与写入侧只要用了不同的分析器，就会出现
// 「文档明明存在却检索不到」这种最难排查的故障。
//
// bm 为零值时使用 scoring.DefaultBM25()。
func NewSearcher(ix *index.InvertedIndex, bm scoring.BM25) *Searcher {
	if bm.K1 == 0 && bm.B == 0 {
		bm = scoring.DefaultBM25()
	}
	return &Searcher{ix: ix, anz: ix.Analyzer(), bm: bm}
}

// Search 执行一次检索。
func (s *Searcher) Search(n Node, opts SearchOptions) (Result, error) {
	if n == nil {
		return Result{}, ErrNilQuery
	}

	limit := clampLimit(opts.Limit)
	offset := clampOffset(opts.Offset)

	var (
		hits    hitList
		evalErr error
	)
	s.ix.View(func(v *index.View) {
		// ⚠️ 这里**不能**因为 fields 为空就提前返回。
		//
		// resolveFields 返回的是「产生了 posting 的字段」，而数值/时间字段
		// 根本不进倒排。一篇只有数值字段的文档会让 fields 为空——
		// 一旦提前返回，范围查询会被整段跳过：既查不到东西，
		// 也不会报「字段类型不对」，静默给出 0 结果。
		fields := resolveFields(v, opts.Fields)
		hits, evalErr = s.eval(v, n, fields)
	})
	if evalErr != nil {
		return Result{}, evalErr
	}

	total := hits.len()
	if total == 0 {
		return Result{Total: 0}, nil
	}

	// 只保留 offset+limit 条：分页不需要对全部命中排序。
	tk := scoring.NewTopK[index.DocID](offset + limit)
	for i, id := range hits.ids {
		tk.Push(id, hits.scores[i])
	}

	ranked := tk.Result()
	if offset >= len(ranked) {
		return Result{Total: total}, nil
	}
	return Result{Total: total, Hits: ranked[offset:]}, nil
}

// QueryTerms 收集语法树里会被**正向匹配**的归一化词条，供高亮使用。
//
// 刻意跳过 MustNot 分支：被排除的词条显然不该在结果里高亮。
//
// 返回的词条已按索引的分析器归一化，与索引里的形态一致——
// 直接拿用户输入的原始词去高亮是匹配不上的，例如原文的 "Go" 归一化成 "go"。
func (s *Searcher) QueryTerms(n Node) []string {
	var (
		out  []string
		seen = make(map[string]struct{})
	)

	collect := func(text string) {
		for _, tok := range s.anz.Analyze(text) {
			if _, dup := seen[tok.Term]; dup {
				continue
			}
			seen[tok.Term] = struct{}{}
			out = append(out, tok.Term)
		}
	}

	var walk func(Node)
	walk = func(node Node) {
		switch v := node.(type) {
		case *Term:
			collect(v.Text)
		case *Phrase:
			collect(v.Raw)
		case *FieldTerm:
			// 走文本侧的规则。**已知不足**：keyword 字段的词条是
			// 「小写化的整值」，这里分析不出它，因此 keyword 字段
			// 目前拿不到高亮。数值范围查询本来就没有可高亮的词条，
			// 所以 FieldRange 直接跳过。
			collect(v.Text)
		case *FieldRange:
			// 无词条可高亮
		case *Bool:
			for _, c := range v.Must {
				walk(c)
			}
			for _, c := range v.Should {
				walk(c)
			}
			// MustNot 刻意跳过。
		}
	}

	walk(n)
	return out
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return defaultLimit
	case n > maxLimit:
		return maxLimit
	default:
		return n
	}
}

func clampOffset(n int) int {
	switch {
	case n < 0:
		return 0
	case n > maxOffset:
		return maxOffset
	default:
		return n
	}
}

// resolveFields 把调用方想要的字段收敛成索引里真实存在的字段。
func resolveFields(v *index.View, want []string) []string {
	if len(want) == 0 {
		return v.Fields()
	}

	out := make([]string, 0, len(want))
	for _, f := range want {
		if _, ok := v.FieldStats(f); ok {
			out = append(out, f)
		}
	}
	return out
}

// docLenAt 从 View.FieldLens 的稠密切片里取某文档在某字段上的 token 数。
// 越界（已删除的文档，或该字段尚未覆盖到的 DocID）视为 0。
func docLenAt(lens []int32, id index.DocID) float64 {
	if int(id) >= len(lens) {
		return 0
	}
	return float64(lens[id])
}

// ---------------------------------------------------------------- 命中集合

// hitList 是按 DocID **升序**排列的命中集合，scores 与 ids 一一对应。
//
// 为什么不用 map[DocID]float64 当累加器：
// posting 列表本身就按 DocID 有序，单字段求值天然产出有序结果，
// 于是求交 / 求并 / 求差全部可以走双指针归并——O(n)、无哈希、顺序访问。
//
// 早先图省事用了 map，2 万条命中就是 4 万次 map 读写，
// `SearchAnd` 实测 26ms，10 万篇量级必然击穿 20ms 预算。
//
// 不变量：所有返回 hitList 的求值函数都必须保证 ids 严格升序。
// 这条不变量是整个归并体系成立的前提。
type hitList struct {
	ids    []index.DocID
	scores []float64
}

func (h hitList) len() int { return len(h.ids) }

// add 追加一条命中。调用方必须按 DocID 升序追加。
func (h *hitList) add(id index.DocID, score float64) {
	h.ids = append(h.ids, id)
	h.scores = append(h.scores, score)
}

func newHitList(capacity int) hitList {
	if capacity < 0 {
		capacity = 0
	}
	return hitList{
		ids:    make([]index.DocID, 0, capacity),
		scores: make([]float64, 0, capacity),
	}
}

// unionAll 求并集，重复文档的分数相加。输入必须各自有序。
func unionAll(sets []hitList) hitList {
	switch len(sets) {
	case 0:
		return hitList{}
	case 1:
		return sets[0]
	}

	acc := sets[0]
	for _, s := range sets[1:] {
		acc = mergeUnion(acc, s)
	}
	return acc
}

func mergeUnion(a, b hitList) hitList {
	out := newHitList(a.len() + b.len())

	i, j := 0, 0
	for i < a.len() && j < b.len() {
		switch {
		case a.ids[i] < b.ids[j]:
			out.add(a.ids[i], a.scores[i])
			i++
		case a.ids[i] > b.ids[j]:
			out.add(b.ids[j], b.scores[j])
			j++
		default:
			out.add(a.ids[i], a.scores[i]+b.scores[j])
			i++
			j++
		}
	}
	for ; i < a.len(); i++ {
		out.add(a.ids[i], a.scores[i])
	}
	for ; j < b.len(); j++ {
		out.add(b.ids[j], b.scores[j])
	}

	return out
}

// intersectAll 求交集，命中文档的分数相加。输入必须各自有序。
func intersectAll(sets []hitList) hitList {
	switch len(sets) {
	case 0:
		return hitList{}
	case 1:
		return sets[0]
	}

	// 从最短的集合开始，尽早把中间结果收敛到空。
	order := make([]int, len(sets))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(x, y int) int {
		return cmp.Compare(sets[x].len(), sets[y].len())
	})

	acc := sets[order[0]]
	for _, i := range order[1:] {
		acc = mergeIntersect(acc, sets[i])
		if acc.len() == 0 {
			break
		}
	}
	return acc
}

func mergeIntersect(a, b hitList) hitList {
	out := newHitList(min(a.len(), b.len()))

	i, j := 0, 0
	for i < a.len() && j < b.len() {
		switch {
		case a.ids[i] < b.ids[j]:
			i++
		case a.ids[i] > b.ids[j]:
			j++
		default:
			out.add(a.ids[i], a.scores[i]+b.scores[j])
			i++
			j++
		}
	}
	return out
}

// subtract 返回 a 中不在 b 里的文档。两者都必须有序。
func subtract(a, b hitList) hitList {
	out := newHitList(a.len())

	j := 0
	for i := 0; i < a.len(); i++ {
		for j < b.len() && b.ids[j] < a.ids[i] {
			j++
		}
		if j < b.len() && b.ids[j] == a.ids[i] {
			continue
		}
		out.add(a.ids[i], a.scores[i])
	}
	return out
}

// addExisting 只给 dst 中**已有**的文档加分，不引入新文档。
// 两者都必须有序。用于「Must 非空时 Should 只加分」。
func addExisting(dst *hitList, src hitList) {
	i, j := 0, 0
	for i < dst.len() && j < src.len() {
		switch {
		case dst.ids[i] < src.ids[j]:
			i++
		case dst.ids[i] > src.ids[j]:
			j++
		default:
			dst.scores[i] += src.scores[j]
			i++
			j++
		}
	}
}

// ---------------------------------------------------------------- 求值

// eval 求值一个节点。返回的 hitList 保证按 DocID 升序。
//
// 返回错误是为了字段类子句：对文本字段做范围查询、数值字段上写了
// 无法解析的值，这类问题**必须报出来**。静默返回空结果会让用户
// 以为「没搜到」，而真正的原因是查询写错了——这是最难自查的一类失败。
func (s *Searcher) eval(v *index.View, n Node, fields []string) (hitList, error) {
	switch node := n.(type) {
	case *Term:
		return s.evalTerm(v, node, fields), nil
	case *Phrase:
		return s.evalPhrase(v, node, fields), nil
	case *FieldTerm:
		return s.evalFieldTerm(v, node, fields)
	case *FieldRange:
		return s.evalFieldRange(v, node, fields)
	case *Bool:
		return s.evalBool(v, node, fields)
	default:
		// 解析器只会产出上面几种节点；走到这里说明有人手工构造了 AST。
		return hitList{}, nil
	}
}

// evalTerm 处理单词条节点。
func (s *Searcher) evalTerm(v *index.View, t *Term, fields []string) hitList {
	tokens := s.anz.Analyze(t.Text)

	switch len(tokens) {
	case 0:
		// 整个词被分析器吃掉了（例如只由停用词或过短字符组成）。
		return hitList{}
	case 1:
		return s.evalSingleTerm(v, tokens[0].Term, fields)
	default:
		// 一个词被切成多个 token，例如 "full-width" -> full + width。
		// 按短语处理最符合直觉：用户写的是一个整体，不应拆成 OR。
		return s.evalPhraseTokens(v, tokens, fields)
	}
}

// evalPhrase 处理双引号短语。
func (s *Searcher) evalPhrase(v *index.View, p *Phrase, fields []string) hitList {
	tokens := s.anz.Analyze(p.Raw)
	if len(tokens) == 0 {
		return hitList{}
	}
	return s.evalPhraseTokens(v, tokens, fields)
}

// evalSingleTerm 求一个已归一化的词条在指定字段上的命中与分数。
//
// 同一词条出现在多个字段时，各字段的贡献相加。注意每个字段的 IDF
// 是**分开算**的：同一个词在 title 与 body 里的稀有程度完全不同，
// 混在一起算会让短字段的高信息量被长字段稀释。
func (s *Searcher) evalSingleTerm(v *index.View, term string, fields []string) hitList {
	var sets []hitList

	for _, field := range fields {
		fs, ok := v.FieldStats(field)
		if !ok {
			continue
		}
		df := v.DocFreq(field, term)
		if df == 0 {
			continue
		}

		idf := s.bm.IDF(df, uint32(fs.Docs))
		avgLen := fs.AvgLength

		// 把字段维度提到循环外取一次：循环内只剩切片索引，
		// 不再对每条 posting 做一次随机 map 查找。
		lens := v.FieldLens(field)

		// posting 有序，因此这里按序追加即得到有序 hitList。
		hl := newHitList(int(df))
		v.ScanIDs(field, term, func(id index.DocID, tf uint32) bool {
			hl.add(id, s.bm.Score(idf, tf, docLenAt(lens, id), avgLen))
			return true
		})

		if hl.len() > 0 {
			sets = append(sets, hl)
		}
	}

	return unionAll(sets)
}

// evalPhraseTokens 按「相对位置一致」判定短语命中。
//
// 关键点一：要求的**不是位置差为 1**，而是位置差等于查询侧分析出的位置差。
// 例如 "quick the brown" 分析后是 quick@0、brown@2（the 被停用词过滤、
// 但位置仍被占用），所以文档里也必须相隔 2 才算命中。
// 若简单按「相隔 1」实现，就会命中 "quick brown"，反倒是错的。
//
// 关键点二：用**游标归并**而不是「扫锚点 + 逐文档二分反查」。
// 后者在 2 万篇规模下实测要 48ms（pprof 显示 BinarySearchFunc 占 13.7%、
// 伴随的字符串 map 查找占 11.2%）。改成归并后每个 posting 只被访问一次，
// 且是顺序访问。
func (s *Searcher) evalPhraseTokens(v *index.View, tokens []analyzer.Token, fields []string) hitList {
	if len(tokens) == 1 {
		return s.evalSingleTerm(v, tokens[0].Term, fields)
	}

	var sets []hitList

	// 词条下标，按 DF 升序排列：最稀有的做驱动游标，候选文档最少。
	order := make([]int, len(tokens))
	for i := range order {
		order[i] = i
	}

	cursors := make([]index.PostingCursor, len(tokens))

	for _, field := range fields {
		fs, ok := v.FieldStats(field)
		if !ok {
			continue
		}

		// 任一词条在该字段不存在，整条短语就不可能命中。
		valid := true
		for _, t := range tokens {
			if v.DocFreq(field, t.Term) == 0 {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}

		// IDF 取各词条之和：短语没有自己的倒排表，也就没有自己的 DF，
		// 这是通行做法。
		var idf float64
		for _, t := range tokens {
			idf += s.bm.IDF(v.DocFreq(field, t.Term), uint32(fs.Docs))
		}

		slices.SortFunc(order, func(a, b int) int {
			return cmp.Compare(v.DocFreq(field, tokens[a].Term), v.DocFreq(field, tokens[b].Term))
		})
		for i, t := range tokens {
			cursors[i] = v.Cursor(field, t.Term)
		}

		driver := order[0]
		avgLen := fs.AvgLength
		lens := v.FieldLens(field)

		hl := newHitList(int(v.DocFreq(field, tokens[driver].Term)))
		for !cursors[driver].Done() {
			id := cursors[driver].DocID()

			// 其余游标单调推进到 id。游标只前进不回退，
			// 因此整轮下来每个 posting 最多被访问一次。
			all := true
			for _, i := range order[1:] {
				if !cursors[i].Seek(id) {
					all = false
					break
				}
			}

			if all {
				if n := countPhrase(tokens, cursors, driver); n > 0 {
					hl.add(id, s.bm.Score(idf, n, docLenAt(lens, id), avgLen))
				}
			}

			cursors[driver].Next()
		}

		if hl.len() > 0 {
			sets = append(sets, hl)
		}
	}

	return unionAll(sets)
}

// countPhrase 统计短语出现次数。
//
// 调用前必须保证所有游标都已停在同一个文档上（由归并循环维护）。
func countPhrase(tokens []analyzer.Token, cursors []index.PostingCursor, anchor int) uint32 {
	anchorPositions := cursors[anchor].Positions()
	base := tokens[anchor].Position

	var count uint32
	for _, start := range anchorPositions {
		if phraseFits(tokens, cursors, anchor, base, start) {
			count++
		}
	}
	return count
}

// phraseFits 判定以 start 为锚点位置时，短语是否整体落在文档里。
//
// 注意「整体落在**同一个字段**里」：各字段的位置都从 0 开始，
// 跨字段拼位置会造出根本不存在的短语。
func phraseFits(tokens []analyzer.Token, cursors []index.PostingCursor, anchor int, base, start uint32) bool {
	for i, t := range tokens {
		if i == anchor {
			continue
		}

		// 目标位置 = 锚点实际位置 + 该词条相对锚点的偏移。
		want := int64(start) + int64(t.Position) - int64(base)
		if want < 0 {
			return false
		}
		if !containsPosition(cursors[i].Positions(), uint32(want)) {
			return false
		}
	}
	return true
}

// containsPosition 在递增的位置序列里二分查找。
//
// 这里保留二分而不是线性扫描：单个文档内同一词条的出现次数通常很少，
// 但个别文档可能几百次；二分能挡住这种长尾。
func containsPosition(positions []uint32, want uint32) bool {
	lo, hi := 0, len(positions)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if positions[mid] < want {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo < len(positions) && positions[lo] == want
}

// evalBool 处理布尔组合。
func (s *Searcher) evalBool(v *index.View, b *Bool, fields []string) (hitList, error) {
	var must, should, mustNot []hitList

	for _, c := range b.Must {
		hl, err := s.eval(v, c, fields)
		if err != nil {
			return hitList{}, err
		}
		must = append(must, hl)
	}
	for _, c := range b.Should {
		hl, err := s.eval(v, c, fields)
		if err != nil {
			return hitList{}, err
		}
		should = append(should, hl)
	}
	for _, c := range b.MustNot {
		hl, err := s.eval(v, c, fields)
		if err != nil {
			return hitList{}, err
		}
		mustNot = append(mustNot, hl)
	}

	var out hitList

	switch {
	case len(must) > 0:
		out = intersectAll(must)
		// Must 非空时，Should 只负责加分，不参与筛选。
		addExisting(&out, unionAll(should))

	case len(should) > 0:
		out = unionAll(should)

	default:
		// 只有否定子句（例如单独一个 -foo）：只能从全量文档出发再排除。
		// 这是 O(N) 的，代价随索引规模线性增长。
		ids := v.SortedDocIDs()
		out = newHitList(len(ids))
		for _, id := range ids {
			out.add(id, 0)
		}
	}

	if len(mustNot) > 0 {
		out = subtract(out, unionAll(mustNot))
	}

	return out, nil
}

// evalFieldTerm 求值「字段:值」。
//
// 分流依据是**字段在索引里的类型**，不是查询的写法：
//   - text / keyword → 当成该字段内的一个词条
//   - number / date  → 等值，等价于 [v TO v]
//
// 字段没被声明过时按 text 处理：这是最常见的用法（查询先于写入到达，
// 或拼错了字段名），报错反而会让人困惑。真正需要报错的是
// 「字段声明成了 number，却给了非数值」——那种静默失败最难查。
func (s *Searcher) evalFieldTerm(v *index.View, t *FieldTerm, fields []string) (hitList, error) {
	kind, declared := v.FieldKind(t.Field)

	if declared && kind.Numeric() {
		value, err := index.ParseNumericValue(kind, t.Text)
		if err != nil {
			return hitList{}, fmt.Errorf("字段 %q: %w", t.Field, err)
		}
		return s.numericRange(v, t.Field, value, value, true, true)
	}

	// keyword 字段必须走 KeywordTerm，**不能**用分析器：
	// 索引侧存的是「小写化的整值」，分析器切出来的词条跟它对不上，
	// 结果就是「写得进去却查不出来」。
	if declared && kind == index.FieldKeyword {
		term := index.KeywordTerm(t.Text)
		if term == "" {
			return hitList{}, nil
		}
		return s.evalSingleTerm(v, term, []string{t.Field}), nil
	}

	// 文本字段（以及未声明的字段，按文本处理）：只在这一个字段里查词条。
	//
	// 这里**不接受 SearchOptions.Fields 的限制**：用户显式写了字段名，
	// 就不该再被外面的字段过滤悄悄排除掉。
	tokens := s.anz.Analyze(t.Text)
	switch len(tokens) {
	case 0:
		return hitList{}, nil
	case 1:
		return s.evalSingleTerm(v, tokens[0].Term, []string{t.Field}), nil
	default:
		return s.evalPhraseTokens(v, tokens, []string{t.Field}), nil
	}
}

// evalFieldRange 求值「字段:[下界 TO 上界]」。
func (s *Searcher) evalFieldRange(v *index.View, r *FieldRange, fields []string) (hitList, error) {
	kind, ok := v.FieldKind(r.Field)
	if !ok {
		return hitList{}, fmt.Errorf(
			"%w: 字段 %q 未被声明过，无法做范围查询"+
				"（范围查询只支持 number 与 date 类型）",
			index.ErrNotNumericField, r.Field)
	}
	if !kind.Numeric() {
		return hitList{}, fmt.Errorf(
			"%w: 字段 %q 是 %s 类型，只能对 number/date 字段做范围查询",
			index.ErrNotNumericField, r.Field, kind)
	}

	lo, hi := math.Inf(-1), math.Inf(1)

	if r.Lower != "" {
		v, err := index.ParseNumericValue(kind, r.Lower)
		if err != nil {
			return hitList{}, fmt.Errorf("字段 %q 的下界: %w", r.Field, err)
		}
		lo = v
	}
	if r.Upper != "" {
		v, err := index.ParseNumericValue(kind, r.Upper)
		if err != nil {
			return hitList{}, fmt.Errorf("字段 %q 的上界: %w", r.Field, err)
		}
		hi = v
	}

	// 两端都为无界是不可能的：解析器已经拒绝 [* TO *]。
	return s.numericRange(v, r.Field, lo, hi, r.IncludeLower, r.IncludeUpper)
}

// numericRange 扫描数值列产出命中列表。
//
// **得分一律为 0**：范围查询是纯过滤器。
//
// BM25 建立在词频与逆文档频率上，而「价格 42」没有词频概念，
// 硬套只会得到一个没有意义的分数。于是：
//   - 单独出现时按 DocID 排序；
//   - 与文本子句 AND 时，分数只来自文本子句；
//   - 与文本子句 OR 时，范围命中的文档也在结果里，只是分数低。
//
// 无需排序：列按 DocID 顺序扫描，输出天然升序，正是 hitList 的不变量。
func (s *Searcher) numericRange(
	v *index.View, field string,
	lo, hi float64, includeLo, includeHi bool,
) (hitList, error) {
	ids, err := v.NumericRange(field, lo, hi, includeLo, includeHi, nil)
	if err != nil {
		return hitList{}, err
	}

	out := newHitList(len(ids))
	for _, id := range ids {
		out.add(id, 0)
	}
	return out, nil
}
