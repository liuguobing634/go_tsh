import React from "react";
import { useCurrentFrame } from "remotion";
import { textFor } from "../data";
import { extract } from "../markdown";
import { COLORS, FONT_MONO, SIZES, paletteFor } from "../theme";
import type { Scene, Shot } from "../types";

export type TerminalShotProps = { shot: Extract<Shot, { type: "terminal" }>; scene: Scene };

/**
 * 终端镜头。
 *
 * `source` 指向 docs/video/demo-cases.md（或 project-brief.md）——本次分析的
 * 实测输出记录。定位方式是从该文件的快照里按**精确标题**取代码块，
 * 数字因此无法被"顺手美化"，也不可能抽到别的章节。
 */
export const TerminalShot: React.FC<TerminalShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const pal = paletteFor(scene.palette);

  const content = textFor(shot.source, false);
  const excerpt = extract({
    markdown: content,
    section: shot.section,
    want: shot.want,
    label: shot.label,
  });

  const lines = excerpt.split("\n").filter((l, i) => l.length > 0 || i === 0);
  const perLine = 6; // 每行 0.2s @30fps
  const revealed = Math.max(1, Math.floor(frame / perLine));
  const visible = lines.slice(0, Math.min(lines.length, revealed));
  const highlight = (shot.highlight ?? []).map((h) => h.trim());

  // 自动缩小字号以适配框宽，保证实测输出不被裁切（数字必须完整可读）。
  const boxW = shot.region?.w ?? 860;
  const longest = Math.max(...lines.map((l) => l.length), 1);
  const fitted = Math.floor((boxW - 46) / (longest * 0.602));
  const fontSize = Math.max(12, Math.min(SIZES.terminal, fitted));

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 80,
        top: shot.region?.y ?? 140,
        width: boxW,
        height: shot.region?.h ?? 300,
        backgroundColor: "#010409",
        border: `1px solid ${pal.border}`,
        borderRadius: 10,
        overflow: "hidden",
        fontFamily: FONT_MONO,
      }}
    >
      <div
        style={{
          padding: "8px 14px",
          borderBottom: `1px solid ${pal.border}`,
          fontSize: 17,
          color: pal.dim,
          display: "flex",
          justifyContent: "space-between",
        }}
      >
        <span>实测输出 · {shot.source.split("/").pop()}</span>
        <span>{shot.section ?? shot.label ?? ""}</span>
      </div>

      <div style={{ padding: "12px 14px", fontSize, lineHeight: 1.4 }}>
        {visible.map((line, i) => {
          const hit = highlight.some((h) => line.includes(h));
          return (
            <div
              key={i}
              style={{
                whiteSpace: "pre-wrap",
                color: hit ? COLORS.green : pal.fg,
                fontWeight: hit ? 600 : 400,
              }}
            >
              {line || " "}
            </div>
          );
        })}
      </div>
    </div>
  );
};
