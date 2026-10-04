# 存取控制與統一權限過濾器（G48）

本文說明 Jelee 如何決定「某個使用者看得到哪些條目」：授權模型、規則優先順序與合併策略、網路限制（G48.5）、分享連結與訪客（G48.6）、統一過濾器的位置與守門、管理 API、效能證據，以及尚未實作的後續項目。

- 統一述詞：`internal/adapter/postgres/visibility.go`（唯一來源）。
- 守門測試：`internal/adapter/postgres/visibility_guard_test.go`。
- 規則管理：`internal/adapter/postgres/content_access.go`、`internal/app/content_access.go`、`internal/adapter/http/content_access.go`；契約 `internal/domain/content_access.go`。
- 網路限制與分享：`internal/adapter/postgres/network_rules.go`、`shares.go`、`internal/app/shares.go`、`internal/adapter/http/shares.go`（含訪客閘門）；契約 `internal/domain/share.go`。
- 遷移：`000069_content_access`、`000079_share_network_access`（`library_network_rules`、`share_links`、`users.share_id`、`client_rules.libraries`）。
- 驗收測試：`content_access_test.go`（權限矩陣、管理 API、遷移 up/down/up）、`content_access_plan_test.go`（EXPLAIN 與單請求 SQL 次數）、`share_access_test.go`／`share_access_plan_test.go`／`share_http_test.go`（網路與分享的矩陣、EXPLAIN、SQL 次數、撤銷斷流、遷移）、`internal/adapter/http/access_leak_test.go`（全路由遍歷）。

## 授權模型

| 層 | 資料 | 誰設定 | 作用 |
| --- | --- | --- | --- |
| 媒體庫授權 | `library_acl`（使用者 × 庫） | 管理員：`PUT /api/v1/users/{id}/libraries` | 外圈邊界。管理員看得到全部庫，一般使用者只看得到被授權的庫 |
| 條目規則 | `user_item_access_rules`（使用者 × 條目，`allow`／`hide`） | 管理員：`PUT/DELETE /api/v1/users/{id}/content-access/items/{itemId}` | 涵蓋該條目與其子樹（劇集 → 季 → 集，經 `item_parent_links`） |
| 標籤／類型封鎖 | `user_blocked_tags` | 管理員：`PUT /api/v1/users/{id}/content-access` | 條目或其祖先的 `tags`、`genres` 含被封鎖的字詞即隱藏（去頭尾空白、不分大小寫） |
| 分級上限 | `users.parental_rating_max`（0–21，代表最低年齡；NULL＝無上限） | 同上 | 條目與其祖先中「可辨識分級」的最高者超過上限即隱藏 |
| 未分級策略 | `users.block_unrated`（NULL＝跟隨全域）、`access_policy.block_unrated` | 同上；全域：`PUT /api/v1/access/policy` | 只在有上限時生效：條目與祖先都沒有可辨識分級時，是否隱藏 |
| 管理員是否受限 | `access_policy.restrict_admins`（預設 `false`） | `PUT /api/v1/access/policy` | 開啟後，管理員也套用自己的條目規則、標籤封鎖與分級上限；媒體庫授權永遠不限制管理員 |
| 網路限制（G48.5） | `library_network_rules`（媒體庫 × 條件） | 管理員：`/api/v1/access/network-rules` | 每請求的外圈：某庫有啟用中的規則時，只有符合其中至少一條的請求看得到該庫 |
| 客戶端管控庫限制（G47） | `client_rules`（`restrict_libraries`＋`libraries`） | 管理員：`/api/v1/client-control/rules` | 每請求的外圈：閘門判定後，本請求只看得到列出的庫（多條取交集） |
| 分享範圍（G48.6） | `share_links`＋訪客帳號 `users.share_id` | 管理員：`/api/v1/shares` | 訪客的「媒體庫授權」：只有分享的整庫，或分享條目與其子孫 |

### 分級資料來源

分級取自條目 metadata 的 `mpaa` 與 `certification` 兩個欄位（`item_metadata_fields`，NFO 的 `<mpaa>`、`<certification>`、`officialrating` 都匯入這裡；手動編輯亦同）。沒有獨立的分級欄位，因此不需新增可為 NULL 的欄位；沒有這兩個欄位的條目即為「未分級」。

比對前先正規化：去頭尾空白、轉大寫、去掉開頭的 `Rated `、去掉兩個字母的國碼前綴（`US:`、`DE: `、全形冒號亦可）。正規化後：

