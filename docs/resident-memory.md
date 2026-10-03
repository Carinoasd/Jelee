# 常駐記憶體預算與回歸門禁

本段對應 G42.9 的固定基線記憶體預算與 CI 校驗。既有管理員指標端點已提供 heap、goroutine、GC 累計暫停及配置 counter；配置速率可由 counter 的 `rate()` 計算，詳見[指標契約](metrics.md)。此處新增固定混合負載的父程序 RSS 採樣預算，沿用[執行時記憶體驗收](runtime-memory.md)的 workload、容器與 OOM 檢查。

基線量測後，兩組預算已凍結，再以兩個新容器執行相同負載。兩組獨立驗證均通過；[完整證據](evidence/resident-memory-gate.json)保留原始採樣、來源雜湊及其他必要驗收結果。

## 基線與固定門檻

基線為 `mixed-1000-default-kdf-v1`：400 個 NFO、100 個影片及 500 個圖片 fixture，包含冷掃描、暖快取、受控替換、五分鐘持續掃描、取消復原及停止。每組使用正式兩槽 KDF，完成兩次 hash 與兩次 verify。測試容器為 Linux amd64、2 CPUs、128 PIDs、768 MiB memory 上限、零 swap；兩組 `GOMEMLIMIT` 都是 512 MiB。

| 項目 | GOGC=100 | GOGC=50 |
| --- | ---: | ---: |
| 基線採樣最大 RSS | 358.5390625 MiB | 277.1875 MiB |
| 固定 RSS 預算 | 464 MiB | 352 MiB |
| 固定預算 bytes | 486539264 | 369098752 |
| 有效樣本數 | 396 | 402 |
| 整段觀測時間 | 387.944 秒 | 393.392 秒 |
| 持續暖掃描 | 20 輪／約 304.92 秒 | 21 輪／約 311.67 秒 |

預算算法為各組基線採樣峰值增加 25% 工程餘量，再向上取整至 16 MiB。每組只有一次基線量測，這項餘量未估計統計波動。門檻以 byte 整數固定在[預算檔](../tools/resident-memory-budget.json)，待測執行不會依當次峰值自動調高。完整樣本與基線結果見[基線證據](evidence/resident-memory-baseline.json)。

## 量測範圍與限制

