# 觀看統計（G23.3、G23.4、G23.5、G48.3）

本文是觀看統計的口徑與實作說明：有效觀看時長、完成率、首播／重看怎麼從播放樣本算出，怎麼增量彙總到日表，API 怎麼讀、誰看得到什麼，刪除與保留期的影響，以及 10 萬會話的效能證據。播放會話與樣本怎麼產生見 [`playback-progress.md`](playback-progress.md)。

規則的程式實作：

| 位置 | 內容 |
| --- | --- |
| `internal/domain/watch_stats.go` | 單一會話的規則 `ComputeWatchSession`、首播／重看分類 `ClassifyWatchPlay`（純函式） |
| `internal/domain/watch_stats_rollup.go` | 增量彙總 `AggregateWatchStats`、查詢參數、期間起點 `WatchStatsBucket`（純函式） |
| `internal/app/watch_stats.go` | 彙總排程、報表與匯出服務 |
| `internal/adapter/postgres/watch_stats.go`、遷移 `000067_watch_statistics` | 日表、彙總交易、報表 SQL、匯出 |
| `internal/adapter/http/watch_stats.go` | 自有 API |

**修改口徑**：參數可以設定；規則本身改變時，必須同時修改本文、`watch_stats.go`／`watch_stats_rollup.go` 的文件註解與單元測試的邊界案例，並說明已彙總的日表是否需要重算（重算只對保留期內仍有樣本的會話可行，見「刪除、清除與保留期」）。

## 資料流

```
客戶端回報 ──> Progress（記憶體、批次 flush）──> playback_sessions／playback_samples
                                                     │ 會話結束（停止、失敗、逾時）
                                                     ▼
                         WatchStats 彙總（排程＋結束喚醒，單一實例持鎖）
                                                     ▼
                     watch_stats_daily（使用者×條目×日）＋ watch_stats_history
                                                     ▼
                         統計 API／匯出（只讀日表，SQL 內套用媒體庫授權）
```

請求路徑**從不讀** `playback_samples` 或 `playback_sessions`；樣本只在彙總時讀，而且每個會話只讀一次（重新加入的逾時會話例外，見下文）。

## 口徑

### 輸入

一個播放會話保留的樣本（`playback_samples`），依伺服器接收順序（`seq`）排列，加上該會話播放版本的時長 `runtime`（來源未探測時為 0，表示未知）。每個樣本：

| 欄位 | 意義 |
| --- | --- |
| `at` | 伺服器接收時間（牆鐘） |
| `kind` | `start`、`progress`、`pause`、`resume`、`seek`、`stop`、`fail` |
| `position` | 事件後的媒體位置 |
| `paused` | 僅 `progress`：客戶端回報目前暫停 |

樣本不是每個回報都留：狀態變化一律保留、進度每 `playback.sampleIntervalSeconds`（預設 60 秒）最多一筆、位置跳動時前後兩筆都留。以保留樣本計算的結果與以完整回報串流計算的結果相同（`TestProgressKeepsStatisticsSamples`）。

### 可設定參數

全部在設定檔 `stats.*`（或對應環境變數），同時作用於播放進度（續播點、樣本保留的跳動判定）與統計彙總。

| JSON（`stats.*`） | 環境變數 | 預設 | 範圍 | 作用 |
| --- | --- | --- | --- | --- |
| `maxReportGapSeconds` | `JELEE_STATS_MAX_REPORT_GAP_SECONDS` | 300 | 10–3600 | 相鄰樣本間隔超過此值時，只計此值（上限） |
| `maxPlaybackRate` | `JELEE_STATS_MAX_PLAYBACK_RATE` | 2.0 | 1–4 | 位置前進超過「牆鐘 × 倍率 ＋ 容差」視為跳轉（快轉） |
| `positionToleranceSeconds` | `JELEE_STATS_POSITION_TOLERANCE_SECONDS` | 3 | 0–30 | 位置抖動容差 |
| `completedRatio` | `JELEE_STATS_COMPLETED_RATIO` | 0.9 | 0.5–1 | 覆蓋率達此值即「完成」；也是已播放的判定門檻 |
| `minViewSeconds` | `JELEE_STATS_MIN_VIEW_SECONDS` | 120 | 0–1800 | 計為一次觀看的最低有效時長 |
| `minViewRatio` | `JELEE_STATS_MIN_VIEW_RATIO` | 0.1 | 0–1 | 短條目依時長比例降低門檻 |
| `minResumeSeconds` | `JELEE_STATS_MIN_RESUME_SECONDS` | 30 | 0–600 | 低於此位置不留續播點 |
| `timeZone` | `JELEE_STATS_TIME_ZONE` | `UTC` | IANA 名稱（不接受 `Local`） | 切日的時區 |
| `weekStart` | `JELEE_STATS_WEEK_START` | `monday` | `monday`／`sunday` | 週的起始日（查詢時套用） |
| `aggregateSeconds` | `JELEE_STATS_AGGREGATE_SECONDS` | 60 | 5–3600 | 彙總排程間隔 |
| `exportMaxRows` | `JELEE_STATS_EXPORT_MAX_ROWS` | 100000 | 1–5000000 | 匯出列數上限 |
| `retentionDays` | `JELEE_STATS_RETENTION_DAYS` | 0 | 0–36500 | 日表保留天數，0＝保留到使用者清除 |