1. 在 `parental_ratings` 表查代碼（遷移內建：美國電影 G／PG／PG-13／R／NC-17、美國電視 TV-Y～TV-MA、英國 U／UC／12A／R18、日本 PG12／R15+／R18+、德國 FSK 0～18、台灣分級繁簡體）。`GET /api/v1/access/parental-ratings` 列出全部代碼與等級。
2. 查不到時，純年齡 `16` 或 `16+` 視為該年齡（上限 21）。
3. 其他值（如 `NR`、`Not Rated`、未知國家代碼）視為未分級。

條目的有效分級＝條目本身、父項、祖父項中可辨識分級的**最大值**。例如劇集為 TV-MA、某集自己標 TV-PG，該集仍以 TV-MA（17）判定——父項被隱藏時子項不會單獨露出。

## 優先順序與合併策略

對使用者 u、本請求 r 與條目 i，依序判定（`visibility.go` 的 `itemVisibleSQL` 即為下列條件的 SQL 版）：

0. **請求限制**（G48.5、G47，作用於任何人，包括訪客；本節之後的規則都不能擴大它）：
   - 網路限制：i 的庫有啟用中的網路規則，而 r 不符合其中任何一條 → 隱藏。管理員只受 `includeAdmins` 的規則限制（管理員的「有效規則」為空時視為不限制）。
   - 客戶端管控 `restrict_libraries`：閘門對 r 判定出庫集合時，i 的庫不在集合內 → 隱藏。此動作是否適用由 G47 自己的優先序、`allow` 與豁免（管理員、環回）決定；它與網路限制同時成立（交集），G47 的 `allow` 只會停住較低優先序的 G47 規則，**不會**放寬網路限制。開發者模式不放寬兩者。
1. **媒體庫授權**：u 不是管理員且 i 的庫不在 u 的授權中 → 隱藏；u 是分享訪客時，授權就是分享範圍（見下）。後面任何規則（包括 `allow`）都不能擴大此邊界。
2. **快速通過**：u 沒有任何限制（`users.content_filtered=false`），或 u 是管理員且 `restrict_admins=false` → 可見。
3. **最近的條目規則**：在 i、父項、祖父項中找 u 的規則，距離最近者決定：`hide` → 隱藏；`allow` → 可見（略過 4、5）。同一條目每個使用者只有一條規則（主鍵），沒有同層衝突。
4. **標籤／類型封鎖**：i 或祖先的 `tags`、`genres` 任一值命中 u 的封鎖 → 隱藏。
5. **分級**：u 有上限時，有效分級 > 上限 → 隱藏；沒有可辨識分級時，`COALESCE(u.block_unrated, access_policy.block_unrated)` 為真 → 隱藏。
6. 其餘 → 可見。

合併要點：

- 規則只會**縮小**媒體庫授權給的範圍；`allow` 是對 4、5 與祖先 `hide` 的例外，不是授權。
- 「最近者勝」讓「隱藏整部劇但允許第一季」成立：對劇集設 `hide`、對第一季設 `allow`，第一季與其各集可見、劇集本身不可見（第一季仍可從搜尋、直接 ID、繼續觀看到達；瀏覽劇集的子項時因父項不可見而回空）。
- 反過來「允許整部劇但隱藏某一集」：劇集 `allow`、該集 `hide`，只有該集隱藏。
- 不同使用者的規則互不影響；矩陣測試以第二個使用者驗證。
- 管理員的規則預設不生效（管理員看得到一切，用於管理）；開 `restrict_admins` 後才套用。這是可設定的建議預設。
- 網路限制與客戶端管控庫限制是「每請求」的條件，同一使用者在區網內外看到的不同；它們先於一切個人規則，`allow` 條目規則與管理員身分（除非規則未 `includeAdmins`）都不能越過。
- 開發者模式（G48.9）：開發者開關 `relax_permission_strict` 生效期間暫停 `restrict_admins`，管理員回到看得到一切；非管理員的授權與規則完全不變，所以不會多看到任何內容。開關在 `devPermissionRelaxedSQL`（同一個 `visibility.go`）以 `dev_mode_state` 的到期時間判斷，工作階段結束即恢復。見 [開發者模式](developer-mode.md)。

## 網路限制（G48.5）

規則：`{libraryId, network: any|lan|wan, cidrs[], clientKinds[], includeAdmins, enabled, note}`。

