# 合併規則掃描的原子保存

## 資料與交易

schema 15 新增 `job_ignore_family_exclusions`，保存相對路徑、種類、規則格式、排除原因、來源目錄及行號。一般規則行號為 1–4096；舊格式空來源及全無效來源使用行號 0。父目錄索引支援重新執行未完成目錄時移除暫存排除記錄。

`SaveFamilyIgnoreScanBatch` 驗證候選互斥、持有目錄身份、自有來源完整父鏈、舊來源查詢範圍及命中來源。舊來源查詢只可指向目前目錄或本批候選子目錄，兩種來源的祖先身份必須相同。

交易內依序保存兩種來源證據、排除資料與 inventory，最後重新核對租約、取消及 inventory 世代。兩種來源共用回滾點；若第二種來源衝突，第一種來源本批新增的證據也會撤回，只保留兩份清單的失效標記。其他錯誤回滾整筆交易。

排除資料以集合查詢檢查既有 inventory／目錄與重播內容，再批次寫入。相同重播不重複計數；已觀察條目與排除條目不可重疊。排除項目也計入檔案／目錄預算。`NextFamilyIgnoreScanDirectory` 重啟未完成目錄時移除該目錄的暫存結果，但保留已接受的來源證據。

## 遷移與限制

001–014 遷移保持原樣。新觸發器在合併模式任一來源清單凍結或失效後拒絕修改 inventory、目錄及排除結果；刪除任務仍可連帶清理。存在合併掃描狀態時拒絕降版。

實測發現多個 query 選定同一祖先時，schema 13 的來源外鍵會阻擋任務連帶刪除。schema 15 將該外鍵改成提交時檢查，讓所有相關 query 的連帶刪除完成後再驗證；完整性限制仍保留，down 會還原檢查時機。

公開合併模式仍關閉。這個保存入口尚未取代正式 worker，也沒有授權基線發布；後續仍需完整基線分類、任務最終來源復核、發布守衛及報告接線。

## 驗證入口

- `TestFamilyIgnoreScan*`：保存、重播、重啟、來源衝突共同回滾、格式／來源錯誤、取消、凍結、預算、模式隔離與升降版。
- `TestFamilyIgnoreNativeStorage`：真實原生枚舉 → 受限匹配子程序 → PostgreSQL 保存，確認自有包含與排除、舊規則排除、空來源及全無效來源原因、排除目錄不入掃描佇列與 helper 結束。
- 真實 helper 測試不使用 race，因其正式位址空間上限與 race 保留量衝突；其餘資料庫邏輯跑 race。CI 的 PostgreSQL 工作另外執行該非 race 驗收。

## 本階段實測

Linux 真實 PostgreSQL race 全套執行 260.134 秒：207 項頂層測試通過、0 跳過；唯一失敗是舊升降版測試漏列 15→14。補齊該測試步驟後，單獨重跑 `TestPostgresIntegration` 通過（23.825 秒），沒有再修改正式實作。兩次執行的來源前後雜湊均一致；保留原失敗證據，不將它改標通過。

本機證據：`.testdata/inventory-family-scan-full-postgres-summary.json` 及 `.testdata/inventory-family-scan-migration-fixed-postgres-summary.json`。原生掃描到真實 helper／PG 的非 race 驗收另行通過（16.710 秒、1 項、0 跳過）；證據 `.testdata/inventory-family-native-storage-postgres-summary.json`。

Windows postgres／architecture 測試、全模組 vet、三命令建置、增量品牌掃描（0 違規／100 合法命中）、gitignore-check 及 diff-check 通過。LICENSE 與需求原文 SHA256 維持原值。
