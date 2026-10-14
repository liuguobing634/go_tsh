package wal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// replayAll 打开日志并把全部记录读出来。
func replayAll(t *testing.T, path string) []string {
	t.Helper()

	log, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer log.Close()

	var out []string
	if err := log.Replay(func(k Kind, payload []byte) error {
		out = append(out, fmt.Sprintf("%s:%s", k, payload))
		return nil
	}); err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	return out
}

// appendN 写 n 条可区分的记录。
func appendN(t *testing.T, log *Log, prefix string, n int) {
	t.Helper()
	for i := range n {
		payload := EncodeDelete(fmt.Sprintf("%s-%d", prefix, i), nil)
		if err := log.Append(KindDelete, payload); err != nil {
			t.Fatal(err)
		}
	}
}

// 压实会把日志重写成 fn 产出的记录。
func TestCompactRewritesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.wal")

	log, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	appendN(t, log, "old", 50)
	sizeBefore := log.Size()

	mark := log.Size()
	if mark != sizeBefore {
		t.Fatalf("mark = %d, want %d", mark, sizeBefore)
	}

	err = log.Compact(mark, func(emit func(Kind, []byte) error) error {
		for i := range 3 {
			if err := emit(KindUpsert, EncodeDelete(fmt.Sprintf("new-%d", i), nil)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("压实失败: %v", err)
	}

	got := replayAll(t, path)
	if len(got) != 3 {
		t.Fatalf("压实后应当只剩 3 条，实际 %d 条: %v", len(got), got)
	}
	if log.Size() >= sizeBefore {
		t.Errorf("压实后日志没变小: %d -> %d", sizeBefore, log.Size())
	}

	// 压实之后还能继续写
	appendN(t, log, "after", 2)
	if got := replayAll(t, path); len(got) != 5 {
		t.Errorf("压实后继续写应当有 5 条，实际 %d", len(got))
	}
}

// **压实期间新产生的写必须被接上**，否则就是丢数据。
//
// 这正是 mark 参数的用途：fn 执行时新追加的记录落在旧文件上，
// Compact 要把 [mark, 当前长度) 原样搬到新文件末尾。
func TestCompactCarriesDeltaWrittenDuringSerialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.wal")

	log, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	appendN(t, log, "old", 20)

	// 记住基线偏移——等价于调用方「在锁内取证」的那一刻。
	mark := log.Size()

	// fn 执行期间又来了 7 条写。
	err = log.Compact(mark, func(emit func(Kind, []byte) error) error {
		appendN(t, log, "during", 7) // 模拟并发写入

		if err := emit(KindDelete, EncodeDelete("snapshot", nil)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("压实失败: %v", err)
	}

	got := replayAll(t, path)
	// 1 条快照 + 7 条增量
	if len(got) != 8 {
		t.Fatalf("应当有 8 条（1 快照 + 7 增量），实际 %d 条: %v", len(got), got)
	}
	if got[0] != fmt.Sprintf("delete:%s", EncodeDelete("snapshot", nil)) {
		t.Errorf("第一条应当是快照内容，实际 %q", got[0])
	}
	for i := range 7 {
		want := fmt.Sprintf("delete:%s", EncodeDelete(fmt.Sprintf("during-%d", i), nil))
		if got[i+1] != want {
			t.Errorf("第 %d 条增量不对:\n  got  %q\n  want %q", i, got[i+1], want)
		}
	}
}

// mark 大于当前长度是调用方用错了，必须报错而不是悄悄丢数据。
func TestCompactRejectsMarkBeyondSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.wal")

	log, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	appendN(t, log, "x", 5)

	err = log.Compact(log.Size()+1000, func(func(Kind, []byte) error) error { return nil })
	if err == nil {
		t.Fatal("mark 超过日志长度应当报错")
	}
	t.Logf("%v", err)
}

func TestCompactRejectsBadMark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.wal")

	log, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	for _, mark := range []int64{-1, 0, 1, headerSize - 1} {
		if err := log.Compact(mark, func(func(Kind, []byte) error) error { return nil }); err == nil {
			t.Errorf("mark = %d 应当被拒绝（至少要越过文件头）", mark)
		}
	}
}

// fn 报错时不能动到正式文件。
func TestCompactAbortsCleanlyOnEmitError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.wal")

	log, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	appendN(t, log, "keep", 10)
	before := replayAll(t, path)
	sizeBefore := log.Size()

	boom := errors.New("模拟序列化中途失败")
	err = log.Compact(log.Size(), func(emit func(Kind, []byte) error) error {
		_ = emit(KindUpsert, EncodeDelete("partial", nil))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("应当把 fn 的错误透出来，实际: %v", err)
	}

	// 正式文件必须一字未动
	if got := replayAll(t, path); len(got) != len(before) {
		t.Errorf("失败后旧日志被改动了: %d 条 -> %d 条", len(before), len(got))
	}
	if log.Size() != sizeBefore {
		t.Errorf("失败后日志长度变了: %d -> %d", sizeBefore, log.Size())
	}

	// 临时文件要被清掉
	if _, err := os.Stat(path + compactSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("失败后不该留下临时文件: %v", err)
	}

	// 而且还能继续正常写
	appendN(t, log, "after", 3)
	if got := replayAll(t, path); len(got) != 13 {
		t.Errorf("失败后应当还能继续写，实际 %d 条", len(got))
	}
}

// **崩溃安全**：上次压实写到一半留下的 .compact 是草稿，
// 正式日志仍然完整，启动时必须照常用正式日志。
func TestStaleCompactFileIsHarmless(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.wal")

	log, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, log, "real", 12)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	want := replayAll(t, path)

	// 伪造一份没写完的压实草稿
	stale := path + compactSuffix
	part := headerBytes()
	part = append(part, EncodeRecord(KindUpsert, []byte("garbage"), nil)...)
	if err := os.WriteFile(stale, part, 0o640); err != nil {
		t.Fatal(err)
	}

	// 正式日志必须不受影响
	got := replayAll(t, path)
	if len(got) != len(want) {
		t.Fatalf("残留草稿影响了正式日志: %d 条 -> %d 条", len(want), len(got))
	}

	// 下一次压实会直接覆盖它
	log2, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}
	defer log2.Close()

	err = log2.Compact(log2.Size(), func(emit func(Kind, []byte) error) error {
		return emit(KindDelete, EncodeDelete("fresh", nil))
	})
	if err != nil {
		t.Fatalf("压实应当能覆盖残留草稿: %v", err)
	}
	// mark 是当前长度，序列化期间没有新写，所以增量是 0 条——只剩那 1 条。
	if got := replayAll(t, path); len(got) != 1 {
		t.Errorf("压实后应当只剩 1 条，实际 %d 条: %v", len(got), got)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Error("压实完成后不该留下草稿文件")
	}
}

