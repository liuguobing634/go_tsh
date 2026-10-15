import { interpolate } from "remotion";

/**
 * 入场动画的安全不透明度。
 *
 * 为什么不能从 0 开始：
 *   Remotion 的 <Sequence> 在每个镜头的第 0 帧就会渲染，如果入场动画从
 *   opacity 0 拉起，那一帧就是**全空白**。实测在 S2 的镜头切换处出现了
 *   855px 的空白带 —— 那正是新镜头刚出现的瞬间。
 *
 * 因此所有入场都从一个可见的基线（默认 0.3）拉满，保证任何一帧都有内容。
 */
export const FADE_FROM = 0.3;

export function entranceOpacity(frame: number, fps: number, seconds = 0.45, from = FADE_FROM): number {
  return interpolate(frame, [0, Math.round(fps * seconds)], [from, 1], {
    extrapolateRight: "clamp",
    extrapolateLeft: "clamp",
  });
}

/** 带延迟的入场（用于错峰出现，同样不会出现全空）。 */
export function staggeredOpacity(
  frame: number,
  fps: number,
  index: number,
  staggerSeconds = 0.12,
  seconds = 0.45,
  from = FADE_FROM,
): number {
  return entranceOpacity(frame - Math.round(fps * staggerSeconds * index), fps, seconds, from);
}

/** 位移动画：从右／下轻微滑入，位移量很小，避免布局抖动。 */
export function entranceShift(frame: number, fps: number, pixels = 16, seconds = 0.45): number {
  return interpolate(frame, [0, Math.round(fps * seconds)], [pixels, 0], {
    extrapolateRight: "clamp",
    extrapolateLeft: "clamp",
  });
}
