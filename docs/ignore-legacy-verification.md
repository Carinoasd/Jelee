# 舊格式來源的任務級復核進度

本段接續 [來源保存與還原](ignore-legacy-storage.md)，新增 schema 14 與獨立的舊格式查詢復核進度。自有規則的既有 schema 11 驗證資料不共用；001–013 遷移不變。

## 交易合同

1. `BeginLegacyIgnoreVerification` 要求來源清單已凍結、未失效，並核對任務租約、取消與媒體庫世代。期限取目前租約期限與開始後 120 秒兩者的較早值。
2. 同一租約代數重複 Begin 不改期限或進度，心跳也不延長來源證據期限。新代數必須重新 Begin，從首筆查詢與初始摘要開始。
3. `NextLegacyIgnoreVerificationPage` 回傳最多 16 次還原查詢，以及綁定 job、generation、sequence、cursor 的獨立 token。回傳的觀察仍須由原生來源復查；讀取頁面不算完成復核。
4. `CommitLegacyIgnoreVerificationPage` 在同一交易讀取預期頁面，逐筆核對 query 與全鏈證據。截斷、重排或舊 token 被拒絕；有效形狀的來源變動會提交清單失效標記。
5. 成功頁面累積查詢數與版本化摘要。摘要包含查詢邊界、所有來源欄位和 Checked 狀態；不能將影子祖先等同來源不存在。讀到空頁後仍需提交 EOF，且累計查詢數必須等於清單計數，才標記 completed。
6. 最終提交重新核對租約、世代及固定期限。交易中即使已更新進度，只要最後發現過期就全部回滾。

token、頁面與來源證據不輸出於 JSON，文字格式化亦脫敏。沒有新增公開 HTTP 入口。

## 遷移

`job_ignore_legacy_verifications` 以任務為主鍵並隨清單刪除；最多 16,384 次查詢、1,025 次提交（1,024 個滿頁加 EOF）。存在復核資料時 down 拒絕，刪除所屬任務後才可回到 schema 13。歷史往返測試增加 14→13 步驟。

## 驗證

相關測試包括重複 Begin 不續期、截斷頁、舊 token 重播、EOF 確認、來源變動、期限到期、取消、新租約重啟，以及提交期間資料庫延遲導致的整筆回滾。遷移測試涵蓋保留資料時拒絕降版、刪除清理及 up/down/up。

Linux 真實 PostgreSQL 全套 race：198 項頂層測試通過、0 失敗、0 跳過，247.412 秒；測試前後來源雜湊一致（本機證據 `.testdata/inventory-legacy-verification-full-postgres-summary.json`）。Windows domain／postgres／architecture 測試、全模組 vet 與三個命令建置通過；Linux domain／architecture race 1.101／1.122 秒。增量品牌掃描 0 違規／100 合法命中，gitignore-check 通過。

## 尚未交付

這是復核進度與期限合同。尚未把舊格式模式接入正式掃描 worker、兩種規則組合、基線比較及最終發布交易；`completed` 本身不授權發布基線，也不代表整個 G22 或第 3 階段完成。
