# TMDB 季與單集候選資料

接續[劇集查詢](tmdb-series-preview.md)，正式管理員API增加季清單與单集詳細資料。原始G14的庫寫入、欄位鎖、置信度及完整刮削仍未完成。

## 介面與資料驗證

- `GET /api/v1/metadata/tmdb/series/{id}/seasons/{season}?language=zh-TW`
- `GET /api/v1/metadata/tmdb/series/{id}/seasons/{season}/episodes/{episode}?language=zh-TW`

相同帳號啟用＋TMDB憑據設定條件、bearer認證、管理員與請求容量；未配置時路由／OpenAPI均隱藏。ID為正int32，季號為非負int32（第0季可查特別篇），集號為正int32；只接受標準十進位字串，拒正號／前導零／負號／溢位。唯一查詢參數language是既有四語，省略時zh-CN，未知／重複／空值／不合法語言回400。

正式適配器使用[官方季介面](https://developer.themoviedb.org/reference/tv-season-details)及[官方集介面](https://developer.themoviedb.org/reference/tv-episode-details)，經既有受控transport、限流／共享冷卻與重試，總15秒／正文1MiB。來源網址使用固定官方域名與標準編碼數字，不取request Host或上游任意URL。

季回應必须有正provider ID、名稱、明確season_number與非null episodes陣列；最多1000集。每集须有正ID／episode_number、明確season_number且與所查季一致。拒絕重複provider ID或集號，整批驗證後才輸出，不回部分清單；空陣列代表已知空季，缺失／null不是空季。直接單集必須episode_number與所查集一致，season_number與所查季一致。若上游提供show_id，必須與所查劇集一致；官方回應不一定包含父劇集ID，缺失時父ID按已驗證的請求路由歸屬，沒有假稱所有回應都有父ID可以核對。

選定文字與日期採既有候選欄位限制（標題1KiB、簡介16KiB、日期合法或空），拒控制字元與原始API key回顯。未知欄位不回傳／抓取。季与每集均附TMDB來源、標準來源頁、UTC取得時間、語言及系列／季／集身份。404保留not_found，其餘供應商失敗轉固定安全503，取消／期限保留408；不輸出原始錯誤正文／URL／憑據。

## 快取與資源

季最多16筆，單集最多256筆；各自獨立LRU、24小時從取得時間到期，命中不延長。鍵含series ID、season number、episode number及language，避免不同劇集／季／集／語言混淆。沿用共用型別化LRU，電影／劇集原各256筆行為保持。

季含可變集陣列，入庫與每次命中都複製陣列；呼叫者修改首次回傳或快取命中的集資料都不能污染快取。單集值只有不可變字串／數字／時間。錯誤／取消資料不cache；季清單不預填單集詳細快取，避免把不同取得步驟的來源混用。不建立背景工作，不合併並行miss，不持久化。大季超过1000集或1MiB回應明確拒絕；不截斷後冒充完整清單，個別集仍可依確定號碼取得。這些固定容量尚未完成可配置provider預算。

## 驗證與剩餘工作

第0季、正常單集、HTTP權限、ID／季／集／語言與query、回應envelope、設定與OpenAPI及Movie schema保持通過。適配器測試核對季／集號不符、缺季號／清單、null、重複ID与不同ID但相同集號、超过1000集、錯日期／憑據、404／401／429／5xx錯誤不cache、取消及非法輸入；應用層验证參數及安全錯誤。

季與單集快取、不同父劇集／語言的鍵、24小時到期、季16容量與首次／命中修改隔離通過。既有LRU／新舊回應／並行電影回歸亦保留。

正式adapter透過自有TLS與受控client測試专用DNS／CA／dial，接實際app驗證第0季、來源／UTC時間、三次季讀一次上游、兩次單集一次上游、首次與命中陣列修改隔離，錯集號回503。移除正式命中複製接線時真TLS負例失敗（cache hit mutated episode list），finally逐位元恢復，相關四包完整回歸通過。

完整Windows Go29包、3,019 pass事件含父／435略過，skip身份與前次完整清單逐項一致；完整vet／產品build、Linux六包race通過（摘要不列skip，未宣稱零略過）。見[機器證據](evidence/tmdb-season-episode-preview.json)。

目前是明確ID及季／集號的候選取得，不包含媒體檔名解析或自動配對。名稱模糊置信度、IMDB／TVDB、使用者／庫語言回退、NFO／人工值優先、欄位鎖、worker／庫寫入及清除外部元資料、圖片與完整前端／TMDB條款验收仍待完成。原媒體／NFO／圖片／已發布遷移未修改，G14.4仍部分完成，品牌與ABI門禁保持。

## 後續語言行為

目前省略 language 時採已認證使用者偏好，缺失簡介與空候選頁已加入四語回退；完整規則與尚缺範圍見 [語言回退](tmdb-language-fallback.md)。以上原階段驗收記錄保留其當時範圍。

## 2026-10-04 更新：並行 miss 合併

本文件先前所寫的「沒有合併並行 miss」與「不建立新 goroutine」已不再成立。`internal/adapter/metadata/flight.go` 的 `flightGroup` 讓同一快取 key 的並行 miss 只發一次上游請求，期間每個進行中的 key 有一個最多存活 15 秒的 goroutine。共享請求只帶 15 秒總預算，不繼承個別呼叫者的期限，所以期限較短的呼叫者會在自己的期限到時收到 `DeadlineExceeded`，請求則繼續替其他等待者跑完。只有在請求成功、且仍有未取消的等待者時才寫入快取；錯誤一律不快取。
