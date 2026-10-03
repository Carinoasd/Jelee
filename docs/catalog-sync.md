# 刪除確認、目錄級並發與掃描→條目同步（schema58）

## 刪除確認（G13.4）

缺失數或比例達閾值時，掃描以 `reviewRequired=true` 結束且保留舊基準。管理員確認後：

```http
POST /api/v1/jobs/REVIEWED_SCAN_ID/accept-missing
{"expectedMissing":15}
```

只接受：`inventory_scan`、succeeded、reviewRequired、零 skipped、非忽略模式、為該庫最新一次掃描、庫範圍世代未變、目前基準重算的缺失數等於任務記錄且等於 `expectedMissing`。成功回 202 與一個 `catalog_sync`（mode=accept）任務；已有進行中或已發布的接受再次提交回 409（不重播）。接受後若該任務被取消或失敗而未發布，可再次接受。

發布由 worker 在租約下分批執行：每批 128 筆複製到不可見快照，最後一批切換 `active_inventory_snapshot` 並把基準版號加一；每批檢查租約、取消、提交者仍為管理員、來源掃描與接受紀錄、舊基準未變。過期或被取代的 owner 寫入整批回滾。稽核：`job.missing_accepted`、`inventory.baseline_accepted`。

## 目錄級並發（G13.5）

`JELEE_SCAN_DIRECTORY_CONCURRENCY`（1–16，預設 2）。一個掃描任務的租約下最多 N 個目錄同時讀取；`ClaimScanDirectory` 以租約 generation 加 slot token 認領目錄，其他 slot 不會拿到同一目錄，持有他人 token 的批次被拒。新 generation（租約遺失後重新領取）使舊認領失效，未完成目錄從頭重掃並扣除部分觀測。第一個錯誤取消其他 slot；取消旗標、心跳、時窗仍由原 monitor 負責。檢查點仍以目錄為單位，結果與循序版一致（1000／100 混合庫回歸）。

## 掃描→條目同步（W8）

新任務種類 `catalog_sync`（不是掃描任務的附加階段）：掃描任務在發布交易中已終結，另一任務可獨立取消、續跑、重試並計入既有指標；接受缺失的發布也走同一任務。庫預設關閉；`PUT /api/v1/libraries/{id}/catalog-sync {"auto":true}` 後，每次發布基準的同一交易會排入同步（佇列滿或庫忙則記 `catalog_sync.deferred`）。`POST /api/v1/libraries/{id}/catalog-sync`（Idempotency-Key）手動排入；`GET /api/v1/jobs/{id}/catalog-sync` 查計數。

階段：publish（僅 accept）→ sources → missing → pending → done，游標與計數和寫入同交易。每批讀 256 筆基準影片、最多寫 32 個檔案。

- 解析：`medianame.ParsePath`。只有 High 且容器可登記才自動建 Movie 或 Series／Season／Episode；Medium／Low／拒絕／不支援容器／與既有結構衝突進 `catalog_scan_pending`，以 `GET /api/v1/libraries/{id}/catalog-sync/pending` 查詢。接受／拒絕待確認項目的入口尚未提供（後續）。
- 結構：電影以「同目錄＋標題＋年份」歸為同一條目（多版本的第一步，完整聚合留給 G20 後續）；劇集資料夾名稱與解析標題相符時登記為 Series 目錄，季資料夾登記為 Season 目錄（供目錄 NFO 使用）；已登記的資料夾條目直接沿用。父子連結套用與匯入相同的合法性規則。
- 優先序：解析值以 `source='scan'` 寫入標題，為最低優先；鎖定、人工、NFO、TMDB 與既有值都不被覆蓋，TMDB 可覆蓋 scan 值。NFO 身分優先：已帶 NFO uniqueIds 的條目不併入另有同名 NFO 的新檔案。
- 增量：已追蹤檔案以 size／mtime／解析版本判定未變；明確匯入的來源不屬同步管理，從不更動。
- 缺失：一般同步只標 `missing_since`；只有 accept 模式才刪除同步登記的來源，並回收沒有媒體、子項、人工／外部值與 facts 的同步建立條目。

## 未涵蓋

待確認項目的人工接受／拒絕、完整多版本聚合、忽略模式發布後的自動同步、NFO 內容直接參與分組（目前只用 NFO 檔存在與條目 uniqueIds facts）。大規模首掃／重掃、GOMAXPROCS=2/4、24 小時混合負載尚待擁有者量測。
