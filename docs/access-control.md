# 存取控制與統一權限過濾器（G48）

本文說明 Jelee 如何決定「某個使用者看得到哪些條目」：授權模型、規則優先順序與合併策略、關鍵字封鎖與限制時段（G48.4）、網路限制（G48.5）、分享連結與訪客（G48.6）、授權矩陣與批量授權（G48.7）、統一過濾器的位置與守門、管理 API、效能證據，以及尚未實作的後續項目。設計理由見 [ADR 0003](adr/0003-unified-access-filter-in-sql.md) 與 [ADR 0009](adr/0009-access-keywords-time-windows-bulk.md)。

- 統一述詞：`internal/adapter/postgres/visibility.go`（唯一來源）。
- 守門測試：`internal/adapter/postgres/visibility_guard_test.go`。
- 規則管理：`internal/adapter/postgres/content_access.go`、`internal/app/content_access.go`、`internal/adapter/http/content_access.go`；契約 `internal/domain/content_access.go`。
- 網路限制與分享：`internal/adapter/postgres/network_rules.go`、`shares.go`、`internal/app/shares.go`、`internal/adapter/http/shares.go`（含訪客閘門）；契約 `internal/domain/share.go`。
- 批量授權、模板與變更預覽（G48.7）：`internal/adapter/postgres/access_bulk.go`、`internal/app/access_admin.go`、`internal/adapter/http/content_access.go`；契約 `internal/domain/access_admin.go`。前端授權矩陣頁 `web/src/features/access/`。
- 合集與播放清單（G02.1）的成員也只經此過濾器讀出，見[合集與播放清單](collections-playlists.md)。
- 遷移：`000069_content_access`、`000079_share_network_access`（`library_network_rules`、`share_links`、`users.share_id`、`client_rules.libraries`）、`000084_access_controls`（`user_blocked_keywords`、`user_access_windows`、`access_templates`、`access_template_libraries`）。
- 驗收測試：`content_access_test.go`（權限矩陣、管理 API、遷移 up/down/up）、`access_controls_test.go`／`access_controls_plan_test.go`（關鍵字與時段矩陣、時段 SQL 對照、分級代碼表、批量與模板、遷移、EXPLAIN）、`content_access_plan_test.go`（EXPLAIN 與單請求 SQL 次數）、`share_access_test.go`／`share_access_plan_test.go`／`share_http_test.go`（網路與分享的矩陣、EXPLAIN、SQL 次數、撤銷斷流、遷移）、`internal/adapter/http/access_leak_test.go`（全路由遍歷）。

## 授權模型

| 層 | 資料 | 誰設定 | 作用 |
| --- | --- | --- | --- |
| 媒體庫授權 | `library_acl`（使用者 × 庫） | 管理員：`PUT /api/v1/users/{id}/libraries` | 外圈邊界。管理員看得到全部庫，一般使用者只看得到被授權的庫 |
| 條目規則 | `user_item_access_rules`（使用者 × 條目，`allow`／`hide`） | 管理員：`PUT/DELETE /api/v1/users/{id}/content-access/items/{itemId}` | 涵蓋該條目與其子樹（劇集 → 季 → 集，經 `item_parent_links`） |
| 標籤／類型封鎖 | `user_blocked_tags` | 管理員：`PUT /api/v1/users/{id}/content-access` | 條目或其祖先的 `tags`、`genres` 含被封鎖的字詞即隱藏（去頭尾空白、不分大小寫） |
| 關鍵字封鎖（G48.4） | `user_blocked_keywords` | 同上（`blockedKeywords`） | 條目或其祖先的標題、中繼資料標題或原名含關鍵字即隱藏（NFKC 正規化、不分大小寫，見下） |
| 分級上限 | `users.parental_rating_max`（0–21，代表最低年齡；NULL＝無上限） | 同上 | 條目與其祖先中「可辨識分級」的最高者超過上限即隱藏 |
| 限制時段（G48.4） | `user_access_windows`（每使用者最多 20 個） | 管理員：`PUT /api/v1/users/{id}/content-access/windows` | 請求時刻落在時段內時：隱藏全部，或把分級上限收緊到時段的上限（見下） |
| 分級代碼表（G48.4） | `parental_ratings` | 管理員：`PUT /api/v1/access/parental-ratings`（整表替換） | 代碼 → 等級；預設為遷移 069 的內建表 |
| 未分級策略 | `users.block_unrated`（NULL＝跟隨全域）、`access_policy.block_unrated` | 同上；全域：`PUT /api/v1/access/policy` | 只在有上限時生效：條目與祖先都沒有可辨識分級時，是否隱藏 |
| 管理員是否受限 | `access_policy.restrict_admins`（預設 `false`） | `PUT /api/v1/access/policy` | 開啟後，管理員也套用自己的條目規則、標籤封鎖與分級上限；媒體庫授權永遠不限制管理員 |
| 網路限制（G48.5） | `library_network_rules`（媒體庫 × 條件） | 管理員：`/api/v1/access/network-rules` | 每請求的外圈：某庫有啟用中的規則時，只有符合其中至少一條的請求看得到該庫 |
| 客戶端管控庫限制（G47） | `client_rules`（`restrict_libraries`＋`libraries`） | 管理員：`/api/v1/client-control/rules` | 每請求的外圈：閘門判定後，本請求只看得到列出的庫（多條取交集） |
| 分享範圍（G48.6） | `share_links`＋訪客帳號 `users.share_id` | 管理員：`/api/v1/shares` | 訪客的「媒體庫授權」：只有分享的整庫，或分享條目與其子孫 |

