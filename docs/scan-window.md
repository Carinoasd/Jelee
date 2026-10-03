# 掃描工作時間窗

## 需求與目前狀態

G13.5 要求掃描窗口避開高峰。時間判斷、設定與 worker 已接線：窗外不領取工作，執行中每秒檢查一次，關窗取消目前操作並在其收束後呼叫有租約保護的計畫暫停。重新開窗從持久 checkpoint 續跑。整個 G13.5 仍有目錄並行、CPU/I/O 全域預算及完整增量整合待完成。

## 設定

在 jobs JSON 設定 `windowStart`、`windowEnd`、`windowTimezone`，例如 `22:00`、`06:00`、`Asia/Taipei`。環境變數 `JELEE_JOB_WINDOW_START`、`JELEE_JOB_WINDOW_END`、`JELEE_JOB_WINDOW_TIMEZONE` 覆寫對應 JSON 欄位。三者預設皆空，全天執行；修改後需重新啟動。job worker 啟用時，不完整或無效窗口會拒絕啟動。

此設定限制該實例的所有 job worker，包含手動、排程、watcher 所建立的掃描及 catalog import；不限制一般 HTTP 讀取。窗外仍可提交至原本有容量上限的持久佇列，沒有額外記憶體等待佇列。多實例應使用一致設定。

關窗觀測粒度為一秒，加上正在進行操作的取消收束時間；不承諾在邊界瞬間搶占同步解碼。開始工作前再檢查一次，避免 claim 期間剛好關窗。正常 Stop、執行期限、租約失效與使用者取消維持既有語意。真正 I/O 失敗即使與關窗同時發生，仍記為失敗。

## 時間規則

使用明確 IANA 時區及每日 HH:MM 起訖。起點包含、終點排除，允許跨午夜；三欄皆空代表全天，相同起訖或不完整設定拒絕。拒絕隱含主機 `Local` 時區。沿用 calendar 套件內嵌 tzdata。

判斷以實際時間點轉換成当地時分，不用固定 24 小時推算一天。夏令時間回撥時，兩次出現的同一時分都依相同規則；跳過的時分沒有可執行時間。單元測試涵蓋 UTC 精確邊界、台北跨午夜與紐約春秋切換。

## 執行契約

所有 claim 分支共用窗外檢查；服務內既有 monitor 增加一個每秒計時器，沒有新增每工作 goroutine。關窗取消後先等待工作與 monitor 收束，再用獨立有界資料庫 context 暫停。只接受能提供 JobPauseRepository 的 repository，避免配置啟用卻無法保存暫停。

暫停與使用者取消透過持久交易排序；已取消的工作不會重新入隊。過期 owner 不可暫停或發布。正常故障與服務中斷維持既有 ReleaseJob 與重試限制。

尚待補充 probe/NFO/ignore 各階段的真實關窗恢复矩陣及多節點測試。正式長測的 c61c12b007 快照不包含本功能。

## 持久暫停驗證

`JobPauseRepository.PauseJob` 與一般 ReleaseJob 共用原租約 fencing 及探測租約清理。只有持有有效 owner/generation/lease 的內部 worker 可呼叫；取消優先轉為 cancelled。正常計畫暫停將此次 claim 的 attempts 扣回一次、回到 queued，保留既有故障次數及 checkpoint。過期或重播租約不得扣回。未新增公開 HTTP 暫停入口，亦未改寫遷移。

真 PostgreSQL + race 測試：六次暫停後仍保留完成根目錄、檔案與位元組計數，續跑從子目錄開始；之前一次失敗仍計入 attempts；普通 ReleaseJob 最終仍達三次上限而失敗。另驗證已取消工作不再排回佇列，以及過期 owner 無法修改狀態。

[暫停測試](evidence/jobs-pause-linux-race.txt)、[既有回歸](evidence/jobs-pause-regression-linux-race.txt) 均通過。回歸包括取消與 fencing、部分目錄重新開始、交易內租約到期回滾、工作指標及 catalog import 續跑。Windows vet 通過。

