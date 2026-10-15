# 演示用例与实测结果

> 第一期技术纪录片素材 · 阶段一产物
> 本文件所有「实际运行结果」都是本次分析在**本机真实跑出来**的原始输出，未做美化、未做推算。
> 【已验证】= 本次实跑；【风险】= 未验证；【推测】= 无证据。

---

## 1. 演示语料

### 1.1 主用例：仓库自带的 9 篇语料（`testdata/corpus/`）

【已验证】这份语料本身就适合入镜：**每篇文档讲的就是搜索引擎的一个概念**，
所以查询结果与文档标题天然自解释。

| 文件 | 字节 | 主题 |
| --- | --- | --- |
| `inverted-index.md` | 542 | 倒排索引与 posting list |
| `bm25-ranking.md` | 795 | BM25 的三个组成部分 |
| `boolean-queries.md` | 598 | AND / OR / NOT 语义 |
| `phrase-queries.md` | 636 | 短语查询与位置差 |
| `tokenization.md` | 652 | 分词、停用词与位置 |
| `latency-budget.md` | 683 | 尾延迟与性能教训 |
| `chinese-segmentation.md` | 1,264 | 中文分词（含子词扩展） |
| `chinese-inverted-index.md` | 696 | 中文倒排索引（含 posting list 术语） |
| `chinese-recall-precision.md` | 827 | 中文召回与精度（含"大学生"例子） |

共 9 个文件。导入后每个文件成为一篇文档：**ID = 文件名（不含扩展名）**，
**`title` 字段 = 文件名**，**`body` 字段 = 文件全文**
（`internal/corpus/corpus.go:86-93`）。

### 1.2 为短语演示追加的第 10 篇

短语查询的关键行为在一篇文档里讲不清楚，因为需要**两篇文档各自只含一种写法**。
演示时建议追加一篇（约 180 字节）：

```markdown
# Stop word positions

The phrase quick the brown appears here literally, with the stop word the in the middle.

The phrase quick brown also appears here, adjacent, without the stop word between them.
```

【风险 · 重要】本次分析第一次尝试时把「两种写法放进同一篇文档」，
结果 `"quick the brown"` 与 `"quick brown"` **都**命中该文档、分数还完全相同
（`total=1 score=6.0195`），**无法说明任何问题**。
要把这个行为讲清楚，必须用**两篇独立文档**，每篇只含一种写法。
第 5 节的演示已按正确做法修正。

### 1.3 启动命令（实测可用）

```powershell
# 1) 构建
go build -o bin\tshd.exe ./cmd/tshd

# 2) 启动并导入语料（默认标准分析器）
.\bin\tshd.exe -addr :8080 -import .\testdata\corpus

# 3) 需要中文分词时
.\bin\tshd.exe -addr :8080 -analyzer chinese -import .\testdata\corpus

# 4) 关闭子词扩展用于对比
.\bin\tshd.exe -addr :8080 -analyzer chinese -no-sub-words -import .\testdata\corpus
```

【已验证】启动日志（真实输出，9 篇语料）：

```json
{"msg":"语料目录已读取","dir":"D:\\codes\\golang\\go_tsh\\testdata\\corpus","files":9}
{"msg":"语料导入完成","docs":9,"terms":528,"fields":2,"avg_doc_len":127.77777777777777,"elapsed":"1ms"}
{"msg":"服务启动","addr":":18080","version":"dev"}
```

> 注意：日志是 JSON 且包含中文，PowerShell 画面里可能显示为乱码，
> 拍摄时建议用 `chcp 65001` 或直接展示 JSON 文件，或使用 `-log-level` 之外的输出方式。

---

## 2. 索引统计（可作开场镜头）

**端点**：`GET /api/v1/stats`

【已验证】9 篇语料、标准分析器：

```json
{"docs":9,"terms":528,"fields":2,"avg_doc_len":127.77777777777777,"index_bytes":39509,
 "tables":[{"name":"default","stats":{"docs":9,"terms":528,"fields":2,
 "avg_doc_len":127.77777777777777,"index_bytes":39509},"persisted":false}]}
```

【已验证】**这是推荐使用的标准对比（直接对 `testdata/corpus/` 的 9 篇语料，
不依赖任何追加文档，可完整复现）**——

