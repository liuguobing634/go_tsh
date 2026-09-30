package index

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/liuguobing/go_tsh/internal/analyzer"
)

// newTestIndex 返回一个关闭了停用词过滤的索引。
//
// 测试关心的是索引行为而不是停用词表内容，关掉过滤可以避免
// 用例里的普通英文单词恰好命中停用词表而让断言悄悄失效。
func newTestIndex(t *testing.T) *InvertedIndex {
	t.Helper()
	return New(Options{
		Analyzer: analyzer.NewStandardWith(analyzer.StandardOptions{KeepStopwords: true}),
	})
}

// assertSorted 校验 posting 列表按 DocID 严格升序。
func assertSorted(t *testing.T, postings []Posting) {
	t.Helper()
	for i := 1; i < len(postings); i++ {
		if postings[i].DocID <= postings[i-1].DocID {
			t.Fatalf("posting 未按 DocID 升序：postings[%d]=%d, postings[%d]=%d",
				i-1, postings[i-1].DocID, i, postings[i].DocID)
		}
	}
}

func TestTermKeyRoundTrip(t *testing.T) {
	key := TermKey("title", "hello")
	field, term, ok := SplitTermKey(key)
	if !ok || field != "title" || term != "hello" {
		t.Fatalf("SplitTermKey(%q) = (%q, %q, %v)", key, field, term, ok)
	}

	if _, _, ok := SplitTermKey("no-separator"); ok {
		t.Error("缺少分隔符时应返回 ok=false")
	}

	// 无歧义性：这是选 NUL 而非 ":" 作为分隔符的全部理由。
	if TermKey("a", "b:c") == TermKey("a:b", "c") {
		t.Error("不同 (字段,词条) 组合不应产生相同 key")
	}
}

func TestAddAndGet(t *testing.T) {
	ix := newTestIndex(t)

	id, err := ix.Add("doc-1", map[string]string{"title": "Hello World"})
	if err != nil {
		t.Fatalf("Add 返回错误: %v", err)
	}
	if id == InvalidDocID {
		t.Fatal("Add 不应返回 InvalidDocID")
	}

	doc, ok := ix.Get("doc-1")
	if !ok {
		t.Fatal("刚写入的文档应当能取到")
	}
	if doc.External != "doc-1" {
		t.Errorf("External = %q", doc.External)
	}
	if doc.Fields["title"] != "Hello World" {
		t.Errorf("Fields[title] = %q", doc.Fields["title"])
	}
	if doc.ID != id {
		t.Errorf("内部 DocID 不一致: %d vs %d", doc.ID, id)
	}
	if doc.TotalLen != 2 || doc.FieldLen["title"] != 2 {
		t.Errorf("token 数应为 2，实际 TotalLen=%d FieldLen=%d", doc.TotalLen, doc.FieldLen["title"])
	}

	// 外部 ID 两端空白应被裁掉后仍然一致。
	if _, ok := ix.Get("  doc-1  "); !ok {
		t.Error("带空白的查询也应命中")
	}
	if _, ok := ix.Get("doc-2"); ok {
		t.Error("不存在的文档不应命中")
	}
}

