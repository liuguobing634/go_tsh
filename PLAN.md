# go_tsh — Go 全文搜索服务 项目规划

> 状态：v1 规划稿
> 目标形态：**单二进制、零第三方依赖、内存倒排索引、HTTP API**

---

## 1. 项目定位

用 Go 从零实现一个**可运行的全文搜索服务**：接收 JSON 文档写入内存索引，提供带相关性排序（BM25）的检索接口。

**成功标准（v1 验收线）**

| 维度 | 目标 |
| --- | --- |
| 功能 | 写入 / 更新 / 删除文档；关键词检索、短语检索、布尔组合检索；按 BM25 排序返回 Top-K |
| 数据量 | 10 万篇文档（平均 200 token/篇）内存常驻不 OOM |
| 检索延迟 | 上述规模下 P99 < 20ms（单机、单次查询 1–3 个 term） |
| 索引吞吐 | ≥ 5000 docs/s（单核基准，Go benchmark） |
| 依赖 | `go.mod` 中第三方依赖数 = **0**（仅标准库） |
| 质量 | `gofmt` / `go vet` 干净；核心包单测覆盖 ≥ 80%；`go test -race` 通过 |

---

## 2. 范围界定

### v1 做什么（In Scope）
- 手写倒排索引（term → posting list，带位置信息）
- 英文 + 通用 Unicode 文本分析（切分、小写、停用词、最小词长）
- BM25 相关性打分 + Top-K 堆
- 布尔查询（AND / OR / NOT）与短语查询（`"..."`）
- 内存文档存储（原始字段回显 + 高亮片段）
- HTTP JSON API、优雅关闭、请求日志
- 单元测试 / 基准测试 / `-race` / Dockerfile / README

### v1 不做什么（Out of Scope，留到后续阶段）
- 持久化与崩溃恢复（重启即重建）
- 中文分词（接口预留，实现后置）
- 分布式 / 分片 / 副本
- 拼写纠错、模糊匹配、同义词、词干还原（stemming）
- 向量检索 / 语义检索
- 认证鉴权、多租户、配额

---

## 3. 技术选型

### 3.1 核心选型决策

| 维度 | 选择 | 理由 | 备选与否决原因 |
| --- | --- | --- | --- |
| 语言 | **Go 1.27.1**（环境实测：`D:\Program Files\Go\bin\go.exe`，`go version go1.27.1 windows/amd64`） | 标准库自带 HTTP 路由（`ServeMux` 方法+路径模式）、`-race`、`testing.B` 完善；单二进制部署。下限 1.22，1.27 完全覆盖 | 低版本 Go：无 1.22 方法路由，需退回第三方路由库 |
| 索引引擎 | **纯手写内存倒排索引** | 零依赖、逻辑完全可控、学习价值最高；本项目核心就是这块 | Bleve：成熟但封装太厚，写不出自己的东西；Meilisearch/Typesense：主体不再是 Go |
| 分词 | **英文 + 通用 Unicode 规范化** | `unicode.IsLetter/IsDigit` rune 扫描即可覆盖英文/数字/CJK 单字；零依赖 | `gojieba`/`gse`：引入词典文件与 CGO/大依赖，v1 不做 |
| 归一化 | `unicode.ToLower` + rune 扫描 | 标准库足够；不做 NFKC 折叠（避免引 `x/text`） | `golang.org/x/text` 的 `norm.NFKC`：全角/连字折叠更准，作为可选增强项 |
| 存储 | **纯内存**（`map` + slice） | v1 聚焦检索算法本身；重启靠 API 重新灌数据 | BoltDB/Badger：需处理事务与 mmap，复杂度显著上升 |
| 并发模型 | `sync.RWMutex` 保护索引 | 典型「写少读多」；实现简单、行为可预测 | 分片锁 / copy-on-write：写压力大时再演进 |
| 排序 | 自实现 BM25（k1=1.2, b=0.75） | 工业界默认相关性模型，几十行代码 | TF-IDF：效果明显更差，无理由选 |
| Top-K | 固定大小小顶堆（`container/heap`） | 避免全量排序，O(N log K) | 全量 `sort.Slice`：Top-10 场景浪费明显 |
| HTTP | 标准库 `net/http` | 零依赖，1.22 路由已够用 | gin/echo/chi：省不了多少事，却破坏零依赖目标 |
| 配置 | 环境变量 + 命令行 flag | 标准库 `flag` + `os.Getenv` | viper：过度设计 |
| 日志 | `log/slog`（结构化） | 标准库，JSON 输出便于后续接采集 | zap/zerolog：非必需 |
| 测试 | `testing` + `httptest` + `testing.B` | 标准库全覆盖 | testify：断言更甜但引入依赖，可后期再议 |

