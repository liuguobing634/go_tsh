"""Layout tuning pass for the storyboards.

Rewrites the `region` coordinates in scripts/video/build-storyboards.py from the
measurements in docs/video/layout-report.json.

Why a script: the regions live inside a large Python literal, and hand-editing 30
coordinates is exactly where transcription errors creep in. This rewrites the
whole SHOTS block from one authoritative table.

Layout system (1920x1080):
    content band   y =  60 .. 930   (870px tall, usable)
    safe margins   x =  60 / 1860
    scene chrome   y = 930 .. 1080  (chapter marker + progress line)

Grid columns: left  x=80  w=860   |  right x=1000 w=840
              full  x=80  w=1760
Two-row: top y=100 h=380, bottom y=520 h=380  (60..900 band)

Run:  python scripts/video/set-layout.py
"""

from __future__ import annotations

import re
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
TARGET = REPO / "scripts" / "video" / "build-storyboards.py"

# 内容带：y = 60 .. 920（场景角标与进度线在 930 以下）
# 每块的高度按"实际内容高度"给，避免框内出现大片死区（那是上一版最大的问题）。
LEFT_X, LEFT_W = 80, 860
RIGHT_X, RIGHT_W = 1000, 840
FULL_X, FULL_W = 80, 1760

ROW1_TERMINAL_H = 380   # 半宽终端：可容纳约 9 行
ROW1_GRAPHIC_H = 460    # 半宽动画/图表
ROW1_CODE_H = 400       # 半宽代码：约 13 行
FULL_WIDE_H = 780       # 全宽代码 / 三面板
FULL_TERMINAL_H = 620   # 全宽终端

REGIONS: dict[tuple[str, int], tuple[int, int, int, int]] = {
    # S0 冷开场：标题在上，代码在下（浅底，视觉书挡）
    ("S0", 0): (320, 470, 1280, 380),   # code-file go.mod
    ("S0", 1): (320, 250, 1280, 170),   # title

    # S1 依赖审计：左列审计表占满高度，右列上下两块
    ("S1", 0): (LEFT_X, 80, LEFT_W, 800),            # import-audit
    ("S1", 1): (RIGHT_X, 80, RIGHT_W, 380),          # code-file chinese.go
    ("S1", 2): (RIGHT_X, 500, RIGHT_W, 380),         # bar-compare 体积

    # S2 倒排索引
    ("S2", 0): (LEFT_X, 80, LEFT_W, 250),            # code-file types.go（8 行）
    ("S2", 1): (RIGHT_X, 80, RIGHT_W, 250),          # code-file TermKey（3 行）
    ("S2", 2): (FULL_X, 360, FULL_W, 380),           # three-panel
    ("S2", 3): (FULL_X, 760, FULL_W, 160),           # invariant-table（2 行）

    # S3 BM25：左代码右曲线，底部逐项计算
    ("S3", 0): (LEFT_X, 80, LEFT_W, 440),            # code-file bm25.go（20 行）
    ("S3", 1): (RIGHT_X, 80, RIGHT_W, 440),          # manim idf-curve
    ("S3", 2): (FULL_X, 560, FULL_W, 360),           # score-breakdown（4 行）

    # S4 短语查询（高潮）：左列竖直排列，右列动画 + 实测
    ("S4", 0): (LEFT_X, 80, LEFT_W, 270),            # terminal analyzer-tokens
    ("S4", 1): (LEFT_X, 380, LEFT_W, 260),           # code-file phraseFits
    ("S4", 2): (LEFT_X, 670, LEFT_W, 250),           # terminal 位置差实测
    ("S4", 3): (RIGHT_X, 80, RIGHT_W, 470),          # manim position-axis
    ("S4", 4): (RIGHT_X, 580, RIGHT_W, 340),         # terminal HTTP 反例

    # S5 布尔与并发：两行两块
    ("S5", 0): (LEFT_X, 80, LEFT_W, 380),            # code-file evalBool
    ("S5", 1): (RIGHT_X, 80, RIGHT_W, 380),          # terminal 布尔 AST
    ("S5", 2): (LEFT_X, 500, LEFT_W, 420),           # manim merge-intersect
    ("S5", 3): (RIGHT_X, 500, RIGHT_W, 420),         # terminal 并发实测

    # S6 工程纪律
    ("S6", 0): (LEFT_X, 80, LEFT_W, 400),            # bar-compare 行数
    ("S6", 1): (RIGHT_X, 80, RIGHT_W, 400),          # coverage-grid
    ("S6", 2): (FULL_X, 520, FULL_W, 400),           # terminal 门禁

    # S7 收尾：文档表格在上，标题在下
    ("S7", 0): (220, 90, 1480, 600),                 # doc-table
    ("S7", 1): (220, 740, 1480, 160),                # title
}


def main() -> int:
    text = TARGET.read_text(encoding="utf-8")

    start = text.index("SHOTS = {")
    end = text.index("\n}\n", start) + len("\n}\n")
    block = text[start:end]

    replaced = 0
    missing: list[str] = []

    # 在 SHOTS 块内按场景定位，逐块替换 region。
    for (scene, idx), (x, y, w, h) in REGIONS.items():
        # 找到该场景段
        m = re.search(rf'^    "{scene}": \[', block, re.M)
        if not m:
            missing.append(f"{scene} 段缺失")
            continue
        seg_start = m.end()
        nxt = re.search(r'^    "S\d": \[', block[seg_start:], re.M)
        seg_end = seg_start + nxt.start() if nxt else len(block)
        seg = block[seg_start:seg_end]

        # 该段内第 idx 个 region
        hits = list(re.finditer(r'"region": \{"x": \d+, "y": \d+, "w": \d+, "h": \d+\}', seg))
        if idx >= len(hits):
            missing.append(f"{scene}[{idx}] region 缺失（段内只有 {len(hits)} 个）")
            continue

        old = hits[idx].group(0)
        new = f'"region": {{"x": {x}, "y": {y}, "w": {w}, "h": {h}}}'
        seg = seg[: hits[idx].start()] + new + seg[hits[idx].end():]
        block = block[:seg_start] + seg + block[seg_end:]
        replaced += 1

    if missing:
        raise SystemExit("布局重排失败：\n  " + "\n  ".join(missing))

    TARGET.write_text(text[:start] + block + text[end:], encoding="utf-8")
    print(f"rewrote {replaced} regions in {TARGET.relative_to(REPO)}")

    # 校验：框必须在安全区内
    bad = []
    for (scene, idx), (x, y, w, h) in REGIONS.items():
        if x < 40 or y < 40 or x + w > 1880 or y + h > 1060:
            bad.append(f"{scene}[{idx}] ({x},{y},{w},{h})")
    if bad:
        raise SystemExit("框超出安全区：\n  " + "\n  ".join(bad))
    print("all regions inside the safe area (x>=40, y>=40, right<=1880, bottom<=1060)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
