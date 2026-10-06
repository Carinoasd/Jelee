# 日志（G46）

本文说明 Jelee 的运行日志、访问日志、审计日志的输出位置、配置、脱敏规则、保留与查询方式，以及尚未完成的部分。内容按 2026-10-06 的代码核对：`internal/platform/logging/`、`internal/platform/config/logging.go`、`internal/adapter/http/server.go`（`boundary`）、`internal/adapter/postgres/audit.go`、`internal/diag/`。日志与追踪的串联（traceId、spanId、采样、Loki 查询示例）见[可观测性](observability.md)；诊断命令与运维错误码见[故障排查](troubleshooting.md)。

## 三类记录

| 类别 | 去向 | 内容 | 能否被日志级别关闭 |
| --- | --- | --- | --- |
| 运行日志 | stdout 和／或日志文件（JSON） | 组件事件、错误码、耗时、追踪 ID；只允许白名单字段 | 可以，按全局或组件级别 |
| HTTP 访问日志 | 同上，组件 `http` | 每个请求一条 `request completed`：`requestId`、方法、耗时，加上 `traceId`／`spanId`；panic 另写一条 ERROR | 可以（INFO） |
| 审计日志 | PostgreSQL 表 `audit_logs`（只追加） | 管理操作、权限变更、安全事件（登录失败、锁定、开发者模式变更等），含操作者、来源地址、前后状态（已脱敏）、`request_id` | **不能**，与业务变更在同一事务写入 |

## 输出与配置（G46.1、G46.2）

日志使用标准库 `log/slog`。进程启动时按配置打开日志路由，失败时向 stderr 写 `Jelee logging: …` 并退出。配置可以写在 `JELEE_CONFIG` 指向的配置文件的 `"logging"` 段，环境变量覆盖配置文件：

| 环境变量 | 配置文件键 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `JELEE_LOG_LEVEL` | `level` | `info` | `debug`、`info`、`warn`（`warning`）、`error`；默认不会是 DEBUG |
| `JELEE_LOG_COMPONENTS` | `components` | 空 | 按组件设置级别，如 `scan=debug,http=warn`；设置后整体替换配置文件中的值 |
| `JELEE_LOG_FORMAT` | `format` | `json` | `json` 或 `console`（`time LEVEL msg k=v`，便于开发时阅读）；只影响 stdout，文件始终是 JSON |
| `JELEE_LOG_OUTPUT` | `output` | `stdout` | `stdout`（容器部署）、`file` 或 `both` |
| `JELEE_LOG_FILE` | `file.path` | 空 | 日志文件的绝对路径；`output` 为 `file` 或 `both` 时必填 |
| `JELEE_LOG_MAX_SIZE_MB` | `file.maxSizeMB` | 100 | 0–10240，0 表示不按大小轮转 |
| `JELEE_LOG_ROTATE_HOURS` | `file.rotateHours` | 24 | 0–744，0 表示不按时间轮转 |
| `JELEE_LOG_MAX_BACKUPS` | `file.maxBackups` | 7 | 0–1000，保留的轮转备份数 |
| `JELEE_LOG_COMPRESS` | `file.compress` | `true` | 轮转备份是否 gzip 压缩 |
| `JELEE_LOG_BUFFER_ENTRIES` | `bufferEntries` | 4096 | 0–1048576，每个输出的异步队列长度 |
| `JELEE_LOG_IP_MODE` | `ipMode` | `redact` | `redact` 删除客户端 IP；`mask` 保留 IPv4 /24 或 IPv6 /48 |
| `JELEE_LOG_PATH_MODE` | `pathMode` | `redact` | `redact` 删除路径；`relative` 对位于 `pathRoots` 下的路径只输出相对路径 |
| `JELEE_LOG_PATH_ROOTS` | `pathRoots` | 空 | 用系统路径分隔符分隔的绝对路径，最多 64 个 |
| `JELEE_TRACE_SAMPLE_RATE` | `traceSampleRate` | 0.1 | 0–1，只影响 span 记录，普通日志从不采样 |

