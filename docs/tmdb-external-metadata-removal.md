# 移除外部元資料與 TMDB 資料使用條款

對應需求 G14.7 原文：「记录数据来源与抓取时间；提供“移除外部元数据”能力；遵守 TMDB 署名与缓存要求。」本頁說明「移除外部元資料」的正式入口，並整理目前程式對 TMDB 資料的保存、快取與署名做法。沒有新增遷移，沿用現行 schema。

## 移除入口

管理員呼叫 `DELETE /api/v1/items/{id}/metadata/external?expectedRevision=N`。依既有 DELETE 慣例不收請求內容，版本前置條件放在必填查詢參數 `expectedRevision`，命名與其他元資料寫入相同。這個入口不需要 TMDB 金鑰，也不呼叫供應商：金鑰撤除後仍可清除已保存的外部資料。

- 單一交易內鎖定項目、比對版本、刪除欄位、同步目錄標題、推進一次版本並寫一筆 `item.external_metadata_removed` 稽核。版本不符回傳 409，不做任何修改。
- 非管理員 403，項目不存在 404，缺少或非正規十進位的 `expectedRevision`、多餘查詢參數或非空內容 400。錯誤碼沿用既有 `forbidden`／`not_found`／`conflict`／`invalid_request`，不新增 i18n 字串。
- 回應 `MetadataRemoveResult`：`metadata` 為移除後的欄位，`removed` 列出已刪除的欄位，`skipped` 列出保留的外部欄位與原因（`locked`／`required`）。沒有可移除的欄位時仍推進版本並記錄稽核，與確認套用時「全部略過也推進版本」一致。
- 稽核前像只記錄被移除欄位的供應商來源（資源、ID、標準頁面、語言、取得時間），不寫入被移除的供應商文字；後像是移除後的欄位。較早的 `item.tmdb_metadata_applied` 稽核仍保有當時的值，本功能不改寫歷史稽核，是否另行清理需擁有者決定。

## 移除與回退規則

外部資料目前只存在 `item_metadata_fields` 中 `source='tmdb'` 的標題、原名、簡介、日期四欄；事實表的來源約束不允許 `tmdb`，圖片也沒有逐項目保存 TMDB 來源，因此兩者不需處理。

- 鎖定欄位保留：Jelee 欄位鎖、NFO 值鎖或獨立 NFO 鎖意圖（與 G14.6／G39 相同的鎖定判斷）任一成立，該 TMDB 值與來源原樣保留，列為 `skipped: locked`。鎖是管理員的明確決定；要移除須先解除鎖再呼叫一次。
- 人工、NFO 與 existing 來源不受影響。
- 回退與融合規則一致：TMDB 從不覆蓋人工、NFO 或非空 existing 值，所以被 TMDB 寫入前，該欄必然是空的或不存在。移除後該欄回到不存在。
- 標題必填。TMDB 只有在 `replaceExistingTitle` 明確要求下才取代匯入時的標題，原值沒有另外保存；移除後回退為本機名稱：媒體檔名（去掉副檔名）優先，其次為目錄來源名稱（`.` 表示庫根目錄名稱）。來源 `existing`，同交易更新目錄標題。找不到本機來源時保留 TMDB 標題並列為 `skipped: required`，管理員可用人工修改接管（人工接管會清除供應商來源）。
- NFO 值不在此入口重新讀取。NFO 優先於 TMDB，下一次 NFO 套用或融合會依既有規則寫入。
- TMDB 套用時把 HomeVideo 改為 Movie 的分類不還原；分類不是外部元資料欄位。
- 任一 SQL 或稽核失敗整筆回滾，欄位、目錄標題、版本與稽核都不留下部分結果。

## TMDB 資料使用與快取條款

需求原文只寫「遵守 TMDB 署名与缓存要求」，沒有列出具體期限或文字。以下分成「程式目前的實際行為」與「需擁有者向官方確認的條款」，不以本頁代替官方條款。

### 目前實際行為

- 快取期限：供應商候選資料只放在行程記憶體內，不持久化，重啟即清空。電影與劇集各最多 256 筆（季 16 筆），以 ID＋語言分區，自取得時間起 24 小時到期，命中不延長；錯誤與認證失敗不快取。搜尋結果不快取。見[電影快取](tmdb-movie-preview.md)、[劇集快取](tmdb-series-preview.md)、[季／集](tmdb-season-episode-preview.md)。
- 持久保存：只有管理員明確確認套用的四個欄位寫入資料庫，每欄保存 TMDB ID、資源類別、官方頁面、要求語言與 UTC 取得時間（G14.7「记录数据来源与抓取时间」）。這些資料沒有自動到期，也不會自動重新抓取；清除方式為本頁的移除入口。
- 署名：設定 TMDB 金鑰後，`/api-docs` 的 Credits 區塊顯示 TMDB 官方未修改藍色短標誌（瀏覽器直接向官方網域載入）、官方連結及聲明「This product uses the TMDB API but is not endorsed or certified by TMDB.」依據見[官方 FAQ](https://developer.themoviedb.org/docs/faq) 與[官方標誌頁](https://www.themoviedb.org/about/logos-attribution)。完整前端的署名展示尚未完成。
- 移除方式：逐項目呼叫上述 DELETE 入口；記憶體快取由 24 小時到期或重啟清除，目前沒有手動清空記憶體快取的入口。

### 需擁有者確認

下列項目需求原文沒有細節，本專案也沒有保存官方條款原文，需由擁有者對照 TMDB 現行 API 使用條款確認後再補：

- 已持久化的 TMDB 欄位是否有保存期限上限，或需要定期重新整理；目前程式不會自動到期。
- 記憶體快取 24 小時是否符合官方要求。
- 署名需出現的位置（目前只在 API 文件頁）與前端呈現方式。
- 歷史稽核中保留的 TMDB 文字是否需要清理或遮蔽。
- 商業使用、再散布或批次清除全部項目外部資料的需求；目前只有逐項目移除。

## 驗證

- 單元測試：`internal/domain/metadata_remove_test.go`（鎖定判斷、輸入範圍、本機標題推導、結果複製隔離）、`internal/app/metadata_remove_test.go`（不需供應商、驗證先於儲存層、衝突傳遞、回應隔離）、`internal/adapter/http/metadata_remove_test.go`（OpenAPI 不依賴 TMDB 金鑰、DELETE 無內容、必填版本、`expectedRevision` 正規解析）。
- 真 PostgreSQL：`internal/adapter/postgres/metadata_remove_test.go` 驗證只移除 tmdb、保留 NFO／人工、鎖定保護與解鎖後移除、標題回退與目錄同步、無本機來源時保留標題、版本衝突、每次一筆稽核且不含被移除文字、欄位刪除與稽核失敗的完整回滾，以及實際 HTTP 的 401／403／404／409／400 與成功路徑。
- 反向驗證：暫時讓鎖定欄位也可移除，鎖定測試失敗；暫時拿掉版本比對，過期版本與 HTTP 409 測試失敗；恢復後全部通過。

G14.7 仍屬部分完成：移除入口已有，但前端、批次清除與官方條款確認尚未完成。
