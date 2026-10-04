# 初始引導（G18）

新安裝的 Jelee 在完成初始引導之前，除了引導本身、健康檢查與前端外殼之外一律不對外服務。引導可以從瀏覽器（HTTP API）或無頭的 `jelee-cli setup --non-interactive` 完成，兩者共用同一套 app 層狀態機與 PostgreSQL 實作。

- 狀態機與驗證：`internal/domain/setup.go`、`internal/app/setup.go`。
- PostgreSQL：`internal/adapter/postgres/setup.go`，遷移 `000071_setup_state`。
- 即時檢查（目錄、連接埠、資料庫版本、工具鏈）：`internal/platform/setupenv`。
- HTTP 引導 API 與閘門：`internal/adapter/http/setup.go`、`setup_openapi.go`。
- 啟動時產生引導權杖：`internal/platform/runtime/setup.go`。
- CLI：`cmd/jelee-cli/setup.go`。
- 前端向導頁：`web/src/features/setup`（路由 `/setup`）。

## 步驟

順序固定（G18.1）：語言 → 管理員 → PostgreSQL → 媒體目錄 → TMDB → 工具鏈 → NFO 與圖片策略 → 網路發布與隱私 → 完成。每一步只接受「目前步驟」的提交，通過即時驗證後才寫入並前進；`back` 回上一步但保留已填資料。

| 步驟 | 輸入 | 即時檢查與錯誤碼（`details.issues[].code`） |
| --- | --- | --- |
| `language` | `locale` | 只接受 zh-CN／zh-TW／ja-JP／en-US：`locale_unsupported` |
| `admin` | `name`、`displayName`、`password` | 名稱規則、密碼 12–1024 位元組、至少 4 種字元、不得包含帳號名：`password_length_invalid`、`password_too_simple`、`password_contains_name`…；別處已建立管理員：`admin_exists` |
| `database` | 無 | 伺服器版本低於 16：`database_server_outdated`；遷移未到本版或 dirty：`database_schema_outdated`、`database_migration_dirty` |
| `media` | `libraries[{name,path}]`（可為空） | 絕對路徑、不重複、不巢狀；目錄不存在／不是目錄／服務帳號讀不到：`media_directory_missing`、`media_directory_not_directory`、`media_directory_unreadable` |
| `tmdb` | `enabled`、`language` | 啟用但未設定 `TMDB_API_KEY(_FILE)`：`tmdb_credential_missing`。引導**從不接受**金鑰本身 |
| `toolchain` | `acceptDegraded` | 檢查隨附的隔離 ffprobe 執行環境；缺少且未接受降級：`toolchain_missing` |
| `metadata-policy` | `nfoRead`、`nfoWrite`、`imageFetch`、`imageWriteBack` | 寫回需要先讀取、抓圖需要 TMDB |
| `network` | `mode`、`listen`、`allowedHosts`、`trustedProxies`、`privacyAcknowledged` | local 必須 loopback、lan 不可 loopback、reverse-proxy 必須有可信代理；被別的程式佔用的埠：`listen_port_in_use`（本服務自己設定的位址永遠視為可用）；lan／reverse-proxy 必須確認隱私說明 |

錯誤回應只含固定的欄位路徑與代碼，從不回顯輸入值（路徑、密碼、主機名稱都不會出現在錯誤裡）。

## 狀態機與持久化（G18.2）

`setup_state` 只有一列（`id=1`）：

| 欄位 | 用途 |
| --- | --- |
| `version` | 樂觀並行控制；每次寫入都比對版本，衝突回 `domain.ErrConflict` |
| `current_step` | 目前步驟名稱 |
| `state` | 各步驟資料的 JSON（不含密碼、雜湊、session 或 TMDB 金鑰），上限 256 KiB |
| `adopted` | 升級前就已在服務的既有部署（見下文） |
| `completed_at` | 完成時間；非空即為最終狀態 |

- **中斷恢復**：每一步都已落盤，重新開啟頁面、重啟伺服器或改用 CLI 都從儲存的步驟繼續。
- **重入不重複建管理員**：管理員在 `admin` 步驟以單一交易建立並把 `userId` 寫入狀態；回到該步驟再提交時只比對名稱，不重新雜湊、不建立第二個帳號。CLI 重跑時先倒回第一步再重播，同樣跳過已建立的管理員。
- **完成即最終**：repository 拒絕修改已完成的狀態，資料庫觸發器 `setup_state_final` 另外擋下任何 UPDATE／DELETE。

### 完成交易（G18.4）

`POST /api/v1/setup/complete`（或 CLI 的最後一步）先重新檢查資料庫與媒體目錄，再在**一個交易**內：

