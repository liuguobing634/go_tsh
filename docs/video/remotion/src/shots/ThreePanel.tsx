import React from "react";
import { interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { textFor } from "../data";
import { extract } from "../markdown";
import { staggeredOpacity, entranceShift } from "../anim";
import { COLORS, FONT_MONO, FONT_SANS, SIZES, paletteFor } from "../theme";
import type { Scene, Shot } from "../types";

export type ThreePanelShotProps = { shot: Extract<Shot, { type: "three-panel" }>; scene: Scene };

/**
 * 三面板镜头（S2 主视觉）：原文 → 分析器 token 流 → 索引 posting。
 *
 * 每一栏的内容都按其 `section` 从快照中精确抽取：
 *   * 原文       —— demo-cases.md 3.1 的语料表
 *   * token 流   —— demo-cases.md 4.4 的分析器实测输出（want="analyzer"）
 *   * posting    —— demo-cases.md 3.2 的真实 posting 列表
 * 面板之间用箭头表达「数据如何流动」，这是整段动画的叙事骨架。
 */
export const ThreePanelShot: React.FC<ThreePanelShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const panelData = shot.panels.map((p) => ({
    ...p,
    text: extract({
      markdown: textFor(p.source, false),
      section: p.section,
      want: p.want,
      label: p.label,
      maxProseLines: 10,
    }),
  }));

  // 面板必须在镜头前 1/3 内全部出现：镜头时长远大于入场动画，
  // 若按每块 0.5s 递进，21 秒的镜头要到第 16 秒才凑齐三块，画面会长时间残缺。
  const stagger = Math.round(fps * 0.1);

  // 三栏应横向铺满 region。做法：先算最长行在基准字号下需要的宽度，
  // 再据可用宽度等比放大字号（上限 1.6 倍），避免"三栏挤在中间、两侧留空"。
  const natural = panelData.map((p) => {
    const longest = Math.max(...p.text.split("\n").map((l) => l.length), 1);
    return longest * SIZES.codeSmall * 0.602 + 34;
  });
  const n = shot.panels.length;
  const regionW = shot.region?.w ?? 1800;
  const availPer = (regionW - 20 * (n + 1) - 44 * (n - 1)) / n;
  const scale = Math.min(1.6, availPer / Math.max(...natural, 1));
  const panelFont = Math.max(13, Math.round(SIZES.codeSmall * scale));

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 60,
        top: shot.region?.y ?? 220,
        width: shot.region?.w ?? 1800,
        height: shot.region?.h ?? 620,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        gap: 20,
      }}
    >
      {panelData.map((p, i) => {
        const o = staggeredOpacity(frame, fps, i, 0.1, 0.45);
        const shift = entranceShift(frame - i * stagger, fps, 20, 0.45);
        return (
          <React.Fragment key={p.title}>
            <div
              style={{
                opacity: o,
                transform: `translateX(${shift}px)`,
                flex: "1 1 0",
                backgroundColor: pal.panel,
                border: `1px solid ${pal.border}`,
                borderRadius: 10,
                overflow: "hidden",
              }}
            >
              <div
                style={{
                  padding: "10px 16px",
                  borderBottom: `1px solid ${pal.border}`,
                  fontFamily: FONT_SANS,
                  fontSize: SIZES.label + 4,
                  color: COLORS.accent,
                  letterSpacing: 1,
                }}
              >
                {p.title}
              </div>
              <div
                style={{
                  padding: "16px",
                  fontFamily: FONT_MONO,
                  fontSize: panelFont,
                  lineHeight: 1.55,
                  color: pal.fg,
                  whiteSpace: "pre-wrap",
                }}
              >
                {p.text || "（未匹配到数据，请检查 storyboards.json 的 section）"}
              </div>
            </div>

            {i < panelData.length - 1 ? (
              <div
                style={{
                  opacity: interpolate(frame - (i + 0.5) * stagger, [0, Math.round(fps * 0.4)], [0, 1], {
                    extrapolateRight: "clamp",
                    extrapolateLeft: "clamp",
                  }),
                  alignSelf: "center",
                  fontFamily: FONT_MONO,
                  fontSize: 40,
                  color: pal.dim,
                }}
              >
                →
              </div>
            ) : null}
          </React.Fragment>
        );
      })}
    </div>
  );
};

/**
 * 按面板宽度选择等宽字号，让最长的行刚好放得下。
 *
 * 三面板是多行文本，横向溢出会很难看；同时面板宽度随布局变化，
 * 所以字号必须由内容推导，而不是写死。
 */
export function panelFontSize(text: string, boxWidth: number): number {
  const longest = Math.max(...text.split("\n").map((l) => l.length), 1);
  const fitted = Math.floor((boxWidth - 34) / (longest * 0.602));
  return Math.max(12, Math.min(SIZES.codeSmall, fitted));
}

