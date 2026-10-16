"""Insert the new opening scenes (S-1 vibe coding, S0 full-text-search primer)
into docs/video/video-script.md and refresh the 全片结构 table.

Why a script and not a hand edit: the file is 741 lines of tightly formatted
markdown, and the new scenes must match the existing seven-part scene template
exactly (旁白全文 / 画面描述 / 需要展示的真实文件与代码 / 重点高亮区域 /
架构图或动画设计说明 / 预计时长 / 技术事实核验清单). One authoritative source
avoids template drift.

Run:  python scripts/video/add-intro-scenes.py
"""

from __future__ import annotations

import re
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
SCRIPT = REPO / "docs" / "video" / "video-script.md"

# ---------------------------------------------------------------------------
# 新场景时长（秒）。原 S0–S7 不变，片头是纯增量。
# ---------------------------------------------------------------------------
S_MINUS_1_SEC = 58   # 主题宣告 + vibecoding 自述
S0_SEC = 56          # 硬证据 + 全文搜索引擎四个要点

NEW_TABLE_ROWS = """| 场景 | 标题 | 时长 | 累计 |
| --- | --- | --- | --- |
| **S-1** | **片头：一个人，四天，四十八次提交** | 0:58 | 0:58 |
| **S0** | **全文搜索引擎：先是哪四件事** | 0:56 | 1:54 |
| S1 | 冷开场：一份 7 行的 go.mod | 0:24 | 2:18 |
| S2 | 定位：手写核心，只借一个库 | 0:37 | 2:55 |
| S3 | 倒排索引：位置是全部的秘密 | 1:01 | 3:56 |
| S4 | BM25：49 行里的两个决定 | 0:50 | 4:46 |
| S5 | 短语查询：差 1 还是差 2（高潮） | 1:06 | 5:52 |
| S6 | 布尔与并发：两条被实测证实的边界 | 0:55 | 6:47 |
| S7 | 工程纪律：测试比源码多 19% | 0:48 | 7:35 |
| S8 | 收尾：边界与未知 | 0:24 | 7:59 |
"""

S_MINUS_1 = """## S-1 · 片头：一个人，四天，四十八次提交

**时长**：0:58

> ⚠️ **本场景含创作者第一人称自述。** 为了让全片的证据纪律保持诚实，
> 自述与实证在画面上必须**显式区分**（见下「画面描述」的角标要求）。
> 实证部分（提交数、时长、代码量）可逐条用 `git log` 复核；
> 自述部分是本片作者的陈述，**不作为纪录片的调查结论**。

### 旁白全文

> 先说结论：**四天，四十八次提交，超过一万行 Go 代码**，
> 手写了一个可以跑的全文搜索引擎。
>
> 这四天里，唯一的协作者是一个 AI 编程工具，叫 DeepSeek Harness。
> 从项目骨架到倒排索引，从打分公式到中文分词，
> 每一行都是在我和它的一来一回里长出来的。
>
> 接下来这八分钟，我想把这个过程摊开给你看。
> 不是讲「AI 能写代码」——这件事没什么好讲的；
> 而是讲**它写出来的东西到底对不对**，以及**我怎么确认它是对的**。
>
> 因为真正的难点从来不在写，在**判断**。

### 画面描述

- 全黑底，白字。三行数字依次打字机式出现，每个数字落地时轻微放大一次：
  - `4` 天
  - `48` 次提交
  - `10,819` 行插入（首日窗口）
- 数字淡出，出现一行大字：**「唯一的协作者是一个 AI 编程工具」**。
- 转场前，右下角出现常驻角标 `〔创作者自述〕`——
  **这个角标必须保留到本场景结束**，让观众明确知道这是谁的陈述。
- 最后一行旁白出现时，角标切换为 `〔以下为可复核的事实〕`。

### 需要展示的真实文件与代码

1. **不用代码。** 本场景刻意不展示任何代码片段——
   数字与自述的对比才是重点，插入代码会稀释节奏。
2. 若要给静态画面的底纹，用 `git log --oneline` 的真实输出：
   ```
   48 次提交 / 1 位作者 / 5 个活跃日
   ```

### 重点高亮区域

- 三行数字：`4`、`48`、`10,819`
- **常驻角标 `〔创作者自述〕`**（不可省略，见核验清单）
- 旁白末句的「**判断**」二字

### 架构图或动画设计说明

**主视觉：数字打字机 + 角标切换。**

```
[黑底]
  4 天          ← 打字机，落地放大
  48 次提交      ← 打字机
  10,819 行插入  ← 打字机
        ↓ 全部淡出
  唯一的协作者是一个 AI 编程工具
        ↓
  右下角：〔创作者自述〕 → 〔以下为可复核的事实〕
```

要点：角标切换是**本场景唯一的转场动作**，因为它承担了
「自述 → 实证」的语义切换，必须在视觉上被看见。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| 四天 | `git log --pretty=format:'%ad' --date=short \\| Sort-Object -Unique` → 5 个活跃日；其中首日最密集。旁白说「四天」指主要开发日，**若严格计 5 天需改口径** |
| 48 次提交 | `git rev-list --count HEAD` |
| 10,819 行插入 | 首日 13 个提交的 `insertions` 之和（`git show --shortstat`），另含 464 行删除 |
| 1 位作者 | `git shortlog -sne --all` 只有一行 |
| 「唯一的协作者是 DSH」 | **【创作者自述】**。仓库内**没有**可核验的对话记录。必须由角标标明，不得作为实证陈述 |
| ⚠️ 不得出现 | 「AI 编写了 `xxx.go`」「AI 发现了某个 bug」「AI 提出了某个设计」——均无证据（见附录 B） |
"""

