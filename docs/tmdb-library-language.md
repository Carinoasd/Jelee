# 媒體庫元資料語言設定

接續[使用者偏好與回退](tmdb-language-fallback.md)，正式管理員可讀取及更新媒體庫語言。Runtime把既有TMDB服務與PostgreSQL偏好儲存接線，保持同一個受控供應商及其生命週期。

## 設定與查詢

- `GET /api/v1/libraries/{id}/metadata-preferences`
- `PUT /api/v1/libraries/{id}/metadata-preferences`，正文例如 `{"language":"zh-TW","expectedRevision":1}`。

回應含 `libraryId`、四語 `language` 與 `revision`。媒體庫預設簡中／版本1。PUT必须提供目前版本；成功後版本加一，即使語言未改也留下更新紀錄。舊版本回409，不覆盖較新的設定。未知／空語言、遺漏版本、未知／重複JSON欄位與非合法UUID均拒絕。路由沿用帳號＋TMDB啟用條件、管理員授權、容量及嚴格JSON限制。

六個TMDB候選查詢增加可選 `libraryId`。語言優先序為明確 `language` → 指定媒體庫的設定 → 已認證使用者偏好 → 簡中。即使明確指定語言，也須验证指定媒體庫與目前管理員session；不存在、失效或無權限時不呼叫供應商。不指定媒體庫時沿用前一階段行為。既有四語回退、欄位來源時間及原始快取隔離保持。

## 資料庫與回復

新增第19版遷移，既有001–018不改寫。`libraries`新增四語約束的 `metadata_language` 與有界正整數 `metadata_preferences_revision`。既有媒體庫升級後預設簡中／版本1。

每次讀寫在短交易重新核對管理員、session及媒體庫。更新鎖住資料列、核對版本，並在同一交易寫入 `library.metadata_preferences_changed` 的前後狀態與操作者；失敗或衝突不留下成功更新紀錄。交易在供應商查詢前結束，不持有資料庫鎖等待網路。

降版先鎖媒體庫表；只允許全數保留預設語言及初始版本時移除新欄位。任何更新過的設定都阻止降版，包含改回簡中的設定，避免默默丟失版本及使用者決策。二進位只接受乾淨schema19；升級需先執行遷移。原始媒體、NFO與圖片沒有寫入。

## 驗收與尚缺範圍

HTTP矩陣覆蓋六路由的庫偏好／明確覆蓋與失效session阻擋，以及設定讀寫的401／403、409、嚴格正文和啟用條件。真PostgreSQL驗證競爭更新只有一個成功、持久化與稽核、一般使用者／失效session拒絕、資料庫欄位約束、舊庫預設、乾淨升降版及保留設定時拒絕回復；另以正式HTTP＋應用＋資料庫驗證完整設定到查詢語言的路徑。

執行數據、反向驗收與來源hash見[證據](evidence/tmdb-library-language.json)。圖片語言偏好、其他文字回退、完整前端、刮削worker及寫入／欄位鎖仍待完成，G14.5保持部分完成。
