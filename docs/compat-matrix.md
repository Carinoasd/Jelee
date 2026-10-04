# 客戶端探測與已移除能力

本表描述目前 Go 入口的實際合同。尚未驗收完整第三方客戶端握手與 UI 行為，不能將本表當成全部舊協定相容。

| 探測表面 | Go 回應 | 行為 |
| --- | --- | --- |
| `/LiveTv` 及其子路徑 | HTTP 501，`feature_removed` | 直播、EPG、調諧器、錄製、計時器均不啟用 |
| `/Channels` 及其子路徑 | HTTP 501，`feature_removed` | 頻道功能不啟用 |
| `/Dlna` 及其子路徑 | HTTP 501，`feature_removed` | 不提供 DLNA HTTP 功能 |
| `/api/v1/system` | HTTP 200 | dlna、discovery、liveTv、epg、tuners、recordings、channels 全為 false |
| 伺服器 UDP 7359 探索 | 舊 C# 探索 host 已刪除 | 客戶端輸入服務網址連線；LAN 封包驗收仍待完成 |
| `/compat/LiveTv`、`/compat/Channels`、`/compat/Dlna` 及子路徑 | HTTP 501，`feature_removed` | 相容前綴下同樣拒絕；前綴與根段都大小寫不敏感；相容層關閉時亦同 |

三類 HTTP 根路徑大小寫不敏感；只比對完整第一段，不影響相似名稱或其他根路徑。所有方法及子路徑都拒絕啟用。既有 Host 驗證仍先回400，轉換／HLS／DASH路徑仍先回409，debug路徑仍回404。

這些是公開的能力拒絕探測，不查驗帳號、不查資料庫、不開啟媒體、不建立工作。錯誤 envelope 包含 code/message/details/traceId，依 Accept-Language 提供四語、預設簡中／未知英文。HEAD 回應仍為501；HTTP傳輸層依HEAD規則不傳送正文。持久化使用者語系優先仍由已驗證的正式驗證流程處理，此公開探測不查詢使用者設定。

OpenAPI 的 `x-jelee-removed-features` 擴充欄位列出三個根路徑、狀態與錯誤碼；其值另有HTTP一致性回歸。17條路徑×6方法×4語系×2開關狀態，共816請求通過，沒有後端／媒體呼叫。另驗證Host／轉碼／debug優先、相似與不同根路徑404、HEAD與OpenAPI；證據見[探測驗證](evidence/removed-feature-http.json)。

G05仍部分完成：舊C#的直播與Channel控制器已刪除並提供[明確拒絕入口](legacy-removed-features.md)；其餘調諧器、EPG、錄製、Channel服務與排程仍待移除，相關資料、設定、翻譯鍵與圖示也未全部清理。沒有將舊C#內部全部功能與排程宣稱為已關閉。防火牆與探索裁剪詳[部署](deployment.md)、[探索裁剪](server-discovery-removal.md)。

另外以真正loopback TCP/HTTP驗證HEAD：501、正文長度0、未知語系回en-US，沒有後端／媒體呼叫；Windows與Linux race回歸通過。

## 舊 C# HTTP 入口

直播／頻道控制器與專用 DTO 已刪除，三類根路徑回501／feature_removed，四語／HEAD／設定不變／OpenAPI已驗證。直播設定仍先驗證授權，受限IP先回既有503；[完整合同與證據](legacy-removed-features.md)。內部服務與排程仍待移除。

## 第三方客戶端相容層（`/compat`，G24.1～G24.4、G10.4）

目前有骨架、系統模組與使用者／登入模組；媒體庫、Items、播放資訊等模組尚未提供，下表以外的路由一律回 404。尚未做真實客戶端驗收（G24.5），不能宣稱任何客戶端已可使用。

### 掛載與開關

- 設定 `JELEE_COMPAT_ENABLED=true`（設定檔 `enableCompat`）才掛載，**預設關閉**。關閉時 `/compat` 底下一律回自有 API 的 404 `not_found`，不會落到前端頁面；三類已移除功能仍回 501。
- 客戶端的伺服器網址填 `https://host/compat`。
- 伺服器 ID：`JELEE_COMPAT_SERVER_ID`（`compatServerId`，32 位小寫 hex、不可全 0）。未設定時由 `allowedHosts` 經 SHA-256 單向推導，主機清單變動 ID 就會變，客戶端會把它當成另一台伺服器；正式部署建議固定設定。

### 請求處理順序

