// Package query 定义查询语法树与查询串解析器。
//
// 支持的语法：
//
//	hello            单词
//	hello world      相邻子句，默认 OR（可用 Options.DefaultOp 改成 AND）
//	hello AND world  显式与
//	hello OR world   显式或
//	-hello / NOT x   排除
//	"hello world"    短语，要求词条在**同一字段内**位置连续
//	(a OR b) AND c   括号分组
//
// # 解析器不做分词
//
// 解析器只负责语法，产出的词条是**原始文本**。分词由执行阶段用
// 索引自己的 Analyzer 完成——这是硬性要求：只要查询侧与写入侧
// 用了不同的分析器，就会出现「文档明明存在却检索不到」。
package query

import "strings"

// Node 是查询语法树的节点。
type Node interface {
	// String 返回节点的可读形式，用于日志与错误信息。
	String() string

	// isNode 封闭本接口，防止包外实现。
	isNode()
}

// Term 是单个词条。
//
// Text 是原始文本，可能被分析器切成 0 个、1 个或多个 token
// （例如 "full-width" 会切成 full 与 width）。执行阶段负责处理这三种情况。
type Term struct {
	Text string
}

func (t *Term) String() string { return t.Text }
func (*Term) isNode()          {}

// Phrase 是双引号短语。
//
// Raw 是引号内的原始文本。执行阶段会用分析器切分它，并**按分析出的位置**
// 判定相邻——而不是简单要求位置差为 1。这一点很重要：
// "quick the brown" 分析后得到 quick@0、brown@2（the 是停用词被过滤但仍占位），
// 位置差是 2，只有同样按位置差 2 去匹配才对得上。
type Phrase struct {
	Raw string
}

func (p *Phrase) String() string { return `"` + p.Raw + `"` }
func (*Phrase) isNode()          {}

// FieldTerm 是字段限定的词条，语法为 field:value。
//
// 求值方式取决于字段类型：
//   - text / keyword：在该字段内查这个词条
//   - number / date：等值查询，等价于 field:[v TO v]
//
// Text 保持原始文本，由执行阶段按字段类型处理——与解析器不做分词
// 是同一个道理：只有索引知道字段是什么类型、该用什么归一化规则。
type FieldTerm struct {
	Field string
	Text  string
}

func (t *FieldTerm) String() string { return t.Field + ":" + t.Text }
func (*FieldTerm) isNode()          {}

// FieldRange 是字段限定的范围，语法为 field:[lo TO hi]。
//
// Lower / Upper 保持**原始文本**，由执行阶段按字段类型解析成数值。
// 解析器不认识字段类型（类型在索引里），也不该认识——
// 否则解析器与索引就得各自维护一套类型知识，迟早走偏。
//
// 空字符串表示该端无界（查询串里写作 *）。
type FieldRange struct {
	Field string
	Lower string
	Upper string

	// IncludeLower / IncludeUpper 对应 [ ] 与 { } 四种组合。
	IncludeLower bool
	IncludeUpper bool
}

func (r *FieldRange) String() string {
	lo, hi := r.Lower, r.Upper
	if lo == "" {
		lo = "*"
	}
	if hi == "" {
		hi = "*"
	}

	left, right := "[", "]"
	if !r.IncludeLower {
		left = "{"
	}
	if !r.IncludeUpper {
		right = "}"
	}
	return r.Field + ":" + left + lo + " TO " + hi + right
}

func (*FieldRange) isNode() {}

// Bool 是布尔组合。
//
// 语义：
//   - Must 中每个子句都必须匹配（AND）
//   - Should 中至少匹配一个（OR）；若 Must 非空，Should 只加分不参与筛选
//   - MustNot 中每个子句都必须不匹配（NOT）
//
// 当 Must 与 Should 都为空、只有 MustNot 时，语义是「全量文档减去这些子句」，
// 执行阶段需要遍历全部文档。这类查询（例如单独一个 -foo）代价较高，
// 但在小规模索引上完全可用。
type Bool struct {
	Must    []Node
	Should  []Node
	MustNot []Node
}

func (*Bool) isNode() {}

func (b *Bool) String() string {
	var parts []string

	if len(b.Must) > 0 {
		parts = append(parts, "MUST("+joinNodes(b.Must)+")")
	}
	if len(b.Should) > 0 {
		parts = append(parts, "SHOULD("+joinNodes(b.Should)+")")
	}
	if len(b.MustNot) > 0 {
		parts = append(parts, "MUSTNOT("+joinNodes(b.MustNot)+")")
	}
	if len(parts) == 0 {
		return "BOOL()"
	}
	return strings.Join(parts, " ")
}

func joinNodes(nodes []Node) string {
	parts := make([]string, len(nodes))
	for i, n := range nodes {
		parts[i] = n.String()
	}
	return strings.Join(parts, " ")
}

// notNode 是解析期的取反标记。
//
// 它**不会出现在最终的语法树里**：解析器在合并子句时会把它的 Child
// 搬进 Bool.MustNot，因此执行阶段永远见不到这个类型。
type notNode struct {
	Child Node
}

func (n *notNode) String() string { return "NOT(" + n.Child.String() + ")" }
func (*notNode) isNode()          {}
