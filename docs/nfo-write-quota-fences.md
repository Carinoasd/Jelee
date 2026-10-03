# NFO 配額的交易快照防護

schema48 修復已在真 PostgreSQL 重現的配額漏洞：兩筆 Repeatable Read 交易先各自看到 31 筆準備資料，依序取得 advisory 鎖後，仍各自成功插入最後一筆，最後共有 33 筆。鎖能序列化寫入，無法刷新既有交易快照。

## 固定防護資料

新增 `nfo_write_quota_fences`，只有 preparation 與 job_intent 兩列。準備資料與工作意圖的 BEFORE INSERT trigger 先取得原來的同 schema advisory 鎖，再更新對應固定列。trigger 名稱排序在既有容量守衛之前；原本容量、雜湊與不可變約束保持。

Read Committed 等待後由原容量守衛使用當下資料計算。Repeatable Read 或 Serializable 若快照早於另一筆已提交的插入，更新固定列會產生 `40001`，整筆交易必須回滾並以新交易重試。拒絕不能只重試最後一個 INSERT。防護列缺少時回報 `23514`，不接受新資料。函式使用 invoker 權限、固定 pg_catalog search_path，並依 trigger 所屬 schema 明確定位資料表。

固定列更新隨整筆交易提交或回滾，沒有無界事件表；配額仍依請求、原文與完整輸出的邏輯 byte 長度計算，不依 TOAST 壓縮後的磁碟大小。兩類資料各自保留原固定鎖，跨類交易仍須遵循一致操作順序並處理 PostgreSQL 交易失敗。

## 升降版與生命週期

新增 migration48，001–047 共94份已發布 SQL 保持。48→47 若準備資料或工作意圖仍有任何列，拒絕移除防護；migration 留下 dirty 目標47，runtime Ready 拒絕。空資料才可移除兩個 trigger、函式與固定表；統計 epoch 與累計保持。

## 驗證範圍

本批真 PostgreSQL race 選測通過，139個通過事件、零跳過／失敗，涵蓋新配額測試及準備資料、工作意圖、metrics、完整升降版案例。Windows八個相關套件1381個通過事件、671個條件跳過；vet、格式、增量品牌0違規／339白名單、gitignore與diff檢查通過。這是相關選測，沒有重跑完整PostgreSQL套件。

測試涵蓋三種隔離層級的 actor32 列及工作意圖全域1024列；全域512MiB測試讓七個各約64MiB的獨立工作先提交，再拒絕第八個工作，核對整批回滾沒有工作、request或entry殘留。每個工作均低於128MiB，避免把單工作界限誤當全域容量證據。Serializable可在建立工作時先回報40001；測試接受整筆准入任一步驟的序列化拒絕，再核對沒有部分資料。

測試使用私有 SQL 夾具建立終態工作。合成 entry 只驗儲存約束與配額，不用於 Writer 重建或正式寫回授權。準備資料全域256列／256MiB的獨立邊界矩陣仍待補；不能由 actor 邊界推論全部容量已驗。

本批不啟用 read-write 政策、capability、公開准入或 worker。下一步仍為提交 journal／恢復保留、租約與媒體實體的提交邊界，再接正式 API／CLI／runtime 寫回。正式24h來源9a74a8932a不含本批。

初始Repeatable Read超量失敗、最終來源SHA256與完整範圍見[安全證據](evidence/nfo-write-quota-fences.json)。歷史schema47報告保持其原測試範圍。
