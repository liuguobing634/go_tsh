import React from "react";
import { interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { COLORS, FONT_MONO, FONT_SANS, SIZES, paletteFor } from "../theme";
import type { Attribution, Scene, Shot } from "../types";

/**
 * 归属角标。
 *
 * 片头含**创作者第一人称自述**，而仓库内没有可核验的对话记录。
 * 因此画面必须常驻一个标注，把「这是谁的陈述」与「这是可复核的事实」分开——
 * 否则整部片子坚持的证据纪律会在第一分钟就自我否定。
 */
export const AttributionBadge: React.FC<{ kind: Attribution; fg: string; border: string; bg: string }> = ({
  kind,
  fg,
  border,
  bg,
}) => {
  const isCreator = kind === "creator-statement";
  return (
    <div
      style={{
        position: "absolute",
        right: 48,
        // 放右上而不是右下：字幕贴在底部（paddingBottom 96），
        // 右下角会被字幕压住——实测 S-1 里角标与字幕直接叠在一起。
        top: 44,
        padding: "8px 16px",
        borderRadius: 6,
        border: `1px solid ${isCreator ? border : COLORS.green}`,
        backgroundColor: bg,
        fontFamily: FONT_SANS,
        fontSize: SIZES.label + 2,
        letterSpacing: 1,
        color: isCreator ? fg : COLORS.green,
      }}
    >
      {isCreator ? "〔创作者自述〕" : "〔以下为可复核的事实〕"}
    </div>
  );
};

export type StatRevealShotProps = { shot: Extract<Shot, { type: "stat-reveal" }>; scene: Scene };

/**
 * 片头 S-1：三行数字打字机 + 标语 + 常驻角标。
 *
 * 数字来自 git（`git rev-list --count HEAD`、首日 13 个提交的 insertions 之和），
 * 因此刻意写进 storyboards.json 的 params，而不是渲染期去读 git ——
 * 渲染期不访问文件系统是这条流水线的硬约定。
 */
export const StatRevealShot: React.FC<StatRevealShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  // 每个数字 0.9s 落地，落地时有一次轻微放大
  const per = Math.round(fps * 0.9);
  const taglineAt = shot.stats.length * per + Math.round(fps * 0.4);
  const taglineO = interpolate(frame, [taglineAt, taglineAt + Math.round(fps * 0.6)], [0, 1], {
    extrapolateRight: "clamp",
    extrapolateLeft: "clamp",
  });

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 260,
        top: shot.region?.y ?? 150,
        width: shot.region?.w ?? 1400,
        height: shot.region?.h ?? 760,
        fontFamily: FONT_SANS,
      }}
    >
      {shot.stats.map((s, i) => {
        const at = i * per;
        const appear = interpolate(frame - at, [0, Math.round(fps * 0.35)], [0, 1], {
          extrapolateRight: "clamp",
          extrapolateLeft: "clamp",
        });
        // 落地瞬间的放大：从 1.14 收到 1.0
        const pop = interpolate(frame - at, [0, Math.round(fps * 0.45)], [1.14, 1], {
          extrapolateRight: "clamp",
          extrapolateLeft: "clamp",
        });
        if (frame < at) return null;
        return (
          <div key={s.unit} style={{ opacity: appear, marginBottom: 34 }}>
            <div style={{ display: "flex", alignItems: "baseline", gap: 20 }}>
              <span
                style={{
                  fontFamily: FONT_MONO,
                  fontSize: 130,
                  fontWeight: 700,
                  lineHeight: 1,
                  color: i === 2 ? COLORS.accentWarm : pal.fg,
                  transform: `scale(${pop})`,
                  transformOrigin: "left bottom",
                  display: "inline-block",
                }}
              >
                {s.value}
              </span>
              <span style={{ fontSize: 46, color: pal.fg }}>{s.unit}</span>
            </div>
            <div style={{ fontSize: 24, color: pal.dim, marginTop: 6, letterSpacing: 1 }}>{s.caption}</div>
          </div>
        );
      })}

      <div style={{ opacity: taglineO, marginTop: 26 }}>
        <div style={{ fontSize: 54, fontWeight: 700, color: pal.fg, lineHeight: 1.35 }}>{shot.tagline}</div>
        {shot.taglineSub ? (
          <div style={{ fontFamily: FONT_MONO, fontSize: 34, color: COLORS.accent, marginTop: 12 }}>
            {shot.taglineSub}
          </div>
        ) : null}
      </div>

      <AttributionBadge kind={shot.attribution} fg={pal.fg} border={pal.border} bg={pal.panel} />
    </div>
  );
};

export type StatementShotProps = { shot: Extract<Shot, { type: "statement" }>; scene: Scene };

