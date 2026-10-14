# 嵌入使用指南

把 go_tsh 跑在**你自己的 Go 进程里**，不额外部署服务。

> 本文里的 API 用法由 [`apicheck/`](../apicheck) 这道**跨 module 编译门禁**守着：
> 它以外部 module 的身份、用 `replace` 指回本仓库来编译，
> 因此受和真实用户完全相同的 `internal` 规则约束。
> 仓库内的测试做不到这件事——`package tsh_test` 虽然在别的包里，
> 但仍在同一个 module 内，照样能 import `internal`。

---

## 什么时候该嵌入，什么时候该起服务

| | 嵌入（`pkg/tsh`） | 起服务（`cmd/tshd`） |
| --- | --- | --- |
| 部署 | 无，跟着你的二进制走 | 多一个进程要运维 |
| 数据共享 | 直接读你的事务数据，不需要同步管道 | 要自己写同步 |
| 多语言 | 只能 Go | 任何语言走 HTTP |
| 扩缩容 | 跟你的进程一起 | 可以独立扩 |

**判断标准很简单**：如果搜索的数据本来就在你的 Go 进程里（或者你能同步地拿到），
嵌入几乎总是更划算——省掉一整条数据同步链路，也省掉一个要运维的东西。

需要独立扩缩容、或者有非 Go 的调用方，才值得起服务。
两者也可以同时要：见下面的「接自己的 HTTP 接口」。

---

## 最小可用

```go
engine, err := tsh.NewWith(tsh.Options{})
if err != nil {
    return err
}
defer engine.Close()

engine.Upsert(tsh.Document{
    ID:     "p1",
    Fields: map[string]string{"title": "笔记本电脑", "body": "轻薄的开发本"},
})

res, err := engine.Search(tsh.SearchRequest{Query: "title:笔记本", Limit: 10})
```

`tsh.Options{}` 的零值就是一套合理默认（标准分析器、纯内存、不落盘）。
要中文分词、要持久化、要类型化字段，用下面的选项。

> `tsh.New()` 是 `NewWith(Options{})` 的简写，**不会失败**，因此不返回错误。
> 但它用不了中文分析器（那要加载词典，可能失败），生产代码请用 `NewWith`。

---

## 引擎的生命周期

```go
engine, err := tsh.NewWith(tsh.Options{
    Analyzer: tsh.AnalyzerChinese, // 中文分词
    DataDir:  "./data",            // 持久化目录；留空则纯内存
})
if err != nil {
    return err
}
defer engine.Close() // ← 必须
```

**`Close` 为什么必须调用**：启用持久化时，写入返回只保证数据交给了操作系统
（进程崩溃不丢），但**断电**最多会丢最后一个 fsync 批次（默认 100 ms）。
`Close` 做最后一次 fsync 并停掉后台刷盘协程。

`Close` 是**幂等**的，可以重复调用。关闭之后的写会返回错误，读仍然可用。

嵌入方通常把它挂在 `main` 的 `defer` 上，或者你那个组件的 `Shutdown` 里。

---

## 字段类型

字段类型**必须由你声明**，引擎不会从值里猜：

```go
engine, err := tsh.NewWith(tsh.Options{
    Schema: map[string]tsh.FieldKind{
        "title":    tsh.FieldText,    // 分词后建倒排 —— 词条、短语查询
        "category": tsh.FieldKeyword, // 整值精确匹配 —— 不分词
        "price":    tsh.FieldNumber,  // 等值、范围、排序
        "created":  tsh.FieldDate,    // 范围、排序（内部按 UTC epoch 毫秒）
    },
})
```

**为什么不猜**：同一个 `"123"` 既可能是数字也可能是编号。猜错了要么查不出来、
要么查出一堆无关的东西。商品编号 `"0755"` 被当成数字 755，前导零就没了。

`FieldDate` 尤其不能猜——**字符串里没有日期类型**，`"2024-01-15"` 只是一个
字符串。靠格式猜的话，版本号 `"2024-01-01"` 会被当成日期，
而这是个很难被发现的静默错误。

`2024-01-15` 一律按 **UTC 当日零点**解释，不猜本地时区。
要本地时间就显式写偏移（`2024-01-15T08:00:00+08:00`）。

### 表级 schema

默认表之外的表，用 `Options.TableSchemas` 声明（表也会被建出来）：

