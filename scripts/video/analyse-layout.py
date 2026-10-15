"""Layout analyser for the documentary stills.

Renders a frame for each scene, converts it to raw RGB with ffmpeg, then measures
where the actual content ("ink") sits. The point is to replace eyeballing with a
number: the content bounding box, the fill ratio, and the empty bands.

Usage:
    python scripts/video/analyse-layout.py            # analyse every scene
    python scripts/video/analyse-layout.py --scene S3 # one scene
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
REMOTION = REPO / "docs" / "video" / "remotion"
STORYBOARDS = REPO / "docs" / "video" / "storyboards.json"
FFMPEG = Path("D:/ffmpeg/bin/ffmpeg.exe")

W, H = 1920, 1080
# 抽帧位置：场景内的相对进度。不同镜头占不同时段，因此取多处。
PROBES = (0.30, 0.50, 0.72)


def render_still(scene_id: str, frame: int, out: Path) -> bool:
    """用 Remotion 渲染单帧。"""
    tmp = REMOTION / ".tmp"
    tmp.mkdir(parents=True, exist_ok=True)
    cmd = [
        "npx.cmd", "remotion", "still", "Episode1", str(out),
        "--frame", str(frame),
    ]
    env = {
        **dict(__import__("os").environ),
        "PATH": f"D:\\ffmpeg\\bin;{__import__('os').environ.get('PATH','')}",
        "TEMP": str(tmp),
        "TMP": str(tmp),
    }
    r = subprocess.run(cmd, cwd=REMOTION, env=env, capture_output=True, text=True, shell=False)
    if r.returncode != 0:
        print(f"  render failed for {scene_id} frame {frame}:\n{r.stderr[-600:]}", file=sys.stderr)
        return False
    return True


def to_raw_rgb(png: Path, raw: Path) -> bool:
    """用 ffmpeg 把 PNG 转成裸 RGB，避免依赖 Pillow。"""
    cmd = [
        str(FFMPEG), "-y", "-v", "error", "-i", str(png),
        "-f", "rawvideo", "-pix_fmt", "rgb24", str(raw),
    ]
    r = subprocess.run(cmd, capture_output=True, text=True)
    if r.returncode != 0:
        print(f"  ffmpeg failed: {r.stderr[-400:]}", file=sys.stderr)
        return False
    return True


def analyse(raw: Path, scene: dict) -> dict:
    """计算内容包围盒与"最大空白带"。

    背景色从画面四角取样（场景调色板决定的实底色）。
    某个像素若与背景色差异超过阈值，即视为"内容"。

    注意：场景角标、字幕、底部进度线属于"框架元素"，
    它们贴着画面边缘，会让包围盒永远撑满全屏。因此空白带分析
    只在**可用内容区**内进行（左右留边、上下避开框架元素）。
    """
    data = raw.read_bytes()
    if len(data) < W * H * 3:
        raise SystemExit(f"raw 数据不完整：{len(data)} 字节")

    def px(x: int, y: int) -> tuple[int, int, int]:
        i = (y * W + x) * 3
        return data[i], data[i + 1], data[i + 2]

    corners = [px(2, 2), px(W - 3, 2), px(2, H - 3), px(W - 3, H - 3)]
    bg = tuple(sorted(c[k] for c in corners)[1] for k in range(3))

    TOL = 24

    def is_ink(x: int, y: int) -> bool:
        r, g, b = px(x, y)
        return abs(r - bg[0]) > TOL or abs(g - bg[1]) > TOL or abs(b - bg[2]) > TOL

    STEP = 3
    min_x, min_y, max_x, max_y = W, H, -1, -1
    ink = 0
    total = 0
    row_density = [0] * H
    col_density = [0] * W

    for y in range(0, H, STEP):
        for x in range(0, W, STEP):
            total += 1
            if is_ink(x, y):
                ink += 1
                row_density[y] += 1
                col_density[x] += 1
                if x < min_x:
                    min_x = x
                if x > max_x:
                    max_x = x
                if y < min_y:
                    min_y = y
                if y > max_y:
                    max_y = y

    if max_y < 0:
        return {"scene": scene["id"], "empty": True}

    # 可用内容区：左右各留 40px；上边 40px；下边避开字幕+进度线（留 150px）。
    CONTENT_TOP, CONTENT_BOTTOM = 40, H - 150
    CONTENT_LEFT, CONTENT_RIGHT = 40, W - 40

    # 帧内"有内容"的行。阈值取内容区宽度的 0.5%：
    # 取 0.2% 时元素之间的细缝（例如框与框之间 10px 的空隙）也会被当成"空行"，
    # 于是最大空白带会错误地落在两个框之间，误导调布局的方向。
    min_ink_per_row = max(1, int(((CONTENT_RIGHT - CONTENT_LEFT) / STEP) * 0.005))

    def row_has_content(y: int) -> bool:
        c = 0
        for x in range(CONTENT_LEFT, CONTENT_RIGHT, STEP):
            if is_ink(x, y):
                c += 1
                if c >= min_ink_per_row:
                    return True
        return False

    has = [row_has_content(y) for y in range(H)]

    # 最大连续空白带（在内容区内）
    best_len, best_start, cur, cur_start = 0, 0, 0, CONTENT_TOP
    for y in range(CONTENT_TOP, CONTENT_BOTTOM):
        if not has[y]:
            if cur == 0:
                cur_start = y
            cur += 1
            if cur > best_len:
                best_len, best_start = cur, cur_start
        else:
            cur = 0

    # 顶部/底部空白（内容区内）
    top_gap = 0
    for y in range(CONTENT_TOP, CONTENT_BOTTOM):
        if has[y]:
            break
        top_gap += 1
    bottom_gap = 0
    for y in range(CONTENT_BOTTOM - 1, CONTENT_TOP, -1):
        if has[y]:
            break
        bottom_gap += 1

    def band(density, reverse=False):
        rows = range(H - 1, -1, -1) if reverse else range(H)
        n = 0
        for y in rows:
            if density[y] == 0:
                n += 1
            else:
                break
        return n

    return {
        "scene": scene["id"],
        "bg": bg,
        "bbox": {"x": min_x, "y": min_y, "w": max_x - min_x, "h": max_y - min_y},
        "bbox_ratio": {
            "x": round(min_x / W, 3),
            "y": round(min_y / H, 3),
            "w": round((max_x - min_x) / W, 3),
            "h": round((max_y - min_y) / H, 3),
        },
        "ink_ratio": round(ink / max(1, total), 4),
        "empty_top_px": band(row_density),
        "empty_bottom_px": band(row_density, reverse=True),
        # 真正用于调布局的指标：
        "content_bbox_y": [next(y for y in range(H) if has[y]), max(y for y in range(H) if has[y])],
        "content_span_ratio": round(
            (max(y for y in range(H) if has[y]) - next(y for y in range(H) if has[y])) / H, 3
        ),
        "largest_gap_px": best_len,
        "largest_gap_y": [best_start, best_start + best_len],
        "gap_top_px": top_gap,
        "gap_bottom_px": bottom_gap,
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--scene", help="只分析某个场景，例如 S3")
    args = ap.parse_args()

    doc = json.loads(STORYBOARDS.read_text(encoding="utf-8"))
    scenes = doc["scenes"]
    if args.scene:
        scenes = [s for s in scenes if s["id"] == args.scene]
        if not scenes:
            raise SystemExit(f"未知场景 {args.scene}")

    (REMOTION / ".tmp").mkdir(parents=True, exist_ok=True)
    results = []

    for scene in scenes:
        fps = doc["meta"]["resolution"]["fps"]
        start = scene["startSec"]
        dur = scene["durationSec"]
        print(f"\n=== {scene['id']}  {scene['title']}  ({dur}s, {len(scene['shots'])} shots)")
        for frac in PROBES:
            frame = int((start + dur * frac) * fps)
            png = REMOTION / ".tmp" / f"probe-{scene['id']}-{int(frac*100)}.png"
            raw = png.with_suffix(".raw")
            if not render_still(scene["id"], frame, png):
                continue
            if not to_raw_rgb(png, raw):
                continue
            r = analyse(raw, scene)
            r["frame"] = frame
            r["probe"] = frac
            results.append(r)
            print(
                f"  f={frame:<6} probe={frac:.2f}  "
                f"内容区 y={r['content_bbox_y'][0]}..{r['content_bbox_y'][1]} "
                f"(占画面 {r['content_span_ratio']:.2f})  "
                f"最大空白 {r['largest_gap_px']}px @ y={r['largest_gap_y'][0]}..{r['largest_gap_y'][1]}  "
                f"ink={r['ink_ratio']:.3f}"
            )
            png.unlink(missing_ok=True)
            raw.unlink(missing_ok=True)

    out = REPO / "docs" / "video" / "layout-report.json"
    out.write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"\nwrote {out.relative_to(REPO)}  ({len(results)} probes)")

    # 汇总：哪些场景最空
    print("\n=== 汇总（按最大空白带降序，越大越空）===")
    by_scene: dict[str, list[dict]] = {}
    for r in results:
        by_scene.setdefault(r["scene"], []).append(r)
    rows = []
    for sid, rs in by_scene.items():
        gaps = [x["largest_gap_px"] for x in rs]
        span = sum(x["content_span_ratio"] for x in rs) / len(rs)
        ink = sum(x["ink_ratio"] for x in rs) / len(rs)
        rows.append((max(gaps), sid, span, ink, sum(gaps) / len(gaps)))
    for mx, sid, span, ink, avg in sorted(rows, reverse=True):
        print(
            f"  {sid}  最大空白={mx:>4}px  平均空白={avg:>6.1f}px  "
            f"内容纵向占比={span:.2f}  平均墨量={ink:.3f}"
        )
    print(
        "\n判据：最大空白带 > 300px 视为需要调整；内容纵向占比目标 ≈0.75–0.90，"
        "墨量目标 0.03–0.10。"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
