# 合併忽略模式的正式服務

HTTP／CLI 已可提交 `jeleeignore-legacy-v1`。服務以功能開關和實際 helper 健康檢查決定准入，預設關閉。

## 啟用

```text
JELEE_ENABLE_ACCOUNTS=true
JELEE_ENABLE_JOBS=true
JELEE_ENABLE_FAMILY_IGNORE=true
```

JSON 設定對應 `enableFamilyIgnore`，環境變數優先。Docker Compose 傳入同一開關。缺少 jobs／accounts 依賴時拒絕啟動。

```json
{"ignore":{"mode":"jeleeignore-legacy-v1","caseMode":"sensitive"}}
```

送至 `POST /api/v1/libraries/{id}/scan`，仍需管理員 bearer token 與 `Idempotency-Key`。可選 `ascii-insensitive`，可搭配既有 `nfo`／`probe` 增量選項，各自仍需准入條件。

```text
jelee-cli jobs scan --id <library-id> --key <unique-key> --ignore jeleeignore-legacy-v1 --ignore-case sensitive --token-stdin
```

這個合同合併自有 `.jeleeignore` 與固定上游版本的 `.ignore` 語意。其他舊格式的來源確認與實作仍待完成。

## 健康與生命週期

Linux／Windows 服務使用本程式固定的 helper 命令，啟動時以固定規則和兩個路徑實際執行並核對結果。最多兩個子程序，每個最多五秒，沿用正式記憶體、輸入及輸出限制。未啟用時不建立暫存目錄或執行 helper；不支援的平台或健康失敗保持不可用。

helper 啟動、逾時、結果或清理失敗會停止後續新准入及認領；取消和忙碌不改變服務健康。檔案來源讀取問題發生在另一邊界，不會誤關閉全服務。重啟後重新驗證健康。相同 key 的已授權保留重送不依賴當前功能開關；建立新工作仍需能力可用。

停止順序為取消工作、等待 worker／heartbeat／helper 完全結束、清理服務自有暫存目錄，再關閉資料庫。啟動途中失敗也清理資源。清理鎖防止刪除仍由 helper 使用的輸入。規則來源、原始媒體與既有 NFO 不由此流程修改。

## 驗收證據

[原生 runtime、HTTP 與 PostgreSQL 證據](evidence/ignore-family-runtime.json)：5 項頂層通過，零失敗、零略過，14.906 秒，來源未變。正式 Fx runtime 實際監聽 HTTP、登入、提交、執行與查詢報告；覆蓋功能關閉／開啟、匿名拒絕、保留重送、重啟關閉功能後重送與新 key 拒絕、媒體内容保持及暫存清理。

Windows 全 Go 套件編譯與可執行測試通過；Linux runtime／jobs／config／HTTP／CLI／architecture／scan race 通過，依序為 1.093／1.343／1.024／3.245／1.893／1.128／1.264 秒。全 vet、三命令 build 與既有 probe 測試入口相容檢查通過。CI 已加入此原生 runtime 驗收並保留日誌。

G22 還需要其他舊格式、規則變更後完整重掃、更多混合 probe／NFO／圖片、取消與租約恢復，以及規模和穩定性驗收。本段不能代表 G22、第 3 階段或全案完成。
