# 忽略規則正式工作流程

`jobs.Runner` 現在可執行保留的忽略請求。Windows/Linux 的 runtime 初始化配置原生掃描器；未配置的 worker 不宣告忽略能力。

## 順序與恢復

1. 在租約內讀取不可變請求、私有根路徑與進度。
2. 尚未開始基線比對時，以原生掃描器列舉並原子保存過濾批次。
3. 開始比對後庫存凍結。處理未見的舊基線頁並保留來源證明；來源不可讀時記錄 unknown，不推論缺失。
4. NFO/probe 只可讀取已完整分類、來源未失效且 epoch/基線版本吻合的庫存。
5. 完成 metadata 後重新讀取持久 unknown。未知結果走 review；其餘逐頁重新觀察來源並取得短期 seal。
6. 成功走 `FinishIgnoreJob` 的保護合併；失敗、取消仍走原終止流程。

重新領取後，已開始的比對不重掃庫存。前任 worker 留下的 unknown 不會被清除。復核仍按新租約世代從頭開始。各資料庫操作使用獨立短時限，檔案 I/O 不持有交易。

## 領取與防護

enabled 工作只有在 worker 宣告 Ignore 能力，且 jobs marker、請求的媒體庫、模式、大小寫及程式/證明版本均相符時可領取。缺失或損壞的請求不能退回普通掃描。舊 worker 可以繼續領取 off 工作。

普通 SaveScanBatch/NextScanDirectory/FinishJob 成功路徑仍拒絕 enabled；metadata 准入不等於發布許可。規則失效、範圍變更、取消或租約過期均不能取得發布權限。

## 驗證邊界

真實暫存目錄與 PostgreSQL worker 測試覆蓋：能力領取與發布、NFO 真解析且排除錯誤 NFO、釋放後重新領取已凍結比對、規則變更拒絕發布。worker 單元測試另驗證持久 unknown、回呼錯誤、缺少/重複 Done 與終止分流。

此階段尚未開放對外 API/CLI 的忽略請求，也尚未提供排除報告；混合外部 probe 工具的忽略場景仍需後續真媒體驗收。測試成果不代表 G00–G51 全案完成。
