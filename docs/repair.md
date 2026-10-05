# 自愈動作（G50.4）

[資料一致性檢查](consistency.md)找出問題；自愈動作把其中能安全處理的部分修好。每個動作都有**預演**（`--dry-run`）：只讀、不寫任何東西，列出會受影響的對象種類、數量與最多 20 筆樣本；**確認後執行**（`--yes`）套用同一份計畫、記錄一次修復執行（`repair_runs`）並寫稽核。執行是冪等的：成功後立刻再跑一次，影響為 0。能回滾的動作（`stats`、`counts`）寫修正日誌，可以 `repair revert` 還原。

## 指令與 API

```
jelee-cli repair items|image-variants|caches|stats|orphans|nfo|counts [--library ID|名稱] [--dry-run | --yes] [--json] [--stat-budget N] [--timeout 30m] [--token-stdin [--url http://127.0.0.1:8097]]
jelee-cli repair revert --run RUN_ID --yes [--json] [--token-stdin [--url …]]
```

- 不帶 `--dry-run` 也不帶 `--yes` 一律拒絕（`repair_confirmation_required`，結束碼 2）：先預演、再確認。
- 預設在服務主機上直接連資料庫執行（與 `consistency` 相同），不需要工作階段，稽核的操作者為空。`image-variants` 與 `nfo` 改動的是**執行中服務**擁有的狀態（記憶體中的圖片變體存放區、NFO 讀取器身分），直接執行會回 `repair_requires_server`；請加 `--token-stdin`（從標準輸入讀管理員權杖），改由服務的管理 API 執行。`--token-stdin` 對任何動作都可用，此時 `--library` 必須是媒體庫 ID，URL 規則同 `jelee-cli jobs`（https，或指向迴環位址的 http）。
- API：`POST /api/v1/admin/repairs`，本體 `{"action":"stats","libraryId":"…","dryRun":true}`；執行要 `"iUnderstand":true`，否則 400 `confirmation_required`。`POST /api/v1/admin/repairs/{id}/revert` 本體 `{"iUnderstand":true}`。只限管理員，與任務 API 同一組（`JELEE_ENABLE_JOBS` 與帳號），受請求逾時（設定檔 `requestTimeoutSeconds`，預設 15 秒）限制；大型媒體庫請在主機上用指令列（`--timeout`，預設 30 分鐘）。
- 結束碼：0 完成（或預演沒有要做的事）、3 預演找到要做的事、1 失敗、2 用法錯誤、130 被中斷。
- 錯誤只輸出固定代碼：`repair_database_unavailable`、`repair_not_found`、`repair_library_busy`（媒體庫有其他進行中的任務）、`repair_queue_full`、`repair_not_revertible`、`repair_requires_server`、`repair_request_rejected (HTTP 狀態 代碼)` 等。輸出先經過與 `doctor` 相同的敏感值掃描，命中就不輸出（`output_unsafe`）；路徑依 `logging.pathMode` 處理（預設 `[redacted]`），絕對路徑、連線字串與其他祕密永遠不會出現。

## 動作

| 動作 | 需求項 | 計畫（預演）讀什麼 | 執行做什麼 | 回滾 |
| --- | --- | --- | --- | --- |
| `items` | 重建條目 | 基準中沒有條目的影片（一致性檢查 `orphan_file` 的 `video_not_cataloged`）；等待審核的待定檔只計入 `info.pendingReview` | 該媒體庫有目標時排一個 `catalog_sync` 任務（[目錄同步](catalog-sync.md)）；同一媒體庫已有進行中的 `catalog_sync` 時回報為重播（`jobs[].replayed`），不再排 | 不可：同步只新增或標記缺失，刪除要另走缺失確認 |
| `image-variants` | 重建圖片變體 | 圖片存放區目前世代的變體數與位元組 | 建立下一個世代並清空變體索引（`ClearVariants`），每個變體在下次請求時重新產生；原圖不動 | 不可：快取，重建即可 |
| `caches` | 重建探測與索引快取 | 已就緒的探測快取列，對應檔案的大小或修改時間已與基準不同（`probe_cache_stale`） | 刪除這些快取列並釋放配額，下次探測重新建立；被進行中探測租用、已非 ready 或期間又變動的列跳過 | 不可：快取 |
| `stats` | 重算統計 | **每一筆**（非抽樣）日彙總列，以已計入工作階段的標記重算 sessions／views／first plays／rewatches／completions／completion milli；只看播放歷史保留期（`JELEE_PLAYBACK_RETENTION_DAYS`）往前兩天以後的日期 | 在彙總鎖下逐列重算覆寫，寫修正日誌 | 可 |
| `orphans` | 清理孤兒記錄 | (1) 媒體庫的探測快取列，其檔案不在基準中、且以 stat 確認已不存在（`probe_cache_orphan`）；(2) 不帶 `--library` 時，整個圖片變體索引中檔案已不在目前世代的列 | 刪除確認過的孤兒；stat 失敗或預算（`--stat-budget`，預設同一致性檢查）用完的候選只計入 `info.unconfirmed`，檔案其實還在的計入 `info.baselineStale`，都不刪 | 不可：衍生資料 |
| `nfo` | 重新同步 NFO | NFO 觀察已過時的條目（一致性檢查 `nfo_state` 的四種發現） | 媒體庫有目標時排一個帶 NFO 驗證的掃描；已有進行中的 NFO 掃描則回報為重播；媒體庫未開啟 NFO 讀取時計入 `info.nfoDisabled` 並跳過 | 不可：任務 |
| `counts` | 修復計數 | 續播資料的「最後使用版本」與播放紀錄的來源指向別的條目的版本（版本計數錯誤，`user_data_foreign_source`、`session_foreign_source`）；條目結構問題（影片條目沒有版本、劇集／季有版本）只計入 `info.manual` | 清除錯誤參照（不影響續播點與統計），寫修正日誌 | 可 |

