# go_tsh

用 Go 从零实现的全文搜索服务：**单二进制、零第三方依赖、内存倒排索引、HTTP JSON API**。

> 当前进度：**Phase 4（HTTP 接口）已完成**，下一步 Phase 5（工程化交付）。
> 10 万篇规模下检索 P99 < 20ms；`make check`（含 `-race`）全绿。
> 完整的项目规划、选型理由与分阶段 TODO 见 [PLAN.md](PLAN.md)。

## 特性（目标）

- 手写倒排索引，posting list 带 token 位置信息，支持短语查询
- BM25 相关性排序（k1 = 1.2，b = 0.75）+ 小顶堆 Top-K
- 布尔查询：`AND` / `OR` / `NOT`，以及 `-term` 排除语法
- 文档增删改查、高亮片段
- 标准库实现：`net/http`、`log/slog`、`encoding/json`——`go.mod` 无任何外部依赖

## 快速开始

本机 Go 未加入 PATH 时，用绝对路径调用即可（本文档以 `D:\Program Files\Go\bin\go.exe` 为例）。

```powershell
# 编译
& "D:\Program Files\Go\bin\go.exe" build -o bin/tshd.exe ./cmd/tshd

# 启动
./bin/tshd.exe -addr :8080 -log-level debug
```

```powershell
# 存活探针
curl.exe --noproxy "*" http://127.0.0.1:8080/healthz
# {"status":"ok"}

# 索引统计
curl.exe --noproxy "*" http://127.0.0.1:8080/api/v1/stats
# {"docs":0,"terms":0,"avg_doc_len":0,"index_bytes":0}
```

## 配置

优先级：**命令行参数 > 环境变量 > 默认值**。环境变量统一为 `TSH_` 前缀。

| 参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-addr` | `TSH_ADDR` | `:8080` | HTTP 监听地址 |
| `-log-level` | `TSH_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `-max-body-bytes` | `TSH_MAX_BODY_BYTES` | `8388608` | 单请求体字节上限（8 MiB） |
| `-max-doc-fields` | `TSH_MAX_DOC_FIELDS` | `32` | 单文档字段数上限 |
| `-max-doc-tokens` | `TSH_MAX_DOC_TOKENS` | `100000` | 单文档 token 数上限 |
| `-max-query-terms` | `TSH_MAX_QUERY_TERMS` | `32` | 单次查询 term 数上限 |
| `-query-timeout` | `TSH_QUERY_TIMEOUT` | `5s` | 单次查询超时 |
| `-read-timeout` | `TSH_READ_TIMEOUT` | `10s` | HTTP 读超时 |
| `-write-timeout` | `TSH_WRITE_TIMEOUT` | `30s` | HTTP 写超时 |
| `-idle-timeout` | `TSH_IDLE_TIMEOUT` | `60s` | HTTP 空闲连接超时 |
| `-shutdown-grace` | `TSH_SHUTDOWN_GRACE` | `10s` | 优雅关闭等待上限 |

## HTTP 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/healthz` | 存活探针 |
| `GET` | `/api/v1/stats` | 索引规模统计 |
| `POST` | `/api/v1/documents` | 新建文档，**已存在返回 409** |
| `PUT` | `/api/v1/documents/{id}` | 覆盖式写入：新建 201 / 覆盖 200 |
| `GET` | `/api/v1/documents/{id}` | 取原文 |
| `DELETE` | `/api/v1/documents/{id}` | 删除，成功返回 204 |
| `GET` | `/api/v1/search` | 检索 |

### 写入文档

```powershell
curl.exe --noproxy "*" -X POST http://127.0.0.1:8080/api/v1/documents `
  -H "Content-Type: application/json" `
  --data-binary "@doc.json"    # 见下方「为什么用文件传 body」
```

```json
{ "id": "doc-1", "fields": { "title": "Inverted index", "body": "..." } }
```

`POST` 与 `PUT` 的语义**刻意分开**：`POST` 撞 ID 返回 `409`，
让「不小心覆盖了别人的文档」变成一个明确的错误；
`PUT` 才是覆盖语义，且是**整体替换**——旧版本独有的词条会被完整摘除。

未知字段（例如把 `fields` 拼成 `field`）会返回 `400`，
而不是静默忽略后写入一篇空文档。

### 检索

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `q` | 必填 | 查询串 |
| `limit` | 10 | 返回条数，上限 100 |
| `offset` | 0 | 跳过条数 |
| `field` | 全部 | 限定字段，**可重复出现** |
| `highlight` | false | 是否返回高亮片段，只接受 `true/false/1/0` |

