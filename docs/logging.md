# 日志（G46）

本文说明 Jelee 的运行日志、访问日志、安全日志、慢查询日志、审计日志的输出位置、配置、脱敏规则、保留与查询方式，以及尚未完成的部分。内容按 2026-10-06 的代码核对：`internal/platform/logging/`、`internal/platform/config/logging.go`、`internal/adapter/http/server.go`（`boundary`）、`internal/adapter/http/access_log.go`、`internal/adapter/http/logging_admin.go`、`internal/adapter/postgres/log_settings.go`、`cmd/jelee-cli/logs.go`、`internal/adapter/postgres/audit.go`、`internal/diag/`。日志与追踪的串联（traceId、spanId、采样、Loki 查询示例）见[可观测性](observability.md)；诊断命令与运维错误码见[故障排查](troubleshooting.md)。

## 三类记录

| 类别 | 去向 | 内容 | 能否被日志级别关闭 |
| --- | --- | --- | --- |
| 运行日志 | stdout 和／或日志文件（JSON） | 组件事件、错误码、耗时、追踪 ID；只允许白名单字段 | 可以，按全局或组件级别 |
| HTTP 访问日志 | 同上，组件 `http` | 每个请求一条 `request completed`：方法、路由样式、状态码、耗时、响应字节、`requestId`／`traceId`／`spanId`、`userId`／`clientId`／`deviceId`、媒体请求标记；panic 另写一条 ERROR（含遮罩后的堆栈） | 可以（INFO） |
| 安全日志 | 同上，组件 `security`（强制范围） | 登录失败、账号锁定、登录限速、被屏蔽客户端、越权 403、CSRF 失败、SSRF 拦截、开发者模式变更、试图关闭审计日志 | **不能**调低或关闭 |
| 慢查询日志 | 同上，组件 `db` | 超过阈值的 SQL 语句样板（不含参数与字面值）、耗时、行数、结果 | 可以（WARN） |
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
| `JELEE_DB_SLOW_QUERY_MS` | `slowQueryMs` | 500 | 0–600000，慢查询日志阈值（毫秒），0 关闭 |

配置校验失败时，错误信息不包含配置值。

组件（级别范围）：G46.2 的 13 个 `http`、`auth`、`access`、`scan`、`probe`、`nfo`、`images`、`jobs`、`webhook`、`compat`、`media`、`db`、`gc`，加上 `system`（启动自检、进程生命周期）；另有两个**强制范围** `security` 与 `audit`（见下文 G46.10）。代码中使用的其他组件名是别名，按所属范围过滤，日志中照原样写出：

| 别名 | 范围 |
| --- | --- |
| `ignore` | `scan` |
| `metrics` | `db` |
| `webhooks` | `webhook` |
| `playback`、`watch_stats`、`subtitle_ocr`、`matroska` | `media` |
| `client_control`、`share` | `access` |
| `accounts`、`setup` | `auth` |
| `devmode` | `security` |
| `selfcheck` | `system` |

`JELEE_LOG_COMPONENTS` 或配置文件中给 `audit`、`security`（或别名 `devmode`）设级别会被拒绝启动（`audit and security log levels cannot be configured`）。

### 运行时调整级别（G46.2）

管理员可以不重启地调整全局级别或单个范围的级别：

| 路由 | 说明 |
| --- | --- |
| `GET /api/v1/admin/logging/levels` | 全局与每个范围的配置级别、覆写（含到期时间）与当前生效级别，`production` 与 `debugMaxTtlSeconds` |
| `PUT /api/v1/admin/logging/levels` | 本体 `{"component":"global"|范围|别名,"level":"debug"|"info"|"warn"|"error"|"reset","ttlSeconds":N}`；`reset` 删除覆写，回到配置级别 |

```sh
echo "$ADMIN_TOKEN" | jelee-cli logs level --component scan debug --ttl 30m --token-stdin
echo "$ADMIN_TOKEN" | jelee-cli logs level --component scan reset --token-stdin
echo "$ADMIN_TOKEN" | jelee-cli logs levels --token-stdin
```

