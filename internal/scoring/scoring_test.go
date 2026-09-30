package scoring

import (
	"cmp"
	"math"
	"slices"
	"testing"
)

func TestIDFIsAlwaysPositive(t *testing.T) {
	bm := DefaultBM25()

	// 关键场景：df > N/2 时经典公式会给出负分，平滑写法必须保持为正。
	cases := []struct{ df, n uint32 }{
		{0, 100}, {1, 100}, {49, 100}, {50, 100}, {51, 100}, {99, 100}, {100, 100},
	}
	for _, c := range cases {
		if got := bm.IDF(c.df, c.n); got <= 0 {
			t.Errorf("IDF(df=%d, N=%d) = %v，必须恒为正", c.df, c.n, got)
		}
	}
}

func TestIDFDecreasesWithDocFreq(t *testing.T) {
	bm := DefaultBM25()

	prev := math.Inf(1)
	for df := uint32(0); df <= 100; df++ {
		got := bm.IDF(df, 100)
		if got >= prev {
			t.Fatalf("IDF 应随 df 增大而单调递减：df=%d 时 %v 未小于 %v", df, got, prev)
		}
		prev = got
	}
}

func TestIDFEmptyIndex(t *testing.T) {
	if got := DefaultBM25().IDF(0, 0); got != 0 {
		t.Errorf("空索引 IDF = %v, want 0", got)
	}
}

func TestScoreIncreasesWithTermFrequency(t *testing.T) {
	bm := DefaultBM25()
	const idf, docLen, avgLen = 1.0, 100.0, 100.0

	prev := 0.0
	for tf := uint32(1); tf <= 50; tf++ {
		got := bm.Score(idf, tf, docLen, avgLen)
		if got <= prev {
			t.Fatalf("分数应随 TF 单调递增：tf=%d 时 %v 未大于 %v", tf, got, prev)
		}
		prev = got
	}
}

// BM25 的核心特征：TF 的增益必须饱和，不能像 TF-IDF 那样线性增长。
func TestScoreSaturatesWithTermFrequency(t *testing.T) {
	bm := DefaultBM25()
	const idf, docLen, avgLen = 1.0, 100.0, 100.0

	gainLow := bm.Score(idf, 50, docLen, avgLen) - bm.Score(idf, 1, docLen, avgLen)
	gainHigh := bm.Score(idf, 500, docLen, avgLen) - bm.Score(idf, 50, docLen, avgLen)

	if gainHigh >= gainLow {
		t.Errorf("TF 增益应饱和：1→50 增量 %v，50→500 增量 %v", gainLow, gainHigh)
	}

	// 上界：tf 趋于无穷时分数收敛到 idf*(k1+1)。
	limit := idf * (bm.K1 + 1)
	if got := bm.Score(idf, 1_000_000, docLen, avgLen); got >= limit {
		t.Errorf("分数应有上界 %v，实际 %v", limit, got)
	}
}

func TestScorePenalizesLongDocuments(t *testing.T) {
	bm := DefaultBM25()

	short := bm.Score(1, 3, 50, 100)
	long := bm.Score(1, 3, 200, 100)

	if short <= long {
		t.Errorf("同样 TF 下短文档应得分更高：short=%v long=%v", short, long)
	}
}

func TestScoreWithBZeroIgnoresLength(t *testing.T) {
	bm := BM25{K1: 1.2, B: 0}

	a := bm.Score(1, 3, 10, 100)
	b := bm.Score(1, 3, 500, 100)

	if math.Abs(a-b) > 1e-12 {
		t.Errorf("B=0 时长度不应影响得分：%v vs %v", a, b)
	}
}

func TestScoreGuards(t *testing.T) {
	bm := DefaultBM25()

	if got := bm.Score(1, 0, 10, 10); got != 0 {
		t.Errorf("tf=0 应得 0 分，实际 %v", got)
	}
	if got := bm.Score(1, 3, 10, 0); got != 0 {
		t.Errorf("avgDocLen=0（空字段）应得 0 分，实际 %v", got)
	}
}

