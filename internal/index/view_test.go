package index

import (
	"fmt"
	"strconv"
	"testing"
)

func TestViewProvidesConsistentSnapshot(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "alpha beta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "alpha gamma"}); err != nil {
		t.Fatal(err)
	}

	var alphaIDs, betaIDs []DocID

	ix.View(func(v *View) {
		if got := v.DocCount(); got != 2 {
			t.Errorf("View.DocCount() = %d, want 2", got)
		}
		if got := v.Fields(); len(got) != 1 || got[0] != "body" {
			t.Errorf("View.Fields() = %v", got)
		}
		if fs, ok := v.FieldStats("body"); !ok || fs.Docs != 2 {
			t.Errorf("View.FieldStats(body) = %+v, %v", fs, ok)
		}
		if got := v.DocFreq("body", "alpha"); got != 2 {
			t.Errorf("View.DocFreq(body, alpha) = %d, want 2", got)
		}

		v.ScanIDs("body", "alpha", func(id DocID, tf uint32) bool {
			if tf != 1 {
				t.Errorf("alpha 的 TF = %d, want 1", tf)
			}
			alphaIDs = append(alphaIDs, id)
			return true
		})
		v.ScanIDs("body", "beta", func(id DocID, tf uint32) bool {
			betaIDs = append(betaIDs, id)
			return true
		})

		if len(alphaIDs) > 0 {
			if got := v.DocLength(alphaIDs[0], "body"); got != 2 {
				t.Errorf("View.DocLength = %d, want 2", got)
			}
			doc, ok := v.Document(alphaIDs[0])
			if !ok || doc.External != "d1" {
				t.Errorf("View.Document = %+v, %v", doc, ok)
			}
		}
	})

	if len(alphaIDs) != 2 {
		t.Fatalf("alpha 命中 %d 篇，want 2", len(alphaIDs))
	}
	if len(betaIDs) != 1 {
		t.Fatalf("beta 命中 %d 篇，want 1", len(betaIDs))
	}
	// 同一篇文档在不同词条下必须得到同一个 DocID。
	if alphaIDs[0] != betaIDs[0] {
		t.Errorf("同一文档的 DocID 不一致: %d vs %d", alphaIDs[0], betaIDs[0])
	}
}

func TestViewScanEarlyExit(t *testing.T) {
	ix := newTestIndex(t)
	for i := 0; i < 10; i++ {
		if _, err := ix.Add(fmt.Sprintf("d%d", i), map[string]string{"body": "common"}); err != nil {
			t.Fatal(err)
		}
	}

	visited := 0
	ix.View(func(v *View) {
		v.ScanIDs("body", "common", func(DocID, uint32) bool {
			visited++
			return visited < 3
		})
	})

	if visited != 3 {
		t.Errorf("提前结束应恰好访问 3 条，实际 %d", visited)
	}
}

func TestViewScanMissingTerm(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "alpha"}); err != nil {
		t.Fatal(err)
	}

	called := false
	ix.View(func(v *View) {
		v.ScanIDs("body", "nope", func(DocID, uint32) bool {
			called = true
			return true
		})
		v.Scan("missing-field", "alpha", func(DocID, uint32, []uint32) bool {
			called = true
			return true
		})
	})

	if called {
		t.Error("不存在的词条或字段不应触发回调")
	}
}

func TestViewEachDocument(t *testing.T) {
	ix := newTestIndex(t)
	for i := 0; i < 5; i++ {
		if _, err := ix.Add(fmt.Sprintf("d%d", i), map[string]string{"body": "token"}); err != nil {
			t.Fatal(err)
		}
	}

	seen := 0
	ix.View(func(v *View) {
		v.EachDocument(func(DocID) bool {
			seen++
			return true
		})
	})
	if seen != 5 {
		t.Errorf("EachDocument 遍历 %d 篇，want 5", seen)
	}

	seen = 0
	ix.View(func(v *View) {
		v.EachDocument(func(DocID) bool {
			seen++
			return false
		})
	})
	if seen != 1 {
		t.Errorf("提前结束时 EachDocument 应只访问 1 篇，实际 %d", seen)
	}
}

// View 内不得调用写方法（会死锁），这里只验证读路径本身不会互相阻塞，
// 以及 View 结束后索引仍然可用。
func TestViewReleasesLock(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "alpha"}); err != nil {
		t.Fatal(err)
	}

	ix.View(func(v *View) { _ = v.DocCount() })

	// View 返回后写操作必须能正常进行，否则说明读锁泄漏了。
	if _, err := ix.Add("d2", map[string]string{"body": "beta"}); err != nil {
		t.Fatalf("View 结束后写操作失败: %v", err)
	}
	if got := ix.DocCount(); got != 2 {
		t.Errorf("DocCount = %d, want 2", got)
	}
}

// ---------------------------------------------------------------- 基准测试

// benchIDs 是一个包级 sink，防止编译器把扫描循环优化掉。
var benchIDs []DocID

// BenchmarkPostingsCopy 是 Phase 2 的旧路径：每次查询深拷贝整条 posting。
func BenchmarkPostingsCopy(b *testing.B) {
	ix := benchmarkIndex(b, 10_000)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		postings := ix.Postings("body", "quick")
		ids := make([]DocID, 0, len(postings))
		for _, p := range postings {
			ids = append(ids, p.DocID)
		}
		benchIDs = ids
	}
}

// BenchmarkViewScan 是 Phase 3 的零拷贝路径。
func BenchmarkViewScan(b *testing.B) {
	ix := benchmarkIndex(b, 10_000)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		var n int
		ix.View(func(v *View) {
			v.ScanIDs("body", "quick", func(DocID, uint32) bool {
				n++
				return true
			})
		})
		if n != 10_000 {
			b.Fatalf("命中 %d 条，want 10000", n)
		}
	}
}

func BenchmarkViewScanPhrase(b *testing.B) {
	ix := benchmarkIndex(b, 10_000)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		var n int
		ix.View(func(v *View) {
			v.Scan("body", "quick", func(_ DocID, _ uint32, positions []uint32) bool {
				if len(positions) > 0 {
					n++
				}
				return true
			})
		})
		if n != 10_000 {
			b.Fatalf("命中 %d 条，want 10000", n)
		}
	}
}

func benchmarkIndex(b *testing.B, docs int) *InvertedIndex {
	b.Helper()

	ix := New(Options{})
	fields := map[string]string{
		"body": "the quick brown fox jumps over the lazy dog",
	}
	for i := 0; i < docs; i++ {
		if _, err := ix.Add("doc-"+strconv.Itoa(i), fields); err != nil {
			b.Fatal(err)
		}
	}
	return ix
}
