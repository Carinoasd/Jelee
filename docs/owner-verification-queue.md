# 待專案擁有者執行或確認的驗證清單

這份清單集中列出：需要長時間執行、需要真實環境（Windows、真客戶端、真金鑰、真媒體庫），或需要擁有者決定的項目。開發端（Claude 與子代理）已經跑完的單元測試和選定的真 PG 測試，不列在這裡。

使用方式：
- 每一項跑完後，在 PR 回覆項目編號、commit、平台，以及 pass、fail、skip 數或結論即可。
- 不要貼 DSN、XML、媒體路徑或錯誤細節原文。
- 跑長測時，同一台機器不要同時跑其他重負載。2026-10-04 那輪 24h 就是被並行測試擠爆記憶體而失敗的，詳見 PR 留言。

狀態欄：
- **待跑**：還沒有人跑。
- **待確認**：需要擁有者做決定。
- **完成**：附上回報連結。

## A. 必跑回歸

| # | 項目 | 指令／方法 | 狀態 |
|---|---|---|---|
| A1 | Linux 完整回歸（race） | `docs/claude-handoff-schema56.md`「請 MoYuanCN 執行的驗證」第 1 步 | 待跑 |
| A2 | Windows 真 PG 回歸 | 同上，第 2 步 | 待跑 |
| A3 | 24h 圖片長測（用最新 commit 重跑） | `docs/image-soak.md`，`scripts/start_images_soak.py` | 待跑 |

## B. Windows 實機

| # | 項目 | 來源 | 狀態 |
|---|---|---|---|
| B1 | 啟動暫存清掃的存活判斷（程序建立時間）| `internal/platform/scratch/owner_windows.go` | 待跑 |
| B2 | NFO 結算原語：開啟中的檔案能否 rename、目錄 sync 是 no-op | `internal/adapter/nfo/settle_commit_files.go` | 待跑 |
| B3 | 圖片持久存放區：開啟中的檔案無法刪除時，`ClearVariants` 與淘汰的行為。**2026-10-04 Windows CI 已證實**：有變體正被讀取時，舊版 `ClearVariants` 把整個 `variants` 改名到 `tmp/trash-*`，因 Windows 不允許改名含開啟檔案的目錄而失敗。**修法**：版面改為 `variants/<世代>/<來源>/<key>`；清除改成建立下一個世代目錄（持久提交點）並同時切換現役世代、清空索引，再逐檔刪除舊世代，全程不改名目錄。刪不掉的檔案留在舊世代、之後的清除與每次啟動重試；重啟只收錄編號最大的世代，舊變體不會復活。Linux 上以注入的「拒絕改名目錄、釘住檔案」規則模擬並測過。實機請跑 `go test -p 1 -count=1 -run "Store" ./internal/adapter/images/`（含 `TestStoreClearVariantsKeepsOriginalsAndRebuilds`，Windows 上已不再豁免讀取者內容比對），再實際在讀取變體時呼叫清除快取 | `internal/adapter/images/store.go` | 待實機複驗 |
| B4 | 新解碼格式（WebP、GIF、BMP、TIFF）與 EXIF 方向 | `internal/adapter/images/decode_formats.go` | 待跑 |
| B5 | doctor 的 Windows 磁碟降級路徑（無 inode） | `internal/diag/` | 待跑 |
| B6 | 直投在 Windows 走緩衝備援路徑（無 sendfile）：實機跑 `go test ./internal/adapter/media/`，並實際播放、拖動一次 | `docs/direct-delivery.md`「零拷贝直投」 | 待跑 |
| B7 | 日誌檔與暫存目錄在 Windows 沒有 POSIX 權限位元，隱私完全依賴所在目錄繼承的 ACL；請確認預設安裝位置（服務帳號、ProgramData）下其他使用者讀不到 `jelee.log` 與暫存目錄，若讀得到需決定是否明確設定 ACL | `internal/platform/logging/rotate.go`、`internal/platform/scratch/` | 待跑 |
| B8 | Windows 上的 mkvtoolnix／MediaInfo（E4）：`scripts/make.ps1 bootstrap-matroska` 後 `scripts/make.ps1 tools-verify`，確認兩個 zip 只取出清單列出的檔案、`--version` 版本字串相符，並以 `.tools/matroska/mkvtoolnix/102.0/windows-amd64/mkvtoolnix/mkvextract.exe` 手動擷取 `.testfixtures` 的 `subtitles-fonts.mkv`。Windows 正式服務的擷取與補充仍停用（沙箱只在 linux-amd64） | `docs/matroska-tools.md` | 待跑 |
| B9 | 圖片處理管線（G40.6、G40.7）：WebP 輸出（nativewebp 純 Go 無損編碼）、填滿裁切與智慧裁切、平均色／主色調取樣在 Windows 上的結果與 Linux 一致。請跑 `go test -p 1 -count=1 -run "TestImage(PlanOutput|CropTransform|SmartCrop|Colors|WebP|Describe|CacheAndVariantKeys)" ./internal/adapter/images/`，並以 `GOOS=windows` 建置確認無 cgo；實際開一個 `format=webp` 與 `fillWidth`／`fillHeight` 的請求看輸出 | `internal/adapter/images/pipeline.go`、`docs/adr/0010-image-output-formats.md` | 待跑 |
| B11 | 章節圖與快取命中率在 Windows（G40.1、G40.13）：開啟 `JELEE_ENABLE_CHAPTER_IMAGES`（連同探測、圖片與存放區）啟動服務，確認因沙箱只在 linux-amd64 而照常啟動、只記 WARN `chapter_image_prerequisite_unavailable` 或 `chapter_image_runtime_unavailable`、`jelee-cli doctor` 回報 `chapter_images_tool_missing`；再跑 `go test -p 1 -count=1 -run "TestImageVariantCounters|TestChapter" ./internal/adapter/images/ ./internal/adapter/probe/ ./internal/domain/ ./internal/platform/jobs/`（含 Windows 存放區上重啟後由存放區供應變體） | `docs/image-assets.md`「章节图」「缓存命中率」 | 待跑 |
| B13 | 預設版本選擇與會話選軌在 Windows（G20.2、G20.4）：Windows 服務上以兩個版本的條目設定 `PUT /api/v1/users/me/version-preferences`，確認自有播放資訊的 `selection` 與相容層 PlaybackInfo 第一個來源一致；用 Windows 版 Jellyfin 相容客戶端播放時切換字幕，確認 `session_track_selections` 寫入、再次開啟時沿用。程式沒有平台分支，主要確認 Windows 客戶端回報的 `SubtitleStreamIndex` 與外掛字幕編號一致 | `internal/domain/version_selection.go`、`internal/domain/session_tracks.go` | 待跑 |
| B14 | 識別修正與待確認決定在 Windows（G21.5）：`go test -p 1 -count=1 -run "ItemIdentity|CatalogPending|PlanCatalog|IdentityPatch" ./internal/domain/ ./internal/app/ ./internal/adapter/http/`；有 PostgreSQL 時再以真 PG 跑 `-run "TestItemIdentity|TestCatalogPending" ./internal/adapter/postgres/`（含重掃、接受、拒絕與降版）。確認媒體根在 Windows 路徑下，接受的待確認檔案（列表路徑以 `/` 分隔）建立的來源可直投播放 | `docs/catalog-sync.md`「識別修正與待確認決定」 | 待跑 |
| B15 | 任務總數與 ETA 在 Windows（G13.3）：Windows 真 PG 跑 `go test -p 1 -parallel 2 -count=1 -run TestJobProgress ./internal/adapter/postgres/`，並以 `.\bin\jelee-cli.exe jobs get` 讀一個執行中的任務，確認 `progress` 的 total、percent、etaSeconds、etaBasis 正常輸出；測試靠 80 ms 間隔的檢查點取樣，Windows 計時器粒度下若 ETA 斷言不穩請回報 | `docs/jobs-api.md`「总数、百分比与 ETA」 | 待跑 |

