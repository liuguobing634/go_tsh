# 代码 Review：值得讲解的 6 个真实案例

> 第一期技术纪录片素材 · 阶段一产物
> 每个案例包含：文件路径、核心代码、设计目的、潜在风险、验证方式、实际结论。
> 结论按【已验证】/【风险】/【推测】三级标注。**本文件不含任何编造的 bug 或测试结果**；
> 所有「实际结论」都对应本次分析中实跑得到的输出，未复现的一律标注为风险。

素材中的每个案例都可独立成段。若纪录片只选 3 个，建议选 **案例 1、案例 3、案例 4**。

---

## 案例 1：倒排索引与词项位置的数据结构

- **文件**：`internal/index/index.go`、`internal/index/types.go`、`internal/index/view.go`
- **关键词**：`TermKey`、`Posting`、`PostingList`、`docFieldLens`、`View.Scan`

### 核心代码

```go
// index.go:197 —— 用 NUL 分隔，把「字段隔离」压进一个扁平 map
func TermKey(field, term string) string {
	return field + "\x00" + term
}

// types.go:60 —— posting 带位置信息，这是短语查询的全部依据
type Posting struct {
	DocID     DocID    `json:"doc_id"`
	TF        uint32   `json:"tf"`
	Positions []uint32 `json:"positions"`
}

// index.go:551 —— 稳定排序后顺序扫描分组，把同词条的多个位置合成一个 Posting
slices.SortStableFunc(tokens, func(a, b analyzer.Token) int {
	return strings.Compare(a.Term, b.Term)
})
```

### 设计目的

1. **一个扁平 map 同时实现字段隔离与字段级 DF。** 用 `"field\x00term"` 当 key，
   于是 `title` 与 `body` 里同一个词的文档频率天然各算各的，不需要嵌套 map。
   NUL 的选择有明确理由：分析器产出的词条只含字母、数字与撇号，永不含 NUL，
   因此不会出现 `"a:b"+"c"` 与 `"a"+"b:c"` 撞车。
2. **位置信息是短语查询的前提。** `Positions` 严格递增，`containsPosition` 对它做二分查找。
3. **稳定排序保住位置递增。** 同词条的多个位置原本按位置递增，
   稳定排序保证分组后仍然递增——一旦乱序，二分查找会找不到任何位置。

### 潜在风险

【风险】「`Positions` 严格递增」是**被保持**而不是**被强制**的。
`SortStableFunc` 只按 `Term` 比较，分组内的顺序等于分析器产出的顺序。
子代理构造了一个「同一个 term 按位置递减产出」的自定义 Analyzer，
索引会存下 `Positions=[5 1]`，而 `containsPosition` 是严格下界二分，
于是**两个位置都找不到**。内置分析器不会这样产出，
因此这是「有依据的风险」而不是现存缺陷。

【风险】`docFieldLens` 的切片按 2 倍扩容且**删除时不缩容、条目也不回收**。
本次实测：加 2 篇文档再全删，`Stats().IndexBytes` 停在 **256**
（即 `64 × 4` 字节的空壳切片），文档数与词条数都已归零。

### 验证方式

```powershell
# 1) 真实 posting 列表与不变量
go run ./scratch/demo-prog          # 本次分析用的素材程序，输出见 demo-cases.md

# 2) 删除后统计残留
#    (见 code-review.md 附录的验证程序)
```

### 实际结论

【已验证】本次对真实索引断言两条不变量，均为 true：
`Postings 按 DocID 升序`、`TF == len(Positions) 且 Positions 严格递增`。

【已验证】真实 posting 输出（4 篇小语料）：

```
body.posting  df=3
    doc=d1   tf=1 positions=[19]
    doc=d3   tf=1 positions=[11]
    doc=d4   tf=1 positions=[18]
body.list  df=2
    doc=d1   tf=3 positions=[8 15 20]     <- 同文档 3 次，TF=3，位置递增
    doc=d3   tf=1 positions=[12]
title.inverted  df=1
    doc=d1   tf=1 positions=[0]
```

【已验证】删除全部文档后 `docs=0 terms=0` 但 `index_bytes=256`。

---

## 案例 2：BM25 公式实现与边界条件

- **文件**：`internal/scoring/bm25.go`（全文仅 **49 行**）
- **关键词**：`IDF`、`Score`、`DefaultBM25`、`NewSearcher`

### 核心代码

