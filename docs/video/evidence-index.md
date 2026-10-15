# 证据索引与人工审核清单

> 第一期技术纪录片素材 · **辅助文件**（不属于任务要求的六份交付物）
> 用途：让审核人能在不读完 141 KB 素材的前提下，快速核对每条结论的证据等级，
> 并逐条复核本次分析的实测记录。六份交付物为：
> `project-brief.md`、`architecture.md`、`code-review.md`、`demo-cases.md`、
> `development-timeline.md`、`video-script.md`。

---

## 1. 三级标注约定（全系列统一）

| 标注 | 含义 | 审核时的判定标准 |
| --- | --- | --- |
| 【已验证】 | 事实 | 本次分析**实跑命令/程序**得到输出，或由代码原文直接读出且给出了行号 |
| 【风险】 | 有依据但未验证 | 由代码或已证事实推理得出，本次**没有**构造实验证明其在真实使用中发生 |
| 【推测】 | 尚无证据 | 主观判断或经验类比；**不得**在纪录片中作为结论陈述 |

本次六份交付物中，**没有使用【推测】级结论**——所有不能用证据支撑的内容
都被移入了「未知」清单（见 `development-timeline.md` 第 6 节）或「不要出镜」清单
（见 `video-script.md` 附录 B）。

---

## 2. 本次分析的实测证据总表

| # | 验证动作 | 命令 / 位置 | 结果 | 影响哪些交付物 |
| --- | --- | --- | --- | --- |
| 1 | 完整测试套件 | `go test ./...` | 12 包 `ok`、1 包无测试文件，退出码 0 | 全部 |
| 2 | 竞态检测 | `go test -race ./internal/... ./pkg/...` | 全部 `ok`，退出码 0 | project-brief、architecture |
| 3 | 项目自有门禁 | `scripts/check.ps1`（`GOPROXY=off`） | `check passed`，退出码 0 | project-brief、video-script S6 |
| 4 | 覆盖率 | `go test -cover ./...` | scoring 100.0% / index 90.5% / query 92.3% 等 | project-brief 第 6.2 节 |
| 5 | 格式与静态检查 | `gofmt -l <pkgdirs>`、`go vet ./...` | 输出为空、退出码 0 | project-brief 第 7 节 |
| 6 | 二进制体积 | `go build` × 4 种参数 | 42.36 / 38.95 / 42.34 / 38.72 MiB | project-brief 第 6.3 节 |
| 7 | 第三方依赖闭包 | `go list -deps ./cmd/tshd` | 只有 gse / gse-hmm / gse-types / cedar | project-brief 第 3 节、architecture 第 1 节 |
| 8 | 逐包 import 检查 | `go list -f '{{range .Imports}}...'` | index/query/scoring/httpapi 第三方为空 | project-brief 第 3.3 节 |
| 9 | 索引结构与不变量 | 进程内演示程序 | posting 列表真实输出；两条不变量为 true | architecture 第 2/3 节、demo-cases 第 3 节 |
| 10 | BM25 边界 | 直接调用 `IDF` / `Score` | 8 组真实返回值 | code-review 案例 2 |
| 11 | BM25 长度归一化 | 真实语料逐步计算 | 短字段 `d3` 得分更高（0.3885 vs 0.3599） | code-review 案例 2、demo-cases 3.3 |
| 12 | 分析器 token/位置 | 真实 `Analyze` 输出 | `quick the brown` → `quick@0 brown@2` | code-review 案例 3/5、demo-cases 4.4 |
| 13 | 短语相对位置差 | 两篇独立文档 | 两个查询命中文档互换 | code-review 案例 3（推荐镜头） |
| 14 | HTTP 端到端 | `curl` 30+ 条查询 × 3 套分析器 | 真实 total / ids / scores / 错误码 | demo-cases 第 4 节 |
| 15 | 中文分词对比 | 9 篇语料 × 3 套配置 | 528/432/421 词条，127.8/78.1/75.8 平均长度 | demo-cases 第 2/5 节 |
| 16 | 子词共享位置 | 真实 gse 词典 | `大学生` → 三个 token 同位置 0 | code-review 案例 5 |
| 17 | 每表分析器内存 | `runtime.ReadMemStats` | 1 表 +109.0 MiB、3 表 +323.7 MiB | code-review 案例 5 |
| 18 | 更新/删除常规路径 | 进程内演示 | 删除后旧词条搜不到、统计下降 | code-review 案例 6 |
| 19 | 幽灵 posting | 直接 `internal/index` API | 声明 `number` 后删除，残留 `DocFreq=1` | code-review 案例 6 |
| 20 | 幽灵对用户的影响 | `pkg/tsh` 引擎层 | **取文档阶段跳过**，用户看不到幽灵文档 | code-review 案例 6（更正了子代理结论） |
| 21 | 括号深度 | 独立进程 | 10 万层通过、100 万层 `fatal error: stack overflow` | demo-cases 6.1 |
| 22 | 子句配额绕过 | `MaxClauses=4` + 100 字中文短语 | 短语被接受，但分析器产出 100 个 token | code-review 附录 C2 |
| 23 | 并发三条边界 | goroutine + 超时 | 死锁 / 读也被冻结 / `OnApply` 持写锁 | demo-cases 6.4、video-script S5 |
| 24 | `OnApply` 失败不回滚 | 注入失败回调 | 返回 `id=0` + 错误，但文档已存在 | code-review 案例 6 |
| 25 | Git 时间线 | `git log` / `git shortlog` | 45 提交、1 作者、4 个活跃日 | development-timeline |
| 26 | AI 参与证据检索 | Markdown 全文 `grep` | 只有 DSH 沙箱记录，**无**编码贡献叙述 | development-timeline 第 4 节 |

