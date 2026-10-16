"""Turn the storyboard narration into text that a TTS engine reads correctly.

Why this exists
---------------
`video-script.md` is written for a human reader. Fed raw to a TTS engine it
mispronounces several things, and the mistakes are the kind that only surface
when you listen:

    BM25        -> "bm二十五"        (should be B-M-二十五)
    go.mod      -> "go点mod"         (should be go点M-O-D)
    8、15、20    -> "八十五二十"      (should be 八、十五、二十)
    quick@0     -> "quick at 0"      (should be quick 在位置 0)
    NUL         -> "null"            (should be N-U-L)
    24–90 秒    -> 破折号被吞掉

Output
------
    docs/video/narration.json   机器可读：逐段的 TTS 输入文本 + 每处改写记录
    docs/video/narration.md     人可读：审核用，逐句对照"原文 -> 朗读文本"

Both are derived from docs/video/storyboards.json, which is itself derived from
video-script.md, so nothing is retyped by hand.

Run:  python scripts/video/prepare-narration.py
"""

from __future__ import annotations

import json
import re
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
STORYBOARDS = REPO / "docs" / "video" / "storyboards.json"
OUT_JSON = REPO / "docs" / "video" / "narration.json"
OUT_MD = REPO / "docs" / "video" / "narration.md"

# ---------------------------------------------------------------------------
# 术语读音表
#
# 规则按顺序应用；每条规则都记录在改写日志里，便于人工复核。
# value 是**朗读文本**，不是拼写替换 —— 例如 BM25 要读成 "B M 二十五"。
# ---------------------------------------------------------------------------
TERMS: list[tuple[str, str, str]] = [
    # (正则, 朗读文本, 说明)
    (r"\bgo\.mod\b", "go点M O D", "文件扩展名按字母读"),
    (r"\bBM25\b", "B M 二十五", "字母数字混排，数字读中文"),
    (r"\bNUL\b", "N U L", "避免读成 null"),
    (r"\bidf\b", "I D F", "小写缩写按字母读"),
    (r"\bIDF\b", "I D F", "大写缩写按字母读"),
    (r"\bMB\b", "兆字节", "单位读中文"),
    (r"\bMiB\b", "兆字节", "单位读中文"),
    (r"\bHTTP\b", "H T T P", "协议名按字母读"),
    (r"\bAND\b", "and", "布尔算子读英文单词"),
    (r"\bOR\b", "or", "布尔算子读英文单词"),
    (r"\bNOT\b", "not", "布尔算子读英文单词"),
    (r"\bPosting\b", "posting", "英文术语保持小写读法"),
]

# 数学/结构符号 -> 口语句式
SYMBOLS: list[tuple[str, str, str]] = [
    # quick@0 / quick@2 这类位置记法
    (r"([A-Za-z][A-Za-z0-9_]*)\s?@\s?(\d+)", r"\1 在位置 \2", "位置记法改口语"),
    (r"–", "到", "en dash 读作「到」"),
    (r"—", "，", "em dash 改为停顿"),
    (r"[、]", "，", "顿号改逗号（部分引擎会吞掉）"),
    (r"→", "变成", "箭头读作「变成」"),
    (r"\s*->\s*", " 变成 ", "ASCII 箭头改口语"),
    (r"\s*<-\s*", " 注 ", "反向箭头表示注释"),
]