### 有效觀看時長

逐一檢查相鄰兩個有效樣本之間的區間（前一個稱「錨點」），依序套用：

1. **播放狀態**：只有錨點處於播放狀態的區間可能計入。`start`、`resume`、未暫停的 `progress` 進入播放；`pause`、帶 `paused` 的 `progress` 進入暫停；`seek` 維持原狀態；`stop`、`fail` 結束會話，之後的樣本全部忽略。**暫停時間永不計入。**
2. **時鐘倒退**：後一樣本的時間早於錨點時，區間不計，後一樣本成為新錨點。
3. **Seek**：帶跳轉前位置的 `seek`，區間只判定到跳轉前位置，新錨點取跳轉後位置；沒有跳轉前位置時當一般區間。目前的回報協定不帶跳轉前位置，樣本表也不存，所以實際上 seek 一律由規則 4 判定。
4. **跳轉（快轉、倒轉、章節跳過）**：位置前進量小於 `-容差`（後退）或大於 `牆鐘 × maxPlaybackRate ＋ 容差` 時，該區間不計。容差內的小幅後退不計也不算跳轉。
5. **計入量** = `min(牆鐘, 位置前進量)`。卡頓或空轉（牆鐘大於前進量，例如客戶端持續回報同一位置）只計前進量；倍速播放（前進量大於牆鐘）只計牆鐘，不灌水。
6. **回報間隔過長**：牆鐘超過 `maxReportGapSeconds` 時，計入量上限為該值（多半已斷線，只信任開頭一段）。等於上限不截。
7. **重疊**：每個計入區間對應牆鐘片段 `[錨點時間, 錨點時間 ＋ 計入量)`，先取聯集再加總。重複樣本、時鐘倒退不會重複計時；**同一使用者同一條目的不同會話（兩台裝置同時播同一段）也取聯集**，不論兩個會話是否在同一次彙總中處理（見「增量彙總」）。

格式錯誤的樣本（時間為零、未知種類、負位置）被忽略並計入診斷計數，不影響其他計算。

### 覆蓋、完成率、續播點

- **覆蓋**：計入區間對應的媒體區段 `[錨點位置, 錨點位置 ＋ 前進量]` 的聯集長度，裁到 `runtime`。規則 6 截斷時只取截斷後的長度。倒回重看會增加有效時長，但不增加覆蓋。
- **單一會話的完成率** = 覆蓋 / `runtime`，上限 1；`runtime` 未知時為 0。
- **完成**：完成率 ≥ `completedRatio`。分多個會話看完一部片，每個會話各自的覆蓋都未達門檻時，不算完成（已播放狀態另由停止位置判定，見 `playback-progress.md`）。
- **彙總的完成率**（API 的 `completionRate`）= 計為觀看的會話的完成率平均值（日表存每會話完成率的千分比總和 `completion_milli` 與會話數）。沒有會話時為 0。
- **續播點**：不在統計日表內，統計 API 的個人 Top 條目附上該使用者目前的 `userData`（續播點、已播放、次數、最後播放），來源是 `user_item_data`。

### 計為觀看、首播與重看

- **計為觀看（counted session）**：已完成，或有效時長 ≥ 門檻。門檻 = `min(minViewSeconds, minViewRatio × runtime)`；`runtime` 未知或 `minViewRatio` 為 0 時門檻為 `minViewSeconds`。有效時長為 0 時永不計為觀看。未計為觀看的會話仍貢獻有效時長，但不計入會話數、觀看次數與完成次數。
- **首播／重看**：每個使用者×條目保留一份分類狀態（`watch_stats_history`：觀看次數、完成次數、最近一次計數會話是否完成），計為觀看的會話依序分類：

