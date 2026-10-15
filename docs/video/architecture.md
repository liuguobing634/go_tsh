# 系统架构

> 第一期技术纪录片素材 · 阶段一产物
> 本文所有结构、类型、方法与行号均取自**代码库当前状态**，可用 `read` 工具逐条复核。
> 【已验证】= 本次分析实跑或直接读码确认；【风险】= 有依据但未验证；【推测】= 无证据。

---

## 1. 分层与依赖方向

【已验证】`go.mod` 声明的依赖方向是单向向内的，README「代码结构」一节对此有明确描述：

```
cmd/tshd  →  pkg/httpapi  →  pkg/tsh  →  { internal/analyzer, internal/query,
                                            internal/index, internal/scoring }
                                              ↓
                                     internal/{highlight, corpus, config,
                                               tablename, wal}
```

【已验证】用 `go list -deps ./cmd/tshd` 反查「链接进二进制的第三方包」，
非标准库的非本项目包**只有三个**，全部属于中文分词这一条链路：

```
github.com/go-ego/gse
github.com/go-ego/gse/hmm
github.com/go-ego/gse/types
github.com/vcaesar/cedar
```

【已验证】再按包逐个查 import：`internal/index`、`internal/query`、`internal/scoring`、
`pkg/httpapi` 的第三方 import 列表**为空**；`internal/analyzer` 的第三方 import 只有
`github.com/go-ego/gse`。

> 结论（可直接入镜）：**「零第三方依赖」在核心检索链路上成立且可机械验证；
> 全部第三方依赖都收敛在中文分词这一个包里。**

### 1.1 各层职责

| 层 | 包 | 职责 | 源码行数 |
| --- | --- | --- | --- |
| 入口 | `cmd/tshd` | 配置装配、语料导入、日志、优雅关闭 | 343 |
| HTTP | `pkg/httpapi` | 路由、中间件、DTO、错误映射 | 1,098 |
| 门面 | `pkg/tsh` | `Engine` / `Table`，可嵌入；持有 WAL 与压实 | 2,330 |
| 分析 | `internal/analyzer` | 文本 → 带位置 token | 588 |
| 查询 | `internal/query` | AST、解析器、求值器、排序 | 1,820 |
| 索引 | `internal/index` | 倒排索引、文档表、数值列、快照 | 1,687 |
| 打分 | `internal/scoring` | BM25 与 Top-K | 167 |
| 持久化 | `internal/wal` | 追加式原文日志的编解码与压实 | 1,099 |
| 支撑 | `internal/{highlight,corpus,config,tablename}` | 高亮、语料、配置、表名 | 182+239+415+98 |

【已验证】行数为本次实测（含空行，口径见 `project-brief.md` 第 6.1 节）。

---

## 2. 核心数据结构

这是纪录片最值得用一段动画讲清楚的部分。

### 2.1 倒排索引本体：`InvertedIndex`

`internal/index/index.go` 中真正承载数据的字段：

```go
terms        map[string]*PostingList   // "field\x00term" -> posting list
docs         map[DocID]*Document       // 内部 DocID -> 文档
byExternal   map[string]DocID          // 外部 ID -> 内部 DocID
fieldSet         map[string]struct{}
fieldTotalTokens map[string]uint64     // 每字段 token 总数（BM25 的 avgdl 用）
fieldDocs        map[string]uint32     // 含该字段的文档数（BM25 的 N 用）
schema       map[string]FieldKind      // 字段类型（索引级，非文档级）
numColumns   map[string]*numericColumn // 数值/时间字段的列式存储
docFieldLens map[string][]int32        // [字段][DocID] = token 数，稠密
nextID       DocID                     // 从 1 开始
bytes        int64                     // 常驻内存的增量记账
```

三个设计决定值得单独讲：

**（1）term key 用 `"field\x00term"` 编码，把「字段隔离」压进一个扁平 map。**

```go
func TermKey(field, term string) string {
	return field + "\x00" + term
}
```

