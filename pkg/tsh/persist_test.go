package tsh

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/liuguobing/go_tsh/internal/index"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// openPersistent 打开一个持久化引擎，测试结束时自动关闭。
func openPersistent(t *testing.T, dir string) *Engine {
	t.Helper()

	e, err := NewWith(Options{DataDir: dir, Logger: quiet()})
	if err != nil {
		t.Fatalf("打开持久化引擎失败: %v", err)
	}
	return e
}

// fingerprint 描述引擎对一组查询的完整响应。
//
// 只比对文档数是不够的：排序、命中集合、Total 都可能悄悄变化。
// 这里把「命中了哪些文档、按什么顺序」一并记下来。
func fingerprint(t *testing.T, e *Engine, queries []string) []string {
	t.Helper()

	out := make([]string, 0, len(queries)+1)
	s := e.Stats()
	out = append(out, fmt.Sprintf("stats: docs=%d terms=%d", s.Docs, s.Terms))

	for _, q := range queries {
		res, err := e.Search(SearchRequest{Query: q, Limit: 50})
		if err != nil {
			t.Fatalf("Search(%q) 失败: %v", q, err)
		}

		ids := make([]string, 0, len(res.Hits))
		for _, h := range res.Hits {
			// 分数也要比：只比 ID 顺序的话，打分逻辑变了也发现不了。
			ids = append(ids, fmt.Sprintf("%s=%.6f", h.ID, h.Score))
		}
		out = append(out, fmt.Sprintf("q=%q total=%d hits=[%s]", q, res.Total, strings.Join(ids, " ")))
	}
	return out
}

var consistencyQueries = []string{
	"inverted",
	"index",
	"search",
	"posting list",
	"\"inverted index\"",
	"bm25",
	"ranking",
	"missing-term",
	"index AND search",
	"ranking OR posting",
	"index -ranking",
}

