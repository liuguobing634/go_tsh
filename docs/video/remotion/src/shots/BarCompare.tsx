import React from "react";
import { spring, useCurrentFrame, useVideoConfig } from "remotion";
import { COLORS, FONT_MONO, FONT_SANS, SIZES, paletteFor } from "../theme";
import { entranceOpacity } from "../anim";
import type { Scene, Shot } from "../types";

export type BarCompareShotProps = { shot: Extract<Shot, { type: "bar-compare" }>; scene: Scene };

/**
 * 数值对比条。数值来自 storyboards.json（其来源为本次实测记录），
 * 这里只负责按最大值等比绘制，不做任何归一化之外的计算。
 */
export const BarCompareShot: React.FC<BarCompareShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const max = Math.max(...shot.items.map((i) => i.value));
  const grow = spring({ frame, fps, config: { damping: 200 }, durationInFrames: Math.round(fps * 1.1) });
  const labelOpacity = entranceOpacity(frame, fps, 0.8);

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 80,
        top: shot.region?.y ?? 200,
        width: shot.region?.w ?? 860,
        height: shot.region?.h ?? 380,
        fontFamily: FONT_SANS,
        display: "flex",
        flexDirection: "column",
        justifyContent: "center",
      }}
    >
      <div style={{ fontSize: SIZES.label + 10, color: pal.dim, marginBottom: 34, letterSpacing: 1 }}>{shot.label}</div>

      {shot.items.map((item, i) => {
        const w = (item.value / max) * 100 * grow;
        const isAccent = i === shot.items.length - 1 && shot.items.length > 1;
        return (
          <div key={i} style={{ marginBottom: 40 }}>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 14 }}>
              <span style={{ fontSize: 34, color: pal.fg }}>{item.name}</span>
              <span style={{ fontFamily: FONT_MONO, fontSize: 38, color: isAccent ? COLORS.accent : pal.fg }}>
                {item.value.toLocaleString("en-US")} {item.unit}
              </span>
            </div>
            <div style={{ height: 34, backgroundColor: pal.panel, border: `1px solid ${pal.border}`, borderRadius: 4 }}>
              <div
                style={{
                  height: "100%",
                  width: `${w}%`,
                  backgroundColor: isAccent ? COLORS.accent : COLORS.accentWarm,
                  borderRadius: 4,
                }}
              />
            </div>
          </div>
        );
      })}

      {shot.ratioLabel ? (
        <div
          style={{
            opacity: labelOpacity,
            fontFamily: FONT_MONO,
            fontSize: 42,
            color: COLORS.green,
            marginTop: 12,
          }}
        >
          比例 {shot.ratioLabel}
        </div>
      ) : null}
    </div>
  );
};
