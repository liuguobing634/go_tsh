# 项目简报：go_tsh

> 第一期技术纪录片素材 · 阶段一产物
> 本文件描述**代码库当前的真实状态**，不含规划中的功能、不含未验证的性能数字。
> 所有结论按三级标注，详见文末「证据分级约定」。

---

## 1. 项目定位

`go_tsh` 是一个用 Go 从零手写核心机制的全文搜索服务：**单二进制、内存倒排索引、HTTP JSON API**，
同时以 `pkg/tsh` 的形式可被嵌入到其它 Go 进程里运行。

模块路径与版本（`go.mod` 全文，共 7 行）：

```
module github.com/liuguobing/go_tsh

go 1.27

require github.com/go-ego/gse v1.1.0

require github.com/vcaesar/cedar v0.50.0 // indirect
```

【已验证】本机工具链：`go version go1.27.1 windows/amd64`（`go version` 实测）。
`go.mod` 声明的语言版本是 `go 1.27`。

### 一句话定位（可直接用作纪录片开场）

> 一个把「倒排索引、BM25、短语查询、布尔求值」全部手写出来的 Go 搜索服务；
> 它只在中文分词这一处引入了第三方库，并且把「为什么只引这一个」的代价量化写进了仓库。

---

## 2. 技术栈

| 维度 | 实际选择 | 证据 |
| --- | --- | --- |
| 语言 | Go 1.27.1 | 【已验证】`go version` |
| HTTP | 标准库 `net/http`（1.22 起的方法+路径模式路由） | 【已验证】`pkg/httpapi/server.go` 使用 `mux.HandleFunc("GET /healthz", …)` 风格 |
| 日志 | 标准库 `log/slog`，JSON handler | 【已验证】`cmd/tshd/main.go` `slog.NewJSONHandler` |
| JSON | 标准库 `encoding/json` | 【已验证】`pkg/httpapi/documents.go` 使用 `json.NewDecoder`（`UseNumber` + `DisallowUnknownFields`） |
| 配置 | 标准库 `flag` + 环境变量 | 【已验证】`internal/config/config.go` |
| 并发 | `sync.RWMutex` 保护整份索引 | 【已验证】`internal/index/index.go` |
| 相关性 | 自实现 Okapi BM25（k1=1.2，b=0.75） | 【已验证】`internal/scoring/bm25.go` |
| Top-K | 自实现固定容量小顶堆 | 【已验证】`internal/scoring/topk.go` |
| 中文分词 | `github.com/go-ego/gse` v1.1.0（内嵌 `zh_s` 词典） | 【已验证】`internal/analyzer/chinese.go` |
| 测试 | 标准库 `testing` + `httptest` + `testing.B` | 【已验证】37 个 `*_test.go` |
| 数据库/外部服务 | **无** | 【已验证】无任何驱动依赖 |

---

## 3. 第三方依赖盘点

### 3.1 直接依赖（`go.mod` 里显式 require）

| 模块 | 版本 | 引入位置 | 说明 |
| --- | --- | --- | --- |
| `github.com/go-ego/gse` | v1.1.0 | `internal/analyzer/chinese.go` | 唯一的**真实**运行时依赖；纯 Go，内嵌简体中文词典 |
| `github.com/vcaesar/cedar` | v0.50.0 | `go.mod` 中标记为 `// indirect` | gse 的传递依赖（双数组 trie） |

### 3.2 传递依赖（`go list -m all` 的完整模块图）

【已验证】`go list -m all` 输出恰好 4 行：

```
github.com/liuguobing/go_tsh      <- 主模块
github.com/go-ego/gse   v1.1.0    <- 直接依赖
github.com/vcaesar/cedar v0.50.0  <- 间接依赖
github.com/vcaesar/tt   v0.40.0   <- 出现在 go.sum 中
```

`github.com/vcaesar/tt v0.40.0` 的定位需要精确表述：