func TestAddValidation(t *testing.T) {
	t.Run("空外部 ID", func(t *testing.T) {
		_, err := newTestIndex(t).Add("   ", map[string]string{"body": "x"})
		if !errors.Is(err, ErrEmptyExternalID) {
			t.Fatalf("err = %v, want ErrEmptyExternalID", err)
		}
	})

	t.Run("没有字段", func(t *testing.T) {
		_, err := newTestIndex(t).Add("d1", map[string]string{})
		if !errors.Is(err, ErrNoFields) {
			t.Fatalf("err = %v, want ErrNoFields", err)
		}
	})

	t.Run("字段名为空白", func(t *testing.T) {
		_, err := newTestIndex(t).Add("d1", map[string]string{"  ": "x"})
		if !errors.Is(err, ErrEmptyFieldName) {
			t.Fatalf("err = %v, want ErrEmptyFieldName", err)
		}
	})

	t.Run("字段数超限", func(t *testing.T) {
		ix := New(Options{
			MaxFields: 2,
			Analyzer:  analyzer.NewStandardWith(analyzer.StandardOptions{KeepStopwords: true}),
		})
		_, err := ix.Add("d1", map[string]string{"a": "1", "b": "2", "c": "3"})
		if !errors.Is(err, ErrTooManyFields) {
			t.Fatalf("err = %v, want ErrTooManyFields", err)
		}
	})

	t.Run("token 数超限", func(t *testing.T) {
		ix := New(Options{
			MaxTokens: 3,
			Analyzer:  analyzer.NewStandardWith(analyzer.StandardOptions{KeepStopwords: true}),
		})
		_, err := ix.Add("d1", map[string]string{"body": "one two three four"})
		if !errors.Is(err, ErrDocumentTooLarge) {
			t.Fatalf("err = %v, want ErrDocumentTooLarge", err)
		}
	})

	t.Run("校验失败不得留下任何痕迹", func(t *testing.T) {
		ix := New(Options{
			MaxFields: 1,
			Analyzer:  analyzer.NewStandardWith(analyzer.StandardOptions{KeepStopwords: true}),
		})
		if _, err := ix.Add("d1", map[string]string{"a": "x", "b": "y"}); err == nil {
			t.Fatal("期望报错")
		}
		if ix.DocCount() != 0 {
			t.Errorf("失败的新增不应改变文档数，实际 %d", ix.DocCount())
		}
		if got := ix.Postings("a", "x"); len(got) != 0 {
			t.Errorf("失败的新增不应写入 posting，实际 %v", got)
		}
	})
}

func TestAddDuplicate(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "alpha"}); err != nil {
		t.Fatal(err)
	}

	_, err := ix.Add("d1", map[string]string{"body": "beta"})
	if !errors.Is(err, ErrDocumentExists) {
		t.Fatalf("err = %v, want ErrDocumentExists", err)
	}

	// 失败的新增不应污染索引。
	if got := ix.Postings("body", "beta"); len(got) != 0 {
		t.Errorf("重复新增不应写入新内容，实际 %v", got)
	}
	if ix.DocCount() != 1 {
		t.Errorf("文档数 = %d, want 1", ix.DocCount())
	}
}

func TestTermFrequencyAndPositions(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "go go now go"}); err != nil {
		t.Fatal(err)
	}

	postings := ix.Postings("body", "go")
	if len(postings) != 1 {
		t.Fatalf("postings 长度 = %d, want 1", len(postings))
	}
	if postings[0].TF != 3 {
		t.Errorf("TF = %d, want 3", postings[0].TF)
	}
	if want := []uint32{0, 1, 3}; !slices.Equal(postings[0].Positions, want) {
		t.Errorf("Positions = %v, want %v", postings[0].Positions, want)
	}

	now := ix.Postings("body", "now")
	if len(now) != 1 || now[0].TF != 1 || !slices.Equal(now[0].Positions, []uint32{2}) {
		t.Errorf("now 的 posting 异常: %+v", now)
	}

	if got := ix.DocFreq("body", "go"); got != 1 {
		t.Errorf("DocFreq = %d, want 1", got)
	}
	if got := ix.DocFreq("body", "missing"); got != 0 {
		t.Errorf("不存在词条的 DocFreq = %d, want 0", got)
	}
}

// 不同字段的 posting 必须完全隔离，位置也各自从 0 开始。
func TestFieldIsolation(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"title": "go", "body": "go go"}); err != nil {
		t.Fatal(err)
	}

	title := ix.Postings("title", "go")
	body := ix.Postings("body", "go")

	if len(title) != 1 || title[0].TF != 1 {
		t.Errorf("title 的 posting 异常: %+v", title)
	}
	if len(body) != 1 || body[0].TF != 2 {
		t.Errorf("body 的 posting 异常: %+v", body)
	}
	if !slices.Equal(title[0].Positions, []uint32{0}) || !slices.Equal(body[0].Positions, []uint32{0, 1}) {
		t.Error("各字段的位置应各自从 0 开始")
	}

	// 在 title 里查 body 才有的词应当查不到。
	if got := ix.Postings("title", "missing"); len(got) != 0 {
		t.Errorf("跨字段不应互相命中，实际 %v", got)
	}
}