```go
func (bm BM25) IDF(docFreq, docCount uint32) float64 {
	if docCount == 0 {
		return 0
	}
	return math.Log(1 + (float64(docCount)-float64(docFreq)+0.5)/(float64(docFreq)+0.5))
}

func (bm BM25) Score(idf float64, tf uint32, docLen, avgDocLen float64) float64 {
	if tf == 0 || avgDocLen <= 0 {
		return 0
	}
	norm := 1 - bm.B + bm.B*(docLen/avgDocLen)
	return idf * (float64(tf) * (bm.K1 + 1)) / (float64(tf) + bm.K1*norm)
}
```

### 设计目的

- **采用 Lucene 的平滑 IDF** `ln(1 + (N-df+0.5)/(df+0.5))` 而不是经典的
  `ln((N-df+0.5)/(df+0.5))`。注释给出了理由：经典写法在 `df > N/2` 时给出**负分**，
  把命中该词的文档压到完全没命中的文档之下——「这是很反直觉的排序 bug」。
- **本包只做纯计算。** 文档数、文档频率、平均长度全部由调用方传入，
  因此 `scoring` 不依赖 `index`，可以独立测试（覆盖率 **100.0%**，本次实测）。
- **IDF 按字段分别计算。** 同一个词在 `title` 与 `body` 里的稀有程度完全不同。

### 潜在风险

【风险 · 有代码依据】`IDF` 的「恒为正」只在 `df <= N` 时成立。
本次直接调用实测：

```
IDF(df=5,    N=5)    = +0.087011
IDF(df=6,    N=5)    = -0.080043     <- 变负
IDF(df=1000, N=5)    = -5.116496
```

`df > N` 在内置路径需要「posting 比存活文档多」。本次分析确认了**存在**制造这种状态的
方法（见案例 6 的 ghost posting），但同时也确认了**用户可见路径不会因此返回错文档**
（ghost 命中的 DocID 不在文档表里，会在取文档阶段被跳过）。
因此本条应表述为「IDF 的恒正性依赖 `df <= N` 这个未加断言的前提」，
**不应**说成「线上会出现负分结果」。

【风险】参数没有校验。`Score` 只检查 `tf == 0 || avgDocLen <= 0`，
不检查 `0 <= B <= 1`。子代理暴力枚举参数发现存在使分母恰好为 0 的有限参数组合
（如 `K1=2, B=2, tf=1, docLen/avgDocLen=0.25`），返回 `+Inf`。
`pkg/tsh/table.go` 会把用户传的 `Options.BM25` 原样传下去，没有范围校验。

【风险 · 本次实测】`NewSearcher` 只在 `K1` 与 `B` **同时**为 0 时才套用默认值：

```go
if bm.K1 == 0 && bm.B == 0 {
	bm = scoring.DefaultBM25()
}
```

因此只设其中一个字段会静默改变公式含义。本次实测（同一索引，短文档 vs 长文档）：

| 参数 | 短文档 | 长文档 | 说明 |
| --- | --- | --- | --- |
| `{K1:1.2, B:0.75}` | 0.267405 | 0.324127 | 正常 |
| `{K1:2, B:0}` | 0.182322 | 0.437572 | 长度归一化被关掉 |
| `{K1:0, B:0.75}` | 0.182322 | 0.182322 | 词频饱和被关掉，两篇同分 |

### 验证方式

```powershell
# BM25 边界与真实语料的逐项打分（输出见 demo-cases.md 第 3 节）
go run ./scratch/demo-prog

# 部分参数的行为对比（见本文件附录「验证程序」）
```

### 实际结论

【已验证】真实语料上的逐步计算（与 `Score` 返回值一致）：

```
field="body" N=4 df=3 idf=0.356675 avgdl=11.2500
    doc=d1   tf=1 dl=11   norm=0.9833  ->  score=0.359947
    doc=d3   tf=1 dl=9    norm=0.8500  ->  score=0.388458
    doc=d4   tf=1 dl=11   norm=0.9833  ->  score=0.359947
```

注意 `d3` 文档更短（`dl=9`）因此 `norm` 更小、得分更高——长度归一化确实在起作用。

【已验证】边界条件直接调用结果：
`IDF(df=5,N=0)=0`、`Score(tf=0,…)=0`、`Score(avgDocLen=0)=0`、
`Score(tf=1,dl=avgdl)=1`、`Score(tf=1,dl=10×avgdl)=0.21359223300970878`。

【已验证】`internal/scoring` 测试覆盖率 **100.0%**，
但 `scoring_test.go` 的 `TestIDFIsAlwaysPositive` 只测到 `df <= N`，
**无法**捕获 `df > N` 的情形——这是一个「名字比覆盖范围更宽」的测试。

---