| 情況 | 分類 | 觀看次數（views） |
| --- | --- | --- |
| 未計為觀看 | `none` | 不變 |
| 此前沒有任何觀看 | `first`（首播） | +1 |
| 最近一次計數會話已完成 | `rewatch`（重看） | +1 |
| 其他（上次沒看完，接著看） | `continue` | 不變 |

「觀看次數」= 首播數 ＋ 重看數；分多次看完一部片只算一次。

**分類順序**：依彙總順序。每批取結束時間最早的待處理會話，批內依開始時間排序後分類。一個開始較早但結束較晚的會話，會在之後的批次才分類；兩台裝置同時播放時，先結束的會話先分類。

### 計數歸屬的日期

- 有效時長在報表時區的午夜切開，跨日會話分別計入兩天。
- 計數（會話、觀看、首播、重看、完成、完成率）歸入該會話**第一個計入片段**所在日期；沒有計入片段時歸入會話開始日期。

### 時區與週起始日

- 日表的「日」是 `stats.timeZone` 的當地日期，在**彙總時**決定。改時區只影響之後彙總的會話；已彙總的日不會重切（要重切需重算，只有保留期內仍有樣本的會話做得到）。夏令時間切換日照樣以當地午夜切開（該日可能是 23 或 25 小時）。
- 週、月、年由日表在查詢時加總：週起點是 `stats.weekStart`（`monday` 為 ISO 8601，`sunday` 為週日），月起點是 1 日，年起點是 1 月 1 日。期間列出的 `start` 是該期間第一天。
- API 的 `from`、`to` 是報表時區的日期（含頭尾）；沒給時 `to` 是報表時區的今天，`from` 是 `to` 前 29 天。

## 儲存（遷移 000067）

| 表／欄位 | 內容 |
| --- | --- |
| `watch_stats_daily` | 主鍵 `(user_id, day, item_id)`；`library_id`（與 `items(id, library_id)` 外鍵一致）、`effective_ms`（0–90,000,000，一天最多 25 小時）、`sessions`、`views`、`first_plays`、`rewatches`、`completions`、`completion_milli`。約束 `views = first_plays + rewatches` 且 `views ≤ sessions`。使用者、條目刪除時級聯 |
| `watch_stats_history` | 主鍵 `(user_id, item_id)`：首播／重看分類狀態 |
| `playback_sessions.stats_*` | 每個會話已彙總到哪：`stats_through`（彙總時讀到的結束時間）、`stats_day`、`stats_counted`、`stats_completed`、`stats_completion_milli`、`stats_play` |
| 索引 | 日表主鍵（個人統計）、`(day, user_id, item_id)`（全站統計與匯出）、`item_id`（級聯）；會話的部分索引 `playback_sessions_stats_pending_idx`（只含待彙總會話） |

**待彙總**：`ended_at IS NOT NULL AND (stats_through IS NULL OR stats_through < ended_at)`。從 066 升級時既有的已結束會話都是待彙總，第一次執行就會補算。

**降級**：日表或分類狀態有資料時，000067 的 down 拒絕執行（`55000`），因為日表的資料比會話活得久（保留期之後），降級會失去它們；與 066 對播放歷史的規則一致。

## 增量彙總（G23.5）

- **何時**：每 `stats.aggregateSeconds`（預設 60 秒）一次；會話結束（停止、失敗、逾時、清掃遺留會話）時播放服務會喚醒彙總，延遲 5 秒以合併一陣停止。進行中的會話**不計入**，結束後最多約一個間隔就出現在統計中。
- **一批一個交易**：每批最多 200 個待彙總會話，每次執行最多 50 批，直到沒有待處理。交易先以 `pg_try_advisory_xact_lock` 取彙總鎖，拿不到（其他實例正在彙總）就直接結束，所以多實例部署同一時間只有一個實例彙總。
- **交易內容**：
  1. 讀待彙總會話（依 `ended_at`）；
  2. 讀「先前已彙總、同使用者同條目、時間重疊」的會話（最多 2000 個），它們的有效時長已在日表中；
  3. 讀這些會話的樣本：待彙總會話讀到它的 `ended_at`，先前會話讀到它的 `stats_through`；
  4. 讀分類狀態；
  5. 以 `domain.AggregateWatchStats` 計算：每使用者×條目×日的**有效時長增量** =（先前 ∪ 本批）的長度 − 先前的長度（規則 7 的聯集），計數依上文；
  6. 寫入會話標記（只在會話仍以讀到的 `ended_at` 結束、且標記未被改過時才寫；任何一列不符就整批回滾，下次重讀）、累加日表、覆寫分類狀態。
