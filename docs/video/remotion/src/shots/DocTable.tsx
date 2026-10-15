import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { textFor } from "../data";
import { staggeredOpacity } from "../anim";
import { COLORS, FONT_SANS, paletteFor } from "../theme";
import type { Scene, Shot } from "../types";

export type DocTableShotProps = { shot: Extract<Shot, { type: "doc-table" }>; scene: Scene };

/**
 * 文档表格镜头（收尾用）。
 *
 * 内容取自 README.md 的真实行范围切片 —— 项目自己写的「不适合」清单。
 * 用文档原文收尾，比旁白复述更可信。
 */
export const DocTableShot: React.FC<DocTableShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const slice = textFor(shot.source, true);
  const rows = slice
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l.startsWith("|"))
    .map((l) =>
      l
        .split("|")
        .map((c) => c.trim())
        .filter((c, i, arr) => !(i === 0 && c === "") && !(i === arr.length - 1 && c === "")),
    )
    // 丢掉 markdown 的分隔行（---|---）
    .filter((cells) => !cells.every((c) => /^-{2,}$/.test(c) || c === ""));

  const [header, ...body] = rows;

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 200,
        top: shot.region?.y ?? 200,
        width: shot.region?.w ?? 1520,
        fontFamily: FONT_SANS,
      }}
    >
      {header ? (
        <div style={{ display: "flex", gap: 20, padding: "12px 16px", borderBottom: `2px solid ${pal.fg}` }}>
          {header.map((c, i) => (
            <div
              key={i}
              style={{
                flex: i === 0 ? 1 : 2,
                fontSize: 28,
                fontWeight: 700,
                color: pal.fg,
              }}
            >
              {c}
            </div>
          ))}
        </div>
      ) : null}

      {body.map((cells, r) => {
        const o = shot.fadeInRows ? staggeredOpacity(frame, fps, r, 0.22, 0.5) : 1;
        return (
          <div
            key={r}
            style={{
              opacity: o,
              display: "flex",
              gap: 20,
              padding: "16px",
              borderBottom: `1px solid ${pal.border}`,
            }}
          >
            {cells.map((c, i) => (
              <div
                key={i}
                style={{
                  flex: i === 0 ? 1 : 2,
                  fontSize: 26,
                  color: i === 0 ? COLORS.accent : pal.fg,
                  lineHeight: 1.45,
                }}
              >
                {c}
              </div>
            ))}
          </div>
        );
      })}
    </div>
  );
};
