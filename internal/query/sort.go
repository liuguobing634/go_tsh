package query

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/liuguobing/go_tsh/internal/index"
)

// SortKey 指定一个排序键。
//
// 只有在传了排序键时，结果才**不**按相关性分数排列——分数仍然照常算出来
// 并返回给调用方（用于展示或二次判断），只是不再决定顺序。
type SortKey struct {
	// Field 是字段名。
	Field string

	// Desc 为 true 时降序（大的在前）。
	Desc bool
}

// 排序相关的错误。
var (
	// ErrBadSortField 表示排序字段不可用（不存在或名字为空）。
	ErrBadSortField = errors.New("query: 排序字段不可用")

	// ErrUnsortableField 表示该字段的类型不支持排序。
	ErrUnsortableField = errors.New("query: 该字段类型不支持排序")

	// ErrTooManySortKeys 表示排序键过多。
	ErrTooManySortKeys = errors.New("query: 排序键过多")
)

// maxSortKeys 是排序键的个数上限。
//
// 每个键都要对每篇候选文档取值并参与比较，代价直接乘在候选集上。
// 3 个键足够表达「先按时间、再按价格」这类需求。
const maxSortKeys = 3

// sortValue 是一个排序键在某篇文档上的取值。
type sortValue struct {
	// ok 为 false 表示该文档缺这个字段。
	//
	// 缺值的文档**一律排在最后**，与升序降序无关——把「没有价格」的
	// 商品排在按价格升序的第一位没有意义。
	ok bool

	num float64 // number / date
	str string  // keyword
}

// sortPlan 是一次排序的完整计划：取哪些键、各自怎么比。
type sortPlan struct {
	keys []SortKey

	// numeric[k] 为 true 表示第 k 个键按数值比，否则按字符串比。
	numeric []bool
}

// size 返回每篇文档占用多少个 sortValue 槽位。
func (p sortPlan) size() int { return len(p.keys) }

// planSort 校验排序键并生成执行计划。必须在 View 内调用（要读 schema）。
func planSort(v *index.View, keys []SortKey) (sortPlan, error) {
	if len(keys) == 0 {
		return sortPlan{}, nil
	}
	if len(keys) > maxSortKeys {
		return sortPlan{}, fmt.Errorf("%w: 最多 %d 个，给了 %d 个",
			ErrTooManySortKeys, maxSortKeys, len(keys))
	}

	plan := sortPlan{
		keys:    keys,
		numeric: make([]bool, len(keys)),
	}

	for i, k := range keys {
		field := strings.TrimSpace(k.Field)
		if field == "" {
			return sortPlan{}, fmt.Errorf("%w: 第 %d 个排序键的字段名为空", ErrBadSortField, i+1)
		}
		plan.keys[i].Field = field

		kind, ok := v.FieldKind(field)
		if !ok {
			return sortPlan{}, fmt.Errorf("%w: 字段 %q 不存在", ErrBadSortField, field)
		}

		switch kind {
		case index.FieldNumber, index.FieldDate:
			plan.numeric[i] = true
		case index.FieldKeyword:
			// 关键字字段存的整段原文就是词条，直接按字节序比。
			plan.numeric[i] = false
		default:
			return sortPlan{}, fmt.Errorf(
				"%w: 字段 %q 是 %s 类型（已分词，没有有意义的顺序）；"+
					"只有 number / date / keyword 字段能排序",
				ErrUnsortableField, field, kind)
		}
	}
	return plan, nil
}

// extract 把全部候选文档的排序键取值取出来。
//
// 返回的是「文档 × 键」的**扁平**数组：values[i*n+k] 是第 i 篇文档在
// 第 k 个键上的取值。用扁平数组而不是 [][]：命中上万篇时，
// 每篇一个切片会让分配次数直接乘以命中数。
//
// 必须在 View 内调用。
func (p sortPlan) extract(v *index.View, ids []index.DocID) []sortValue {
	n := p.size()
	out := make([]sortValue, len(ids)*n)

	for k := range p.keys {
		field := p.keys[k].Field
		for i, id := range ids {
			sv := &out[i*n+k]
			if p.numeric[k] {
				sv.num, sv.ok = v.NumericValue(id, field)
			} else {
				sv.str, sv.ok = v.FieldValue(id, field)
			}
		}
	}
	return out
}

// rank 按计划排出前 keep 条，返回**位置下标**（对应 ids 的下标）。
//
// 排序本身不碰索引，所以调用方应当在 View **之外**调用它：
// O(n log n) 的比较不该占着读锁。
func (p sortPlan) rank(ids []index.DocID, values []sortValue, keep int) []int {
	n := len(ids)
	if n == 0 {
		return nil
	}

	pos := make([]int, n)
	for i := range pos {
		pos[i] = i
	}

	nk := p.size()
	less := func(a, b int) bool {
		for k := range p.keys {
			va, vb := values[a*nk+k], values[b*nk+k]

			// 缺值一律排最后：与升降序无关，也与它是第几个键无关。
			if va.ok != vb.ok {
				return va.ok
			}
			if !va.ok {
				continue // 两个都缺，看下一个键
			}

			var c int
			if p.numeric[k] {
				switch {
				case va.num < vb.num:
					c = -1
				case va.num > vb.num:
					c = 1
				}
			} else {
				c = strings.Compare(va.str, vb.str)
			}

			if c != 0 {
				if p.keys[k].Desc {
					return c > 0
				}
				return c < 0
			}
		}

		// 所有键都相等：按 DocID 升序兜底。
		//
		// 没有这条，同键文档的先后会随排序算法与内部遍历顺序漂移，
		// 分页就会出现重复或漏项。
		return ids[a] < ids[b]
	}

	sort.SliceStable(pos, func(i, j int) bool { return less(pos[i], pos[j]) })

	if keep > 0 && keep < n {
		return pos[:keep]
	}
	return pos
}

// ParseSort 解析 `field` 或 `field:desc` / `field:asc` 形式的排序声明。
//
// 多个键用逗号分隔，靠前的优先：`created:desc,price:asc`。
// 省略方向时是升序——与 SQL 的默认一致。
func ParseSort(spec string) ([]SortKey, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}

	parts := strings.Split(spec, ",")
	out := make([]SortKey, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		field, dir, hasDir := strings.Cut(part, ":")
		field = strings.TrimSpace(field)
		if field == "" {
			return nil, fmt.Errorf("%w: %q 里没有字段名", ErrBadSortField, part)
		}

		key := SortKey{Field: field}
		if hasDir {
			switch strings.ToLower(strings.TrimSpace(dir)) {
			case "asc", "":
				key.Desc = false
			case "desc":
				key.Desc = true
			default:
				return nil, fmt.Errorf(
					"%w: %q 的方向 %q 无法识别，只能是 asc 或 desc",
					ErrBadSortField, part, dir)
			}
		}
		out = append(out, key)
	}

	if len(out) > maxSortKeys {
		return nil, fmt.Errorf("%w: 最多 %d 个，给了 %d 个",
			ErrTooManySortKeys, maxSortKeys, len(out))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
