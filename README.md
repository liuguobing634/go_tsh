# go_tsh

用 Go 从零实现的全文搜索服务：**单二进制、零第三方依赖、内存倒排索引、HTTP JSON API**。

> 当前进度：**Phase 0（骨架与工具链）**。
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
internal/analyzer/ # 文本分析（Phase 1）
internal/index/    # 倒排索引（Phase 2）
internal/query/    # 查询 AST 与解析（Phase 3）
internal/scoring/  # BM25 与 Top-K（Phase 3）
pkg/tsh/           # 对外门面 Engine
```

依赖方向单向向内：`httpapi → tsh → {analyzer, query, index, scoring}`。
