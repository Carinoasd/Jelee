# TMDB 圖片語言偏好與候選清單

接續[媒體庫語言](tmdb-library-language.md)，正式設定保存有序圖片語言偏好，電影／劇集圖片查詢實際使用這份清單。仍須管理員確認候選，本階段没有下載圖檔、圖片處理或原圖覆蓋。

## 語言設定與升級

`metadata-preferences`回應新增 `imageLanguages`，取值為 `zh`、`ja`、`en`、字串 `null`（無語言標記），依順序表達偏好。清單須1–4項、不重複且無未知／真null值。TMDB目前不支援圖片語言地域區分，簡繁文字偏好皆映射中文圖片 `zh`。[官方圖片語言](https://developer.themoviedb.org/docs/image-languages)

更新範例：

```json
{"language":"zh-TW","expectedRevision":1,"imageLanguages":["zh","en","null"]}
```

舊客戶端省略新欄位會保留目前清單；明確JSON `null`、空陣列與重複／非法語言回400。文字與圖片偏好在同一版本檢查及交易中更新，成功後版本加一並記前後狀態。讀寫回傳的陣列由呼叫者擁有，不共用快取或來源陣列。

新增不可改寫原001–019的schema20。資料庫預設 `zh,ja,en,null`；DB同樣限制單一維度、標準下標、1–4項、合法元素與唯一性。升級保留原文字語言與版本。二進位接受乾淨schema20。降版在表鎖下核對清單仍為預設且全局版本仍為1，否則拒絕；此條件也會保守阻止只有文字設定更新過的媒體庫降版，避免丟失已更新的偏好。

## 正式圖片查詢

- `GET /api/v1/metadata/tmdb/movies/{id}/images?libraryId={uuid}`
- `GET /api/v1/metadata/tmdb/series/{id}/images?libraryId={uuid}`

指定庫時先重新核對session、管理員及庫，再讀取圖片清單。省略庫時按可信使用者語言產生初始清單：中文用 `zh,ja,en,null`、日文用 `ja,en,null`、英文用 `en,null`。明確文字 `language`參數不屬於此介面；圖片偏好透過庫設定更新。沿用容量、嚴格查詢、標準正int32 ID及啟用條件；OpenAPI包含兩條路由與新設定欄位。

正式適配器使用[電影圖片](https://developer.themoviedb.org/reference/movie-images)與[劇集圖片](https://developer.themoviedb.org/reference/tv-series-images)API，經原受控出站／DNS及IP固定／TLS、共享限流／冷卻／重試。`include_image_language`採有序清單，省略會額外過濾資料的 `language`；整段15秒，正文最多1MiB。

只處理海報與背景圖，兩個陣列必須存在且非null，整批最多1000筆。逐筆解析、每筆核對取消，達上限便停止繼續配置候選。回應ID須等於請求；圖片語言欄位必須明確存在，原JSON null轉字串 `null`。其他語言的合法圖片可由供應商回傳，應用層只保留偏好清單內的候選。

檔名限定固定單一檔名及jpg／png／webp副檔名、上限256 bytes，拒任意URL、目錄、穿越、編碼分隔、query／fragment、控制字元與原始憑據回顯。尺寸為1–32768、分數0–10、票數為非負int32；缺失／非法欄位或同類型重複路徑拒整批。圖片網址只由固定官方圖片域名與已驗證路徑組成，不取供應商任意網址。來源頁及UTC取得時間保留。這些驗證只屬圖片候選資料；實際圖檔解碼與G24资源預算尚待實作。

候選按海報／背景圖分組，再依語言清單、分數降序、票數降序與檔名排序。每張 `needsConfirmation=true`，不自行選首張。未知欄位及logo圖不輸出。

## 快取與驗收

圖片共16筆LRU，鍵含資源類型、ID與完整有序清單，24小時由原取得時間到期。插入及每次命中都複製清單與候選陣列；應用層再複製後篩選／排序／標記確認，原供應商資料保持。錯誤與取消不cache，沒有背景任務或圖片位元組快取。

專項覆蓋SQL約束、升級保留、舊客戶端更新、版本衝突／稽核／降版保護，正式HTTP讀庫偏好到圖片查詢及撤銷session。實際TLS供應商＋應用驗證編碼參數、來源、語言排序／篩選、待確認與跨資源快取隔離；單元驗證TTL、清單順序鍵、陣列擁有權、非法回應與1000／1001界線、取消後不發布快取。刻意忽略正式HTTP的庫圖片偏好時，真HTTP／PG驗收失敗；逐位元復原後相關六項PG race專項通過。

完整執行統計及来源hash見[證據](evidence/tmdb-image-preferences.json)。G14.5仍部分完成，季／集圖片、logo、完整前端及原100電影／20劇集刮削／寫入驗收尚缺；欄位鎖與人工／NFO優先、元資料持久化／清除及G24圖片處理仍待完成。完整品牌與實際ABI差異保持門禁。