- 某庫有啟用中的規則時，請求至少要符合其中一條（規則之間是「或」）；一條規則設定的條件都要成立（條件之間是「且」），沒設定的條件不限制。
- `lan`：客戶端位址是私有位址（RFC 1918）、唯一本地位址（ULA，`fc00::/7`）或環回；`wan`：其他位址。連結本地（`169.254.0.0/16`、`fe80::/10`）不算區網。IPv4 對映的 IPv6 位址以 IPv4 判斷。
- 位址一律是**依可信代理設定算出的客戶端位址**（`internal/adapter/http/proxies.go`，與 G47 相同），不直接讀轉送標頭；放在反向代理之後時務必設定可信代理，否則所有請求都是代理的位址（多半是區網或環回）。
- `cidrs`：位址或前綴（單一位址存成 /32 或 /128，前綴存成遮罩後的形式，`::ffff:a.b.c.d/n` 存成 IPv4 前綴），最多 64 個。
- `clientKinds`：`web`／`native`，伺服器發出工作階段時決定的種類，不可偽造。**裝置類型**：G47 的 `device_type` 維度目前沒有任何來源（客戶端不回報、伺服器也不推斷），因此以工作階段種類代替；要依應用或裝置分流時，用 G47 規則（`app_name`、`device_id` 等）配 `restrict_libraries`。
- `includeAdmins`：預設 `false`，管理員不受該規則限制（與內容規則的「管理員預設不受限」一致，避免管理員在外網被鎖出管理用的瀏覽）；設為 `true` 才連管理員也限制。管理 API 本身不經統一過濾器，所以規則永遠不會讓管理員無法管理規則。
- 沒有請求的語句（背景工作、CLI）沒有網路屬性，條件全為未知：有條件的規則一律不符合（fail closed）。
- 規則上限 1,000 條；增刪改寫稽核 `access.network_rule_created`／`updated`／`deleted`，下一個請求即生效（沒有快取）。

### 下推方式

HTTP 層在驗證後把本請求的網路屬性與 G47 的庫集合掛在 principal（`access.RequestScope`）。儲存層每條使用統一述詞的語句多帶**一個** jsonb 參數（`requestScopeArg`：`{"ip","lan","kind","libraries"}`），述詞中的 `requestLibrarySQL` 以這個參數算出「本請求被隱藏的庫」——這是與資料列無關的子查詢，PostgreSQL 每條語句只算一次（InitPlan），每列只比較自己的庫 ID。沒有先查後濾，也不增加語句數。列表（`listItemsSQL`）更進一步：被隱藏的庫在讀取它的分頁之前就被剔除，不會讀到任何條目列。

## 分享連結與訪客（G48.6）

採「分享連結」：不需帳號，持連結者以受限訪客身分存取。可登入的「訪客使用者帳號」本次不另做：一般帳號＋媒體庫授權＋內容規則＋並發覆寫已能表達「受限帳號」，缺的只有到期時間，列為後續。

### 模型

- 只有**管理員**能建立分享（避免一般使用者把自己的授權轉給他人，也不需要「分享範圍 ≤ 建立者可見範圍」的額外規則）。
- 範圍：整個媒體庫（`libraryId`），或一個條目與其子孫（`itemId`，劇集 → 季 → 集）。建立時記錄條目所屬的庫（複合外鍵保證一致）。
- 到期（5 分鐘～90 天）、唯讀、是否允許播放、並發上限（1–16）、備註。最多 1,000 個有效分享。
- 權杖 32 位元組隨機值，只在建立回應出現一次；資料庫只存加了領域分隔字串的 SHA-256。網頁連結是 `/share#<token>`，權杖在 fragment，不會送到伺服器或進入日誌。
- 每個分享有一個隱藏的**訪客帳號**（`users.share_id`，無密碼、`hidden`、不得為管理員〔CHECK〕、不出現在使用者列表、無法以名稱登入或重設密碼）。所有訪客工作階段都是這個帳號的工作階段，所以分享的權限、進度、並發都以帳號為單位，而統一述詞從 `users.share_id` 讀出範圍：
  - 授權：分享未撤銷且未到期時，整庫分享授權該庫；條目分享只授權該條目與其子孫（`shareItemSQL`），且不列出媒體庫本身。
  - 訪客帳號上仍可設內容規則，只會再縮小；網路限制與 G47 庫限制照常適用。