- 覆写存在表 `log_settings`（迁移 000085）的 `level_overrides`，所有实例共用：发出请求的实例立即生效，其他实例在 15 秒内读取生效。覆写到期按各实例本机时钟立即失效，数据库不可用时也一样。
- 生效顺序：管理员覆写 > 开发者模式 `debug_verbose_logging`（只影响全局）> 配置级别。
- `ttlSeconds` 为 0 表示一直有效直到 `reset`，最大 86400。**生产环境（`JELEE_ENV=production`）的 DEBUG 必须到期**（G46.8 “DEBUG 不得在生产默认开启”）：不给 `ttlSeconds` 时为 1 小时，超过 4 小时返回 400；其他级别不受限。
- 每次修改写审计 `logging.level_changed`（目标 `log_level:<范围>`，前后覆写），并在 `audit` 范围写一条 WARN `log level changed by an administrator`。
- 对 `audit`、`security` 或别名 `devmode` 的任何修改返回 409 `log_component_mandatory`，写安全类审计 `logging.level_change_refused`，并在安全日志写一条 ERROR（`event=log_disable_refused`）。

日志文件按大小和／或时间轮转，备份文件名形如 `<名称>-20261006T120000.000000000Z.000<扩展名>`，文件权限 0600、目录 0700；只保留 `maxBackups` 个备份。文件位置登记在[存储布局](storage-layout.md)。

