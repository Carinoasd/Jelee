# 共用 CPU／I/O 配額：底層實作

目前新增 `app.WorkBudget` 介面與 `platform/resources.Budget`。正式 runtime 已建立單一實例，HTTP 直投使用共享 I/O／總量配額；目錄掃描與 NFO 讀取／解析亦已接入；探測、圖片、忽略掃描等仍待接入。G41.3 與 G13.5 保持部分完成。

## 已實作

- 一把鎖同時取得工作類型與總量配額，不先占住其中一種再等待另一種。
- CPU、I/O、總量及等待數各有上限；等待佇列滿時回傳 `domain.ErrResourceBusy`。
- 優先派發最早且目前符合配額的等待者；CPU 飽和不會擋住仍有空位的 I/O。
- 等待支援 context 取消；已授予但尚未返回的工作取消時會退回配額。
- release 可重複呼叫，不會重複扣除。限額器本身不建立 goroutine。
- 使用者必須等子工作全部結束後才 release；禁止巢狀取得共用配額。

## 驗證

Windows：`scripts/run-go.ps1 test ./internal/platform/resources ./internal/architecture` 通過；resources vet 通過。
Linux：固定 Go 工具鏈 `go test -race -count=1 ./internal/platform/resources` 通過，輸出見 [race 證據](evidence/resources-race-linux.txt)。

覆蓋分類與總量上限、零等待容量、佇列背壓、先到且可執行者順序、重複釋放、32 個並行工作各 100 次取得，以及 100 次已排隊取消與釋放競爭。另以受控鎖順序驗證授予後、Acquire 返回前取消的退款分支。

## 待接入

Runtime 單一實例、配置及直投已接入。下一步接探測、忽略掃描與圖片等其餘消費者。各操作依階段取得配額，跨 CPU／I/O 階段先釋放再取得，避免巢狀等待。背壓不得被誤記為壞媒體或解析失敗。補齊實際混合工作、HTTP、取消／停機、可觀測性及調校驗收後才能關閉需求。

## 正式配置與直投接入

`resources` JSON 物件與下列環境變數皆可配置，環境變數優先。啟動時生效，需要重啟。

| JSON 欄位 | 環境變數 | 預設 | 範圍 |
| --- | --- | --- | --- |
| cpuFactor | JELEE_RESOURCE_CPU_FACTOR | 1 | 0.125–8，有限數值 |
| io | JELEE_RESOURCE_IO | 16 | 1–1024 |
| total | JELEE_RESOURCE_TOTAL | 32 | 1–1024 |
| queue | JELEE_RESOURCE_QUEUE | 128 | 0–4096 |

CPU 配額為啟動時 `GOMAXPROCS × cpuFactor` 向上取整、限制於 1–256。總量限制可能低於類型配額。這是初始配置，尚非不同機器實測後的調校建議；CPU 配額已約束 NFO 解析，目前尚未約束探測／圖片。

正式 runtime 的帳號開啟與關閉兩條 HTTP 建構路徑皆傳入共享實例。直投通過原有授權查詢後、開檔之前取得 I/O 配額，等候最長為既有 requestTimeout；排隊滿或等候逾時回傳 429 與 Retry-After: 1。取消不輸出錯誤本文；配額在傳輸結束、取消回呼加入與檔案關閉後釋放。既有 MaxStreams 上限仍生效，避免無界等待連線。

測試覆蓋真限額器 CPU 占住總量時的直投等待、恢復、取消、佇列滿與逾時；Range 傳輸持有配額，讀檔失敗與成功後配額歸零。配置檔、環境覆蓋、預設與非法值亦驗證。見 [Linux race](evidence/resources-direct-race-linux.txt)。[真 PG runtime 測試](evidence/resources-direct-runtime.txt) 驗證 Fx 啟動、監看與背景作業；它不是所有模組共用預算的混合壓測。

Windows internal 套件回歸除 images/toolidentity 因沙箱無法設定 fixture ACL 失敗外通過；兩個套件用正常權限重跑通過。受影響套件 vet 通過。

## 背景目錄掃描與 NFO

Runtime 將同一個資源實例傳給 jobs.Options.Budget。Inventory 的每次 ScanDirectory 取得 I/O 配額；NFO 每次 Read 取得 I/O、Parse 取得 CPU，回傳前釋放，不跨階段巢狀持有。每檔逾時從取得配額後才開始，排隊時間不會被記成 NFO 解析逾時。

背景佇列滿時，同一個既有 worker 按 PollInterval 等待後重試；不增加 goroutine，不建立無界佇列，不寫媒體失敗。等待仍受父 job 的取消、時間窗及 MaxJobRuntime 約束，monitor 繼續維護租約。零佇列也採此方式。這不等於持久化排程暫停；超過整體作業期限仍遵循既有作業期限政策。

真限額器測試驗證 total=1 下 NFO read/parse/revalidate 的類型與精確執行次數，掃描被 CPU 總量擋住時的溢出等待／取消／恢復，掃描 panic 退款；關窗 NFO read/parse 的既有矩陣新增真配額歸零斷言。Windows jobs/runtime/architecture 及 vet 通過，[Linux race](evidence/resources-jobs-race-linux.txt) 通過。[真 PG runtime](evidence/resources-jobs-runtime.txt) 驗證正式 Fx 注入與掃描生命週期；NFO 分類測試使用受控 reader，尚非完整混合負载驗收。

探測有資料庫子租約期限，需先決定 CPU 配額與子租約的取得順序，以及探測後 I/O 複核的等候上限，避免持有子租約長時間排隊後反覆過期。目錄監看、忽略掃描、索引、圖片與下載亦仍在接入清單。
