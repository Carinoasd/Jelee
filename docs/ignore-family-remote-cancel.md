# 跨實例取消的有界旗標讀取

本機通知只對該 runtime 的執行 worker 有效。兩個正式 runtime 使用同一個專用 PostgreSQL schema、各自的連線池及 worker。第一個實例負責合併忽略工作，第二個關閉該家族的准入／認領，在第二個實例的 HTTP 入口提交經授權的取消。

原來只有十秒一次的租約心跳能讀到旗標。第一輪工作安全 cancelled 且未發布基線，但活躍 helper 自行完成兩次呼叫，Cancelled=0，沒有證明取消中斷程序。

## 實作

選用 JobCancellationReader 以單次主鍵查詢讀取 cancel_requested，同時要求精確工作 ID、running、owner、generation、未過期租約；錯誤或失去租約使 worker 停止。查詢自身有兩秒 context 上限，不取得排程鎖、不寫資料列、不續租，也不取代終態及發布交易的租約檢查。只可見已提交的旗標。

既有每工作的 monitor 增加一個一秒 timer，讀完才重新安排下一次；不新增 goroutine、無界 queue 或每個請求的 listener。心跳頻率與租約寫入保持原樣。本機通知仍提供較快的本機取消；未支援讀取 port 的 repository 沿用心跳。這是定期讀取，不保證每個外部取消都在固定一秒內完成；排程及有界 DB 操作可能增加等待。

## 原生驗收

最初的 helper 計算短於一秒，無法證明讀取期間中斷。加入長前綴後工作先失敗，不能當作取消成功；單條規則的 50 毫秒上限保持。最後使用自有測試檔案的十字元前綴與 4,000 條合法有界來源，讓多條實際匹配工作持續而不先觸發單條上限；沒有放寬 helper 的規則、程序或記憶體限額，取消計數必須等於 1 的斷言保留。各失敗紀錄保留。

[正式原生 runtime／真實 PG 證據](evidence/ignore-family-remote-cancel.json)：10 頂層 pass、零 fail／skip、19.069 秒、sourceUnchanged=true。第二實例 HTTP 取消至終態為 897 毫秒，Cancelled=1、TimedOut=0、Started=2（健康檢查與掃描）；Active=0、服務仍可用、baseline=0、owner=0、停止後暫存空、原檔案保持。包含既有本機取消、停止、飽和及重啟驗收。

worker 時鐘專項核對旗標讀取不增加心跳寫入、讀取錯誤／租約丟失停止、timer 釋放；Linux jobs／app／runtime／architecture race 通過。[完整 PostgreSQL race 證據](evidence/ignore-family-remote-postgres.json)為 254 頂層 pass、零 fail／skip、357.980 秒、來源保持，包含新增的已提交可見性／只讀／租約檢查。最後調整 monitor 在通知 join 完成前停止全部 timer；PG 來源保持，相關四包 race 與完整原生驗收再次通過。Windows 全 Go、probe tag、全 vet／三命令 build 通過。

[相同素材反向驗證](evidence/ignore-family-remote-negative.json)：暫時使用上一提交的 runner，9 項既有情境通過，僅新增跨實例情境失敗；原始檔案逐位元恢復後，修復版全部 10 項通過。該失敗是預期的反向證據。G22 與全案維持部分完成，長時間穩定性與其他歷史格式仍待驗收。
