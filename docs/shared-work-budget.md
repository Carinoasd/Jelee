# 共用 CPU／I/O 配額：底層實作

目前新增 `app.WorkBudget` 介面與 `platform/resources.Budget`。正式 runtime 已建立單一實例，HTTP 直投使用共享 I/O／總量配額；目錄掃描與 NFO 讀取／解析亦已接入；探測的 Inspect／Probe 亦已接入；圖片處理亦已接入；忽略目錄掃描亦已接入；忽略基線比對與複核亦已接入；目錄監看建置／重建／根檢查亦已接入；索引與其他下載等操作仍須逐一稽核。G41.3 與 G13.5 保持部分完成。

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

Runtime 單一實例、配置及直投已接入。下一步稽核索引與其他下載等消費者並補混合負載驗收。各操作依階段取得配額，跨 CPU／I/O 階段先釋放再取得，避免巢狀等待。背壓不得被誤記為壞媒體或解析失敗。補齊實際混合工作、HTTP、取消／停機、可觀測性及調校驗收後才能關閉需求。

## 正式配置與直投接入

`resources` JSON 物件與下列環境變數皆可配置，環境變數優先。啟動時生效，需要重啟。

| JSON 欄位 | 環境變數 | 預設 | 範圍 |
| --- | --- | --- | --- |
| cpuFactor | JELEE_RESOURCE_CPU_FACTOR | 1 | 0.125–8，有限數值 |
| io | JELEE_RESOURCE_IO | 16 | 1–1024 |
| total | JELEE_RESOURCE_TOTAL | 32 | 1–1024 |
| queue | JELEE_RESOURCE_QUEUE | 128 | 0–4096 |

CPU 配額為啟動時 `GOMAXPROCS × cpuFactor` 向上取整、限制於 1–256。總量限制可能低於類型配額。這是初始配置，尚非不同機器實測後的調校建議；CPU 配額已約束 NFO 解析，亦約束 Probe 與圖片解碼／縮圖／編碼。

正式 runtime 的帳號開啟與關閉兩條 HTTP 建構路徑皆傳入共享實例。直投通過原有授權查詢後、開檔之前取得 I/O 配額，等候最長為既有 requestTimeout；排隊滿或等候逾時回傳 429 與 Retry-After: 1。取消不輸出錯誤本文；配額在傳輸結束、取消回呼加入與檔案關閉後釋放。既有 MaxStreams 上限仍生效，避免無界等待連線。

測試覆蓋真限額器 CPU 占住總量時的直投等待、恢復、取消、佇列滿與逾時；Range 傳輸持有配額，讀檔失敗與成功後配額歸零。配置檔、環境覆蓋、預設與非法值亦驗證。見 [Linux race](evidence/resources-direct-race-linux.txt)。[真 PG runtime 測試](evidence/resources-direct-runtime.txt) 驗證 Fx 啟動、監看與背景作業；它不是所有模組共用預算的混合壓測。

Windows internal 套件回歸除 images/toolidentity 因沙箱無法設定 fixture ACL 失敗外通過；兩個套件用正常權限重跑通過。受影響套件 vet 通過。

## 背景目錄掃描與 NFO

Runtime 將同一個資源實例傳給 jobs.Options.Budget。Inventory 的每次 ScanDirectory 取得 I/O 配額；NFO 每次 Read 取得 I/O、Parse 取得 CPU，回傳前釋放，不跨階段巢狀持有。每檔逾時從取得配額後才開始，排隊時間不會被記成 NFO 解析逾時。

背景佇列滿時，同一個既有 worker 按 PollInterval 等待後重試；不增加 goroutine，不建立無界佇列，不寫媒體失敗。等待仍受父 job 的取消、時間窗及 MaxJobRuntime 約束，monitor 繼續維護租約。零佇列也採此方式。這不等於持久化排程暫停；超過整體作業期限仍遵循既有作業期限政策。