### 3.2 明确接受的技术债
1. **无持久化** —— 重启丢失全部索引，靠外部重新导入。
2. **无中文分词** —— 中文只能整段/单字命中，实际中文检索效果差。Analyzer 设计成接口以便后续替换。
3. **无词干还原** —— `running` 搜不到 `run`。作为可选增强项排到 Phase 6。
4. **删除采用逻辑删除 + 惰性压缩** —— 删除后 posting list 中残留 docID 靠 tombstone 跳过，定期重建。

---

## 4. 架构设计

### 4.1 分层

```
                    ┌─────────────────────────────────────┐
   HTTP 请求  ───▶  │  internal/httpapi                    │
                    │  handler / 中间件 / 参数校验 / 错误映射 │
                    └───────────────┬─────────────────────┘
                                    │
                    ┌───────────────▼─────────────────────┐
                    │  pkg/tsh  (Engine 门面)              │
                    │  Index() / Update() / Delete() /     │
                    │  Search(Query) -> []Hit              │
                    └───────┬─────────────────┬───────────┘
                            │                 │
        ┌───────────────────▼──┐      ┌───────▼─────────────┐
        │ internal/analyzer     │      │ internal/query      │
        │ Analyzer 接口          │      │ Query AST + 解析器   │
        │ StandardAnalyzer      │      │ 求交/求并/短语判定    │
        └───────────────────┬───┘      └───────┬─────────────┘
                            │                  │
                    ┌───────▼──────────────────▼───────────┐
                    │  internal/index                      │
                    │  InvertedIndex: term -> PostingList  │
                    │  DocStore: DocID -> Document         │
                    │  stats: N / avgdl / df               │
                    └───────────────┬─────────────────────┘
                                    │
                    ┌───────────────▼─────────────────────┐
                    │  internal/scoring  BM25 + TopK 堆    │
                    └─────────────────────────────────────┘
```

依赖方向**单向向内**：`httpapi → tsh → {analyzer, query, index, scoring}`，`index` 不反向依赖 `httpapi`。

### 4.2 目录结构

```
go_tsh/
├── cmd/
│   └── tshd/
│       └── main.go              # 入口：flag/env 解析、装配、优雅关闭
├── internal/
│   ├── analyzer/
│   │   ├── analyzer.go          # Analyzer/Token 接口定义
│   │   ├── standard.go          # rune 扫描切分 + 小写 + 长度过滤
│   │   ├── stopwords.go         # 内嵌英文停用词表 (go:embed)
│   │   └── standard_test.go
│   ├── index/
│   │   ├── types.go             # DocID / Document / Posting / PostingList
│   │   ├── index.go             # InvertedIndex: Add/Update/Delete/Get
│   │   ├── merge.go             # posting list 归并求交/求并
│   │   └── index_test.go
│   ├── query/
│   │   ├── ast.go               # TermQuery / PhraseQuery / BooleanQuery
│   │   ├── parser.go            # q 字符串 -> AST
│   │   └── parser_test.go
│   ├── scoring/
│   │   ├── bm25.go              # IDF / BM25 打分
│   │   ├── topk.go              # 小顶堆 Top-K
│   │   └── bm25_test.go
│   └── httpapi/
│       ├── server.go            # 路由注册 + 中间件链
│       ├── handlers.go          # 各端点实现
│       ├── dto.go               # 请求/响应结构体
│       ├── errors.go            # 统一错误码与映射
│       └── server_test.go       # httptest 端到端
├── pkg/
│   └── tsh/
│       └── engine.go            # 对外门面（可被其他 Go 程序直接嵌入）
├── testdata/
│   └── corpus/                  # 小规模固定语料，用于确定性测试
├── deploy/
│   └── Dockerfile               # 多阶段构建，产出静态二进制
├── .golangci.yml                # 可选
├── Makefile                     # fmt / vet / test / race / bench / run
├── PLAN.md                      # 本文档
└── README.md
```

