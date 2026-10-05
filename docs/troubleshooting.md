# 故障排查：doctor 与诊断包

本文对应 G50.1（`jelee-cli doctor`）与 G50.2（`jelee-cli diag export`），以 `internal/diag/` 的实现为准。每个错误码都在下文有一行说明；`internal/diag` 的测试会核对代码里登记的每个错误码都出现在本文中。

## doctor

```sh
jelee-cli doctor                 # 表格输出
jelee-cli doctor --json          # 机器可读 JSON
jelee-cli doctor --external      # 额外检查 TMDB 连通性（默认不检查）
jelee-cli doctor --max-roots 256 # 最多检查的媒体库根数量（默认 64，上限 1024）
jelee-cli doctor --checks config,database,migrations  # 只跑指定检查（容器 HEALTHCHECK 用这一组）
jelee-cli doctor probe           # 原有：隔离 ffprobe helper 自检（行为不变）
jelee-cli doctor tools           # 原有：项目内固定 ffprobe 身份诊断（行为不变）
```

- **结束码**：任一检查为 `fail` 时为 1；只有 `ok`／`warn` 时为 0；参数错误为 2。
- **配置读取**：与服务相同（`JELEE_CONFIG` 文件 + 环境变量）。配置不合法时 doctor 仍会继续，用已读到的部分做其他检查。
- **数据库**：用只读会话（`default_transaction_read_only=on`、语句超时 5 秒、最多 2 个连接）连接，不要求 schema 是当前版本，因此能报告未迁移、dirty、落后或过新。
- **时间上限**：每项检查 20 秒，整次运行 3 分钟；每个媒体库根 3 秒。卡住的网络文件系统只会让该项报 `library_root_timeout` 或 `check_timeout`。
- **容器健康检查**：`Dockerfile` 的 `HEALTHCHECK` 只跑 `--checks config,database,migrations`，与原 doctor 的范围（配置、PostgreSQL、schema）相同，避免每 30 秒做磁盘、库根与工具哈希检查。完整 doctor 请手动执行。
- **外部连通性**：只有加 `--external` 才检查，并使用正式 TMDB 适配器与受控出站 client（固定 DNS 结果、拒绝私网地址、不跟随重定向），见 [受控出站](outbound-tmdb-preflight.md)。

### 输出不含敏感值

- 结果里只有固定错误码、固定说明与固定修复建议。路径用配置键（`images.tempRoot`、`logging.file`、`tempdir`）或数据库代号（`root:<uuid>`）表示，不输出绝对路径、主机名、DSN、密码、token 或 API key。
- 配置校验错误只有在是纯文字（不含 `/`、`:`、`@` 等）时才放进 `detail`。
- 打印前会用与诊断包相同的敏感扫描器检查一次输出；若命中，不打印，改为在 stderr 输出 `doctor_output_unsafe` 并以 1 结束。

### JSON 格式（format 1）

```json not-http
{
  "format": 1,
  "generatedAt": "2026-10-04T12:00:00Z",
  "status": "fail",
  "summary": {"ok": 8, "warn": 1, "fail": 1},
  "results": [
    {
      "check": "migrations",
      "status": "fail",
      "code": "db_migration_dirty",
      "fix": "…",
      "findings": [{"status": "fail", "code": "db_migration_dirty", "message": "…", "fix": "…"}],
      "facts": {"required": "60", "version": "60", "dirty": "true"}
    }
  ]
}
```

`results` 的顺序固定：`config`、`database`、`migrations`、`library_roots`、`tools`、`disk`、`network`、`directories`、`privacy`、`devmode`，加 `--external` 时最后再加 `external`。每项的 `status`／`code`／`fix` 取自最严重的 finding；`facts` 只放数字、版本号与枚举值。

## 错误码

状态列：ok 表示仅为信息；warn 不影响结束码；fail 使结束码为 1。

### 通用

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `ok` | ok | 检查通过 | — |
| `check_timeout` | fail | 单项检查超过 20 秒 | 重跑；若重复出现，检查是否有卡住的网络文件系统或数据库 |
| `check_panicked` | fail | 检查内部错误 | 附上 `doctor --json` 输出回报问题 |