【已验证】选择 NUL 而不是 `:` 的理由在注释里：分析器输出的词条只含字母、数字与撇号，
永不含 NUL，因此不会出现 `"a:b"+"c"` 与 `"a"+"b:c"` 撞车。
副产品是**字段级 DF 天然独立**——同一个词在 `title` 与 `body` 里的 IDF 自动分开算，
不需要嵌套 map。实测证据：`body.posting df=3`、`title.inverted df=1`。

**（2）posting 位置信息让短语查询成为可能。**

```go
type Posting struct {
	DocID     DocID
	TF        uint32     // 该词条在这篇文档这个字段里的出现次数
	Positions []uint32   // 各次出现的位置，严格递增
}
```

【已验证】实测真实索引里的 posting（`docs/video` 素材程序输出）：

```
body.posting  df=3
    doc=d1   tf=1 positions=[19]
    doc=d3   tf=1 positions=[11]
    doc=d4   tf=1 positions=[18]
body.list  df=2
    doc=d1   tf=3 positions=[8 15 20]     <- 同一文档出现 3 次，TF=3
    doc=d3   tf=1 positions=[12]
```

【已验证】本次分析对真实索引断言了两条不变量，结果均为 true：
`Postings 按 DocID 升序`、`TF == len(Positions) 且 Positions 严格递增`。

**（3）`docFieldLens` 的稠密切片替代 map，是性能改造留下的实物。**

注释记录了动机：早期用 `Document.FieldLen`（`map[string]int`）时，
检索热路径上每条 posting 都要做一次随机 map 查找，
10 万篇规模的 pprof 显示 `docLengthLocked` 占 **37.4%** 累计耗时。
换成按 DocID 下标的切片后，字段维度可以在循环外取一次。

【风险】上述 **37.4%** 是**代码注释中记录的 pprof 数字**，本次分析未复现
（需要 10 万篇语料与 pprof 采集）。纪录片引用时必须标注「项目自测」。
同类数字还有注释中的「0.7ms / 400KB / 1 万次分配」（`Postings` 的深拷贝代价）
与「p50 105ms、p99 401ms」（`Stats()` 全量遍历的代价）。

### 2.2 一致性快照：`View`

```go
func (ix *InvertedIndex) View(fn func(v *View)) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	fn(&View{ix: ix})
}
```

【已验证】`View` 解决的问题写在注释里且逻辑成立：`Scan` 这类零拷贝接口各自持锁，
同一 goroutine 反复调用不会死锁，但两次调用之间可能插入写操作，
于是整次检索看到的是**不同时刻**的索引状态——对 AND 查询会产生
「文档 A 在前一个词条的 posting 里、却不在后一个里」这种无法向用户解释的结果。
`View` 把整次检索放进同一把读锁，同时保持零拷贝。

【已验证】代价也写清楚了：`fn` 执行期间写操作被阻塞。
`pkg/tsh/search.go` 因此把高亮渲染挪到 `View` 之外，注释原话是
「否则会一直占着索引读锁、把写请求全部堵住」。

### 2.3 单调游标：`PostingCursor`

```go
type PostingCursor struct {
	list []Posting
	at   int
}
func (c *PostingCursor) Seek(id DocID) bool {
	for c.at < len(c.list) && c.list[c.at].DocID < id {
		c.at++
	}
	return c.at < len(c.list) && c.list[c.at].DocID == id
}
```

【已验证】游标只能前进不能回退，这正是归并求交/求并需要的访问模式：
每个 posting 最多被访问一次、摊还 O(1)、顺序访问。
它是 `PostingFor`（二分查找）的反面；注释记录了改用游标的理由：
「二分查找每次要在几万条记录里随机跳约 15 次，
实测是短语查询的首要瓶颈（pprof 显示 `BinarySearchFunc` 占 13.7%、
伴随的字符串 map 查找占 11.2%）」。