- 【已验证】它出现在 `go.sum` 里（`go.sum` 第 1–2 行；`vcaesar/cedar` 在第 3–4 行），且被完整下载进本地模块缓存
  （`go mod download` 的产物目录中可见 `vcaesar/tt@v0.40.0`）。
- 【已验证】它**不在** `go.mod` 的 require 列表中，只约束校验和。
- 【风险】从 `go.sum` 留下校验和与缓存中已解包这一点推断，它属于构建/测试期依赖
  （典型情况：`cedar` 自身测试用到的断言库）。本次分析**没有**验证它的具体导入路径，
  因为分析环境无外网、且它不参与 `go build ./cmd/tshd` 的产物。

### 3.3 「零依赖」这一目标的真实状态

项目 README 与 `PLAN.md` 都把最初的「零第三方依赖」目标写成了**修订后的准确表述**
（README 第 42–57 行「依赖说明（相对原计划的修订）」）：

> 现在准确的表述是：**核心检索路径（索引、查询、打分、HTTP）零第三方依赖，
> 只有中文分析器引入 gse**。不启用 `-analyzer chinese` 时，gse 的代码路径不会被走到。

【已验证】这个表述与代码一致：`import "github.com/go-ego/gse"` 只出现在
`internal/analyzer/chinese.go`；`internal/index`、`internal/query`、`internal/scoring`、
`pkg/httpapi` 的 import 列表里没有第三方包。

【风险】「不启用 `-analyzer chinese` 时代码路径不会被走到」说的是**运行期**。
但 `go build` 仍然会编译 `internal/analyzer/chinese.go`，因此 gse 与内嵌词典
**始终**进入二进制。这正是「不启用中文分词时二进制也要 42 MB」的原因
（见第 6 节实测体积）。

---

## 4. 功能范围

### 4.1 已实现（本次分析逐项确认到文件/类型/方法）

| 功能 | 核心文件 | 关键类型 / 方法 | 测试文件 |
| --- | --- | --- | --- |
| 倒排索引 | `internal/index/index.go`、`types.go`、`view.go` | `InvertedIndex`、`PostingList`、`Posting`、`View`、`PostingCursor` | `index_test.go`(19)、`view_test.go`(8) |
| BM25 相关性评分 | `internal/scoring/bm25.go` | `BM25`、`DefaultBM25`、`IDF`、`Score` | `scoring_test.go`(12) |
| Top-K 选择 | `internal/scoring/topk.go` | `TopK`、`Push`、`Result` | `scoring_test.go` |
| 布尔查询 | `internal/query/parser.go`、`searcher.go`、`ast.go` | `Bool`、`parseOr/parseAnd/parseClause`、`evalBool`、`mergeUnion/mergeIntersect/subtract/addExisting` | `parser_test.go`(8)、`searcher_test.go`(23) |
| 短语查询 | `internal/query/searcher.go`、`index/view.go` | `Phrase`、`evalPhraseTokens`、`countPhrase`、`phraseFits`、`containsPosition` | `searcher_test.go` |
| 内存文档存储 | `internal/index/types.go`、`index.go` | `Document`、`Clone`、`Get`、`Add/Update/Upsert/Delete` | `index_test.go` |
| 中文分词（gse） | `internal/analyzer/chinese.go` | `ChineseAnalyzer`、`NewChinese`、`analyzeHan`、`appendSubWords` | `chinese_test.go`(10 + 3 benchmark) |
| 文本分析（默认） | `internal/analyzer/standard.go`、`stopwords.go` | `StandardAnalyzer`、`analyzeInto`、`keep` | `standard_test.go`(9 + 3 benchmark) |

括号内数字为【已验证】的 `func Test` 计数。

### 4.2 代码库实际包含、但超出任务描述范围的部分

任务描述只列出了 6 项功能。代码库实际还实现了以下内容，**它们同样是可验证的真实素材**，
因此记录在此以免纪录片把它们当成不存在：

- **类型化字段**：`text` / `keyword` / `number` / `date` 四种字段类型 + 数值列式存储
  （`internal/index/field.go`、`columns.go`、`value.go`）