```go
engine, err := tsh.NewWith(tsh.Options{
    TableSchemas: map[string]map[string]tsh.FieldKind{
        "products": {"name": tsh.FieldText, "price": tsh.FieldNumber},
        "articles": {"title": tsh.FieldText, "published": tsh.FieldDate},
    },
})
```

也可以运行时建：

```go
products, err := engine.CreateTable("products", map[string]tsh.FieldKind{
    "name":  tsh.FieldText,
    "price": tsh.FieldNumber,
})
```

---

## 写入

`Document` 按类型分成几张表，**放进哪张就是什么类型**：

```go
_, err := engine.Upsert(tsh.Document{
    ID:       "p1",
    Fields:   map[string]string{"title": "笔记本电脑", "body": "轻薄的开发本"},
    Keywords: map[string]string{"category": "electronics"},
    Numbers:  map[string]float64{"price": 4999, "stock": 12},
    Dates:    map[string]time.Time{"created": time.Now()},
})
```

| 方法 | 语义 |
| --- | --- |
| `Upsert(doc)` | 不存在则新建、存在则**整体替换**；返回 `created` 区分 201/200 |
| `Create(doc)` | 只新建；已存在返回 `tsh.ErrDocumentExists` |
| `Delete(id)` | 删除；不存在返回 `tsh.ErrDocumentNotFound` |
| `GetDocument(id)` | 取回，字段会按 schema **还原成各自的类型** |

**`Create` 与 `Upsert` 分开是有意的**：让「不小心覆盖了已有文档」变成一个明确的
错误，而不是无声无息。反过来，`Upsert` 的覆盖是**整体替换**——
旧版本独有的词条会被完整摘除，不会留下幽灵命中。

同一个字段名不能同时出现在两张表里（`Fields` 与 `Numbers` 里都有 `price`），
那会在写入前报 `tsh.ErrAmbiguousField`。

### 从 JSON 原生值构造

手里的数据是 `map[string]any`（比如刚解出来的 JSON）时：

```go
doc, err := engine.ParseDocument("p1", map[string]any{
    "title":   "笔记本电脑",  // → 文本
    "price":   4999,        // → 数值
    "onSale":  true,        // → 关键字 "true"
    "gone":    nil,         // 跳过
})
```

`ParseDocument` 会查**引擎的字段类型表**，因此声明成 `date` 的字段
收到字符串会按日期解析，声明成 `number` 的会按数值解析。

不查类型表的版本是 `tsh.DocumentFromValues(id, values)`——
它只按 JSON 原生类型推断，**日期与关键字推断不出来**，适合确实不需要它们的场景。

> 超过 2^53 的整数会被**拒绝**（`tsh.ErrIntegerTooLarge`），
> 而不是被 `float64` 静默改掉。订单号这类值请用字符串字段。

---

## 检索

```go
res, err := engine.Search(tsh.SearchRequest{
    Query:     "category:electronics AND price:[1000 TO 6000]",
    Fields:    []string{"title", "body"}, // 限定检索字段；空表示全部
    Sort:      []tsh.SortKey{{Field: "price", Desc: true}},
    Limit:     20,
    Offset:    0,
    Highlight: true,
})
```

### 查询语法

```
term               词条（经分析器归一化：大小写、分词、停用词）
"some phrase"      短语，要求词条位置连续
-term  /  NOT term 排除
a AND b            两者都要（也支持 &&）
a OR b             任一即可（也支持 ||）
(a OR b) AND c     括号分组
field:term         限定字段
field:[10 TO 100]  闭区间；{ } 表示开区间，* 表示无界
field:>=10         比较运算符（等价于 field:[10 TO *]）
```

字段限定与范围查询**只对声明过类型的字段有意义**：

| 类型 | 能做什么 |
| --- | --- |
| `text` | 词条、短语。**不能**范围查询、**不能**排序 |
| `keyword` | 整值精确匹配、排序 |
| `number` / `date` | 等值、范围、排序 |

对不支持的字段做这些事会**报错**，不会静默返回空——静默返回空最难自查。

### 排序

```go
Sort: []tsh.SortKey{{Field: "created", Desc: true}, {Field: "price"}}
```

或从字符串解析（HTTP 的 `?sort=created:desc,price:asc` 就是这么来的）：

```go
keys, err := tsh.ParseSort("created:desc,price:asc")
```

三条明确规则：

1. **缺值的文档一律排在最后**，与升降序无关。把「没有价格」的商品排在
   按价格升序的第一位没有意义。
