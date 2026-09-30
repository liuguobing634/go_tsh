package corpus

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles 在临时目录里造一批文件。
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoad(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"alpha.txt":      "alpha content here",
		"beta.md":        "# Beta\n\nbeta content",
		"gamma.markdown": "gamma",
		"notes.log":      "log line",
		"ignored.json":   `{"not": "text"}`,
		"ignored.bin":    "\x00\x01\x02",
		".hidden.txt":    "should be skipped",
		"sub/delta.txt":  "nested content",
	})

	docs, err := Load(dir)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}

	ids := make([]string, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}

	// 结果按 ID 排序，同样目录每次导入顺序一致。
	want := []string{"alpha", "beta", "delta", "gamma", "notes"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("导入的 ID = %v, want %v", ids, want)
	}

	// 字段内容
	for _, d := range docs {
		if d.Fields["title"] != d.ID {
			t.Errorf("%s 的 title = %q, want %q", d.ID, d.Fields["title"], d.ID)
		}
		if d.Fields["body"] == "" {
			t.Errorf("%s 的 body 为空", d.ID)
		}
	}
}

func TestLoadSubdirectory(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"top.txt":        "top level",
		"a/b/nested.txt": "nested",
		"a/c/deep.md":    "deep",
	})

	docs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("导入 %d 篇，want 3", len(docs))
	}
}

func TestLoadErrors(t *testing.T) {
	t.Run("目录不存在", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("期望报错")
		}
	})

	t.Run("不是目录", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{"a.txt": "x"})
		if _, err := Load(filepath.Join(dir, "a.txt")); err == nil {
			t.Fatal("期望报错")
		}
	})

	t.Run("没有可导入的文件", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{"a.json": "{}", "b.bin": "x"})

		_, err := Load(dir)
		if !errors.Is(err, ErrNoDocuments) {
			t.Fatalf("err = %v, want ErrNoDocuments", err)
		}
	})

	t.Run("空目录", func(t *testing.T) {
		if _, err := Load(t.TempDir()); !errors.Is(err, ErrNoDocuments) {
			t.Fatal("空目录应返回 ErrNoDocuments")
		}
	})
}

func TestLoadSkipsOversizedFile(t *testing.T) {
	dir := t.TempDir()

	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", maxImportBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 超大文件应报错，而不是让它去撞索引的 token 上限。
	_, err := Load(dir)
	if err == nil {
		t.Fatal("期望因超大文件报错")
	}
	if !strings.Contains(err.Error(), "big.txt") {
		t.Errorf("错误信息应指出是哪个文件: %v", err)
	}
}

// ---------------------------------------------------------------- 合成语料

func TestGenerate(t *testing.T) {
	t.Run("数量与字段", func(t *testing.T) {
		docs := Generate(50)
		if len(docs) != 50 {
			t.Fatalf("生成 %d 篇，want 50", len(docs))
		}
		for i, d := range docs {
			if !strings.HasPrefix(d.ID, "gen-") {
				t.Errorf("docs[%d].ID = %q", i, d.ID)
			}
			if d.Fields["title"] == "" || d.Fields["body"] == "" {
				t.Errorf("docs[%d] 字段不完整: %+v", i, d.Fields)
			}
		}
	})

	t.Run("确定性", func(t *testing.T) {
		a := Generate(30)
		b := Generate(30)

		for i := range a {
			if a[i].ID != b[i].ID || a[i].Fields["body"] != b[i].Fields["body"] {
				t.Fatalf("同样输入应产生同样语料，docs[%d] 不一致", i)
			}
		}
	})

	t.Run("边界", func(t *testing.T) {
		if got := Generate(0); got != nil {
			t.Errorf("Generate(0) = %v, want nil", got)
		}
		if got := Generate(-5); got != nil {
			t.Errorf("Generate(-5) = %v, want nil", got)
		}
	})

	// 词频必须是不均匀的：生僻词只出现在少数文档里。
	// 若所有文档一模一样，这个断言会失败。
	t.Run("词频不均匀", func(t *testing.T) {
		const n = 500
		docs := Generate(n)

		counts := make(map[string]int, n)
		for _, d := range docs {
			seen := make(map[string]struct{})
			for _, w := range strings.Fields(d.Fields["body"]) {
				if strings.HasPrefix(w, "rare") {
					seen[w] = struct{}{}
				}
			}
			for w := range seen {
				counts[w]++
			}
		}

		if len(counts) < n/4 {
			t.Fatalf("生僻词种类只有 %d 个，分布过于集中", len(counts))
		}

		maxCount := 0
		for _, c := range counts {
			if c > maxCount {
				maxCount = c
			}
		}
		if maxCount >= n/10 {
			t.Errorf("最高频的生僻词出现在 %d/%d 篇文档里，分布不够长尾", maxCount, n)
		}
	})
}

func BenchmarkGenerate(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Generate(1000)
	}
}
