/**
 * markdown 快照切片工具。
 *
 * 所有终端/文档类镜头都从这里取文本，保证：
 *   1. 只按**精确标题**定位，不做模糊子串匹配
 *      （模糊匹配曾经把语料表格里的 `d1` 当锚点，抽出了完全无关的段落）；
 *   2. 切片边界是该标题到下一个同级或更高级标题，不会跨章节偷内容；
 *   3. 优先返回代码块内容——实测输出都写在那里。
 */

interface Section {
  heading: string;
  level: number;
  lines: string[];
}

/** 把 markdown 按标题切成小节。 */
export function sections(markdown: string): Section[] {
  const lines = markdown.split("\n");
  const starts: { i: number; heading: string; level: number }[] = [];

  lines.forEach((l, i) => {
    const m = /^(#{1,6})\s+(.*)$/.exec(l);
    if (m) starts.push({ i, heading: l.trim(), level: m[1].length });
  });

  return starts.map((s, k) => {
    let end = lines.length;
    for (let j = k + 1; j < starts.length; j++) {
      if (starts[j].level <= s.level) {
        end = starts[j].i;
        break;
      }
    }
    return { heading: s.heading, level: s.level, lines: lines.slice(s.i + 1, end) };
  });
}

/** 按精确标题找小节；找不到返回 null。 */
export function findSection(markdown: string, heading: string): Section | null {
  const want = heading.trim();
  return sections(markdown).find((s) => s.heading === want) ?? null;
}

/** 取出小节里的所有 ``` 代码块内容。 */
export function codeBlocks(lines: string[]): string[] {
  const out: string[] = [];
  let i = 0;
  while (i < lines.length) {
    if (lines[i].trim().startsWith("```")) {
      const start = i + 1;
      let end = start;
      while (end < lines.length && !lines[end].trim().startsWith("```")) end++;
      out.push(lines.slice(start, end).join("\n"));
      i = end + 1;
      continue;
    }
    i++;
  }
  return out;
}

export interface ExtractRequest {
  markdown: string;
  /** 精确标题（含 # 前缀）。 */
  section?: string;
  /** section 内挑代码块的提示词，匹配代码块首行。 */
  want?: string;
  /** 兼容旧字段：模糊匹配的锚点文本。 */
  label?: string;
  /** 没有代码块时，最多返回多少行正文。 */
  maxProseLines?: number;
}

/**
 * 按请求抽取用于渲染的文本。
 *
 * 优先级：精确 section + want → section 的第一个代码块 → section 的正文
 *        → label 模糊匹配（仅作兜底）→ 返回空串并让调用方显式暴露问题。
 */
export function extract(req: ExtractRequest): string {
  const { markdown, section, want, label, maxProseLines = 12 } = req;

  let target: Section | null = null;
  if (section) {
    target = findSection(markdown, section);
    if (!target) {
      // 标题写错是配置错误，必须显式失败，否则画面会静默变空。
      throw new Error(
        `storyboards.json 的 section 在快照中不存在：${section}\n` +
          `可用的标题：\n${sections(markdown)
            .map((s) => "  " + s.heading)
            .join("\n")}`,
      );
    }
  }

  if (target) {
    const blocks = codeBlocks(target.lines);
    if (blocks.length > 0) {
      if (want) {
        const hit = blocks.find((b) => b.toLowerCase().includes(want.toLowerCase()));
        if (hit) return hit;
      }
      return blocks[0];
    }
    return target.lines
      .filter((l) => l.trim() !== "" && !l.trim().startsWith(">"))
      .slice(0, maxProseLines)
      .map((l) => l.replace(/\*\*/g, "").replace(/`/g, ""))
      .join("\n");
  }

  // 兜底：模糊 label（旧行为）。保留是为了不破坏已有配置，但新配置不应使用。
  if (label) {
    const idx = markdown.split("\n").findIndex((l) => l.includes(label));
    if (idx >= 0) {
      const blocks = codeBlocks(markdown.split("\n").slice(idx));
      if (blocks.length > 0) return blocks[0];
    }
  }

  return "";
}
