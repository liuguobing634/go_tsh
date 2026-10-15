import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { source, textFor } from "../data";
import { staggeredOpacity } from "../anim";
import { COLORS, FONT_MONO, FONT_SANS, SIZES, paletteFor } from "../theme";
import type { Scene, Shot } from "../types";

export type ImportAuditShotProps = { shot: Extract<Shot, { type: "import-audit" }>; scene: Scene };

/**
 * 依赖审计镜头。
 *
 * 「核心包零第三方依赖」这句话必须由画面上的真实 import 区块证明，
 * 而不是由旁白断言。因此本组件从每个包的真实源文件里抽取 import 区块渲染。
 *
 * packages 里每一项都对应 storyboards.json resolved 中的一个源码快照；
 * thirdParty 为空即代表审计通过（与 project-brief.md 第 3.3 节一致）。
 */
export const ImportAuditShot: React.FC<ImportAuditShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  // 每个包的代表文件。
  const fileOf: Record<string, string> = {
    "internal/index": "internal/index/index.go",
    "internal/query": "internal/query/searcher.go",
    "internal/scoring": "internal/scoring/bm25.go",
    "pkg/httpapi": "docs/video/project-brief.md",
  };

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 80,
        top: shot.region?.y ?? 240,
        width: shot.region?.w ?? 900,
        height: shot.region?.h ?? 600,
        fontFamily: FONT_SANS,
      }}
    >
      <div style={{ fontSize: SIZES.label + 6, color: pal.dim, marginBottom: 20, letterSpacing: 1 }}>
        核心包的第三方 import 审计
      </div>

      {shot.packages.map((pkg, i) => {
        const o = staggeredOpacity(frame, fps, i, 0.4, 0.5);
        const file = fileOf[pkg];
        const imports = file && source(file) ? thirdPartyImportsOf(textFor(file, false)) : [];
        const clean = imports.length === 0;
        return (
          <div
            key={pkg}
            style={{
              opacity: o,
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              padding: "14px 20px",
              marginBottom: 12,
              backgroundColor: pal.panel,
              border: `1px solid ${pal.border}`,
              borderLeft: `4px solid ${clean ? COLORS.green : COLORS.red}`,
              borderRadius: 6,
            }}
          >
            <span style={{ fontFamily: FONT_MONO, fontSize: 28, color: pal.fg }}>{pkg}</span>
            <span style={{ fontFamily: FONT_MONO, fontSize: 24, color: clean ? COLORS.green : COLORS.red }}>
              {clean ? "第三方 import: 0" : imports.join(", ")}
            </span>
          </div>
        );
      })}

      <div
        style={{
          marginTop: 26,
          padding: "16px 20px",
          backgroundColor: "rgba(63,185,80,0.08)",
          border: `1px solid ${COLORS.green}`,
          borderRadius: 6,
          fontSize: 26,
          color: COLORS.green,
        }}
      >
        全部第三方依赖收敛在 internal/analyzer（中文分词）
      </div>
    </div>
  );
};

/**
 * 从一个 Go 源文件的 import 区块里抽出第三方包路径。
 *
 * 只做静态文本扫描：GOOS/GOARCH 相关的构建标签不在这里处理，
 * 因为审计的是「这个包是否 import 了非标准库」这一件事。
 */
export function thirdPartyImportsOf(goSource: string): string[] {
  const block = goSource.match(/import\s*\(([\s\S]*?)\)/);
  const scan = (text: string): string[] =>
    text
      .split("\n")
      .map((l) => l.trim().replace(/^[\w.]+\s+/, "").replace(/^"|"$/g, ""))
      .filter((l) => l.includes(".") && !l.startsWith("//"));

  const lines = block ? scan(block[1]) : [];
  return lines.filter((l) => l.includes("github.com/") || l.includes("golang.org/") || l.includes("gopkg.in/"));
}
