/**
 * 类型定义 —— 与 docs/video/storyboards.json 的结构一一对应。
 *
 * 设计意图：把「画面内容」全部表达为**对真实数据源的引用**，而不是内联文本。
 * 见 storyboards.json 的 renderPolicy：type=code-file / terminal / doc-table
 * 的元素必须从 source 指向的真实文件读取后原样渲染。
 */

export interface Resolution {
  width: number;
  height: number;
  fps: number;
}

export interface Meta {
  project: string;
  episode: number;
  title: string;
  sourceScript: string;
  evidenceIndex: string;
  totalDurationSec: number;
  resolution: Resolution;
  generatedBy: string;
  narrationCharsPerSec: number;
  aspect: string;
}

export interface AudioSpec {
  narrationLang: string;
  charsPerSec: number;
  pauseAfterSceneSec: number;
  voiceStyle: string;
  /** "audio" = 各场景预算取自真实音频；"estimate" = 按字数推算。 */
  timingSource?: string;
  /** 旁白结束后的收尾余量（秒）。 */
  tailSec?: number;
  /** 是否每个场景各有一个音频文件。 */
  perSceneFiles?: boolean;
}

export interface RenderPolicy {
  codeSourceOfTruth: boolean;
  terminalSourceOfTruth: boolean;
  note: string;
  /** 负向约束：画面与旁白中都不得出现的内容。 */
  forbidden: string[];
}

export interface Region {
  x: number;
  y: number;
  w: number;
  h: number;
}

interface ShotBase {
  /** 1080p 画布坐标；渲染器不做布局猜测。 */
  region?: Region;
  /** 必须在画面中做视觉强调的片段（对应脚本的「重点高亮区域」）。 */
  highlight?: string[];
  /** 精确的 markdown 标题（含 # 前缀），比模糊 label 更可靠。 */
  section?: string;
  /** 在 section 内挑选代码块的提示词（匹配 ``` 之后的第一行）。 */
  want?: string;
}

/** 从仓库真实文件读取代码并渲染，可选择行范围。 */
export interface CodeFileShot extends ShotBase {
  type: "code-file";
  source: string;
  /** "12-34" 形式的行范围；省略表示全文。 */
  lines?: string;
  effect?: "typewriter" | "none";
  lineDelaySec?: number;
  /** 打字机等效果揭示真实的文件，绝不由模型生成代码。 */
  reveal?: boolean;
}

/** 从 docs/video/demo-cases.md 的实测输出中读取并逐字渲染。 */
export interface TerminalShot extends ShotBase {
  type: "terminal";
  source: string;
  /** demo-cases.md 中的锚点标签（模糊匹配，优先用 section）。 */
  label?: string;
}

/** 由代码内联渲染的动画（参数来自实测数据；见 shots/Manim.tsx）。 */
export interface IdfCurveParams {
  points: { df: number; idf: number }[];
  n: number;
  markHalf?: number;
}
export interface AxisToken {
  term: string;
  pos: number;
  filtered?: boolean;
}
export interface PositionAxisParams {
  query: AxisToken[];
  gap: AxisToken[];
  adj: AxisToken[];
}
export interface MergeIntersectParams {
  left: { field: string; docs: number[] };
  right: { field: string; docs: number[] };
}

export interface ManimShot extends ShotBase {
  type: "manim";
  animation: "idf-curve" | "position-axis-align" | "merge-intersect";
  params: IdfCurveParams | PositionAxisParams | MergeIntersectParams;
}

export interface TitleShot extends ShotBase {
  type: "title";
  text: string;
  subtitle?: string;
}

export interface BarCompareShot extends ShotBase {
  type: "bar-compare";
  label: string;
  items: { name: string; value: number; unit: string }[];
  ratioLabel?: string;
  source?: string;
}

export interface CoverageGridShot extends ShotBase {
  type: "coverage-grid";
  items: { pkg: string; pct: number }[];
  source?: string;
}

export interface InvariantTableShot extends ShotBase {
  type: "invariant-table";
  rows: { name: string; value: boolean }[];
  source?: string;
}