func TestPostingsStaySortedByDocID(t *testing.T) {
	ix := newTestIndex(t)

	const n = 30
	ids := make([]DocID, n)
	for i := 0; i < n; i++ {
		ext := fmt.Sprintf("doc-%02d", i)
		id, err := ix.Add(ext, map[string]string{"body": "common term"})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	assertSorted(t, ix.Postings("body", "common"))

	// 删除中间若干篇，再补写新的，顺序必须依然成立。
	for i := 0; i < n; i += 3 {
		if err := ix.Delete(fmt.Sprintf("doc-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	assertSorted(t, ix.Postings("body", "common"))

	for i := 0; i < 5; i++ {
		if _, err := ix.Add(fmt.Sprintf("late-%d", i), map[string]string{"body": "common term"}); err != nil {
			t.Fatal(err)
		}
	}
	sorted := ix.Postings("body", "common")
	assertSorted(t, sorted)

	// 被删掉的那几篇不应再出现。
	for _, p := range sorted {
		for i := 0; i < n; i += 3 {
			if p.DocID == ids[i] {
				t.Errorf("已删除的 doc-%02d 仍出现在 posting 中", i)
			}
		}
	}
}

func TestUpdate(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "alpha beta"}); err != nil {
		t.Fatal(err)
	}

	if _, err := ix.Update("d1", map[string]string{"body": "gamma"}); err != nil {
		t.Fatalf("Update 返回错误: %v", err)
	}

	if got := ix.Postings("body", "alpha"); len(got) != 0 {
		t.Errorf("更新后旧词条 alpha 应彻底消失，实际 %v", got)
	}
	if got := ix.Postings("body", "beta"); len(got) != 0 {
		t.Errorf("更新后旧词条 beta 应彻底消失，实际 %v", got)
	}
	if got := ix.Postings("body", "gamma"); len(got) != 1 {
		t.Errorf("更新后新词条 gamma 应可命中，实际 %v", got)
	}
	if ix.DocCount() != 1 {
		t.Errorf("文档数 = %d, want 1", ix.DocCount())
	}
	// alpha/beta 的 posting 空了应当连同 key 一起回收。
	if got := ix.Stats().Terms; got != 1 {
		t.Errorf("Terms = %d, want 1（旧词条应被回收）", got)
	}

	doc, _ := ix.Get("d1")
	if doc.Fields["body"] != "gamma" {
		t.Errorf("Fields[body] = %q", doc.Fields["body"])
	}
}

func TestUpdateAndDeleteMissingDocument(t *testing.T) {
	ix := newTestIndex(t)

	if _, err := ix.Update("nope", map[string]string{"body": "x"}); !errors.Is(err, ErrDocumentNotFound) {
		t.Errorf("Update 不存在的文档: err = %v, want ErrDocumentNotFound", err)
	}
	if err := ix.Delete("nope"); !errors.Is(err, ErrDocumentNotFound) {
		t.Errorf("Delete 不存在的文档: err = %v, want ErrDocumentNotFound", err)
	}
	if err := ix.Delete("   "); !errors.Is(err, ErrEmptyExternalID) {
		t.Errorf("Delete 空 ID: err = %v, want ErrEmptyExternalID", err)
	}
}

func TestDelete(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "alpha beta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "alpha gamma"}); err != nil {
		t.Fatal(err)
	}

	if err := ix.Delete("d1"); err != nil {
		t.Fatalf("Delete 返回错误: %v", err)
	}

	alpha := ix.Postings("body", "alpha")
	if len(alpha) != 1 {
		t.Fatalf("alpha 应只剩 d2，实际 %v", alpha)
	}
	if got := ix.DocFreq("body", "alpha"); got != 1 {
		t.Errorf("alpha 的 DF = %d, want 1", got)
	}
	if got := ix.Postings("body", "beta"); len(got) != 0 {
		t.Errorf("d1 独有的 beta 应彻底消失，实际 %v", got)
	}
	if ix.DocCount() != 1 {
		t.Errorf("文档数 = %d, want 1", ix.DocCount())
	}
	if _, ok := ix.Get("d1"); ok {
		t.Error("已删除的文档不应还能取到")
	}
	if ix.Has("d1") {
		t.Error("Has 应为 false")
	}

	// 删除后同一个外部 ID 应当可以重新新增。
	if _, err := ix.Add("d1", map[string]string{"body": "delta"}); err != nil {
		t.Errorf("删除后重新新增应成功: %v", err)
	}
}

func TestUpsert(t *testing.T) {
	ix := newTestIndex(t)

	if _, created, err := ix.Upsert("d1", map[string]string{"body": "alpha"}); err != nil || !created {
		t.Fatalf("首次 Upsert: created=%v err=%v, want true/nil", created, err)
	}
	if _, created, err := ix.Upsert("d1", map[string]string{"body": "beta"}); err != nil || created {
		t.Fatalf("二次 Upsert: created=%v err=%v, want false/nil", created, err)
	}

	if got := ix.Postings("body", "alpha"); len(got) != 0 {
		t.Errorf("覆盖后 alpha 应消失，实际 %v", got)
	}
	if got := ix.Postings("body", "beta"); len(got) != 1 {
		t.Errorf("覆盖后 beta 应命中，实际 %v", got)
	}
	if ix.DocCount() != 1 {
		t.Errorf("文档数 = %d, want 1", ix.DocCount())
	}
}

// 索引对外必须返回副本，否则调用方能绕过锁改坏内部状态。
func TestReturnedValuesAreCopies(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "alpha beta"}); err != nil {
		t.Fatal(err)
	}

	doc, _ := ix.Get("d1")
	doc.Fields["body"] = "tampered"
	doc.FieldLen["body"] = 999
	doc.TotalLen = 999

	again, _ := ix.Get("d1")
	if again.Fields["body"] != "alpha beta" {
		t.Errorf("修改 Get 的返回值影响了索引内部状态: %q", again.Fields["body"])
	}
	if again.FieldLen["body"] != 2 || again.TotalLen != 2 {
		t.Errorf("长度统计被外部修改污染: FieldLen=%d TotalLen=%d", again.FieldLen["body"], again.TotalLen)
	}

	postings := ix.Postings("body", "alpha")
	postings[0].Positions[0] = 999
	fresh := ix.Postings("body", "alpha")
	if fresh[0].Positions[0] != 0 {
		t.Errorf("修改 Postings 的返回值影响了索引内部状态: %v", fresh[0].Positions)
	}
}

