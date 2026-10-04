# 存取控制與統一權限過濾器（G48）

本文說明 Jelee 如何決定「某個使用者看得到哪些條目」：授權模型、規則優先順序與合併策略、統一過濾器的位置與守門、管理 API、效能證據，以及尚未實作的後續項目。

- 統一述詞：`internal/adapter/postgres/visibility.go`（唯一來源）。
- 守門測試：`internal/adapter/postgres/visibility_guard_test.go`。
- 規則管理：`internal/adapter/postgres/content_access.go`、`internal/app/content_access.go`、`internal/adapter/http/content_access.go`；契約 `internal/domain/content_access.go`。
- 遷移：`000069_content_access`。
- 驗收測試：`content_access_test.go`（權限矩陣、管理 API、遷移 up/down/up）、`content_access_plan_test.go`（EXPLAIN 與單請求 SQL 次數）、`internal/adapter/http/access_leak_test.go`（全路由遍歷）。

## 授權模型

| 層 | 資料 | 誰設定 | 作用 |
| --- | --- | --- | --- |
| 媒體庫授權 | `library_acl`（使用者 × 庫） | 管理員：`PUT /api/v1/users/{id}/libraries` | 外圈邊界。管理員看得到全部庫，一般使用者只看得到被授權的庫 |
| 條目規則 | `user_item_access_rules`（使用者 × 條目，`allow`／`hide`） | 管理員：`PUT/DELETE /api/v1/users/{id}/content-access/items/{itemId}` | 涵蓋該條目與其子樹（劇集 → 季 → 集，經 `item_parent_links`） |
| 標籤／類型封鎖 | `user_blocked_tags` | 管理員：`PUT /api/v1/users/{id}/content-access` | 條目或其祖先的 `tags`、`genres` 含被封鎖的字詞即隱藏（去頭尾空白、不分大小寫） |
| 分級上限 | `users.parental_rating_max`（0–21，代表最低年齡；NULL＝無上限） | 同上 | 條目與其祖先中「可辨識分級」的最高者超過上限即隱藏 |
| 未分級策略 | `users.block_unrated`（NULL＝跟隨全域）、`access_policy.block_unrated` | 同上；全域：`PUT /api/v1/access/policy` | 只在有上限時生效：條目與祖先都沒有可辨識分級時，是否隱藏 |
| 管理員是否受限 | `access_policy.restrict_admins`（預設 `false`） | `PUT /api/v1/access/policy` | 開啟後，管理員也套用自己的條目規則、標籤封鎖與分級上限；媒體庫授權永遠不限制管理員 |

### 分級資料來源

分級取自條目 metadata 的 `mpaa` 與 `certification` 兩個欄位（`item_metadata_fields`，NFO 的 `<mpaa>`、`<certification>`、`officialrating` 都匯入這裡；手動編輯亦同）。沒有獨立的分級欄位，因此不需新增可為 NULL 的欄位；沒有這兩個欄位的條目即為「未分級」。

比對前先正規化：去頭尾空白、轉大寫、去掉開頭的 `Rated `、去掉兩個字母的國碼前綴（`US:`、`DE: `、全形冒號亦可）。正規化後：

1. 在 `parental_ratings` 表查代碼（遷移內建：美國電影 G／PG／PG-13／R／NC-17、美國電視 TV-Y～TV-MA、英國 U／UC／12A／R18、日本 PG12／R15+／R18+、德國 FSK 0～18、台灣分級繁簡體）。`GET /api/v1/access/parental-ratings` 列出全部代碼與等級。
2. 查不到時，純年齡 `16` 或 `16+` 視為該年齡（上限 21）。
3. 其他值（如 `NR`、`Not Rated`、未知國家代碼）視為未分級。

條目的有效分級＝條目本身、父項、祖父項中可辨識分級的**最大值**。例如劇集為 TV-MA、某集自己標 TV-PG，該集仍以 TV-MA（17）判定——父項被隱藏時子項不會單獨露出。

## 優先順序與合併策略

對使用者 u 與條目 i，依序判定（`visibility.go` 的 `itemVisibleSQL` 即為下列條件的 SQL 版）：

1. **媒體庫授權**：u 不是管理員且 i 的庫不在 u 的授權中 → 隱藏。後面任何規則（包括 `allow`）都不能擴大此邊界。
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

守門：`TestVisibilityPredicateHasOneSource` 掃描 `internal/adapter/postgres` 的非測試原始碼，`library_acl` 只能出現在 `visibility.go` 與 `account_acl.go` 的三條授權管理語句；新規則表（`user_item_access_rules`、`user_blocked_tags`、`access_policy`、`parental_ratings`、`parental_rating_max`）只能出現在 `visibility.go` 與管理檔 `content_access.go`。任何查詢重新抄一份授權條件都會失敗；`TestVisibilityGuardDetectsCopiedPredicate` 以植入副本驗證守門本身。

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
- 全路由遍歷 `TestAccessLeakHiddenContentPostgres`：四種隱藏機制（媒體庫授權、條目規則、分級、標籤）× 三種隱藏狀態模式（預設 404、明示 404、設定 403），每次遍歷 146 條路由，零洩漏。
- 遷移 `TestContentAccessMigrationRoundTrip`：有規則時拒絕降級（避免降級後默默露出被隱藏內容），清除後 up/down/up，稽核保留。

## 後續（本次不在範圍）

- G48.1 使用者群組、目錄根級（library root）規則。
- G48.4 時間窗（限制時段）、關鍵字（標題／簡介）封鎖、管理員自訂分級代碼表。
- G48.5 裝置類型、IP／CIDR、是否區網的限制及與 G47 的優先順序。
- G48.6 共享連結與來賓使用者。
- G48.7 授權矩陣頁面、批量套用、模板、變更預覽（本次只有 API 與稽核）。
- G48.9 dev 模式暫時關閉嚴格權限（需標記與自動恢復）。
- Webhook 以使用者視角過濾的端點選項；物化每條目分級／標籤以降低大庫總數成本。
