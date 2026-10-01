package query

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Operator 是相邻子句之间的隐式关系。
type Operator int

const (
	// OpOr 让 "a b" 等价于 "a OR b"（零值，默认）。
	OpOr Operator = iota

	// OpAnd 让 "a b" 等价于 "a AND b"。
	OpAnd
)

// defaultMaxClauses 是子句数上限的默认值。
const defaultMaxClauses = 64

// Options 配置解析器；零值即为一套合理默认。
type Options struct {
	// DefaultOp 是相邻子句之间的隐式关系，零值为 OpOr。
	//
	// 显式写出的 AND / OR 永远优先于它。
	DefaultOp Operator

	// MaxClauses 是子句数上限，<= 0 时取 64。
	//
	// 这是一道防护：查询串来自外部输入，不设上限的话一句
	// "a a a a ... a"（几千个词）就能构造出巨大的布尔树并打爆内存与 CPU。
	// 短语按其中的单词数计入。
	MaxClauses int
}

// ErrEmptyQuery 表示查询串里没有任何可检索内容。
var ErrEmptyQuery = errors.New("query: 查询串为空")

// ErrTooManyClauses 表示子句数超过 Options.MaxClauses。
var ErrTooManyClauses = errors.New("query: 子句数超出上限")

// SyntaxError 描述查询串的语法错误，带出错位置便于定位。
type SyntaxError struct {
	Query string
	Pos   int // 出错处在 Query 中的 rune 下标
	Msg   string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("query: 第 %d 个字符处语法错误: %s (查询串 %q)", e.Pos+1, e.Msg, e.Query)
}

// Parse 把查询串解析成语法树。
//
// 返回的树中只包含 Term、Phrase、Bool 三种节点。
func Parse(q string, opts Options) (Node, error) {
	if opts.MaxClauses <= 0 {
		opts.MaxClauses = defaultMaxClauses
	}

	toks, err := scan(q)
	if err != nil {
		return nil, err
	}

	p := &parser{query: q, toks: toks, opts: opts}

	if p.peek().kind == tokEOF {
		return nil, ErrEmptyQuery
	}

	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tokEOF {
		return nil, &SyntaxError{Query: q, Pos: t.pos, Msg: fmt.Sprintf("多余的内容 %q", t.text)}
	}
	return node, nil
}

// ---------------------------------------------------------------- 词法分析

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokWord
	tokPhrase
	tokAnd
	tokOr
	tokNot
	tokLParen
	tokRParen

	// tokFieldTerm / tokFieldRange 是字段限定子句。
	//
	// 它们的语义在词法阶段就已经完全确定（信息量超过一个字符串），
	// 因此直接把构造好的 AST 节点挂在 token 上，
	// 而不是塞进 text 让语法阶段再拆一次。
	tokFieldTerm
	tokFieldRange
)

type token struct {
	kind tokenKind
	text string
	pos  int // rune 下标

	// node 只对 tokFieldTerm / tokFieldRange 有效。
	node Node
}

