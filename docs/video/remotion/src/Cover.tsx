import React from "react";
import { AbsoluteFill, useVideoConfig } from "remotion";
import { COLORS, FONT_MONO, FONT_SANS } from "./theme";

/**
 * 视频封面（1920×1080 单帧）。
 *
 * 设计原则与正片一致：
 *  1. 画面上的每个数字都必须可从仓库复核——封面最容易变成「随便放几个数」，
 *     这里全部取自已验证值，并在图表里写明出处口径。
 *  2. 中英混排要分开处理：中文用黑体栈，`Go` / `DSH` 用等宽字体，
 *     让标题里的工具名一眼可辨，而不是混成一片。
 *  3. 深色底 + 高对比，保证在视频列表的小尺寸缩略图里也读得清。
 *
 * 数字来源：
 *   52 次提交 / 16 天 / 1 位作者  → git rev-list --count HEAD 等
 *   10,515 行源码 / 12,530 行测试 → 排除 .gocache 后统计
 *   48 个本体提交                 → 未触及 docs/video 与 scripts/video
 */

const STATS = [
  { value: "52", unit: "次提交", note: "1 位作者" },
  { value: "16", unit: "天", note: "6 个活跃日" },
  { value: "10,515", unit: "行 Go 源码", note: "另 12,530 行测试" },
];

const PILLARS = ["倒排索引", "BM25 排序", "布尔求值", "短语匹配"];

export const Cover: React.FC = () => {
  const { width, height } = useVideoConfig();
  // 封面可能被渲染成其他尺寸，按 1080p 基准等比缩放，避免换尺寸就错版
  const s = Math.min(width / 1920, height / 1080);

  return (
    <AbsoluteFill style={{ backgroundColor: COLORS.bgDark, fontFamily: FONT_SANS }}>
      {/* 背景：极淡的网格，暗示「索引结构」，不抢主体 */}
      <AbsoluteFill
        style={{
          backgroundImage:
            `linear-gradient(${COLORS.panelBorder}22 1px, transparent 1px),` +
            `linear-gradient(90deg, ${COLORS.panelBorder}22 1px, transparent 1px)`,
          backgroundSize: `${64 * s}px ${64 * s}px`,
        }}
      />
      {/* 右上角暖色光晕，让深色底不平 */}
      <AbsoluteFill
        style={{
          background: `radial-gradient(circle at 88% 6%, ${COLORS.accentWarm}22, transparent 46%)`,
        }}
      />

      {/* 主体用 flex 纵向分布填满画布：
          第一版把统计条写死在 marginTop:58，下方标签写死在 bottom:92，
          结果两者之间空出约 300px，右下角一大片死区。
          改用 space-between 让内容自己撑开，换尺寸也不会塌。 */}
      <div
        style={{
          position: "absolute",
          inset: 0,
          padding: `${92 * s}px ${96 * s}px ${86 * s}px`,
          display: "flex",
          flexDirection: "column",
          justifyContent: "space-between",
        }}
      >
        {/* 上半：徽标 + 标题 + 副标题 */}
        <div>
          <div style={{ display: "flex", alignItems: "center", gap: 16 * s, marginBottom: 34 * s }}>
            <div
              style={{
                fontFamily: FONT_MONO,
                fontSize: 30 * s,
                fontWeight: 700,
                letterSpacing: 1,
                color: COLORS.bgDark,
                backgroundColor: COLORS.accent,
                padding: `${6 * s}px ${16 * s}px`,
                borderRadius: 6 * s,
              }}
            >
              DSH
            </div>
            <div style={{ fontSize: 26 * s, color: COLORS.textDim, letterSpacing: 3 }}>
              DeepSeek Harness · 实战记录
            </div>
          </div>

          <div style={{ fontSize: 108 * s, fontWeight: 700, color: COLORS.text, lineHeight: 1.3 }}>
            用 <span style={{ fontFamily: FONT_MONO, color: COLORS.accent }}>DSH</span> 从 0 写一个
          </div>
          <div
            style={{
              fontSize: 108 * s,
              fontWeight: 700,
              color: COLORS.text,
              lineHeight: 1.3,
              // 108px 的中文字形实际高度接近 1.3em，原 marginTop 6 会让两行
              // 标题的上下缘视觉上叠在一起（渲染出来能看到笔画贴住）。
              marginTop: 22 * s,
            }}
          >
            <span style={{ fontFamily: FONT_MONO, color: COLORS.accent }}>Go</span> 全文搜索引擎
          </div>

          <div style={{ fontSize: 34 * s, color: COLORS.textDim, marginTop: 26 * s, lineHeight: 1.5 }}>
            手写倒排索引与 BM25，只借一个中文分词库
          </div>
        </div>

        {/* 统计条 */}
        <div style={{ display: "flex", gap: 72 * s }}>
          {STATS.map((st) => (
            <div key={st.unit}>
              <div style={{ display: "flex", alignItems: "baseline", gap: 12 * s }}>
                <span
                  style={{
                    fontFamily: FONT_MONO,
                    fontSize: 84 * s,
                    fontWeight: 700,
                    color: COLORS.accentWarm,
                    lineHeight: 1,
                  }}
                >
                  {st.value}
                </span>
                <span style={{ fontSize: 32 * s, color: COLORS.text }}>{st.unit}</span>
              </div>
              <div style={{ fontSize: 23 * s, color: COLORS.textDim, marginTop: 10 * s }}>{st.note}</div>
            </div>
          ))}
        </div>

        {/* 底部：要点标签 + 项目标识 */}
        <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between" }}>
          <div style={{ display: "flex", gap: 18 * s }}>
            {PILLARS.map((p) => (
              <div
                key={p}
                style={{
                  fontFamily: FONT_SANS,
                  fontSize: 26 * s,
                  color: COLORS.text,
                  backgroundColor: COLORS.panel,
                  border: `${1 * s}px solid ${COLORS.panelBorder}`,
                  borderRadius: 8 * s,
                  padding: `${12 * s}px ${22 * s}px`,
                }}
              >
                {p}
              </div>
            ))}
          </div>

          <div style={{ textAlign: "right", fontFamily: FONT_MONO }}>
            <div style={{ fontSize: 42 * s, color: COLORS.text, fontWeight: 700 }}>go_tsh</div>
            <div style={{ fontSize: 22 * s, color: COLORS.textDim, marginTop: 6 * s }}>
              github.com/liuguobing/go_tsh
            </div>
          </div>
        </div>
      </div>

      {/* 底部强调线 */}
      <div
        style={{
          position: "absolute",
          left: 0,
          bottom: 0,
          height: 8 * s,
          width: "100%",
          background: `linear-gradient(90deg, ${COLORS.accent}, ${COLORS.accentWarm})`,
        }}
      />
    </AbsoluteFill>
  );
};