## C. 長時間、規模、效能

| # | 項目 | 說明 | 狀態 |
|---|---|---|---|
| C1 | 新格式大圖混合負載的 RSS／GC | 接近預算上限的有損 WebP、多 strip TIFF、惡意無損 WebP | 待跑 |
| C2 | 圖片存放區十萬張冷熱命中率與淘汰穩態 | 需先設 `JELEE_IMAGE_STORE_ROOT`；另量重啟重建索引的時間與記憶體 | 待跑 |
| C3 | 遠端圖片抓取：1 萬個 URL 注入失敗、慢速、429，連續跑數小時 | 觀察 goroutine 數、主機表大小、預算是否歸還 | 待跑 |
| C4 | 50 萬條目掃描（首掃＋重掃）與 GOMAXPROCS=2／4 | 等掃描→條目同步合入後再跑 | 待跑 |
| C5 | 掃描＋探測＋NFO＋ignore＋自動同步的混合負載 24h | 等掃描→條目同步與 NFO worker 合入後再跑 | 待跑 |
| C6 | 日誌高 QPS 下 INFO 與 DEBUG 對 P95 的影響、丟棄計數、輪轉不阻塞 | G46.8 | 待跑 |
| C7 | 權限規則開銷 ≤10%（無規則基線對比） | 客戶端管控已接上 HTTP。開發機單次樣本：真 PG `GET /api/v1/users/me` 無規則對 10,000 條規則比值 0.98；記憶體後端 +0.5～0.8 µs。請在目標硬體以 `TestClientControlOverheadPostgres`（`-v` 看比值）與 `BenchmarkClientGate*` 重跑並在高並發下確認，見 `docs/client-control.md`「效能」 | 待跑 |
| C8 | 稽核表大量資料時 `purge_audit_logs` 與 `ListAudit` 的效能 | 等 L4 合入後再跑 | 待跑 |
| C9 | 開發者模式在真實時鐘下 12 小時到期 | 已接上 CLI 與 HTTP（`claude/devmode`）：依 [開發者模式](developer-mode.md)「擁有者驗證」開一次預設 12 小時工作階段，確認到期後標頭、系統資訊、橫幅與放寬全部消失、稽核恰一筆 `devmode.expired`、期間每 5 分鐘 WARN | 待跑 |
| C10 | fuzz 長跑：`FuzzParsePath`（命名解析）、`FuzzProductionGuard`（轉碼守衛）、相容層認證 | 各跑數小時，或排進夜間 CI | 待跑 |
| C11 | 正式效能基準線：在固定、閒置的硬體上以 `make bench` 重產 `docs/evidence/bench-baseline.txt` | 目前的基準是開發機產生，只供參考 | 待跑 |
| C12 | 掃描→條目同步：50 萬條目首掃與重掃（目錄並發 1／2／4 × GOMAXPROCS 2／4）、accept 模式發布 50 萬列的時間 | `docs/catalog-sync.md` | 待跑 |
| C13 | 遷移 062 在既有大型 `media_sources` 上執行 `ADD CONSTRAINT UNIQUE(id,library_id)` 的時間與鎖表影響（正式資料量） | `000062_media_sidecar_tracks.up.sql` | 待跑 |
| C14 | 會話 `last_seen_at` 每 60 秒節流寫入：數百個並行 native 會話持續請求時的 DB 寫入量與鎖等待 | `docs/accounts-api.md` | 待跑 |
| C15 | 撤銷斷流與限速在 HTTP/2、反向代理（Nginx／Caddy 緩衝）下的表現：撤銷後幾秒斷線、客戶端實測速率與設定值的偏差 | `docs/direct-delivery.md` | 待跑 |
| C16 | 零拷貝直投：多串流並發的 CPU 與吞吐、極小 Range 請求的固定開銷、正式 WriteTimeout 30 秒下低碼率客戶端的容忍度、TLS 代理與 HTTP/2 部署確實回到緩衝路徑 | `docs/direct-delivery.md` | 待跑 |
| C17 | 相容層瀏覽：在 scale 資料庫（大媒體庫）上量 `/compat/Items` 總數計算與名稱排序的延遲，以及 `/compat/UserViews` 推導 CollectionType 的成本 | `internal/adapter/postgres/catalog_browse.go` | 待跑 |
| C18 | 外掛軌配對的額外成本：50 萬條目（含大量外掛檔）首掃與重掃時 sources 階段每批多出的兩條查詢、`library_inventory_sidecar_owner_idx` 讓每次基準發布多寫的索引量與 WAL；遷移 065 在既有大型基準上建索引的時間與鎖表；首次同步後的字元集／指紋檢查頁（每檔頭尾 128 KiB＋字幕至多 1 MiB）在 NAS／網路掛載上的總時間 | `docs/catalog-sync.md` | 待跑 |
| C19 | 播放進度：多日運作的保留期清理、遺留會話清掃、記憶體上限與 flush 延遲；多實例下同一會話跨實例回報；上萬同時播放時單一批次語句的耗時與鎖等待 | `docs/playback-progress.md` | 待跑 |
| C20 | 觀看統計：多日運作下排程彙總與兩種保留期的交互、夏令時間切日、多實例彙總鎖輪替；日表數百萬列時全站一年報表的耗時；從 066 升級時已有數百萬會話的補算交易長度與對播放寫入的影響 | `docs/watch-statistics.md` | 待跑 |
| C21 | Webhook：大量事件下 outbox 清理（每小時、每批 1000）能否跟上與資料表膨脹；持續高流量與多實例下首次投遞延遲、慢端點是否拖住其他端點；播放 flush 與登入多出的 outbox 寫入成本；送出後記錄前強制終止，確認租約到期後以同一 `eventId` 重送 | `docs/webhooks.md` | 待跑 |
| C22 | 統一權限過濾器：受限使用者在 10 萬條目以上的庫，瀏覽總數、庫清單內容種類探查（某種類全被隱藏時）的耗時，以及 G48 要求的 ≤10% 開銷與 P95 | `docs/access-control.md` | 待跑 |
| C23 | 客戶端管控：目標硬體與高並發下重跑 C7 的 P95；多實例長時間的規則版本失效與命中寫入延遲；命中表 30 天保留的大小與清除速度、已知客戶端接近 10 萬筆上限 | `docs/client-control.md` | 待跑 |
| C24 | 資料一致性檢查：在目標硬體的真實大型媒體庫（含 NAS／網路掛載）重跑 `JELEE_CONSISTENCY_SCALE=1 go test -p 1 -run TestConsistencyScale100000Postgres ./internal/adapter/postgres/`，並以 `jelee-cli consistency check` 量測全庫耗時、stat 預算是否足夠；開 `JELEE_JOB_CONSISTENCY_INTERVAL_HOURS=168` 運作數週，確認排程不與掃描搶佔、報告保留與 `jelee_consistency_*` 指標正常 | `docs/consistency.md` | 待跑 |
| C25 | 圖片刷新任務（`image_refresh`）規模與長時間：十萬個遠端圖片列的媒體庫跑完一輪的時間、每列檢查點交易的資料庫負載、混合成功／404／429／慢速來源時的總耗時與主機冷卻；與觀看請求同時跑時處理槽等待對 `/images` P95 的影響；`item_image_fetch_failures` 大量資料時候選查詢（`item_images_refresh_idx`）的計畫 | `docs/image-refresh-jobs.md` | 待跑 |
| C26 | WebP 輸出與裁切的負載：`JELEE_IMAGE_MAX_CONCURRENT` 滿載下連續要求 1024×1024 的 `format=webp`（無損，編碼約 73 B／像素工作記憶體，估算按 80 B／像素＋4 MiB 預留），量 RSS／GC、編碼耗時（nativewebp 中途不可取消，只受輸出像素上限約束）、413 比例（無損輸出超過 `JELEE_IMAGE_MAX_OUTPUT_BYTES`）、與 JPEG 的檔案大小比；另量智慧裁切的額外 CPU。合成雜訊圖 1024² 的 WebP 約 2 MB（JPEG q85 約 0.12 MB），真實海報開發機抽樣約 1.8–6.6 倍 | `internal/adapter/images/pipeline.go`、[ADR 0010](adr/0010-image-output-formats.md) | 待跑 |
| C29 | 縮放快取命中率在真實負載（G40.13）：長時間真實瀏覽下記錄 `jelee_images_cache_hits_total`、`_misses_total`、`jelee_images_store_served_total`、`jelee_images_variants_generated_total`，依文件公式算記憶體與整體命中率；重啟後一段時間內由存放區供應的比例，以及記憶體快取大小（`JELEE_IMAGE_CACHE_BYTES`／`_ENTRIES`）調整對命中率的影響；可與 A3、C2 同輪記錄 | `docs/metrics.md`「命中率怎麼算」 | 待跑 |
| C28 | 章節圖擷取規模與尖峰資源（G40.1）：在含數千個有章節圖軌的 MP4／M4V／M4B 的媒體庫開 `JELEE_ENABLE_CHAPTER_IMAGES` 跑 catalog sync，量測本段耗時（每次 sync 上限 256 個檔案、每檔 60 秒）、單檔 64 張／16 MiB 輸出時的程序 RSS 尖峰（文件估約 40 MiB 以內）、與探測及內嵌封面搶 IO 許可時對 `/images` P95 的影響；確認指紋不變的第二輪不再啟動子行程 | `docs/image-assets.md`「章节图」 | 待跑 |
| C31 | 會話選軌的長時間與規模（G20.4）：多日運作下 `session_track_selections` 的大小是否穩定在「近 30 天有播放的（使用者，條目）」以內（寫入時順帶刪除全站最多 100 列過期資料，長期無人播放的實例過期列會留到下一次寫入）；多人同時播放、頻繁切軌時每次切換一個寫入語句的負載，以及多實例下同一會話跨實例回報造成的重複寫入次數；播放資訊每次多一個讀取語句的 P95 影響 | `docs/item-versions.md`「會話層級選軌」 | 待跑 |
| C32 | 識別鎖與待確認決定的規模（G21.5）：十萬集以上、數千個鎖定條目的媒體庫，解析器版本變更後全庫重新解析（每個已追蹤檔案多一次季集號比對與條件更新）的耗時與 `protected` 計數；長時間同步進行中送出 `PUT /api/v1/items/{id}/identity` 與待確認接受／拒絕（它們會等目前批次的媒體庫列鎖）時的延遲；上萬筆已拒絕待確認列時列表分頁與 pending 清理批次的耗時 | 只做過小型真 PG 短測 | 待跑 |
| C33 | 任務 ETA 準確度與觸發器開銷（G13.3）：十萬條目以上的 `nfo_apply`、`nfo_export`、`image_refresh` 與大型掃描，每分鐘記錄 `etaSeconds` 與實際剩餘時間的偏差、`etaBasis` 轉換（`insufficient_samples`→`throughput`、暫停後 `paused`、慢來源下的 `stalled`）；比較有無觸發器 `jobs_progress_rate` 時檢查點交易的 P95（觸發器每次 `jobs` 更新都執行），以及 `nfo_apply`／`nfo_export`／`image_refresh` 提交時 `count(*)` 在大型媒體庫的耗時是否逼近 2 秒語句期限 | `docs/jobs-api.md` | 待跑 |