### 分級資料來源

分級取自條目 metadata 的 `mpaa` 與 `certification` 兩個欄位（`item_metadata_fields`，NFO 的 `<mpaa>`、`<certification>`、`officialrating` 都匯入這裡；手動編輯亦同）。沒有獨立的分級欄位，因此不需新增可為 NULL 的欄位；沒有這兩個欄位的條目即為「未分級」。

比對前先正規化：去頭尾空白、轉大寫、去掉開頭的 `Rated `、去掉兩個字母的國碼前綴（`US:`、`DE: `、全形冒號亦可）。正規化後：

1. 在 `parental_ratings` 表查代碼（遷移內建：美國電影 G／PG／PG-13／R／NC-17、美國電視 TV-Y～TV-MA、英國 U／UC／12A／R18、日本 PG12／R15+／R18+、德國 FSK 0～18、台灣分級繁簡體）。`GET /api/v1/access/parental-ratings` 列出全部代碼與等級；管理員可用 `PUT` 整表替換（見「自訂分級代碼表」）。
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
3. **限制時段**：本請求的時刻落在 u 的某個時段內：沒有分級上限的時段 → 隱藏；有上限的時段把第 7 步的上限收緊為 `min(u 的上限, 時段上限)`（多個時段同時成立取最小）。`allow` 規則不能越過這一步。
4. **最近的條目規則**：在 i、父項、祖父項中找 u 的規則，距離最近者決定：`hide` → 隱藏；`allow` → 可見（略過 5、6 與 u 自己的上限，但不略過時段上限）。同一條目每個使用者只有一條規則（主鍵），沒有同層衝突。
5. **標籤／類型封鎖**：i 或祖先的 `tags`、`genres` 任一值命中 u 的封鎖 → 隱藏。
6. **關鍵字封鎖**：i 或祖先的標題、中繼資料標題、原名含 u 的任一關鍵字 → 隱藏。
7. **分級**：u 有上限（或時段上限）時，有效分級 > 上限 → 隱藏；沒有可辨識分級時，`COALESCE(u.block_unrated, access_policy.block_unrated)` 為真 → 隱藏。
8. 其餘 → 可見。

合併要點：

- 規則只會**縮小**媒體庫授權給的範圍；`allow` 是對 4、5 與祖先 `hide` 的例外，不是授權。
- 「最近者勝」讓「隱藏整部劇但允許第一季」成立：對劇集設 `hide`、對第一季設 `allow`，第一季與其各集可見、劇集本身不可見（第一季仍可從搜尋、直接 ID、繼續觀看到達；瀏覽劇集的子項時因父項不可見而回空）。
- 反過來「允許整部劇但隱藏某一集」：劇集 `allow`、該集 `hide`，只有該集隱藏。
- 不同使用者的規則互不影響；矩陣測試以第二個使用者驗證。
- 管理員的規則預設不生效（管理員看得到一切，用於管理）；開 `restrict_admins` 後才套用。這是可設定的建議預設。
- 網路限制與客戶端管控庫限制是「每請求」的條件，同一使用者在區網內外看到的不同；它們先於一切個人規則，`allow` 條目規則與管理員身分（除非規則未 `includeAdmins`）都不能越過。
- 開發者模式（G48.9）：開發者開關 `relax_permission_strict` 生效期間暫停 `restrict_admins`，管理員回到看得到一切；非管理員的授權與規則完全不變，所以不會多看到任何內容。開關在 `devPermissionRelaxedSQL`（同一個 `visibility.go`）以 `dev_mode_state` 的到期時間判斷，工作階段結束即恢復。見 [開發者模式](developer-mode.md)。

