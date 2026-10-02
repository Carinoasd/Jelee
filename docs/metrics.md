# 執行時與連線池指標

設定 `JELEE_ENABLE_ACCOUNTS=true` 及 `JELEE_ENABLE_METRICS=true` 後，管理員可用有效 Bearer session 讀取 `GET /metrics`。兩者預設關閉；JSON 設定對應 `enableAccounts` 與 `enableMetrics`。啟用指標但沒有帳戶功能或 exporter 時，服務拒絕啟動。

端點回傳 Prometheus exposition，沿用 no-store 與安全標頭。每次重新查驗 session 及管理員身分，撤銷、過期、停用或降權立即生效；不接受 query 參數或 query token。認證與收集合計最多兩個並行請求，滿載回 503／Retry-After: 1，context 與 write deadline 最多三秒。認證依賴資料庫，資料庫不可用時會回錯誤，這個端點不能代替獨立的存活檢查。

## 指標契約

指標由正式 OTel instruments／SDK 與私有 Prometheus exporter 產生。每個服務實例獨立註冊，不使用全域 registry，不增加背景輪詢。每次收集各取一次 runtime 與本機 pgxpool 快照；收集不取得資料庫連線、不執行 SQL，也不取得 jobs 鎖。

所有指標都是目前程序的值，沒有動態 labels。Counter 是程序或連線池建立後的累計值；讀取多次不會重複加總，重啟可歸零。多副本以各自的 scrape target 區分，不能當作共享資料庫工作計數。

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

Exporter 關閉 target_info、scope_info 及 resource constant labels，也不註冊預設 Go／process collectors。SDK 仍可能讀入 `OTEL_RESOURCE_ATTRIBUTES`；本端點不輸出這些屬性，固定 `service.name=jelee` 不能被解讀為 SDK 內完全沒有環境屬性。未來 OTLP／trace 出口需另行制定過濾契約。

真 gather 每次只允許一個；另一個已通過認證的請求在可取消的閘門等待。正在執行的本機同步快照無法被 request deadline 強制中止，不使用 detached gather goroutine。

Fx 的單一資源擁有者在 HTTP／worker 結束後關閉 metrics，再關閉 pool；圖建構及啟動失敗也走相同清理。Exporter shutdown 與正在讀取的快照同步，停止後不再讀 pool。若第一次清理超時，資源擁有者保留 pool 並等待清理完成；停止仍失敗時回報錯誤並保留 pool。

## 相依與驗收範圍

固定 OTel API／SDK `v1.47.0`、Prometheus exporter `v0.69.0`、client_golang `v1.24.1`，組合依上游 exporter 的 go.mod 選定；相依由 go.mod／go.sum 記錄。OTel 與 client_golang 採 Apache-2.0，來源見 [OTel release](https://github.com/open-telemetry/opentelemetry-go/releases/tag/v1.47.0)、[exporter go.mod](https://github.com/open-telemetry/opentelemetry-go/blob/exporters/prometheus/v0.69.0/exporters/prometheus/go.mod)、[OTel 授權](https://github.com/open-telemetry/opentelemetry-go/blob/v1.47.0/LICENSE)及 [Prometheus client 授權](https://github.com/prometheus/client_golang/blob/v1.24.1/LICENSE)。原專案 LICENSE 保留。

本段驗證已通過：Windows 576 個通過事件、Linux race 570、真 PostgreSQL race 5。Windows 略過 7 個依賴原生環境／DB 的案例；Linux race 略過 1 個 DB 案例，該案另由真 PG 執行通過。事件數含父測試。vet、三個命令 build、模組 checksum、增量品牌、gitignore 與格式檢查通過；全量品牌仍失敗。見[執行證據與來源雜湊](evidence/metrics.json)。尚未提供工作佇列／耗時／取消等持久計數、tracing、GOGC 配置、容器 OOM／記憶體預算或 24h 驗收；G41.8 與 G42.9 僅完成指標子集，G42.8 的配置及容器驗收尚待實作。
