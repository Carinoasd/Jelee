# NFO 匯出全部任務（G39.14）

管理員可把一個媒體庫所有條目目前儲存的 metadata 寫回各自既有的 NFO：一個 `nfo_export` 任務逐批準備受控文字編輯，再交給一般的 `nfo_write` 子任務寫檔。這是 G39.14 三種批次操作的第二種；[全量導入](nfo-apply-jobs.md)已完成，只匯出缺失（新建檔案）尚未完成。匯出任務自己沒有任何寫檔路徑：準備、備份、提交、恢復全是既有的[寫回任務](nfo-write-jobs.md)與[準備](nfo-write-preparations.md)。

## 提交

```http
POST /api/v1/libraries/LIBRARY_ID/nfo/export
Authorization: Bearer SESSION_TOKEN
Idempotency-Key: unique-confirmation-key
Content-Type: application/json

{"priority":"manual"}
```

- 僅管理員；`priority` 為 `manual`（預設）或 `background`，body 為 `{}` 亦可。
- 新任務 202 與 `Location`；相同 key、相同庫與優先級回 200 與 `Idempotency-Replayed: true`（庫之後關閉寫入權限也照樣重播）；同 key 不同庫、優先級或任務種類為 409 `conflict`。
- 庫不是 `read-only` 或沒有開啟寫回權限（`nfo_write_enabled`），或 runtime 沒有啟用 `enableNFOWrite`，為 409 `nfo_write_disabled`，與 `POST …/nfo/writes` 同一檢查。庫已有活動任務為 409 `job_busy`，全域佇列滿為 429 `job_queue_full`。
- 稽核事件 `nfo.export_submitted`（庫、優先級）。每個子任務另寫既有的 `nfo.write_submitted`（含 `exportJobId` 與 chunk 編號），每份準備寫既有的 `nfo.write_prepared`。

CLI：

```sh
jelee-cli nfo export --library ID [--priority manual|background] --key KEY --token-stdin [--url ORIGIN] [--json]
jelee-cli nfo export-status --job ID --token-stdin [--url ORIGIN] [--json]
```

## 觀察

`GET /api/v1/jobs/JOB_ID/nfo-export`（僅管理員）：

```json
{"jobId":"…","libraryId":"…","cursorItemId":"…","childJobId":null,"chunks":3,"written":34,"unchanged":51,"skippedMissing":17,"skippedInvalid":17,"skippedOther":17,"failed":0,"updatedAt":"…"}
```

`cursorItemId` 在第一個檢查點前為 `null`；`childJobId` 是尚未計入的 `nfo_write` 子任務，沒有時為 `null`；`chunks` 是已提交的子任務數。其他種類的任務回 404。既有 `GET /api/v1/jobs/JOB_ID` 的 `files` 為 written＋unchanged，`skipped` 為三種 skipped 加 failed，`kind` 為 `nfo_export`。

## 資料庫到 NFO 的對應

每個條目讀目前的 metadata（修訂版與已儲存的欄位）與它的 NFO，只為**已儲存且與 NFO 不同**的欄位建立編輯，欄位以準備支援的受控文字元素寫入；不同的欄位才寫，沒有差異的條目不準備、不寫檔。

| 資料庫欄位 | NFO 元素 | 種類 |
| --- | --- | --- |
| `title` | `title` | 電影、影集、季、單集 |
| `originalTitle` | `originaltitle` | 全部 |
| `overview` | `plot` | 全部 |
| `sortTitle` | `sorttitle` | 全部 |
| `tagline`、`outline`、`mpaa`、`certification` | 同名 | 全部 |
| `showTitle`（事實） | `showtitle` | 單集 |
| `seriesStatus`（事實） | `status` | 影集 |