2. **0 与缺值是两回事**。`price = 0` 是「价格是 0」，没有 `price` 是另一回事。
3. 所有排序键都相等时按内部 DocID 兜底，**顺序是确定的**——
   否则分页会重复或漏项。

传了 `Sort` 之后分数**不再决定顺序**，但照常算出来放在 `SearchHit.Score` 里。

### 结果

```go
res.Total        // 命中总数（分页之前）
res.Hits[i].ID   // 你写入时的 ID
res.Hits[i].Score
res.Hits[i].Fields     // 原文
res.Hits[i].Highlights // 字段名 → 已 HTML 转义、命中词包在 <em> 里的片段
```

`Total` 不受 `Limit` 影响。`Highlights` 只在 `Highlight: true` 时有。

---

## 表

`Engine` 持有一组**互相隔离的表**：各自的 schema、文档、数值列与日志文件。

```go
products, err := engine.Table("products")
articles, err := engine.Table("articles")

products.Upsert(...)
articles.Search(...)
```

**Engine 上直接调用的方法都作用在默认表上**（`tsh.DefaultTable`），
这是为了让引入表概念不破坏既有代码：

```go
engine.Upsert(doc)        // ≡ engine.Table(tsh.DefaultTable).Upsert(doc)
```

生命周期：

```go
names := engine.TableNames()          // 已排序
stats := engine.TableStats()          // 每张表的规模
err = engine.DropTable("articles")    // 关闭日志并删文件
```

**表不会自动创建**。往不存在的表写入返回 `tsh.ErrTableNotFound`——
表名拼错却静默产生一张新表，在导入管道里极难发现。

默认表**不能删**（`tsh.ErrCannotDropDefault`）：Engine 上那批方法都指着它。

### 为什么必须是独立的索引

不是封装偏好，是硬约束：**字段类型是索引级的**，同一个字段名在一份索引里
只能有一种类型。所以「商品表的 `price` 是数字、资讯表的 `price` 是字符串编号」
这种正常需求，只有让每张表拥有独立索引才能成立。

顺带的好处是查询只扫自己的表。

---

## 持久化

```go
engine, err := tsh.NewWith(tsh.Options{
    DataDir:      "./data",
    SyncInterval: 100 * time.Millisecond, // 批量 fsync 间隔
})
```

存的是**原文**而不是索引：换分析器、改索引格式、修索引 bug 之后，
索引都可以从原文重建。索引是派生数据，原文才是权威数据。

目录布局：

```
data/
  tables/
    default.wal      # 每张表一个日志
    products.wal
```

### 日志压实

日志只追加：同一篇文档更新 10 次就有 11 条记录，重启要把这 11 条全重放一遍。
**日志明显大于存活数据时会自动压实**，你不需要做什么。

实测（5000 篇 × 10 次更新）：日志 25.43 MB → **2.32 MB**，
重启重放 11.3 s → **263 ms**。

代价是同一批写入慢约 10%。想手动触发（比如停机前把重启时间压下去）：

```go
err := tb.Compact()
```

可观测性：

```go
tb.LogBytes()    // 日志字节数
tb.LogRecords()  // 日志里的记录条数；与存活文档数的比值就是「重启要做多少无用功」
tb.Persisted()   // 这张表是否落盘
```

压实**失败不影响正确性**——旧日志仍然完整，重启照常能恢复。
它只会记一条日志，不会让你的写入失败。

---

## 并发

**`Engine` 与 `Table` 的所有方法都可以并发调用。**

- `Engine` 用一把 `RWMutex` 保护表集合（建表 / 删表 / 取句柄）
- 每张表内部由自己的索引锁保护

两级锁**刻意不嵌套**：持有 `Engine` 的锁时不会去调表上的方法，
因此不会死锁。

写入之间是串行的（日志追加在同一把写锁内完成）——这是有意的取舍：
它换来的是「写入返回 = 已进日志」，重启后一定还在。
读可以并发，但**一次查询从头到尾持有读锁**，所以大量写会阻塞读。

> `View` 把整次检索放进同一把读锁，是为了保证快照一致。
> 代价是高写入压力下读会被挡住，取舍记录在 `docs/PERFORMANCE.md`。

---

## 接自己的 HTTP 接口

想同时提供 HTTP，又不被接管整个 `http.Server`：

```go
api := httpapi.New(httpapi.DefaultOptions(), engine, logger)

mux := http.NewServeMux()
api.Register(mux)                                  // 路由进自己的 mux
mux.HandleFunc("GET /my/custom", myHandler)        // 自己的路由共存
http.ListenAndServe(":8080", api.Wrap(mux))        // 中间件套在最外层
```

