# 維護任務：字幕擷取快取清理、備份提醒與批次移除外部元資料（G13.6、G14.7）

本頁說明遷移 000105（暫定編號，合併時依前面分支重新編號；SchemaVersion 105）新增的三種工作：

| 種類 | 需求 | 範圍 | 送件 | 進度／結果 |
| --- | --- | --- | --- | --- |
| `subtitle_cache_cleanup` | G13.6「提取字幕清理」 | 整個執行個體的 Matroska 擷取快取 | `POST /api/v1/maintenance/subtitle-cache/cleanup` | `GET /api/v1/jobs/{id}/subtitle-cache-cleanup` |
| `backup_reminder` | G13.6「備份提醒」 | 整個執行個體 | `POST /api/v1/maintenance/backup-reminder`；可設定每日排程 | `GET /api/v1/jobs/{id}/backup-reminder`；即時狀態 `GET /api/v1/maintenance/backup-status` |
| `external_metadata_removal` | G14.7「提供『移除外部元數據』能力」的批次版 | 一個媒體庫 | `POST /api/v1/libraries/{id}/metadata/external/remove` | `GET /api/v1/jobs/{id}/external-metadata-removal` |

所有路由僅限管理員（P22），送件需要 `Idempotency-Key`，回 202 與 `Location`；相同鍵與優先序重送回 200 加 `Idempotency-Replayed: true`。三種任務都在既有任務佇列中，可以 `POST /api/v1/jobs/{id}/cancel` 取消、在 `GET /api/v1/jobs` 觀察，並有自己的錯誤碼（`subtitle_cache_cleanup_failed`、`backup_reminder_failed`、`external_metadata_removal_failed`）。

## G13.6 其餘項目的對照

G13.6 原文列出：元數據刷新、NFO 寫回、圖片抓取／重建、探測與關鍵幀索引快取清理、提取字幕清理、統計聚合、資料一致性校驗、備份提醒。

| 項目 | 狀態 |
| --- | --- |
| 元數據刷新 | `metadata_refresh`（[元資料刷新任務](metadata-refresh-jobs.md)） |
| NFO 寫回 | `nfo_write`、`nfo_export` |
| 圖片抓取／重建 | `image_refresh` |
| 探測快取清理 | 探測快取在資料庫內，有 TTL 與配額回收（[探測快取](probe-cache.md)），不另設任務 |
| **關鍵幀索引快取清理** | **不適用**：Jelee 沒有關鍵幀索引。服務端只做原檔直投，Seek 由客戶端依自身索引定位、服務端以 Range 回應（G10.7「服務端不得為 Seek 重新編碼」），程式碼與資料庫都沒有關鍵幀表或其快取，因此沒有可清理的對象。若日後新增（G10.6 的啟動加速），應以同一套 `Kinds` 機制加一個種類 |
| 提取字幕清理 | **本頁** `subtitle_cache_cleanup` |
| 統計聚合 | 觀看統計的排程彙總（[觀看統計](watch-statistics.md)） |
| 資料一致性校驗 | `consistency_check`（[一致性檢查](consistency.md)） |
| 備份提醒 | **本頁** `backup_reminder` |

需求追蹤表本身不改，待合併時依本頁更新 G13.6 與 G14.7 的狀態欄。

## 執行個體層級任務掛在哪個媒體庫

`jobs.library_id` 必填，而且一個媒體庫同時只能有一個進行中的任務（`jobs_active_library_idx`）。快取清理與備份提醒不屬於任何媒體庫，送件時掛在**第一個沒有進行中任務的媒體庫**（依 ID 排序）：

- 這個媒體庫只是佔用它的佇列位置；任務處理的是整個快取或整個執行個體。`Job.libraryId` 就是這個錨點。
- 同一種類同時只能有一個進行中的任務，否則 409 `job_busy`；所有媒體庫都忙也是 409 `job_busy`；沒有任何媒體庫時 409 `conflict`。
- 兩個任務都很短（快取清理以檔案系統操作為主，提醒只有一次查詢），佔用期間該媒體庫的掃描會收到 409，稍後重送即可。

## 字幕擷取快取清理（`subtitle_cache_cleanup`）