## 關鍵字封鎖（G48.4）

- 比對對象：條目本身與祖先（季、劇集）的**掃描標題**（`items.title`）、中繼資料 `title` 與 `originalTitle`。**不比對簡介**：簡介誤判率高（家長控制的關鍵字常出現在無關作品的劇情描述裡）且可長達 16 KiB；主題性的封鎖請用標籤／類型。理由見 [ADR 0009](adr/0009-access-keywords-time-windows-bulk.md)。
- 正規化：關鍵字與標題兩邊都做 Unicode NFKC（全形英數、半形片假名、全形空白都折成標準形）、去頭尾空白、轉小寫，然後做子字串比對（`strpos`，沒有萬用字元）。例：關鍵字 `ＧＨＯＳＴ` 命中原名 `Ghost Story`；`ｑｘ７` 命中 `… Qx7`。大小寫轉換依資料庫的 `LC_CTYPE`（與標籤封鎖相同；`C` 語系只轉 ASCII）。
- 每位使用者最多 100 個，各 128 位元組；正規化後為空的關鍵字（例如只有全形空白）拒絕。儲存的是正規化後的形式，`GET` 回傳的也是。
- 單字母這類很短的關鍵字會命中大量條目，由管理員自行判斷；`allow` 條目規則可以對個別條目放行（與標籤封鎖相同）。

## 限制時段（G48.4）

時段：`{weekdays, start, end, timeZone, ratingMax?}`，以 `PUT /api/v1/users/{id}/content-access/windows` 整組替換（最多 20 個，空陣列清除）。

- `weekdays`：0（週日）～6（週六），**時段開始的那天**；空＝每天。`start`／`end`：`HH:MM`，`end` 可為 `24:00`；`end` 小於或等於 `start` 表示跨午夜，例如週五 `21:00`～`07:00` 涵蓋週五晚上到週六早上七點。`start` 不得等於 `end`。
- `timeZone`：**必填的 IANA 名稱**（如 `Asia/Taipei`），每個時段自己的時區；沒有「伺服器時區」預設（容器多半是 UTC、多實例可能不同），不接受 `Local`。寫入時同時以伺服器與 PostgreSQL（`pg_timezone_names`）驗證，任一方不認得即 400。日光節約時間依時區規則自動處理。
- `ratingMax`：省略 → 時段內**隱藏全部條目**（帳號仍可登入，看到空的媒體庫，直投被拒）；有值 → 時段內的分級上限取 `min(使用者上限, ratingMax)`，未分級條目依使用者／全域的未分級策略。
- 為什麼是「時段內限制」而不是上游的「允許時段」、為什麼不拒絕登入，見 [ADR 0009](adr/0009-access-keywords-time-windows-bulk.md)。允許時段可用互補的限制時段表達（允許 08:00–21:00＝限制 21:00–08:00）。
- 時刻：HTTP 層在驗證後取**請求時刻**放進 `access.RequestScope.At`，隨既有的單一 jsonb 請求參數（`at`）進入統一述詞，在 SQL 裡以 `AT TIME ZONE` 換成各時段的當地時間判斷；同一請求的所有語句用同一個時刻。沒有請求的語句（背景工作、CLI）用 `statement_timestamp()`。測試以 `httpapi.WithAccessClock` 或 principal 的 `RequestScope.At` 注入時鐘。
- 管理員的時段只在 `restrict_admins` 開啟時生效（與其他內容規則相同）。已在進行的直投不會因進入時段而中斷；下一次 `Resolve` 會依時段拒絕。
- 變更寫稽核 `user.access_windows_changed`（前後時段清單）；未變不寫。

## 自訂分級代碼表（G48.4）

`PUT /api/v1/access/parental-ratings` 以 `{"ratings":[{"code","level"}]}` 整表替換（最多 500 個；回傳新表）。預設就是遷移 069 的內建表，不設定即維持原行為。

