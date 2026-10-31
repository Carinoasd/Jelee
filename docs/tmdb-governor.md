# TMDB 請求限流與共享冷卻

G14.3 保持部分完成。正式 authentication 預檢已走適配器自己的 governor，所有嘗試（含重試）都經過相同入口；不是僅建立未接入的限流工具。

## 行為

- 同一適配器的相鄰請求啟動至少相隔 250 ms，最多四個呼叫占用容量；包含傳輸與等待時段。既有傳輸每域名四連線上限仍保持。
- 等待接受呼叫者 context／15秒預檢總期限；取消釋放容量，等待者不預占未來啟動時段。governor 不建立背景 goroutine。
- 收到429／503時，依現有Retry-After／退避政策發布共享冷卻，先更新狀態再釋放容量；即使該回應因預算不足或耗盡不再重試，後續呼叫仍受冷卻限制。
- 格式無效的Retry-After保守冷卻15秒，當次失敗不重試；合法長等待保持指定最小值。等待者醒來後重新檢查同步狀態，延長的冷卻不能被舊timer繞過。
- 既有已送出的請求可能仍在進行。共享範圍是同一TMDB適配器，跨實例／跨程序／另建適配器的協調尚未實作；固定預算是本階段產品選擇，不能當作TMDB帳戶配額。

## 驗證

容量／啟動間隔、取消不增加預約、八呼叫併發峰值四、queued取消與全slot釋放、冷卻延長及長伺服器等待通過。真TLS正式適配器先收到Retry-After=1；第一個呼叫因150ms預算不重試，第二個600ms呼叫（長於普通250ms間隔）仍被共享冷卻攔住並取消，上游只有一次請求；後續恢復至少等待1秒後成功。

只繞過正式 governedFetch 接線時，第二個呼叫提早成功，負例失敗。finally來源逐位元恢復，完整相關回歸通過。Windows全Go 29包／2,863 pass events（含父測試）／435 skip events，skip名稱集合與前段完整清單逐項一致；完整vet與產品build通過。Linux四包race通過，摘要不列skip，未宣稱零略過。[機器證據](evidence/tmdb-governor.json)。

完整電影／劇集刮削與多實例協調尚未完成；可配置預算見下節。原[HTTP重試驗收](tmdb-retry.md)保留之前階段來源／測試範圍，本文件補目前實際治理接線。

## 可配置預算（第 9 階段補缺）

間隔、並發與新的每日上限可由 `JELEE_TMDB_REQUEST_INTERVAL_MS`、`JELEE_TMDB_MAX_CONCURRENT`、`JELEE_TMDB_DAILY_REQUEST_LIMIT`（或設定檔 `tmdb` 區段）調整，範圍與啟動錯誤見[部署](deployment.md#tmdb-請求預算g143)。預設值與上文相同（250 ms、4、無每日上限）。

- 設定在啟動時、憑據預檢之前套用到正式適配器；適配器自己再檢查一次範圍（20 ms–1 分鐘、1–32、0–10,000,000），超出時 `tmdb_governance_invalid` 並關閉已建立的 client。
- 每日上限在取得容量與啟動間隔之後、真正送出前計數，以 UTC 日為界；每次嘗試（含 429／5xx 重試與啟動檢查）各算一次，快取命中與合併的同 key 等待者不算。用完時 governor 回 `tmdb_daily_budget_exhausted`，不送出請求、不寫快取並立即釋放容量；各查詢把它映射為 `tmdb_unavailable`，應用層對外一律是 503 `metadata_unavailable`，啟動預檢則以 `tmdb_unavailable` 失敗。
- 未做：持久化快取（G14.3 列為可選）、跨程序／多實例共享限流與每日計數（重啟歸零；屬第 14 階段）。

驗證：`TestGovernanceConfigurationValidationAndCapacity`、`TestGovernorDailyLimitCountsAttemptsAndResetsAtUTCMidnight`、`TestGovernorDailyLimitErrorIsDistinct`（適配器）；`TestTMDBBudgetEnvironmentDefaultsAndBounds`、`TestTMDBBudgetConfigurationFileAndEnvironmentPrecedence`（設定）；`TestGovernedMetadataRejectsBudgetOutsideAdapterBounds`（啟動接線）；真 TLS 的 `TestConfiguredTMDBBudgetThroughActualTLS` 以並發 1、間隔 100 ms、每日 3 次驗證峰值、間隔與第四次不出站。把計數那行換成不遞增時兩項適配器測試與真 TLS 測試都失敗。