### 4.3 核心数据结构

```go
type DocID uint32 // 内部稠密 ID，从 1 递增；externalID -> DocID 走 map

type Document struct {
    ID       DocID
    External string              // 调用方业务 ID
    Fields   map[string]string   // title / body / tags ...
    FieldLen map[string]int      // 每字段 token 数，BM25 归一化用
    TotalLen int
    Tombstone bool               // 逻辑删除标记
}

type Posting struct {
    DocID     DocID
    TF        uint32   // term frequency
    Positions []uint32 // 文档内 token 位置，短语查询用
}

type PostingList struct {
    Postings []Posting // 按 DocID 升序，保证归并求交为 O(n+m)
    DF       uint32    // document frequency
}

type InvertedIndex struct {
    mu        sync.RWMutex
    terms     map[string]*PostingList // key = "field\x00term"
    docs      map[DocID]*Document
    byExt     map[string]DocID
    nextID    DocID
    nDocs     uint32 // 未删除文档数
    totalLen  uint64 // 所有文档 token 总数 -> avgdl
}
```

**字段隔离方案**：term key 采用 `field + "\x00" + term`（`\x00` 不会出现在分析器输出中）。好处是无需嵌套 map、字段级 BM25 统计天然隔离、跨字段查询就是多个 key 的并集。

### 4.4 写入流程

```
POST /documents {id, fields}
   ↓ 校验：id 非空、字段数/单字段长度/总字节数上限
   ↓ 若 id 已存在 → 先 Delete 旧版本（tombstone + 从 posting 中摘除）
   ↓ 分配 DocID
   ↓ 对每个字段：Analyzer.Analyze(field, text) -> []Token{Term, Position}
   ↓ 按 term 聚合成倒排（同 term 同 doc 的 TF 自增，Positions 追加）
   ↓ 持写锁：写入 terms / docs / byExt，更新 nDocs、totalLen、DF
   ↓ 返回 201
```

**删除策略**：`Delete` 直接遍历该文档涉及的 term 并从各 posting list 中移除该 docID（文档通常只涉及几百个 term，成本可控）；同时标记 tombstone。若后测显示删除成为瓶颈，再切换为「逻辑删除 + 查询期跳过 + 后台压缩」。

### 4.5 查询流程

```
GET /search?q=hello "world" -foo&limit=10&mode=and
   ↓ 解析 q -> AST（引号 -> PhraseQuery；- 前缀 -> MustNot；bare -> 默认 Should/Must）
   ↓ 分析器对每个 term 做同样的归一化（关键：保证 query 与 index 走同一 pipeline）
   ↓ 取各 term 的 PostingList
   ↓ 归并求交/求并，得到候选 DocID 集
   ↓ 短语查询：对候选 doc 校验 Positions 是否存在连续递增序列
   ↓ MustNot：从候选集中减去
   ↓ BM25 打分（逐 term 累加 IDF * tf 饱和项）
   ↓ Top-K 小顶堆取出 limit 条
   ↓ 拼装 Hit{docID, score, 高亮片段}
   ↓ 返回 JSON
```

