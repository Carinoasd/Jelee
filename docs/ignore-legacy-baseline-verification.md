# 基線來源與缺失邊界的任務級復核

schema17 新增獨立的基線查詢復核進度。原本舊格式來源復核只遍歷仍存在的來源查詢；本段另行遍歷原始 lookup 與缺失邊界，兩種游標不可互換。

## 流程

1. `BeginLegacyIgnoreBaselineVerification` 要求來源清單已凍結且有效，記錄租約世代與固定期限：租約到期或120秒後，以較早者為準。同一世代重複呼叫不延長期限；新世代從第一筆重新驗證。
2. `NextLegacyIgnoreBaselineVerificationPage` 每頁還原最多16筆基線查詢，回傳綁定任務、世代、序號及 lookup 游標的私有 token。
3. 呼叫端以原生 observer 重新確認每筆來源鏈與缺失邊界。`CommitLegacyIgnoreBaselineVerificationPage` 在同一交易內重新讀取預期頁面並比對，不能略過、換序或重用舊 token。
4. 查找目錄、來源鏈或缺失邊界改變時，提交來源清單的失效標記，保持復核進度不變。
5. 摘要包含 lookup、完整來源證據及缺失邊界。到達空頁後仍需提交 EOF，已驗證數量必須等於保存的基線查詢總數才會完成。
6. 寫入後、提交前重新核對租約、取消、inventory 世代與固定期限，避免提交期間過期仍留下進度。

基線查詢最多16384筆，復核最多1024個資料頁及1個EOF頁。token與觀察資料不可經JSON或一般日誌輸出。

## 範圍

001–016遷移保持原樣；存在復核紀錄時拒絕降版，刪除任務可連帶清理後再降版。此進度完成只證明本輪基線來源與缺失邊界已核對，尚不能授權基線發布。合併基線分類、兩種來源全部復核與正式發布交易仍需接線。

## 驗證

- 固定期限不可續期、換租約從頭開始、取消与過期拒絕。
- 18筆共用既有來源但lookup不同的查詢跨頁完整覆蓋、空頁EOF確認、截斷與舊token拒絕。
- 來源內容改變、父目錄重現造成缺失邊界移動，均使清單失效且不推进已驗證數量。
- PostgreSQL trigger注入300毫秒提交延遲，150毫秒期限到期後進度完整回滾。
- 真實原生觀察→保存→凍結→重新觀察→提交復核→EOF完成。

本段 Linux 真實 PostgreSQL 全套 race：219 項頂層測試通過，0 失敗、0 跳過，288.142 秒；受測 PG 來源前後雜湊一致（`.testdata/inventory-baseline-verification-full-postgres-summary.json`）。首輪5項復核測試23.725秒通過。Windows postgres／domain／architecture 測試、全模組vet與三命令建置通過；Linux domain／architecture race為1.056／1.092秒。增量品牌掃描0違規／100合法命中、gitignore-check與diff-check通過。
