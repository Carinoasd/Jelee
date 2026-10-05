# NFO 多來源評分

第34版新增 `multi-source-ratings-v1`，固定24欄：九文字、四數值、八字串列表、演員、供應商識別碼及 `ratings`。已發布投影保持原欄位集合。

`facts[].field = ratings` 是有順序的 JSON 物件陣列，以 PostgreSQL JSONB 保存。單一一般評分 `rating` 和使用者評分 `userRating` 保持原有0–10契約。

| 欄位 | 契約 |
| --- | --- |
| name | 可省略或為空字串；最多1024個 UTF-8 位元組。未命名來源保持未命名 |
| value | 必填、有限數值，介於0與該來源的尺度上限 |
| max | 可省略或 null；提供時須為大於0且不超過1000000的有限數值。未提供時以10為有效尺度，但保留缺省表示 |
| votes | 可省略或 null；提供時為0–2147483647的整數。明確零票數與缺省保持區別 |
| default | 可省略，布林值；保留原來源的預設旗標 |

最多128筆，來源名稱合計最多16384個 UTF-8 位元組。拒絕 NUL、未知 JSON 屬性、非法型別與超限資料；保留來源順序和同名來源，不換算或壓成單一評分。

確認套用拒絕同一 `<rating>` 的重複 value／votes，也拒絕大小寫折疊後重複的 name／max／default 屬性。一般唯讀相容 parser 保持原行為；原始 NFO 不會被修改。來源觀察深複製 max／votes 指標，重核包含其值與缺省狀態。

人工省略 value 保留評分；null 和空陣列為明確清除，保持各自表示。只改 locked 保留来源證明，人工給值或清除解除值與鎖證明。後續確認保留人工優先權，也可建立新的正鎖。Ratings／SourceRatings 指定鎖保護整個多來源評分欄位；缺值鎖不虛構 NFO 值来源或時間。

```json
{"expectedRevision":2,"facts":[{"field":"ratings","value":[{"name":"imdb","value":7.5,"max":10,"votes":123,"default":true},{"name":"custom","value":85,"max":100}]}]}
```

OpenAPI 宣告七種 fact 變體、15項人工 facts、24欄套用報告與新版来源投影。最大九文字＋15種 fact 的合法混合請求經 JSON 跳脫後仍接受，請求上限保持2 MiB。

新遷移只新增第34版；001–033共66份已發布 SQL 保持。保留多來源評分（包括人工清除）、評分鎖或新版投影證明時拒絕降版；旧識別碼資料可往返34→33→34。

## 驗證

第34版保存多來源評分的 name／value／max／votes／default 與來源順序；缺省尺度和零票数保持區別，人工清除、來源、獨立鎖及確認觀察共交易。固定24欄、15種facts與七種API變體；001–033共66份SQL保持。完整HTTP驗收包含可空max／votes、最大合法混合請求及120筆合成確認寫入。五份初始失敗、票數傳遞停用的實際失敗與逐位元復原後完整通過均保存。Windows全套、Linux race、PG與原生worker、vet／建置及增量品牌通過；全量品牌14735與既有ABI差異仍未解決。

{"windows": {"passedPackages": 29, "passedTestEventsIncludingParents": 3264, "skippedTestEvents": 489, "elapsedSeconds": 8.657}, "native": {"passedPackages": 5, "passedTestEventsIncludingParents": 1130, "skippedTestEvents": 0, "elapsedSeconds": 3.452}, "full-pg": {"passedPackages": 1, "passedTestEventsIncludingParents": 809, "skippedTestEvents": 0, "elapsedSeconds": 474.239}, "native-pg": {"passedPackages": 1, "passedTestEventsIncludingParents": 11, "skippedTestEvents": 0, "elapsedSeconds": 15.685}, "e2e-final": {"passedPackages": 1, "passedTestEventsIncludingParents": 1, "skippedTestEvents": 0, "elapsedSeconds": 76.128}}

完整PG與原生worker驗證後，正式資料庫／遷移／worker來源未變；最後修正HTTP解碼器的可空值規則，再跑Windows全套、Linux五套件race、完整HTTP及全部靜態檢查。Windows略過的PG與HTTP測試由實際執行補足。完整證據見[驗收紀錄](evidence/nfo-ratings.json)。

其他NFO欄位、季集、實際匯入、前端與無損回寫仍待接續，需求總計保持4完成／184部分／148阻塞。