| 分析器 | docs | terms | avg_doc_len | index_bytes |
| --- | --- | --- | --- | --- |
| 默认（standard，逐字索引中文） | 9 | **528** | **127.8** | 39,509 |
| `-analyzer chinese` | 9 | **432** | **78.1** | 33,674 |
| `-analyzer chinese -no-sub-words` | 9 | **421** | **75.8** | 33,029 |

> 可直接入镜的结论：**同一份语料，换成词级分词后词条数下降约 18%、
> 正文 token 数下降约 39%**（127.8 → 78.1），索引内存下降约 15%。
> 原因是逐字索引把每个汉字都建成独立词条，而词级分词同时过滤掉了单字。

【已验证】另有一组加了 1 篇短语演示文档（10 篇）的对照数据，
结论方向一致（533 / 437 / 426 个词条），可作旁证。

---

## 3. 倒排索引与评分：进程内演示（推荐的主镜头）

HTTP 查询只返回命中，看不到索引内部。要展示 posting list、位置、BM25 逐项计算，
建议用一个约 300 行的演示程序直接调用 `internal/index` 与 `internal/scoring`。
本节输出全部来自本次实跑。

### 3.1 语料与索引统计

演示语料（4 篇，刻意做小以便打印全部 posting，`title` + `body` 两个字段）：

| ID | title | body |
| --- | --- | --- |
| `d1` | Inverted index | An inverted index maps each term to the list of documents that contain it. That list is called a posting list. |
| `d2` | BM25 ranking | BM25 scores a document for a query term using inverse document frequency, term frequency saturation and length normalization. |
| `d3` | Boolean queries | A boolean query combines clauses with AND, OR and NOT. A posting list is intersected with a merge pass. |
| `d4` | Phrase queries | A phrase query needs the terms in order, inside a single field, so positions are stored with each posting. |

【已验证】索引统计：

```
docs                       4
distinct (field,term) keys 41
fields                     2
total tokens               53
avg doc len                13.2500
estimated resident bytes   3897
```

【已验证】逐字段统计（BM25 的 `N` 与 `avgdl` 来源）：

```
field "body"    docs=4 total_tokens=45 avg_len=11.2500
field "title"   docs=4 total_tokens=8  avg_len=2.0000
```

### 3.2 真实 posting 列表（核心镜头）

【已验证】

```
body.posting  df=3
    doc=d1   tf=1 positions=[19]
    doc=d3   tf=1 positions=[11]
    doc=d4   tf=1 positions=[18]
body.list  df=2
    doc=d1   tf=3 positions=[8 15 20]     <- 同文档出现 3 次
    doc=d3   tf=1 positions=[12]
body.bm25  df=1
    doc=d2   tf=1 positions=[0]
body.term  df=2
    doc=d1   tf=1 positions=[5]
    doc=d2   tf=2 positions=[7 12]        <- 同文档出现 2 次，位置递增
title.inverted  df=1
    doc=d1   tf=1 positions=[0]
```

【已验证】同一份索引上的不变量断言：

```
Postings sorted by DocID asc                                  true
TF == len(Positions) and Positions strictly increasing        true
```

> 镜头建议：`d1` 的 `list` 有 3 个位置 `[8 15 20]`，这 3 个数字就是短语查询判定
> `"posting list"` 的原始依据；`d1` 的 `posting` 在位置 19，
> 与 `list` 的位置 20 相邻——所以 `d1` 命中 `"posting list"`。

### 3.3 BM25 逐项计算

【已验证】IDF 随文档频率变化（`N = 1000`）：

```
df=1     idf=+6.503290
df=10    idf=+4.557380
df=100   idf=+2.298597
df=400   idf=+0.916041
df=500   idf=+0.693147
df=600   idf=+0.510992
df=900   idf=+0.105805
df=1000  idf=+0.000500
```

> 可直接入镜：**`df` 从 1 涨到 1000，IDF 从 6.50 单调降到 0.0005**；
> 在 `df = N` 时 IDF 接近 0 但不为负——这正是采用 Lucene 平滑写法
> `ln(1 + (N-df+0.5)/(df+0.5))` 而不是经典写法的可观测效果。

【已验证】真实语料上的完整计算链（`term = "posting"`）：

