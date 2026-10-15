/**
 * 统一的视觉设计令牌。
 *
 * 纪录片风格：深色底、高对比、字距克制；代码与终端用等宽字体，
 * 中文用系统自带黑体栈（避免额外字体依赖让渲染在无网环境失败）。
 */

export const FONT_SANS =
  '"Microsoft YaHei", "Noto Sans SC", "PingFang SC", "Source Han Sans SC", "Segoe UI", sans-serif';

export const FONT_MONO =
  '"Cascadia Mono", "JetBrains Mono", Consolas, "Courier New", monospace';

export const COLORS = {
  bgDark: "#0d1117",
  bgLight: "#fbfbfa",
  panel: "#161b22",
  panelBorder: "#30363d",
  text: "#e6edf3",
  textDim: "#8b949e",
  textDark: "#1f2328",
  accent: "#58a6ff",
  accentWarm: "#f0883e",
  green: "#3fb950",
  red: "#f85149",
  yellow: "#d29922",
} as const;

export const SIZES = {
  /** 代码与终端字号；1080p 下这个尺寸在手机上也读得清。 */
  code: 26,
  codeSmall: 22,
  terminal: 24,
  caption: 34,
  title: 96,
  subtitle: 32,
  label: 20,
} as const;

/** 场景配色：S0 / S7 用浅底做「书挡」，中间保持深色。 */
export function paletteFor(palette: string) {
  if (palette === "black-on-white") {
    return { bg: COLORS.bgLight, fg: COLORS.textDark, dim: "#59636e", panel: "#ffffff", border: "#d1d9e0" };
  }
  return { bg: COLORS.bgDark, fg: COLORS.text, dim: COLORS.textDim, panel: COLORS.panel, border: COLORS.panelBorder };
}
