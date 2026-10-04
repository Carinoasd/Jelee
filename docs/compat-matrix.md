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

目前只有骨架與系統模組；媒體庫、Items、播放資訊等模組尚未提供，下表以外的路由一律回 404。尚未做真實客戶端驗收（G24.5），不能宣稱任何客戶端已可使用。

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

- JSON 為上游預設格式：PascalCase、null 成員省略、`application/json; charset=utf-8`。尚未支援 `profile="CamelCase"` 的 Accept 協商。
- `Version` 是相容層模擬的上游協定版本線，不是 Jelee 建置版本；客戶端依它判斷功能。是否需要調整待真實客戶端驗收確認。
- `StartupWizardCompleted` 固定 true：HTTP 伺服器沒有初始引導閘門，能連到這裡就代表可用。
- `CanSelfRestart` 依實際能力回 false（沒有重新啟動路由），不照抄上游常數。
- G11.6：不回傳 `LocalAddress`、各種資料夾路徑、`PackageName`、`SystemArchitecture`，也不回傳監聽位址、主機名稱或連接埠；測試會掃描主體與標頭中的 IPv4／IPv6／絕對路徑與設定值。

### 對客戶端的已知影響（G10.4）

能力宣告明確表示不轉碼、不提供 HLS／DASH、不提供 remux，`EncoderLocation`=`NotFound`。依賴伺服器轉碼的客戶端在無法直投的格式上會失敗，而不是降級；這是 G10 鐵律的預期結果。

### 契約與測試

- 黃金檔：`internal/adapter/compat/testdata/golden/`（`system_info_public.json`、`system_info.json`、`system_ping.json`），以 `go test ./internal/adapter/compat -run TestGoldenResponses -update` 重產。
- OpenAPI：相容路由不屬於自有 API，在 `openapi_contract_test.go` 的 `undocumentedRoutes` 以理由豁免，不寫入 `api/openapi.json`；`leakRouteTable` 已逐條登記。
- 真 PG：`TestCompatSessionKindsPostgres` 驗證 native 可用、web（標頭、query、cookie）與已撤銷的 native 都回 401。
