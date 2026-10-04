# 開發者模式（G45）

> **警告：開發者模式只供開發與除錯，絕對不可在生產環境啟用。** 開啟後可以放寬登入限速、權限嚴格模式、Host 校驗、SSRF 攔截等保護，並把額外資料寫進日誌。生產部署請設定 `JELEE_ENV=production`（官方容器映像預設如此），此時所有開發者設定一律被忽略並告警。

本文列出 Jelee 開發者模式的開啟方式、可見標記、每一個開關（預設值、風險、恢復方式）、危險操作的二次確認、自動降級，以及尚未接線的項目。

- 狀態機與門檻：`internal/platform/devmode/`（`gate.go` 門檻、`state.go` 狀態機、`toggles.go` 開關與危險操作目錄、`controller.go` 共用工作階段與到期、`record.go`）。
- 儲存：`internal/adapter/postgres/devmode.go`，遷移 `000072_dev_mode`（`dev_mode_state` 單列、`dev_mode_tokens`）。
- HTTP：`internal/adapter/http/devmode.go`（路由、pprof）、`devbody.go`（請求／回應體日誌）、`devmode_openapi.go`；邊界中介層 `server.go` 的 `boundary`。
- 執行期：`internal/platform/runtime/devmode.go`（控制器、日誌層級、SQL 日誌、背景重新整理）。
- CLI：`jelee-cli devmode enable|disable|status`（`cmd/jelee-cli/devmode.go`）。
- 前端：`web/src/features/devmode/`（頂部常駐橫幅，四語）。

## 開啟流程（G45.1、G45.2）

開發者模式需要**同時**滿足下列門檻，缺任何一項都不會開啟：

1. 伺服器與 CLI 的環境變數 `JELEE_DEV_MODE=true`（只接受 `true`／`false`／空值，其他值拒絕啟動）。
2. 設定檔（`JELEE_CONFIG`）中 `"dev": {"enabled": true}`。
3. 一次性權杖：只有伺服器能產生，且只從**環回入口**取得：

   ```sh
   curl -sS -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:8097/api/v1/dev/token
   ```

   傳輸層來源必須是環回位址，而且請求不得帶任何轉送標頭（`Forwarded`、`X-Forwarded-*`、`X-Real-IP`），否則一律回 404——同主機上的反向代理不會把遠端用戶端變成「環回」。權杖 5 分鐘內有效，只能兌換一次；資料庫只存 SHA-256 摘要；簽發寫稽核 `devmode.token_issued`。
4. 用 CLI 兌換權杖：

   ```sh
   JELEE_DEV_MODE=true JELEE_CONFIG=/path/dev.json jelee-cli devmode enable --token jdm_… [--ttl 2h]
   ```

   CLI 自己也要滿足 1、2 且不在生產環境；權杖一經提交就作廢（即使這次因其他原因被拒）。成功寫稽核 `devmode.enabled`（時間、來源 `cli`、設定差異 `configDiff`、開始與到期時間），被拒寫 `devmode.denied`（含缺少的門檻；生產環境拒絕標記 `alert`）。

HTTP **永遠不能**開啟開發者模式：沒有任何網址能直接開啟，只有環回入口能拿到權杖，權杖只有 CLI 兌換。

生產環境：`JELEE_ENV=production`（不分大小寫、可含前後空白）時，伺服器與 CLI 都忽略全部開發者設定；伺服器啟動時寫 ERROR 日誌（`devmode_production_denied`），`jelee-cli doctor` 回報 `devmode_production_ignored`。官方 `Dockerfile` 設定 `ENV JELEE_ENV=production` 與標籤 `org.jelee.channel=production`，所以容器映像預設禁用；要在容器裡開發，必須刻意覆寫 `JELEE_ENV`。

在 1、2 都成立的實例（「可開發實例」）上才會註冊開發者路由；其他實例完全沒有這些路由（404），也不套用任何放寬。初始引導（G18）完成前，引導閘門會擋下權杖路由（503 `setup_required`），請先完成引導。

## 狀態保存與多實例

