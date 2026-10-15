# 技术纪录片脚本（第一期 · 初版）

> 第一期技术纪录片素材 · 阶段一产物
> 风格定位：**技术纪录片**，不是课堂教学，不是全屏录屏。
> 每个场景包含：旁白全文 / 画面描述 / 需展示的真实文件与代码 / 重点高亮区域 /
> 架构图或动画设计说明 / 预计时长 / 技术事实核验清单。
>
> **事实纪律**：本脚本中每个数字、每段输出都来自 `docs/video/demo-cases.md` 与
> `docs/video/code-review.md` 中标注【已验证】的实测记录。
> 凡未实测者一律不出现在旁白里。核验清单会逐条给出复核命令。
>
> **标注体例说明**：本文件即旁白与分镜，为保持可读性，**不在旁白正文里插入标注**；
> 每个场景末尾的「技术事实核验清单」承担标注职责——清单中每一行都必须能复核，
> 凡带 ⚠️ 的行是「不要引用 / 需特别注意」的负向约束。
> 其他五份分析类文档则在每条结论处直接标注【已验证】/【风险】/【推测】。
> 全系列证据总表见 `docs/video/evidence-index.md`。

---

## 全片结构

| 场景 | 标题 | 时长 | 累计 |
| --- | --- | --- | --- |
| S0 | 冷开场：一份 7 行的 go.mod | 0:35 | 0:35 |
| S1 | 定位：手写核心，只借一个库 | 0:50 | 1:25 |
| S2 | 倒排索引：位置是全部的秘密 | 1:30 | 2:55 |
| S3 | BM25：49 行里的两个决定 | 1:10 | 4:05 |
| S4 | 短语查询：差 1 还是差 2（高潮） | 1:35 | 5:40 |
| S5 | 布尔与并发：两条被实测证实的边界 | 1:15 | 6:55 |
| S6 | 工程纪律：测试比源码多 19% | 0:45 | 7:40 |
| S7 | 收尾：边界与未知 | 0:20 | 8:00 |

---

## S0 · 冷开场：一份 7 行的 go.mod

**时长**：0:35

### 旁白全文

> 一个全文搜索引擎，核心要有四样东西：倒排索引、相关性打分、布尔求值、短语匹配。
> 这四样通常来自三四个成熟的库。而这个项目，把它们全部手写了一遍。
>
> 它的 `go.mod` 只有 7 行。真正的运行时依赖，只有一个。

### 画面描述

- 全黑背景，白字逐行打印 `go.mod` 内容（打字机效果，每行停顿 0.4 秒）。
- 最后一行 `require github.com/go-ego/gse v1.1.0` 打印完成后，
  画面轻微放大到这一行，其余行淡出。
- 转场：一个极简的目录树从左侧滑入。

### 需要展示的真实文件与代码

`go.mod` 全文（仓库真实内容，共 7 行）：

```
module github.com/liuguobing/go_tsh

go 1.27

require github.com/go-ego/gse v1.1.0

require github.com/vcaesar/cedar v0.50.0 // indirect
```

### 重点高亮区域

- `go 1.27`（工具链版本）
- `require github.com/go-ego/gse v1.1.0`（唯一真实依赖）
- `// indirect` 注释（说明 cedar 是传递依赖，不是项目直接使用）

### 架构图或动画设计说明

极简目录树动画：`cmd/` → `pkg/` → `internal/` 三层依次展开，
`internal/` 下并列出现 `index`、`query`、`scoring`、`analyzer` 四个方块，
每个方块的下方用极小的字标出源码行数（1122 / 1345 / 113 / 382）。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| `go.mod` 共 7 行 | `Get-Content go.mod`（实测 7 行） |
| 唯一真实运行时依赖是 gse | `go list -deps ./cmd/tshd`，非标准库非本项目的包只有 gse / gse/hmm / gse/types / cedar |
| 各包源码行数 | 见 `project-brief.md` 第 6.1 节（本次实测） |

---

## S1 · 定位：手写核心，只借一个库

**时长**：0:50

### 旁白全文

