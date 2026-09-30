package scoring

import (
	"cmp"
	"slices"
)

// Hit 是一条候选结果。
type Hit[ID cmp.Ordered] struct {
	ID    ID
	Score float64
}

// TopK 维护分数最高的 K 条候选。
//
// 用固定容量小顶堆：堆顶 h[0] 始终是「当前最差的一条」。
// 新候选只有在优于堆顶时才可能入堆，于是 N 条候选只需 O(N log K)，
// 而不是全量排序的 O(N log N)。取前 10 条时差距非常明显。
//
// 排序规则固定为「分数降序，同分按 ID 升序」，保证结果确定可复现——
// 没有这条约定，同分文档的先后会随 map 遍历顺序漂移，测试会变得不可靠。
type TopK[ID cmp.Ordered] struct {
	k int
	h []Hit[ID]
}

// NewTopK 创建容量为 k 的收集器。k <= 0 时不会收集任何结果。
func NewTopK[ID cmp.Ordered](k int) *TopK[ID] {
	t := &TopK[ID]{k: k}
	if k > 0 {
		t.h = make([]Hit[ID], 0, k)
	}
	return t
}

// Push 提交一条候选。
func (t *TopK[ID]) Push(id ID, score float64) {
	if t.k <= 0 {
		return
	}

	cand := Hit[ID]{ID: id, Score: score}

	if len(t.h) < t.k {
		t.h = append(t.h, cand)
		t.siftUp(len(t.h) - 1)
		return
	}

	// 堆已满：只有严格优于堆顶（当前最差）才替换，否则直接丢弃。
	if !better(cand, t.h[0]) {
		return
	}
	t.h[0] = cand
	t.siftDown(0)
}

// Len 返回当前已收集的候选数，最多为 k。
func (t *TopK[ID]) Len() int { return len(t.h) }

// Result 返回按「分数降序、同分 ID 升序」排好的结果。
func (t *TopK[ID]) Result() []Hit[ID] {
	out := make([]Hit[ID], len(t.h))
	copy(out, t.h)

	slices.SortFunc(out, func(a, b Hit[ID]) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

// siftUp 把 i 处的元素上浮到合适位置，维持「堆顶最差」的小顶堆性质。
func (t *TopK[ID]) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !worse(t.h[i], t.h[parent]) {
			return
		}
		t.h[i], t.h[parent] = t.h[parent], t.h[i]
		i = parent
	}
}

// siftDown 把 i 处的元素下沉到合适位置。
func (t *TopK[ID]) siftDown(i int) {
	n := len(t.h)
	for {
		left, right := 2*i+1, 2*i+2
		worst := i

		if left < n && worse(t.h[left], t.h[worst]) {
			worst = left
		}
		if right < n && worse(t.h[right], t.h[worst]) {
			worst = right
		}
		if worst == i {
			return
		}

		t.h[i], t.h[worst] = t.h[worst], t.h[i]
		i = worst
	}
}

// better 报告 a 是否应当排在 b 前面：分数降序，同分按 ID 升序。
func better[ID cmp.Ordered](a, b Hit[ID]) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.ID < b.ID
}

// worse 是 better 的反面，小顶堆靠它把「最差」浮到堆顶。
func worse[ID cmp.Ordered](a, b Hit[ID]) bool { return better(b, a) }
