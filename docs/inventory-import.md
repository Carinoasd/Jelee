# 從掃描候選匯入影片

本機管理員先完成掃描，再從 `jobs inventory` 取得候選 ID，執行：

```text
jelee-cli import-inventory --job JOB_ID --entry ENTRY_ID --title "影片名稱" --kind Movie
```

`--kind` 支援 HomeVideo（預設）、Movie、Episode。Episode 可加 `--parent ITEM_ID`，父層沿用同庫、同根、實際目錄包含關係檢查。這是具有資料庫管理權限的本機命令；已登入管理員也可使用下方 API。

## 接受條件

- 指定候選屬於該庫最新掃描，任務成功、未要求取消且不需覆核。
- 根世代與目前相同；已接受基線的最新觀察包含該影片，而且種類、大小與修改時間一致。
- 檔案存在於登記根內，根本身及根內路徑元件沒有符號連結；檔案為普通檔案，size／mtime 符合盤點。
- 在資料庫短交易中重新解析候選，必須與檔案核對前取得的完整候選相同。

新建 item、media source、可選父層及含 job／entry ID 的稽核同一交易提交。重複位置拒絕，不改寫原條目，也不留下孤立 item。錯誤訊息不輸出來源絕對路徑或資料庫連線資訊。

## 邊界

本命令只登記操作者確認的單筆影片；不執行 probe、NFO 套用或原檔寫入。全庫批次與自動辨識仍待接續。重新掃描會使舊候選失效；忽略規則排除而保留的舊基線也不能用來匯入。

檔案核對是 size／mtime 的檢查點。它不識別保留這兩個屬性的內容修改，也不保證核對後檔案不再改變。根上層目錄、掛載點與其他程序的檔案操作不因此受到控制。

## 驗證

真實Scanner、CLI與隔離PostgreSQL成功匯入並可從catalog查詢，重複匯入不留下額外item。候選失效及三個寫入位置的故障回滾通過；檔案變更與符號連結拒絕由CLI檢查測試覆蓋。兩平台結果見[證據](evidence/inventory-import.json)。

## 已登入管理 API

```http
PUT /api/v1/jobs/JOB_ID/entries/ENTRY_ID/item
Authorization: Bearer SESSION_TOKEN
Content-Type: application/json

{"title":"影片名稱","kind":"Movie"}
```

回應為 `data.itemId` 與 `data.sourceId`；Episode 可加 `parentId`。只允許管理員，來源解析前與提交交易內均重新查有效身分，檔案核對在交易外進行。此操作使用現有 jobs 開關與請求容量限制。

PUT 相同資料且來源條件仍有效時回傳相同 ID，不新增條目或稽核。已存在但標題、種類或父層不同時回傳409，不覆寫人工內容。新掃描、候選失效或來源變更後，重試可被拒絕；這不是跨任意歷史狀態永久保存的結果快取。CLI 的重複匯入依舊拒絕。

實際 TLS／PG 驗收涵蓋匿名401、非管理員403、成功與可瀏覽、PUT重試、衝突、來源欄位注入拒絕及撤銷session。來源核對期間撤權／停權／降權／到期與六路並行PUT另由應用／Store整合驗證；六路只提交一份item、source與audit。

### API階段驗收結果

| 驗證 | 套件 | 通過事件（含父測試） | 略過 |
| --- | ---: | ---: | ---: |
| Windows全套 | 29 | 3313 | 542 |
| Linux race | 6 | 1147 | 0 |
| 專項PG／CLI／TLS | 1 | 22 | 0 |
| 最終TLS與OpenAPI | 1 | 1 | 0 |

見[API驗收證據](evidence/inventory-api.json)。Windows略過項不能視為本輪PG通過項；本輪原生專項無略過。
