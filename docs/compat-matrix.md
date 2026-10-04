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

目前有骨架、系統模組、使用者／登入模組、媒體庫瀏覽模組（只讀）、播放模組（播放資訊、原檔直投串流、外掛字幕原樣直投）、播放狀態模組與圖片模組（條目海報／背景圖與列表圖片 tag）；收藏等模組尚未提供，下表以外的路由一律回 404。尚未做真實客戶端驗收（G24.5），不能宣稱任何客戶端已可使用。

### 掛載與開關

- 設定 `JELEE_COMPAT_ENABLED=true`（設定檔 `enableCompat`）才掛載，**預設關閉**。關閉時 `/compat` 底下一律回自有 API 的 404 `not_found`，不會落到前端頁面；三類已移除功能仍回 501。
- 客戶端的伺服器網址填 `https://host/compat`。
- 伺服器 ID：`JELEE_COMPAT_SERVER_ID`（`compatServerId`，32 位小寫 hex、不可全 0）。未設定時由 `allowedHosts` 經 SHA-256 單向推導，主機清單變動 ID 就會變，客戶端會把它當成另一台伺服器；正式部署建議固定設定。

### 請求處理順序

1. 伺服器既有邊界（不因相容層改變）：Host 驗證 400 → 路徑型轉換／HLS／DASH 409 `transcode_disabled` → debug 404 → 已移除功能 501 `feature_removed`。這幾項沿用自有 API 的錯誤 envelope。
2. 相容層邊界：帶 `Origin` 標頭（任何值，包括空字串與 `null`）一律 403、空主體，且從不送出任何 `Access-Control-*` 標頭，所以 CORS 預檢也被拒絕。任何瀏覽器頁面（含同源）都無法經相容層取得原生能力。
3. 轉碼守衛：`GuardProduction` 檢查**所有**相容路由的路徑、query 與 JSON／表單主體；轉換參數回 409 `transcode_disabled`（自有 envelope，全域統一，G10.3）。例外有二：`/Items/{itemId}/PlaybackInfo` 改用 `GuardPlaybackInfo`，只把上游 PlaybackInfo 文件化的能力聲明成員（見下方「播放模組」）當資料讀，其餘所有參數（query、表單、JSON 任何層級）照 `GuardProduction` 的規則檢查；`POST /Sessions/Playing`、`/Sessions/Playing/Progress`、`/Sessions/Playing/Stopped` 三個播放回報改用 `GuardPlaybackReport`：JSON 主體是客戶端的播放狀態描述（位置、暫停、它顯示的條目、佇列、它以為的播放方式），不決定伺服器送出什麼，所以只檢查語法、深度與大小；路徑、query 與表單主體仍照 `GuardProduction`（見下方「播放狀態模組」）；`GET`／`HEAD /Items/{itemId}/Images/{imageType}[/{imageIndex}]` 改用 `GuardImage`：只把上游圖片 API 文件化的成員（`MaxWidth`、`MaxHeight`、`Width`、`Height`、`FillWidth`、`FillHeight`、`Quality`、`Format`、`Tag`、`ImageIndex`、`PercentPlayed`、`UnplayedCount`、`Blur`、`BackgroundColor`、`ForegroundLayer`）當圖片參數，其餘一切（`VideoCodec`、`MaxStreamingBitrate`、`Static=false`、`SegmentContainer`…）與路徑仍照 `GuardProduction` 回 409（見下方「圖片模組」）。`GuardProduction` 本身沒有任何放寬：串流路由、PlaybackInfo、單一條目路由與圖片路徑上的非 GET／HEAD 請求帶 `Width`／`MaxWidth` 仍是 409。
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
| `GET /compat/UserViews`、`GET /compat/Users/{userId}/Views` | native 工作階段（目錄開啟時才掛載） | 可見媒體庫的 `QueryResult`（`CollectionFolder`） |
| `GET /compat/Items`、`GET /compat/Users/{userId}/Items` | 同上 | 篩選／排序／分頁後的 `QueryResult<BaseItemDto>` |
| `GET /compat/Items/{itemId}`、`GET /compat/Users/{userId}/Items/{itemId}` | 同上 | 單一條目或媒體庫的 `BaseItemDto`；可播放條目附直投 `MediaSources` |
| `GET`、`POST /compat/Items/{itemId}/PlaybackInfo` | 同上 | `PlaybackInfoResponse`：只列客戶端可直投的來源；沒有則 `ErrorCode`=`NoCompatibleStream` |
| `GET`、`HEAD /compat/Videos/{itemId}/stream`、`…/stream.{container}` | native 工作階段（直投開啟時才掛載） | 原檔位元組（交給伺服器的直投模組，Range／HEAD／限流／撤銷斷流同自有 API） |
| `GET`、`HEAD /compat/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/Stream.{format}`、`…/{index}/{startPositionTicks}/Stream.{format}` | 同上 | 外掛字幕原檔位元組（`format` 必須是原檔格式） |
| `GET`、`HEAD /compat/Audio/{itemId}/stream`、`…/stream.{container}` | 同上 | 目錄沒有音訊條目，驗證後一律回隱藏狀態（預設 404） |
| `POST /compat/Sessions/Playing`、`…/Playing/Progress`、`…/Playing/Stopped` | native 工作階段（目錄開啟時才掛載） | 播放開始／進度／停止回報，一律 204 空主體（見「播放狀態模組」） |
| `POST /compat/Sessions/Playing/Ping?playSessionId=` | 同上 | 讓播放工作階段保持活著；204 |
| `POST`、`DELETE /compat/UserPlayedItems/{itemId}`、`/compat/Users/{userId}/PlayedItems/{itemId}` | 同上；自己或管理員 | 標記已播放／未播放，回 `UserItemDataDto` |
| `GET /compat/UserItems/{itemId}/UserData`、`/compat/Users/{userId}/Items/{itemId}/UserData` | 同上；自己或管理員 | `UserItemDataDto` |
| `GET /compat/UserItems/Resume`、`/compat/Users/{userId}/Items/Resume` | 同上；自己或管理員 | 繼續觀看的 `QueryResult<BaseItemDto>` |
| `GET`、`HEAD /compat/Items/{itemId}/Images/{imageType}`、`…/{imageType}/{imageIndex}` | native 工作階段（目錄與圖片功能都開啟時才掛載；上游允許匿名，見「圖片模組」） | 經 `/images` 同一條管線產生的 JPEG；隱藏狀態同其他條目路由 |

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

