# 第 3A 段：持久任务与只读盘点

设置 `JELEE_ENABLE_ACCOUNTS=true` 和 `JELEE_ENABLE_JOBS=true` 后启用任务。默认关闭；`/api/v1/system` 的 `inventoryScan` 表示开关状态，`probe` 仍为 false。任务仅支持 `inventory_scan`，观察普通文件的相对路径、大小、mtime 与扩展名分类，不创建完整媒体条目。扫描器不读取媒体内容、不执行外部工具。

## 注册与触发

本地受信任数据库操作者先通过 CLI 注册存在且可读的目录：

```sh
./bin/jelee-cli library add --name Movies --root /absolute/local/media
```

输出包含 library/root UUID，不含绝对路径。HTTP 不能提供媒体根或程序参数；所有任务操作须有效管理员会话，事务内再次验证会话与角色。配置目录及其祖先必须由可信操作者管理；支持范围和文件系统限制见[扫描器说明](scanning-filesystem.md)。

| 方法与路径 | 输入 | 结果 |
| --- | --- | --- |
| GET `/api/v1/libraries` | cursor/limit | 库摘要分页，不暴露根路径 |
| POST `/api/v1/libraries/{id}/scan` | JSON `{}` 或 priority manual/background；Idempotency-Key | 202 + Job + Location；重放 200 + Idempotency-Replayed |
| GET `/api/v1/jobs` | cursor/limit/state | 全局保留任务分页 |
| GET `/api/v1/jobs/{id}` | 无查询参数 | Job 状态与实际计数 |
| GET `/api/v1/jobs/{id}/entries` | cursor/limit | 普通文件观测分页，未完成任务结果可能变化 |
| POST `/api/v1/jobs/{id}/cancel` | JSON `{}` | 排队任务立即取消；执行任务持久设置取消标志 |
| POST `/api/v1/jobs/{id}/retry` | JSON `{}`；Idempotency-Key | 对失败/取消任务建立新的完整扫描，202；同 key 重放 200 |

页大小 1–100，默认 50，UUID 游标按 UUID 排序。body 上限 64 KiB，拒绝未知/重复键、null、路径输入与未知查询参数。key 为 1–128 字节 ASCII `!` 至 `~`，不含空白；同一 actor/key 仅在历史保留期间重放；改变库、priority 或重试来源返回 409。历史清理后原 key 可以建立新任务。提交成功后任务独立于请求连接；断线后重送相同提交请求及 key 取得幂等结果，取消走明确 cancel 操作。

库已有 queued/running 任务返回 `job_busy`/409；全局 queued+running 达到限额返回 `job_queue_full`/429；单进程 HTTP 名额满返回 `jobs_busy`/503，均不会建立无限等待队列。错误四语翻译沿用账户保存语言。

## 总数、百分比与 ETA（G13.3）

列表与详情的每个 Job 带 `progress` 对象（迁移 000101，暂定编号）：

| 字段 | 含义 |
| --- | --- |
| `done` | 已完成的工作量。`inventory_scan` 按已完成目录计（文件是扫描中才发现的，不是待处理清单）；其他种类按已处理条目计，即 `files+skipped` |
| `total` | 同一单位的总量；未知时为 null |
| `totalKnownAt` | 总量确定的时间；未知时为 null |
| `percent` | `done/total`，一位小数、向下取整、上限 100；没有总量时为 null |
| `etaSeconds` | 预计剩余秒数；只有 `etaBasis` 为 `throughput` 时才有值，否则为 null |
| `etaBasis` | 给出或不给 ETA 的依据，见下表 |

各种类的总量来源：

