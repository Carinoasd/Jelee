# 交給專案擁有者開發的任務

這份文件列出要請 MoYuanCN（或代跑的 Codex）實作的開發任務。另一份 [`owner-verification-queue.md`](owner-verification-queue.md) 是「只需要跑或確認」的驗證；本文件是「需要寫程式」的工作。

開始前請先閱讀：
- [`requirements-source.md`](requirements-source.md)：G39（NFO 寫回）、G40（圖片）、G13（任務）的原文。
- [`nfo-commit-recovery-lease.md`](nfo-commit-recovery-lease.md)：恢復租約（schema57）。
- `nfo-commit-settlement.md`：結算、收尾與 nfo_write 工作程序（schema59），隨 NFO 寫回工作程序一起合入。
- [`local-images.md`](local-images.md)、[`storage-layout.md`](storage-layout.md)。

共同規則：
- 架構：`internal/domain`、`internal/app` 不得 import `os`、`net`、`net/*`、`database/*` 或第三方套件，`internal/architecture` 的測試會擋。
- 不得新增 Go 依賴（go.mod 已發布）。
- 新 migration 接在目前最新編號之後。測試裡一律用 `SchemaVersion` 或 `downgradeAboveMigration(…, "<migration 名稱>")`，不寫死版本號。
- 改到 HTTP 路由或錯誤碼時，要跑 `make openapi` 重新產生 `api/openapi.json`，並把新路由登記到 `internal/adapter/http/access_leak_test.go` 的 `leakRouteTable`。
- 真 PG 測試要加 `-p 1 -parallel 2`，不要和其他重負載同時跑（機器記憶體有限，曾因此 OOM）。
- 每項完成後，在 PR 回報 commit 與測試結果。

建議順序：任務一 → 任務三；任務二可以和任務一並行。

---

## 任務一：NFO 寫回的清理機制（優先）

### 為什麼要做
每次寫回都會保留以下物件，目前沒有任何程式移除它們：
- 結算後的 pin（original-pin、output-pin、rollback-pin）與回滾檔。
- 備份輪替時被擠出的 `<base>-backup-evicted`。
- attempt 容量：資料表 `nfo_write_commit_attempt_reservations` 只增不減，全域上限是 256 筆／1GiB。

**累計到 256 個 token 後，系統的所有新寫回都會被拒絕。** 任務三的批次匯出一定會撞到這個上限，所以任務一必須先完成。

### 要做的事
1. **資料庫**（新 migration）：
   - 新增「已清理」紀錄：每個 token 一筆，記錄清理階段與首次時間，只能往前推進，不可改寫。
   - 清理完成後釋放該 token 的 reservation，讓全域容量回收。
   - 守衛：只有已收尾（`nfo_write_commit_resolutions` 已有該 job）而且該 token 已有終態結算（替換或回滾），才可以清理。清理和正在進行的寫回不可同時發生，沿用 `nfo_commit_live_lease` 與 job 租約的規則。
   - quota fence（`fence_nfo_commit_attempt_quota`）要涵蓋「釋放」，避免舊快照超收。
2. **檔案系統**（`internal/adapter/nfo`）：
   - 只刪除本次 token 建立、而且身分（native identity）與持久紀錄一致的物件。身分不一致或觀察不清楚時，一律保留並回報，**絕不刪除使用者的檔案或不明物件**。
   - 需要刪除的有：五個 commit 名稱（各 attempt 的 namespace）、被擠出的舊備份（依 G39.8 的保留份數判斷是否超量）。
   - 保留：目標檔、保留份數內的 `.jelee.bak`。
   - 每一步都要能從持久紀錄冪等續作。
3. **排程**：在 nfo_write 工作程序的恢復迴圈之後，或另開一個背景任務，定期清理已收尾的 token。

### 可以直接使用的現有程式
- `internal/adapter/nfo/commit_files.go`：`nfoCommitFilePlan.names()`、`verifyNFOCommitFiles`。
- `internal/adapter/nfo/settle_commit_files.go`：結算階段判定，以及被擠出備份的命名。
- `internal/adapter/postgres/nfo_commit_attempts.go`：attempt 與 reservation 的存取，以及 quota fence。
- `internal/adapter/postgres/nfo_commit_recovery.go`：恢復租約。

### 驗收
- 真 PG：累計超過 256 筆寫回後，新的寫回仍然成功，容量有被回收。
- 清理期間不能對同一 token 再寫回；並發的清理只有一個能成功。
- 用子程序 `os.Exit` 中斷（參考 `internal/adapter/postgres/nfo_commit_attempt_process_test.go`）：在刪除每個檔案的前後各中斷一次，續作後結果正確，而且沒有誤刪。
- 外部替換了 pin 或回滾檔（身分不一致）時，保留該物件並回報，不刪除。
- 反向驗證：故意拿掉身分比對，測試必須失敗。