---

## 3. 【风险】级清单（共 12 条，审核时可逐条决定是否补测）

| # | 风险 | 位置 | 是否值得补测 |
| --- | --- | --- | --- |
| R1 | IDF「恒为正」依赖 `df <= N` 这一未断言前提（`df>N` 时变负） | code-review 案例 2 | 低（需要先构造 `df>N`） |
| R2 | BM25 参数无范围校验，存在使分母为 0 的有限参数 → `+Inf` | code-review 案例 2 | 中（可直接单元测试） |
| R3 | 只设 K1 或只设 B 会静默改变公式含义（实测已证实行为） | code-review 案例 2 | 低（行为已确认） |
| R4 | `Positions` 严格递增是「被保持」而非「被强制」 | code-review 案例 1 | 低（内置分析器不触发） |
| R5 | `phraseFits` 的 int64→uint32 截断可造成短语假阳性 | code-review 案例 3 | 低（需 12 亿位置） |
| R6 | `docFieldLens` 删除后不缩容（实测 `IndexBytes` 停在 256） | code-review 案例 1 | 低（影响仅统计） |
| R7 | 重复子句重复计分（实测已证实） | demo-cases 6.2 | 低（事实已确认） |
| R8 | `-a OR b` 的分组语义（d3 两子句都不满足却被返回） | demo-cases 6.3 | **中（建议补测，语义影响用户预期）** |
| R9 | 括号深度可达性未经 HTTP 验证（需约 1 MB URL） | demo-cases 6.1 | 中（涉及 DoS 判断） |
| R10 | 幽灵 posting 使 `Total` 与 `len(Hits)` 不一致 | code-review 案例 6 | **中（建议补测并考虑加一致性断言）** |
| R11 | `View.EachDocument` / `FieldValue` 返回内部引用，误用会静默改数据 | code-review 案例 6 | 低（属契约） |
| R12 | 分析器一致性失败模式无测试覆盖 | code-review 案例 5 | 中（可补一个错配分析器的测试） |

---

## 4. 本次**未能复现**、因此禁止写入旁白的内容

