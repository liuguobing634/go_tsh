# 视频制作指南（Remotion 工程说明）

> 第一期技术纪录片素材 · 制作侧文档
> 本文说明**如何按照 `video-script.md` 出片**：工具链、目录结构、命令、
> 以及一条必须遵守的纪律 —— 画面上的代码与数字不许被"美化"。

---

## 0. 为什么不用文生视频工具

这是本工程存在的原因，先说清楚：

| 脚本必须展示的内容 | 文生视频会做什么 | 后果 |
| --- | --- | --- |
| 逐行真实代码（`bm25.go` 19 行、`TermKey`、`phraseFits`） | 生成"看起来像代码"的乱码，标识符会漂移 | 观众逐帧暂停即发现是假的 |
| 真实终端输出（`positions=[8 15 20]`、`deadlock confirmed`） | 无法复现，通常生成无意义字符 | 整期素材的证据链断掉 |
| 精确动画（IDF 曲线、位置轴对齐、双指针归并） | 生成"科技感"抽象画面 | 信息量为零 |

因此本工程的做法是：**真实数据 + 程序化合成**。
文生视频只可用于 S7 之类**不承载任何事实**的气氛空镜，且需人工核对不得夹带数字。

---

## 1. 工具链（本机已验证）

| 组件 | 版本 / 位置 | 用途 |
| --- | --- | --- |
| Node.js | **v22.21.0** | Remotion 运行时 |
| npm | **10.9.4** | 依赖安装 |
| Python | **3.12.4** | `build-storyboards.py` 生成分镜 |
| ffmpeg | **`D:\ffmpeg\bin`**（gyan.dev full build） | 编码与合成 |
| Remotion | **4.0.380** | 时间轴、渲染 |
| Chrome Headless Shell | 由 Remotion 首次运行时自动下载（约 102 MB） | 截图渲染 |

ffmpeg 的关键能力确认（`ffmpeg -encoders`）：`libx264`、`libx265`、`aac`、
`prores_ks`、`libvpx-vp9` 均可用；并带 `--enable-libvpl` / `--enable-amf`，
Intel Arc 可用硬件加速（当前配置走 CPU，见 `remotion.config.ts`）。

`remotion.config.ts` 已把 `D:\ffmpeg\bin` 追加进 `PATH`，因此**无需手动配置系统 PATH**。

> ⚠️ 本机 GPU 是 Intel Arc，**没有 NVIDIA CUDA**。
> 本地跑开源文生视频模型（Wan / HunyuanVideo / LTX 等）不现实，不要在这条路上浪费时间。

---

## 2. 目录结构

```
docs/video/
├── video-script.md           ← 人读的脚本（旁白 + 分镜 + 核验清单）
├── demo-cases.md             ← 实测输出记录（画面的数据源头）
├── project-brief.md          ← 实测数据（体积、覆盖率、行数、门禁）
├── storyboards.json          ← 机器可读分镜（由脚本生成，勿手改）
└── remotion/
    ├── package.json
    ├── remotion.config.ts     ← ffmpeg PATH、编码参数
    ├── tsconfig.json
    └── src/
        ├── index.ts           ← registerRoot
        ├── Root.tsx           ← 组合定义、场景序列、字幕层
        ├── data.ts            ← 读取 storyboards.json，禁止渲染期访问文件系统
        ├── markdown.ts        ← 按**精确标题**从快照中取代码块
        ├── theme.ts           ← 配色与字体令牌
        ├── types.ts           ← 与 storyboards.json 一一对应的类型
        ├── storyboards.json   ← 上面 JSON 的镜像（放在 bundler root 内）
        └── shots/             ← 11 种镜头渲染器
            ├── CodeFile.tsx      代码（真实文件快照 + 自动缩放字号）
            ├── Terminal.tsx      实测输出
            ├── Manim.tsx         IDF 曲线 / 位置轴 / 双指针归并（代码内联）
            ├── ThreePanel.tsx    原文 → token → posting
            ├── ImportAudit.tsx   依赖审计
            ├── ScoreBreakdown.tsx BM25 逐项计算
            ├── InvariantTable.tsx 不变量断言结果
            ├── CoverageGrid.tsx  覆盖率
            ├── BarCompare.tsx    数值对比条
            ├── DocTable.tsx      文档表格
            └── Title.tsx         标题
```