- `Register(mux)` **只注册路由**，不套本包的中间件
- `Wrap(handler)` 套上三层中间件：panic 恢复 → 访问日志 → 请求体上限
- 两者可以分开用：只要路由就 `Register`，两者都要就 `Wrap(mux)`

想让它自己就是一个服务：

```go
srv := httpapi.New(opts, engine, logger)
srv.ListenAndServe()   // 内嵌 *http.Server，Shutdown / Handler 都能直接用
```

`httpapi.Options` 是可调的：
`Addr` / `MaxBodyBytes` / `ReadTimeout` / `WriteTimeout` / `IdleTimeout`，
还有 `Options.Validate()` 供你在拥有上下文的地方（比如读配置文件之后）报错。

---

## 常见陷阱

### 单字符词条会被过滤

标准分析器有最小长度与停用词过滤，`"a"` 这类词**不会进索引**，
所以搜不到。这不是 bug，是分词规则。标签这类需要单字符精确匹配的值，
用 `tsh.FieldKeyword`（关键字不做长度过滤）。

### 单次返回最多 100 条

`Limit` 超过 100 会被截到 100。要更多就翻页（`Offset`），
或者用 `Total` 配合多次查询。

> 这条是从 HTTP 服务形态带进来的假设，对嵌入场景偏紧。
> 如果你需要「取回全部匹配」自己做聚合，请提出来。

### 一个字段只能有一个值

`"go,search"` 写进关键字字段是**一个**整词条，`category:go` 匹配不上。
要多值只能拆成多个字段（`tag_1` / `tag_2`）。

### 停用词与 `keyword`

文本字段会过滤停用词（`the`、`的` 之类），关键字字段**不过滤**——
所以 `"the"` 作为关键字是能精确匹配的。

### `:` 是字段分隔符

`a:b` 是「字段 a 里的 b」，不是一个普通词条。字段名不允许数字开头，
所以 `2024-01-01T10:00:00Z` 这类时间戳不会被误拆。

### 换分析器必须重建索引

分析器决定索引里存的是什么词条。持久化存的是原文，所以换个分析器重启就会
按新分析器自动重建——但**纯内存模式下数据就没了**。

---

## 容量与性能

**索引全在内存**，数据量直接受 RAM 限制。标准分析器、20000 篇实测：

| 正文长度 | 每篇内存 | 2 GB 预算约可装 |
| --- | --- | --- |
| 短（约 50 字 / 144 B） | **1 537 B** | 约 **139 万**篇 |
| 中（约 500 字 / 1.4 KB） | **3 216 B** | 约 66 万篇 |
| 长（约 5000 字 / 14 KB） | **20 496 B** | 约 10 万篇 |

中文分析器另加约 **108 MB** 词典基线。

更多数据（延迟、吞吐、日志增长、每次查询的分配量）见
[docs/PERFORMANCE.md](PERFORMANCE.md)。

---

## API 速查

**引擎**

```go
tsh.New()                                  // 默认配置，不会失败
tsh.NewWith(opts) (*Engine, error)
tsh.DefaultOptions() Options               // 拿一份默认值再改
```

**Engine**

```go
Table(name) (*Table, error)    TableNames() []string
CreateTable(name, schema)      DropTable(name) error
TableStats() []TableStat
// 以下作用在默认表上：
Create / Upsert / Delete / GetDocument / Search / ParseDocument
Fields() / Schema() / Stats() / Index()
Close() error
```

**Table**

```go
Name() string
Create / Upsert / Delete / GetDocument / Search / ParseDocument
Fields() / Schema() / Stats() / Index()
Persisted() bool
Compact() error                MaybeCompact()
LogBytes() / LogRecords()
MarkImported(m) error          Imported() (ImportMarker, bool)
Close() error
```

**辅助**

```go
tsh.ParseSort(spec) ([]SortKey, error)
tsh.DocumentFromValues(id, values) (Document, error)
tsh.FieldText / FieldKeyword / FieldNumber / FieldDate
tsh.AnalyzerStandard / AnalyzerChinese
tsh.DefaultTable
```

---

## 一个完整的例子

[`examples/blog`](../examples/blog) 是一个 60 行、可直接跑的程序：

```bash
go run ./examples/blog
```

它演示了建引擎（含 schema）、灌带类型的文章、相关性搜索 + 高亮、
中文搜索 + 排序、关键字精确匹配 + 时间倒序。