### 4.6 API 契约

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/api/v1/documents` | 新建文档。body: `{"id":"doc-1","fields":{"title":"...","body":"..."}}` → `201` |
| `PUT` | `/api/v1/documents/{id}` | 覆盖更新 → `200` |
| `DELETE` | `/api/v1/documents/{id}` | 删除 → `204` |
| `GET` | `/api/v1/documents/{id}` | 取原文 → `200` |
| `GET` | `/api/v1/search` | 查询，见下 |
| `GET` | `/api/v1/stats` | `{"docs":N,"terms":M,"avg_doc_len":X,"index_bytes":B}` |
| `GET` | `/healthz` | `200 ok` |

**查询参数**：`q`（必填）、`limit`（默认 10，上限 100）、`offset`（默认 0）、`mode`（`or` 默认 / `and`）、`field`（限定单字段，可选）、`highlight`（bool，默认 false）。

**查询响应**：

```json
{
  "took_ms": 3,
  "total": 42,
  "hits": [
    {
      "id": "doc-1",
      "score": 8.734,
      "fields": { "title": "Hello World", "body": "..." },
      "highlights": { "body": "...<em>hello</em> <em>world</em>..." }
    }
  ]
}
```

**统一错误体**：`{"error":{"code":"INVALID_QUERY","message":"..."}}`，HTTP 状态码语义化（400 / 404 / 409 / 413 / 500）。

### 4.7 关键服务端约束（防打挂自己）

| 约束 | 默认值 | 配置项 |
| --- | --- | --- |
| 单请求体上限 | 8 MiB | `TSH_MAX_BODY_BYTES` |
| 单文档总 token 上限 | 100k | `TSH_MAX_DOC_TOKENS` |
| 单文档字段数上限 | 32 | — |
| 查询 term 数上限 | 32 | — |
| 查询超时 | 5s | `TSH_QUERY_TIMEOUT` |
| 读超时 / 写超时 / Idle | 10s / 30s / 60s | — |
| 内存软上限告警 | 1 GiB | `TSH_MEM_WARN_BYTES` |

---

## 5. 里程碑

| 阶段 | 内容 | 产出 | 预估 |
| --- | --- | --- | --- |
| **M0** | 环境与骨架 | 可编译空服务、Makefile、CI 雏形 | 0.5 天 |
| **M1** | 文本分析器 | StandardAnalyzer + 单测 + 基准 | 1 天 |
| **M2** | 索引内核 | Add/Update/Delete/Get + 并发安全 + 基准 | 2 天 |
| **M3** | 查询与打分 | Query AST、归并、短语、BM25、Top-K | 2.5 天 |
| **M4** | HTTP 服务 | 全部端点 + 中间件 + httptest 端到端 | 1.5 天 |
| **M5** | 工程化交付 | README、demo、Dockerfile、压测报告 | 1 天 |
| **M6** | 可选演进 | 持久化 / 中文分词 / 前缀模糊 / 分片 | 按需 |

> 每个里程碑结束都必须满足：`make check` 全绿（fmt + vet + test -race）。

---

## 6. TODO List

### Phase 0 — 环境准备

> **实测环境记录**
> - Go：`go1.27.1 windows/amd64`，安装于 `D:\Program Files\Go`，`GOROOT` 自动识别正确
> - **`go` 未加入 PATH**（`where go` 无结果），`GOROOT` 环境变量为空 → 需显式加 PATH 或用全路径调用
> - `GOPATH=C:\Users\liuguobing\go`，`GOPROXY=https://proxy.golang.org,direct`
> - **本项目零第三方依赖，因此 GOPROXY 是否可达不影响构建**（已实测外网代理在非交互 shell 下不可验证，可忽略）
> - 沙箱：`workspace-write` 模式初始化失败，见下方阻塞项

- [x] ~~确认 Go 工具链~~ → 已定位 `D:\Program Files\Go\bin\go.exe`，版本 1.27.1
- [x] ~~创建 `D:\codes\golang\go_tsh` 目录~~ → 已由文档写入创建
- [ ] **解决沙箱阻塞（最高优先级）**：`pwsh` 在 `workspace-write` 模式下每次启动即报 `SetNamedSecurityInfoW failed (Win32 5): grantWrite(D:\codes\golang\go_tsh)`，无法执行任何命令；仅 `danger-full-access` 可用。需以管理员身份授予当前用户对该目录的完全控制（或 `WRITE_DAC`），否则后续所有 `go build/test` 都只能走逐条审批
- [ ] 把 `D:\Program Files\Go\bin` 加入用户 PATH（或在 `Makefile` / 脚本中用绝对路径 + `GO` 变量封装）
- [ ] `go mod init github.com/<username>/go_tsh`（模块名待定，需确认）
- [ ] 建目录骨架：`cmd/tshd`、`internal/{analyzer,index,query,scoring,httpapi}`、`pkg/tsh`、`testdata/corpus`、`deploy`
- [ ] `Makefile`：`fmt` / `vet` / `test` / `race` / `bench` / `run` / `check`（`GO ?= go` 便于覆盖路径）
- [ ] `.gitignore`（二进制、覆盖率、`*.out`）
- [ ] `git init` + 首次提交
- [ ] **验收**：`make check` 在空骨架下即全绿