## 案例 3：短语查询的顺序、位置与匹配逻辑（推荐入镜）

- **文件**：`internal/query/searcher.go`（`evalPhraseTokens` 526、`countPhrase` 611、`phraseFits` 628、`containsPosition` 650）
- **关键词**：相对位置差、单调游标归并、单字段约束

### 核心代码

```go
// searcher.go:635 —— 判定依据是「与查询侧相同的位置差」，不是位置差 1
want := int64(start) + int64(t.Position) - int64(base)
if want < 0 {
	return false
}
if !containsPosition(cursors[i].Positions(), uint32(want)) {
	return false
}
```

### 设计目的

这是整个项目里「一个小决定带来一个正确行为」的最佳示例。
查询 `"quick the brown"` 经过分析器变成 `quick@0 / brown@2`——
`the` 是停用词被过滤，**但位置仍被占用**。于是：

- 文档 `"quick the brown fox"`（`quick@0, brown@2`）→ 命中
- 文档 `"quick brown fox"`（`quick@0, brown@1`）→ **不命中**

若按朴素的「位置差为 1」实现，结论会完全反过来。
代码注释明确点出了这一点，`searcher_test.go:376`（`TestPhraseUsesAnalyzerPositionGaps`）
也把它固定成了回归测试。

### 潜在风险

【风险】`want` 是 `int64`，只检查了 `want < 0` 就转成 `uint32`。
子代理构造了一个符合「位置严格递增」契约的自定义分析器，
令 `want = 5_500_000_000`，截断为 `1_205_032_704`，从而让一个不存在的短语返回 `total=1`。
内置分析器使用 token 序号，需要约 12 亿个位置才可能触发，
因此这是**契约边界上的风险**，不是现实缺陷。

【已验证】短语得分与单词条得分不在同一量纲：
短语的 IDF 是各 token 的 IDF **之和**（注释中说明这是通行做法，因为短语没有自己的倒排表），
而 TF 是短语出现次数。子代理实测 `p` = 0.4521、`"p q"` = 0.9041（恰好 2 倍）。

### 验证方式

```powershell
go run ./scratch/demo-prog      # 「Phrase matching is by relative position」一节
go test ./internal/query/ -run TestPhraseUsesAnalyzerPositionGaps -v
```

### 实际结论

【已验证】本机实跑（`gap` = `"quick the brown fox"`，`adj` = `"quick brown fox"`）：

```
"quick the brown" -> total=1 ids=[gap]      <- 只有保留位置差的文档命中
"quick brown"     -> total=1 ids=[adj]      <- 只有真正相邻的文档命中
quick brown       -> total=2 ids=[gap adj]  <- 隐式 OR，两者都命中
```

【已验证】分析器侧的直接证据（真实输出）：

```
"quick the brown"  -> quick@0 brown@2   (n=2)   <- the 占用了位置 1
"quick brown"      -> quick@0 brown@1   (n=2)
```

【已验证】工程侧另有可讲的一笔：短语求值从「扫锚点 + 逐文档二分反查」
改成**游标归并**后，每个 posting 只被访问一次且是顺序访问。
注释记录的改造理由是 pprof 显示 `BinarySearchFunc` 占 13.7%、
伴随的字符串 map 查找占 11.2%。
【风险】这两个百分比是注释中的历史 pprof 数据，本次未复现。

---

## 案例 4：布尔查询的语义与 posting list 运算

- **文件**：`internal/query/searcher.go`（`evalBool` 664、`unionAll` 299、`intersectAll` 343、`mergeIntersect` 370、`subtract` 390、`addExisting` 408）
- **关键词**：hitList 升序不变量、双指针归并、Must 非空时 Should 只加分

### 核心代码

```go
// searcher.go:264 —— 命中集合按 DocID 升序，整条归并体系都建立在这个不变量上
type hitList struct {
	ids    []index.DocID
	scores []float64
}

// searcher.go:691 —— 三种布尔形态
switch {
case len(must) > 0:
	out = intersectAll(must)
	addExisting(&out, unionAll(should))   // Should 只给已有文档加分
case len(should) > 0:
	out = unionAll(should)
default:
	ids := v.SortedDocIDs()               // 只有否定子句：全量起步
	...
}
```

### 设计目的

1. **不用 `map[DocID]float64` 当累加器。** 注释记录的原因：posting 本身按 DocID 有序，
   单字段求值天然有序，于是求交/求并/求差全部走双指针归并——O(n)、无哈希、顺序访问。
   注释中记录了改造前「2 万条命中就是 4 万次 map 读写，`SearchAnd` 实测 26ms」。
   【风险】26ms 为注释中的历史实测，本次未复现。