---

## 任務二：圖片掃描入庫、刷新任務、管理 API

### 要做的事
1. **掃描入庫**（G40.2、G40.3）：
   - 掃描或 catalog 發布時，對每個條目的目錄呼叫 `images.RecognizeImageNames`（`internal/adapter/images/naming.go`），把結果寫進 `item_images`。
   - 寫入使用 `postgres.upsertItemImage(ctx, tx, domain.Actor{}, in)`（非手動，`Manual=false`），在自己有 lease fence 的交易內呼叫。
   - 季圖（`season01-poster` 等）掛到 Season 條目；`season-all` 要展開到各季。Fanart 存成 Backdrop。
   - NFO 的 `art` 事實（目前在 `item_metadata_facts` 的 `field='art'`）：相對路徑要在 root 內才寫成 nfo 來源；遠端網址寫成 remote 來源，不在這裡抓取。
   - 一個目錄放多部電影時，`poster.jpg` 這類共用名稱要決定是否採用（建議只在一個目錄一部影片時採用）。
2. **刷新任務**（G40.9、G40.10）：
   - 新 job kind `image_refresh`（要改 jobs 的 kind CHECK 約束與指標維度）。
   - 用 `internal/adapter/images/fetch.go` 抓取尚未有內容的 remote 來源，寫入持久存放區（`store.go`）並回填 `content_sha256` 等欄位。
   - 必須尊重鎖定；可以取消、可以續跑；每筆失敗各自隔離，不影響其他條目。
3. **管理 API**（G40.10、G40.11）：
   - `POST`／`DELETE /api/v1/items/{id}/images/{type}`（`index` 用查詢參數，和現有 `/images` 一致）與鎖定、解鎖。
   - 刪除或更換要求明確確認（例如 `confirm=true`），並寫稽核。事件名要加進 `internal/adapter/postgres/audit.go` 的白名單（L4 合入後）。
   - 只清 Jelee 自己產生的變體，**不得動使用者原圖**。

### 可以直接使用的現有程式
- `internal/adapter/images/`：`naming.go`、`fetch.go`、`store.go`、`processor.go`。
- `internal/adapter/postgres/item_images.go`、`internal/app/images_ports.go`。

### 驗收
- 真 PG＋測試素材：電影、劇集、季、集各一套。驗證各命名模式都正確入庫，單集縮圖的三種來源（同名 thumb、NFO、遠端）都正確。
- 外部抓取失敗時，掃描與發布不受影響。
- 刷新任務的取消與續跑、鎖定的圖不被改寫。
- 管理 API：沒帶確認回 400、稽核有寫入、原圖 checksum 不變。

---

## 任務三：NFO 三種批次操作（G39.14）

需求原文：提供「從 NFO 全量導入」「匯出全部條目為 NFO」「只匯出缺失 NFO」三種批次操作，全部走任務系統（可取消、可觀測、批次處理）。

**前置條件：任務一必須先完成**，否則大量匯出會撞到 256 筆的容量上限。

### 要做的事
1. **全量導入**：
   - 掃描的 NFO 讀取階段已會整庫讀 NFO，但套用到資料庫目前只能逐條做（`app/nfo_item_apply.go`）。
   - 要做成批次 job：可取消、有進度、可續跑；遵守欄位鎖與人工優先。
2. **匯出全部**：
   - 寫一套「資料庫欄位 → NFO」的轉換，電影、劇集、季、集各一套。
   - 既有 NFO 裡的未知標籤、屬性與註解要保留（參考 `internal/adapter/nfo` 現有的受控編輯）。
   - 透過既有的 preparation → nfo_write job → 工作程序流程寫回，不另外開寫檔路徑。
3. **只匯出缺失**：
   - 目標檔不存在時要**新建**。目前整套寫回流程（plan、native receipt、結算）都假設目標檔已存在，需要設計一套安全的新建流程：
     - 以 `O_EXCL` 建立。
     - 有持久證據。
     - 失敗或當機後可以安全清理，或續作。
     - 不得覆蓋並發出現的同名檔。
4. **配額**：
   - 每個 actor 目前最多 32 筆 preparation，批次匯出需要分批，或為批次任務設計專用額度。
   - 單一 job 最多 100 筆 entry，大量匯出要拆成多個 job。

### 驗收
- 每種操作在真 PG 上跑 100 筆以上，並驗證取消與續跑。
- 匯出後再導入，往返內容一致；未知標籤要保留。
- 「只匯出缺失」不得覆蓋已存在或並發出現的檔案。
- 用子程序中斷，驗證新建流程可安全續作。
