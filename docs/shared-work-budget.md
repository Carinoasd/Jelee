# 共用 CPU／I/O 配額：底層實作

目前新增 `app.WorkBudget` 介面與 `platform/resources.Budget`。尚未注入正式 runtime，現行掃描、探測、NFO、圖片及直投仍使用各自限額。G41.3 與 G13.5 保持部分完成。

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

Runtime 只建立一個共享實例；提供 CPU 核數係數、I/O、總量、佇列配置與預設值。各操作依階段取得配額，跨 CPU／I/O 階段先釋放再取得，避免巢狀等待。背壓不得被誤記為壞媒體或解析失敗。補齊實際混合工作、HTTP、取消／停機、可觀測性及調校驗收後才能關閉需求。
