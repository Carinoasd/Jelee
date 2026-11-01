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

## 背景工作

Webhook 投遞、觀看統計彙總、播放進度 flush（G41.1、G41.3、G41.8）與字幕 OCR 派送（第 13 階段 F5）原本是 runtime 或子系統各自起的背景迴圈，現在是任務執行器的**背景工作**：`internal/platform/jobs.Background` 擁有每個種類的一個 goroutine（與服務同生命週期），每次執行前先做准入（叢集鎖、共用配額）並記錄指標；步調（間隔、喚醒、停止寬限）由 `app.RunBackground` 負責，各子系統只提供「做一次」的內容（`WebhookDispatcher.Task`、`WatchStats.Task`、`Progress.Task`、`subtitleocr.Service.Task`）。runtime 只啟動這一條背景工作線，關機時依序等待：先等 flush 工作停下再做最後一次 flush，再等其餘背景工作（含字幕 OCR 進行中的工作被取消並返回），最後才關連線池與 OCR 服務。

| 種類 | 範圍 | 間隔（沿用原設定） | 批量（沿用原設定） | 共用配額 | 提前執行 |
| --- | --- | --- | --- | --- | --- |
| `webhook_delivery` | 叢集單例 | 一輪結束後閒置 `webhooks.pollMilliseconds`（預設 1 秒）；啟動時立刻一輪；整批滿了立刻下一輪 | 每輪 `webhooks.batch`（50）、並行 `webhooks.concurrency`（4）、租約 `webhooks.leaseSeconds`（120） | 每次投遞嘗試一個 I/O（輪次本身不持有） | `Wake` |
| `watch_stats_aggregation` | 叢集單例 | 固定節奏 `stats.aggregateSeconds`（60 秒），第一次在一個間隔後 | 每批 200 個會話、每次最多 50 批；每小時一次保留期清理 | 每次執行一個 I/O | 會話結束後延遲 5 秒，期間的喚醒合併 |
| `playback_progress_flush` | 每個實例 | 固定節奏 `playback.flushSeconds`（10 秒）；每 6 次成功 flush 做一次維護（關閉遺留會話、保留期清理） | `playback.maxBatch`（500） | 每次執行一個 I/O | dirty 會話達 `maxBatch` 時立刻 |
| `subtitle_ocr` | 叢集單例 | 一次執行結束後閒置 5 秒（`subtitleocr.DispatchPoll`，只用來重試被跳過的執行）；啟動時立刻一次；佇列還有來源就立刻下一次 | 每次一個來源（佇列 `subtitleOcr.queueSize`，預設 16；每個工作最長 4 小時） | 執行本身不持有；每張圖一個 CPU（沿用原本的逐圖准入） | 有來源排入佇列時立刻 |

沒有新增設定，也沒有遷移：四者都不寫 `jobs` 表。字幕 OCR 原有設定（佇列大小、並發、每分鐘圖片數、快取上限）語意不變。

### 為什麼不是 `jobs` 表的任務