S0 = """## S0 · 全文搜索引擎：先是哪四件事

**时长**：0:56

### 旁白全文

> 在讲怎么做的之前，得先说清楚**要做哪四件事**。
>
> 第一，**倒排索引**。把「哪些词在这篇文档里」反过来，
> 变成「哪些文档有这个字」，并且记住每个词出现的位置。
>
> 第二，**相关性排序**。搜出来一堆结果，谁该排前面？
> 这里用的是 BM25，工业界用了三十年的公式。
>
> 第三，**布尔求值**。用户会写「A 和 B」「A 或 B」「不要 C」，
> 这些条件得能在文档集合上算出来。
>
> 第四，**短语匹配**。双引号里的东西要按顺序、按位置出现，
> 这一条最考验索引设计。
>
> 而我要用一个很特别的方式做完这四件事。

### 画面描述

- 四个概念像卡片一样，从左到右依次推入画面，每张卡片带一个极简图标：
  - 「倒排索引」—— 一个词条指向多个文档的箭头
  - 「相关性排序」—— 一堆结果按高低排列的柱形
  - 「布尔求值」—— 两个集合相交、相并
  - 「短语匹配」—— 一串格子中被点亮的连续三格
- 每张卡片出现时，右下角同步打出它在**本片中的位置**
  （`S4` / `S5` / `S7` / `S6`），让观众知道后面会讲。
- 四张卡片并排停住 1.5 秒，然后整排轻微后移，
  让出位置给最后一句旁白的文字。

### 需要展示的真实文件与代码

本场景是概念引入，**不展示代码**。四处对应关系用真实文件路径标注在卡片底部：

| 卡片 | 对应实现文件 |
| --- | --- |
| 倒排索引 | `internal/index/index.go`、`internal/index/types.go` |
| 相关性排序 | `internal/scoring/bm25.go` |
| 布尔求值 | `internal/query/searcher.go` 的 `evalBool` |
| 短语匹配 | `internal/query/searcher.go` 的 `evalPhraseTokens` |

### 重点高亮区域

- 四张卡片的标题
- 卡片底部对应的真实文件路径（**证明这四件事都有实体**，不是空谈）
- 卡片角落的场景号 `S4` / `S5` / `S7` / `S6`

### 架构图或动画设计说明

**主视觉：四象限展开 → 收拢为一条时间轴。**

```
阶段一（并排四卡）：
  [倒排索引]   [相关性排序]   [布尔求值]   [短语匹配]
     S4            S5            S7           S6

阶段二（收拢）：四卡缩成上方四个小圆点，下方拉出一条横轴，
  指向「S1 → S2 → … → S8」，为全片导航。
```

要点：这个收拢动作给观众一张**地图**，
后面每进一个场景，横轴上对应节点点亮。

### 技术事实核验清单

| 断言 | 核验方式 |
| --- | --- |
| 四件事的定义 | 通用定义，非本项目独有；表述与 `PLAN.md`「v1 做什么」一节一致 |
| BM25 是「工业界用了三十年的公式」 | 属**通行说法**。若审核认为需要精确出处，改为「工业界长期使用的公式」，避免具体年数争议 |
| 对应实现文件 | `internal/scoring/bm25.go`（49 行）、`internal/index/index.go`（`TermKey`/`Posting`/`insertFieldLocked`）、`internal/query/searcher.go`（`evalBool` 664 行处、`evalPhraseTokens` 526 行处）。行号以 `code-review.md` 的引用为准 |
| 各包源码规模 | 见 `project-brief.md` 第 6.1 节（本次实测） |
| ⚠️ 不得出现 | 任何「性能领先」「比 X 快」之类比较——本片没有做横向对比测试 |
"""


