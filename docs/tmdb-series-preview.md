# TMDB 劇集搜尋、詳細資料與快取

接續[電影候選查詢](tmdb-movie-search.md)，正式管理員API提供劇集搜尋及詳細資料；季與單集、媒體庫寫入與完整刮削驗收尚未完成。G14仍部分完成。

## 行為

- `GET /api/v1/metadata/tmdb/series?query=Series&year=2024&language=zh-TW`：year選填，代表劇集首播年份；必須通過既有bearer與管理員權限。
- `GET /api/v1/metadata/tmdb/series/{id}?language=zh-TW`：正int32標準十進位ID；回傳TMDB劇集來源頁、UTC取得時間、標題／原名／簡介與firstAirDate。
- 兩者使用相同帳號啟用＋TMDB憑據設定條件、帳號請求容量、四語及非法／重複／未知query拒絕政策。未配置時路由及OpenAPI一起隱藏；未知上游欄位不回傳或抓取。

依[官方TV搜尋](https://developer.themoviedb.org/reference/search-tv)，使用first_air_date_year限定首播年份；不使用會涵蓋所有單集播出年份的year參數。第一頁、非成人、最多20候選、名稱256bytes、有效UTF-8、去除前後空白後拒控制字元；query以url.Values編碼。所有候選都標記needsConfirmation=true，標題／原名比較及首播年份比較僅供人工檢視。搜尋不自動選首筆或取得每筆詳細資料。

[官方劇集詳細介面](https://developer.themoviedb.org/reference/tv-series-details)使用 `/3/tv/{series_id}`。瀏覽工具讀該頁逾時，已由同一官方URL的HTTP回應核對正式endpoint，沒有改用非官方資料。劇集與電影共用受控transport／250ms間隔／四容量／重試及429／503冷卻；15秒總期限、1MiB正文。整批ID唯一／日期／選定欄位容量與原始憑據回顯驗證；404只在詳細查詢保留為not_found，其他供應商失敗為固定安全錯誤，context取消保留。

## 快取與模型

電影與劇集各自最多256筆、各自ID＋語言索引、24小時從取得時間到期。兩個快取共用型別化LRU實作及時間契約，但保有獨立索引，避免同ID混淆；電影原行為與回歸保留。資料只含不可變字串、數字與時間，回傳值不共用可變陣列，較舊並行結果不能覆蓋新資料。錯誤／認證不cache、命中不延長TTL；搜尋結果仍不cache／合併並行miss。沒有背景工作或新增goroutine，也沒有修改NFO／圖片／原媒體或資料庫遷移。

劇集使用自己的SeriesCandidate／SeriesMatch及firstAirDate欄位；MovieCandidate的releaseDate、MovieMatch的movie與原OpenAPI schema保持。搜尋輸入共用MetadataSearchInput規則，正式runtime依賴MetadataProvider編譯期涵蓋電影與劇集，不提供只有半個服務的API。

## 驗證

正式adapter經自有TLS及既有受控transport測試專用DNS／CA／dial，連入實際app查出20劇集，逐筆保留首播年份／標題比較与待確認，搜尋沒有抓詳細；明確指定同ID的劇集兩次只到上游一次，再取同ID電影仍到其自己的endpoint。只繞過正式劇集快取命中時，真TLS負例失敗，觀察search=1／series=2／movie=1；finally逐位元恢復，相關四套件完整回歸通過。

正反權限、ID／年份／語言／query、設定與OpenAPI、schema隔離、取消與安全錯誤；TV上游endpoint／首播年參數／轉義、日期／ID／正文契約、錯誤不cache、空／null／錯页／重複ID及21候選拒絕；電影／劇集與語言隔離、TTL精確到期／不延長、LRU容量及既有並行回歸通過。20合成劇集只是候選查詢契約，不能替代完整100電影／20劇集刮削與鎖定欄位驗收。

完整Go／vet／產品build、Linux六包race与來源hash／負例恢復／略過清單見[機器證據](evidence/tmdb-series-preview.json)。完整品牌與ABI門禁仍保持，沒有增加豁免或合併PR。

下一步仍需季／集號與資料、IMDB／TVDB、模糊匹配置信度、使用者／庫語言回退、NFO／人工值優先與欄位鎖、worker及媒體庫持久化／清除元資料、圖片與完整前端／TMDB條款驗收。不得把候選API當作全刮削完成。

## 後續語言行為

目前省略 language 時採已認證使用者偏好，缺失簡介與空候選頁已加入四語回退；完整規則與尚缺範圍見 [語言回退](tmdb-language-fallback.md)。以上原階段驗收記錄保留其當時範圍。