- RSS 取自執行 workload 的 worker Go 程序 `/proc/self/statm` 第 2 欄，依系統 page size 換算 bytes；不包含 helper、ffprobe 或外部 PostgreSQL 程序。它與 Go `HeapAlloc`、容器 cgroup 用量是不同範圍。
- Linux man-pages 明示 `statm` 的 resident 值會受 kernel 記帳最佳化影響而不精確；kernel 文件說明 RSS 記帳採非同步方式。需要更精確的單次觀測可使用 `smaps`／`smaps_rollup`，但成本較高。本門禁固定沿用同一來源比較基線，報告標記 `approximate=true`。[statm 官方 man-page](https://man7.org/linux/man-pages/man5/proc_pid_statm.5.html)、[Linux Kernel /proc 文件](https://docs.kernel.org/filesystems/proc.html)。
- 每秒採樣仍可能漏掉間隔內的短暫峰值；表中數字是採樣最大 RSS，不能當作作業系統保證的瞬時上限。容器 `memory.peak` 與 OOM events 仍是必需的獨立守衛，768 MiB 容器硬上限不充當這份 RSS 預算。
- 每組最多保存 1024 筆，並在 `startup`、`cold`、`warm`、`changed`、`sustained`、`cancellation`、`shutdown`、`stopped` 八個階段記錄樣本。驗證要求階段完整且有序、時間遞增、相鄰樣本間隔不超過五秒、持續階段至少五分鐘及採樣完整。缺值、超出筆數上限或採樣失敗都不能補零後通過。

## 固定來源與失敗處理

[Controller](../scripts/test_nfo_worker.py) 只讀取兩個固定路徑：

- `tools/resident-memory-budget.json`，最多 64 KiB。
- `docs/evidence/resident-memory-baseline.json`，最多 4 MiB。

沒有環境變數可替換預算路徑或略過門禁。JSON 重複 key、NaN／Infinity、缺檔或超大檔案均拒絕。Controller 對基線檔的實際 bytes 計算 SHA256，由[驗證器](../scripts/resident_memory.py)核對預算內的 `baselineEvidenceSha256`、各組 `baselineSourceDigest`，並從樣本重算 `baselinePeakBytes`。固定檔記錄的來源如下：

| 識別 | SHA256 |
| --- | --- |
| 兩組基線的來源 digest | `6cc2731c0f64fb819bc5b08e98fdfa94fc357704bbcd860f3a7b8dd24b2a8d07` |
| 基線證據檔 bytes | `56288a9b07a22068445eb404049a16403a49c64a570f4486e59eebf74a2f06ac` |

預算與基線檔也納入待測來源 digest；載入前後、各組執行前後及兩組結束後都檢查來源是否改動。未來待測來源可以不同於歷史基線，才能偵測程式變更造成的回歸；一次驗收執行期間則須保持來源不變。

每組先保存採樣驗證結果，再記錄預設失敗的 `residentBudget`，包含預算 hash、門檻及觀測峰值。只有固定預算比較成功後才改為通過。結束前還會從原始樣本重新驗證 GOGC=100／50 兩組結果，不能只信任既有成功旗標。超標或其他失敗仍保存 case、可取得的日誌與清理結果；預算載入失敗也會留下失敗 summary。清理只針對該次 UUID 自建資源。

## 本機與 CI 入口

在已備妥工具、媒體 fixtures、Linux Docker 與專用 `jelee_test` PostgreSQL 的環境中，以 `JELEE_TEST_DATABASE_URL` 提供測試連線後，沿用原入口：

```sh
make runtime-memory-test
```

此入口先執行 contracts，再執行兩組真容器負載。[CI workflow](../.github/workflows/jelee.yml) 分開執行相同的 contracts 與 `make runtime-memory-worker-test`；預算失敗會使步驟失敗。`runtime-memory-acceptance` artifact 保存 contracts JSON、驗收日誌、summary、各組 case，以及固定預算／基線檔，失敗時也嘗試上傳。

本機產物位於 `.testdata/runtime-memory-*`。Controller 拒絕覆寫既有驗收日誌或 summary；重跑前先另存歷史證據。基線資料與凍結後的獨立門禁結果須分別記錄，不能把用來制定門檻的基線稱為獨立門禁已通過。

## 獨立門禁結果

| 項目 | GOGC=100 | GOGC=50 |
| --- | ---: | ---: |
| 採樣最大 RSS | 358.65234375 MiB | 277.39453125 MiB |
| 固定 RSS 預算 | 464 MiB，通過 | 352 MiB，通過 |
| 有效樣本數 | 406 | 397 |
| 整段觀測時間 | 397.737 秒 | 388.519 秒 |
| 持續暖掃描 | 21 輪／約 313.74 秒 | 20 輪／約 304.28 秒 |
| 最大採樣間隔 | 約 1.017 秒 | 約 1.021 秒 |
| cgroup `memory.peak` | 347.6953125 MiB | 272.109375 MiB |

待測來源 digest 為 `846c44a2e15a7f022faf06bb3ce7e0bc543aab397f76861f4452f614b66f96e1`，固定預算檔 SHA256 為 `8cd20c103e92ee1ae11677998e553650eec553bdbf98a0f1199f44297ae61e99`。兩組來源與 862 份凍結檔案保持不變。基線與門禁使用相同的 Go 採樣程式；中間新增的是固定預算、Python 校驗及 CI 接線。

兩組均完成原本的檔案計數、正式兩槽 KDF、取消復原、正式 `/jelee` 入口健康檢查與 SIGTERM。正常容器退出碼為零，OOM 事件增量為零；獨立 OOM 負向仍退出 137 且 `OOMKilled=true`。自建 schema、容器與 image 均清理，原始 fixtures 保持。RSS 與 cgroup 的記帳範圍、共享頁歸屬及近似程度不同，不以兩者數字大小互相代替。

負向重播在真基線的複本中注入一筆超出預算 1 byte 的 RSS，兩組子程序均以退出碼 1 拒絕；這是純驗證器測試，不是新增實測或 OOM。控制器測試另確認超標時保留 samples-only 結果、失敗狀態、門檻、觀測值與 hash，仍完成清理。

Windows Go 79 個通過事件／13 個略過，Linux race 73／7；事件包含父測試，依賴資料庫或專用容器的普通略過項另列。44 個 Python 測試涵蓋容器證據、失敗保存、RSS 及控制器，Linux contracts 另驗真正的 Compose 合併與 Go 原生子程序。最初 Windows 採樣單元測試的時鐘解析度問題已修正；控制器測試在沙箱中遇到私有暫存目錄權限限制，改以標準本機權限執行通過。原生 RSS 讀取未增加等待或捏造時間戳。

遠端 CI 須依該提交的實際結果確認；完整品牌門禁仍有既存舊名稱殘留，不能宣稱全綠。

本段僅約束上述固定負載與環境，不外推為典型 4C8G 主機或所有合法配置的容量保證。G42.10 的 pprof 前後比較、50 萬條目完整掃描、十萬圖片及至少 24 小時驗收不在本段完成範圍。