生成脚本在 `scripts/video/`：

| 脚本 | 作用 |
| --- | --- |
| `build-storyboards.py` | 从 `video-script.md` 抽旁白、绑数据源、校验标题锚点、定时长（**优先用真实音频时长**） |
| `set-layout.py` | **布局权威表**：把 26 个镜头的 `region` 坐标写入 `build-storyboards.py`，并校验不出安全区 |
| `analyse-layout.py` | 渲染抽帧 → ffmpeg 转裸 RGB → 量内容包围盒与最大空白带，输出 `layout-report.json` |
| `prepare-narration.py` | 把旁白转成 TTS 就绪文本（术语注音、数字口语化、剥 markdown），输出 `narration.json` |
| `synthesize-narration.py` | 调用本地 CosyVoice 2 合成 8 段旁白，输出 WAV + 真实时长 `audio-manifest.json` |
| `fence-demo-data.py` | 一次性工具：把 `demo-cases.md` 里的实测数据块包进代码围栏，便于按标题抽取 |

**布局改动请改 `set-layout.py` 的 `REGIONS` 表，不要手改 `build-storyboards.py`**——
后者是被覆盖的产物，且 26 个坐标手改极易错位。

---

## 3.6 旁白配音（本地 CosyVoice 2）

### 环境（已装好，位于仓库外）

| 项 | 值 |
| --- | --- |
| 位置 | `D:\tools\CosyVoice`（**不进本仓库**） |
| Python | 3.10.19（用 `uv` 装载，无需 conda） |
| torch | 2.3.1+**cpu**（Intel Arc 无 CUDA，刻意避开 CUDA wheel） |
| 模型 | `CosyVoice2-0.5B`，**5.36 GB** |
| 参考音频 | `D:\tools\CosyVoice\asset\my_voice.wav`（12.85s，24 kHz 单声道 PCM16） |

### 三步流程

```powershell
# 1) 旁白 → TTS 就绪文本（术语注音、数字口语化、剥 markdown）
python scripts\video\prepare-narration.py
#    输出 docs/video/narration.json 与 narration.md

# 2) 逐场景合成（CPU，约 8–15 倍实时；全片 90–120 分钟）
$env:PYTHONPATH = "D:\tools\CosyVoice\third_party\Matcha-TTS"
& D:\tools\CosyVoice\.venv\Scripts\python.exe scripts\video\synthesize-narration.py `
  --cosyvoice D:\tools\CosyVoice `
  --prompt-wav D:\tools\CosyVoice\asset\my_voice.wav `
  --prompt-text "<参考音频里实际读的那段，必须逐字一致>" `
  --threads 8
#    输出 docs/video/remotion/public/audio/S0.wav … S7.wav
#         docs/video/audio-manifest.json（含每段真实时长与实时率）

# 3) 重建分镜：场景时长改为「音频时长 + 1.6s 收尾」
python scripts\video\build-storyboards.py
```

### 关键设计

- **时长由音频驱动。** `build-storyboards.py` 检测到 `audio-manifest.json` 就切换为
  「场景时长 = 音频真实时长 + `TAIL_SEC`(1.6s)」，字幕预算也取音频真实时长。
  这条路径下**不再**把总长压到 8:00 —— 成片该多长就多长，强行凑满会让字幕与语音错位。
- **固定随机种子**（`SEED = 20261015`），同一文本重跑得到同样音频。
  这个工程整体建立在「同输入必得同输出」上，配音若每次不同，字幕时间轴就失效了。
- **逐句合成再拼接**（句间插 0.18s 静音）。长句单独合成会韵律漂移，
  而且没法只重录一句。
- **`staticFile` 安全判断**：未配音时不得让 Remotion 因引用缺失文件而渲染失败，
  所以 `data.ts` 的 `hasAudioFor()` 会先查 `window.remotion_staticFiles`。

### 踩过的坑（都有记录）

