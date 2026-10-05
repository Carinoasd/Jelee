# 品質門禁：golangci-lint、覆蓋率棘輪與基準回歸

對應需求 G30.1（gofmt、go vet、golangci-lint 指定 linter、`go test -race`、核心包 ≥70%／關鍵包 ≥85% 覆蓋率）、G30.5（不得以關閉 lint、跳過測試或降低安全設定換取 CI 通過）、G30.6（工具固定版本與來源）、G38.1（CI 流程）與 G26.4（效能回歸門禁）。工具引導見 [工具鏈](toolchain.md)，CI 定義見 [`.github/workflows/jelee.yml`](../.github/workflows/jelee.yml)。

| 門禁 | 入口 | CI 位置 | 失敗條件 |
| --- | --- | --- | --- |
| golangci-lint | `make lint`（含 `golangci-lint`）／`scripts/make.ps1 lint` | foundation（Linux、Windows） | 基線外的新問題；或已修復但仍留在基線的條目 |
| 覆蓋率棘輪 | `make coverage-check` | foundation（Linux） | 任一受管包低於最低值，或該包沒有覆蓋率資料 |
| 基準回歸 | `make bench-compare BENCH_BASE_REF=<提交>` | `bench-regression` 獨立 job（Linux） | 中位數 ns/op +25%、allocs/op +10%、B/op +20%，或基準被移除 |
| 門禁工具自身 | `make quality-gates-test`／`scripts/make.ps1 quality-gates-test` | foundation（Linux、Windows） | lintgate、covergate、benchgate、bench-compare 單元測試失敗 |

## golangci-lint