### config：配置合法性

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `config_ok` | ok | 配置合法 | — |
| `config_invalid` | fail | 配置校验失败，`detail` 写明哪一项 | 按 `detail` 修正环境变量或 `JELEE_CONFIG` 文件后重跑 |
| `config_database_missing` | fail | 没有配置数据库 | 设置 `JELEE_DATABASE_URL` 或 `JELEE_DATABASE_URL_FILE` |
| `config_secret_file_permissions` | warn | `JELEE_CONFIG`、`JELEE_DATABASE_URL_FILE` 或 `TMDB_API_KEY_FILE` 指向的文件可被其他用户读取（subject 为变量名） | `chmod 600`，属主为服务账户 |

### database／migrations：数据库连接与迁移

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `db_connected` | ok | 连接成功 | — |
| `db_not_configured` | fail | 未配置数据库，未检查 | 同 `config_database_missing` |
| `db_config_invalid` | fail | 连接串无法解析 | 使用指明主机、端口与数据库的 PostgreSQL URL；凭据建议放 `JELEE_DATABASE_URL_FILE` |
| `db_unreachable` | fail | 连不上 PostgreSQL | 确认 PostgreSQL 在运行、主机与端口从本机可达、TLS 设置一致 |
| `db_auth_failed` | fail | PostgreSQL 拒绝凭据（SQLSTATE 28P01／28000） | 修正用户名与密码，或服务器的 `pg_hba.conf` |
| `db_query_failed` | fail | 只读诊断查询失败 | 给服务账户其 schema 与 `pg_stat_*` 视图的读取权限；检查语句超时 |
| `db_unchecked` | warn | 数据库不可用，迁移未检查 | 先修好数据库连接 |
| `db_schema_current` | ok | schema 为本版本要求的版本且 clean | — |
| `db_schema_missing` | fail | 从未迁移（无 `schema_migrations`） | `jelee-migrate up` |
| `db_migration_dirty` | fail | 上次迁移没有完成 | 从备份还原或手动修复失败的迁移，再 `jelee-migrate status`；dirty 时不要启动服务 |
| `db_migration_behind` | fail | schema 比本版本旧 | 先备份，再 `jelee-migrate up` |
| `db_schema_newer` | fail | schema 比本版本新 | 改用对应的新版 Jelee，或还原升级前的备份 |

### library_roots：媒体库根

最多检查 `--max-roots` 个根（默认 64）；每个根 3 秒超时。subject 为 `root:<库根 UUID>`，不输出路径。

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `library_roots_none` | ok | 尚未登记任何库根 | — |
| `library_root_ok` | ok | 存在且可列目录 | — |
| `library_root_missing` | fail | 路径不存在 | 挂载媒体卷或修正库根；服务只读媒体，不会自建根目录 |
| `library_root_not_directory` | fail | 路径不是目录 | 把库根指向目录 |
| `library_root_unreadable` | fail | 服务账户无法列出目录 | 给服务账户读与执行权限（只读挂载即可） |
| `library_root_timeout` | fail | 3 秒内没有响应 | 检查该根背后的网络或可移动文件系统 |
| `library_roots_truncated` | warn | 库根数量超过上限，其余未检查 | 用更大的 `--max-roots` 重跑 |
| `library_roots_unchecked` | warn | 数据库不可用，库根未检查 | 先修好数据库连接 |

### tools：外部工具版本与哈希