func TestTopKKeepsHighest(t *testing.T) {
	tk := NewTopK[uint32](3)
	for i := uint32(1); i <= 10; i++ {
		tk.Push(i, float64(i))
	}

	got := tk.Result()
	want := []Hit[uint32]{{ID: 10, Score: 10}, {ID: 9, Score: 9}, {ID: 8, Score: 8}}
	if !slices.Equal(got, want) {
		t.Fatalf("Result() = %v, want %v", got, want)
	}
	if tk.Len() != 3 {
		t.Errorf("Len() = %d, want 3", tk.Len())
	}
}

// 同分时必须有确定性的次序，否则结果会随 map 遍历顺序漂移。
func TestTopKTiesBreakByIDAscending(t *testing.T) {
	tk := NewTopK[uint32](3)
	for _, id := range []uint32{7, 3, 9, 1, 5} {
		tk.Push(id, 1.0)
	}

	got := tk.Result()
	want := []Hit[uint32]{{ID: 1, Score: 1}, {ID: 3, Score: 1}, {ID: 5, Score: 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("Result() = %v, want %v", got, want)
	}
}

func TestTopKEdgeCases(t *testing.T) {
	t.Run("k<=0 不收集", func(t *testing.T) {
		for _, k := range []int{0, -1, -100} {
			tk := NewTopK[uint32](k)
			tk.Push(1, 1)
			tk.Push(2, 2)
			if got := tk.Result(); len(got) != 0 {
				t.Errorf("k=%d 时 Result() = %v, want 空", k, got)
			}
		}
	})

	t.Run("候选少于 k", func(t *testing.T) {
		tk := NewTopK[uint32](5)
		tk.Push(2, 2)
		tk.Push(1, 1)

		got := tk.Result()
		want := []Hit[uint32]{{ID: 2, Score: 2}, {ID: 1, Score: 1}}
		if !slices.Equal(got, want) {
			t.Fatalf("Result() = %v, want %v", got, want)
		}
	})

	t.Run("空收集器", func(t *testing.T) {
		if got := NewTopK[uint32](3).Result(); len(got) != 0 {
			t.Errorf("Result() = %v, want 空", got)
		}
	})
}

// 用全量排序做对照，覆盖各种 k 与堆操作路径（含 siftUp / siftDown / 替换堆顶）。
func TestTopKMatchesFullSort(t *testing.T) {
	const n = 500

	hits := make([]Hit[uint32], n)
	for i := range hits {
		// 刻意制造大量同分，把 tie-break 也压进对照里。
		hits[i] = Hit[uint32]{ID: uint32(i), Score: float64((i * 7919) % 50)}
	}

	for _, k := range []int{1, 2, 7, 50, n - 1, n, n + 10} {
		tk := NewTopK[uint32](k)
		for _, h := range hits {
			tk.Push(h.ID, h.Score)
		}

		want := slices.Clone(hits)
		slices.SortFunc(want, func(a, b Hit[uint32]) int {
			if c := cmp.Compare(b.Score, a.Score); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})
		if k < len(want) {
			want = want[:k]
		}

		got := tk.Result()
		if !slices.Equal(got, want) {
			t.Fatalf("k=%d 结果与全量排序不一致\n got: %v\nwant: %v", k, got, want)
		}
	}
}

// ---------------------------------------------------------------- 基准测试

// 收集器写进包级变量，避免编译器把整个循环优化掉。
var benchSink []Hit[uint32]

func BenchmarkTopK10From100k(b *testing.B) {
	scores := make([]float64, 100_000)
	for i := range scores {
		scores[i] = float64((i * 2654435761) % 1_000_000)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		tk := NewTopK[uint32](10)
		for id, s := range scores {
			tk.Push(uint32(id), s)
		}
		benchSink = tk.Result()
	}
}

func BenchmarkBM25Score(b *testing.B) {
	bm := DefaultBM25()
	idf := bm.IDF(1000, 100_000)

	b.ReportAllocs()
	b.ResetTimer()

	total := 0.0
	for i := 0; i < b.N; i++ {
		total += bm.Score(idf, uint32(i%20+1), 120, 100)
	}
	_ = total
}
