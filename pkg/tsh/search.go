package tsh

import (
	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/query"
)

// SearchRequest 描述一次检索。
type SearchRequest struct {
	// Query 是查询串。语法：bare term、`"短语"`、`-排除`、
	// AND / OR / NOT、括号，详见 internal/query 的包注释。
	Query string

	// Fields 限定检索字段；为空表示全部字段。
	Fields []string

	// Limit 是返回条数上限；<= 0 时取 10，超过 100 会被截到 100。
	Limit int

	// Offset 是跳过的条数。
	Offset int

	// Highlight 为 true 时，为每个命中字段生成高亮片段。
	Highlight bool
}

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
