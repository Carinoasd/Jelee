# TMDB 電影名稱／年份候選查詢

接續[電影詳細資料與快取](tmdb-movie-preview.md)，正式管理員API增加名稱及選填上映年份查詢。這是G14.4的一部分；完整自動匹配、劇集与寫入仍未完成。

## 行為

`GET /api/v1/metadata/tmdb/movies?query=Movie&year=2024&language=zh-TW` 採相同帳號啟用＋TMDB憑據設定條件、bearer認證、管理員檢查及既有請求容量。未配置時API與OpenAPI同步隱藏。輸入query去除前後空白後必須非空、有效UTF-8、最多256bytes且不含控制字元；year選填四位十進位1000–9999。year=0／空值不能取代省略。language只接受既有四語，省略時zh-CN。未知、重複或編碼不合法的query參數回400。

依[官方電影搜尋介面](https://developer.themoviedb.org/reference/search-movie)，只請求 `/3/search/movie` 的第一頁，明確指定include_adult=false；選填年份傳入primary_release_year。所有值以url.Values編碼，標題含`&api_key=...`不會覆蓋真正憑據或增加上游參數。使用同一runtime適配器／受控transport／限流與共享冷卻，最多15秒、1MiB正文；不建立背景工作。

回應要求page=1、明確非null results陣列、最多20筆及唯一正int32 ID。整批先完成驗證，任何候選缺標題、日期非法、超過欄位容量或回顯原始憑據皆拒絕，不回部分資料。電影詳細與搜尋共用候選欄位驗證／來源標準頁／取得時間邏輯。

應用層逐筆提供exactTitle（trim後標題或原名EqualFold比較）、exactYear（有指定年份且上映日期年份相同）及needsConfirmation=true。這兩個比較欄位供人工檢視，沒有數值置信度、模糊匹配、選出第一筆或自動寫入。即使標題與年份都一致，仍要求確認。沒有年份或上游缺上映日期時exactYear=false。

空結果回200與空candidates陣列。搜尋本身不觸發各候選的詳細請求；管理員選定ID後可用既有詳細API。所有候選的來源及取得時間隨結果回傳。搜尋結果本段不快取；只有先前詳細API的ID＋語言快取，不能聲稱已合併並行搜尋或持久化所有元資料。

## 驗證與限制

管理員／一般使用者／未登入、名稱空值／UTF-8／控制字元／長度、年份格式與重複、語言、未知參數、輸出envelope、設定與OpenAPI一致，以及供應商失敗通過。適配器驗證來源URL參數轉義、頁數、20筆上限、重複／非法ID、無效JSON／日期／憑據與取消；詳細資料原回歸仍通過。

標題／原名大小寫比較、不同年份、不同標題及缺年份／日期通過；另以100個合成電影名稱驗證比較与待確認欄位，這只是應用層契約，不代表原始「100電影／20劇集」全刮削矩陣完成。

正式adapter經自有TLS服務與受控transport，連到實際app查出兩筆：第一筆標題／年份一致，另一筆都不符，兩者均待確認；搜尋未觸發detail，後續明確指定ID才抓一次詳細資料。只移除正式app的待確認標記時真TLS負例失敗，finally逐位元恢復，相關四套件完整重跑通過。

Windows完整Go29套件、2,961個pass事件（含父測試）、435個略過；略過身份與既有完整清單逐項相同。完整vet、產品build與Linux六套件race通過；Linux摘要未列略過，不宣稱零略過。見[來源與執行證據](evidence/tmdb-movie-search.json)。

尚缺劇集／季／集、IMDB／TVDB解析、名稱正規化／模糊置信度、媒體庫與工作佇列寫入、使用者／庫語言回退、NFO／人工值與欄位鎖。來源／UTC時間與API docs Credits已有實際輸出，但移除外部元資料、完整前端歸屬、完整TMDB條款／快取要求验收仍缺。G14.4及G14.7維持部分完成。原始媒體／NFO／圖片／既有遷移沒有修改，品牌／ABI失敗仍保留。

最後檢查將詳細資料的取消檢查置於共用欄位驗證之後、快取寫入之前；確定時鐘在驗證時取消的回歸確認不發布候選快取。最终完整Go／vet／build及Linux race已重新驗證，證據使用最後來源hash。