# ---------------------------------------------------------------------------
# markdown 标记：旁白是写在 markdown 里的，反引号 / ** / 书名号都不该被读出来。
#
# 这一步必须**先于**符号替换执行：
#   原文 "丢掉——但它的**位置被保留**。"
#   若先做 em dash -> 逗号，就得到 "丢掉，但它的**位置被保留**。"
#   再剥 ** 时逗号已经就位，不会产生问题；但如果顺序颠倒，
#   "，**位置" 这类相邻标点在剥除后也可能留下孤立的逗号。
# ---------------------------------------------------------------------------
MARKDOWN: list[tuple[str, str, str]] = [
    (r"```[\s\S]*?```", "", "代码块整段不读"),
    (r"`([^`]*)`", r"\1", "行内代码去掉反引号"),
    (r"\*\*([^*]*)\*\*", r"\1", "粗体去掉星号"),
    (r"\*([^*]*)\*", r"\1", "斜体去掉星号"),
    # ⚠️ 「」**必须保留**。
    # 第一版把它们剥掉，结果相邻的引用项直接粘连：
    #   「A 和 B」「A 或 B」「不要 C」  ->  A 和 BA 或 B 不要 C
    # 读出来会变成「和BA」，语义全错。而且全片 10 个场景里有 15 处「」，
    # 影响面并不小。多数中文 TTS 会把「」处理成自然停顿，正是我们想要的。
    (r"《([^》]*)》", r"\1", "书名号去掉"),
    (r"\[([^\]]*)\]\([^)]*\)", r"\1", "markdown 链接只读文字"),
    (r"^>\s?", "", "去掉引用符号"),
]

# 连续标点去重：替换后常见 "，，" / "，。"
PUNCT_DEDUP = [
    (r"，\s*，+", "，"),
    (r"，\s*。", "。"),
    (r"、\s*，+", "，"),
    (r"\s+，", "，"),
    (r"，\s+", "，"),
    (r"，\s*$", "。"),
]

# 中英之间补空格。
# 剥掉反引号后会出现 "的短语是quick brown" 这种紧贴，
# 中文引擎在无空格处容易把英文单词与相邻汉字连读，加一个空格更稳。
SPACING = [
    (r"(?<=[\u4e00-\u9fff])(?=[A-Za-z0-9])", " ", "中文与英文之间补空格"),
    (r"(?<=[A-Za-z0-9])(?=[\u4e00-\u9fff])", " ", "英文与中文之间补空格"),
    (r"(?<=[\u4e00-\u9fff])\s+(?=[，。！？；：])", "", "标点前不留空格"),
]

DIGITS = "零一二三四五六七八九"

# 需要读成中文的数字（计数器、序号），而不是数值
CN_NUMBERS: list[tuple[str, str]] = [
    (r"(?<![0-9])1 个", "一个"),
    (r"(?<![0-9])2 个", "两个"),
    (r"(?<![0-9])3 个", "三个"),
]


def digits_to_chinese(num: str) -> str:
    """把一串数字逐位读出来（用于 480、1998 这类需要逐位或成组读的数字）。"""
    if len(num) == 1:
        return DIGITS[int(num)]
    # 两位以上按位读，避免"一千零一"这类歧义
    return "".join(DIGITS[int(c)] for c in num)