配置校验失败时，错误信息不包含配置值。

组件：`http`、`auth`、`access`、`scan`、`probe`、`nfo`、`images`、`jobs`、`webhook`、`compat`、`media`、`db`、`gc`；`ignore` 是 `scan` 的别名，`metrics` 是 `db` 的别名。

日志文件按大小和／或时间轮转，备份文件名形如 `<名称>-20261006T120000.000000000Z.000<扩展名>`，文件权限 0600、目录 0700；只保留 `maxBackups` 个备份。文件位置登记在[存储布局](storage-layout.md)。

**背压**：每个输出前都有一个有界异步队列，队列满时直接丢弃并计数，不会阻塞请求。

## 脱敏（G46.5）

运行日志采用**白名单**：

- 只有少数键可以原样输出，而且值也要通过校验：时间、级别、消息、`status`／`durationMs`／`count`（只接受数值）、`component`（必须是已知组件）、`traceId`／`spanId`（固定长度十六进制）、`requestId`、`method`（只接受已知 HTTP 方法）、任务 `state`、少数固定的 `code` 等。**其他任何键的值一律写成 `[redacted]`**。
- 客户端 IP 只认 `clientIp` 键，按 `ipMode` 删除或掩码；路径只认 `path` 键，按 `pathMode` 删除或改为相对路径。
- 消息必须是固定文案；含 URL、`=`、路径或长令牌的消息会被替换。不安全的键名本身也会被改名。

因此运行日志中不会出现令牌、密码、Cookie、`Authorization` 头、数据库连接串、查询字符串、请求体或完整路径。HTTP 访问日志也不记录路径、状态码与客户端 IP。

审计日志的前后状态另有脱敏：键名为 `authorization`、`cookie`、`dsn`、`credential`、`privatekey` 等，或以 `password`、`token`、`tokenhash`、`secret`、`apikey` 结尾的值会被替换；以 `$argon2` 开头的值同样替换；单个状态超过 32 KiB 时改存大小与 SHA-256。

守门测试：`internal/platform/logging/` 的 `TestSensitiveSampleScanFindsNothing`（注入连接串、令牌等样本后扫描输出）、`TestRedaction`、`TestGroupedAttributesCannotExposeSensitiveValues`、`TestIPMaskOption`、`TestRelativePathOption`、`TestMessageAndKeyGuards`、`TestTraceFieldWhitelist`、`TestDebugIsOffByDefault`。

## 请求 ID 与追踪（G46.4、G46.6）

- 每个请求由服务端生成 16 字节随机数作为请求 ID，写入响应头 `X-Request-ID`。服务端**不信任**入站的 `X-Request-ID` 与 `traceparent`。
- 请求 ID 同时是该请求根 span 的 `traceId`；用这个上下文写的每条日志自动带 `traceId`／`spanId`。
- 错误响应的 `error.traceId` 与审计行的 `request_id` 是同一个值。用户报告错误时，用响应中的 `traceId` 即可在日志与审计中找到对应记录。

详见[可观测性](observability.md)。

## 审计日志（G46.3、G46.9）

- 表 `audit_logs`（迁移 000001 建立，000002、000060 扩展）：`category`（`audit` 或 `security`）、事件名、操作者与来源地址、目标、前后状态、`request_id`、时间。
- **只追加**：触发器拒绝 UPDATE 与 TRUNCATE，写入时强制使用数据库时间；DELETE 只允许在 `purge_audit_logs()` 中删除超过保留期的行。
- 事件名是封闭集合（`internal/adapter/postgres/audit.go` 的 `auditEvents`），未知事件直接拒绝；安全类事件会强制保留对应的追踪记录。
- 保留期存在表 `audit_retention`：`audit_days`、`security_days`，默认都是 365 天，范围 7–36500。
- 查询：目前只有分享链接的访问记录可以通过 `GET /api/v1/shares/{id}/access` 查询（管理员）；其他审计事件只能直接查询数据库或通过 `pg_dump` 获取。审计不进入元数据导出，见[备份与还原](backup-restore.md)。

