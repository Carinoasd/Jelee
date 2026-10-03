# 執行時、連線池與共享工作指標

設定 `JELEE_ENABLE_ACCOUNTS=true` 及 `JELEE_ENABLE_METRICS=true` 後，管理員可用有效 Bearer session 讀取 `GET /metrics`。兩者預設關閉；JSON 設定對應 `enableAccounts` 與 `enableMetrics`。啟用指標但沒有帳戶功能或 exporter 時，服務拒絕啟動。

端點回傳 Prometheus exposition，沿用 no-store 與安全標頭。每次重新查驗 session 及管理員身分，撤銷、過期、停用或降權立即生效；不接受 query 參數或 query token。認證與收集合計最多兩個並行請求，滿載回 503／Retry-After: 1，context 與 write deadline 最多三秒。認證依賴資料庫，資料庫不可用時會回錯誤，這個端點不能代替獨立的存活檢查。

## 指標契約

指標由正式 OTel instruments／SDK 與私有 Prometheus exporter 產生。每個服務實例獨立註冊，不使用全域 registry，不增加背景輪詢。每次通過認證及收集閘門的請求先用單一 SQL 讀取工作快照，context 最多兩秒，包含等候連線；不取得 jobs 寫入鎖。之後 SDK callback 各取一次 runtime 與本機 pgxpool 快照，Producer 只讀本輪已準備的工作數據，兩者不執行 SQL。

下表 15 個指標都是目前程序的值，沒有 labels。Counter 是程序或連線池建立後的累計值；讀取多次不會重複加總，重啟可歸零。多副本以各自的 scrape target 區分，不能當作共享資料庫工作計數。

| Prometheus 名稱 | 型別 | 意義 |
| --- | --- | --- |
| `jelee_runtime_heap_bytes` | gauge | 目前配置中的 heap bytes |
| `jelee_runtime_goroutines` | gauge | 目前 goroutine 數 |
| `jelee_runtime_allocated_bytes_total` | counter | 程序累計配置 bytes，使用 `rate(...[5m])` 計算配置速率 |
| `jelee_runtime_gc_cycles_total` | counter | 已完成 GC 次數 |
| `jelee_runtime_gc_pause_seconds_total` | counter | 累計 GC 暫停秒數；尚非 histogram／P99 |
| `jelee_db_pool_connections_acquired` | gauge | 已借出的連線 |
| `jelee_db_pool_connections_idle` | gauge | 閒置連線 |
| `jelee_db_pool_connections_constructing` | gauge | 建立中的連線 |
| `jelee_db_pool_connections_total` | gauge | 連線總數，含建立中 |
| `jelee_db_pool_connections_max` | gauge | 設定上限；使用率由 acquired/max 求得 |
| `jelee_db_pool_acquire_success_total` | counter | 成功取得連線次數 |
| `jelee_db_pool_acquire_duration_seconds_total` | counter | 成功取得連線的累計耗時 |
| `jelee_db_pool_acquire_canceled_total` | counter | 因 context 取消而失敗的取得次數 |
| `jelee_db_pool_acquire_empty_total` | counter | 曾等待空池且最後成功的次數 |
| `jelee_db_pool_acquire_empty_wait_seconds_total` | counter | 上述成功等待的累計秒數，不含取消的等待 |

## 共享工作指標

