# 五十萬條目單輪掃描記憶體驗收

狀態：**五十萬條目單輪原生驗收通過**。本輪驗收 G42.10 的五十萬條目完整掃描子項，使用一輪純檔案盤點（inventory）。圖片實際處理與 24 小時穩態仍待驗收，G42.10 整項維持部分完成。

## 執行方式與前置條件

在 Linux 原生 Docker 環境執行，先設定專用測試資料庫的 `JELEE_TEST_DATABASE_URL`，以及該既有 PostgreSQL 容器名稱 `JELEE_SCAN_MEMORY_PG_CONTAINER`。連線資訊須指向允許建立獨立測試 schema 的 `jelee_test` 資料庫。

```sh
make scan-memory-smoke-test
make scan-memory-test
```

第一個命令只有 **1000 檔**，報告標為 `scope: smoke-1000`、`finalAcceptance: false`；它用來檢查接線與收尾。第二個命令固定 **500000 檔**，只有全部守衛通過才可標為 `scope: scan-500000`、`finalAcceptance: true`。短測試通過不能代替五十萬驗收。

Controller 在原生 ext4 的 `/var/tmp` 下建立本輪專屬目錄，存放 fixture 與編譯產物。啟動前核對 fixture 和 PostgreSQL 資料儲存均為原生 ext4、各至少有 **20 GiB 可用空間**，並檢查所需可用 inode；不符合即拒絕執行。每個 fixture 為 34 bytes，按每 1000 檔一個目錄分布，掛載至容器的唯讀 `/media`。

測試使用獨立 schema，清理自己建立的容器、schema 與暫存產物；保留既有 PostgreSQL 容器及其餘資料。證據留在 `.testdata/scan-memory-<UUID>/`，記錄來源、預算與測試 binary 的雜湊。失敗同樣保留脫敏日誌，不輸出 DSN、登入憑證或宿主私有路徑。

## 實際驗證範圍

`jelee_probe_tests` 標籤下的 `TestScanMemoryAcceptance` 使用正式 Fx 組裝、HTTP server、帳號服務、PostgreSQL Store 與 worker：

1. 以正式預設 Argon2id 參數建立管理員，再透過 HTTP 密碼登入取得憑證。
2. 透過 HTTP 提交一次掃描，等待工作成功；要求只執行一次、檔案與位元組數精確相符，沒有跳過、遺失或待確認項目。
3. 核對 DB 的本次 inventory、已生效 baseline、snapshot 完成狀態與屬性；要求一個 root、所有目錄完成且沒有待處理目錄。另核對探測、NFO 與忽略工作未啟用。
4. 比對首、中、末三個原檔的身分、大小、時間、模式及內容。這是原檔抽樣證據，並非逐一雜湊五十萬檔。
5. 完成後輸出就緒訊號，由 controller 傳入真正 `SIGTERM`；確認 Fx 停止、HTTP 關閉、lifetime context 取消、工作租約歸零，且此 runtime 的 DB 連線清空。

程序觀測從登入與 Fx 建構之前開始，涵蓋 `startup → login → scan → shutdown → stopped`。掃描起訖使用相同單調時鐘；正常結束在停止與連線檢查後結算，schema 刪除不計入該觀測窗。沒有增加 GC、`FreeOSMemory`、暖機、額外掃描輪次或 pprof 採集。

## 固定資源與門檻

門檻由 [tools/scan-memory-budget.json](../tools/scan-memory-budget.json) 固定，不能依當次結果自動放寬。

| 項目 | 設定或門檻 |
| --- | --- |
| Worker／掃描上限 | 1 個 worker；`maxEntries=500000`，短測試亦相同 |
| 執行資源 | 2 CPUs、`GOMAXPROCS=2`、128 PIDs |
| GC／Go soft limit | `GOGC=100`、512 MiB |
| 容器 hard limit／swap | 768 MiB／0 |
| 程序 RSS | 每筆採樣不超過 464 MiB |
| RSS 採樣 | 每秒及階段切換；最多 3600 筆，間隔不超過 5 秒 |
| 全段觀測 | 不超過 1 小時；缺失、不完整或倒退的證據均拒絕 |
| 每次 GC STW 暫停 | histogram 差分所在桶的保守上界不超過 50 ms |
| GC 暫停占比 | 以下兩種比例各不超過 1% |

464 MiB 沿用既有容量目標；**50 ms 與 1% 是實測前訂定的保守工程門檻**，不是需求原文的數字，也不是由本輪五十萬掃描樣本推導出的統計基準。

### GC 的兩種 1% 指標

前後讀取完整 `/sched/pauses/total/gc:seconds` histogram，計算每個桶的計數差分，保留所有桶，避免只看 `MemStats.PauseNs` 最近 256 個 GC cycle 的環形緩衝區。