> 项目最初的计划是「零第三方依赖」。前五个阶段也确实做到了。
>
> 加上中文分词的时候，这个目标被修改了。修订后的表述是：
> 核心检索路径——索引、查询、打分、HTTP——零第三方依赖，
> 只有中文分析器引入一个纯 Go 的库。
>
> 代价被量化写进了仓库：不启用中文分词时，代码路径不会被走到；
> 但二进制里始终包含那本内嵌词典，所以体积从大约 7 MB 变成了 42 MB。

### 画面描述

- 左侧画面：`internal/index`、`internal/query`、`internal/scoring`、`pkg/httpapi`
  四个包的 import 区块依次高亮，**每个区块内部只有标准库**。
- 右侧画面：`internal/analyzer/chinese.go` 的第 10 行
  `"github.com/go-ego/gse"` 单独高亮，其余包变暗。
- 底部出现两条并排的体积条：42.36 MiB 与 38.95 MiB。

### 需要展示的真实文件与代码

1. `internal/analyzer/chinese.go` 的 import 区块（含 `gse`）。
2. 任选一个核心包的 import 区块，例如 `internal/index/index.go` 开头：

```go
import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/liuguobing/go_tsh/internal/analyzer"
)
```

3. `README.md` 第 28–29 行的准确表述：
   > 现在准确的表述是：**核心检索路径（索引、查询、打分、HTTP）零第三方依赖，
   > 只有中文分析器引入 gse**。

### 重点高亮区域

- 四个核心包的 import 里**没有**任何 `github.com/...`
- `internal/analyzer` 的第三方 import **只有一行**
- 体积对比：42.36 MiB / 38.95 MiB（`-ldflags "-s -w"`）

### 架构图或动画设计说明

用「依赖收敛」动画：把所有第三方包的图标放在下端，
画箭头向上连到 `internal/analyzer/chinese.go` 一个点；
`internal/index`、`internal/query`、`internal/scoring` 的箭头全部指向标准库图标。
视觉结论：**依赖被收敛到一个点**。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| `internal/{index,query,scoring}`、`pkg/httpapi` 的第三方 import 为空 | `go list -f '{{range .Imports}}{{.}} {{end}}' ./internal/index` 等（本次实测为空） |
| `internal/analyzer` 只 import 一个第三方包 | 同上 |
| 二进制约 42.36 MiB / 38.95 MiB | `go build` + `(Get-Item …).Length`（本次实测） |
| 「不启用中文分词时代码路径不会走到」 | 属运行期事实；但**二进制始终包含词典**，勿把两者混为一谈 |
| ⚠️ 「不启用中文分词时约 6.7 MB」 | **不要引用**。本次无法复现（`chinese.go` 无条件 import gse），该数字应为引入 gse 之前的历史值 |

---

## S2 · 倒排索引：位置是全部的秘密

**时长**：1:30

### 旁白全文

> 倒排索引做的事很朴素：把「哪些词在这篇文档里」反过来，
> 变成「哪些文档有这个字」。
>
> 这个项目的索引里，真正重要的不是词，而是**位置**。
> 每条记录不只有文档编号和词频，还有这个词出现的每一个位置。
>
> 看这一条：词条 `list` 在文档 `d1` 里出现了三次，
> 位置是 8、15、20。这里的三个数字，就是短语查询将来判定
> `"posting list"` 能不能命中的全部依据。
>
> 还有一个不显眼但要紧的决定：词条的 key 用 NUL 字符分隔字段名和词。
> 因为分析器产出的词条只包含字母、数字和撇号，永远不会含 NUL，
> 所以这种编码不会产生歧义——而用冒号就可能会。
> 好处是文档频率天然按字段分开算，不需要嵌套的 map。

### 画面描述

- 三个面板并排：
  - 左：一篇文档的原文（高亮 `posting list` 两个词）。
  - 中：分析器输出的 token 流（`term@position` 形式滚动出现）。
  - 右：索引里的 posting list 结构（逐条淡入）。
- 镜头推进到 `doc=d1 tf=3 positions=[8 15 20]` 这一行，
  在 8 / 15 / 20 上依次弹出小标签。
- 切到 `TermKey` 函数：`field + "\x00" + term`，
  用动画展示 NUL 在字节层面的样子。

### 需要展示的真实文件与代码

1. `internal/index/types.go` —— `Posting` 结构体：

```go
type Posting struct {
	DocID     DocID    `json:"doc_id"`
	TF        uint32   `json:"tf"`
	Positions []uint32 `json:"positions"`
}
```