快取是 `JELEE_MATROSKA_CACHE_ROOT`（[Matroska 工具](matroska-tools.md)）：`<根>/<媒體來源 ID>/<修訂>/{entry.json, t*.srt|ass…, 字型}`，修訂名是 32 位十六進位，另有擷取中的 `.staging-*`。原本只有寫入時的 LRU 容量上限（`JELEE_MATROSKA_CACHE_MAX_BYTES`，預設 1 GiB）；來源刪除後的目錄要等容量壓力才會被淘汰。

請求內容（皆可省略）：`{"priority":"manual"|"background","maxIdleDays":30}`，`maxIdleDays` 1–3650，0 或省略為 30 天。伺服器沒有啟用擷取（`JELEE_ENABLE_MATROSKA_EXTRACTION`）或快取根不是私有目錄（0700、非符號連結）時，送件回 503 `subtitle_cache_unavailable`；Windows 無法證明目錄私有，一律不可用（與擷取本身一致）。mkvtoolnix 執行環境缺失**不影響**清理：快取只是檔案。

### 清理規則

依來源目錄名稱遞增處理，每批 64 個目錄：

1. 先問資料庫哪些來源 ID 仍存在（`media_sources`）。不存在的來源，**整個來源目錄刪除**（計入 `orphaned`）。
2. 存在的來源，只看本套件寫的名字：
   - 修訂目錄（32 位十六進位）的 `entry.json` 讀不到、格式不對，或最後使用時間（`entry.json` 的修改時間，每次命中都會更新）早於 `maxIdleDays` 天前，刪除（計入 `revisions`）；
   - `.staging-*` 超過 1 小時（崩潰殘留），刪除；未滿 1 小時可能是進行中的擷取，保留；
   - 其他任何名字都不碰。
3. 全部來源處理完，依擷取器同一套計算（`entry.json` 的 bytes、最久未用者先刪）把快取壓到容量上限內（計入 `capacity`）。

安全邊界：所有操作經 `os.Root` 限定在快取根之內，不會跟隨符號連結離開；快取根下名稱不是 UUID 的東西不碰；刪除失敗（例如 Windows 上檔案正在串流）只計入 `failed`，留給下次。擷取快取是可重建的（[備份與還原](backup-restore.md)），刪掉的項目下次請求時重新擷取。

點陣字幕 OCR 的快取（`JELEE_SUBTITLE_OCR_CACHE_ROOT`）不在本任務範圍：它是另一個選用功能，有自己的容量上限與修訂規則，結構相同但條目格式不同；如需一併清理，可以同樣方式另加一個掃描器。

### 進度、取消與續跑

`subtitle_cache_cleanup_job_state` 每個任務一列：`phase`（`sources` → `capacity` → `done`）、`cursor_source`（最後完成的來源目錄）與計數。每批處理完在一個受租約保護的交易內推進游標與計數；取消會在目前來源完成後記錄已完成的部分再停止；租約遺失後的新租約從游標之後繼續，重做同一目錄也無害（刪除是冪等的）。

Job 的 `files` 是無失敗完成的目錄數、`skipped` 是有刪除失敗的目錄數、`bytes` 是釋放的位元組；`progress.total` 是第一次執行時列出的來源目錄數（估計值，與 `nfo_apply` 等相同）。

## 備份提醒（`backup_reminder`）

需求只寫「備份提醒」，沒有要求自動備份，所以這個任務**只檢查、不備份**。Jelee 看得到的備份只有 `jelee-cli metadata export`（每次寫一筆 `metadata.exported` 安全稽核）；`pg_dump` 在 Jelee 之外執行，無法觀察，文件中會明說。

