# 任務交易鎖順序：CI 失敗與修正驗證

日期：2026-10-01。狀態：**本地修正驗證通過；修正後遠端 CI 待執行**。

本文件先保存 PR #13 的失敗證據。另一輪通過不能抵銷已觀察到的 deadlock；後續須在修正來源上補上可重現的交易測試及真媒體驗收。本文不宣稱 ignore C2 比較或 FS worker 已完成。

## 來源與 CI 範圍

- PR head：`89362b5a4569197c5d728dfbd4d4d85442dd0c08`。
- [PR run 36806931736](https://github.com/MoYuanCN/Jelee/actions/runs/36806931736) 的 checkout 日誌記錄：`d55fad9` 將上述 head 合併到 `e9aea71540ea4add86b2ebeabfb81f7e764ba1b1`。因此這是 PR 合併工作樹的執行結果，不能把 checkout 記成單獨 head。
- [同 head 的 push run 36806927198](https://github.com/MoYuanCN/Jelee/actions/runs/36806927198) 已由主代理查核完成狀態。兩輪結果不同，符合並發問題，並非修正證據。

| 檢查 | PR run 36806931736 | 同 head push run 36806927198 |
| --- | --- | --- |
| Windows foundation | 通過 | 通過 |
| Linux foundation | 通過 | 通過 |
| PostgreSQL 主整合及 race 步驟 | 通過 | 通過 |
| 真媒體 probe worker 驗收 | 失敗，GET 回應 503 | 通過 |
| 真 NFO worker 驗收 | 不列為通過；前序 probe 步驟失敗 | 通過 |
| 全量 branding | 既有失敗 | 既有失敗 |

PR run 的 `make bootstrap tools-verify test-integration test-race` 已結束並進入真媒體步驟；PostgreSQL race 套件日誌記錄 `ok .../internal/adapter/postgres 237.702s`。這只能證明該次測試套件通過，不能涵蓋後續真服務的同時輪詢與 worker 交易。

## 已觀察到的失敗

時間均為 CI 日誌的 UTC：

1. `02:45:16` 啟動 `scripts/test_probe_worker.py`。
2. `TestProductionProbeWorkerAcceptance` 在 `83.29s` 後失敗。測試第 241 行記錄 `/api/v1/jobs/{id}` 的 GET 回應 HTTP `503`；此數字是整個測試耗時，不能解讀為單次 GET 延遲。
3. PostgreSQL 在 `02:47:24.197` 與 `02:47:24.286` 記錄兩次 `deadlock detected`。第一個 cycle 涉及 PID 1276、1277、1395；第二個涉及 1277、1395。
4. 日誌顯示一方等待 `pg_advisory_xact_lock(hashtext(current_schema()),17481204)`；另一方執行 worker 最後的 `UPDATE jobs SET generation=generation ... lease_until>clock_timestamp()`，等待對方 transaction 的 ShareLock。
5. make 步驟以 exit code `2` 結束。驗收 artifact 只有失敗文字日誌，沒有成功 summary JSON。外層清理日誌有 `owned acceptance schema cleaned`，並記錄移除該次容器與測試映像。

原始 PostgreSQL 日誌也包含整合測試故意觸發的限制、權限與逾時錯誤。此處只將真媒體時段的兩筆 deadlock 列為本次故障證據。

## 鎖順序問題

失敗來源中的 `authorizedJobs` 先呼叫 `authorizedTransaction`。後者以 `FOR UPDATE OF u,s` 鎖住當前 actor 的 user 與 session，之後 `authorizedJobs` 才取得 jobs advisory lock。即使是 `GetJob`，也走此交易路徑。

worker 的 `jobTransaction` 則先取得 jobs advisory lock，接著修改任務資料；`jobs.actor_id` 與其他 actor 關聯資料有 user 外鍵，可能再等待同一 actor 的資料列鎖。這形成相反順序：

| 路徑 | 原先鎖順序 |
| --- | --- |
| 管理員任務讀寫交易 | actor user/session → jobs advisory |
| worker 任務交易及 actor 外鍵檢查 | jobs advisory → actor 相關資料列 |

來源中的相反鎖順序與 PostgreSQL 的等待 cycle 一致。日誌提供 transaction／advisory 等待，不包含每個隱含外鍵檢查的完整執行堆疊；修正仍須以受控並發測試證明，同時保留交易內即時驗證 actor、撤銷 session 及帳戶停用的語義。

## 證據識別

已下載並核對的原始記錄位於忽略目錄 `.testdata/ci-ignore-inventory/`；未將完整服務日誌複製到本文件，也未記錄資料庫連線憑據。

| 記錄 | SHA-256 |
| --- | --- |
| `probe-worker-acceptance.txt`，7,121 bytes | `016f4b4cd38e551f5dbf7155698bbd8b3b70439b177cbf4b515434027f01ca0e` |
| `postgres-job.txt`，305,674 bytes | `58d8565095455b76734d52f848644a235b950b060af4b2bb99287d3f126c7da5` |
| GitHub 上傳 artifact ZIP，2,182 bytes，ID `11138257158` | `514356e58f5c208c01f24da28877ca3586738459def21b0abbc87f21b847479a` |

文字檔雜湊由本地檔案計算；ZIP 雜湊與大小取自該 CI 上傳日誌。兩者是不同內容，不應互換。原始 artifact：[probe-worker-acceptance](https://github.com/MoYuanCN/Jelee/actions/runs/36806931736/artifacts/11138257158)，該輪設定保留 7 天。

## 修正與驗證進度

| 項目 | 狀態 |
| --- | --- |
| 統一管理員任務交易與 worker 的鎖順序 | 已改為 account advisory → jobs advisory → actor rows；保留原授權條件與回滾 |
| 固定鎖交錯的真 PostgreSQL 回歸 | 同一測試修正前失敗、修正後 race 通過；透過 pg_locks 等待實際交錯，沒有用固定睡眠猜時序 |
| 修正後 PostgreSQL 整合與 race 回歸 | 132 頂層測試通過、0 skip，151.118 秒；來源雜湊未變 |
| 修正後真 probe／NFO worker 驗收 | probe 1,000項三輪實際呼叫1,000／0／17；NFO混合庫1,000與100項、取消／恢復及SIGTERM驗收通過，素材保護與測試資源清理通過 |
| 修正後 Windows／Linux foundation | Windows build/lint/全模組測試命令通過：25 測試 package、4 無測試 package；189 PG 與14工具/平台 skip。Linux vet 與三個命令 build 通過；本輪未重跑 Linux 全模組或 Windows race |
| 修正後 GitHub CI | 未完成 |

以上修正後結果來自本次實際命令；沒有沿用同 head 的舊成功 run。最初子代理的媒體執行被自動審查誤判為唯讀授權而拒絕；主代理核對使用者驗證授權與 UUID 測試資源範圍後，兩項驗收已成功執行。

具體來源與日誌 SHA 見 [驗證證據](evidence/jobs-lock-order.json)。修正維持 schema 008；所有已發布 migration、LICENSE 與原始需求不變。