- 工作階段存在 PostgreSQL 的單列 `dev_mode_state`（是否啟用、開始與到期時間、來源、已開啟的開關、版本號），所有實例與 CLI 共用。每個實例每 2 秒重新讀取（不可開發的實例每 10 秒），變更也會立即更新發出變更的那個實例。
- 每個實例**用自己的時鐘**判斷到期：到期那一刻起，標頭、系統資訊與所有放寬立刻失效，不必等資料庫。
- 任何**不滿足門檻**（缺開關或在生產環境）的實例只要看到資料庫中有啟用中的工作階段，就立刻把它關掉並寫稽核 `devmode.disabled`（原因 `instance does not meet the developer mode thresholds`）：混合部署時以最嚴格的實例為準。
- **重啟預設不繼承**（G45.7）：每個實例啟動時會把資料庫中的工作階段關掉（稽核 `devmode.disabled`，原因 `process restart`）。多實例滾動重啟時，開發者模式也會因此結束，需要重新開啟。若確實要跨重啟保留，設定 `"dev": {"persistAcrossRestart": true}`；每次因此保留都會寫 WARN 日誌 `devmode_persisted`。到期時間不因重啟延長。

## 持續可見（G45.3）

開發者模式啟用期間：

- 每個 HTTP 回應帶 `X-Jelee-Dev-Mode: true`（關閉時一律 `false`，包括 404、錯誤與相容層回應）。
- `GET /api/v1/system` 回 `"devMode": true` 與 `"devModeExpiresAt"`（RFC 3339）；這兩個欄位公開，讓任何用戶端都能警告使用者。開關清單只給管理員。
- 啟動時：可開發實例寫 WARN（`never run this configuration in production`）；生產環境卻帶開發設定寫 ERROR。
- 每 5 分鐘一次 WARN 提醒（`devmode_active`，含到期時間與已開啟的開關）；每次開啟、關閉、到期、開關變更與拒絕也各寫一行 WARN（生產環境拒絕為 ERROR）。
- 網頁前端每一頁頂部顯示常駐紅色橫幅（`role="alert"`，含自動關閉時間），每分鐘重新確認；四語（zh-CN、zh-TW、ja-JP、en-US）。

## 開關一覽（G45.4、G45.5）

所有開關預設關閉，只在工作階段有效期間生效，工作階段結束（到期、`jelee-cli devmode disable`、`POST /api/v1/dev/disable`、重啟、被不可開發實例關閉）時全部恢復生產預設。標示「危險」的開關開啟時必須帶 `"iUnderstand": true`（G45.6），關閉時不需要。開關以管理員 API `PUT /api/v1/dev/toggles/{toggle}` 切換，每次切換寫稽核 `devmode.toggle_changed`，被拒寫 `devmode.denied`。

「未接線」的開關在 API 中顯示 `available: false`，開啟時回 409 `devmode_toggle_unavailable`（不會假裝生效）。

### 可放寬的限制（G45.4）