2. **`intersectAll` 从最短集合开始**，尽早把中间结果收敛到空。
   子代理验证了结果正确性：`x AND y AND z` 的得分恰好等于 `x + y + z`（差值为 0）。
3. **`addExisting` 只加分不引入新文档**，这就是「Must 存在时 Should 退化为加分项」的实现。

### 潜在风险

【已验证】**重复子句会重复计分。** 解析器不做去重，归并函数把分数相加，本次实测：

| 查询 | AST | 得分（同一个文档） |
| --- | --- | --- |
| `alpha` | `alpha` | 0.1823215568 |
| `alpha AND alpha` | `MUST(alpha alpha)` | 0.3646431136（恰好 2×） |
| `alpha AND alpha AND alpha` | `MUST(alpha alpha alpha)` | 0.5469646704（3×） |
| `alpha OR alpha` | `SHOULD(alpha alpha)` | 0.3646431136（2×） |

单次查询内部的排序不受影响，但绝对分数会变——若调用方拿分数做阈值判断，
或在两次查询之间比较分数，行为会静默改变。

【已验证】**否定子句在 OR 分支里会被分组。** `-a OR b` 解析为 `SHOULD(MUSTNOT(a) b)`。
子代理实测：文档 `d1=a`、`d2=b`、`d3=c`、`d4=a b` 时，`-a OR b` → `[d2 d4 d3]`，
即 **d3 两个子句都不满足却被返回**，因为它满足 "not a"。
对照：`-a -b` → `[d3]`。同一批文档、看起来相似的写法，语义完全不同。
这是 AST 形状决定的，非常适合在纪录片里用一张对比图讲清楚。

【已验证】只有否定子句时代价是 O(N log N)：`SortedDocIDs` 会 `slices.Sort`。
子代理在 20 万篇实测 `-absent` 21.0 ms vs 普通词条 2.0 ms。
（本次分析未复现该延迟数字，仅记录子代理实测结果。）

【风险】`evalBool` 会**先求值所有子句**（没有短路），
`intersectAll` 里的 `acc.len() == 0` 只在归并阶段提前退出，省不下子句求值。

### 验证方式

```powershell
go run ./scratch/demo-prog      # 「Boolean」与「Pagination」两节
```

### 实际结论

【已验证】本机实跑（语料 `d1` 含 posting list、`d3` 含 posting list、`d4` 含 posting）：

```
"posting AND list"        ast=MUST(posting list)              total=2
"posting OR bm25"         ast=SHOULD(posting bm25)            total=4
"posting AND NOT list"    ast=MUST(posting) MUSTNOT(list)     total=1
"-list"                   ast=MUSTNOT(list)                   total=2  (score=0)
"posting AND list AND merge"  ast=MUST(posting list merge)    total=1
"(posting OR bm25) AND document"  ast=MUST(SHOULD(posting bm25) document)  total=1
```

【已验证】分页与确定性：`limit=2 offset=0 → [d1 d3]`、`limit=2 offset=2 → [d4]`，
`total` 始终是分页前的 3，与注释「Total 是命中文档总数（分页之前）」一致。

---

## 案例 5：中文分词与索引/查询处理的一致性

- **文件**：`internal/analyzer/chinese.go`、`internal/analyzer/standard.go`、`pkg/tsh/table.go`
- **关键词**：gse 接入、位置语义、子词扩展、每表一个分析器实例

### 核心代码

```go
// chinese.go:112 —— 分流：连续汉字走 gse，其余整块走英文规则
for i := 0; i < len(text); {
	r, _ := utf8.DecodeRuneInString(text[i:])
	if isHan(r) {
		start := i
		for i < len(text) { ... }                       // 吃掉连续汉字
		tokens, pos = a.analyzeHan(text[start:i], start, tokens, pos)
		continue
	}
	start := i
	for i < len(text) { ... }                           // 吃掉非汉字段
	tokens, pos = a.std.analyzeInto(text[start:i], start, tokens, pos)
}

// chinese.go:180 —— 被过滤的词同样占位，与英文侧口径一致
for i := range words {
	...
	if a.std.keep(term, utf8.RuneCountInString(term)) {
		tokens = append(tokens, Token{Term: term, Position: pos, ...})
		if !a.noSubWords {
			tokens = a.appendSubWords(tokens, word, base+w.Start(), term, pos)
		}
	}
	pos++
}
```

### 设计目的

1. **只有中文换算法，英文侧行为原样保留。** 小写化、词内撇号、全角折叠、停用词过滤
   全部复用 `StandardAnalyzer`，因此中英混排时两侧口径一致。