【风险】13.7% / 11.2% 同为注释中的历史 pprof 数字，本次未复现。

---

## 3. 索引构建流程（写入一篇文档）

【已验证】完整调用链，行号取自当前代码：

```
Table.Upsert(doc)                        pkg/tsh/typed.go
  └─ InvertedIndex.Upsert(external, fields)        internal/index/index.go:385
       ① prepare(external, fields)                 index.go:264   ← 在写锁之外
       │    ├─ 校验：外部 ID 非空 / 字段数 1..maxFields / 字段名非空
       │    ├─ 字段名 slices.Sort(names)           index.go:289   ← 保证同输入同布局
       │    ├─ snapshotKinds(fields)               index.go:243   ← 短暂持读锁取类型后释放
       │    ├─ 逐字段 fieldTokens(kind, raw)       index.go:493
       │    │     ├─ Numeric  → 不进倒排，走数值列
       │    │     ├─ Keyword  → 整值一个 token（位置 0，区间覆盖整个值）
       │    │     └─ 其它     → analyzer.Analyze(raw)
       │    └─ 累计 token 数，超 maxTokens 则报 ErrDocumentTooLarge
       ② ix.mu.Lock()                              index.go:391   ← 从这里开始持写锁
       ③ removeLocked(old)（仅 Update/Upsert 命中已存在文档时）  index.go:592
       ④ insertLocked(p)                            index.go:445
       │    ├─ nextID++ → 新 DocID
       │    ├─ 写 docs / byExternal
       │    ├─ 每字段：fieldSet、fieldTotalTokens、fieldDocs、setDocFieldLenLocked
       │    │           insertFieldLocked(...)      index.go:542
       │    │     ├─ slices.SortStableFunc(tokens, by Term)   ← 稳定排序，保住位置递增
       │    │     ├─ 顺序扫描分组：同词条的多个位置合成一个 Posting
       │    │     └─ pl.Postings = append(...)；pl.DF++
       │    └─ 数值字段写入 numColumns
       ⑤ notifyApply(changeFor(p))                  index.go:427   ← 仍在写锁内
       ⑥ defer ix.mu.Unlock()
```

### 3.1 三个关键设计点（适合做动画）

**（a）分析在锁外，写入在锁内。**

`prepare` 刻意跑在写锁之外（注释原文：「把分析与写入拆开，
是为了让分词这段 CPU 密集工作跑在写锁之外」），因此并发写可以并行分词。
代价是 `prepare` 只能靠 `snapshotKinds` 短暂读一次 schema 快照。

**（b）稳定排序是短语查询正确性的前提。**

```go
slices.SortStableFunc(tokens, func(a, b analyzer.Token) int {
	return strings.Compare(a.Term, b.Term)
})
```

【已验证】注释解释了为什么必须「稳定」：同一词条的多个位置原本就按位置递增，
稳定排序保证分组后 `Positions` 仍然递增——短语查询靠 `containsPosition` 做二分查找，
一旦乱序会找不到。本次分析对真实索引断言 `Positions 严格递增` 为 true。

**（c）`OnApply` 钩子在写锁内调用。**

【已验证】注释给出三条约定：必须足够快；回调失败时索引变更**不回滚**
（语义上理解为「本次写未提交」）；回调失败后调用方应进入降级状态拒绝后续写。
`pkg/tsh` 的持久化日志正是挂在这个钩子上，从而保证「索引生效顺序 == 日志顺序」。

【已验证】另有实测事实支持这个设计：三重子代理实验显示，
在 `OnApply` 内调用 `ix.DocCount()` 会被阻塞 **501 ms**（当 `OnApply` 睡眠 500 ms 时），
证明它确实运行在写锁内。

【风险】`OnApply` 失败不回滚这一点有可观测后果：
实验显示 `Add` 返回 `id=0 + 错误`，但 `DocCount()==1`、`Has(d1)==true`、`DocFreq==1`。
这与注释描述一致（属于**已声明的**语义），但若视频要表现「写入失败」画面，
必须同时说明索引里已经有数据。