- 兌換：`POST /api/v1/shares/redeem`（網頁工作階段，設 `__Host-` Cookie 並回 CSRF，與登入相同）、`POST /api/v1/shares/redeem/native`（原生工作階段，權杖只在回應本文，拒絕帶瀏覽器標頭的請求）。共用登入限速；每個分享的有效工作階段數受每使用者工作階段上限限制；工作階段到期時間不超過分享到期時間。不存在、已撤銷、已過期的權杖一律 404 `share_unavailable`。

### 訪客能做什麼（直投與播放）

| | 網頁訪客 | 原生訪客（分享 `allowPlayback`） |
| --- | --- | --- |
| 瀏覽、搜尋、詳情、圖片、檔案資訊 | 可，限分享範圍 | 可，限分享範圍 |
| 播放 | **不可**（網頁永不播放） | 只能**直投原檔**（既有 `/api/v1/sources/{id}/stream` 與外掛字幕／音軌），沒有任何轉碼 |
| 進度回報、標記已看 | 唯讀分享拒絕（403 `share_read_only`）；否則寫入訪客帳號 | 同左 |
| 其他路由（帳號、管理、統計、相容層…） | 403 `share_forbidden` | 同左；相容層把訪客權杖當成未知權杖（401） |

- 訪客閘門（`internal/adapter/http/shares.go` 的 `guestRoutes`）以「方法＋路由樣式」白名單在處理器執行前判定，白名單以外一律拒絕；未允許播放的分享兌換原生工作階段回 403 `share_playback_disabled`，`Resolve` 另以 `sharePlaybackSQL` 在儲存層再擋一次。
- 並發上限：分享的上限隨 `Resolve` 回到直投限制器，**不受**全站「同時播放數」開關與開發者模式放寬影響（它是分享授權的一部分）；比使用者既有上限更嚴時才生效。
- 撤銷：`POST /api/v1/shares/{id}/revoke` 在同一交易撤銷分享與訪客的所有工作階段；進行中的直投在下一次工作階段檢查（`streaming.revokeCheckSeconds`）時斷流，沿用 G07.4 的撤銷機制。到期不需任何動作：驗證與串流檢查都直接檢查分享（`guestLiveSQL`），到期即失效。

### 稽核與存取紀錄

| 事件 | 類別 | 內容 |
| --- | --- | --- |
| `share.created`／`share.revoked` | audit | 分享設定（不含權杖）／撤銷的工作階段數 |
| `share.redeemed` | security | 工作階段 ID、種類、裝置名稱、客戶端 |
| `share.redeem_refused` | security | 原因：`revoked`／`expired`／`playback`／`session_limit`（權杖不認得時沒有目標，不寫） |
| `share.accessed`／`share.access_refused` | security | 訪客請求的工作階段、種類與「方法＋路由樣式」；不記路徑、查詢字串或憑證 |

每個訪客請求都被稽核涵蓋：同一工作階段、同一路由樣式、同一結果每分鐘寫一筆（播放器的大量 Range 請求共用一筆），寫不進去時拒絕該請求（503），不會有未稽核的訪客存取。`GET /api/v1/shares/{id}/access` 依目標列出這些事件。稽核紀錄在降級遷移與分享刪除後都保留。

### 元資料備份

網路規則與 `client_rules.libraries` 納入元資料備份（G36.4，新種類 `library_network_rule`；匯入時庫 ID 依對照表換成目的地的庫，規則的庫沒有一起匯入時略過）。分享連結**不**納入：持權杖者即可存取，還原等於讓舊連結復活；訪客帳號及其進度、規則、授權也一併排除。

## 統一過濾器與守門（G48.2）

所有使用者面向的讀取都在**同一條 SQL** 內以 `itemVisibleSQL`／`libraryVisibleSQL` 綁定呼叫者，不先查後在展示層過濾：

| 響應面 | 位置 |
| --- | --- |
| 列表（游標）、單筆 | `store.go` `listItemsSQL`（規則放在每個授權庫的有界分頁內，隱藏條目不佔名額）、`GetItem` |
| 瀏覽、搜尋、詳情、庫清單的內容種類 | `catalog_browse.go`（`libraryKindsSQL` 只計可見條目） |
| 直投、外掛字幕／音軌 | `store.go` `Resolve`、`ResolveTrack`；`sidecars.go` 兩處 |
| 播放資訊、檔案資訊 | `playback.go` `listSourcesSQL` |
| 圖片（本機海報、條目圖片、摘要） | `images.go`、`item_images.go` |
| 開始播放、使用者資料、標記已看、繼續觀看 | `progress.go` |
| 統計（個人／管理員報表） | `watch_stats.go` `watchStatsScope` |
| 相容層（`/compat/...`） | 走同一組 app 服務與上列 store 方法，無獨立查詢 |