- **重新加入的逾時會話**：逾時（`timed_out`）的會話在客戶端回來時會重新開啟同一列，再結束時又變成待彙總。它先前計入的部分（到 `stats_through` 為止）當作「先前」，所以只加新增的時長；它的會話數、首播／重看分類維持原樣，只有新達到的完成、提高的完成率會被加上。
- **與清除、刪除的競爭**：彙總只讀會話時沒有上鎖；清除歷史或刪除使用者若在讀取後刪掉會話，寫標記時筆數不符，整批回滾。若彙總已鎖住會話列，清除會等彙總提交後才刪除會話，接著（同一清除交易中的下一個語句）刪除日表，不會留下孤兒資料。

## 刪除、清除與保留期

| 事件 | 對統計的影響 |
| --- | --- |
| 使用者清除自己的歷史（`DELETE /api/v1/users/me/playback-history`，W11-6） | 同一交易刪除會話、樣本、續播點與已播放，**以及該使用者的日表與分類狀態**：統計歸零。審計 `playback.history_cleared` 只記筆數 |
| 軟刪除使用者 | 同上，在刪除交易中一起刪除；還原使用者不會帶回 |
| 硬刪除使用者、刪除條目 | 外鍵級聯刪除日表與分類狀態 |
| 收回媒體庫授權 | 日表不動；查詢時過濾（見下）。恢復授權後統計回來 |
| 播放保留期 `playback.retentionDays`（預設 365 天） | 只刪除**已彙總完畢**（`stats_through ≥ ended_at`）的會話；尚未彙總的會話保留到彙總之後，統計不會因保留期遺失。日表比會話活得久，但會話與樣本刪除後就無法重算（例如改時區或改規則） |
| 統計保留期 `stats.retentionDays`（預設 0＝不刪） | 每小時最多一次，分批刪除報表時區中早於「今天 − 保留天數」的整日資料列。分類狀態保留，所以之後的首播／重看判定不受影響 |

## 自有 API（G23.3、G23.4、G48.3）

| 路由 | 身分 | 說明 |
| --- | --- | --- |
| `GET /api/v1/users/me/watch-stats` | 任何工作階段（web 與 native） | 自己的統計 |
| `GET /api/v1/users/{id}/watch-stats` | 管理員 | 指定使用者的統計；不存在或已刪除的使用者 404 |
| `GET /api/v1/watch-stats` | 管理員 | 全站統計，另附 `topUsers` |
| `GET /api/v1/watch-stats/export` | 管理員 | 匯出日表 |

查詢參數（前三條）：`from`、`to`（`YYYY-MM-DD`，最多 3660 天；`period=day` 時最多 400 天）、`period`（`day`／`week`／`month`／`year`，預設 `day`）、`top`（1–100，預設 10）。其他參數一律 400。

回應 `data`：`userId`（個人）、`from`、`to`、`period`、`timeZone`、`weekStart`、`totals`、`periods`（只列有資料的期間）、`topItems`（依有效時長排序；個人統計附 `userData`）、`libraries`（最多 200）、`kinds`、`topUsers`（僅全站）。每組數字都是 `effectiveSeconds`（無條件捨去到秒）、`sessions`、`views`、`firstPlays`、`rewatches`、`completions`、`completionRate`。

**統計不是播放**：所有回應只有條目、媒體庫、使用者的識別碼與名稱，**不含任何直投 URL**（串流、字幕、來源路徑、檔名），web 工作階段可以使用。`TestWatchStatsHTTPPostgres` 檢查回應不含 `/stream`、`/api/v1/sources`、檔名或 URL。

### 誰看得到什麼（G23.4、G48.3）

- **個人統計只有本人與管理員看得到**：一般使用者只能讀 `/users/me/watch-stats`；`/users/{id}/watch-stats`、全站統計與匯出回 403。
- **本人視角**：每個查詢在 SQL 內以「讀取者目前的媒體庫授權」過濾日表的 `library_id`。使用者失去某媒體庫的權限後，該庫條目的時長、次數、Top 條目、媒體庫與類型分布全部從他的統計消失（不是只隱藏條目名稱，數字也扣除）；資料列保留，恢復授權後回來。
- **管理員視角**：管理員可見所有媒體庫，所以管理員讀任何使用者或全站統計時**不套用該使用者的授權**，看到的是實際發生過的全部觀看（包括使用者已失去權限的媒體庫）。因此「管理員看某使用者的總時長」可以大於「該使用者自己看到的總時長」，差額就是已收回授權的媒體庫。
- 已停用的使用者：日表保留，管理員統計照常列入；已刪除（軟刪或硬刪）的使用者沒有統計資料。