| 内容 | 来源 | 处理方式 |
| --- | --- | --- |
| 「不启用中文分词时二进制约 6.7 MB」 | README（历史数据） | `project-brief.md` 第 6.3 节标注为无法复现，列入不要出镜 |
| 注释中的历史 pprof 数字：37.4%、13.7%、11.2%、0.7ms/400KB、p50 105ms/p99 401ms、26ms | 代码注释 | 各处均标注「注释中的历史数据，本次未复现」 |
| 提交信息中的 4.6 倍、10 万篇 P99 < 20ms、11.3s→263ms | `git log` | `development-timeline.md` 第 3.2 节标注为开发者自述 |
| 20 万篇下 21.0ms vs 2.0ms | 子代理实测 | code-review 附录 C 标注未复现 |
| NaN 使 TopK 丢分、`-a OR b` 的逐字结果 | 子代理实测 | code-review 附录 C 标注未复现 |

---

## 5. 环境事实（审核者复现时必须知道）

项目的 `.gocache/` 是工作区内的构建缓存与模块缓存。默认 `GOMODCACHE`
在用户目录且为空，直接跑命令会尝试联网下载 `gse` 并超时：

```
internal\analyzer\chinese.go:10:2: github.com/go-ego/gse@v1.1.0:
Get "https://proxy.golang.org/...": connectex: A connection attempt failed
```

可用环境（本次全部结论均由此得到）：

```powershell
$env:GOCACHE    = '<repo>\.gocache\build'
$env:GOMODCACHE = '<repo>\.gocache\mod'
$env:GOPROXY    = 'off'      # 模块缓存完整，禁止联网
```

`scripts/check.ps1` 自身提供了 `-GoProxy` 开关并注释说明了这一点
（第 157–166 行），所以这是**已知的环境约束，不是代码缺陷**。

**统计口径提醒**：PowerShell 的 `Measure-Object -Line` **会跳过空行**
（会把 `internal/scoring/bm25.go` 统计成 28 行而不是真实的 49 行）。
本系列文档的行数均按「换行符计数」口径，复核方式：

```powershell
([System.IO.File]::ReadAllText($path) -split "`n").Count - 1
```

---

## 6. 人工审核检查清单

请逐项确认：

- [ ] **证据纪律**：六份文档中没有未经标注的断言；没有把【风险】写成【已验证】。
- [ ] **无编造**：没有虚构的 bug、性能指标、测试结果、开发对话。
- [ ] **AI 贡献**：`development-timeline.md` 第 4 节的「不可以说」清单是否被严格遵守；
      是否需要按建议补充可核验的会话记录。
- [ ] **业务代码**：`git status --porcelain` 只显示 `?? docs/video/`（本次分析未改动任何业务代码）。
- [ ] **不生成视频**：本次只产出脚本与分镜，未生成任何视频文件。
- [ ] **风险清单**：`code-review.md` 与 `demo-cases.md` 中列出的 12 条【风险】，
      哪些需要在第二期补测（建议优先 R8、R10、R12）。
- [ ] **镜头取舍**：`video-script.md` 的 8 分钟结构是否需要调整
      （S4 短语查询是技术高潮，S3 BM25 可压缩）。
- [ ] **性能数据**：是否需要在第二期重跑 `docs/PERFORMANCE.md` 的基准命令，
      以便脚本能安全引用延迟数字。

---

## 7. 已知的素材缺口（供第二期决策）

| 缺口 | 影响 | 补齐方式 |
| --- | --- | --- |
| 无真实会话记录 | 无法支撑「AI 贡献」叙事 | 导出会话记录并核对时间戳 |
| 性能数据均为项目自测 | 脚本无法陈述延迟结论 | 按 `docs/PERFORMANCE.md` 复现命令重跑 |
| 无并发/压力画面素材 | S5 只有终端文字 | 用 `-generate 100000` 合成语料补拍 |
| 中文分词内存代价只有单机单点 | 「每表 108 MB」是单机实测 | 多环境复现 |
| 无画外音录制与配乐 | — | 制作阶段事项，不属素材范围 |
