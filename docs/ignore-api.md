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

## 合併模式報告

保留的合併模式工作也可使用同一報告 API 與 CLI。公開掃描也接受合併模式，啟用方式與完整驗收見 [正式服務說明](ignore-family-runtime.md)。

合併模式排除結果增加 `family` 與 `reason`：

- `family=jeleeignore`、`reason=rule`：自有規則命中，保留從 1 開始的行號。
- `family=legacy-ignore-021`：由已固定版本的 `.ignore` 語意排除。`reason=rule` 帶行號；`blank-source` 或 `invalid-source` 表示整份來源造成排除，沒有個別行號，故省略 `ruleLine`。
- `unknown` 不歸屬任何規則家族，保留來源不可用等原因；不能當作確定缺失。

目錄自己的 `.ignore` 可以是該目錄的排除來源；歷史基線檔案的來源仍須符合祖先關係。兩份来源任一失效，`invalidated` 都為 true。報告不包含規則內容、來源雜湊、檔案系統身分或絕對根路徑。

[合併模式報告證據](evidence/ignore-family-report.json)：真實 PostgreSQL race 專項 8 項頂層通過，零失敗、零略過，28.684 秒，來源未變；涵蓋歷史基線排除、133 筆結果分頁、權限撤銷、舊格式清單失效、兩萬筆 generic/custom 分頁計畫，以及既有 HTTP 到正式 worker 的報告回歸。Windows 相關模組測試與 Linux domain／HTTP／CLI／architecture race 通過；全 vet、三命令 build 通過。這些是報告驗收，完整 G22 仍未完成。