2. `internal/index/index.go:197` —— `TermKey`
3. 真实 posting 输出（来自 `demo-cases.md` 第 3.2 节，本次实测）：

```
body.list  df=2
    doc=d1   tf=3 positions=[8 15 20]
    doc=d3   tf=1 positions=[12]
```

4. 同一处展示的两条不变量断言结果：

```
Postings sorted by DocID asc                                  true
TF == len(Positions) and Positions strictly increasing        true
```

### 重点高亮区域

- `Positions []uint32` 这一行
- `positions=[8 15 20]` 里的三个数字
- `TermKey` 中的 `"\x00"`

### 架构图或动画设计说明

**主视觉**：三层堆叠结构图。

```
原文      "…a posting list…"（高亮两个词）
   ↓ 分析器
token 流   posting@19   list@20   …
   ↓ 按词条分组 + 追加
索引       list → [ {doc:d1, tf:3, positions:[8,15,20]} , {doc:d3, tf:1, positions:[12]} ]
```

动画要点：位置数字从 token 流「飞」进 posting 的 `positions` 数组，
强调**位置是从原文档一路带过来的，不是事后算的**。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| `Posting` 含 `Positions []uint32` | 读 `internal/index/types.go` |
| `TermKey` 用 NUL 分隔 | `internal/index/index.go:197`；注释说明了理由 |
| `list` 在 `d1` 出现 3 次、位置 `[8 15 20]` | 跑 `demo-cases.md` 第 3.2 节的进程内演示 |
| 两条不变量为 true | 同上（本次实测） |
| 「用冒号会撞车」 | 代码注释的论证：`"a:b"+"c"` 与 `"a"+"b:c"`——属**论证**而非实验，旁白中表述为理由而非实测 |

---

## S3 · BM25：49 行里的两个决定

**时长**：1:10

### 旁白全文

> BM25 的完整实现只有 49 行。里面有两个决定值得讲。
>
> 第一个是逆文档频率的写法。经典公式在词条出现在超过一半文档里的时候
> 会给出**负分**，把命中这个词的文档压到完全没命中的文档下面。
> 这个项目用的是 Lucene 的平滑写法——多套了一个 `1 +`。
> 看这组数字：文档频率从 1 涨到 1000，IDF 从 6.5 单调降到 0.0005，
> 但没有变成负数。
>
> 第二个是长度归一化。三篇文档里同一个词都只出现一次，
> 但其中一篇的字段更短，它的分数就更高——
> 因为长文档本来就更容易「顺便」包含一个词，不该因此占便宜。

### 画面描述

- 全屏展示 `bm25.go` 的两个函数（共 19 行），代码以正常字号呈现，
  背景是暗色 IDE 风格但不出现 IDE 界面（避免变成录屏教学）。
- 切到 IDF 曲线：横轴文档频率 1→1000，纵轴 IDF 6.5→0.0005，
  一条平滑下降的曲线；在 df=500 处打一个标记。
- 再切到三行得分对比，`norm` 与 `score` 数字依次出现。

### 需要展示的真实文件与代码

`internal/scoring/bm25.go`（关键部分，仓库真实内容）：

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

真实 IDF 数据（`demo-cases.md` 第 3.3 节，本次实测）：

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

真实长度归一化数据（本次实测）：

```
field="body" N=4 df=3 idf=0.356675 avgdl=11.2500
    doc=d1   tf=1 dl=11   norm=0.9833  ->  score=0.359947
    doc=d3   tf=1 dl=9    norm=0.8500  ->  score=0.388458
    doc=d4   tf=1 dl=11   norm=0.9833  ->  score=0.359947
```

### 重点高亮区域

- `math.Log(1 + ...)` 里的 `1 +`
- `Score` 里的 `norm := 1 - bm.B + bm.B*(docLen/avgDocLen)`
- `d3` 那一行的 `dl=9`、`norm=0.8500`、`score=0.388458`（三处同时高亮）

### 架构图或动画设计说明

两段动画：

1. **IDF 曲线**：动态绘制 8 个点并连线；在 df=500（恰好一半）处
   叠加一条水平参考线，标注「经典公式从这里开始变负，平滑写法不为负」。