- **計數器比例**：`ΔPauseTotalNs / counterIntervalNanos ≤ 1%`。
- **Histogram 保守比例**：`Σ(桶計數差分 × 該桶上界奈秒) / histogramIntervalNanos ≤ 1%`。

各分母使用「後次讀取開始 − 前次讀取完成」，即兩次讀取之間可確定涵蓋的最短區間。計數器與 histogram 並非同一個原子快照，分母分開計算，不以較寬的整體執行時間稀釋比例。

`PauseTotalNs` 隨已完成的 GC cycle 更新；histogram 記錄個別 GC STW 暫停，含開始停止世界到恢復執行的時間。兩者的邊界及事件數不必相等，不能把 histogram 次數當成 `NumGC`。任一新增暫停落在上界超過 50 ms 的桶就拒絕，即使無法知道該事件的精確時間；有事件落在無限上界桶、計數倒退或資料缺失也拒絕。這些指標不等同整個 GC cycle 耗時或 GC CPU 使用率。

## 記憶體證據的邊界

RSS 來自 worker 程序的 `/proc/self/statm`，包含 Go heap 以外的常駐頁。Kernel 記帳與每秒採樣都有近似性，短於採樣間隔的尖峰可能未被捕捉。`HeapAlloc`、RSS、Go soft limit 與容器 cgroup 用量意義不同，不能互相代換。

Controller 另核對 cgroup 的 hard limit、swap、peak 與 OOM 事件，作為獨立守衛。PostgreSQL 在外部容器執行，其記憶體不計入 worker 的 RSS 或此 worker 容器的 cgroup 預算；本輪不能據此宣稱整個部署只需 464 MiB。

先前完成的 [heap 前後比較](heap-profile.md) 提供混合 worker 的 `heap/inuse_space` 證據，本輪直接引用，不重做三輪掃描、GOGC 矩陣或 pprof。它不能代替本輪五十萬檔結果。

## 本輪實測結果

本輪 [來源、採樣與驗證證據](evidence/scan-memory.json) 的 source digest 為 `ba1c92fe86842a70d9953e91b6ac4d409ada342b226cd1555c7949167e2842fb`，876 份凍結來源在實測前後保持一致。

| 項目 | 正式五十萬檔結果 | 門檻 |
| --- | ---: | ---: |
| 檔案／位元組 | 500,000／17,000,000 | 精確相符 |
| inventory／已生效 baseline | 各 500,000 筆 | 精確相符 |
| 完成／待處理目錄 | 501／0 | 全部完成 |
| HTTP 提交至觀察成功 | 136.28 秒 | 單次嘗試 |
| 全段觀測／RSS 採樣 | 138.11 秒／144 筆 | 1 小時／最多 3600 筆 |
| 程序 RSS 峰值 | 154.32 MiB | 464 MiB |
| Go heap 採樣峰值 | 123.40 MiB | 補充觀測 |
| GC 暫停最高桶上界 | 0.196608 ms | 50 ms |
| 計數器暫停比例 | 0.02080% | 1% |
| histogram 保守暫停比例 | 0.02335% | 1% |
| GC cycle／STW 暫停事件 | 633／1266 | 分別記錄 |
| 容器 cgroup peak | 138.44 MiB | 768 MiB hard limit |

RSS 與 cgroup 採不同記帳方式，不用兩者的大小關係判斷成功。實際設定為 GOGC100、Go soft limit 512 MiB、2 CPUs、無 swap。原檔抽樣保持，SIGTERM 後 HTTP、lifetime、租約與連線清理通過；容器退出 0，OOM 增量零。所有本輪自建資源已清理，既有測試 PostgreSQL 保留。

同來源的 1000 檔短測試先通過，RSS 152.95 MiB、掃描 0.777 秒，明列 `finalAcceptance: false`。正式結論只引用後續五十萬檔那一輪。

專項驗證：Windows Go 與 Linux race 各 24 個通過事件、1 個需原生容器的略過事件；Python Windows 21 項通過、1 項 Linux 程序群組測試略過，Linux 22 項通過。CI 契約的 10 步、tagged vet、格式與差異檢查通過。90 份既有 SQL、五份 LiveTV 核心、LICENSE 與需求原文未改。

首輪前置檢查正確拒絕位於 tmpfs 的 fixture 目錄，改用 `/var/tmp` 的 ext4 後才執行負載；既有 PostgreSQL 儲存維持 ext4。第一次原生短測試因測試請求包含 API 不接受的 `ignore:null` 而失敗，省略該可選欄位後重跑專項、短測試與正式負載。失敗與修正後證據均保留，沒有改動產品 API 或放寬門檻。

這是一輪固定 Linux 資源設定的完整純清單掃描；影片探測、NFO、忽略規則、十萬圖片解碼及至少 24 小時運行另有驗收範圍。完整品牌門禁仍有既存殘留，遠端 CI 結果另行查核。
