# NFO 擴充文字欄位

第28版正式套用 `tagline`、`outline`、`mpaa`、`certification`。連同原有五欄，共支援九個文字欄位。Movie、Series 與可確認為 Movie 的 HomeVideo 使用相同可信來源、完整原文重核及最終交易；NFO-only 或 NFO／TMDB 融合都只推進一次版本、一次稽核。原媒體、NFO 及圖片保持。

## 欄位與來源

`outline` 獨立保存短述，`overview` 保存 plot，兩者不互相截斷或覆蓋。`tagline`、`mpaa`、`certification` 各最多1024 UTF-8 bytes；`outline` 最多16384 bytes。人工空值是明確接管，後續 NFO 不會覆蓋。缺少 NFO 文字不清除既有值。

確認投影拒絕這四種單值標籤的重複、大小寫變體及空標籤混用；歧義有效 XML 不當作損壞回退，在供應商查詢與保存前拒503。通用解析與原文保留仍維持原契約。

## 投影與鎖

新 `extended-text-fields-v1` 固定支援九欄，零文字時須有已知正鎖。各已發布投影保留固定欄位集合；四文字、舊 lock-only 與五欄投影不會偷偷接受新欄位或多出鎖。一般人工欄位清單與供應商接受範圍分別驗證，TMDB 仍提供舊四欄。

四個新欄位可用同名 `lockedfields` 指令；`OfficialRating` 保護 MPAA 與認證，`lockdata=true` 在新版投影保護九欄。鎖來源獨立保存，即使沒有文字也不虛構 NFO 值來源或時間。人工文字接管清除該欄鎖；人工鎖開關保留 NFO 鎖。下一次明確 NFO 確認可再保存正鎖，人工值保持。

## API 與遷移

API、人工 patch 與套用報告上限為九欄；每欄值仍有長度限制。人工修改請求有256 KiB上限，涵蓋九欄最大值的 JSON HTML 跳脫擴張，不接受無界輸入。正式輸出仍用 JSON 序列化文字。

新增 `000028_nfo_text_fields`，001–027 保持。鍵、值及兩種來源證明的欄位／投影綁定一併擴充；嚴格 JSON／hash／數值／時間條件保持。新欄位不能持有舊 NFO 投影或 TMDB 來源。保留任何新欄位（包括人工空值）或新版來源證明時，28→27 會拒絕丟棄資料。

本段只屬文字欄位子集。數值、人物、評分、多值與 IDs 的正式套用仍須保留型別，季集／實際匯入／前端／無損回寫與 G00–G51 未完成。G39 保持部分完成。

## 實測

[證據與來源雜湊](evidence/nfo-text-fields.json)。九個初始Reader／Store／HTTP body回歸各1葉失敗，修正後通過。Windows全Go29套件／3242通過事件含父／474測試略過，原身份保持並新增4個PG頂層。Linux五套件race 1108通過事件含父、零失敗零略過。

完整PG race 764通過事件含父、399.771秒、零失敗零略過；原生worker另跑11通過事件含父，涵蓋所有PG略過身份。四table失敗原子回滾、manual九欄接管、OfficialRating缺值鎖、舊版本及provider拒新欄位、retained降版拒絕通過。完整HTTP／TLS／NFO／PG 73.697秒1通過0失敗0略過，九欄融合／來源／鎖、最大JSON跳脫、人工優先、duplicate503及原120合成寫入保持。停用正式tagline投影，完整路徑抓到漏欄而失敗，逐位元復原後通過。

vet／產品build／增量brand／gitignore通過，全量brand仍14735違規／186合法保留；54份已發布遷移、原授權與需求、五待授權核心保持。
