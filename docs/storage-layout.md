# 存储布局

本文以当前代码为准（schema 000069，`internal/adapter/postgres/store.go` 的 `SchemaVersion = 69`，2026-10-04 更新），列出 Jelee 在本机会读写的全部资产：每一项写明由谁建立、由谁清理、能否删除后重建。G09.1 要求媒体、字幕、音轨、NFO、图片、封面、章节、探测缓存都位于本地卷；本文是这份目录结构的定义。

总原则：

- **PostgreSQL 是唯一的持久状态。** 服务不使用 SQLite、嵌入式 KV 或本地 JSON 状态文件，日志只写到 stdout。
- **媒体库目录默认只读。** 服务只读取媒体、字幕、音轨、`.jeleeignore` 和 NFO，不覆写、不自动删除这些文件。Compose 也把媒体以 `:ro` 方式挂载。
- **本地暂存都在私有目录内。** 目录名都由程序自己生成，崩溃留下的残留由启动清扫处理，详见 [暂存与崩溃残留](#暂存与崩溃残留)。

## 一览

| 资产 | 位置 | 建立者 | 清理者 | 能否删除／重建 |
| --- | --- | --- | --- | --- |
| 数据库 | `JELEE_DATABASE_URL`；Compose 为 `data/postgres` | `jelee-migrate up` | 运维备份与还原 | **不可删。** 账户、库、任务、缓存都在这里 |
| 媒体根 | `library_roots` 里的绝对路径（可多库、多路径） | 运维 | 运维 | 服务只读 |
| `.jeleeignore` | 媒体目录内 | 用户 | 用户 | 服务只读；判断结果存在数据库 |
| NFO 与 `.jelee*` 旁车文件 | 媒体目录内 | 目前的正式服务不会建立，见 [NFO](#nfo-旁车文件) | — | 见该节 |
| 探测缓存 | 数据库 `probe_cache` 等 | probe worker | TTL 与 quota；随任务历史清除 | 可删，下次扫描会重新探测 |
| NFO 缓存 | 数据库 `nfo_cache` 等 | NFO worker | TTL 与 quota | 可删，会重新读取 |
| 图片内存缓存 | 进程内存 | 图片处理器 | LRU 与 TTL | 重启即清空 |
| 图片暂存 | `JELEE_IMAGE_TEMP_ROOT/image-…partial` | 图片请求 | 请求结束时删除；启动清扫 | 停机时可删 |
| 图片持久存放区 | `JELEE_IMAGE_STORE_ROOT` | 目前没有接入 | — | 见 [图片](#图片) |
| 外部工具暂存 | `$TMPDIR/jelee-service-*`、`$TMPDIR/jelee-probe-check-*` | probe／ignore／mkv 服务 | 服务关闭时删除；启动清扫 | 停机时可删 |
| 工具与授权文件 | 容器内 `/usr/lib/jelee/ffprobe`、`/usr/lib/jelee/mediainfo`、`/usr/lib/jelee/mkvtoolnix/`、`/lib`、`/lib64`、`/licenses` | 镜像 | — | 不可改，身份与 SHA-256 都会校验 |
| Matroska 擷取快取 | `JELEE_MATROSKA_CACHE_ROOT/<sourceID>/<修訂>/`（內嵌文字字幕與字型附件的原樣副本） | 擷取請求（E4，預設關閉） | 新修訂取代舊修訂；超過 `JELEE_MATROSKA_CACHE_MAX_BYTES` 依最近使用淘汰；staging 殘留一小時後清除 | 可隨時刪除，下次請求重新擷取；不需備份，見 [mkvtoolnix 與 MediaInfo](matroska-tools.md) |
| CLI 诊断暂存 | `<项目>/.testdata/tool-doctor-*` | `jelee-cli doctor tools` | 命令结束时删除 | 可删 |
| doctor 可写性探针 | 暂存、图片与日志目录内的 `.jelee-doctor-*` | `jelee-cli doctor` | 建立后立即删除；崩溃时只留空文件 | 可删 |
| 诊断包 | `jelee-cli diag export --out` 指定的 `.zip`（0600，不覆盖已有文件） | 运维 | 运维 | 可删；内容已脱敏，分享前仍应检查，见[故障排查](troubleshooting.md#diag-export) |
| 开发产物 | `.tools/`、`.cache/`、`.bin/`、`.testfixtures/`、`.testdata/` | 开发脚本 | 手动 | 可删可重建，都已被 Git 忽略 |

## 数据库（schema 1–69）

当前二进制只接受 clean schema 69；版本低一、高一，或 `dirty` 都会拒绝启动，要先执行 `jelee-migrate up`。所有迁移都没有 DROP TABLE。依功能分组：

| 迁移 | 内容 |
| --- | --- |
| 001–002 | 账户、会话、库、`library_roots`（绝对媒体根只在这里，HTTP 不返回）、ACL、items、`media_sources`、审计；002 加入密码、锁定与 `user_creation_keys` |
| 003 | `jobs`、`job_directories`、`job_inventory`、`library_inventory_baseline`：有界的任务状态与盘点 |
| 004–005 | 探测契约：`tool_versions`（不可变的工具／runtime 身份，默认 16 行，硬上限 32）、`probe_cache`（默认全域 100,000 行，单份 metadata 最多 128 KiB）、`probe_job_state`、`probe_cache_quota`／`probe_library_quota`、`probe_requests` |
| 006–007 | NFO 缓存与 worker：`nfo_cache*`、`nfo_job_*`、`image_job_state`、`inventory_generation` |
| 008–018 | ignore：`job_ignore_*`（请求、manifest、proof、比对、验证、扫描排除、legacy 与 family 决策） |
| 019–022 | 元数据语言、图片语言、`item_metadata_state`／`item_metadata_fields` 与 TMDB 来源 |
| 023–039 | NFO 观测、字段锁、`item_metadata_facts`、目录 NFO（`item_directory_sources`、`item_parent_links`）；其中多数迁移只放宽 CHECK |
| 040–045 | `catalog_import_*`、`scan_schedules`、`scan_watch_state`、盘点快照（`library_inventory_baseline` 改为 view，数据在 `library_inventory_baseline_data`）、ignore 快照栏位、任务指标 |
| 046–057 | NFO 写入流程：preparations、`nfo_write_requests`／`entries`、quota fence、commit journal、commit 文件计划与 checkpoint、native receipt／claims、commit attempts，以及 057 的 `nfo_write_commit_recovery_leases` |
| 058 | `item_images`：条目图片槽、锁定与来源（图片管理；入库与刷新见擁有者任務二） |
| 059 | `nfo_commit_settlements`：NFO commit 结算记录 |
| 060 | 审计表只可追加（append-only 触发器） |
| 061 | 目录同步：`catalog_sync` 任务种类与指标列、`job_directories` 目录认领栏、`catalog_sync_requests`、`inventory_missing_acceptances`、`catalog_scan_items`／`sources`／`pending`、`libraries.catalog_sync_auto`、元数据来源 `scan`；见 [目录同步](catalog-sync.md) |
| 062 | `media_sidecar_tracks`：外挂字幕／音轨（`media_sources` 加 `UNIQUE(id,library_id)`） |
| 063 | 原生会话：`users.allow_native`，`sessions` 的设备、客户端、最后使用时间与地址 |
| 064 | `users.max_streams`／`max_kbps`：每用户投递上限覆写；见 [直投](direct-delivery.md) |
| 065 | 扫描配对外挂轨：`inventory_sidecar_owner` 函数与部分索引 |
| 066 | 播放进度：`playback_sessions`、`playback_samples`、`user_item_data`；见 [播放进度](playback-progress.md) |
| 067 | 观看统计：`watch_stats_daily` 等日汇总；见 [观看统计](watch-statistics.md) |
| 068 | Webhook：`webhooks`、`webhook_outbox`、`webhook_deliveries`（密钥以主钥 AES-GCM 封存）；见 [Webhook](webhooks.md) |
| 069 | 内容访问：条目规则、分级上限、标签封锁、`users.content_filtered`；见 [访问控制](access-control.md) |

数据量上限、回收与降级的细节分别写在 [探测缓存](probe-cache.md)、[NFO 缓存](nfo-cache.md)、[任务](jobs-worker.md)、[ignore 存储](ignore-family-storage.md)、[NFO commit 恢复租约](nfo-commit-recovery-lease.md)。回滚说明在各迁移对应的文档里；降级会丢失该迁移之后的状态，但不会改动媒体目录。

## 媒体目录

- **媒体、外挂字幕、外挂音轨、章节**：留在原来的本地目录，只在数据库记录 root UUID 和相对路径。所有打开都经过 `os.OpenRoot`、相对路径校验和 symlink 逃逸检查（G09.5）。
- **`.jeleeignore`**：只读。Linux 用 `O_RDONLY|O_NONBLOCK` 打开。判断结果写进数据库的 `job_ignore_*` 表，不在媒体目录留下任何文件。

### NFO 旁车文件

NFO 写回在开启 `JELEE_ENABLE_NFO_WRITE`（`enableNFOWrite`）时由任务 runner 领取 `nfo_write` 任务执行（`internal/platform/runtime/runtime.go`）；未开启时只读。已知限制：写回保留记录只增不减，累积到上限后新写回会被拒，清理协议是擁有者任務一（`docs/owner-dev-tasks.md`）。写回时会在 NFO 所在目录出现下列文件，列出来方便运维辨认：

| 名称 | 用途 | 清理 | 手动删除 |
| --- | --- | --- | --- |
| `.jelee-nfo-<sha256(文件名)>.lock` | 每个 NFO 的 0 字节锁文件（`flock`／`LockFileEx`） | 不删除：为了 inode 一致，刻意保留 | 没有写入在进行时可以删 |
| `.jelee-nfo-stage-<32hex>` | 简单 replace 路径的暂存、回滚和备份副本 | 成功时 rename 或删除。回滚失败时会刻意保留，因为那时它是原文件的最后一份副本 | 先确认原 NFO 完好，再删 |
| `.jelee-nfo-commit-<32hex>[-attempt-<1..3>]-{original-pin,output,output-pin,rollback,rollback-pin}` | commit 计划的 hardlink 与见证，名称会先写进 `nfo_write_commit_file_plans`／attempts | 尚无清理协议，过期回收只处理数据库状态 | journal 还引用这个 token 时**不可删** |
| `…-backup-evicted` | settle 时被轮换出去的最旧备份 | 同上 | 同上 |
| `<文件名>.jelee.bak`、`.jelee.bak.1`…`.15` | 用户备份，数量由请求的 `Backups`（0–16）决定 | 简单路径会删除超出数量的旧备份；settle 只做 rename | 可删，程序不会再读回 |

## 图片

- **处理器**（`JELEE_ENABLE_IMAGES`）：
  - 必须设置 `JELEE_IMAGE_TEMP_ROOT`。它必须是绝对路径、私有、不是别名，也不能和媒体根或存放区重叠。
  - 每个请求会把来源复制到 `image-o<pid>-<启动标识>-<32hex>.partial`（0600，`O_EXCL`），处理完就删除。
  - 输出缓存只在内存里：`JELEE_IMAGE_CACHE_BYTES` 默认 32 MiB，`JELEE_IMAGE_CACHE_ENTRIES` 默认 128，`JELEE_IMAGE_CACHE_TTL_SECONDS` 默认 300。
- **持久存放区**（`JELEE_IMAGE_STORE_ROOT`，`internal/adapter/images/store.go`）：
  - 设置后由 runtime 在启动时调用 `OpenStore`（`internal/platform/runtime/runtime.go`），图片请求读写这个目录。
  - 布局：
    - `originals/<sha256[:2]>/<sha256>`：按内容寻址的原图
    - `variants/<世代 16hex>/<来源 sha256>/<key>`：48 字节标头 `JLVAR01\n` 加衍生图。只有编号最大的世代是现役；较旧的世代和旧版没有世代层的 `variants/<来源 sha256>/` 都是已清除的内容，只会被删除，绝不会被收录
    - `tmp/put-<64hex>.partial`：写入暂存
    - `tmp/trash-<64hex>/`：旧版清空 variants 时留下的分离树，只做清理
  - 上限：`JELEE_IMAGE_STORE_ORIGINAL_BYTES` 默认 4 GiB、`JELEE_IMAGE_STORE_VARIANT_BYTES` 默认 1 GiB，`JELEE_IMAGE_STORE_ENTRIES` 默认每类 131072。
  - `OpenStore` 会：
    - 选编号最大的世代为现役（没有就建立第 1 世代）；
    - 清掉 `tmp/` 里自己命名的残留，每次最多 128 个 trash 树；
    - 从文件 metadata 重建内存索引，LRU 顺序就是文件 mtime；
    - 删除损坏、超量的条目；
    - 删除旧世代里自己命名的文件，删不掉的（例如在 Windows 上被其他程序以不共享删除的方式开着）留到下次清除或启动再试。
  - `ClearVariants` 不改名目录：先建立下一个世代目录作为持久提交点，再同时切换现役世代、清空索引，最后逐文件删除旧世代。Windows 不允许改名里面有开启中文件的目录，所以旧做法（整个 `variants` 改名到 `tmp/trash-*`）在有变体正被读取时会失败。
  - 其他名称一律视为外来文件，只计数、不碰。整个目录可以在停机时删除，只会损失缓存。

## 暂存与崩溃残留

| 名称 | 父目录 | 建立／正常删除 |
| --- | --- | --- |
| `jelee-service-probe-o<pid>-<启动标识>-<32hex>/` | `os.TempDir()`（`$TMPDIR`） | probe 服务启动时建立，关闭时 `RemoveAll` |
| `jelee-service-ignore-o<pid>-<启动标识>-<32hex>/` | 同上 | family ignore 服务，同上 |
| `jelee-probe-check-o<pid>-<启动标识>-<32hex>/health-*` | 同上 | 启动健康检查和 `jelee-cli doctor probe`；检查结束就删除 |
| `run-*/`、`ignore-input-*/request.bin` | 上述服务根目录内 | 每次执行时建立，结束时删除；子进程的 `TMPDIR`／`TMP`／`TEMP` 指向 `run-*` |
| `image-o<pid>-<启动标识>-<32hex>.partial` | `JELEE_IMAGE_TEMP_ROOT` | 每个图片请求 |

ffprobe 不会被解包到磁盘：它以镜像里固定的路径，加上已验证的 inode 执行；sandbox helper 和 ignore helper 都是重新执行本程序。Compose 的 `/tmp` 是 64 MiB 的 tmpfs（`noexec,nosuid,nodev`），容器重建时就会清空。

**启动清扫（G09.4）。** `runtime.New` 在建立任何服务以前，调用 `internal/platform/scratch.Sweep` 清扫 `os.TempDir()`；启用图片时也清扫 `JELEE_IMAGE_TEMP_ROOT`。只有同时满足下列所有条件的对象才会被删除：

1. 位于该根目录的第一层，名称与上表某个固定前缀＋`o<pid>-<启动标识>-<32 小写 hex>`＋后缀**完全一致**；旧版 `MkdirTemp` 产生的 `jelee-service-probe-<数字>` 不符合，永远不会被删。
2. 类型正确：目录类必须是真目录，`.partial` 必须是普通文件。symlink 一律视为外来对象，不跟随也不删除。在 Linux 上，拥有者还必须是当前的 effective UID。
3. **能证明拥有者进程已经结束**：
   - Linux 的启动标识是 `b<boot_id 前 16 hex>n<PID namespace inode>t<starttime ticks>`。以下情况判定为已结束：boot_id 不同（之前那次开机）；或 `/proc/<pid>` 不存在；或 starttime 不同（PID 已被重用）。PID namespace 不同（例如其他容器共用同一个 `/tmp`）、`/proc` 读不到、启动标识是 `u`（建立时读不到身份）时，一律**保留**。
   - Windows 用 `GetProcessTimes` 的建立时间。`OpenProcess` 返回 `ERROR_INVALID_PARAMETER`（PID 不存在）、建立时间不同或已经有 exit code，才判定为已结束；权限不足就保留。
   - 其他平台一律保留。

删除全程使用 `os.Root`：先确认根目录不是 symlink，并且开启前后是同一个对象；每个残留目录再以自己的 `os.Root` 打开，并核对 identity。之后逐层删除，symlink 只删链接本身，不会跟随到根目录外或残留目录外。

单次清扫的上限：检查 4096 个第一层项目、删除 64 个残留、所有残留树内合计 65536 个项目、深度 16 层、5 秒。超过上限就停下来，标记为 incomplete，剩下的留给下一次启动。错误不会阻止启动，只写一条日志：区域（`process`／`images`）、各类计数和固定原因码（例如 `scratch_root_unavailable`），日志里不含任何路径或文件名。

运维如果要手动清理旧版残留或 `.testdata/`，请在服务停止时进行。
