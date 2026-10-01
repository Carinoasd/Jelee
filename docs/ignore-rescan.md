# 規則修改後重掃驗收

`TestIgnoreRunnerRescanSameSizeAndMtime` 使用同一個正式 worker、同一原生 scanner 與來源快取，連續執行四次工作。所有來源都是測試建立的臨時檔案，PostgreSQL 使用隔離 schema。

1. 規則排除 `a.mkv`：只把 `b.mkv` 加入媒體基線。
2. 規則改為排除 `b.mkv`：將 `a.mkv` 納入，保留被排除的 `b.mkv` 舊觀察版本。
3. 規則改回排除 `a.mkv`：更新 `b.mkv` 觀察版本，保留 `a.mkv` 舊版本。
4. 刪除 `.jeleeignore`：兩個媒體檔均重新觀察，排除計數歸零；只有刻意移除的規則檔被記為真正遺失。

前三次規則檔均為6 bytes，mtime固定為相同時間。測試確認實際來源檔符合這兩個條件，再驗證報告的規則目錄、行號、匹配路徑、scan/baseline來源，以及基線的檔案大小與觀察版本。這證明規則內容變更不會被相同size/mtime的舊快取遮蔽。

最終 Linux PostgreSQL race：1項頂層測試通過、零跳過，18.761秒；執行前後PG來源檔雜湊一致。[執行證據](evidence/ignore-rescan.json)。Windows僅編譯與vet驗證，未配置Windows資料庫整合環境。

本段只新增驗收測試與文件，未修改正式程式碼或遷移。前階段完整PG185項回歸見[API證據](evidence/ignore-api.json)。尚未驗證舊格式兼容、混合外部probe真媒體、worker取消與unknown完整情境；不能據此宣稱整體G22或全案完成。