### 匯出（G23.4）

- 參數：`from`、`to`（同上，最多 3660 天）、`format`（`csv` 預設、`ndjson`）、`limit`（降低上限，不得超過 `stats.exportMaxRows`）、`userId`（只匯出一位使用者）。
- 匯出的是聚合資料：每列是一個「日 × 使用者 × 條目」的日表列，依日、使用者、條目排序。CSV 欄位 `day,user_id,user_name,item_id,library_id,item_kind,item_title,effective_seconds,sessions,views,first_plays,rewatches,completions,completion_rate`；NDJSON 每行一個物件（`effectiveMillis` 為毫秒）。
- **上限**：送出任何位元組前先計數（`LIMIT 上限+1`），超過上限回 409 `stats_export_limit`（四種語系），不寫審計，請縮小範圍。
- **稽核**：通過上限後、送出第一列前，寫審計 `watch_stats.exported`（`after`：`from`、`to`、`format`、`rows`；指定使用者時 `target_id` 為該使用者）。
- **串流**：`X-Jelee-Export-Rows` 預告列數，`Cache-Control: no-store`、`Content-Disposition: attachment`；最後以 trailer `X-Jelee-Export-Complete: true|false` 告知是否完整送出（狀態碼已送出後中途失敗只能這樣告知）。整次匯出限時 2 分鐘。串流查詢本身仍檢查呼叫者是有效的管理員工作階段。
- **試算表安全**：CSV 中以 `=`、`+`、`-`、`@`、Tab、CR 開頭的文字欄位前面加 `'`，避免被試算表當成公式（條目標題可能來自外部中繼資料）。

錯誤碼：新增 `stats_export_limit`（409），已加入錯誤碼表與 en、zh-CN、zh-TW、ja 訊息。

## 效能證據（G23.5）

`internal/adapter/postgres/watch_stats_test.go` 的 `TestWatchStatsScalePostgres`（設 `JELEE_STATS_SCALE_SESSIONS` 才執行）：真 PostgreSQL，1000 位使用者、2000 個條目、10 萬個已結束會話分布在一年內，每個會話 40 分鐘、每 4 分鐘一個樣本（共 110 萬樣本），五分之一以 1.6 倍速播完。先清空待彙總，再量報表並印出計畫。

2026-10-04 開發機（WSL2，`-p 1`，`JELEE_STATS_SCALE_SESSIONS=100000`）：

| 項目 | 結果 |
| --- | --- |
| 建立資料（SQL 直接寫入） | 10 萬會話、110 萬樣本，21.8 秒 |
| 補算 10 萬個會話（升級或彙總停擺後的最壞情況） | 63.9 秒，約 1,564 會話/秒；產生 105,421 個日表列 |
| 全站、一年、按月（含 Top 條目、媒體庫、類型、Top 使用者） | 中位數 90.6 ms，最大 224 ms |
| 一位使用者、一年、按月 | 中位數 4.6 ms，最大 8.0 ms |
| 全站、30 天、按日 | 中位數 17.2 ms，最大 18.1 ms |

計畫（`EXPLAIN (ANALYZE, BUFFERS)`，節錄；測試會在計畫出現 `playback_samples` 或 `playback_sessions` 時失敗）：

```
一位使用者的總計（以該使用者身分，套用授權）
Aggregate (actual time=0.071..0.071 rows=1)
  ->  Bitmap Heap Scan on watch_stats_daily d (rows=105)
        Recheck Cond: ((user_id = '…'::uuid) AND (day >= '2025-03-11'::date) AND (day <= '2026-03-10'::date))
        Filter: (hashed SubPlan 2)
        ->  Bitmap Index Scan on watch_stats_daily_pkey (rows=105)
        SubPlan 2
          ->  Index Only Scan using library_acl_pkey on library_acl a (rows=1)
Execution Time: 0.122 ms

全站一年按月
Finalize GroupAggregate (rows=13)
  ->  Gather Merge (Workers Launched: 1)
        ->  Partial HashAggregate
              ->  Parallel Seq Scan on watch_stats_daily d (rows=52698 loops=2)
                    Filter: ((day >= '2025-03-11'::date) AND (day <= '2026-03-10'::date))
Buffers: shared hit=1737
Execution Time: 27.415 ms

全站一年 Top 條目
Limit (rows=10) -> Sort (top-N heapsort) -> Finalize HashAggregate (rows=2000)
  ->  Parallel Seq Scan on watch_stats_daily d (rows=52698 loops=2)
Execution Time: 14.328 ms
```