- 代碼存成去頭尾空白、轉大寫；帶 `Rated ` 開頭或兩字母國碼前綴（`US:`、`DE: `）的代碼拒絕——篩選器比對前會把條目值的這些前綴去掉，這種代碼永遠比不到。正規化後重複的代碼拒絕。
- 條目的值查不到代碼時，純年齡（`16`、`16+`）仍視為該年齡；其他視為未分級。所以刪掉某個代碼會讓只標該代碼的條目變成「未分級」。
- 變更寫稽核 `access.rating_codes_changed`（新增、刪除、改等級的代碼與前後等級）；未變不寫。下一個請求即生效。

## 授權矩陣、批量授權與模板（G48.7）

| 方法與路徑 | 內容 |
| --- | --- |
| GET `/api/v1/access/library-grants` | 全部媒體庫與全部帳號（不含已刪除與分享訪客）的授權庫 ID；最多 1,000 位使用者，超過時 `truncated:true` |
| POST `/api/v1/access/library-grants/bulk` | `{"operations":[{"action":"add"\|"remove","userIds":[…],"libraryIds":[…]}],"preview":bool}`：依序套用，同一交易；最多 200 個操作、100 位不同使用者；本文上限 1 MiB |
| GET／POST `/api/v1/access/templates`、PUT／DELETE `/api/v1/access/templates/{id}` | 模板：`{name, libraryIds, parentalRatingMax?, blockUnrated?, blockedTags, blockedKeywords?}`；名稱不分大小寫唯一（409），最多 100 個 |
| POST `/api/v1/access/templates/{id}/apply` | `{"userIds":[…],"preview":bool}`：最多 100 位使用者 |

- **預覽**：`preview` 必填。`true` 時在交易裡實際做完變更、計數後回滾，什麼都不寫（也不寫稽核）；`false` 時做同一件事並提交。回應 `{applied, users, items, shown, hidden, changes[]}`：設定有變的使用者數、可見性改變的不同條目數、變成可見／變成隱藏的「使用者 × 條目」對數，以及逐使用者的新增／移除庫與 `shown`／`hidden`。
- 計數方法：變更前把這些使用者看得到的「使用者 × 條目」存進交易暫存表，變更後以 `grantVisibleSQL`（`itemVisibleSQL` 去掉每請求的網路與 G47 限制）在同一交易再算一次並比較——數字就是統一述詞在**請求時刻**的結果（含時段），與從哪個網路發出預覽無關。批量授權只比較操作中的媒體庫；模板會改內容限制，所以比較全部條目。
- **模板是複製**：套用＝使用者的授權庫完全等於模板的庫集合，分級上限、未分級策略、封鎖標籤與關鍵字整組換成模板的；條目規則與限制時段保留。之後修改或刪除模板不影響已套用的使用者。
- 目標使用者必須存在、未刪除且不是分享訪客，媒體庫必須存在，否則 404；管理員帳號可以被套用，但授權庫不限制管理員（內容限制依 `restrict_admins`）。
- **稽核**：套用時每位授權有變的使用者寫 `user.library_access_replaced`（前後庫清單，與單一使用者 API 相同），模板改到內容限制的使用者寫 `user.content_access_changed`；整次操作另寫一筆 `access.grants_bulk_applied`（操作清單）或 `access.template_applied`（模板 ID、名稱、使用者），兩者都帶與預覽相同的 `users`／`items`／`shown`／`hidden`。重複套用同一變更仍會寫整次操作的紀錄（數字為 0）。模板增刪改寫 `access.template_created`／`updated`／`deleted`。
- 前端：管理頁「存取控制 → 授權矩陣」以使用者 × 媒體庫勾選表格編輯，「預覽變更」把差異轉成上述操作並顯示確認對話框（影響使用者數、條目數、逐使用者明細），確認後才套用；模板可建立、編輯、刪除並套用到選取的使用者（同樣先預覽）。

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

封鎖關鍵字、限制時段與存取模板（G48.4、G48.7）也納入元資料備份（種類 `user_blocked_keyword`、`user_access_window`、`access_template`；目的地 PostgreSQL 不認得時區的時段以 `time_zone_unknown` 略過，名稱已被別的模板使用的模板以 `template_name_taken` 略過，模板的媒體庫只保留一起匯入的）。網路規則與 `client_rules.libraries` 納入元資料備份（G36.4，新種類 `library_network_rule`；匯入時庫 ID 依對照表換成目的地的庫，規則的庫沒有一起匯入時略過）。分享連結**不**納入：持權杖者即可存取，還原等於讓舊連結復活；訪客帳號及其進度、規則、授權也一併排除。

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

