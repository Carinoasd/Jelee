# 程式碼分析器品牌重命名

分析器的專案、程序集、namespace 與目錄改為 `Jelee.CodeAnalysis`，同步更新 solution 路徑與根建置屬性的分析器引用及自引用排除條件。診斷規則與原發布紀錄保持；這批沒有修改應用、遷移、原始媒體或授權文件。

.NET SDK 10.0.400：模組 Debug 建置零警告／錯誤，完整 solution Debug 建置 34.50 秒、零錯誤、215 個既有區域的警告。未放寬分析設定。模組 `dotnet format --verify-no-changes --no-restore` 通過。

使用實際 C# 編譯器與根建置引用驗證：同一個實作 IDisposable／IAsyncDisposable、非同步建立的資源，以同步 using 釋放時被 JF0001 拒絕；改用 await using 後成功且沒有該診斷。這證明重命名後分析器可被載入，規則仍有作用。測試專案與日誌位於被忽略的 `.testdata/analyzer-rename-check`；[結果與來源雜湊](evidence/analyzer-brand-rename.json)已保存。

完整品牌掃描 15,237 → 15,233，減少 4 處，仍失敗且保留門禁。此模組的專案與發布紀錄逐位元保持，分析器邏輯只有 namespace 改動；其餘 C# 模組重命名與 G00 全部驗收仍未完成。