查询语法：`quick brown`（默认 OR）、`"inverted index"`（短语）、
`-excluded` 或 `NOT excluded`、`a AND b`、`(a OR b) AND c`。

```json
{
  "took_ms": 0,
  "total": 2,
  "hits": [
    {
      "id": "doc-1",
      "score": 2.81,
      "fields": { "title": "Inverted index" },
      "highlights": { "title": "\u003cem\u003eInverted\u003c/em\u003e index" }
    }
  ]
}
```

> **注意 `highlights` 里的 `\u003c`**：这是 Go `encoding/json` 的默认行为，
> 它会把 `<` `>` `&` 转义掉。JSON 解析后拿到的就是正常的 `<em>`。
> 这是**刻意保留**的：文档字段内容由调用方提供，
> 不转义的话，把响应直接嵌进 HTML 页面就会变成 XSS 入口。

### 错误响应

统一为：

```json
{ "error": { "code": "INVALID_QUERY", "message": "..." } }
```

| 状态码 | `code` | 场景 |
| --- | --- | --- |
| 400 | `BAD_REQUEST` | 请求体非法、参数非整数、字段数或 token 数超限 |
| 400 | `INVALID_QUERY` | 查询串为空或语法错误 |
| 404 | `NOT_FOUND` | 文档不存在 |
| 409 | `CONFLICT` | `POST` 的 ID 已存在 |
| 413 | `PAYLOAD_TOO_LARGE` | 请求体超过上限 |
| 500 | `INTERNAL` | 服务端故障（细节只记日志，不外泄） |

## 开发

```powershell
# Windows：一键门禁（格式 + vet + 竞态测试）
pwsh -File scripts/check.ps1 -GoExe "D:\Program Files\Go\bin\go.exe"

# 或者用 make（需要 Unix shell）
make check
```

## 代码结构

```
cmd/tshd/           # 守护进程入口：配置装配、日志、优雅关闭
internal/config/    # 配置解析（flag + env）
internal/httpapi/   # HTTP 路由、中间件、DTO、错误映射 ✅ 全部端点
internal/analyzer/  # 文本分析 ✅ StandardAnalyzer + 内置停用词表
internal/index/     # 倒排索引 ✅ InvertedIndex + View / PostingCursor
internal/query/     # 查询 AST、解析器与执行器（归并求值 + BM25）✅
internal/scoring/   # BM25 与 Top-K ✅
internal/highlight/ # 高亮片段（HTML 转义 + 窗口截断）✅
pkg/tsh/            # 对外门面 Engine ✅ 文档读写 + 检索
```

依赖方向单向向内：`httpapi → tsh → {analyzer, query, index, scoring}`。

## 故障排查

### 为什么用文件传 body：PowerShell 会吃掉 JSON 里的双引号

在 Windows PowerShell 5.1 里这样调 curl 是不行的：

```powershell
curl.exe -d '{"id":"doc-1"}' http://127.0.0.1:8080/api/v1/documents
# 服务端收到的是 {id:doc-1} —— 双引号没了
# → BAD_REQUEST: invalid character 'i' looking for beginning of object key string
```

PowerShell 把参数拼成命令行时会剥掉内嵌的双引号，这是它调用原生命令的经典缺陷。
绕开的方式是写进文件再让 curl 读：

```powershell
Set-Content -Path doc.json -Value '{"id":"doc-1","fields":{"title":"..."}}' -Encoding ascii -NoNewline
curl.exe --noproxy "*" -X POST -H "Content-Type: application/json" `
  --data-binary "@doc.json" http://127.0.0.1:8080/api/v1/documents