| 開關 | 危險 | 接線 | 開啟時的效果 | 風險 | 恢復 |
| --- | --- | --- | --- | --- | --- |
| `relax_login_rate_limit` | 否 | 已接 | 網頁、原生與相容層登入不再計入 IP／帳號的登入限速桶（`LoginLimiter`） | 可被暴力嘗試密碼 | 關閉後下一次嘗試起恢復計數；**帳號連續失敗鎖定（`accounts.lockAfter`）仍然生效** |
| `relax_api_rate_limit` | 否 | 已接 | 客戶端管控（G47）的 `rate_limit` 規則不再回 429；命中仍會記錄 | 單一客戶端可耗盡資源 | 下一個請求起恢復 |
| `relax_playback_concurrency` | 否 | 已接 | 新的播放不受每使用者／每裝置並發上限（含使用者覆寫）限制 | 使用者可同時開大量串流 | 之後的新播放恢復限制；已放行的播放跑完為止。程序層級的 `maxStreams` 資源上限**不放寬** |
| `relax_bandwidth_limit` | 否 | 已接 | 新的播放不受頻寬上限限制 | 單一使用者可佔滿頻寬 | 之後的新播放恢復；已放行的播放維持放行時的速率 |
| `relax_ignore_rules` | 否 | 未接 | — | — | 見「後續」 |
| `relax_nfo_read_only` | 是 | 未接 | — | — | 見「後續」 |
| `relax_image_lock` | 否 | 未接 | — | — | 見「後續」 |
| `relax_permission_strict` | 是 | 已接 | 暫停 `access_policy.restrict_admins`：**只有管理員**恢復看見全部內容（見下節 G48.9） | 管理員看到原本對自己隱藏的內容 | 關閉或工作階段結束後下一個查詢起恢復 |
| `relax_host_strict` | 是 | 已接 | 任何語法合法的 `Host` 都接受（不再比對 `allowedHosts`）；格式錯誤仍回 400 | DNS rebinding、Host 標頭攻擊 | 下一個請求起恢復 |
| `relax_ssrf_strict` | 是 | 已接（需啟用 Webhook） | Webhook 出站客戶端額外允許私網（RFC 1918、ULA）、CGNAT 與環回目標，便於本機測試接收端；連結本地位址（含雲端中繼資料 169.254.169.254）、未指定與多播位址**永遠拒絕**；TMDB／圖片抓取仍受主機白名單限制 | 透過 Webhook 探測內網 | 下一次連線起恢復；已存的私網目標之後投遞會被拒絕 |
| `relax_public_ip_hiding` | 是 | 未接 | — | — | 見「後續」 |
| `relax_client_ua_block` | 否 | 已接 | 客戶端管控的拒絕（`client_blocked`）與待核准（`client_pending_approval`）判定暫停，適用所有維度（不只 User-Agent）；唯讀、強制重新登入、限速照常 | 被封鎖的客戶端可存取 | 下一個請求起恢復 |

#### G48.9：`relax_permission_strict` 的定義

嚴格權限的放寬**只作用在管理員**：統一可見性述詞（`internal/adapter/postgres/visibility.go`）在 `restrict_admins` 開啟時，若 `dev_mode_state` 中有未到期且開啟此開關的工作階段，管理員就回到「看得到一切」的預設；媒體庫授權本來就不限制管理員。**非管理員的媒體庫授權、條目規則、分級上限與標籤封鎖完全不受影響，所以不可能洩漏任何內容給非管理員**（`TestDevModePermissionRelaxPostgres` 斷言檢視者與其他使用者的可見集合不變）。此判斷在 SQL 內以資料庫時鐘比對 `expires_at`，到期即失效；不可開發的實例會主動關閉共用工作階段，所以混合部署也不會殘留。

### 調試選項（G45.5）

| 開關 | 危險 | 接線 | 開啟時的效果 | 風險 | 恢復 |
| --- | --- | --- | --- | --- | --- |
| `debug_verbose_logging` | 否 | 已接 | 全域日誌層級改為 DEBUG（`jelee` 主程式經 `runtime.NewWithLogs` 接上日誌路由） | 日誌量大增 | 回到設定的層級 |
| `debug_sql_logging` | 否 | 已接 | 每條 SQL 寫一行 INFO（元件 `db`，代碼 `devmode_sql_log`）：語句文字（壓成一行、最多 512 位元組）、耗時微秒、影響列數、是否失敗。**不記錄參數**。追蹤器只掛在可開發實例的連線池上，生產環境零成本 | 日誌量大增；語句結構外露 | 下一條語句起停止 |
| `debug_body_logging` | 是 | 已接 | 每個請求寫一行 INFO（代碼 `devmode_body_log`）：方法、路由樣式（不含實際 ID）、狀態碼、JSON 請求體與回應體各最多 4 KiB。鍵名含 password、token、secret、apiKey、key、authorization、cookie、csrf、credential 等的值一律換成 `[redacted]`；超過上限、非 JSON（媒體、HTML、圖片）只記大小 | 日誌含使用者輸入與中繼資料 | 下一個請求起停止 |
| `debug_pprof` | 是 | 已接 | `GET /debug/pprof/*`、`POST /debug/pprof/symbol`（net/http/pprof）；只給環回（無轉送標頭）或管理員，其他人 404 | 洩漏記憶體內容、CPU 負載 | 關閉後立即 404 |
| `debug_openapi_internal` | 否 | 未接 | — | — | 可開發實例的 `/api/v1/openapi.json` 已包含開發者路由；目前沒有另外的內部 API |
| `debug_error_stacks` | 是 | 未接 | — | — | 見「後續」 |
| `debug_simulated_clients` | 否 | 未接 | — | — | 見「後續」 |
| `dev-transcode` | 是 | **刻意不接** | — | — | G10 鐵律：二進位內沒有任何編碼器路徑（`TestDeliveryPackagesCannotRunEncoders`），不為開發者模式加入 |
| `debug_mock_external` | 否 | 未接 | — | — | 見「後續」 |
| `debug_seed_data` | 否 | 未接 | — | — | 見「後續」 |
| `debug_force_jobs` | 否 | 未接 | — | — | 管理員本來就能用 `POST /api/v1/libraries/{id}/schedule/run` 立即執行排程 |

