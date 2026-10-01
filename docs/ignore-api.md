# 忽略掃描與排除報告

管理員可在掃描請求明確啟用 `.jeleeignore`：

```json
{"ignore":{"mode":"jeleeignore","caseMode":"sensitive"}}
```

送至 `POST /api/v1/libraries/{id}/scan`，沿用管理員 bearer token 與 `Idempotency-Key`。`caseMode` 必填，可選 `sensitive` 或 `ascii-insensitive`。省略 `ignore` 才是不啟用；`null`、空物件與未知欄位都會被拒絕。規則與媒體來源不會由此請求修改。

CLI 對應參數：

```text
jelee-cli jobs scan --id <library-id> --key <unique-key> --ignore jeleeignore --ignore-case sensitive --token-stdin
```

權杖由標準輸入讀取。可搭配既有 `--nfo` 與 `--probe` 選项，仍需各自符合功能准入條件。伺服器無法執行忽略掃描時，新請求回覆 `503 ignore_unavailable`；仍保留的相同 key 重送可讀回原工作。重試沿用父工作的忽略意圖，且新執行仍需能力可用。

## 讀取報告

```text
jelee-cli jobs ignore --id <job-id> --limit 50 --token-stdin
jelee-cli jobs ignore --id <job-id> --limit 50 --cursor <nextCursor> --token-stdin
```

對應 `GET /api/v1/jobs/{id}/ignore?limit=50&cursor=...`。每頁重新驗證管理員權限與登入階段。`limit` 為 1–100，預設50；`nextCursor` 為不透明游標，原樣傳回即可，不能跨工作使用。

工作仍在排隊或執行中時回覆409。完成、失敗或取消後可讀取已保留的觀察結果；失敗或取消的報告可能不完整。查詢不重新讀取檔案系統，工作歷史清理後報告也會移除。

回應 `data.entries` 包含：

- `source=scan`：本次掃描排除的檔案或目錄，含 `kind`。
- `source=baseline`：舊基線路徑的分類，`outcome` 可為 `excluded`、`included_missing` 或 `unknown`。
- `rootId` 與 `path`：媒體根的識別碼與相對路徑，不含絕對根路徑。
- 排除結果的 `ruleDirectory`、`ruleLine` 與 `matchedPath`：規則所在相對目錄、`.jeleeignore` 的行號（從1開始）及匹配路徑。
- 未知結果的 `reason`：`source_unavailable`、`source_changed` 或 `coverage_unknown`。

同一路徑可能各有一筆 scan 與 baseline 觀察。`excludedFiles`、`excludedDirectories` 計算本次掃描排除項目；`unknown` 計算舊基線的未知分類，因此不能直接加總為報告列數。`reviewRequired` 表示需要檢視工作結果；`invalidated` 表示保留的忽略證據失效。未知結果不能當作檔案已消失。

CLI 僅輸出已知公開欄位，並拒絕非法相對路徑、無效規則來源、重複 JSON 欄位與超出要求頁面大小的回應。

## 本階段驗證

[執行證據與來源雜湊](evidence/ignore-api.json)：完整 PostgreSQL race 回歸185項頂層測試通過、零跳過，224.669秒；Linux 全模組 vet 與三個程式建置通過。CLI/HTTP/app 的 Linux race 及 Windows 對應測試通過。

整合測試串起真實登入 token、HTTP 提交、正式 worker、原生來源讀取、PostgreSQL 分頁報告與登入撤銷。兩萬筆合成報告資料的 custom/generic 查詢計畫皆限制於兩來源各 `limit+1` 筆索引讀取。此結果僅證明報告分頁，不能當作完整媒體庫規模效能驗收。

本段未修改既有遷移，schema仍為12。舊格式忽略兼容、規則修改後完整重掃、混合外部probe真媒體及worker取消/unknown完整情境仍待驗收；3D1與全案尚未完成。