---

## 4. 一次查询的完整执行流程

以 `GET /api/v1/search?q=posting%20AND%20list&limit=10` 为例。

```
① net/http ServeMux                     pkg/httpapi/server.go:193
   mux.HandleFunc("GET /api/v1/search", s.handleSearch)
      │
② handleSearch                           pkg/httpapi/search.go:41
   ├─ 从 URL query 取 q / fields / limit / offset / sort / highlight
   └─ 构造 tsh.SearchRequest
      │
③ Table.Search(req)                      pkg/tsh/search.go:90
   ├─ query.Parse(req.Query, t.popts)     internal/query/parser.go:59
   注释：解析器**不做分词**，产出的词条是原始文本；分词由执行阶段用索引自己的
   Analyzer 完成。这是硬性要求，否则会出现「文档明明存在却检索不到」。
   │    词法 scan()   parser.go:135
   │      ├─ 处理 ( ) " - 与 field: 前缀
   │      ├─ scanFieldPrefix/scanFieldRange/scanComparison
   │      └─ AND/OR/NOT 关键字（大小写不敏感）
   │    语法 parseOr → parseAnd → parseClause → parseAtom
   │      └─ combine() 把 notNode 搬进 Bool.MustNot
   ├─ t.search.Search(node, opts)          internal/query/searcher.go:86
   └─ 之后在 View 之外做高亮
      │
④ Searcher.Search                        internal/query/searcher.go:86
   ├─ clampLimit / clampOffset            searcher.go:216 / 227
   ├─ s.ix.View(func(v *index.View){ ... })   ← 整次求值在同一把读锁内
   │    ├─ resolveFields(v, opts.Fields)  searcher.go:239
   │    └─ s.eval(v, n, fields)           searcher.go:431
   │         └─ *Bool → evalBool          searcher.go:664
   │              ├─ 逐子句 eval（Term/Phrase/FieldTerm/FieldRange）
   │              ├─ Must 非空 → intersectAll(must)    searcher.go:343
   │              ├─ Should     → addExisting(&out, unionAll(should))  searcher.go:408
   │              └─ MustNot    → subtract(out, unionAll(mustNot))     searcher.go:390
   └─ 锁外：scoring.NewTopK(offset+limit)  searcher.go:152
      │
⑤ Table.Search 取文档 + 高亮             pkg/tsh/search.go:127
   ├─ 再开一次 View：v.Document(hit.ID) 取深拷贝（廉价）
   └─ View 之外：highlight.Highlight(text, terms)
      │
⑥ 返回 SearchResult{Total, Hits}          pkg/tsh/search.go:158
```

### 4.1 求值器的核心约定：`hitList` 按 DocID 升序

```go
type hitList struct {
	ids    []index.DocID
	scores []float64
}
```

【已验证】注释解释了为什么不用 `map[DocID]float64` 当累加器：
posting 列表本身按 DocID 有序，单字段求值天然产出有序结果，
于是求交/求并/求差全部可以走双指针归并——O(n)、无哈希、顺序访问。
注释记录的改造前后对比是「2 万条命中就是 4 万次 map 读写，
`SearchAnd` 实测 26ms」。

【风险】26ms 为注释中的历史实测，本次未复现。

【已验证】不变量「所有返回 hitList 的求值函数都必须保证 ids 严格升序」由三处支撑：
`insertFieldLocked` 只 append、`removeLocked` 原地摘除、`SortedDocIDs` 显式 `slices.Sort`。

### 4.2 短语查询的判定

```
evalPhraseTokens(tokens, fields)         searcher.go:526
  ├─ 逐字段（位置只在字段内有意义）
  ├─ 任一 token 在该字段 df==0 → 整个短语不可能命中，跳过该字段
  ├─ IDF = Σ 各 token 的逐字段 IDF（短语没有自己的倒排表）
  ├─ order 按该字段的 DF 升序 → order[0] 作 driver 游标
  ├─ 主循环：driver 停在 docID 上，其余游标 Seek(id) 单调推进
  └─ all 命中 → countPhrase → phraseFits → containsPosition（二分）
```

