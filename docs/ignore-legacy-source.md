# 舊格式最近來源觀察

此階段新增固定`.ignore`來源觀察與證據投影，尚未接到持久family、worker或公開API。執行器另見[子程序合同](ignore-executor.md)。

## 查找與驗證

`Resolver.ObserveLegacy`接受受信任且canonical的庫根，以及根內相對**目錄**。檔案呼叫者傳父目錄，目錄呼叫者傳自身。先沿既有native no-follow介面開啟目錄鏈，再由最深處向庫根讀取固定`.ignore`，第一份存在檔案即為來源；不讀被遮蔽的祖先，不搜尋庫根外。空白或零byte來源仍為存在，不能跳過後改用祖先。

實際內容沿用既有唯讀句柄的前後Stat、來源大小預算、完整SHA256、取消與關閉錯誤檢查。完成後重新開啟整條鏈並再讀一次，任何身份、存在狀態、mtime、大小或雜湊差異都丟棄結果。原先不存在的近層檔案若出現，也會失效。這是兩次觀察，並非鎖住外部檔案系統的原子快照；停滯的核心open/stat仍有既有取消限制。

每次最多2 slots、30秒；路徑/深度沿用來源驗證上限；只從每輪最近來源讀內容，沒有任意檔名入口。getter回傳資料副本。來源文字、路徑與證據不由預設JSON或String輸出。

## 證據狀態

`LegacyDirectoryProof`有獨立版本`legacy-nearest-source-v1`，保留根到目標目錄的身份與父鏈。

| Checked | RulePresent | 含義 |
|---|---|---|
| false | false | 祖先被更近來源遮蔽，未讀取規則檔；不是缺失證據 |
| true | false | 在已開目錄確認固定規則檔不存在 |
| true | true | 選中的最近來源，含身份、大小、mtime及內容SHA256 |

token還包括受信任根與相對目錄，因舊匹配器使用完整路徑，不能跨根文字重用。現有自有規則的proof/version不改寫。此投影尚不是資料庫執行授權或整個掃描的seal。

## 已驗證與剩餘工作

Windows來源全套test/vet通過；實際junction拒絕。WSL掛載檔案系統專項race涵蓋readonly、symlink/dangling拒絕、missing/directory/closed分類、來源分離、同size/mtime改文、近層空檔、影子祖先、庫外來源、兩輪間新增近層、proof副本/脱敏。mtime案例固定整秒，保持metadata完全相等/hash不同斷言。

baseline祖先已缺失的觀察、解碼與matcher接線、新持久來源版本、報告及端到端重掃仍待後續。不得把此來源介面當作舊格式掃描已啟用。
Linux原生/tmp來源全套race於本段完成：來源套件10.496秒、架構1.097秒；使用固定Go1.27.1及gcc15。新增範圍品牌掃描0違規，原allowlist未改。