1. 鎖定狀態列並比對版本；
2. 確認引導建立的管理員仍是啟用中的管理員；
3. 以純 INSERT 建立媒體庫與根目錄，帶入 NFO 讀取模式與中繼資料語言（TMDB 語言，未啟用 TMDB 時用介面語言）；名稱或根目錄被別人先佔用就是衝突，不會合併；
4. 寫入完成狀態與稽核。

任何一步失敗整個交易回滾：不留下媒體庫、不留下稽核列、狀態版本不變，引導停在「完成」步驟，可排除原因後重試。

NFO 寫回、抓圖與網路設定目前記錄在完成的引導狀態裡；實際生效的監聽位址、Host 白名單、可信代理與 NFO 寫回開關仍以伺服器設定（`JELEE_LISTEN`、`JELEE_ALLOWED_HOSTS`、`JELEE_TRUSTED_PROXIES`、`JELEE_ENABLE_NFO_WRITE`）為準，兩者不一致時以設定為準。

## 閘門（半初始化實例不對外服務）

閘門在 HTTP 邊界中介層（`boundary`）裡、Host 檢查之後執行，涵蓋原生 API、相容層 `/compat`、`/metrics` 與其他所有路由。未完成引導時：

| 請求 | 結果 |
| --- | --- |
| `GET /healthz` | 200。**存活檢查**：只表示程序在跑，不碰資料庫 |
| `GET /readyz` | 資料庫可用即 200，`data.setup` 為 `required` 或 `completed`。等待引導的實例算就緒，否則負載平衡不會把流量送進來、引導頁也打不開；資料庫或引導狀態讀不到則 503 `not_ready` |
| `GET /api/v1/system`、`/api/v1/openapi.json`、`/api-docs` | 照常（探索用，不碰資料庫） |
| `/api/v1/setup/**` | 引導 API |
| 沒有任何 API 路由認領、不在 `/api` 與 `/compat` 之下的 GET／HEAD | 前端外殼與靜態資源，讓瀏覽器能載入引導頁 |
| 其他一切（含相容層、登入、非正規拼寫） | **503 `setup_required`** |

引導狀態讀取失敗時一律當作未完成（fail closed，回 503 `not_ready`）。完成是最終的：同一程序在完成請求成功時立即放行；其他實例或 CLI 完成時，本實例最多 1 秒內跟上。啟動時已完成的實例完全不再查詢引導狀態。

完成之後，`/api/v1/setup/**` 全部回 **410 `setup_completed`**（包含 `status`），其餘路由照常。沒有帳號功能（`JELEE_ENABLE_ACCOUNTS=false`）的部署沒有引導、也不受閘門影響，因為引導要建立的是登入用的管理員。

### 既有部署升級

遷移 `000071` 在建立資料表時，只要已有任何未刪除的使用者或任何媒體庫，就直接寫入一列 `adopted=true`、已完成的狀態，正在運作的伺服器升級後不會被鎖。即使之後所有管理員都被降級，這一列仍維持完成。另外，全新資料庫若在引導前以 `jelee-cli account bootstrap` 建了管理員，執行時也視為已完成（adopted）。

降版 `071→070` 時若有**未完成**的引導列會拒絕（`55000`），避免舊版程式在半初始化狀態下對外服務；已完成或 adopted 的列可直接降版，再升級時會重新 adopted。

## 安全：一次性引導權杖

選擇「一次性權杖」而不是「只允許本機或設定的來源位址」：在 Docker 連接埠映射或反向代理後面，合法的引導者並不是 loopback；若把代理位址列為允許，就等於允許整個網際網路。權杖則只有能讀到伺服器主控台或權杖檔的維運者拿得到。

- 伺服器啟動時若引導未完成，產生 32 位元組隨機權杖（base64url 43 字元）。設定 `JELEE_SETUP_TOKEN_FILE`（絕對路徑）時以 0600 寫入該檔；否則印到**標準錯誤**的一行文字。權杖不經過結構化日誌（日誌白名單本來就會遮蔽它），日誌只記一筆「需要初始引導」的警告。
- 每次啟動都換新權杖；引導完成後權杖失去作用。
- 除了 `GET /api/v1/setup/status` 之外，所有引導 API 都要求 `X-Jelee-Setup-Token` 標頭完全相符（常數時間比較），否則 401 `setup_token_invalid`，並記一筆含遮蔽後用戶端位址的警告日誌。權杖放在自訂標頭，瀏覽器不會自動附帶，因此沒有 CSRF 問題。
- 多實例部署請在引導期間只啟動一個實例，或改用 CLI 引導；每個實例的權杖不同。

### 稽核

在同一交易內寫入 `audit_logs`（目標 `setup`），HTTP 來源帶用戶端位址與請求 ID，`after_state.channel` 為 `http` 或 `cli`：

