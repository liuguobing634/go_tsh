import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { FONT_MONO, FONT_SANS } from "../theme";
import { paletteFor } from "../theme";
import { entranceOpacity, entranceShift } from "../anim";
import type { Scene, Shot } from "../types";

export type TitleShotProps = { shot: Extract<Shot, { type: "title" }>; scene: Scene };

export const TitleShot: React.FC<TitleShotProps> = ({ shot, scene }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pal = paletteFor(scene.palette);

  const opacity = entranceOpacity(frame, fps, 1.2);
  const rise = entranceShift(frame, fps, 16, 1.2);

  return (
    <div
      style={{
        position: "absolute",
        left: shot.region?.x ?? 200,
        top: shot.region?.y ?? 300,
        width: shot.region?.w ?? 1500,
        opacity,
        transform: `translateY(${rise}px)`,
      }}
    >
      <div style={{ fontFamily: FONT_MONO, fontSize: 96, fontWeight: 700, color: pal.fg, letterSpacing: 2 }}>
        {shot.text}
      </div>
      {shot.subtitle ? (
        <div style={{ fontFamily: FONT_SANS, fontSize: 32, color: pal.dim, marginTop: 18 }}>{shot.subtitle}</div>
      ) : null}
    </div>
  );
};