隱藏與不存在一律回同一結果：依設定為 404（預設）或 403（設定檔 `access.hiddenStatus`／環境變數 `JELEE_HIDDEN_CONTENT_STATUS`），相容層回空本文的同一狀態碼。

守門：`TestVisibilityPredicateHasOneSource` 掃描 `internal/adapter/postgres` 的非測試原始碼，`library_acl` 只能出現在 `visibility.go` 與 `account_acl.go` 的三條授權管理語句；新規則表（`user_item_access_rules`、`user_blocked_tags`、`access_policy`、`parental_ratings`、`parental_rating_max`）只能出現在 `visibility.go` 與管理檔 `content_access.go`；`share_links` 只能出現在 `visibility.go` 與 `shares.go`，`library_network_rules` 只能出現在 `visibility.go` 與 `network_rules.go`（元資料備份與舊庫匯入檔例外，只搬資料）。使用者讀自己的媒體庫授權清單（`GET /api/v1/users/{id}/libraries`）也套用本請求的網路與 G47 限制，管理員管理他人授權時看到全部。任何查詢重新抄一份授權條件都會失敗；`TestVisibilityGuardDetectsCopiedPredicate` 以植入副本驗證守門本身。

### Webhook 載荷

Webhook 端點只有管理員能建立（G12），接收端屬管理層級，載荷不依個別使用者過濾——等同管理員在 `restrict_admins=false` 下的視角。載荷只含 ID 與少量欄位（`media.added` 含 `title`；播放事件含 `userId`／`itemId`），不含路徑。開啟 `restrict_admins` **不會**過濾 Webhook 載荷；需要「以某使用者視角過濾」的端點列為後續。

管理員專用的統計匯出（`GET /api/v1/watch-stats/export`）同理，屬管理稽核用途，不套用條目規則。

## 管理 API（僅管理員）

| 方法與路徑 | 內容 | 稽核 |
| --- | --- | --- |
| GET `/api/v1/users/{id}/content-access` | `parentalRatingMax`、`blockUnrated`、`blockedTags`、`rules[]`（含條目名稱、種類、庫） | — |
| PUT `/api/v1/users/{id}/content-access` | `{"parentalRatingMax":0..21,"blockUnrated":true\|false,"blockedTags":[...]}`；整組替換：省略 `parentalRatingMax` 即無上限、省略 `blockUnrated` 即跟隨全域（嚴格 JSON 不接受 null），`blockedTags` 必填、空陣列清除；條目規則不受影響；標籤存為去空白小寫並去重，最多 100 個、各 128 位元組 | `user.content_access_changed`（前後值）；未變不寫 |
| PUT `/api/v1/users/{id}/content-access/items/{itemId}` | `{"effect":"allow"\|"hide"}`；任何庫的條目皆可設；每使用者最多 1000 條（超過 409） | `user.item_access_rule_set`；未變不寫 |
| DELETE `/api/v1/users/{id}/content-access/items/{itemId}` | 空本文；沒有規則回 404 | `user.item_access_rule_removed` |
| GET／PUT `/api/v1/access/policy` | `{"restrictAdmins":bool,"blockUnrated":bool}`（PUT 兩者必填） | `access.policy_changed`；未變不寫 |
| GET `/api/v1/access/parental-ratings` | 可辨識的分級代碼與等級 | — |

變更在下一個請求即生效（過濾在 SQL 內即時計算，沒有快取）。已在進行的直投串流不會因規則變更被中斷；下一次 `Resolve` 會依新規則拒絕。

`users.content_filtered` 是快速通過旗標：任何寫入規則或封鎖標籤的路徑都會由觸發器設為 true，設定分級上限時資料庫 CHECK 要求旗標為 true；移除最後一條限制時管理 API 重算為 false。旗標只可能「多餘地為 true」（只多花查詢），不會在仍有限制時為 false。

## 效能（G48.8）