版本 2.14.0，由 `tools/manifest.json` 固定（官方 release HTTPS URL 與 SHA256），引導、校驗與環境隔離見 [工具鏈](toolchain.md#golangci-lintg301)。設定檔 [`.golangci.yml`](../.golangci.yml)：

- 只啟用需求列出的七個 linter：errcheck、staticcheck、govet、revive、gosec、bodyclose、contextcheck；沒有任何一個被整體關閉。測試檔一併檢查（`run.tests: true`）。
- `uniq-by-line: false`：預設「每行只留一個問題」會依 linter 排程決定留下哪一個，導致基線比對不穩定，因此每個問題都回報。
- 以 `CGO_ENABLED=0` 執行，各主機型別檢查同一組檔案。Windows 結果以 `GOOS=windows` 交叉檢查產生基線，Windows CI 以原生執行比對同一份基線。

### 設定層排除（每類都是誤報，並寫明理由）

| 範圍 | 排除 | 理由 |
| --- | --- | --- |
| errcheck | `(pgx.Tx).Rollback` | pgx 的「defer Rollback、Commit 後回傳 ErrTxClosed」慣用法；Commit 錯誤仍受檢查 |
| staticcheck | `QF*` | quick-fix 重構建議（如「套用 De Morgan 定律」），不指出缺陷 |
| staticcheck | ST1000、ST1003、ST1016、ST1020、ST1021、ST1022 | golangci-lint 自身預設排除的命名與註解格式規則 |
| gosec | G104 | 與 errcheck 重複（errcheck 已用更嚴格規則啟用） |
| gosec，`_test.go` | G101 G112 G115 G117 G122 G124 G204 G301 G302 G304 G306 G404 G602 G702 G703 G705 G710 | 測試輸入在 `t.TempDir()`、httptest 只聽 loopback、固定假憑證、重新執行本專案 helper 二進位 |
| staticcheck，`_test.go` | SA1012 | 測試刻意傳入 nil context 以驗證參數檢查 |
| gosec，`tools/` | G204 G301 G304 G306 | 開發工具讀寫開發者在命令列指定的儲存庫檔案並執行固定工具鏈，不在伺服器程序中執行 |

其餘無法修正的誤報一律以單行 `//nolint:<linter> // 理由` 標註（只作用於那一行），目前包括：刻意不可取消的清理流程（rename 之後的 NFO 回滾、shutdown 收尾，contextcheck）、header／診斷碼／欄位名稱被誤判為憑證（G101）、已有長度比較或迴圈保證的索引（G602）、命令列或設定指定的檔案路徑（G304/G703/G704）、Linux seccomp／landlock 與 Windows API 必須的 `unsafe` 指標（G103）、`crypto/rand` 已填入的 nonce（G407）、重試抖動用的 `math/rand`（G404），以及測試中刻意呼叫兩次 `Close` 驗證冪等（SA4000）等。

### 本次修正的實際問題

逐項審查 bodyclose（3）、contextcheck（32）、gosec（非 G115 的全部）與 staticcheck SA/S/ST 後，**沒有發現可被利用的正式程式缺陷**：bodyclose 3 項都在測試中由 helper 以 `t.Cleanup` 關閉；contextcheck 全是刻意不可取消的收尾；gosec 非 G115 項目都是誤報。修正的項目如下：

| 檔案 | 檢查 | 問題與修法 |
| --- | --- | --- |
| `internal/domain/sidecar.go` | gosec G116 | 原始碼含雙向文字控制字元字面值（Trojan Source 風險，審查時不可見）；改為 `‎` 等跳脫寫法，比對行為不變 |
| `internal/adapter/postgres/nfo_commit_files_test.go` | staticcheck SA4006 | `tx.Rollback` 的錯誤被賦值後丟棄，回滾失敗時測試仍會通過；改為檢查並 `t.Fatal` |
| `internal/adapter/nfo/commit_files_abrupt_test.go` | staticcheck SA4006 | helper 未在預期崩潰點結束時丟棄回傳錯誤；改為列入失敗訊息 |
| `internal/adapter/http/progress_load_test.go` | errcheck／contextcheck | defer 關閉連線的錯誤被忽略；改為明確忽略並標註收尾使用獨立 context |
| `internal/platform/config/config.go` | gosec G109 | `JELEE_MAX_CONNECTIONS` 以 `Atoi` 解析後轉 `int32`；改為 `ParseInt(…, 10, 32)`，解析即限定 32 位（原 1–128 範圍檢查保留） |
| `internal/adapter/postgres/probe_maintenance.go` | staticcheck SA4006 | 被下一次查詢覆寫、從未讀取的 `slot`（死存值）改為 `_` |
| `internal/adapter/postgres/schedules.go` | staticcheck SA4003 | `>= 9223372036854775807` 改寫為 `== math.MaxInt64`，語意相同但可讀 |
| `internal/platform/telemetry/jobs.go` | staticcheck SA4017 | `len(fixedJobDimensions())` 呼叫只為取陣列長度；改用型別別名的編譯期常數長度 |
| `internal/adapter/compat/wire.go` | staticcheck SA9004 | 常數群組只有第一個具型別；把刻意未定型的 `dlnaProfileTypeVideoNumber` 獨立宣告並說明 |
| `cmd/jelee-cli/accounts_test.go`、`internal/app/jobs_test.go` | staticcheck SA1029 | 以空匿名 struct 當 context key；改為具名型別 |
| 其他測試 | SA4006、S1016、S1038、ST1013、ST1023 | 移除未使用的賦值、改用型別轉換、`t.Logf`、`http.Status*` 常數與型別推斷 |

### 既有問題基線（新程式碼嚴格、舊程式碼基線）

未在本次修正或標註的既有問題記在 [`tools/lint-baseline/linux.json`](../tools/lint-baseline/linux.json) 與 [`tools/lint-baseline/windows.json`](../tools/lint-baseline/windows.json)，由 [`tools/lintgate`](../tools/lintgate/main.go) 比對：

- 條目以「linter＋檔案＋訊息＋該行去除空白後的 SHA256 前 12 碼」識別，不含行號：檔案其他位置的修改不影響，但**改到基線內的那一行就會重新回報，必須修掉**。同鍵出現次數超過基線計數的部分也算新問題。
- **只減不增**：基線內但已不再出現的條目讓門禁失敗，必須執行 `make lint-baseline-prune`（Linux；同時處理 linux 與交叉檢查的 windows）把它移除；`-prune` 只會降低計數，從不加入新條目；`-init` 只在基線檔不存在時建立；`total` 與條目合計不符的手改基線會被拒絕。審查時基線檔只應出現刪除行。
- 不採用 golangci-lint 的 `new-from-rev`：它依賴完整 git 歷史與 merge-base（淺 clone 與 Windows 易誤判），且舊程式碼一旦不在 diff 中就永遠不檢查、也無法量化是否在減少；計數基線可在每次 CI 精確驗證「只減不增」。

2026-10-04 基線數量（同一份程式碼）：

| 類別 | linux 正式碼 | linux 測試 | windows 正式碼 | windows 測試 |
| --- | ---: | ---: | ---: | ---: |
| revive `exported`（匯出識別字缺註解） | 1254 | 0 | 1254 | 0 |
| revive 其他（unused-parameter、redefines-builtin-id、error-return、context-as-argument、package-comments 等） | 128 | 140 | 129 | 138 |
| errcheck（主要為 CLI 的 `fmt.Fprint*` 與唯讀／錯誤路徑上的 `Close`） | 199 | 259 | 198 | 228 |
| gosec G115（整數轉換溢位，多為雜湊編碼的位元重解讀或已設上限的值，待逐項審查） | 70 | 0 | 63 | 0 |
| 小計 | 1651 | 399 | 1644 | 366 |

合計：linux 2050、windows 2010。

優先順序：G115 與正式碼 errcheck 先清，再處理 revive `exported`。每批修正後執行 `make lint-baseline-prune` 並提交縮小後的基線。

## 覆蓋率棘輪

設定在 [`tools/coverage-thresholds.json`](../tools/coverage-thresholds.json)，由 [`tools/covergate`](../tools/covergate/main.go) 讀取 `go test -coverprofile` 結果按包計算語句覆蓋率（與 `go test -cover` 相同算法）：

- **關鍵包（目標 ≥85%）**：存取規則與內容可見性預過濾（`internal/access`）、HTTP 認證／session／存取洩漏防護（`internal/adapter/http`）、相容 API 認證與可見性（`internal/adapter/compat`）、媒體直接交付（`internal/adapter/media`）、決定檔案是否可見的忽略規則（`internal/adapter/media/ignore`、`internal/platform/ignore`），以及密碼雜湊、機密加密、受信任代理位址、對外請求 SSRF 防護（`internal/platform/password`、`secretbox`、`netaddr`、`outbound`）。
- **核心包（目標 ≥70%）**：`internal/domain`、`internal/domain/medianame`、`internal/app`、`internal/platform/jobs`、掃描／NFO／圖片／字幕／中繼資料／探測 adapter，以及 config、logging、legacyignore、resources。
- **最低值（棘輪）**：取實測值向下取整到整數百分點，吸收不到一個百分點的執行間與主機間雜訊；低於最低值即失敗。補測試後執行 `make coverage-ratchet` 把最低值提高到新的實測值，該模式從不降低最低值，也在任何包失敗時拒絕寫入。最低值低於目標的包必須在設定中寫明理由。
- **測量條件**：不連 PostgreSQL（與 CI foundation job 相同；需資料庫的測試在此跳過），只在 Linux 執行；Windows 覆蓋率不同，不作門禁。

2026-10-04 實測（WSL2 Linux，兩次重跑僅 `internal/adapter/probe` 差 0.1）：

| 包 | 層級 | 實測 | 最低值 | 目標差距 |
| --- | --- | ---: | ---: | --- |
| internal/access | 關鍵 | 96.2% | 96 | 達標 |
| internal/adapter/compat | 關鍵 | 80.3% | 80 | **差 4.7 點** |
| internal/adapter/http | 關鍵 | 83.1% | 83 | **差 1.9 點** |
| internal/adapter/media | 關鍵 | 92.4% | 92 | 達標 |
| internal/adapter/media/ignore | 關鍵 | 85.7% | 85 | 達標 |
| internal/platform/ignore | 關鍵 | 90.6% | 90 | 達標 |
| internal/platform/netaddr | 關鍵 | 100.0% | 100 | 達標 |
| internal/platform/outbound | 關鍵 | 87.6% | 87 | 達標 |
| internal/platform/password | 關鍵 | 95.9% | 95 | 達標 |
| internal/platform/secretbox | 關鍵 | 90.9% | 90 | 達標 |
| internal/domain | 核心 | 75.2% | 75 | 達標 |
| internal/domain/medianame | 核心 | 97.6% | 97 | 達標 |
| internal/app | 核心 | 71.4% | 71 | 達標 |
| internal/platform/jobs | 核心 | 71.4% | 71 | 達標 |
| internal/adapter/nfo | 核心 | 84.6% | 84 | 達標 |
| internal/adapter/scan | 核心 | 79.1% | 79 | 達標 |
| internal/adapter/images | 核心 | 85.4% | 85 | 達標 |
| internal/adapter/subtitles | 核心 | 90.8% | 90 | 達標 |
| internal/adapter/metadata | 核心 | 93.9% | 93 | 達標 |
| internal/adapter/probe | 核心 | 93.7–93.8% | 93 | 達標 |
| internal/platform/config | 核心 | 92.0%（91.98） | 91 | 達標 |
| internal/platform/logging | 核心 | 90.3% | 90 | 達標 |
| internal/platform/legacyignore | 核心 | 92.4% | 92 | 達標 |
| internal/platform/resources | 核心 | 94.6% | 94 | 達標 |

尚未納入門禁：`internal/adapter/postgres`（可見性 SQL 的實際落點，但測試需要真實 PostgreSQL，CI 中分成 4 片 race 執行，需合併分片覆蓋率後才能計算，列為後續工作）、`internal/platform/runtime`／`sandbox`／`process`（原生沙箱與程序管理，覆蓋率高度依賴主機能力）與 `cmd/*`。這些包沒有被宣稱達標。

## 基準回歸（同機比較）

`docs/evidence/bench-baseline.txt` 是開發機參考（同機兩次 ns/op 差約 11%），GitHub 託管 runner 的硬體每次都可能不同，拿它比 CI 數字沒有意義。因此 CI 改用 [`scripts/bench-compare.py`](../scripts/bench-compare.py)：

1. 以 `git worktree` 取出基準提交（PR 的 base；push 的前一個 head；兩者都不可用時退回 `HEAD^`；連 `HEAD^` 都沒有的首個提交則略過比較）。
2. 在同一台 runner 上交替執行 3 輪 base／head（每輪 `-count=2`、`-benchtime=500ms`、`-p 1`，第二輪顛倒順序），每邊得 6 個樣本，抵消執行期間的漂移。
3. 以 [`tools/benchgate`](../tools/benchgate/main.go) 比較中位數。門檻取捨：allocs/op 與 B/op 是確定值，維持嚴格（+10%、+20%）；ns/op 受共用 runner 雜訊影響，放寬到 +25%，只擋明顯退化，小幅計時退化仍需每個最佳化提交附 benchstat（G26.4）。
4. base 有、head 沒有的基準視為失敗（刪除或改名基準不能讓門禁失效，G30.5）；head 新增的基準只列出不擋。
5. 放在獨立的 `bench-regression` job，不與 PostgreSQL job 同機，避免資料庫負載干擾計時；結果檔上傳為 `bench-compare` 製品。

### 刻意回歸的接受清單

有些改動是刻意用一邊的成本換另一邊（例如 G47 在規則編譯時建立前置篩選，編譯配置數上升、評估時間大幅下降）。這類回歸記在 `tools/bench-accepted.json`，由 `scripts/bench-compare.py` 自動傳給 `benchgate -accept`：

- 每筆必須有 `benchmark`（完整名稱）、`unit`（`ns/op`、`B/op`、`allocs/op` 之一）、`max`（允許的上限，用實測值加少量餘裕）與 `reason`（理由與相關提交）；缺任何一項或有未知欄位，門禁直接以設定錯誤結束。
- 只放行該基準、該單位，而且只到 `max`；之後再漲超過上限仍是回歸，所以一筆紀錄不會讓某個基準永久失去保護。
- 被接受的項目會在報告中以 `accepted` 列出，CI 紀錄看得到。
- 這次加入的唯一一筆：`internal/access.BenchmarkCompile10k` 的 allocs/op（CI 實測 36094 → 43565，上限 45000）。

## 反向驗證（2026-10-04 本機）

- 新增一個含 `os.Remove(...)` 未檢查錯誤的檔案：`make golangci-lint` 失敗，列出 `errcheck: Error return value of os.Remove is not checked`。
- 修改基線內的一行（`defer root.Close()` 加註解）：該行以新問題回報，原條目列為「已修復但仍在基線」，門禁失敗。
- 把 `internal/access` 最低值改成 97（高於實測 96.2%）：covergate 回報 `FAIL: below ratchet minimum`，結束碼 1。
- 在 `medianame.ParsePath` 暫時加入 64 次配置：`make bench-compare` 回報 ns/op +44%、B/op +106%、allocs/op +270% 三項 REGRESSION，結束碼非零；還原後 base 與 head 相同時各項差距在 ±2% 內並通過。