def normalize(text: str) -> tuple[str, list[dict]]:
    """返回（朗读文本, 改写日志）。"""
    out = text
    log: list[dict] = []
    # 用不可打印占位符保护每一步的替换结果，避免后续规则把替换文本内部再改一遍
    # （踩过的坑：BM25 -> "B M 二十五" 之后，中英补空格规则把 "go点M O D"
    #   进一步改成 "go 点 M O D"，读出来变成 "go 点 M O D"）。
    held: list[str] = []

    def hold(s: str) -> str:
        held.append(s)
        return f"\x00{len(held) - 1}\x00"

    def restore(s: str) -> str:
        """把保护段还原回文本。

        必须在**每个阶段之间**调用。踩过的坑：剥反引号时把 `go.mod` 保护成
        \\x000\\x00，而还原只在函数末尾做一次，于是后续的术语表看到的是占位符，
        `go.mod` 永远匹配不上，读出来就是原样的 "go.mod"。
        """
        for i, v in enumerate(held):
            s = s.replace(f"\x00{i}\x00", v)
        held.clear()
        return s

    # 1) 先剥 markdown 标记（必须在符号替换之前，见 MARKDOWN 的注释）
    #    注意 lambda 的默认参数绑定：`lambda m: hold(repl)` 会让所有迭代共享同一个
    #    repl/out（延迟绑定），替换结果会互相覆盖。踩过一次，务必保留 `r=repl`。
    for pattern, repl, why in MARKDOWN:
        hits = re.findall(pattern, out, re.MULTILINE)
        if hits:
            if "\\1" in repl:
                out = re.sub(
                    pattern,
                    lambda m, p=pattern, r=repl: hold(re.sub(p, r, m.group(0), flags=re.MULTILINE)),
                    out,
                    flags=re.MULTILINE,
                )
            else:
                out = re.sub(pattern, repl, out, flags=re.MULTILINE)
            log.append({"kind": "markdown", "pattern": pattern, "to": repl or "(删除)",
                        "why": why, "hits": len(hits)})
    # 剥完立刻还原：否则术语表看到的是占位符（见 restore 的注释）
    out = restore(out)

    # 2) 术语读音（整段保护）
    for pattern, repl, why in TERMS:
        hits = re.findall(pattern, out)
        if hits:
            out = re.sub(pattern, lambda m, r=repl: hold(r), out)
            log.append({"kind": "term", "pattern": pattern, "to": repl, "why": why, "hits": len(hits)})

    # 3) 符号口语化
    for pattern, repl, why in SYMBOLS:
        hits = re.findall(pattern, out)
        if hits:
            if "\\1" in repl:
                out = re.sub(pattern, lambda m, p=pattern, r=repl: hold(re.sub(p, r, m.group(0))), out)
            else:
                out = re.sub(pattern, repl, out)
            log.append({"kind": "symbol", "pattern": pattern, "to": repl, "why": why, "hits": len(hits)})

    # 4) 数字
    for pattern, repl in CN_NUMBERS:
        if re.search(pattern, out):
            out = re.sub(pattern, lambda m, r=repl: hold(r), out)
            log.append({"kind": "number", "pattern": pattern, "to": repl, "why": "口语计数", "hits": 1})

    if re.search(r"\d{1,3}(,\d{3})+", out):
        out = re.sub(r"(\d{1,3}(?:,\d{3})+)", lambda m: m.group(1).replace(",", ""), out)
        log.append({"kind": "number", "pattern": "千分位逗号", "to": "去掉逗号", "why": "避免逐位读", "hits": 1})

    # 5) 清理替换产生的连续标点
    for pattern, repl in PUNCT_DEDUP:
        if re.search(pattern, out):
            out = re.sub(pattern, repl, out)

    # 5b) 中英之间补空格（保护段不参与）
    for pattern, repl, why in SPACING:
        if re.search(pattern, out):
            n = len(re.findall(pattern, out))
            out = re.sub(pattern, repl, out)
            if why != "标点前不留空格":
                log.append({"kind": "spacing", "pattern": pattern, "to": repl, "why": why, "hits": n})

    # 6) 还原被保护的替换结果（正常情况下 held 已被各阶段清空，这里是兜底）
    out = restore(out)

    # 7) 收尾
    out = re.sub(r"[ \t]{2,}", " ", out)
    out = "\n".join(line.strip() for line in out.split("\n"))
    out = re.sub(r"\n{2,}", "\n", out).strip()
    return out, log


def split_for_tts(text: str, max_chars: int = 60) -> list[str]:
    """切句。

    ⚠️ **必须尊重原文换行**。旁白是按「一行一口气」写的，
    行边界就是作者标注的停顿点。第一版只按标点切、忽略换行，
    结果把不同行的内容并成一句——例如

        用户会写「A 和 B」「A 或 B」「不要 C」，
        这些条件得能在文档集合上算出来。

    会被并成一句，读起来赶，而且与后面字幕的分屏对不上。

    规则（按优先级）：
      1. 原文换行 = 硬边界，绝不跨越；
      2. 行内按。！？；再切；
      3. 仍超长才按逗号切。
    """
    chunks: list[str] = []
    for raw_line in text.split("\n"):
        line = raw_line.strip()
        if not line:
            continue
        # 行内先按句末标点切
        for part in re.split(r"(?<=[。！？；])", line):
            part = part.strip()
            if not part:
                continue
            if len(part) <= max_chars:
                chunks.append(part)
                continue
            # 仍超长，再按逗号切
            for sub in re.split(r"(?<=[，,])", part):
                sub = sub.strip()
                if sub:
                    chunks.append(sub)
    return chunks