2. **长度归一化**：三根柱子代表三篇文档的字段长度（11 / 9 / 11），
   同一句查询下三根柱子顶部弹出得分（0.3599 / 0.3885 / 0.3599），
   中间那根更矮但分数更高。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| `bm25.go` 全文 49 行 | `(Get-Content internal\scoring\bm25.go).Count`（实测 49） |
| IDF 单调下降且 df=N 时接近 0 | `demo-cases.md` 第 3.3 节真实输出 |
| 短字段得分更高 | 同上（`d3` 的 `dl=9`、`score=0.388458`） |
| 「经典公式给负分」 | `bm25.go` 注释中的论证 + 本次实测 `IDF(df=6,N=5) = -0.080043`（**注意：这是 `df > N` 的越界输入，不是正常路径**，旁白不应把它说成线上现象） |
| 覆盖率 100% | `go test -cover ./internal/scoring/`（本次实测） |

---

## S4 · 短语查询：差 1 还是差 2（全片高潮）

**时长**：1:35

### 旁白全文

> 现在看短语查询。这是整期里最值得停下来看的一处。
>
> 查询 `"quick the brown"`。分析器处理它的时候，`the` 是停用词，
> 会被丢掉——但它的**位置被保留**。
> 所以这句话最后得到两个词：`quick` 在位置 0，`brown` 在位置 2。位置差是 2。
>
> 再看文档。第一篇写的是 `quick the brown fox`，
> 里面的 `quick` 在 0、`brown` 在 2，位置差也是 2——命中。
> 第二篇写的是 `quick brown fox`，位置差是 1——**不命中**。
>
> 注意这个结果有多反直觉：如果要搜的短语是「quick brown」，
> 那么**只有第二篇会命中**。同样两个词，
> 中间少一个停用词，命中的文档就换了一篇。
>
> 所以短语查询比较的不是「相邻」，而是
> 「和查询侧一模一样的位置差」。
> 如果按朴素的「位置差等于 1」去实现，结论会完全反过来。

### 画面描述

- 上半屏：查询串 `"quick the brown"` 与它的分析结果，
  `the` 被划掉但**位置 1 保留为一个虚线占位块**。
- 下半屏：两篇文档并列。
  - 左（`gap`）：`quick[0] the[1] brown[2] fox[3]`
  - 右（`adj`）：`quick[0] brown[1] fox[2]`
- 在两个 `brown` 之间画一条跨度线：左边是 2 格，右边是 1 格。
- 结果面板：`"quick the brown"` → 只亮 `gap`；
  切到 `"quick brown"` → 只亮 `adj`；都给出真实分数。
- 最后切到真实测试代码与通过结果。

### 需要展示的真实文件与代码

1. 分析器真实输出（`demo-cases.md` 第 4.4 节，本次实测）：

```
"quick the brown"  ->  quick@0 brown@2   (n=2)
"quick brown"      ->  quick@0 brown@1   (n=2)
```

2. 判定代码 `internal/query/searcher.go:635`：

```go
want := int64(start) + int64(t.Position) - int64(base)
if want < 0 {
	return false
}
if !containsPosition(cursors[i].Positions(), uint32(want)) {
	return false
}
```

3. 实测结果（进程内演示，本次实跑）：

```
"quick the brown" -> total=1 ids=[gap]
"quick brown"     -> total=1 ids=[adj]
quick brown       -> total=2 ids=[gap adj]
```

4. 回归测试 `internal/query/searcher_test.go:376`
   `TestPhraseUsesAnalyzerPositionGaps` 从 `--- PASS` 到 `ok` 的输出。

5. HTTP 侧的真实对比（9 篇语料，本次实测）：

```
q="posting list"   -> total=2
q="list posting"   -> total=0
q="posting the list" -> total=0
```

### 重点高亮区域

- `the` 被划掉但**保留的虚线占位**
- `quick@0` 与 `brown@2` 之间的「2」
- 两篇文档 `brown` 位置差的对比（2 vs 1）
- `want := int64(start) + int64(t.Position) - int64(base)` 这一行
- 两次查询里互相交换的命中结果

### 架构图或动画设计说明

**核心动画**：两条位置轴并排滑动对齐。

```
查询侧：  [quick]──[   ]──[brown]        ← the 占位但不索引
            0       1       2
文档 gap：[quick]──[the]──[brown]──[fox]  ✓ 位置差 2，命中
文档 adj：[quick]─[brown]─[fox]           ✗ 位置差 1，不命中
```

