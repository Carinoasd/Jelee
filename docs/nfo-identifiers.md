# NFO 供應商識別碼

第33版新增 `provider-identifiers-v1`，固定23欄：九文字、四數值、八字串列表、演員與 `uniqueIds`。已發布投影保留原欄位集合。

`facts[].field = uniqueIds` 的值是有順序的 JSON 物件陣列，以 PostgreSQL JSONB 保存。

| 欄位 | 契約 |
| --- | --- |
| type | 必填、非空白，最多64個 UTF-8 位元組 |
| value | 必填、非空白，最多1024個 UTF-8 位元組 |
| default | 可省略，布林值；保存原來源的預設旗標 |

最多128筆，type 與 value 合計最多16384個 UTF-8 位元組。拒絕 NUL、未知 JSON 屬性、錯誤型別及超限資料。識別碼只保存字串，不觸發供應商查詢。

Reader 支援 `<uniqueid type="..." default="true">` 與既有 imdbid／tmdbid／tvdbid／id 別名，id 對應 IMDb。供應商名稱沿用 parser 的小寫表示，來源順序與相同值的重複項保持。同一供應商有不同值時，確認套用拒絕，NFO-only 與融合路徑均不保存、不查詢其他來源；一般唯讀相容 parser 保持原警告行為。

觀察資料獨立持有識別碼切片；重核比較 type、value 與 default，並沿用來源候選、實體身分及完整位元組檢查。

人工省略 value 保留值；null 和空陣列是明確清除，兩種表示保持。人工給值或清除解除 NFO 值與鎖證明；後續確認維持人工優先權。ProviderIds／UniqueId／UniqueIds／ImdbId／TmdbId／TvdbId 都保護整個識別碼欄位。指定鎖而沒有值時，保留獨立正鎖，不虛構 NFO 值來源或時間。

```json
{"expectedRevision":2,"facts":[{"field":"uniqueIds","value":[{"type":"imdb","value":"tt1234567","default":true},{"type":"custom","value":"vendor-42"}]}]}
```

OpenAPI 宣告識別碼結構、人工清除與兩種來源證明的新版投影。人工 facts 上限為14，請求上限維持2 MiB。

新遷移只新增第33版。保留識別碼資料（包括人工清除）、識別碼鎖或新版投影證明時拒絕降版；舊演員資料可往返33→32→33。001–032的64份已發布 SQL 保持原樣。

本段已完成下列本地驗證；远端 CI 另行核對。多來源評分、季集與實際匯入等工作仍未完成。


## 實測

Windows全Go29套件／3260通過事件（含父測試）／487略過；Linux五套件race 1126事件，零失敗零略過。完整PG race 802事件／482.614秒，原生worker另11事件，皆零失敗零略過。Windows略過的PG／HTTP測試已核對实际執行。

完整HTTP／TLS／NFO／PG驗收涵蓋識別碼型別與順序、別名與自訂來源、default旗標、供應商衝突拒絕、OpenAPI、人工null／空陣列與重新確認、無效型別與未知屬性拒絕、原始來源保持。既有120筆合成確認寫入保持。五個寫入環節失敗均回滾；人工優先與正鎖重建、缺值ProviderIds獨立鎖、舊演員資料往返與保留新資料拒降通過。

四份實際初始失敗已保存。正式Reader的default傳遞刻意停用會讓完整HTTP測試失敗；逐位元復原後完整測試通過。vet、三個產品建置、增量品牌0／gitignore0通過；全量品牌仍14735違規／186合法保留，ABI仍待處理。64份已發布遷移、需求原文／LICENSE及五個直播核心保持。[實測證據](evidence/nfo-identifiers.json)。