守門：`TestVisibilityPredicateHasOneSource` 掃描 `internal/adapter/postgres` 的非測試原始碼，`library_acl` 只能出現在 `visibility.go`、`account_acl.go` 的三條授權管理語句與 `access_bulk.go` 的三條批量管理語句；新規則表（`user_item_access_rules`、`user_blocked_tags`、`user_blocked_keywords`、`user_access_windows`、`access_policy`、`parental_ratings`、`parental_rating_max`）只能出現在 `visibility.go` 與管理檔 `content_access.go`；`share_links` 只能出現在 `visibility.go` 與 `shares.go`，`library_network_rules` 只能出現在 `visibility.go` 與 `network_rules.go`（元資料備份與舊庫匯入檔例外，只搬資料）。使用者讀自己的媒體庫授權清單（`GET /api/v1/users/{id}/libraries`）也套用本請求的網路與 G47 限制，管理員管理他人授權時看到全部。任何查詢重新抄一份授權條件都會失敗；`TestVisibilityGuardDetectsCopiedPredicate` 以植入副本驗證守門本身。

### Webhook 載荷

Webhook 端點只有管理員能建立（G12），接收端屬管理層級，載荷不依個別使用者過濾——等同管理員在 `restrict_admins=false` 下的視角。載荷只含 ID 與少量欄位（`media.added` 含 `title`；播放事件含 `userId`／`itemId`），不含路徑。開啟 `restrict_admins` **不會**過濾 Webhook 載荷；需要「以某使用者視角過濾」的端點列為後續。

管理員專用的統計匯出（`GET /api/v1/watch-stats/export`）同理，屬管理稽核用途，不套用條目規則。

## 管理 API（僅管理員）

| 方法與路徑 | 內容 | 稽核 |
| --- | --- | --- |
| GET `/api/v1/users/{id}/content-access` | `parentalRatingMax`、`blockUnrated`、`blockedTags`、`rules[]`（含條目名稱、種類、庫） | — |
| PUT `/api/v1/users/{id}/content-access` | `{"parentalRatingMax":0..21,"blockUnrated":true\|false,"blockedTags":[...],"blockedKeywords":[...]}`；整組替換：省略 `parentalRatingMax` 即無上限、省略 `blockUnrated` 即跟隨全域（嚴格 JSON 不接受 null），`blockedTags` 必填、空陣列清除，省略 `blockedKeywords` 即清除關鍵字；條目規則與限制時段不受影響；標籤存為去空白小寫並去重，關鍵字另做 NFKC，各最多 100 個、各 128 位元組 | `user.content_access_changed`（前後值）；未變不寫 |
| PUT `/api/v1/users/{id}/content-access/windows` | `{"windows":[{"weekdays":[0..6],"start":"HH:MM","end":"HH:MM","timeZone":"IANA","ratingMax":0..21}]}`，見「限制時段」 | `user.access_windows_changed`；未變不寫 |
| PUT `/api/v1/users/{id}/content-access/items/{itemId}` | `{"effect":"allow"\|"hide"}`；任何庫的條目皆可設；每使用者最多 1000 條（超過 409） | `user.item_access_rule_set`；未變不寫 |
| DELETE `/api/v1/users/{id}/content-access/items/{itemId}` | 空本文；沒有規則回 404 | `user.item_access_rule_removed` |
| GET／PUT `/api/v1/access/policy` | `{"restrictAdmins":bool,"blockUnrated":bool}`（PUT 兩者必填） | `access.policy_changed`；未變不寫 |
| GET `/api/v1/access/parental-ratings` | 可辨識的分級代碼與等級 | — |
| PUT `/api/v1/access/parental-ratings` | `{"ratings":[{"code","level"}]}` 整表替換，見「自訂分級代碼表」 | `access.rating_codes_changed`；未變不寫 |
| 授權矩陣、批量授權、模板 | 見「授權矩陣、批量授權與模板」 | 見該節 |