### Phase 1 — 文本分析器
- [ ] 定义 `Token{Term string; Position uint32}` 与 `Analyzer` 接口
- [ ] `StandardAnalyzer`：rune 扫描按 `unicode.IsLetter/IsDigit` 切分（CJK/emoji/连字符/下划线正确处理）
- [ ] 归一化：`unicode.ToLower`；可选 `strings.ToLower` 前置
- [ ] 最小词长过滤（默认 2，数字除外）
- [ ] 内嵌英文停用词表（`go:embed` + `stopwords.txt`，约 120 词）
- [ ] 单测：大小写、标点、连续空白、空串、纯标点、混合 CJK、emoji、超长 token
- [ ] 基准：`BenchmarkAnalyze`（1KB / 10KB 文本）
- [ ] **验收**：`Hello, WORLD!` → `[hello, world]`；位置连续且与原文顺序一致

### Phase 2 — 索引内核
- [ ] 定义 `DocID` / `Document` / `Posting` / `PostingList`
- [ ] `InvertedIndex` 结构 + `sync.RWMutex`
- [ ] `AddDocument`：分析 → 按 term 聚合 TF 与 Positions → 写索引
- [ ] term key 编码 `field + "\x00" + term`，并提供跨字段查询用的 key 生成函数
- [ ] `UpdateDocument`：先摘除旧版本再插入，保证不产生幽灵命中
- [ ] `DeleteDocument`：从所有相关 posting list 摘除 + tombstone
- [ ] `GetDocument` / `Stats`（nDocs、term 数、avgdl）
- [ ] posting list 保持按 DocID 有序（插入时二分定位）
- [ ] 单测：新增/更新/删除、重复 term TF 正确、Positions 正确、删除后查不到、更新后旧词查不到
- [ ] 并发测试：`go test -race` 下 N 写 M 读无数据竞争
- [ ] 基准：`BenchmarkIndex10kDocs`、`BenchmarkDelete`
- [ ] **验收**：10 万篇文档索引完成且 RSS 可观测、无泄漏

### Phase 3 — 查询与打分
- [ ] Query AST：`TermQuery` / `PhraseQuery` / `BooleanQuery{Must,Should,MustNot}`
- [ ] `q` 字符串解析器：bare term、`"phrase"`、`-neg`、`AND`/`OR`/`NOT`、括号（可选）
- [ ] **query 与 index 共用同一 Analyzer**（防止归一化不一致导致查不到）
- [ ] posting list 归并：有序求交、求并（二分 + 双指针）
- [ ] 短语查询：候选 doc 内位置差分连续判定
- [ ] BM25：`IDF = ln(1 + (N-df+0.5)/(df+0.5))`，`tf` 饱和项，`k1=1.2 b=0.75`
- [ ] Top-K 小顶堆（`container/heap`）
- [ ] 高亮片段生成（窗口截取 + `<em>` 包裹，需 HTML 转义）
- [ ] 单测：单 term、多 term OR/AND、短语误召拦截、否定词、空结果、排序确定性（同分按 DocID 升序）
- [ ] 基准：`BenchmarkSearch1Term` / `3TermsPhrase` / `AndQuery`
- [ ] **验收**：P99 < 20ms @ 10 万文档

### Phase 4 — HTTP 服务
- [ ] `net/http` + Go 1.22 `ServeMux` 路由注册
- [ ] 中间件链：`recover` → 请求日志（`slog`）→ 超时 → body 大小限制 → CORS（可选）
- [ ] 各端点 handler + 请求体校验（`json.Decoder` + `DisallowUnknownFields`）
- [ ] 统一错误响应与状态码映射
- [ ] 优雅关闭（`signal.NotifyContext` + `srv.Shutdown`）
- [ ] `/healthz`、`/api/v1/stats`
- [ ] `httptest` 端到端测试：写入→查询→更新→查询→删除→查不到
- [ ] 限流/并发上限保护（`semaphore` 或 `http.MaxBytesReader`）
- [ ] **验收**：`curl` 走通 README 中的完整示例

