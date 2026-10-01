# 合併忽略模式的准入交易

應用層與儲存層提供明確的合併模式准入合同。HTTP／CLI 掃描與正式服務已接線，功能開關及 helper 健康檢查詳見 [正式服務說明](ignore-family-runtime.md)。

## 固定合同

合併模式為 `jeleeignore-legacy-v1`，大小寫策略可為 `sensitive` 或 `ascii-insensitive`。伺服器固定程式版本 `jeleeignore-legacy-v1` 與證據版本 `jeleeignore-legacy-proof-v1`；呼叫端不能指定規則內容、來源證據或媒體根。原模式的驗證與提交入口仍拒絕合併合同。

`FamilyIgnoreAdmissionRepository` 使用獨立的 `Custom`／`Family` 可用性；一種規則家族可用不能授權另一種。`ScanServices.FamilyIgnoreAvailable` 必須搭配完整的准入 repository，缺少依賴時拒絕啟動。未設定可用性視為不可用。

## 提交、重送與重試

交易先重新驗證管理員及登入狀態，再讀相同 key 的保留請求。請求的库、優先級、NFO／probe／忽略意圖必須完全一致；原請求仍保留時，即使目前家族不可用也可重送。重試從已失敗或取消的父工作複製模式與固定身份；建立新工作仍需要对应家族可用。重試工作已存在時可依相同 key 讀回。

NFO／probe 的固定身份、功能政策、庫世代、佇列限制、忙碌檢查、根目錄預算、歷史清理及最後授權檢查沿用共同交易。忽略模式只可搭配增量 probe；重建 probe 不接受此組合。任何失敗整筆回滾，不能留下普通未過濾任務。

## 原生驗收

正式 worker 測試現在透過應用層及准入交易建立合併請求，移除先建立原模式再以 SQL 替換請求的做法。原生驗收涵蓋發布、NFO 排除、中斷恢復、來源變更及未知來源；成功工作再讀正式報告，確認自有與舊格式排除來源都保留。

公開入口、helper 健康檢查、服務停止後清理及功能開關已另段驗證；G22 與第 3 階段未完成。

## 專項證據

- 真實 PostgreSQL race 准入與既有請求合同：16 項頂層通過，零失敗／略過，36.203 秒。
- 正式准入到原生 worker 及報告：5 項通過，零失敗／略過，20.817 秒；公開 HTTP／CLI 掃描入口已另段切換並驗證。
- Windows domain／app／HTTP／runtime／PostgreSQL 模組测试、全 vet、三命令 build 通過。
- Linux domain／app／HTTP／runtime／architecture race 通過，分別為 1.068／1.031／3.224／1.084／1.118 秒。

[完整 PostgreSQL race 回歸證據](evidence/ignore-family-admission.json)：252 項頂層通過，零失敗、零略過，349.781 秒，受驗來源未變。完整 G22 仍未完成；服務接線已有獨立驗收。
