import storyboardsJson from "./storyboards.json";
import type { Scene, Shot, Storyboards } from "./types";

/**
 * 数据绑定层。
 *
 * 故事的唯一来源是 src/storyboards.json —— 它由
 * `python scripts/video/build-storyboards.py` 从
 *   * docs/video/video-script.md（旁白与分镜，逐字提取）
 *   * docs/video/demo-cases.md  / project-brief.md（实测输出）
 *   * 仓库真实源码（internal/**、go.mod、README.md）
 * 生成，并把所有被引用的文件内容快照进 `resolved`。
 *
 * 因此渲染器**不访问文件系统**，也不可能渲染出仓库里不存在的代码。
 * 这是 docs/video/evidence-index.md 所要求的证据纪律的工程实现。
 */
export const storyboards = storyboardsJson as unknown as Storyboards;

export const { meta, audio, renderPolicy, resolved, scenes } = storyboards;

/** 字幕时序来源："audio" 表示 narrationBudgetSec 取自真实音频时长。 */
export const audioTimingSource: string = (audio as { timingSource?: string }).timingSource ?? "estimate";

/**
 * 判断某个 public 下的静态文件是否真的存在。
 *
 * 为什么需要：Remotion 在 staticFile() 引用不存在的文件时会直接报错，
 * 而"还没配音"应当只是静音，不该让整片渲染失败。
 *
 * ⚠️ Remotion 注入的 `window.remotion_staticFiles` 元素**是对象**，不是字符串：
 *     {"name":"audio/S0.wav","lastModified":...,"sizeInBytes":...,"src":"/public/audio/S0.wav"}
 * 第一版写成 `list.includes(relativePath)` 拿字符串比对象，恒为 false ——
 * 结果是所有音频都被判定为"不存在"而静音，且没有任何报错。
 * 必须按 `name` 字段匹配。
 */
export function hasAudioFor(relativePath: string): boolean {
  const w = globalThis as unknown as { remotion_staticFiles?: unknown };
  const list = w.remotion_staticFiles;
  if (!Array.isArray(list)) return true; // 拿不到清单就不拦，让错误正常暴露

  const want = relativePath.replace(/^\/+/, "");
  return list.some((entry) => {
    if (typeof entry === "string") return entry === want;
    const name = (entry as { name?: unknown })?.name;
    return typeof name === "string" && name.replace(/^\/+/, "") === want;
  });
}

/** 取某个 source 的快照；缺失即抛错，绝不静默渲染空内容。 */
export function source(rel: string): { lineCount: number; content: string; slice?: string; sliceRange?: number[] } {
  const entry = resolved[rel.split("#", 1)[0]];
  if (!entry) {
    throw new Error(
      `storyboards.json 缺少 source "${rel}" 的快照。请重新运行 scripts/video/build-storyboards.py。`,
    );
  }
  return entry;
}

/**
 * 取用于渲染的文本：优先使用行范围切片，否则用全文。
 * `preferSlice` 让 code-file / doc-table 这类带行范围的镜头自动拿到精确片段。
 */
export function textFor(rel: string, preferSlice = true): string {
  const entry = source(rel);
  if (preferSlice && entry.slice !== undefined) return entry.slice;
  return entry.content;
}

export function sceneById(id: string): Scene {
  const s = scenes.find((x) => x.id === id);
  if (!s) throw new Error(`未知场景：${id}`);
  return s;
}

export function shotTypes(s: Scene): string[] {
  return s.shots.map((x: Shot) => x.type);
}

/** 供 renderPolicy 校验使用：把 forbidden 列表暴露给运行时断言。 */
export const forbiddenPhrases = renderPolicy.forbidden;