```text
field="body" N=4 df=3 idf=0.356675 avgdl=11.2500
    doc=d1   tf=1 dl=11   norm=0.9833  ->  score=0.359947
    doc=d3   tf=1 dl=9    norm=0.8500  ->  score=0.388458
    doc=d4   tf=1 dl=11   norm=0.9833  ->  score=0.359947
```

> 可直接入镜：三篇文档 `tf` 都是 1，**得分却不同**——
> `d3` 的字段更短（`dl=9` vs `11`），`norm` 更小（0.85 vs 0.9833），所以得分更高。
> 这就是「长度归一化惩罚长文档」的可视化证据。

【已验证】边界条件（直接调用）：

```
IDF(df=5, N=0)              0
Score(tf=0, ...)            0
Score(avgDocLen=0)          0
Score(tf=1, dl=avgdl)       1
Score(tf=100, dl=avgdl)     2.173913043478261
Score(tf=1, dl=10*avgdl)    0.21359223300970878
```

> 镜头建议：`tf` 从 1 涨到 100（100 倍），得分只从 1 涨到 2.17（2.17 倍）——
> 词频饱和的量化展示。

### 3.4 分页与确定性

【已验证】

```
limit=2    offset=0   total=3 page=[d1 d3]
limit=2    offset=2   total=3 page=[d4]
limit=0    offset=0   total=3 page=[d1 d3 d4]     <- limit=0 取默认 10
limit=1000 offset=0   total=3 page=[d1 d3 d4]     <- limit>100 截到 100
```

【已验证】`total` 始终是分页前的总数（3），与注释「Total 是命中文档总数（分页之前）」一致。

---

## 4. 真实 HTTP 查询与结果

以下全部为本次对真实服务的 `curl` 实测。三种服务器配置分别启动，
每组跑同一批查询，便于横向对比。

### 4.1 单词条查询

| 查询 | standard | chinese | chinese -no-sub-words |
| --- | --- | --- | --- |
| `posting` | total=4 | total=4 | total=4 |
| `bm25` | total=1 `bm25-ranking` 5.0206 | total=1 4.6603 | total=1 4.6355 |
| `idf` | **total=0** | **total=0** | **total=0** |

【已验证】`idf` 返回 0 条是正确且值得讲的一课：
`idf` 只有 3 个字母，会被分析器当作正常词条索引；
但语料里写的是 `inverse document frequency`，没有出现过独立的 `idf`。
**「查询词没有被索引」与「文档不存在」在结果上都是 0 条**——
这正是项目把「分析器一致性」写成硬性约束的原因。

【已验证】`posting` 的排序（语料 9 篇版本）：

```
total=4
  inverted-index            score=1.3077
  chinese-inverted-index    score=1.2351
  phrase-queries            score=1.0156
  chinese-segmentation      score=0.4685
```

### 4.2 布尔查询

【已验证】10 篇语料、三种分析器下的一致性表现：

| 查询 | AST / 语义 | standard | chinese | nosub |
| --- | --- | --- | --- | --- |
| `posting AND list` | 两者都要 | total=2 | total=2 | total=2 |
| `posting OR skip` | 任一即可 | total=4 | total=4 | total=4 |
| `posting AND NOT list` | 含 posting 不含 list | total=2 | total=2 | total=2 |
| `-list` | 排除 list | total=8（分数全 0） | total=8 | total=8 |
| `(posting OR skip) AND list` | 括号分组 | total=2 | total=2 | total=2 |

【已验证】`-list` 的完整返回（standard，按 DocID 升序，分数全 0）：

```
total=8
  bm25-ranking, boolean-queries, chinese-recall-precision, chinese-segmentation,
  latency-budget, phrase-queries, stopword-phrase, tokenization
scores=0,0,0,0,0,0,0,0
```

> 镜头建议：这一条最能说明「只有否定子句」的语义——
> 引擎必须从**全量文档**出发再做减法，所以结果是「除 list 命中外的所有文档」，
> 而且这些文档与查询的相关性分数是 0（它们不是因为「相关」才被返回的）。

### 4.3 短语查询与一个真实的反例

【已验证】10 篇语料下（含第 1.2 节的短语文档）：

| 查询 | standard | chinese | nosub |
| --- | --- | --- | --- |
| `"posting list"` | total=2 | total=2 | total=2 |
| `"list posting"` | **total=0** | **total=0** | **total=0** |
| `"posting the list"` | **total=0** | **total=0** | **total=0** |
| `"quick the brown"` | total=1 | total=1 | total=1 |
| `"quick brown"` | total=1 | total=1 | total=1 |

