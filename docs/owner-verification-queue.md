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

## C. 長時間、規模、效能

| # | 項目 | 說明 | 狀態 |
|---|---|---|---|
| C1 | 新格式大圖混合負載的 RSS／GC | 接近預算上限的有損 WebP、多 strip TIFF、惡意無損 WebP | 待跑 |
| C2 | 圖片存放區十萬張冷熱命中率與淘汰穩態 | 需先設 `JELEE_IMAGE_STORE_ROOT`；另量重啟重建索引的時間與記憶體 | 待跑 |
| C3 | 遠端圖片抓取：1 萬個 URL 注入失敗、慢速、429，連續跑數小時 | 觀察 goroutine 數、主機表大小、預算是否歸還 | 待跑 |
| C4 | 50 萬條目掃描（首掃＋重掃）與 GOMAXPROCS=2／4 | 等掃描→條目同步合入後再跑 | 待跑 |
| C5 | 掃描＋探測＋NFO＋ignore＋自動同步的混合負載 24h | 等掃描→條目同步與 NFO worker 合入後再跑 | 待跑 |
| C6 | 日誌高 QPS 下 INFO 與 DEBUG 對 P95 的影響、丟棄計數、輪轉不阻塞 | G46.8 | 待跑 |
| C7 | 權限規則開銷 ≤10%（無規則基線對比） | 等 C3 客戶端管控接上 HTTP 後再跑 | 待跑 |
| C8 | 稽核表大量資料時 `purge_audit_logs` 與 `ListAudit` 的效能 | 等 L4 合入後再跑 | 待跑 |
| C9 | 開發者模式在真實時鐘下 12 小時到期 | 等 D2 接上 CLI 與 HTTP 後再跑 | 待跑 |
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
| D16 | 相容層圖片：客戶端取圖是否帶驗證標頭或 `api_key`（不帶會 401、海報空白；Jelee 刻意不允許匿名取圖）、64 位 hex tag、要求 WebP 拿到 JPEG、Logo／Thumb 回退、背景圖索引、缺 `PrimaryImageAspectRatio` 的版面。注意：只有媒體檔旁海報的條目要等「圖片入庫」（擁有者任務二）完成後列表才會有 Primary tag | `docs/compat-matrix.md` | 待跑 |

## E. 需要擁有者決定

| # | 項目 | 目前做法 | 狀態 |
|---|---|---|---|
| E1 | 外部工具用 `os.StartProcess`／Windows Job，而不是 `os/exec` | `docs/adr/0001-external-process-start.md` | 待確認 |
| E2 | 兩種上游舊品牌忽略檔的語義（G22.2 列出的兩個檔名） | 上游原始碼找不到入口，暫記為阻塞 | 待確認 |
| E3 | TMDB 資料使用條款：保存期限、24 小時快取是否合規、署名位置 | `docs/tmdb-external-metadata-removal.md` | 待確認 |
| E4 | 外部工具 MediaInfo、mkvtoolnix 的下載與授權核准（G19.1、G51） | 尚未引入 | 待確認 |
| E5 | 刪除 C# 樹後，只靠 Git 歷史與 tag `upstream-csharp-final` 提供舊原始碼，是否滿足 GPL 義務（含倉庫轉私有、遷移、被 fork 的情況） | `docs/LICENSE-COMPLIANCE.md` | 待確認 |
| E6 | Go 程式中是否有逐段移植自上游 C# 的部分，需要帶上原檔版權頭 | 同上 | 待確認 |
| E7 | 根目錄 `LICENSE` 是 GPL v2，上游套件元資料寫 GPL-3.0-only，Jelee 對外宣告哪一版 | 同上 | 待確認 |
| E8 | 已不再分發的 ListenBrainz 圖示，其 NOTICE 是否繼續保留（目前保守保留） | `docs/legal/upstream/` | 待確認 |
| E9 | 預設開啟「每位使用者同時最多 4 個不同播放」會改變既有部署行為；是否改成預設不限（`DefaultStreamingConfig.EnableStreamLimit`） | `internal/platform/config/streaming.go` | 待確認 |
| E10 | 並發計數只存在單一行程記憶體，多實例部署時同一使用者分散到多台可超過上限；撤銷檢查則跨實例。是否需要跨實例計數 | `docs/direct-delivery.md` | 待確認 |
| E11 | 相容層刻意比上游嚴格：`GET /Users/{id}` 只允許本人或管理員、`/Users/Public` 不列出帳號；若某客戶端依賴舊行為是否接受 | `docs/compat-matrix.md` | 待確認 |

## F. 一次性維運

| # | 項目 | 說明 | 狀態 |
|---|---|---|---|
| F1 | 舊版殘留的 `jelee-service-probe-<數字>` 暫存目錄 | 不會自動清除，停機時請手動刪除 | 待跑 |
