package tsh

import (
	"fmt"
	"sync/atomic"
	"testing"
)

// 持久化的核心代价问题：开了日志之后写入慢多少。
//
// 三个基准必须**在同一次运行里**对比——涉及磁盘与内存压力的基准
// 跨运行比较会得出错误结论（这个坑在中文分析器那一轮已经踩过一次）。
func benchDoc(i int) Document {
	return Document{
		ID: fmt.Sprintf("doc-%d", i),
		Fields: map[string]string{
			"title": fmt.Sprintf("document %d about search", i),
			"body": "an inverted index maps terms to the documents that contain them, " +
				"and bm25 ranks those documents by relevance",
		},
	}
}

func benchmarkUpsert(b *testing.B, opts Options) {
	b.Helper()

	if opts.DataDir != "" {
		opts.DataDir = b.TempDir()
	}
	opts.Logger = quiet()

	e, err := NewWith(opts)
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := e.Upsert(benchDoc(i)); err != nil {
			b.Fatal(err)
		}
	}
}

// 基线：纯内存，不落盘。
func BenchmarkUpsertInMemory(b *testing.B) {
	benchmarkUpsert(b, Options{})
}

// 默认批量策略（100ms fsync 一次）。
func BenchmarkUpsertPersistentBatch(b *testing.B) {
	benchmarkUpsert(b, Options{DataDir: "unused"})
}

// 每次写都等在 fsync 后面——只用来量化 fsync 的绝对成本，
// 不是默认配置。
func BenchmarkUpsertPersistentSyncEach(b *testing.B) {
	dir := b.TempDir()

	e, err := NewWith(Options{DataDir: dir, Logger: quiet()})
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := e.Upsert(benchDoc(i)); err != nil {
			b.Fatal(err)
		}
		if err := e.persist.log.Sync(); err != nil {
			b.Fatal(err)
		}
	}
}

// 并发写入：验证「钩子放在索引写锁内」**没有**把分析阶段也串行化。
//
// 这是设计上的一个明确主张，必须实测而不是靠推理：
// prepare（分析）刻意跑在写锁之外，钩子只在写锁内做一次追加。
// 如果实现退化成「引擎级大锁包住整个 Add」，这里的吞吐会明显塌下来。
func benchmarkParallelUpsert(b *testing.B, opts Options) {
	b.Helper()

	if opts.DataDir != "" {
		opts.DataDir = b.TempDir()
	}
	opts.Logger = quiet()

	e, err := NewWith(opts)
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()

	var seq atomic.Int64

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// 每个 writer 用不重复的 ID，避免相互覆盖影响可比性
			i := int(seq.Add(1))
			if _, err := e.Upsert(benchDoc(i)); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkParallelUpsertInMemory(b *testing.B) {
	benchmarkParallelUpsert(b, Options{})
}

func BenchmarkParallelUpsertPersistent(b *testing.B) {
	benchmarkParallelUpsert(b, Options{DataDir: "unused"})
}

// 重放开销：它决定重启要多久，是持久化最容易被忽略的成本。
//
// 计时区间里包含「打开日志 + 扫描校验 + 重放 + 重建索引」全过程，
// 也就是一次真实的启动。
//
// 注意不要用 -benchtime 2s 跑这个：单次迭代就几十毫秒，框架会算出
// 一个很大的 b.N。用固定次数，例如 -benchtime 10x。
func BenchmarkReplay(b *testing.B) {
	const docs = 2000

	dir := b.TempDir()

	// 先在计时之外把日志准备好
	e, err := NewWith(Options{DataDir: dir, Logger: quiet()})
	if err != nil {
		b.Fatal(err)
	}
	for i := range docs {
		if _, err := e.Upsert(benchDoc(i)); err != nil {
			b.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		b.Fatal(err)
	}

	b.ReportMetric(docs, "docs/op")
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		eng, err := NewWith(Options{DataDir: dir, Logger: quiet()})
		if err != nil {
			b.Fatal(err)
		}
		if got := eng.Stats().Docs; got != docs {
			b.Fatalf("重放后文档数 = %d, want %d", got, docs)
		}
		if err := eng.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