| 现象 | 根因 | 处理 |
| --- | --- | --- |
| `openai-whisper` 构建失败，提示缺 `pkg_resources` | setuptools 84 已移除该模块，PyPI 上也没有独立包 | 降 setuptools 到 `<81`，再用 `--no-build-isolation` 装 |
| 拼接时报 `unknown format: 3` | torchaudio 默认按张量 dtype 存 float32 WAV，Python `wave` 不支持 | 显式 `encoding="PCM_S", bits_per_sample=16` |
| 同一句耗时差 2.4 倍 | 机器负载竞争（Docker/IDEA/Chrome 同时跑） | 把 `--threads` 从默认 16 降到 8，并尽量关掉抢 CPU 的程序 |
| 术语没被注音（`go.mod` 原样输出） | 剥 markdown 时把内容保护成占位符，却到函数末尾才还原，术语表看到的是占位符 | 每阶段结束调用 `restore()` |
| 替换结果被二次改写（`go 点 M O D`） | lambda 闭包延迟绑定 + 中英补空格规则改了替换文本内部 | 用占位符保护替换结果，lambda 用默认参数绑定 |

### 音色版权

`CosyVoice2-0.5B` **不带内置音色**（没有 `spk2info.pt`，`list_available_spks()` 返回空），
必须提供参考音频做零样本克隆。参考音频决定了成片里的**音色与表达方式**。

- 当前使用**项目作者本人录制**的 `my_voice.wav`，权利干净。
- 不要用仓库自带的 `asset/zero_shot_prompt.wav` 做对外发布 —— 那是别人的声音。
- 克隆质量可客观核对：比较参考与合成的基频中位数，差异应在 ±15% 内。

---

## 3.5 布局是怎么定下来的（可复现）

不靠目测，靠像素测量：

```powershell
python scripts\video\set-layout.py          # 写入布局
python scripts\video\build-storyboards.py   # 重新生成分镜
python scripts\video\analyse-layout.py      # 渲染 24 个探针帧并测量
```

`analyse-layout.py` 对每个场景取 3 个进度点（0.30 / 0.50 / 0.72），
统计**内容包围盒**与**最大连续空白带**（只在内容区内统计，
排除场景角标、字幕、底部进度线这些贴边框架元素）。

### 本轮调整结果（调前 → 调后，内容纵向占比，越高越实）

| 场景 | 调前 | 调后 |
| --- | --- | --- |
| S1 定位 | 0.67 | **0.79** |
| S2 倒排索引 | 0.56 | **0.74** |
| S3 BM25 | 0.73 | **0.78** |
| S4 短语查询 | 0.62 | 0.65 |
| S5 布尔与并发 | 0.64 | 0.67 |
| S6 工程纪律 | 0.69 | **0.79** |

三处具体改动：

1. **框高改为内容匹配**。原来按"均分行"给高度，导致稀疏镜头（不变量表、
   覆盖率、柱状对比）内部出现 300–500px 死区。改为按内容行数给高度。
2. **入场动画不再从 opacity 0 起**。这是最值得记的一处：Remotion 在每个镜头的
   **第 0 帧**就会渲染，而所有入场动画都从 0 拉起，于是**每个镜头的首帧是全空白**。
   实测在 S2 的镜头切换处抓到了 855px 空白带。现在统一走
   `src/anim.ts` 的 `entranceOpacity`，从 0.3 基线拉满，任何一帧都有内容。
3. **三面板入场递进从 0.18s 降到 0.10s**。原来三栏按 0.18s 错峰出现，
   在 21 秒的镜头里要等 0.36 秒才凑齐；加上逐块 0.45s 的生长，
   镜头前段会长时间只有一两栏。

【风险】S4 与 S5 的剩余空白（约 600px / 460px）主要是**单镜头帧的构图留白**，
以及探针恰好落在镜头切换点；不是布局错误。若要进一步压平，
需要给同一场景的多个镜头做连续的舞台调度（而不是整屏切换），
那是剪辑层面的改动，不属于坐标微调。

---

## 3. 数据流（这是全工程的核心设计）

```
video-script.md  ──抽旁白/时长/镜头──┐
                                     ├──► storyboards.json ──► Remotion 渲染
demo-cases.md    ──实测输出快照─────┤         │
仓库真实源码      ──代码切片快照─────┘         │
                                              └─ 同时镜像到 src/storyboards.json
```

`build-storyboards.py` 做四件事，每一件都是为了**让画面不可能偏离事实**：

