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

階段：publish（僅 accept）→ sources → missing → pending → versions（先組多分片版本，再做跨目錄聚合）→ done，游標與計數和寫入同交易。每批讀 256 筆基準影片、最多寫 32 個檔案。

- 解析：`medianame.ParsePath`。只有 High 且容器可登記才自動建 Movie 或 Series／Season／Episode；Medium／Low／拒絕／不支援容器／與既有結構衝突進 `catalog_scan_pending`，以 `GET /api/v1/libraries/{id}/catalog-sync/pending` 查詢（每筆帶 `decision`：`pending`／`rejected`）；接受與拒絕見下方「識別修正與待確認決定」。
- 結構：新檔案以「同目錄＋標題＋年份」（電影）或「同季＋同目錄＋集號」（單集）歸入已有條目；分放在其他目錄的同一作品由同步最後的 versions 階段依 NFO 外部 ID 與時長合併（見下方「跨目錄多版本聚合」）；劇集資料夾名稱與解析標題相符時登記為 Series 目錄，季資料夾登記為 Season 目錄（供目錄 NFO 使用）；已登記的資料夾條目直接沿用。父子連結套用與匯入相同的合法性規則。
- 優先序：解析值以 `source='scan'` 寫入標題，為最低優先；鎖定、人工、NFO、TMDB 與既有值都不被覆蓋，TMDB 可覆蓋 scan 值。NFO 身分優先：已帶 NFO uniqueIds 的條目不併入另有同名 NFO 的新檔案；條目的 NFO 季號／集號與新檔案檔名不同時（且該條目的季集號沒有識別鎖），新檔案也不併入，改成單檔條目並記 `grouping_reason='nfo_numbers_differ'`（G21.2）。
- 增量：已追蹤檔案以 size／mtime／解析版本判定未變；解析版本是 `medianame-v2` 加上媒體庫啟用的命名選項（見下方「命名選項」），所以改選項等同換解析版本。明確匯入的來源不屬同步管理，從不更動。
- 缺失：一般同步只標 `missing_since`；只有 accept 模式才刪除同步登記的來源，並回收沒有媒體、子項、人工／外部值與 facts 的同步建立條目。

## 同步後的元資料刷新（G13.6，schema 103）

媒體庫開啟自動刮削（`PUT /api/v1/libraries/{id}/metadata/refresh/settings` 的 `auto`）時，`catalog_sync` 成功結束的同一交易在 savepoint 內排入一個背景優先的 `metadata_refresh` 任務；忙碌、佇列滿或沒有有效管理員時改寫 `metadata_refresh.deferred`，其他錯誤只回滾 savepoint，都不影響同步本身。TMDB 的失敗只發生在刷新任務內。配對、門檻與待確認見[元資料刷新任務](metadata-refresh-jobs.md)。

## 外掛字幕／音軌配對（G15.3、G16.2，W10-8，schema 65）

掃描本身不變：外掛檔照常以 `other` 進入清單與基準，不會被當成條目、來源或待確認項目。配對發生在同步的 sources 階段，與來源建立／更新**同一個交易**：

