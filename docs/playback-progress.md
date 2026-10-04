# 播放會話與觀看進度（G23.1、G23.2、G23.4、G20.4、G48.3、G07.7）

本文說明播放會話、續播點、已播放狀態的資料模型、寫入方式、自有 API 與相容層，以及「並發進度上報不造成寫放大」的實測證據。統計聚合（G23.3、G23.5）不在此範圍；`docs/watch-stats.md` 的計算規則直接讀本文的樣本表。

## 資料模型（遷移 000066）

| 表 | 一列代表 | 重點 |
| --- | --- | --- |
| `playback_sessions` | 一個使用者以一個播放鍵（play key）播放一個邏輯條目 | `UNIQUE(user_id, play_key)` 即會話去重；`state` 為 `active`／`stopped`／`failed`／`timed_out`，`ended_at` 只在非 active 時有值；`failure_reason` 只在 `failed` 時有值，限 `transcode_disabled`、`codec_unsupported`、`client_blocked`、`permission_denied`、`playback_error`；`delivery` 只能是 `direct`；記錄開始／最後回報／結束時間、位置、播放版本 `source_id`、版本時長 `runtime_ticks`、暫停、回報次數，以及開始時從登入工作階段複製的 `device_id`／`client_name`（客戶端自報標籤，不是證明） |
| `playback_samples` | 會話的一個保留樣本 | `(session_id, seq)` 主鍵；種類 `start`／`progress`／`pause`／`resume`／`seek`／`stop`／`fail`；每會話最多 4096 個；就是 `domain.ComputeWatchSession` 的輸入 |
| `user_item_data` | 一個使用者對一個**邏輯條目**的進度 | 續播點 `resume_ticks`、`played`、`play_count`、`last_played_at`、`last_source_id`（最後播放的版本）。進度歸屬條目而不是檔案（G20.4），同條目所有版本共用 |

- 外鍵：使用者刪除（硬刪）、條目刪除都級聯刪除三張表的資料；來源刪除只把 `source_id`／`last_source_id` 設為 NULL。
- 軟刪除使用者（`DELETE /api/v1/users/{id}`）在同一個交易裡刪除該使用者的會話、樣本與進度，**還原使用者不會帶回**（G07.7）。
- 降級：表內仍有會話或進度時，000066 的 down 拒絕執行（`55000`），需要操作者先清除，與外掛軌、圖片的保留資料規則一致。
- 支撐之後的聚合：會話有使用者、條目、所屬媒體庫、版本、裝置、開始／結束時間與狀態；樣本保留統計規則需要的報告串流（見下方「樣本保留」）。按日／週／月的彙總可以直接從這兩張表加上 `items` 計算。

## 寫入：緩衝、批次 flush、去重與逾時（G23.2）

`internal/app/progress.go` 的 `Progress` 是每個實例一份的記憶體緩衝，時間來源是可注入的 `Clock`。

| 事件 | 資料庫動作 |
| --- | --- |
| 開始（或未知播放鍵的第一個回報） | 1 個語句：同一語句內檢查 live native 工作階段、使用者與媒體庫授權後 `INSERT … ON CONFLICT (user_id, play_key)`。同鍵 active 或 timed_out 且同條目 → 接回原會話；已 stopped／failed 或條目不同 → 衝突，不寫 |
| 重複的開始、進度、Ping | **不寫**，只改記憶體；兩次 flush 之間只保留最新狀態 |
| 定期 flush（`playback.flushSeconds`，預設 10 秒；dirty 會話達 `playback.maxBatch` 時提前） | 每批最多 `maxBatch` 個會話**一個語句**：以 `unnest` 一次更新所有會話列、合併寫入條目進度、插入保留樣本。只更新仍為 active 的會話；進度與樣本只跟著被更新的列，所以已結束、已清除歷史、已刪除使用者的會話不會被寫回 |
| 停止 | 立即寫 1 個語句（同上，單一會話），讓客戶端停止後馬上讀得到續播點；寫入失敗則保留到下次 flush 重試。重複的停止不寫 |
| 逾時（`playback.sessionTimeoutSeconds`，預設 300 秒沒有回報） | 由下次 flush 關閉為 `timed_out`，依停止規則計算續播點／已播放 |
| 其他實例遺留的 active 會話 | 每 6 次 flush 查一次逾時的 active 會話（部分索引），本實例沒有持有的就以儲存的位置關閉 |
| 保留期（`playback.retentionDays`，預設 365，0＝不自動刪） | 每小時最多一次，分批刪除 `ended_at` 早於保留期的會話（樣本級聯）。續播點與已播放屬於使用者資料，不受保留期影響 |

