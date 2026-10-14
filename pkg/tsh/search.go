package tsh

import (
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/query"
)

// SearchRequest 描述一次检索。
type SearchRequest struct {
	// Query 是查询串。
	//
	// # 语法
	//
	//	term               词条（会经分析器归一化：大小写、分词、停用词）
	//	"some phrase"      短语，要求词条位置连续
	//	-term  /  NOT term 排除
	//	a AND b            两者都要（也支持 &&）
	//	a OR b             任一即可（也支持 ||）
	//	(a OR b) AND c     括号分组
	//	field:term         限定字段
	//	field:[10 TO 100]  闭区间；`{` `}` 表示开区间，`*` 表示无界
	//	field:>=10         比较运算符（等价于 field:[10 TO *]）
	//
	// 字段限定与范围查询**只对声明过类型的字段有意义**：
	// number / date 支持等值与范围，keyword 支持整值精确匹配，
	// text 只能做词条与短语查询（对它做范围查询会报错，不会静默返回空）。
	//
	// 冒号是字段分隔符，因此 `a:b` 是「字段 a 里的 b」而不是一个普通词条。
	// 字段名不允许数字开头，所以时间戳这类写法不会被误拆。
	Query string

	// Fields 限定检索字段；为空表示全部字段。
	Fields []string

	// Limit 是返回条数上限；<= 0 时取 10，超过 100 会被截到 100。
	Limit int

	// Offset 是跳过的条数。
	Offset int

	// Highlight 为 true 时，为每个命中字段生成高亮片段。
	Highlight bool

	// Sort 指定排序键；为空表示按相关性分数降序。
	//
	// 传了它之后，分数**不再决定顺序**（但仍然照常算出来并返回）：
	// 用户说「按价格排」的时候，相关性只用来筛选。
	//
	// 只有 number / date / keyword 字段能排序。text 字段已经分词，
	// 没有有意义的顺序，传了会返回 query.ErrUnsortableField。
	Sort []SortKey
}

// SortKey 是一个排序键。
//
// 它是 query.SortKey 的类型别名：既能被外部命名与构造，
// 又不需要在门面里重复一遍字段再写转换。
type SortKey = query.SortKey

// ParseSort 解析 `field` 或 `field:desc` 形式的排序声明。
//
// 多个键用逗号分隔，靠前的优先：`created:desc,price:asc`。
// 省略方向时是升序。空的 spec 返回 (nil, nil)。
func ParseSort(spec string) ([]SortKey, error) { return query.ParseSort(spec) }

// SearchHit 是一条命中结果。
type SearchHit struct {
	ID     string            `json:"id"`
	Score  float64           `json:"score"`
	Fields map[string]string `json:"fields,omitempty"`

	// Highlights 的 key 是字段名，value 是已做 HTML 转义、
	// 并用 <em> 标出命中词条的片段。没有命中的字段不会出现。
	Highlights map[string]string `json:"highlights,omitempty"`
}

// SearchResult 是一次检索的结果。
type SearchResult struct {
	// Total 是命中文档总数（分页之前）。
	Total int `json:"total"`

	// Hits 是当前页结果，按「分数降序、同分 ID 升序」排列。
	Hits []SearchHit `json:"hits"`
}

// Search 执行一次检索。
//
// 查询串的错误（ErrEmptyQuery / ErrTooManyClauses / *query.SyntaxError）
// 原样返回，HTTP 层据此映射成 400。
func (t *Table) Search(req SearchRequest) (SearchResult, error) {
	node, err := query.Parse(req.Query, t.popts)
	if err != nil {
		return SearchResult{}, err
	}

	res, err := t.search.Search(node, query.SearchOptions{
		Fields: req.Fields,
		Limit:  req.Limit,
		Offset: req.Offset,
		Sort:   req.Sort,
	})
	if err != nil {
		return SearchResult{}, err
	}

	out := SearchResult{Total: res.Total}
	if len(res.Hits) == 0 {
		return out, nil
	}

	var terms []string
	if req.Highlight {
		terms = t.search.QueryTerms(node)
	}

	// View 内只做廉价的事：按 DocID 取文档副本。
	//
	// 高亮要遍历整段原文，属于耗时工作，必须挪到 View 之外——
	// 否则会一直占着索引读锁、把写请求全部堵住。
	type fetched struct {
		id     string
		fields map[string]string
		score  float64
	}

	docs := make([]fetched, 0, len(res.Hits))
	t.idx.View(func(v *index.View) {
		for _, h := range res.Hits {
			doc, ok := v.Document(h.ID)
			if !ok {
				// 命中的文档在「检索」与「取文档」之间被删掉了。
				// 跳过即可，Total 仍是检索那个时点的数量。
				continue
			}
			docs = append(docs, fetched{id: doc.External, fields: doc.Fields, score: h.Score})
		}
	})

	out.Hits = make([]SearchHit, 0, len(docs))
	for _, d := range docs {
		hit := SearchHit{ID: d.id, Score: d.score, Fields: d.fields}

		if len(terms) > 0 {
			hl := make(map[string]string)
			for field, text := range d.fields {
				if snippet := t.hl.Highlight(text, terms); snippet != "" {
					hl[field] = snippet
				}
			}
			if len(hl) > 0 {
				hit.Highlights = hl
			}
		}

		out.Hits = append(out.Hits, hit)
	}

	return out, nil
}