| 种类 | total |
| --- | --- |
| `inventory_scan` | 扫描期间目录树仍在展开，总量未知；所有已发现目录都完成（`directories` 等于目录总数）时才确定，此后只剩探测、发布等后续阶段，不另估 |
| `catalog_import`、`nfo_write`（含导出产生的子任务） | 提交时的条目／写回意图数，固定不变，是精确值 |
| `nfo_apply`、`nfo_export` | 提交时媒体库的条目数，是估计值：运行期间新增或删除条目不会更新它 |
| `image_refresh` | 提交时符合刷新条件（远程、无内容、未锁定、非手动）的图片行数，同为估计值 |
| `catalog_sync`、`consistency_check` | 多阶段工作没有统一的工作量单位，始终为 null |
| `external_metadata_removal` | 提交时有 TMDB 字段或自动配对待确认记录的条目数，估计值（见[维护任务](maintenance-jobs.md)） |
| `subtitle_cache_cleanup` | 第一次执行时列出的缓存来源目录数，估计值；容量阶段不另计 |
| `backup_reminder` | 固定为 1 |

ETA = 剩余工作量 ÷ 近期吞吐量。吞吐量是检查点之间「新完成数 ÷ 经过秒数」的指数移动平均（新样本权重 0.3），由 `jobs` 上的 BEFORE UPDATE 触发器在既有检查点更新同一行时计算，不增加任何语句；相隔不到 50 ms 的检查点累积到下一个样本。任务不在 running、被新的租约 generation 领取，或完成数倒退（扫描恢复）时，采样起点重置，所以暂停、排队与恢复时间不会算进吞吐量。

| `etaBasis` | 何时出现 |
| --- | --- |
| `throughput` | 运行中、有总量、至少 3 个样本、最近样本不超过 2 分钟；ETA 至少 1 秒、最多 30 天 |
| `total_unknown` | 运行中但没有总量 |
| `insufficient_samples` | 样本少于 3 个或吞吐量不为正 |
| `not_started` | 排队且从未开始 |
| `paused` | 开始过但目前回到队列（计划暂停、让出、租约过期恢复） |
| `cancelling` | 已请求取消，剩余工作不会执行 |
| `stalled` | 运行中但超过 2 分钟没有新样本，旧吞吐量不再代表现状 |
| `total_reached` | 完成数已达到总量：估计值偏小，或只剩不计量的后续阶段 |
| `too_slow` | 估计会超过 30 天 |
| `finished` | 已成功、失败或取消 |

迁移只在 `jobs` 增加 `progress_total`、`progress_total_at`、`rate_ema`、`rate_samples`、`rate_mark_at`、`rate_mark_done` 六列与触发器 `jobs_progress_rate`。写入仍只发生在 lease fence 保护的检查点内；状态读取以数据库时钟计算最近样本的年龄，避免应用与数据库时钟偏差。降级只丢掉估计值，不影响任务本身；升级前已提交的任务总量为 null。任务指标（`job_metric_*`）维度不变。

## 状态、恢复与缺失保护

状态为 queued → running → succeeded/failed/cancelled。计数 files/directories/skipped/bytes 是已经保存的实际观测，directories 包括根 `.`。总数、百分比与 ETA 见下一节；未知时返回 null，不返回猜测值。

每个 run 保存配置快照；worker 租约使用数据库时钟和 generation，过期 owner 不能续约、提交或结束任务。恢复保留已完成目录，清掉未完成目录的局部结果后从头读取，防止重复计数及保留消失的局部文件。恢复达到尝试上限会失败；主动 retry 建立新 run。

只有完整成功、零 skipped 的扫描能比较上一份完整基准。skipped>0 时 missing=0 且 reviewRequired=true，并保留基准。缺失数或比例达到阈值时同样保留基准，连续重扫仍会显示缺失警告。管理员可用 `POST /api/v1/jobs/{id}/accept-missing` 明确接受缺失并发布该次观测为新基准，详见[目录同步](catalog-sync.md)。未触阈值的完整观测可替换基准。失败、取消、根失联和半次扫描不会产生删除依据；本段完全不删除原文件或 catalog。

每库基准最多保存一次完整 run 的 MaxEntries；HistoryLimit 是全局终态 run 上限。队列、每 run 文件/目录、worker、HTTP 准入与请求/DB 期限均有限额，详见[worker](jobs-worker.md)。既有 libraries/audit_logs 未增加全局总量保留策略，不宣称整个数据库恒定大小。

