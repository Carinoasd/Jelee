# NFO 片長與評分保存

第 30 版新增 `numeric-facts-v1`，包含九個文字欄位與四個數值欄位。已發布的四欄、五欄、九欄與年份投影保留原本的集合。

| API facts 欄位 | JSON 型別與範圍 | NFO 值／鎖別名 |
| --- | --- | --- |
| year | 整數，1–9999 | year；Year／ProductionYear 鎖 |
| runtimeMinutes | 整數，0–10000000 | runtime，支援 min／minutes；Runtime／RuntimeMinutes 鎖 |
| rating | 數字，0–10 | rating／communityrating；Rating／CommunityRating 鎖 |
| userRating | 數字，0–10 | userrating；UserRating 鎖 |

Reader 保留零值、支援評分的小數逗號，並拒絕重複的直接子元素及別名衝突。巢狀多來源 ratings 不會混入這些單值欄位。數值經來源選擇、所有權與二次觀察核對後，以 JSON 數字保存於 API facts 和 PostgreSQL JSONB，沒有轉成文字欄位。

文字、數值、獨立欄位鎖、確認觀察、版次與稽核一起提交。新投影的全域鎖涵蓋十三個欄位；只有數值欄位鎖而沒有值時，回應 null 與 existing 來源及獨立鎖證明，不虛構 NFO 值來源或更新時間。

人工修改最多接受四個不重複 facts，可與文字一起修改。省略 value 保留值，數字表示人工接管，null 表示明確人工清除。人工給值或清除移除該欄位的 NFO 值與鎖證明；只改 locked 旗標保留 NFO 證明。後續明確 NFO 確認仍可記錄新的正鎖，但不覆蓋人工數值或清除。

```json
{"expectedRevision":2,"facts":[{"field":"runtimeMinutes","value":0},{"field":"rating","value":null},{"field":"userRating","value":9.25}]}
```

JSON null 只允許在 facts[].value。錯誤型別、重複欄位、超出範圍的值與其他位置的 null 仍拒絕。內部人工輸入亦拒絕非標準的空白 null，避免解碼成評分零值。TMDB 仍只產生原有四個文字欄位。

第 30 版擴充既有數值資料表與來源／鎖約束。保留新增數值資料（包括人工 null）、新數值鎖，或任何 numeric-facts-v1 證明時，拒絕降到第 29 版。只有舊年份投影的資料可往返 30→29→30，來源與鎖保持。第 1–29 版遷移保持原樣，歷史降版測試先經 30→29。

本段範圍是明確確認套用與人工修改。人物、多值、季集來源、掃描後自動匯入、前端與無損 NFO 寫回仍待完成；G39.3／G39.6 保持部分完成。

## 實測

Windows 全 Go 29 套件／3253 通過事件（含父測試）／480 測試略過；Linux 五套件 race 1119 通過事件，零失敗零略過。完整 PostgreSQL race 780 通過事件，零失敗零略過；原生 worker 另跑 11 通過事件。Windows 的 PostgreSQL 與完整 HTTP 略過身份均有實際通過紀錄。

完整 HTTP／TLS／NFO／PostgreSQL 74.007 秒通過，涵蓋十三欄同次套用、零值與上限、混合人工修改／清除、更新鎖與無效型別拒絕，既有 120 筆合成確認寫入保持。片長、評分、使用者評分各有實際初始失敗；空白 null 轉零錯誤也先重現再修正。刻意停用正式片長投影時，完整流程缺少 typed fact 而失敗；逐位元復原後完整通過。

五個融合寫入環節失敗全回滾、人工來源優先、數值 lock-only 不虛構值、保留數值／人工 null／新證明拒降、舊年份投影往返保持皆通過。vet／產品 build／增量品牌／gitignore 通過；全量品牌仍 14735 違規／186 合法保留，ABI 相容性差異仍待處理。58 份已發布遷移、原授權／需求與五個待授權核心保持；本段沒有 C# 變更。詳見[驗證證據](evidence/nfo-numeric-facts.json)。