## D. 真實資料／真金鑰／真客戶端

| # | 項目 | 說明 | 狀態 |
|---|---|---|---|
| D1 | TMDB find（IMDb／TVDB／Wikidata）回應形狀與命中率 | 需要真 TMDB 金鑰 | 待跑 |
| D2 | 字幕編碼偵測在真實字幕庫上的正確率 | 尤其是存成 GBK 的繁體字幕、CP949、單行短字幕；結果寫進 G15 驗收紀錄 | 待跑 |
| D3 | 命名解析與版本標籤在真實媒體庫檔名上的誤判率 | 統計 Low 與 unknown 的比例 | 待跑 |
| D4 | 第三方客戶端握手、起播、Seek、進度上報 | 等第 10／11 階段相容層完成後再跑 | 待跑 |
| D5 | 網頁登入：Chrome、Firefox、Safari 在 `http://localhost` 是否接受 `__Host-` Cookie；TLS 反向代理後的端到端登入、CSRF 流程 | `docs/security-model.md` | 待跑 |
| D6 | 正式容器中的完整 `jelee-cli doctor` 與新的 HEALTHCHECK；掛真實或網路媒體根時的逾時表現 | `internal/diag/`、`Dockerfile` | 待跑 |
| D7 | 前端：在你的環境執行 `make bootstrap` 取得 Node，再跑 `make web-install web-lint web-test web-build` | `docs/frontend-adr.md` | 待跑 |
| D8 | 原生登入：管理員以 `PUT /users/{id}/native` 開啟後，用真實非瀏覽器客戶端（或 curl）走 `POST /api/v1/auth/login/native`、直投、輪換、撤回權限後 native 會話立即失效 | `docs/accounts-api.md`「原生设备登录」 | 待跑 |
| D9 | 並發播放上限：真實播放器拖動、多段 Range、預載時是否被誤判為超限（429 `user_stream_limit`） | `docs/direct-delivery.md` | 待跑 |
| D10 | 相容層登入：Findroid、Swiftfin、Infuse、官方 Web／Android 客戶端能否用 `/compat` 登入並解析精簡版 UserDto／SessionInfo；`/Users/Public` 回空陣列時是否改成手動輸入帳號；未開 allowNative 時 403 的顯示是否可理解 | `docs/compat-matrix.md` | 待跑 |
| D11 | 外掛字幕／音軌直投：原生播放器載入 srt／ass（含 Shift_JIS、GB18030、Big5 等非 UTF-8）、PGS `.sup`、VobSub `.idx`＋`.sub`、外掛 mka／eac3／truehd／dts 音軌的同步與拖動 | `docs/direct-delivery.md` | 待跑 |
| D12 | 相容層瀏覽：混合媒體庫省略 CollectionType 時是否被隱藏、500 筆上限下是否依 TotalRecordCount 翻頁、ImageTags 為空、UserData（已改用真實播放進度）、被忽略的篩選參數（Filters、Genres）回出較多結果時客戶端是否正常 | `docs/compat-matrix.md` | 待跑 |
| D13 | 相容層播放：Findroid、Swiftfin、Infuse、官方 Android 能否經 `/compat` 起播與拖動；各客戶端的 DeviceProfile 是否被判為可直投（目前不評估 CodecProfiles，可能判可直投但客戶端解不了）；`NoCompatibleStream` 與位元率不足時的呈現；外掛字幕 `DeliveryUrl` 不含 token 時客戶端是否會帶驗證；帶 `AudioCodec` 或非 static 網址被 409 時能否起播。官方 Web 在瀏覽器帶 Origin，預期 403 無法使用 | `docs/compat-matrix.md` | 待跑 |
| D14 | 前端第一批頁面（登入、媒體庫、條目、詳情、個人頁）在 Chrome／Firefox／Safari：`__Host-` Cookie 與重新整理後維持登入、海報顯示、純鍵盤操作、螢幕閱讀器（NVDA／VoiceOver）、亮暗主題與對比（axe）、減少動態、手機／平板／桌面版面、CSP 無違規 | `docs/frontend-adr.md` | 待跑 |
| D15 | 播放進度：各客戶端是否帶 `PlaySessionId`／`ItemId`、實際回報頻率；停止後續播點與「已播放」是否立即更新；「繼續觀看」與進度條（需來源已探測時長）；斷線重連接回同一會話、Seek 後進度 | `docs/playback-progress.md` | 待跑 |
| D16 | 相容層圖片：客戶端取圖是否帶驗證標頭或 `api_key`（不帶會 401、海報空白；Jelee 刻意不允許匿名取圖）、64 位 hex tag、要求 WebP（`Format=Webp` 現在回 WebP；其他上游格式名回預設格式）、Logo／Thumb 回退、背景圖索引、缺 `PrimaryImageAspectRatio` 的版面。注意：只有媒體檔旁海報的條目要等「圖片入庫」（擁有者任務二）完成後列表才會有 Primary tag | `docs/compat-matrix.md` | 待跑 |
| D17 | Webhook：在真實 HTTPS 接收端（不同憑證鏈、自簽 CA 用 `JELEE_WEBHOOK_CA_FILE`）驗證簽章、時間窗與 `eventId` 去重；需設定 `JELEE_WEBHOOK_MASTER_KEY` | `docs/webhooks.md` | 待跑 |
| D18 | 受限帳號（條目隱藏、分級上限、標籤封鎖）經相容層的實際體驗：被隱藏的父項底下有允許的子項時的瀏覽、分級上限下的海報與繼續觀看 | `docs/access-control.md` | 待跑 |
| D19 | 客戶端管控：真實第三方客戶端遇到相容層無內容 403／429 時是否卡住或不斷重試；唯讀動作下播放進度回報被拒的影響；反向代理部署下可信代理與環回豁免的組合 | `docs/client-control.md` | 待跑 |
| D20 | 初始引導：真實瀏覽器走完前端向導；Compose 容器內 `jelee-cli setup --non-interactive` 一鍵初始化；反向代理後完成引導（一次性引導權杖從日誌或 `JELEE_SETUP_TOKEN_FILE` 取得） | `docs/setup-wizard.md` | 待跑 |
| D21 | 預設告警規則：以實際 Prometheus（及其 `promtool check rules deploy/prometheus/jelee-alerts.yml`）載入 `deploy/prometheus/` 範例，抓取真實服務；分別製造資料庫停止、磁碟接近滿、Webhook 接收端離線、開發者模式開啟、一致性檢查發現，確認對應告警在預期時間觸發與解除，並把 Alertmanager 路由與 runbook 連結接上 | `docs/runbook.md` | 待跑 |
| D22 | 舊庫遷移：以真實 Jellyfin 10.11 `jellyfin.db`（停機後複製，含 `-wal`）跑 `jelee-cli legacy-import --preflight`、匯入、掃描、再匯入。確認 GUID 文字大小寫、`DateTime` 格式、CollectionFolder `Data` JSON 的 `PhysicalLocationsList`／`CollectionType`；10.10 升級上來的庫的 `UserData`（`CustomDataKey` 多列、`RetentionDate`、佔位條目）；Windows／NAS 路徑對照；多版本、分段檔、附加影片的分類與對應率；10 萬條目以上、多使用者的耗時與記憶體；改寫前 C# Jelee 的實際資料庫 | `docs/legacy-import.md`「需要以真實資料庫驗證的項目」 | 待跑 |
| D23 | API 控制台（G49.4）：在可開發實例以真實瀏覽器開啟開發者模式，管理員進入「管理 → API 控制台」，對一個讀取與一個寫入操作各送一次，確認狀態碼、耗時、traceId 與日誌中的 `requestId` 一致、`Set-Cookie` 被遮罩、複製的 cURL 只有佔位符；換成非管理員、關閉開發者模式、生產組態各試一次，確認導覽沒有入口且直接輸入網址得到 404 頁面 | `docs/developer-mode.md`「API 控制台」 | 待跑 |
| D24 | Matroska 擷取與 MediaInfo 補充（E4，G15.5、G15.7、G19.1）：在正式映像（`make bootstrap-matroska` 後建置）以真實 MKV 庫開啟 `JELEE_ENABLE_MATROSKA_EXTRACTION`，用真客戶端（Jellyfin 相容 App、mpv 等）播放含 ASS 內嵌字幕與字型附件的影片，確認字幕與字型正確顯示、原檔雜湊不變、快取上限與淘汰合理、大檔（數十 GB）首次擷取時間可接受；同時確認探測後章節名稱、軌名與附件出現在播放資訊，且 Web 端拿不到任何字幕或字型位元組 | `docs/matroska-tools.md`；容器實測 `docs/evidence/matroska-runtime-image.txt` | 待跑 |
| D25 | 字幕 OCR 真實點陣字幕準確率（G15.6）：在 OCR 層映像（`make bootstrap-ocr`、`deploy/ocr/Dockerfile`）開啟 `JELEE_ENABLE_SUBTITLE_OCR`，以真實藍光 PGS（1080p／4K，含粗描邊、斜體、雙行、強制字幕）與 DVD VobSub（720×480、各種調色盤）各至少一部英文與一部中文（繁、簡）、一部日文片，人工比對抽樣 100 句以上的字元正確率與時間軸，記錄每部片的辨識耗時、Tesseract 峰值記憶體與 CPU、快取大小；確認原檔 SHA 不變、原點陣軌仍可直投、Web 端拿不到 SRT、真客戶端（Jellyfin 相容 App、mpv）能選到「(OCR)」軌並正常顯示；與 [字幕 OCR](subtitle-ocr.md) 的合成樣本數據對照 | `docs/subtitle-ocr.md`；合成樣本 `TestRealOCRAccuracyAndCost`；容器實測 `docs/evidence/ocr-runtime-image.txt` | 待跑 |
| D26 | 圖片刷新對真實外部主機：開啟 `JELEE_ENABLE_IMAGE_FETCH`，以含 TMDB 圖片網址與其他 HTTPS 網址的真實 NFO 媒體庫跑一次刷新，確認圖片可顯示、`image.tmdb.org` 不跟隨重新導向、私有位址被拒（`target_denied`）、失敗原因合理；Windows 上同樣跑一次（存放區寫入與暫存清理） | `docs/image-refresh-jobs.md` | 待跑 |
| D27 | 圖片格式與裁切的真實客戶端（G40.6、G40.7）：Jellyfin 相容 App 與瀏覽器要求 `Format=Webp`、`FillWidth`＋`FillHeight` 時顯示是否正常（Jelee 的填滿是裁切語意，與上游只縮放不同，確認海報牆、背景與縮圖構圖可接受）；智慧裁切在真實海報、背景、劇照上的取景是否合理（必要時改 `JELEE_IMAGE_CROP=center` 或 `off`）；上傳與內嵌封面的平均色／主色調在真實作品上是否合理 | `docs/image-assets.md`、`docs/compat-matrix.md` | 待跑 |
| D29 | 章節圖真實工具與真實檔案（G40.1）：在正式受保護映像（固定 ffprobe n9.0.2）開啟 `JELEE_ENABLE_CHAPTER_IMAGES`，以真實含章節圖的 MP4／M4B（iTunes、Podcast、有聲書工具產生）與不含章節圖的 MP4、MKV 各數個跑 catalog sync，確認章節圖逐位元組等於檔內樣本、序號與章節對應、MKV 與無章節圖檔記為 `absent` 或不成為候選；並以 Jellyfin 相容客戶端開啟 `/Items/{id}/Images/Chapter/{index}` 顯示。開發端的解析樣本（`internal/adapter/probe/testdata/chapters-*.json`）是用開發機 ffprobe（非固定版本）對產生的檔案擷取的，固定版本輸出格式需在此確認；另請沿用開發者固定檔案跑一次 `JELEE_SANDBOX_REAL_PROFILE` 真沙箱讀取 | `docs/image-assets.md`「章节图」 | 待跑 |
| D31 | 預設版本與會話選軌的真實客戶端（G20.2、G20.4）：以 Jellyfin Web、Android TV、Infuse／Swiftfin 等真實客戶端播放含 4K HDR 與 1080p SDR 兩個版本的條目：(1) 設定 `maxHeight=1080` 或 `dynamicRange=sdr` 後客戶端預設播放的是否為 `MediaSources` 第一個（部分客戶端記住上次選的版本或自行排序）；(2) 送出的 DeviceProfile 排除 HEVC 時是否只看到可直投版本，且不會改要求轉碼；(3) 播放中切換音軌／字幕（含外掛字幕與關閉字幕）後，停止再續播、換裝置續播時是否從 `DefaultAudioStreamIndex`／`DefaultSubtitleStreamIndex` 開始；(4) 哪些客戶端在 Progress 回報裡不帶 `AudioStreamIndex`／`SubtitleStreamIndex`（屆時只能靠偏好） | `docs/item-versions.md`、`docs/compat-matrix.md` | 待跑 |
| D32 | 識別修正的真實資料（G21.5）：以真實動畫庫（絕對集數、OVA、跨季合訂）與誤放資料夾的劇集，依待確認列表逐筆接受（補季號／集號、改劇集名）與拒絕，修正幾個誤判的類型與所屬並鎖定；之後跑真實掃描（含更新檔案、新增版本）、元資料備份匯出再匯入到另一個實例，確認鎖定值、拒絕狀態（不匯出，匯入後應重新出現為 pending）與條目結構都符合預期 | 需要真實媒體庫；本輪無網頁介面，以 API 操作 | 待跑 |
| D33 | 任務 ETA 的真實使用感受（G13.3）：在真實媒體庫觸發掃描、NFO 全量導入與圖片刷新，從 API／CLI 觀察進度：掃描期間總數保持 null 是否可接受、提交後新增條目造成的 `total_reached` 是否需要改成執行中重算總數、ETA 的抖動程度（EMA 新樣本權重 0.3）是否需要調整 | `docs/jobs-api.md` | 待確認 |

