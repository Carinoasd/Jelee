# NFO 原始 payload 的生命週期配額

共享 `resources.Budget` 新增獨立的原始 payload bytes 保留，不占用 CPU／I/O／Total class slot。`New` 預設192MiB；內部 `NewWithPayloadLimit` 可設定有界上限。`ReservePayloadBytes` 在容量不足時立即回 Busy，不建立額外等待列；取消或不合法大小拒絕，release 可安全重複呼叫。`PayloadBytes` 提供目前保留量與上限，尚未接公開設定或遙測。

StageCommitFiles 的 owner 在讀取前要求 `app.PayloadBudget` 能力，按 SQL 的 request／original／replacement 三份各最大32MiB，保守預留96MiB；不依呼叫者 Source.maxBytes 縮小另一工作資料的預留。容量不足或 Budget 沒有這項能力，在 repository 讀取及檔案副作用之前拒絕。相同 flight 的等待者不再預留；不同 Writer 共用同一 Budget，預留仍共享。預設容量可容納兩份最大原始 payload。

保留跨越 payload 讀取、CPU 重建／排隊、後續 I/O 準備及 ready 保存／清理。取消訊號不提前釋放仍由 owner 使用的 bytes；所有 native handle、class permit 與 child activity 返回後才釋放。既有未知 ready 結果契約仍返回 ErrReplace 並保留五份證據。

## 驗證範圍

資源測試驗128個並行預留競爭64-byte容量，恰64個成功，並行重複release不重複扣除；滿額、過大／溢位大小、不合法limit、nil／已取消context與CPU獨立性均驗。Stage測試持有CPU造成排隊，此時IO已釋放但96MiB仍保留；第二個Writer在讀取前Busy，取消後容量可再使用。另驗未知ready清理等待期間bytes及IO保持、清理後釋放但五份檔案證據保持，以及缺bytes能力或容量不足零讀取。

Windows resources／NFO／architecture／jobs／runtime五套件607通過事件／13條件跳過；Linux同五套件race616通過事件／6條件跳過。初次Windows唯一失敗是新ready取消測試誤期待context.Canceled，依既有ErrReplace契約修正夾具並重驗，保留失敗紀錄。真PG選測、隔離red overlay及來源雜湊見[本批安全證據](evidence/nfo-payload-budget.json)。100份已發布SQL、需求原文、模組與LICENSE保持。本批没有新migration，不當完整PG回歸重跑。

## 尚未證明的記憶體與恢復

這個上限計算原始 request／original／replacement，沒有計入呼叫者已讀取的 Source、重建副本、XML parser物件、其他模組配置或GC尚未歸還的resident頁。不能把192MiB預留上限當成heap／RSS上限。完整混合記憶體與延遲、公開配額設定／遙測，以及持久證據安全讀取、staged重開續作、完整root／媒體授權、正式提交／結算／恢復仍需完成。runtime沒有啟用NFO写回；正式24h仍是24caf凍結來源，不包含本批。
