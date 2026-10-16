"""Extract the narration and shot plan from docs/video/video-script.md and emit
docs/video/storyboards.json — the machine-readable intermediate that Remotion
(or any editor) consumes.

Design rules (see docs/video/evidence-index.md):
  * narration is copied verbatim from the script, never retyped by hand;
  * every on-screen code / terminal element is a *reference* to a real source
    file or to a label inside demo-cases.md, never inline literal text;
  * the scene durations are derived from measured narration length so the total
    lands on the 8-minute target instead of being guessed.

Run:  python scripts/video/build-storyboards.py
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
SCRIPT = REPO / "docs" / "video" / "video-script.md"
OUT = REPO / "docs" / "video" / "storyboards.json"
# Mirror copy inside the Remotion project so `import storyboards from
# "./storyboards.json"` stays inside the bundler root (no outside-root config).
OUT_MIRROR = REPO / "docs" / "video" / "remotion" / "src" / "storyboards.json"
# 由 synthesize-narration.py 产出的真实音频时长。存在时优先采用它，
# 而不是按字数推算 —— 字幕与画面才能跟真实语音对齐。
MANIFEST = REPO / "docs" / "video" / "audio-manifest.json"

# Total target in seconds (见 video-script.md 全片结构表: 8:00)
TARGET_TOTAL = 480

# 旁白语速（字/秒）。**只在没有真实音频时**用于推算时长；
# 真实时长从 audio-manifest.json 读取。
CHARS_PER_SEC = 4.05
PAUSE_SEC = 0.40  # trailing beat after each scene's narration
# 旁白结束后留给画面的余量：让镜头不至于在最后一个字上被切断。
TAIL_SEC = 1.6

# Scene-level metadata that is editorial, not photographic.
SCENE_META = {
    # 顺延后必须整体重写：原表是按旧编号写的，插入片头后 slug 与调色板
    # 全部错位一位（例如 S1 还留着旧的 "cold-open"），只会误导排查。
    "S-1": {"slug": "cold-open", "palette": "black-on-white"},
    "S0": {"slug": "four-pillars", "palette": "black-on-white"},
    "S1": {"slug": "go-mod", "palette": "black-on-white"},
    "S2": {"slug": "positioning", "palette": "dark"},
    "S3": {"slug": "inverted-index", "palette": "dark"},
    "S4": {"slug": "bm25", "palette": "dark"},
    "S5": {"slug": "phrase-query", "palette": "dark"},
    "S6": {"slug": "boolean-concurrency", "palette": "dark"},
    "S7": {"slug": "engineering-discipline", "palette": "dark"},
    "S8": {"slug": "closing", "palette": "black-on-white"},
}

# Shot plans, transcribed from each scene's 「画面描述」+「架构图或动画设计说明」
# +「需要展示的真实文件与代码」sections. x/y/w/h are 1080p canvas coordinates in
# pixels, so the renderer needs no layout guessing.
SHOTS = {
    "S-1": [
        {"type": "stat-reveal",
         "stats": [
             {"value": "4", "unit": "天", "caption": "全部开发时间"},
             {"value": "48", "unit": "次提交", "caption": "1 位作者"},
             {"value": "10,819", "unit": "行插入", "caption": "首日 78 分钟内"},
         ],
         "tagline": "唯一的协作者是一个 AI 编程工具",
         "taglineSub": "DeepSeek Harness",
         "attribution": "creator-statement",
         "region": {"x": 260, "y": 150, "w": 1400, "h": 760}},

        {"type": "statement",
         "text": "真正的难点从来不在写，在判断。",
         "attribution": "verified-facts",
         "region": {"x": 260, "y": 300, "w": 1400, "h": 420}},
    ],

    # ---- 新增片头：S0 全文搜索引擎四要点 ----
    "S0": [
        {"type": "pillar-cards",
         "pillars": [
             {"name": "倒排索引", "scene": "S3", "files": "internal/index/index.go"},
             {"name": "相关性排序", "scene": "S4", "files": "internal/scoring/bm25.go"},
             {"name": "布尔求值", "scene": "S6", "files": "internal/query/searcher.go"},
             {"name": "短语匹配", "scene": "S5", "files": "internal/query/searcher.go"},
         ],
         "collapseToAxis": True,
         "region": {"x": 80, "y": 140, "w": 1760, "h": 700}},
    ],
    "S1": [
        {"type": "code-file", "source": "go.mod", "lines": "1-7",
         "effect": "typewriter", "lineDelaySec": 0.4,
         "highlight": ["go 1.27", "github.com/go-ego/gse v1.1.0", "// indirect"],
         "region": {"x": 320, "y": 470, "w": 1280, "h": 380}},
        {"type": "title", "text": "go_tsh", "subtitle": "手写核心机制的 Go 全文搜索引擎",
         "region": {"x": 320, "y": 250, "w": 1280, "h": 170}},
    ],
    "S2": [
        {"type": "import-audit",
         "packages": ["internal/index", "internal/query", "internal/scoring", "pkg/httpapi"],
         "thirdParty": [],
         "region": {"x": 80, "y": 80, "w": 860, "h": 800}},
        {"type": "code-file", "source": "internal/analyzer/chinese.go", "lines": "3-11",
         "highlight": ["\"github.com/go-ego/gse\""],
         "region": {"x": 1000, "y": 80, "w": 840, "h": 380}},
        {"type": "bar-compare", "label": "二进制体积",
         "items": [{"name": "默认构建", "value": 42.36, "unit": "MiB"},
                   {"name": "-ldflags \"-s -w\"", "value": 38.95, "unit": "MiB"}],
         "source": "docs/video/project-brief.md#6.3",
         "region": {"x": 1000, "y": 500, "w": 840, "h": 380}},
    ],
    "S3": [
        {"type": "code-file", "source": "internal/index/types.go", "lines": "60-68",
         "highlight": ["Positions []uint32"],
         "region": {"x": 80, "y": 80, "w": 860, "h": 250}},
        {"type": "code-file", "source": "internal/index/index.go", "lines": "197-199",
         "highlight": ["\"\\x00\""],
         "region": {"x": 1000, "y": 80, "w": 840, "h": 250}},
        {"type": "three-panel",
         "panels": [
             {"kind": "text", "title": "原文",
              "source": "docs/video/demo-cases.md#3.1", "section": "### 3.1 语料与索引统计"},
             {"kind": "tokens", "title": "分析器输出",
              "source": "docs/video/demo-cases.md#4.4", "section": "### 4.4 短语「相对位置差」的决定性演示（推荐镜头）", "want": "analyzer"},
             {"kind": "postings", "title": "索引 posting",
              "source": "docs/video/demo-cases.md#3.2", "section": "### 3.2 真实 posting 列表（核心镜头）"},
         ],
         "region": {"x": 80, "y": 360, "w": 1760, "h": 380}},
        {"type": "invariant-table",
         "rows": [
             {"name": "Postings sorted by DocID asc", "value": True},
             {"name": "TF == len(Positions) and Positions strictly increasing", "value": True},
         ],
         "source": "docs/video/demo-cases.md#3.2",
         "region": {"x": 80, "y": 760, "w": 1760, "h": 160}},
    ],
    "S4": [
        {"type": "code-file", "source": "internal/scoring/bm25.go", "lines": "30-49",
         "highlight": ["math.Log(1 + ", "norm := 1 - bm.B"],
         "region": {"x": 80, "y": 80, "w": 860, "h": 440}},
        {"type": "manim", "animation": "idf-curve",
         "params": {"points": [
             {"df": 1, "idf": 6.503290}, {"df": 10, "idf": 4.557380},
             {"df": 100, "idf": 2.298597}, {"df": 400, "idf": 0.916041},
             {"df": 500, "idf": 0.693147}, {"df": 600, "idf": 0.510992},
             {"df": 900, "idf": 0.105805}, {"df": 1000, "idf": 0.000500}],
             "n": 1000, "markHalf": 500},
         "source": "docs/video/demo-cases.md#3.3",
         "region": {"x": 1000, "y": 80, "w": 840, "h": 440}},
        {"type": "score-breakdown",
         "source": "docs/video/demo-cases.md#3.3", "section": "### 3.3 BM25 逐项计算", "want": "field=",
         "emphasise": ["dl=9", "norm=0.8500", "score=0.388458"],
         "region": {"x": 80, "y": 560, "w": 1760, "h": 360}},
    ],
    "S5": [
        {"type": "terminal", "source": "docs/video/demo-cases.md#4.4", "section": "### 4.4 短语「相对位置差」的决定性演示（推荐镜头）", "want": "analyzer",
         "highlight": ["quick@0 brown@2", "quick@0 brown@1"],
         "region": {"x": 80, "y": 80, "w": 860, "h": 270}},
        {"type": "code-file", "source": "internal/query/searcher.go", "lines": "635-644",
         "highlight": ["want := int64(start) + int64(t.Position) - int64(base)"],
         "region": {"x": 80, "y": 380, "w": 860, "h": 260}},
        {"type": "terminal", "source": "docs/video/demo-cases.md#4.4", "section": "### 4.4 短语「相对位置差」的决定性演示（推荐镜头）", "want": "phrase",
         "highlight": ["total=1 ids=[gap]", "total=1 ids=[adj]", "total=2 ids=[gap adj]"],
         "region": {"x": 80, "y": 670, "w": 860, "h": 250}},
        {"type": "manim", "animation": "position-axis-align",
         "params": {"query": [{"term": "quick", "pos": 0}, {"term": "brown", "pos": 2}],
                    "gap": [{"term": "quick", "pos": 0}, {"term": "the", "pos": 1, "filtered": True},
                            {"term": "brown", "pos": 2}, {"term": "fox", "pos": 3}],
                    "adj": [{"term": "quick", "pos": 0}, {"term": "brown", "pos": 1},
                            {"term": "fox", "pos": 2}]},
         "region": {"x": 1000, "y": 80, "w": 840, "h": 470}},
        {"type": "terminal", "source": "docs/video/demo-cases.md#4.3", "section": "### 4.3 短语查询与一个真实的反例",
         "highlight": ["total=2", "total=0"],
         "region": {"x": 1000, "y": 580, "w": 840, "h": 340}},
    ],
    "S6": [
        {"type": "code-file", "source": "internal/query/searcher.go", "lines": "691-708",
         "highlight": ["addExisting(&out, unionAll(should))", "v.SortedDocIDs()"],
         "region": {"x": 80, "y": 80, "w": 860, "h": 380}},
        {"type": "terminal", "source": "docs/video/demo-cases.md#3.3", "section": "### 3.3 BM25 逐项计算",
         "highlight": ["MUST(posting list)", "MUSTNOT(list)"],
         "region": {"x": 1000, "y": 80, "w": 840, "h": 380}},
        {"type": "manim", "animation": "merge-intersect",
         "params": {"left": {"field": "posting", "docs": [1, 3, 4]},
                    "right": {"field": "list", "docs": [1, 3]}},
         "region": {"x": 80, "y": 500, "w": 860, "h": 420}},
        {"type": "terminal", "source": "docs/video/demo-cases.md#6.4", "section": "### 6.4 并发契约的三条实测边界",
         "highlight": ["deadlock confirmed", "freezes reads too", "write lock IS held"],
         "region": {"x": 1000, "y": 500, "w": 840, "h": 420}},
    ],
    "S7": [
        {"type": "bar-compare", "label": "代码行数",
         "items": [{"name": "源码", "value": 10501, "unit": "行"},
                   {"name": "测试", "value": 12530, "unit": "行"}],
         "source": "docs/video/project-brief.md#6.1", "ratioLabel": "1 : 1.19",
         "region": {"x": 80, "y": 80, "w": 860, "h": 400}},
        {"type": "coverage-grid",
         "items": [{"pkg": "internal/scoring", "pct": 100.0},
                   {"pkg": "internal/tablename", "pct": 100.0},
                   {"pkg": "internal/analyzer", "pct": 95.9},
                   {"pkg": "internal/query", "pct": 92.3},
                   {"pkg": "internal/index", "pct": 90.5},
                   {"pkg": "pkg/tsh", "pct": 83.2}],
         "source": "docs/video/project-brief.md#6.2",
         "region": {"x": 1000, "y": 80, "w": 840, "h": 400}},
        {"type": "terminal", "source": "docs/video/project-brief.md#7", "section": "## 7. 质量门禁的真实状态",
         "highlight": ["==> gofmt -l (formatting)", "==> go vet",
                       "==> apicheck (external consumer)", "==> go test -race",
                       "check passed"],
         "region": {"x": 80, "y": 520, "w": 1760, "h": 400}},
    ],
    "S8": [
        {"type": "doc-table", "source": "README.md", "lines": "56-68",
         "fadeInRows": True, "region": {"x": 220, "y": 90, "w": 1480, "h": 600}},
        {"type": "title", "text": "go_tsh",
         "subtitle": "Go 1.27 · 手写倒排索引 · BM25 · 单一第三方依赖",
         "region": {"x": 220, "y": 740, "w": 1480, "h": 160}},
    ],
}

# The negative constraints, copied from video-script.md 附录 B so the renderer
# and any reviewing human see them in machine-readable form.
FORBIDDEN = [
    "不启用中文分词时二进制约 6.7 MB",
    "任何毫秒级延迟数字（除非同时标注「项目自测，本次未复现」）",
    "注释中的历史 pprof 数字：37.4% / 13.7% / 11.2% / 0.7ms / 400KB / 26ms",
    "「AI 编写了某段代码」或「AI 发现了某个 bug」",
    "「删掉的文档还能被搜到」",
    "「线上可被 1 MB 查询打崩」",
]


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def strip_blockquote(line: str) -> str:
    return re.sub(r"^\s*>\s?", "", line).strip()


def narration_for_caption(raw: str) -> str:
    """把旁白里的 markdown 标记剥掉，供字幕显示使用。

    为什么在构建期做、而不是在渲染器里做：
      与 `resolved` 的设计一致 —— 所有文本转换都在建造期完成，渲染器只消费成品。
      否则同一套剥离规则要在 TS 与 Python 里各写一份，迟早走偏
      （注音表已经在 Python 侧的 prepare-narration.py 里了）。

    踩过的坑：Caption 直接渲染 `narration` 字段，画面上出现 `**倒排索引**`，
    星号被读了出来。全片 10 个场景里有 8 个受影响。
    """
    s = raw
    s = re.sub(r"`([^`]*)`", r"\1", s)                  # 行内代码
    s = re.sub(r"\*\*([^*]*)\*\*", r"\1", s)            # 粗体
    s = re.sub(r"(?<!\*)\*([^*]+)\*(?!\*)", r"\1", s)   # 斜体
    s = re.sub(r"^>\s?", "", s, flags=re.M)             # 引用符号
    s = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", s)      # 链接
    s = "\n".join(line.strip() for line in s.split("\n"))
    return re.sub(r"\n{2,}", "\n", s).strip()

def parse_scenes(script: str) -> list[dict]:
    """Split the script into scenes and pull each scene's narration verbatim."""
    # Scene headers look like:  ## S4 · 短语查询：差 1 还是差 2（全片高潮）
    # 场景标题形如：  ## S-1 · 片头：...   ## S4 · BM25：...
    # 编号里的负号是必须支持的：片头用 S-1 表示「在原 S0 之前」。
    # 早先写成 (S\d) 会把 S-1 整段静默丢掉，且不报错——只能靠场景数对不上才发现。
    header = re.compile(r"^## (S-?\d) · (.+?)\s*$", re.M)
    marks = list(header.finditer(script))
    scenes = []
    for i, m in enumerate(marks):
        sid, title = m.group(1), m.group(2)
        body = script[m.end(): marks[i + 1].start() if i + 1 < len(marks) else len(script)]

        # Duration: from the scene's 「**时长**：0:35」 line.
        dur_m = re.search(r"\*\*时长\*\*：(\d+):(\d+)", body)
        declared = int(dur_m.group(1)) * 60 + int(dur_m.group(2)) if dur_m else None

        # Narration: everything between 「### 旁白全文」 and the next ### heading.
        nar_m = re.search(r"### 旁白全文\s*\n(.*?)(?=\n###|\Z)", body, re.S)
        if not nar_m:
            raise SystemExit(f"scene {sid}: no narration block found")
        lines = [strip_blockquote(l) for l in nar_m.group(1).splitlines()]
        lines = [l for l in lines if l]
        narration = "\n".join(lines)

        meta = SCENE_META.get(sid, {})
        scenes.append({
            "id": sid,
            "slug": meta.get("slug", sid.lower()),
            "title": title,
            "declaredDurationSec": declared,
            "narration": narration,
            "narrationPlain": narration_for_caption(narration),
            "narrationChars": len(re.sub(r"\s", "", narration)),
            "palette": meta.get("palette", "dark"),
            "shots": SHOTS.get(sid, []),
        })
    return scenes


