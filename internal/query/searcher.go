package query

import (
	"cmp"
	"errors"
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
	// 没有这条确定性约定，同分文档的先后会随 map 遍历顺序漂移，
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

	var scores docScores
	s.ix.View(func(v *index.View) {
		fields := resolveFields(v, opts.Fields)
		if len(fields) == 0 {
			return
		}
		scores = s.eval(v, n, fields)
	})

	total := len(scores)
	if total == 0 {
		return Result{Total: 0}, nil
	}

	// 只保留 offset+limit 条：分页不需要对全部命中排序。
	tk := scoring.NewTopK[index.DocID](offset + limit)
	for id, score := range scores {
		tk.Push(id, score)
	}

	hits := tk.Result()
	if offset >= len(hits) {
		return Result{Total: total}, nil
	}
	return Result{Total: total, Hits: hits[offset:]}, nil
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

// ---------------------------------------------------------------- 求值

// docScores 是求值结果：出现在 map 里就表示命中，值是 BM25 分数。
//
// 用 map 而不是有序 posting 归并，是「先正确、再快」的取舍：
// 归并需要跨字段维护游标，复杂得多。Phase 5 压测若显示这里是瓶颈，
// 再换成基于 posting 有序性的双指针归并。
type docScores map[index.DocID]float64

func (s *Searcher) eval(v *index.View, n Node, fields []string) docScores {
	switch node := n.(type) {
	case *Term:
		return s.evalTerm(v, node, fields)
	case *Phrase:
		return s.evalPhrase(v, node, fields)
	case *Bool:
		return s.evalBool(v, node, fields)
	default:
		// 解析器只会产出上面三种节点；走到这里说明有人手工构造了 AST。
		return nil
	}
}

// evalTerm 处理单词条节点。
func (s *Searcher) evalTerm(v *index.View, t *Term, fields []string) docScores {
	tokens := s.anz.Analyze(t.Text)

	switch len(tokens) {
	case 0:
		// 整个词被分析器吃掉了（例如只由停用词或过短字符组成）。
		return nil
	case 1:
		return s.evalSingleTerm(v, tokens[0].Term, fields)
	default:
		// 一个词被切成多个 token，例如 "full-width" -> full + width。
		// 按短语处理最符合直觉：用户写的是一个整体，不应拆成 OR。
		return s.evalPhraseTokens(v, tokens, fields)
	}
}

// evalPhrase 处理双引号短语。
func (s *Searcher) evalPhrase(v *index.View, p *Phrase, fields []string) docScores {
	tokens := s.anz.Analyze(p.Raw)
	if len(tokens) == 0 {
		return nil
	}
	return s.evalPhraseTokens(v, tokens, fields)
}

// evalSingleTerm 求一个已归一化的词条在指定字段上的命中与分数。
//
// 同一词条出现在多个字段时，各字段的贡献相加。注意每个字段的 IDF
// 是**分开算**的：同一个词在 title 与 body 里的稀有程度完全不同，
// 混在一起算会让短字段的高信息量被长字段稀释。
func (s *Searcher) evalSingleTerm(v *index.View, term string, fields []string) docScores {
	out := make(docScores)

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

		v.ScanIDs(field, term, func(id index.DocID, tf uint32) bool {
			out[id] += s.bm.Score(idf, tf, float64(v.DocLength(id, field)), avgLen)
			return true
		})
	}

	return out
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
// 且是顺序访问。由于所有 posting 列表都按 DocID 有序，归并天然成立。
func (s *Searcher) evalPhraseTokens(v *index.View, tokens []analyzer.Token, fields []string) docScores {
	if len(tokens) == 1 {
		return s.evalSingleTerm(v, tokens[0].Term, fields)
	}

	out := make(docScores)

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
					out[id] += s.bm.Score(idf, n, float64(v.DocLength(id, field)), avgLen)
				}
			}

			cursors[driver].Next()
		}
	}

	return out
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
func (s *Searcher) evalBool(v *index.View, b *Bool, fields []string) docScores {
	var must, should, mustNot []docScores

	for _, c := range b.Must {
		must = append(must, s.eval(v, c, fields))
	}
	for _, c := range b.Should {
		should = append(should, s.eval(v, c, fields))
	}
	for _, c := range b.MustNot {
		mustNot = append(mustNot, s.eval(v, c, fields))
	}

	var out docScores

	switch {
	case len(must) > 0:
		out = intersect(must)
		// Must 非空时，Should 只负责加分，不参与筛选。
		for _, sc := range should {
			addExisting(out, sc)
		}

	case len(should) > 0:
		out = union(should)

	default:
		// 只有否定子句（例如单独一个 -foo）：只能从全量文档出发再排除。
		// 这是 O(N) 的，代价随索引规模线性增长。
		out = make(docScores)
		v.EachDocument(func(id index.DocID) bool {
			out[id] = 0
			return true
		})
	}

	for _, neg := range mustNot {
		for id := range neg {
			delete(out, id)
		}
	}

	return out
}

// intersect 求多个分数集合的交集，命中文档的分数相加。
func intersect(sets []docScores) docScores {
	if len(sets) == 0 {
		return nil
	}

	// 从最小的集合出发，减少比较次数。
	smallest := 0
	for i, s := range sets {
		if len(s) < len(sets[smallest]) {
			smallest = i
		}
	}

	out := make(docScores, len(sets[smallest]))
	for id, score := range sets[smallest] {
		total := score
		matched := true

		for i, other := range sets {
			if i == smallest {
				continue
			}
			sc, ok := other[id]
			if !ok {
				matched = false
				break
			}
			total += sc
		}

		if matched {
			out[id] = total
		}
	}
	return out
}

// union 求多个分数集合的并集，分数相加。
func union(sets []docScores) docScores {
	out := make(docScores)
	for _, s := range sets {
		for id, score := range s {
			out[id] += score
		}
	}
	return out
}

// addExisting 只给 dst 里**已有**的文档加分，不引入新文档。
func addExisting(dst, src docScores) {
	for id, score := range src {
		if _, ok := dst[id]; ok {
			dst[id] += score
		}
	}
}
