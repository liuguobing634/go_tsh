package tsh

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/tablename"
)

// seedUpdates 往默认表里灌 docs 篇文档，每篇再更新 updates 次。
//
// 这正是压实要对付的形态：日志里的记录数 = docs × (updates+1)。
//
// **注意自动压实**：记录数一旦越过阈值就会自己压回去，所以不能假设
// 「写入次数 == 记录数」。需要精确控制日志规模时，
// 请让 docs×(updates+1) 保持在 compactMinRecords 以下。
func seedUpdates(t *testing.T, e *Engine, docs, updates int) {
	t.Helper()

	body := strings.Repeat("轻量级全文搜索引擎 ", 8)
	for round := 0; round <= updates; round++ {
		for i := range docs {
			if _, err := e.Upsert(Document{
				ID:     fmt.Sprintf("d%d", i),
				Fields: map[string]string{"body": fmt.Sprintf("%s round%d", body, round)},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// searchAll 检索并返回**最多 100 条**命中的 ID。
//
// ⚠️ 不要用它来「数文档有没有丢」：引擎把单次返回硬性截到 100 条
// （internal/query 的 maxLimit），300 篇文档只会回来 100 条，
// 于是得出「丢了 200 篇」的假结论。数数请用 countMatches。
func searchAll(t *testing.T, tb *Table, query string) []string {
	t.Helper()

	res, err := tb.Search(SearchRequest{Query: query, Limit: 100_000})
	if err != nil {
		t.Fatalf("表 %q 检索 %q 失败: %v", tb.Name(), query, err)
	}

	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.ID)
	}
	return out
}

// countMatches 返回命中的**总数**，不受返回条数上限影响。
func countMatches(t *testing.T, tb *Table, query string) int {
	t.Helper()

	res, err := tb.Search(SearchRequest{Query: query, Limit: 1})
	if err != nil {
		t.Fatalf("表 %q 检索 %q 失败: %v", tb.Name(), query, err)
	}
	return res.Total
}

// 手动压实的测试规模：记录数必须**低于**自动压实的下限，
// 否则日志会被自动压掉，测不到「手动压实把一个膨胀的日志压小」。
//
// 写成算式而不是硬编码，这样改了常量测试仍然自洽。
var (
	compactTestDocs    = int(compactMinRecords)/6 - 1 // 留出余量
	compactTestUpdates = 5
)

// **核心验收：压实让日志变小，且重启不再随历史写入次数增长。**
func TestCompactionShrinksLog(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	seedUpdates(t, e, compactTestDocs, compactTestUpdates)

	tb := mustDefault(t, e)
	beforeBytes := tb.LogBytes()
	beforeRecords := tb.LogRecords()

	// 这一批规模刻意压在自动压实的下限之下，所以记录数应当正好是写入次数
	wantRecords := int64(compactTestDocs*(compactTestUpdates+1) + 1) // +1 是 body 的 declare
	if beforeRecords != wantRecords {
		t.Fatalf("压实前记录数 = %d, want %d（自动压实不该在这个规模触发）",
			beforeRecords, wantRecords)
	}

	if err := tb.Compact(); err != nil {
		t.Fatalf("压实失败: %v", err)
	}

	afterBytes := tb.LogBytes()
	afterRecords := tb.LogRecords()

	t.Logf("日志 %d 字节 -> %d 字节（%.1f 倍）",
		beforeBytes, afterBytes, float64(beforeBytes)/float64(afterBytes))
	t.Logf("记录 %d 条 -> %d 条", beforeRecords, afterRecords)

	// 记录数必须回到文档数量级——这是重启时间的直接决定因素
	if afterRecords > int64(compactTestDocs)+1 {
		t.Errorf("压实后记录数 = %d，应当只剩约 %d 条（文档数 + 1 条声明）",
			afterRecords, compactTestDocs)
	}
	// 体积要显著变小
	if afterBytes*3 > beforeBytes {
		t.Errorf("压实后日志只缩到 %d/%d，没有明显效果", afterBytes, beforeBytes)
	}

	// 数据必须完好
	if got := countMatches(t, tb, "body:轻量级"); got != compactTestDocs {
		t.Errorf("压实后命中 %d 篇，want %d", got, compactTestDocs)
	}

	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启后数据仍在
	e2 := openTables(t, dir)
	defer e2.Close()

	tb2 := mustDefault(t, e2)
	if got := countMatches(t, tb2, "body:轻量级"); got != compactTestDocs {
		t.Errorf("重启后命中 %d 篇，want %d", got, compactTestDocs)
	}
	if got := tb2.LogBytes(); got != afterBytes {
		t.Errorf("重启后日志大小变了: %d -> %d", afterBytes, got)
	}
}

// 压实必须保留字段类型：数值与时间不能被降级成文本。
func TestCompactionPreservesFieldKinds(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	// 反复更新，逼出远超阈值的日志
	for round := range 8 {
		for i := range 80 {
			if _, err := e.Upsert(Document{
				ID:       fmt.Sprintf("p%d", i),
				Fields:   map[string]string{"name": fmt.Sprintf("商品%d v%d", i, round)},
				Numbers:  map[string]float64{"price": float64(i*100 + round)},
				Keywords: map[string]string{"sku": fmt.Sprintf("SKU-%d", i)},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	tb := mustDefault(t, e)
	if err := tb.Compact(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启后类型必须还在
	e2 := openTables(t, dir)
	defer e2.Close()
	tb2 := mustDefault(t, e2)

	schema := tb2.Schema()
	if schema["price"] != FieldNumber {
		t.Errorf("price 类型 = %v, want number（schema = %v）", schema["price"], schema)
	}
	if schema["sku"] != FieldKeyword {
		t.Errorf("sku 类型 = %v, want keyword", schema["sku"])
	}

	// 而且**行为**上确实还是数值：范围查询可用
	if got := searchAll(t, tb2, "price:[0 TO 150]"); len(got) == 0 {
		t.Error("压实重启后数值范围查询没有结果")
	}
	// 对文本字段做范围查询应当报错，反证类型没被降级成 text
	if _, err := tb2.Search(SearchRequest{Query: "name:[1 TO 2]"}); err == nil {
		t.Error("name 是文本字段，范围查询应当报错")
	}
}

// **空表的 schema 必须活过压实。**
//
// 空表没有文档，schema 只存在于日志的 declare 记录里。
// 压实如果不重写它，重启后字段类型就全丢了。
func TestCompactionPreservesEmptyTableSchema(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)

	empty, err := e.CreateTable("empty", map[string]FieldKind{
		"created": FieldDate,
		"price":   FieldNumber,
		"sku":     FieldKeyword,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 往别的表灌数据把日志撑到阈值以上，再压实**空表**
	// （空表本身没有文档，压实是它唯一的保命手段）
	for i := range 200 {
		if _, err := e.Upsert(Document{
			ID:     fmt.Sprintf("x%d", i),
			Fields: map[string]string{"body": "hello"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := empty.Compact(); err != nil {
		t.Fatal(err)
	}

	// 空表压实后日志里应当仍有一条 declare 记录
	if recs := empty.LogRecords(); recs != 1 {
		t.Errorf("空表压实后应当只有 1 条 declare 记录，实际 %d", recs)
	}

	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := openTables(t, dir)
	defer e2.Close()

	tb, err := e2.Table("empty")
	if err != nil {
		t.Fatalf("空表重启后不见了: %v", err)
	}
	schema := tb.Schema()
	for field, want := range map[string]FieldKind{
		"created": FieldDate,
		"price":   FieldNumber,
		"sku":     FieldKeyword,
	} {
		if got := schema[field]; got != want {
			t.Errorf("空表字段 %q 压缩重启后 = %v, want %v", field, got, want)
		}
	}
}

// 删除的文档不该在压实后复活。
func TestCompactionDropsDeletedDocuments(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	seedUpdates(t, e, 200, 5)

	tb := mustDefault(t, e)
	for i := range 100 {
		if err := tb.Delete(fmt.Sprintf("d%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tb.Compact(); err != nil {
		t.Fatal(err)
	}

	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := openTables(t, dir)
	defer e2.Close()

	tb2 := mustDefault(t, e2)
	got := searchAll(t, tb2, "body:轻量级")
	if len(got) != 100 {
		t.Fatalf("压实重启后命中 %d 篇，want 100（删掉的不该复活）", len(got))
	}
	for _, id := range got {
		var n int
		if _, err := fmt.Sscanf(id, "d%d", &n); err == nil && n < 100 {
			t.Errorf("已删除的 %s 复活了", id)
		}
	}
}

// **压实期间并发写入不能丢。**
//
// 这是整个设计里最容易出错的地方：序列化阶段不持锁，
// 那段时间的写落在旧文件上，必须靠增量接续补回来。
func TestCompactionDoesNotLoseConcurrentWrites(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	tb := mustDefault(t, e)

	// 先垫一批，让日志有东西可压实
	seedUpdates(t, e, 150, 4)

	const writers = 4
	var (
		mu       sync.Mutex
		written  = map[string]bool{}
		wg       sync.WaitGroup
		stop     = make(chan struct{})
		writeErr error
	)

	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}

				id := fmt.Sprintf("w%d-%d", w, i)
				if _, err := tb.Upsert(Document{
					ID:     id,
					Fields: map[string]string{"body": "concurrent write"},
				}); err != nil {
					mu.Lock()
					if writeErr == nil {
						writeErr = err
					}
					mu.Unlock()
					return
				}

				mu.Lock()
				written[id] = true
				mu.Unlock()
			}
		}()
	}

	// 一边写一边压实若干次
	for range 3 {
		if err := tb.Compact(); err != nil {
			t.Errorf("并发写入期间压实失败: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(stop)
	wg.Wait()

	if writeErr != nil {
		t.Fatalf("并发写失败: %v", writeErr)
	}

	mu.Lock()
	want := len(written)
	explicit := make([]string, 0, want)
	for id := range written {
		explicit = append(explicit, id)
	}
	mu.Unlock()

	t.Logf("并发期间共写入 %d 篇不同的文档", want)
	if want == 0 {
		t.Fatal("并发写者一条都没写进去，测试没有意义")
	}

	// 内存里必须全都在
	if st := tb.Stats(); st.Docs < want {
		t.Errorf("内存里只有 %d 篇，至少应当是 %d 篇", st.Docs, want)
	}

	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// **重启后也必须全都在**——这是「没有丢写」的真正检验
	e2 := openTables(t, dir)
	defer e2.Close()

	tb2 := mustDefault(t, e2)
	missing := 0
	for _, id := range explicit {
		if _, ok := tb2.GetDocument(id); !ok {
			missing++
			if missing <= 5 {
				t.Errorf("重启后 %s 不见了（压实期间丢写）", id)
			}
		}
	}
	if missing > 0 {
		t.Fatalf("共丢了 %d 篇文档", missing)
	}
}

// 自动触发：日志超过阈值时不需要手动调用。
//
// 断言的是**稳态**而不是某个精确值：记录数会反复「涨到阈值 → 压回去」，
// 关键是它不会随总写入次数线性增长。
func TestCompactionTriggersAutomatically(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	defer e.Close()

	tb := mustDefault(t, e)

	const docs = 300
	const rounds = 6
	bound := int64(docs*compactRatio + 1) // 阈值 + 一条 declare

	for round := range rounds {
		for i := range docs {
			if _, err := e.Upsert(Document{
				ID:     fmt.Sprintf("d%d", i),
				Fields: map[string]string{"body": fmt.Sprintf("hello round%d", round)},
			}); err != nil {
				t.Fatal(err)
			}
		}

		// 每一轮结束时记录数都不该越界
		if recs := tb.LogRecords(); recs > bound {
			t.Fatalf("第 %d 轮后记录数 = %d 超过上界 %d，自动压实没有生效",
				round, recs, bound)
		}
	}

	total := docs * rounds
	recs := tb.LogRecords()
	t.Logf("共写入 %d 次，日志里只有 %d 条记录（上界 %d）", total, recs, bound)

	// 这才是重点：总写入次数是 1800，记录数却被压在了 900 以下
	if recs >= int64(total) {
		t.Errorf("记录数 %d 与总写入次数 %d 相当，说明日志完全没有被压过", recs, total)
	}

	if got := countMatches(t, tb, "body:hello"); got != docs {
		t.Errorf("命中 %d 篇，want %d", got, docs)
	}
}

// 小索引不做无谓的压实：阈值下限就是为了它。
func TestNoCompactionForTinyIndex(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	defer e.Close()

	tb := mustDefault(t, e)

	// 10 篇文档反复更新 50 次 = 500 次写，低于 compactMinRecords
	// （再加 1 条 body 的 declare 记录 = 501）
	for round := range 50 {
		for i := range 10 {
			if _, err := e.Upsert(Document{
				ID:     fmt.Sprintf("d%d", i),
				Fields: map[string]string{"body": fmt.Sprintf("x%d", round)},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 没被自动压实，记录数应当就是写入次数 + 1 条 declare。
	// 若这里明显小于 500，说明小索引也被反复压实了——那是浪费。
	recs := tb.LogRecords()
	if recs < 490 {
		t.Errorf("小索引被自动压实了（记录数 %d），阈值下限没有生效", recs)
	}
	if recs > 510 {
		t.Errorf("记录数 %d 超出预期（应当是 500 次写 + 1 条 declare）", recs)
	}
}

// 压实期间没有任何文档处于「半写」状态：并发读应当始终看到一致结果。
func TestConcurrentReadsDuringCompaction(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	defer e.Close()

	tb := mustDefault(t, e)
	seedUpdates(t, e, 200, 4)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 读者：始终应当能看到全部 200 篇
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			res, err := tb.Search(SearchRequest{Query: "body:轻量级", Limit: 500})
			if err != nil {
				t.Errorf("并发读失败: %v", err)
				return
			}
			if res.Total != 200 {
				t.Errorf("并发读看到 %d 篇，want 200——压实期间出现了不一致的读", res.Total)
				return
			}
		}
	}()

	for range 3 {
		if err := tb.Compact(); err != nil {
			t.Errorf("压实失败: %v", err)
		}
	}

	close(stop)
	wg.Wait()
}

// 没有启用持久化时，压实相关的调用都应当是安全的空操作。
func TestCompactionWithoutPersistence(t *testing.T) {
	e := New()
	defer e.Close()

	tb := mustDefault(t, e)
	if _, err := tb.Upsert(Document{ID: "d1", Fields: map[string]string{"body": "hello"}}); err != nil {
		t.Fatal(err)
	}

	if err := tb.Compact(); err != nil {
		t.Errorf("纯内存表的 Compact 应当是无害的空操作: %v", err)
	}
	if got := tb.LogBytes(); got != 0 {
		t.Errorf("纯内存表的 LogBytes = %d, want 0", got)
	}
	if got := tb.LogRecords(); got != 0 {
		t.Errorf("纯内存表的 LogRecords = %d, want 0", got)
	}
}

// 压实要留下一条可查的日志，且路径与内容都对得上。
func TestCompactionLogFileIsTheSamePath(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	seedUpdates(t, e, 120, 5)

	tb := mustDefault(t, e)
	if err := tb.Compact(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// 正式路径存在，草稿路径不存在
	official := filepath.Join(dir, tablesSubdir, tablename.Default+logExt)
	if _, err := os.Stat(official); err != nil {
		t.Errorf("正式日志应当存在: %v", err)
	}
	if _, err := os.Stat(official + ".compact"); !os.IsNotExist(err) {
		t.Error("压实完成后不该留下草稿文件")
	}

	// 目录里不该多出别的文件
	entries, err := os.ReadDir(filepath.Join(dir, tablesSubdir))
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if ent.Name() != tablename.Default+logExt {
			t.Errorf("表目录里多出了 %q", ent.Name())
		}
	}
}

// 压实之后仍能正常写入与检索，这是「压实没把日志弄坏」的基本要求。
func TestWritesAfterCompaction(t *testing.T) {
	dir := t.TempDir()

	e := openTables(t, dir)
	tb := mustDefault(t, e)

	seedUpdates(t, e, 150, 4)
	if err := tb.Compact(); err != nil {
		t.Fatal(err)
	}

	// 压实后继续写
	for i := range 30 {
		if _, err := tb.Upsert(Document{
			ID:     fmt.Sprintf("after%d", i),
			Fields: map[string]string{"body": "post compaction write"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tb.Delete("after0"); err != nil {
		t.Fatal(err)
	}

	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := openTables(t, dir)
	defer e2.Close()
	tb2 := mustDefault(t, e2)

	if got := countMatches(t, tb2, "body:post"); got != 29 {
		t.Errorf("压实后的新写命中 %d 篇，want 29", got)
	}
	if got := countMatches(t, tb2, "body:轻量级"); got != 150 {
		t.Errorf("压实前的旧数据命中 %d 篇，want 150", got)
	}
}

// 让 index 包在本文件里被用到（EachDocument 的返回类型）。
var _ = (*index.View)(nil)
var _ = io.Discard
var _ = slog.New