配置與 worker 已接上；probe/NFO 等所有執行階段仍需擴充真實關窗中斷矩陣。

## Worker 驗證

受控計時器覆蓋窗外不 claim、開窗後 claim、執行中關窗等待 scanner 收束後 PauseJob、claim 期间關窗不開始掃描，以及真實掃描失敗不被關窗掩蓋。Stop 後沒有遺留 timer。

真 PostgreSQL worker 保存根目錄 checkpoint 後關窗，工作回 queued、attempts 歸零且檔案/位元組保留；開窗後從子目錄續跑至 succeeded、attempts 為一。證據：[worker/配置/calendar Linux race](evidence/jobs-window-unit-linux-race.txt)、[PostgreSQL worker 與暫停 Linux race](evidence/jobs-window-linux-race.txt)。這不是整套 G13.5 效能驗收，也不代替多節點負載測試。

## 探測與 NFO 關窗矩陣

worker 測試使用可控時間與階段替身，分別在探測 Inspect、Probe、NFO Read、Parse 阻塞處關窗。四個案例均等待處理返回再暫停，未提交失敗媒體結果、未中止整個階段、未遺留 gate slot；再開窗只完成一筆預期項目。這些替身測試不代表外部 ffprobe 程序的端到端驗收。

真 PostgreSQL 驗證探測子租約由父工作 PauseJob 交易清理，active leases 與 quota 回到零，phase checkpoint 保留；旧父/子租約無法提交，重新領取可完成。另在 NFO 第一筆提交後暫停，恢復只处理第二筆，總 processed/valid 為二，沒有重複計數。

證據：[Linux worker/config/calendar/scan race](evidence/jobs-window-stages-unit.txt)、[真 PostgreSQL 暫停與階段續跑 race](evidence/jobs-window-stages-postgres.txt)。Windows 關窗測試及 jobs/scan 套件回歸通過。

仍待實際 ffprobe/NFO runtime 與 ignore/catalog 各階段整合的關窗恢復驗收；不把受控替身矩陣等同完整實際程序測試。

## Windows CI 期限測試

ff1d7eccaf 的 Windows foundation job 111101499591 在 `TestFamilyBaselineBatchKeepsPerCandidateDeadline` 失敗，另一個 Windows job 通過。原測試直接要求連續建立的期限嚴格遞增；Windows 時鐘刻度可能相同。修正先等待時鐘跨過第一個期限建立刻度，仍要求第二個期限嚴格較晚，並檢查前一 candidate context 已取消、下一個仍有效及最終清理。Windows 100 次通過；沒有放寬生產期限或刪除新期限斷言。遠端修正後結果仍待 CI 確認。

## 正式 runtime 配置與匯入續跑

`TestJobWindowProductionRuntimeConfiguration` 使用真 Fx runtime、設定載入、HTTP listener、PostgreSQL 與檔案掃描。啟動關閉的時間窗後，同一持久工作保持 queued/attempts0；停止服務再以全天配置啟動，該工作 succeeded/attempts1，檔案數與位元組正確，原媒體內容不變。這驗證正式建構流程確實注入時間窗，不只是直接建構 worker。

匯入整合先驗證並提交第一個 catalog 項目，再以 PauseJob 暫停；舊 owner 不可再次提交。實際 catalog worker 恢復後報告 completed2、資料庫 items2，無重複或跳過；attempts只計一次。此例驗證持久匯入前綴恢復，尚未涵蓋真驗證 I/O 進行中的關窗。

[正式 runtime Linux race](evidence/jobs-window-runtime-linux-race.txt)、[匯入 PostgreSQL Linux race](evidence/jobs-window-catalog-postgres.txt) 通過。452a37ebd2 的 [Windows foundation](https://github.com/Carinoasd/Jelee/actions/runs/37088061602/job/111102378156) 已通過；其他未完成 CI 與品牌門禁不可推定通過。