`stats` 與 `counts` 與一致性檢查 `--fix` 是同一組修正（同樣在交易內以讀到的值為前提再比對、統計重算持有彙總鎖），差別在自愈動作掃描全部列、不抽樣，日誌寫入 `repair_journal`。

### 預演與執行的一致性

執行重新讀一次計畫並逐頁套用，所以結果中 `planned` 是執行當下找到的數量、`applied` 是實際改動的數量、`skipped` 是讀取後到寫入前已被改過（或任務已在進行中）而沒有改的數量。沒有並行變動時，預演的 `planned` 等於執行的 `applied`；這由 `internal/adapter/postgres/repair_test.go` 對每個動作以真 PostgreSQL 驗證（同時驗證預演不寫執行紀錄與稽核、第二次執行影響為 0、回滾後問題重新出現）。

排任務的動作（`items`、`nfo`）的 `applied` 是交給新任務的目標數；實際建立或更新了什麼看該任務的報告（`jelee-cli jobs get --id …`、`GET /api/v1/jobs/{id}/catalog-sync`）。任務跑完後再預演，目標應為 0。

## 稽核、紀錄與回滾

- 每個寫入交易寫一筆 `repair.applied`（目標為執行 ID，內容為動作、套用數、跳過數與各代碼數）；每次執行結束寫 `repair.finished`（狀態、計畫／套用／跳過數、媒體庫、排入的任務 ID）；回滾每批寫 `repair.reverted`。經 API 執行時稽核帶管理員身分。`items` 另寫 `catalog_sync.submitted`（帶 `repairRunId`）。
- 預演不寫任何紀錄或稽核。
- `repair revert --run RUN_ID` 依修正日誌由新到舊還原，只還原仍保持修正後數值的列，其他跳過並計數；重複執行不重放。非 `stats`／`counts` 的執行回 `repair_not_revertible`（API 為 409 `conflict`）。
- 執行中斷時已完成的批次保留，結果以 `partial`／`failed` 寫入；超過一天仍是 `running` 的執行（程序已死）在下次開始時標為 `failed`。有日誌的執行永不清除；其他只保留最近 100 次。

## 結果文件

`--json` 與 API 回傳同一份文件（也存在 `repair_runs.result`，上限 256 KiB，超過時捨棄樣本保留計數）。`schema` 為 `jelee-repair-result/v1`；新增欄位不換版本，刪除或改名必須換版本。

| 欄位 | 說明 |
| --- | --- |
| `action`、`origin`（`cli`／`api`）、`libraryId`、`runId`（只有執行才有） | 這次的範圍 |
| `dryRun`、`state` | `planned`（預演）、`completed`、`partial`、`failed` |
| `reason` | `no_baseline`（媒體庫沒有基準，比對類動作跳過）、`unconfirmed`（有候選未能確認而未處理）、`repair_failed` |
| `revertible` | 這次執行可否 `repair revert` |
| `planned`、`applied`、`skipped`、`targets[]` | 總數與各種類（`uncataloged_video`、`image_variant`、`probe_cache_stale`、`probe_cache_orphan`、`variant_index_orphan`、`daily_counters`、`nfo_observation`、`user_data_reference`、`session_reference`）的數量 |
| `jobs[]` | 排入或沿用的任務：`libraryId`、`jobId`、`replayed` |
| `info` | 不是受影響對象的計數：`pendingReview`、`unconfirmed`、`baselineStale`、`unverified`、`manual`、`nfoDisabled`、`no_baseline`、`image_store_unavailable`、`bytes` |
| `samples[]`、`samplesTruncated` | 最多 20 筆受影響對象，只有識別碼與依路徑模式處理的相對路徑 |

## 不做或做不到的部分

- **不刪除條目、來源或使用者資料。** 檔案已不存在的來源由目錄同步的缺失確認處理（[目錄同步](catalog-sync.md)）；條目結構問題（`info.manual`）需要人工合併、移動或刪除。
- **缺少的日彙總列不補**（一致性檢查 `daily_row_missing`）：可能是有人刪了統計列，自動補列會掩蓋原因。**有效觀看時間**不重算，理由同一致性檢查。
- **`image-variants` 只能由服務執行**：存放區的索引在服務記憶體中，另一個程序清空會與它不一致。變體的資料庫索引列不會因清空而刪除，之後以 `orphans` 清理檔案已不在的列。
- **`nfo` 只能由服務執行**：掃描任務要固定在服務的 NFO 讀取器身分上。
- **條目重建只依基準與同步**：檔名信心不足的待定檔不會自動建條目（仍待審核）；要重新讀取元資料或圖片，用中繼資料與圖片的既有入口。
- **API 受請求逾時限制**：逾時中斷的執行以 `partial` 記錄，可重跑（冪等）。

## 資料表（遷移 000083）

- `repair_runs`：每次執行一列（動作、媒體庫、來源、操作者、狀態、計畫／套用／跳過數、結果 jsonb）。預演不寫。
- `repair_journal`：`stats`／`counts` 的修正日誌，格式同 `consistency_fix_journal`；有日誌的執行不被清理。

降級遷移在仍有執行紀錄或日誌時拒絕（`retained repair state prevents downgrade`）。