`jobs` 表的任務掛在一個媒體庫上，而一個媒體庫同時只能有一個進行中的任務（[維護任務](maintenance-jobs.md#執行個體層級任務掛在哪個媒體庫)）；排程最短間隔是 60 秒（[任務排程](job-schedules.md)）。Webhook 每秒輪詢、進度每 10 秒 flush，若每次都排一個任務，`jobs` 表一天多出十萬列，而且會不斷佔住錨點媒體庫，讓那個庫的掃描與手動任務收到 `409 job_busy`；輪詢間隔也只能放寬到 60 秒，違反「間隔語意不變」。字幕 OCR 同理（與[擁有者驗證清單](owner-verification-queue.md) E19 的理由相同）：它的佇列在實例記憶體裡、同一來源修訂只排一次、滿了丟棄並由下一次查詢重排，觸發來自每次讀播放資訊；若改成 `jobs` 列，每次查詢都要寫資料庫、在錨點媒體庫上與掃描搶唯一的任務位，一個來源最長 4 小時的辨識會讓該庫的掃描整段收到 `409 job_busy`。所以四者沿用任務模型的其餘部分，不佔佇列列：

- **種類白名單**：`domain.BackgroundJobKinds()` 只有上表四種；執行器拒絕其他名稱，每種只能註冊一次。它們不在 `GET /api/v1/jobs` 的 `kind` 列舉裡，也不能用 `/api/v1/schedules` 排程。
- **優先序**：一律 `background`（指標的 `priority` 維度）。
- **單例**：叢集單例的種類每次執行前以 `pg_try_advisory_lock(hashtext(current_schema()), 17481268／17481269／17481270)`（Webhook 投遞／統計彙總／字幕 OCR） 取 session 層級的建議鎖，拿不到（另一個實例正在跑）就跳過這一次（`skipped`），由下一次再試；鎖在這次執行期間佔用一條池連線，結束時解鎖並歸還，解鎖失敗就關掉那條連線（鎖隨 session 釋放），實例崩潰時同樣由資料庫釋放。統計彙總原本交易內的 `pg_try_advisory_xact_lock` 保留，修復工具仍可用它擋住彙總。
- **字幕 OCR 單例**：OCR 是服務中最重的 CPU 工作，叢集同時只辨識一個來源。每次執行只取本實例佇列最舊的一個來源，結束就解鎖，所以持有者的佇列清空後其他實例在下一次輪詢（5 秒內）或下一次排入時接手；被跳過的執行不取出來源，佇列順序、去重、10 分鐘失敗退避與丟棄計數都不變。持有者佇列一直有來源時會連續執行，其他實例的來源在那段期間等待（不遺失，滿了照舊丟棄並由下一次查詢重排）；辨識期間佔一條池連線。佇列與快取仍是各實例自己的。
- **每個實例**：進度緩衝在各實例的記憶體裡，只有該實例能寫，所以 flush 不能是單例；跨實例的遺留會話清掃本來就只關閉資料庫中逾時、本實例沒持有的會話，多實例同時做也不衝突。
- **取消**：工作隨服務生命週期取消：停止後不再開始新的一次；Webhook 進行中的投遞嘗試有 `StopGrace`（5 秒）完成並記錄，逾時才取消，未記錄的投遞在租約到期後以同一個 `eventId` 重送；統計與 flush 的執行直接取消，flush 在 HTTP 排空後再以 10 秒期限做最後一次；字幕 OCR 進行中的工作直接取消（沒有停止寬限，與原本關閉服務時相同），staging 與工作檔刪除、不記為失敗也不進退避，佇列中的來源丟棄。
- **租約**：Webhook 的每筆投遞仍是原本的資料庫租約（`webhooks.leaseSeconds`）；叢集鎖本身就是這次執行的租約，持有者連線中斷即失效。字幕 OCR 沒有另外的租約或心跳：鎖隨 session 存在，實例崩潰時資料庫釋放，工作本身另有 4 小時上限。

### 共用配額（G41.3）

- Webhook：投遞器在**簽章之前**為每次嘗試取得一個 I/O 配額，請求結束就釋放（不涵蓋寫回嘗試紀錄）；投遞器用的 HTTP client 不再自己取配額，避免巢狀。等待最多到「租約到期 − 端點逾時 − 10 秒」，保證嘗試與紀錄仍在租約內完成；佇列滿或等不到時**不記錄嘗試**（不計入重試次數、不會因背壓進死信），租約到期後重送。管理員的測試送出仍由它自己的 client 取配額。
- 字幕 OCR：執行本身不取配額，每張圖在辨識前取一個 CPU 配額（原本的做法），等配額的時間算在該次執行的 `duration` 內，並反映在 `jelee_resources_waiting`；共用配額禁止巢狀，所以執行層不能再持有一個。
- 統計彙總與進度 flush：每次執行在開始前取得一個 I/O 配額（資料庫讀寫為主；彙總的計算在交易內，若在交易中再等 CPU 配額會延長鎖的持有），最多等一個間隔，佇列滿或等不到就跳過這一次（`skipped`），留給下一次；flush 的狀態仍在記憶體，不會遺失。關機時的最後一次 flush 與停止回報的即時寫入不經配額。

### 指標（G41.8）

每個實例輸出四個背景工作家族，維度沿用工作指標的 `kind`、`priority`（固定 `background`）、`outcome`，再加 `skipped`：

| Prometheus 名稱 | 型別 | 意義 |
| --- | --- | --- |
| `jelee_jobs_background_running` | gauge | 本實例正在執行的次數（每種 0 或 1） |
| `jelee_jobs_background_runs_total` | counter | 執行次數，`outcome`＝succeeded／failed／cancelled／skipped |
| `jelee_jobs_background_wait_seconds` | histogram | 到期到真正開始（取得叢集鎖與配額之後）的等待 |
| `jelee_jobs_background_duration_seconds` | histogram | 一次執行的耗時 |

細節與數量見[指標](metrics.md#背景工作指標)。
