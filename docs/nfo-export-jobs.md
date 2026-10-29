# NFO 匯出全部任務（G39.14）

管理員可把一個媒體庫所有條目目前儲存的 metadata 寫回各自既有的 NFO：一個 `nfo_export` 任務逐批準備受控文字編輯，再交給一般的 `nfo_write` 子任務寫檔。這是 G39.14 三種批次操作的第二種；[全量導入](nfo-apply-jobs.md)為第一種，第三種「只匯出缺失」是同一個任務的 `missing` 模式（見下）。匯出任務自己沒有任何寫檔路徑：準備、備份、提交、恢復全是既有的[寫回任務](nfo-write-jobs.md)與[準備](nfo-write-preparations.md)。

## 提交

```http
POST /api/v1/libraries/LIBRARY_ID/nfo/export
Authorization: Bearer SESSION_TOKEN
Idempotency-Key: unique-confirmation-key
Content-Type: application/json

{"priority":"manual","mode":"all"}
```

- 僅管理員；`priority` 為 `manual`（預設）或 `background`；`mode` 為 `all`（預設，本文以下的行為）、`missing`（見「只匯出缺失」）或 `fix`（見「安全修復」），其他值為 400；body 為 `{}` 亦可。
- 新任務 202 與 `Location`；相同 key、相同庫、優先級與模式回 200 與 `Idempotency-Replayed: true`（庫之後關閉寫入權限也照樣重播）；同 key 不同庫、優先級、模式或任務種類為 409 `conflict`（冪等比對含模式）。
- 庫不是 `read-only` 或沒有開啟寫回權限（`nfo_write_enabled`），或 runtime 沒有啟用 `enableNFOWrite`，為 409 `nfo_write_disabled`，與 `POST …/nfo/writes` 同一檢查。庫已有活動任務為 409 `job_busy`，全域佇列滿為 429 `job_queue_full`。
- 稽核事件 `nfo.export_submitted`（庫、優先級、模式）。每個子任務另寫既有的 `nfo.write_submitted`（含 `exportJobId` 與 chunk 編號），每份準備寫既有的 `nfo.write_prepared`。

CLI：

```sh
jelee-cli nfo export --library ID [--missing] [--priority manual|background] --key KEY --token-stdin [--url ORIGIN] [--json]
jelee-cli nfo export-status --job ID --token-stdin [--url ORIGIN] [--json]
```

## 觀察

`GET /api/v1/jobs/JOB_ID/nfo-export`（僅管理員）：

```json
{"jobId":"…","libraryId":"…","mode":"all","cursorItemId":"…","childJobId":null,"chunks":3,"written":34,"fixed":0,"unfixable":0,"unchanged":51,"skippedExisting":0,"skippedMissing":17,"skippedInvalid":17,"skippedOther":17,"failed":0,"updatedAt":"…"}
```

`mode` 是任務的模式，`skippedExisting` 只在 `missing` 模式非零，`fixed` 與 `unfixable` 只在 `fix` 模式非零（`fix` 模式的 `written` 恆為 0）。`cursorItemId` 在第一個檢查點前為 `null`；`childJobId` 是尚未計入的 `nfo_write` 子任務，沒有時為 `null`；`chunks` 是已提交的子任務數。其他種類的任務回 404。既有 `GET /api/v1/jobs/JOB_ID` 的 `files` 為 written＋unchanged，`skipped` 為 skippedExisting 加三種 skipped 再加 failed，`kind` 為 `nfo_export`。

## 資料庫到 NFO 的對應

每個條目讀目前的 metadata（修訂版與已儲存的欄位）與它的 NFO，只為**已儲存且與 NFO 不同**的欄位建立編輯，欄位以準備支援的受控文字元素寫入；不同的欄位才寫，沒有差異的條目不準備、不寫檔。

| 資料庫欄位 | NFO 元素 | 種類 |
| --- | --- | --- |
| `title` | `title` | 電影、影集、季、單集 |
| `originalTitle` | `originaltitle` | 全部 |
| `overview` | `plot` | 全部 |
| `sortTitle` | `sorttitle` | 全部 |
| `tagline`、`outline`、`mpaa`、`certification` | 同名 | 全部 |
| `showTitle`（事實） | `showtitle` | 單集 |
| `seriesStatus`（事實） | `status` | 影集 |

