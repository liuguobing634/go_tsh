import React from "react";
import { interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { COLORS, FONT_MONO, FONT_SANS, SIZES, paletteFor } from "../theme";
import type {
  AxisToken,
  IdfCurveParams,
  MergeIntersectParams,
  PositionAxisParams,
  Scene,
  Shot,
} from "../types";

export type ManimShotProps = { shot: Extract<Shot, { type: "manim" }>; scene: Scene };

/**
 * 动画镜头。
 *
 * 三个动画（IDF 曲线、位置轴对齐、双指针归并）在 storyboards.json 里被标为
 * type=manim，原计划由 Manim 预渲染成片段后嵌入。为了让工程在装好依赖的当天
 * 就能端到端出片，这里先用**代码内联实现**，参数全部来自 storyboards.json：
 *
 *   idf-curve           —— 8 个真实 (df, idf) 采样点，来自 demo-cases.md 第 3.3 节
 *   position-axis-align —— 查询侧/文档侧的真实 token 位置，来自第 4.4 节
 *   merge-intersect     —— 两条真实 posting 列表的 DocID，来自第 3.2 节
 *
 * 若要换成 Manim 渲染，只需在此组件里改为 <OffthreadVideo src={staticFile(...)} />；
 * 参数无需改动。**不得**为了画面好看而改动这些数值。
 */
export const ManimShot: React.FC<ManimShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const body = (() => {
    switch (shot.animation) {
      case "idf-curve":
        return <IdfCurve params={shot.params as IdfCurveParams} frame={frame} fps={fps} pal={pal} />;
      case "position-axis-align":
        return <PositionAxis params={shot.params as PositionAxisParams} frame={frame} fps={fps} pal={pal} />;
      case "merge-intersect":
        return <MergeIntersect params={shot.params as MergeIntersectParams} frame={frame} fps={fps} pal={pal} />;
      default: {
        const never: never = shot.animation;
        throw new Error(`未实现的动画：${String(never)}`);
      }
    }
  })();

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 900,
        top: shot.region?.y ?? 140,
        width: shot.region?.w ?? 940,
        height: shot.region?.h ?? 420,
        backgroundColor: pal.panel,
        border: `1px solid ${pal.border}`,
        borderRadius: 10,
        padding: "16px 20px",
        fontFamily: FONT_SANS,
      }}
    >
      <div style={{ fontSize: SIZES.label + 2, color: pal.dim, marginBottom: 10, letterSpacing: 1 }}>
        {ANIM_TITLES[shot.animation]}
      </div>
      {body}
    </div>
  );
};

const ANIM_TITLES: Record<string, string> = {
  "idf-curve": "IDF 随文档频率下降（N = 1000，实测采样）",
  "position-axis-align": "位置轴对齐：位置差必须与查询侧一致",
  "merge-intersect": "双指针归并求交（DocID 升序）",
};

/* ------------------------------------------------------------------ IDF 曲线 */

const IdfCurve: React.FC<{
  params: IdfCurveParams;
  frame: number;
  fps: number;
  pal: ReturnType<typeof paletteFor>;
}> = ({ params, frame, fps, pal }) => {
  // SVG 尺寸贴合容器（框高 430 / 480 减去标题），曲线因此不再缩在左上角。
  const W = 780;
  const H = 320;
  const maxIdf = Math.max(...params.points.map((p) => p.idf));
  const maxDf = Math.max(...params.points.map((p) => p.df));
  const x = (df: number) => (Math.log10(df + 1) / Math.log10(maxDf + 1)) * W;
  const y = (idf: number) => H - (idf / maxIdf) * H;
  const progress = interpolate(frame, [0, Math.round(fps * 2.2)], [0, 1], { extrapolateRight: "clamp" });
  const shown = Math.max(1, Math.ceil(progress * params.points.length));
  const pts = params.points.slice(0, shown);

  return (
    <div>
      <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} style={{ overflow: "visible" }}>
        <line x1={0} y1={H} x2={W} y2={H} stroke={pal.border} strokeWidth={1} />
        <line x1={0} y1={0} x2={0} y2={H} stroke={pal.border} strokeWidth={1} />
        {params.markHalf ? (
          <line x1={x(params.markHalf)} y1={0} x2={x(params.markHalf)} y2={H} stroke={COLORS.yellow} strokeDasharray="4 4" />
        ) : null}
        <polyline
          points={pts.map((p) => `${x(p.df)},${y(p.idf)}`).join(" ")}
          fill="none"
          stroke={COLORS.accent}
          strokeWidth={3}
        />
        {pts.map((p) => (
          <g key={p.df}>
            <circle cx={x(p.df)} cy={y(p.idf)} r={5} fill={COLORS.accent} />
            <text x={x(p.df)} y={y(p.idf) - 13} fill={pal.fg} fontSize={16} fontFamily={FONT_MONO} textAnchor="middle">
              {p.idf.toFixed(3)}
            </text>
            <text x={x(p.df)} y={H + 20} fill={pal.dim} fontSize={15} fontFamily={FONT_MONO} textAnchor="middle">
              {p.df}
            </text>
          </g>
        ))}
      </svg>
      <div style={{ fontFamily: FONT_MONO, fontSize: 18, color: pal.dim, marginTop: 30 }}>
        横轴 df · 纵轴 IDF · 全部为正（平滑写法）
      </div>
    </div>
  );
};

/* -------------------------------------------------------------- 位置轴对齐 */

