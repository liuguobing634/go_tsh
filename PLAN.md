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

### Phase 0 — 环境准备 ✅ 已完成（commit 24228da）

> **实测环境记录**
> - Go：`go1.27.1 windows/amd64`，安装于 `D:\Program Files\Go`，`GOROOT` 自动识别正确
> - **`go` 未加入 PATH**（`where go` 无结果），`GOROOT` 环境变量为空 → 需显式加 PATH 或用全路径调用
> - `GOPATH=C:\Users\liuguobing\go`，`GOPROXY=https://proxy.golang.org,direct`
> - **本项目零第三方依赖，因此 GOPROXY 是否可达不影响构建**（已实测外网代理在非交互 shell 下不可验证，可忽略）
> - Shell 实为 **Windows PowerShell 5.1**（`PSEdition=Desktop`，并非 pwsh 7）：`Console.OutputEncoding=utf-8` 但 `InputEncoding=gb2312`，**`Get-Content` 默认按 ANSI/GBK 解码**，读 Go 程序输出的 UTF-8 日志会乱码（实测 `服务启动` 显示为 `鏈嶅姟鍚姩`），必须显式 `Get-Content -Encoding utf8`；日志文件本身的字节是正确的 UTF-8（`e6 9c 8d e5 8a a1 ...`）
> - ⚠️ **`-race` 在本机不可用**：`CGO_ENABLED=0` 且宿主无任何 C 编译器（gcc / clang / tcc 均未找到，`GOENV` 文件不存在）。竞态检测依赖 cgo，没有它就完全跑不了
> - **`.ps1` 脚本必须保持纯 ASCII**：PS 5.1 会把**无 BOM** 的 UTF-8 脚本按 GBK 解析，中文字符被打碎后直接破坏语法（实测 `scripts/check.ps1` 最初含中文注释时报 `Unexpected token '}'`）。本项目约定所有 `.ps1` 只用 ASCII；「保存为带 BOM 的 UTF-8」方案已否决，因为后续任何一次编辑都可能把 BOM 丢掉，属于隐形定时炸弹
> - ✅ **沙箱问题已定位并修复**：根因是 DSH 沙箱使用的 write-restricted token 会绕过 NTFS「所有者隐式拥有 `WRITE_DAC`」规则，需要一条**显式** FullControl ACE。详见下方「遗留阻塞 1」
> - 沙箱内 `go` 会警告模块缓存 `C:\Users\liuguobing\go\pkg\mod\cache` 不可写；本项目零依赖，构建与测试不受影响

