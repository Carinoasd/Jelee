# 掃描工作時間窗

## 需求與目前狀態

G13.5 要求掃描窗口避開高峰。此功能尚未啟用；已具備 `internal/adapter/calendar/window.go` 時間判斷與 PostgreSQL `PauseJob` 持久暫停操作，不能視為 worker 已受到限制。

## 時間規則

使用明確 IANA 時區及每日 HH:MM 起訖。起點包含、終點排除，允許跨午夜；三欄皆空代表全天，相同起訖或不完整設定拒絕。拒絕隱含主機 `Local` 時區。沿用 calendar 套件內嵌 tzdata。

判斷以實際時間點轉換成当地時分，不用固定 24 小時推算一天。夏令時間回撥時，兩次出現的同一時分都依相同規則；跳過的時分沒有可執行時間。單元測試涵蓋 UTC 精確邊界、台北跨午夜與紐約春秋切換。

## 必須完成的執行整合

1. 設定載入與嚴格驗證，預設不限制。直到 worker 真正接線前，不公開會被忽略的設定。
2. 所有工作領取分支在窗外停止領取，保持持久佇列；不能只限制 scheduler，因為手動及 watcher 同樣會排入掃描。
3. 工作執行中關窗時，以既有取消機制停止 I/O／子程序，等待 monitor 收束，保留 checkpoint 並交還租約；重新開窗才續跑。
4. 新增受 owner/generation/lease 保護的「計畫暫停」持久操作。現有 ReleaseJob 保留 attempts 並在達上限時直接失敗，不能直接拿來實作正常關窗，否則跨多日掃描會耗盡重試。
5. 取消請求優先於暫停；原工作重試與故障上限不可被暫停繞過。核對整合的 probe/NFO/catalog stage 狀態與恢復行為。
6. 真 PostgreSQL 驗證 checkpoint 保留、嘗試次數不耗盡、過期 owner 拒絕、取消競態；worker 以受控時鐘驗證窗外不領取、關窗停止、重新開窗續跑及 Stop 收束。
7. 更新設定說明、需求追蹤與執行證據。多節點需一致時區及時間窗設定；這不是跨節點全域 CPU/I/O 配額的替代方案。

未完成以上整合前，G13.5 的掃描窗口仍屬未交付。正式長測的 c61c12b007 快照不包含本功能。

## 持久暫停驗證

`JobPauseRepository.PauseJob` 與一般 ReleaseJob 共用原租約 fencing 及探測租約清理。只有持有有效 owner/generation/lease 的內部 worker 可呼叫；取消優先轉為 cancelled。正常計畫暫停將此次 claim 的 attempts 扣回一次、回到 queued，保留既有故障次數及 checkpoint。過期或重播租約不得扣回。未新增公開 HTTP 暫停入口，亦未改寫遷移。

真 PostgreSQL + race 測試：六次暫停後仍保留完成根目錄、檔案與位元組計數，續跑從子目錄開始；之前一次失敗仍計入 attempts；普通 ReleaseJob 最終仍達三次上限而失敗。另驗證已取消工作不再排回佇列，以及過期 owner 無法修改狀態。

[暫停測試](evidence/jobs-pause-linux-race.txt)、[既有回歸](evidence/jobs-pause-regression-linux-race.txt) 均通過。回歸包括取消與 fencing、部分目錄重新開始、交易內租約到期回滾、工作指標及 catalog import 續跑。Windows vet 通過。

尚未接上配置與 worker，時間窗仍未啟用；probe/NFO 等所有執行階段的關窗中斷需隨 worker 整合驗證。