- **范围查询与比较运算符**：`price:[10 TO 100]`、`price:>=10`（`internal/query/parser.go`、`searcher.go`）
- **排序**：`sort=price:desc`，多键、空值置后（`internal/query/sort.go`、`sort_test.go`(9)）
- **高亮**：HTML 转义 + 窗口截断（`internal/highlight/highlight.go`）
- **持久化**：追加式原文日志 WAL + 启动重放 + 压实（`internal/wal/`、`pkg/tsh/persist.go`）
- **表（多命名空间）**：每表独立 schema / 文档 / 日志（`pkg/tsh/table.go`、`engine.go`）
- **HTTP API**：文档 CRUD、检索、统计、健康检查、表管理（`pkg/httpapi/`）
- **语料导入与合成**：`-import` 目录、`-generate N` 合成语料（`internal/corpus/`）
- **对外 API 编译门禁**：`apicheck/`（独立 module + `replace` 指回本仓库）

【已验证】以上每一处都在文件系统中存在，并有对应测试；数字为实测的测试函数计数。

### 4.3 明确不做（README「不适合」表格的自我界定）

【已验证】README 第 84–95 行「不适合」表格明确列出不做的项：分布式/副本/聚合分面、
十亿级数据、高可用、复杂相关性调优、模糊匹配与拼写纠错、多值字段、地理检索。
`PLAN.md` 第 55 行起的「v1 不做什么（Out of Scope）」与此一致。

对纪录片的意义：**项目自己划出了安全边界**，这让「它是什么」比「它有多强」更好讲。

---

## 5. 设计约束（从代码与文档中提取的硬性约定）

这些约束在注释里被反复强调，且大部分在代码结构上被**强制**而非仅靠约定：

1. **索引侧与查询侧必须共用同一个 Analyzer。**
   【已验证】结构上强制：`Searcher` 不接受 analyzer 参数，只从 `ix.Analyzer()` 取
   （`internal/query/searcher.go`）；`Analyzer()` 只读、无 setter。
   【风险】这个「不一致就会搜不到」的失败模式**没有测试**——
   `searcher_test.go:TestSearchReusesIndexAnalyzer` 只证明了复用成立，没有构造错配。

2. **倒排索引的 posting 列表按 DocID 严格升序。**
   【已验证】`insertFieldLocked` 只做 append（DocID 单调递增），删除是原地摘除；
   本次分析对真实索引断言了「Postings 按 DocID 升序」与「TF == len(Positions) 且 Positions 严格递增」，均为 true。

3. **被过滤的词条仍然占用一个位置。**
   【已验证】标准分析器的 `flush` 在 `keep` 判断之外执行 `pos++`；
   实测 `"quick the brown"` → `quick@0 brown@2`，`"quick brown"` → `quick@0 brown@1`。
   这是短语查询正确性的基础。

4. **短语匹配比较的是「与查询侧相同的位置差」，不是位置差为 1。**
   【已验证】实测：文档 `"quick the brown fox"` 能命中 `"quick the brown"` 但不命中 `"quick brown"`；
   文档 `"quick brown fox"` 反之。这正是 `stopwords` 占用位置带来的可观测后果。

5. **索引是派生数据，原文才是权威数据。**
   【已验证】持久化只写原文日志（`Change.Fields`），重启时重新分析重建索引；
   换分析器不需要迁移数据。

6. **核心检索路径零第三方依赖。**
   【已验证】见 3.3。

---

## 6. 规模与体积（本次实测）

### 6.1 代码规模

【已验证】排除 `.gocache/` 与临时目录后统计（行数含空行，与编辑器/`wc -l` 口径一致）：

| 类别 | 文件数 | 行数 |
| --- | --- | --- |
| 源码（非 `_test.go`） | 45 | 10,515 |
| 测试（`*_test.go`） | 37 | 12,530 |
| `func Test` 总数 | — | **346** |

按包统计的**源码**行数（含 `apicheck/`、`examples/`）：