## 危險操作二次確認（G45.6）

| 目錄中的操作 | Jelee 目前的對應 | 確認方式 | 稽核 |
| --- | --- | --- | --- |
| 重建庫 `rebuild_library` | `POST /api/v1/libraries/{id}/probe/rebuild`（丟棄整個媒體庫的探測結果並重新掃描） | API 本體必須帶 `"iUnderstand": true`，否則 400 `confirmation_required`；CLI `jelee-cli jobs probe-rebuild-library … --i-understand` | `probe.library_invalidated` |
| 關閉鑑權 `disable_auth` | Jelee 沒有關閉驗證的開關。最接近的兩個：開發者開關 `relax_permission_strict`（危險開關確認），以及停用全部客戶端管控規則的 `jelee-cli access reset-policies` | 前者 `"iUnderstand": true`；後者 `--i-understand`（否則結束碼 2、不連資料庫） | `devmode.toggle_changed`／`client_control.policies_reset` |
| 其他危險開關 | 上表標示「危險」者 | `"iUnderstand": true` | `devmode.toggle_changed`，被拒 `devmode.denied` |
| 刪除全部資料 `delete_all_data` | **不存在**：沒有刪除全部資料的 API 或 CLI | — | — |
| 清空快取 `clear_cache` | **不存在**：圖片與探測快取由容量、TTL 與工作流程自行回收，沒有全域清除入口 | — | — |
| 導入不受信 NFO `import_untrusted_nfo` | **不存在**：NFO 一律唯讀解析並經安全驗證，沒有略過驗證的匯入入口（`relax_nfo_read_only` 未接） | — | — |

單一條目的探測重建（`POST /api/v1/items/{id}/probe/rebuild`）影響範圍有限，不要求確認。日後新增上述「不存在」的操作時，必須沿用相同的 `iUnderstand`／`--i-understand` 與稽核。

## 自動降級與手動關閉（G45.7）

- 有效期：`"dev": {"ttlMinutes": N}`，預設 720（12 小時），上限 1440（24 小時）；`jelee-cli devmode enable --ttl` 可為單次工作階段指定，同樣不得超過 24 小時。資料庫約束也拒絕超過 24 小時的工作階段。
- 到期：各實例在到期那一刻停止套用；背景迴圈隨即把資料庫中的工作階段關閉並寫稽核 `devmode.expired`（含被恢復的開關清單 `restored`，標記 `alert`），只會寫一次。
- 手動關閉：`jelee-cli devmode disable`（任何程序都能關，不需要門檻）或管理員 `POST /api/v1/dev/disable`（本體 `{}`），寫 `devmode.disabled`。
- 查詢：`jelee-cli devmode status`、管理員 `GET /api/v1/dev`。
- 緊急情況：把 `JELEE_DEV_MODE` 拿掉或設 `JELEE_ENV=production` 後重啟任一實例，該實例會立刻把共用工作階段關掉。

