# 網路模組品牌重命名

本批盤點 15 個既有檔案，將網路模組及測試的 namespace、程序集、project／solution／consumer 引用改為 `Jelee.Networking`／`Jelee.Networking.Tests`。一般訊息改稱 Jelee，任意示例網址與 base path 使用中性示例或 Jelee 名稱。

舊探索 request 與環境變數識別完整保留在只有兩個常數的 Compatibility/LegacyNetworkNames.cs；品牌白名單只增加該精確邊界檔，沒有豁免模組。警告的環境變數選項改為 structured log argument，實際值保持；警告條件與憑據不外洩檢查沒有移除。12 個 C# 檔核對明確 namespace／訊息／常數搬移與示例映射後，網路控制邏輯保持。探索埠與啟閉方式未改；這不完成 G05 的發現能力裁剪。

HappyEyeballs 的原 MIT 標頭逐位元保持，LICENSE 與需求來源 SHA256 不變，既有 migrations 未改。模組仍依賴保留的舊核心型別；G00 純淨性與全案未完成。

## 驗證

.NET SDK 10.0.400：首次模組測試 142 通過、5 失敗，皆為警告斷言仍期待舊品牌；更新斷言後 147 通過、零失敗／略過。完整 solution Debug 增量建置 4.87 秒、零警告／錯誤，17 個測試套件通過，4,098 Passed、21 NotExecuted、零失敗；略過項不能當已驗收。變更 C# 檔 format --verify-no-changes 通過，工具有 workspace 載入警告。

[TRX counters、來源等價核對與日誌雜湊](evidence/networking-brand-rename.json)。原失敗與最終原始紀錄仍在被忽略的 .testdata。完整品牌門禁 15,007 → 14,960，減少 47，仍保留失敗。沒有發布或合併。
