# 客戶端管控（G47）

本文說明 Jelee 如何依「誰在呼叫」（User-Agent、客戶端回報的應用與裝置、位址、請求頭特徵）拒絕、限制或只記錄請求：規則模型、閘門位置與流程、各動作的效果、管理 API、緊急恢復、可觀測與隱私、防護邊界、效能證據，以及尚未實作的後續項目。媒體內容的可見範圍屬於 [存取控制](access-control.md)（G48），兩者互不取代。

- 規則引擎：`internal/access/`（`rules.go` 模型、`compile.go` 編譯與索引、`prefilter.go` 字面量預篩、`evaluate.go` 評估）。
- 請求閘門：`internal/adapter/http/client_control.go`；管理 API：`client_control_routes.go`、`client_control_openapi.go`。
- 儲存：`internal/adapter/postgres/client_control.go`；應用服務 `internal/app/client_control.go`；契約 `internal/domain/client_control.go`。
- 遷移：`000079_share_network_access` 允許 `restrict_libraries` 並新增 `client_rules.libraries`；`000070_client_control`（`client_control_policy`、`client_rules`、`known_clients`、`known_client_sessions`、`client_control_hits`）；`000073_user_preferences` 另加部分索引 `client_rules_block_lookup_idx`（`dimension,pattern` WHERE 啟用的 `deny`），供已知客戶端列表判斷是否已屏蔽。
- 緊急恢復：`jelee-cli access reset-policies --i-understand`（`cmd/jelee-cli/access.go`；G45.6 危險操作，必須帶確認旗標）。

## 規則模型

一條規則＝維度 × 比對方式 × 動作，外加優先序、作用範圍、生效時間窗、啟用旗標、備註與命中計數。

| 欄位 | 值 |
| --- | --- |
| 維度 `dimension` | `user_agent`、`app_name`、`app_version`、`device_id`、`device_name`、`device_type`、`ip`、`api_key_fingerprint`、`header`（配 `header` 指定標頭名） |
| 比對 `match` | `exact`、`prefix`、`glob`（`*`、`?`，反斜線跳脫，比對整個值）、`regex`（RE2，非錨定；用 `^`／`$` 做整段比對）、`cidr`（只限 `ip`）、`absent`（值不存在時命中，`pattern` 須為空） |
| 大小寫 `caseFold` | `true` 時不分大小寫 |
| 動作 `action` | `allow`（白名單）、`deny`、`read_only`、`rate_limit`（配 `rateLimit.requests`／`periodSeconds`）、`force_relogin`、`restrict_libraries`（配 `libraries`）、`observe`、`shadow` |
| 意圖 `intent` | 只給 `observe`／`shadow`（必填）：切到攔截後要執行的動作 |
| 媒體庫 `libraries` | 只給 `restrict_libraries`（含作為意圖，必填，最多 1,000 個）：本請求仍看得到的媒體庫 ID |
| 優先序 `priority` | −1,000,000～1,000,000，大者優先 |
| 作用範圍 `scopeKind` | `global`、`user`（`scopeValues` 為使用者 ID）、`client_kind`（`web`／`native`，伺服器發出工作階段時決定的種類，不可偽造） |
| 時間窗 `window` | `from`／`until`（絕對區間）、`dailyStart`／`dailyEnd`（HH:MM，可跨午夜）、`weekdays`（0＝週日）、`timeZone`（IANA） |

各維度的值從哪裡來：

