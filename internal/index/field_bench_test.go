package index

import (
	"fmt"
	"testing"
)

// 这条基准回答的是选型的立足点：**稠密数值列的线性扫描到底有多贵**。
//
// 方案对比里我选了「稠密列 + 线性扫描」而不是「有序数组 + 二分」，
// 理由是前者按 DocID 顺序扫描、输出天然升序，能直接接进归并求值器。
// 代价是每次查询 O(N)——这个代价必须实测，不能靠"应该够快"糊过去。
//
// 参照物：同规模下一次词条查询的耗时。
func BenchmarkNumericRangeScan(b *testing.B) {
	for _, docs := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("docs=%d", docs), func(b *testing.B) {
			ix := New(Options{})
			if err := ix.DeclareField("price", FieldNumber); err != nil {
				b.Fatal(err)
			}

			for i := range docs {
				if _, err := ix.Add(fmt.Sprintf("d%d", i), map[string]string{
					"price": fmt.Sprint(i),
				}); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(docs), "docs/op")
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				ix.View(func(v *View) {
					// 窄范围：只命中很少的文档
					if _, err := v.NumericRange("price", 100, 200, true, true, nil); err != nil {
						b.Fatal(err)
					}
				})
			}
		})
	}
}

// 同规模下的词条查询，作为参照物。
func BenchmarkTermLookupReference(b *testing.B) {
	for _, docs := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("docs=%d", docs), func(b *testing.B) {
			ix := New(Options{})

			for i := range docs {
				if _, err := ix.Add(fmt.Sprintf("d%d", i), map[string]string{
					"body": "shared inverted index term",
				}); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(docs), "docs/op")
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				ix.View(func(v *View) {
					v.ScanIDs("body", "shared", func(DocID, uint32) bool { return false })
				})
			}
		})
	}
}