每條規則都是以條目 ID、至多兩層祖先與使用者為鍵的索引探查：`user_item_access_rules`（主鍵／`item_id` 索引）、`item_parent_links`（主鍵）、`item_metadata_fields`、`item_metadata_facts`（主鍵）、`user_blocked_tags`（主鍵）、`parental_ratings`（主鍵）。`access_policy` 只有一列，每個語句讀一次（InitPlan）。沒有限制的使用者在 `content_filtered` 就短路。

`TestContentAccessPlanPostgres`（20,000 個條目：12,000 部電影＋1,600 部各一季三集的劇；全部有分級、每十個標 horror；另有 400 個使用者各 25 條規則、5 個封鎖標籤），受限使用者（上限 13、封鎖 horror、隱藏一部劇）的 EXPLAIN (ANALYZE, BUFFERS)：

| 查詢 | 結果 | 單次樣本執行時間 |
| --- | --- | --- |
| `listItemsSQL` 第一頁 50 筆 | 只走 `items_id_library_id_key` 有序索引，造訪 **109** 個條目列（約一半被規則濾掉），規則表全為 Index／Index Only Scan | 2.9 ms |
| 整庫可見數（瀏覽總數的形狀） | `items` 依庫讀全部 20,000 列（總數本來就要看全部），每列規則全走索引；`library_acl` 每語句一次 | 549 ms（含 EXPLAIN ANALYZE 計時開銷） |
| 單一條目（直接 ID） | 全部 Index Scan | 0.5 ms |

測試斷言：規則相關表（`items` 除整庫計數外、`user_item_access_rules`、`user_blocked_tags`、`item_metadata_fields`、`item_metadata_facts`、`item_parent_links`）不得出現 Seq Scan；`library_acl` 不得每列重掃；有界分頁造訪的條目列 ≤ 400。以上為單次樣本，不是 P95 或統計結論。

單請求 SQL 次數：`TestContentAccessStatementCountPostgres` 以 pgx tracer 計數，以下請求在受限與不受限使用者都恰為 **2 條**語句（驗證工作階段 1 條＋業務查詢 1 條），規則不增加語句數：

- `GET /api/v1/items?limit=50`
- `GET /api/v1/items?limit=50&q=movie&type=Movie`
- `GET /api/v1/items/{id}/details`
- `GET /api/v1/items/{id}`

已知成本：整庫總數與大範圍瀏覽是「每個條目數次索引探查」，受限使用者在大庫上的總數查詢與條目數成正比。庫清單的「內容種類」探查對受限使用者會一直找到第一個可見條目為止；某種類在大庫中全部被隱藏時，該探查會走完該種類的條目。若日後需要，可將每條目的有效分級與標籤物化成一張由寫入路徑維護的表，以單一索引條件取代探查；本次未做，列為後續。

## 驗收

- 權限矩陣 `TestContentAccessMatrixPostgres`：14 個情境（無規則、上限 13／16、使用者／全域未分級策略與覆寫、標籤封鎖、子樹隱藏、近者允許、近者隱藏、允許蓋過分級、允許不擴大庫授權、管理員預設不受限、`restrict_admins`），每個情境讀 viewer／peer／admin 三個使用者的 ListItems（分頁）、瀏覽、搜尋、繼續觀看、使用者資料、統計，以及逐條目的 GetItem、GetBrowseItem、GetItemDetails、ListItemImages、ListItemSources、SetPlayed、Resolve（直投）、ResolveImageSource，全部必須與預期集合一致；隱藏與不存在同為 not found。
- 全路由遍歷 `TestAccessLeakHiddenContentPostgres`：七種隱藏機制（媒體庫授權、條目規則、分級、標籤、網路限制、G47 `restrict_libraries`、分享範圍外）× 三種隱藏狀態模式（預設 404、明示 404、設定 403），每次遍歷 187 條路由，零洩漏。分享範圍機制以原生訪客權杖遍歷：白名單內的路由照一般檢查（可見對照 200、隱藏與不存在同狀態），白名單外的路由必須 403 `share_forbidden`（相容層 401）且不含任何標記。
- 網路與分享矩陣 `TestNetworkAccessMatrixPostgres`（11 個情境：無規則、僅區網〔RFC 1918、ULA、環回、IPv4 對映、連結本地、無位址〕、僅外網、CIDR〔IPv4／IPv6〕、工作階段種類、條件且／規則或、停用規則、`includeAdmins`、`allow` 條目規則不能越過、G47 庫集合〔含空集合與管理員〕、G47＋網路交集），`TestShareGuestMatrixPostgres`（整庫、劇集、季、他庫條目分享；訪客內容規則；訪客＋網路規則；直投與分享上限；網頁訪客不能直投；過期、撤銷、二次撤銷、未知權杖；稽核；訪客帳號不出現在帳號管理），全部經同一組 store 表面（列表、瀏覽、搜尋、繼續觀看、使用者資料、統計、單筆、詳情、圖片、來源、標記、直投、圖片來源）。
- HTTP 端到端 `TestShareGuestsOverHTTP`（真 PostgreSQL＋loopback TCP）：建立與驗證、網頁／原生兌換、瀏覽器擋原生兌換、訪客閘門、全站同時播放開關關閉下分享上限仍生效（第二個直投 429）、撤銷後進行中的下載在約 1 秒內斷流（檢查間隔 1 秒）、存取紀錄、唯讀分享、未允許播放的分享、經可信代理判定的區網／外網。
- 遷移 `TestShareNetworkMigrationRoundTrip`：有網路規則、有 `restrict_libraries` 規則、有有效分享時都拒絕降級；全部清除（分享撤銷）後 down／up，訪客帳號隨之刪除，稽核保留。
- 遷移 `TestContentAccessMigrationRoundTrip`：有規則時拒絕降級（避免降級後默默露出被隱藏內容），清除後 up/down/up，稽核保留。