## API

只在可開發實例上存在；其他實例 404。可開發實例的 `/api/v1/openapi.json` 會列出這些路徑（標記 `x-jelee-dev-only`）；提交的 `api/openapi.json` 是生產參考部署，**不含**它們。

| 路徑 | 權限 | 說明 |
| --- | --- | --- |
| `POST /api/v1/dev/token` | 環回、無轉送標頭 | 本體 `{}`，回 201 `{token, expiresAt, enable}` |
| `GET /api/v1/dev` | 管理員 | 工作階段與 23 個開關（`name`、`kind`、`dangerous`、`available`、`enabled`）、`ttlSeconds` |
| `PUT /api/v1/dev/toggles/{toggle}` | 管理員 | 本體 `{"enabled": bool, "iUnderstand"?: bool}`；409 `devmode_inactive`、409 `devmode_toggle_unavailable`、400 `confirmation_required`、未知開關 404 |
| `POST /api/v1/dev/disable` | 管理員 | 本體 `{}`；204，沒有工作階段時 409 `devmode_inactive` |
| `GET /debug/pprof/*`、`POST /debug/pprof/symbol` | 環回或管理員 | 未開 `debug_pprof` 時 404 |

錯誤碼（四語訊息）：`devmode_inactive`（409）、`devmode_toggle_unavailable`（409）、`confirmation_required`（400）。

## 測試隔離（G45.8）

所有測試預設在開發者模式關閉態執行（沒有設定 `JELEE_DEV_MODE` 與 `dev.enabled`）。斷言：

- `TestDevModeUnreachableInProduction`：先從可開發實例自動走訪出全部開發者路由，再對預設、生產（含大小寫與空白變形）、只設環境變數、只設設定檔四種組態——即使傳入一個會套用「已開啟、含 `relax_host_strict` 與 `debug_pprof`」共用工作階段的控制器——斷言：不註冊任何開發者路由；每條開發者路由以管理員權杖、從環回請求都回 404；**自動走訪所有已註冊路由**，回應一律 `X-Jelee-Dev-Mode: false` 且外來 Host 一律 400；系統資訊 `devMode:false`；規格不含開發者路徑。
- `TestDevModeOpenAPIMatchesRoutes`：可開發實例的規格與路由一一對應，全部標記 `x-jelee-dev-only`。
- `TestAccessLeakRouteTableIsComplete`／`TestAccessLeakHiddenContentPostgres`：走訪時含開發者路由（可開發但未開啟），每條都已登記。
- 門檻、權杖、到期、重啟、混合部署：`internal/platform/devmode/controller_test.go`、`internal/adapter/postgres/devmode_test.go`、`cmd/jelee-cli/devmode_test.go`、`internal/platform/runtime/devmode_integration_test.go`。

## 後續（未接線的開關）

- `relax_ignore_rules`、`relax_image_lock`、`relax_nfo_read_only`：這三項放寬的結果會**永久寫進目錄或媒體庫**（被忽略的檔案進入目錄與基準、鎖定的圖片被自動更新取代、NFO 被寫回），工作階段到期也無法復原，違反「到期恢復生產限制」。需要先設計「開發者模式產生的結果可標記並撤回」或逐次確認的流程。
- `relax_public_ip_hiding`：目前沒有可放寬的執行期約束（只有 doctor 的隱私檢查），等網路隱私約束實作後再接。
- `debug_error_stacks`、`debug_simulated_clients`、`debug_mock_external`、`debug_seed_data`：尚未實作對應功能。
- `debug_openapi_internal`：沒有獨立的內部 API。
- `dev-transcode`：依 G10 鐵律不實作。

## 擁有者驗證

- C9：在真實時鐘下開啟一次預設 12 小時的工作階段，確認 12 小時後標頭、系統資訊、前端橫幅與所有放寬自動消失，稽核出現一筆 `devmode.expired`，期間每 5 分鐘有 WARN 提醒。自動測試以注入時鐘覆蓋到期邏輯。