### 媒體庫瀏覽模組（G24.2，只讀）

只在目錄（`enableCatalog`）開啟且目錄服務接上瀏覽查詢時掛載；否則這些路由回 404。

**資料來源與權限**：相容層不 import postgres，只呼叫目錄服務 `app.Catalog` 的 `LibraryViews`／`Browse`／`BrowseItem`／`PlaybackSources`。儲存層每一條查詢都在同一個 SQL 裡解析即時使用者（停用、軟刪除即看不到任何東西）並套用媒體庫授權；授權判斷是與自有 API `GET /api/v1/items/{id}` 共用的同一個片段 `libraryVisibleSQL`（管理員全部可見，其他人只看 `library_acl` 授權的庫）。授權即時生效，收回後下一個請求就看不到。

**讀取身分（`{userId}`）**：路徑 `{userId}` 或 query `userId` 必須等於目前使用者，或呼叫者是管理員，否則 403 空主體（在任何目錄查詢前判定，不論該帳號是否存在）。省略或全 0 代表目前使用者（上游同樣把空 ID 視為自己）。格式錯誤 400。管理員指定其他使用者時，以該使用者的授權讀取（看到的就是對方看到的）；直投來源仍以管理員自己的工作階段查詢。

**`GET /UserViews`**：依名稱排序的可見媒體庫，`Type`=`CollectionFolder`、`IsFolder`=true。`CollectionType` 依內容推得：只有電影 `movies`、只有劇集／單集 `tvshows`、只有家庭影片 `homevideos`；混合或空庫省略（上游的 null，代表混合內容）。上限 1000 個庫。`includeExternalContent`、`presetViews`、`includeHidden` 忽略。

**`GET /Items`** 支援的參數（名稱大小寫不敏感；未列出的參數忽略，結果可能比上游多）：