1. 伺服器既有邊界（不因相容層改變）：Host 驗證 400 → 路徑型轉換／HLS／DASH 409 `transcode_disabled` → debug 404 → 已移除功能 501 `feature_removed`。這幾項沿用自有 API 的錯誤 envelope。
2. 相容層邊界：帶 `Origin` 標頭（任何值，包括空字串與 `null`）一律 403、空主體，且從不送出任何 `Access-Control-*` 標頭，所以 CORS 預檢也被拒絕。任何瀏覽器頁面（含同源）都無法經相容層取得原生能力。
3. 轉碼守衛：`GuardProduction` 檢查**所有**相容路由的路徑、query 與 JSON／表單主體；轉換參數回 409 `transcode_disabled`（自有 envelope，全域統一，G10.3）。之後的模組若需要把客戶端能力宣告和轉換請求分開解析（例如 PlaybackInfo 的 DeviceProfile），也不能放寬守衛。
4. 路由比對：前綴與路徑的字面段都大小寫不敏感，忽略一個結尾斜線；字面段優先於參數段；空段、`%2F` 編碼斜線、多一段或少一段都回 404。

### 驗證

- 用 `ParseClientAuth` 從參數化 Authorization 標頭、兩個舊 token 標頭、`ApiKey`／`api_key` query 取得 token，再交給伺服器既有的工作階段查詢（同一個 `Authenticate`，到期、撤銷、停用與刪除規則相同，沒有另一套）。
- **只接受 native 工作階段**。web 工作階段、未知或格式錯誤的 token、`Bearer` 標頭、session cookie 一律 401、空主體。選 401 而不是 403 的理由：上游對無法使用的憑證一律回驗證挑戰（401），而且 web token 與未知 token 回應完全相同，相容層不會替攻擊者確認某個 token 是有效的 web 工作階段。cookie 完全不讀取。
- 資料庫不可用回 503，其他內部錯誤回 500，主體只有 `Error processing request.`。

### 錯誤格式

比照上游正式環境：401／403／404／405／413／415／503 為空主體；400 與 500 為 `text/plain`、固定文字 `Error processing request.`。不回傳例外訊息、路徑、SQL 或位址。轉換請求（409）與已移除功能（501）沿用自有 envelope，因為 G10.3 要求全域統一的錯誤碼。

### 已支援路由

| 路由 | 驗證 | 回應 |
| --- | --- | --- |
| `GET /compat/System/Info/Public` | 免驗證，不查資料庫 | `ServerName`=`Jelee`、`Version`=`10.11.0`、`ProductName`=`Jelee Server`、`OperatingSystem`=`""`、`Id`、`StartupWizardCompleted`=`true` |
| `GET /compat/System/Info` | native 工作階段 | 上列欄位＋`HasPendingRestart`、`IsShuttingDown`、`SupportsLibraryMonitor`、`CanSelfRestart`、`CanLaunchWebBrowser`、`HasUpdateAvailable` 全為 false，`WebSocketPortNumber`=0，`CompletedInstallations`／`CastReceiverApplications` 為空陣列，`EncoderLocation`=`NotFound`，`JeleeCapabilities` 的 Transcoding／Remux／Hls／Dash／LiveTv／Channels／Dlna／Downloads 全為 false |
| `GET`、`POST /compat/System/Ping` | 免驗證 | JSON 字串 `"Jelee Server"` |
| `POST /compat/Users/AuthenticateByName` | 免驗證（帳號服務開啟時才掛載） | 見下方「使用者／登入模組」；成功回 `AuthenticationResult` |
| `GET /compat/Users/Public` | 免驗證，不查資料庫 | 固定空陣列 `[]` |
| `GET /compat/Users/Me` | native 工作階段 | 目前使用者的 `UserDto` |
| `GET /compat/Users/{id}` | native 工作階段；自己或管理員 | `UserDto`；`{id}` 接受 32 位 hex 或帶連字號 UUID（大小寫皆可） |
| `POST /compat/Sessions/Logout` | native 工作階段 | 撤銷目前這一個工作階段，204 空主體 |

- JSON 為上游預設格式：PascalCase、null 成員省略、`application/json; charset=utf-8`。尚未支援 `profile="CamelCase"` 的 Accept 協商。
- `Version` 是相容層模擬的上游協定版本線，不是 Jelee 建置版本；客戶端依它判斷功能。是否需要調整待真實客戶端驗收確認。
- `StartupWizardCompleted` 固定 true：HTTP 伺服器沒有初始引導閘門，能連到這裡就代表可用。
- `CanSelfRestart` 依實際能力回 false（沒有重新啟動路由），不照抄上游常數。
- G11.6：不回傳 `LocalAddress`、各種資料夾路徑、`PackageName`、`SystemArchitecture`，也不回傳監聽位址、主機名稱或連接埠；測試會掃描主體與標頭中的 IPv4／IPv6／絕對路徑與設定值。

