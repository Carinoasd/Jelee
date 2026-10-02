# 三輪清單掃描與快照清理

## 範圍與方法

沿用 schema43 的正式 Scanner、worker 與 PostgreSQL。每次在獨立 schema 及 Linux 原生 ext4 目錄建立一萬、十萬或五十萬個 34-byte 自製檔案，依序執行首次掃描、不變重掃、第三輪清理重掃。

第三輪必須清掉第一輪留下的不可見快照。每輪核對可見基準及工作清單數量、原始檔案抽樣、128 項批次與 attempts=1；額外直接核對基底表的可見列數。相同檔案數的本測試中，首次完成保留 N 列，第二、三輪皆保留 2N 列。這是本測試終態的斷言，不是任何不同大小媒體庫都固定保留 2N 列的宣稱。

GOMAXPROCS 分別設為 2、4，GOMEMLIMIT=512MiB、單一 worker；不把 GOMAXPROCS 稱為硬體 CPU 配額。PostgreSQL、WSL 主機資源不由這個值限制。每檔位保存每秒 heap／RSS／GC／goroutine／FD／連線樣本與強制 GC 前後的 profile，生成素材及 profile 不計入掃描耗時。

列數表示交易可見資料，不代表 PostgreSQL 表檔已縮小或磁碟空間已歸還。每組設定目前各量測一次；掃描輪次不同，不用這些單次數值宣稱穩定加速倍數。

## 資料庫儲存環境

最初使用整合測試既有的 2 GiB PostgreSQL tmpfs。五十萬檔案第三輪填滿資料目錄，checkpointer 因空間不足中止，資料庫恢復期間讓狀態查詢失敗。這次失敗保留，不能歸因為 worker 鎖問題或算成通過。

正式矩陣改用相同固定 PostgreSQL 16.15 映像、獨立容器及原生 ext4 資料目錄，啟動前確認至少 20 GiB 可用；只綁定本機回環位址。六組設定全部在相同新環境重新量測，不混用先前 tmpfs 的時間，也不直接和舊報告計算加速倍數。

## 驗收狀態

六組設定、十八輪掃描皆通過，均 attempts=1，批次不超過 128。第二、三輪保留列數皆為 2N，原始檔案抽樣保持。

| GOMAXPROCS | 檔案數 | 首次（秒） | 重掃（秒） | 清理重掃（秒） | heap 峰值（MiB） | RSS 峰值（MiB） |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 2 | 10,000 | 3.55 | 3.76 | 4.77 | 3.37 | 22.66 |
| 2 | 100,000 | 45.55 | 23.76 | 24.76 | 3.32 | 23.39 |
| 2 | 500,000 | 129.30 | 122.76 | 136.76 | 3.49 | 23.75 |
| 4 | 10,000 | 3.04 | 3.27 | 3.27 | 2.72 | 23.15 |
| 4 | 100,000 | 26.05 | 23.51 | 25.51 | 3.19 | 23.34 |
| 4 | 500,000 | 127.55 | 124.28 | 133.77 | 3.45 | 24.19 |

[機器可讀證據](evidence/scan-repeated.json)。本段只擴充 opt-in 測試與報告，產品程式碼未修改；先前 schema43 的跨平台／race／完整 PG 與串接回歸見[快照驗收](inventory-snapshots.md)。

## 重現設定

使用專用 `jelee_test` 資料庫及環境變數 `JELEE_TEST_DATABASE_URL`，不要把連線字串寫進報告。使用 manifest 固定的 Go 工具鏈，設定 `JELEE_REQUIRE_INTEGRATION=true`、`JELEE_RUN_SCAN_SCALE=true`、`JELEE_SCAN_SCALE_FILES=10000|100000|500000`、`JELEE_SCAN_SCALE_OUTPUT` 為尚不存在的絕對輸出目錄，以及 `GOMAXPROCS=2|4`、`GOMEMLIMIT=512MiB`。執行 PostgreSQL 套件的 `TestScanScale`，加上 `-count=1`，不用 race 版作耗時量測。

Go 的 TMPDIR 必須位於原生 ext4；專案若位於 Windows／DrvFS，需在既有 `toolchain.go_environment` 建立環境後，把 TMPDIR／GOTMPDIR 指向原生磁碟上的專用臨時目錄。Go 測試會核對檔案系統、可用空間及 inode；PostgreSQL 資料目錄的空間須另外預檢。

## 限制

這是純清單負載；未包含忽略規則歷史合併、圖片解碼、混合 probe／NFO 負載、容器 OOM 或真實 24 小時穩態。三輪不變重掃不能證明長時間沒有資源增長；仍需獨立長測。既有全量品牌與 ABI 門禁也不因本段量測通過而解除。
