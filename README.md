# go_tsh

用 Go 从零实现的全文搜索服务：**单二进制、手写内存倒排索引、HTTP JSON API**。

## ⚠️ 这个项目没有在生产环境验证过

**它是为学习与实验写的。** 功能是完整的、`-race` 全绿、有实测数据——
但这些都不等于「可以放进生产」。下面这些**具体**的东西没做过：

| 没做过 | 意味着什么 |
| --- | --- |
| **任何生产部署** | 没有真实流量、真实数据形态、真实故障检验过它 |
| **长时间稳定性验证** | 所有压测都是短时 benchmark，没跑过几天几夜的 soak test |
| **真正的进程强杀测试** | 崩溃恢复是用**构造出来的**场景验的（残留草稿、写一半）。`os.Rename` 的原子性依赖实测过的 OS 行为，但**没有在改名那一瞬间真杀过进程** |
| **多副本 / 高可用** | 单进程。进程挂了服务就没了，没有故障转移 |
| **鉴权 / 限流 / 多租户** | HTTP 层完全没有（只有请求体上限与超时） |
| **监控接入** | 只有 `/healthz` 与 `/api/v1/stats`，没有 metrics 端点 |

另外两条要留意：

- **索引全在内存**，容量直接受 RAM 限制——短文本约 1537 B/篇。
  详见 [docs/EMBEDDING.md](docs/EMBEDDING.md) 的容量一节。
- **API 还在 v0.x**，允许破坏性变更，而且**没有 CHANGELOG 文件**
  （变更历史靠 `git log`，提交信息写得比较细）。

**拿去用之前请自己评估。** 要放在无人值守的核心链路上，
上面这些至少要自己补上并验证。

启动时 `tshd` 会打一条同内容的 `WARN` 日志——**因为不读 README 的人也需要
看到它**（比如直接 `docker run` 一个镜像）。不想看到就用 `-log-level error`
静音，刻意没有为它加专门的开关。

> 当前进度：**Phase 0–5 全部完成**，并已加入**中文分词与检索**、
> **L1 文档持久化**（追加式原文日志 + 启动重放 + 自动压实）、
> **类型化字段与范围查询**、**表（多命名空间）**、**排序**。
> 10 万篇规模下检索 P99 < 20ms；`make check`（含 `-race`）全绿。
> 想嵌进自己的 Go 程序看 **[docs/EMBEDDING.md](docs/EMBEDDING.md)**，
> 性能与容量数据见 [docs/PERFORMANCE.md](docs/PERFORMANCE.md)，
> 中文支持的实测对比见 [docs/CHINESE.md](docs/CHINESE.md)，
> 方案与取舍见 [PLAN.md](PLAN.md)。