解讀：

- 請求只讀日表。個人統計走主鍵範圍掃描，成本與該使用者在範圍內的日表列數成正比（這裡 105 列，0.1 ms）。
- 全站一年的統計掃描範圍內全部日表列（約 10.5 萬列、14 MB，已在快取中），成本與「使用者×條目×日」的列數成正比，而不是與樣本數成正比。
- 對照：從樣本彙總同樣的 10 萬會話（讀樣本、套用規則、聯集與分類、寫日表）花了 64 秒；如果每個請求都從樣本算，成本就是這個量級，這正是彙總只做一次的原因。
- 穩定狀態下每次彙總只處理上一個間隔內結束的會話；10 萬會話的補算只在升級或彙總長時間停擺後出現。

## 測試

- 口徑單元測試（`internal/domain/watch_stats_rollup_test.go`、既有 `watch_stats_test.go`）：快轉、倍速上限、暫停、空轉、回報間隔上限、完成；首播／接著看／重看（同批與跨批，依開始時間排序）；跨日、台北時區、台北跨日、紐約夏令時間當晚；兩台裝置同批與跨批只算一次、無關條目的先前會話不影響、重新加入的逾時會話只加新增部分；錯誤輸入；查詢參數預設與上限、期間起點（週一／週日）、總計換算。
- 服務單元測試（`internal/app/watch_stats_test.go`）：分批直到清空、每次執行的批數上限、衝突時安靜結束；會話停止喚醒彙總；報表以報表時區的今天決定預設範圍；匯出參數驗證、上限被拒時不開始輸出；統計保留期以當地午夜切、每小時最多一次。
- 設定（`internal/platform/config/stats_test.go`）。
- 真 PG（`internal/adapter/postgres/watch_stats_test.go`）：暫停與快轉不計、彙總冪等、兩裝置跨批只算一次、逾時後重新加入只加新增、保留期不刪未彙總會話且刪除後日表仍在；本人／管理員／全站報表、週（週一與週日）／月／年期間與 domain 的期間起點一致、Top N、空範圍；檢視者讀他人或全站 403、未知使用者 404；收回授權後隱藏（數字也扣除）、管理員仍可見、恢復後回來；彙總與清除競爭時整批回滾；清除後統計歸零且不影響他人；軟刪除與還原、刪除條目級聯；匯出權限、計數、上限、審計（含指定使用者）、串流仍檢查管理員、統計保留期；約束；遷移 up/down（有資料時拒絕）/up 與升級後補算。既有 `TestPlaybackProgressTimeoutSweepAndRetentionPostgres` 加上「未彙總的會話不被保留期刪除」。
- 真 PG HTTP（`internal/adapter/http/watch_stats_test.go`）：web 工作階段讀自己的統計、回應不含直投 URL、隱藏媒體庫不出現、參數驗證；檢視者打管理員路由 403；管理員讀使用者與全站；CSV（表頭、公式防護、列數標頭、完成 trailer）與 NDJSON 匯出、上限 409、審計筆數。
- 存取外洩表：新增 4 條路由（`/users/me/watch-stats` 以列表模式檢查隱藏條目與媒體庫不外洩、管理員對照看得到；其餘三條為管理員路由）。

## 需要長時間或大量資料驗證

- 長時間：多日運作下的排程彙總、統計保留期與播放保留期的交互（彙總停擺時播放保留期會延後刪除，資料表會長大）；跨夏令時間切換的實際切日；多實例部署時彙總鎖的輪替。
- 規模：使用者×條目×日列數遠大於 10 萬（例如數百萬列）時，全站一年報表的掃描時間與記憶體；屆時可再加一層全站日彙總或物化視圖。補算大量歷史（例如從 066 升級時已有數百萬會話）時每批交易的長度與對播放寫入的影響。
- 真實客戶端：各客戶端回報頻率、暫停與快轉回報方式對有效時長的實際影響（目前以合成樣本驗證）。