2. **位置语义在两种分析器之间必须一致。** 被停用词/长度规则过滤的词仍占一个位置，
   否则中英混排时位置序列会错乱、短语查询会错配。
3. **子词扩展共享父词位置**，这是 Lucene `SynonymGraphFilter` 的语义：
   这样短语查询在词级与子词级都能对齐，而不会因为插入子词把后续位置整体推后。

### 潜在风险

【已验证】**子词扩展会给出「原文里不存在的短语」命中。**
代码注释自己承认了这个取舍（`chinese.go:186-193`），本次实测确认它真实成立：

```
分析器输出：Analyze("中华人民共和国万岁")
  = 中华人民共和国@0 中华@0 华人@0 人民@0 共和@0 共和国@0 万岁@1
```

所有子词共享位置 0，于是短语 `"中华万岁"` 与 `"人民万岁"`
都能命中只含 `"中华人民共和国万岁"` 的文档——**这两个字符串在原文里并不存在**。

【已验证】同一现象在**内置词典上**就能触发，不需要自定义词典。
这是纪录片里「为了召回率牺牲精度」的最佳实物。

【已验证 · 高价值】**中文分析器会过滤单字，标准分析器不会**，
而且这两条规则写在不同地方，注释的适用范围被写宽了：

| | `Analyze("书")` | `Analyze("我买了一本书")` |
| --- | --- | --- |
| `StandardAnalyzer` | `书@0` | 逐字：我@0 买@1 了@2 一@3 本@4 书@5 |
| `ChineseAnalyzer` | `[]`（被过滤） | `一本@3`（我/买/了 被过滤但占位） |

`standard.go:16` 的注释说「该限制不作用于 CJK 单字」，这对 `StandardAnalyzer` 成立
（逐字分支不调用 `keep`），但对 `ChineseAnalyzer` **不成立**
（`chinese.go:168` 对 gse 切出的词调用了 `std.keep`）。
后果：启用中文分析器后单字查询返回 0 结果，而默认分析器能命中。
两种分析器的 CJK 单字行为**恰好相反**，且没有任何测试把这一点固定下来。

【已验证】同一处分支还使 `StandardAnalyzer` **忽略 CJK 停用词**：
`Standard(MinTokenLen=1, Stopwords=["的"]).Analyze("的")` 仍返回 `的@0`。

【已验证 · 高价值】**每个表各自加载一份中文词典。**
`pkg/tsh/engine.go:103-107` 的注释明确说分析器共用是刻意的
（「中文词典加载一次要几百毫秒与上百 MB，每个表各来一份不可接受」），
但 `pkg/tsh/table.go:64-76` 在 `idxOpts.Analyzer == nil` 时**为每张表**
调用一次 `analyzer.NewChinese(...)`。本次实测（`runtime.ReadMemStats`，两次 GC 后取样）：

| 配置 | 堆增量 | 所有表共享同一个 Analyzer 实例 |
| --- | --- | --- |
| `AnalyzerStandard`，1 表 | 0.0 MiB | true |
| `AnalyzerStandard`，3 表 | 0.0 MiB | false（实例不同，但无成本） |
| `AnalyzerChinese`，1 表 | **109.0 MiB** | true |
| `AnalyzerChinese`，3 表 | **323.7 MiB** | false |

即 **3 张表 ≈ 3 × 108 MB ≈ 324 MB**，与注释所声明的设计意图相反。
注意 `Options.Analyzer` 的类型是 `AnalyzerKind`（字符串），
**没有**接收现成分析器实例的口子，所以外部调用方只能构造多份。

### 验证方式

```powershell
go run ./scratch/demo-prog      # 「PART 2: text analysis」两节对比输出
# 每表内存：见本文件附录「验证程序」（runtime.ReadMemStats 采样）
```

### 实际结论

【已验证】真实分词对比（同一输入，两种分析器）：

| 输入 | `StandardAnalyzer` | `ChineseAnalyzer`（子词开） |
| --- | --- | --- |
| `全文搜索服务` | 全@0 文@1 搜@2 索@3 服@4 务@5（n=6） | 全文@0 搜索@1 服务@2（n=3） |
| `倒排索引与跳表` | 倒@0 排@1 索@2 引@3 与@4 跳@5 表@6 | 倒排@0 索引@1 跳表@3（n=3，位置 2 被"与"占用） |
| `大学生` | 大@0 学@1 生@2 | 大学生@0 大学@0 学生@0（三个 token，同一位置） |
| `don't stop the Go search` | stop@1 go@3 search@4 | 同左（英文侧规则完全共用） |