- 編輯用既有的受控編輯：未知標籤、屬性、註解與排版原樣保留，BOM 保留原樣，NFO 缺少的元素會新增（`createMissing`），備份保留 3 份（`.jelee.bak`）。與單筆寫回相同，寫入時會補上缺少的 Jelee 識別碼（`uniqueid type="jelee"`）。
- 只匯出已儲存的值。仍是掃描得到的名稱（來源 `existing`）的標題不算儲存值；空值**不會**清掉 NFO 內的內容，所以尚未導入過的庫不會被檔名覆蓋 NFO。
- 不匯出的欄位：`date`、年份、片長、評分、類型等列表、演員、識別碼、系列／季／集數字與時間等；準備的受控編輯目前只支援上表的文字元素。
- 電影、影集（`tvshow.nfo`）、季（`season.nfo`）、單集（`episodedetails`）都走同一條路徑；真實 PG 測試各有一套。其他種類或沒有唯一媒體來源、NFO 範圍不成立、條目已刪除的條目歸入 `skippedOther`。

| 計數 | 條件 |
| --- | --- |
| written | 子任務的條目已被取代（結算階段 replaced） |
| unchanged | 沒有任何已儲存值與 NFO 不同 |
| skippedMissing | 條目沒有 NFO 檔（`all` 模式不建立檔案；要建立用 `missing` 模式） |
| skippedInvalid | NFO 無效 |
| skippedOther | 無法準備：NFO 帶全域鎖（`lockdata`）、差異欄位全被 NFO 鎖住（被鎖的欄位不編輯，其餘照寫）、無媒體來源、不支援的種類、已刪除 |
| failed | 條目層級的錯誤（例如修訂版衝突經一次重試仍衝突、來源變更經 5 次嘗試仍變更）、已準備但子任務沒有取代的條目（子任務失敗、取消、被拒絕） |

### 讀取時的衝突重試

條目的 NFO 讀取在觀察期間比對父目錄與檔案的狀態，變了就回「來源已變更」（`nfo_changed`，對外是衝突）。同一個目錄也會被不相干的活動改動：背景的寫回清理程序（[docs/nfo-commit-cleanup.md](nfo-commit-cleanup.md)）會刪除已結束子任務的 token 檔，目錄修改時間隨之變動，同一個季資料夾內的許多集數因此有機會讀到「來源已變更」。偵測以目錄身分與修改時間為準而非名稱，無法把清理程序擁有的名稱排除而不削弱檢查，所以檢查本身不變，改由 worker 重試：

- 來源已變更：最多 5 次嘗試，每次之間暫停 25、50、100、200 毫秒（隨 context 取消而中止）。每次都重新讀取資料庫與 NFO 並重做完整檢查，只是不採用有衝突的那次觀察。
- 修訂版衝突與準備被拒的衝突：維持重試一次。
- 5 次後仍變更（例如檔案真的在被改動）計為 `failed`。

單一條目失敗不會停止任務。資料庫、租約、授權與 context 錯誤停止執行並保留游標；失敗碼為 `nfo_export_failed`，逾時為 `job_timeout`。寫入權限在任務途中被收回時，下一個子任務的提交被拒（409 對應的 `nfo_write_disabled`），任務失敗，不再寫檔。

## 不佔住 worker 的協調

匯出任務從不在 worker 內等子任務，否則只有一個 worker 時會死結。協調用三個零件：