- **配對規則**：`domain.PairSidecarFiles`。影片同目錄，以及該目錄下 `Sub`／`Subs`／`Subtitle`／`Subtitles`（字幕）與 `Audio`／`Audios`（音軌）子目錄（只比 ASCII 大小寫）中的檔案，依 `ParseSidecarName` 的命名規則（`名稱.語言[.forced][.sdh|.cc][.default][.commentary].副檔名`、同名多軌、多語標記）配給「基底名最長」的影片；`Movie.Part2.en.srt` 屬於 `Movie.Part2.mkv` 而非 `Movie.mkv`。基底名在大小寫折疊後相同的兩部影片（`Movie.mkv` 與 `Movie.mp4`）視為歧義，兩邊都不配。字幕放在 `Audio/`、音軌放在 `Subs/`、更深一層、別的目錄都不配。每個來源最多 256 軌（依路徑取前 256）。
- **批次查詢**：每批 256 部影片只多兩條 SQL——一條以 `inventory_sidecar_owner(path)`（遷移 065 的不可變函式＋部分索引 `library_inventory_sidecar_owner_idx`，只涵蓋 `video`／`other` 列）一次取出這些影片所屬目錄的全部候選檔，一條以 `source_id=ANY(...)` 取出既有軌。沒有逐檔查詢。函式與 `domain.SidecarOwnerDirectory` 必須一致（真 PG 測試逐例比對）。
- **寫入**：與既有軌比對（大小／mtime 未變則沿用已檢測的字元集與指紋），完全相同就不寫；否則呼叫 `UpsertSidecarTracks`（以來源為單位整批替換、per-source 序列化、`media.sidecars_changed` 稽核只記數量不記檔名）。影片未變但外掛軌增／刪／改名，同樣在增量重掃時反映，並計入該批的 32 筆寫入上限；來源計數（created／updated／unchanged）不因外掛變化而改變，批次稽核 `catalog_sync.batch` 另帶 `sidecarSources`。
- **忽略規則**：只採用發布目標基準那次掃描實際觀測到的外掛檔（`observed_revision` 等於目標版號）。忽略模式下被排除的檔案在基準中保留舊觀測，因此不會變成軌道；條目本身仍依原規則處理。
- **刪除**：影片在一般同步中被標記缺失時，其外掛軌一併清空（稽核 removed＝原軌數）；影片重新出現時再配對。接受缺失（accept）刪除來源時，外掛軌經外鍵 `ON DELETE CASCADE` 一起刪除。明確匯入的來源不屬同步管理，同步不為它們配對外掛軌。
- **字元集與指紋**：寫入時兩者先留空（大小＋mtime 代表內容）。同步各階段完成後、任務結束前，worker 在交易之外逐頁（每頁 64 軌，`media_sidecar_tracks_uninspected_idx`）讀取 `fingerprint IS NULL` 的軌：指紋沿用媒體探測的邊緣指紋（`probe.EdgeFingerprint`：大小＋頭尾各至多 64 KiB 的 SHA-256，不讀整檔），文字字幕再以 `subtitles.DetectCharset` 讀至多 1 MiB 判斷字元集，低信心不記錄。檔案以 `os.OpenRoot` 唯讀開啟，前後比對大小與 mtime，期間被改寫、已消失或已被替換就跳過，下次掃描再處理；寫回時再以大小＋mtime＋`fingerprint IS NULL` 為條件並受租約保護。從不轉碼、改名或改寫原檔。
- **`.sub` 內容判別與 `.idx`＋`.sub` 成對（B4，schema 109）**：同一趟檢查對 `.sub` 讀檔頭至多 4 KiB（`domain.SniffSubtitleSub`）：MPEG-PS pack header `00 00 01 BA` 為 VobSub 資料，第一個非空行是 `{n}{n}` 幀號（可有 UTF-8／UTF-16 BOM）為 MicroDVD 文字，結果記在 `subtitle_kind`（其他內容為 NULL）；VobSub 一律不記字元集，即使偵測器猜到。成對只看檔名：同一來源、同一目錄、去掉副檔名後完全相同的 `.idx` 與 `.sub`，`.idx` 列的 `paired_track_id` 指向 `.sub` 列（自參照外鍵 `(paired_track_id,source_id)`，`ON DELETE SET NULL (paired_track_id)`，不能跨來源、只有 `.idx` 能指）。每次該來源的外掛軌有變動時以一條 SQL 重算；`.sub` 消失時外鍵自動解除成對，`.idx` 仍是軌。播放資訊不把成對的 `.sub` 列為獨立軌，`.idx` 帶 `pairedTrackId`，`.sub` 本身仍可經同一字幕路由取原檔。升級到 schema 109 時，既有 `.idx` 立即成對，既有 `.sub` 清掉指紋排回下一趟檢查（同時重判字元集），音軌、`.sub`、`.sup` 全部排為待探測。
- **按需探測（B4，schema 109）**：音軌、`.sub`、`.sup` 寫入時 `probe_state='pending'`，其他文字字幕為 `not_applicable`（資料庫 CHECK 限定只有這三類可排程）。檢查之後、圖片入庫之前，worker 以 `NextSidecarProbes` 逐頁（每頁 16 軌，部分索引 `media_sidecar_tracks_probe_pending_idx`）取出待探測軌，每個檔在交易之外、IO 工作配額下以**同一個沙箱 ffprobe runtime**（媒體探測的 `IsolatedRunner`、同樣的進程上限與 30 秒逾時；新的封閉操作 `sidecar`，只把 demuxer 白名單換成外掛音軌與字幕需要的格式，見 `docs/direct-delivery.md`）探測，每次同步至多 512 個，其餘留給下次同步。結果寫回 `codec`、`profile`、`channels`、`channel_layout`、`sample_rate`、`bit_rate`、`duration_micros` 與探測時的 `probed_size`／`probed_modified_unix_nano`；寫回條件是列仍為 pending 且大小＋mtime 等於探測的那一版。檔案本身的失敗（ffprobe 拒絕、輸出不合白名單或超限、沒有音訊／字幕串流）記為 `failed`，同一版檔案不再重試；忙碌、逾時、檔案變動或讀不到則保持 pending；runtime 不可用就結束這一趟。資料庫 CHECK 保證 ready／failed 的探測戳記必須等於列的戳記，重掃發現大小、mtime、種類或格式改變時，同一條 UPDATE 把探測欄位與 `subtitle_kind` 清回初始狀態，之後重新檢查與探測。探測結果不進探測快取，不改探測身分（仍是 `media-metadata-v5`）。
- **直投**：寫入的列即 `/api/v1/sources/{id}/subtitles|audio/{trackId}` 的資料來源（見 `direct-delivery.md`）。