// 空日志也能压实（写出一条记录或什么都不写）。
func TestCompactEmptyLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.wal")

	log, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	if err := log.Compact(log.Size(), func(func(Kind, []byte) error) error { return nil }); err != nil {
		t.Fatalf("空日志压实失败: %v", err)
	}
	if got := replayAll(t, path); len(got) != 0 {
		t.Errorf("应当没有记录，实际 %d 条", len(got))
	}
	// 文件头还在
	if log.Size() != headerSize {
		t.Errorf("空日志压实后大小 = %d, want %d", log.Size(), headerSize)
	}
}

// 压实后写进去的记录，重新打开仍然读得回来
// ——这是「写出来的确实是一份合法日志」的直接验证。
func TestCompactResultIsAValidLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.wal")

	log, err := Open(path, Options{SyncInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}

	appendN(t, log, "junk", 30)

	err = log.Compact(log.Size(), func(emit func(Kind, []byte) error) error {
		// 各种记录类型都写一遍
		if err := emit(KindDeclare, EncodeDeclare(map[string]uint8{"price": KindFieldNumber}, nil)); err != nil {
			return err
		}
		if err := emit(KindUpsert, EncodeUpsert("d1", map[string]string{"body": "hello"}, nil, nil)); err != nil {
			return err
		}
		return emit(KindDelete, EncodeDelete("d2", nil))
	})
	if err != nil {
		t.Fatal(err)
	}

	// 继续写一条，然后关掉重新打开
	if err := log.Append(KindDelete, EncodeDelete("d3", nil)); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	got := replayAll(t, path)
	if len(got) != 4 {
		t.Fatalf("重新打开后应当有 4 条，实际 %d: %v", len(got), got)
	}
	if got[0][:7] != "declare" {
		t.Errorf("第一条应当是 declare，实际 %q", got[0])
	}
}