doctor 只计算 SHA-256，不执行工具。候选位置为运行镜像的固定路径（subject `ffprobe:runtime`）与项目内 `.tools/` 安装（`ffprobe:project`），期望值来自编进程序的 `tools/manifest.json`。哈希一致即证明版本一致，`facts.expectedVersion` 为固定版本号。

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `tool_verified` | ok | ffprobe 与固定哈希一致 | — |
| `tool_missing` | warn／fail | 没有安装固定 ffprobe；启用 probe（`JELEE_ENABLE_PROBE`）时为 fail | 容器使用正式运行镜像；开发环境执行 `make bootstrap-media`（Windows：`scripts/make.ps1 bootstrap-media`） |
| `tool_hash_mismatch` | fail | 文件与 manifest 的 SHA-256 不一致 | `make tools-clean bootstrap-media` 重新安装，或重建运行镜像；不要换成系统 ffprobe |
| `tool_unreadable` | fail | 文件存在但不是可读的普通文件 | 让服务账户可读，并确认是普通文件 |
| `tool_platform_unsupported` | warn | 本平台没有固定 ffprobe | 探测只支持 linux-amd64；其他平台不提供媒体探测 |
| `tool_manifest_invalid` | fail | 编进程序的 manifest 无效 | 从干净的源码重新构建 jelee-cli |
| `embedded_covers_ready` | ok | 已启用内嵌封面擷取（`JELEE_ENABLE_EMBEDDED_COVERS`），且找到通过哈希校验的固定 ffprobe；该功能只用 ffprobe，不需要也不调用 ffmpeg | — |
| `embedded_covers_tool_missing` | fail | 已启用内嵌封面擷取，但没有通过校验的固定 ffprobe；服务照常启动，该段保持关闭并记 WARN `embedded_cover_prerequisite_unavailable` | 使用正式运行镜像（内含 ffprobe、绝不含 ffmpeg），或设 `JELEE_ENABLE_EMBEDDED_COVERS=false` |
| `matroska_tool_verified` | ok | 可选 mkvtoolnix／MediaInfo 与清单 SHA256 一致 | — |
| `matroska_tool_missing` | warn（启用抽取时 mkvmerge／mkvextract 为 fail） | 可选工具未安装，对应功能保持关闭 | 容器用官方运行镜像；开发环境 `make bootstrap-matroska`（Windows：`scripts/make.ps1 bootstrap-matroska`） |
| `matroska_tool_hash_mismatch` | fail | 工具与 tools/manifest.json 固定的 SHA256 不符 | 删除 `.tools/matroska` 后重新 `make bootstrap-matroska`，或重建运行镜像；不得换成系统副本 |
| `matroska_tool_unreadable` | fail | 工具存在但不可读 | 让该文件成为服务账户可读的普通文件 |
| `matroska_tool_platform_unsupported` | warn | 本平台没有固定的 mkvtoolnix／MediaInfo | 清单固定 linux-amd64 与 windows-amd64；沙箱运行只在 linux-amd64 |
| `ocr_tool_verified` | ok | 可选 Tesseract（字幕 OCR，G15.6）的执行档或语言数据与清单 SHA256 一致 | — |
| `ocr_tool_missing` | warn（启用 OCR 时执行档与所配置语言的数据为 fail） | 可选 OCR 运行时未安装，字幕 OCR 保持关闭 | 开发环境 `make bootstrap-ocr`；容器用 `deploy/ocr/Dockerfile` 的 OCR 镜像；见 [字幕 OCR](subtitle-ocr.md) |
| `ocr_tool_hash_mismatch` | fail | 文件与 tools/manifest.json 固定的 SHA256 不符 | 删除 `.tools/ocr` 后重新 `make bootstrap-ocr`，或重建 OCR 镜像；不得换成系统副本 |
| `ocr_tool_unreadable` | fail | 文件存在但不可读 | 让该文件成为服务账户可读的普通文件 |
| `ocr_tool_platform_unsupported` | warn（启用 OCR 时为 fail） | 本平台没有固定的 Tesseract | 字幕 OCR 只在 linux-amd64 运行；其他平台设 `JELEE_ENABLE_SUBTITLE_OCR=false` |

完整的执行级检查仍用 `jelee-cli doctor probe`（隔离 helper）与 `jelee-cli doctor tools`（项目快照执行 `-version`）。

### disk：磁盘空间与 inode

检查 `tempdir`（`$TMPDIR`）、`images.tempRoot`、`images.storeRoot` 与日志目录（`logging.file`）所在卷。Linux 用 `statfs`；Windows 只有字节数（无 inode）；其他平台报 `disk_stat_unavailable`。阈值：可用空间低于 min(2 GiB, 卷容量 10%) 为 warn，低于 min(256 MiB, 卷容量 2%) 为 fail（因此 Compose 的 64 MiB `/tmp` tmpfs 按比例判断）；可用 inode < 5% 为 warn，< 1% 为 fail。

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `disk_ok` | ok | 空间与 inode 充足 | — |
| `disk_space_low` | warn | 可用空间偏低 | 清理该卷或把目录搬到更大的卷 |
| `disk_space_critical` | fail | 可用空间严重不足 | 立即清理；暂存与日志将写入失败 |
| `disk_inodes_low` | warn | 可用 inode 偏低 | 删除大量小文件（过期暂存、日志） |
| `disk_inodes_critical` | fail | 可用 inode 严重不足 | 立即删除过期小文件；inode 用尽后无法建立文件 |
| `disk_stat_unavailable` | warn | 读不到磁盘用量 | 确认目录存在；不支持的平台请手动检查 |
| `disk_inodes_not_applicable` | ok | 本平台没有 inode 计数（Windows） | — |