變更在下一個請求即生效（過濾在 SQL 內即時計算，沒有快取）。已在進行的直投串流不會因規則變更被中斷；下一次 `Resolve` 會依新規則拒絕。

`users.content_filtered` 是快速通過旗標：任何寫入規則、封鎖標籤、關鍵字或時段的路徑都會由觸發器設為 true，設定分級上限時資料庫 CHECK 要求旗標為 true；移除最後一條限制時管理 API 重算為 false。旗標只可能「多餘地為 true」（只多花查詢），不會在仍有限制時為 false。

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

### 關鍵字與限制時段的 EXPLAIN 證據（G48.4、G48.8）

`TestAccessControlsPlanPostgres`：同上 2 萬個條目，另有 400 個使用者各 3 個關鍵字、2 個時段；受測使用者有關鍵字 `ｐｌａｎ ７`（命中標題以「plan 7」開頭的電影）與一個週五 21:00–07:00（台北）分級上限 13 的時段，分別以時段內（週五 22:30）與時段外（週六 22:30）的請求時刻執行；單次樣本：

| 查詢 | 結果 |
| --- | --- |
| `listItemsSQL` 第一頁 50 筆，時段外 | 分頁造訪 52 個條目列，2.6 ms |
| 同上，時段內 | 分頁造訪 88 個條目列（時段上限濾掉 R／TV-MA），4.1 ms |
| 整庫可見數，時段外／時段內 | 18,904／9,456 列可見，各約 1.0／1.1 秒（EXPLAIN ANALYZE 計時、JIT 開啟） |
| 批量預覽（一位使用者、整庫 2 萬條目、受限使用者） | 約 0.64 秒（變更前後各判定一次全部條目；預覽交易 `SET LOCAL jit=off`，開著 JIT 時約 1.6 秒） |

測試斷言：`user_blocked_keywords`、`user_access_windows` 與既有規則表任何節點不得出現 Seq Scan（整庫計數的 `items` 主掃描除外）、`library_acl` 不得每列重掃、分頁造訪 ≤ 400 列，且時段內外的可見數與逐條計算的預期完全一致。時段判定不增加語句數：時刻跟著既有的請求參數進入同一條語句。

已知成本：預覽與套用都要對每位目標使用者判定（被比較範圍內的）每個條目兩次，時間與「使用者數 × 條目數」成正比；受限使用者在 2 萬條目上約 0.64 秒，數十位使用者的模板預覽在大庫上可能超過請求逾時（設定 `requestTimeoutSeconds`，預設 15 秒，逾時回 408 `request_timeout` 且交易回滾、不寫入任何東西），請分批；改為背景工作列為後續。

已知成本：整庫總數與大範圍瀏覽是「每個條目數次索引探查」，受限使用者在大庫上的總數查詢與條目數成正比。庫清單的「內容種類」探查對受限使用者會一直找到第一個可見條目為止；某種類在大庫中全部被隱藏時，該探查會走完該種類的條目。若日後需要，可將每條目的有效分級與標籤物化成一張由寫入路徑維護的表，以單一索引條件取代探查；本次未做，列為後續。

## 驗收

