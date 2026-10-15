import React from "react";
import { interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { COLORS, FONT_MONO, FONT_SANS, SIZES, paletteFor } from "../theme";
import type { Scene, Shot } from "../types";

export type CoverageGridShotProps = { shot: Extract<Shot, { type: "coverage-grid" }>; scene: Scene };

/** 覆盖率仪表盘。百分比数值来自 project-brief.md 的实测记录。 */
export const CoverageGridShot: React.FC<CoverageGridShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const stagger = Math.round(fps * 0.12);

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 1000,
        top: shot.region?.y ?? 200,
        width: shot.region?.w ?? 840,
        height: shot.region?.h ?? 380,
        fontFamily: FONT_SANS,
        display: "flex",
        flexDirection: "column",
        justifyContent: "center",
      }}
    >
      <div style={{ fontSize: SIZES.label + 10, color: pal.dim, marginBottom: 30, letterSpacing: 1 }}>
        测试覆盖率（语句）
      </div>

      {shot.items.map((item, i) => {
        const local = frame - i * stagger;
        const p = interpolate(local, [0, Math.round(fps * 0.9)], [0, item.pct], { extrapolateRight: "clamp" });
        const full = item.pct >= 100;
        return (
          <div key={item.pkg} style={{ display: "flex", alignItems: "center", gap: 18, marginBottom: 22 }}>
            <span style={{ fontFamily: FONT_MONO, fontSize: 28, color: pal.fg, width: 330 }}>{item.pkg}</span>
            <div
              style={{
                flex: 1,
                height: 18,
                backgroundColor: pal.panel,
                border: `1px solid ${pal.border}`,
                borderRadius: 3,
              }}
            >
              <div
                style={{
                  height: "100%",
                  width: `${p}%`,
                  backgroundColor: full ? COLORS.green : COLORS.accent,
                  borderRadius: 3,
                }}
              />
            </div>
            <span
              style={{
                fontFamily: FONT_MONO,
                fontSize: 28,
                width: 104,
                textAlign: "right",
                color: full ? COLORS.green : pal.fg,
              }}
            >
              {p.toFixed(1)}%
            </span>
          </div>
        );
      })}
    </div>
  );
};