## 诊断导出与查询（G46.7）

- `jelee-cli doctor`：检查配置、数据库、目录、外部工具等；输出前先做敏感信息扫描，命中则不打印并报 `doctor_output_unsafe`。
- `jelee-cli diag export --out <文件>.zip [--since 24h] [--max-log-bytes …]`：生成诊断包。`--since` 默认 24 小时、最长 30 天；`--max-log-bytes` 默认 1 MiB、最大 16 MiB。只读取当前日志文件（不读轮转备份），并对每一行重新应用白名单脱敏；没有配置日志文件时在清单中标记 `no_log_file_configured`。
- Loki 等日志系统的查询示例见[可观测性](observability.md)。

## 开发者模式下的日志

开发者模式（见[开发者模式](developer-mode.md)）提供三个日志相关开关：

| 开关 | 效果 |
| --- | --- |
| `debug_verbose_logging` | 全局级别临时改为 DEBUG，关闭或会话结束时恢复配置值 |
| `debug_sql_logging` | 每条 SQL 写一行：语句文本（压成一行、最多 512 字节，不含参数）、耗时、行数、是否失败 |
| `debug_body_logging`（危险开关） | 每个请求写一行：路由样式、状态码、JSON 请求体与响应体各最多 4 KiB，敏感键名的值替换为 `[redacted]` |

**已知问题**：`debug_sql_logging` 与 `debug_body_logging` 写出的 `statement`、`durationMicros`、`rows`、`failed`、`route`、`requestBody`、`responseBody` 等字段，以及它们的 `code`（`devmode_sql_log`、`devmode_body_log`），都不在上文的白名单中。经过正式的日志路由输出时，这些值会被写成 `[redacted]`，两个开关目前实际上看不到内容。现有测试使用普通的 JSON handler，没有覆盖这种组合。修正需要在白名单中为开发者模式会话放行这些字段（读代码得出，尚未实测确认）。

## 保留与清理（G46.9）

| 记录 | 保留方式 |
| --- | --- |
| 日志文件 | `JELEE_LOG_MAX_BACKUPS`、`JELEE_LOG_MAX_SIZE_MB`、`JELEE_LOG_ROTATE_HOURS`、`JELEE_LOG_COMPRESS`；stdout 由容器运行时或日志系统管理 |
| 审计日志 | 表 `audit_retention`（默认 365 天） |
| 任务历史 | `JELEE_JOB_HISTORY_LIMIT`（默认 20，范围 1–100），超出的已结束任务被删除；任务没有独立的日志存储 |
| Webhook 投递记录 | `JELEE_WEBHOOK_RETENTION_DAYS`（默认 14） |

## 尚未完成

- 审计保留期的设置与清理函数（`SetAuditRetention`、`PurgeAudit`）已经实现，但没有路由、CLI 或定时任务调用，审计日志目前不会被自动清理。
- 没有通用的日志或审计查询接口，也没有 `jelee-cli logs tail/filter/export`（G46.7、G49.6）。
- 没有运行期调整日志级别的 HTTP 或 CLI 接口；只有开发者模式的 `debug_verbose_logging` 会临时改变级别。
- syslog、Loki、OTLP 转发器只定义了类型，尚未实现；请用容器运行时或日志代理收集 stdout 或日志文件。
- 队列丢弃与写入失败的计数尚未接到指标。
- 访问日志不记录状态码、路径、用户与客户端（G46.3 要求的部分字段），慢查询日志也尚未提供。
- 开发者模式日志被白名单涂掉的问题，见上文“已知问题”。