### network：监听地址与可信代理

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `net_ok` | ok | 监听地址与代理设置合法 | — |
| `net_listen_invalid` | fail | `JELEE_LISTEN` 不是明确的 IP:端口 | 例如 `127.0.0.1:8097` |
| `net_proxy_invalid` | fail | 可信代理不是合法 CIDR | 修正 `JELEE_TRUSTED_PROXIES`，每项如 `127.0.0.1/32` |
| `net_proxy_too_broad` | fail | 可信代理范围过宽（IPv4 短于 /8、IPv6 短于 /16，例如 `0.0.0.0/0`），客户端可伪造地址 | 只列代理自己的地址，见 [可信代理](trusted-proxies.md) |
| `net_proxy_none` | ok | 未配置可信代理，转发头被忽略 | — |

### directories：暂存、缓存与日志目录

- `tempdir`：允许全局可写，但必须带 sticky 位（服务在其中建立 0700 子目录）。
- `images.tempRoot`、`images.storeRoot`：必须是真目录（不能是 symlink）、属于服务账户、无组／其他用户权限。
- 日志目录：必须可写；权限过宽只警告。日志文件本身应为 0600（轮转器会自动收紧）。
- 可写性用 `.jelee-doctor-*` 临时文件测试，随即删除。Windows 不检查权限位，只报 `dir_permissions_unchecked`。

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `dir_ok` | ok | 存在、可写、权限安全 | — |
| `dir_missing` | fail（日志目录为 warn） | 目录不存在；启用图片但未设 `JELEE_IMAGE_TEMP_ROOT` 也报此码 | 以 0700 建立目录，属主为服务账户；日志目录会由服务自动建立 |
| `dir_not_directory` | fail | 不是目录 | 改指向目录 |
| `dir_symlink` | fail | 私有目录是 symlink | 配置真实目录 |
| `dir_not_writable` | fail | 服务账户不可写 | 授予写权限 |
| `dir_permissions_too_broad` | fail（日志目录为 warn） | 其他用户可访问 | `chmod 700` |
| `dir_owner_mismatch` | fail | 属主不是服务账户 | 改属主 |
| `dir_shared_not_sticky` | warn | 全局可写的暂存目录没有 sticky 位 | `chmod 1777`，或把 `TMPDIR` 设为私有目录 |
| `dir_permissions_unchecked` | ok | 本平台不检查权限位 | — |
| `log_file_permissions` | warn | 日志文件可被其他用户读取 | 对日志文件与轮转备份 `chmod 600` |

### privacy：隐私开关

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `privacy_listen_loopback` | ok | 只监听环回 | — |
| `privacy_listen_exposed` | warn | 监听所有接口或私网地址 | 确认是有意为之；前面保留防火墙或反向代理，见 [网络隐私](network-privacy.md) |
| `privacy_listen_public` | warn | 直接监听公网地址，客户端会知道这个地址 | 改为环回监听并在前面放反向代理 |
| `privacy_debug_logging` | warn | 全局或某组件为 DEBUG 日志 | 生产环境用 info：取消 `JELEE_LOG_LEVEL` 或设为 info |
| `privacy_ip_masked` | ok | 客户端 IP 以网段掩码记录 | — |
| `privacy_paths_relative` | ok | 日志保留配置根以下的相对路径 | — |
| `privacy_tmdb_outbound` | ok | 已配置 TMDB，元数据请求会发往 TMDB | — |

### devmode：开发者模式

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `devmode_disabled` | ok | 开发者模式关闭：`JELEE_DEV_MODE=true` 与 `dev.enabled` 未同时设置 | — |
| `devmode_env_set` | warn | 两项开关只设了一项，开发者模式仍关闭 | 若不是在准备开发实例，删掉残留设置；见 [开发者模式](developer-mode.md) |
| `devmode_env_invalid` | fail | `JELEE_DEV_MODE` 不是 true/false，服务会拒绝启动 | 设为 true 或 false，或取消 |
| `devmode_capable` | warn | 本实例可开启开发者模式（两项开关都已设置；实际开启还需一次性令牌） | 生产环境绝不可这样配置：取消 `JELEE_DEV_MODE` 或把 `dev.enabled` 设为 false |
| `devmode_production_environment` | ok | `JELEE_ENV=production` 强制关闭开发者模式 | — |
| `devmode_production_ignored` | warn | 有开发者设置，但因 `JELEE_ENV=production` 被忽略 | 从生产部署中删除 `JELEE_DEV_MODE` 与 `dev.enabled` |