// 字段名的合法字符：必须以字母或下划线开头。
//
// **不允许数字开头**是刻意的：查询里出现 `2024-01-01T10:00:00Z`
// 这类时间戳时，若允许数字开头，`2024-01-01T10` 会被当成字段名，
// 后面那个冒号就成了字段分隔符，整个时间戳被拆坏。
func isFieldNameStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isFieldNameRune(r rune) bool {
	return r == '_' || r == '.' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isQueryDelimiter(r rune) bool {
	return unicode.IsSpace(r) || r == '(' || r == ')' || r == '"'
}

// scan 把查询串切成 token 序列，末尾总是补一个 tokEOF。
func scan(q string) ([]token, error) {
	runes := []rune(q)
	toks := make([]token, 0, 8)

	for i := 0; i < len(runes); {
		r := runes[i]

		switch {
		case unicode.IsSpace(r):
			i++

		case r == '(':
			toks = append(toks, token{kind: tokLParen, text: "(", pos: i})
			i++

		case r == ')':
			toks = append(toks, token{kind: tokRParen, text: ")", pos: i})
			i++

		case r == '-':
			// '-' 只作为前缀运算符；连字符出现在词的中间不算
			// （下面的 default 分支会把 "full-width" 整体吃成一个词）。
			toks = append(toks, token{kind: tokNot, text: "-", pos: i})
			i++

		case r == '"':
			start := i
			i++

			var sb strings.Builder
			closed := false
			for i < len(runes) {
				if runes[i] == '\\' && i+1 < len(runes) {
					sb.WriteRune(runes[i+1])
					i += 2
					continue
				}
				if runes[i] == '"' {
					closed = true
					i++
					break
				}
				sb.WriteRune(runes[i])
				i++
			}
			if !closed {
				return nil, &SyntaxError{Query: q, Pos: start, Msg: "引号未闭合"}
			}
			toks = append(toks, token{kind: tokPhrase, text: sb.String(), pos: start})

		default:
			// 先看是不是 field: 前缀。字段限定的范围语法可能跨空格
			// （field:[10 TO 20]），所以必须在「扫整词」之前判断，
			// 否则 `field:[10` 会先被当成一个词吃掉。
			field, after, ok := scanFieldPrefix(runes, i)
			if ok {
				start := i
				i = after

				if i < len(runes) && (runes[i] == '[' || runes[i] == '{') {
					node, next, err := scanFieldRange(q, runes, field, start, i)
					if err != nil {
						return nil, err
					}
					toks = append(toks, token{kind: tokFieldRange, pos: start, node: node})
					i = next
					continue
				}

				vs := i
				for i < len(runes) && !isQueryDelimiter(runes[i]) {
					i++
				}
				value := string(runes[vs:i])
				if value == "" {
					return nil, &SyntaxError{
						Query: q, Pos: start,
						Msg: fmt.Sprintf("字段 %q 后面缺少值", field),
					}
				}
				toks = append(toks, token{
					kind: tokFieldTerm, pos: start,
					node: &FieldTerm{Field: field, Text: value},
				})
				continue
			}

			start := i
			for i < len(runes) && !isQueryDelimiter(runes[i]) {
				i++
			}

			word := string(runes[start:i])
			kind := tokWord
			// 关键字不区分大小写：and / AND / And 等价。
			switch strings.ToUpper(word) {
			case "AND":
				kind = tokAnd
			case "OR":
				kind = tokOr
			case "NOT":
				kind = tokNot
			}
			toks = append(toks, token{kind: kind, text: word, pos: start})
		}
	}

	return append(toks, token{kind: tokEOF, pos: len(runes)}), nil
}

// scanFieldPrefix 尝试从 i 处读出一个 "field:" 前缀。
//
// 返回字段名、冒号之后的位置、以及是否识别成功。
func scanFieldPrefix(runes []rune, i int) (field string, after int, ok bool) {
	if i >= len(runes) || !isFieldNameStart(runes[i]) {
		return "", i, false
	}

	j := i
	for j < len(runes) && isFieldNameRune(runes[j]) {
		j++
	}
	if j >= len(runes) || runes[j] != ':' {
		return "", i, false
	}
	return string(runes[i:j]), j + 1, true
}

// scanFieldRange 解析 field:[lo TO hi] 这类范围子句。
//
// 四种括号组合都允许（[ ]、{ }，以及 Lucene 也接受的混合写法 [a TO b}）。
func scanFieldRange(q string, runes []rune, field string, start, open int) (Node, int, error) {
	includeLower := runes[open] == '['

	// 闭合括号找最近的一个，且允许与开启括号不同型。
	j := open + 1
	for j < len(runes) && runes[j] != ']' && runes[j] != '}' {
		j++
	}
	if j >= len(runes) {
		return nil, 0, &SyntaxError{
			Query: q, Pos: start,
			Msg: "范围缺少闭合的 ] 或 }",
		}
	}
	includeUpper := runes[j] == ']'

	content := strings.TrimSpace(string(runes[open+1 : j]))
	lo, hi, err := splitRangeBounds(q, content, start)
	if err != nil {
		return nil, 0, err
	}

	return &FieldRange{
		Field:        field,
		Lower:        lo,
		Upper:        hi,
		IncludeLower: includeLower,
		IncludeUpper: includeUpper,
	}, j + 1, nil
}

// splitRangeBounds 把 "10 TO 20" 拆成两端；* 表示该端无界。
func splitRangeBounds(q, content string, pos int) (lo, hi string, err error) {
	// 用 Fields 而不是自己找 "TO"：这样 "TO" 必须是独立的一个词，
	// 不会把值里恰好含有 TO 的部分切错（例如某个字段值叫 "TODO"）。
	parts := strings.Fields(content)
	if len(parts) != 3 || !strings.EqualFold(parts[1], "TO") {
		return "", "", &SyntaxError{
			Query: q, Pos: pos,
			Msg: "范围要写成 字段:[下界 TO 上界]，例如 price:[10 TO 100]；" +
				"无界的一端写 *",
		}
	}

	lo, hi = parts[0], parts[2]
	if lo == "*" {
		lo = ""
	}
	if hi == "*" {
		hi = ""
	}
	if lo == "" && hi == "" {
		return "", "", &SyntaxError{
			Query: q, Pos: pos,
			Msg: "范围两端不能都是 *（那等于不设条件）",
		}
	}
	return lo, hi, nil
}

// ---------------------------------------------------------------- 语法分析

type parser struct {
	query   string
	toks    []token
	at      int
	opts    Options
	clauses int
}

func (p *parser) peek() token { return p.toks[p.at] }

func (p *parser) next() token {
	t := p.toks[p.at]
	if p.at < len(p.toks)-1 {
		p.at++
	}
	return t
}

// parseOr 处理最低优先级：OR 分组。
func (p *parser) parseOr() (Node, error) {
	first, err := p.parseAnd()
	if err != nil {
		return nil, err
	}

	groups := []Node{first}
	for p.peek().kind == tokOr {
		p.next()

		g, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}

	if len(groups) == 1 {
		return groups[0], nil
	}
	return &Bool{Should: groups}, nil
}

// parseAnd 收集一串相邻子句。
//
// 相邻关系可能是显式 AND，也可能是隐式的（由 Options.DefaultOp 决定）。
// 只要这一组里出现过任何一个显式 AND，整组就按 AND 处理——
// 这符合直觉：用户既然写了 AND，就不希望同组的其它词变成 OR。
func (p *parser) parseAnd() (Node, error) {
	var clauses []Node
	explicitAnd := false

	for {
		if p.peek().kind == tokAnd {
			explicitAnd = true
			p.next()
		}

		c, err := p.parseClause()
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, c)

		if !p.startsClause() {
			break
		}
	}

	must := explicitAnd || p.opts.DefaultOp == OpAnd
	return combine(clauses, must), nil
}

