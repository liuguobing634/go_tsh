import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { COLORS, FONT_MONO, FONT_SANS, SIZES, paletteFor } from "../theme";
import { staggeredOpacity } from "../anim";
import type { Scene, Shot } from "../types";

export type InvariantTableShotProps = { shot: Extract<Shot, { type: "invariant-table" }>; scene: Scene };

/**
 * 不变量断言表。
 *
 * 这些断言由本次分析对真实索引实跑得到（见 demo-cases.md 第 3.2 节），
 * 不是设计文档里的承诺 —— 表头刻意写成「实测断言结果」。
 */
export const InvariantTableShot: React.FC<InvariantTableShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 60,
        top: shot.region?.y ?? 880,
        width: shot.region?.w ?? 1800,
        height: shot.region?.h ?? 190,
        fontFamily: FONT_SANS,
        display: "flex",
        flexDirection: "column",
        justifyContent: "center",
      }}
    >
      <div style={{ fontSize: SIZES.label + 6, color: pal.dim, marginBottom: 16, letterSpacing: 1 }}>
        实测断言结果（对真实索引执行）
      </div>
      {shot.rows.map((row, i) => {
        const o = staggeredOpacity(frame, fps, i, 0.25, 0.5);
        return (
          <div
            key={row.name}
            style={{
              opacity: o,
              display: "flex",
              alignItems: "center",
              gap: 20,
              padding: "12px 18px",
              marginBottom: 10,
              backgroundColor: pal.panel,
              border: `1px solid ${pal.border}`,
              borderLeft: `4px solid ${row.value ? COLORS.green : COLORS.red}`,
              borderRadius: 6,
            }}
          >
            <span style={{ fontFamily: FONT_MONO, fontSize: 26, color: pal.dim, width: 76 }}>
              {row.value ? "true" : "false"}
            </span>
            <span style={{ fontFamily: FONT_MONO, fontSize: 26, color: pal.fg }}>{row.name}</span>
          </div>
        );
      })}
    </div>
  );
};