| 參數 | 行為 |
| --- | --- |
| `ParentId` | 媒體庫：非遞迴為頂層條目（沒有上層連結者），遞迴為全庫；影集／季：非遞迴為直接子項，遞迴含孫項（季底下的單集）。不存在與無權限的 parent 一律回空結果（`TotalRecordCount`=0），兩者無法分辨 |
| 無 `ParentId` | 非遞迴：上游列使用者根資料夾的子項，即媒體庫資料夾（依名稱排序，可用 `SearchTerm` 過濾）；遞迴：所有可見條目 |
| `Recursive` | `true`／`false`（大小寫不敏感），其他值 400。未指定且 `ParentId` 是媒體庫並有 `IncludeItemTypes` 時，比照上游預設遞迴 |
| `IncludeItemTypes`、`ExcludeItemTypes` | `Movie`、`Series`、`Season`、`Episode`、`Video`（Jelee 的 HomeVideo）、`CollectionFolder`；其他合法名稱（`Audio`、`BoxSet`、`Folder`…）不匹配任何條目；非英數字 400 |
| `SortBy` | `SortName`／`Name`（排序標題，否則標題；不分大小寫）、`PremiereDate`、`ProductionYear`（年份事實，否則上映日期的年）；可多個，ID 為最後的穩定排序。`DateCreated`、`Random` 等 Jelee 沒有資料的鍵忽略。預設依 `SortName` 升冪 |
| `SortOrder` | `Ascending`／`Descending`，逐鍵對應；不足者沿用第一個（上游 `GetOrderBy` 規則）。升冪時無值者在前、降冪時在後 |
| `StartIndex` | ≥0，上限 1,000,000；超出結尾回空 `Items` 但保留 `TotalRecordCount` |
| `Limit` | 未指定或大於 500 一律以 500 計（上游無上限）；`0` 只回 `TotalRecordCount` |
| `SearchTerm` | 標題不分大小寫的子字串比對，`%`、`_`、`\` 按字面比對；最多 128 字元，否則 400 |
| `Fields` | `Overview`、`SortName`、`ParentId` 依上游只在要求時輸出；其他值忽略。列表不輸出 `MediaSources` |
| `Ids` | 最多 100 個；只回可見者（依要求順序，仍套用類型過濾），不存在與無權限者同樣略過 |

回應 `QueryResult`：`Items`、`TotalRecordCount`（符合條件的總數，不受分頁影響）、`StartIndex`。

**`GET /Items/{itemId}`**：比照上游單一條目回傳全部欄位：`Name`、`ServerId`、`Id`、`Type`（Movie／Series／Season／Episode／Video，或媒體庫的 `CollectionFolder`）、`IsFolder`、`ParentId`（上層影集／季，否則所在媒體庫）、`SortName`、`Overview`、`PremiereDate`（UTC 七位小數）、`ProductionYear`、`RunTimeTicks`（最佳來源的探測時長）、`UserData`、`MediaType`（`Video`／`Unknown`）、`LocationType`=`FileSystem`。不存在與無權限一律回設定的隱藏狀態（預設 404，`access.hiddenStatus=403` 時 403），空主體，兩者無法分辨。

**`MediaSources`（只限直投）**：只有可播放條目（電影、單集、家庭影片）才有，來源與自有 API 播放資訊相同（`PlaybackSources`，同樣要求 native 工作階段與授權，最佳版本在前）。`Protocol`=`File`、`Type`=`Default`、`Id`、`Container`、`Size`、`Bitrate`、`RunTimeTicks`、`DefaultAudioStreamIndex`（預設音軌，否則第一條）、`MediaStreams`（已探測的內嵌影像／音訊／字幕軌；直投開啟時另列外掛字幕，見「播放模組」）。**`SupportsTranscoding` 一律 false**；`SupportsDirectStream` 一律 false（上游客戶端以 DirectStream 要求重新封裝容器，Jelee 不做）；`SupportsDirectPlay` 等於伺服器是否開啟直投（`enableDirect`）。不輸出 `Path`、檔名與任何轉碼欄位（`TranscodingUrl`、`TranscodingContainer`、`TranscodingSubProtocol`、`TranscodeReasons`；結構體根本沒有這些欄位）。

**`UserData`** 是讀取身分（自己，或管理員指定的使用者）對該條目的真實進度：`PlaybackPositionTicks`（續播點）、`PlayCount`、`Played`、`LastPlayedDate`（有播放過才有）、`PlayedPercentage`（續播點／`RunTimeTicks`，兩者皆有才有）、`IsFavorite`=false（尚未記錄收藏）、`Key`／`ItemId`=條目 ID。列表一頁只多一次批次查詢；影集與季沒有位置，維持未播放。看不到的條目沒有 UserData（列表本來就不含它）。見[播放進度](playback-progress.md)。

**圖片欄位**：`ImageTags`、`BackdropImageTags`、`PrimaryImageAspectRatio`（`Fields` 要求時；單一條目一律），見「圖片模組」。圖片功能未開啟時 `ImageTags` 為空物件、`BackdropImageTags` 為空陣列。

**省略**（上游的 null）：`SeriesId`／`SeasonId`／`SeriesName`、`IndexNumber`／`ParentIndexNumber`、`ChildCount`、`DateCreated`、人物、類型、片商、外部 ID、評分、`Path`。列表不提供 `RunTimeTicks`。

### 播放模組（G24.2、G10.4）

PlaybackInfo 隨媒體庫模組掛載；串流、字幕與音訊路由只在直投（`enableDirect`）開啟時掛載，否則回 404。相容層不自己串流：先以呼叫者的工作階段向目錄查出條目的來源（授權在 SQL 內），再把來源 ID 交給伺服器唯一的直投模組 `media.Handler`（`ServeSource`／`ServeTrack`），所以 Range、HEAD、條件請求、`Content-Security-Policy: sandbox`、sendfile 零拷貝、並發與頻寬上限、撤銷即斷流（G07.4）都與自有 API 的 `/api/v1/sources/{id}/stream` 完全相同，也共用同一組額度。直投模組進入前會再跑一次 `GuardProduction`（縱深防禦）。

**`GET`／`POST /Items/{itemId}/PlaybackInfo`**

- 讀取身分：query `UserId`，否則主體 `UserId`；規則同瀏覽模組（自己或管理員，否則 403）。條目以該身分的授權查詢，不存在與無權限一律回隱藏狀態（預設 404、空主體）；來源以呼叫者自己的工作階段查詢。
- 請求主體（POST，可省略或空白）必須是 `application/json`，否則 415；型別錯誤 400。上游同名 query 成員優先於主體。
- **能力聲明與轉換請求分開解析**：`GuardPlaybackInfo` 把以下上游文件化的 PlaybackInfo 成員當成客戶端聲明，不當轉換參數檢查：`UserId`、`MediaSourceId`、`LiveStreamId`、`AutoOpenLiveStream`、`StartTimeTicks`、`AudioStreamIndex`、`SubtitleStreamIndex`、`MaxStreamingBitrate`、`MaxAudioChannels`、`EnableDirectPlay`、`EnableDirectStream`、`EnableTranscoding`、`AllowVideoStreamCopy`、`AllowAudioStreamCopy`、`AlwaysBurnInSubtitleWhenTranscoding`、`DeviceProfile`（整棵子樹只做語法、深度 32、64 KiB 檢查）。清單固定，`TestPlaybackInfoDeclarationsAreFixed` 鎖住。其他參數仍是轉換請求：例如 query 的 `VideoCodec`、`AudioCodec`、`SegmentContainer`、`TranscodingProtocol`、`TranscodingContainer`、`Static=false`、`Width`、`h264-profile`、`SubtitleMethod=Encode`、`Container=m3u8`，或主體頂層／任何非 DeviceProfile 巢狀處的同類成員，一律 409 `transcode_disabled`，且在查目錄之前。
- **直投判定**（只決定要不要列出來源，不改變任何位元組）：
  - 沒有 `DeviceProfile`（GET 或主體未帶）：直投開啟就列出全部來源。
  - 有 `DeviceProfile`：至少一個 `Type`=`Video`（名稱或數值 1）的 `DirectPlayProfiles` 同時接受容器、主視訊流編碼與預設音軌編碼；空清單代表不限。標記不分大小寫並接受常見別名（同自有 API：`matroska`→`mkv`、`h265`→`hevc`、`ac-3`→`ac3`…；`pcm` 涵蓋所有 PCM）。清單無法解析（超過 32 項、非法字元）的 profile 視為不接受。
  - 位元率上限：query `MaxStreamingBitrate`，否則主體 `MaxStreamingBitrate`，否則 `DeviceProfile.MaxStreamingBitrate`；來源已知位元率嚴格大於上限時不列出。
  - `EnableDirectPlay=false`（query 優先）：不列出任何來源。
  - `MediaSourceId`：只考慮該來源。
  - 伺服器不知道的值（未探測來源的編碼、未知位元率、未知容器）不據以拒絕；客戶端可以嘗試，拿到的永遠是原檔。
  - `CodecProfiles`、`ContainerProfiles` 的條件（解析度、Level、位元深度等）與 `SubtitleProfiles` **目前不評估**；`TranscodingProfiles`、`MaxAudioChannels`、串流索引、`StartTimeTicks`、`LiveStreamId` 等只關乎轉換的成員接受但忽略。
- **回應**：`MediaSources` 只含可直投的來源（`SupportsDirectPlay`=true，`SupportsTranscoding`／`SupportsDirectStream`=false，沒有任何轉碼欄位），`PlaySessionId` 為 32 位 hex 隨機值；客戶端在播放回報中帶回它，伺服器以（使用者，`PlaySessionId`）識別同一個播放工作階段並去重。**沒有可直投來源時**（條目沒有來源、不是可播放條目、客戶端聲明無法解碼、超出位元率、直投關閉或 `EnableDirectPlay=false`）回 200、`{"MediaSources":[],"ErrorCode":"NoCompatibleStream"}`，不提供任何轉碼替代；上游只在沒有來源時這樣回答，其他情況會改給轉碼網址，這裡刻意不做（G10）。

**`GET`／`HEAD /Videos/{itemId}/stream`、`/Videos/{itemId}/stream.{container}`**

- `MediaSourceId` 選來源（32 位 hex 或帶連字號），省略則最佳版本；來源不屬於該條目、條目不存在或無權限一律回隱藏狀態、空主體。格式錯誤 400。
- `Static=true`：原檔直投，上游同樣忽略的定位成員（`AudioStreamIndex`、`SubtitleStreamIndex`、`StartTimeTicks`…）一併忽略。沒有 `Static` 時上游會走編碼器，所以只允許 `MediaSourceId`、`DeviceId`、`PlaySessionId`、`Tag`、`Container`、`api_key`／`ApiKey`；多帶任何其他成員（例如 `AudioStreamIndex`、`StartTimeTicks`、`Context`）視為要求不同的串流，409。
- 轉換參數（`VideoCodec`、`AudioCodec`——含與原始相同的值與 `copy`、`MaxStreamingBitrate`、`TranscodingMaxAudioChannels`、`SegmentContainer`、`Width`、`Static=false` 或空值、`<codec>-level`…）不論有無 `Static` 都由 `GuardProduction` 回 409，連直投模組都不會進入。
- 路徑的 `.{container}` 或 query `Container` 必須等於原檔容器（接受 `matroska`、`ts`、`m2ts`、`m4v` 別名），否則是 remux 要求，409；原檔容器未知時帶容器一律 409。
- 驗證：與其他路由相同，只接受 native 工作階段；播放器常把 token 放在 `api_key`／`ApiKey` query，照樣接受，但 web 工作階段一律 401。
- 查詢目錄失敗依瀏覽模組的錯誤格式（空主體 404／403／503）；進入直投模組後的錯誤（429 並發上限、416 Range、412…）沿用自有 envelope，與 409 一致。成功回應不受相容層請求逾時限制，由直投模組的寫入期限控制。

**外掛字幕**

- 直投開啟時，`MediaSources[].MediaStreams` 列出外掛字幕：`Type`=`Subtitle`、`IsExternal`=true、`DeliveryMethod`=`External`、`SupportsExternalStream`=true、`Codec`=副檔名、`Language`、`Title`、`IsDefault`、`IsForced`、`IsHearingImpaired`（SDH）、`IsTextSubtitleStream`，`DeliveryUrl` 為上游格式 `/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/0/Stream.{副檔名}`（相對於客戶端設定的伺服器網址，**不含 token**）。`Index` 依上游接在所有內嵌流之後，按儲存層固定順序編號。內嵌字幕標 `DeliveryMethod`=`Embed`。外掛音軌不列（上游沒有投遞外掛音軌的路由）。
- `GET`／`HEAD …/Subtitles/{index}/Stream.{format}`（及含 `{startPositionTicks}` 的形式）經 `ServeTrack` 原樣直投。`format`（或上游已過時的 query `format`）必須等於原檔副檔名或同一格式的別名（`vtt`／`webvtt`、`srt`／`subrip`），否則 409，不轉換字幕格式。`startPositionTicks` 或 query `StartPositionTicks` 非 0、`EndPositionTicks`、`AddVttTimeMap=true`（皆需改寫字幕時間）409；`index` 指向內嵌字幕（需從容器抽出）409；不存在的索引回隱藏狀態。

**音訊**：`/Audio/{itemId}/stream` 與 `.{container}` 已註冊，但目錄沒有音訊條目，驗證身分後一律回隱藏狀態（預設 404）；轉換參數仍先回 409。

### 播放狀態模組（G24.2、G23.2、G48.3）

隨媒體庫模組掛載。回報交給伺服器自己的進度緩衝（`app.Progress`，詳見[播放進度](playback-progress.md)）：不是每次回報寫一次資料庫，而是同一工作階段在兩次 flush 之間只保留最新狀態，每個 flush 間隔一個批次語句寫完。

- **`POST /Sessions/Playing`**（`PlaybackStartInfo`）、**`/Sessions/Playing/Progress`**（`PlaybackProgressInfo`）、**`/Sessions/Playing/Stopped`**（`PlaybackStopInfo`）：讀 `ItemId`、`MediaSourceId`（只在開始時、且是 ID 形態才用來選版本）、`PositionTicks`、`IsPaused`、`PlaySessionId`、`Failed`（停止時）；其他成員（串流索引、音量、佇列、`Item`、`PlayMethod`、`MaxStreamingBitrate`…）忽略，投遞方式一律記為 direct。工作階段鍵是（使用者，`PlaySessionId`）；沒帶或格式不合時改用（目前 native 工作階段，`ItemId`）。重複的開始回報併入同一工作階段，重複的停止不再寫入。主體必須是 `application/json`（否則 415），`ItemId` 格式錯誤 400。**看不到的條目、不存在的條目、已結束的工作階段一律回 204 且不記錄**，所以這三條路由無法用來探測條目是否存在。緩衝已滿（`playback.maxSessions`）或資料庫不可用回 503。
- **`POST /Sessions/Playing/Ping?playSessionId=`**：更新最後回報時間，避免逾時關閉；缺 `playSessionId` 400，未知工作階段 204 不動作。
- **`POST`／`DELETE /UserPlayedItems/{itemId}`**（及舊式 `/Users/{userId}/PlayedItems/{itemId}`）：比照上游：標記已播放＝播放次數 +1、清除續播點、`LastPlayedDate`＝`datePlayed`（ISO 8601 或舊式 `yyyyMMddHHmmss`，UTC）或現在；標記未播放＝播放次數歸零、清除續播點。`userId` 規則同媒體庫模組（自己，或管理員代他人）。看不到或不存在回隱藏狀態。
- **`GET /UserItems/{itemId}/UserData`**（及 `/Users/{userId}/Items/{itemId}/UserData`）：單一條目的 `UserItemDataDto`；看不到回隱藏狀態。
- **`GET /UserItems/Resume`**（及 `/Users/{userId}/Items/Resume`）：有續播點且未播完的電影、劇集、家庭影片，最近播放的在前；支援 `StartIndex`、`Limit`（上限 500）、`IncludeItemTypes`／`ExcludeItemTypes`、`Fields`；`ParentId`、`MediaTypes`、`SearchTerm` 等其他篩選目前忽略。`RunTimeTicks` 取該使用者最近一次播放版本的時長（未探測則省略），`UserData.PlayedPercentage` 據此計算。**收回媒體庫授權後該條目立刻不再出現**（SQL 內套用授權，G48.3），恢復授權後續播點仍在。
- 播放中的進度最多延遲一個 flush 間隔才寫入資料庫；同一實例上的 `UserData` 讀取會疊加尚未 flush 的位置（只疊加到 SQL 判定可見的條目上），繼續觀看清單則在下次 flush 後更新。

### 圖片模組（G24.2、G40.8、G48.3）

隨媒體庫模組掛載，且需要圖片功能（`JELEE_ENABLE_IMAGES`）；否則圖片路由回 404、所有條目的圖片 tag 為空。相容層不自己解碼、縮放或快取：解析參數後交給伺服器的圖片管線（`app.Images.Get`，與 `GET /images/{type}/{id}` 共用），處理上限、准入槽（滿載 503＋`Retry-After`）、逾時、處理前後兩次權限查驗、ETag、`If-None-Match` 304 都與 `/images` 相同，詳見[本地圖片](local-images.md#相容層圖片路由g242g408)。

**`GET`／`HEAD /Items/{itemId}/Images/{imageType}`、`/Items/{itemId}/Images/{imageType}/{imageIndex}`**

- `imageType`：上游 `ImageType` 名稱，大小寫不敏感。`Primary`、`Backdrop`、`Banner`、`Disc`、`Box`、`BoxRear`、`Menu`、`Chapter`、`Profile` 直接對應同名的 Jelee 圖片槽；`Logo`、`Art`、`Thumb` 先找同名槽，沒有可用圖片再找 `ClearLogo`、`ClearArt`、`Landscape`（上游把這三種存成前者，Jelee 依 G40.1 分開存）。`Screenshot` 沒有對應，視同沒有該圖。未知名稱（含 Jelee 的 `Fanart` 別名、數字）400。
- `imageIndex`：路徑或 query `ImageIndex`，非負十進位整數，否則 400；只有 `Backdrop`／`Chapter` 有非 0 的槽，其他類型帶非 0 或超過 9999 視同沒有該圖。
- 參數（名稱大小寫不敏感）：`Width`、`MaxWidth`、`FillWidth` 取其中最小的正值為寬度上限（高度同理），保持比例、不放大、不裁切，超過 2048 降為 2048，之後仍受設定的輸出上限（預設 1024）約束；0 或空值視同未給，負數或非整數 400。`Quality` 0 為預設、超過 100 以 100 計。`Format` 必須是上游 `ImageFormat`（`Bmp`、`Gif`、`Jpg`、`Png`、`Webp`、`Svg`）否則 400，但輸出一律 JPEG，以 `Content-Type` 為準。`PercentPlayed`、`UnplayedCount`、`Blur`、`BackgroundColor`、`ForegroundLayer` 只檢查數值格式後忽略（不疊加播放進度、不模糊）。`Tag` 為 1–128 位 hex 時交給管線比對；其他形式忽略。未列出的參數比照上游忽略，但轉換參數在邊界就是 409。
- 回應：與 `/images` 相同的 `image/jpeg`、`Content-Length`、強 `ETag`、`Accept-Ranges: none`；`Tag` 等於原圖內容 SHA-256（即列表給出的 tag）時 `Cache-Control: private, max-age=31536000, immutable`，否則 `private, no-cache, must-revalidate`。上游為 `public`；這裡因為圖片需要驗證，一律 `private`，共用快取不得保存。`Vary` 列出相容層讀取憑證的四個標頭（`Authorization` 與三個舊標頭）；`api_key` 在 URL 裡，本身就是快取鍵的一部分。
- 錯誤：條目不存在、看不到、或可見但沒有該圖片，一律回隱藏狀態（預設 404，設定 403 時 403），空主體且標頭相同；管線無法使用的圖片 404、超過處理預算 413、格式不支援 415、滿載 503＋`Retry-After: 1`、資料庫或逾時 503。
- **驗證的取捨**：上游 `GetItemImage` 不要求登入，任何知道條目 ID 的人都能取圖。Jelee 要求 native 工作階段（標頭或 `api_key`／`ApiKey` query 皆可），並以呼叫者自己的工作階段與媒體庫授權讀取。理由：G48.3 要求不可見條目的圖片不得外洩，而條目 ID 會出現在日誌、分享連結與客戶端快取裡，不能當成存取憑證；海報本身也可能透露受限媒體庫的內容。代價：用一般圖片載入器、不帶驗證標頭也不在 URL 加 `api_key` 的客戶端會拿到 401、顯示不出圖。這需要真實客戶端驗證；若確實常見，後續可考慮短效簽章圖片 URL，而不是放寬成匿名。

**列表的圖片 tag**

- `ImageTags`：每個上游類型（`Backdrop`、`Chapter` 除外）若有可用圖片，給一個 tag；`Logo`／`Art`／`Thumb` 依上面的回退順序取。`BackdropImageTags`：從 index 0 起連續存在的背景圖（遇到缺號即停，上限 32 張），客戶端以位置當 index，與 Jelee 的 index 一致。只列 Jelee 的 G40.1 類型中上游也有名稱者，不會出現上游客戶端無法解析的鍵。
- tag 取自 G40.10 選出的那一列：已讀取內容時是原圖 SHA-256（64 位小寫 hex，也是 `Tag` 長快取的條件）；尚未讀取內容的本地檔，是該列 ID、更新時間與檔案大小／修改時間的摘要。兩者都不含路徑或 URL，內容或來源一變 tag 就變。
- `PrimaryImageAspectRatio`：`Fields` 含 `PrimaryImageAspectRatio` 時（單一條目一律），以 Primary 圖已知的寬高計算；寬高未知時省略。
- `EnableImages=false` 時省略 `ImageTags`；`ImageTypeLimit`（每類型張數，0 代表都不要）與 `EnableImageTypes`（限定類型）比照上游 `DtoOptions.GetImageLimit`，背景圖張數為 0 時省略 `BackdropImageTags`。格式錯誤 400。
- **不會 N+1**：每次列表（`/Items`、`Ids`、`/UserItems/Resume`、單一條目）只對當頁的條目做一次批次讀取 `ItemImageSummaries`：一條 SQL 以 `DISTINCT ON` 選出每個槽的勝出列，授權（讀取身分的媒體庫授權、停用帳號）在同一條 SQL 內。媒體庫資料夾不查。
- **限制**：tag 只來自 `item_images` 資料表。目前掃描尚未把同目錄海報寫入 `item_images`（見 [本地圖片](local-images.md#尚未涵蓋)），只靠「媒體檔旁海報」回退的條目不會有 `Primary` tag，客戶端因此不會去要圖；直接要求 `Primary` 仍會經回退取得。

### 對客戶端的已知影響（G10.4）

能力宣告明確表示不轉碼、不提供 HLS／DASH、不提供 remux，`EncoderLocation`=`NotFound`。依賴伺服器轉碼的客戶端在無法直投的格式上會失敗，而不是降級；這是 G10 鐵律的預期結果。

### 契約與測試

- 黃金檔：`internal/adapter/compat/testdata/golden/`（`system_info_public.json`、`system_info.json`、`system_ping.json`、`users_authenticate_by_name.json`、`users_me.json`、`users_by_id_admin.json`、`library_user_views.json`、`library_user_views_admin.json`、`library_items.json`、`library_items_root.json`、`library_item_detail.json`、`library_item_folder.json`、`playback_info.json`、`playback_info_no_compatible.json`、`library_items_images.json`、`library_item_detail_images.json`），以 `go test ./internal/adapter/compat -run 'TestGoldenResponses|TestAuthenticateByName$|TestCurrentUser|TestUserByID|TestUserViews|TestItems|TestItemByID|TestItemImageTags|TestPlaybackInfoAcceptsCapabilityDeclarations|TestPlaybackInfoRefusals' -update` 重產。`playback_info.json` 同時是「帶真實客戶端形狀的 DeviceProfile（含 TranscodingProfiles、CodecProfiles、編碼清單）的 POST」、「GET」與「空主體 POST」三種請求的預期回應。
- 播放模組單元測試（`playback_test.go`，用真的 `media.Handler` 與暫存檔）：能力聲明不觸發 409、而同一聲明在串流路由與 `GuardProduction` 上仍是 409；PlaybackInfo 上真正的轉換參數（query、主體頂層、巢狀）409 且不查目錄；直投判定逐項（容器、編碼、別名、數值型別、位元率三個來源與優先順序、`EnableDirectPlay`、`MediaSourceId`、未探測來源）；`Static` 直投 SHA-256 一致、`api_key`、Range 206 精確位元組、HEAD；串流與字幕的每一種轉換要求 409 且沒進直投模組；字幕格式／時間位移／內嵌字幕 409；音訊 404；沒有直投模組時路由不存在。`internal/adapter/media` 的 `TestPlaybackInfoGuardSeparatesDeclarations` 對 `transformParams` 每一項（不在聲明清單者）逐一驗證 query、主體頂層、巢狀與宣告成員底下都仍被拒。
- 圖片模組單元測試（`images_test.go`）：參數解析（最小上限、2048 上限、0 視同未給、品質範圍、`Format` 名稱、`Tag` 形式、效果參數語法）；經管線的請求內容與 actor、大小寫、HEAD、`api_key`、`Logo`→`ClearLogo` 回退、最終錯誤不再嘗試下一槽、各錯誤對應；看不到、不存在、不可能存在的槽三者回應與標頭相同（404 與 403 兩種設定）；**守衛分離**：圖片路由的尺寸參數不觸發 409，但 `VideoCodec`、`MaxStreamingBitrate`、`SegmentContainer`、`Static=false`、`<codec>-level`… 仍 409，而影片／音訊串流、PlaybackInfo、單一條目與圖片路徑的 POST 帶 `Width`／`MaxWidth`／`MaxHeight` 仍 409；列表 tag 的黃金檔、每次列表一次批次讀取、`EnableImages`／`ImageTypeLimit`／`EnableImageTypes`、資料夾不查、讀取身分。`internal/adapter/media` 的 `TestImageGuardReadsOnlyImageMembers` 對每個圖片成員的各種拼法驗證只在 `GuardImage` 放行、`GuardProduction` 仍拒絕尺寸成員，並對 `transformParams` 其餘每一項驗證 `GuardImage` 仍拒絕。
- 真 PG：`TestCompatImagesPostgres` 以真實 `item_images` 與授權驗證：五部電影的列表只執行一條讀 `item_images` 的 SQL（以 pgx tracer 計數）、各自的 `Primary` tag 正確、本地列勝過遠端列、背景圖與 `ClearLogo` 回退、長寬比；B 的系列圖片 tag 不出現在 A 的任何回應；有權限可取圖且尺寸參數送到管線、HEAD＋`api_key`、304；無權限、不存在、可見但無此圖三者回應相同（404 與 403 設定）；匿名與 web 工作階段 401；串流帶 `MaxWidth` 仍 409、圖片帶 `VideoCodec` 409；更新原圖內容後列表 tag 變、ETag 變、舊 ETag 不再 304、舊 tag 失去長快取；收回授權後圖片與 tag 立即消失。`internal/adapter/postgres` 的 `TestItemImageSummariesSelectAndAuthorize` 驗證選列順序（鎖定 > local > NFO > remote、未讀內容的本地檔可用、無內容的 URL 不可用）、`Chapter` 與超過上限的背景圖不列、未授權與停用帳號看不到、輸入檢查。
- OpenAPI：相容路由不屬於自有 API，在 `openapi_contract_test.go` 的 `undocumentedRoutes` 以理由豁免，不寫入 `api/openapi.json`；`leakRouteTable` 已逐條登記。
- 真 PG：`TestCompatSessionKindsPostgres` 驗證 native 可用、web（標頭、query、cookie）與已撤銷的 native 都回 401。
- 真 PG：`TestCompatUsersPostgres` 驗證未開 `allowNative` 時 403 並寫 `login.native_denied`、不簽發；密碼錯與帳號不存在回應相同；開啟後登入簽發 native 工作階段（寫 `session.created`、自有 API 可列出 client 標籤並可直投）；`/Users/Me`、`/Users/{id}` 自己／他人／管理員；`/Users/Public` 為 `[]`；Logout 只撤銷目前工作階段、舊 token 在兩邊皆 401；相容入口五次失敗後兩個入口都被鎖定且失敗審計含用戶端位址。`TestCompatLoginSharesRateLimitPostgres` 驗證相容登入與原生登入雙向共用名稱限速桶。
- 真 PG：`TestCompatLibraryPostgres` 用兩個使用者、兩個不同授權的媒體庫驗證：各自只看到自己的庫；以 parent、Ids、類型、搜尋或單筆查詢都碰不到對方的庫與條目（不存在與無權限回應相同）；分頁串接等於完整排序、超出結尾保留總數；名稱／年份／上映日期排序與空值位置；搜尋萬用字元按字面；影集／季的子項與遞迴；詳情的直投來源（`SupportsTranscoding`／`SupportsDirectStream` 為 false、不含路徑）；收回授權立即生效；設定 403 時隱藏與不存在皆 403。
- 存取外洩：`leakRouteTable` 已登記六條媒體庫路由、播放模組全部 16 條路由（PlaybackInfo、影片串流、字幕以 ID 查詢模式；音訊以無媒體模式）與圖片模組 4 條路由（ID 查詢模式，含管理員對照），三種隱藏狀態都跑，外洩標記同時比對帶連字號與相容層 32 位 hex 兩種 ID 形態。
- 播放狀態：`internal/adapter/media` 的 `TestPlaybackReportGuardReadsBodyAsState` 鎖住回報主體只做語法檢查、而路徑／query／表單與 `GuardProduction` 不變；真 PG `TestProgressHTTPPostgres` 以真實客戶端形狀的主體（含 `MaxStreamingBitrate`、`PlayMethod`、`NowPlayingQueue`）走開始 → 進度 → Ping → `Items/{id}` 的 UserData（含未 flush 的位置）→ 停止 → `UserItems/Resume`、舊式 Resume 與 `Items` 列表的續播點 → 看不到的條目回報 204 但不記錄、UserData 與標記回隱藏狀態 → 標記已播放／未播放；存取外洩表登記全部 12 條播放狀態路由（回報以理由豁免，其餘以 ID 查詢與列表模式跑三種隱藏狀態，續播清單的固定資料同時含看得到與看不到的條目）。
- 真 PG：`TestCompatPlaybackPostgres` 走完整流程：相容登入 → 瀏覽 → 帶 DeviceProfile 的 PlaybackInfo（只列直投、無轉碼欄位、外掛字幕 `DeliveryUrl`）→ 不可直投回 `NoCompatibleStream` → 串流 4 MiB 原檔 SHA-256 一致（標頭與 `api_key` 兩種憑證）、Range、HEAD → 字幕原樣 → 各種轉換要求 409 `transcode_disabled` → web 工作階段（標頭與 `api_key`）401 → 真 TCP 上限速播放中登出，串流在數秒內被切斷且未送完、舊 token 401、另一個工作階段不受影響。

### 需要真實客戶端驗證（G24.5）

以下尚未用真實客戶端驗證，不能宣稱可用：Findroid、Swiftfin、Infuse、官方 Web 與 Android 客戶端能否在 `/compat` 登入後起播、Seek（Range）是否順暢、外掛字幕能否載入（`DeliveryUrl` 不含 token，若客戶端取字幕不帶驗證標頭會 401）、各客戶端送的 DeviceProfile 是否被判為可直投（未評估 CodecProfiles 可能造成「判可直投但客戶端解不了」）、`NoCompatibleStream` 的錯誤呈現、以及 `MaxStreamingBitrate` 低於來源位元率時的行為。官方 Web 在瀏覽器中執行，請求會帶 `Origin`，依層邊界規則一律 403，預期無法使用。

圖片同樣尚未用真實客戶端驗證：各客戶端取圖時是否帶驗證標頭或 `api_key`（不帶就會 401、海報空白，這是要求驗證的主要風險）；是否接受 64 位 hex 的 tag 與 JPEG 回應（即使要求 `Format=Webp`）；`Logo`／`Thumb` 回退後的顯示；以位置取背景圖是否正確；`PrimaryImageAspectRatio` 缺少時的版面；`private` 長快取在客戶端的實際命中；以及只有「媒體檔旁海報」、尚未寫入 `item_images` 的條目在客戶端沒有海報的情況。

播放狀態同樣尚未用真實客戶端驗證：各客戶端是否帶 `PlaySessionId`／`ItemId`、回報頻率、停止後客戶端畫面上的續播點與「已播放」是否立即更新、「繼續觀看」列是否出現且進度條正確（`PlayedPercentage` 依賴已探測的時長）、斷線後重連是否接回同一工作階段、Seek 後的進度是否正確。