def load_audio_durations() -> dict[str, float]:
    """读取真实旁白音频时长（秒）；没有 manifest 时返回空字典。"""
    if not MANIFEST.is_file():
        return {}
    try:
        doc = json.loads(MANIFEST.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return {}
    out: dict[str, float] = {}
    for s in doc.get("scenes", []):
        d = s.get("durationSec")
        if isinstance(d, (int, float)) and d > 0:
            out[s["id"]] = float(d)
    return out


def retime(scenes: list[dict]) -> str:
    """确定每个场景的时长；返回实际采用的方式（"audio" / "estimate"）。

    两条路径：

      1. 有真实旁白音频（audio-manifest.json 存在）：
         场景时长 = 音频实际时长 + TAIL_SEC，即画面跟着语音走；
         字幕预算 = 音频真实时长，两者严格同步。
         这条路径下**不再**把总长压到 TARGET_TOTAL —— 成片该多长就多长，
         强行凑满 8:00 会重新引入画面与语音的错位。

      2. 只有文本（还没配音）：按 CHARS_PER_SEC 推算，与编辑声明的时长加权，
         再把总长压到 TARGET_TOTAL。
    """
    audio = load_audio_durations()

    if audio:
        for s in scenes:
            real = audio.get(s["id"])
            if real is None:
                raise SystemExit(
                    f"audio-manifest.json 缺少场景 {s['id']} 的时长。"
                    "请先把 8 段音频都合成完再重建分镜，否则字幕会与语音错位。"
                )
            s["audioDurationSec"] = round(real, 2)
            s["narrationBudgetSec"] = round(real, 2)
            s["durationSec"] = round(real + TAIL_SEC)
        return "audio"

    for s in scenes:
        s["_raw"] = s["narrationChars"] / CHARS_PER_SEC + PAUSE_SEC

    raw_total = sum(s["_raw"] for s in scenes)
    factor = TARGET_TOTAL / raw_total

    for s in scenes:
        measured = s["_raw"] * factor
        declared = s["declaredDurationSec"] or measured
        s["durationSec"] = round(measured * 0.6 + declared * 0.4)
        del s["_raw"]

    drift = TARGET_TOTAL - sum(s["durationSec"] for s in scenes)
    longest = max(scenes, key=lambda s: s["durationSec"])
    longest["durationSec"] += drift
    return "estimate"

def collect_anchors(scenes: list[dict]) -> list[tuple[str, str]]:
    """Enumerate every (file, exact markdown heading) the shots reference.

    A `section` that does not exist is a configuration error: the renderer would
    otherwise silently draw an empty panel on camera.
    """
    out: list[tuple[str, str]] = []
    for s in scenes:
        for shot in s["shots"]:
            src = shot.get("source")
            if src and shot.get("section"):
                out.append((src.split("#", 1)[0], shot["section"]))
            for panel in shot.get("panels") or []:
                psrc = panel.get("source")
                if psrc and panel.get("section"):
                    out.append((psrc.split("#", 1)[0], panel["section"]))
    return out


def validate_anchors(anchors: list[tuple[str, str]]) -> list[dict]:
    """Verify each heading exists in its file, and report the matched line."""
    cache: dict[str, list[str]] = {}
    checked: list[dict] = []
    missing: list[str] = []

    for rel, heading in anchors:
        if rel not in cache:
            path = REPO / rel
            if not path.is_file():
                missing.append(f"{rel} (文件不存在)")
                continue
            cache[rel] = path.read_text(encoding="utf-8").splitlines()

        want = heading.strip()
        hits = [i + 1 for i, l in enumerate(cache[rel]) if l.strip() == want]
        if not hits:
            missing.append(f"{rel} :: {want}")
            continue
        checked.append({"file": rel, "heading": want, "line": hits[0]})

    if missing:
        raise SystemExit("storyboards 引用了不存在的标题：\n  " + "\n  ".join(sorted(set(missing))))
    return checked


def collect_sources(scenes: list[dict]) -> list[tuple[str, str | None]]:
    """Enumerate every (repo-relative file, line range) the shots refer to."""
    out: list[tuple[str, str | None]] = []
    for s in scenes:
        for shot in s["shots"]:
            t = shot.get("type")
            src = shot.get("source")
            if src and t in {"code-file", "doc-table", "terminal"}:
                out.append((src, shot.get("lines")))
    return out


def resolve_sources(refs: list[tuple[str, str | None]]) -> dict:
    """Read each referenced file and slice the requested line range.

    A source may carry a `#fragment` (an anchor into a markdown document). The
    fragment is part of the citation, not part of the path, so it is stripped
    before reading the file.

    The rendered text therefore comes from the real file on disk, not from a
    transcription. A reference that cannot be resolved is a hard error, so a
    renamed file fails the build instead of silently rendering nothing.
    """
    resolved: dict[str, dict] = {}
    missing: list[str] = []

    for rel, rng in refs:
        path_rel = rel.split("#", 1)[0]
        if path_rel in resolved:
            continue
        path = REPO / path_rel
        if not path.is_file():
            missing.append(path_rel)
            continue
        text = path.read_text(encoding="utf-8")
        lines = text.splitlines()
        entry: dict = {
            "lineCount": len(lines),
            "content": text,
            "citedAs": rel,
        }
        if rng:
            lo_s, hi_s = rng.split("-")
            lo, hi = int(lo_s), int(hi_s)
            if lo < 1 or hi > len(lines) or lo > hi:
                raise SystemExit(f"{path_rel}: line range {rng} out of bounds (file has {len(lines)} lines)")
            entry["slice"] = "\n".join(lines[lo - 1: hi])
            entry["sliceRange"] = [lo, hi]
        resolved[path_rel] = entry

    if missing:
        raise SystemExit("storyboards reference missing files: " + ", ".join(sorted(set(missing))))
    return resolved


def main() -> int:
    script = read(SCRIPT)
    scenes = parse_scenes(script)
    timing = retime(scenes)

    total = sum(s["durationSec"] for s in scenes)
    # 有真实音频时不强求 8:00：画面跟着语音走，总长该多长就多长。
    if timing == "estimate" and total != TARGET_TOTAL:
        print(f"warning: durations sum to {total}s, expected {TARGET_TOTAL}s", file=sys.stderr)

    cursor = 0.0
    for s in scenes:
        s["startSec"] = round(cursor, 2)
        s["endSec"] = round(cursor + s["durationSec"], 2)
        cursor += s["durationSec"]
        # 只有在没有真实音频时才用字数推算字幕预算；
        # 有音频时 retime() 已把预算设为音频真实时长，这里覆盖会导致字幕与语音错位。
        if timing == "estimate":
            s["narrationBudgetSec"] = round(s["narrationChars"] / CHARS_PER_SEC, 2)
        s.setdefault("audioDurationSec", None)
        s["hasAudio"] = s["audioDurationSec"] is not None
        s["audioFile"] = f"audio/{s['id']}.wav" if s["hasAudio"] else None

    resolved = resolve_sources(collect_sources(scenes))
    anchors = validate_anchors(collect_anchors(scenes))

    doc = {
        "$schema": "./storyboards.schema.json",
        "meta": {
            "project": "go_tsh",
            "episode": 1,
            "title": "go_tsh 技术纪录片 第一期",
            "sourceScript": "docs/video/video-script.md",
            "evidenceIndex": "docs/video/evidence-index.md",
            "totalDurationSec": total,
            "resolution": {"width": 1920, "height": 1080, "fps": 30},
            "generatedBy": "scripts/video/build-storyboards.py",
            "narrationCharsPerSec": CHARS_PER_SEC,
            "aspect": "16:9",
        },
        "audio": {
            "narrationLang": "zh-CN",
            "charsPerSec": CHARS_PER_SEC,
            "pauseAfterSceneSec": PAUSE_SEC,
            "voiceStyle": "冷静、克制、纪录片解说；中英混排处英文按字母读",
            # timing 为 "audio" 时，各场景的 narrationBudgetSec 是**真实音频时长**，
            # 字幕必须按它推进；为 "estimate" 时才是按字数推算。
            "timingSource": timing,
            "tailSec": TAIL_SEC,
            "perSceneFiles": timing == "audio",
        },
        "renderPolicy": {
            "codeSourceOfTruth": True,
            "terminalSourceOfTruth": True,
            "note": "所有 type=code-file / terminal / doc-table 的元素必须从 source "
                    "指向的真实文件或 docs/video/demo-cases.md 的实测输出读取后原样渲染，"
                    "禁止由模型生成、补全或改写其中任何字符。"
                    "resolved 是构建时从磁盘读取的快照，渲染器一律使用它，"
                    "不得在渲染期访问文件系统。",
            "forbidden": FORBIDDEN,
        },
        # Build-time snapshot of every referenced file, so the renderer needs no
        # filesystem access and rendered code can never drift from the repo.
        "resolved": resolved,
        # 已校验存在的 markdown 标题锚点（渲染器按精确标题取代码块）。
        "anchors": anchors,
        "scenes": scenes,
    }

    payload = json.dumps(doc, ensure_ascii=False, indent=2) + "\n"
    OUT.write_text(payload, encoding="utf-8")
    OUT_MIRROR.parent.mkdir(parents=True, exist_ok=True)
    OUT_MIRROR.write_text(payload, encoding="utf-8")

    print(f"wrote {OUT.relative_to(REPO)}")
    print(f"wrote {OUT_MIRROR.relative_to(REPO)}  (mirror for the Remotion bundler)")
    print(f"  scenes={len(scenes)}  total={total}s ({total // 60}:{total % 60:02d})")
    print(f"  resolved sources={len(resolved)}  validated anchors={len(anchors)}")
    print(f"  {'id':<4} {'dur':>5} {'chars':>6} {'budget':>7}  title")
    for s in scenes:
        print(f"  {s['id']:<4} {s['durationSec']:>4}s {s['narrationChars']:>6} "
              f"{s['narrationBudgetSec']:>6}s  {s['title'][:34]}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