动画细节：查询侧的位置轴与文档侧的位置轴逐格对齐，
对齐成功时整行变绿，失败时红闪一次。
切到 `"quick brown"` 时，查询侧的占位格消失，
两条轴重新对齐，绿/红结果互换。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| `the` 被过滤但占用位置 1 | 分析器输出实测：`"quick the brown" -> quick@0 brown@2` |
| `"quick the brown"` 只命中含该写法的文档 | 进程内演示实测 `total=1 ids=[gap]` |
| `"quick brown"` 只命中相邻写法的文档 | 同上 `total=1 ids=[adj]` |
| 两个查询的命中文档互换 | 同上 |
| 存在固定该行为的回归测试 | `go test ./internal/query/ -run TestPhraseUsesAnalyzerPositionGaps -v` |
| HTTP 侧 `"list posting"` / `"posting the list"` 返回 0 | `demo-cases.md` 第 4.3 节真实 curl 输出 |
| ⚠️ 不要在一篇文档里同时放两种写法演示 | 本次实测：那样两个查询都会命中同一篇，无法说明问题（见 `demo-cases.md` 第 1.2 节） |

---

## S5 · 布尔与并发：两条被实测证实的边界

**时长**：1:15

### 旁白全文

> 布尔查询的三种形态各有分工。Must 里的子句都要命中；
> Should 只要一个命中；MustNot 必须不命中。
> 但有个容易被忽略的规则：**当 Must 存在时，Should 退化成加分项，不再参与筛选。**
>
> 还有一处语义值得记下来：如果查询里只写了否定子句，
> 引擎没有正向的起点，只能把全部文档拿出来做减法。
> 这个成本随索引规模线性增长，代码注释里写明了这一点。
>
> 并发方面有两条硬边界，我们实测过。
> 第一，把写操作放进只读快照的回调里会死锁——
> 因为底层的读写锁不可重入。
> 第二，`OnApply` 回调运行时**确实持有写锁**，实测中一次读操作被阻塞了 500 毫秒。

### 画面描述

- 上半部分：三色布尔示意图（Must 蓝 / Should 绿 / MustNot 红），
  三条 posting list 用双指针归并动画合并。
- 中段：真实 AST 字符串逐个出现（`MUST(posting list)`、
  `MUST(posting) MUSTNOT(list)`、`MUSTNOT(list)`）。
- 下半部分：并发实测输出逐行打印。

### 需要展示的真实文件与代码

1. `internal/query/searcher.go:691` —— 三种布尔形态：

