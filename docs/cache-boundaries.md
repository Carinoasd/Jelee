# 快取邊界（G04.7）

本文盤點 Jelee 所有的快取與跨行程協調點，逐項寫明保證與限制，最後給出多實例部署的建議。決策背景見 [ADR 0002：不引入 Redis](adr/0002-no-redis-cache-boundary.md)。

總則：

- **沒有外部快取。** 快取都在行程內記憶體，或是本機磁碟上可以刪除重建的衍生檔，或是 PostgreSQL 裡帶戳記的快取表，而且都有上限。
- **失效方式。** 有三種：內容定址（鍵本身就是版本）、資料庫版本欄位、本實例的寫入事件。只靠 TTL 的只有兩類：一是外部來源（TMDB），二是跨實例時有上限的延遲（下表標「跨實例 TTL」）。
- **降級。** 來源讀不到時沿用上次的值，或退回安全預設值，並記錄錯誤碼；安全閘門則刻意失敗即關閉，見[刻意不降級的地方](#刻意不降級的地方)。
- **共用快取的內容。** 只能放對所有呼叫者都相同的資料；凡是依帳號、工作階段或權限判斷得出的結果，一律不進共用快取（見 `internal/platform/cache` 套件說明）。媒體庫存取權限每次都在 SQL 裡和查詢一起判斷，不快取。

## 行程內快取

| 項目 | 存放位置 | 鍵 | 上限 | 失效方式 | 多實例下 | 來源不可用時 | 測試 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 客戶端管控規則（G47） | `internal/adapter/http/client_control.go`：`atomic.Pointer[clientGateState]`，編譯後的不可變 `access.Snapshot` | `client_control_policy.version` | 一份（目前版本） | **版本號**：每個寫入者（規則增刪改、啟用、政策、信任客戶端、重設、匯入）都在同一交易內遞增版本；版本隨每個已驗證請求的工作階段查詢一起取回，登入另以一個小查詢讀取 | 提交後，每個實例在下一個請求時就重新編譯，不需要額外查詢 | 重新載入失敗時沿用上一版，退避 1 秒再試，記錄 `client_control_reload_failed`；登入時版本查詢失敗，就用已編譯的版本 | `TestClientGateReloadsOnlyForNewerVersions`、`TestClientGateKeepsLastVersionWhenReloadFails`、`TestClientGateLoginUsesCompiledRulesWhenVersionUnavailable` |
| 前端 CSP 外部字型白名單（G33.4） | `internal/adapter/http/site_settings.go`：`frontendPolicy` | 單一值 | 一個標頭字串 | **事件**：本實例的外觀寫入、重設、匯入成功後立即失效（`generation` 防止載入期間的競態）；**跨實例 TTL** 30 秒 | 其他實例最多延遲 30 秒 | 沿用上次成功的標頭；從未讀到時用不含外部字型的基本 CSP；失敗後 5 秒再試，單次讀取上限 2 秒 | `TestFrontendPolicyCachesAndSurvivesStorageErrors`、`TestFrontendPolicyInvalidatedDuringLoadReloads`、`TestFrontendPolicyConcurrentUse` |
| 開發者模式工作階段（G45） | `internal/platform/devmode/controller.go`：`atomic.Pointer[Record]` | 單列 `dev_mode_state`（含 `version`） | 一筆 | **事件**：本實例的每次寫入都直接更新本地紀錄；**跨實例輪詢**：可啟用開發者模式的實例每 2 秒、其他實例每 10 秒；到期依本機時鐘立即生效，不需要等資料庫 | 其他實例在一個輪詢間隔內跟上；不符門檻的實例永遠不視為啟用 | 沿用上次的紀錄並記錄 `devmode_refresh_failed`（每分鐘最多一次）；到期仍依本機時鐘生效 | `TestControllerRefreshFailureKeepsLastRecordUntilDeadline`、`TestControllerExpiryRestoresProduction` |
| 初始設定是否完成（G18） | `internal/adapter/http/setup.go`：`setupGate` | 單一布林值 | 一個 | 「已完成」是最終狀態，永久快取；「未完成」**跨實例 TTL** 1 秒 | 其他實例最多延遲 1 秒放行；精靈的每個步驟都以資料庫版本做 CAS，不會因為快取而重複完成 | 錯誤不快取，**失敗即關閉**（見下節） | `TestSetupGateRechecksAndFailsClosed` |
| 圖片輸出記憶體快取（G40） | `internal/adapter/images/cache.go` | `sha256(來源鍵, 寬, 高, 品質)`；來源鍵由原圖內容摘要導出 | 預設 128 筆、32 MiB、TTL 300 秒（`images.cacheEntries`／`cacheBytes`／`cacheTTLSeconds`） | **內容定址**：原圖內容一變，鍵就不同；TTL 只用來回收記憶體 | 各實例各自一份，內容一定相同 | 不依賴資料庫；原圖讀不到時回 `ErrImageUnavailable`，不會送出過期的圖 | `internal/adapter/images` 的快取與處理器測試 |
| OpenAPI 文件 | `internal/adapter/http/openapi_cache.go`，使用 `internal/platform/cache` | 單一鍵 | 1 筆、4 MiB | 只由二進位檔與設定決定，重新啟動才會改變 | 相同 | 不依賴資料庫；編碼失敗時直接即時產生 | `internal/platform/cache` 測試 |
| API 參考頁（`/api-docs`，G49.3） | `internal/adapter/http/apidocs.go`：每個伺服器一個 `sync.OnceValues` | 單一值 | 1 份（約 1.1 MiB HTML） | 只由二進位檔、設定與棄用表決定，重新啟動才會改變；不依呼叫者而變 | 相同 | 不依賴資料庫；渲染失敗回 `internal_error`，結果同樣保留到重新啟動（模板固定，失敗只可能來自程式錯誤） | `TestAPIDocsRendersTheServedDocument` |
| 忽略規則編譯程式 | `internal/adapter/media/ignore/cache.go` | 規則文字的 SHA-256 | 64 筆、16 MiB 權重 | **內容定址** | 各實例各自一份 | 不依賴資料庫 | `internal/adapter/media/ignore` 測試 |
| TMDB 候選（電影、劇集、季、集、圖片、外部 ID） | `internal/adapter/metadata/movie_cache.go` 的 `candidateCache` | (TMDB ID, 語言) 等 | 電影、劇集、集、外部 ID 各 256 筆；季、圖片各 16 筆 | **TTL** 24 小時，以 `fetchedAt` 起算（外部來源沒有版本號）；並發的未命中會合併成一次請求；錯誤從不快取 | 各實例各自一份 | 回 `ErrUnavailable`／`ErrRateLimited`，不改用過期資料（理由見 ADR 0002） | `TestMovieFetchCacheLanguageAndExpiry`、`TestCoalescedErrorsAreNotCached` 等 |
| Webhook 端點 | `internal/app/webhook_dispatcher.go`：`webhookEndpointCache` | 端點 ID | 只在一輪投遞內有效 | 每輪重建 | — | 該筆投遞跳過，租約到期後重送 | `internal/app` webhook 測試 |
| 監控用儲存空間讀數 | `internal/platform/telemetry/ops.go`：`storageSampler` | 卷名稱 | 設定的卷數 | TTL 30 秒，在背景重新整理 | 各實例各自量測 | 讀不到就不輸出該值，不阻塞抓取 | `internal/platform/telemetry` 測試 |

## 磁碟與資料庫中的衍生快取

| 項目 | 存放位置 | 鍵／有效條件 | 上限 | 失效方式 | 多實例下 | 來源不可用時 |
| --- | --- | --- | --- | --- | --- | --- |
| 圖片變體儲存 | 本機 `images` 儲存目錄（[儲存配置](storage-layout.md)） | 原圖內容摘要 + `variantKey`（管線版本 `fit-v1`、最大邊長、格式、尺寸、品質） | 依儲存配額 | **版本號**：輸出一變就提高管線版本；內容定址 | 各實例各自的目錄，內容一定相同 | 讀取失敗或檔案損毀視為未命中，重新產生；寫入只盡力而為，失敗不影響回應 |
| 字幕 UTF-8 複本 | 字幕快取目錄 | 來源身分（媒體庫、相對路徑、大小、mtime 或摘要）+ 字元集 + `ConverterVersion` | 依目錄 | **版本號**＋內容定址 | 同上 | 視為未命中，重新產生 |
| 探測快取（G06） | PostgreSQL `probe_cache` | (root, 相對路徑)；只有檔案戳記（大小、mtime、指紋與指紋版本）、工具身分、媒體庫／根目錄／條目 generation 全部相符才算命中 | 全域 100,000 列／1 GiB；每個媒體庫 50,000 列／256 MiB | **版本號**（戳記與 generation）；正向 TTL 30 天、負向 15 分鐘只用來回收 | 同一個資料庫；同一個檔案用租約去重（30 秒） | 資料庫就是來源；探測任務失敗後依任務重試，不影響已發布的條目。見 [探測快取](probe-cache.md) |
| NFO 驗證快取 | PostgreSQL NFO 快取表 | 檔案戳記 + 媒體庫／根目錄 generation | 全域 100,000 列／256 MiB | 同上；正向 30 天、負向 15 分鐘 | 同上 | 同上。見 [NFO 快取](nfo-cache.md) |
| 瀏覽器端圖片快取 | 用戶端 | 回應的 `ETag` 就是內容摘要；`tag` 參數相符才回 `immutable` | — | **內容定址** | — | — |

## 跨實例協調

協調都在 PostgreSQL 內完成，不需要額外的服務。

| 用途 | 機制 | 位置 | 說明 |
| --- | --- | --- | --- |
| 帳號管理序列化（避免同時降級或刪除最後的管理員） | `pg_advisory_xact_lock(hashtext(current_schema()),17481203)`，lock 等待上限 1.5 秒 | `postgres/accounts.go` | 和寫入同一交易，交易結束自動釋放 |
| 任務佇列的狀態轉換 | `pg_advisory_xact_lock(…,17481204)` | `postgres/jobs.go` | 搭配下列任務租約 |
| 任務租約 | `jobs.lease_until` + `owner` + `generation`（fencing），預設 30 秒，背景續約 | `postgres/jobs_execution.go`、`platform/jobs/runner.go` | 實例當掉時租約過期，由其他實例接手；舊擁有者的寫入因 generation 不符而被拒絕 |
| 監看（檔案系統事件）租約 | `ClaimWatch`／`RenewWatch`，30 秒 | `platform/jobs/watch.go`、`postgres/watch.go` | 同一個媒體庫同時只有一個實例在監看 |
| 探測／NFO 去重租約 | 快取列上的 `lease_owner`／`lease_generation`／`lease_until` | `postgres/probe_*.go`、`postgres/nfo_*.go` | 同一個檔案同時只有一個實例在探測 |
| NFO 寫入配額圍欄 | `pg_advisory_xact_lock(…,17481247)` + 圍欄列 | `postgres/nfo_write_preparations.go` | 固定加鎖順序，避免死鎖 |
| 觀看統計彙總 | `pg_try_advisory_xact_lock`：拿不到鎖就跳過這一輪 | `postgres/watch_stats.go`、`postgres/consistency_fix.go` | 同時只有一個彙總器在跑 |
| 分享連結數量上限 | `pg_advisory_xact_lock(hashtext('jelee.share_links'))` | `postgres/shares.go` | 並發建立時不會一起超過上限 |
| 媒體庫網路規則 | `pg_advisory_xact_lock(hashtext('jelee.library_network_rules'))` | `postgres/network_rules.go` | 序列化規則數量檢查 |
| 舊庫匯入 | 工作階段級 `pg_try_advisory_lock`，在專用連線上持有 | `postgres/legacy_import.go` | 同時只能有一個匯入 |
| Webhook 投遞 | 投遞列的認領與租約 | `postgres/webhooks.go` | 租約過期就重送；投遞語意是「至少一次」 |
| 版本欄位廣播 | `client_control_policy.version`、`dev_mode_state.version`、`site_appearance.revision` | 見上表 | 在同一交易內遞增；讀取端比較版本 |

目前沒有使用 `LISTEN`／`NOTIFY`。

## 各實例獨立的計數

以下狀態刻意只存在各實例的記憶體裡。部署 N 個實例時，最寬鬆的情況下效果是設定值的 N 倍：

| 項目 | 位置 | 全域一致的部分 |
| --- | --- | --- |
| 登入限速（IP 與帳號桶） | `internal/adapter/http/login_limiter.go` | 密碼錯誤累計與帳號鎖定寫在 `users.failed_login`／`locked_until`，全域一致 |
| 客戶端管控 `rate_limit` 動作 | `client_control.go` 的 `limiters` | 規則本身是全域的（版本號） |
| 直接串流的並發與頻寬上限 | `internal/adapter/media/limits.go` | 上限值（使用者、分享連結）來自資料庫，但計數在各實例 |
| TMDB 請求節流 | `internal/adapter/metadata/governor.go` | — |
| 稽核與活動紀錄的去重 | `shares.go` 的 `shareAccessLog`、客戶端活動節流 | 只影響紀錄筆數，不影響存取判斷 |

## 刻意不降級的地方

- **初始設定閘門**：還沒確認設定已完成時，讀不到資料庫就回 `ErrDatabase`。降級等於在設定未完成時放行一般 API。
- **分享訪客稽核**：寫不進存取紀錄就拒絕該請求，確保訪客的每次存取都有紀錄。
- **工作階段驗證與內容讀取**：本來就必須查資料庫，沒有可以安全沿用的「上次的值」（撤銷必須立即生效）。

## 多實例部署建議

1. 所有實例連到同一個 PostgreSQL；版本號與租約在提交後立即對每個實例生效，不需要 Redis 或訊息匯流排。
2. 引導期間只啟動一個實例，見 [部署：初始化](deployment.md#初始化g18)。
3. 規劃限流時，把各實例的登入限速、客戶端 `rate_limit`、每位使用者的串流上限，設為「全域目標 ÷ 實例數」；若需要精確的全域上限，見 ADR 0002「何時值得重新考慮 Redis」。
4. 在負載平衡器上對串流與 Range 請求啟用工作階段黏著（sticky），讓同一次播放留在同一個實例，並發與頻寬上限才會準確。
5. 修改字型白名單後，其他實例最多 30 秒才更新 CSP；開發者模式的變更最多延遲 10 秒。需要立即生效時，可以逐一重新啟動實例。
6. 圖片變體目錄每個實例各自一份；各實例會各自渲染一次，但內容定址保證結果相同。