【已验证】同一份 10 篇中文语料（9 篇自带 + 1 篇短语演示）、两种分析器的索引形态差异
（`/api/v1/stats` 实测；9 篇语料的标准对比见 `demo-cases.md` 第 2 节，结论方向一致）：

| 分析器 | docs | terms | avg_doc_len | index_bytes |
| --- | --- | --- | --- | --- |
| `standard` | 10 | 533 | 117.2 | 40,230 |
| `chinese` | 10 | 437 | 72.5 | 34,395 |
| `chinese -no-sub-words` | 10 | 426 | 70.4 | 33,750 |

逐字索引的词条数比词级索引多约 **22%**（533 vs 437），正文 token 数多约 **62%**（117.2 vs 72.5）。

【已验证】「大学 → 大学生」这类子词召回确实生效：

```
chinese  下查询 "大学"(daxue)   -> total=2
chinese  下查询 "学生"(xuesheng) -> total=2
chinese -no-sub-words 下 "学生" -> total=1   <- 只命中真正含"学生"的文档
```

【已验证】分析器一致性的强制方式：
`Searcher` 不接受 analyzer 参数，只从 `ix.Analyzer()` 取；
`Analyzer()` 只读、无 setter；`Table` 把同一个实例同时交给 `Searcher` 与 `Highlighter`。
全仓库 `NewSearcher(` 的非测试调用点只有 `pkg/tsh/table.go:102` 一处。

【风险】这个「不一致就搜不到」的失败模式**没有测试**。
`searcher_test.go:159`（`TestSearchReusesIndexAnalyzer`）只证明了复用成立，
没有构造错配的分析器对。子代理用「保留一切词条」的分析器建索引后，
用默认分析器查询 `The` 得到空结果，从而复现了失败模式的代价。

---

## 案例 6：文档存储、索引更新与错误处理（含一个真实的一致性缺陷）

- **文件**：`internal/index/index.go`（`removeLocked` 592、`fieldTokens` 493、`DeclareField` 在 `field.go:86`）、`internal/index/types.go`、`pkg/tsh/search.go`
- **关键词**：重新分析定位 posting、schema 快照 vs 当前 schema、ghost posting、Total 与 Hits 不一致

### 核心代码

```go
// index.go:587 —— 删除靠「重新分析原文」定位当初写入的 posting
//
// 这里选择「重新分析原文」来定位需要清理的 posting，而不是在文档上
// 常驻一份 term key 列表：后者在 10 万文档量级要多吃数百 MB 常驻内存。
func (ix *InvertedIndex) removeLocked(doc *Document) {
	for field, text := range doc.Fields {
		tokens, numeric := ix.fieldTokens(ix.schema[field], text)   // <- 用「当前」schema
		...
	}
}
```

而写入侧用的是**快照**：

```go
// index.go:295 —— prepare 在写锁之外，只能短暂读一下 schema 快照
kinds := ix.snapshotKinds(fields)
```

### 设计目的

「用重算代替常驻」是一个合理的工程取舍：删除是低频操作，
重算一遍分词的 CPU 成本远比每篇文档常驻一份 term key 列表（10 万文档量级数百 MB）划算。
代码注释还专门警告了这个取舍的前提：

> ⚠️ 这个函数**必须被 prepare 与 removeLocked 共用**。
> 删除时靠「重新分析原文」定位当初写入的 posting，只要两边对同一个字段
> 产出的词条有任何差异，删除就会摘不干净——留下永远不会消失的幽灵命中，
> 而症状是「删掉的文档还能搜到」，非常难查。

### 潜在风险：这个前提在 schema 变化时被破坏

【已验证】**「两边共用同一个函数」不足以保证「产出相同的词条」**，
因为函数的输入之一（字段类型）在两边取自不同时刻。

`DeclareField`（`field.go:95-103`）只拒绝与**已声明**的类型冲突；
一个从未声明过的字段在 `schema` 里没有条目，因此**可以**在第一篇文档写入之后
才被声明为 `keyword` 或 `number`。此时：

- 写入时：字段类型是「未声明 → 按 text 处理」，索引里是分词后的词条
- 删除时：字段类型是 `keyword`（整值一个词条）或 `number`（走数值列），
  于是 `removeLocked` 既找不到原来的词条，也不会去清数值列

本次实测（直接调用 `internal/index` 的 API）：