// startsClause 报告当前 token 能否作为「下一个子句」的开始。
//
// tokAnd 也必须算进来：它本身不是一个子句，但它的存在说明后面还跟着
// 一个子句，循环必须继续下去才能把 AND 消费掉。
// 少了这一条，"a AND b" 会在读到 AND 时直接跳出循环，
// 然后被外层当成「多余的内容」报语法错误。
func (p *parser) startsClause() bool {
	switch p.peek().kind {
	case tokWord, tokPhrase, tokLParen, tokNot, tokAnd, tokFieldTerm, tokFieldRange:
		return true
	default:
		return false
	}
}

// parseClause 处理前导若干取反运算符，再解析一个原子。
func (p *parser) parseClause() (Node, error) {
	negated := false
	for p.peek().kind == tokNot {
		// 双重取反互相抵消：--foo 等价于 foo。
		negated = !negated
		p.next()
	}

	atom, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	if negated {
		return &notNode{Child: atom}, nil
	}
	return atom, nil
}

// parseAtom 解析最基本的单元：词、短语、括号分组。
func (p *parser) parseAtom() (Node, error) {
	t := p.peek()

	switch t.kind {
	case tokWord:
		p.next()
		if err := p.countClauses(t.pos, 1); err != nil {
			return nil, err
		}
		return &Term{Text: t.text}, nil

	case tokPhrase:
		p.next()
		// 短语按其中的单词数计入配额，否则一个长短语就能绕过上限。
		n := len(strings.Fields(t.text))
		if n == 0 {
			n = 1
		}
		if err := p.countClauses(t.pos, n); err != nil {
			return nil, err
		}
		return &Phrase{Raw: t.text}, nil

	case tokFieldTerm, tokFieldRange:
		// 语义在词法阶段就定好了，这里只做配额统计。
		p.next()
		if err := p.countClauses(t.pos, 1); err != nil {
			return nil, err
		}
		return t.node, nil

	case tokLParen:
		p.next()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen {
			return nil, &SyntaxError{Query: p.query, Pos: t.pos, Msg: "缺少右括号"}
		}
		p.next()
		return inner, nil

	case tokEOF:
		return nil, &SyntaxError{Query: p.query, Pos: t.pos, Msg: "表达式不完整"}

	default:
		// 字段子句的 text 为空（信息在 node 上），用 String() 才能给出
		// 有意义的报错。
		what := t.text
		if what == "" && t.node != nil {
			what = t.node.String()
		}
		return nil, &SyntaxError{Query: p.query, Pos: t.pos, Msg: fmt.Sprintf("意外的 %q", what)}
	}
}

func (p *parser) countClauses(pos, n int) error {
	p.clauses += n
	if p.clauses > p.opts.MaxClauses {
		return fmt.Errorf("%w: 上限 %d（出错位置约第 %d 个字符）",
			ErrTooManyClauses, p.opts.MaxClauses, pos+1)
	}
	return nil
}

// combine 把一组子句合并成节点，同时把取反子句搬进 MustNot。
func combine(clauses []Node, must bool) Node {
	var positive, negative []Node
	for _, c := range clauses {
		if n, ok := c.(*notNode); ok {
			negative = append(negative, n.Child)
			continue
		}
		positive = append(positive, c)
	}

	// 单个正向子句且无取反时不必包一层 Bool。
	if len(negative) == 0 && len(positive) == 1 {
		return positive[0]
	}

	b := &Bool{MustNot: negative}
	if must {
		b.Must = positive
	} else {
		b.Should = positive
	}
	return b
}