- 權限矩陣 `TestContentAccessMatrixPostgres`：14 個情境（無規則、上限 13／16、使用者／全域未分級策略與覆寫、標籤封鎖、子樹隱藏、近者允許、近者隱藏、允許蓋過分級、允許不擴大庫授權、管理員預設不受限、`restrict_admins`），每個情境讀 viewer／peer／admin 三個使用者的 ListItems（分頁）、瀏覽、搜尋、繼續觀看、使用者資料、統計，以及逐條目的 GetItem、GetBrowseItem、GetItemDetails、ListItemImages、ListItemSources、SetPlayed、Resolve（直投）、ResolveImageSource，全部必須與預期集合一致；隱藏與不存在同為 not found。
- 全路由遍歷 `TestAccessLeakHiddenContentPostgres`：九種隱藏機制（媒體庫授權、條目規則、分級、標籤、關鍵字〔全形關鍵字 `ｑＸ７`〕、限制時段〔遍歷以 `WithAccessClock` 固定在週三 18:30 台北，時段 18:00–19:00 上限 13〕、網路限制、G47 `restrict_libraries`、分享範圍外）× 三種隱藏狀態模式（預設 404、明示 404、設定 403），零洩漏。分享範圍機制以原生訪客權杖遍歷：白名單內的路由照一般檢查（可見對照 200、隱藏與不存在同狀態），白名單外的路由必須 403 `share_forbidden`（相容層 401）且不含任何標記。
- 網路與分享矩陣 `TestNetworkAccessMatrixPostgres`（11 個情境：無規則、僅區網〔RFC 1918、ULA、環回、IPv4 對映、連結本地、無位址〕、僅外網、CIDR〔IPv4／IPv6〕、工作階段種類、條件且／規則或、停用規則、`includeAdmins`、`allow` 條目規則不能越過、G47 庫集合〔含空集合與管理員〕、G47＋網路交集），`TestShareGuestMatrixPostgres`（整庫、劇集、季、他庫條目分享；訪客內容規則；訪客＋網路規則；直投與分享上限；網頁訪客不能直投；過期、撤銷、二次撤銷、未知權杖；稽核；訪客帳號不出現在帳號管理），全部經同一組 store 表面（列表、瀏覽、搜尋、繼續觀看、使用者資料、統計、單筆、詳情、圖片、來源、標記、直投、圖片來源）。
- HTTP 端到端 `TestShareGuestsOverHTTP`（真 PostgreSQL＋loopback TCP）：建立與驗證、網頁／原生兌換、瀏覽器擋原生兌換、訪客閘門、全站同時播放開關關閉下分享上限仍生效（第二個直投 429）、撤銷後進行中的下載在約 1 秒內斷流（檢查間隔 1 秒）、存取紀錄、唯讀分享、未允許播放的分享、經可信代理判定的區網／外網。
- 遷移 `TestShareNetworkMigrationRoundTrip`：有網路規則、有 `restrict_libraries` 規則、有有效分享時都拒絕降級；全部清除（分享撤銷）後 down／up，訪客帳號隨之刪除，稽核保留。
- 遷移 `TestContentAccessMigrationRoundTrip`：有規則時拒絕降級（避免降級後默默露出被隱藏內容），清除後 up/down/up，稽核保留。
- 關鍵字與時段矩陣 `TestContentAccessKeywordsAndWindowsPostgres`（13 個情境：全形關鍵字、祖先標題、原名與中繼資料標題、`allow` 勝過關鍵字、時段內隱藏全部、跨午夜、其他日不生效、時段上限、時段外不生效、兩個上限取小、`allow` 不越過時段、時段內未分級策略、管理員預設不受限），每個情境以注入的請求時刻讀 viewer／peer／admin 三人的全部儲存層表面；`TestAccessWindowSQLMatchesReference` 以 17 分鐘間隔掃過 2026-10-24～11-03（歐美日光節約時間結束）五種時段，SQL 判定與參考實作 `AccessWindow.Contains` 逐點一致。
- 管理 `TestContentAccessKeywordsAndWindowsAdministrationPostgres`、`TestParentalRatingCodesPostgres`（自訂代碼即時生效、前綴／重複代碼拒絕、稽核內容）、`TestAccessBulkGrantsAndTemplatesPostgres`（矩陣、預覽不寫入、套用的稽核數字等於預覽、重複套用為 0、模板 CRUD、名稱衝突、套用與刪除後設定保留）、HTTP `TestAccessAdministrationHTTPPostgres`（嚴格本文、`preview` 必填、注入時鐘下的時段、模板與代碼 API）。
- 遷移 `TestAccessControlsMigrationRoundTrip`：有關鍵字、時段或模板時都拒絕降級；清除後 down／up，稽核保留。

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
- G48.4：關鍵字比對簡介（本次刻意不做，見 ADR 0009）；讓使用者自己查看目前生效的限制時段（目前只有管理員看得到）。
- G48.5：裝置類型的真實來源（目前以工作階段種類代替）；依使用者或群組套用的網路規則（目前規則作用於所有非管理員，管理員依 `includeAdmins`）。
- G48.6：可登入、可到期的訪客使用者帳號；非管理員建立分享（需「分享範圍 ≤ 建立者可見範圍」）；分享連結的 QR／短網址；同一分享的進度依裝置分開。
- G48.7：超過 1,000 位使用者時矩陣分頁；大量使用者的預覽改為背景工作（目前同步、受請求逾時限制）。
- G48.9 dev 模式暫時關閉嚴格權限（需標記與自動恢復）。
- Webhook 以使用者視角過濾的端點選項；物化每條目分級／標籤以降低大庫總數成本。