> **依赖说明（相对原计划的修订）**
>
> 本项目原本以「零第三方依赖」为目标，Phase 0–5 也确实做到了。
> 加入中文分词时引入了 **一个** 外部依赖：
> [`github.com/go-ego/gse`](https://github.com/go-ego/gse)（纯 Go，仅间接依赖
> `vcaesar/cedar`）。
>
> 取舍理由与代价：
> - 换成 **CGO** 方案的 `gojieba` 会摧毁静态构建、scratch 镜像与四目标交叉编译矩阵，
>   因此不采用；
> - gse 经实测 `CGO_ENABLED=0 GOOS=linux` 构建通过，**静态交付形态保持不变**；
> - 代价是**二进制从 6.71 MB 涨到 38.47 MB**（内嵌词典约 30 MB），
>   以及 **108 MB 的内存基线**。
>
> 现在准确的表述是：**核心检索路径（索引、查询、打分、HTTP）零第三方依赖，
> 只有中文分析器引入 gse**。不启用 `-analyzer chinese` 时，gse 的代码路径不会被走到。

## 特性

- 手写倒排索引，posting list 带 token 位置信息，支持短语查询
- BM25 相关性排序（k1 = 1.2，b = 0.75）+ 小顶堆 Top-K
- 布尔查询：`AND` / `OR` / `NOT`，以及 `-term` 排除语法
- 文档增删改查、高亮片段
- **中文检索**：词典分词 + 子词扩展（`-analyzer chinese`）
- **持久化**：追加式原文日志 + 启动重放（`-data-dir`），崩溃可恢复
- **类型化字段**：数值与时间的等值/范围查询、关键字精确匹配
- **表（多命名空间）**：商品只查商品、资讯只查资讯，各表独立 schema
- 标准库实现：`net/http`、`log/slog`、`encoding/json`

## 适合 / 不适合

**它是什么**：一个可以**嵌进 Go 程序**的轻量全文搜索引擎，也能当独立服务跑。
单二进制、纯内存索引、只依赖标准库（中文分词引入一个 gse）。

**适合**：

- 想在 Go 服务里加搜索，但不想再部署一个 Meilisearch / Typesense
- 数据量在**百万篇以内**（见下面的容量表），且能接受索引常驻内存
- 需要中文分词开箱可用
- 需要数值/时间的范围查询、按字段排序
- 按内容分表（商品 / 资讯 / 日志各一张）

**不适合**：

| 不需要 | 为什么 |
| --- | --- |
| 替换 Elasticsearch / Solr | 没有分布式、没有副本、没有聚合与分面 |
| 十亿级数据 | 索引全在内存，容量直接受 RAM 限制 |
| 高可用 / 多副本 | 单进程单写入者；持久化是「重启能恢复」，不是「不怕宕机」 |
| 复杂相关性调优 | 只有一套 BM25，没有自定义打分脚本 |
| 模糊匹配 / 拼写纠错 | 未实现 |
| 多值字段 | 一个字段只能有一个值。`"go,search"` 写进关键字字段是**一个**整词条，`category:go` 匹配不上；要多值只能拆成多个字段 |
| 聚合 / 分组统计 | 未实现（MVP 可以用几次查询自己拼） |
| 地理位置检索 | 未实现 |

写清楚「不做」是为了划出安全边界——**知道自己在什么范围内是安全的，
才敢用**。

### 容量：每篇文档占多少内存

纯内存索引，数据量直接受 RAM 限制。标准分析器，20000 篇实测：

| 正文长度 | 每篇内存 | 相对正文 | 2 GB 预算约可装 |
| --- | --- | --- | --- |
| 短（约 50 字 / 144 B） | **1 537 B** | 10.7 倍 | 约 **139 万**篇 |
| 中（约 500 字 / 1.4 KB） | **3 216 B** | 2.2 倍 | 约 66 万篇 |
| 长（约 5000 字 / 14 KB） | **20 496 B** | 1.4 倍 | 约 10 万篇 |

**短文本的放大率最高（10.7 倍）**：词条倒排、每篇文档的字段长度表、
DocID 映射这些固定开销摊到小文档上就很显眼。

启用中文分析器要再加约 **108 MB** 的词典基线，以及约 700 ms 的加载时间。

二进制体积：默认 **42.3 MB**，`-ldflags "-s -w"` 后 **38.9 MB**
（大头是内嵌的中文词典，约 30 MB）；不启用中文分词时约 6.7 MB。

## 快速开始

**想先看代码**：`go run ./examples/blog` —— 一个 60 行的完整例子，
演示建引擎、灌带类型的文章、相关性搜索 + 高亮、中文搜索 + 排序、
关键字精确匹配 + 时间倒序。它不启动服务，引擎就跑在那个进程里。

本机 Go 未加入 PATH 时，用绝对路径调用即可（本文档以 `D:\Program Files\Go\bin\go.exe` 为例）。

```powershell
# 编译
& "D:\Program Files\Go\bin\go.exe" build -o bin/tshd.exe ./cmd/tshd
```

### 一键跑起来：导入自带的示例语料

```powershell
./bin/tshd.exe -addr :8080 -import ./testdata/corpus
```

启动日志会告诉你灌进去了什么：

```
{"level":"INFO","msg":"语料目录已读取","dir":".\\testdata\\corpus","files":6}
{"level":"INFO","msg":"语料导入完成","docs":6,"terms":246,"index_mb":0.018,"elapsed":"0s"}
```

然后就能搜了：

```powershell
curl.exe --noproxy "*" "http://127.0.0.1:8080/api/v1/search?q=%22posting%20list%22&highlight=true"
```

```powershell
# 存活探针
curl.exe --noproxy "*" http://127.0.0.1:8080/healthz
# {"status":"ok"}

# 索引统计
curl.exe --noproxy "*" http://127.0.0.1:8080/api/v1/stats
# {"docs":6,"terms":246,"fields":2,"avg_doc_len":64.17,"index_bytes":19549}
```

### 压测用的合成语料

```powershell
# 直接在内存里合成 10 万篇（固定随机种子，可复现）
./bin/tshd.exe -addr :8080 -generate 100000
```

`-generate` 与 `-import` 可以同时使用。合成语料刻意做成**不均匀**的词频分布，
理由见 [docs/PERFORMANCE.md](docs/PERFORMANCE.md)。

### 中文检索

```powershell
./bin/tshd.exe -addr :8080 -analyzer chinese -import ./testdata/corpus
```

```powershell
# "倒排索引" 会被切成「倒排」「索引」两个词
curl.exe --noproxy "*" "http://127.0.0.1:8080/api/v1/search?q=%E5%80%92%E6%8E%92%E7%B4%A2%E5%BC%95"
```

领域词（如「倒排索引」「跳表」）如果内嵌词典没收录会被切碎，
可以用自定义词典补充：

```powershell
./bin/tshd.exe -analyzer chinese -dict ./words.txt
```

`words.txt` 每行一个词，支持 `#` 注释，也接受 gse 的「词 词频 词性」格式。

> **注意**：启动时加载约 108 MB 词典、耗时约 0.7 秒，
> 二进制也会因为内嵌词典从 6.71 MB 涨到 38.47 MB。
> 完整实测数据、两个分析器的真实差别、以及已知取舍见
> **[docs/CHINESE.md](docs/CHINESE.md)**。

### 持久化

```powershell
./bin/tshd.exe -addr :8080 -data-dir ./data
```

数据存成 `data/documents.wal`：一个**只追加的原文日志**，启动时重放它重建索引。
不带 `-data-dir` 就是纯内存，行为与以前完全一致。

**为什么存原文而不是索引**：换分析器、改索引格式、修索引 bug 之后，
索引都可以从原文重建；反过来只存索引，就等于把数据锁死在一种格式上。
**索引是派生数据，原文才是权威数据。**

**持久性保证**（批量 fsync，默认 100ms）：

| 场景 | 保证 |
| --- | --- |
| 写入返回后**进程崩溃** | **不丢** —— 数据已经交给操作系统 |
| **断电 / 宿主机崩溃** | 最多丢 100ms 内的写 |
| 正常关闭（Ctrl+C / SIGTERM） | 做最后一次 fsync |

代价是写入慢约 **3.9 倍**（11µs → 44µs，折合约 22,800 次写/秒单线程）。
每次写都 fsync 是 **76 倍**——这就是选批量策略的量化依据。

**重启代价 ≈ 重建索引代价**，因为 L1 不落索引，每次都要重新分析：
2000 篇实测 32.8ms，换算到 10 万篇中等长度文档约 **15 秒**。
如果启动时间成为痛点，下一步是持久化 token 流（PLAN 里的 L3）。

**损坏处理**：

- 日志**尾部**不完整或校验失败 → 自动截断并告警（这是崩溃时写了一半的记录）
- 损坏之后**还跟着大量数据** → **拒绝启动**，交人工检查
  （截断等于把后面可能完好的记录一起丢掉，不能静默做）
- 文件头魔数或版本不符 → 拒绝启动，**不覆盖**已有文件
- 日志写不下去（磁盘满等）→ 进入降级状态，后续写一律快速失败，
  读请求不受影响。**不会静默丢数据。**

### 类型化字段与范围查询

字段类型由 **JSON 本身**表达，与 ES 的用法一致：

```json
{"fields":{"title":"笔记本电脑","sku":"LAP-1","price":4999,"onSale":true}}
```

| 写法 | 类型 | 能怎么查 |
| --- | --- | --- |
| `"hello"` | 文本 | 词条、短语 |
| `4999` | 数值 | 等值、范围 |
| `true` | 关键字 `"true"` | 精确匹配 |
| `null` | 跳过 | — |

**日期与关键字必须显式声明**：

```powershell
./bin/tshd.exe -mapping "created:date,sku:keyword"
```

为什么不能自动识别：**JSON 里没有日期类型**，`"2024-01-15"` 只是一个
字符串。靠猜格式是错的——版本号 `"2024-01-01"` 会被当成日期，
而这是个很难被发现的静默错误。

查询语法：

```sql
price:[10 TO 100]                闭区间
price:{10 TO 100}                开区间
price:[10 TO *]                  单边（* 表示无界）
price:>=10                       比较运算符（等价于 [10 TO *]）
price:4999                       数值等值
created:[2024-01-01 TO 2024-12-31]
sku:LAP-1                        关键字精确匹配
title:笔记本                      字段限定的文本查询
```

**几点需要知道的语义**：

- **范围查询不打分**（恒为 0）。BM25 建立在词频上，「价格 42」没有词频
  概念，硬套只会得到没有意义的分数。所以与文本子句 AND 时分数只来自
  文本子句，OR 时范围命中的文档排在后面。
- **时间的时区**：`2024-01-01` 一律按 **UTC 当日零点**解释，不猜本地时区。
  要本地时间就显式写偏移（`2024-01-01T08:00:00+08:00`）。
  内部精度是**毫秒**。
- **超过 2^53 的整数会被拒绝**，不会静默改变你的数据
  （`float64` 表示不了 `20240101123456789`）。这类值请用字符串字段。
- **类型冲突报 400**，错误里会带上字段名、两种类型与文档 ID。
  一个字段在一份索引里只能有一种类型。
- **`:` 的语义变了**（破坏性变更）：以前 `a:b` 是一个普通词，
  现在是「字段 a 里的 b」。时间戳不受影响——字段名不允许数字开头。
- 范围查询是 **O(文档数)** 的线性扫描（按 DocID 顺序，输出天然有序）。
  10 万篇实测 0.2 ms，但千万篇会到 20 ms，那时需要换实现。

## 持久化

启用 `-data-dir` 后数据会落到磁盘，重启自动恢复：

```powershell
./bin/tshd.exe -data-dir ./data
```

存的是**原文**而不是索引：换分析器、改索引格式、修索引 bug 之后，索引
都可以从原文重建；反过来只存索引就等于把数据锁死在一种格式上。
索引是派生数据，原文才是权威数据。

### 日志压实

日志是只追加的：同一篇文档更新 10 次就有 11 条记录，重启要把这 11 条
全部重放一遍。**日志明显大于存活数据时会自动压实**——重写成当前状态的
快照，两个数字都回到 1 倍附近。

实测（5000 篇 × 10 次更新）：

| | 压实前 | 压实后 |
| --- | --- | --- |
| 日志大小 | 25.43 MB | **2.32 MB** |
| **重启重放** | **11.3 s** | **263 ms** |

代价是同一批写入慢约 10%。想手动触发（比如停机前把重启时间压下去）：

```go
tb, _ := engine.Table(tsh.DefaultTable)
tb.Compact()
```

`tb.LogBytes()` / `tb.LogRecords()` 可以在压不压实之间做判断。

## 表（多命名空间）

把不同内容分开放，商品只查商品、资讯只查资讯。

```powershell
# 建表并声明字段类型
curl.exe --noproxy "*" -X PUT -H "Content-Type: application/json" `
  -d '{"schema":{"name":"text","price":"number","created":"date","sku":"keyword"}}' `
  http://127.0.0.1:8080/api/v1/tables/products

# 写进这张表
curl.exe --noproxy "*" -X PUT -H "Content-Type: application/json" `
  -d '{"fields":{"name":"笔记本电脑","price":4999,"sku":"LAP-1"}}' `
  http://127.0.0.1:8080/api/v1/tables/products/documents/p1

# 只在这张表里搜
curl.exe --noproxy "*" "http://127.0.0.1:8080/api/v1/tables/products/search?q=price:%5B0%20TO%206000%5D"
```

| 端点 | 作用 |
| --- | --- |
| `GET /api/v1/tables` | 列出所有表（含各自的 schema 与统计） |
| `PUT /api/v1/tables/{table}` | 建表（**幂等**，body 可带 schema） |
| `GET /api/v1/tables/{table}` | 表详情 |
| `DELETE /api/v1/tables/{table}` | 删表 |
| `* /api/v1/tables/{table}/documents[/{id}]` | 表内文档增删改查 |
| `GET /api/v1/tables/{table}/search` | 表内检索 |

### 每张表是独立的索引

这不是组织方式上的包装，而是**硬性要求**：字段类型是索引级的，
同一个字段名在一份索引里只能有一种类型。所以「商品表的 `price` 是数字、
资讯表的 `price` 是字符串编号」这种再正常不过的需求，只有让每张表
拥有独立索引才能成立。

顺带的好处是**查询只扫自己的表**——查商品不会去扫资讯的 posting。

### 表名规则

只允许**小写字母、数字、下划线、连字符**，首字符必须是字母或数字，
长度 1–64。

**不允许大写**不是洁癖：Windows 与 macOS 的文件系统**大小写不敏感**，
`Products.wal` 与 `products.wal` 是同一个文件（实测确认：
依次写入两个名字后目录里只有一个文件，`os.SameFile` 返回 true）。
允许大写就意味着两张表共用一份日志、数据互相污染——而这个问题
在 Linux 上完全测不出来。

### 几点需要知道的

- **不会自动建表**。往不存在的表写入返回 404 而不是顺手创建——
  表名拼错却静默产生一张新表，在导入管道里极难发现。
- **默认表 `default` 不能删**。旧的扁平路由（`/api/v1/documents`、
  `/api/v1/search`）都指向它，删掉会让一半接口失去目标。
- **每张表一个日志文件**：`<data-dir>/tables/<name>.wal`。
  删表就是删文件。旧布局的 `<data-dir>/documents.wal` 会在启动时
  自动迁移成 `tables/default.wal`。
- **暂不支持跨表查询**。要同时搜多张表请分别调用再自行合并。
- 所有表共用同一个分析器：中文词典加载一次要几百毫秒与上百 MB，
  每张表各来一份不可接受。

配置文件里批量声明（`-mapping-file`）：

```json
{
  "products": {"name": "text", "price": "number", "created": "date"},
  "articles": {"title": "text", "price": "keyword", "published": "date"}
}
```

## 容器与 CI

```bash
docker build -f deploy/Dockerfile -t go_tsh .
docker run --rm -p 8080:8080 go_tsh
```

镜像基于 `scratch`：完全静态链接，以 `nobody` 运行。

CI 跑的是 `scripts/check.ps1`（gofmt + vet + 跨 module 的对外 API 门禁 +
`go test -race`）。其中 **apicheck 那一步不能省**：它用 `replace` 指回本仓库、
以外部 module 的身份编译，因此受和真实用户完全相同的 `internal` 规则约束。
仓库内的测试测不到这件事——`package tsh_test` 虽然在别的包里，
但仍在同一个 module 内，照样能 import `internal`。

## 版本策略

当前是 **v0.x**：允许破坏性变更，但每一处都会在提交信息里写清楚
（例如 `:` 变成字段分隔符、`-query-timeout` 被移除）。

打 **v1.0** 的时机是：核心 API（`tsh.Engine` / `tsh.Table` /
`tsh.Document` / `tsh.SearchRequest`）稳定下来，且已经有外部使用者
报过问题。在那之前，`apicheck` 门禁保证的是「**能编译**」，
不是「不会变」。

尚未有的：CHANGELOG 文件。变更历史目前看 `git log`——提交信息写得比较细，
每条都说明了「为什么」而不只是「改了什么」。
代价是**不能 `docker exec` 进去排查**（容器里没有任何可执行文件），
也没有 `HEALTHCHECK` 可写——请由编排层通过 HTTP 探 `/healthz`。

> **镜像体积**：引入 gse 之前实测 **7.03 MB**；现在因为内嵌中文词典，
> 二进制为 **38.47 MB**（linux/amd64，实测），镜像体积与之相当。
> 若对体积敏感，`go build -tags ne` 可降到 **7.61 MB**，
> 但词典不再内嵌，需要自行提供词典文件——当前代码走的是内嵌路径，
> 切换过去需要额外改造。详见 [docs/CHINESE.md](docs/CHINESE.md)。

CI 在 `.github/workflows/ci.yml`：Linux runner 上跑 `gofmt + go vet + go test -race`，
外加一个四目标交叉编译矩阵（linux/amd64、linux/arm64、darwin/arm64、windows/amd64）。
Linux 上 `-race` 不需要额外配置——`ubuntu-latest` 自带 gcc，`CGO_ENABLED` 默认为 1，
与 Windows 上要手动装 MSYS2 完全不同。
CI runner 能直连 `proxy.golang.org`，不需要镜像配置；
但**首次构建会下载 gse 与 cedar**，因此 `go.sum` 必须入库（已在库中）。

## 性能

10 万篇语料下的实测数据、方法说明、以及压测抓到的真实缺陷记录，
见 **[docs/PERFORMANCE.md](docs/PERFORMANCE.md)**。

一句话版本：所有检索路径 P99 < 20 ms；并发检索约 5,100 req/s，
并发写入约 19,400 req/s。

## 配置

优先级：**命令行参数 > 环境变量 > 默认值**。环境变量统一为 `TSH_` 前缀。

| 参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-addr` | `TSH_ADDR` | `:8080` | HTTP 监听地址 |
| `-log-level` | `TSH_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `-import` | `TSH_IMPORT` | 空 | 启动时导入的语料目录（txt/md），**启动动作** |
| `-generate` | `TSH_GENERATE` | `0` | 启动时合成的文档数，**压测用** |
| `-analyzer` | `TSH_ANALYZER` | `standard` | `standard` / `chinese`，**换值必须重建索引** |
| `-dict` | `TSH_DICT` | 空 | 中文自定义词典文件；需 `-analyzer chinese` |
| `-no-sub-words` | `TSH_NO_SUB_WORDS` | `false` | 关闭中文子词扩展；需 `-analyzer chinese` |
| `-data-dir` | `TSH_DATA_DIR` | 空 | 持久化目录；**留空即纯内存，重启丢失** |
| `-sync-interval` | `TSH_SYNC_INTERVAL` | `100ms` | 批量 fsync 间隔，必须为正 |
| `-mapping` | `TSH_MAPPING` | 空 | 默认表的字段类型声明，如 `"created:date"` |
| `-mapping-file` | `TSH_MAPPING_FILE` | 空 | 按表声明字段类型的 JSON 文件 |
| `-import-table` | `TSH_IMPORT_TABLE` | `default` | `-import` / `-generate` 的目标表 |
| `-mapping` | `TSH_MAPPING` | 空 | 字段类型声明，如 `created:date,sku:keyword` |
| `-max-body-bytes` | `TSH_MAX_BODY_BYTES` | `8388608` | 单请求体字节上限（8 MiB） |
| `-max-doc-fields` | `TSH_MAX_DOC_FIELDS` | `32` | 单文档字段数上限 |
| `-max-doc-tokens` | `TSH_MAX_DOC_TOKENS` | `100000` | 单文档 token 数上限 |
| `-max-query-terms` | `TSH_MAX_QUERY_TERMS` | `32` | 单次查询 term 数上限 |
| `-read-timeout` | `TSH_READ_TIMEOUT` | `10s` | HTTP 读超时 |
| `-write-timeout` | `TSH_WRITE_TIMEOUT` | `30s` | HTTP 写超时 |
| `-idle-timeout` | `TSH_IDLE_TIMEOUT` | `60s` | HTTP 空闲连接超时 |
| `-shutdown-grace` | `TSH_SHUTDOWN_GRACE` | `10s` | 优雅关闭等待上限 |

## HTTP 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/healthz` | 存活探针 |
| `GET` | `/api/v1/stats` | 索引规模统计 |
| `POST` | `/api/v1/documents` | 新建文档，**已存在返回 409** |
| `PUT` | `/api/v1/documents/{id}` | 覆盖式写入：新建 201 / 覆盖 200 |
| `GET` | `/api/v1/documents/{id}` | 取原文 |
| `DELETE` | `/api/v1/documents/{id}` | 删除，成功返回 204 |
| `GET` | `/api/v1/search` | 检索 |

### 写入文档

```powershell
curl.exe --noproxy "*" -X POST http://127.0.0.1:8080/api/v1/documents `
  -H "Content-Type: application/json" `
  --data-binary "@doc.json"    # 见下方「为什么用文件传 body」
```

```json
{ "id": "doc-1", "fields": { "title": "Inverted index", "body": "..." } }
```

`POST` 与 `PUT` 的语义**刻意分开**：`POST` 撞 ID 返回 `409`，
让「不小心覆盖了别人的文档」变成一个明确的错误；
`PUT` 才是覆盖语义，且是**整体替换**——旧版本独有的词条会被完整摘除。

未知字段（例如把 `fields` 拼成 `field`）会返回 `400`，
而不是静默忽略后写入一篇空文档。

### 检索

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `q` | 必填 | 查询串 |
| `limit` | 10 | 返回条数，上限 100 |
| `offset` | 0 | 跳过条数 |
| `field` | 全部 | 限定字段，**可重复出现** |
| `highlight` | false | 是否返回高亮片段，只接受 `true/false/1/0` |

查询语法：`quick brown`（默认 OR）、`"inverted index"`（短语）、
`-excluded` 或 `NOT excluded`、`a AND b`、`(a OR b) AND c`。

```json
{
  "took_ms": 0,
  "total": 2,
  "hits": [
    {
      "id": "doc-1",
      "score": 2.81,
      "fields": { "title": "Inverted index" },
      "highlights": { "title": "\u003cem\u003eInverted\u003c/em\u003e index" }
    }
  ]
}
```

> **注意 `highlights` 里的 `\u003c`**：这是 Go `encoding/json` 的默认行为，
> 它会把 `<` `>` `&` 转义掉。JSON 解析后拿到的就是正常的 `<em>`。
> 这是**刻意保留**的：文档字段内容由调用方提供，
> 不转义的话，把响应直接嵌进 HTML 页面就会变成 XSS 入口。

### 错误响应

统一为：

```json
{ "error": { "code": "INVALID_QUERY", "message": "..." } }
```

| 状态码 | `code` | 场景 |
| --- | --- | --- |
| 400 | `BAD_REQUEST` | 请求体非法、参数非整数、字段数或 token 数超限 |
| 400 | `INVALID_QUERY` | 查询串为空或语法错误 |
| 404 | `NOT_FOUND` | 文档不存在 |
| 409 | `CONFLICT` | `POST` 的 ID 已存在 |
| 413 | `PAYLOAD_TOO_LARGE` | 请求体超过上限 |
| 500 | `INTERNAL` | 服务端故障（细节只记日志，不外泄） |

## 开发

```powershell
# Windows：一键门禁（格式 + vet + 竞态测试）
pwsh -File scripts/check.ps1 -GoExe "D:\Program Files\Go\bin\go.exe"

# 或者用 make（需要 Unix shell）
make check
```

## 代码结构

```
cmd/tshd/           # 守护进程入口：配置装配、语料导入、日志、优雅关闭
internal/config/    # 配置解析（flag + env）
internal/analyzer/  # 文本分析 ✅ StandardAnalyzer + 内置停用词表
internal/index/     # 倒排索引 ✅ InvertedIndex + View / PostingCursor
internal/query/     # 查询 AST、解析器与执行器（归并求值 + BM25）✅
internal/scoring/   # BM25 与 Top-K ✅
internal/highlight/ # 高亮片段（HTML 转义 + 窗口截断）✅
internal/corpus/    # 语料导入与合成（演示 / 压测用）✅
internal/tablename/ # 表名校验（四个调用方共用一条规则）✅
pkg/tsh/            # 对外门面 Engine ✅ 可被别的 Go 程序嵌入
pkg/httpapi/        # HTTP 路由、中间件、DTO、错误映射 ✅ 可挂进调用方进程
apicheck/           # 跨 module 的对外 API 编译门禁
deploy/             # Dockerfile（多阶段 → scratch）
docs/               # 性能报告
```

依赖方向单向向内：
`cmd/tshd → pkg/httpapi → pkg/tsh → {analyzer, query, index, scoring}`。

### 作为库嵌入

`pkg/tsh` 可以直接跑在你自己的进程里，不需要额外部署服务：

```go
engine, err := tsh.NewWith(tsh.Options{
    Analyzer: tsh.AnalyzerChinese,
    DataDir:  "./data",          // 留空则纯内存
})
defer engine.Close()

engine.Upsert(tsh.Document{
    ID:     "p1",
    Fields: map[string]string{"name": "笔记本电脑"},
    Numbers: map[string]float64{"price": 4999},
})

res, _ := engine.Search(tsh.SearchRequest{Query: "name:笔记本", Limit: 10})
```

➡️ **完整指南见 [docs/EMBEDDING.md](docs/EMBEDDING.md)**：字段类型与为什么不能猜、
`Create` / `Upsert` 的区别、查询语法、排序的三条规则、表的隔离、
持久化与压实、并发模型、接自己的 HTTP 接口、**六个常见陷阱**、API 速查。

`apicheck/` 是一道**跨 module** 的编译门禁：它用 `replace` 指回本仓库、
以外部 module 的身份编译，因此受和真实用户完全相同的 `internal` 规则约束。
仓库内的测试测不到这件事——`package tsh_test` 虽然在别的包里，
但仍在同一个 module 内，照样能 import `internal`。
**EMBEDDING.md 里出现的每个 API 都由它守着**，所以文档不会漂移。

## 故障排查

### 为什么用文件传 body：PowerShell 会吃掉 JSON 里的双引号

在 Windows PowerShell 5.1 里这样调 curl 是不行的：

```powershell
curl.exe -d '{"id":"doc-1"}' http://127.0.0.1:8080/api/v1/documents
# 服务端收到的是 {id:doc-1} —— 双引号没了
# → BAD_REQUEST: invalid character 'i' looking for beginning of object key string
```

PowerShell 把参数拼成命令行时会剥掉内嵌的双引号，这是它调用原生命令的经典缺陷。
绕开的方式是写进文件再让 curl 读：

```powershell
Set-Content -Path doc.json -Value '{"id":"doc-1","fields":{"title":"..."}}' -Encoding ascii -NoNewline
curl.exe --noproxy "*" -X POST -H "Content-Type: application/json" `
  --data-binary "@doc.json" http://127.0.0.1:8080/api/v1/documents
```

用 `-Encoding ascii`（或 UTF-8 无 BOM）很重要：PS 5.1 的 `-Encoding UTF8`
会写入 BOM，服务端会把它当成 JSON 的第一个字符而报错。

### 不要把 PowerShell 函数命名为 `Curl`

PowerShell 的命令解析优先级是 **Alias > Function > Cmdlet > Application**，
而 `curl` 是 `Invoke-WebRequest` 的内置别名。定义一个叫 `Curl` 的函数会被别名截胡，
报出莫名其妙的 `Invoke-WebRequest: A positional parameter cannot be found`。

要么换个函数名，要么始终写 `curl.exe`（带扩展名可以绕过别名）。

### `workspace-write` 沙箱初始化失败：`SetNamedSecurityInfoW failed (Win32 5)`

**现象**：在 DSH 中以 `workspace-write` 模式执行任何命令，都在沙箱准备阶段报
`grantWrite(<workspace>)` 失败，只有 `danger-full-access` 可用。

**根因**：DSH 沙箱用 **write-restricted token** 运行命令。这类令牌会绕过 NTFS
「对象所有者隐式拥有 `READ_CONTROL` + `WRITE_DAC`」的规则，强制所有写访问
（含改 DACL）必须由 ACL **显式**授权。而工作区默认继承来的
`Authenticated Users:(M)` 与 `Users:(RX)` 都不含 `WRITE_DAC`。

**修复**（无需管理员——你是目录所有者）：

```powershell
icacls "D:\codes\golang\go_tsh" /grant "%USERNAME%:(OI)(CI)F"
```

等价写法：

```powershell
$me  = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$acl = Get-Acl 'D:\codes\golang\go_tsh'
$acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new(
    $me, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow'))
Set-Acl -Path 'D:\codes\golang\go_tsh' -AclObject $acl
```

**回滚**：`icacls "D:\codes\golang\go_tsh" /remove:g "%USERNAME%"`

### `Get-Content` 读日志乱码

Windows PowerShell 5.1 的 `Get-Content` 默认按 ANSI（简体中文下为 GBK）解码，
读 Go 输出的 UTF-8 日志会显示成乱码（`服务启动` → `鏈嶅姟鍚姩`）。
加 `-Encoding utf8` 即可，日志文件本身的字节是正确的。

### `scripts/check.ps1` 报 `Unexpected token '}'`

该脚本必须保持**纯 ASCII**。PS 5.1 会把无 BOM 的 UTF-8 `.ps1` 按 GBK 解析，
中文注释被打碎后会直接破坏语法。若要写中文，必须存为 UTF-8 **带 BOM**。

### 启用 `-race`（竞态检测）

竞态检测依赖 cgo，而 Go 在 Windows 上的 cgo 走 MinGW-w64 路线，
必须是 **GCC 风格**的驱动（官方 LLVM 发布版 target 是 MSVC，**装了对 cgo 没用**）。

推荐用 MSYS2 装 GCC：

```powershell
# 在 MSYS2 的 UCRT64 shell 里
pacman -S mingw-w64-ucrt-x86_64-gcc
```

装好后工具链在 `C:\msys64\ucrt64\bin`。**两个坑**：

1. MSYS2 的工具链**不在 Windows PATH 上**（MSYS2 shell 自己会设），
   所以 cmd 里敲 `gcc` 无效。需要自己加：

   ```powershell
   [Environment]::SetEnvironmentVariable('Path',
       ([Environment]::GetEnvironmentVariable('Path','User').TrimEnd(';') + ';C:\msys64\ucrt64\bin'),
       'User')
   ```

2. ⚠️ **绝不能把 `C:\msys64\usr\bin` 加进 PATH**。那里有 MSYS2 自己的
   `link.exe` / `find.exe` / `sort.exe` / `sh.exe`，会遮蔽 Windows 原生命令
   并破坏无关的构建。只需加 `ucrt64\bin`（或 `mingw64\bin`）。

`scripts/check.ps1` **会自动探测** `ucrt64` / `mingw64` / `clang64` / `mingw32`
四个变体目录，找到就临时加进 PATH 并设 `CGO_ENABLED=1`，
因此项目不依赖你的 PATH 配置。若确实找不到 C 编译器，
它会自动降级为普通 `go test` 并明确告警——**不会假装通过**。

### 沙箱内 `go build` 报 `Access is denied`（GOCACHE）

```
open C:\Users\<user>\AppData\Local\go-build\...: Access is denied
```

文件系统沙箱（`workspace-write`）**只放行工作区**，而 Go 的构建缓存默认在
工作区之外。注意这**不是 ACL 问题**——实测给缓存目录补写显式 FullControl
ACE 完全无效，DSH 沙箱按路径白名单判定，而非按 ACL。

`scripts/check.ps1` 会自动处理：它**实际探测**默认缓存能否写入
（建一个探针文件再删掉。ACL、受限令牌、只读挂载在 `Test-Path` 下长得一模一样，
只有真写一次才能区分），不可写就回退到 `<repo>/.gocache/`。
沙箱外行为不变。

手动执行 `go` 命令时同理：

```powershell
$env:GOCACHE = "$PWD\.gocache"
```

### 沙箱内 `go` 警告模块缓存不可写

```
writing stat cache: open C:\Users\<user>\go\pkg\mod\cache\...: Access is denied
```

引入 gse 之后**不能再忽略**这一条：模块缓存默认在 `%GOPATH%\pkg\mod`，
在工作区之外，沙箱会拒绝写入。解决办法与 `GOCACHE` 相同——
把 `GOMODCACHE` 指向工作区内：

```powershell
$env:GOMODCACHE = "$PWD\.gocache\mod"
```

`scripts/check.ps1` 已自动做这件事（会先探测默认可否写入）。

### 拉取新依赖：代理必须换成国内镜像

本机实测：

| 目标 | 沙箱内 | 提权后 |
| --- | --- | --- |
| `https://www.baidu.com` | ❌ 连接失败 | ✅ |
| `https://goproxy.cn/...` | ❌ 连接失败 | ✅ |
| `https://proxy.golang.org/...` | ❌ | ❌ **被墙** |

两点结论：

1. **沙箱会拦截 HTTPS 出网**（不只是文件系统），因此 `go get` / `go mod tidy`
   需要提权执行；依赖进入工作区内的 `GOMODCACHE` 之后，沙箱内的普通构建就正常了。
2. **`proxy.golang.org` 本身不可达**，必须换镜像：

```powershell
scripts/check.ps1 -GoProxy https://goproxy.cn,direct
```

`-GoProxy` 会同时设 `GOSUMDB=off`——sumdb 落在 `%GOPATH%\pkg\sumdb`，
同样在工作区外，沙箱会拒绝写入。