func TestFieldsAreReclaimedAfterDelete(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"title": "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "beta"}); err != nil {
		t.Fatal(err)
	}

	if got := ix.Fields(); !slices.Equal(got, []string{"body", "title"}) {
		t.Fatalf("Fields() = %v, want [body title]", got)
	}

	if err := ix.Delete("d1"); err != nil {
		t.Fatal(err)
	}
	if got := ix.Fields(); !slices.Equal(got, []string{"body"}) {
		t.Errorf("删除后空字段应被回收，Fields() = %v", got)
	}

	if err := ix.Delete("d2"); err != nil {
		t.Fatal(err)
	}
	if got := ix.Fields(); len(got) != 0 {
		t.Errorf("全部删除后不应残留字段，Fields() = %v", got)
	}
	if got := ix.Stats(); got.Terms != 0 || got.Fields != 0 || got.Docs != 0 || got.TotalTokens != 0 {
		t.Errorf("全部删除后统计应归零，实际 %+v", got)
	}
}

func TestStats(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "alpha beta gamma"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "alpha"}); err != nil {
		t.Fatal(err)
	}

	s := ix.Stats()
	if s.Docs != 2 {
		t.Errorf("Docs = %d, want 2", s.Docs)
	}
	if s.Terms != 3 {
		t.Errorf("Terms = %d, want 3 (alpha/beta/gamma，d2 的 alpha 已计过)", s.Terms)
	}
	if s.Fields != 1 {
		t.Errorf("Fields = %d, want 1", s.Fields)
	}
	if s.TotalTokens != 4 {
		t.Errorf("TotalTokens = %d, want 4", s.TotalTokens)
	}
	if s.AvgDocLen != 2 {
		t.Errorf("AvgDocLen = %v, want 2", s.AvgDocLen)
	}
	if s.IndexBytes <= 0 {
		t.Errorf("IndexBytes = %d, want > 0", s.IndexBytes)
	}
}