| 維度 | 自有 API | 相容層 |
| --- | --- | --- |
| `user_agent`、`header` | 請求標頭 | 請求標頭 |
| `app_name`、`app_version`、`device_id`、`device_name` | 原生登入時記錄在工作階段的標籤（網頁工作階段只有裝置名稱） | 工作階段記錄的標籤優先；沒有時用該請求授權標頭的 `Client`／`Version`／`DeviceId`／`Device` |
| `device_type` | 目前沒有客戶端回報，永遠為空（只能用 `absent` 命中）；網路限制（G48.5）以工作階段種類代替裝置類型，見[存取控制](access-control.md#網路限制g485) | 同左 |
| `ip` | 依可信代理設定算出的客戶端位址（`internal/adapter/http/proxies.go`），從不直接讀轉送標頭 | 同左 |
| `api_key_fingerprint` | `sha256:` 加上憑證 SHA-256 前 16 位元組的十六進位；只有規則用到此維度時才計算 | 同左 |

工作階段記錄的標籤優先於請求自己的標頭：一個工作階段的標籤在登入時就固定，客戶端無法靠下一個請求換掉標頭來躲開規則。

### 優先順序與合併（G47.2、G47.4）

（與 `internal/access/evaluate.go` 的 `Evaluate` 註解一致，由 `rules_test.go` 的 `TestPriorityAndConflicts` 等測試釘住。）

1. 啟用、比對命中、作用範圍涵蓋該請求、時間窗包含當下的規則才是候選。空值不會被任何樣式命中，只有 `absent` 會。
2. 執行中（非 `observe`／`shadow`）的候選依優先序由高到低、同優先序依 ID 排序，一層一層處理：
   - 該層有 `deny` → 直接拒絕（同優先序 `deny` 勝過 `allow`）；
   - 否則合併該層的限制：`read_only`、`force_relogin` 取聯集，`rate_limit` 取最嚴格者，`restrict_libraries` 的媒體庫取交集；
   - 該層有 `allow` → 停止往下（較低優先序的拒絕與限制不再適用；同層或更高層的限制仍適用）。
3. 沒有 `allow` 停住、且客戶端未被標記可信時，套用「未知客戶端預設策略」：`allow`（不動作）、`read_only`（加上唯讀）、`deny`（403 `client_blocked`）、待核准（403 `client_pending_approval`，直到管理員把該客戶端標記為可信）。
4. 任一維度的值超過 8 KiB（或同名標頭超過 32 個）時不比對並拒絕：攻擊者不能靠灌長值躲規則或耗 CPU。
5. 管理員工作階段與本機環回（且沒有轉送標頭）預設豁免，命中仍會記錄為 `exempt`（G47.7，可在策略中關閉）。
6. `observe`／`shadow` 永不改變結果，只記錄；評估結果另附「若全部改成攔截」的模擬結果。

## 閘門：在哪裡、怎麼跑

```
請求 → boundary（主機檢查、可信代理位址）
     ├─ 自有 API：authenticate 中介層 ─┐
     └─ 相容層：layer 的 authenticate ─┼→ AuthenticateClient（一條 SQL：工作階段＋標籤＋規則版本）
                                         └→ ClientControl.check → 拒絕／限制／放行
登入（/api/v1/auth/login、/login/native、/compat/Users/AuthenticateByName）
     → 讀取版本（一條小查詢）→ ClientControl.check（無工作階段）→ 密碼驗證
```

- **自有 API 與相容層都生效**：兩者共用同一個工作階段查詢與同一個 `check`。相容層的拒絕照上游慣例回無內容的 403／429（限速附 `Retry-After`），自有 API 回標準錯誤信封與錯誤碼。
- **登入**在驗證密碼之前檢查，被擋的客戶端無法試密碼。登入時沒有工作階段，因此以使用者為範圍的規則不適用；**未知客戶端預設策略也不套用在登入**，讓新客戶端可以登入並出現在已知客戶端清單等候核准。
- **規則快取與失效**：規則依版本編譯成不可變快照（`access.Snapshot`）後只在記憶體評估。任何改變評估的操作（規則增刪改、模式切換、策略、可信標記、緊急重設）都在同一交易把 `client_control_policy.version` 加一。每個已驗證請求的工作階段查詢本來就要跑，版本號附在同一條 SQL 裡回來；看到較新的版本就重新編譯一次（同時到的請求等這一次編譯，不會有請求在變更提交後還用舊規則）。因此**規則變更後下一個請求生效**，多個伺服器實例也一樣。重新載入失敗時沿用上一版並記錄 `client_control_reload_failed`，一秒後再試。

### 動作的效果（G47.3）

| 動作 | 自有 API | 相容層 | 備註 |
| --- | --- | --- | --- |
| `deny` | 403 `client_blocked` | 403（無內容） | |
| `read_only` | 寫入方法（POST／PUT／PATCH／DELETE）回 403 `client_read_only` | 403 | 例外：登出、輪換工作階段、登入，以及讀取型 POST（`/api/v1/items/{id}/playback/check`、相容層 `PlaybackInfo`、`System/Ping`）。播放進度回報屬於寫入，會被擋 |
| `rate_limit` | 429 `client_rate_limited`＋`Retry-After` | 429＋`Retry-After` | 以「使用者＋客戶端識別」為鍵的固定視窗，重用登入限速器（`LoginLimiter`）；每個不同的限額一個限速器，程序內計數、重啟歸零 |
| `force_relogin` | 撤銷該工作階段並回 401 | 401 | 只撤銷**在規則最後修改之前**發出的工作階段，重新登入後的新工作階段不受影響（否則等同永久封鎖）；寫入安全稽核 `client_control.session_revoked` |
| `observe` | 放行 | 放行 | 計入命中紀錄與統計，供評估影響；`POST /rules/{id}/enforce` 切成攔截 |
| `shadow` | 放行 | 放行 | 只寫入命中紀錄，不進統計與告警 |
| `restrict_libraries` | 本請求只看得到列出的媒體庫（多條取交集，可能為空），其他庫的內容一律當作不存在 | 同左（相容層的庫清單、條目、播放都經同一述詞） | 閘門把結果掛在 principal 的 `RequestScope.Libraries`，儲存層以統一述詞的請求參數下推到 SQL（`visibility.go` 的 `requestLibrarySQL`，每語句算一次，不增加語句數）；與網路限制（G48.5）同時成立，優先順序見[存取控制](access-control.md#優先順序與合併策略)。豁免（管理員、環回）時不套用；開發者模式不放寬 |

## 管理 API（僅管理員，G47.3、G47.5、G47.8）

全部在 `/api/v1/client-control` 下，契約見 `api/openapi.json`；每個變更寫稽核（未改變者不寫）。

| 方法與路徑 | 用途 | 稽核事件 |
| --- | --- | --- |
| `GET/PUT /policy` | 未知客戶端預設策略、管理員豁免、環回豁免 | `client_control.policy_changed` |
| `GET/POST /rules`、`GET/PUT/DELETE /rules/{id}` | 規則 CRUD；建立／修改前先用引擎編譯（無效樣式、正則過大、時間窗錯誤回 400）；上限 10,000 條規則、1,000 條啟用中的正則（409） | `client_control.rule_created`／`rule_updated`／`rule_deleted` |
| `POST /rules/{id}/enforce`、`/observe` | 觀察 ↔ 攔截切換；正文 `{}`（OpenAPI `Empty`） | `client_control.rule_mode_changed` |
| `GET /hits` | 命中紀錄（遮罩後），可依規則、模式、時間篩選，游標分頁 | — |
| `GET /hits/export` | 匯出最多 10,000 筆（遮罩後，JSON 附件）；超過回 409 `stats_export_limit` | `client_control.hits_exported` |
| `GET /stats?hours=&top=` | 總數、被擋數、觀察數、依模式與動作、Top UA、Top IP、Top 規則（不含 `shadow`） | — |
| `GET /clients` | 已知客戶端：名稱、版本、UA、裝置、種類、最後活躍、最後 IP、最後使用者、使用中的工作階段數，以及 `blocked`／`blockRuleId`（存在與 `block` 相同識別的啟用、全域、無時間窗 `deny` 規則時為已屏蔽；其他屬性的拒絕規則不反映） | — |
| `PATCH /clients/{id}` | 重新命名（`alias`）、標記可信（`trusted`） | `client_control.client_updated` |
| `POST /clients/{id}/block` | 以裝置 ID（沒有時用 UA）建立精確比對的 `deny` 規則，優先序 100000；正文 `{}` | `client_control.client_blocked` |
| `POST /clients/{id}/kick` | 撤銷該客戶端用過的所有使用中工作階段（不阻止重新登入）；正文 `{}` | `client_control.client_kicked` |

已知客戶端的識別：有裝置 ID 時為「應用名＋裝置 ID」，否則為 UA；存成 SHA-256 摘要。只記錄**已驗證**請求，每個工作階段與識別每分鐘最多寫一次，表上限 100,000 筆（超過後只更新既有客戶端）。

## 緊急恢復（G47.7）

```
jelee-cli access reset-policies --i-understand
```

在一個交易內停用所有規則（保留不刪）、把策略恢復為預設（未知客戶端允許、管理員與環回豁免）、版本加一，並寫入安全類稽核 `client_control.policies_reset`（無操作者）。執行中的伺服器在下一個請求就套用。輸出為 JSON：`{"rulesDisabled":N,"policy":{…}}`；失敗只印固定代碼（`access_database_unavailable`、`access_reset_failed`），不印連線字串。

降級遷移 000070 在仍有啟用中的執行規則或嚴格的未知客戶端策略時會拒絕（避免降級後默默放行），先跑此指令即可。降級遷移 000079 在仍有任何 `restrict_libraries` 規則（啟用與否）時拒絕，需先刪除。

## 可觀測與隱私（G47.8、G47.9）

- **命中紀錄**：每分鐘 × 規則 × 模式 × 動作 × 使用者 × 位址 × UA 聚合成一列（`client_control_hits.hits` 為次數），大流量命中的規則也只寫有界的列數；`allow` 規則只累加命中計數不寫列。紀錄在背景每 2 秒批次寫入，管理員讀取前會先寫入本實例的緩衝；保留 30 天，每次寫入順帶清掉過期列。
- **安全日誌**：每次批次寫入時，有被擋的請求就記一行 `client requests blocked`（`code=client_blocked`，只含規則 ID 與次數）；一分鐘內被擋達 100 次記 `client_control_block_burst` 告警。
- **脫敏**：列表與匯出的位址遮罩成 /24（IPv4）或 /48（IPv6），UA 截斷為 256 位元組並去除控制字元，**不記錄路徑、查詢字串或任何憑證**；日誌不含 UA、位址或路徑。Top IP 統計顯示完整位址（僅管理員可讀，與工作階段清單的 `lastIp` 同級）。
- 被拒的請求不會因規則透露其他使用者的任何資訊：回應只有固定錯誤碼。

## 防護邊界（G47.6）

- **UA、應用名、版本、裝置 ID、裝置名、請求頭全部由客戶端自行宣稱，可以偽造。** 依這些維度的規則只能擋「守規矩」的客戶端（官方 App、爬蟲預設 UA、舊版客戶端），擋不住刻意偽裝者。
- 較可靠的組合：`ip`（伺服器觀察到的位址；位於反向代理之後時務必設定可信代理，否則所有請求看起來都來自代理）、`api_key_fingerprint`（綁定某個憑證）、`client_kind` 作用範圍（伺服器發出的工作階段種類），以及「未知客戶端預設策略＋管理員核准」。
- 工作階段的標籤在登入時固定，換標頭換不掉；但登入時回報的標籤本身仍是客戶端宣稱的。
- 本機環回豁免只在請求**沒有轉送標頭**時成立；反向代理在同一台機器上時，帶轉送標頭的請求不會被當成本機診斷。
- TLS／JA3 指紋未實作：TLS 通常終止在反向代理，本程序拿不到 ClientHello。

## 效能（C7、G47.10）

規則依版本編譯一次，評估全在記憶體：精確與大小寫折疊比對用雜湊表，前綴與 CIDR 依長度索引，`glob`／`regex` 先以必要字面量建 Aho-Corasick 自動機一次掃描值，只有字面量出現的規則才跑完整比對（`prefilter.go`；找不到可用字面量的規則仍逐一掃描）。正則用 RE2（線性時間）並限制編譯後指令數，值長度有上限。

基準（AMD Ryzen 7 9850X3D，`go test -bench`，混合規則：40% 裝置 ID 精確、10% 觀察、10% 大小寫折疊應用名、10% 前綴、10% glob、1% 正則、其餘裝置名與 CIDR）：

| 基準 | 結果 |
| --- | --- |
| `BenchmarkEvaluate1k`／`10k`（引擎單次評估） | 約 0.3 µs／0.34 µs（加入預篩前為 12 µs／130 µs） |
| `BenchmarkClientGateOff`（驗證中介層，無閘門，後端為記憶體） | 約 3.0 µs |
| `BenchmarkClientGateNoRules` | 約 3.1 µs（+3%） |
| `BenchmarkClientGateRules1k` | 約 3.8 µs（+0.8 µs） |
| `BenchmarkClientGateRules10k` | 約 3.5 µs（+0.5 µs） |

記憶體後端讓分母只有 3 µs，比例被放大；實際請求的工作階段查詢是一次資料庫往返。`TestClientControlOverheadPostgres` 在真 PostgreSQL 上量 `GET /api/v1/users/me`（中位數，每組 200 次 × 7 組）：無規則與 10,000 條規則的比值為 **0.98**（2.70 ms 對 2.65 ms，差異在雜訊內），符合 C7「≤10%」。以上為單機單次樣本，不是長時間或多核壓測結論。

重跑：

```
go test -p 1 -run '^$' -bench 'BenchmarkClientGate|BenchmarkEvaluate' -count 5 ./internal/adapter/http/ ./internal/access/
JELEE_TEST_DATABASE_URL=… go test -p 1 -run TestClientControlOverheadPostgres -v ./internal/adapter/http/
```

## 驗收測試

- 引擎：`internal/access/rules_test.go`（每種比對方式 × 維度、大小寫、CIDR、時間窗、優先序與衝突、作用範圍、豁免、未知客戶端、觀察與影子、超長值、編譯限制）、`prefilter_test.go`（預篩與逐條直接比對的隨機等價測試，含 Kelvin、long s 等大小寫陷阱）。
- 閘門單元：`internal/adapter/http/client_control_unit_test.go`（版本只在變新時重載且只編譯一次、重載失敗沿用上一版、命中聚合與活動節流、記錄緩衝上限、強制重登與關閉時寫入、唯讀例外路由）。
- 真 PostgreSQL（`client_control_test.go`）：每種比對方式經自有 API 與相容層；每種動作；觀察 → 攔截 → 觀察；未知客戶端待核准 → 標記可信；登入閘門（網頁、原生、相容層）；環回與轉送標頭；命中紀錄遮罩、匯出、統計；已知客戶端踢下線與加入屏蔽；全員（含管理員）被鎖後以緊急重設恢復，第二個實例在下一個請求就看到規則變更與恢復；管理 API 驗證、角色與稽核。
- 儲存：`internal/adapter/postgres/client_control_test.go`（版本只在評估改變時遞增、存儲拒絕無法執行的規則、正則上限、活動合併與分頁、可信與踢下線、命中聚合、刪除規則後的命中、保留期清除、匯出上限、統計、緊急重設、遷移 up/down/up 與降級保護）。
- CLI：`cmd/jelee-cli/access_test.go`（用法、錯誤不洩漏連線字串、真 PostgreSQL 恢復）。
- `internal/adapter/http/access_leak_test.go`：全路由遍歷在閘門開啟（無規則）下執行，管理 API 路由已登記。

## 後續（本次不在範圍）

- 「按庫」作用範圍（`scopeKind: library`）：閘門在路由前不知道請求的目標庫，存儲仍拒絕。`restrict_libraries` 動作已實作（遷移 000079）。
- 使用者群組作用範圍（目前沒有群組模型）。
- 異常 UA 暴增告警（目前只有批量被拒告警）；告警門檻可設定。
- 管理頁 UI（目前只有 API 與 OpenAPI 型別）。
- `device_type` 的來源（需客戶端回報或由伺服器推斷）。
- 限速計數跨實例共享（目前每個實例各自計數）。
- TLS／JA3 指紋。
