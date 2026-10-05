# NFO 多值字串保存

第31版新增 `string-lists-v1`：九個文字欄位、四個數值欄位與八個有順序的字串列表。已發布投影保留原本的欄位集合。

| API facts 欄位 | NFO 元素 | 常用欄位鎖別名 |
| --- | --- | --- |
| genres | genre | Genre／Genres |
| tags | tag／style | Tag／Tags／Style |
| studios | studio | Studio／Studios |
| countries | country | Country／Countries／ProductionLocations |
| languages | language | Language／Languages |
| directors | director | Director／Directors |
| writers | writer／credits | Writer／Writers／Credits |
| producers | producer | Producer／Producers |

沿用通用 NFO parser 的重複元素與斜線分隔相容行為。列表保留順序與重複值，以 JSON 陣列保存於 API facts 和 PostgreSQL JSONB。正式來源選擇、實體身分與二次觀察核對包含列表；回應切片與其中的字串切片都由呼叫者獨立持有。

每列最多128個值；每值最多1024個 UTF-8 位元組，整列合計最多16384個位元組。拒絕空白、NUL、非字串及超過限制的值，不截斷或保存部分結果。管理請求限制為1 MiB，以容納九文字與八列表在 JSON HTML 轉義後的最大合法修改。

新版全域鎖涵蓋21個欄位。指定列表鎖沒有值時，保存獨立鎖證明並回應 null／existing，不虛構 NFO 值來源或更新時間。文字、數值、列表、來源、鎖、確認觀察、版次與稽核共用交易。

人工 value 省略時保留值；null 和空陣列都是明確人工清除，兩種表示保留各自型別。人工給值或清除會移除該欄位的 NFO 值與鎖證明；只改 locked 保留 NFO 證明。後續明確 NFO 確認仍可記錄新的正鎖，並保留人工值與清除。

```json
{"expectedRevision":2,"facts":[{"field":"genres","value":["Drama","Mystery"]},{"field":"tags","value":[]}]}
```

第31版擴充既有 facts／來源／鎖約束，新增不可變的有界列表驗證函式。保留列表資料（包括人工 null／空陣列）、列表鎖或任何新版投影證明時，拒絕降到第30版。舊數值資料可往返31→30→31，來源與鎖保持；001–030共60份已發布遷移保持原樣。

本段涵蓋八種字串多值的明確確認套用與人工修改。actor 的 name／role／thumb／order、巢狀多來源 ratings、ID、圖像來源、季集、掃描後自動匯入、前端與無損回寫仍待完成；全案與第三階段仍未完成。

## 實測

Windows 全 Go 29套件／3255通過事件（含父測試）／483測試略過。Linux 五套件 race 1121通過事件，零失敗零略過。完整 PostgreSQL race 788通過事件、440.619秒，零失敗零略過；原生 worker 另跑11通過事件。Windows 的 PG／完整 HTTP 略過身份均有實際通過紀錄。

完整 HTTP／TLS／NFO／PG 74.648秒通過：八種列表的 NFO-only 與供應商同次融合、順序與來源、最大九文字＋八列表修改、人工 null／空陣列清除與優先權、無效型別／界限拒絕、新全域鎖、超限 NFO 不回退供應商及原始位元組保持。既有120筆合成確認寫入保持。

genre 缺少保存、八列表未一起保存、最大合法請求413與資料庫接受tab空值，各有初始失敗紀錄。刻意停用正式列表傳遞時，完整 HTTP 因缺少 genre fact 失敗；逐位元復原後通過。五個融合寫入環節失敗全回滾、指定缺值鎖、人工清除與新鎖、保留資料拒降、舊數值投影往返及資料庫值約束皆通過。Reader 的巢狀切片所有權與超限無部分結果通過。

完整 PG 結束後只增加 HTTP 融合驗收案例；正式程式、PG 測試與原生單元測試保持，最終完整 HTTP 與 Windows 全模組重跑。來源快照差異記錄在[驗證證據](evidence/nfo-string-lists.json)。vet／產品build／增量品牌／gitignore通過；全量品牌仍14735違規／186合法保留，ABI相容性差異仍待處理。60份已發布遷移、LICENSE／原始需求與五個待授權核心保持，本段無C#變更。
