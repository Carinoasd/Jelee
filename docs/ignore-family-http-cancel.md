# HTTP 取消與本機 worker 的即時通知

正式 HTTP 取消交易原本只保存旗標，worker 依心跳讀取。原生實測觀察到 helper Active=1 後取消，工作最終安全取消且沒有發布基線，但 helper 完成兩次呼叫；取消與逾時終止計數都是零。這不足以證明取消旗標已中斷正在執行的程序。

## 修正

app 增加選用 JobCancellationNotifier：只有 CancelJob 已成功返回、同一工作仍 running 且 cancelRequested=true 才通知。本機通知在資料庫授權與提交之後發送；拒絕授權、交易錯誤、queued／既有終態不發送。

正式 runtime 將通知接到 worker。runner 在開始执行前註冊帶租約世代的 context cancellation，完成所有执行及持久化後移除。舊世代的清理不能移除新世代的註冊；未執行或其他工作 ID 沒有作用。註冊由 mutex 保護，最多對應本機的執行 worker，不建立全域變數、無界 queue 或新 goroutine。

資料庫旗標、授權、租約 fence、最後發布檢查仍是最終依據。其他實例的 owner 或未註冊的任務沿用原有旗標／心跳路徑；本段不宣稱跨實例通知的即時性。

## 程序計數

process.Stats 新增 Cancelled／TimedOut。只在成功建立 OS 子程序後，等待分支先觀察到 context 取消／逾時時增加；啟動前取消、先觀察到正常退出不計入。這表示程序回收走了哪個控制分支，不是 OS 終止原因的獨立量測。不記錄路径、輸入或輸出。

既有原生 helper 生命周期測試核對取消 1、逾時 1、回收與 slot 重用；正常請求不增加這兩項。HTTP 情境以預設 30 秒租約、固定正式 helper、4,000 條合法有界來源及實際登入／監聽／PostgreSQL，在觀察 child Active=1 後送出取消請求。69 毫秒內完成，cancelStops=1、timeoutStops=0、childStarts=2（健康檢查與掃描各一次），Active=0、服務仍可用、沒有基線或 owner 殘留。停止後暫存清空、HTTP 關閉與 fixture 內容保持。

## 驗證

[正式 runtime 與真實 PostgreSQL 證據](evidence/ignore-family-http-cancel.json)：8 項頂層 pass、0 fail、0 skip，76.058 秒，sourceUnchanged=true；包括公開入口、重啟、活躍 child 停止與 HTTP 取消，以及兩條原生服務取消／Close 情境。初次修正後專項為 39.313 秒，取消耗時 55 毫秒。

兩份失敗證據保留：第一輪測試請求未帶 JSON 格式，回傳 415；修正後第二輪確認取消計數仍為零。沒有放寬 HTTP 請求驗證、helper 限額或發布 fence。修復後保留要求取消分支計數為 1 的斷言。

app 專項核對只有授權交易成功後才通知；worker 專項在不推進心跳時計的情況下驗證即時取消、無關 ID 不影響、註冊釋放與舊世代清理安全。Windows 相關五包通過；Linux app／runtime／jobs／architecture race 為 1.042／1.109／1.372／1.366 秒。process race 亦通過（152.824 秒）；正式原生 helper 的有界地址空間不以 race 子程序執行。全 vet 與三命令 build 通過。

Windows 全 Go 與 probe 標籤測試已完成（exit 0）。完整 PostgreSQL 回歸首輪在 600.067 秒外層逾時，已有 229 頂層 pass、零已記錄測試 fail／skip，來源保持；這不是完整通過。重跑時另外校正 Go 內層預設期限，該次人工中止紀錄也保留。最終以 Go 20 分鐘、外層 25 分鐘期限執行全部測試，未修改測試內容或斷言：[完整 PG race 證據](evidence/ignore-local-cancellation-postgres.json)為 252 項頂層 pass、零 fail／skip，344.936 秒，exit 0、sourceUnchanged=true。G22 仍待其他歷史格式、壓力與長時間穩定性，相關需求與全案保持未完成。
