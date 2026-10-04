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

## 外掛字幕／音軌配對（G15.3、G16.2，W10-8，schema 65）

掃描本身不變：外掛檔照常以 `other` 進入清單與基準，不會被當成條目、來源或待確認項目。配對發生在同步的 sources 階段，與來源建立／更新**同一個交易**：

- **配對規則**：`domain.PairSidecarFiles`。影片同目錄，以及該目錄下 `Sub`／`Subs`／`Subtitle`／`Subtitles`（字幕）與 `Audio`／`Audios`（音軌）子目錄（只比 ASCII 大小寫）中的檔案，依 `ParseSidecarName` 的命名規則（`名稱.語言[.forced][.sdh|.cc][.default][.commentary].副檔名`、同名多軌、多語標記）配給「基底名最長」的影片；`Movie.Part2.en.srt` 屬於 `Movie.Part2.mkv` 而非 `Movie.mkv`。基底名在大小寫折疊後相同的兩部影片（`Movie.mkv` 與 `Movie.mp4`）視為歧義，兩邊都不配。字幕放在 `Audio/`、音軌放在 `Subs/`、更深一層、別的目錄都不配。每個來源最多 256 軌（依路徑取前 256）。
- **批次查詢**：每批 256 部影片只多兩條 SQL——一條以 `inventory_sidecar_owner(path)`（遷移 065 的不可變函式＋部分索引 `library_inventory_sidecar_owner_idx`，只涵蓋 `video`／`other` 列）一次取出這些影片所屬目錄的全部候選檔，一條以 `source_id=ANY(...)` 取出既有軌。沒有逐檔查詢。函式與 `domain.SidecarOwnerDirectory` 必須一致（真 PG 測試逐例比對）。
- **寫入**：與既有軌比對（大小／mtime 未變則沿用已檢測的字元集與指紋），完全相同就不寫；否則呼叫 `UpsertSidecarTracks`（以來源為單位整批替換、per-source 序列化、`media.sidecars_changed` 稽核只記數量不記檔名）。影片未變但外掛軌增／刪／改名，同樣在增量重掃時反映，並計入該批的 32 筆寫入上限；來源計數（created／updated／unchanged）不因外掛變化而改變，批次稽核 `catalog_sync.batch` 另帶 `sidecarSources`。
- **忽略規則**：只採用發布目標基準那次掃描實際觀測到的外掛檔（`observed_revision` 等於目標版號）。忽略模式下被排除的檔案在基準中保留舊觀測，因此不會變成軌道；條目本身仍依原規則處理。
- **刪除**：影片在一般同步中被標記缺失時，其外掛軌一併清空（稽核 removed＝原軌數）；影片重新出現時再配對。接受缺失（accept）刪除來源時，外掛軌經外鍵 `ON DELETE CASCADE` 一起刪除。明確匯入的來源不屬同步管理，同步不為它們配對外掛軌。
- **字元集與指紋**：寫入時兩者先留空（大小＋mtime 代表內容）。同步各階段完成後、任務結束前，worker 在交易之外逐頁（每頁 64 軌，`media_sidecar_tracks_uninspected_idx`）讀取 `fingerprint IS NULL` 的軌：指紋沿用媒體探測的邊緣指紋（`probe.EdgeFingerprint`：大小＋頭尾各至多 64 KiB 的 SHA-256，不讀整檔），文字字幕再以 `subtitles.DetectCharset` 讀至多 1 MiB 判斷字元集，低信心不記錄。檔案以 `os.OpenRoot` 唯讀開啟，前後比對大小與 mtime，期間被改寫、已消失或已被替換就跳過，下次掃描再處理；寫回時再以大小＋mtime＋`fingerprint IS NULL` 為條件並受租約保護。從不轉碼、改名或改寫原檔。
- **直投**：寫入的列即 `/api/v1/sources/{id}/subtitles|audio/{trackId}` 的資料來源（見 `direct-delivery.md`）。

## 未涵蓋

待確認項目的人工接受／拒絕、完整多版本聚合、忽略模式發布後的自動同步、NFO 內容直接參與分組（目前只用 NFO 檔存在與條目 uniqueIds facts）、明確匯入來源的外掛軌配對、`.idx`＋`.sub` 成對關係的記錄（兩者各自成軌）。大規模首掃／重掃、GOMAXPROCS=2/4、24 小時混合負載尚待擁有者量測。