def read() -> str:
    return SCRIPT.read_text(encoding="utf-8")


def main() -> int:
    text = read()

    # --- 1) 替换全片结构表 ---
    table_re = re.compile(r"## 全片结构\n\n\| 场景 \|.*?\n\n", re.S)
    if not table_re.search(text):
        raise SystemExit("找不到「## 全片结构」表格，脚本需适配")
    text = table_re.sub("## 全片结构\n\n" + NEW_TABLE_ROWS + "\n", text, count=1)

    # --- 2) 把正文按「## 场景标题」切片 ---
    #     先切片再重编号，而不是就地替换。
    #     踩过的坑：原 S0 改成 S1 后与原 S1 **撞号**，反向逐个替换只会改到
    #     第一个匹配，结果顺序变成 S-1/S0/S2/S1/...。切片法不受撞号影响。
    head_re = re.compile(r"^## (S-?\d+) · (.+)$", re.M)
    marks = list(head_re.finditer(text))
    if not marks:
        raise SystemExit("找不到任何场景标题")

    body_start = marks[0].start()
    tail_start = marks[-1].start()
    # 附录（## 附录 ...）在最后一个场景之后，必须原样保留
    tail = text[tail_start:]
    appendix_at = re.search(r"^## 附录", tail, re.M)
    if not appendix_at:
        raise SystemExit("找不到附录标题，脚本需适配")
    appendix = tail[appendix_at.start():]

    # 各场景的正文块（含标题行）
    blocks: list[tuple[str, str]] = []
    for i, m in enumerate(marks):
        start = m.start()
        end = marks[i + 1].start() if i + 1 < len(marks) else tail_start + appendix_at.start()
        blocks.append((m.group(1), text[start:end]))

    if len(blocks) != 8:
        raise SystemExit(f"期望 8 个原场景，实际 {len(blocks)}")

    # --- 3) 原场景顺延：S0→S1, S1→S2, ... S7→S8 ---
    #     用正则只改标题行，块内其它地方出现的场景号（例如「见 S4」）不动，
    #     因为那属于正文引用，需人工确认；本脚本不擅自改。
    renumbered: list[tuple[str, str]] = []
    for old_id, block in blocks:
        n = int(old_id[1:])
        new_id = f"S{n + 1}"
        block = re.sub(rf"^## {re.escape(old_id)} · ", f"## {new_id} · ", block, count=1, flags=re.M)
        renumbered.append((new_id, block))

    # --- 4) 拼装：新片头 + 顺延后的原场景 + 附录 ---
    intro = S_MINUS_1 + "\n---\n\n" + S0 + "\n---\n\n"
    text = text[:body_start] + intro + "".join(b for _, b in renumbered) + appendix

    # --- 5) 顺手修正附录里的场景号引用 ---
    #     附录是按**旧编号**写的（旧 S3=BM25、旧 S5=短语查询），顺延后要跟着改。
    #     用占位符两步替换，避免 S1→S2→S3 这种链式误改。
    #     ⚠️ 必须**逆序**替换。第一版用正序迭代 + 占位符，结果链式误改：
    #     S3→标记S4，随后迭代到 S4 时又把标记改成 S5……最终 S3 和 S5 都变成 S8。
    #     逆序（S7 先改）时，改出的新编号 S8 不会被后续迭代碰到。
    appendix_mapping = {
        "S7": "S8", "S6": "S7", "S5": "S6", "S4": "S5",
        "S3": "S4", "S2": "S3", "S1": "S2", "S0": "S1",
    }
    tail_start_idx = text.index("## 附录")
    head_part, tail_part = text[:tail_start_idx], text[tail_start_idx:]
    for old_id, new_id in appendix_mapping.items():  # 已是逆序
        tail_part = re.sub(rf"\b{old_id}\b", new_id, tail_part)
    text = head_part + tail_part

    SCRIPT.write_text(text, encoding="utf-8")

    # --- 校验 ---
    heads = re.findall(r"^## (S-?\d+) · (.+)$", text, re.M)
    print(f"wrote {SCRIPT.relative_to(REPO)}")
    print(f"场景数：{len(heads)}")
    for sid, title in heads:
        print(f"  {sid:<4} {title}")
    expected = ["S-1", "S0", "S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8"]
    got = [h[0] for h in heads]
    if got != expected:
        raise SystemExit(f"场景编号不符：期望 {expected}，实际 {got}")
    print("场景编号校验通过")
    if "## 附录 A" not in text:
        raise SystemExit("附录丢失")
    print("附录保留校验通过")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