【已验证】`"posting list"` 的真实完整响应（standard 分析器，节选自原始 JSON）：

```json
{"took_ms":0,"total":2,"hits":[
  {"id":"chinese-inverted-index","score":3.3793180593143872, ...},
  {"id":"inverted-index","score":2.8496223090986907, ...}]}
```

【已验证】`"list posting"` 与 `"posting the list"` 都返回 `total=0`：
前者词序反了，后者中间多了一个词。**这两个查询的单词条都存在于语料中**
（`posting` 命中 4 篇、`list` 命中多篇），所以它们返回 0 条只能由位置判定解释。

### 4.4 短语「相对位置差」的决定性演示（推荐镜头）

要让第 4.3 节的效果无可辩驳，必须用**两篇各自只含一种写法**的文档。
【已验证】进程内演示（`gap` 含 `"quick the brown fox"`，`adj` 含 `"quick brown fox"`）：

| 查询 | 结果 | 说明 |
| --- | --- | --- |
| `"quick the brown"` | `total=1` → `gap` | 只有保留位置差 2 的文档命中 |
| `"quick brown"` | `total=1` → `adj` | 只有真正相邻的文档命中 |
| `quick brown` | `total=2` → `gap adj` | 隐式 OR，两者都命中 |

【已验证】分析器侧的原始输出（这是整条链路的起点）：

```
"quick the brown"  ->  quick@0 brown@2   (n=2)   <- the 被过滤，但占用了位置 1
"quick brown"      ->  quick@0 brown@1   (n=2)   <- 位置差是 1
```

> 镜头建议（这一段是整期纪录片的技术高潮）：
> 同一句话，去掉一个停用词 `the`，位置差从 2 变成 1；
> 短语查询比较的不是「相邻」，而是「和查询侧一样的位置差」。
> 若按朴素的「位置差为 1」实现，`"quick the brown"` 会去命中 `"quick brown"`——完全反了。

【已验证】回归测试固定了该行为：
`internal/query/searcher_test.go:376` `TestPhraseUsesAnalyzerPositionGaps`。

### 4.5 字段限定查询

【已验证】

| 查询 | 结果 | 说明 |
| --- | --- | --- |
| `title:posting` | total=0 | `title` 只有文件名，不含 posting |
| `body:posting` | total=4（与无限定时相同） | 命中的都是 body |
| `nosuchfield:posting` | total=0 | 不存在的字段被静默忽略（注释声明这是有意的） |

### 4.6 错误路径（必须实测，不能编）

【已验证】四条错误路径的真实响应：

```
q=title:[1 TO 2]
→ ERROR code=BAD_REQUEST
  msg=index: 字段不是数值或时间类型: 字段 "title" 是 text 类型，只能对 number/date 字段做范围查询

q="posting
→ ERROR code=INVALID_QUERY
  msg=query: 第 1 个字符处语法错误: 引号未闭合 (查询串 "\"posting")

q=title:[1 2]
→ ERROR code=INVALID_QUERY
  msg=query: 第 1 个字符处语法错误: 范围要写成 字段:[下界 TO 上界]，例如 price:[10 TO 100]；无界的一端写 * (查询串 "title:[1 2]")

q=title:[* TO *]
→ ERROR code=INVALID_QUERY
  msg=query: 第 1 个字符处语法错误: 范围两端不能都是 *（那等于不设条件） (查询串 "title:[* TO *]")
```

> 镜头建议：**「对文本字段做范围查询」返回 400 而不是空结果**，
> 这是项目刻意选择的行为（代码注释：「静默返回空结果会让用户以为『没搜到』，
> 而真正的原因是查询写错了——这是最难自查的一类失败」）。

---

## 5. 中文分析器对比演示（推荐镜头）

### 5.1 分词结果对比

【已验证】同一输入的两种分析器输出：

