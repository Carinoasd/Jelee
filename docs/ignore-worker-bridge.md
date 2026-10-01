# 忽略掃描工作介面橋接

`app.IgnoreInventoryScanner` 與 `app.IgnoreBaselineObserver` 只使用領域型別；`scan.IgnoreScanner` 將原生來源觀察轉換成這兩個介面。

- 列舉沿用 native Resolver 的同句柄證明，不重新用路徑讀取媒體。包含項目沿用既有副檔名分類；排除項目保留規則目錄、行號、匹配路徑。回呼錯誤原樣回傳。
- 舊基線判定只接受資料庫已確認未見的候選；included_missing 是暫定結果，仍須資料庫確認完整目錄覆蓋。來源讀取失敗不產生缺失判定。
- 重新觀察回傳指定目錄的完整證明，交由資料庫與凍結頁比較。若較早祖先缺失，回傳失效錯誤，不偽造目標目錄缺失。
- 保留敏感路徑的私有型別與固定錯誤映射，未開放 enabled 領取、runner、API 或 CLI。

## 驗證

Windows scan/architecture 測試、scan/app vet 與相關套件編譯測試通過。Linux 原生暫存目錄中 scan 套件 race 16 項頂層通過、零跳過、86.3% 覆蓋率，vet 通過。

真實暫存目錄與 PostgreSQL 跨元件 race 測試通過：列舉→同交易保存→重新讀取來源→凍結頁提交→seal→發布。修改規則後，資料庫拒絕復核並禁止發布。此測試使用測試用租約，未驗證正式領取、排程、NFO/probe 或對外 API。

證據見 `docs/evidence/ignore-worker-bridge.json`。沒有新增或改寫遷移。
