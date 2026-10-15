"""Wrap the real-data blocks in demo-cases.md that the storyboards need as code
fences, so the renderer can extract them by exact heading.

Idempotent: a block that already sits inside a fence is left alone.
"""

from __future__ import annotations

from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
DOC = REPO / "docs" / "video" / "demo-cases.md"

# (anchor line that introduces the block, list of block body lines)
BLOCKS = [
    (
        '真实语料上的完整计算链（`term = "posting"`）：',
        [
            'field="body" N=4 df=3 idf=0.356675 avgdl=11.2500',
            '    doc=d1   tf=1 dl=11   norm=0.9833  ->  score=0.359947',
            '    doc=d3   tf=1 dl=9    norm=0.8500  ->  score=0.388458',
            '    doc=d4   tf=1 dl=11   norm=0.9833  ->  score=0.359947',
        ],
        "text",
    ),
    (
        "边界条件（直接调用）：",
        [
            "IDF(df=5, N=0)              0",
            "Score(tf=0, ...)            0",
            "Score(avgDocLen=0)          0",
            "Score(tf=1, dl=avgdl)       1",
            "Score(tf=100, dl=avgdl)     2.173913043478261",
            "Score(tf=1, dl=10*avgdl)    0.21359223300970878",
        ],
        "text",
    ),
    (
        "limit=2    offset=0   total=3 page=[d1 d3]",
        [
            "limit=2    offset=0   total=3 page=[d1 d3]",
            "limit=2    offset=2   total=3 page=[d4]",
            "limit=0    offset=0   total=3 page=[d1 d3 d4]     <- limit=0 取默认 10",
            "limit=1000 offset=0   total=3 page=[d1 d3 d4]     <- limit>100 截到 100",
        ],
        "text",
    ),
    (
        "scores=0,0,0,0,0,0,0,0",
        [
            "total=8",
            "  bm25-ranking, boolean-queries, chinese-recall-precision, chinese-segmentation,",
            "  latency-budget, phrase-queries, stopword-phrase, tokenization",
            "scores=0,0,0,0,0,0,0,0",
        ],
        "text",
    ),
]


def main() -> int:
    text = DOC.read_text(encoding="utf-8")
    lines = text.split("\n")
    added = 0

    for anchor, body, lang in BLOCKS:
        first = body[0]
        # locate the block body
        try:
            start = lines.index(first)
        except ValueError:
            print(f"SKIP (body not found): {first[:48]}")
            continue

        end = start + len(body)
        # Already fenced?
        if start > 0 and lines[start - 1].strip().startswith("```"):
            print(f"SKIP (already fenced): {first[:48]}")
            continue

        lines[end:end] = ["```"]
        lines[start:start] = [f"```{lang}"]
        added += 1
        print(f"fenced: {first[:48]}")

    if added:
        # keep exactly one trailing newline
        DOC.write_text("\n".join(lines).rstrip("\n") + "\n", encoding="utf-8")
    print(f"blocks fenced: {added}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