【已验证】判定用的是**相对位置差**：

```go
want := int64(start) + int64(t.Position) - int64(base)
```

【已验证】实测后果（文档集合：`gap` 含 `"quick the brown fox"`，`adj` 含 `"quick brown fox"`）：

| 查询 | 结果 |
| --- | --- |
| `"quick the brown"` | 命中 `gap`，不命中 `adj` |
| `"quick brown"` | 命中 `adj`，不命中 `gap` |
| `quick brown`（隐式 OR） | 两者都命中 |

【已验证】`phraseFits` 只在同一字段内比较位置，注释说明了原因：
各字段位置都从 0 开始，跨字段拼位置会造出根本不存在的短语。

【风险】`phraseFits` 里 `want`（int64）在只检查 `want < 0` 后被转成 `uint32`。
子代理构造了一个符合「位置严格递增」契约的自定义分析器，
让 `want = 5_500_000_000` 溢出成 `1_205_032_704`，从而使一个不存在的短语
返回 `total=1`。**内置分析器用 token 序号，需要约 12 亿个位置才可能触发**，
因此本条属于「有依据的风险」而非现实缺陷。

### 4.3 三路分流的字段子句

【已验证】`evalFieldTerm` 的分流依据是**字段在索引里的类型**，不是查询写法：

| 字段类型 | 处理 | 得分 |
| --- | --- | --- |
| `number` / `date` | 等值，等价于 `[v TO v]`，走数值列扫描 | 恒为 0 |
| `keyword` | `index.KeywordTerm(text)` 精确匹配一个整值词条 | BM25 分数 |
| `text` / 未声明 | 交给分析器，1 个 token 走 `evalSingleTerm`，多个走短语 | BM25 分数 |

【已验证】范围查询对非数值字段**必须报错**而不是静默返回空。
实跑输出：

```
query = title:[1 TO 2]
ERROR code=BAD_REQUEST
msg=index: 字段不是数值或时间类型: 字段 "title" 是 text 类型，只能对 number/date 字段做范围查询
```

### 4.4 布尔求值语义

【已验证】三块逻辑：

| 情形 | 实现 | 语义 |
| --- | --- | --- |
| Must 非空 | `intersectAll(must)` 后 `addExisting(&out, unionAll(should))` | Should 只加分，**不引入新文档** |
| Must 为空、Should 非空 | `unionAll(should)` | 经典 OR |
| 只有 MustNot | `v.SortedDocIDs()` 全量起步，再 `subtract` | 全量文档减排除项 |

【已验证】实测证据（语料 `d1=alpha beta`、`d2=alpha gamma`）：
`posting AND list` 只返回同时含两者的文档；
`posting AND NOT list` 返回含 posting 但不含 list 的文档；
`-list` 返回除 list 命中外的全部文档且分数为 0。

【已验证】`-list` 这类查询的成本是可测量的：子代理在 20 万篇规模实测
`-absent` 21.0 ms vs 普通词条查询 2.0 ms，因为 `SortedDocIDs` 做了一次 `slices.Sort`（O(N log N)）。

【已验证】一个容易惊讶的语义：否定子句出现在 OR 分支里会被**分组**。
`-a OR b` 解析成 `SHOULD(MUSTNOT(a) b)`，因此一个「既不含 a 也不含 b」的文档
会因为满足 "not a" 而被返回。子代理实测 `-a OR b` → `[d2 d4 d3]`（d3 只含 c），
而 `-a -b` → `[d3]`。这是真实、可复现、且非常适合纪录片讲解的语义差异。

---

## 5. 并发模型

【已验证】层次：

