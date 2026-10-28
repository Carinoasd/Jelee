# NFO 全量導入任務（G39.14）

管理員可把一個媒體庫所有條目的 NFO 欄位排入單一 `nfo_apply` 任務，經由[單筆套用](nfo-item-metadata.md)同一條路徑寫入資料庫。這是 G39.14 三種批次操作的第一種；[匯出全部](nfo-export-jobs.md)已完成，只匯出缺失尚未完成。

## 提交

```http
POST /api/v1/libraries/LIBRARY_ID/nfo/apply
Authorization: Bearer SESSION_TOKEN
Idempotency-Key: unique-confirmation-key
Content-Type: application/json

{"priority":"manual"}
```

- 僅管理員；`priority` 為 `manual`（預設）或 `background`，body 為 `{}` 亦可。
- 新任務 202 與 `Location`；相同 key、相同庫與優先級回 200 與 `Idempotency-Replayed: true`；同 key 不同庫、優先級或任務種類為 409 `conflict`。
- 庫的 NFO 政策不是 `read-only` 為 409 `conflict`。庫已有活動任務為 409 `job_busy`，全域佇列滿為 429 `job_queue_full`，沿用 `nfo/validate` 的容量與歷史規則。
- 稽核事件 `nfo.apply_submitted`（庫、優先級）。每個實際寫入的條目另寫既有的 `item.nfo_metadata_applied`。

CLI：

```sh
jelee-cli nfo apply --library ID [--priority manual|background] --key KEY --token-stdin [--url ORIGIN] [--json]
jelee-cli nfo apply-status --job ID --token-stdin [--url ORIGIN] [--json]
```

## 觀察

`GET /api/v1/jobs/JOB_ID/nfo-apply`（僅管理員）：

```json
{"jobId":"…","libraryId":"…","cursorItemId":"…","applied":60,"unchanged":20,"skipped":40,"failed":0,"updatedAt":"…"}
```

`cursorItemId` 在第一個檢查點前為 `null`。其他種類的任務回 404。既有 `GET /api/v1/jobs/JOB_ID` 的 `files` 為 applied＋unchanged，`skipped` 為 skipped＋failed，`kind` 為 `nfo_apply`。計數在每個檢查點（50 個條目）提交，因此最多落後一批。

## 執行與分類

worker 宣告套用能力才會領取此種類。條目依 ID 遞增、從游標之後每批 50 個處理；每個條目讀目前的 metadata（得到修訂版與套用前狀態），以 `confirmed=true` 呼叫 `Metadata.ApplyNFO`，所以欄位鎖、人工值與所有驗證都是單筆路徑的，批次不重寫合併。

| 結果 | 條件 |
| --- | --- |
| applied | 套用後儲存的欄位值、來源、鎖或種類與套用前不同 |
| unchanged | 套用成功但儲存內容相同（單筆路徑仍會重寫 NFO 來源列與增加修訂版） |
| skipped | 無 NFO、NFO 無效、條目沒有唯一媒體來源、NFO 範圍不成立（`metadata unavailable`），或條目已被刪除 |
| failed | 其他只與該條目有關的錯誤，例如修訂版／來源衝突經一次重讀重試後仍衝突 |

單一條目失敗不會停止任務。資料庫、租約、授權與 context 錯誤停止執行並保留游標；失敗碼為 `nfo_apply_failed`，逾時為 `job_timeout`。

執行身分是提交者：每批取其目前有效的 session 作為 actor，所以任務可以比提交時的 session 活得久，但提交者被停權、刪除、失去管理員身分或沒有任何有效 session 時，下一批即停止（失敗），不再寫入。

## 取消與恢復

- 取消用既有 `POST /api/v1/jobs/JOB_ID/cancel`，在條目之間檢查；已套用的條目保留，進行中批次已完成的條目仍記入游標與計數，任務為 `cancelled`。
- 服務停止、租約到期或程序當機後重新領取，從 `cursor_item_id` 之後繼續。檢查點與計數、`jobs.files`／`jobs.skipped` 在同一個受租約保護的交易。
- 檢查點之間的當機會讓該批已完成的條目再處理一次：資料結果相同（第二次為 unchanged），但該批的 applied 計數可能變成 unchanged，條目修訂版多加一次。游標之前的條目不會重做。

## 限制

- 不處理 NFO 檔案內容的寫回；匯出全部見 [nfo-export-jobs.md](nfo-export-jobs.md)，只匯出缺失屬任務三第 3 項。
- 以條目為單位的 `item.nfo_metadata_applied` 稽核會隨庫大小成比例增加。
- 任務以庫為單位，一庫一個活動任務；沒有跨庫批次。
- 遷移 000088 新增 `nfo_apply_job_state`（每任務一列，隨任務歷史清理），並擴充 `jobs.kind`、錯誤碼與工作指標維度；仍有 `nfo_apply` 任務或狀態列時拒絕降級。該表不進中繼資料備份。