def estimate_seconds(text: str, chars_per_sec: float) -> float:
    return round(len(re.sub(r"\s", "", text)) / chars_per_sec, 2)


def main() -> int:
    doc = json.loads(STORYBOARDS.read_text(encoding="utf-8"))
    cps = doc["meta"]["narrationCharsPerSec"]

    scenes_out = []
    md: list[str] = [
        "# 旁白朗读文本（TTS 就绪）",
        "",
        "> 由 `scripts/video/prepare-narration.py` 从 `storyboards.json` 生成，**请勿手改**。",
        "> 「朗读文本」才是喂给 TTS 的字符串；「原始旁白」保留供对照。",
        "> 逐条核对改写是否合理，特别是术语读音。",
        "",
    ]

    total_chars = 0
    total_secs = 0.0

    for scene in doc["scenes"]:
        raw = scene["narration"]
        norm, log = normalize(raw)
        chunks = split_for_tts(norm)
        secs = estimate_seconds(norm, cps)
        total_chars += len(re.sub(r"\s", "", norm))
        total_secs += secs

        scenes_out.append(
            {
                "id": scene["id"],
                "title": scene["title"],
                "ttsText": norm,
                "chunks": chunks,
                "rawNarration": raw,
                "edits": log,
                "estimatedDurationSec": secs,
                "sceneDurationSec": scene["durationSec"],
                "audioFile": f"audio/{scene['id']}.wav",
            }
        )

        md.append(f"## {scene['id']} · {scene['title']}")
        md.append("")
        md.append(f"- 预估时长：**{secs}s**（场景分配 {scene['durationSec']}s）")
        md.append(f"- 切句数：{len(chunks)}")
        md.append("")
        if log:
            md.append("改写记录：")
            md.append("")
            md.append("| 类型 | 命中 | 改为 | 原因 | 次数 |")
            md.append("| --- | --- | --- | --- | --- |")
            for e in log:
                pat = e["pattern"].replace("|", "\\|")
                md.append(f"| {e['kind']} | `{pat}` | {e['to']} | {e['why']} | {e['hits']} |")
            md.append("")
        else:
            md.append("（无需改写）")
            md.append("")
        md.append("**朗读文本（逐句）：**")
        md.append("")
        for i, c in enumerate(chunks, 1):
            md.append(f"{i}. {c}")
        md.append("")
        md.append("<details><summary>原始旁白</summary>")
        md.append("")
        for line in raw.split("\n"):
            md.append(f"> {line}")
        md.append("")
        md.append("</details>")
        md.append("")
        md.append("---")
        md.append("")

    payload = {
        "meta": {
            "source": "docs/video/storyboards.json",
            "generatedBy": "scripts/video/prepare-narration.py",
            "charsPerSec": cps,
            "totalChars": total_chars,
            "totalEstimatedSec": round(total_secs, 1),
            "episodeDurationSec": doc["meta"]["totalDurationSec"],
            "note": "ttsText 是喂给 TTS 的最终字符串；rawNarration 仅供对照。",
        },
        "scenes": scenes_out,
    }

    OUT_JSON.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    OUT_MD.write_text("\n".join(md).rstrip("\n") + "\n", encoding="utf-8")

    print(f"wrote {OUT_JSON.relative_to(REPO)}")
    print(f"wrote {OUT_MD.relative_to(REPO)}")
    print(f"  scenes={len(scenes_out)}  字数={total_chars}  预估总时长={total_secs:.1f}s "
          f"（成片 {doc['meta']['totalDurationSec']}s）")
    print()
    print(f"  {'id':<4} {'字数':>5} {'预估':>7} {'分配':>6} {'切句':>5}  改写")
    for s in scenes_out:
        print(f"  {s['id']:<4} {len(re.sub(r'[ ]', '', s['ttsText'])):>5} "
              f"{s['estimatedDurationSec']:>6}s {s['sceneDurationSec']:>5}s "
              f"{len(s['chunks']):>5}  {len(s['edits'])}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