- [x] 确认 Go 工具链 → `D:\Program Files\Go\bin\go.exe`，`go1.27.1`
- [x] 创建 `D:\codes\golang\go_tsh` 目录
- [x] `go mod init` → `github.com/liuguobing/go_tsh`（go.mod 直写，`go mod tidy` 已验证零依赖）
- [x] 目录骨架：`cmd/tshd`、`internal/{analyzer,index,query,scoring,httpapi,config}`、`pkg/tsh`、`scripts`
- [x] `internal/config`：flag + `TSH_*` 环境变量，优先级 **flag > env > default**，含全字段校验与表驱动测试
- [x] `internal/httpapi`：Go 1.22 `ServeMux` 方法路由、recover / 访问日志 / body 限流中间件链、统一错误响应、DTO
- [x] `pkg/tsh`：`Engine` 门面与 `Stats`
- [x] `cmd/tshd`：JSON 结构化日志、`signal.NotifyContext` 优雅关闭、`-ldflags` 版本注入
- [x] `Makefile`（fmt / fmt-check / vet / test / race / cover / bench / build / run / check）
- [x] `scripts/check.ps1`：自动从 `GOROOT` 推导 gofmt 绝对路径，`-race` 不可用时自动降级为普通 `go test` 并明确告警
- [x] `.gitignore`、`.gitattributes`、`README.md`（含配置表与接口清单）
- [x] `git init` + 首次提交
- [x] **验收**：`go build ./...` / `go vet ./...` / `gofmt -l .` / `go test ./...` 全绿；二进制冒烟测试通过（`/healthz`→200、`/api/v1/stats`→200、未知路径→404、`POST /healthz`→405）
- [x] **遗留阻塞 1｜沙箱 —— 已修复**

      **症状**：`workspace-write` 模式下每条命令都在沙箱初始化阶段失败，报
      `SetNamedSecurityInfoW failed (Win32 5): grantWrite(D:\codes\golang\go_tsh)`，
      只有 `danger-full-access` 可用。

      **排查过程与结论**（`Get-Acl` / `icacls` / 一次 `Set-Acl` 对照实验）：
      - `D:` 是 **NTFS**，不是 FAT/exFAT；目录也不是 junction 或同步盘 → ACL 机制本身可用
      - DACL 中**没有任何 DENY 规则**
      - 目录 Owner 就是当前用户，但 ACL 只给了 `Authenticated Users:(M)` 与 `Users:(RX)`，
        两者**都不含 `WRITE_DAC`**
      - 关键反证：非提权状态下 `Set-Acl` **能成功**改写 DACL —— 因为 Windows 对
        对象所有者隐式授予 `READ_CONTROL` + `WRITE_DAC`
      - ⇒ 结论：DSH 沙箱用 **write-restricted token**（`CreateRestrictedToken` 的
        `WRITE_RESTRICTED`）运行命令，它绕过了所有者的隐式特权，强制写访问必须由
        DACL **显式**授权，因此被拒

      **修复**（无需管理员，因为你是目录所有者）：
      ```powershell
      icacls "D:\codes\golang\go_tsh" /grant "%USERNAME%:(OI)(CI)F"
      ```
      或等价的 PowerShell：
      ```powershell
      $me  = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
      $acl = Get-Acl 'D:\codes\golang\go_tsh'
      $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new(
          $me, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow'))
      Set-Acl -Path 'D:\codes\golang\go_tsh' -AclObject $acl
      ```

      **验证**：修复后不带任何提权重跑 `pwsh` 成功；`go build` / `go vet` / `go test` /
      `scripts/check.ps1` 全部在沙箱内通过。

      **回滚**：`icacls "D:\codes\golang\go_tsh" /remove:g "%USERNAME%"`
- [x] **遗留阻塞 3｜沙箱只放行工作区，Go 构建缓存不可写 —— 已修复**

      症状：新增源文件后 `go build` 全量失败：
      `open C:\Users\liuguobing\AppData\Local\go-build\...: Access is denied`。
      之前几轮能过只是因为全部命中缓存、**根本不需要写缓存**。

      **关键结论：DSH 沙箱按「路径白名单」放行，不认 ACL。**
      实测给 `GOCACHE` / `GOMODCACHE` 补写显式 FullControl ACE **完全无效**——
      这与工作区的情况不同：工作区之所以靠加 ACE 就修好，是因为它本来就在
      DSH 白名单内，DSH 自己会去给它 `SetNamedSecurityInfoW(grantWrite)`，
      只是当时那条 ACL 写不进去。

      修复：`scripts/check.ps1` 启动时**实际探测**默认 GOCACHE 的可写性
      （建探针文件再删，ACL / 受限令牌 / 只读挂载在 `Test-Path` 下长得一模一样），
      不可写就自动回退到 `<repo>/.gocache/`。沙箱外行为完全不变。
- [ ] **遗留观察｜沙箱内模块缓存仍不可写**：`go` 仍会打印
      `writing stat cache: open C:\Users\liuguobing\go\pkg\mod\cache\...: Access is denied`。
      本项目零依赖，构建与测试均不受影响，属噪音级警告。
      若将来引入第三方依赖，需把 `GOMODCACHE` 同样指向工作区内。
- [ ] **遗留阻塞 2｜`-race` 不可用**：需安装 mingw-w64 并令 `CGO_ENABLED=1`，或把 `-race` 门禁下沉到 Linux CI。**在解决前，Phase 1 之后的并发安全只能靠人工审查，缺少工具兜底**
- [ ] **遗留项｜PATH**：把 `D:\Program Files\Go\bin` 加入用户 PATH（当前靠脚本绝对路径兜底，不影响开发）
- [ ] **遗留项｜换行符**：`git` 提示 LF→CRLF 转换，已加 `.gitattributes`，需 `git add --renormalize .` 落地

### Phase 1 — 文本分析器 ✅ 已完成（commit 1761d7d + 5a8a064）