1. **一庫一活動任務的例外。** 資料庫以唯一索引保證一庫同時只有一個 queued／running 任務。遷移 000089 把索引改成 `(library_id, (export_job_id IS NOT NULL))`：新欄位 `jobs.export_job_id`（`parent_id` 已用於重試，不能共用）由匯出子任務填入匯出任務的 ID，所以一庫最多一個活動的頂層任務加一個活動的匯出子任務；`CHECK` 限制只有 `nfo_write` 任務能有 `export_job_id`。索引的部分條件不變，工作指標查詢仍用它。

    遷移 000093 補上這個欄位的完整性（先前只有上述 `CHECK`）：外鍵 `jobs_export_job_fkey` 指向 `jobs(id)`，觸發器 `guard_job_export_parent` 要求被指的任務是同一媒體庫的 `nfo_export`（否則 `23514`；不存在的任務由外鍵拒絕，`23503`），`guard_job_export_children` 讓帶有子任務的匯出任務不能改 `kind` 或 `library_id`。外鍵用 `ON DELETE SET NULL`：歷史修剪依時間刪除已結束的匯出任務，而留有提交日誌的子任務依 `retain_nfo_write_commit_job` 不能被刪，`CASCADE` 或 `RESTRICT` 都會讓修剪整批失敗；子任務因此保留自己的歷史，只失去指標。修剪另外不刪仍有 queued／running 子任務的匯出任務（清除指標會讓子任務變成第二個活動的頂層任務）。升級時，先前因匯出任務被修剪而懸空（或指向不合規任務）的指標會清為 NULL，子任務本身不動；降級只移除外鍵、觸發器與索引，不需要拒絕。
2. **領取閘門。** 匯出任務的子任務是 queued 或 running 時，領取查詢不會選它（`nfo_export_job_state.child_job_id` 指向的任務）。閘門不靠計時，子任務一終止，下一次輪詢就能領取。
3. **讓出（yield）。** 每次領取只推進一步：(a) 把已終止的子任務結算進計數；(b) 從游標往後準備條目，直到有 16 份準備或該批 50 個條目讀完；(c) 沒有需要寫的條目就在同一步直接記入游標與計數，繼續往下讀；(d) 有準備就在**一個受租約保護的交易**內建立子任務、複製其工作意圖、刪除這一 chunk 的準備、寫入子任務 ID、chunk 編號、游標與計數；(e) 以 `ErrNFOExportYield` 結束這一步。runner 在等監控執行緒結束後呼叫 `YieldNFOExport`，把任務放回 queued 並退還這次領取的 attempt（讓出不消耗重試次數），然後 worker 立刻能領取子任務。子任務終止後，匯出任務再被領取，從 (a) 繼續；沒有條目可讀時以成功結束。

子任務用匯出任務的政策欄位建立（不再檢查佇列上限，那在匯出任務准入時檢查過），優先級與匯出任務相同。一個 chunk 最多 16 份準備（每個 actor 的準備上限是 32 份，留一半給管理員自己的準備），遠低於每個 `nfo_write` 任務 100 個條目的上限。chunk 在 16 份準備或一次 50 個條目的讀取結束時截止，所以子任務數取決於需要寫入的比例。

### 取消

- 取消用既有 `POST /api/v1/jobs/JOB_ID/cancel`。沒有子任務的 queued 匯出任務立即 `cancelled`。有子任務時，同一交易要求子任務取消（queued 的立即取消、running 的在下一個心跳停止並由既有的恢復迴圈回復），匯出任務留在佇列中，等子任務終止後被領取，結算子任務的結果（已取代的條目記入 `written`，其餘記入 `failed`）再以 `cancelled` 結束。running 的匯出任務取消時，結束交易同樣先要求子任務取消，子任務仍在跑就把自己放回佇列做同樣的收尾。
- 取消之後不再準備新的 chunk；游標之後的條目與檔案不被碰。已經完成的 chunk 不會被還原。

### 租約到期與恢復

- 每個步驟的持久狀態只有 `nfo_export_job_state` 一列，所有變更在受租約保護的交易內，所以租約遺失的 worker 無法留下半個子任務。子任務 ID、chunk 編號、游標與計數同交易提交，不存在「子任務已建立但沒記錄」的窗口。
- 在提交之前租約到期：這一 chunk 的準備留在準備表。重新領取後以同一組冪等鍵（`nfo-export:<任務>:<chunk>:<條目>`）重播，取回第一次保存的準備與固定 UUID，再提交一次；子任務鍵為 `nfo-export:<任務>:<chunk>`，並受 `UNIQUE(actor_id, idempotency_key)` 保護，不會產生兩個子任務。
- 在提交之後、讓出之前租約到期：領取時的過期租約清理把任務放回佇列（消耗一次 attempt），下一步看到子任務仍在跑就讓出，終止了就結算。
- 任務結束（任何終止狀態）會刪除它名下殘留的準備；成功提交的 chunk 在提交交易內就刪除準備，所以大批匯出不會碰到每 actor 32 份的上限。這個刪除是新增的：此前準備只靠 24 小時過期清理。
- 子任務被歷史修剪移除時，它的條目計為 `failed`。

