import React from "react";
import {
  AbsoluteFill,
  Audio,
  Composition,
  Sequence,
  staticFile,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";
import { hasAudioFor, meta, scenes } from "./data";
import { COLORS, FONT_SANS, SIZES, paletteFor } from "./theme";
import { entranceOpacity } from "./anim";
import type { Scene, Shot } from "./types";
import { CodeFileShot } from "./shots/CodeFile";
import { TerminalShot } from "./shots/Terminal";
import { TitleShot } from "./shots/Title";
import { BarCompareShot } from "./shots/BarCompare";
import { CoverageGridShot } from "./shots/CoverageGrid";
import { InvariantTableShot } from "./shots/InvariantTable";
import { ImportAuditShot } from "./shots/ImportAudit";
import { ScoreBreakdownShot } from "./shots/ScoreBreakdown";
import { DocTableShot } from "./shots/DocTable";
import { ThreePanelShot } from "./shots/ThreePanel";
import { ManimShot } from "./shots/Manim";

/**
 * 一个镜头 = 一个 shot 渲染器，放在自己的 <Sequence> 里。
 * 同一场景内的镜头均分场景时长（每个至少 2.5 秒）。
 */
const ShotRenderer: React.FC<{ shot: Shot; scene: Scene }> = ({ shot, scene }) => {
  const common = { shot, scene } as never;
  switch (shot.type) {
    case "code-file":
      return <CodeFileShot {...(common as { shot: Extract<Shot, { type: "code-file" }>; scene: Scene })} />;
    case "terminal":
      return <TerminalShot {...(common as { shot: Extract<Shot, { type: "terminal" }>; scene: Scene })} />;
    case "title":
      return <TitleShot {...(common as { shot: Extract<Shot, { type: "title" }>; scene: Scene })} />;
    case "bar-compare":
      return <BarCompareShot {...(common as { shot: Extract<Shot, { type: "bar-compare" }>; scene: Scene })} />;
    case "coverage-grid":
      return <CoverageGridShot {...(common as { shot: Extract<Shot, { type: "coverage-grid" }>; scene: Scene })} />;
    case "invariant-table":
      return <InvariantTableShot {...(common as { shot: Extract<Shot, { type: "invariant-table" }>; scene: Scene })} />;
    case "import-audit":
      return <ImportAuditShot {...(common as { shot: Extract<Shot, { type: "import-audit" }>; scene: Scene })} />;
    case "score-breakdown":
      return <ScoreBreakdownShot {...(common as { shot: Extract<Shot, { type: "score-breakdown" }>; scene: Scene })} />;
    case "doc-table":
      return <DocTableShot {...(common as { shot: Extract<Shot, { type: "doc-table" }>; scene: Scene })} />;
    case "three-panel":
      return <ThreePanelShot {...(common as { shot: Extract<Shot, { type: "three-panel" }>; scene: Scene })} />;
    case "manim":
      return <ManimShot {...(common as { shot: Extract<Shot, { type: "manim" }>; scene: Scene })} />;
    default: {
      const never: never = shot;
      throw new Error(`未实现的 shot 类型：${JSON.stringify(never)}`);
    }
  }
};

/** 场景内镜头的可见周期：均分，但每个镜头至少 2.5 秒。 */
export function shotWindows(scene: Scene, fps: number) {
  const total = Math.round(scene.durationSec * fps);
  const n = Math.max(1, scene.shots.length);
  const minFrames = Math.round(2.5 * fps);
  const each = Math.max(minFrames, Math.floor(total / n));
  return scene.shots.map((shot, i) => {
    const from = i * each;
    const remaining = total - from;
    const dur = i === n - 1 ? remaining : Math.min(each, remaining);
    return { shot, from, dur: Math.max(1, dur) };
  });
}

const SceneView: React.FC<{ scene: Scene }> = ({ scene }) => {
  const { fps } = useVideoConfig();
  const frame = useCurrentFrame();
  const pal = paletteFor(scene.palette);
  const windows = shotWindows(scene, fps);
  const sceneFrames = Math.round(scene.durationSec * fps);

  return (
    <AbsoluteFill style={{ backgroundColor: pal.bg }}>
      {windows.map((w, i) => (
        <Sequence key={i} from={w.from} durationInFrames={w.dur} name={`${scene.id}-${i + 1}-${w.shot.type}`}>
          <ShotRenderer shot={w.shot} scene={scene} />
        </Sequence>
      ))}

      {/* 场景角标：场景号 + 标题，纪录片里用于锚定进度 */}
      <div
        style={{
          position: "absolute",
          left: 48,
          bottom: 36,
          display: "flex",
          alignItems: "baseline",
          gap: 14,
          fontFamily: FONT_SANS,
          color: pal.dim,
          fontSize: SIZES.label + 2,
          letterSpacing: 2,
        }}
      >
        <span style={{ color: COLORS.accent, fontWeight: 700 }}>{scene.id}</span>
        <span>{scene.title}</span>
      </div>

      {/* 进度细线 */}
      <div style={{ position: "absolute", left: 0, bottom: 0, height: 3, width: "100%", backgroundColor: pal.border }}>
        <div
          style={{
            height: "100%",
            width: `${Math.min(100, (frame / Math.max(1, sceneFrames)) * 100)}%`,
            backgroundColor: COLORS.accent,
          }}
        />
      </div>
    </AbsoluteFill>
  );
};

/**
 * 旁白音频。
 *
 * 用 <Audio> 而不是 <Video>：只需要声音。
 * 文件确实不存在时**不渲染 Audio** —— Remotion 引用缺失的静态文件会直接报错，
 * 而未配音时应当只是静音，不是渲染失败。
 */
const SceneAudio: React.FC<{ scene: Scene }> = ({ scene }) => {
  const src = scene.audioFile;
  if (!src || !hasAudioFor(src)) return null;
  return <Audio src={staticFile(src)} volume={1} />;
};

/**
 * 旁白字幕。
 *
 * 有真实音频时按**音频真实时长**推进（旁白时长跟随音频，任何说话人语速变化都自动适配）；
 * 没有音频时才退回按字数估算。分段仍然是按旁白行切，一行一屏。
 */
const Caption: React.FC<{ scene: Scene }> = ({ scene }) => {
  const { fps } = useVideoConfig();
  const frame = useCurrentFrame();
  const pal = paletteFor(scene.palette);

  const budgetFrames = Math.round(scene.narrationBudgetSec * fps);
  if (frame >= budgetFrames) return null;

  const chunks = scene.narration
    .split(/\n/)
    .map((l) => l.trim())
    .filter(Boolean);
  const per = Math.max(1, Math.floor(budgetFrames / Math.max(1, chunks.length)));
  const idx = Math.min(chunks.length - 1, Math.floor(frame / per));

  // 字幕本身也做一次入场，避免切换瞬间突兀
  const o = entranceOpacity(frame - idx * per, fps, 0.35, 0.75);

  return (
    <AbsoluteFill style={{ justifyContent: "flex-end", alignItems: "center", paddingBottom: 96 }}>
      <div
        style={{
          maxWidth: 1500,
          padding: "18px 30px",
          borderRadius: 10,
          opacity: o,
          backgroundColor: scene.palette === "black-on-white" ? "rgba(255,255,255,0.88)" : "rgba(13,17,23,0.82)",
          border: `1px solid ${pal.border}`,
          fontFamily: FONT_SANS,
          fontSize: SIZES.caption,
          lineHeight: 1.5,
          color: pal.fg,
          textAlign: "center",
        }}
      >
        {chunks[idx]}
      </div>
    </AbsoluteFill>
  );
};

export const Episode1: React.FC = () => {
  const { fps } = useVideoConfig();
  let cursor = 0;

  return (
    <AbsoluteFill style={{ backgroundColor: COLORS.bgDark }}>
      {scenes.map((scene) => {
        const dur = Math.round(scene.durationSec * fps);
        const from = cursor;
        cursor += dur;
        return (
          <React.Fragment key={scene.id}>
            <Sequence from={from} durationInFrames={dur} name={scene.id}>
              <SceneAudio scene={scene} />
              <SceneView scene={scene} />
              <Caption scene={scene} />
            </Sequence>
          </React.Fragment>
        );
      })}
    </AbsoluteFill>
  );
};

/**
 * 诊断组合：把 Remotion 暴露的静态文件清单与音频判断结果画出来。
 *
 * 存在的理由：`hasAudioFor()` 依赖 window.remotion_staticFiles，而这个名字
 * 在不同版本间可能变化。没有这个面板就只能靠"渲染出来是静音"来反推，
 * 排查代价太高。
 */
export const DataProbe: React.FC = () => {
  const pal = paletteFor("dark");
  const w = globalThis as unknown as { remotion_staticFiles?: unknown };
  const list = w.remotion_staticFiles;
  const asArray = Array.isArray(list) ? (list as unknown[]) : null;
  const first = asArray && asArray.length > 0 ? asArray[0] : null;
  const asNames = asArray
    ? asArray.map((x) => (typeof x === "string" ? x : String((x as { name?: unknown })?.name ?? "?")))
    : [];
  const wavs = asNames.filter((n) => n.endsWith(".wav"));

  return (
    <AbsoluteFill style={{ backgroundColor: pal.bg, padding: 40, fontFamily: FONT_SANS, color: pal.fg }}>
      <h1 style={{ fontSize: 34, marginBottom: 6 }}>Remotion 静态文件诊断</h1>
      <p style={{ fontSize: 20, color: COLORS.accent }}>
        typeof = {typeof list} · Array.isArray = {String(Array.isArray(list))} · 项数 ={" "}
        {asArray ? asArray.length : "n/a"}
      </p>
      <p style={{ fontSize: 18, color: COLORS.yellow, wordBreak: "break-all" }}>
        第一项类型 = {first === null ? "null" : typeof first} · 内容 ={" "}
        {first === null ? "n/a" : JSON.stringify(first).slice(0, 220)}
      </p>
      <p style={{ fontSize: 18, color: COLORS.green }}>前 6 项 name = {asNames.slice(0, 6).join(" | ") || "（空）"}</p>
      <p style={{ fontSize: 18, color: COLORS.green }}>其中 .wav 共 {wavs.length} 个：{wavs.join("、") || "（无）"}</p>
      <p style={{ fontSize: 18, color: pal.dim, marginTop: 10 }}>
        hasAudioFor("audio/S0.wav") = {String(hasAudioFor("audio/S0.wav"))}
      </p>
      <ul style={{ fontSize: 16, color: pal.dim, lineHeight: 1.45 }}>
        {scenes.map((s) => (
          <li key={s.id}>
            {s.id} · 画面 {s.durationSec}s · 预算 {s.narrationBudgetSec}s · {s.audioFile ?? "无"} · 判定{" "}
            {s.audioFile ? String(hasAudioFor(s.audioFile)) : "n/a"}
          </li>
        ))}
      </ul>
    </AbsoluteFill>
  );
};

export const RemotionRoot: React.FC = () => {
  const fps = meta.resolution.fps;
  const durationInFrames = Math.round(meta.totalDurationSec * fps);

  return (
    <>
      <Composition
        id="Episode1"
        component={Episode1}
        durationInFrames={durationInFrames}
        fps={fps}
        width={meta.resolution.width}
        height={meta.resolution.height}
      />
      <Composition
        id="DataProbe"
        component={DataProbe}
        durationInFrames={fps * 6}
        fps={fps}
        width={1920}
        height={1080}
      />
    </>
  );
};
