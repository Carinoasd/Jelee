# 元資料刷新任務（自動刮削，G13.6、G14.1、G14.4、G14.6）

`metadata_refresh` 任務把一個媒體庫裡的電影（`Movie`、`HomeVideo`）與劇集（`Series`）逐一和 TMDB 配對：先用條目上的外部 ID（NFO 的 `uniqueid`），沒有時再用檔名標題＋年份搜尋；算出置信度與理由碼後，**達到媒體庫門檻的直接走既有的「確認後套用」流程寫入**，其餘寫進待確認佇列，由管理員接受某個候選或拒絕。季與單集不在這個任務內（TMDB 套用只支援 movie／series 資源；季集號的識別見[目錄同步](catalog-sync.md#識別修正與待確認決定g215schema-100)）。

遷移 000103（暫定編號，合併時會依前面分支重新編號）新增工作種類、錯誤碼 `metadata_refresh_failed`、工作指標維度（共用工作指標成為固定 18 組）與三張表：`metadata_refresh_settings`、`metadata_refresh_job_state`、`metadata_refresh_reviews`。

## 已做的決定（皆可調）

| 決定 | 預設 | 調整方式 |
| --- | --- | --- |
| 自動刮削預設關閉，按媒體庫開啟 | `auto=false` | `PUT …/metadata/refresh/settings` 的 `auto` |
| 自動套用門檻保守：只有外部 ID 精確命中（高置信）直接寫入；標題＋年份唯一且相似度高為中置信，預設進待確認 | `autoApply=high` | `autoApply`：`high`／`medium`（中置信也自動套用）／`never`（全部進待確認） |
| 資料來源：先本地外部 ID（`nfo`），再 TMDB；備用源只做設定框架，不接任何第三方 | `sources=["nfo","tmdb"]` | `sources` 是有序清單；去掉 `nfo` 就不讀外部 ID。新的備用源要在 `domain.metadataRefreshProviders` 登記並實作後才可寫入，目前只有 `tmdb`（G14.1 不得網頁抓取或繞過 TMDB 條款） |
| 已被管理員拒絕的條目，之後的刷新不再碰 | — | 管理員可隨時以既有 `POST /api/v1/items/{id}/metadata/tmdb` 手動套用 |
| 自動排入的刷新任務以觸發它的同步任務的管理員身分執行 | — | 見下方「身分」 |

## 設定

`GET`／`PUT /api/v1/libraries/{id}/metadata/refresh/settings`（僅管理員）：

```json
{"libraryId":"…","auto":false,"autoApply":"high","sources":["nfo","tmdb"]}
```

`PUT` 三個欄位都必填；`autoApply` 只收 `high`／`medium`／`never`，`sources` 必須是 `nfo`、`tmdb` 的不重複組合且至少有一個遠端提供者，否則 400。沒有設定列的媒體庫回預設值。變更寫稽核 `library.metadata_refresh_changed`（前後值）。

## 送件與觀察

```http
POST /api/v1/libraries/LIBRARY_ID/metadata/refresh
Authorization: Bearer SESSION_TOKEN
Idempotency-Key: unique-key
Content-Type: application/json

{"priority":"manual"}
```

- 僅管理員；`priority` 為 `manual`（預設）或 `background`。手動送件不看 `auto`，一律用媒體庫目前的門檻與來源。
- 新任務 202 與 `Location`；相同 key、相同庫與優先級回 200 與 `Idempotency-Replayed: true`；同 key 不同庫或優先級為 409。庫已有活動任務為 409 `job_busy`，佇列滿為 429 `job_queue_full`，庫不存在為 404，沒有設定 TMDB 金鑰為 503 `metadata_unavailable`。稽核 `metadata_refresh.submitted`。
- 取消用既有 `POST /api/v1/jobs/JOB_ID/cancel`。

`GET /api/v1/jobs/JOB_ID/metadata-refresh`：

```json
{"jobId":"…","libraryId":"…","trigger":"manual","cursorItemId":"…","applied":3,"unchanged":0,"review":2,"unmatched":1,"skipped":1,"failed":0,"updatedAt":"…"}
```

`trigger` 為 `manual` 或 `catalog_sync`；其他種類的任務回 404。`GET /api/v1/jobs/JOB_ID` 的 `files` 是 applied＋unchanged＋review＋unmatched，`skipped` 是 skipped＋failed。

| 計數 | 條件 |
| --- | --- |
| applied | 經確認套用，至少一個欄位的值、來源或提供者 ID 改變 |
| unchanged | 經確認套用但沒有任何改變（欄位全被鎖定／人工／NFO 保護，或與 TMDB 現值相同；只更新抓取時間） |
| review | 寫進待確認佇列 |
| unmatched | 提供者沒有任何候選 |
| skipped | 沒有查詢：四個提供者欄位全部鎖定或人工、已被拒絕、類型被識別鎖鎖住的 `HomeVideo`（套用會把它改成 `Movie`），或處理中被刪除 |
| failed | 提供者暫時失敗或單一條目的錯誤（版本衝突重試一次仍衝突、資料不合法） |

## 配對與置信度

每個條目先組出配對主體（純函式 `domain.NewMetadataRefreshSubject`）：

- **標題**：NFO 或人工的標題優先；否則用檔名解析的標題（`catalog_scan_items.scan_title`）；再否則用目前標題。
- **年份**：年份事實（NFO／人工）→ 檔名年份 → 日期欄位的年。
- **外部 ID**：`uniqueIds` 事實中第一個有效的 `tmdb`、`imdb`（`tt` 加 7–10 位數字）、`tvdb`，`default="true"` 的優先。
- **既有提供者 ID**：先前套用過 TMDB 的欄位所記的 `providerOrigin`。

配對順序與結果（`domain.MatchMetadataByIdentifier`、`MatchMetadataByTitle`、`ReconcileMetadataMatch`）：

| 順序 | 方法 | 置信度 | 理由碼 |
| --- | --- | --- | --- |
| 1 | NFO TMDB ID，直接取詳情 | 高 | `nfo_tmdb_id` |
| 2 | NFO IMDb、TVDB ID，經 TMDB find：同資源恰好一個候選 | 高 | `nfo_imdb_id`、`nfo_tvdb_id` |
|  | find 回多個候選 | 低 | `external_ambiguous` |
|  | 外部 ID 指向的提供者 ID 與先前套用的不同 | 低（兩個都列為候選） | `external_conflict` |
| 3 | 先前套用的提供者 ID（再取一次，即「元資料刷新」） | 高 | `existing_provider` |
| 4 | 標題＋年份搜尋：標題或原標題相似度 ≥ 90%、年份相同的候選恰好一個 | 中 | `title_year_unique` |
|  | 上述候選超過一個 | 低 | `title_year_ambiguous` |
|  | 相似但條目沒有年份 | 低 | `title_without_year` |
|  | 相似但年份不同（帶年份搜尋沒有結果時，再不帶年份搜一次） | 低 | `year_mismatch` |
|  | 有候選但都不夠相似 | 低 | `low_similarity` |
|  | 沒有候選 | 無 | `no_candidates`（只計 unmatched，不建待確認） |

相似度：大小寫折疊、去掉標點與符號後的 Levenshtein 距離占較長者的百分比（最多比較 256 個字元）。候選依相似度、年份相符、提供者 ID 排序，最多保留 5 個。找不到某個外部 ID 時退到下一種方法。

## 套用規則（G14.6）

自動套用與待確認的「接受」都呼叫同一個 `Metadata.ApplyTMDB`（`confirmed=true`、`replaceExistingTitle=false`、帶讀到的版本號），與管理員手動套用完全相同：

- 鎖定欄位、人工欄位不覆寫；NFO 鎖定與 NFO 來源的欄位不覆寫；唯讀 NFO 媒體庫在同一交易先融合 NFO 再套 TMDB（NFO 優先，G21.2）。
- 匯入或舊資料的非空標題（來源 `existing`）不被取代；同步建立的條目標題來源是 `scan`，會被取代。
- 每次套用都寫 `item.tmdb_metadata_applied` 稽核，`actor` 是任務的管理員。
- 版本衝突（同時有人編輯）重讀一次再試，仍衝突計 failed，不覆寫。

## 待確認佇列

- `GET /api/v1/libraries/{id}/metadata/refresh/reviews?state=pending|rejected&cursor=&limit=`：依條目 ID 分頁。每筆有條目類型、目前標題、`revision`（接受時要帶的 `expectedRevision`）、資源、置信度、理由碼、查詢標題與年份、候選（`providerId`、`title`、`originalTitle`、`date`、`similarity`）、產生它的任務與時間。不含任何網址。
- `POST /api/v1/items/{id}/metadata/refresh/accept`，body `{"providerId":…,"expectedRevision":…}`：候選必須在清單內（否則 400），已拒絕的為 409，版本不符為 409；成功時回傳與手動套用相同的 `MetadataApplyResult`，刪除這筆待確認並寫 `metadata_refresh.review_accepted`（提供者 ID 與新版本號）。
- `POST /api/v1/items/{id}/metadata/refresh/reject`，body `{}`：狀態改為 `rejected`、記錄拒絕者與時間，寫 `metadata_refresh.review_rejected`；再拒絕一次為 409。之後的刷新略過這個條目（計 skipped），拒絕紀錄保留。
- 同一條目再次配對時，待確認列會被新的結果取代；被套用（自動或接受）後刪除；條目刪除時一併刪除。

## 觸發

1. **手動**：上面的 `POST …/metadata/refresh`。
2. **目錄同步之後**：媒體庫 `auto=true` 時，`catalog_sync` 成功結束的同一交易內排入一個背景優先的刷新任務（冪等鍵 `metadata-refresh:<同步任務 ID>`，`trigger=catalog_sync`）。排入在 savepoint 裡進行，任何失敗都回滾到 savepoint，**不會讓同步失敗或延遲**；媒體庫仍有活動任務、佇列已滿、或同步任務背後沒有有效的管理員時不排入，改寫稽核 `metadata_refresh.deferred`（`reason` 為 `job_busy`／`job_queue_full`／`no_administrator`）。TMDB 的任何失敗只發生在之後的刷新任務裡，與同步無關。

## 任務執行

- **游標與檢查點**：依條目 ID 遞增，每次讀 16 個；**每個條目一個檢查點**（結果、待確認寫入、游標與計數同一個受租約保護的交易），所以取消或換租約續跑都從游標之後接著做，不重複計數。已套用但還沒記錄就中斷的條目，續跑時會再套一次（結果為 unchanged）。
- **可取消**：取消後在下一個條目之前停下，已完成的條目保留。
- **單筆失敗隔離**：一個條目的提供者錯誤只把該條目記為 failed；連續 10 個條目都因提供者無法使用而失敗（例如 TMDB 中斷或請求預算用完）時任務以 `metadata_refresh_failed` 結束，避免把整個媒體庫走完卻什麼都做不了。
- **租約**：讀批次、記錄、結束都在 `fencedJob` 之內；過期的租約不能再寫進度。
- **請求預算**：所有 TMDB 請求走既有的轉接器（受控出站、[請求間隔與並行上限、429／503 共享冷卻](tmdb-governor.md)、候選快取與合併相同請求），任務本身一次只處理一個條目。
- **沒有提供者時**：未設定 TMDB 金鑰的實例不能送件（503），已排入的任務會被領取並以 `metadata_refresh_failed` 結束，不會占住媒體庫。

## 身分

刷新任務以排入它的管理員身分執行（自動排入時是同步任務的管理員，排程掃描即排程擁有者），每批次挑該管理員最晚到期、未撤銷的工作階段，套用與稽核都記在此管理員名下。管理員被停用、刪除、失去權限，或**沒有任何有效工作階段**時，任務以 `metadata_refresh_failed` 結束。長時間無人登入的實例因此可能讓自動刷新失敗；待驗證清單 C35 會確認這個行為在實際部署是否可接受。

## 備份與降版

- `metadata_refresh_job_state` 屬工作進度、`metadata_refresh_reviews` 屬可重建的衍生狀態（再跑一次刷新就會產生），都不進元資料備份；拒絕紀錄因此不會隨備份帶走，匯入後會重新出現在待確認。
- `metadata_refresh_settings` 也不匯出：自動刮削是「自動化開關」，還原後預設關閉，避免還原出的目錄在管理員確認前就被提供者資料改寫，需要時重新開啟。
- 降版：有刷新任務或其進度、指標、拒絕紀錄、或任何非預設設定時拒絕（`55000`）；待確認列直接丟棄。

## 已知限制

- 季與單集不做自動配對；外部 ID 不做跨目錄聚合（A9）。
- 只看條目自己的 `uniqueIds`，不再額外讀 NFO 檔案；唯讀 NFO 媒體庫請先跑 NFO 全量導入，讓外部 ID 進到事實。
- `find` 只在 TMDB 轉接器上；換提供者時沒有實作 `MetadataRefreshFinder` 的提供者會跳過 IMDb／TVDB。
- 自動套用不取代來源為 `existing` 的非空標題（與手動套用預設相同）。

## 驗證

- 純函式表格測試：`go test ./internal/domain/ -run "MatchMetadata|ReconcileMetadata|MetadataAutoApplies|TitleSimilarity|MetadataRefresh"`。
- 應用層（記憶體假提供者）：`go test ./internal/app/ -run MetadataRefresh`——高置信套用、IMDb find、中低置信進待確認、門檻 `medium`／`never`、來源去掉 `nfo`、外部 ID 衝突、拒絕略過、鎖定 `HomeVideo` 略過、單筆失敗隔離與連續失敗中止、取消與續跑、接受／拒絕。
- 真 PG（`JELEE_TEST_DATABASE_URL`，行程內假 TMDB，不連外網）：`go test -p 1 -parallel 2 ./internal/adapter/postgres/ -run TestMetadataRefresh`——高置信自動套用、鎖定與人工欄位不被改、保護條目不讀不寫、低置信進待確認、接受（含過期版本與非候選）與拒絕、第二輪刷新、取消後計數精確、租約遺失續跑不重複計數、TMDB 失敗只影響單筆、catalog sync 成功後自動排入（未開啟不排、沒有管理員時延後且同步仍成功）、設定與送件規則、降版拒絕與往返。
- 長時間、真金鑰與 Windows 項目見[待驗證清單](owner-verification-queue.md) B17、C35、D35。
