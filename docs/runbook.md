# 維運告警 Runbook（G50.6）

本頁對應 [`deploy/prometheus/jelee-alerts.yml`](../deploy/prometheus/jelee-alerts.yml) 的每一條預設告警。每節依序寫明**意義**（告警代表什麼）、**確認**（如何證實不是誤報）、**處置**（怎麼修）、**回復驗證**（怎麼確認已恢復）。告警名稱就是本頁的錨點，規則的 `runbook` 註解直接連到這裡。

## 啟用

1. 服務端設 `JELEE_ENABLE_ACCOUNTS=true`、`JELEE_ENABLE_METRICS=true`。`GET /metrics` 只給管理員的 Bearer 工作階段，權杖存成 Prometheus 機器上 0600 的檔案；工作階段到期（`accounts.sessionHours`）前要換新權杖，否則 `JeleeScrapeFailed` 會觸發。
2. 參考 [`deploy/prometheus/prometheus.example.yml`](../deploy/prometheus/prometheus.example.yml)：抓取 job 名稱必須是 `jelee`，`rule_files` 指向 `jelee-alerts.yml`。
3. 告警只用 `/metrics` 實際輸出的指標；`internal/platform/telemetry/alerts_test.go` 會解析規則檔，確認每個指標名稱、分組標籤與 runbook 小節都存在。版本內沒有固定 `promtool`，規則語法以該測試把關；有 `promtool` 的環境可再跑 `promtool check rules deploy/prometheus/jelee-alerts.yml`。

