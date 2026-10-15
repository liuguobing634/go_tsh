import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { textFor } from "../data";
import { extract } from "../markdown";
import { COLORS, FONT_MONO, FONT_SANS, SIZES, paletteFor } from "../theme";
import type { Scene, Shot } from "../types";

export type ScoreBreakdownShotProps = { shot: Extract<Shot, { type: "score-breakdown" }>; scene: Scene };

/**
 * BM25 逐项计算镜头。
 *
 * 数值来自 docs/video/demo-cases.md 的实测输出；`emphasise` 里的片段
 * （例如 dl=9 / norm=0.8500 / score=0.388458）必须在画面中被强调，
 * 因为「短字段得分更高」这个结论完全依赖这三个数字。
 */
export const ScoreBreakdownShot: React.FC<ScoreBreakdownShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const content = textFor(shot.source, false);
  const excerpt = extract({
    markdown: content,
    section: shot.section,
    want: shot.want,
    label: shot.label,
  });
  const lines = excerpt.split("\n").filter((l) => l.trim() !== "");
  const perLine = Math.round(fps * 0.35);
  const revealed = Math.max(1, Math.floor(frame / perLine) + 1);
  const visible = lines.slice(0, Math.min(lines.length, revealed));
  const emphasise = (shot.emphasise ?? []).map((e) => e.trim());

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 80,
        top: shot.region?.y ?? 620,
        width: shot.region?.w ?? 1760,
        height: shot.region?.h ?? 360,
        backgroundColor: pal.panel,
        border: `1px solid ${pal.border}`,
        borderRadius: 10,
        padding: "18px 24px",
        fontFamily: FONT_MONO,
      }}
    >
      <div style={{ fontFamily: FONT_SANS, fontSize: SIZES.label + 4, color: pal.dim, marginBottom: 14 }}>
        逐项计算：IDF × 词频饱和 ÷ 长度归一化
      </div>

      {visible.map((line, i) => {
        const hit = emphasise.some((e) => line.includes(e));
        return (
          <div
            key={i}
            style={{
              whiteSpace: "pre-wrap",
              fontSize: SIZES.codeSmall,
              lineHeight: 1.6,
              color: hit ? COLORS.accentWarm : pal.fg,
              fontWeight: hit ? 600 : 400,
            }}
          >
            {line}
          </div>
        );
      })}
    </div>
  );
};