### 使用者／登入模組（G24.2）

只在帳號服務開啟（`JELEE_ENABLE_ACCOUNTS`）時掛載；否則這些路由回 404。

**登入（`POST /Users/AuthenticateByName`）**：主體 `{"Username":"…","Pw":"…"}`（成員名大小寫不敏感、未知成員忽略，與上游綁定一致），`Content-Type` 必須是 `application/json`。客戶端身分 `Client`、`Device`、`DeviceId`、`Version` 取自參數化 `Authorization`（或其為空時的舊標頭 `X-Emby-Authorization`），與驗證共用同一個解析器；登入請求上若帶有過期或外來的 token 一律忽略。

- **決策 (c)**：相容登入直接呼叫伺服器的原生登入 `LoginNative`，簽發 native 工作階段，需要帳號已由管理員開啟 `allowNative`。限速（同一張 IP／名稱限速表）、密碼驗證與虛擬驗證、失敗計數與鎖定、同時工作階段上限、審計（`login.failed`、`login.native_denied`、`session.created`）、帳號名額（密碼並發 ×4、不排隊）全部共用原生登入那一套，相容層沒有自己的帳號邏輯；換入口不會多出嘗試次數。
- **回應**：帳號不存在、密碼錯、停用、軟刪除、鎖定一律 401 空主體（無法分辨帳號是否存在）；密碼正確但未開 `allowNative` 回 403 空主體——上游對被拒帳號（停用）同樣在密碼驗證後回 403，且此判斷只在密碼正確後發生，不會向未持有密碼者洩漏設定；缺少 `Client`／`DeviceId`、欄位超長或含控制字元、主體缺 `Username`／`Pw` 或非 JSON 回 400；限速回 429 空主體與 `Retry-After`；工作階段數已滿回 429 空主體；帳號名額滿回 503 空主體與 `Retry-After: 1`；資料庫不可用回 503。
- 帶 `Origin`（層邊界）、`Sec-Fetch-Site` 或 `Sec-Fetch-Mode` 的登入一律 403，在讀密碼前拒絕，與原生登入的「拒絕瀏覽器」規則一致。token 只在主體 `AccessToken` 回傳，不設 Cookie。
- `AuthenticationResult`：`User`（同下方 UserDto）、`SessionInfo`（精簡）、`AccessToken`、`ServerId`。`SessionInfo` 只含新工作階段的 `Id`、`UserId`、`UserName`、`Client`、`DeviceName`、`DeviceId`、`ApplicationVersion`、`LastActivityDate`（建立時間，UTC 七位小數）、`ServerId`，以及上游不可為 null 的成員（`PlayableMediaTypes`／`SupportedCommands` 空陣列、`LastPlaybackCheckIn` 為最小時間、`IsActive`=true、`SupportsMediaControl`／`SupportsRemoteControl`／`HasCustomDeviceName`=false）。**不回傳 `RemoteEndPoint`**（用戶端位址）及播放狀態、佇列、能力等成員。

**UserDto**：只放 Jelee 有的資料。

| 欄位 | 值 |
| --- | --- |
| `Id` | 使用者 UUID 轉 32 位小寫 hex（`id.go`） |
| `Name` | 登入名稱（不是顯示名稱），客戶端可直接拿來重新登入 |
| `ServerId` | 同系統資訊 |
| `HasPassword`、`HasConfiguredPassword` | 固定 true：沒有密碼的帳號根本無法登入 |
| `HasConfiguredEasyPassword`、`EnableAutoLogin` | 固定 false：沒有 PIN 與自動登入 |
| `Policy.IsAdministrator`／`IsHidden`／`IsDisabled` | 帳號的 admin／hidden／disabled |
| `Policy.EnableMediaPlayback` | 帳號的 `allowNative`（只有開啟者能持有可播放的工作階段） |
| `Policy.EnableAllFolders` | 管理員為 true；一般使用者受媒體庫授權限制，為 false |
| `Policy.EnableRemoteAccess`、`EnableAllDevices` | true |
| `Policy` 其餘功能開關 | 全部 false：轉碼（音訊／影片／同步）、remux、媒體轉換、下載、刪除、直播存取與管理、遠端控制、共用裝置控制、公開分享、收藏集／字幕／歌詞管理、偏好設定存取；`SyncPlayAccess`=`None` |
| `Policy.AuthenticationProviderId`／`PasswordResetProviderId` | `Jelee.LocalPassword`／`Jelee.NoPasswordReset`（上游要求非空；客戶端只會原樣送回） |
| `Configuration` | 上游預設值的不可為 null 成員（`PlayDefaultAudioTrack`、`SubtitleMode`=`Default`、`HidePlayedInLatest`、`RememberAudioSelections`、`RememberSubtitleSelections`、`EnableNextEpisodeAutoPlay` 為 true，其餘 false，資料夾清單為空陣列）。Jelee 不保存客戶端偏好，相容層也沒有修改路由，所以是固定值 |