/** 片头 S-1：单句结论，重心是那句话与角标的切换。 */
export const StatementShot: React.FC<StatementShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const o = interpolate(frame, [0, Math.round(fps * 0.7)], [0, 1], { extrapolateRight: "clamp" });
  const shift = interpolate(frame, [0, Math.round(fps * 0.7)], [18, 0], { extrapolateRight: "clamp" });

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 260,
        top: shot.region?.y ?? 300,
        width: shot.region?.w ?? 1400,
        height: shot.region?.h ?? 420,
        display: "flex",
        alignItems: "center",
        fontFamily: FONT_SANS,
      }}
    >
      <div style={{ opacity: o, transform: `translateY(${shift}px)`, fontSize: 92, fontWeight: 700, color: pal.fg, lineHeight: 1.3 }}>
        {shot.text}
      </div>
      <AttributionBadge kind={shot.attribution} fg={pal.fg} border={pal.border} bg={pal.panel} />
    </div>
  );
};

export type PillarCardsShotProps = { shot: Extract<Shot, { type: "pillar-cards" }>; scene: Scene };

/**
 * 片头 S0：四个要点卡片 → 收拢为全片导航轴。
 *
 * 每张卡片底部标注**真实实现文件**：这一场景是概念引入，
 * 若不给出实体路径，观众无从判断这四件事是否真的都做了。
 */
export const PillarCardsShot: React.FC<PillarCardsShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const n = shot.pillars.length;
  const stagger = Math.round(fps * 0.42);
  const cardsIn = n * stagger + Math.round(fps * 0.5);
  // 收拢动画：卡片缩成上方小圆点，下方拉出导航轴
  const collapse = shot.collapseToAxis
    ? interpolate(frame, [cardsIn + Math.round(fps * 1.6), cardsIn + Math.round(fps * 2.6)], [0, 1], {
        extrapolateRight: "clamp",
        extrapolateLeft: "clamp",
      })
    : 0;

  const axisO = interpolate(collapse, [0.15, 0.8], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });

  const LANES = ["S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8"];

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 80,
        top: shot.region?.y ?? 140,
        width: shot.region?.w ?? 1760,
        height: shot.region?.h ?? 700,
        fontFamily: FONT_SANS,
      }}
    >
      <div style={{ display: "flex", gap: 24, justifyContent: "center" }}>
        {shot.pillars.map((p, i) => {
          const at = i * stagger;
          const appear = interpolate(frame - at, [0, Math.round(fps * 0.5)], [0, 1], {
            extrapolateRight: "clamp",
            extrapolateLeft: "clamp",
          });
          const slide = interpolate(frame - at, [0, Math.round(fps * 0.5)], [-28, 0], {
            extrapolateRight: "clamp",
            extrapolateLeft: "clamp",
          });
          // 收拢时卡片整体缩小并上移，让位给导航轴
          const scale = interpolate(collapse, [0, 1], [1, 0.52], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
          });
          const rise = interpolate(collapse, [0, 1], [0, -70], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
          });
          if (frame < at) return null;
          return (
            <div
              key={p.name}
              style={{
                opacity: appear,
                transform: `translateX(${slide}px) translateY(${rise}px) scale(${scale})`,
                width: 380,
                padding: "22px 22px 18px",
                borderRadius: 12,
                backgroundColor: pal.panel,
                border: `1px solid ${pal.border}`,
                borderTop: `4px solid ${COLORS.accent}`,
              }}
            >
              <div style={{ fontSize: 40, fontWeight: 700, color: pal.fg, marginBottom: 14 }}>{p.name}</div>
              <div style={{ fontFamily: FONT_MONO, fontSize: 17, color: pal.dim, marginBottom: 12, wordBreak: "break-all" }}>
                {p.files}
              </div>
              <div
                style={{
                  display: "inline-block",
                  fontFamily: FONT_MONO,
                  fontSize: 20,
                  color: COLORS.accent,
                  border: `1px solid ${COLORS.accent}`,
                  borderRadius: 4,
                  padding: "2px 10px",
                }}
              >
                {p.scene}
              </div>
            </div>
          );
        })}
      </div>

      {/* 全片导航轴 */}
      {shot.collapseToAxis ? (
        <div style={{ opacity: axisO, marginTop: 150, position: "relative" }}>
          <div style={{ height: 2, backgroundColor: pal.border, margin: "0 40px" }} />
          <div style={{ display: "flex", justifyContent: "space-between", margin: "0 40px" }}>
            {LANES.map((sid) => {
              const highlight = shot.pillars.some((p) => p.scene === sid);
              return (
                <div key={sid} style={{ display: "flex", flexDirection: "column", alignItems: "center", marginTop: -7 }}>
                  <div
                    style={{
                      width: highlight ? 16 : 10,
                      height: highlight ? 16 : 10,
                      borderRadius: "50%",
                      backgroundColor: highlight ? COLORS.accent : pal.border,
                    }}
                  />
                  <div
                    style={{
                      fontFamily: FONT_MONO,
                      fontSize: 20,
                      marginTop: 12,
                      color: highlight ? COLORS.accent : pal.dim,
                    }}
                  >
                    {sid}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      ) : null}
    </div>
  );
};