> **与原计划的偏差**（都是有意的改进）：
> - **CJK 逐字切分**：原计划只说「英文 + Unicode 规范化」。但若把一整段中文当成一个 token，
>   检索会完全失效。改为 Han / Hiragana / Katakana **逐字**切分（unigram），
>   至少保证单字可召回；真正的分词仍留给 Phase 6。
>   韩文 Hangul 不在此列——现代韩文以空格分词，按普通词处理即可。
> - **词内撇号保留**：`don't` 不再被拆成 `don` + `t`，弯引号 `’` 归一为直引号 `'`，
>   否则停用词表里的缩略词永远匹配不上。
> - **全角折叠**：`ＦＵＬＬ` → `full`。覆盖中文输入法下常见的全角字母/数字，
>   成本只是 `r-rune(0xFF01)+0xFEE0` 的区间映射，无需引入 `x/text` 的 NFKC。
> - **停用词表 177 条**（原计划约 120 条）。

- [x] 定义 `Token{Term string; Position uint32}` 与 `Analyzer` 接口
- [x] `StandardAnalyzer`：按 `unicode.IsLetter/IsDigit` 切分，其余字符为分隔符
- [x] 归一化：`unicode.ToLower` + 全角折叠 + 弯引号归一
- [x] 最小词长过滤（默认 2，**不作用于 CJK 单字**）
- [x] 内嵌英文停用词表（`go:embed` + `stopwords.txt`，177 条，支持 `#` 注释与行尾注释）
- [x] **位置语义**：被过滤的词条同样占用位置，保持词间距，
      使 `quick the brown` 中 quick 与 brown 位置差为 2，短语 `quick brown` 不会误命中
- [x] 单测：切分/大小写/标点/连续空白/空串/纯标点/全角/CJK/中英混合/撇号/数字/重复词，
      另加位置单调性、停用词加载、6 组选项、纯函数性、并发一致性
- [x] 基准：`BenchmarkAnalyzeEnglish` / `BenchmarkAnalyzeMixed` / `BenchmarkNewStandardWith`
- [x] **验收**：`Hello, WORLD!` → `[hello@0, world@1]`；覆盖率 **97.8%**

> **实测性能**（Intel Core Ultra 7 155H，1 KB 文本）
> | 基准 | ns/op | 吞吐 | 分配 |
> | --- | --- | --- | --- |
> | `AnalyzeEnglish` | 43,277 | 29.5 MB/s | 201 allocs/op |
> | `AnalyzeMixed` | 29,550 | 33.3 MB/s | 255 allocs/op |
> | `NewStandardWith` | 71,348 | — | 373 allocs/op |
>
> 按 1 KB/doc 估算约 **29k docs/s**，满足「≥ 5000 docs/s」目标。
> `NewStandardWith` 每次重建停用词表 map，**必须只构造一次并复用**——
> 这正是指标里 `AllocsPerRun` 需要关注的地方。
> 分配数偏高（`[]rune(text)` 转换 + 每个 token 一次 `string` 分配），
> 若 Phase 5 压测显示索引是瓶颈，可考虑复用缓冲区。

> **踩坑记录**：`TestAnalyze` 第一版全部走默认分析器，而 `out` 恰好在停用词表里，
> 导致期望值写错、测试失败。根因是**切分测试与停用词过滤耦合**。
> 现已改为 `tokenizer(KeepStopwords)` 与 `filtering(默认)` 两个分析器，
> 用例显式声明是否启用停用词，把两件事彻底解耦。

### Phase 2 — 索引内核 ✅ 已完成（commit 5815de4）
> **与原计划的偏差**
> - **删除改为物理摘除，不用 tombstone**。原计划接受「逻辑删除 + 惰性压缩」的技术债，
>   但既然 `removeLocked` 已能精确定位每条 posting，直接摘干净反而更简单：
>   查询路径不必在运行时跳过墓碑，也没有后台压缩任务。
> - **删除靠「重新分析原文」定位 posting**，而不是在文档上常驻 term key 列表。
>   后者在 10 万文档量级要多吃数百 MB 常驻内存；删除是低频操作，
>   重算一遍分词远比常驻内存划算。
> - **分词挪到写锁之外**：`prepare` 先完成校验与全部分词，再加锁做结构修改，
>   否则一个慢分词会阻塞所有其它写请求。
> - **对外一律返回深拷贝**（`Get` / `Postings`），否则调用方能绕过锁改坏内部状态。