## E. 需要擁有者決定

> 2026-10-05：E1–E14 由本專案（Carinoasd）依需求原文決定，決定內容寫在各列狀態欄；標「待實作」的會接著實作。若擁有者有不同意見，直接在 PR 留言即可調整。

| # | 項目 | 目前做法 | 狀態 |
|---|---|---|---|
| E1 | 外部工具用 `os.StartProcess`／Windows Job，而不是 `os/exec` | `docs/adr/0001-external-process-start.md` | 已決定（2026-10-05）：採用：維持 `os.StartProcess`／Windows Job（ADR 0001），理由是需要行程群組、資源限制與沙箱，`os/exec` 做不到；同樣只用參數陣列、不經 shell |
| E2 | 兩種上游舊品牌忽略檔的語義（G22.2 列出的兩個檔名） | 上游原始碼找不到入口，暫記為阻塞 | 已決定（2026-10-05）：採用：兩種舊忽略檔作為 `.jeleeignore` 的別名，語義與 `.jeleeignore` 相同（gitignore 語法），同目錄同時存在時以 `.jeleeignore` 為準；已實作（2026-10-05，``）：入口 `internal/adapter/media/ignore/rule_names.go` 的 `OpenRule`，兩個別名依原文順序排在 `.jeleeignore` 之後，兩種任務模式皆生效，見 [忽略規則來源](ignore-source.md) |
| E3 | TMDB 資料使用條款：保存期限、24 小時快取是否合規、署名位置 | `docs/tmdb-external-metadata-removal.md` | 已決定（2026-10-05）：接受：TMDB 資料 24 小時快取、只隨條目保存、條目刪除即清除；署名放在「關於」頁與 API 文件，並遵守其非商業條款 |
| E4 | 外部工具 MediaInfo、mkvtoolnix 的下載與授權核准（G19.1、G51） | `tools/manifest.json` 的 `matroskaTools`；`docs/matroska-tools.md` | 已決定（2026-10-05）：核准：MediaInfo 與 mkvtoolnix 依 G30.6 納入 `tools/manifest.json`（固定版本、官方來源、SHA256、授權記錄），預設不強制安裝；**已實作（2026-10-04）**：mkvtoolnix 102.0、MediaInfo 26.05（Linux／Windows amd64）固定與可選引導、`tools-verify` 校驗；Linux 沙箱執行、映像、MediaInfo 探測補充、內嵌文字字幕與字型附件擷取（自有 API＋相容層）；mkvpropedit 只固定不使用。實機項目見 B8、D24 |
| E5 | 刪除 C# 樹後，只靠 Git 歷史與 tag `upstream-csharp-final` 提供舊原始碼，是否滿足 GPL 義務（含倉庫轉私有、遷移、被 fork 的情況） | `docs/LICENSE-COMPLIANCE.md` | 已決定（2026-10-05）：接受：發佈的二進位與映像一律附原始碼位置（含 tag `upstream-csharp-final` 與對應提交），符合 GPL v2 第 3 條；寫進發佈流程 |
| E6 | Go 程式中是否有逐段移植自上游 C# 的部分，需要帶上原檔版權頭 | 同上 | 已決定（2026-10-05）：決定：不逐行移植，Go 程式不加上游版權頭；若日後有逐段移植，該檔需加註來源與原版權 |
| E7 | 根目錄 `LICENSE` 是 GPL v2，上游套件元資料寫 GPL-3.0-only，Jelee 對外宣告哪一版 | 同上 | 已決定（2026-10-05）：決定：Jelee 對外宣告 GPL-2.0-only，與根目錄 `LICENSE` 一致 |
| E8 | 已不再分發的 ListenBrainz 圖示，其 NOTICE 是否繼續保留（目前保守保留） | `docs/legal/upstream/` | 已決定（2026-10-05）：決定：繼續保留 NOTICE（成本低、風險為零） |
| E9 | 預設開啟「每位使用者同時最多 4 個不同播放」會改變既有部署行為；是否改成預設不限（`DefaultStreamingConfig.EnableStreamLimit`） | `internal/platform/config/streaming.go` | 已決定（2026-10-05）：決定：維持預設「每位使用者同時最多 4 個不同播放」，升級說明中註明可關閉 |
| E10 | 並發計數只存在單一行程記憶體，多實例部署時同一使用者分散到多台可超過上限；撤銷檢查則跨實例。是否需要跨實例計數 | `docs/direct-delivery.md` | 已決定（2026-10-05）：決定：目前接受單一行程計數並在文件註明；多實例部署建議把同一使用者導向同一實例，跨實例計數列為後續 |
| E11 | 相容層刻意比上游嚴格：`GET /Users/{id}` 只允許本人或管理員、`/Users/Public` 不列出帳號；若某客戶端依賴舊行為是否接受 | `docs/compat-matrix.md`；[ADR 0004](adr/0004-compat-stricter-than-upstream.md) | 已決定（2026-10-05）：接受：相容層維持較嚴格的行為（隱私優先） |
| E12 | 權限規則預設：管理員預設不受條目規則、分級、標籤限制（`restrict_admins` 可開）；未分級條目的全域預設策略；內建分級代碼表（美、英、日、德、台）是否足夠 | `docs/access-control.md`；[ADR 0003](adr/0003-unified-access-filter-in-sql.md) | 已決定（2026-10-05）：接受：管理員預設不受條目規則／分級／標籤限制、未分級預設允許、內建分級表維持現狀 |
| E13 | 舊庫遷移工具（G04.6：Jellyfin SQLite → PostgreSQL）需要讀 SQLite，go.mod 目前沒有 SQLite 驅動。是否核准新增純 Go 的 `modernc.org/sqlite`（BSD 授權）？未核准前此項維持阻塞 | `docs/requirements-traceability.md` G04.6；[ADR 0005](adr/0005-postgresql-only-state.md) | 已決定（2026-10-05）：核准：新增純 Go 的 `modernc.org/sqlite`（BSD 授權）供舊庫遷移工具使用，授權記錄進 LICENSE-COMPLIANCE；已實作：`modernc.org/sqlite v1.60.1` 只連結進 `jelee-cli legacy-import`（[舊庫遷移](legacy-import.md)），真實資料驗證見 D22 |
| E14 | 初始引導選用「一次性引導權杖」而非來源位址白名單（理由：Docker 連接埠映射與反向代理後合法引導者本來就不是 loopback）；多實例部署請在引導期間只開一個實例或改用 CLI。是否接受 | `docs/setup-wizard.md`；[ADR 0008](adr/0008-setup-bootstrap-token.md) | 已決定（2026-10-05）：接受：一次性引導權杖 |
| E15 | G04.7 快取邊界：不引入 Redis；接受三處跨實例只靠時間的延遲（CSP 字型白名單 30 秒、開發者模式 2／10 秒、初始設定閘門 1 秒，只延後生效、不讀錯資料）；登入限速、客戶端限流、串流並發與頻寬維持各實例計數，多實例時以實例數倍放寬 | `docs/adr/0002-no-redis-cache-boundary.md`、`docs/cache-boundaries.md` | 已決定（2026-10-05）：三點全部接受；若日後實例數增加或需要全域精確限流，依 ADR 0002 改用 LISTEN/NOTIFY 或引入 Redis |
| E16 | 外部工具 Tesseract OCR 的下載與授權核准（G15.6、G30.6、G51） | `tools/manifest.json` 的 `ocrTools`；`docs/subtitle-ocr.md` | 已決定（2026-10-05）：核准：Tesseract（Apache-2.0）作為選用外部工具，依 G30.6 納入清單（固定版本、可信 HTTPS 來源、SHA256、授權記錄），語言資料同樣固定版本與雜湊；預設不安裝、不進正式映像的預設階段。上游不發 Linux 可攜二進位，改固定 Debian 13 套件（比照 mkvtoolnix 補函式庫的做法）；**已實作（2026-10-05）**：Tesseract 5.5.0、eng／chi_tra／chi_sim／jpn（tessdata_fast）固定與可選引導、`tools-verify` 校驗；沙箱 `tesseract-ocr` 模式、可選 OCR 映像層、PGS／VobSub 解碼、背景限流與並發、衍生 SRT 軌（自有 API＋相容層）、doctor 與指標。實機項目見 D25 |

## F. 一次性維運

| # | 項目 | 說明 | 狀態 |
|---|---|---|---|
| F1 | 舊版殘留的 `jelee-service-probe-<數字>` 暫存目錄 | 不會自動清除，停機時請手動刪除 | 待跑 |