**背压**：每个输出前都有一个有界异步队列，队列满时直接丢弃并计数，不会阻塞请求。丢弃与写入失败的计数导出为指标 `jelee_logging_records_dropped_total`、`jelee_logging_write_failures_total`，默认告警 `JeleeLogRecordsDropped`（10 分钟内有丢弃即告警，见 [runbook](runbook.md#jeleelogrecordsdropped)）。

**性能（G46.8）**：`internal/platform/logging/bench_test.go` 的 `BenchmarkLogAccessRecord`、`BenchmarkLogContextFields`、`BenchmarkLogFilteredDebug`、`BenchmarkLogParallel` 与 `internal/adapter/http` 的 `BenchmarkBoundaryRequest`（整条请求边界含访问日志）由 `make bench` 运行、`tools/benchgate` 比较。开发机参考值（AMD Ryzen 7 9850X3D，2026-10-06）：一条访问日志约 2.1 µs、5 次分配；被过滤的 DEBUG 约 6 ns、0 分配；`GET /healthz` 经过边界由约 4.8 µs／51 次分配变为约 5.2 µs／58 次分配（加入状态码、字节数、路由与身份字段）。

## 脱敏（G46.5）

运行日志采用**白名单**：

- 只有少数键可以原样输出，而且值也要通过校验：时间、级别、消息、`status`／`durationMs`／`count`（只接受数值）、`component`（必须是已知组件）、`traceId`／`spanId`（固定长度十六进制）、`requestId`、`method`（只接受已知 HTTP 方法）、任务 `state`、少数固定的 `code` 等。**其他任何键的值一律写成 `[redacted]`**。
- 客户端 IP 只认 `clientIp` 键，按 `ipMode` 删除或掩码；路径只认 `path` 键，按 `pathMode` 删除或改为相对路径。
- 消息必须是固定文案；含 URL、`=`、路径或长令牌的消息会被替换。不安全的键名本身也会被改名。

- 其他白名单字段各有校验：`userId`、`clientId`、`itemId`、`libraryId`、`taskId`、`rule` 只接受 UUID；`jobRunId` 为 `<任务 UUID>:<租约代数>`；`deviceId` 由客户端上报，**只写摘要** `d_` + SHA-256 前 16 位十六进制；`route` 只接受服务端注册过的路由样式（`chi.Walk` 登记）与 `unmatched`；`code` 接受 snake_case 代码（开发者模式代码仍只在开关生效时放行）；`reason`、`toggles`、`expiresAt`、`ttl`、`logLevel`、`scope`、`check`、`status`（自检的 `ok`／`warn`／`fail`）等按固定格式校验。
- 慢查询的 `sql` 与 panic 的 `stack` 只接受 `logging.SQLTemplate`、`logging.PanicStack` 包装的值，同名键下的普通字符串写成 `[redacted]`。

因此运行日志中不会出现令牌、密码、Cookie、`Authorization` 头、数据库连接串、查询字符串、请求体或完整路径。HTTP 访问日志只记录路由样式（如 `/api/v1/items/{id}`），不记录实际路径、查询字符串与客户端 IP。

审计日志的前后状态另有脱敏：键名为 `authorization`、`cookie`、`dsn`、`credential`、`privatekey` 等，或以 `password`、`token`、`tokenhash`、`secret`、`apikey` 结尾的值会被替换；以 `$argon2` 开头的值同样替换；单个状态超过 32 KiB 时改存大小与 SHA-256。

守门测试：`internal/platform/logging/` 的 `TestSensitiveSampleScanFindsNothing`（注入连接串、令牌等样本后扫描输出，包括上述新字段与上下文字段）、`TestComponentFieldsPassTheWhitelist`、`TestRouteFieldNeedsARegisteredPattern`、`TestRedaction`、`TestGroupedAttributesCannotExposeSensitiveValues`、`TestIPMaskOption`、`TestRelativePathOption`、`TestMessageAndKeyGuards`、`TestTraceFieldWhitelist`、`TestDebugIsOffByDefault`。

## 请求 ID 与追踪（G46.4、G46.6）

- 每个请求由服务端生成 16 字节随机数作为请求 ID，写入响应头 `X-Request-ID`。服务端**不信任**入站的 `X-Request-ID` 与 `traceparent`。
- 请求 ID 同时是该请求根 span 的 `traceId`；用这个上下文写的每条日志自动带 `traceId`／`spanId`。
- 错误响应的 `error.traceId` 与审计行的 `request_id` 是同一个值。用户报告错误时，用响应中的 `traceId` 即可在日志与审计中找到对应记录。

### 上下文字段（G46.4）

`domain.WithLogFields` 把字段放进 context，日志路由在写出时自动加上（调用处显式给出的同名字段优先），由该 context 派生、交给其他 goroutine 的 context 同样带上：

| 字段 | 注入位置 |
| --- | --- |
| `userId`、`clientId`（会话 ID）、`deviceId`（摘要） | 认证中间件（原生 API）；兼容层认证后写入访问日志 |
| `itemId`、`libraryId`、`taskId` | 认证中间件按匹配的路由参数（`/api/v1/items/{id}`、`{itemId}`、`/api/v1/libraries/{id}`、`{libraryId}`、`/api/v1/jobs/{id}`） |
| `taskId`、`libraryId`、`jobRunId` | 任务执行器领取任务时（`internal/platform/jobs/runner.go`），该任务的所有日志与它启动的 goroutine |

守门测试：`TestContextFieldsAreInjectedAcrossGoroutines`、`TestRouteFieldsAndRecordedIdentity`。

### 访问、安全、慢查询与 panic 日志（G46.3）

- **访问日志**：每个请求一条 `request completed`（组件 `http`，INFO）：`method`、`route`（路由样式；没有匹配为 `unmatched`）、`status`、`durationMs`、`bytes`（实际送出的字节，压缩后）、`media`（直投流、音轨、字幕、附件与兼容层视频／音频路由为 `true`）、`requestId`、`traceId`／`spanId`，已认证时加 `userId`、`clientId`、`deviceId`。
- **安全日志**：组件 `security`，WARN `security event`，`event` 为：`login_failed`、`second_factor_failed`（存储层在计数时记下）、`account_locked`、`login_while_locked`、`login_throttled`（错误码 `auth_rate_limited`）、`client_blocked`／`client_pending`／`client_rate_limited`、`access_denied`（错误码 `forbidden`、`share_forbidden`）、`csrf_failed`、`ssrf_blocked`（Webhook 目标被拒：创建时的错误码 `webhook_target_denied` 与投递时的出站拦截都记）、`setup_token_rejected`，以及 `log_disable_refused`。开发者模式控制器的日志（组件 `devmode`）同属安全范围。每种事件每秒最多 20 条，超出的计数在下一条以 `suppressed` 给出；安全事件强制保留追踪。
- **慢查询日志**：组件 `db`，WARN `slow database query`，`code=db_slow_query`：`sql`（语句样板：字面值替换为 `?`，参数占位符保留，压成一行，最多 512 字节）、`durationMs`、`thresholdMs`、`count`（行数）、`outcome`。阈值 `JELEE_DB_SLOW_QUERY_MS`／`logging.slowQueryMs`，默认 500，0 关闭，最大 600000；从不记录参数。
- **panic**：请求中的 panic 写一条 ERROR `request panic`：`route`、`panicType`（Go 类型名）与 `stack`（只保留函数名与模块内相对路径和行号，去掉参数值、goroutine 头与绝对目录，最多 64 帧／8 KiB）。panic 的值本身不写入（可能含请求数据）。堆栈随日志文件轮转、压缩与保留，即为归档。进程级未恢复崩溃仍由 Go 运行时写到 stderr，由容器运行时收集。

守门测试：`TestAccessSecurityAndPanicLogs`、`TestSecurityEventsClassification`、`TestBoundaryArchivesPanicStack`、`TestLoggingMandatoryScopesRefused`（HTTP，使用正式日志路由）、`TestSlowQueryLogThroughRouter`、`TestSQLTemplate`、`TestPanicStackIsMasked`，真 PG `TestLoginFailureNotesSecurityEvents`。

详见[可观测性](observability.md)。

## 审计日志（G46.3、G46.9）

- 表 `audit_logs`（迁移 000001 建立，000002、000060 扩展）：`category`（`audit` 或 `security`）、事件名、操作者与来源地址、目标、前后状态、`request_id`、时间。
- **只追加**：触发器拒绝 UPDATE 与 TRUNCATE，写入时强制使用数据库时间；DELETE 只允许在 `purge_audit_logs()` 中删除超过保留期的行。
- 事件名是封闭集合（`internal/adapter/postgres/audit.go` 的 `auditEvents`），未知事件直接拒绝；安全类事件会强制保留对应的追踪记录。
- 保留期存在表 `audit_retention`：`audit_days`、`security_days`，默认都是 365 天，范围 7–36500。
- 查询：目前只有分享链接的访问记录可以通过 `GET /api/v1/shares/{id}/access` 查询（管理员）；其他审计事件只能直接查询数据库或通过 `pg_dump` 获取。审计行不进入元数据导出（保留期设置会导出），见[备份与还原](backup-restore.md)。

## 诊断导出与查询（G46.7）

- `jelee-cli doctor`：检查配置、数据库、目录、外部工具等；输出前先做敏感信息扫描，命中则不打印并报 `doctor_output_unsafe`。
- `jelee-cli diag export --out <文件>.zip [--since 24h] [--max-log-bytes …]`：生成诊断包。`--since` 默认 24 小时、最长 30 天；`--max-log-bytes` 默认 1 MiB、最大 16 MiB。只读取当前日志文件（不读轮转备份），并对每一行重新应用白名单脱敏；没有配置日志文件时在清单中标记 `no_log_file_configured`。
- `jelee-cli logs tail|filter|export`（G46.7）读取本机日志文件（`logging.file.path`，或 `--file`），包括已轮转与 gzip 压缩的备份，按时间顺序读取，每行重新套用白名单：

| 命令 | 说明 |
| --- | --- |
| `logs tail [--lines 100] [--follow]` | 当前文件最后 N 条符合条件的记录；`--follow` 持续输出新记录，轮转或截断后从新文件开头继续 |
| `logs filter [--limit 1000]` | 全部文件中符合条件的记录 |
| `logs export --out 文件|- [--max-bytes 67108864]` | NDJSON，最大 1 GiB；新文件以 0600 创建且不覆盖已有文件；达到上限时停止并提示 `logs_export_truncated` |

  过滤：`--level`（最低级别）、`--component`（范围或别名，按范围匹配）、`--since`／`--until`（RFC 3339 或“多久之前”，如 `2h`）、`--trace`（32 位十六进制）。`tail`／`filter` 可用 `--format console` 输出可读格式。轮转时间早于 `--since` 的备份直接跳过。

### Loki／Grafana／ELK 查询示例

字段都是 JSON 顶层键，可直接按字段过滤。

```text
# Loki / Grafana：某个请求的全部日志（traceId 即响应头 X-Request-ID）
{app="jelee"} | json | traceId="5f0c6a3b9d2e4f718a6b5c4d3e2f1a0b"
# 安全事件按类型计数（Grafana 面板）
sum by (event) (count_over_time({app="jelee"} | json | component="security" [1h]))
# 5xx 的路由排行
topk(10, sum by (route) (count_over_time({app="jelee"} | json | msg="request completed" | status >= 500 [15m])))
# 媒体请求的 P95 耗时
quantile_over_time(0.95, {app="jelee"} | json | msg="request completed" | media="true" | unwrap durationMs [5m]) by (route)
# 某个用户的操作（去识别化后只剩 UUID）
{app="jelee"} | json | userId="00000000-0000-4000-8000-000000000001"
# 慢查询
{app="jelee"} | json | code="db_slow_query" | line_format "{{.durationMs}}ms {{.sql}}"
```

```text
# Elasticsearch / Kibana（KQL），日志以 JSON 采集到 jelee-* 索引
component : "security" and event : ("login_failed" or "account_locked")
msg : "request completed" and status >= 500 and not route : "/healthz"
taskId : "0b6d1c2e-…" or jobRunId : 0b6d1c2e-*
code : "db_slow_query" and durationMs > 2000
```

```json
// Elasticsearch 查询 DSL：最近一小时某条 trace
{"query":{"bool":{"filter":[{"term":{"traceId":"5f0c6a3b9d2e4f718a6b5c4d3e2f1a0b"}},{"range":{"time":{"gte":"now-1h"}}}]}},"sort":[{"time":"asc"}]}
```

更多关联追踪的示例见[可观测性](observability.md)。

## 开发者模式下的日志

开发者模式（见[开发者模式](developer-mode.md)）提供三个日志相关开关：

| 开关 | 效果 |
| --- | --- |
| `debug_verbose_logging` | 全局级别临时改为 DEBUG，关闭或会话结束时恢复配置值 |
| `debug_sql_logging` | 每条 SQL 写一行：语句文本（压成一行、最多 512 字节，不含参数）、耗时、行数、是否失败 |
| `debug_body_logging`（危险开关） | 每个请求写一行：路由样式、状态码、JSON 请求体与响应体各最多 4 KiB，敏感键名的值替换为 `[redacted]` |

这两类日志的字段（`statement`、`durationMicros`、`rows`、`failed`、`route`、`requestBody`、`responseBody`）与代码 `devmode_sql_log`、`devmode_body_log` 在白名单中另有规则（`internal/platform/logging/devlog.go`）：

- 只有在**对应开关此刻生效**时才放行；开关由 `jelee` 主程序在启动时接到日志路由（`Router.SetDeveloperLogging`，判断与 `devmode.Controller.Effective` 相同，生产环境与不可开发实例永远为否）。开关关闭、会话过期或被关掉之后，这些字段一律写成 `[redacted]`，而且调用方本身也不会再写这两类日志。
- 语句与请求体必须由 `logging.DeveloperSQL`、`logging.DeveloperBody` 包装；同名键下的普通字符串（例如别处误用 `statement`）照样写成 `[redacted]`。包装后的值交给普通 handler 输出时也只会显示 `[redacted]`。
- 放行前**再遮罩一次**：请求体与响应体重新解析 JSON，键名属于密码、令牌、密钥、`authorization`、`cookie`、`dsn`／连接串、`csrf`、`credential`、TOTP（`otp`、`totp`、`totpCode`、`recoveryCode(s)`、`uri`）或以 `password`、`token`、`secret`、`apikey`、`key` 结尾的值，数字形式的 `code`（一次性验证码），以及 `$argon2` 开头、`otpauth:`、`Bearer `／`Basic `、`jdm_`、`whsec_` 开头或带用户名密码的 URL 的值，都替换为 `[redacted]`；SQL 语句文本中的 URL 凭据、`password=…` 一类的赋值、`PASSWORD '…'`、`Authorization` 方案值与服务器签发的令牌格式同样被遮罩，参数占位符 `$1` 保留。
- 诊断包（`diag export`）重新脱敏时没有开发者开关，这些字段一律写成 `[redacted]`。

守门测试：`TestDeveloperFieldsVisibleWhileSwitchedOn`、`TestDeveloperFieldsRedactedWhileSwitchedOff`、`TestDeveloperFieldsNeedTheMarker`、`TestMaskSecrets`（`internal/platform/logging/`），`TestQueryLogThroughRouter`（`internal/adapter/postgres/`），`TestDevModeBodyLoggingRedacts`（HTTP，使用正式日志路由），真 PG 端到端 `TestDevModeRuntimePostgres`（启动整个服务，开关打开后 SQL 与请求体可读、密码被遮罩，关闭后不再出现）。

## 保留与清理（G46.9）

| 记录 | 保留方式 |
| --- | --- |
| 日志文件 | `JELEE_LOG_MAX_BACKUPS`、`JELEE_LOG_MAX_SIZE_MB`、`JELEE_LOG_ROTATE_HOURS`、`JELEE_LOG_COMPRESS`；另有按天保留期与总容量上限（`log_settings`，见下文）；stdout 由容器运行时或日志系统管理 |
| 审计日志 | 表 `audit_retention`（默认 365 天）；服务器按 `JELEE_AUDIT_PURGE_*` 定期清理，见下文 |
| 任务历史 | `JELEE_JOB_HISTORY_LIMIT`（默认 20，范围 1–100），超出的已结束任务被删除；任务没有独立的日志存储 |
| Webhook 投递记录 | `JELEE_WEBHOOK_RETENTION_DAYS`（默认 14） |

### 保留期设置（G46.9）

| 路由／命令 | 说明 |
| --- | --- |
| `GET /api/v1/admin/logging/retention` | `logDays`、`logMaxTotalMB`、`auditDays`、`securityDays`，以及本实例是否写日志文件（`fileLogging`） |
| `PUT /api/v1/admin/logging/retention` | 四个字段都必填；`logDays` 0–3650（0 不按天删除），`logMaxTotalMB` 0–1048576（0 不设上限），审计两类 7–36500 |
| `jelee-cli logs retention [--log-days N] [--log-max-total-mb N] [--audit-days N] [--security-days N] --token-stdin` | 先读取，给了参数时只改这些字段 |

- 默认 `logDays=30`、`logMaxTotalMB=2048`。每个实例对自己的日志文件生效：轮转时与每小时一次删除轮转时间早于保留期的备份，以及总大小（当前文件加全部备份）超过上限时从最旧的备份删起；`maxBackups` 照旧生效；**当前文件从不删除**。
- 日志保留变更写审计 `logging.retention_changed`，审计保留变更写 `audit.retention_changed`；两者在同一交易。审计与安全保留期仍与普通日志独立。

### 用户数据删除时的日志处置

- **审计日志**：按[用户数据权利](user-data-rights.md#稽核紀錄的處理決定與理由)去识别化（清除 IP、改写前后状态），事件保留到各自保留期。
- **运行日志（stdout、日志文件、转发器）不改写**：日志只追加，内容经白名单只含随机 UUID（`userId`、`clientId`、条目 ID）、设备 ID 摘要、路由样式与固定代码，不含用户名、邮箱、IP（默认）、路径或请求内容；永久删除账号后这些 UUID 在数据库中再也对应不到任何人，成为假名。它们随日志保留期（`logDays`、`logMaxTotalMB`、`maxBackups`）自然删除。需要更快清除时缩短 `logDays`，或在日志系统（Loki、ELK）按 `userId` 删除。
- 诊断包与 `jelee-cli logs export` 的输出同样只含这些字段；导出文件由操作者自行保管与删除。

### 审计保留期清理

`jelee` 服务器启动后按固定间隔调用 `purge_audit_logs()`（`app.AuditJanitor` → `Store.PurgeAudit`），这是审计表触发器唯一放行的删除路径：只删除早于各自类别保留期（`audit_retention.audit_days`／`security_days`）的行，直接 `DELETE` 仍被拒绝，`UPDATE`／`TRUNCATE` 仍被拒绝。

| 环境变量 | 配置键 | 默认 | 说明 |
| --- | --- | --- | --- |
| `JELEE_AUDIT_PURGE_INTERVAL_MINUTES` | `audit.purgeIntervalMinutes` | 60 | 0–10080；0 关闭自动清理。第一次清理在启动后一个间隔执行 |
| `JELEE_AUDIT_PURGE_BATCH` | `audit.purgeBatch` | 1000 | 1–10000，单个事务最多删除的行数（与数据库函数的上限相同） |
| `JELEE_AUDIT_PURGE_MAX_BATCHES` | `audit.purgeMaxBatches` | 100 | 1–1000，一轮最多执行的批数；积压超过时下一轮继续 |

- 每一批在自己的事务中执行，删除了行时追加一条 `audit.retention_purged` 审计（记录两类各删除多少）。
- 一轮删除了行时写一条 INFO 日志（组件 `gc`，`count`、`auditRows`、`securityRows`、`batches`、`durationMs`）；达到批数上限时消息注明下一轮继续；失败写 WARN，下一轮重试；没有可删的行时不写日志。
- 多个实例共用数据库时每个实例都会清理；被别的实例先删掉的行会直接跳过，结果相同。
- 保留天数通过 `PUT /api/v1/admin/logging/retention`、`jelee-cli logs retention` 或元数据导入修改，见上文与[备份与还原](backup-restore.md)。

守门测试：`TestAuditJanitorPassIsBounded`、`TestAuditJanitorOptions`、`TestAuditJanitorRunStopsWithContext`（`internal/app/`），`TestAuditConfigDefaultsAndEnvironment`（配置），真 PG `TestAuditJanitorPurgesExpiredRowsPostgres`（分批删除过期行、保留期内的行与清理记录保留、直接 DELETE 仍被拒、日志字段通过正式白名单）。

## 审计日志不能关闭（G46.10）

- 审计行与业务变更在同一交易写入数据库，与日志级别无关。
- 运行日志中的 `audit`、`security` 范围（含别名 `devmode`）门槛永远不高于 INFO，全局设为 ERROR 时这两个范围的 INFO 仍写出；配置、`SetLevel`、管理员覆写都不能修改它们，试图修改时 API 返回 409 并留下安全审计与安全日志（见上文）。
- 守门测试：`TestMandatoryComponentsCannotBeLowered`、`TestLoggingRefusesMandatoryComponentLevels`（配置）、`TestLoggingMandatoryScopesRefused`（HTTP）、真 PG `TestLogSettingsIntegration`。

## 尚未完成

- 没有通用的审计查询接口（G49.6）；审计行只能直接查询数据库，分享链接的访问记录除外。
- syslog、Loki、OTLP 转发器只定义了类型，尚未实现；请用容器运行时或日志代理收集 stdout 或日志文件。
- `jelee-cli logs` 只读本机日志文件；多实例部署请在日志系统中集中查询。
- 级别覆写在其他实例上最多延迟 15 秒生效。
- 高 QPS 压测（P95）只有开发机基准，尚未在固定硬件上做端到端压测（需要长时间验证）。
- 进程级未恢复的崩溃（非请求 goroutine 的 panic、fatal error）由 Go 运行时写 stderr，未经遮罩，也不进入日志文件。