停止規則（與 `docs/watch-stats.md` 相同門檻，`domain.ResolvePlaybackEnd`）：位置達到已知時長的 90% → 已播放、播放次數 +1、續播點清零；否則續播點＝位置，但小於 30 秒時不留續播點。時長未知（來源未探測）時不會因位置判定播完。失敗的播放不算看完。

同一使用者在同一批次裡用兩個裝置播同一條目時，進度合併成一列：最新回報決定續播點，每個播完的會話各算一次。

客戶端節流：開始回應帶 `reportIntervalSeconds`（`playback.reportIntervalSeconds`，預設 10），客戶端應以此頻率回報；更快的回報不會被拒，只會在記憶體裡被合併。

樣本保留：開始、暫停、恢復、停止、失敗一律保留；進度最多每 `playback.sampleIntervalSeconds`（預設 60 秒）保留一個；位置跳動（seek、快轉，判定同統計規則的跳躍）時前後兩個回報都保留。`TestProgressKeepsStatisticsSamples` 驗證以保留樣本計算的有效時長、覆蓋、跳躍數與以完整回報串流計算的結果相同。

上限：記憶體內最多 `playback.maxSessions`（預設 10000）個會話，超過時新的開始回 503 `playback_busy`（`Retry-After: 5`）。

關機：HTTP 排空後再 flush 一次。會話在資料庫維持 active：客戶端繼續回報就接回，否則逾時關閉。

### 設定

| JSON（`playback.*`） | 環境變數 | 預設 | 範圍 |
| --- | --- | --- | --- |
| `flushSeconds` | `JELEE_PLAYBACK_FLUSH_SECONDS` | 10 | 1–300 |
| `maxBatch` | `JELEE_PLAYBACK_MAX_BATCH` | 500 | 1–10000 |
| `maxSessions` | `JELEE_PLAYBACK_MAX_SESSIONS` | 10000 | 1–1000000 |
| `sessionTimeoutSeconds` | `JELEE_PLAYBACK_SESSION_TIMEOUT_SECONDS` | 300 | 30–86400，且至少 3 個 flush 間隔 |
| `reportIntervalSeconds` | `JELEE_PLAYBACK_REPORT_INTERVAL_SECONDS` | 10 | 1–300 |
| `sampleIntervalSeconds` | `JELEE_PLAYBACK_SAMPLE_INTERVAL_SECONDS` | 60 | 5–300 |
| `retentionDays` | `JELEE_PLAYBACK_RETENTION_DAYS` | 365 | 0–36500（0＝不自動刪除） |

## 寫放大證據

`internal/adapter/http/progress_load_test.go` 的 `TestProgressWriteAmplificationPostgres`：真 PostgreSQL、真路由與驗證；100 個 native 工作階段（100 個使用者）並發，每 100 ms 回報一次進度（目標 1000 req/s），`flushSeconds` 等效 1 秒；最後全部停止。資料列寫入次數取自 PostgreSQL 的 `pg_stat_user_tables`（測試前後各換一次連線池讓統計落地）。`JELEE_PROGRESS_LOAD_SECONDS` 調整持續時間。

2026-10-04 在開發機（WSL2，`-p 1`）的結果：

| 項目 | 10 秒 | 60 秒 |
| --- | --- | --- |
| 請求（開始＋進度＋停止） | 10,200（0 失敗） | 60,200（0 失敗） |
| 達成速率 | 995 req/s | 999 req/s |
| 延遲 p50／p99／max | 0.54 ms／79 ms／166 ms | 0.55 ms／3.2 ms／188 ms |
| 寫入語句（定期 flush／停止／開始） | 210（10／100／100） | 260（60／100／100） |
| 每個回報的寫入語句 | 0.0206 | 0.0043 |
| `playback_sessions` 列 | +100 插入、+1,100 更新 | +100 插入、+6,100 更新 |
| `user_item_data` 列 | +100 插入、+1,000 更新 | +100 插入、+6,000 更新 |
| `playback_samples` 列 | +200 插入 | +200 插入 |
| 三表列寫入合計／每回報 | 2,500／0.245 | 12,500／0.208 |
| `sessions`（既有的最後使用時間記錄，限流） | +100 更新 | +200 更新 |

解讀：

- 寫入**語句數**只跟 flush 次數與開始／停止次數有關（每次 flush 一個語句，最多 `maxBatch` 個會話），與回報頻率無關；60 秒、6 萬個回報只有 60 個 flush 語句。
- 資料**列**更新的上限是「會話數 × flush 次數」：每個會話每次 flush 最多更新一次自己的會話列與一列進度。測試裡客戶端每秒回報 10 次而 flush 每秒一次，所以列寫入約為回報數的 1/5；以預設設定（客戶端每 10 秒回報、每 10 秒 flush），列寫入約等於回報數，但仍然是每 10 秒一個批次語句，而不是每個回報一個交易。
- 延遲在共用開發機上量測；回報路徑除了驗證查詢之外不碰資料庫，10 秒那次 p99 的尖峰來源未個別分析。