```go
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

2. `internal/index/view.go` 的 `View` 与它的死锁警告（真实的注释）：

```go
// fn 内**绝不可**调用索引的写方法（Add / Update / Upsert / Delete），
// 否则会死锁（RWMutex 不可重入）。
func (ix *InvertedIndex) View(fn func(v *View)) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	fn(&View{ix: ix})
}
```

3. 真实 AST 与结果（`demo-cases.md` 第 3.3、4.2 节，本次实测）：

```
"posting AND list"        ast=MUST(posting list)              total=2
"posting OR bm25"         ast=SHOULD(posting bm25)            total=4
"posting AND NOT list"    ast=MUST(posting) MUSTNOT(list)     total=1
"-list"                   ast=MUSTNOT(list)                   total=2  (score 全 0)
```

4. 并发实测输出（本次新增实测，见 `code-review.md` 案例 6 附录）：

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

### 重点高亮区域

- `addExisting(&out, unionAll(should))` 及其注释
- `v.SortedDocIDs()`（全量起步）
- `RWMutex 不可重入` 那句警告
- 实测输出里的 `deadlock confirmed`、`freezes reads too`、`write lock IS held`

### 架构图或动画设计说明

1. **布尔归并动画**：三条按 DocID 升序的列表，两个游标从左侧出发，
   同步右移；命中时该文档号变绿并累加分数；MustNot 命中时划掉。
2. **死锁动画**：一个 goroutine 持有读锁（蓝色锁图标），
   再申请写锁（红色锁图标）被挡住；后续到达的读请求也全部排队停在门外，
   直观表达「一次误用连读也一起冻结」。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| Must 存在时 Should 只加分不筛选 | 读 `evalBool` + `addExisting`；测试 `TestSearchShouldOnlyBoostsWhenMustPresent` |
| 只有否定子句时从全量文档起步 | 读 `evalBool` 的 `default` 分支；实测 `-list` → total=8、分数全 0 |
| `View` 内调写方法会死锁 | 本次安全复现（goroutine + 2s 超时）：`deadlock confirmed` |
| 卡住的 View 会连读一起阻塞 | 本次实测：新读者被阻塞 >1.5s |
| `OnApply` 持有写锁 | 本次实测：回调内 `DocCount()` 被阻塞 500ms |
| `OnApply` 失败不回滚 | 本次实测：返回 id=0 + 错误，但 `DocCount()=1`、`Has(d1)=true` |
| ⚠️ 「只有否定子句是 O(N)」 | 代码是 `SortedDocIDs`（含 `slices.Sort`），严格说是 **O(N log N)**；旁白用「随规模线性增长」偏宽容，建议改为「随规模增长」 |

---

## S6 · 工程纪律：测试比源码多 19%

**时长**：0:45

### 旁白全文

> 还有一个数字比任何功能都更能说明这个项目的状态。
> 源码 10,501 行，测试 12,530 行。测试代码比源码多出将近两成。
> 一共 346 个测试函数。
>
> 核心包的分支覆盖率都在 90% 以上：打分模块 100%，索引 90.5%，查询 92.3%。
>
> 仓库自带一条门禁命令，一次跑四项：格式检查、静态检查、
> 一个跨 module 的对外 API 编译门禁、以及带竞态检测的完整测试。
> 我们在干净的工作树上跑过：四项全过。

### 画面描述

- 屏幕一分为二：左边是源码行数柱状图，右边是测试行数柱状图，
  两条柱子拉到同一比例后，右边明显更高。
- 覆盖率数字以仪表盘形式逐个填充（100% / 90.5% / 92.3% / 83.2%）。
- 最后切到门禁命令的真实输出，逐行滚动到 `check passed`。

### 需要展示的真实文件与代码

1. 行数统计（`project-brief.md` 第 6.1 节，本次实测，行数含空行）：

```
源码（非 _test.go）   45 个文件  10,501 行
测试（*_test.go）     37 个文件  12,530 行
func Test 总数                   346
```

2. 覆盖率（本次实测，节选）：

```
internal/scoring     100.0%
internal/tablename   100.0%
internal/analyzer     95.9%
internal/query        92.3%
internal/index        90.5%
pkg/tsh               83.2%
```

3. `scripts/check.ps1` 的四项（真实输出，本次实测）：

```
==> gofmt -l (formatting)
==> go vet
==> apicheck (external consumer)
==> go test -race
...
check passed
```

### 重点高亮区域

- `10,501` 与 `12,530` 两个数字
- `346`
- `100.0%`
- `check passed`

### 架构图或动画设计说明

**「测试 > 源码」对比条**：两条水平柱，长度按真实行数比例绘制
（10,501 : 12,530 ≈ 1 : 1.19），并在右侧标注倍数 1.19×。

门禁部分用四格进度条（格式 / vet / apicheck / race），逐格点亮为绿色。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| 源码 10,501 行 / 测试 12,530 行 | 本次实测（排除 `.gocache/` 与临时目录；行数含空行） |
| 346 个 `func Test` | `Select-String '^func Test'` 汇总（本次实测） |
| 覆盖率数字 | `go test -cover ./...`（本次实测） |
| 门禁四项全过 | `scripts/check.ps1`，需先设 `GOPROXY=off`（见下方环境提示） |
| ⚠️ 统计口径 | PowerShell 的 `Measure-Object -Line` **会跳过空行**，用它统计会得到偏小的数字（例如 bm25.go 会变成 28 行而不是 49 行）。本节数字按换行符计数 |
| ⚠️ 环境提示 | 门禁默认走 `proxy.golang.org`，本机会超时失败。这不是代码缺陷：本地模块缓存完整，设 `GOPROXY=off` 即可通过。**拍摄前务必先跑通，否则画面会出现红色 FAIL** |

---

## S7 · 收尾：边界与未知

**时长**：0:20

### 旁白全文

> 它不是一个要替代 Elasticsearch 的系统。它没有分布式、没有副本、没有聚合。
> 索引全在内存里，容量直接受内存限制。
>
> 而这些「不做」是被项目自己写进文档的。知道自己在什么范围内是安全的，
> 才敢用——这大概是这个项目最值得记录的一点。

### 画面描述

- 屏幕展示 README 的「不适合」表格，逐行淡入。
- 最后定格在项目名与一行小字（技术栈摘要）。
- 背景缓慢淡出到黑。

### 需要展示的真实文件与代码

`README.md` 第 56–68 行的「不适合」表格（真实内容，节选）：

| 不需要 | 为什么 |
| --- | --- |
| 替换 Elasticsearch / Solr | 没有分布式、没有副本、没有聚合与分面 |
| 十亿级数据 | 索引全在内存，容量直接受 RAM 限制 |
| 高可用 / 多副本 | 单进程单写入者；持久化是「重启能恢复」，不是「不怕宕机」 |
| 模糊匹配 / 拼写纠错 | 未实现 |
| 聚合 / 分组统计 | 未实现 |
| 地理位置检索 | 未实现 |

### 重点高亮区域

- 「没有分布式、没有副本、没有聚合与分面」
- 「索引全在内存」
- 「重启能恢复，不是不怕宕机」

### 架构图或动画设计说明

不使用新图形，直接使用文档表格作为收尾画面（技术纪录片常见的「回到文档」收束）。
可在右下角保留一个极小的三层架构缩略图作为视觉锚点。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| 上述「不做」清单 | 读 `README.md` 第 56–68 行；与 `PLAN.md` 第 36–42 行一致 |
| 索引全在内存 | `internal/index` 的数据结构全部是 Go map/slice |
| 持久化是「重启能恢复」 | `README.md` 的持久性保证表格；WAL 是追加式原文日志 |

---

## 附录 A · 拍摄前置检查清单

```powershell
# 每次开拍前执行一次，确保不会在画面里出现失败
Set-Location <repo>
$env:GOCACHE    = "$PWD\.gocache\build"
$env:GOMODCACHE = "$PWD\.gocache\mod"
$env:GOPROXY    = 'off'