## 圖片入庫（G40.2、G40.3，schema 94）

外掛軌檢查之後、內嵌封面之前，worker 以 `IngestItemImages` 逐頁（每頁 64 個條目、每頁一個帶 lease fence 的交易）把整個媒體庫的圖片引用寫入 `item_images`：條目目錄中依 G40.2 命名的圖片（`local`）與 NFO art（root 內路徑為 `nfo`、`https://` 網址為 `remote`，不抓取）。只讀發布的基準與目錄資料，不開檔、不連網；基準被新的盤點取代時整段停止（`Stale`）。鎖定與手動列不動，掃描寫入的 local／nfo 列在檔案或引用消失時移除。詳見 [image-ingest.md](image-ingest.md)。

## 人工版本決定（G20.3，schema 78）

管理員的拆分、合併與排除不會被同步還原：已登記來源本來就不被搬動，人工放置的來源（`catalog_scan_sources.manual`）不再以檔名改寫標題；新檔案分組時，指向已排除條目的群組改用單檔群組（與 NFO 身分衝突相同），被合併條目的群組經 `catalog_scan_item_aliases` 回到目標條目。詳見[多版本](item-versions.md)。

## 識別修正與待確認決定（G21.5，schema 100）

遷移 100（暫定編號）新增 `item_identity_corrections` 與 `catalog_scan_pending.rejected_at`／`rejected_by`。