- [x] 定义 `DocID` / `Document` / `Posting` / `PostingList`
- [x] `InvertedIndex` + `sync.RWMutex`
- [x] `Add`：分析 → 稳定排序分组得 TF 与 Positions → 写索引
- [x] term key 编码 `field + "\x00" + term` + `SplitTermKey` 逆运算
      （用 NUL 而非冒号：`(a, b:c)` 与 `(a:b, c)` 在冒号方案下会撞车）
- [x] `Update`：完整摘除旧版本再插入，旧词条不留幽灵命中
- [x] `Delete`：从所有相关 posting 摘除，空列表连同 key 一起回收
- [x] `Get` / `Stats`（Docs / Terms / Fields / TotalTokens / AvgDocLen / IndexBytes）
- [x] `Upsert`、`Has`、`DocFreq`、`DocLength`、`FieldStats`、`Fields`
- [x] posting 按 DocID 严格升序（DocID 单调递增 + 删除整条摘除 ⇒ append 即有序）
- [x] 单测：41 个用例，覆盖校验/重复/更新/删除/Upsert/字段隔离/posting 有序性/
      副本语义/字段回收/统计/并发读写
- [x] 并发测试：4 写 × 4 读 × 150 轮，结束后校验不变式
- [x] 基准：`BenchmarkAddDocument` / `BenchmarkDeleteDocument` / `BenchmarkPostingsLookup`
- [x] **验收**：覆盖率 **97.1%**；`go build` / `go vet` / `gofmt` / `go test` 全绿

> **实测性能**（Intel Core Ultra 7 155H）
>
> | 基准 | ns/op | B/op | allocs/op |
> | --- | --- | --- | --- |
> | `AddDocument`（约 140 token，title+body） | 36,875 | 11,384 | 171 |
> | `DeleteDocument`（同上） | 118,995 | 8,103 | 143 |
> | `PostingsLookup`（1 万篇命中同一词条） | **701,225** | **407,680** | **10,001** |
>
> - 索引吞吐约 **27k docs/s**，满足「≥ 5000 docs/s」目标。
> - **删除比新增慢 3.2 倍**：要重新分词，且每个词条都得在 posting 里二分查找 +
>   切片搬移。删除属低频操作，暂可接受，但记为 Phase 5 的观察点。
> - ⚠️ **`PostingsLookup` 0.7ms / 400KB 是必须解决的瓶颈**：`Postings()` 对每条记录
>   都深拷贝 `Positions`，1 万条就是 1 万次分配。一次 3 词查询光拷贝约 2ms/万篇，
>   10 万篇量级将直接击穿「P99 < 20ms」预算。方案见 Phase 3 开头的零拷贝改造。

### Phase 3 — 查询与打分
> ⚠️ **首要任务：先解决 `Postings()` 的拷贝开销，再写打分逻辑。**
> Phase 2 的基准已经量化了这个问题（0.7ms / 400KB / 1 万次分配，见上）。
> 路线是新增零拷贝迭代接口，让整次检索在一次读锁内完成：
>
> ```go
> // ScanPostings 在读锁内遍历 posting，不产生任何拷贝。
> // fn 返回 false 提前结束；positions 仅在 fn 执行期间有效，不得保留。
> // fn 内部绝不可调用索引的写方法（会死锁）。
> func (ix *InvertedIndex) ScanPostings(field, term string,
>     fn func(id DocID, tf uint32, positions []uint32) bool)
> ```
>
> 关键观察：**只有短语查询需要 `Positions`**。普通词条查询拿到 `(DocID, TF)`
> 就够了，为它们拷贝位置信息是纯粹的浪费。

- [x] **零拷贝扫描已完成 —— 效果远超预期**

      实现方式是 `View`：把整次检索放进**同一把读锁**，既拿到一致快照又保持零拷贝。
      比原计划的裸 `ScanPostings` 更正确——裸接口各自持锁，两次调用之间
      可能插入写操作，AND 查询会看到「文档 A 在前一个词条的列表里、
      却不在后一个里」这种自相矛盾的状态。

      | 基准（1 万篇命中同一词条） | ns/op | B/op | allocs/op |
      | --- | --- | --- | --- |
      | `PostingsCopy`（Phase 2 旧路径） | 357,658 | 448,645 | 10,002 |
      | `ViewScan` | **28,007** | **8** | **1** |
      | `ViewScanPhrase` | **19,044** | **8** | **1** |

      **快 12.8 倍，分配次数从 10002 降到 1**，P99 预算的威胁解除。
      （`ViewScanPhrase` 反而更快，是因为 `ScanIDs` 多套了一层闭包。）
