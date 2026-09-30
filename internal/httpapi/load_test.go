package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuguobing/go_tsh/internal/config"
	"github.com/liuguobing/go_tsh/internal/corpus"
	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// loadDocs 是压测语料的规模，与 PLAN 的验收规模对齐。
const loadDocs = 100_000

var (
	loadOnce sync.Once
	loadSrv  *httptest.Server
)

// loadServer 起一个装了 10 万篇语料的真实 HTTP 服务。
//
// 用 httptest.NewServer 而不是直接调 handler：这样测到的是
// **真实 HTTP 栈**——TCP、请求解析、路由、中间件、JSON 序列化全都在内。
// 进程内直接调 Engine 会把这些全部漏掉，测出来的数字偏乐观。
func loadServer(b *testing.B) *httptest.Server {
	b.Helper()

	loadOnce.Do(func() {
		engine := tsh.New()

		start := time.Now()
		for _, d := range corpus.Generate(loadDocs) {
			if _, err := engine.Upsert(tsh.Document{ID: d.ID, Fields: d.Fields}); err != nil {
				panic(err)
			}
		}

		st := engine.Stats()
		b.Logf("语料构建完成：%d 篇 / %d 个词条 / 索引约 %.1f MB / 耗时 %s",
			st.Docs, st.Terms, float64(st.IndexBytes)/(1<<20), time.Since(start).Round(time.Millisecond))

		srv := New(config.Default(), engine, slog.New(slog.NewTextHandler(io.Discard, nil)))
		// Server 内嵌的是 *http.Server，它本身没有 ServeHTTP；
		// 路由与中间件都挂在 Handler 字段上。
		loadSrv = httptest.NewServer(srv.Handler)
	})

	return loadSrv
}

// benchmarkHTTP 对给定 URL 打满 b.N 次请求，并汇报尾延迟分位数。
func benchmarkHTTP(b *testing.B, target string) {
	b.Helper()

	srv := loadServer(b)
	client := srv.Client()

	latencies := make([]time.Duration, 0, b.N)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		start := time.Now()

		resp, err := client.Get(srv.URL + target)
		if err != nil {
			b.Fatalf("请求失败: %v", err)
		}
		// 必须读完并关闭响应体，否则连接无法复用，
		// 测出来的会是「每次新建连接」的吞吐，严重偏低。
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			b.Fatalf("状态码 = %d", resp.StatusCode)
		}

		latencies = append(latencies, time.Since(start))
	}

	b.StopTimer()
	reportLatency(b, latencies)
}

// reportLatency 汇报 P50 / P95 / P99 与最大值，单位毫秒。
func reportLatency(b *testing.B, latencies []time.Duration) {
	if len(latencies) == 0 {
		return
	}
	slices.Sort(latencies)

	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

	quantile := func(p float64) float64 {
		i := int(float64(len(latencies)) * p)
		if i >= len(latencies) {
			i = len(latencies) - 1
		}
		return ms(latencies[i])
	}

	// 原始分位数一并打进日志：自定义指标的显示精度有限，
	// 出现「p50 显示为 0」这种读数时要能立刻对照原始值判断真假。
	b.Logf("延迟分布 n=%d min=%v p50=%v p90=%v p95=%v p99=%v max=%v",
		len(latencies),
		latencies[0],
		latencies[len(latencies)/2],
		latencies[len(latencies)*90/100],
		latencies[len(latencies)*95/100],
		latencies[len(latencies)*99/100],
		latencies[len(latencies)-1],
	)

	b.ReportMetric(ms(latencies[0]), "min_ms")
	b.ReportMetric(quantile(0.50), "p50_ms")
	b.ReportMetric(quantile(0.95), "p95_ms")
	b.ReportMetric(quantile(0.99), "p99_ms")
	b.ReportMetric(ms(latencies[len(latencies)-1]), "max_ms")
	b.ReportMetric(float64(len(latencies)), "samples")
}

// 最常见的路径：单词检索。
func BenchmarkHTTPGetTerm(b *testing.B) {
	benchmarkHTTP(b, "/api/v1/search?q=inverted")
}

// 短语检索：要碰位置信息，是检索里最贵的一种。
func BenchmarkHTTPGetPhrase(b *testing.B) {
	benchmarkHTTP(b, `/api/v1/search?q=%22inverted%20index%22`)
}

// 布尔组合：走归并求交。
func BenchmarkHTTPGetAnd(b *testing.B) {
	benchmarkHTTP(b, "/api/v1/search?q=inverted%20AND%20index")
}

// 带高亮：额外要在原文上做一遍标注。
func BenchmarkHTTPGetHighlight(b *testing.B) {
	benchmarkHTTP(b, "/api/v1/search?q=inverted&highlight=true")
}

// 低命中：生僻词只出现在极少数文档里，用来对比命中集大小的影响。
func BenchmarkHTTPGetRare(b *testing.B) {
	benchmarkHTTP(b, "/api/v1/search?q=rare00042")
}

// 只读的元信息接口，作为 HTTP 栈本身开销的基线。
func BenchmarkHTTPGetStats(b *testing.B) {
	benchmarkHTTP(b, "/api/v1/stats")
}

// benchmarkHTTPParallel 用 GOMAXPROCS 个并发客户端打满服务。
//
// 与逐请求测量延迟的 benchmarkHTTP 互补：那个测的是**单连接延迟**，
// 这个测的是**聚合吞吐**。两者不能互相推导——
// 单连接延迟决定用户体验，聚合吞吐决定要几台机器。
func benchmarkHTTPParallel(b *testing.B, target string) {
	b.Helper()

	srv := loadServer(b)
	client := srv.Client()

	// 默认 MaxIdleConnsPerHost 只有 2，并发下会不断新建连接，
	// 测出来的会是 TCP 握手开销而不是服务本身的开销。
	if tr, ok := client.Transport.(*http.Transport); ok {
		tr.MaxIdleConnsPerHost = 128
	}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := client.Get(srv.URL + target)
			if err != nil {
				b.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				b.Errorf("状态码 = %d", resp.StatusCode)
				return
			}
		}
	})
}

// 并发检索吞吐：这是决定「要几台机器」的数字。
func BenchmarkHTTPParallelSearch(b *testing.B) {
	benchmarkHTTPParallel(b, "/api/v1/search?q=inverted")
}

// 并发短语检索：最贵的检索形态。
func BenchmarkHTTPParallelPhrase(b *testing.B) {
	benchmarkHTTPParallel(b, `/api/v1/search?q=%22inverted%20index%22`)
}

// 并发写入吞吐。
func BenchmarkHTTPParallelWrite(b *testing.B) {
	srv := loadServer(b)
	client := srv.Client()

	if tr, ok := client.Transport.(*http.Transport); ok {
		tr.MaxIdleConnsPerHost = 128
	}

	body := `{"fields":{"title":"parallel write","body":"a document written concurrently by the benchmark harness"}}`

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/documents/bench-parallel", strings.NewReader(body))
			if err != nil {
				b.Error(err)
				return
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				b.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
				b.Errorf("状态码 = %d", resp.StatusCode)
				return
			}
		}
	})
}

// 写入路径（单连接）。
func BenchmarkHTTPPutDocument(b *testing.B) {
	srv := loadServer(b)
	client := srv.Client()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		body := `{"fields":{"title":"benchmark write","body":"a document written by the benchmark harness"}}`
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/documents/bench-write", strings.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			b.Fatalf("请求失败: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		// 首次是新建（201），之后都是覆盖（200）。
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			b.Fatalf("状态码 = %d", resp.StatusCode)
		}
	}
}
