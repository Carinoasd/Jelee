# 圖片刷新任務（G40.9、G40.10）

`image_refresh` 任務把一個媒體庫中**尚未有內容**的遠端圖片（`item_images.source_kind='remote'`，來自 NFO 掃描入庫或元資料匯入）抓下來，驗證後放進持久存放區，並回填該列的 `content_sha256`、寬高、格式、大小與抓取時間；之後 `/images` 與相容層才能使用這些圖片。這是[擁有者任務二](owner-dev-tasks.md)的第 2 項；掃描入庫見[圖片掃描入庫](image-ingest.md)，管理 API 見[圖片資產](image-assets.md#管理接口g4010g4011)。

## 啟用與「僅本地圖片」模式

| 設定 | 環境變數 | 預設 | 說明 |
| --- | --- | --- | --- |
| `enableImageFetch` | `JELEE_ENABLE_IMAGE_FETCH` | `false` | 允許刷新任務對外抓取圖片。需要同時啟用 jobs、images 與 `JELEE_IMAGE_STORE_ROOT`，否則設定驗證失敗 |

預設關閉，也就是 G40.9 的「僅本地圖片」模式：伺服器不發出任何圖片請求，刷新任務**不能送件**（404 `image_unavailable`）。已排入的刷新任務在任何設定下都會被 runner 領取；沒有抓取能力（關閉抓取或沒有存放區）的 runner 會讓它以 `image_refresh_failed` 明確失敗，而不是永遠留在佇列裡占住媒體庫。歷史任務的進度在關閉後仍可查詢。

## 送件與觀察

```http
POST /api/v1/libraries/LIBRARY_ID/images/refresh
Authorization: Bearer SESSION_TOKEN
Idempotency-Key: unique-key
Content-Type: application/json

{"priority":"manual"}
```

- 僅管理員；`priority` 為 `manual`（預設）或 `background`，body 為 `{}` 亦可。
- 新任務 202 與 `Location`；相同 key、相同庫與優先級回 200 與 `Idempotency-Replayed: true`；同 key 不同庫、優先級或任務種類為 409 `conflict`。庫已有活動任務為 409 `job_busy`，全域佇列滿為 429 `job_queue_full`，庫不存在為 404。
- 稽核事件 `image.refresh_submitted`（庫、優先級）。
- 取消用既有 `POST /api/v1/jobs/JOB_ID/cancel`。

`GET /api/v1/jobs/JOB_ID/image-refresh`（僅管理員）：

```json
{"jobId":"…","libraryId":"…","cursorImageId":"…","fetched":6,"failed":1,"deferred":0,"skipped":0,"updatedAt":"…"}
```

`cursorImageId` 在第一個檢查點前為 `null`；其他種類的任務回 404。既有 `GET /api/v1/jobs/JOB_ID` 的 `kind` 為 `image_refresh`，`files` 為 fetched，`skipped` 為 failed＋deferred＋skipped。沒有 CLI 命令。

| 計數 | 條件 |
| --- | --- |
| fetched | 取得內容並寫入該列 |
| failed | 抓取或驗證失敗，失敗原因與下次重試時間記在該列 |
| deferred | 同一網址先前失敗、重試時間未到，這次不抓 |
| skipped | 抓取期間該列被鎖定、改成手動、換了網址、已有內容或被刪除，這次結果不寫入 |

## 哪些列會被抓

只選「`remote`、沒有內容、未鎖定、非手動」的列（`NOT locked AND NOT manual`），依列 ID 遞增、從游標之後每次讀 32 列。

- **鎖定與手動列從不被選取、從不被寫入。** G40.10 要求使用者鎖定的圖片不被刷新覆蓋；`manual` 列（管理員寫的、元資料匯入或遷移 000094 標成手動的）是人的選擇，刷新不接手。這代表沒有內容的手動遠端列刷新後仍然沒有內容；要讓它可用，請上傳圖片或改用掃描來源。
- **已有內容的列不再抓。** 重跑任務不會重新下載；網址改變時，掃描入庫會清掉舊內容，這列就會再次成為候選。
- **寫入時再確認一次。** 每筆結果在受租約保護的交易內，先鎖條目（與所有圖片寫入者相同的順序：條目 → 圖片列），再以 `id`、條目、媒體庫、**相同網址**以及上述條件做條件式 `UPDATE`。抓取期間管理員鎖定、上傳、改成手動或刪除，條件不成立，內容不寫入，計為 skipped；下載的位元組只留在內容定址的存放區，由存放區自己的淘汰處理。

## 抓取

每列透過 `internal/adapter/images/fetch.go` 的 `RemoteFetcher` 下載，外連規則全部沿用：

- **受控傳輸。** 只用 `internal/platform/outbound` 的 client：僅 HTTPS、預設埠、不帶帳密、每次重新導向都檢查目標、DNS 結果固定，私有、迴環與其他非公網位址一律拒絕（SSRF）。開發者模式的 `relax_ssrf_strict`（G45.4）開啟時與 Webhook 一樣允許私有目標。
- **來源規則。** `https://image.tmdb.org/` 的網址走 TMDB 規則（只接受官方圖片主機上的標準路徑，不跟隨重新導向）；其他網址走 NFO 規則（最多 3 次重新導向）。
- **限流與退避。** 全域同時 4 個、每主機同時 2 個、同一主機請求間隔 250ms；429／503 依 `Retry-After` 冷卻該主機，暫時性錯誤指數退避重試，單張總時限 2 分鐘、單次 45 秒。單一任務本身一次只抓一列。
- **大小上限。** 回應主體上限等於上傳上限（`JELEE_IMAGE_MAX_SOURCE_BYTES`，且不超過單圖處理預算），宣告或實際超過即拒絕。
- **內容不被信任。** `Content-Type` 必須是圖片且與魔數一致；讀完主體後交給與上傳相同的驗證：頭部尺寸預檢、記憶體估算（像素炸彈 413）、完整解碼一次，通過後才寫入內容定址的存放區。AVIF 等無法解碼的格式記為 `unsupported`。驗證在圖片處理槽內執行；槽被觀看請求占滿時等待（每 250ms 重試，在單次時限內），不會失敗。
- **工作預算。** 刷新用的 outbound client 不帶共享 I/O 預算：存放是在回應仍開著時於處理器的准入內進行，下載若同時持有 I/O 許可再等另一個許可，預算飽和時會等自己。網路端由抓取器自身的併發與每主機間隔限制。
- **不記錄網址。** 錯誤、計數、稽核與失敗紀錄都只有穩定的原因代碼與 HTTP 狀態碼，不含網址、主機、標頭或回應內容。

## 失敗隔離與重試

單列失敗只影響該列：原因寫進 `item_image_fetch_failures`（每列一筆），任務繼續下一列。

| 原因 | 種類 | 首次延遲 |
| --- | --- | --- |
| `url_rejected`、`target_denied`、`redirect_denied`、`too_large`、`content_type_rejected`、`format_rejected`、`not_found`、`status_rejected`、`unsupported` | 永久性（來源回答了並拒絕，或內容不是可用圖片） | 1 天，每次再失敗加倍，最多 30 天 |
| `rate_limited`、`unavailable`、`timeout`、`store_failed`、`busy`、`local_only` | 暫時性 | 1 小時，每次再失敗加倍，最多 7 天 |

- 紀錄綁定失敗時的網址：網址改了就不算同一個失敗，立即成為候選，次數從 1 重新計算。
- 重試時間未到的列計為 deferred，不發出請求。成功後刪除該列的失敗紀錄；圖片列刪除時紀錄一併刪除。
- 沒有「立即重試」的開關；需要時刪除該列的失敗紀錄或等延遲結束。
- 只有與單列無關的錯誤會讓任務停止：資料庫、租約、取消、執行時限，以及抓取器回報的非單列錯誤。失敗碼為 `image_refresh_failed`，逾時為 `job_timeout`。

## 取消與續跑

- 每抓完一列就提交一個檢查點（該列結果、游標、計數與 `jobs.files`／`jobs.skipped` 同一交易）；重試時間未到的列併入下一個檢查點。取消或執行時限會中斷進行中的請求，已提交的列保留，中斷的那列沒有寫入任何東西，任務為 `cancelled`。
- 服務停止、租約到期或程序當機後重新領取，從 `cursor_image_id` 之後繼續；已記錄的列不會再抓。被中斷而沒記錄的那一列會再抓一次，結果相同（內容定址）。
- 失去租約的舊 worker 無法再提交任何結果（租約圍欄）。

## 遷移 000096（暫定編號）

- `jobs.kind` 新增 `image_refresh`、錯誤碼 `image_refresh_failed`（只能用於該種類）；工作指標維度增為 16 組（`image_refresh` × background／manual）。
- `image_refresh_job_state`：每任務一列，游標與 fetched／failed／deferred／skipped，隨任務歷史清理。
- `item_image_fetch_failures`：每個遠端列最後一次失敗的網址、次數、原因、HTTP 狀態、嘗試與重試時間；隨圖片列刪除。
- 部分索引 `item_images_refresh_idx (library_id,id) WHERE source_kind='remote' AND content_sha256 IS NULL`：只有等待內容的列負擔這個索引。
- 降級：仍有 `image_refresh` 任務、狀態列或該種類的非零指標時拒絕；否則移除兩張表、索引與約束。失敗紀錄只是重試延遲，直接丟棄；已抓到的內容留在 `item_images`。
- 兩張新表都不進中繼資料備份（任務狀態與可重建的衍生資料）。

## 驗證

真 PG（`internal/adapter/postgres/image_refresh_jobs_test.go`，抓取全部走本機 `httptest`，以測試傳輸層取代 outbound client，不連外網；處理器與存放區是正式實作）：

- `TestImageRefreshJobFetchesOnlyUnlockedRemoteRows`：6 個可抓列、1 個 404、鎖定列、手動列、已有內容列與本地列；可抓列各只請求一次、內容與寬高格式正確、原圖在存放區；404 列記 `not_found`／404／延遲 1 天，其他列不受影響；鎖定、手動、已有內容與本地列整列（`to_jsonb`）不變且從未被請求。第二次執行不發任何請求、所有列不變（冪等），404 列計為 deferred；重試時間過後再抓、延遲加倍；網址改變後立即重抓並清除失敗紀錄。
- `TestImageRefreshJobLockWinsDuringFetch`：下載途中把列鎖定並改為手動，內容不寫入、計為 skipped，前後兩列照常寫入。
- `TestImageRefreshJobCancelAndResume`：第四列請求阻塞時取消任務，任務 `cancelled`、恰好三列有內容；新任務由手動領取的 worker 跑兩列後中斷並讓租約過期，runner 重新領取後從游標繼續，所有列有內容，只有被中斷的兩列各多請求一次；舊租約無法再提交。
- `TestImageRefreshJobAdmission`：重播、同 key 改優先級 409、庫忙碌、庫不存在、初始狀態、稽核、取消排隊中的任務。
- 遷移：`TestImageRefreshMigrationRoundTrip`（降級移除兩張表、拒絕新種類，升級後可送件）、`TestImageRefreshMigrationRefusesRetainedJobs`。
- 單元：`internal/app/image_refresh_worker_test.go`（失敗隔離、延遲計算、每抓一列一個檢查點、取消後續跑不重抓、非單列錯誤停止、未設定抓取時失敗）、`internal/adapter/images/refresh_test.go`（驗證後存放、TMDB 與 NFO 規則、各失敗原因與永久／暫時分類、錯誤不含網址、處理槽滿時等待、未設存放區與僅本地模式）、`internal/domain/image_refresh_jobs_test.go`。
- 反向驗證：拿掉候選條件中的 `NOT g.locked AND NOT g.manual`，`TestImageRefreshJobFetchesOnlyUnlockedRemoteRows`（鎖定與手動列被抓、被改寫）與 `TestImageRefreshJobLockWinsDuringFetch` 失敗。

## 限制

- 手動或鎖定、但沒有內容的遠端列不會被抓（見上）。
- 只抓沒有內容的列；不會定期重新驗證已抓內容是否在來源端更新。
- 單一任務逐列抓取；大量遠端圖片的媒體庫需要較長時間，數量級與長時間行為尚未量測（見[擁有者驗證清單](owner-verification-queue.md) C25）。
- 沒有自動排程；刷新由管理員送件。
- 對真實外部主機（TMDB、任意 NFO 網址）的抓取沒有在開發端執行過（D26）。