真限額器測試驗證 total=1 下 NFO read/parse/revalidate 的類型與精確執行次數，掃描被 CPU 總量擋住時的溢出等待／取消／恢復，掃描 panic 退款；關窗 NFO read/parse 的既有矩陣新增真配額歸零斷言。Windows jobs/runtime/architecture 及 vet 通過，[Linux race](evidence/resources-jobs-race-linux.txt) 通過。[真 PG runtime](evidence/resources-jobs-runtime.txt) 驗證正式 Fx 注入與掃描生命週期；NFO 分類測試使用受控 reader，尚非完整混合負载驗收。

探測有資料庫子租約期限，需先決定 CPU 配額與子租約的取得順序，以及探測後 I/O 複核的等候上限，避免持有子租約長時間排隊後反覆過期。目錄監看、忽略掃描、索引、圖片與下載亦仍在接入清單。

## 探測配額與子租約

Inspect 使用 I/O；probeMiss 在取得資料庫子租約前先取得 CPU。Probe 返回後立即釋放 CPU，然後以獨立 I/O 配額複核，避免 total=1 時巢狀等待。錯誤、取消、能力變更或租約取得失敗的提前返回均釋放 CPU。

等待 CPU 不占子租約。探測後等 I/O 時，既有 HeartbeatJob 會續租有效子租約，且期限不超過父作業；過期子租約不復活。真 PostgreSQL 的 `TestProbeAdversarialHeartbeatDoesNotReviveExpiredChild` 重新通過，见[心跳證據](evidence/resources-probe-heartbeat.txt)。心跳失敗依原流程取消，不移除資料庫 fencing。

Windows jobs/architecture、vet 與 [Linux race](evidence/resources-probe-race-linux.txt) 通過。真 budget total1 驗證 Inspect→CPU lease/probe→Inspect 無巢狀；另驗證 CPU 飽和時 child acquire 次數為0、取消不產生媒體結果、釋放後恰好恢復一次。關窗矩陣也涵蓋新的配額。這些 worker 測試使用受控 prober，不等同所有外部 ffprobe 混合壓測。

## 圖片分階段配額

Runtime 的圖片處理器取得同一 budget。來源暫存取得 I/O；快取未命中時，檢查、解碼、縮圖、編碼取得 CPU；來源複核及暫存關閉再次取得 I/O。先釋放上一階段再取得下一階段。快取命中不取得 CPU。取得失敗會走原有清理；共享佇列滿轉 ErrImageBusy，等待仍受圖片操作期限、請求及生命週期取消控制。

共用處理配額在 Render 結束前釋放；圖片既有記憶體保留維持至回應 Body.Close。同步 decode 收到取消或逾時後，仍須等 decode 返回才釋放 CPU。Windows 完整圖片測試、vet 通過；[Linux race](evidence/resources-images-race-linux.txt)、[真 PG／HTTP runtime](evidence/resources-images-runtime.txt) 通過。新增 cold IO/CPU/IO、warm IO/IO、body記憶體與處理配額分離、queue-full、排隊取消；既有同步取消／逾時測試新增 CPU 配額斷言。

此變更尚未包含在目前來源38a47082e9的隔離smoke中，不能把該長測證據歸於圖片配額實作。完整混合負載驗收仍待後續執行。

探測既有 BusyReleasesBeforeBackoff 矩陣亦使用真共用限額器，驗證 lookup/acquire/process 三類忙碌退避前 total/CPU/IO 歸零，恢復成功後仍無配額殘留；Windows及Linuxrace通過。

## 忽略目錄掃描

兩種目錄遍歷路徑 ScanIgnoreDirectory／ScanFamilyIgnoreDirectory 都使用 runner 既有 acquireWork 取得 I/O，掃描與同步 SaveBatch 回呼返回後釋放。因為範圍只包單一目錄，沒有把整個工作或基線階段包在配額內；佇列滿與取消沿用共用 worker 的有界等待。