- 編輯用既有的受控編輯：未知標籤、屬性、註解與排版原樣保留，BOM 保留原樣，NFO 缺少的元素會新增（`createMissing`），備份保留 3 份（`.jelee.bak`）。與單筆寫回相同，寫入時會補上缺少的 Jelee 識別碼（`uniqueid type="jelee"`）。
- 只匯出已儲存的值。仍是掃描得到的名稱（來源 `existing`）的標題不算儲存值；空值**不會**清掉 NFO 內的內容，所以尚未導入過的庫不會被檔名覆蓋 NFO。
- 不匯出的欄位：`date`、年份、片長、評分、類型等列表、演員、識別碼、系列／季／集數字與時間等；準備的受控編輯目前只支援上表的文字元素。
- 電影、影集（`tvshow.nfo`）、季（`season.nfo`）、單集（`episodedetails`）都走同一條路徑；真實 PG 測試各有一套。其他種類或沒有唯一媒體來源、NFO 範圍不成立、條目已刪除的條目歸入 `skippedOther`。

| 計數 | 條件 |
| --- | --- |
| written | 子任務的條目已被取代（結算階段 replaced） |
| unchanged | 沒有任何已儲存值與 NFO 不同 |
| skippedMissing | 條目沒有 NFO 檔（新建檔案是任務三的第 3 項） |
| skippedInvalid | NFO 無效 |
| skippedOther | 無法準備：NFO 帶全域鎖（`lockdata`）、差異欄位全被 NFO 鎖住（被鎖的欄位不編輯，其餘照寫）、無媒體來源、不支援的種類、已刪除 |
| failed | 條目層級的錯誤（例如修訂版或來源衝突經一次重試仍衝突）、已準備但子任務沒有取代的條目（子任務失敗、取消、被拒絕） |

單一條目失敗不會停止任務。資料庫、租約、授權與 context 錯誤停止執行並保留游標；失敗碼為 `nfo_export_failed`，逾時為 `job_timeout`。寫入權限在任務途中被收回時，下一個子任務的提交被拒（409 對應的 `nfo_write_disabled`），任務失敗，不再寫檔。

## 不佔住 worker 的協調

匯出任務從不在 worker 內等子任務，否則只有一個 worker 時會死結。協調用三個零件：

1. **一庫一活動任務的例外。** 資料庫以唯一索引保證一庫同時只有一個 queued／running 任務。遷移 000089 把索引改成 `(library_id, (export_job_id IS NOT NULL))`：新欄位 `jobs.export_job_id`（`parent_id` 已用於重試，不能共用）由匯出子任務填入匯出任務的 ID，所以一庫最多一個活動的頂層任務加一個活動的匯出子任務；`CHECK` 限制只有 `nfo_write` 任務能有 `export_job_id`。索引的部分條件不變，工作指標查詢仍用它。
2. **領取閘門。** 匯出任務的子任務是 queued 或 running 時，領取查詢不會選它（`nfo_export_job_state.child_job_id` 指向的任務）。閘門不靠計時，子任務一終止，下一次輪詢就能領取。
3. **讓出（yield）。** 每次領取只推進一步：(a) 把已終止的子任務結算進計數；(b) 從游標往後準備條目，直到有 16 份準備或該批 50 個條目讀完；(c) 沒有需要寫的條目就在同一步直接記入游標與計數，繼續往下讀；(d) 有準備就在**一個受租約保護的交易**內建立子任務、複製其工作意圖、刪除這一 chunk 的準備、寫入子任務 ID、chunk 編號、游標與計數；(e) 以 `ErrNFOExportYield` 結束這一步。runner 在等監控執行緒結束後呼叫 `YieldNFOExport`，把任務放回 queued 並退還這次領取的 attempt（讓出不消耗重試次數），然後 worker 立刻能領取子任務。子任務終止後，匯出任務再被領取，從 (a) 繼續；沒有條目可讀時以成功結束。

子任務用匯出任務的政策欄位建立（不再檢查佇列上限，那在匯出任務准入時檢查過），優先級與匯出任務相同。一個 chunk 最多 16 份準備（每個 actor 的準備上限是 32 份，留一半給管理員自己的準備），遠低於每個 `nfo_write` 任務 100 個條目的上限。chunk 在 16 份準備或一次 50 個條目的讀取結束時截止，所以子任務數取決於需要寫入的比例。

