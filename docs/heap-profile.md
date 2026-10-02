# 混合 worker 的 heap profile 前後比較

狀態：兩組原生容器實測通過，完成 heap 前後比較子項。本文件只涵蓋 G42.10 的第一子項：在既有混合 worker 驗收中取得 `heap`／`inuse_space` 前後證據。五十萬條目完整掃描、十萬圖片處理及至少 24 小時穩態驗收仍各自待完成。

## 負載與既有門禁

沿用 [RSS 預算驗收](resident-memory.md) 的 1000 檔 fixture：400 個 NFO、100 個影片、500 個圖片，執行冷掃描、暖快取、受控替換、五分鐘持續暖掃、取消復原與 SIGTERM 收尾。圖片工作包含屬性及檔案盤點（inventory）；本輪未執行圖片解碼，不能作為十萬圖片處理的證據。

兩組分別使用 `GOGC=100` 與 `GOGC=50`，各自保留一組同程序、同來源的前後 profile。正式兩槽 Argon2id、worker 工作量與既有時限保持原設定。Linux amd64 驗收容器沿用 2 CPUs、128 PIDs、768 MiB 記憶體上限、零 swap，以及 512 MiB Go soft memory limit。

固定的父程序 RSS 門禁保持 **GOGC100 為 464 MiB、GOGC50 為 352 MiB**，仍由[預算檔](../tools/resident-memory-budget.json)核對。本輪 profiler 的配置與 RSS 也在既有採樣範圍內；新增 profile 不會自動提高門檻。原本的 heap、goroutine、cgroup OOM、工作完成計數、取消復原與停止守衛同樣保留。

## 兩次採樣的實際時序

採集程式只存在於 `jelee_probe_tests` 標籤的測試檔，隨既有 memory 模式啟用。一般產品 build 沒有新增 pprof HTTP 端點或背景 profiler。

| 階段 | 採樣點 | 與持續負載的關係 |
| --- | --- | --- |
| `before` | 冷掃描、暖快取與變更掃描三輪完成後，沿用既有 `runtime.GC()`／`ReadMemStats`；確認 NFO 呼叫與子程序均已閒置，再寫 profile | 等 controller 取回並回覆 SHA256 後，才進入 sustained 階段及開始五分鐘計時 |
| `after` | 五分鐘持續暖掃結束，先記錄 elapsed，再沿用既有 `runtime.GC()`／`ReadMemStats`，通過原本 heap／goroutine 斷言後寫 profile | 採集及取回耗時不算進原本持續工作秒數；完成後才進入取消復原測試 |
| 最終 `memoryProfile` | 取消復原、SIGTERM、worker／HTTP 停止及清理完成後結算 | 這是較晚的 RSS／cgroup 與 runtime 計數器結算，與 `after` profile 的時間不同 |

本輪沒有額外呼叫 GC、`FreeOSMemory`、profiler 暖機，也沒有修改 `runtime.MemProfileRate`。兩份 profile 使用相同且大於零的採樣率。每份 metadata 記錄採集前讀到的 `NumGC` 與相對程序觀測起點的整數奈秒時間。

`before`／`after` 比較涵蓋持續暖掃期間的存活配置變化。取消復原、最後一筆在途工作與停止清理發生在 `after` 之後，不能用這一對 profile 代表那些階段的 retained heap。

## 如何解讀數字

| 證據 | 含義與限制 |
| --- | --- |
| `inuse_space` | Go heap profiler 依配置抽樣與 GC 週期所估計的存活配置 bytes；適合找出保留配置的呼叫來源 |
| `HeapAlloc` | `ReadMemStats` 當下的 Go heap 配置計數；其採樣時間與計量方式不同，不能要求與 pprof 總量相等 |
| 父程序 RSS | `/proc/self/statm` 的近似 resident pages，加上每秒採樣的時間限制；包含 Go runtime、測試框架等程序記憶體 |
| cgroup current／peak | 容器被計入的記憶體，包括子程序及部分 cache／tmpfs；profile 檔案寫入 tmpfs 也可能影響它 |

採樣率相同有助於比較，仍有抽樣誤差及執行間波動。很小的前後差異不能單獨證明洩漏，也不要求差值必須為零。