| 输入 | standard | chinese（子词开） | chinese（子词关） |
| --- | --- | --- | --- |
| `全文搜索服务` | 全@0 文@1 搜@2 索@3 服@4 务@5（n=6） | 全文@0 搜索@1 服务@2（n=3） | 全文@0 搜索@1 服务@2 |
| `倒排索引与跳表` | 倒@0 排@1 索@2 引@3 与@4 跳@5 表@6 | 倒排@0 索引@1 跳表@**3**（n=3） | 倒排@0 索引@1 跳表@3 |
| `大学生` | 大@0 学@1 生@2 | 大学生@0 大学@**0** 学生@**0**（n=3） | 大学生@0（n=1） |
| `中华人民共和国万岁` | 逐字 9 个 token | 中华人民共和国@0 中华@0 华人@0 人民@0 共和@0 共和国@0 万岁@1 | 中华人民共和国@0 万岁@1 |
| `don't stop the Go search` | stop@1 go@3 search@4 | 同左 | 同左 |

> 三个可直接入镜的观察：
> 1. `跳表` 的位置是 **3** 而不是 2——因为「与」被过滤了但**占位**。
> 2. `大学生` 的子词 `大学`、`学生` 与父词**共享位置 0**。
> 3. 英文部分（`don't`、`Go`）在两种分析器下**完全一致**——中文只换了分词算法。

### 5.2 子词扩展带来的召回与代价

【已验证】9 篇语料、三种配置的实测（`total` 与命中文档 ID）：

| 查询 | standard | chinese | chinese -no-sub-words |
| --- | --- | --- | --- |
| `大学生` | total=2 `recall-precision, segmentation` | total=2 同左 | total=2 同左 |
| `大学` | total=2 同左 | total=2 同左 | total=2 同左 |
| `学生` | total=2 同左 | total=2 同左 | **total=1** `recall-precision` |
| `生`（单字） | **total=3** `recall-precision, inverted-index, segmentation` | **total=0** | **total=0** |
| `全文` | total=2 `segmentation, recall-precision` | total=2 同左 | total=2 同左 |
| `检索` | total=3 `segmentation, inverted-index, recall-precision` | total=3 同左 | total=3 同左 |
| `倒排索引` | total=2 `inverted-index, recall-precision` | total=2 同左 | total=2 同左 |
| `跳表` | total=2 同左 | total=2 同左 | total=2 同左 |
| `分词` | total=2 `segmentation, recall-precision` | total=2 同左 | total=2 同左 |
| `文档` | total=3 `inverted-index, segmentation, recall-precision` | total=3 同左 | total=3 同左 |

> 镜头建议：`学生` 在关闭子词扩展后从 2 条降到 1 条——
> 少掉的那条（`chinese-segmentation`）正文里只有「大学生」这个整词，
> 没有独立的「学生」。这正是「整词 + 子词都进索引」用索引体积换召回率的具体体现。

> 另一个更醒目的对比：**单字 `生` 在默认分析器下命中 3 篇，启用中文分析器后命中 0 篇**
> （见 5.4 节，两种分析器对 CJK 单字的处理规则相反）。

### 5.3 子词扩展的已知代价（真实的假阳性）

【已验证】代码注释自己承认了这个取舍，本次实测确认它成立：

```
Analyze("中华人民共和国万岁") 的 token：
  中华人民共和国@0  中华@0  华人@0  人民@0  共和@0  共和国@0  万岁@1
```

由于所有子词共享位置 0，只含 `"中华人民共和国万岁"` 的文档会**同时命中**
短语 `"中华万岁"` 与 `"人民万岁"`——**这两个字符串在原文里并不存在**。

> 镜头建议：这是「为召回率牺牲精度」的最好实物。
> 强调这是**已声明的、有意的**取舍，不是 bug；
> 关闭方式就是 `-no-sub-words`。

### 5.4 两种分析器的 CJK 单字行为相反

【已验证】这是一个真实且未被测试覆盖的行为差异：

| 输入 | `StandardAnalyzer` | `ChineseAnalyzer` |
| --- | --- | --- |
| `书` | `书@0` | `[]`（被最小词长规则过滤） |
| `我买了一本书` | 逐字 6 个 token | `一本@3`（我/买/了 被过滤但占位） |

【已验证】端到端后果（9 篇语料实测）：

| 查询 | `standard` | `chinese` |
| --- | --- | --- |
| `生`（单字） | **total=3**（`chinese-recall-precision`、`chinese-inverted-index`、`chinese-segmentation`） | **total=0** |
| `书` 之类单字 | 命中该字 | 不命中 |