func TestFieldStats(t *testing.T) {
	ix := newTestIndex(t)
	if _, err := ix.Add("d1", map[string]string{"body": "alpha beta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Add("d2", map[string]string{"body": "alpha beta gamma delta"}); err != nil {
		t.Fatal(err)
	}

	fs, ok := ix.FieldStats("body")
	if !ok {
		t.Fatal("body 字段应存在")
	}
	if fs.Docs != 2 || fs.TotalTokens != 6 || fs.AvgLength != 3 {
		t.Errorf("FieldStats = %+v, want docs=2 tokens=6 avg=3", fs)
	}

	if _, ok := ix.FieldStats("missing"); ok {
		t.Error("不存在的字段应返回 false")
	}
}

func TestDocLength(t *testing.T) {
	ix := newTestIndex(t)
	// 用双字母词：MinTokenLen 默认为 2，单字母词会被过滤掉。
	id, err := ix.Add("d1", map[string]string{"title": "aa bb", "body": "aa bb cc dd"})
	if err != nil {
		t.Fatal(err)
	}

	if got := ix.DocLength(id, "title"); got != 2 {
		t.Errorf("title 长度 = %d, want 2", got)
	}
	if got := ix.DocLength(id, "body"); got != 4 {
		t.Errorf("body 长度 = %d, want 4", got)
	}
	if got := ix.DocLength(id, "missing"); got != 0 {
		t.Errorf("不存在字段长度 = %d, want 0", got)
	}
	if got := ix.DocLength(DocID(9999), "body"); got != 0 {
		t.Errorf("不存在文档长度 = %d, want 0", got)
	}
}

// 多写多读并发。本机缺少 cgo 时 -race 跑不了，此处只能兜住 panic 与
// 明显的不变式破坏；真正的数据竞争检测等 -race 可用后由 CI 覆盖。
func TestConcurrentReadWrite(t *testing.T) {
	const (
		writers = 4
		readers = 4
		ops     = 150
		seeds   = 50
	)

	ix := newTestIndex(t)
	for i := 0; i < seeds; i++ {
		if _, err := ix.Add(fmt.Sprintf("seed-%d", i), map[string]string{
			"title": "concurrent index test",
			"body":  "common word alpha beta gamma",
		}); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				ext := fmt.Sprintf("w%d-%d", w, i)
				if _, err := ix.Add(ext, map[string]string{"body": "common concurrent wrote"}); err != nil {
					t.Errorf("并发 Add(%s): %v", ext, err)
					return
				}
				if _, err := ix.Update(ext, map[string]string{"body": "common concurrent updated"}); err != nil {
					t.Errorf("并发 Update(%s): %v", ext, err)
					return
				}
				if err := ix.Delete(ext); err != nil {
					t.Errorf("并发 Delete(%s): %v", ext, err)
					return
				}
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				assertSortedQuiet(t, ix.Postings("body", "common"))
				_ = ix.Stats()
				_ = ix.Fields()
				_, _ = ix.Get("seed-0")
				_ = ix.DocFreq("body", "common")
			}
		}()
	}

	wg.Wait()

	// 所有临时文档都被删干净了，只剩种子文档。
	if got := ix.DocCount(); got != seeds {
		t.Errorf("并发结束后文档数 = %d, want %d", got, seeds)
	}
	if got := ix.Postings("body", "concurrent"); len(got) != 0 {
		t.Errorf("临时文档的 posting 应全部清理，实际残留 %d 条", len(got))
	}
	assertSorted(t, ix.Postings("body", "common"))
}

// assertSortedQuiet 是 assertSorted 的并发安全版本：只记录错误，不终止测试。
func assertSortedQuiet(t *testing.T, postings []Posting) {
	t.Helper()
	for i := 1; i < len(postings); i++ {
		if postings[i].DocID <= postings[i-1].DocID {
			t.Errorf("并发下 posting 顺序被破坏：%d 出现在 %d 之后",
				postings[i].DocID, postings[i-1].DocID)
			return
		}
	}
}

// ---------------------------------------------------------------- 基准测试

func BenchmarkAddDocument(b *testing.B) {
	ix := New(Options{})
	fields := map[string]string{
		"title": "Inverted index design notes",
		"body":  strings.Repeat("the quick brown fox jumps over the lazy dog ", 15),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := ix.Add("doc-"+strconv.Itoa(i), fields); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDeleteDocument(b *testing.B) {
	ix := New(Options{})
	fields := map[string]string{
		"title": "Inverted index design notes",
		"body":  strings.Repeat("the quick brown fox jumps over the lazy dog ", 15),
	}
	for i := 0; i < b.N; i++ {
		if _, err := ix.Add("doc-"+strconv.Itoa(i), fields); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := ix.Delete("doc-" + strconv.Itoa(i)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPostingsLookup(b *testing.B) {
	ix := New(Options{})
	for i := 0; i < 10_000; i++ {
		if _, err := ix.Add("doc-"+strconv.Itoa(i), map[string]string{
			"body": "the quick brown fox jumps over the lazy dog",
		}); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = ix.Postings("body", "quick")
	}
}