## 執行身分

與全量導入相同：執行身分是提交者，每一步取其目前有效的 session 作為 actor，所以任務可以比提交時的 session 活得久，但提交者被停權、刪除、失去管理員身分或沒有任何有效 session 時，下一步即停止（失敗），不再準備或建立子任務。子任務的 `actor_id` 是提交者，其執行與一般寫回任務相同（不依賴 session）。

## 限制

- 只匯出上表的文字欄位；其他 NFO 內容（日期、評分、演員、識別碼等）不被覆寫。往返（匯出後再全量導入）的資料庫 metadata 不變，唯一差異是寫入時補上的 Jelee 識別碼會在導入後出現在 `uniqueIds`（型別 `jelee`），且僅限實際寫過的條目。
- `all` 模式沒有 NFO 檔的條目只計數，不建立檔案；建立檔案是 `missing` 模式。
- 空值不清除 NFO 內容；要清掉 NFO 欄位必須在 NFO 內處理。
- 被 NFO 鎖住的欄位與 `lockdata` 的條目不寫（鎖是 NFO 對外部編輯的聲明）。
- 條目的 `written` 只在子任務終止後才加進計數，所以進行中的 chunk 不在其中；`failed` 也會包含被取消的子任務未寫的條目。
- 準備表滿（其他人佔用）時，已備妥的條目先提交，下次再繼續；一個都備不出來就失敗。
- 一庫一個活動頂層任務；沒有跨庫批次。每份準備和每個子任務各寫稽核，量隨寫入條目數成比例。
- 遷移 000089 新增 `nfo_export_job_state`（每任務一列，隨任務歷史清理），並擴充 `jobs.kind`、錯誤碼、工作指標維度（共 14 組）、`jobs_export_job_kind_check` 與新的活動任務索引；仍有 `nfo_export` 任務、狀態列或帶 `export_job_id` 的子任務時拒絕降級。該表不進中繼資料備份。
- 能領取匯出任務的 runner 必須同時能領取 `nfo_write`（`enableNFOWrite`），`jobs.New` 強制這點。

## 只匯出缺失（`missing` 模式，遷移 000091）

