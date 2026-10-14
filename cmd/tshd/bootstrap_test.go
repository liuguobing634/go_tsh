package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/internal/tablename"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedCorpusDir 造一个小语料目录。
func seedCorpusDir(t *testing.T, files int) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "corpus")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range files {
		body := strings.Repeat("这是用来测试导入标记的文档内容 ", 3)
		path := filepath.Join(dir, "doc"+itoa(i)+".txt")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

// openWithBootstrap 按给定配置起一个引擎并跑一次 bootstrap，返回日志字节数。
func openWithBootstrap(t *testing.T, cfg config.Config) int64 {
	t.Helper()

	engine, err := newEngine(cfg)
	if err != nil {
		t.Fatalf("构造引擎失败: %v", err)
	}
	if err := bootstrap(engine, cfg, quiet()); err != nil {
		t.Fatalf("bootstrap 失败: %v", err)
	}

	tb, err := engine.Table(tablename.Default)
	if err != nil {
		t.Fatal(err)
	}
	size := tb.LogBytes()

	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	return size
}

// **核心验收：第二次启动不能再导一遍。**
//
// 这正是引入标记要解决的问题：`-import` 原本无条件执行，
// 于是带持久化的部署光是重启就让日志膨胀。
func TestBootstrapImportsOnlyOnce(t *testing.T) {
	dataDir := t.TempDir()
	corpusDir := seedCorpusDir(t, 5)

	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.ImportDir = corpusDir

	first := openWithBootstrap(t, cfg)
	if first == 0 {
		t.Fatal("第一次导入后日志不该是空的")
	}

	// 再启动几次，日志都不该再长
	for i := range 3 {
		again := openWithBootstrap(t, cfg)
		if again != first {
			t.Fatalf("第 %d 次重启后日志从 %d 变成 %d——又导入了一遍",
				i+2, first, again)
		}
	}

	t.Logf("4 次启动后日志稳定在 %d 字节", first)
}

// -reimport 必须能强制重导。
func TestReimportForcesAnotherImport(t *testing.T) {
	dataDir := t.TempDir()
	corpusDir := seedCorpusDir(t, 5)

	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.ImportDir = corpusDir

	first := openWithBootstrap(t, cfg)

	cfg.Reimport = true
	again := openWithBootstrap(t, cfg)

	if again <= first {
		t.Errorf("-reimport 之后日志应当变大：%d -> %d", first, again)
	}
	t.Logf("-reimport: %d -> %d 字节", first, again)

	// 重导之后再启动（且不再带 -reimport）应当又稳定下来
	cfg.Reimport = false
	stable := openWithBootstrap(t, cfg)
	if stable != again {
		t.Errorf("重导后又不稳定了：%d -> %d", again, stable)
	}
}

// 换了语料目录时跳过导入，但**必须留下痕迹**——
// 静默什么都不导、而用户以为导了，是最难自己发现的一类问题。
func TestChangedImportParamsAreReported(t *testing.T) {
	dataDir := t.TempDir()
	dirA := seedCorpusDir(t, 3)
	dirB := seedCorpusDir(t, 7)

	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.ImportDir = dirA

	openWithBootstrap(t, cfg)

	// 换成另一个目录，不该重导
	cfg.ImportDir = dirB
	sizeB := openWithBootstrap(t, cfg)

	// 但 shouldSkipImport 要把「参数变了」讲清楚
	engine, err := newEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	tb, err := engine.Table(tablename.Default)
	if err != nil {
		t.Fatal(err)
	}

	skip, changed, reason := shouldSkipImport(tb, cfg)
	if !skip {
		t.Fatal("换目录后仍然默认跳过")
	}
	if !changed {
		t.Error("换目录后应当报告「参数变了」")
	}

	// ⚠️ 不要拿原始路径去 Contains：原因里是 %q 格式化的，
	// Windows 的反斜杠会变成 \\，直接比对必然匹配不上。
	// 改用不含分隔符的父目录名，两边都稳定。
	parentA := filepath.Base(filepath.Dir(dirA))
	parentB := filepath.Base(filepath.Dir(dirB))

	for _, want := range []string{parentA, parentB, "-reimport"} {
		if !strings.Contains(reason, want) {
			t.Errorf("跳过原因里应当提到 %q：%s", want, reason)
		}
	}
	t.Logf("跳过原因: %s", reason)

	// 而且数据确实还是 A 的（没有偷偷换掉）
	if got := countDefault(t, engine); got != 3 {
		t.Errorf("文档数 = %d，want 3（应当保持 A 的内容）", got)
	}
	_ = sizeB
}

// -reimport 加上新目录时，新目录的内容要真的导进去。
func TestReimportWithNewDirLoadsNewContent(t *testing.T) {
	dataDir := t.TempDir()
	dirA := seedCorpusDir(t, 3)
	dirB := seedCorpusDir(t, 7)

	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.ImportDir = dirA
	openWithBootstrap(t, cfg)

	cfg.ImportDir = dirB
	cfg.Reimport = true
	openWithBootstrap(t, cfg)

	// 重新打开确认
	engine, err := newEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	// 两个目录的文件名一样（doc0..docN），所以 B 覆盖了同名的，
	// 最终应当是 max(3,7) = 7 篇
	if got := countDefault(t, engine); got != 7 {
		t.Errorf("文档数 = %d, want 7", got)
	}
}

// 只给 -generate 不给 -import 时，标记同样要生效。
func TestBootstrapMarkersCoverGenerateOnly(t *testing.T) {
	dataDir := t.TempDir()

	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.GenerateDocs = 20

	first := openWithBootstrap(t, cfg)
	again := openWithBootstrap(t, cfg)

	if again != first {
		t.Errorf("只用 -generate 时也应当只导一次：%d -> %d", first, again)
	}

	// 改了篇数 → 跳过，但要能看出来参数变了
	cfg.GenerateDocs = 50
	engine, err := newEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	tb, _ := engine.Table(tablename.Default)
	_, changed, reason := shouldSkipImport(tb, cfg)
	if !changed {
		t.Error("改了 generate 篇数后应当报告「参数变了」")
	}
	if !strings.Contains(reason, "不同") {
		t.Errorf("改了 generate 篇数后应当提示参数不同：%s", reason)
	}
}

// 没有 -import / -generate 时 bootstrap 什么都不做，也不该写标记。
func TestBootstrapNoopWithoutImportFlags(t *testing.T) {
	dataDir := t.TempDir()

	cfg := config.Default()
	cfg.DataDir = dataDir

	if err := func() error {
		engine, err := newEngine(cfg)
		if err != nil {
			return err
		}
		defer engine.Close()
		return bootstrap(engine, cfg, quiet())
	}(); err != nil {
		t.Fatal(err)
	}

	engine, err := newEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	tb, _ := engine.Table(tablename.Default)
	if _, ok := tb.Imported(); ok {
		t.Error("什么都没导入时不该有标记")
	}
	if tb.LogBytes() != 0 && tb.LogRecords() != 0 {
		t.Errorf("什么都没做，日志不该有记录：%d 条", tb.LogRecords())
	}
}

// 纯内存模式下，标记是无害的空操作（反正重启什么都不剩）。
func TestImportMarkerWithoutPersistence(t *testing.T) {
	engine, err := newEngine(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	tb, _ := engine.Table(tablename.Default)
	if err := tb.MarkImported(tsh.ImportMarker{Dir: "x", Files: 1}); err != nil {
		t.Errorf("纯内存模式下不该报错: %v", err)
	}
	if _, ok := tb.Imported(); ok {
		t.Error("纯内存模式没有持久化，不该读回标记")
	}
}

// countDefault 数默认表里的文档数。
func countDefault(t *testing.T, engine *tsh.Engine) int {
	t.Helper()
	tb, err := engine.Table(tablename.Default)
	if err != nil {
		t.Fatal(err)
	}
	return tb.Stats().Docs
}