- 送件：`{"priority":"manual"|"background","thresholdDays":7}`，`thresholdDays` 1–365，0 或省略用設定值。
- 判斷（`domain.EvaluateBackupStatus`，資料庫時鐘）：沒有任何匯出紀錄為 `never`；最後一次匯出超過門檻天數為 `overdue`；恰好門檻天數仍算 `ok`。
- `never` 或 `overdue` 時寫一筆 `backup.reminder` 稽核（後像含 `status`、`thresholdDays`、`lastExportAt`、`ageSeconds`、`trigger`），結果同時存在 `backup_reminder_job_state`。續跑的任務讀回已記錄的結果，不會重複寫稽核。
- `GET /api/v1/maintenance/backup-status` 以設定的門檻即時計算同樣的結果，不排任務，供健康狀態或前端顯示。
- 排程：`jobs.backupReminderDays`（`JELEE_JOB_BACKUP_REMINDER_DAYS`，0–365，預設 0＝關閉）大於 0 時，排程分派器（與掃描排程、一致性檢查同一個迴圈，優先序最後）每 24 小時排一個背景提醒，門檻為該天數；手動送件的預設門檻也取這個值，未設定時為 7 天。
- 通用排程（遷移 000107）：也可以用 `POST /api/v1/schedules` 建立 `kind=backup_reminder`（`params.thresholdDays`，0＝上述門檻）或 `kind=subtitle_cache_cleanup`（`params.maxIdleDays`）的伺服器層級排程，可設時區、窗口、停用與立即執行，到期以系統身分排入；環境變數排程保留相容，等價於每 86400 秒的 `backup_reminder` 排程，兩者並存時 24 小時內只排一次。見[任務排程](job-schedules.md#備份提醒的環境變數)。
- 限制：匯出紀錄是安全類稽核，保留期依 `audit_retention.security_days`（預設 365 天）；若門檻大於保留期，舊紀錄被清除後會顯示 `never`。

## 批次移除外部元資料（`external_metadata_removal`）

單筆入口 `DELETE /api/v1/items/{id}/metadata/external`（[移除外部元資料](tmdb-external-metadata-removal.md)）的媒體庫批次版。送件：

```json
{"confirm": true, "dropRejectedReviews": false, "priority": "manual"}
```

- `confirm` 必須為 `true`，缺少或為 `false` 回 400 `confirmation_required`，不建立任務；型別錯誤或多餘欄位 400 `invalid_request`。
- 不需要 TMDB 金鑰；金鑰撤除後仍可清除。
- 送件寫 `external_metadata_removal.submitted`（媒體庫、優先序、`confirmed`、`dropRejectedReviews`、候選數），結束寫 `external_metadata_removal.finished`（最終狀態與計數）。

### 每個條目的規則

候選條目：該媒體庫中有 `source='tmdb'` 欄位，或有自動配對待確認紀錄（`pending`；`dropRejectedReviews` 時也含 `rejected`）的條目，依 ID 遞增。每個條目在一個受租約保護的交易內：

1. 鎖定條目後，以單筆入口**同一個函式**（`removeExternalFields`）刪除未鎖定的 TMDB 欄位：Jelee 欄位鎖、NFO 值鎖或 NFO 鎖定意圖任一成立的欄位保留；人工、NFO、existing 來源本來就不是外部資料，不受影響；標題回退為本機檔名或目錄名，找不到本機來源時保留（`required`）。
2. 有欄位被刪除時，元資料修訂號 +1，並寫一筆 `item.external_metadata_removed` 稽核（操作者是送件的管理員，後像多一個 `jobId`；前像只記被刪欄位的供應商來源，不記被刪文字）。沒有欄位被刪時不推進修訂、不寫稽核——與單筆入口不同，批次不製造空修訂。
3. 刪除該條目的 `pending` 待確認（候選內含 TMDB 標題）；`dropRejectedReviews` 時也刪除 `rejected`。
4. 推進游標與計數。

計數：`removed`（至少刪一個欄位，`fields` 為欄位總數）、`kept`（還有 TMDB 欄位，但全部鎖定或必填）、`skipped`（只剩待確認紀錄、期間被刪除或移到別的媒體庫）、`failed`、`reviews`（刪除的待確認紀錄）。Job 的 `files`＝`removed+kept`、`skipped`＝`skipped+failed`，`progress.total` 是送件時的候選數（估計值）。

### 取消、續跑與單筆失敗隔離

- 取消在條目之間生效，已完成的條目全部保留；新租約從游標之後繼續，不重複計數或寫稽核。
- 每批讀取時確認送件者仍是啟用中的管理員，否則任務以 `external_metadata_removal_failed` 結束（權限被撤銷的管理員排的任務不會繼續改資料）。
- 單一條目被資料庫拒絕（約束、觸發器，即 `ErrInvalid`／`ErrConflict`）時，該條目的交易整個回滾，另以一筆交易記為 `failed` 並跨過；連續 10 筆失敗才中止任務。儲存層故障、租約遺失與取消則停止任務並保留已完成的部分。

### 與元資料刷新（`metadata_refresh`）的互動

- 兩者都是媒體庫任務，同一媒體庫不會同時執行。
- **待確認**：清除後 `pending` 紀錄一律刪除——它們存的是 TMDB 候選標題，而且是可重建的推導資料。
- **被拒絕的紀錄**：預設保留。它是管理員「不要自動配對這個條目」的決定；保留它，之後的自動刷新仍會跳過該條目，清除的結果不會被自動刮削寫回。代價是紀錄中最多 5 個候選的標題與日期仍在資料庫裡。需要連這些一起清掉時送 `dropRejectedReviews: true`；此後自動刷新可能重新配對並寫入這些條目。
- **已套用條目的提供者 ID**：清除後 `existing_provider` 依據消失，下一次刷新會回到 NFO 外部 ID 或標題＋年份配對。
- **自動刷新仍開啟時**：清除不會改媒體庫設定。開啟 `auto` 的媒體庫在下一次目錄同步成功後會排入刷新，高置信（外部 ID）配對會依門檻再寫入 TMDB 欄位。要讓清除結果保持，請先 `PUT …/metadata/refresh/settings` 關閉 `auto`（或 `autoApply=never` 讓結果只進待確認），或對要保留的欄位加鎖——鎖定的欄位刷新也不會寫。
- 備份：三張狀態表都屬於任務狀態，不進元資料備份。

## 遷移與指標

- `jobs.kind`、`jobs.error_code` 的 CHECK 加入三個種類與錯誤碼，另各有「錯誤碼只屬於自己種類」的約束。
- 工作指標維度加 3 個種類×2 優先序，共用工作指標固定 24 組、72 個 outcome 點；測試中的工作 exposition 為 22 families／903 series，最大測試刻度約 88 KiB，工作、資源、圖片 exposition 與執行期整合測試的上限調為 128 KiB（[指標](metrics.md)）。
- 新表：`subtitle_cache_cleanup_job_state`、`backup_reminder_job_state`、`external_metadata_removal_job_state`。
- 降級：有任一種類的任務、狀態列或非零指標時拒絕（`55000`），因為舊版無法執行或顯示它們。
- 任務執行器新增通用的 `Options.Kinds`（每個種類一個 worker、結束函式與錯誤碼），領取 SQL 以 `kind=ANY($18)` 接受，只認這三個名稱；之後新增同類維護任務不必再改領取條件。

## 驗證

- 真 PostgreSQL（`internal/adapter/postgres/maintenance_jobs_test.go`）：批次移除尊重欄位鎖與人工值、待確認與拒絕紀錄、逐條稽核含 jobId 且不含被刪文字、取消只保留游標之前、租約遺失續跑不重複、單筆被拒只計 failed；送件確認、重送、忙碌、錨點媒體庫；快取清理的取消（剛好一批）、續跑、孤兒／閒置／新鮮修訂；備份提醒 never／overdue／ok、續跑不重寫稽核、取消、排程每日一次；遷移往返與保留狀態拒絕降級。
- 單元：`internal/adapter/matroska/sweep_test.go`（只刪自己的名字、符號連結不跟隨、容量 LRU、不安全的快取根）、`internal/app/maintenance_workers_test.go`、`internal/platform/jobs/kinds_test.go`、`internal/adapter/http/maintenance_test.go`（含缺少確認 400）、`internal/domain/maintenance_jobs_test.go`。
- 反向驗證：暫時讓鎖定欄位也可移除，批次移除測試失敗；暫時把所有來源當作孤兒，快取清理測試失敗（存活來源的新鮮修訂被刪）；恢復後通過。
- 待擁有者驗證：B20（Windows）、C38（規模與長時間）、D38（真實資料），見[驗證清單](owner-verification-queue.md)。