**同一个查询串、同一份语料，换个分析器召回结果从 3 条变成 0 条。**
这正是项目把「分析器一致性」写成硬性约束的另一个侧面：
分析器不仅影响「能不能搜到」，还直接决定「哪些长度的词根本不会被索引」。

【风险】`internal/analyzer/standard.go:16` 的注释写的是
「该限制不作用于 CJK 单字」，这句话对 `StandardAnalyzer` 成立、对 `ChineseAnalyzer` 不成立。
两边的测试各自断言了相反的行为
（`standard_test.go` 断言单字保留、`chinese_test.go` 断言单字被过滤），
**但没有任何测试把「两者行为相反」这件事本身固定下来**。

---

## 6. 需要谨慎使用的演示（有风险，不建议作为「成功案例」）

以下现象本次已验证或由代码支持，但**不适合**作为「功能正确」的展示画面。
若要使用，必须同时说明其性质。

### 6.1 括号嵌套不受子句数上限约束

【已验证】`Options.MaxClauses` 只统计子句，**不统计括号**。实测：

```
MaxClauses=4   depth=100000   -> 解析成功，AST = alpha
MaxClauses=64  depth=100000   -> 解析成功，AST = alpha
```

【已验证 · 本次独立复现】深度加到 1,000,000 时进程**直接崩溃**：

```
fatal error: stack overflow
goroutine 1 [running]:
  query.(*parser).parseAnd(...)
  query.(*parser).parseOr(...)
  query.(*parser).parseAtom(...)
  query.(*parser).parseClause(...)
  query.(*parser).parseAnd(...)     <- 四层循环递归，重复出现
  ...
```

退出码 2（在独立进程中复现，不影响其它实验）。

【风险】可达性：搜索是 GET 接口，查询串来自 URL；
Go `net/http` 默认 `MaxHeaderBytes` 为 1 MiB，
构造约 100 万个 `(`（约 1 MB）在传输层边界附近。
子代理测得 ~800 KB 的 URL 已消耗约 400 MB 临时栈、约 0.9 秒 CPU。
**本次未通过 HTTP 实测这条路径**，因此只能说「解析器本身可被击穿」，
不能说「线上可被任意用户打崩」。

> 处理建议：纪录片可以讲这个发现，但**必须**同时说明
> 它需要约 1 MB 的构造输入、且真实部署还有传输层限制；
> 正确的表述是「子句数上限没有覆盖括号嵌套深度，这是一个防护缺口」。

### 6.2 重复子句会重复计分

【已验证】实测（同一文档的分数）：

| 查询 | AST | 分数 |
| --- | --- | --- |
| `alpha` | `alpha` | 0.1823215568 |
| `alpha AND alpha` | `MUST(alpha alpha)` | 0.3646431136 |
| `alpha AND alpha AND alpha` | `MUST(alpha alpha alpha)` | 0.5469646704 |
| `alpha OR alpha` | `SHOULD(alpha alpha)` | 0.3646431136 |

> 处理建议：可以作为「解析器不做子句去重」的事实陈述；
> 但不要渲染成严重 bug——单次查询内部的排序不受影响，
> 受影响的是绝对分数（阈值、跨查询比较）。

### 6.3 否定子句出现在 OR 分支里

【已验证】`-a OR b` 会被分组为 `SHOULD(MUSTNOT(a) b)`。
子代理实测（`d1=a`、`d2=b`、`d3=c`、`d4=a b`）：

```
-a OR b  ->  [d2 d4 d3]     <- d3 两个子句都不满足却被返回
-a -b    ->  [d3]
-a AND b ->  [d2]
```

【风险】本次未逐字复现该结果，但 `evalBool` 的代码路径
（只有 MustNot 时从 `SortedDocIDs()` 全量起步）支持这个行为。

---

### 6.4 并发契约的三条实测边界

【已验证】本次用一个「goroutine + 超时」的安全复现程序得到以下输出（不会挂住 shell）：

```
=== TRAP 1: write inside View ===
  View did NOT return within 2s -> deadlock confirmed
=== TRAP 2: does the wedge block new readers? ===
  a new reader was blocked >1.5s -> the wedge freezes reads too
=== TRAP 3: OnApply runs while holding the write lock ===
  DocCount() inside OnApply blocked 500ms -> write lock IS held
=== TRAP 4: OnApply failure leaves the write applied ===
  Add returned id=0 err=simulated log write failure
  afterwards: DocCount()=1 Has(d1)=true DocFreq(body,alpha)=1
```

