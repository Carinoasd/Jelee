# 清單批次入庫與規模基線

每批最多 128 項的掃描清單原本仍逐項往返資料庫。真實 pgx query tracer 量測：128 個檔案需要 395 次 SQL 往返，128 個目錄需要 267 次；重播同樣付出此成本。

本段以有界陣列合併查詢與寫入。先一次讀取該批既有大小及檔案／目錄衝突，再按既有順序驗證檔案數與位元組溢位，最後一次 upsert。目錄採一次衝突查詢及一次插入。資料、計數與租約末端守衛保持同一交易；重播保留既有 entry ID。衝突或到期則整批回滾。

## 已測範圍

規模工具使用 Linux 原生 ext4、自製的 34-byte 檔案、正式 scanner／worker／PostgreSQL、單 worker、128 項批次、GOMAXPROCS=2 與 GOMEMLIMIT=512MiB。它量測清單掃描，不執行媒體 probe 或圖片解碼。GOMAXPROCS 是 Go 排程設定，不代表獨占兩個 CPU；沒有清除作業系統快取，首次掃描不稱為冷快取。

素材建立與 profile 寫入不計入掃描耗時；完成輪詢約每 250ms 一次，資源每秒取樣。heap／RSS 峰值因此是觀測峰值。保存完整樣本及強制 GC 前後的 heap profile，結果不包含 DSN 或來源絕對路徑。pprof 使用預設配置的抽樣估計，小型 heap 的樣本誤差不能當成精確配置量；長期增長仍須穩態測試。

優化前一萬檔案首次 14.124 秒、不變重掃 12.699 秒；優化後最終樣本分別為 2.266 秒與 2.304 秒。這是單次本機比較，不能推論所有媒體庫的加速比例。

## 十萬檔案揭露的問題

十萬檔案尚未通過。掃描已到約 9.8 萬筆，收尾交易要把整份清單複製為接受基準。SQL 計時捕捉到該 INSERT SELECT 在約 1.982 秒被兩秒操作期限取消，另一次任務查詢等待工作鎖 1.500 秒逾時。失敗保留為反例，不能記為規模驗收。

下一段將基準建立拆成有界準備與短交易切換；需保持取消、恢復、舊基準可讀及忽略規則排除項的歷史屬性。五十萬、不同 CPU 設定、圖片負載、容器 OOM 與二十四小時穩態均尚未驗收。

## 重現

測試名稱 `TestScanScale`；預設明確略過。Linux 執行時需設定 `JELEE_RUN_SCAN_SCALE=true`、`JELEE_SCAN_SCALE_FILES=10000`（另接受 100000 或 500000）、`JELEE_SCAN_SCALE_OUTPUT` 為尚不存在的絕對輸出目錄，以及既有專用 `JELEE_TEST_DATABASE_URL`。TMPDIR 必須位於 ext4 且有足夠容量及 inode。執行 `go test -count=1 -timeout=125m -run '^TestScanScale$' ./internal/adapter/postgres`，使用固定工具鏈。

`JELEE_SCAN_SCALE_DIAGNOSTICS=true` 可記錄超過 100ms 或失敗的 SQL 模板與錯誤型別；不記錄參數或原始錯誤。診斷模式會增加追蹤成本，需與正式效能數字分開。測試只建立隨機自有 schema，結束時清理測試資料；輸出目錄保留證據。

[規模計畫](scan-scale-plan.md)。仍屬第三階段，沒有修改已發布的八十四份 SQL。

## 回歸驗證

| 驗證 | 套件 | 通過事件（含父測試） | 略過 |
| --- | ---: | ---: | ---: |
| Windows PostgreSQL 套件 | 1 | 181 | 536 |
| 完整 PostgreSQL race | 1 | 916 | 0 |
| 原生 worker | 1 | 11 | 0 |
| 完整 HTTP／TLS／PG | 1 | 1 | 0 |

[機器可讀證據](evidence/scan-batch.json)。SQL tracer 實測檔案與目錄每批均為 13 次往返，涵蓋首次與重播。新增衝突回滾與既有容量／溢位／租約到期案例通過。大型十萬檔案測試失敗獨立保存，未計入通過項。

[pprof inuse_space 前後摘要](evidence/scan-batch-heap-inuse.txt)。
