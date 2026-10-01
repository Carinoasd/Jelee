# 合併忽略模式的工作程序整合

目前已完成私有讀取與依賴合同，正式工作程序尚未啟用合併模式。

`ReadExecutionIgnoreRequest` 只讀取目前租約所屬任務保留的請求。原模式仍使用原本的驗證；合併模式需要有效任務／媒體庫 ID、明確大小寫模式，以及配對的程式與證據版本。取消、過期世代、缺失請求與不一致的啟用標記會拒絕讀取。既有 `ReadIgnoreRequest` 與公開提交驗證繼續只接受原模式。

`FamilyIgnoreExecutionRepository` 由 PostgreSQL Store 完整實作，包含掃描、基線分類、三路復核與共同封存後的發布。`FamilyIgnoreScanner` 由原生掃描適配器實作，沿用現有自有規則、舊格式來源及缺失邊界的重新觀察流程。介面實作以編譯斷言驗證。

## 驗證

- 真實 PostgreSQL race 請求專項：16 項頂層通過，零失敗、零略過，43.674 秒，測試期間來源未改變。
- Windows app、domain、scan、postgres、architecture 測試通過，全專案 vet 與三命令 build 通過。
- Linux 原生 race：domain、app、scan、architecture 通過。

## 接續工作

接入 runner 的模式辨識、合併模式選項與啟動依賴驗證，加入明確工作認領能力；完整執行掃描、分類、NFO／probe、三路重新觀察、封存與發布。補足正式 worker 在中斷、重啟、取消、來源改變與租約失效下的原生驗收後，才能開放公開入口。G22 與第 3 階段仍為部分完成。

完整 PostgreSQL race 回歸：242 項頂層通過，零失敗、零略過，351.415 秒，sourceUnchanged=true。