export interface ImportAuditShot extends ShotBase {
  type: "import-audit";
  packages: string[];
  thirdParty: string[];
}

export interface ScoreBreakdownShot extends ShotBase {
  type: "score-breakdown";
  source: string;
  label?: string;
  emphasise?: string[];
}

export interface DocTableShot extends ShotBase {
  type: "doc-table";
  source: string;
  lines?: string;
  fadeInRows?: boolean;
}

export interface ThreePanelShot extends ShotBase {
  type: "three-panel";
  panels: { kind: string; title: string; source: string; label?: string; section?: string; want?: string }[];
}

/**
 * 归属标注。
 *
 * 存在的理由：片头包含**创作者第一人称自述**，而仓库内没有可核验的对话记录，
 * 自述不得冒充实证。因此自述场景与实证场景必须在画面上被显式区分。
 */
export type Attribution = "creator-statement" | "verified-facts";

/** 片头 S-1：数字打字机 + 常驻归属角标。数字来自 git，写死在 params 里。 */
export interface StatRevealShot extends ShotBase {
  type: "stat-reveal";
  stats: { value: string; unit: string; caption: string }[];
  tagline: string;
  taglineSub?: string;
  attribution: Attribution;
}

/** 片头 S-1：单句结论 + 归属角标。 */
export interface StatementShot extends ShotBase {
  type: "statement";
  text: string;
  attribution: Attribution;
}

/** 片头 S0：四要点卡片，可收拢为一条全片导航轴。 */
export interface PillarCardsShot extends ShotBase {
  type: "pillar-cards";
  pillars: { name: string; scene: string; files: string }[];
  collapseToAxis?: boolean;
}

export type Shot =
  | CodeFileShot
  | TerminalShot
  | ManimShot
  | TitleShot
  | BarCompareShot
  | CoverageGridShot
  | InvariantTableShot
  | ImportAuditShot
  | ScoreBreakdownShot
  | DocTableShot
  | ThreePanelShot
  | StatRevealShot
  | StatementShot
  | PillarCardsShot;

export interface Scene {
  id: string;
  slug: string;
  title: string;
  declaredDurationSec: number | null;
  /** 旁白全文，逐字来自 video-script.md（**保留 markdown**，供核对原文）。 */
  narration: string;
  /**
   * 去掉 markdown 标记后的旁白，**字幕必须用它**。
   *
   * 直接用 narration 会让画面上出现 `**倒排索引**` 这样的星号
   * （全片 10 个场景里有 8 个中招）。转换在构建期完成，见
   * scripts/video/build-storyboards.py 的 narration_for_caption()。
   */
  narrationPlain?: string;
  narrationChars: number;
  /**
   * 字幕/旁白预算时长。
   *
   * audio.timingSource === "audio" 时这是**真实音频时长**（秒）；
   * 为 "estimate" 时是按 narrationCharsPerSec 推算的估计值。
   */
  narrationBudgetSec: number;
  palette: string;
  startSec: number;
  endSec: number;
  durationSec: number;
  /** 真实旁白音频时长；未配音时为 null。 */
  audioDurationSec?: number | null;
  /** 该场景是否已有旁白音频。 */
  hasAudio?: boolean;
  /** public 下的音频相对路径，例如 "audio/S0.wav"；无音频时为 null。 */
  audioFile?: string | null;
  shots: Shot[];
}

/** 构建时从磁盘读出的文件快照，渲染器据此渲染，无需访问文件系统。 */
export interface ResolvedSource {
  lineCount: number;
  content: string;
  /** 被引用时的完整写法（可能含 #fragment）。 */
  citedAs?: string;
  /** 请求了行范围时存在。 */
  slice?: string;
  sliceRange?: [number, number];
}

export interface Storyboards {
  $schema?: string;
  meta: Meta;
  audio: AudioSpec;
  renderPolicy: RenderPolicy;
  /** key 为仓库相对路径。 */
  resolved: Record<string, ResolvedSource>;
  scenes: Scene[];
}