一般忽略掃描既有批次矩陣加入真 budget total=1；family 新增成功、資料庫回呼失敗（即使掃描器吞掉回呼錯誤）、缺少 done、取消四種案例，驗證 scanner 執行時 IO/total各1、返回後皆0及原錯誤分類不變。Windows jobs/architecture、vet 與 [Linux race](evidence/resources-ignore-race-linux.txt) 通過。基線觀察、批次比對與目錄證據複核仍需各自接入，不能據此聲稱整個忽略流程受限。

## 忽略基線與複核配額

一般基線 EvaluateIgnoreBaseline、ReobserveIgnoreProof，以及 family 基線批次／逐筆與三種 verification stream 的 observe，均經 withJobIO 取得共用 I/O。範圍只包含同步觀察操作；批次返回釋放後才進入逐筆 fallback，觀察返回釋放後才 Commit 頁面。既有錯誤、unknown 與 fencing 規則保持。

Family baseline success/unavailable/failure/short/wrong-path/root-failure矩陣加入真budget total1，驗證批次退回逐筆不巢狀且每次結束配額歸零。verifyFamilyStream新增成功、觀察錯誤與取消，確認持IO時觀察、提交前已釋放，錯誤不提交。Windows jobs/architecture、vet與[Linux race](evidence/resources-baseline-race-linux.txt)通過。仍需真正外部helper與其他模組的混合壓測，不能據此關閉G41.3或G41.9。

## 目錄監看

WatchOptions.Budget 使用runtime同一實例。build 的遍歷／註冊及失敗清理持 I/O；建置完成即釋放，重建重新取得。每5秒的根身分檢查另取得短期I/O。常駐原生事件輪詢不持有共享名額。佇列滿時既有有限數量的watch worker每250ms重試，等待可被關窗／取消／停止中斷，不新增背景goroutine。

測試涵蓋 queue0 退避及 queue1 等待取消、建置目錄上限失敗回收，真原生監看新增子目錄後重建再次取得I/O且dirty callback時無持有。Windows scan/runtime/architecture與vet通過；[Linux race](evidence/resources-watch-race-linux.txt)及[真PG runtime時間窗／watch](evidence/resources-watch-runtime.txt)通過。這不是全部混合負載或不同核心數調校驗收。

## 共用資源指標

正式 runtime 使用 `NewWithResources`，傳入與 HTTP、jobs、images、watch 相同的 Budget。管理員端點另增加八個無 labels 的 gauge：`jelee_resources_cpu_active`、`io_active`、`total_active`、`waiting`、`cpu_limit`、`io_limit`、`total_limit`、`queue_limit`（各名稱均使用 `jelee_resources_` 前綴）。active 表示持有配額的操作，waiting 表示已進入共用等待佇列的操作；佇列滿時在既有 worker 退避的操作不計入 waiting。

每次 callback 只讀一次鎖內 Stats 快照，CPU＋I/O＝total；限額讀取不可變配置的副本。收集不取得工作配額、不執行 I/O、不建立 goroutine。每個程序獨立計數，多副本不能當作全域總量。正式端點共30家族／171系列；原有New與NewWithJobs保留15與22家族契約。

本批 Windows telemetry/resources/runtime/architecture 測試及 vet 通過；[Linux race](evidence/resources-metrics-race-linux.txt) 覆蓋 telemetry/resources/jobs/runtime；[真 PostgreSQL HTTP race](evidence/resources-metrics-runtime.txt) 執行 TestMetricsRuntimePostgresIntegration，核對配置限額及匿名／一般帳戶不得洩漏資源指標。真 budget 的滿載、排隊、取消與回收測試核對30家族、無動態labels及64KiB回應上限。這些驗證未涵蓋完整混合壓測或資源等待時間分布。

G41.8 原有工作等待／耗時統計只涵蓋 inventory_scan、catalog_import。其他需求中的任務類型尚未全部實作與觀測；本批八個gauge不能補足此缺口，G41.8回復部分完成。G41.3、G41.9與G41.10也仍未完成。