profiler 自己會配置壓縮、編碼與暫存資料。首次採集造成的部分配置或快取可能延續到後測；這些成本必須納入解讀。單檔輸出限額只限制 artifact 大小，不能保證 profiler 內部所有暫存配置都受同一限額約束。整個測試程序仍受原本容器與測試期限限制。

分析選用 `inuse_space`，保留正負差值，不做 normalize 或移除負值。報告中的淨變化由 `after` 與 `before` 的 raw sample 總量相減；pprof diff 表頭的 magnitude 另列，避免把絕對值合計當成淨增長。函式列表最多 20 項，因此不能直接加總列表來取代完整 profile 總量。本輪沒有用這份比較推算配置速率、RSS 或 GC 暫停。

## 有界發布與取回

worker 在容器中排他建立 0700 的 `/tmp/jelee-heap-profiles`，固定產生：

- `before.heap.pb.gz`
- `after.heap.pb.gz`

每檔最多 8 MiB，兩份最多 16 MiB。檔案以 0600、`O_EXCL` 建立固定 `.partial`，由 `runtime/pprof.WriteHeapProfile` 寫入 gzip protobuf。只有寫入、關閉都成功後才 rename 成正式檔名。超量、短寫、producer 或 close 失敗均不發布完整 artifact，既有檔案也不覆寫。

發布後 stdout 只輸出固定 `heapProfileReady` 事件，內容為：

```text
version、stage、bytes、sha256、memProfileRate、numGC、elapsedNanos
```

事件沒有原始路徑、profile bytes 或底層錯誤文字。最終報告的 `memoryProfile.heapProfiles` 使用相同 metadata，依 `before`、`after` 固定順序保存。

容器 `/tmp` 是 tmpfs，必須在 worker 尚存活時取回。此 Docker 環境的 archive／`docker cp` 看不到 live tmpfs；小型重現已排除檔案權限因素。controller 改以 `docker exec` 呼叫 tagged 測試 binary 的 `--export-heap-profile before|after`，僅讀兩個固定檔名。匯出程式檢查普通檔、禁止 symlink、大小及前後檔案狀態；controller 再核對 SHA256 與 gzip 完整性。壓縮檔上限 8 MiB，解壓內容上限 64 MiB。

二進位 stdout 直接寫入排他建立的私有檔，不經 shell、TTY 或文字日誌。匯出程式有 20 秒 watchdog；controller 也以 20 秒執行預算限制 Docker CLI。若匯出失敗，清理流程會移除本次容器及其 exec 程序；單純殺掉 Docker CLI 不代表遠端程序已退出。

校驗成功後，controller 在既有唯讀 control 掛載的宿主端，以暫存檔再 replace 原子寫入 `heap-before-copied` 或 `heap-after-copied`。內容必須剛好是 64 字元 SHA256，沒有換行。worker 等待最多 30 秒並接受父 context 取消；缺回執、內容不符、階段逆序或缺失都使驗收失敗。

取回的檔案立即保存在本次專屬 `.testdata` 目錄。後測、分析或停止失敗時，已完整取回的前測仍保留；原始失敗不會被下一次成功覆蓋。容器、獨立 schema、secret 與 image 仍須按原流程清理。

## 本機 pprof 分析與隱私

測試 binary 使用固定 Go SDK、`-trimpath` 建置並在 `/worker.test` 執行。heap profile 含函式、來源檔名、mapping 與 build 資訊，因此原始檔與解碼文字先留在私有目錄；`-trimpath` 本身不足以構成完整的隱私檢查。

controller 使用固定 SDK 內附的 pprof，僅讀本地固定檔名，以 `-symbolize=none` 分析，不下載符號或啟動網頁。工作目錄與 `PPROF_TMPDIR`、`PPROF_BINARY_PATH`、`PPROF_TOOLS` 限於本次專屬分析目錄，不改使用者全域設定，也不發布測試 executable。

分析流程先以 `-raw` 核對 sample type、bytes 單位、採樣率及隱私，再產出下列三份摘要：

