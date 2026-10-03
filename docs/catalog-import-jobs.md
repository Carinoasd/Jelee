# 持久批次匯入

管理員可把同次已接受掃描的1至100個影片候選排入工作佇列。每筆明確提供種類、標題與可選單集父層；不從檔名猜測影視資訊。

## 提交與查詢

```http
POST /api/v1/jobs/SOURCE_SCAN_ID/imports
Authorization: Bearer SESSION_TOKEN
Idempotency-Key: unique-confirmation-key
Content-Type: application/json

{"priority":"manual","items":[{"entryId":"INVENTORY_ENTRY_ID","title":"影片名稱","kind":"Movie"}]}
```

新任務回應202及 `data.id`，相同key與相同意圖回應200及原任務；不同意圖衝突。順序也是意圖的一部分。使用回傳的匯入任務ID查詢：

```http
GET /api/v1/jobs/IMPORT_JOB_ID
GET /api/v1/jobs/IMPORT_JOB_ID/imports
```

第一個入口回傳既有任務狀態，`kind` 為 `catalog_import`，`files` 為已完成項目數。第二個入口回傳 `total`、`completed` 與最多100筆結果；完成項目含 `itemId`、`sourceId`。結果不含絕對來源路徑。

僅支援現有可登記的mp4、mkv、webm、mov、avi、ts影片；種類為HomeVideo（預設）、Movie或Episode。每庫至多一個活動任務，沿用現有佇列容量及手動／背景優先級。請求JSON上限64 KiB，因此100筆長標題可能需要拆批。

## 取消、失敗與恢復

- 使用既有 `POST /api/v1/jobs/IMPORT_JOB_ID/cancel` 取消，body為 `{}`。
- 已完成的條目保留；取消或失敗不回滾先前成功項目。報告列出完成與未完成的項目，不能只看「任務失敗」便認為沒有寫入。
- 服務停止或租約到期後重新領取時，worker從下一筆未完成項目繼續。單筆item／source／audit與進度在同一交易，不能出現建立條目卻漏存進度。
- 終止後重新確認可用相同選取及新的Idempotency-Key提交新批次；相同已有條目回傳既有ID，不重複建立。既有 `/retry` 仍只支援掃描任務，此段不將它冒充批次重試。
- 根、已接受基線、候選或檔案變更會拒絕後續項目。失敗碼為 `catalog_import_failed`，逾時另為 `job_timeout`。重新掃描後應重新確認候選。

活動批次會保留來源掃描，避免歷史清理破壞未完成工作。任務終止後來源可依原有保留策略回收；相同key的結果重播只在任務歷史仍保留時成立。

## 權限與安全邊界

提交與報告使用有效管理員session。worker執行已保存的意圖，逐筆確認提交者仍為有效管理員；一般session登出不等於取消已提交任務。停權、刪除或移除管理員身分會阻止後續寫入。

worker必須宣告匯入能力才能領取此種類。檔案核對在交易外，提交時再查來源與租約。原檔不寫入；size／mtime與根內元件檢查沿用[單筆匯入邊界](inventory-import.md)，不宣稱檔案系統快照或永久不變。

## 資料庫與範圍

schema40新增不可變批次意圖及逐筆結果。001–039共78份SQL保持。仍有匯入任務、意圖或結果時，降版拒絕，避免丟失進度。

這是明確選取的有界批次；全庫自動辨識、自動匯入所有候選、監看／排程及大規模持續負載尚未完成。

## 驗證狀態

專項31個通過事件（含父測試），包含100筆實際worker、TLS入口、CLI回歸、取消恢復、舊owner拒絕、寫入／進度回滾、來源變更拒絕與活動來源保留。完整本地驗證已通過；遠端CI以推送後結果為準。

| 驗證 | 套件 | 通過事件（含父測試） | 略過 |
| --- | ---: | ---: | ---: |
| Windows全套 | 29 | 3314 | 550 |
| Linux race | 7 | 1321 | 0 |
| 完整PostgreSQL race | 1 | 901 | 0 |
| 原生worker | 1 | 11 | 0 |
| 完整HTTP／TLS／PG | 1 | 1 | 0 |

[完整證據](evidence/catalog-import-jobs.json)。vet、產品建置、增量品牌與gitignore通過；全量品牌與既有ABI差異仍未解決。
