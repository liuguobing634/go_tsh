import { Config } from "@remotion/cli/config";

// ---------------------------------------------------------------------------
// ffmpeg: 本机安装在 D:\ffmpeg\bin。
// Remotion 通过 PATH 查找 ffmpeg / ffprobe，因此这里显式追加该目录，
// 避免依赖用户手动配置系统 PATH。
// ---------------------------------------------------------------------------
const FFMPEG_DIR = "D:\\ffmpeg\\bin";
if (!(process.env.PATH ?? "").includes(FFMPEG_DIR)) {
  process.env.PATH = `${FFMPEG_DIR};${process.env.PATH ?? ""}`;
}

// 发布规格：1920x1080 / 30fps / H.264（与 storyboards.json 的 meta 一致）
Config.setVideoImageFormat("jpeg");
Config.setPixelFormat("yuv420p");
Config.setCodec("h264");
Config.setCrf(18); // 高质量；代码画面需要锐利的文字边缘
Config.setConcurrency(4); // Intel Arc 无 CUDA，CPU 编码即可
Config.overrideWebpackConfig((cfg) => cfg);