```text
pprof -top -inuse_space -unit=bytes -nodecount=20 -symbolize=none before.heap.pb.gz
pprof -top -inuse_space -unit=bytes -nodecount=20 -symbolize=none after.heap.pb.gz
pprof -top -inuse_space -unit=bytes -nodecount=20 -symbolize=none -base before.heap.pb.gz after.heap.pb.gz
```

上列是固定 SDK leaf tool 的引數示意，controller 以 argv 呼叫。每次分析的執行預算為 30 秒；raw stdout 上限 16 MiB，top／diff 及 stderr 各有 64 KiB 上限，超量或逾時會終止並等待子程序結束。OS 的 kill／wait 與 reader join 不宣稱有獨立硬期限。原始輸出不直接送入公開日誌。

隱私檢查拒絕已知宿主 checkout／使用者／cache 前綴、資料庫連線字串、token 與 fixture 私有標記。命中時保留私有失敗證據並拒絕發布；不以文字替換修改 protobuf。公開內容只使用核對過的 metadata、數值與函式摘要。

## 本輪實測

Linux amd64、Go 1.27.1 的兩組驗收均通過。完整來源、逐秒樣本、配對 SHA256、image／binary 身分、函式列表與原始報告雜湊見[本輪證據](evidence/heap-profile.json)。

| GOGC | before inuse bytes | after inuse bytes | 淨變化 bytes | RSS 峰值／預算 | 持續暖掃輪數／秒數 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 100 | 4,222,367 | 5,285,045 | +1,062,678 | 359.30 / 464 MiB | 19 / 312.35 秒 |
| 50 | 1,577,988 | 2,102,420 | +524,432 | 222.36 / 352 MiB | 19 / 314.13 秒 |

兩組的 `MemProfileRate` 都是 524,288 bytes。這些是 sampled inuse 數字，並非 `HeapAlloc` 或 RSS；前後增加量只描述本次短時間配對，不能據此判定洩漏或 24 小時穩態。各函式的 flat／cumulative 配置與帶正負號的 diff 保留在證據中。

兩組都完成 1000 檔冷／暖／變更掃描、正式兩槽 Argon2id 與持續暖掃，取消後基準不變且可復原，SIGTERM 後 HTTP、worker 與子程序均停止。正常容器退出 0，OOM／OOM kill 增量為零；另建的 OOM 負向退出 137 且 `OOMKilled=true`。自建 container、schema、secret 與 image 清理通過，原檔抽樣及受控替換的校驗保持。

最終來源共 869 份，實測期間沒有變動。Windows Go 有 64 個通過事件、Linux race 有 80 個，兩者各 2 個正常略過事件（需 opt-in 的獨立 memory snapshot／OOM 測試，已由原生容器流程另行執行）；事件數包含父測試。兩平台 Python 各 63 項通過，CI 契約包 8 個步驟通過。另用小型真實 profile 驗證固定 SDK 的負差值解析，以及同 UID 匯出、SHA 回執與清理。

首輪完整驗收因 Docker archive 看不到 live tmpfs 而失敗；先以小型容器重現並排除權限因素，再改用固定匯出命令，最後重跑上述兩組。首輪失敗、Windows 時鐘粒度／清理斷言修正、宿主 profile 隱私拒絕及 Linux FIFO fixture 路徑問題都保留在證據，沒有以成功結果覆蓋。

原始 profile 與 raw／top 文字留在私有忽略目錄；正式測試 binary 清理後只保存 SHA。CI 上傳驗收日誌、校驗後的摘要及分析狀態，排除原始 profile、private 文字及 binary。全量品牌檢查仍有 14,735 項殘留，遠端新提交的結果需另查。

本輪完成 G42.10 的 heap／inuse_space 前後比較子項；整行維持「部分完成」。五十萬條目完整掃描的現行容器預算／GC 驗收、十萬圖片的實際處理及至少 24 小時驗收仍待接續。

## 程式入口

- [測試採集與回執](../internal/platform/runtime/heap_profile_test.go)
- [既有負載的兩個採樣點](../internal/platform/runtime/nfo_acceptance_test.go)
- [controller 取回與配對](../scripts/runtime_heap_controller.py)
- [檔案、隱私與 pprof 分析校驗](../scripts/heap_profile_acceptance.py)
- [既有 RSS 預算與證據](resident-memory.md)