1. **旁白逐字提取**：从 `video-script.md` 的「### 旁白全文」块原样取出，
   不做任何改写（已验证 8 个场景的首行与末行都与原文完全一致）。
2. **数据源快照**：把每个镜头引用的文件读进 `resolved`（含行范围切片）。
   渲染期**不访问文件系统**，因此不可能渲染出仓库里不存在的代码。
   引用了不存在的文件 → **构建失败**，而不是静默渲染空白。
3. **标题锚点校验**：校验每个 `section`（含 `#` 前缀的精确标题）在目标文件中存在，
   并把命中行号记入 `anchors`。写错标题 → **构建失败**。
   （这条是踩过坑才加的：早期用模糊子串匹配，把 `d1` 匹配到了语料表格里，
   于是 S3 画面显示的是完全无关的段落。）
4. **时长推导**：按旁白字数 ÷ 4.05 字/秒 得到"播报预算"，与该场景的编辑时长
   加权混合（6:4），最后把舍入误差吸收到最长场景，使总长**精确等于 480 秒**。

`renderPolicy.forbidden` 把 `video-script.md` 附录 B 的「不得出镜」清单
写进了 JSON，任何审查者（人或模型）都能机械地对照。

---

## 4. 常用命令

```powershell
# 0) 前置：ffmpeg 已在 D:\ffmpeg（remotion.config.ts 会自己加到 PATH）

# 1) 改了 video-script.md 或源文件后，重新生成分镜
python scripts\video\build-storyboards.py
#    预期输出：scenes=8  total=480s (8:00)  resolved sources=N  validated anchors=N

# 2) 进工程目录
cd docs\video\remotion

# 3) 类型检查（改过 TS 后必跑）
npx tsc --noEmit

# 4) 可视化编辑（浏览器打开 Studio，逐场景预览）
npx remotion studio

# 5) 抽单帧核对（--frame 是绝对帧号，30fps）
npx remotion still Episode1 out\check.png --frame 5910

# 6) 快速预览（半分辨率，编得快）
npx remotion render Episode1 out\episode1-preview.mp4 --scale 0.5 --codec h264

# 7) 正式出片（1920x1080 / 30fps / CRF 18）
npx remotion render Episode1 out\episode1.mp4 --codec h264
```

### 场景帧号对照表（30fps，来自 `storyboards.json`）

| 场景 | 时间 | 起始帧 | 结束帧 | 中点（抽帧核对用） |
| --- | --- | --- | --- | --- |
| S0 | 0:00–0:31 | 0 | 930 | 465 |
| S1 | 0:31–1:19 | 930 | 2370 | 1650 |
| S2 | 1:19–2:43 | 2370 | 4890 | 3630 |
| S3 | 2:43–3:51 | 4890 | 6930 | **5910** |
| S4 | 3:51–5:29 | 6930 | 9870 | **8400** |
| S5 | 5:29–6:44 | 9870 | 12120 | **10995** |
| S6 | 6:44–7:33 | 12120 | 13590 | **12855** |
| S7 | 7:33–8:00 | 13590 | 14400 | **13995** |

---

## 5. 配音怎么接

脚本的旁白是纯文本，可直接交给任一中文 TTS。

1. 从 `storyboards.json` 取 `scenes[].narration`（`\n` 分隔，一行为一屏字幕）。
2. 每场景单独合成一个音频文件，命名 `S0.wav` … `S7.wav`，放进 `public/audio/`。
3. **以真实音频时长为准修正 `durationSec`**：
   当前时长是按 4.05 字/秒推算的，真人或 TTS 的语速会有偏差。
   改 `build-storyboards.py` 顶部的 `CHARS_PER_SEC`，或直接在 JSON 里改
   `scenes[].durationSec` 后重算总长（`retime()` 会把总长重新压到 480 秒）。
4. 把 `Root.tsx` 的 `Caption` 换成 `<Audio src={staticFile(`audio/${scene.id}.wav`)} />`
   并让字幕跟随音频时间轴。

> 中英混排处建议在 TTS 里标注英文按字母读（`go.mod`、`BM25`、`PostingList`）。

---

## 6. 必须遵守的三条纪律

