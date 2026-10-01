# 媒體檔名解析模組品牌重命名

檔名解析模組及測試改為 `Jelee.Naming`／`Jelee.Naming.Tests`。本批事前盤點 124 個檔案：模組 49、測試 29、引用及建置 46；同步 namespace、專案／程序集／套件識別、solution／project 引用、版本腳本路徑與 ABI 比較的 base／head 程序集對應。沒有執行版本修改或發布。

116 個 C# 原始檔經逐檔核對，解析內容在明確 namespace 對應、3 處相對 namespace 改為完整引用及 using 排序後保持一致。原 Authors 與 GPL 授權搬到只有歸屬資訊的 Attribution.props；原 AssemblyCopyright 字串逐字保留在專用 Copyright.cs。品牌白名單只增加這兩個法定歸屬檔，其他引用不豁免。編譯程序集的名稱／title 為 Jelee.Naming、product 為 Jelee Server，copyright 保持原文。

## 驗證

- .NET SDK 10.0.400：命名模組 701 個 Debug 測試通過，零失敗／略過。
- 首次完整建置有 3 個相對引用與 4 個 using 排序錯誤；修復後完整 Debug solution 建置 5.89 秒，6 個警告、零錯誤。
- 首次完整測試有 2 個語系相關失敗：本機 CurrentUICulture 為繁中，但原測試期待英文。保留單參數方法與斷言，測試明確設定 en-US 並於 finally 恢復；沒有修改正式語系邏輯。
- 最終完整 Debug 測試 17 個套件，4,098 通過、零失敗、21 個 NotExecuted。這些略過仍須靠跨平台及各項功能驗收補齊，不能宣稱所有測試皆執行。
- 變更 C# 檔的 format --verify-no-changes 通過；工具有 workspace 載入警告。版本腳本與 ABI 比較 shell 語法檢查通過；本機沒有執行實際 APICompat 比較，不把 syntax 檢查當相容性證據。
- LICENSE、需求來源 hash 保持；既有 migrations 未改。

[結果、TRX counters 與日誌雜湊](evidence/naming-brand-rename.json)。原失敗日誌與最終日誌留在被忽略的 .testdata 中。完整品牌殘留 15,233 → 15,007，減少 226；完整門禁仍失敗。

Namespace／程序集識別改名會在 C# API 比較中顯示相應變更；ABI 工作流程保留比較與差異報告，不隱藏改名差異。此模組仍引用保留的舊核心型別，也仍有待裁剪媒體域；G00／G02 全部驗收及 G00–G51 全案未完成。
