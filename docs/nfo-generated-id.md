# NFO 缺ID生成與既有ID保護

`Document.EnsureID`從保留原文重新解析，僅在指定條目沒有任何已識別ID時新增UUID v4。生成節點固定為`<uniqueid type="jelee">UUID</uniqueid>`，不設定default。`EnsureIDValue`接受已固定的canonical UUID，供正式任務先持久化寫入意圖與預期輸出。既有uniqueid（包括自訂type與手工jelee值）、imdbid/tmdbid/tvdbid/id均保持；公開Metadata/Entries被清空也不能騙過判斷。ID衝突警告不授權覆盖既有值；空值或其他semantic error拒絕，不自動修復。

缺ID新增受原文lockdata與uniqueid/uniqueids/providerids/id/imdbid/tmdbid/tvdbid鎖保護。其他未知標籤/屬性/註解保持；多條目只操作指定條目，沿用原行尾與縮排，輸出UTF-8及BOM選項。原文與新輸出仍受maxBytes限制。新增ID和後續文字修改保留同一原文的私有雜湊證明，能交給原文綁定Writer；任意替換ID的外部XML仍拒絕。

Writer.Replace在共用操作中補齊原文缺ID的各条目，已有ID的条目直接略過；同時相同意圖只生成一次，後續重新觀察再改寫仍保留該ID。預先固定的EnsureIDValue不被自動生成覆蓋。尚未接持久jobs，因此正式任務必須先固定並保存生成ID、完整輸出意圖與恢復資訊，不能只依賴未保存的隨機UUID重試。缺失NFO建立也尚未實作。

Windows nfo/architecture與vet、jobs/runtime通過；[Linux nfo/architecture race](evidence/nfo-id-write-race-linux.txt)通過。測試核七種手工ID與公開視圖修改、固定ID精確XML、自閉合/CRLF tab/包裝/多根episode指定條目、20次UUID v4格式/variant/不同值、UTF-16轉UTF-8/BOM、鎖/損壞/大小上限/取消。實際Writer測試核多條目一個手工ID及一個缺ID、兩個相同請求共用寫入、未知XML保持、重寫後生成ID不變、固定ID保持與ID鎖拒絕；原文身分/singleflight/備份回滾及100次同程序/跨程序替換亦回歸通過。

G39.10仍部分完成：adapter已具備缺ID生成與不覆寫既有ID，但正式庫read-write策略、持久工作流程/輸出意圖、缺失NFO建立、全部欄位及真實上游客戶端互操作尚缺。全案與正式24h長測的完成狀態另按實際證據判定。