- **同一把 `sync.RWMutex` 保护整份索引。** 写操作（`Add`/`Update`/`Upsert`/`Delete`/`DeclareField`）
  持写锁；读操作持读锁。
- **`View(fn)` 在读锁内执行整次检索**，保证快照一致且零拷贝。
- **`Engine` 另有一把 `mu` 保护表映射**；注释声明两级锁互不嵌套。
- **分析器无状态、可并发调用**（`StandardAnalyzer` 构造后只读；
  gse 的 `Segment`/`CutSearch` 经 `-race` 验证）。

【已验证】本次实跑 `go test -race ./internal/... ./pkg/...` 全部 ok（退出码 0）；
项目门禁 `scripts/check.ps1` 亦含 `-race` 并全绿。
子代理另写了 4 写 × 200 次 + 4 读 × 200 次的并发用例，`-race` 通过，`Docs=800`。

【已验证】两条硬性陷阱，都有实测：

1. **`fn` 内调用写方法会死锁。** `ix.Add` 放进 `ix.View` 里，2 秒内不返回。
   更严重的是：卡住的 goroutine 持有读锁并等待写锁，
   之后**新的读者也会被阻塞**（Go 的 `RWMutex` 写者优先），
   一次误用就让整份索引（含读）冻结。
2. **`OnApply` 在写锁内**，实测阻塞 501 ms（见 3.1）。

【风险】`Searcher.Search` 在整个求值期间持有 `View`，
因此慢查询会阻塞全部写请求。这是注释里明确承认的取舍。

---

## 6. 持久化对架构的影响

【已验证】持久化走 `Options.OnApply` 钩子，因此**不需要第二套写入路径**：

```
写请求 → InvertedIndex 写锁 → 变更生效 → notifyApply(Change) → WAL 追加
                                                                  └─ 批量 fsync
```

【已验证】`Change` 结构只描述**结果**不描述过程：
「Add / Update / Upsert 产生的都是『这个 ID 现在的字段是这些』，
因此重放时一律按 Upsert 处理即可」。另有 `Kinds` 字段携带字段类型，
原因是动态映射「首次出现的类型即为该字段类型」，
若日志只存文本，重启重放时数字会退化成文本。

【已验证】设计立场写得很直接：**索引是派生数据，原文才是权威数据**。
因此换分析器、改索引格式、修索引 bug 之后都能从原文重建。

【已验证】压实（compaction）利用了一个前面提过的性质：
`EachDocument` 传给回调的 `*Document` 是索引内部那一份且**不会被索引修改**
（`Upsert` 覆盖时创建新对象，旧对象只是从映射里摘掉），
所以可以「在锁内取证，在锁外慢慢序列化」，避免把写请求停住。

---

## 7. 值得做成动画的三张图

1. **写入路径图**：原文 → 分析器（位置语义）→ 按词条分组 → posting 列表 append；
   高亮标注「分析在锁外 / 写入在锁内」这条分界线。
2. **归并求交图**：两条按 DocID 升序的 posting 列表 + 两个游标，
   动画展示 `mergeIntersect` 的双指针推进；叠加上层 `Must/Should/MustNot` 三色。
3. **短语判定图**：查询 `"quick the brown"` 分析成 `quick@0 / brown@2`（`the` 占位但被过滤），
   并与两个文档的位置序列对比，直观展示「位置差必须等于 2」为何能排除相邻写法。

---

## 8. 本文件的事实边界

- 【已验证】全部结构、字段、函数签名与行号引用：来自当前源码。
- 【已验证】全部实跑输出：来自 `docs/video` 素材程序与 `curl` 实测（见 `demo-cases.md`）。
- 【风险】标注为「注释中的历史 pprof 数字」者（37.4%、0.7ms/400KB、
  p50 105ms/p99 401ms、26ms、13.7%/11.2%）：**本次分析未复现**，
  引用时必须注明来源是代码注释或 `docs/PERFORMANCE.md` 的项目自测。
- 【推测】本文件没有使用【推测】级结论。
