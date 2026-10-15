import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { textFor } from "../data";
import { COLORS, FONT_MONO, SIZES, paletteFor } from "../theme";
import type { Region, Scene, Shot } from "../types";

export type CodeFileShotProps = { shot: Extract<Shot, { type: "code-file" }>; scene: Scene };

/**
 * 代码镜头。
 *
 * 关键约束（见 storyboards.json 的 renderPolicy）：屏幕上的每一行代码都来自
 * `resolved[source].slice` —— 构建时从仓库真实文件读出的快照。
 * 这个组件不做任何补全、改写或语法高亮重排，只做逐行渲染与命中高亮。
 */
export const CodeFileShot: React.FC<CodeFileShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const code = textFor(shot.source, true);
  const lines = code.split("\n");

  // 自动缩小字号以适配框宽：等宽字体下一行代码的长度是可预测的，
  // 因此可以保证再长的行也不会被裁掉——画面裁字会让"真实代码"这件事失真。
  const boxW = shot.region?.w ?? 900;
  const gutter = 62; // 行号列 + 内边距
  const longest = Math.max(...lines.map((l) => l.length), 1);
  const fitted = Math.floor((boxW - gutter) / (longest * 0.602));
  const fontSize = Math.max(13, Math.min(SIZES.code, fitted));

  // 打字机：逐行揭示真实文件内容（不是在"打字生成"代码）。
  const revealLines =
    shot.effect === "typewriter"
      ? Math.max(1, Math.floor(frame / Math.max(1, Math.round((shot.lineDelaySec ?? 0.4) * fps))))
      : lines.length;
  const visible = lines.slice(0, Math.min(lines.length, revealLines));

  const highlight = new Set(shot.highlight ?? []);

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 80,
        top: shot.region?.y ?? 140,
        width: boxW,
        height: shot.region?.h ?? 400,
        backgroundColor: pal.panel,
        border: `1px solid ${pal.border}`,
        borderRadius: 10,
        overflow: "hidden",
        fontFamily: FONT_MONO,
      }}
    >
      <div
        style={{
          padding: "10px 16px",
          borderBottom: `1px solid ${pal.border}`,
          fontSize: 18,
          color: pal.dim,
          display: "flex",
          justifyContent: "space-between",
          fontFamily: FONT_MONO,
        }}
      >
        <span>{shot.source}</span>
        <span>{shot.lines ? `L${shot.lines}` : `${lines.length} 行`}</span>
      </div>

      <div style={{ padding: "14px 16px", fontSize, lineHeight: 1.45 }}>
        {visible.map((line, i) => {
          // 命中判定按整行去空白后比较，避免子串命中把无关行染色。
          const trimmed = line.trim();
          const hit = [...highlight].some((h) => trimmed.includes(h.trim()));
          return (
            <div
              key={i}
              style={{
                whiteSpace: "pre",
                color: hit ? COLORS.accentWarm : pal.fg,
                backgroundColor: hit ? "rgba(240,136,62,0.12)" : "transparent",
                borderLeft: hit ? `3px solid ${COLORS.accentWarm}` : "3px solid transparent",
                paddingLeft: 8,
                marginLeft: -8,
              }}
            >
              <span style={{ color: pal.dim, marginRight: 16, userSelect: "none" }}>
                {String(i + 1).padStart(3, " ")}
              </span>
              {line || " "}
            </div>
          );
        })}
      </div>
    </div>
  );
};

/** 复用的坐标盒子，供其它渲染器使用。 */
export const regionStyle = (region: Region | undefined, fallback: Region): React.CSSProperties => ({
  position: "absolute",
  left: (region ?? fallback).x,
  top: (region ?? fallback).y,
  width: (region ?? fallback).w,
  height: (region ?? fallback).h,
});
