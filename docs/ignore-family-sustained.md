# 原生忽略服務持續負載

固定五分鐘內反覆使用同一個正式 familyIgnoreService、兩個原生執行額度及固定 helper。每輪觀察兩個 OS 子程序 active，再拒絕 32 次額外請求，取消並 join 兩個活躍程序，核對清理及正常包含／排除結果，直到五分鐘結束，至少完成 100 輪。沒有用等待填充負載時長或按環境縮短驗收。

測試在每輪函式返回前釋放 context，不將每輪取消函式累積到測試結束。每 64 輪抽樣父程序 Go heap（64 MiB 上限），結束時 GC 後的 heap 增長限制為 16 MiB、goroutine 增量不超過 4。這些是父程序診斷；不是 RSS、子程序總記憶體或整個伺服器的長期數據。程序 Peak 必須 2，TimedOut／Active 必須 0，Cancelled／Started／busy 次數必須吻合實際輪數。每輪輸入目錄清空，Close 後服務暫存樹清空。

make ignore-sustained-test 執行固定驗收。腳本驗證五分鐘、輪數、計數、無 skip／fail、來源保持並保存 JSONL 和摘要，拒絕覆寫舊證據。Linux 使用既有 POSIX 工具鏈；Windows 使用既有 PowerShell 工具鏈。Go timeout 為 7 分鐘、外層為 8 分鐘，來源的 helper 限額保持。CI 加入必要步驟並在執行後保存證據，缺檔則失敗。

首輪 Linux 為 300.023 秒實際負載、5,146 輪、164,672 次 busy、10,292 次取消，Peak2／Active0／TimedOut0；抽樣 heap 最高 3,295,272 bytes，goroutine 2→2。首次 Windows 因誤用 POSIX 入口而在測試前失敗，紀錄保留；沒有當成測試通過。改正入口後兩平台最終驗收皆通過，來源保持，零 fail／skip。

[兩平台最終證據](evidence/ignore-family-sustained.json)：

| 平台 | 負載秒數 | 輪數 | busy | Started | Cancelled | heap 抽樣最高 bytes | 結束 heap bytes |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Linux | 300.022 | 5,043 | 161,376 | 15,130 | 10,086 | 3,339,848 | 970,688 |
| Windows | 300.025 | 9,399 | 300,768 | 28,198 | 18,798 | 3,381,040 | 1,244,784 |

兩者 Peak=2、TimedOut=0、Active=0，goroutine 2→2，每輪及最後暫存清理通過。兩平台在同一主機同時執行，輪數是本次穩定性證據，不作跨平台效能比較。Linux／Windows 總命令时间 312.061／301.406 秒，包含啟動與編譯。

Windows 短週期與 probe 標籤、全 Go 測試、runtime vet、格式／Python AST／workflow YAML／Make dry-run、增量品牌與忽略門禁通過。本次只變更驗收程式及 CI，未修改正式 worker、PG 或遷移來源，不重跑未改範圍的完整 PG。

這證明的是原生服務在連續程序啟停及滿載取消下的資源穩定性。正式 runtime／HTTP／PG／混合媒體有各自的其他验收證據；本段不能代替整個伺服器長時間或所有舊格式的验收，G22 與全案仍未完成。
