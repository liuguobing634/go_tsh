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
- ~~持久化与崩溃恢复（重启即重建）~~ → **已排入 Phase 8**，方案已定，尚未实施
- ~~中文分词（接口预留，实现后置）~~ → **已于 Phase 7 完成**
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

> **修订（v1 之后）**：中文分词最终**做了**，选的是 `gse`。
> 上方「CGO/大依赖」的顾虑对 `gojieba` 成立、对 `gse` 不成立——
> gse 是纯 Go，实测 `CGO_ENABLED=0 GOOS=linux` 构建通过，静态交付形态未受影响。
> 代价在**体积与内存**（二进制 +30 MB、内存基线 108 MB），不在交付形态。
> 详见 [Phase 7](#phase-7--中文分词与检索--已完成) 与
> [docs/CHINESE.md](docs/CHINESE.md)。
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
1. ~~**无持久化** —— 重启丢失全部索引，靠外部重新导入。~~
   **已排入 Phase 8**（方案已定、尚未实施）。届时的定位是：
   **原文持久化是必须的，索引快照只是可丢弃的启动加速缓存**。
2. ~~**无中文分词** —— 中文只能整段/单字命中，实际中文检索效果差。Analyzer 设计成接口以便后续替换。~~
   **已于 Phase 7 补齐，且原始判断被实测推翻**：标准分析器把汉字逐字索引，
   执行器对「一个词拆成多个 token」按短语求值（位置须连续），
   所以它对中文做的是逐字连续匹配，精度并不差。真正的差距在索引形态
   （词条数约两倍、高频功能字建立 df≈N 的 posting）。见 [docs/CHINESE.md](docs/CHINESE.md)。
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
| **M6.1** | 中文分词与检索 | ✅ 已完成（Phase 7） | 已完成 |
| **M6.2** | 文档持久化 | 📋 方案已定（Phase 8），尚未实施 | 按需 |
| **M6.3** | 前缀与模糊查询 | 待排期 | 按需 |

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
> - ✅ **`-race` 已可用**：原先 `CGO_ENABLED=0` 且宿主无 C 编译器。后经 MSYS2 安装
>   GCC 16.2.0（`C:\msys64\ucrt64\bin`，target `x86_64-w64-mingw32`）解决。
>   `scripts/check.ps1` 会自动探测该路径并设置 `CGO_ENABLED=1`，不依赖用户 PATH。
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
- [x] **遗留阻塞 2｜`-race` 不可用 —— 已解决**

      **根因**：`CGO_ENABLED=0` 且宿主无任何 C 编译器。竞态检测依赖 cgo，
      而 Go 在 Windows 上的 cgo 走 MinGW-w64 路线，必须是 GCC 风格的驱动。

      **解决**：经 MSYS2 安装 GCC 16.2.0，位于 `C:\msys64\ucrt64\bin`，
      target 为 `x86_64-w64-mingw32`（UCRT 变体，正是推荐组合）。

      **两个坑**：
      1. MSYS2 的工具链**不在 Windows PATH 上**（MSYS2 shell 自己会设），
         所以 cmd 里敲 `gcc` 无效。已把 `C:\msys64\ucrt64\bin` 追加到用户 PATH。
      2. **绝不能把 `C:\msys64\usr\bin` 加进 PATH**——那里有 MSYS2 自己的
         `link.exe` / `find.exe` / `sort.exe` / `sh.exe`，会遮蔽原生命令并
         破坏无关的构建。这是 MSYS2 最经典的陷阱。

      **加固**：`scripts/check.ps1` 会自动探测 MSYS2 的四个变体目录
      （ucrt64 / mingw64 / clang64 / mingw32），找到就临时加进 PATH 并设
      `CGO_ENABLED=1`。因此项目**不依赖用户 PATH 是否配置正确**，
      换机器或 PATH 未刷新时同样能跑。

      **反向验证**：仅"测试通过"不能证明竞态检测真的在工作。用一个
      **故意制造数据竞争**的临时探针（绕过锁读内部 map）验证，
      确认 `go test -race` 报出 `WARNING: DATA RACE` 且退出码非 0；
      删除探针后全绿。**工具本身必须先被验证过，才能用它来验证别的东西。**
- [ ] **遗留项｜PATH**：`D:\Program Files\Go\bin` 仍未加入用户 PATH。
      不影响开发与 CI：`check.ps1` 支持 `-GoExe` 传绝对路径，
      `Makefile` 也有可覆盖的 `GO` 变量。纯属便利性问题
      （顺带一提，MSYS2 的 `C:\msys64\ucrt64\bin` 已加入，见「遗留阻塞 2」）
- [x] **遗留项｜换行符**：`.gitattributes` 统一为 LF，并已执行
      `git add --renormalize .` 落地；后续提交不再出现 LF→CRLF 警告

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

### Phase 3 — 查询与打分 ✅ 已完成

> 分三次提交完成：`37217ad`（零拷贝 View + BM25 + TopK + 解析器）、
> `f1ee74e`（Searcher 执行器 + 短语游标归并）、
> `a2cc3c6`（有序切片归并 + 稠密词长切片，达成验收线）。
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
- [x] **query 与 index 共用同一 Analyzer**（`NewSearcher` 内部取 `ix.Analyzer()`，
      调用方无从传入别的分析器；并有 `TestSearchReusesIndexAnalyzer` 守着）
- [x] posting list 归并：**有序切片 + 双指针**（求交 / 求并 / 求差全部 O(n)，
      无哈希；这正是把 `map[DocID]float64` 累加器换掉之后的结果）
- [x] 短语查询：**必须在单个字段内**判定位置连续性——各字段位置都从 0 开始，
      跨字段拼位置会产生假阳性（`TestPhraseMustMatchWithinOneField`）
- [x] BM25：`IDF = ln(1 + (N-df+0.5)/(df+0.5))`，`tf` 饱和项，`k1=1.2 b=0.75`；
      **IDF 按字段计算**，否则短字段的高信息量会被长字段稀释
- [x] 跨字段检索：分别求值再把分数相加
- [x] Top-K 小顶堆（手写小顶堆，避开 `container/heap` 的 `any` 断言）
- [x] 高亮片段生成（独立成 `internal/highlight`，HTML 转义 + 窗口截断）
- [x] 单测：单 term、多 term OR/AND、短语误召拦截、否定词、空结果、
      排序确定性（同分按 DocID 升序）
- [x] 基准：`BenchmarkSearch*`（2 万篇）与 `BenchmarkSearch100k*`（验收规模）
- [x] **验收**：功能、正确性、**性能全部达成**，见下

> ### ✅ 性能：P99 < 20ms @ 10 万文档 —— 达成
>
> 语料是最坏情况（每个词条都命中全部 10 万篇，df = N）。
>
> | 基准（**10 万篇**） | 初版 | 最终 | 提升 |
> | --- | --- | --- | --- |
> | `Search100kTerm` | 27.77 ms | **3.48 ms** | 8.0× |
> | `Search100kPhrase` | 42.46 ms | **12.42 ms** | 3.4× |
> | `Search100kAnd` | 64.86 ms | **8.10 ms** | 8.0× |
> | `Search100kMustNot` | 58.07 ms | **4.86 ms** | 12.0× |
>
> `Search100kAnd` 的分配次数从 366 降到 **28**，`Search100kTerm` 从 152 降到 **14**。
>
> #### 三次优化，每次都由 pprof 指路
>
> **① 短语查询 48.35 → 10.40 ms。**
> profile 显示 `slices.BinarySearchFunc` 占 13.7%、`mapaccess2_faststr` 占 11.2%。
> 根因是「扫锚点词条 + 逐候选文档二分反查其余词条」。改成 `PostingCursor`
> 归并后每个 posting 只被顺序访问一次。
>
> **② 布尔组合 64.86 → 8.10 ms。**
> 根因是求值器拿 `map[DocID]float64` 当累加器，2 万条命中就是 4 万次 map 读写。
> 换成**按 DocID 有序的切片** + 双指针归并（求交/求并/求差全部 O(n)）。
> 这正是 PLAN 最初写的方案，被我用 map 走了捷径，现在还回来了。
>
> **③ 词长查询 27.77 → 3.48 ms。**
> 这条最有意思：2 万 → 10 万篇，数据量只涨 5 倍，耗时却涨了 12 倍——典型的
> 缓存失效特征。pprof 确认 `docLengthLocked` 占 **37.4%** 累计耗时，
> 其中约 31% 全花在 map 操作上：它对每条 posting 都要做**两次随机 map 查找**
> （`ix.docs[id]` 再 `doc.FieldLen[field]`）。
> 改为按 DocID 稠密下标的 `[]int32` 后，字段维度提到循环外取一次，
> 循环内只剩一次顺序友好的切片索引。
> **这里的教训是：不要从 2 万篇线性外推 10 万篇，超线性增长会骗人。**
>
> #### 两个测量方法的坑
>
> - 早先每个基准函数内部都重建 2 万篇语料，虽在 `ResetTimer` 之前，
>   但 CPU profile 覆盖**整个测试进程**，建索引的时间会混进 profile
>   并淹没真正的热点。改为包级 `sync.Once` 只建一次。
> - 后来直接在 10 万篇上测量，而不是从 2 万篇外推。

### Phase 4 — HTTP 服务 ✅ 已完成

> **与原计划的偏差**
> - **`POST` 与 `PUT` 语义分开**：原计划两者都写进同一张表，但没区分语义。
>   现在 `POST` 撞 ID 返回 **409**，`PUT` 才是覆盖（新建 201 / 覆盖 200）。
>   理由是「不小心覆盖了别人的文档」应当是一个明确的错误，而不是无声无息。
>   为此给索引补了 `Add`（仅新建，检查与写入在同一把写锁内，无 TOCTOU 窗口）。
> - **高亮单独成包**：`internal/highlight`。为了让高亮能定位原文区间，
>   给 `analyzer.Token` 加了 `Start`/`End` 字节偏移，并把分析器从
>   `[]rune` 转换改成**字节游标**——既省掉一次分配（实测快 5–11%），
>   又让偏移天然可得。
> - **`Engine.Search` 把高亮挪到 `View` 之外**：`View` 持有索引读锁，
>   在其中遍历整段原文会把写请求全堵住。
> - **`highlights` 里的 `<` 会被 JSON 转义成 `\u003c`**，这是 Go
>   `encoding/json` 的默认行为，**刻意保留**：字段内容由调用方提供，
>   不转义的话把响应嵌进 HTML 就是 XSS 入口。客户端 `JSON.parse` 后拿到的是正常的 `<em>`。

- [x] `net/http` + Go 1.22 `ServeMux` 路由注册（含 `{id}` 路径参数）
- [x] 中间件链：`recover` → 请求日志（`slog`）→ `MaxBytesReader` body 限制
- [x] 各端点 handler + 请求体校验（`json.Decoder` + `DisallowUnknownFields`）
- [x] 统一错误响应与状态码映射（`writeMappedError`）
- [x] 优雅关闭（`signal.NotifyContext` + `srv.Shutdown`，Phase 0 已就位）
- [x] `/healthz`、`/api/v1/stats`
- [x] `httptest` 端到端测试：写入→查询→更新→查询→删除→查不到
- [x] 请求体上限保护（`http.MaxBytesReader` → 413）
- [x] **验收**：`curl` 走通完整示例（真实进程冒烟测试通过）

> **两个值得记下来的坑**
>
> **① `%v` 包装错误会丢掉具体类型。**
> `decodeJSON` 最初写 `fmt.Errorf("%w: %v", errBadBody, err)`，
> 把 `*http.MaxBytesError` 格式化成字符串，于是超限请求体再也识别不出来，
> 被误报成 400 而不是 413。改用 `%w: %w` 才能同时被 `errors.Is` 与
> `errors.As` 认出来。**并且 `MaxBytesError` 的判断必须排在 `errBadBody` 之前**，
> 否则解码错误会先把「太大」误判成「格式不对」。
>
> **② `switch` 的分支不写 `return` 会继续往下走。**
> `writeMappedError` 的 switch 每个 case 都调用了 `writeError` 但没 `return`，
> 结果写完 404 之后又走到末尾写了第二次 500 —— 响应被写两遍。
> Go 的 `switch` 不像 C 需要 `break`，但**不会自动返回**。

### Phase 5 — 工程化与交付 ✅ 已完成

> **与原计划的偏差**
> - 压测没有用 `hey` / `wrk`，而是写成 **Go benchmark**（`internal/httpapi/load_test.go`）。
>   理由：外部压测工具测的是「某个二进制 + 某台机器」，换台机器就不可复现；
>   写成 benchmark 后，任何人都能用一条 `go test -bench` 复现同样的语料与同样的度量。
>   代价是它测的是**进程内 httptest 服务 + 真实 HTTP 客户端**，
>   不含跨机网络延迟——这一点在报告里写明了。
> - **`Stats()` 从 O(索引规模) 改成 O(1)**：压测发现 `/api/v1/stats`
>   均值 121ms、P99 401ms，根因是 `IndexBytes` 每次全量遍历索引。
>   这是个监控端点，110ms 的响应毫无可用性。改成增量记账后降到 0.24ms。

- [x] `README.md`：项目简介、快速开始、完整 API 文档、curl 示例、配置表、故障排查
- [x] demo 命令：`tshd -import ./testdata/corpus`（附带 6 篇示例语料）
- [x] 语料合成：`tshd -generate 100000`，固定种子可复现，词频刻意做成不均匀
- [x] `deploy/Dockerfile`：多阶段 + `CGO_ENABLED=0` → `scratch`，**实测 7.03 MB**
- [x] `.dockerignore`：否则构建上下文会把 `.gocache` 几百 MB 一起打包
- [x] CI：`.github/workflows/ci.yml`，`gofmt` + `vet` + `test -race` + 覆盖率 + 四目标交叉编译矩阵
- [x] 压测报告：`docs/PERFORMANCE.md`（QPS、P95/P99/最大值、分配量、已知限制）
- [x] **验收**：`go build` / `go vet` / `go test -race` 在 Linux 与 Windows 下均通过

> **验证做到了什么程度（如实记录）**
>
> | 项 | 状态 |
> | --- | --- |
> | linux/amd64、linux/arm64 交叉编译 | ✅ 实测通过 |
> | 产物是**静态链接**的 ELF（`scratch` 的前提） | ✅ 实测（无 `ld-linux`） |
> | `scratch` 镜像可运行、`/healthz` 与 `/api/v1/stats` 有响应 | ✅ 实测（**7.03 MB**，以 nobody 运行） |
> | 完整 `docker build`（含 `golang:1.27-alpine` 构建阶段） | ❌ **未验证**——本机无法访问 docker.io，拉不到基础镜像 |
>
> 因此 Dockerfile 的**运行阶段**经过真实验证，**构建阶段**只在逻辑上成立。
> 另：已刻意去掉 `# syntax=docker/dockerfile:1`——那条指令会强制去
> docker.io 拉一个 BuildKit 前端镜像，而本文件并未用到需要它的特性。

> **一个值得记下的测量教训**：验证 ELF 魔数时，我拿 `bytes[1]` 去比 `'L'`、
> `bytes[2]` 去比 `'F'`，整体错位了一位，于是得出了「不是 ELF」的错误结论。
> 二进制本身一直是好的。**又一次是尺子错了，不是被测对象错了。**
> 压测的 P50 也有同类问题：本机 harness 出现了亚微秒伪影，
> 约一半样本读数为 0，与 `ns/op` 均值自相矛盾，因此报告里明确弃用 P50。

### Phase 6 — 可选演进（按需排期，不在 v1 承诺内）
- [ ] 快照持久化：`gob`/自定义二进制序列化 + 启动加载 + 定期 `Save`
- [x] **中文分词**：`gse` + 词典分词 + 搜索模式子词扩展，接入 `Analyzer` 接口
      —— 已于 v1 之后完成，见下方「Phase 7」
- [ ] 词干还原（Porter Stemmer，纯 Go 移植）
- [ ] 前缀查询（`hel*`）与编辑距离模糊查询（Levenshtein ≤ 2）
- [ ] 索引分片：按 DocID 取模分 N 片，各片独立锁提升写并发
- [ ] 段合并（LSM 思路）+ 增量快照
- [ ] 字段权重（`title^3 body`）与自定义 boost
- [ ] 向量检索接入

---

### Phase 7 — 中文分词与检索 ✅ 已完成

> **这一阶段推翻了本项目的一处原始判断。**
>
> 选型时把中文分词的结论写成了「中文只能整段/单字命中，实际中文检索效果差」。
> 实测证明**后半句是错的**：标准分析器把汉字逐字索引，而执行器对
> 「一个词被拆成多个 token」的情况**按短语求值**（要求位置连续），
> 因此标准分析器对中文做的其实是逐字连续匹配，等价于子串匹配，精度并不差。
>
> 两个分析器的**真正差别在索引形态**：同一篇文档，逐字索引 69 个词条／4341 字节，
> 词级索引 34 个词条／2545 字节。更关键的是「的」「是」「在」这类高频功能字——
> 逐字索引会为它们建立 df 接近 N 的 posting 列表，对打分贡献接近 0，
> 却让每次查询都要扫描它。
>
> 中间还有两次判断被实测推翻，一并记在 [docs/CHINESE.md](docs/CHINESE.md) 的踩坑记录里。

- [x] 接入 `github.com/go-ego/gse` v1.1.0（纯 Go，仅间接依赖 `vcaesar/cedar`）
- [x] 验证 `CGO_ENABLED=0 GOOS=linux` 构建通过——**静态交付形态未受影响**
- [x] `internal/analyzer/chinese.go`：汉字段走词典分词，其余整块委托给标准规则
- [x] 位置语义与英文侧一致：每个词占一个位置，被过滤的词同样占位
- [x] 子词与父词共享位置（Lucene `SynonymGraphFilter` 语义），保证短语查询对齐
- [x] 词典用 `zh_s`（仅简体）：732 ms / 108 MB，比 `zh` 省一半
- [x] 自定义词典：`-dict` 支持行注释与 gse 的「词 词频 词性」格式
- [x] 配置接入：`-analyzer` / `-dict` / `-no-sub-words`，含非法值校验
- [x] 中文端到端：`pkg/tsh` 与 `cmd/tshd` 两层测试 + 真实二进制的 HTTP 冒烟验证
- [x] 实测交付代价：二进制 6.71 → **38.47 MB**，内存基线 **108 MB**，加载 **732 ms**
- [x] **顺带修掉一个真实缺陷**：`-max-doc-fields` / `-max-doc-tokens` /
      `-max-query-terms` 三个参数一直被解析、校验、写进文档，**却从未接进引擎**。
      现在有测试钉住它们真的生效。
- [x] **顺带修掉门禁的两个问题**：`gofmt -l .` 会扫进工作区内的模块缓存
      （`gofmt` 不跳过点开头的目录），把第三方依赖的格式报成我们的；
      `check.ps1` 现在用 `go list` 取本项目自己的包目录。

> **已知取舍（而非缺陷）**
>
> - 子词与父词共享位置，使「子词 + 后续词」的短语查询可能命中原文中
>   并不相邻的组合。这是换取「大学」能命中「大学生」的代价，
>   可用 `-no-sub-words` 关掉。
> - 高亮一个多词中文查询会产出相邻的多个 `<em>`（如
>   `<em>倒排</em><em>索引</em>`），视觉上与一个 `<em>` 等价。
> - 内嵌词典使二进制增大 30 MB；`-tags ne` 可降到 7.61 MB，
>   但需要改为外部词典加载，当前代码未走这条路。

---

### Phase 8 — 文档持久化 📋 待实施（本阶段只有方案，尚未写代码）

> 当前状态：纯内存索引，**重启即丢失一切**，靠 `-import` 或 API 重新灌数据。
> 这一步要回答的是：「服务重启后，数据还在吗？」

#### 8.1 先定架构判断：原文是权威数据，索引是派生数据

只持久化索引看起来最省事，实则有三个硬伤：

- 换分析器（`-analyzer`）后旧索引作废，而原文已经没了，**无法重建**；
- 索引格式一旦演进，老快照就读不出来，**数据被格式锁死**；
- 索引实现有 bug 导致损坏，等同于**数据永久丢失**。

所以最低限度必须持久化**文档原文**，索引永远可以从原文重建。
在原文之上，「分析结果」或「索引结构」只能作为**可随时丢弃的启动加速缓存**。

这一条决定了后面所有设计：**原文格式要稳，索引缓存可以随时重建。**
反过来说，任何让索引缓存变成"不可丢弃"的设计都是错的。

#### 8.2 分层推进：先做最笨的，再逐层优化

| 层 | 内容 | 恢复代价 | 优先级 |
| --- | --- | --- | --- |
| **L1** | 追加式原文日志，启动全量重放 | 重新分析 + 建索引 | **P0，MVP** |
| **L2** | 定期快照 + 截断日志 | 同上，但日志更短、恢复更快 | P1 |
| **L3** | 持久化 **token 流**（分析结果） | 跳过分析，只建索引 | P2 |
| **L4** | 持久化 posting list（索引结构） | 直接加载 | **暂不做** |

L4 与索引内部结构强耦合，**收益（省一次建索引）远小于代价（格式锁死）**。
除非将来实测证明 L3 仍然太慢，否则不做。

L3 是性价比最高的一档：token 流是**分析器的输出**，不是索引的结构，
既跳过了最贵的一步，又保住了格式解耦。分析器换了只需重新分析。

> 参考基线：10 万篇语料建索引实测约 **15.4 秒**。
> 中文场景下这个数字还会明显变大（分词远慢于 rune 扫描）。
> 这就是 L3 存在的意义——它是唯一能真正缩短启动时间的层次。

#### 8.3 一致性：快照不必是「某个时点的快照」

直觉上快照要冻结整个索引，代价是长时间持锁。10 万篇序列化要几百毫秒到数秒，
**这段期间服务等于停写**，不可接受。

配合追加日志就不需要冻结：

1. 记下快照开始时的日志位置 `L`；
2. 遍历导出（**不持写锁**，允许并发写）；
3. 完成后记下位置 `L'`。

恢复时「加载快照 + 重放 `L'` 之后的日志」。快照里的每条记录
要么是修改前、要么是修改后的**完整版本**，重放会把不一致的部分覆盖掉，
最终状态仍然一致。**所以快照本身允许不一致，只要日志是完整的。**

这条成立有两个**必须验证的前提**（见 8.5），不成立则方案要改。

**前提 1 已读源码验证通过**（见 8.5）。

> ⚠️ **一个需要注意的实现细节**：`View.Document` 返回的是 `doc.Clone()`，
> 每次调用都会新建并填充一个 `map`。遍历 10 万篇做快照就是 **10 万次 map 分配**，
> 这条路径的耗时可能明显高于"读出字节"本身。
> 实施时要么实测这条路径的开销，要么给快照加一个"持读锁直接序列化"的专用入口——
> 但它会长时间阻塞写，需要权衡。**先测量再决定。**

#### 8.4 格式与元数据

| 内容 | 格式 | 理由 |
| --- | --- | --- |
| `MANIFEST` / 元数据 | **JSON** | 可读、可手工检查、演进不敏感；放版本号、分析器指纹、快照编号、各文件校验和 |
| 文档记录 / token 流 | **自定义二进制** | `gob` 体积大、速度慢、跨版本脆弱，且它的类型自描述对完全自控的格式是纯开销。只需 magic + 版本号 + 长度前缀 + CRC32 + 变长整数 |

目录布局（草案）：

```
data/
  MANIFEST              # 当前快照编号、格式版本、分析器指纹
  snapshot-000042.dat   # 原文快照（L2 起）
  wal/000042.wal        # 快照之后的增量
  tmp/                  # 写临时文件用（原子提交的中间态）
```

**不引入嵌入式 KV**（BoltDB / Badger）。它们会把整个数据层换掉，项目性质就变了。
这与引入 gse 不同——gse 只影响一个**可选**分析器，不启用时那部分代码路径根本不会走到。

#### 8.5 待验证的前提：先验证，再动手

这几条如果不成立，上面的方案要改。**在写任何代码之前先逐条实测**：

- [x] ~~**`View.Document` 返回的 `Fields` 是否是拷贝？**~~
      **已验证通过**（读源码即可确认，无需写代码）：
      - `prepare` 里 `p.fields[name] = fields[name]` 写入的是**新建的 map**，
        不是调用方传进来的那个（`index.go:175`、`index.go:194`）；
      - `Upsert` 走 `removeLocked(old)` + `insertLocked(new)`，
        **整体替换 `*Document`**，从不就地修改（`index.go:248-263`）；
      - `View.Document` 返回 `doc.Clone()`，对外一律给副本（`types.go:42`）。

      字符串在 Go 里不可变，拷贝字符串头就足够。
      **结论：并发读永远拿到完整的某个版本，8.3 的免冻结方案成立。**
- [ ] **Windows 上 `os.Rename` 覆盖已存在文件是否原子？会不会被「文件被占用」挡住？**
      Go 在 Windows 上用 `MoveFileEx(MOVEFILE_REPLACE_EXISTING)`，
      语义弱于 POSIX 的 `rename(2)`。本项目在 Windows 上开发，必须实测。
- [ ] **Windows 上 `file.Sync()` 是否真的落到磁盘？**
- [ ] **日志的并发追加是否可靠？** 追加必须在索引写锁内完成才能保证
      「日志顺序 = 索引生效顺序」，需要确认这个约束能落地。
- [ ] **10 万篇的原文快照体积与耗时**——需要实测数字才能定快照触发策略。
- [ ] **`-sync=always` 的写吞吐损失**——决定默认同步策略。

#### 8.6 待办清单

**8.6.1 MVP：追加式原文日志（P0）**

- [ ] 定义文档记录格式：操作类型（upsert / delete）+ 外部 ID + 字段 + CRC32
- [ ] WAL 追加放在索引写锁内，保证日志顺序与索引生效顺序一致
- [ ] `Engine.Open(dataDir)` / `Engine.Close()` 生命周期
- [ ] 启动重放日志，复用现有 `Add` / `Delete` 路径（不另写一套写入逻辑）
- [ ] 新增 `-data-dir` 配置；空值等价于「不持久化」，行为与现在完全一致
- [ ] 优雅关闭时 flush 并关闭文件
- [ ] 厘清与 `-import` / `-generate` 的关系：它们是**额外灌数据**，不是恢复路径

**8.6.2 崩溃一致性（P1）**

- [ ] 同步策略 `-sync=always|batch|never`，默认 `batch`
- [ ] 批量 fsync（默认 100 ms 窗口）
- [ ] **日志尾部截断**：崩溃时最后一条可能只写了一半，
      重放必须能识别并丢弃，而不是当成损坏数据
- [ ] 校验和校验；损坏时截断到最后一条完整记录并**告警**，不静默吞掉

**8.6.3 快照与压缩（P1）**

- [ ] 快照触发：日志条数 / 字节数 / 手工触发
- [ ] 快照写入：临时文件 → fsync → 原子 rename
- [ ] `MANIFEST` 原子切换
- [ ] 快照完成后截断日志
- [ ] 保留最近 N 份快照，清理更早的

**8.6.4 启动加速（P2）**

- [ ] 持久化 token 流（分析结果），随附分析器指纹
- [ ] 加载时校验指纹；不匹配则**回退到从原文重新分析**，而不是报错
- [ ] 实测对比：从原文重建 vs 从 token 流加载（10 万篇，英文与中文各一组）

**8.6.5 测试**

- [ ] **重启一致性**：重启前后同一组查询返回完全相同的命中与顺序
- [ ] **崩溃恢复**：写入过程中 kill 进程，重启后不丢已确认的写（`-sync=always`）
- [ ] **损坏注入**：翻转日志/快照中的若干字节，必须被校验和发现
- [ ] **版本不匹配**：伪造一个高版本号，必须拒绝加载而不是崩溃
- [ ] **分析器指纹不匹配**：换 `-analyzer` 后必须重建，而不是加载出错误索引
- [ ] 边界：空索引、单文档、超大文档、删除后重启、只删不增
- [ ] 10 万篇的恢复时间与磁盘占用实测，写进 `docs/PERFORMANCE.md`
- [ ] `make check`（含 `-race`）全绿

**8.6.6 运维与文档**

- [ ] `/api/v1/stats` 增加磁盘占用与日志长度
- [ ] 磁盘满时的行为：拒绝写 + 明确错误，**不静默丢数据**
- [ ] README：持久化配置、恢复语义、备份方式
- [ ] `docs/PERSISTENCE.md`：格式规范，供将来写恢复/检查工具

#### 8.7 风险

| 风险 | 应对 |
| --- | --- |
| 快照期间的锁竞争 | 8.3 的日志位置对齐方案，不做长期持锁 |
| 格式演进破坏老数据 | 版本号 + 显式迁移或拒绝加载；**原文格式设计得足够稳定**，索引缓存随时可丢 |
| Windows 的 rename / fsync 语义弱于 POSIX | 8.5 先实测；必要时改用「写新文件 + MANIFEST 指向」而非覆盖 |
| 磁盘满 | 写前检查 + 明确报错；绝不静默丢弃 |
| 持久化拖慢写入 | 默认 `batch`；实测 `always` 的吞吐损失并写进文档 |
| 日志无限增长 | 快照后截断；设日志上限，超限强制快照 |

#### 8.8 明确的非目标

- 不做多进程 / 分布式 / 跨机复制
- 不做真正的 MVCC 或事务
- 不做在线备份 / 增量备份
- 不引入嵌入式 KV

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
