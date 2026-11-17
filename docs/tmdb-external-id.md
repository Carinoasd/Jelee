# TMDB 外部 ID 反查（G14.4）

第 9 階段補缺：原本只有適配器層的 `FindByExternalID`（IMDb／TVDB／Wikidata → TMDB 候選，見[電影搜尋](tmdb-movie-search.md)與 `internal/adapter/metadata/find.go`），現在接上應用層與管理員 HTTP 路由。仍只列候選，不自動選擇、不寫入元資料；套用仍走既有的確認流程（[套用](tmdb-metadata-apply.md)）。

## 介面

`GET /api/v1/metadata/tmdb/find?source={source}&id={id}[&language=…][&libraryId=…]`

| 參數 | 說明 |
| --- | --- |
| `source` | `imdb_id`、`tvdb_id`、`wikidata_id`（TMDB ID 不需要反查，直接用預覽介面） |
| `id` | 該來源的標準形式：IMDb 只收作品 ID `tt`＋7–10 位數字（`nm` 人物 ID 拒絕）；TVDB 為正 int32 十進位、不得有前導 0；Wikidata 為 `Q`＋數字、不得有前導 0 |
| `language` | 管理員明確指定（`zh-CN`、`zh-TW`、`ja-JP`、`en-US`） |
| `libraryId` | 未指定 `language` 時用該庫的文字語言；兩者都省略時用登入者語言，再退回 `zh-CN` |

與其他 TMDB 路由相同：只有在設定 TMDB 憑據且啟用帳號時註冊；管理員限定（匿名 401、一般使用者 403）、在同一個帳號容量與驗證群組內；嚴格查詢（未知或重複參數 400）。不合法的 ID 在讀取媒體庫設定與呼叫 TMDB 之前就回 400。未知的庫 404，TMDB 不可用（含每日預算用完）503 `metadata_unavailable`，逾時 408。

回應 `data` 是 `ExternalIDMatches`：`externalSource`、`externalId`、`language`、`fetchedAt`、`movies`（`MovieCandidate`，最多 20）、`series`（`SeriesCandidate`，最多 20）與恆為 `true` 的 `needsConfirmation`。查不到時是 200 加兩個空陣列（TMDB find 對未知 ID 回空清單，不是 404）。

## 行為

- 出站只經正式 TMDB 適配器：受控 client（固定主機、DNS 與 IP 檢查、TLS）、共享限流／冷卻／重試與[可配置預算](tmdb-governor.md#可配置預算第-9-階段補缺)、15 秒總期限；回應 24 小時快取，鍵含來源、ID 與語言，同 key 並行 miss 合併為一次請求。
- 應用層核對回應的來源、ID、語言與請求一致、清單不超過 20 筆、候選 ID 為正，否則 503；供應商錯誤只回安全錯誤（取消、逾時或 `metadata_unavailable`），不外洩上游訊息。
- 簡介依請求語言標記 `overviewSource`；空白簡介清空且不標來源。反查不做 `zh-CN → zh-TW → ja-JP → en-US` 簡介回退（避免一次反查變成多次出站），需要回退時對選定候選呼叫電影／劇集預覽。

## 驗證

應用層 `TestFindByExternalIDConfirmationSourcesAndOwnership`、`TestFindByExternalIDRejectsMismatchedProviderAnswersAndHidesErrors`；HTTP `TestExternalIDFindHTTPAuthorityLanguageAndContract`（18 個子案例：權限、三種來源、語言優先順序、非法 ID 與查詢、未知庫、503）；真 TLS `TestExternalIDApplicationThroughActualTLS`；真 TLS＋HTTP＋PostgreSQL `TestFindAndEpisodeImagesThroughTLSHTTPAndPostgres`（庫語言 `ja-JP` 實際送到上游）。把路由的庫語言換成登入者語言時，HTTP 與真 PG 測試都失敗。

真實 TMDB 回應形狀與命中率需要真金鑰，列在擁有者清單 D1 與 D28。