### external：外部源连通性（仅 `--external`）

| 错误码 | 状态 | 含义 | 修复步骤 |
| --- | --- | --- | --- |
| `external_tmdb_ok` | ok | TMDB 接受当前凭据 | — |
| `external_tmdb_not_configured` | ok | 未配置 TMDB，服务为本地模式 | — |
| `external_tmdb_credentials` | fail | TMDB 拒绝凭据 | 用有效的 v3 API key 替换 `TMDB_API_KEY`／`TMDB_API_KEY_FILE` |
| `external_tmdb_rate_limited` | warn | 本次检查被限流 | 稍后重跑；不代表配置错误 |
| `external_tmdb_unreachable` | fail | 经受控出站 client 连不上 TMDB | 检查 DNS、防火墙与到 api.themoviedb.org 的 HTTPS 出站 |

## diag export

```sh
jelee-cli diag export --out jelee-diag.zip [--since 24h] [--max-log-bytes 1048576] [--external] [--max-roots 64]
```

- `--out` 必须是 `.zip`，以 `O_EXCL` 建立、权限 0600；**已存在的文件不会被覆盖**。
- `--since` 是日志与任务的时间窗，默认 24 小时，上限 30 天；`--max-log-bytes` 默认 1 MiB，上限 16 MiB。整个包上限 32 MiB。
- 只读日志的当前文件（`JELEE_LOG_OUTPUT=file|both` 时的 `JELEE_LOG_FILE`），从文件尾部最多读取 8 倍字节上限；轮转备份与压缩档不读。超过 64 KiB 的单行与非 JSON 行直接丢弃。
- **不收集 pprof**：pprof 需要开发者模式（G45.5），manifest 里标为 `not_collected_requires_developer_mode`。

包内文件：

| 文件 | 内容 |
| --- | --- |
| `manifest.json` | 格式版本、生成时间、时间窗、上限、平台、Go 版本、文件清单、各段状态（例如 `database_unavailable`、`no_log_file_configured`、`no_records_in_window`） |
| `config.json` | 白名单配置摘要：开关、数值上限、日志模式；数据库、TMDB、各目录只记“是否已配置”，监听地址只记范围（loopback／private／all／public）与端口 |
| `doctor.json` | 完整 doctor 报告（同 `--json`） |
| `db-stats.json` | 当前 schema 各表名称、估计行数（`pg_stat_user_tables.n_live_tup`）与总大小，最多 256 张表，不含任何行内容 |
| `jobs.json` | 时间窗内任务按 kind／state／error_code 分组的数量与最近时间，不含 ID 或路径 |
| `logs.jsonl` | 时间窗内的日志记录，逐条重新套用日志白名单脱敏（与 G46.5 同一套规则）；不安全的键名整个丢弃 |

### 二次扫描

写完后会重新打开 zip，扫描每个条目名称与解压后的内容；命中以下任一项就删除文件并以 `diag_sensitive_content` 失败：

- 本次配置与环境中的具体值：数据库连接串、其中的密码与非环回主机、TMDB key、所有已配置的绝对路径（图片目录、日志文件、日志路径根、库根、`HOME`、工作目录、输出目录）；
- 通用形态：`postgres://`、URL 内嵌凭据、`password=`、`Bearer`／`Basic` 凭据、`token=`／`api_key=`、JWT、Unix 与 Windows 绝对路径、UNC 路径。

### 导出错误码

| 错误码 | 含义 | 修复步骤 |
| --- | --- | --- |
| `diag_output_exists` | `--out` 已存在 | 换一个新文件名 |
| `diag_output_failed` | 无法写入（目录不存在、不可写或归档损坏） | 确认输出目录存在且可写 |
| `diag_output_path_invalid` | 输出不是 `.zip` | `--out` 用 `.zip` 文件名 |
| `diag_sensitive_content` | 包内扫描到敏感值，文件已删除 | 回报问题；不会留下任何包 |
| `diag_bundle_too_large` | 超过 32 MiB，文件已删除 | 降低 `--max-log-bytes` 或 `--since` |
| `doctor_output_unsafe` | doctor 输出命中敏感扫描而未打印 | 回报问题；可先在本机用 `--json` 检查 |

## 尚未覆盖

- G50.2 的慢查询、客户端与规则命中统计、库统计：对应功能尚未实现，诊断包暂不包含。
- G50.3–G50.6（一致性检查、自愈、运行时不变量扩充、告警 runbook）不在本次范围。