| 事件 | 時機 |
| --- | --- |
| `setup.step_saved` | 每次前進或後退（before／after 為步驟名稱） |
| `setup.admin_created` | 引導建立管理員（目標為使用者 ID；密碼雜湊依稽核規則遮蔽） |
| `library.registered` | 完成時每個媒體庫 |
| `setup.completed` | 完成摘要：媒體庫數、語言、網路模式、TMDB、NFO 讀取模式 |

## HTTP API

完整契約見 `api/openapi.json`。

| 方法與路徑 | 說明 |
| --- | --- |
| `GET /api/v1/setup/status` | 公開。未完成 200 `{setupRequired:true, tokenRequired:true}`；完成後 410 |
| `GET /api/v1/setup` | 讀取目前狀態 |
| `POST /api/v1/setup/steps/{step}` | 提交目前步驟；`database` 不需內容 |
| `POST /api/v1/setup/back` | 回上一步 |
| `POST /api/v1/setup/complete` | 完成交易 |

錯誤碼：400 `setup_validation_failed`（`details.step`、`details.issues`）、401 `setup_token_invalid`、409 `setup_step_order`／`conflict`、410 `setup_completed`、503 `setup_required`／`account_busy`。四語訊息在 `internal/platform/i18n/messages.go`。

## 前端向導頁

`web/src/features/setup/SetupView.vue`（路由 `/setup`，公開頁）。前端每次載入先問一次 `GET /api/v1/setup/status`：200 就把所有導覽導向 `/setup`，其他回應（410、舊版伺服器、網路錯誤）照常載入應用程式。頁面先要求輸入權杖，接著依伺服器回傳的目前步驟顯示對應表單，續做時以儲存的資料預填；驗證錯誤依固定代碼顯示四語訊息（`setup.issues.*`），未知代碼顯示通用訊息。權杖只放在記憶體，重新整理後要重輸；不寫入 localStorage／sessionStorage。完成後提示前往登入。

## 無頭初始化（G18.5）

`jelee-cli setup --non-interactive` 讀取與伺服器相同的設定（`JELEE_DATABASE_URL`、`JELEE_ENABLE_ACCOUNTS=true`、Argon2 參數等），密碼只從標準輸入讀：

```sh
printf '%s\n' "$ADMIN_PASSWORD" | jelee-cli setup --non-interactive --password-stdin \
  --admin-name admin --locale zh-TW --library Movies=/media/movies --accept-degraded-tools
```

成功時在標準輸出印出完成的狀態 JSON（不含任何密碼）並以 0 結束。輸入錯誤以 2 結束並逐行列出 `欄位 代碼`；已完成時印 `setup_already_completed` 並以 1 結束，因此容器每次啟動前執行都安全。其他結束碼：`setup_configuration_invalid`（設定不可用或未啟用帳號）、`setup_database_unavailable`（含尚未遷移）、`setup_conflict`、`setup_timeout`（124）、`setup_cancelled`（130）。

## 驗證

| 項目 | 測試 |
| --- | --- |
| 中斷恢復、後退、重入不重複建管理員、完成後最終（真 PG） | `TestSetupPostgresResumeReentryAndCompletion` |
| 完成交易失敗整體回滾、排除後重試（真 PG） | `TestSetupPostgresCompletionRollsBack` |
| 版本 CAS、競爭建立管理員只成功一次 | `TestSetupPostgresCompareAndSwap` |
| 既有部署升級自動完成、up/down/up | `TestSetupMigrationAdoptsExistingDeploymentUpDownUp` |
| 未完成引導拒絕降版 | `TestSetupMigrationRefusesDowngradeOfUnfinishedWizard` |
| 遍歷所有已註冊路由（含相容層）都回 503 | `TestSetupGateBlocksEveryRegisteredRoute` |
| 權杖、驗證錯誤格式、410、1 秒內跟上其他實例、fail closed | `internal/adapter/http/setup_test.go` |
| 實際 runtime：印出權杖、閘門、HTTP 完成引導後可登入（真 PG） | `TestSetupRuntimePostgresWizardOpensGate` |
| CLI 無頭初始化與重跑（真 PG） | `TestSetupCLIHeadlessPostgres` |
| 目錄可讀性、埠衝突 | `internal/platform/setupenv/setupenv_test.go` |
| 前端：導向向導、權杖、逐步提交、驗證訊息、後退、完成後回到登入 | `web/src/features/setup/setup.test.ts` |

## 尚未完成

- 前端向導頁只在 jsdom 測過，尚未在真實瀏覽器與真實伺服器上走過一遍。
- 未在真實容器（Compose）與真實反向代理後做過端到端驗收。