const PositionAxis: React.FC<{
  params: PositionAxisParams;
  frame: number;
  fps: number;
  pal: ReturnType<typeof paletteFor>;
}> = ({
  params,
  frame,
  fps,
  pal,
}) => {
  const CELL = 86;
  const rows: { label: string; tokens: AxisToken[]; verdict?: "hit" | "miss" }[] = [
    { label: "查询", tokens: params.query },
    { label: "文档 gap", tokens: params.gap, verdict: "hit" },
    { label: "文档 adj", tokens: params.adj, verdict: "miss" },
  ];
  const step = Math.round(fps * 1.1);

  return (
    <div>
      {rows.map((row, r) => {
        const o = interpolate(frame - r * step, [0, Math.round(fps * 0.5)], [0, 1], {
          extrapolateRight: "clamp",
          extrapolateLeft: "clamp",
        });
        return (
          <div key={row.label} style={{ opacity: o, marginBottom: 16 }}>
            <div style={{ fontFamily: FONT_SANS, fontSize: 18, color: pal.dim, marginBottom: 6 }}>{row.label}</div>
            <div style={{ display: "flex", gap: 8 }}>
              {row.tokens.map((t, i) => (
                <div
                  key={i}
                  style={{
                    width: CELL,
                    padding: "8px 6px",
                    textAlign: "center",
                    borderRadius: 6,
                    fontFamily: FONT_MONO,
                    fontSize: 18,
                    border: t.filtered ? `1px dashed ${pal.dim}` : `1px solid ${pal.border}`,
                    color: t.filtered ? pal.dim : pal.fg,
                    textDecoration: t.filtered ? "line-through" : "none",
                    backgroundColor: t.filtered ? "transparent" : pal.bg ?? "rgba(255,255,255,0.03)",
                  }}
                >
                  <div>{t.term}</div>
                  <div style={{ fontSize: 14, color: t.filtered ? pal.dim : COLORS.accent }}>@{t.pos}</div>
                </div>
              ))}
              {row.verdict ? (
                <div
                  style={{
                    alignSelf: "center",
                    marginLeft: 10,
                    fontFamily: FONT_MONO,
                    fontSize: 22,
                    color: row.verdict === "hit" ? COLORS.green : COLORS.red,
                  }}
                >
                  {row.verdict === "hit" ? "✓ 位置差一致" : "✗ 位置差 1 ≠ 2"}
                </div>
              ) : null}
            </div>
          </div>
        );
      })}
    </div>
  );
};

/* ------------------------------------------------------------ 双指针归并求交 */

const MergeIntersect: React.FC<{
  params: MergeIntersectParams;
  frame: number;
  fps: number;
  pal: ReturnType<typeof paletteFor>;
}> = ({ params, frame, fps, pal }) => {
  const CELL = 78;
  const left = params.left.docs;
  const right = params.right.docs;

  // 用真实 DocID 推导游标位置：i/j 同步推进，与 mergeIntersect 的双指针一致。
  const total = left.length + right.length;
  const t = interpolate(frame, [0, Math.round(fps * 2.6)], [0, total], { extrapolateRight: "clamp" });
  let i = 0;
  let j = 0;
  let steps = 0;
  const matched: number[] = [];
  while (steps < t && i < left.length && j < right.length) {
    if (left[i] < right[j]) i++;
    else if (left[i] > right[j]) j++;
    else {
      matched.push(left[i]);
      i++;
      j++;
    }
    steps++;
  }

  const laneStyle: React.CSSProperties = { display: "flex", gap: 8, alignItems: "center", marginBottom: 12 };

  return (
    <div>
      <div style={laneStyle}>
        <span style={{ fontFamily: FONT_MONO, fontSize: 18, color: pal.dim, width: 96 }}>{params.left.field}</span>
        {left.map((d, k) => (
          <div
            key={d}
            style={{
              width: CELL,
              textAlign: "center",
              padding: "8px 0",
              borderRadius: 6,
              fontFamily: FONT_MONO,
              fontSize: 20,
              border: `1px solid ${k === i ? COLORS.accent : pal.border}`,
              backgroundColor: k < i ? "rgba(88,166,255,0.10)" : "transparent",
              color: matched.includes(d) ? COLORS.green : pal.fg,
            }}
          >
            {d}
          </div>
        ))}
      </div>

      <div style={laneStyle}>
        <span style={{ fontFamily: FONT_MONO, fontSize: 18, color: pal.dim, width: 96 }}>{params.right.field}</span>
        {right.map((d, k) => (
          <div
            key={d}
            style={{
              width: CELL,
              textAlign: "center",
              padding: "8px 0",
              borderRadius: 6,
              fontFamily: FONT_MONO,
              fontSize: 20,
              border: `1px solid ${k === j ? COLORS.accent : pal.border}`,
              backgroundColor: k < j ? "rgba(88,166,255,0.10)" : "transparent",
              color: matched.includes(d) ? COLORS.green : pal.fg,
            }}
          >
            {d}
          </div>
        ))}
      </div>

      <div style={{ display: "flex", alignItems: "center", gap: 12, marginTop: 18 }}>
        <span style={{ fontFamily: FONT_SANS, fontSize: 20, color: pal.dim }}>求交结果</span>
        {matched.length === 0 ? (
          <span style={{ fontFamily: FONT_MONO, fontSize: 22, color: pal.dim }}>—</span>
        ) : (
          matched.map((d) => (
            <span key={d} style={{ fontFamily: FONT_MONO, fontSize: 24, color: COLORS.green }}>
              {d}
            </span>
          ))
        )}
      </div>
      <div style={{ fontFamily: FONT_SANS, fontSize: 18, color: pal.dim, marginTop: 10 }}>
        两个游标各自只前进一次，每个 posting 最多访问一遍
      </div>
    </div>
  );
};