| 场景 | 删除后 `docs` | 删除后 `terms` | 残留 posting | 删除后 `index_bytes` |
| --- | --- | --- | --- | --- |
| 写入后声明 `number`，再删除 | 0 | **1**（应为 0） | `DocFreq(price,100)=1`，`[{1 1 [0]}]` | 309（对照：干净删除为 256） |
| 写入后声明 `keyword`，再删除 | 0 | 0 | 无残留 | 256 |

【已验证】**这个残留 posting 会不会变成「删掉的文档还能搜到」？**
本次用两组实验确认了答案，并且这个答案与子代理的初判不同：

- **用户可见路径不会返回幽灵文档。** 幽灵 posting 的 DocID 已经不在文档表里，
  取文档阶段会跳过它。本次在 `pkg/tsh` 引擎层实测：
  ```
  after delete-all:  total=0 hits=0
  with live doc:     total=1 hits=1 ids=[live]
  ```
  HTTP 层同理（`pkg/tsh/search.go:127-137` 对取不到的文档 `continue`）。
- **但 `total` 会被幽灵 posting 抬高。** 直接用 `internal/query` 的 Searcher 时
  `total` 统计的是**求值阶段的命中数**（含幽灵），而 `Hits` 是取文档后过滤的结果。
  子代理实测 `Total=2 但 Hits=[d2]`。本次也复现了同类现象。
  也就是说：**`Total` 与 `len(Hits)` 之间没有一致性保证**。
- 【风险】幽灵 posting 还会让 `df` 大于该字段的存活文档数 `N`，
  从而把 IDF 推向负值（案例 2）。

### 其他可讲的错误处理事实

【已验证】哨兵错误与 `errors.Is` 都工作正常（本机实跑）：

```
Add(已存在 id)    err=index: 文档已存在: "d1"        is ErrDocumentExists=true
Delete(不存在 id) err=index: 文档不存在: "nosuchdoc"  is ErrDocumentNotFound=true
Add(空 id)        err=index: 文档外部 ID 不能为空      is ErrEmptyExternalID=true
Add(无字段)       err=index: 文档至少需要一个字段      is ErrNoFields=true
Add(空字段名)     err=index: 字段名不能为空            is ErrEmptyFieldName=true
```

【已验证】超限拒绝**不留半成品**：`MaxTokens=5` 时写入 7 个 token 的文档返回
`ErrDocumentTooLarge`（`errors.Is` 为 true），之后
`docs=0 terms=0 fields=0 bytes=0`——`prepare` 只操作局部变量，没有污染索引。

【已验证】`Get` 返回深拷贝：把返回值的 `Fields["body"]` 改成 `"MUTATED"` 后，
再取一次仍是原文 `"An inverted index maps e…"`。

【风险】`View.EachDocument` 与 `View.FieldValue` **刻意返回内部引用**（零拷贝）。
子代理实测在 `EachDocument` 回调里修改 `*Document` 会改掉已存文档，
且没有拷贝、没有报错、没有竞态报告。这是注释声明的契约（「不要修改它」），
但属于「只要误用就静默损坏数据」的设计。

【风险】`OnApply` 失败不回滚：实测 `Add` 返回 `id=0 + 错误`，
但索引里 `DocCount()==1`、`Has(d1)==true`。这与 `index.go:88-94` 的注释一致。

### 验证方式

```powershell
# 1) 正常删除/更新的 ghost-free 行为
go run ./scratch/demo-prog      # 「PART 5: in-memory document store」一节

# 2) 声明时机导致的残留（见本文件附录「验证程序」）
```

### 实际结论

【已验证】**正常路径（不中途改 schema）是干净的**：
`Update` 之后旧词条搜不到，`Delete` 之后 `terms` 与 `index_bytes` 都下降，
`docs=0 terms=0`。

```
after Update(d3)   docs=4 terms=38 index_bytes=3678   (原 41 / 3897)
search merge       total=0   (更新前为 1)
after Delete(d2)   docs=3 terms=27 index_bytes=2762
search bm25        total=0   (d2 是唯一命中)
```

【已验证】**一旦在第一篇文档写入之后改变字段类型，删除就会留下残留 posting**，
但该残留**不会**让用户搜到已删除的文档；可观测后果是
`Stats().Terms` / `IndexBytes` 偏高，以及 `Total` 与 `len(Hits)` 可能不一致。

【已验证 · 影响面】`pkg/tsh` 的文档写入 API（`Create`/`Upsert`）会为每个字段
先声明 text 类型，因此**从文档 API 无法触发**这个路径
（之后 `DeclareField` 转为 `keyword` 会返回 `ErrFieldKindConflict`）。
可触发路径是直接使用 `internal/index`，或经 `Table.Index()`
（`pkg/tsh/table.go:140`）拿到原始索引。

