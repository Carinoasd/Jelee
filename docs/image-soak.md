# 圖片與掃描 24 小時驗收（實作中）

本頁記錄驗收工具的進度，**目前沒有正式 24 小時結果**，G42.10 仍未完成。既有十萬圖片結果見 [image-memory.md](image-memory.md)。

## 已實作

- 測試專用 Go 採樣器：每秒讀取 worker RSS、heap、GC 累積值和正式圖片 Processor 統計，階段切換另取樣。每批最多 60 筆、佇列最多 4 批；背壓超過 5 秒即失敗。只保留目前批次，總量上限 90,000 筆、時間上限 25 小時。
- 單一 JSONL 寫入器：固定 run ID、連續序號與非遞減事件時間；只接受明確的證據型別。每行最多 64 KiB、事件總量最多 64 MiB。目的地必須支援寫入期限；寫入逾時、取消、部分寫入或序列錯誤後不可恢復成功。工作輪次結束後仍允許關閉階段採樣。
- Python 採樣判定：跨批次檢查索引、時間間隔、累積計數器與記憶體上限，只保留最後一筆和聚合值。
- Python 穩態判定：288 個五分鐘輪次分成 24 組，每組 12 個靜止狀態檢查點。以第 2～4 小時作參考，比較末 3 小時與第 2～24 小時範圍；heap 容差 32 MiB、RSS 64 MiB。末 6 小時若連續每小時增加至少 1 MiB，判未建立穩態。這些是事前工程容差，不能解讀為零洩漏證明。
- 既有冷／暖 HTTP 圖片負載抽出共同的單調時間起點；原一小時驗收繼續由原 sampler 委派，未放寬既有門檻。

採樣的 RSS 上限仍為 464 MiB；Active ≤2、reserved ≤192 MiB、每圖估算 ≤96 MiB、cache ≤128 entries／32 MiB。Go 工具僅以 `jelee_probe_tests` 編譯，沒有新增產品採樣執行緒或 API。

## 尚待整合

以上元件尚未形成完整驗收。仍須接上單一事件消費者、輪次／rotation 型別、逐小時 GC 與 cgroup 邊界、真實掃描與圖片輪次、12 小時 session rotation、前後負例與 SIGTERM 清理，以及固定提交快照的背景控制器。控制器還需核對所有事件的身分與序號、原始檔大小與 SHA、退出狀態及自建資源清理。

正式流程將使用同一程序連續至少 86,400 秒、288 輪；smoke 固定兩輪／600 秒。合成資料、短測和單元測試都不能替代正式長跑。工作後的關閉採樣也不能計入 24 小時工作時長。

## 目前驗證

Python 16 項合成資料測試通過，包括串流處理 86,400 筆的固定保留狀態檢查；這不是實際經過一天的量測。Go Windows 選測與 Linux race 選測通過；Linux 另驗真 `os.Pipe` 寫入，Windows 略過這個平台專項。新測試已加入 `scripts/runtime_memory_contracts.py` 的 CI 契約步驟。

執行：`python -m unittest discover -s scripts -p 'test_images_soak*.py'`，以及固定 SDK 的 `go test -tags jelee_probe_tests -run '^TestImages(Soak|Memory)' ./internal/platform/runtime`。完整正式結果將另保存，不以本頁或測試通過宣稱 G42.10 達成。