省略（等同上游的 null）：`ServerName`、`PrimaryImageTag`、`PrimaryImageAspectRatio`、`LastLoginDate`、`LastActivityDate`、語言偏好、資料夾／裝置／頻道清單、家長分級、存取排程、`InvalidLoginAttemptCount`、`LoginAttemptsBeforeLockout`、`MaxActiveSessions`、`RemoteClientBitrateLimit`。計數與上限屬於伺服器設定，不在相容層公開；`Locale`、`DisplayName`、`CreatedAt`、`allowNative` 原值也不回傳。

**`GET /Users/{id}`**：一般使用者只能讀自己；讀其他帳號時由帳號儲存層依即時工作階段在查詢前判定，一律 403（不論該帳號是否存在）。管理員讀不存在或已軟刪除的帳號回 404。`{id}` 格式錯誤（含全 0、括號、長度不符、`AuthenticateByName` 這類字面值）比照上游模型綁定回 400。上游允許任何已登入者讀任何使用者；這裡沿用 Jelee 的「自己或管理員」規則（普通使用者沒有全體使用者發現接口）。

**`GET /Users/Public` 回空陣列的理由**：上游用它在登入畫面列出可見帳號。在這裡回傳帳號清單，等於把登入流程刻意不確認的帳號名稱交給任何未驗證的呼叫者，與「帳號不存在／密碼錯統一 401」的原則衝突。空陣列時客戶端會改為讓使用者手動輸入名稱。此路由不查工作階段、不查資料庫。

**`POST /Sessions/Logout`**：撤銷驗證本次請求的那一個工作階段（`Accounts.Revoke`，寫 `session.revoked` 審計），同帳號其他工作階段不受影響；回 204 空主體。撤銷後舊 token 在相容層與自有 API 都是 401，進行中的直投串流依撤銷即斷流規則中止。

### 對客戶端的已知影響（G10.4）

能力宣告明確表示不轉碼、不提供 HLS／DASH、不提供 remux，`EncoderLocation`=`NotFound`。依賴伺服器轉碼的客戶端在無法直投的格式上會失敗，而不是降級；這是 G10 鐵律的預期結果。

### 契約與測試

- 黃金檔：`internal/adapter/compat/testdata/golden/`（`system_info_public.json`、`system_info.json`、`system_ping.json`、`users_authenticate_by_name.json`、`users_me.json`、`users_by_id_admin.json`），以 `go test ./internal/adapter/compat -run 'TestGoldenResponses|TestAuthenticateByName$|TestCurrentUser|TestUserByID' -update` 重產。
- OpenAPI：相容路由不屬於自有 API，在 `openapi_contract_test.go` 的 `undocumentedRoutes` 以理由豁免，不寫入 `api/openapi.json`；`leakRouteTable` 已逐條登記。
- 真 PG：`TestCompatSessionKindsPostgres` 驗證 native 可用、web（標頭、query、cookie）與已撤銷的 native 都回 401。
- 真 PG：`TestCompatUsersPostgres` 驗證未開 `allowNative` 時 403 並寫 `login.native_denied`、不簽發；密碼錯與帳號不存在回應相同；開啟後登入簽發 native 工作階段（寫 `session.created`、自有 API 可列出 client 標籤並可直投）；`/Users/Me`、`/Users/{id}` 自己／他人／管理員；`/Users/Public` 為 `[]`；Logout 只撤銷目前工作階段、舊 token 在兩邊皆 401；相容入口五次失敗後兩個入口都被鎖定且失敗審計含用戶端位址。`TestCompatLoginSharesRateLimitPostgres` 驗證相容登入與原生登入雙向共用名稱限速桶。