| 包 | 文件 | 行数 |
| --- | --- | --- |
| `pkg/tsh` | 10 | 2,330 |
| `internal/query` | 4 | 1,820 |
| `internal/index` | 6 | 1,687 |
| `pkg/httpapi` | 7 | 1,098 |
| `internal/wal` | 2 | 1,099 |
| `internal/analyzer` | 4 | 588 |
| `internal/config` | 1 | 415 |
| `cmd/tshd` | 3 | 343 |
| `apicheck` | 1 | 317 |
| `internal/corpus` | 2 | 239 |
| `internal/highlight` | 1 | 182 |
| `internal/scoring` | 2 | 167 |
| `examples/blog` | 1 | 118 |
| `internal/tablename` | 1 | 98 |

测试代码按包分布（用于对纪录片说明「测试往哪里投」）：

| 包 | 文件 | 测试行数 |
| --- | --- | --- |
| `pkg/tsh` | 9 | 3,237 |
| `pkg/httpapi` | 7 | 2,026 |
| `internal/query` | 4 | 1,715 |
| `internal/index` | 4 | 1,593 |
| `internal/wal` | 3 | 1,264 |
| `internal/analyzer` | 2 | 753 |
| `cmd/tshd` | 3 | 678 |
| `internal/config` | 1 | 461 |
| `internal/scoring` | 1 | 239 |
| `internal/highlight` | 1 | 224 |
| `internal/corpus` | 1 | 211 |
| `internal/tablename` | 1 | 129 |

纪录片可用的对比数字：**测试代码行数（12,530）多于源码行数（10,515）**，
即测试/源码行比约 **1.19 : 1**（测试多出约 **19.3%**）。

> ⚠️ 统计口径提醒：PowerShell 的 `Measure-Object -Line` **会跳过空行**，
> 用它统计 `internal/scoring/bm25.go` 会得到 28 行而不是真实的 49 行。
> 本节数字用「按换行符切分」的口径，可用 `([System.IO.File]::ReadAllText($p) -split "`n").Count - 1` 复核。

### 6.2 测试覆盖率

【已验证】`go test -cover ./...` 实测：

| 包 | 覆盖率 |
| --- | --- |
| `internal/scoring` | 100.0% |
| `internal/tablename` | 100.0% |
| `internal/config` | 97.6% |
| `internal/analyzer` | 95.9% |
| `internal/corpus` | 92.9% |
| `internal/query` | 92.3% |
| `internal/highlight` | 93.2% |
| `internal/index` | 90.5% |
| `pkg/httpapi` | 88.9% |
| `internal/wal` | 87.0% |
| `pkg/tsh` | 83.2% |
| `cmd/tshd` | 43.5% |
| `examples/blog` | 0.0%（示例程序，无测试） |

### 6.3 二进制体积

【已验证】本次重新构建实测（不是引用文档数字）：

| 构建方式 | 字节 | MiB |
| --- | --- | --- |
| `go build ./cmd/tshd`（默认） | 44,421,632 | 42.36 |
| `go build -ldflags "-s -w"` | 40,845,824 | 38.95 |
| `go build -trimpath` | 44,399,104 | 42.34 |
| `CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w"` | 40,599,712 | 38.72 |

【已验证】这印证了 README 的体积说明（默认约 42.3 MB、`-s -w` 后约 38.9 MB），
且 Linux 静态构建（`CGO_ENABLED=0`）成功，交付形态未受 gse 影响。

【风险】README 另有一句「不启用中文分词时约 6.7 MB」。本次分析**无法**复现该数字：
`internal/analyzer/chinese.go` 无条件 import gse，二进制里始终包含内嵌词典。
该数字应来自引入 gse 之前的 commit，若在纪录片中引用需明确标注为历史数据。
**建议不引用，或注明其时间点。**

---

## 7. 质量门禁的真实状态

【已验证】仓库自带门禁 `scripts/check.ps1`，本次在**干净的 master 工作树**上实跑通过：

