package wal

import (
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// quietLogger 避免测试输出里混进恢复过程的告警。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testOptions() Options {
	return Options{Logger: quietLogger(), SyncInterval: 10 * 1000 * 1000 * 1000}
}

// appendRaw 直接往文件末尾塞字节，用来模拟崩溃留下的半条记录。
func appendRaw(t *testing.T, path string, b []byte) {
	t.Helper()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCreatesHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	l, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if got := l.Size(); got != headerSize {
		t.Errorf("新日志大小 = %d, want %d", got, headerSize)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:4]) != Magic {
		t.Errorf("魔数 = %q, want %q", raw[:4], Magic)
	}
	if raw[4] != FormatVersion {
		t.Errorf("版本 = %d, want %d", raw[4], FormatVersion)
	}
}

func TestAppendAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	want := []struct {
		kind    Kind
		payload string
	}{
		{KindUpsert, "doc-1"},
		{KindUpsert, "doc-2"},
		{KindDelete, "doc-1"},
		{KindUpsert, "doc-3"},
	}

	l, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if err := l.Append(w.kind, []byte(w.payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// 重新打开：内容必须逐条一致、顺序一致。
	l2, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()

	var got []string
	err = l2.Replay(func(k Kind, payload []byte) error {
		got = append(got, k.String()+":"+string(payload))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != len(want) {
		t.Fatalf("重放得到 %d 条，want %d（%v）", len(got), len(want), got)
	}
	for i, w := range want {
		expect := w.kind.String() + ":" + w.payload
		if got[i] != expect {
			t.Errorf("第 %d 条 = %q, want %q", i, got[i], expect)
		}
	}
}

// 崩溃时最后一条可能只写了一半。它必须被识别并截断，
// 而**前面已经落盘的记录一条都不能少**。
func TestTornTailIsTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	l, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}

	records := []string{"first", "second", "third"}
	for _, p := range records {
		if err := l.Append(KindUpsert, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// 追加半条记录：类型 + 长度 100，但只跟 10 个字节。
	partial := make([]byte, 0, 15)
	partial = append(partial, byte(KindUpsert))
	partial = binary.LittleEndian.AppendUint32(partial, 100)
	partial = append(partial, []byte("0123456789")...)
	appendRaw(t, path, partial)

	// 三条记录的长度并不相同，按实际长度算，别想当然。
	sizeBefore := int64(headerSize)
	for _, p := range records {
		sizeBefore += 5 + int64(len(p)) + 4
	}

	l2, err := Open(path, testOptions())
	if err != nil {
		t.Fatalf("半条尾部应当被自动截断，却报错: %v", err)
	}
	defer l2.Close()

	if got := l2.Size(); got != sizeBefore {
		t.Errorf("截断后大小 = %d, want %d", got, sizeBefore)
	}

	var got []string
	if err := l2.Replay(func(_ Kind, p []byte) error {
		got = append(got, string(p))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("重放得到 %v，三条完整记录应当全部保留", got)
	}
}

// 尾部记录长度字段被写坏成一个极大值：不能去分配几个 GB，
// 而要当成不完整记录截断掉。
func TestAbsurdLengthInTailIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	l, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(KindUpsert, []byte("keep-me")); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	bogus := []byte{byte(KindUpsert), 0xff, 0xff, 0xff, 0x7f}
	appendRaw(t, path, bogus)

	l2, err := Open(path, testOptions())
	if err != nil {
		t.Fatalf("超长长度字段应当被当成尾部不完整，却报错: %v", err)
	}
	defer l2.Close()

	var got []string
	if err := l2.Replay(func(_ Kind, p []byte) error {
		got = append(got, string(p))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "keep-me" {
		t.Errorf("重放得到 %v，应当只剩 keep-me", got)
	}
}

// 损坏发生在**中间**（后面还有大量数据）时，截断等于把可能完好的记录
// 一起丢掉。这时必须拒绝启动，交给人工处理，而不是静默丢数据。
func TestCorruptionInMiddleRefusesToStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	opts := testOptions()
	opts.MaxRecordBytes = 256

	l, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("x", 100)
	for range 3 {
		if err := l.Append(KindUpsert, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// 翻转第一条记录 payload 里的一个字节（记录从 headerSize 开始，
	// 前面是 1 字节 kind + 4 字节长度）。
	//
	// 必须以 O_RDWR 打开：Windows 上对 O_WRONLY 的句柄调 ReadAt 会
	// 直接报 "Access is denied"。
	target := int64(headerSize + 5 + 10)
	f, err := os.OpenFile(path, os.O_RDWR, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	one := make([]byte, 1)
	if _, err := f.ReadAt(one, target); err != nil {
		t.Fatal(err)
	}
	one[0] ^= 0xff
	if _, err := f.WriteAt(one, target); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path, opts)
	if err == nil {
		t.Fatal("中间损坏必须拒绝启动，而不是截断丢掉后面的记录")
	}
	if !errors.Is(err, ErrCorrupted) {
		t.Errorf("错误应当是 ErrCorrupted，实际: %v", err)
	}
	t.Logf("拒绝启动的理由: %v", err)
}

// 尾部记录的校验和对不上，且在安全阈值内 → 截断并告警。
func TestChecksumMismatchInTailIsTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	opts := testOptions()
	opts.MaxRecordBytes = 4096

	l, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(KindUpsert, []byte("good")); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(KindUpsert, []byte("bad-payload")); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// 只破坏最后一条的 payload
	target := int64(headerSize + (5 + len("good") + 4) + 5)
	f, err := os.OpenFile(path, os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	one := []byte{'X'}
	if _, err := f.WriteAt(one, target); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	l2, err := Open(path, opts)
	if err != nil {
		t.Fatalf("尾部校验失败应当被截断，却报错: %v", err)
	}
	defer l2.Close()

	var got []string
	if err := l2.Replay(func(_ Kind, p []byte) error {
		got = append(got, string(p))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "good" {
		t.Errorf("重放得到 %v，应当只剩 good", got)
	}
}

func TestFormatMismatch(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"魔数不对", []byte("XXXX\x01\x00\x00\x00")},
		{"版本不对", []byte(Magic + "\x63\x00\x00\x00")},
		{"文件太短", []byte("TSH")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wal.log")
			if err := os.WriteFile(path, tc.raw, 0o640); err != nil {
				t.Fatal(err)
			}

			_, err := Open(path, testOptions())
			if err == nil {
				t.Fatal("格式不匹配必须报错，绝不能覆盖已有文件")
			}
			if !errors.Is(err, ErrFormatMismatch) {
				t.Errorf("错误应当是 ErrFormatMismatch，实际: %v", err)
			}

			// 关键：原文件必须**原封不动**，不能被"修复"掉。
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(tc.raw) {
				t.Errorf("文件被改动了：%q -> %q", tc.raw, after)
			}
		})
	}
}

func TestAppendRejectsOversizedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	opts := testOptions()
	opts.MaxRecordBytes = 32

	l, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := l.Append(KindUpsert, []byte(strings.Repeat("y", 33))); err == nil {
		t.Fatal("超过上限的记录必须被拒绝——否则写进去的下次打开会当成损坏")
	}

	// 边界值应当通过
	if err := l.Append(KindUpsert, []byte(strings.Repeat("y", 32))); err != nil {
		t.Errorf("恰好等于上限的记录应当被接受: %v", err)
	}
}

func TestAppendAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	l, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	if err := l.Append(KindUpsert, []byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("关闭后追加应当返回 ErrClosed，实际: %v", err)
	}

	// 重复关闭必须安全
	if err := l.Close(); err != nil {
		t.Errorf("重复关闭应当无副作用，实际: %v", err)
	}
}

func TestReplayPropagatesError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	l, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a", "b", "c"} {
		if err := l.Append(KindUpsert, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	defer l.Close()

	sentinel := errors.New("回调故意失败")
	seen := 0
	err = l.Replay(func(_ Kind, _ []byte) error {
		seen++
		if seen == 2 {
			return sentinel
		}
		return nil
	})

	if !errors.Is(err, sentinel) {
		t.Errorf("回调错误应当原样返回，实际: %v", err)
	}
	if seen != 2 {
		t.Errorf("应当在第二条就停下，实际处理了 %d 条", seen)
	}
}

// 重新打开后继续追加，新记录要接在旧记录后面，不能覆盖。
func TestAppendAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	l, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(KindUpsert, []byte("gen-1")); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	l2, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.Append(KindDelete, []byte("gen-2")); err != nil {
		t.Fatal(err)
	}
	if err := l2.Close(); err != nil {
		t.Fatal(err)
	}

	l3, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer l3.Close()

	var got []string
	if err := l3.Replay(func(k Kind, p []byte) error {
		got = append(got, k.String()+":"+string(p))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := []string{"upsert:gen-1", "delete:gen-2"}
	if len(got) != len(want) {
		t.Fatalf("重放得到 %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条 = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConcurrentAppendAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	l, err := Open(path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	const writers = 8
	const perWriter = 50

	done := make(chan error, writers)
	for w := range writers {
		go func() {
			for i := range perWriter {
				payload := []byte{byte(w), byte(i)}
				if err := l.Append(KindUpsert, payload); err != nil {
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

	// 并发追加的**每条记录**都必须完整可读（总条数与内容按 writer 分组校验）。
	counts := map[byte]int{}
	err = l.Replay(func(k Kind, p []byte) error {
		if k != KindUpsert || len(p) != 2 {
			return errors.New("记录被并发写坏了")
		}
		counts[p[0]]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for w := range writers {
		if counts[byte(w)] != perWriter {
			t.Errorf("writer %d 的记录数 = %d, want %d", w, counts[byte(w)], perWriter)
		}
	}
}
