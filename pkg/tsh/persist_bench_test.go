package tsh

import (
	"fmt"
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