`NewWithJobs` 增加以下 7 個指標家族；正式服務使用 `NewWithResources`，另增加[共用資源契約](shared-work-budget.md#共用資源指標)中的八個 gauge。工作功能在本副本關閉時仍可監測同一資料庫中的其他 worker。每次抓取直接輸出資料庫絕對值；不重播歷史事件、不在程序內再次累加。舊 `New` 建構器保留上述 15 個本機系列。

| Prometheus 名稱 | 型別 | 意義 |
| --- | --- | --- |
| `jelee_jobs_shared_queued` | gauge | 目前排隊數 |
| `jelee_jobs_shared_running` | gauge | 租約有效的執行數 |
| `jelee_jobs_shared_expired_running` | gauge | 租約已過期的執行數；收集不接手工作 |
| `jelee_jobs_shared_oldest_queued_age_seconds` | gauge | 最舊排隊工作的年齡，無排隊工作時為零 |
| `jelee_jobs_shared_outcomes_total` | counter | 成功、失敗、取消總數 |
| `jelee_jobs_shared_initial_wait_seconds` | histogram | 提交至首次開始的等待時間 |
| `jelee_jobs_shared_duration_seconds` | histogram | 首次開始至終態的耗時，包含重排間隔 |

固定 labels 為 `kind`（catalog_import／inventory_scan／nfo_write）、`priority`（background／manual）；outcomes 另有 `outcome`（succeeded／failed／cancelled）。所有零值組合也會輸出。Histogram 的上界與計數語意見[持久工作統計](job-metrics.md)。OTel 接收互斥桶並標記 cumulative；官方 exporter 轉成 Prometheus 累積 le 桶。

schema47工作指標固定222個series，連同本機指標共22個families／237個series，classic exposition上限測試維持64KiB。另有八個資源gauge，包含資源的端點共30個families。沒有job、媒體庫、使用者、路徑、owner、DSN或schema labels。OTel counter／histogram的StartTime使用資料庫epoch；目前classic exporter不輸出`_created`，不能從文字端點讀取該epoch。nfo_write正式准入與worker仍未啟用，驗證見[任務資料契約](nfo-write-jobs.md)。

同一資料庫的多個服務副本共享工作累計，不能把副本數值相加。部署時優先選一個收集目標；若需多目標，先以部署端設定的有限 cluster 維度去重，再計算速率，histogram 的 bucket／sum／count 需使用相同選擇規則。不同副本的抓取時間不同，跨副本取 max 也不等於單一資料庫快照。程序重啟或清理工作歷史不使累計歸零；資料庫還原造成的下降照實輸出，由監控端處理 counter reset。

預讀查詢、取消、逾時或快照驗證失敗時回安全 503，不輸出舊值或只剩本機指標的部分結果。私有 gather guard 核對本輪 Producer 收集及完整工作家族；內部收集不完整時，由官方 HTTP exporter 回安全 500。每次成功重試都重新讀取資料庫。

## 收集與關閉

Exporter 關閉 target_info、scope_info 及 resource constant labels，也不註冊預設 Go／process collectors。SDK 仍可能讀入 `OTEL_RESOURCE_ATTRIBUTES`；本端點不輸出這些屬性，固定 `service.name=jelee` 不能被解讀為 SDK 內完全沒有環境屬性。未來 OTLP／trace 出口需另行制定過濾契約。

真 gather 每次只允許一個；另一個已通過認證的請求在可取消的閘門等待。正在執行的本機同步快照無法被 request deadline 強制中止，不使用 detached gather goroutine。

Fx 的單一資源擁有者在 HTTP／worker 結束後關閉 metrics，再關閉 pool；圖建構及啟動失敗也走相同清理。Exporter shutdown 先拒絕新抓取，再等待已接納的資料庫預讀及本機快照結束；進入 SDK shutdown 前釋放收集閘門，停止後不再讀 pool。若第一次清理超時，資源擁有者保留 pool 並等待清理完成；停止仍失敗時回報錯誤並保留 pool。

## 相依與驗收範圍

固定 OTel API／SDK `v1.47.0`、Prometheus exporter `v0.69.0`、client_golang `v1.24.1`，組合依上游 exporter 的 go.mod 選定；相依由 go.mod／go.sum 記錄。OTel 與 client_golang 採 Apache-2.0，來源見 [OTel release](https://github.com/open-telemetry/opentelemetry-go/releases/tag/v1.47.0)、[exporter go.mod](https://github.com/open-telemetry/opentelemetry-go/blob/exporters/prometheus/v0.69.0/exporters/prometheus/go.mod)、[OTel 授權](https://github.com/open-telemetry/opentelemetry-go/blob/v1.47.0/LICENSE)及 [Prometheus client 授權](https://github.com/prometheus/client_golang/blob/v1.24.1/LICENSE)。原專案 LICENSE 保留。

原本 runtime／pool 段驗證：Windows 576 個通過事件、Linux race 570、真 PostgreSQL race 5。Windows 略過 7 個依賴原生環境／DB 的案例；Linux race 略過 1 個 DB 案例，該案另由真 PG 執行通過。事件數含父測試。vet、三個命令 build、模組 checksum、增量品牌、gitignore 與格式檢查通過；全量品牌仍失敗。見[執行證據與來源雜湊](evidence/metrics.json)。工作系列已接入同一端點；目前等待與耗時只涵蓋兩種工作，其他需求中的任務尚未全部實作及觀測，G41.8 保持部分完成。後續 G42.8 的可覆寫配置與容器 OOM 驗收見[執行時記憶體設定](runtime-memory.md)，G42.9 的固定 RSS 基線、預算及獨立門禁見[常駐記憶體預算](resident-memory.md)。Tracing 及 24h 驗收仍未完成。

## 共享工作端點驗證

本段 Windows 518 個通過事件／9 略過，Linux race 512／3 略過，真 PostgreSQL／HTTP race 10／0 略過；事件包含父測試，分平台列出，不相加。Windows 的原生環境案例未在本段重跑；三個資料庫頂層案例均由真 PG 執行通過。固定 22 家族／163 系列，測得最大 20,353 bytes，小於 64 KiB 限額。

vet、三命令 build、模組 checksum、格式、增量品牌與 gitignore 通過；全量品牌仍有 14,735 項。817 份來源在各輪維持不變，90 份已發布 SQL、五份直播核心、授權及需求原文不變。見[端點驗證證據](evidence/jobs-exporter.json)。
