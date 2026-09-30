# go_tsh

用 Go 从零实现的全文搜索服务：**单二进制、零第三方依赖、内存倒排索引、HTTP JSON API**。

> 当前进度：**Phase 2（倒排索引内核）已完成**，下一步 Phase 3（查询与打分）。
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

| 方法 | 路径 | 状态 |
| --- | --- | --- |
| `GET` | `/healthz` | ✅ 已实现 |
| `GET` | `/api/v1/stats` | ✅ 已实现 |
| `POST` | `/api/v1/documents` | ⬜ Phase 4 |
| `PUT` | `/api/v1/documents/{id}` | ⬜ Phase 4 |
| `DELETE` | `/api/v1/documents/{id}` | ⬜ Phase 4 |
| `GET` | `/api/v1/documents/{id}` | ⬜ Phase 4 |
| `GET` | `/api/v1/search` | ⬜ Phase 4 |

失败响应统一为：

```json
{ "error": { "code": "INVALID_QUERY", "message": "..." } }
```

## 开发

```powershell
# Windows：一键门禁（格式 + vet + 竞态测试）
pwsh -File scripts/check.ps1 -GoExe "D:\Program Files\Go\bin\go.exe"

# 或者用 make（需要 Unix shell）
make check
```

## 代码结构

```
cmd/tshd/          # 守护进程入口：配置装配、日志、优雅关闭
internal/config/   # 配置解析（flag + env）
internal/httpapi/  # HTTP 路由、中间件、DTO、错误映射
internal/analyzer/ # 文本分析 ✅ StandardAnalyzer + 内置停用词表
internal/index/    # 倒排索引 ✅ InvertedIndex + posting list
internal/query/    # 查询 AST 与解析（Phase 3）
internal/scoring/  # BM25 与 Top-K（Phase 3）
pkg/tsh/           # 对外门面 Engine
```

依赖方向单向向内：`httpapi → tsh → {analyzer, query, index, scoring}`。

## 故障排查

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

### `SKIP -race (needs cgo + a C compiler)`

竞态检测依赖 cgo。本机 `CGO_ENABLED=0` 且没有 C 编译器，因此
`check.ps1` 自动降级为普通 `go test` 并明确告警——**不会假装通过**。
安装 mingw-w64 后设置 `CGO_ENABLED=1` 即可启用。

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