- **修正即鎖定**：管理員以 `PUT /api/v1/items/{id}/identity` 修正條目的類型、所屬（季／劇集）、季號、集號（含多集範圍），每個修正值都鎖定；`locked:false` 解鎖，把該欄交還掃描。介面細節見[人工元資料](item-metadata.md#識別修正與鎖定g215schema-100)。
- **掃描值照常更新、鎖定值優先讀出**：同步重新解析已追蹤檔案（大小、mtime 或解析器版本改變）時，照常把檔名得到的季號、集號寫回 `catalog_scan_items`（以前只更新標題）；鎖定的季號／集號存在 `item_identity_corrections`，讀取優先序為「鎖定 > NFO 事實（`seasonNumber`／`episodeNumber`）> 檔名」（G21.2）。所以鎖定期間重掃不會改變有效值，報告的 `protected` 計入被擋下的變化；解鎖後立即回到（最新的）掃描值，不必等下一次掃描。季資料夾圖片配對也改用鎖定的季號。
- **類型與所屬**：同步本來就不搬動已登記的來源，也不重新推導類型；改過類型的條目，其掃描列跟著換類型，之後檔名推得的舊類型與它不符，重新解析不再改它的標題與集號。分組時新檔案（例如同集的另一個版本）經原本的群組鍵併入已鎖定的條目，不會把它拆開或改掛到檔案所在的季。
- **剪除**：接受缺失後，帶任何識別鎖的同步建立條目和帶欄位鎖的一樣保留，不因沒有媒體而被刪。
- **待確認接受**：`POST /api/v1/libraries/{id}/catalog-sync/pending/{entry}/accept`，正文可補齊或更正解析值（`kind`：`movie`／`episode`、`title`、`seriesTitle`、`year`、`season`、`episode`、`episodeEnd`；省略者沿用解析值，集數或季號缺失回 400）。在與同步批次相同的媒體庫列鎖下執行：檔案必須仍是已接受基準觀測到的那一個（大小、mtime 相同）且尚未登記，否則 409；容器無法登記也回 409。之後走與自動同步相同的建立路徑（分組、劇集／季資料夾、父子合法性，結構衝突回 409）。新建的條目把類型與（劇集的）季號、集號記為鎖定，給了 `title` 時標題記為人工值；併入既有條目時不改那個條目的識別。回 `{entryId,itemId,sourceId,kind,created}`，待確認列刪除。稽核 `catalog_sync.pending_accepted`（不記路徑）。
- **待確認拒絕**：`POST …/pending/{entry}/reject`（正文 `{}`）標記為拒絕，列表仍可見（`decision:"rejected"`）。拒絕只對當時的檔案有效：大小與 mtime 不變時，連解析器版本改變也不再規劃；檔案一變就回到 `pending`。重複拒絕回 409（不重播）；已拒絕的項目仍可接受。檔案離開基準時與一般待確認列一起刪除。稽核 `catalog_sync.pending_rejected`。
- **權限**：四個入口都只限管理員（一般使用者在查找前就回 403）；不存在的條目、不存在的待確認項目、或以另一個媒體庫的路徑存取的待確認項目一律回 404，回應相同。
- **降版**：仍有鎖定的修正或拒絕紀錄時拒絕降版（`55000`）；只剩未鎖定的列可以降版。元資料備份匯出修正列（`item_identity_correction`，目的地已有修正的條目保留自己的），不匯出拒絕紀錄。

## 命名選項、跨季合訂與特別篇歸類（G21.3，schema 102）

遷移 102（暫定編號；101 保留給另一分支）在 `libraries` 新增 `naming_date_episodes`、`naming_absolute_episodes`（預設皆 `false`），在 `catalog_scan_items` 新增 `season_end`、`absolute`、`air_date`、`special`。

- **設定入口**：管理員 `GET`／`PUT /api/v1/libraries/{id}/naming`，正文 `{"dateEpisodes":bool,"absoluteEpisodes":bool}` 兩欄都必填（整份替換，避免漏寫的選項被悄悄關掉）；回 `{libraryId,dateEpisodes,absoluteEpisodes,parserVersion}`。值有變才寫稽核 `library.catalog_naming_changed`（before／after 只有兩個布林）。寫入時取與同步批次相同的媒體庫列鎖，一個批次只會用到舊選項或新選項其中一組。
- **重解析**：每列存的解析版本是 `domain.CatalogSyncParserVersionFor(選項)`：`medianame-v2`，啟用日期加 `+date`、啟用絕對集數加 `+absolute`。改選項不立刻排任務；下一次同步（自動或手動）看到已追蹤檔案與待確認列的版本不同，就用新規則重新規劃。已登記的來源照舊不搬動，所以關掉選項不會拆掉已建的條目；鎖定的季號／集號照樣優先讀出，被擋下的變化計入 `protected`（G21.5）。被拒絕的待確認項目在檔案不變時仍然維持拒絕。基底版本從 `medianame-v1` 升到 v2（新增跨季合訂與下方的 Season 0 規則），升級後第一次同步會把全庫已追蹤檔案各重新規劃一次。
- **日期型集數**（`dateEpisodes`）：解析器的 `Options.DateEpisodes` 由媒體庫設定傳入。`2024-03-15`、`2024.03.15`、`2024_03_15`、`2024 03 15`、`2024年3月15日` 對應到「季＝年份、集＝月×100＋日」（例：第 2024 季第 315 集），`air_date` 記播出日。理由：每天一集的節目沒有可靠的序號，月日編碼在同一年內唯一、可排序且不必查其他檔案；跨年自然分季。檔案位在別年份的季資料夾（`Season 1/… 2024-03-15`）時季號互相矛盾，進待確認。同一天的兩個檔案會被視為同一集的兩個版本。
- **絕對集數**（`absoluteEpisodes`）：解析器的 `Options.AbsoluteEpisodes` 讓「沒有任何季資訊」的集數（`EP01`、`第01話`、`#01`，以及字幕組常見的 ` - 01`、`[01]`）視為明確集數，信心 High；規劃層把它放在第 1 季，`absolute` 記原始絕對集數。純數字（`Title 05 [1080p]`）仍是低信心猜測，進待確認；有季資料夾或季標記時不算絕對集數。理由：Jellyfin／Emby 在沒有季資訊時也把動畫掛在第 1 季，之後若 NFO 或 TMDB 給出季別，可用識別修正鎖定（G21.5）。
- **跨季合訂**：`S01E10-S02E01`、`S01E09-E10-S02E01`、`1x10-2x01` 解析為起始季、起始集與結束季（`Parsed.SeasonEnd`）、結束集。條目掛在起始季，`season`／`episode` 是起點，`episode_end` 是結束季裡的集號，`season_end` 記結束季；`GET /api/v1/items/{id}/identity` 的 `scan.seasonEnd` 可查。分組鍵另含結束季，跨季檔案不會和同季的 `S01E10` 合成同一條目的兩個版本。往前跨季（`S02E10-S01E01`）不是範圍，只取起點。理由：起始季是播放順序上第一次需要它的地方，識別修正（G21.5）的集數範圍仍只描述同一季，所以跨季資訊只存在掃描值。
- **OVA／SP／特別篇**：`OVA`、`OAD`、`SP`、`スペシャル`、`特別篇`、`番外篇`、`Specials`／`Season 0` 都歸到該劇集的第 0 季（`Specials`），`special` 記 `ova`／`oad`／`sp`／`extra`／`season`。帶集號的特別篇旁邊若只有裸季標記（`S01 OVA 02`、`第1季 OVA 2`），改歸第 0 季，季標記作為被覆蓋的證據，不再被當成第 1 季第 2 集而和正篇合併；明確的 `S01E05 OVA` 仍以 `S01E05` 為準。沒有集號的特別篇（`Title OVA.mkv`）信心不足，進待確認。
- **劇場版**：`劇場版`／`剧场版`／`Gekijouban` 一律是獨立的 Movie，不掛在劇集下，即使檔案在劇集或季資料夾裡；`special` 記 `theatrical`。解析信心固定為 Medium（劇場版後面的標題常與劇集名混在一起），所以一律經待確認接受，接受成電影時保留 `theatrical`。要把它放進劇集的第 0 季，用識別修正改類型與所屬並鎖定，之後重掃不會改回（類型不同的掃描值不改條目）。
- **與 NFO、鎖定的關係**：以上都是檔名值，寫在 `catalog_scan_items` 的掃描欄位。讀取優先序不變：鎖定 > NFO 事實（`seasonNumber`／`episodeNumber`）> 檔名（G21.2、G21.5）；NFO uniqueIds 衝突時照舊不併組。
- **降版**：仍有任何媒體庫啟用命名選項時拒絕降版（`55000`）；選項都關閉時可降版，四個條目欄位直接捨棄（只是檔名事實，舊版解析版本不同，下一次同步會重新規劃）。元資料備份匯出並匯入媒體庫的兩個選項與條目的四個欄位（舊備份沒有這些鍵時視為關閉／空值）。
- **待確認列**：仍只記 `absolute` 與 `special`；日期型與跨季的檔案絕大多數直接成為條目，進待確認時（例如季資料夾衝突）由管理員以接受正文補季號與集號。

## 跨目錄多版本聚合（G20.1、G20.5、G21.2，schema 104）

遷移 104（暫定編號；103 保留給另一分支）新增同步階段 `versions` 與報告計數 `aggregated`、`catalog_scan_items.grouping_evidence`／`grouping_reason`、`item_version_operations.automatic`，以及兩個索引（`catalog_scan_items(library_id,item_id)` 限 Movie／Episode；`item_metadata_facts` 上只涵蓋 NFO `uniqueIds` 的 GIN `jsonb_path_ops`）。

- **時機**：新檔案登記時 NFO 還沒套用（NFO 全量導入或自動刮削在同步之後），所以跨目錄的副本先各自成為條目；pending 之後的 versions 階段逐頁（每批 128 個條目、最多 8 次合併）讀取帶 NFO `uniqueIds` 的同步條目，把它們合併。NFO 或探測在同步之後才到時，在下一次同步合併。
- **只判斷證據變了的條目**：每個條目存一份證據摘要（`domain.VersionGroupingEvidence`：分組版本 `domain.CatalogGroupingVersion`、類型、NFO uniqueIds、有效季集號、保留原因、每個版本的探測時長）。摘要與上次相同就跳過，所以重複同步只讀不判；新的 NFO、新的探測、加鎖或解鎖、人工操作都會讓條目重新判斷。升級後所有條目的摘要是 NULL，第一次同步把全庫判斷一次；之後改了規則就提升 `CatalogGroupingVersion`，效果相同。
- **找對象**：只用強識別碼 `imdb`、`tmdb`、`tvdb`。對每個識別碼以 GIN 包含查詢找同媒體庫、同類型、帶相同（照原樣拼寫的）識別碼的同步條目，每個識別碼最多 16 個。只靠 `custom` 之類的其他識別碼不合併。
- **合併條件**（`domain.DecideVersionGrouping`，全部成立才合併，否則不合）：
  1. 兩邊都不是保留的條目（下一條）。
  2. 同類型（Movie 或 Episode），任何提供者上都沒有衝突的值（`ExternalIDConflicts`，不分大小寫），且至少一個強識別碼的值相同。
  3. 單集的有效季號、集號、結束集號都已知且相同。有效值的優先序與識別 API 相同：識別鎖 > NFO 事實（`seasonNumber`／`episodeNumber`）> 檔名（G21.2）。檔名 `S01E05` 但 NFO 說第 6 集的檔案，會和真正的第 6 集合併；這時合併的稽核證據帶 `nfo_numbers_over_filename`，識別 API 的 `episodeSource:"nfo"` 與 `scan.episode` 也能看出兩者不同。
  4. 兩邊每個版本都有可用的探測時長（探測快取 `ready`，大小與 mtime 等於登記時的檔案），且兩邊任兩個版本的時長差 ≤ 容許值。容許值＝較長者的 2%，最少 30 秒、最多 3 分鐘。
- **門檻理由**：外部 ID 是主要證據，但 NFO 可能抄錯（季包裡整季共用劇集 ID、刮削器把重拍片配到原版），時長是獨立於 NFO 的旁證。同一來源的不同編碼（1080p／4K／HDR／不同音軌）時長差通常在幾秒內；30 秒下限吸收容器與編碼的捨入、片頭商標；2% 隨片長放寬（兩小時的電影約 2.4 分鐘）；3 分鐘上限把導演剪輯版、加長版擋在外面——那是不同剪輯，管理員可以手動合併（G20.3）。沒探測、或時長是 0，一律不合（寧可不合）。PAL 加速（約 4%）的版本因此不會自動合併，列入 D37 由真實資料確認。
- **保留的條目不被改動**：有任何識別鎖（類型、所屬、季號、集號，G21.5，`locked`）；涉及任何人工版本操作（拆分、合併、主版本、解除排除，或任何被撤銷過的操作，包括被撤銷的自動合併）、有排除紀錄或有人工放置的來源（G20.3，`manual`）；或有不屬同步管理的版本（明確匯入，`imported`）。這些條目既不會被吸收，也不會成為合併目標。
- **合併本身**：與管理員合併同一條路徑（`mergeVersionItems`：G20.5 邊界、播放中／探測中／NFO 寫入中／有分享連結即拒絕、使用者資料與歷史依合併策略轉移、30 天內可撤銷），只是沒有操作者：`item_version_operations.automatic=true`、`actor_id` 為 NULL，稽核仍是 `item.versions_merged`，細節多 `automatic:true` 與 `evidence`（例如 `["external_id:tmdb","duration"]`）。版本較多的條目留下（相同時取 ID 較小者）。條目忙碌時記 `busy` 且不存摘要，下一次同步再試；邊界拒絕時記 `incompatible`。管理員以 `POST /api/v1/version-operations/{id}/undo` 撤銷後，兩個條目都因「被撤銷的操作」而保留，之後不會再自動合併。
- **只增不拆**：versions 階段只合併，從不拆分；已登記的來源仍不被同步搬動。條目的判斷結果存在 `grouping_reason`（`merged`、`alone`、`external_id_conflict`、`no_shared_external_id`、`numbers_differ`、`duration_unknown`、`duration_mismatch`、`locked`、`manual`、`imported`、`incompatible`、`busy`、`nfo_numbers_differ`），目前只供查資料庫與除錯，沒有 API。
- **人工合併的邊界跟著改**：`MergeItems` 比較季集號時也改用有效值（鎖 > NFO > 檔名），檔名不同但 NFO 相同的兩個單集現在可以手動合併；NFO 或鎖不同時照舊拒絕。
- **降版**：允許。執行中的同步若停在 versions 階段直接標為 done；自動合併留下為一般合併，舊版仍可撤銷；三個新欄位與兩個索引丟棄。再升級後這些合併的 `automatic` 是 false，相關條目會被當成人工決定而不再自動合併（保守）。

## 多分片版本（G21.1，schema 106）

遷移 106（暫定編號；105 保留給另一分支）新增 `media_source_parts`（每片一列：來源、所屬版本的第一片、片號 1..99）、`catalog_scan_sources.part_reason` 與 `catalog_sync_requests.parts_cursor`／`parts_done`，以及索引 `catalog_scan_sources(library_id,item_id)`。

- **決定（我們已決定）**：同一目錄、同標題年份（電影）或同季同集（單集）、檔名只差 Part 標記且片號連續 1..n（n ≥ 2）的檔案，合成同一條目的**一個版本、多個分片**；分片有序，第一片就是這個版本（版本 ID＝第一片的來源 ID，主檔）。不轉碼、不串接（ADR 0007 只直放）：每一片仍是一個原樣直放的來源。
- **為什麼它們已經在同一條目**：Part 標記不在分組鍵裡（電影是目錄＋標題＋年份，單集是季＋目錄＋集號），所以 sources 階段本來就把 `Movie (2010) - cd1.mkv`、`cd2.mkv` 登記成同一條目的兩個版本；這一步只把它們改成一個版本的兩片。
- **時機**：versions 階段開頭，先於跨目錄聚合。逐頁（每批 128 個條目）讀取「有兩個以上掃描來源、或已有分片結果」的同步條目，以 `medianame` 重新解析每個檔名的 `Part` 與 `PartStem`（小寫檔名、含副檔名，Part 標記換成 `#`，例如 `movie (2010) - #.mkv`），交給 `domain.PlanSourceParts`。結果與現有列相同就不寫，所以重複同步只讀不寫；檔案新增、消失或改名在下一次同步反映。只有一個檔案的條目不判斷（沒有可合併的對象）。
- **合併條件**（全部成立才合併）：同根目錄＋同目錄＋同 `PartStem`（等於同標題、年份、發行標記、容器；`CD1` 與 `Part 2`、全形 `ＣＤ１` 的 stem 相同，視為同一版本）；片號不重複且正好 1..n、n ≥ 2。
- **不合併與原因**（`catalog_scan_sources.part_reason`，只記帶 Part 標記的檔案；每個檔案維持為獨立版本）：`gap`（缺片、不從 1 開始，如 Part 1＋Part 3）、`duplicate`（兩個檔案同片號）、`single`（同 stem 只有一片）、`mixed`（只有跨不同發行標記或容器才湊得成 1..n，例如 720p cd1＋1080p cd2，屬不同版本）、`manual`（見下）；合併者記 `merged`。目前只供資料庫查詢與除錯，沒有 API。
- **與多版本並存**：一個條目可以同時有多個版本，每個版本各自可以有分片（例如 720p 兩片、1080p 三片、2160p 單檔＝三個版本）。播放資訊、檔案資訊、預設版本選擇（G20.2）、相容層只把第一片當版本；後續片不會出現在版本清單。
- **跨目錄聚合（G20.1）**：多分片版本算一個版本，時長取各片總和（任一片未探測即視為未知），所以 `A/Movie - cd1/cd2` 可以和 `B/Movie.mkv` 依同一門檻自動合併；合併後兩個目錄的分片各自保留。
- **鎖定與人工版本操作不被改**：條目涉及任何人工版本操作（拆分、合併、主版本、解除排除，或任何被撤銷的操作）、排除或人工放置的來源時，分片結構凍結：既有的分片列不動、新檔案也不會被組進去，帶 Part 標記的檔案記 `manual`。識別鎖只鎖類型、所屬與季集號，不影響分片。人工操作的邊界見[多版本](item-versions.md#多分片版本g211schema-106)。
- **刪除**：接受缺失刪除某片的來源時，其分片列經外鍵一起刪除；刪的是第一片時整個版本解散（`version_source_id` 也是 `ON DELETE CASCADE`），同一個同步的分片步驟再依剩下的檔案重新判斷（例如剩 cd2、cd3 → `gap`）。一般同步只標 `missing_since`，分片不變。
- **待確認**：`Movie.2010.cd1.avi` 這類 Medium 信心的檔名照舊進待確認；接受後經同一分組加入條目，下一次同步的分片步驟才組成分片。
- **稽核**：有寫入的批次記 `catalog_sync.batch`，細節 `partItems`（寫入的條目數）與 `multiPartVersions`（這些條目現有的多分片版本數），不含路徑。
- **降版**：允許，直接丟棄分片表、`part_reason` 與游標欄位；各片回到「各自是一個版本」。再升級後下一次同步重建（人工凍結的條目除外）。元資料備份不匯出分片表（衍生資料，匯入後由同步重建）。

## 未涵蓋

識別修正與待確認決定的網頁介面、自動合併原因（`grouping_reason`）與分片原因（`part_reason`）的 API 與網頁顯示、只有一個檔案的條目的分片原因、只靠解析度／HDR／剪輯版標記而沒有 NFO 外部 ID 的跨目錄聚合、忽略模式發布後的自動同步、明確匯入來源的外掛軌配對。大規模首掃／重掃、GOMAXPROCS=2/4、24 小時混合負載尚待擁有者量測。