```

用 `-Encoding ascii`（或 UTF-8 无 BOM）很重要：PS 5.1 的 `-Encoding UTF8`
会写入 BOM，服务端会把它当成 JSON 的第一个字符而报错。

### 不要把 PowerShell 函数命名为 `Curl`

PowerShell 的命令解析优先级是 **Alias > Function > Cmdlet > Application**，
而 `curl` 是 `Invoke-WebRequest` 的内置别名。定义一个叫 `Curl` 的函数会被别名截胡，
报出莫名其妙的 `Invoke-WebRequest: A positional parameter cannot be found`。

要么换个函数名，要么始终写 `curl.exe`（带扩展名可以绕过别名）。

### `workspace-write` 沙箱初始化失败：`SetNamedSecurityInfoW failed (Win32 5)`

**现象**：在 DSH 中以 `workspace-write` 模式执行任何命令，都在沙箱准备阶段报
`grantWrite(<workspace>)` 失败，只有 `danger-full-access` 可用。

**根因**：DSH 沙箱用 **write-restricted token** 运行命令。这类令牌会绕过 NTFS
「对象所有者隐式拥有 `READ_CONTROL` + `WRITE_DAC`」的规则，强制所有写访问
（含改 DACL）必须由 ACL **显式**授权。而工作区默认继承来的
`Authenticated Users:(M)` 与 `Users:(RX)` 都不含 `WRITE_DAC`。

**修复**（无需管理员——你是目录所有者）：

```powershell
icacls "D:\codes\golang\go_tsh" /grant "%USERNAME%:(OI)(CI)F"
```

等价写法：

```powershell
$me  = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$acl = Get-Acl 'D:\codes\golang\go_tsh'
$acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new(
    $me, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow'))
Set-Acl -Path 'D:\codes\golang\go_tsh' -AclObject $acl
```

**回滚**：`icacls "D:\codes\golang\go_tsh" /remove:g "%USERNAME%"`

### `Get-Content` 读日志乱码

Windows PowerShell 5.1 的 `Get-Content` 默认按 ANSI（简体中文下为 GBK）解码，
读 Go 输出的 UTF-8 日志会显示成乱码（`服务启动` → `鏈嶅姟鍚姩`）。
加 `-Encoding utf8` 即可，日志文件本身的字节是正确的。

### `scripts/check.ps1` 报 `Unexpected token '}'`

该脚本必须保持**纯 ASCII**。PS 5.1 会把无 BOM 的 UTF-8 `.ps1` 按 GBK 解析，
中文注释被打碎后会直接破坏语法。若要写中文，必须存为 UTF-8 **带 BOM**。

### 启用 `-race`（竞态检测）

竞态检测依赖 cgo，而 Go 在 Windows 上的 cgo 走 MinGW-w64 路线，
必须是 **GCC 风格**的驱动（官方 LLVM 发布版 target 是 MSVC，**装了对 cgo 没用**）。

推荐用 MSYS2 装 GCC：

```powershell
# 在 MSYS2 的 UCRT64 shell 里
pacman -S mingw-w64-ucrt-x86_64-gcc
```

装好后工具链在 `C:\msys64\ucrt64\bin`。**两个坑**：

1. MSYS2 的工具链**不在 Windows PATH 上**（MSYS2 shell 自己会设），
   所以 cmd 里敲 `gcc` 无效。需要自己加：

   ```powershell
   [Environment]::SetEnvironmentVariable('Path',
       ([Environment]::GetEnvironmentVariable('Path','User').TrimEnd(';') + ';C:\msys64\ucrt64\bin'),
       'User')
   ```

2. ⚠️ **绝不能把 `C:\msys64\usr\bin` 加进 PATH**。那里有 MSYS2 自己的
   `link.exe` / `find.exe` / `sort.exe` / `sh.exe`，会遮蔽 Windows 原生命令
   并破坏无关的构建。只需加 `ucrt64\bin`（或 `mingw64\bin`）。

`scripts/check.ps1` **会自动探测** `ucrt64` / `mingw64` / `clang64` / `mingw32`
四个变体目录，找到就临时加进 PATH 并设 `CGO_ENABLED=1`，
因此项目不依赖你的 PATH 配置。若确实找不到 C 编译器，
它会自动降级为普通 `go test` 并明确告警——**不会假装通过**。

### 沙箱内 `go build` 报 `Access is denied`（GOCACHE）

```
open C:\Users\<user>\AppData\Local\go-build\...: Access is denied
```

文件系统沙箱（`workspace-write`）**只放行工作区**，而 Go 的构建缓存默认在
工作区之外。注意这**不是 ACL 问题**——实测给缓存目录补写显式 FullControl
ACE 完全无效，DSH 沙箱按路径白名单判定，而非按 ACL。

`scripts/check.ps1` 会自动处理：它**实际探测**默认缓存能否写入
（建一个探针文件再删掉。ACL、受限令牌、只读挂载在 `Test-Path` 下长得一模一样，
只有真写一次才能区分），不可写就回退到 `<repo>/.gocache/`。
沙箱外行为不变。

手动执行 `go` 命令时同理：

```powershell
$env:GOCACHE = "$PWD\.gocache"
```

### 沙箱内 `go` 警告模块缓存不可写

```
writing stat cache: open C:\Users\<user>\go\pkg\mod\cache\...: Access is denied
```

本项目零第三方依赖，构建与测试都不受影响，属噪音级警告。
若将来引入依赖，把 `GOMODCACHE` 同样指向工作区内。