### 網路參數下推的 EXPLAIN 證據（G48.5、G48.8）

`TestNetworkAccessPlanPostgres`：同上 20,000 個條目，另有 300 個媒體庫各一條網路規則，受測庫有一條「僅區網」規則；單次樣本：

| 查詢 | 結果 |
| --- | --- |
| `listItemsSQL` 第一頁 50 筆，區網請求 | 讀 **50** 個條目列；網路規則表每語句讀 2 次（管理員與非管理員兩個 InitPlan，各一次），不隨條目數增加；1.1 ms |
| 同上，外網請求 | 隱藏的庫在讀分頁前被剔除：讀 **0** 個條目列 |
| 整庫可見數，區網 | 讀全部 20,016 列（總數本來就要），網路規則表仍只讀 1 次 |
| 單一條目 | 全部索引，0.4 ms |
| 條目分享訪客的第一頁 | 只走分享的子樹：讀 4 個條目列，不讀整庫 |

測試斷言：網路規則表任何節點的 `Actual Loops` ≤ 1、`items` 不得出現 Seq Scan（整庫計數除外）、`library_acl` 不得每列重掃、條目分享列表讀的條目列 ≤ 20。

單請求 SQL 次數：`TestNetworkAccessStatementCountPostgres` 在有網路規則的情況下，區網、外網與訪客（同一分鐘內第二次走同一路由）請求上列四個路徑都是 **2 條**語句；訪客每個工作階段、每個路由樣式每分鐘第一次存取多 1 條稽核寫入。

已知成本：整庫總數這類「每個條目都要判定」的大語句，所測 PostgreSQL 開著 JIT（預設）時，時間主要花在 JIT 編譯而不是判定本身：同一 2 萬條目的整庫計數，`jit=off` 時舊述詞 3.8 ms、新述詞 4.3 ms，`jit=on` 時約 330 ms 對 510 ms（述詞變長讓編譯變久）。這是既有現象，本次未改資料庫設定；若大庫瀏覽總數偏慢，可考慮對 Jelee 的資料庫角色設 `jit=off`，列為後續評估。

## 後續（本次不在範圍）

- G48.1 使用者群組、目錄根級（library root）規則。
- G48.4 時間窗（限制時段）、關鍵字（標題／簡介）封鎖、管理員自訂分級代碼表。
- G48.5：裝置類型的真實來源（目前以工作階段種類代替）；依使用者或群組套用的網路規則（目前規則作用於所有非管理員，管理員依 `includeAdmins`）。
- G48.6：可登入、可到期的訪客使用者帳號；非管理員建立分享（需「分享範圍 ≤ 建立者可見範圍」）；分享連結的 QR／短網址；同一分享的進度依裝置分開。
- G48.7 授權矩陣頁面、批量套用、模板、變更預覽（本次只有 API 與稽核）。
- G48.9 dev 模式暫時關閉嚴格權限（需標記與自動恢復）。
- Webhook 以使用者視角過濾的端點選項；物化每條目分級／標籤以降低大庫總數成本。