---

## 附录：本次 Review 的验证方法

### A. 环境（必须交代，否则命令会失败）

项目自带 `.gocache` 作为工作区内的构建缓存与模块缓存。
默认 `GOMODCACHE` 在用户目录且为空，直接跑命令会尝试联网下载 `gse` 并超时：

```
internal\analyzer\chinese.go:10:2: github.com/go-ego/gse@v1.1.0:
Get "https://proxy.golang.org/...": connectex: A connection attempt failed
```

可用环境（本次全部结论均由此得到）：

```powershell
$env:GOCACHE    = 'D:\codes\golang\go_tsh\.gocache\build'
$env:GOMODCACHE = 'D:\codes\golang\go_tsh\.gocache\mod'
$env:GOPROXY    = 'off'      # 模块缓存已完整，禁止联网
```

### B. 本次实跑过的命令

| 命令 | 结果 |
| --- | --- |
| `go test ./...` | 12 个包 `ok`、`examples/blog` 无测试文件，退出码 0（共 13 个包） |
| `go test -race ./internal/... ./pkg/...` | 全部 `ok`，退出码 0 |
| `scripts/check.ps1`（`GOPROXY=off`） | `check passed`，退出码 0 |
| `gofmt -l <全部项目包目录>` | 输出为空 |
| `go vet ./...` | 退出码 0 |
| `go test -cover ./...` | 见 `project-brief.md` 第 6.2 节 |
| `go build`（4 种参数组合） | 均成功，体积见 `project-brief.md` 第 6.3 节 |
| `curl` 对真实服务 30+ 条查询 | 见 `demo-cases.md` |

三个并行子代理各自独立写了临时验证程序（针对索引/打分、查询层、分析器/存储层），
本次分析**没有直接采信**子代理结论，而是独立复现了其中影响最大的一条
（案例 6 的 ghost posting），并修正了它的严重性判断。

### C. 本次分析中「未复现」的第三方结论

以下是子代理报告、但本次分析**未独立复现**的结论。纪录片若引用，必须标注为二手证据：

| 结论 | 状态 |
| --- | --- |
| 20 万篇下单条否定查询 21.0 ms vs 普通词条 2.0 ms | 【风险】未复现 |
| 1,000,000 层括号导致 `fatal error: stack overflow` | 【已验证】本次已独立复现（见下） |
| `phraseFits` uint32 截断导致短语假阳性 | 【风险】未复现 |
| `p` vs `"p q"` 短语得分 2 倍关系 | 【已验证】量纲关系与本次实测一致（不同语料数值不同） |
| NaN 使 TopK 丢弃最高分候选 | 【风险】未复现，且需要病态配置才能产生 NaN |
| `-a OR b` → `[d2 d4 d3]` | 【风险】未逐字复现，但 AST 形状与 `evalBool` 代码支持该行为 |
| `View` 内写操作死锁 / 卡住后连读一起阻塞 / `OnApply` 持写锁 | 【已验证】本次已独立复现，见 `demo-cases.md` 第 6.4 节 |

### C2. 本次独立复现的细节

**括号深度导致栈溢出。** 在独立进程中执行（避免影响其它实验），
深度 100,000 时解析成功，深度 1,000,000 时进程直接终止：

```
fatal error: stack overflow
goroutine 1 [running]:
  query.(*parser).parseAnd(...)
  query.(*parser).parseOr(...)
  query.(*parser).parseAtom(...)
  query.(*parser).parseClause(...)
  query.(*parser).parseAnd(...)      <- 四层递归循环，重复出现数百次
  ...
```

同时实测确认 `Options.MaxClauses` **不约束括号深度**：即使设成 4，
10 万层括号仍然解析成功（`ast=alpha`）。
另实测一条配额绕过：一条 100 字的**中文**短语在 `MaxClauses=4` 下被接受，
而分析器会把它切成 **100 个 token**；等价的空格分隔短语则被正确拒绝。

### D. 本次独立复现的弃用结论

子代理初判「删除的文档会被当作命中返回（`sku:abc` → `Total=1 Hits=[{ID:1}]`）」。
本次独立复现发现：**`internal/query` 的 Searcher 确实会返回幽灵 DocID，
但 `pkg/tsh` 与 HTTP 层在取文档阶段会跳过它**，用户看不到幽灵文档。
因此纪录片**不应**把它讲成「删掉的文档还能被搜到」，
正确表述是「内部命中集合存在幽灵条目，导致 `Total` 与 `Hits` 不一致、
统计指标偏高」。