同一個 `nfo_export` 任務、同一套[不佔住 worker 的協調](#不佔住-worker-的協調)（分批至多 16 份、子 `nfo_write` 任務、讓出、取消、租約到期續跑、冪等鍵 `nfo-export:<任務>:<chunk>[:<條目>]`），差別只在每個條目怎麼處理。模式存在 `nfo_export_job_state.mode`（`all` 或 `missing`，預設 `all`），不新增工作種類、錯誤碼或工作指標維度。

| 條目 | 處理 | 計數 |
| --- | --- | --- |
| 慣用路徑（與 [3c-i](nfo-create-target.md) 同一個解析：電影與單集為媒體旁的 `<主檔名>.nfo`、影集 `tvshow.nfo`、季 `season.nfo`）沒有 NFO，且有可匯出的已儲存欄位 | 準備 `create: true` 的請求（`backups` 0、`bom` 為 `omit`），編輯為上表所有「已儲存且非空」的欄位，並由子任務建立 | written（子任務結算為 replaced 時） |
| 沒有 NFO，但沒有任何可匯出的已儲存值（空值與掃描名稱回退不算） | 不準備、不寫檔 | unchanged |
| 已有 NFO（有效或無效，含 `lockdata`） | 不碰 | skippedExisting |
| 建立時目標已出現，被新建協議拒絕 | 外來檔案位元組與 inode 不變，見下 | skippedExisting |
| 其他（無媒體來源、不支援的種類、已刪除） | 同 `all` | skippedOther |
| 條目層級錯誤、子任務失敗、取消或被拒絕但目標並不存在 | 同 `all` | failed |

`all` 模式的行為與計數完全不變；`missing` 模式不使用 `skippedMissing`、`skippedInvalid` 與 NFO 欄位鎖（沒有 NFO 就沒有鎖）。

### 並發出現的檔案

準備時目標不存在，但提交時（子任務執行之前，或在 phase 1 之後、link 之前）名稱可能已被別人建立。新建協議不會覆蓋它：條目被 `nfo_target_appeared` 拒絕，該子任務最後以 `nfo_write_failed` 結束（既有行為，其餘條目照寫）。拒絕的原因沒有持久化，所以匯出任務在子任務終止後這樣判斷，才把拒絕記為 skippedExisting：

1. 資料庫端只挑出**候選**：子任務中沒有被取代的條目，且其 token 沒有「已進入 phase 1 而未回滾」的證據（沒有 token、phase 0，或 phase 1 之後已 phase 3 回滾）。已進入 phase 1 而未回滾的條目，目標上的檔案可能就是 token 自己的輸出，不當作外來檔案，一律是 failed。
2. 任務在讓出之後被再次領取時（`NextNFOExportStep` 回傳 settle 步驟，尚未提供任何條目），以任務身分重讀每個候選的 NFO 狀態；**目標確實存在**（有效或無效）才計 skippedExisting，不存在或讀不到的計 failed。
3. `SettleNFOExportChild` 在受租約保護的交易內重新讀取子任務結果，確認 skippedExisting 不超過候選數，把 written、skippedExisting 與 failed 與清除子任務 ID 同交易提交；結算前租約遺失則整個 settle 步驟重做，不會重複計數。

所以目標存在但該條目其實是別的原因沒寫成（例如子任務在外來檔案出現後才被取消）也會算 skippedExisting；這是可接受的近似，檔案反正沒被動。取消中的任務（已要求取消）不做這個判斷：未取代的條目一律 failed。

### 驗證與限制

- 真實 PG：104 個條目（8 部影集各含影集、季、4 集，加 56 部電影；每五個條目有一個既有 NFO，其中一半無效；部分條目沒有任何已儲存值）。涵蓋建立檔案的內容（BOM、宣告、根元素、欄位、`uniqueid type="jelee"`）、四種種類、既有檔案的位元組、inode 與修改時間不變、無 `.jelee.bak`、計數、無殘留準備與 token 檔；`missing` 之後再跑 `missing`（全部變 skippedExisting，不提交子任務）與 `all`；與 `nfo_apply` 往返（唯一差異是新檔補上的 jelee 識別碼）；兩種並發出現的檔案；子任務在不寫檔下結束（failed）；兩種取消；租約在子任務記錄前後遺失，與不中斷的任務逐檔相同且不重複或遺失子任務（續跑測試不斷言精確的 attempt 次數，只要求被重新領取）；HTTP 准入（無效模式 400、寫入未開 409、重播、同 key 換模式 409）、遷移往返與帶資料拒絕降級。
- 建立的檔案沒有原文，也沒有備份；`.jelee.bak` 只屬於 `all` 模式改寫的檔案。
- 遷移 000091 為 `nfo_export_job_state` 新增 `mode`（CHECK 為 `all` 或 `missing`）與 `skipped_existing`（只有 `missing` 能非零）；降級在仍有任何 `missing` 模式的狀態列（或非零 `skipped_existing`）時拒絕，空資料或只有 `all` 任務時直接移除兩欄。
- 範圍外：不建立 NFO 以外的檔案（海報、字幕）；目標是目錄、符號連結或懸空連結時，新建協議視為已存在而拒絕（不覆蓋）；這些情況沒有專屬測試，條目歸入 skippedExisting 還是 failed 取決於 NFO 讀取端是否觀察到該名稱。

## 安全修復（`fix` 模式，遷移 000092，G39.11 `--fix`）

`jelee-cli nfo validate --library X --fix` 的修復部分。同一個 `nfo_export` 任務、同一套[不佔住 worker 的協調](#不佔住-worker-的協調)（分批至多 16 份、子 `nfo_write` 任務、`.jelee.bak` 備份保留 3 份、讓出、取消、租約到期續跑、冪等鍵 `nfo-export:<任務>:<chunk>[:<條目>]`），沒有任何新的寫檔路徑。模式存在 `nfo_export_job_state.mode`（`all`、`missing` 或 `fix`），不新增工作種類、錯誤碼或工作指標維度。准入與其他模式相同：庫須 `read-only` 且開啟寫回權限，否則 409 `nfo_write_disabled`。

每個條目以庫校驗任務同一個解析器驗證它選到的 NFO（`ValidateItemNFO`：與條目選擇相同的目錄觀察與拒絕，加上同一份摘要），**只**修復一種問題，其餘一律不碰：

| 問題 | 修復 | 條件 |
| --- | --- | --- |
| `nfo_title_missing` | 把 `title` 設為資料庫**已儲存**的標題（來源不是掃描名稱 `existing`，且非空） | 沒有已儲存標題時不修復該問題；NFO 沒有鎖住標題 |

沒有識別碼**不是**校驗問題（沒有 `nfo_id_missing`：絕大多數既有 NFO 本來就沒有 Jelee 識別碼，把它們都算成警告只會讓警告數失去意義），`fix` 也不會只為補識別碼而重寫檔案。但寫入路徑對**任何**重寫都經 `EnsureID` 補上缺少的識別碼（G39.10 在每一次寫入成立，不靠校驗或 `fix`），所以修復缺標題時，同一份沒有識別碼的 NFO 也會得到 `<uniqueid type="jelee">UUID</uniqueid>`；不覆蓋任何既有識別碼（手工、provider、自訂 type 或手工 `jelee` 值），NFO 有 `lockdata` 或識別碼鎖時不補。

| 條目的狀況 | 處理 | 計數 |
| --- | --- | --- |
| 沒有 NFO | 不建立（建立是 `missing` 模式） | skipped_missing |
| 沒有任何問題 | 不碰 | unchanged |
| 至少一個可修復的問題，且寫入可行 | 準備受控編輯（標題編輯；重寫時順帶補識別碼），子任務重寫並留 `.jelee.bak` | fixed（子任務結算為 replaced 時） |
| 只有不修復的問題：`nfo_season_missing`、`nfo_episode_missing`、`nfo_conflicting_id`，或缺標題但沒有已儲存標題 | 不碰 | unfixable |
| XML 損壞、不安全 XML、編碼無效或不支援、過於複雜（解析失敗，無摘要問題） | 不碰 | unfixable |
| 任何 error 問題：`nfo_invalid_date`、`nfo_invalid_integer`、`nfo_invalid_number`、`nfo_invalid_boolean`、`nfo_unsafe_reference`、`nfo_id_incomplete` 等 | 不碰（寫入路徑本來就拒絕有 error 的文件） | unfixable |
| 未知根元素、包裝根、多條目文件、根與條目種類不符（`nfo_unknown_root`、`nfo_wrapper_fields_ignored`、`nfo_kind_unknown`） | 不碰 | unfixable |
| 編碼是猜的（`nfo_encoding_guessed`） | 不碰（重寫會轉成 UTF-8，不改寫猜測的編碼） | unfixable |
| `lockdata`，或缺標題而 NFO 鎖住標題 | 不碰 | unfixable |
| 路徑非法（`nfo_path_*`，見[路徑問題](#非法路徑問題nfo_path_)） | 不碰 | unfixable |
| 無媒體來源、不支援的種類、已刪除、檔案讀取失敗 | 同 `all` | skipped_other |
| 條目層級錯誤、子任務失敗、取消或被拒絕但條目並未被取代 | 同 `all` | failed |

`fix` 模式把子任務取代的條目計為 `fixed`（`written` 恆為 0）；`fixed`＋`unchanged` 計入 `files`，`unfixable`、`skipped_*` 與 `failed` 計入 `skipped`。已修復的 NFO 之後再跑 `fix`（或再校驗）不再有該問題，所以重跑是 unchanged 且不提交子任務。

### 非法路徑問題（`nfo_path_*`）

庫校驗任務在讀檔**之前**檢查每個 NFO 的位置，把拒絕轉成一個 error 問題（欄位 `path`，條目 -1），與條目選擇拒絕的是同一批位置，拒絕本身沒有放寬（拒絕仍是 `ErrNFOInputUnavailable`，只是多帶固定理由碼）。檔案不被讀取，摘要的條目數為 0、編碼與根為 `unknown`，只含這一個問題；永不自動修復。

| 代碼 | 意義 |
| --- | --- |
| `nfo_path_symlink` | NFO 名稱本身是符號連結 |
| `nfo_path_element_symlink` | 路徑上的目錄（或媒體錨點、庫根）是符號連結，即使連結指向庫內 |
| `nfo_path_not_regular` | NFO 是目錄、FIFO、socket、裝置等非一般檔案（或其上層不是目錄） |
| `nfo_path_case_collision` | 同一目錄有另一個名稱僅大小寫不同的項目（Linux 可發生），兩個都被標示 |
| `nfo_path_outside_root` | 相對路徑為絕對路徑或含 `..` |
| `nfo_path_too_long` | 路徑超過 1,024 位元組或單一元素超過 255 |
| `nfo_path_invalid_name` | 含控制字元、反斜線、冒號或非 UTF-8 |

結果存在與一般摘要相同的觀察列，經既有的 `current-validations` 與 `…/issues` 取得：觀察的 `entries` 為 0、`errorCount` 為 1。檔案沒有被讀，所以戳記是清單記錄的大小與修改時間加上只由代碼決定的摘要；位置修好後，下一次校驗以真實戳記取代該列。條目選擇在 `fix`（與其他模式）遇到同樣的位置時，`fix` 計為 unfixable。

限制：掃描器本來就不把符號連結與非一般檔案放進清單，所以從未進入清單的連結 NFO 不會出現在校驗任務的問題清單；只有清單記錄之後才變成連結或非一般檔案的 NFO、路徑上的目錄被換成連結，以及大小寫衝突（兩個都是一般檔案）會出現。`fix` 對條目的 NFO 做的是條目選擇，所以連結 NFO 在 `fix` 計為 unfixable。`nfo_path_outside_root`、`nfo_path_too_long`、`nfo_path_invalid_name` 是防守性的詞彙：清單與條目範圍的路徑在進入資料庫時已被詞法檢查擋下。大小寫衝突的檢查每個 NFO 列出一次它的目錄（上限同條目選擇的 65,536 項）。

### 驗證與限制

- 真實 PG：115 個條目（110 部電影分十一類各十份、影集、兩集、連結 NFO、大小寫衝突），涵蓋：只有可修復檔案被重寫、備份與原位元組相同、未知標籤與註解保留、既有識別碼（手工、provider、自訂、手工 `jelee`）位元組不變、缺標題又沒有識別碼的檔案得到標題與恰好一個 `jelee` UUID、只缺識別碼而有標題的檔案 unchanged（位元組、inode、修改時間不變）、其餘檔案位元組、inode、修改時間不變、計數；修復前後各跑一次真實校驗任務，已修復的問題消失、不修復的問題仍列出；第二次 `fix` 全部 unchanged 且不提交子任務；兩種取消；兩種租約遺失續跑與不中斷的任務逐檔相同（不斷言精確的 attempt 次數）；寫入權限被收回；HTTP 准入（無效模式 400、寫入未開 409、重播、換模式 409）；遷移往返與帶資料拒絕降級。
- 校驗任務的路徑問題另有真實 PG 測試：符號連結、FIFO、目錄取代 NFO、路徑上的目錄被換成連結、大小寫衝突，位置修好後觀察被取代；DB-free 單元測試涵蓋對應表與詞法分類（連結與 FIFO 的檔案系統測試只在 Linux 執行）。
- 遷移 000092 為 `nfo_export_job_state` 把 `mode` 的 CHECK 擴充為 `all`／`missing`／`fix`，新增 `fixed`、`unfixable`（CHECK：`fix` 模式 `written` 為 0，其他模式兩者為 0）；降級在仍有 `fix` 模式的狀態列（或非零計數）時拒絕，否則還原約束並移除兩欄。
- 範圍外：不修復日期、數字、參照、編碼或結構；不補 `season`／`episode`；不為沒有已儲存標題的條目猜標題；沒有真實客戶端往返驗證。
