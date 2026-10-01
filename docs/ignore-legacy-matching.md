# 舊格式來源解碼與批次匹配

## 固定來源與編碼

固定包裝器 blob `023c1e891532e5baa022ef0e48b7179e991f59e3` 使用沒有指定編碼的 `File.ReadAllText`。本段直接呼叫相同 API 產生 47 組對照，執行環境為 .NET 10.0.11；命令為 `scripts/test_ignore_decode.ps1`。原始 bytes 與解碼後 UTF-8 bytes 保存於 `internal/platform/legacyignore/testdata/decode.json`，可重新產生比對。

`DecodeSource` 辨識 UTF-8 BOM、UTF-16 LE／BE BOM、UTF-32 LE／BE BOM；無 BOM 時使用 UTF-8。錯誤編碼依對照結果替換為 U+FFFD，保留換行與 NUL。不能共用自有規則的嚴格錯誤編碼拒絕政策。原始資料最多 256 KiB、解碼結果最多 384 KiB，超限或取消不回傳部分文字。

另以固定 Ignore 0.2.1 來源執行含 NUL 的規則替代分支，確認 NUL 可作為規則字元；`(NUL|a).mkv` 接受 `/media/a.mkv`、不接受 `/media/b.mkv`。規則來源與正則轉換現保留 NUL，檔案路徑仍禁止 NUL。內部 request 格式從 JIG1 升為 **JIG2** 並拒絕舊版本；回覆格式 JIR1 不變。

## 批次流程

`scan.IgnoreScanner.MatchLegacyIgnore` 接受最多 128 個共用查詢目錄的候選。檔案以父目錄查規則；目錄以自己查規則，匹配時完整路徑追加單一 `/`。Windows 路徑轉為 `/` 分隔。

1. 原生來源觀察仍執行兩輪開檔與完整內容核對。
2. 沒有來源回傳 NoMatch；存在空來源交給匹配器，保留空檔排除全部的上游語義。
3. 解碼後整批送入固定 helper，驗證回覆數量、結果與行號。
4. 匹配後再觀察來源，token 必須相同。更近來源出現、身份或內容變動時，整批失效，不回傳部分決定。
5. 回傳領域來源證據供後續保存。這仍不是候選檔案身份的枚舉證明，正式掃描需維持原本持有目錄與檔案的約束。

## 實測

- 47 組 ReadAllText 對照在 Windows Go 測試全部吻合，包括 BOM、補充平面字元、截斷及錯誤編碼。
- Windows scan、legacyignore、helper、process、architecture 測試及全模組 vet／三命令 build 通過。
- Linux 原生 `/tmp` race：legacyignore 1.114 秒、helper 1.375 秒、scan 1.260 秒、architecture 1.222 秒。
- 兩平台真正 helper 接線驗證：UTF-16LE 來源、含 NUL 的規則、兩個候選一次啟動、Active 歸零、暫存目錄清空。Linux 非 race 的 process／helper／scan 專項分別 0.113／0.062／0.007 秒。
- Linux CI 的非 race helper 步驟加入 `TestLegacyMatchNativeHelper`；因正式子程序位址空間上限與 race 保留量衝突，原生子程序測試須獨立執行，純邏輯仍跑 race。

schema 保持 14。公開舊格式模式、兩種規則組合、基線與正式 worker／報告接線尚未完成；本段不代表完整語法或 G22 驗收。

JIG2 輸入協議另外執行 5 秒 fuzz，231,768 次輸入通過（2 workers）。增量品牌掃描 0 違規／100 合法命中，gitignore-check 通過。

PR34 有一個 PostgreSQL CI 工作因整套 race 超過 Go 預設 10 分鐘而中止，當時單項測試僅跑 1 秒；另一個同類工作通過。本段將 Makefile 與 PowerShell 的 race 套件總時限明確設為 20 分鐘，保留各 fixture 的 90 秒上下文及既有操作期限，未移除測試。新遠端 run 尚待驗證。