工作、Webhook、掃描、開發者模式與一致性的數值來自共用資料庫，每個副本輸出相同值，所以規則先取 `max()` 再比較；磁碟、記憶體、連線池與被拒請求是各副本自己的值。指標意義見[指標契約](metrics.md#維運告警指標g506)。

嚴重度：`critical` 需要立即處理；`warning` 當日處理；`info` 只提醒。

## 可用性

### JeleeScrapeFailed

- **意義**：Prometheus 連續 2 分鐘抓不到 `/metrics`。`/metrics` 每次都向 PostgreSQL 重新驗證管理員工作階段，所以**資料庫不可達**、服務停止、權杖過期或被撤銷都會讓抓取失敗。這條就是「DB 不可達」告警：資料庫斷線時程序內沒有任何指標能被抓到。
- **確認**：
  1. `curl -fsS http://服務位址/healthz`：失敗表示程序本身不在；成功則往下。
  2. `curl -fsS http://服務位址/readyz`：回 503 `not_ready` 表示資料庫連不到或 schema 不符。
  3. 在服務主機執行 `jelee-cli doctor --checks config,database,migrations`，看 `db_*`／`migration_*` 錯誤碼（意義與修復見[故障排查](troubleshooting.md#databasemigrations数据库连接与迁移)）。
  4. `/readyz` 正常但抓取仍失敗：用 Prometheus 的權杖手動 `curl -H "Authorization: Bearer …" /metrics`，401 表示權杖過期或被撤銷，503 `metrics_busy` 表示抓取太頻繁。
- **處置**：資料庫問題依 doctor 建議修復（服務、網路、密碼、`jelee-migrate up`）；權杖問題以管理員重新登入或 `jelee-cli provision --admin` 取得新權杖並更新憑證檔；程序不在則查容器日誌後重啟。
- **回復驗證**：`/readyz` 回 200，Prometheus target 狀態回到 UP，`up{job="jelee"}` 為 1 且告警自動解除。

### JeleeDatabasePoolSaturated

- **意義**：連線池全部借出，而且有請求等不到連線而放棄（`jelee_db_pool_acquire_canceled_total` 在增加）。使用者會看到逾時或 503。
- **確認**：查看 `jelee_db_pool_connections_acquired` 與 `jelee_db_pool_connections_max`；`rate(jelee_db_pool_acquire_empty_wait_seconds_total[5m])` 顯示等待時間。在資料庫端查 `pg_stat_activity` 是否有長時間執行或鎖等待的查詢；`jelee-cli diag export` 的 DB 統計可一併附上。
- **處置**：先找出佔用連線的原因（大量掃描、匯出、慢查詢、鎖）；暫時可取消大型工作（`jelee-cli jobs cancel`）。容量確實不足時調高 `JELEE_MAX_CONNECTIONS`（須同時符合資料庫 `max_connections` 與工作所需的保留量），再重啟服務。
- **回復驗證**：`rate(jelee_db_pool_acquire_canceled_total[5m])` 回到 0，`acquired` 低於 `max`，告警解除。

## 資源

### JeleeDiskSpaceLow

- **意義**：`volume` 標籤所指目錄的檔案系統可用空間低於 10%。`volume` 是設定鍵名，與 `jelee-cli doctor` 的磁碟檢查相同：`tempdir`、`images.tempRoot`、`images.storeRoot`、`logging.file`（日誌目錄）。
- **確認**：`jelee-cli doctor --checks disk` 會列出同一目錄的 `disk_space_low`／`disk_space_critical`；在主機上 `df -h` 該目錄。
- **處置**：
  - `images.storeRoot`：調低 `images.storeOriginalBytes`／`images.storeVariantBytes` 讓存放區淘汰（存放區是可重建的快取，不要手動刪除其中檔案），或擴充磁碟。
  - `images.tempRoot`／`tempdir`：確認沒有其他程序佔用；Jelee 的暫存檔在請求結束時清除，殘留可在服務停止後刪除。
  - `logging.file`：調低 `logging.file.maxBackups`／`maxSizeMB` 或改用外部日誌。
- **回復驗證**：`jelee_storage_available_bytes / jelee_storage_size_bytes` 回到 10% 以上，doctor 的 disk 檢查為 ok，告警解除。

### JeleeDiskSpaceCritical

- **意義**：可用空間低於 2% 或 256 MiB（與 doctor 的 critical 門檻一致）。圖片快取、暫存與日誌寫入即將失敗；日誌寫不進去時安全事件也會遺失。
- **確認**：同 [JeleeDiskSpaceLow](#jeleediskspacelow)。另查 `jelee_images_store_failures_total` 是否在增加。
- **處置**：立即釋出空間或擴充磁碟，方法同上；必要時暫停大量圖片抓取（關閉 `enableImages` 後重啟）。
- **回復驗證**：可用空間回到 2% 與 256 MiB 以上，`jelee_images_store_failures_total` 不再增加，告警解除。

### JeleeDiskWillFillIn24h

- **意義**：依最近 6 小時的減少速度，`volume` 所在檔案系統會在 24 小時內用完。
- **確認**：在 Prometheus 繪出 `jelee_storage_available_bytes{volume="…"}` 6–24 小時的走勢，判斷是持續成長（快取填滿、日誌暴增）還是一次性大量寫入。
- **處置**：持續成長就依 [JeleeDiskSpaceLow](#jeleediskspacelow) 調整上限；日誌暴增先查 `logging.level` 是否被調成 debug（開發者模式可能開了詳細日誌，見 [JeleeDevModeActive](#jeleedevmodeactive)）。
- **回復驗證**：走勢轉平，`predict_linear` 預測值回到正數，告警解除。

### JeleeMemoryNearLimit

- **意義**：Go heap 連續 10 分鐘高於 `GOMEMLIMIT` 的 90%。GC 會頻繁執行、延遲上升，容器記憶體上限較緊時可能被 OOM 終止。沒有設定 `GOMEMLIMIT` 時 `jelee_runtime_memory_limit_bytes` 為 0，這條不會觸發。
- **確認**：比較 `jelee_runtime_heap_bytes`、`jelee_runtime_memory_limit_bytes` 與 `rate(jelee_runtime_gc_cycles_total[5m])`；查看同時段的 `jelee_images_reserved_bytes`、`jelee_images_active` 與 `jelee_jobs_shared_running`，判斷是圖片轉檔、大型工作還是請求量。
- **處置**：依[執行時記憶體設定](runtime-memory.md)調整 `GOMEMLIMIT` 與容器上限（兩者一起改）；降低圖片並行（`images` 設定）或工作 worker 數（`JELEE_JOB_WORKERS`）。持續成長而不回落時以 `jelee-cli diag export`（開發者模式下含 pprof）收集資料後回報。
- **回復驗證**：heap 回到上限 80% 以下且 GC 頻率正常，告警解除。

## 工作

### JeleeScanConsecutiveFailures

- **意義**：至少一個媒體庫最近 3 次（含以上）盤點掃描都失敗，目錄不再更新。數值是保留歷史中各媒體庫結尾連續失敗次數的最大值；`jelee_scan_failing_libraries` 是最近一次掃描失敗的媒體庫數。
- **確認**：`jelee-cli jobs list --state failed --token-stdin` 找出失敗的 `inventory_scan` 與 `errorCode`；`scan_unavailable` 多半是根目錄不在或權限不足，`scan_limit` 是超過 `JELEE_SCAN_MAX_ENTRIES`／`JELEE_SCAN_MAX_DIRECTORIES`，`job_timeout` 是超過 `JELEE_JOB_MAX_RUNTIME_SECONDS`。`jelee-cli doctor --checks library_roots` 檢查根目錄。
- **處置**：修復根目錄掛載與權限（UID 65532 需可讀與遍歷）；上限不足就調高限制；逾時則延長執行時間或加開掃描並行（`JELEE_SCAN_DIRECTORY_CONCURRENCY`）。修好後 `jelee-cli jobs retry --id 失敗的工作 --key 新鍵 --token-stdin` 或手動掃描。
- **回復驗證**：該媒體庫下一次掃描成功，`max(jelee_scan_consecutive_failures)` 歸 0，告警解除。

### JeleeJobLeasesExpired

- **意義**：有執行中的工作持有已過期的租約超過 15 分鐘，表示持有它的 worker 停了而沒有其他 worker 接手（例如所有實例的 `enableJobs` 都關了，或 worker 卡死）。
- **確認**：`jelee_jobs_shared_expired_running` 依 `kind` 分組看是哪一類工作；`jelee_jobs_shared_running` 與 `jelee_jobs_shared_queued` 是否也停滯；查服務日誌中的 `job_lease_lost`、`scan_unavailable`。
- **處置**：確認至少一個實例 `JELEE_ENABLE_JOBS=true` 且正常運作；worker 卡住就重啟該實例。下一次領取時過期租約會被回收（重新排隊或依嘗試次數失敗）。`nfo_write` 帶有提交日誌的工作會轉為失敗並由恢復流程處理，見 [NFO 寫回](nfo-write-jobs.md)。
- **回復驗證**：`jelee_jobs_shared_expired_running` 歸 0，佇列繼續前進。

## 整合

### JeleeWebhookDeadLetters

- **意義**：有 Webhook 投遞用完重試次數成為死信，而且數量在最近一小時內增加。接收端漏收事件。
- **確認**：`GET /api/v1/webhooks` 與 `GET /api/v1/webhooks/{id}` 看各端點的 dead 數；`GET /api/v1/webhooks/{id}/deliveries?state=dead` 看最近的 `lastOutcome`／`lastStatus`：`http` 表示接收端回錯誤碼，`timeout`／`network` 表示連不到，`blocked` 表示目標被出站政策拒絕，`tls` 表示憑證問題。
- **處置**：修好接收端或網路後，`POST /api/v1/webhooks/{id}/test` 確認可達，再對需要的死信 `POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/replay`。接收端長期不用時停用該端點，避免持續累積。
- **回復驗證**：重放的投遞變成 `delivered`，`jelee_webhooks_deliveries_dead` 不再增加（一小時後 `delta` 為 0），告警解除。

### JeleeWebhookDeadLettersHigh

- **意義**：死信累積到 100 筆以上並持續 15 分鐘，表示問題已持續一段時間。
- **確認**：同 [JeleeWebhookDeadLetters](#jeleewebhookdeadletters)，並確認是否集中在單一端點。
- **處置**：同上；大量重放前先確認接收端能承受（重放會依序走一般投遞與重試）。不再需要的事件可以不重放，刪除端點會一併刪除其投遞紀錄。
- **回復驗證**：`jelee_webhooks_deliveries_dead` 降到 100 以下並不再增加。

## 安全

### JeleeClientBlockBurst

- **意義**：強制模式的客戶端控制規則（G47）每分鐘拒絕超過 100 個請求，持續 5 分鐘（G47.8 的批量被拒）。可能是被屏蔽的客戶端大量重試、攻擊，或新規則誤擋了正常客戶端。門檻與服務日誌中的 `client_control_block_burst` 一致。
- **確認**：`GET /api/v1/client-control/stats?hours=1` 看 Top 規則、Top UA 與 Top IP；`GET /api/v1/client-control/hits` 依規則篩選明細；確認最近是否新增或改成攔截模式的規則（稽核事件 `client_control.rule_created`／`rule_mode_changed`）。
- **處置**：誤擋就把規則改回觀察模式（`POST /api/v1/client-control/rules/{id}/observe`）或修正條件；真實攻擊則保留規則並在反向代理或防火牆層擋下來源。規則造成全面無法使用時用 `jelee-cli access reset-policies --i-understand` 緊急恢復（會停用所有規則，見[客戶端控制](client-control.md#緊急恢復g477)）。
- **回復驗證**：`sum(rate(jelee_client_control_blocked_total[5m])) * 60` 回到 100 以下，正常客戶端可以登入，告警解除。

### JeleeDevModeActive

- **意義**：開發者模式（G45）已開啟 30 分鐘。它的開關會放寬正式環境的保護（詳細日誌、SSRF 寬鬆、危險操作等），只應在除錯期間短暫開啟；工作階段最長 24 小時自動到期。
- **確認**：`jelee-cli devmode status` 顯示開啟時間、到期時間與開關；稽核事件 `devmode.enabled` 記錄是誰從哪裡開啟。
- **處置**：除錯完成就 `jelee-cli devmode disable`；不是計畫中的開啟就視為安全事件，檢查稽核與存取紀錄。
- **回復驗證**：`jelee-cli devmode status` 顯示未開啟，`jelee_devmode_active` 為 0，告警解除。

### JeleeDevModeLongRunning

- **意義**：開發者模式已開啟超過 8 小時，幾乎可以確定是忘了關。
- **確認**：同 [JeleeDevModeActive](#jeleedevmodeactive)。
- **處置**：立即 `jelee-cli devmode disable`，並確認 `dev.persistAcrossRestart` 沒有被設為 true。
- **回復驗證**：`jelee_devmode_active_duration_seconds` 歸 0，告警解除。

## 資料

### JeleeConsistencyFindings

- **意義**：某個媒體庫最近一次資料一致性檢查（G50.3）的 `check` 項目有發現：目錄、盤點基準、衍生表與實際檔案之間不一致。數值是每個媒體庫最新一次完成的檢查之發現數總和。各檢查與發現代碼的意義見[資料一致性檢查](consistency.md)。
- **確認**：`jelee-cli consistency report --library 名稱或ID` 看最新報告（`--json` 有樣本與識別碼）；需要即時結果就 `jelee-cli consistency check --library …`。
- **處置**：依[資料一致性檢查](consistency.md#發現與處置)該代碼的建議處理：多數是重新掃描與同步（`jelee-cli jobs scan … --probe --nfo`）；`version_count` 的跨條目版本參照與 `watch_stats_drift` 的計數漂移可以 `jelee-cli consistency check --fix` 修復（可逆、寫稽核，`jelee-cli consistency revert --run …` 還原）；刪除類（孤兒條目、孤兒記錄）一律人工決定。
- **回復驗證**：再跑一次 `jelee-cli consistency check`（或等下一次排程），該檢查的發現數為 0，`jelee_consistency_findings{check="…"}` 歸 0。

### JeleeConsistencyCheckStale

- **意義**：曾經跑過一致性檢查，但超過 8 天沒有任何一次完成。排程可能被關閉，或檢查工作一直失敗、被取消。
- **確認**：`JELEE_JOB_CONSISTENCY_INTERVAL_HOURS` 是否仍大於 0；`jelee-cli jobs list --token-stdin` 找 `consistency_check` 工作與其 `errorCode`；`jelee-cli consistency report` 看最新報告的 `state`。
- **處置**：恢復排程，或修正失敗原因（多半是資料庫逾時或媒體庫一直有其他工作在跑）後 `jelee-cli consistency enqueue --library …` 手動排一次。
- **回復驗證**：`jelee_consistency_last_completed_timestamp_seconds` 更新為最近時間，告警解除。