go version                                    # go1.27.1 windows/amd64
powershell -ExecutionPolicy Bypass -File .\scripts\check.ps1   # 期望 "check passed"
go build -o bin\tshd.exe .\cmd\tshd
```

**不要在画面中出现的东西**（见 `project-brief.md` 第 8 节）：

- 仓库根目录的 `debug.log`（内容是**搜狗输入法的日志**，与项目无关）
- 仓库根目录的 `query.test.exe`
- `bin/` 下的 4 个约 40 MB 的旧可执行文件

## 附录 B · 绝对不要出镜的未验证内容

| 内容 | 原因 |
| --- | --- |
| 「不启用中文分词时二进制约 6.7 MB」 | 本次无法复现，应属引入 gse 之前的历史值 |
| 任何毫秒级延迟数字（除非引用 `docs/PERFORMANCE.md` 并标注「项目自测」） | 本次未复现 |
| 注释中的历史 pprof 数字（37.4%、13.7%、11.2%、26ms 等） | 属代码注释中的历史数据，非本次实测 |
| 「AI 编写了某段代码 / 发现了某个 bug」 | 无任何证据（见 `development-timeline.md` 第 4 节） |
| 「删掉的文档还能被搜到」 | 本次独立复现**否定了**这个说法：用户可见路径会跳过幽灵条目（见 `code-review.md` 案例 6） |
| 「线上可被 1 MB 查询打崩」 | 仅验证了「解析器可被击穿」，未验证 HTTP 可达性 |

## 附录 C · 一期素材缺口（供人工审核决策）

| 缺口 | 影响 | 建议 |
| --- | --- | --- |
| 无真实会话记录 | 无法支撑「AI 贡献」叙事 | 若需要，请补交谈记录并核对时间戳 |
| 无本次复现的性能数据 | S3 无法展示「P99 < 20ms」这类结论 | 若要展示，需按 `docs/PERFORMANCE.md` 的复现命令重跑一次并留证 |
| 无并发/压力画面的录制素材 | S5 只有终端文字输出 | 可用 `-generate 100000` 合成语料补拍 |
| 中文分词的内存代价只有本机单点数据 | 「每表 108 MB」是单机实测 | 若作为卖点需多环境复现 |