四条结论，全部与代码注释的声明一致：

| 现象 | 说明 |
| --- | --- |
| `View` 内调用 `Add` 死锁 | `sync.RWMutex` 不可重入，注释已警告 |
| **卡住的 View 会连读一起冻住** | 新读者也被阻塞 >1.5 秒——一次误用的影响面比想象的更大 |
| `OnApply` 确实在写锁内 | 回调里的一次 `DocCount()` 被阻塞 500 毫秒 |
| `OnApply` 失败不回滚 | `Add` 返回 `id=0 + 错误`，但索引里文档已经存在 |

> 镜头建议：第 2 条最适合做可视化——一个 goroutine 握着一把读锁去申请写锁，
> 后面所有读请求都排在门外。这是「锁不可重入」最直观的后果。

---

## 7. 演示脚本清单（可直接执行的命令）

```powershell
# ---------- 准备 ----------
Set-Location D:\codes\golang\go_tsh
$env:GOCACHE    = "$PWD\.gocache\build"
$env:GOMODCACHE = "$PWD\.gocache\mod"
$env:GOPROXY    = 'off'          # 模块缓存已完整；否则会联网超时

# ---------- 镜头 1：门禁全绿 ----------
powershell -ExecutionPolicy Bypass -File .\scripts\check.ps1
# 预期：gofmt / vet / apicheck / go test -race 四项全过，最后一行 "check passed"

# ---------- 镜头 2：构建与体积 ----------
go build -o bin\tshd.exe .\cmd\tshd
(Get-Item bin\tshd.exe).Length / 1MB                  # 约 42.36
go build -ldflags "-s -w" -o bin\tshd-slim.exe .\cmd\tshd
(Get-Item bin\tshd-slim.exe).Length / 1MB             # 约 38.95

# ---------- 镜头 3：启动并导入语料 ----------
.\bin\tshd.exe -addr :8080 -import .\testdata\corpus
# 另开一个终端：
curl.exe --noproxy "*" "http://127.0.0.1:8080/api/v1/stats"

# ---------- 镜头 4：短语查询对比 ----------
curl.exe --noproxy "*" "http://127.0.0.1:8080/api/v1/search?q=%22posting%20list%22"
curl.exe --noproxy "*" "http://127.0.0.1:8080/api/v1/search?q=%22list%20posting%22"
# 预期：前者有命中，后者 total=0

# ---------- 镜头 5：错误路径 ----------
curl.exe --noproxy "*" "http://127.0.0.1:8080/api/v1/search?q=title:%5B1%20TO%202%5D"
# 预期：HTTP 400 + "字段不是数值或时间类型"

# ---------- 镜头 6：中文对比 ----------
.\bin\tshd.exe -addr :8080 -analyzer chinese -import .\testdata\corpus
curl.exe --noproxy "*" "http://127.0.0.1:8080/api/v1/search?q=%E5%80%92%E6%8E%92%E7%B4%A2%E5%BC%95"
# 预期：命中两篇中文文档
```

> 环境提示：`--noproxy "*"` 在 Windows 上是必要的（避免系统代理拦截本地请求），
> README「故障排查」一节也提到了 PowerShell 5.1 会吃掉 JSON 引号的问题——
> 演示写文档接口时建议把 body 写进文件再让 `curl` 读。

---

## 8. 素材边界声明

- 【已验证】本文件所有 HTTP 结果与进程内输出，均由本次分析在**本机实跑**得到，
  命令与预期已整理成第 7 节。
- 语料文档是**仓库自带的** `testdata/corpus/`（9 篇）加 1 篇为演示短语而追加的文档（第 1.2 节）。
  追加文档的**内容**由本次分析编写，不是仓库原有素材。
- 【风险】标注的项（括号深度可达性、`-a OR b` 的逐字结果、
  `phraseFits` 溢出）**未在本次分析中独立复现**，引用前请复核。
- 本文件**不含**任何性能延迟数字（毫秒级）——
  唯一可引用的延迟数据在 `docs/PERFORMANCE.md`，属项目自测，须标注来源。
