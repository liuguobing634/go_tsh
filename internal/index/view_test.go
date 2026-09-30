package index

import (
	"fmt"
	"slices"
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

func TestViewSortedDocIDs(t *testing.T) {
	ix := newTestIndex(t)

	var want []DocID
	for i := 0; i < 5; i++ {
		ext := fmt.Sprintf("d%d", i)
		if _, err := ix.Add(ext, map[string]string{"body": "token"}); err != nil {
			t.Fatal(err)
		}
		want = append(want, docIDOf(t, ix, ext))
	}

	ix.View(func(v *View) {
		got := v.SortedDocIDs()
		if !slices.Equal(got, want) {
			t.Fatalf("SortedDocIDs() = %v, want %v", got, want)
		}
	})

	// 删除中间一篇后仍应有序、且不再包含被删的 ID。
	if err := ix.Delete("d2"); err != nil {
		t.Fatal(err)
	}

	ix.View(func(v *View) {
		got := v.SortedDocIDs()
		if len(got) != 4 {
			t.Fatalf("删除后应有 4 篇，实际 %d", len(got))
		}
		for i := 1; i < len(got); i++ {
			if got[i] <= got[i-1] {
				t.Fatalf("必须严格升序：%v", got)
			}
		}
		for _, id := range got {
			if id == want[2] {
				t.Errorf("已删除的 %d 不应出现", id)
			}
		}
	})
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

// ---------------------------------------------------------------- 游标

func TestViewPostingFor(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "go go now go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "go"}); err != nil {
		t.Fatal(err)
	}

	d1 := docIDOf(t, ix, "d1")

	ix.View(func(v *View) {
		tf, positions, ok := v.PostingFor("body", "go", d1)
		if !ok {
			t.Fatal("d1 应当含有 go")
		}
		if tf != 3 {
			t.Errorf("TF = %d, want 3", tf)
		}
		if want := []uint32{0, 1, 3}; !slices.Equal(positions, want) {
			t.Errorf("Positions = %v, want %v", positions, want)
		}

		if _, _, ok := v.PostingFor("body", "nope", d1); ok {
			t.Error("不存在的词条应返回 ok=false")
		}
		if _, _, ok := v.PostingFor("body", "go", DocID(9999)); ok {
			t.Error("不存在的文档应返回 ok=false")
		}
		if _, _, ok := v.PostingFor("missing", "go", d1); ok {
			t.Error("不存在的字段应返回 ok=false")
		}
	})
}

func TestViewCursor(t *testing.T) {
	ix := newTestIndex(t)
	for i := 0; i < 5; i++ {
		if _, err := ix.Add(fmt.Sprintf("d%d", i), map[string]string{"body": "common token"}); err != nil {
			t.Fatal(err)
		}
	}

	ix.View(func(v *View) {
		var ids []DocID
		c := v.Cursor("body", "common")
		for !c.Done() {
			ids = append(ids, c.DocID())
			if c.TF() != 1 {
				t.Errorf("TF = %d, want 1", c.TF())
			}
			c.Next()
		}
		if len(ids) != 5 {
			t.Fatalf("游标遍历 %d 条，want 5", len(ids))
		}
		assertSorted(t, postingsFromIDs(ids))
	})

	// 空游标：不存在的词条不应 panic。
	ix.View(func(v *View) {
		c := v.Cursor("body", "nope")
		if !c.Done() {
			t.Error("不存在的词条应返回已结束的游标")
		}
		if c.DocID() != InvalidDocID || c.TF() != 0 || c.Positions() != nil {
			t.Error("越界游标的取值应全部为零值")
		}
		c.Next()
	})
}

func TestViewCursorSeekIsMonotonic(t *testing.T) {
	ix := newTestIndex(t)
	const n = 20
	for i := 0; i < n; i++ {
		if _, err := ix.Add(fmt.Sprintf("d%02d", i), map[string]string{"body": "common"}); err != nil {
			t.Fatal(err)
		}
	}

	ix.View(func(v *View) {
		c := v.Cursor("body", "common")

		// 取出全部 DocID 作为参照。
		var all []DocID
		for probe := v.Cursor("body", "common"); !probe.Done(); probe.Next() {
			all = append(all, probe.DocID())
		}

		// 按递增顺序 Seek 每一个，应当逐个命中。
		for _, want := range all {
			if !c.Seek(want) {
				t.Fatalf("Seek(%d) 应命中", want)
			}
			if c.DocID() != want {
				t.Fatalf("Seek(%d) 停在 %d", want, c.DocID())
			}
		}

		// 已经走到末尾，再 Seek 一个更大的值应当返回 false 且不回退。
		if c.Seek(DocID(9999)) {
			t.Error("越过末尾的 Seek 应返回 false")
		}
		if !c.Done() {
			t.Errorf("Seek 越界后游标应处于结束状态，实际停在 %d", c.DocID())
		}
	})
}

// docIDOf 按外部 ID 取内部 DocID。
func docIDOf(t *testing.T, ix *InvertedIndex, external string) DocID {
	t.Helper()

	d, ok := ix.Get(external)
	if !ok {
		t.Fatalf("文档 %q 不存在", external)
	}
	return d.ID
}

// postingsFromIDs 把 DocID 列表包成 Posting 切片，便于复用 assertSorted。
func postingsFromIDs(ids []DocID) []Posting {
	out := make([]Posting, len(ids))
	for i, id := range ids {
		out[i] = Posting{DocID: id}
	}
	return out
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