### Phase 5 — 工程化与交付
- [ ] `README.md`：项目简介、架构图、快速开始、API 文档、curl 示例、性能数据
- [ ] demo 命令：`tshd -import ./testdata/corpus` 一键灌数据
- [ ] 语料/基准数据生成脚本（合成 10 万篇）
- [ ] `deploy/Dockerfile`：多阶段 + `CGO_ENABLED=0` 静态二进制 + distroless/scratch
- [ ] CI：`gofmt -l`、`go vet`、`go test -race ./...`、覆盖率上传
- [ ] 压测报告（`hey` / `wrk`）：QPS、P50/P95/P99、内存曲线
- [ ] 编写 `docs/API.md` 与 `docs/DESIGN.md`（把本文档拆分沉淀）
- [ ] **验收**：新机器 `make check && make run` 5 分钟内跑通

### Phase 6 — 可选演进（按需排期，不在 v1 承诺内）
- [ ] 快照持久化：`gob`/自定义二进制序列化 + 启动加载 + 定期 `Save`
- [ ] 中文分词：`gse` 或自研词典 + 最大正向匹配，接入 `Analyzer` 接口
- [ ] 词干还原（Porter Stemmer，纯 Go 移植）
- [ ] 前缀查询（`hel*`）与编辑距离模糊查询（Levenshtein ≤ 2）
- [ ] 索引分片：按 DocID 取模分 N 片，各片独立锁提升写并发
- [ ] 段合并（LSM 思路）+ 增量快照
- [ ] 字段权重（`title^3 body`）与自定义 boost
- [ ] 向量检索接入

---

## 7. 测试与验收策略

| 层级 | 手段 | 覆盖对象 |
| --- | --- | --- |
| 单元测试 | `testing` 表驱动 | analyzer / index / query / scoring |
| 并发测试 | `go test -race` | index 读写混合 |
| 端到端 | `httptest.NewServer` | httpapi 全链路 |
| 基准测试 | `testing.B` + `-benchmem` | Analyze / Index / Search / Delete |
| 数据校验 | `testdata/corpus` 固定语料 + 黄金输出 | 防止重构引入相关性回归 |
| 压测 | `hey -n 100000 -c 50` | 真实 QPS 与延迟分布 |

**CI 门禁**：`gofmt -l .` 无输出 → `go vet ./...` 无告警 → `go test -race ./...` 全绿 → 覆盖率 ≥ 80%（核心包）。

---

## 8. 风险与应对

| 风险 | 影响 | 应对 |
| --- | --- | --- |
| **内存占用失控** | 10w 文档 OOM | `Positions []uint32` 是内存大头；先做容量基线测量，必要时改为 delta 编码或按字段可选关闭位置信息 |
| **删除/更新性能** | 写延迟抖动 | v1 用「摘除 posting」；不达标则切 tombstone + 后台压缩 |
| **query/index 归一化不一致** | 明明存在却搜不到 | 强制共用同一个 `Analyzer` 实例，并加一条「写入后立即可查到」的回归测试 |
| **中文检索效果差** | 中文用户体感差 | 文档中明确标注限制；Phase 6 接入可插拔分词器 |
| **并发写下的 map 竞争** | 数据损坏 / panic | 所有写路径持写锁；`-race` 进 CI 门禁 |
| **相关性无客观标准** | 调参全靠感觉 | 固定黄金语料 + 期望 Top-3 结果集，作为回归测试 |
| **沙箱/权限阻塞** | `workspace-write` 下 `pwsh` 完全不可用，构建与测试无法自动化 | **最高优先级**：修复 `D:\codes\golang\go_tsh` 目录 ACL（授予当前用户完全控制），否则 Phase 0 之后的每个命令都要逐条审批 |
| **`go` 不在 PATH** | 脚本与 CI 找不到工具链 | 用户 PATH 中追加 `D:\Program Files\Go\bin`；同时在 `Makefile` 里用可覆盖的 `GO` 变量兜底 |

---

## 9. 下一步

**已完成**
- ✅ 技术选型确认（手写内存倒排索引 / 英文 Unicode 分析 / HTTP API / 纯内存）
- ✅ 规划文档落盘
- ✅ Go 工具链定位：`D:\Program Files\Go`，`go1.27.1 windows/amd64`

**待办（按顺序）**
1. 用户确认本文档规划是否符合预期（尤其**范围界定**与**验收线**）。
2. 修复 `D:\codes\golang\go_tsh` 目录 ACL，使 `workspace-write` 沙箱可用。
3. 确认 Go module 路径（`go mod init` 用什么名字）。
4. 执行 Phase 0 → Phase 1，跑通第一个可编译、可测试的最小闭环（分析器）。