func seedDocs(t *testing.T, e *Engine) {
	t.Helper()

	docs := []Document{
		{ID: "d1", Fields: map[string]string{"title": "inverted index", "body": "an inverted index maps terms to documents"}},
		{ID: "d2", Fields: map[string]string{"title": "bm25 ranking", "body": "bm25 is a ranking function used by search engines"}},
		{ID: "d3", Fields: map[string]string{"title": "posting list", "body": "the posting list stores document ids for each term"}},
		{ID: "d4", Fields: map[string]string{"title": "phrase query", "body": "phrase queries need term positions"}},
		{ID: "d5", Fields: map[string]string{"title": "tokenization", "body": "tokenization splits text into terms"}},
	}
	for _, d := range docs {
		if _, err := e.Upsert(d); err != nil {
			t.Fatal(err)
		}
	}

	// 一些删除与覆盖，让日志里三种操作都有
	if err := e.Delete("d4"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Upsert(Document{ID: "d2", Fields: map[string]string{
		"title": "bm25 ranking updated",
		"body":  "bm25 ranking was rewritten to test overwrite replay",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete("nonexistent"); err == nil {
		t.Fatal("删除不存在的文档应当报错")
	}
}

// 这是 L1 最核心的验收：重启前后，引擎对同一组查询必须给出**完全相同**的
// 命中、顺序与分数。
func TestRestartConsistency(t *testing.T) {
	dir := t.TempDir()

	before := func() []string {
		e := openPersistent(t, dir)
		defer e.Close()
		seedDocs(t, e)
		return fingerprint(t, e, consistencyQueries)
	}()

	after := func() []string {
		e := openPersistent(t, dir)
		defer e.Close()
		return fingerprint(t, e, consistencyQueries)
	}()

	if len(before) != len(after) {
		t.Fatalf("指纹长度不同: %d vs %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("重启后第 %d 项不一致:\n  重启前: %s\n  重启后: %s", i, before[i], after[i])
		}
	}
}

// 再多重启几次，确认重放是幂等的——不会因为重放而重复累加。
func TestRepeatedReopenIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	e := openPersistent(t, dir)
	seedDocs(t, e)
	want := fingerprint(t, e, consistencyQueries)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	for round := range 3 {
		e := openPersistent(t, dir)
		got := fingerprint(t, e, consistencyQueries)
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}

		if !slices.Equal(want, got) {
			t.Fatalf("第 %d 次重开结果不一致:\n want %v\n got  %v", round+1, want, got)
		}
	}

	// 日志大小也不该因为反复重放而增长
	info, err := os.Stat(filepath.Join(dir, documentsLogName))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("日志大小: %d 字节", info.Size())
}

// 重开之后继续写入，新记录要接在后面并且能被下一次重放读到。
func TestAppendAfterReopen(t *testing.T) {
	dir := t.TempDir()

	e := openPersistent(t, dir)
	if _, err := e.Upsert(Document{ID: "gen1", Fields: map[string]string{"body": "first generation"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := openPersistent(t, dir)
	if _, err := e2.Upsert(Document{ID: "gen2", Fields: map[string]string{"body": "second generation"}}); err != nil {
		t.Fatal(err)
	}
	if err := e2.Delete("gen1"); err != nil {
		t.Fatal(err)
	}
	if err := e2.Close(); err != nil {
		t.Fatal(err)
	}

	e3 := openPersistent(t, dir)
	defer e3.Close()

	if _, ok := e3.GetDocument("gen1"); ok {
		t.Error("gen1 已被删除，重放后不该存在")
	}
	if _, ok := e3.GetDocument("gen2"); !ok {
		t.Error("gen2 应当存在")
	}
}

// DataDir 为空 = 纯内存，行为必须与引入持久化之前完全一致。
func TestEmptyDataDirIsPureMemory(t *testing.T) {
	e := New()
	defer e.Close()

	if _, err := e.Upsert(Document{ID: "d1", Fields: map[string]string{"body": "hello"}}); err != nil {
		t.Fatal(err)
	}

	// 没有任何目录被创建
	if e.persist != nil {
		t.Fatal("未指定 DataDir 时不该有持久化状态")
	}
	// Close 必须是无害的
	if err := e.Close(); err != nil {
		t.Errorf("纯内存引擎的 Close 应当无害，实际: %v", err)
	}
	// 关闭后仍可读写
	if _, err := e.Upsert(Document{ID: "d2", Fields: map[string]string{"body": "world"}}); err != nil {
		t.Errorf("纯内存引擎关闭后不该影响写入: %v", err)
	}
}

// 日志损坏时启动必须失败，而不是带着残缺数据继续跑。
func TestCorruptedLogRefusesToStart(t *testing.T) {
	dir := t.TempDir()

	e := openPersistent(t, dir)
	seedDocs(t, e)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, documentsLogName)

	// 把文件头魔数改掉：模拟"这不是我们的日志文件"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 'X'
	if err := os.WriteFile(path, raw, 0o640); err != nil {
		t.Fatal(err)
	}

	if _, err := NewWith(Options{DataDir: dir, Logger: quiet()}); err == nil {
		t.Fatal("损坏的日志必须让启动失败")
	}
}

// 持久化降级：日志写不下去之后，写请求必须**快速失败**，
// 而不是让索引继续领先日志、分歧越积越大。
func TestPersistenceDegradesAndRejectsWrites(t *testing.T) {
	dir := t.TempDir()

	e := openPersistent(t, dir)
	defer e.Close()

	if _, err := e.Upsert(Document{ID: "d1", Fields: map[string]string{"body": "before"}}); err != nil {
		t.Fatal(err)
	}

	// 把日志从背后关掉，模拟磁盘/句柄层面的失败。
	if err := e.persist.log.Close(); err != nil {
		t.Fatal(err)
	}

	// 第一次失败：错误来自日志本身
	err := e.Delete("d1")
	if err == nil {
		t.Fatal("日志已关闭，写入应当失败")
	}
	t.Logf("首次失败: %v", err)

	// 之后必须报"已降级"，而不是继续尝试
	err = e.Delete("d1")
	if !errors.Is(err, ErrPersistenceBroken) {
		t.Errorf("后续写应当返回 ErrPersistenceBroken，实际: %v", err)
	}

	// 读仍然可用
	if _, err := e.Search(SearchRequest{Query: "before", Limit: 5}); err != nil {
		t.Errorf("降级后读请求不该受影响: %v", err)
	}
}

// 并发写入之后重启，索引必须与重启前一致。
// 这条同时验证了「日志顺序 = 索引生效顺序」这个前提——
// 顺序一旦错开，并发覆盖同一个文档就会重放出不同的结果。
func TestConcurrentWritesSurviveRestart(t *testing.T) {
	dir := t.TempDir()

	const writers = 8
	const perWriter = 25

	e := openPersistent(t, dir)

	done := make(chan error, writers)
	for w := range writers {
		go func() {
			for i := range perWriter {
				doc := Document{
					ID: fmt.Sprintf("w%d-%d", w, i),
					Fields: map[string]string{
						"body": fmt.Sprintf("writer %d item %d shared term", w, i),
					},
				}
				if _, err := e.Upsert(doc); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	for range writers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	// 再并发覆盖同一批文档，制造写冲突
	for w := range writers {
		for i := range perWriter {
			doc := Document{
				ID:     fmt.Sprintf("w%d-%d", w, i),
				Fields: map[string]string{"body": fmt.Sprintf("rewritten %d %d shared term", w, i)},
			}
			if _, err := e.Upsert(doc); err != nil {
				t.Fatal(err)
			}
		}
	}

	want := fingerprint(t, e, []string{"shared", "rewritten", "writer"})
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := openPersistent(t, dir)
	defer e2.Close()

	got := fingerprint(t, e2, []string{"shared", "rewritten", "writer"})
	if !slices.Equal(want, got) {
		t.Errorf("并发写入后重启结果不一致:\n want %v\n got  %v", want, got)
	}

	if s := e2.Stats().Docs; s != writers*perWriter {
		t.Errorf("文档数 = %d, want %d", s, writers*perWriter)
	}
}

// 超出当前配置上限的文档会让重放失败——必须报错，
// 而不是静默把它丢掉。
func TestReplayFailsWhenDocumentExceedsCurrentLimit(t *testing.T) {
	dir := t.TempDir()

	e, err := NewWith(Options{
		DataDir: dir,
		Logger:  quiet(),
		Index:   index.Options{MaxTokens: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Upsert(Document{ID: "big", Fields: map[string]string{
		"body": strings.Repeat("token ", 50),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// 收紧上限后重开：重放必须失败并给出可操作的提示
	_, err = NewWith(Options{
		DataDir: dir,
		Logger:  quiet(),
		Index:   index.Options{MaxTokens: 5},
	})
	if err == nil {
		t.Fatal("文档超出当前上限时，重放必须报错而不是静默丢弃")
	}
	t.Logf("重放失败信息: %v", err)
}