```
==> gofmt -l (formatting)
==> go vet
==> apicheck (external consumer)
==> go test -race
ok  	github.com/liuguobing/go_tsh/cmd/tshd	24.381s
...
ok  	github.com/liuguobing/go_tsh/pkg/tsh	(cached)
check passed
```

退出码 0（`CHECK_EXIT=0`）。

【已验证】门禁含四项：`gofmt -l`、`go vet`、`apicheck`（跨 module 编译门禁）、`go test -race`。
另外单独实测 `gofmt -l` 对全部项目包输出为空、`go vet ./...` 退出码 0。

【风险 / 环境事实】门禁默认走 `proxy.golang.org`，在本机**会失败**：
`dial tcp 142.250.192.145:443: connectex: A connection attempt failed`。
本地模块缓存（`.gocache/mod`，17 MB 的 `gse` zip 已完整存在）是齐备的，
把模块代理设为离线即可通过。`scripts/check.ps1` 自己提供了
`-GoProxy` 开关并注释说明这一点（`scripts/check.ps1` 第 157–166 行）。
**这不是代码缺陷，是网络环境事实**，但纪录片若展示门禁画面必须交代，
否则观众会以为门禁是坏的。

---

## 8. 仓库卫生观察（与代码质量无关，但影响素材采集）

【已验证】

| 观察 | 事实 |
| --- | --- |
| 工作树 | `git status --porcelain` 输出为空（干净） |
| 未跟踪且被 ignore 的残留物 | 仓库根目录有 `debug.log`、`query.test.exe`；`bin/` 下有 4 个约 40 MB 的可执行文件；`apicheck/` 下有 `apicheck.exe` |
| git 中的二进制 | 【已验证】`git ls-files` 中**没有**任何 `.exe`/`.log`/`bin/` 文件；只有 `apicheck/` 的源码与 go.mod/go.sum |
| `debug.log` 内容 | 【已验证】是**搜狗输入法**的日志（`sgime_skin.v3.filemap.sogouime`），与项目无关 |
| `.gitignore` | 【已验证】正确忽略了 `*.exe`、`*.log`、`/bin/`、`.gocache/` |

对纪录片的意义：`debug.log` 与 `query.test.exe` 若出现在画面里会被观众误认成项目产物，
**拍摄前建议清理或避免入镜**。这不是代码问题。

---

## 9. 证据分级约定（全系列文档统一）

本次素材为每条结论标注三级，请人工审核时逐条核对：

| 标注 | 含义 | 判定标准 |
| --- | --- | --- |
| 【已验证】 | 事实 | 本次分析中通过**实跑命令/测试**得到输出，或由代码原文直接读出且可复核 |
| 【风险】 | 有依据但未验证 | 由代码或已证事实推理得出，但本次分析**没有**构造实验证明其在真实使用中发生 |
| 【推测】 | 尚无证据 | 仅为主观判断或经验类比，**不得**在纪录片中作为结论陈述 |

**不得**写进视频的：任何本次未实测的性能数字（除引用 `docs/PERFORMANCE.md` 且同时标注
「该项目自测数据，本次未复现」）、任何开发过程细节（见 `development-timeline.md`）、
任何「AI 发现了某个 bug」的叙述（见同文件第 5 节）。

---

## 10. 供纪录片使用的「一句话事实」清单

以下每条都可用画面直接验证：

1. `go.mod` 只有 **7 行**，`require` 只有 **1 个真实运行时依赖**。
2. 索引、查询、打分、HTTP 全部零第三方依赖。
3. BM25 的完整实现是 **49 行**（`internal/scoring/bm25.go`）。
4. 手写小顶堆 Top-K 是 **118 行**（`internal/scoring/topk.go`）。
5. 测试函数 346 个，测试代码行数比源码多约 **19.3%**。
6. `check.ps1` 门禁四项全过（含 `-race`）。
7. 内置中文词典代价：二进制约 +32 MB、单表内存基线约 +109 MB（本机实测，见 `code-review.md` 案例 5）。
8. 短语查询的判定依据是位置差的**相对值**，有可复现实验（见 `demo-cases.md`）。