## CLI 与配置

`jobs scan|list|libraries|get|entries|cancel|retry` 调用相同 HTTP API。必须提供 `--token-stdin`，token 不放 argv；标准输入读取一个 43 字符令牌和可选行尾后应结束输入。HTTP 只允许明确 loopback IP，远程必须 HTTPS。默认 URL 为 `http://127.0.0.1:8097`，禁止重定向和环境代理，使用 15 秒 context 期限，响应最多 1 MiB。

CLI 仅输出重新编码的公开 JSON 字段，丢弃未知字段；`progress`（total、percent、etaSeconds、etaBasis）原样输出，缺少时视为旧服务器，存在时检查一致性（例如没有 total 却有 ETA、percent 超出 0–100 或未知的 basis 都会失败）。HTTP 200/202 仍须包含非 null 的 `data` 及完整有效的 Job 或分页；空对象、非法 UUID/状态/错误码、超出请求页大小的数组、不一致的页大小和非法相对路径都会失败。`get`、`scan`、`cancel`、`retry` 输出 `data` 中的 Job；分页命令输出 `data.jobs`、`data.entries` 或 `data.libraries` 及 `data.pagination`。

退出码 0 表示本次 API 调用及响应验证成功；`scan`/`retry` 的 0 表示任务已接受或幂等重放，不表示后台扫描已完成。退出码 2 表示命令用法或参数错误；其他请求、读取、验证、输出、取消或超时失败返回 1。`jobs get` 成功读取一个 failed 状态的 Job 仍返回 0，调用者须检查 `data.state`。

```sh
# 通过受限管道提供令牌；每次独立调用读取一个令牌。
./bin/jelee-cli jobs scan --id LIBRARY_UUID --key SCAN_KEY --token-stdin
./bin/jelee-cli jobs get --id JOB_UUID --token-stdin
./bin/jelee-cli jobs cancel --id JOB_UUID --token-stdin
```

以上为 Linux 构建产物；Windows 使用 `.\bin\jelee-cli.exe`。`scan --id` 指定库 UUID，其他带 `--id` 的命令指定任务 UUID。`list`、`entries`、`libraries` 支持 `--cursor UUID --limit 1..100`，`list` 另支持 `--state`；`scan` 和 `retry` 必须提供 `--key`。

`.env.example` 列出全部配置。默认 worker=2、pool=8、队列=100、全局历史=20、文件=100000、目录=10000、尝试=3；租约=30 秒、DB 操作=2 秒、poll=250 ms、每次尝试最大运行=3600 秒；缺失阈值数量100或比例20%。pool 必须至少 worker+2；连接由 HTTP 与 worker 共享，不是专属保留。系统允许的较大上限不代表该规模已完成性能验收。

停止时立即取消服务 context，并行 drain HTTP 和 join workers，最后释放池；超时强制关闭 HTTP 并报告错误。底层文件系统调用若仍阻塞，会保留池到 worker 真正退出；不能把此状态报告为干净关闭。关掉功能开关后不启动 worker/路由，原 queued run 保留到再次启用。

## 迁移与回滚

任务表由 migration 000003 引入；当前 binary 启动要求 schema 4 clean。部署先备份，再 `jelee-migrate up`。关闭任务开关是首选回滚方式。`down --i-understand` 每次只回退一版：004 down 丢失 probe cache/phase，保留任务/盘点/基准；再回退003才删除任务/盘点/基准表。必须停止相应 worker 并配合接受该 schema 的 binary，生产数据回退须单独规划。000001–000003 保持不变，详见[快取回滚](probe-cache.md#升级与回滚)。

本段未实现 ffprobe、快取、增量指纹、忽略规则、cron/fsnotify、人工基准确认、图片/NFO解析或目录级并发，不能视为完整 G13/G19/G41/G42 验收。