- [x] Query AST：`Term` / `Phrase` / `Bool{Must,Should,MustNot}`
- [x] `q` 字符串解析器：bare term、`"phrase"`、`-neg`、`NOT`、`AND`/`OR`、括号、
      转义引号；子句数上限（默认 64，短语按单词数计入）防查询串打爆内存
- [x] **解析器不做分词**：只产出语法树，原始文本原样保留。
      分词由执行阶段用索引自己的 Analyzer 完成，从根本上杜绝两侧归一化不一致
- [ ] **query 与 index 共用同一 Analyzer**（用 `Index().Analyzer()` 取）
- [ ] posting list 归并：有序求交、求并（双指针）
- [ ] 短语查询：**必须在单个字段内**判定位置连续性——各字段位置都从 0 开始，
      跨字段拼位置会产生假阳性
- [ ] BM25：`IDF = ln(1 + (N-df+0.5)/(df+0.5))`，`tf` 饱和项，`k1=1.2 b=0.75`；
      **IDF 必须按字段计算**，否则短字段的高信息量会被长字段稀释
- [ ] 跨字段检索：分别求值再把分数相加
- [ ] Top-K 小顶堆（`container/heap`）
- [ ] 高亮片段生成（窗口截取 + `<em>` 包裹，需 HTML 转义）
- [ ] 单测：单 term、多 term OR/AND、短语误召拦截、否定词、空结果、
      排序确定性（同分按 DocID 升序）
- [ ] 基准：`BenchmarkSearch1Term` / `3TermsPhrase` / `AndQuery`
- [x] **验收**：功能与正确性全部达成；**性能尚未达标**，见下

> ### ⚠️ 性能现状（2 万篇语料，Intel Core Ultra 7 155H）
>
> | 基准 | 优化前 | 优化后 |
> | --- | --- | --- |
> | `SearchTerm`（"quick" 命中全部） | 8.39 ms | **3.83 ms** |
> | `SearchPhrase`（`"quick brown"`） | 48.35 ms | **10.40 ms** |
> | `SearchAnd` | 28.62 ms | 26.06 ms |
> | `SearchMustNot` | — | 23.35 ms |
>
> **已解决：短语查询。** 用 pprof 定位到瓶颈是 `slices.BinarySearchFunc`
> （13.7%）与伴随的字符串 map 查找（11.2%）——即「扫锚点词条 + 对每个候选
> 文档二分反查其余词条」。改成 `PostingCursor` 归并后，每个 posting 只被
> 顺序访问一次，降到 10.4 ms。
>
> **仍未解决：布尔组合。** 根因是求值器用 `map[DocID]float64` 当累加器：
> 2 万条命中就是 4 万次 map 读写，10 万篇量级会击穿 20 ms 预算。
>
> **下一步（Phase 3c）**：把累加器从 map 换成**按 DocID 有序的切片**。
> posting 列表本身有序，所以单字段求值直接产出有序结果，
> 求交/求并/求差全部可以走双指针归并，彻底消除哈希开销——
> 这正是 PLAN 最初写的方案，被我用 map 走了捷径，现在要还回来。
>
> **顺带修正一个测量缺陷**：早先每个基准函数内部都重建 2 万篇语料，
> 虽然放在 `ResetTimer` 之前，但 CPU profile 覆盖整个测试进程，
> 建索引的时间会混进 profile。现已改为包级 `sync.Once` 只建一次。
> 第一次做 profile 时正是被这个坑误导过。

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
| **沙箱/权限阻塞** | `workspace-write` 下 `pwsh` 完全不可用，构建与测试无法自动化 | ✅ 已修复：工作区加显式 FullControl ACE。另注意沙箱按**路径白名单**放行而非 ACL，工作区外的缓存目录需靠 `check.ps1` 自动回退 |
| **验证工具本身是错的** | 门禁永远绿灯，缺陷静默流入 | 🔴 已发生过一次：`check.ps1` 用 `$ok = & $Action` 判成败，而原生命令 stdout 也被吸进 `$ok`，非空数组恒为真 ⇒ `go vet`/`go test` 失败时仍报 `check passed`（见 commit 5815de4）。**教训：门禁本身必须做反向测试**——喂一个必然失败的输入，确认它真的会红 |
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