### 取消

- 取消用既有 `POST /api/v1/jobs/JOB_ID/cancel`。沒有子任務的 queued 匯出任務立即 `cancelled`。有子任務時，同一交易要求子任務取消（queued 的立即取消、running 的在下一個心跳停止並由既有的恢復迴圈回復），匯出任務留在佇列中，等子任務終止後被領取，結算子任務的結果（已取代的條目記入 `written`，其餘記入 `failed`）再以 `cancelled` 結束。running 的匯出任務取消時，結束交易同樣先要求子任務取消，子任務仍在跑就把自己放回佇列做同樣的收尾。
- 取消之後不再準備新的 chunk；游標之後的條目與檔案不被碰。已經完成的 chunk 不會被還原。

### 租約到期與恢復

- 每個步驟的持久狀態只有 `nfo_export_job_state` 一列，所有變更在受租約保護的交易內，所以租約遺失的 worker 無法留下半個子任務。子任務 ID、chunk 編號、游標與計數同交易提交，不存在「子任務已建立但沒記錄」的窗口。
- 在提交之前租約到期：這一 chunk 的準備留在準備表。重新領取後以同一組冪等鍵（`nfo-export:<任務>:<chunk>:<條目>`）重播，取回第一次保存的準備與固定 UUID，再提交一次；子任務鍵為 `nfo-export:<任務>:<chunk>`，並受 `UNIQUE(actor_id, idempotency_key)` 保護，不會產生兩個子任務。
- 在提交之後、讓出之前租約到期：領取時的過期租約清理把任務放回佇列（消耗一次 attempt），下一步看到子任務仍在跑就讓出，終止了就結算。
- 任務結束（任何終止狀態）會刪除它名下殘留的準備；成功提交的 chunk 在提交交易內就刪除準備，所以大批匯出不會碰到每 actor 32 份的上限。這個刪除是新增的：此前準備只靠 24 小時過期清理。
- 子任務被歷史修剪移除時，它的條目計為 `failed`。

## 執行身分

與全量導入相同：執行身分是提交者，每一步取其目前有效的 session 作為 actor，所以任務可以比提交時的 session 活得久，但提交者被停權、刪除、失去管理員身分或沒有任何有效 session 時，下一步即停止（失敗），不再準備或建立子任務。子任務的 `actor_id` 是提交者，其執行與一般寫回任務相同（不依賴 session）。

## 限制

- 只匯出上表的文字欄位；其他 NFO 內容（日期、評分、演員、識別碼等）不被覆寫。往返（匯出後再全量導入）的資料庫 metadata 不變，唯一差異是寫入時補上的 Jelee 識別碼會在導入後出現在 `uniqueIds`（型別 `jelee`），且僅限實際寫過的條目。
- 沒有 NFO 檔的條目只計數，不建立檔案（任務三第 3 項）。
- 空值不清除 NFO 內容；要清掉 NFO 欄位必須在 NFO 內處理。
- 被 NFO 鎖住的欄位與 `lockdata` 的條目不寫（鎖是 NFO 對外部編輯的聲明）。
- 條目的 `written` 只在子任務終止後才加進計數，所以進行中的 chunk 不在其中；`failed` 也會包含被取消的子任務未寫的條目。
- 準備表滿（其他人佔用）時，已備妥的條目先提交，下次再繼續；一個都備不出來就失敗。
- 一庫一個活動頂層任務；沒有跨庫批次。每份準備和每個子任務各寫稽核，量隨寫入條目數成比例。
- 遷移 000089 新增 `nfo_export_job_state`（每任務一列，隨任務歷史清理），並擴充 `jobs.kind`、錯誤碼、工作指標維度（共 14 組）、`jobs_export_job_kind_check` 與新的活動任務索引；仍有 `nfo_export` 任務、狀態列或帶 `export_job_id` 的子任務時拒絕降級。該表不進中繼資料備份。
- 能領取匯出任務的 runner 必須同時能領取 `nfo_write`（`enableNFOWrite`），`jobs.New` 強制這點。