1. **不改画面上的数字。** 终端与代码镜头的内容来自 `resolved` 快照，
   改数字只能改 `demo-cases.md`，而那份文件里的每条输出都对应一次真实运行。
2. **不给未验证的内容加画面。** `renderPolicy.forbidden` 里的六条是硬约束，
   新增镜头前先读 `video-script.md` 附录 B。
3. **改了源文件就重跑 `build-storyboards.py`。**
   代码切片是构建时快照，源码变了不会自动同步。

---

## 7. 已知待办

- ~~**布局微调**~~：已完成第一轮（见第 3.5 节，含调前/调后数据）。
  进一步的压缩需要舞台调度而非坐标调整。
- **动画升级**：三个 `type=manim` 的动画目前是**代码内联实现**（见 `shots/Manim.tsx`），
  可平滑替换为 Manim 预渲染片段（`<OffthreadVideo src={staticFile(...)} />`），
  参数无需改动。
- **字幕**：当前按旁白行均分时间轮播；接入真实配音后应改为跟随音频。
- **空镜**：S7 收尾可考虑加 3–5 秒气氛镜头；如使用文生视频，须核对不含任何数字。
- **配乐与混音**：尚未涉及。

---

## 8. 环境注意事项

### 8.1 渲染必须把临时目录指向工程内（否则 Permission denied）

在本机（DSH 文件沙箱环境）直接 render 会失败：

```
Error opening output C:\Users\...\Temp\react-motion-render...\pre-encode.mp4: Permission denied
```

原因：Remotion 把 ffmpeg 的预编码中间文件写到**系统临时目录**，而该路径不在可写范围内。
必须把 `TEMP`/`TMP` 指到工程目录内：

```powershell
cd docs\video\remotion
New-Item -ItemType Directory -Force -Path .tmp | Out-Null
$env:TEMP = (Resolve-Path .tmp).Path
$env:TMP  = (Resolve-Path .tmp).Path
$env:PATH = "D:\ffmpeg\bin;$env:PATH"
npx remotion render Episode1 out/episode1-preview.mp4 --scale 0.5 --codec h264
```

`still` 抽帧命令不受影响（不走 ffmpeg 预编码），无需改 `TEMP`。

### 8.2 其他

- **首次渲染会下载 Chrome Headless Shell（约 102 MB）**，需要网络；之后走缓存。
- **Bundling 约 24 秒**（首次），之后有缓存快很多。
- **渲染耗时实测**：半分辨率（960×540）全片 **4.6 分钟**。
  全分辨率 1080p 预计 15–25 分钟（CPU 编码，无 CUDA）。
- **产物体积实测**：半分辨率 21.8 MB / 480.04 秒 / 14400 帧 / H.264 + AAC。
- 工程目录含 `node_modules`（**738.7 MB**）与 `out/`（渲染产物），
  已在 `remotion/.gitignore` 中忽略，不要提交进仓库。

---

## 9. 已验证的端到端状态

本次已实跑通过，可作为基线：

| 步骤 | 命令 | 结果 |
| --- | --- | --- |
| 生成分镜 | `python scripts/video/build-storyboards.py` | 8 场景 / 480s / 9 个数据源快照 / 10 个标题锚点校验通过 |
| 布局写入 | `python scripts/video/set-layout.py` | 26 个 region 写入，全部在安全区内 |
| 布局测量 | `python scripts/video/analyse-layout.py` | 24 个探针帧，内容纵向占比 0.65–0.79（S0/S7 浅底场景为满幅） |
| 类型检查 | `npx tsc --noEmit` | 退出码 0 |
| 组合识别 | `npx remotion compositions` | `Episode1` 30fps 1920x1080 14400 帧（480.00 秒）；`DataProbe` |
| 抽帧核对 | `npx remotion still Episode1 out/sN.png --frame N` | 各场景均渲染成功，中文与代码显示正常 |
| 全片渲染 | `npx remotion render Episode1 ... --scale 0.5` | 退出码 0，14400 帧，480.04 秒，22.7 MB（约 5.2 分钟） |
| Go 侧回归 | `go build ./...` | 退出码 0，业务代码未被改动 |

**尚未完成**：旁白配音（`audio/` 为空，字幕按时间均分）、配乐。