## 自有 API

| 路由 | 身分 | 說明 |
| --- | --- | --- |
| `POST /api/v1/playback/start` | 僅 native（web 403 `web_playback_disabled`） | `{"itemId", "sourceId"?, "playSessionId"?, "positionTicks"?, "paused"?}`；回 `playSessionId`（沒帶時為 `s:{工作階段}:{條目}`）與 `reportIntervalSeconds`。看不到／不存在的條目 404（設定 403 時 403） |
| `POST /api/v1/playback/progress` | 僅 native | `{"playSessionId"?, "itemId"?, "positionTicks"?, "paused"?}`；204 |
| `POST /api/v1/playback/stop` | 僅 native | 另可帶 `failed`、`failureReason`；204；重複停止也是 204 |
| `GET /api/v1/items/{id}/user-data` | 任何工作階段 | 續播點、已播放、次數、最後播放時間；看不到的條目 404 |
| `PUT`／`DELETE /api/v1/items/{id}/played` | 任何工作階段 | 標記已播放（次數 +1、清續播點）／未播放（次數歸零、清續播點）；PUT 主體 `{}` |
| `GET /api/v1/users/me/resume?offset&limit` | 任何工作階段 | 繼續觀看（最近播放在前，`limit` 1–500，預設 50） |
| `DELETE /api/v1/users/me/playback-history` | 任何工作階段 | 清除自己的會話、樣本、續播點、已播放與次數；寫審計 `playback.history_cleared`（只記筆數） |
| `GET /api/v1/playback/sessions` | 管理員 | 進行中的會話（最多 500），含本實例尚未 flush 的位置 |

回報路由先過 `GuardProduction`（轉換參數 409 `transcode_disabled`），不接受 query。新錯誤碼 `playback_busy`（503）已加入錯誤碼表與四種語系。

相容層路由見 [`compat-matrix.md` 的「播放狀態模組」](compat-matrix.md#播放狀態模組g242g232g483)。

## 不可見條目（G48.3）

所有讀取（`user-data`、續播清單、相容層 `UserData` 與 Resume、標記）都在 SQL 內套用讀取身分的媒體庫授權；收回授權後條目立刻從續播清單與 UserData 消失、按 ID 讀取回隱藏狀態，回報開始也回隱藏狀態。資料列本身保留，恢復授權後續播點仍在。同一實例上 UserData 讀取疊加尚未 flush 的位置時，只疊加到 SQL 判定可見的條目。

## 測試

- 單元（`internal/app/progress_test.go`，假時鐘、假儲存）：100 會話 × 30 秒每秒回報只產生 3 個 flush 語句且只帶最新位置；`maxBatch` 分批與提前喚醒；開始／停止去重、已停止會話不能重開、播放鍵換條目衝突；停止規則（播完、中段、開頭、失敗）；逾時關閉；統計樣本保留與完整串流計算一致；flush 失敗重試（含停止）；會話上限；清除歷史後緩衝不寫回；UserData 疊加不讓看不到的條目出現；遺留會話關閉與每小時清理；無效回報拒絕。
- 領域與設定：`internal/domain/progress_test.go`、`internal/platform/config/playback_test.go`。
- 真 PG（`internal/adapter/postgres/progress_test.go`）：起播 → 進度 → flush → 停止 → 續播點、會話列、樣本、重複停止、續播清單、播完標記已播放、標記／取消、web 工作階段與他條目版本被拒；兩裝置合併；收回授權後隱藏、恢復後回來；清除歷史（含審計與緩衝）、軟刪除使用者（緩衝中的會話不寫回、還原不帶回）、硬刪除級聯；逾時、接回、遺留會話關閉、保留期清除、管理員列表；資料庫約束；遷移 up/down（保留資料時拒絕）/up。
- 真 PG HTTP（`internal/adapter/http/progress_test.go`、`progress_load_test.go`）與存取外洩表（新增 9 條自有路由與 12 條相容路由）。

## 需要長時間或真實環境驗證

- 長時間：多日運作下的保留期清理、遺留會話關閉、記憶體上限與 flush 延遲；多實例部署時同一會話在不同實例間回報（停止只帶 `playSessionId` 而落在沒有持有該會話的實例時，會話以逾時關閉，位置是最後一次 flush 的值）。
- 規模：上萬個同時播放、`maxBatch` 上限附近的單語句耗時與鎖等待。
- 真實客戶端：見 `compat-matrix.md`「需要真實客戶端驗證」。
