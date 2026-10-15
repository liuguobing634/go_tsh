"""Synthesise the narration with CosyVoice 2 (local, CPU).

Reads docs/video/narration.json (produced by prepare-narration.py) and writes one
WAV per scene into the Remotion public folder, plus a manifest with the REAL
durations so the captions can follow the audio instead of assuming a narration
rate.

Design notes
------------
* Every chunk is synthesised separately and then concatenated. Long single
  utterances drift in prosody and make it impossible to re-take one sentence.
* The seed is fixed, so re-running produces the same audio. That matters here:
  the whole video pipeline is built on "the same input always yields the same
  output", and a voice that changes between renders would break caption timing.
* The sample rate and channel count are read back from the produced file, not
  assumed.

Usage
-----
    python scripts/video/synthesize-narration.py ^
        --cosyvoice D:\\tools\\CosyVoice ^
        --prompt-wav  D:\\tools\\CosyVoice\\asset\\zero_shot_prompt.wav ^
        --prompt-text "希望你以后能够做的比我还好呦。"

Run it from the Go repo root. Use --scene S4 to do a single scene first.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
import wave
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
NARRATION = REPO / "docs" / "video" / "narration.json"
OUT_DIR = REPO / "docs" / "video" / "remotion" / "public" / "audio"
MANIFEST = REPO / "docs" / "video" / "audio-manifest.json"
FFMPEG = Path("D:/ffmpeg/bin/ffmpeg.exe")

SEED = 20261015  # 固定种子，保证可复现

# 存成 16-bit PCM。
# torchaudio.save 默认按张量 dtype 存 float32，而 Python 的 wave 模块不支持
# WAVE_FORMAT_IEEE_FLOAT（format 3），读回来会报 "unknown format: 3"。
# 16-bit PCM 也是语音常规交付格式，Remotion/ffmpeg 都直接吃。
PCM_BITS = 16


def save_pcm16(path: Path, speech, sample_rate: int) -> None:
    import torchaudio

    torchaudio.save(str(path), speech, sample_rate, encoding="PCM_S", bits_per_sample=PCM_BITS)


def probe_wav(path: Path) -> dict:
    """读取 WAV 的真实参数（不假设采样率/声道）。"""
    with wave.open(str(path), "rb") as w:
        frames = w.getnframes()
        rate = w.getframerate()
        return {
            "sampleRate": rate,
            "channels": w.getnchannels(),
            "sampleWidth": w.getsampwidth(),
            "frames": frames,
            "durationSec": round(frames / float(rate), 3),
        }


def concat_wavs(parts: list[Path], dest: Path, gap_sec: float = 0.18) -> dict:
    """按顺序拼接若干 WAV，插入短静音间隔（句间停顿）。"""
    if not parts:
        raise SystemExit("没有待拼接的音频片段")

    first = probe_wav(parts[0])
    rate, ch, sw = first["sampleRate"], first["channels"], first["sampleWidth"]
    frame_bytes = ch * sw
    silence = b"\x00" * int(rate * gap_sec) * frame_bytes

    dest.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(dest), "wb") as out:
        out.setnchannels(ch)
        out.setsampwidth(sw)
        out.setframerate(rate)
        for i, p in enumerate(parts):
            info = probe_wav(p)
            if (info["sampleRate"], info["channels"], info["sampleWidth"]) != (rate, ch, sw):
                raise SystemExit(f"{p.name} 的参数与首段不一致：{info}")
            with wave.open(str(p), "rb") as src:
                out.writeframes(src.readframes(info["frames"]))
            if i != len(parts) - 1:
                out.writeframes(silence)
    return probe_wav(dest)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cosyvoice", required=True, help="CosyVoice 仓库根目录")
    ap.add_argument("--model", default="pretrained_models/CosyVoice2-0.5B")
    ap.add_argument("--prompt-wav", required=True, help="参考音频（音色来源）")
    ap.add_argument("--prompt-text", required=True, help="参考音频对应的文本")
    ap.add_argument("--scene", action="append", help="只合成指定场景，可重复")
    ap.add_argument("--threads", type=int, default=8,
                    help="torch CPU 线程数（实测 8 线程比默认 16 快约 40%）")
    ap.add_argument("--dry-run", action="store_true", help="只列出将要合成的内容")
    args = ap.parse_args()

    cv_root = Path(args.cosyvoice).resolve()
    if not cv_root.is_dir():
        raise SystemExit(f"CosyVoice 目录不存在：{cv_root}")

    prompt_wav = Path(args.prompt_wav).resolve()
    if not prompt_wav.is_file():
        raise SystemExit(f"参考音频不存在：{prompt_wav}")

    doc = json.loads(NARRATION.read_text(encoding="utf-8"))
    scenes = doc["scenes"]
    if args.scene:
        want = {s.upper() for s in args.scene}
        scenes = [s for s in scenes if s["id"].upper() in want]
        if not scenes:
            raise SystemExit(f"没有匹配的场景：{args.scene}")

    total_chunks = sum(len(s["chunks"]) for s in scenes)
    print(f"CosyVoice : {cv_root}")
    print(f"模型      : {args.model}")
    print(f"参考音频  : {prompt_wav}")
    print(f"输出目录  : {OUT_DIR}")
    print(f"场景 {len(scenes)} 个，共 {total_chunks} 句，种子 {SEED}")
    print()

    if args.dry_run:
        for s in scenes:
            print(f"  {s['id']}  {len(s['chunks'])} 句  预估 {s['estimatedDurationSec']}s")
            for i, c in enumerate(s["chunks"], 1):
                print(f"      {i:>2}. {c}")
        return 0

    # 让 CosyVoice 的 import 能解析 third_party/Matcha-TTS
    os.environ["PYTHONPATH"] = str(cv_root / "third_party" / "Matcha-TTS")
    sys.path.insert(0, str(cv_root))
    sys.path.insert(0, str(cv_root / "third_party" / "Matcha-TTS"))

    import torch
    from cosyvoice.cli.cosyvoice import AutoModel

    if args.threads > 0:
        torch.set_num_threads(args.threads)
        print(f"torch 线程数设为 {args.threads}")

    torch.manual_seed(SEED)
    if torch.cuda.is_available():
        torch.cuda.manual_seed_all(SEED)

    t0 = time.time()
    model = AutoModel(model_dir=str(cv_root / args.model))
    print(f"模型加载完成，用时 {time.time() - t0:.1f}s，采样率 {model.sample_rate}")
    print(f"内置音色：{model.list_available_spks() or '（无，需提供参考音频）'}")
    print()

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    tmp_dir = OUT_DIR / "_tmp"
    tmp_dir.mkdir(exist_ok=True)

    manifest = {"seed": SEED, "promptWav": str(prompt_wav), "promptText": args.prompt_text,
                "model": args.model, "scenes": []}

    # 与既有 manifest **合并**，而不是覆盖。
    # 踩过的坑：先单独跑一轮 S0、再跑一轮 S1–S7，第二轮的覆盖把 S0 的记录丢了，
    # 于是 build-storyboards.py 认为音频不全而拒绝重建分镜。
    # 只保留同一次运行的条目（种子/参考音频/模型一致），避免混入无关结果。
    if MANIFEST.is_file():
        try:
            prev = json.loads(MANIFEST.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, OSError):
            prev = {}
        same_run = (
            prev.get("seed") == SEED
            and prev.get("promptWav") == str(prompt_wav)
            and prev.get("promptText") == args.prompt_text
            and prev.get("model") == args.model
        )
        if same_run:
            manifest["scenes"] = list(prev.get("scenes", []))
            print(f"manifest 已存在且同源，保留其中 {len(manifest['scenes'])} 条旧记录")
        else:
            print("manifest 存在但来源不同（种子/参考音频/模型不一致），将整体替换")

    for s in scenes:
        sid = s["id"]
        print(f"[{sid}] {len(s['chunks'])} 句")
        parts: list[Path] = []
        t_scene = time.time()

        for i, chunk in enumerate(s["chunks"], 1):
            pieces = [
                j["tts_speech"]
                for j in model.inference_zero_shot(
                    chunk, args.prompt_text, str(prompt_wav), stream=False
                )
            ]
            if not pieces:
                raise SystemExit(f"{sid} 第 {i} 句没有产生音频")
            speech = torch.cat(pieces, dim=1)

            part = tmp_dir / f"{sid}-{i:02d}.wav"
            save_pcm16(part, speech, model.sample_rate)
            info = probe_wav(part)
            parts.append(part)
            # flush=True 很重要：否则长时间后台运行时进度不可见，
            # 只能等整段跑完才看到输出（第一轮就吃了这个亏）。
            print(f"   {i:>2}/{len(s['chunks'])}  {info['durationSec']:>6.2f}s  {chunk[:34]}", flush=True)

        dest = OUT_DIR / f"{sid}.wav"
        info = concat_wavs(parts, dest)
        elapsed = time.time() - t_scene
        rtf = elapsed / max(info["durationSec"], 0.01)

        print(f"   -> {dest.name}  {info['durationSec']}s  "
              f"（合成耗时 {elapsed:.1f}s，实时率 {rtf:.2f}x）")

        manifest["scenes"].append({
            "id": sid,
            "title": s["title"],
            "file": f"audio/{sid}.wav",
            "durationSec": info["durationSec"],
            "sampleRate": info["sampleRate"],
            "channels": info["channels"],
            "chunkCount": len(s["chunks"]),
            "estimatedDurationSec": s["estimatedDurationSec"],
            "sceneDurationSec": s["sceneDurationSec"],
            "synthSeconds": round(elapsed, 1),
            "realtimeFactor": round(rtf, 3),
        })

    # 按场景 id 排序去重，保证 manifest 顺序稳定（合并旧记录后可能乱序）
    by_id = {}
    for x in manifest["scenes"]:
        by_id[x["id"]] = x
    manifest["scenes"] = [by_id[k] for k in sorted(by_id)]

    # 清理临时片段
    shutil.rmtree(tmp_dir, ignore_errors=True)

    total_audio = sum(x["durationSec"] for x in manifest["scenes"])
    manifest["totalAudioSec"] = round(total_audio, 3)
    manifest["episodeDurationSec"] = doc["meta"]["episodeDurationSec"]
    MANIFEST.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    print()
    print(f"音频总长 {total_audio:.1f}s / 成片 {doc['meta']['episodeDurationSec']}s")
    print(f"wrote {MANIFEST.relative_to(REPO)}")
    for x in manifest["scenes"]:
        delta = x["sceneDurationSec"] - x["durationSec"]
        flag = "OK" if delta >= 0 else "超长"
        print(f"  {x['id']}  音频 {x['durationSec']:>6.2f}s  场景 {x['sceneDurationSec']:>3}s  "
              f"余量 {delta:>+6.2f}s  {flag}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
