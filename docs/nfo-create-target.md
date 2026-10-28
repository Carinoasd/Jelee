# NFO 缺失檔案的建立（schema90）

接續 [結算、收尾與 worker](nfo-commit-settlement.md) 與 [清理與容量回收](nfo-commit-cleanup.md)。既有寫回流程（plan、native receipt、結算）全部假設目標檔已存在。本批讓一份準備可以指向**不存在的 NFO 路徑**：提交時用準備好的位元組建立它，不覆蓋任何存在或同時出現在該路徑的物件，任何時間點被殺掉都能安全續作，只留下 token 自己的名稱給既有的清理程序。對應 [交給擁有者的開發任務](owner-dev-tasks.md) 任務三第 3 項的新建協議（3c-i）。批次「只匯出缺失」任務（3c-ii）不在本批：本批只提供逐項的 HTTP／CLI 入口，讓整個協議可達、可測。

## 請求與命名

`POST /api/v1/items/{id}/nfo/write-preparations` 與 `jelee-cli nfo write prepare` 新增 `create: true`（`--create`）。仍限管理員，`revision` 照舊必填，之後走同一組 `POST …/nfo/writes` 與 nfo_write 任務。與既有的元素層級 `createMissing`（在既有 NFO 內新增元素）不同；兩者不能同時使用。

- **檔名**：不另訂規則，直接用讀取端的路徑解析（`domain.AdjacentNFOPath`、`domain.DirectoryNFOPath`，也就是 `ResolveItemNFO` 的 `Source.RelativePath`）：電影、家庭影片、單集為 `<媒體主檔名>.nfo`（與媒體同目錄），影集為影集目錄內的 `tvshow.nfo`，季為季目錄內的 `season.nfo`。不探測大小寫變體（讀取端也只查這個確切名稱）。
- **准入**：目標名稱在父目錄下**任何形式**存在（檔案、空檔、目錄、符號連結、懸空連結）都是 409 `conflict`，不是 create；媒體檔（或影集／季目錄）必須存在。
- **文件內容**：由空根建立，根依種類為 `movie`（電影、家庭影片）、`tvshow`、`season`、`episodedetails`（單集），先是 `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` 宣告，再以同一個受控編輯器加入請求的文字欄位（每個欄位視為「缺少就新增」）並補上 `uniqueid type="jelee"`；BOM 只在 `bom=include` 時寫入，縮排預設兩個空白，`indent` 可改。上限與配額不變（`maxBytes`、32MiB、256 列等），原文為空，不計備份：`backups` 必須是 0（HTTP 與 CLI 預設即 0，其他值為 400）。
- 回應摘要不變：`originalBytes` 為 0，`originalSha256` 為空內容的雜湊。

## 四個不變式與落實位置

| # | 不變式 | 落實 |
| --- | --- | --- |
| 1 | 目標名稱只由 `link(output, target)` 建立；沒有 rename、沒有 O_TRUNC、目標上沒有 O_CREAT | `settleNFOCreateFiles` 是唯一呼叫 `ops.link` 的地方（Linux `linkat`，Windows 見下），名稱存在時 link 失敗；目標名稱的其他操作只有 abort／回滾的 `Remove`（見 3） |
| 2 | 沒有 phase 1 ⇒ 這個 token 從未 link 過目標 | `settleNFOCreateFiles` 先呼叫 progress(BackedUp)（資料庫 phase 1 提交成功）才 link；目標已是輸出物件但沒有 phase 1 時回 `ErrChanged`，不補寫 |
| 3 | 目標物件的原生身分不是 token 記錄的輸出身分，這個 token 在任何階段（含 abort、cleanup）都不修改、不移除 | link 只在名稱不存在時成功；`rollbackNFOCreate` 與 abort 在 native 鎖下要求身分等於輸出身分**且**位元組等於替換內容才 `Remove`；cleanup 只刪身分與持久紀錄相符的 token 名稱 |
| 4 | 同一（父目錄身分，檔名）的兩個未解決 create token 不能共存（資料庫強制）；未解決期間不能開始改寫剛建立的檔案 | `nfo_absent` claim 與輸出身分 claim，見下 |

## 資料庫（遷移 000090）

- **旗標**：`nfo_write_preparations` 與 `nfo_write_entries` 新增 `create_target boolean NOT NULL DEFAULT false`。`original_bytes` 的長度檢查改為 `CASE WHEN create_target THEN 長度=0 ELSE 長度 BETWEEN 1 AND max_bytes END`，改寫列因此維持原本的嚴格範圍。CHECK 另要求旗標等於請求 JSON 的 `create` 鍵（`false` 時省略該鍵，所以改寫請求的編碼與雜湊完全不變）也等於收據標頭第 4 個位元組。
- **收據**：標頭的保留位元組 3 為 1 表示「目標名稱不存在」，此時第三筆（NFO）48 位元組記錄必須全為 0，不得出現目標身分；最後一個祖先仍是父目錄身分。`nfo_write_native_receipt_valid` 改為接受這個形狀（`CREATE OR REPLACE`，CHECK 約束依 OID 指向同一函式），改寫收據仍須有合法的 NFO 記錄。
- **計畫**：`nfo_write_commit_file_plans` 的版本檢查改為 `(version=1 AND target_identity IS NOT NULL) OR (version=2 AND target_identity IS NULL)`；`guard_nfo_commit_plan_native` 另要求 create 條目的計畫必為版本 2 且父目錄等於收據最後一個祖先，改寫條目必為版本 1 並等於收據的 NFO 身分。
- **ready／checkpoint**：`rollback_identity` 對 ready 與 attempt ready 可為 NULL，但只有 create 計畫可以（`guard_nfo_commit_files`、`guard_nfo_commit_attempt` 依計畫版本檢查；create 只有 checkpoint phase 1，且 phase 2 與帶 rollback 的 ready 一律拒絕，改寫的 ready 不得缺 rollback）。create 的 attempt ready 以 `complete_phase=1` 指向它的 phase 1 checkpoint（觸發器填入，CHECK 要求 `complete_phase=1` 等價於 rollback 為 NULL）。
- **預留**：attempt reservation 的 `original_bytes` 下限改為 0；公式不變（`4×(3×原文+2×替換)`，create 即 `8×替換`），觸發器仍要求它等於意圖的實際大小，而意圖只有 create 列的原文才為空。
- **claims**：`nfo_write_native_claims` 新增種類 `nfo_absent`。
  - 鍵為 `nfo_absent_target_key(父目錄身分, 檔名)`：`SHA-384('jelee/nfo-absent-target/v1' ‖ 0x00 ‖ 48 位元組父目錄身分 ‖ 檔名 UTF-8)`，**第一個位元組強制為 255**。合法的原生身分第一個位元組是 1，所以鍵不可能等於任何實體身分（CHECK `(kind='nfo_absent') = (get_byte(identity,0)=255)` 雙向強制），資料庫內由收據與條目計算，使用者輸入進不去。
  - journal 開啟時（`record_nfo_native_claims`）create token 取得 `media`（原本的媒體身分）與 `nfo_absent` 兩份 claim，同一交易、相同的主鍵（身分）唯一約束 `nfo_native_target_unresolved`，所以另一個未解決 token 持有同一名稱時第二個 `BeginNFOWriteCommit` 以 `ErrConflict`（23505）失敗，整筆回滾。
  - ready 提交時（`claim_nfo_create_output`，ready 與 attempt ready 的 AFTER INSERT 觸發器）再取得種類 `nfo` 的 claim，身分是 ready 的輸出身分：連結後它就是目標，改寫剛建立檔案的 token 會在自己的 `nfo` claim 撞上主鍵而無法開始，直到這個 token 收尾（`release_nfo_native_claims`，不變）。create token 因此持有 2 份（ready 前）或 3 份（ready 後）claim，`verify_nfo_native_claims` 與 `check_nfo_create_claim_scope` 依此核對。
- **catalog 守衛**：沿用包裝模式——`check_nfo_commit_catalog` 改名為 `check_nfo_commit_catalog_v87`，新函式對改寫條目原樣呼叫它，對 create 條目依序檢查範圍與 root generation（`_v52`）、收據形狀（目標不存在）、計畫（版本 2、無目標身分、父目錄相符）、claim（`check_nfo_create_claim_scope`）與寫回權限。phase 3 照舊不走 catalog 守衛。
- 已發布的 178 份 SQL 保持原樣；`checksums.txt` 新增 000090 兩列。

## 檔案系統

準備（`WritePreparer`）：沿用改寫相同的三階段（IO、CPU、IO）與祖先鏈核對（root、各層祖先、媒體），但 NFO 名稱以 `Lstat` 在已持有的父目錄 handle 下觀察，必須是「不存在」；首次與最後一次觀察的收據（父目錄與「目標不存在」）必須相等。

階段（`stageCreateFiles` → `prepareNFOCreateFiles`）：

1. 重核 root／父目錄身分與目標不存在（`boundCreateCheck` 呼叫收據核對，含目標不存在）。
2. 目標若已存在，在寫入任何計畫或檔案前回 `ErrTargetAppeared`。
3. 持久化版本 2 計畫（任何檔案效果之前，鎖旁檔也在其後）。
4. 持 native 鎖（鎖檔名由目標檔名導出，不需要目標存在）後再核對父目錄與目標不存在。
5. 以 `O_EXCL` 建立 `-output`（權限 0644 經 umask）、分 32KiB 寫入並 fsync，hardlink `-output-pin`；沒有原文 pin、沒有回滾副本與回滾 pin（cleanup 本來就把不存在的名稱視為已刪除）。
6. 核對輸出位元組與身分、同步目錄，持久化 checkpoint phase 1，再持久化 ready（只含輸出身分）。ready 提交的同一交易取得輸出身分 claim。

結算（`settleCreateFiles` → `settleNFOCreateFiles`）：

- 持 native 鎖觀察目標：
  - **不存在**：核對輸出與 pin、儲存 phase 1，**然後**才 `link(-output, 目標)`。成功後核對目標身分等於輸出身分且位元組等於替換內容、同步目錄，儲存 phase 2。
  - **link 回 EEXIST**（名稱在 phase 1 與 link 之間出現）：不碰該物件，不信錯誤碼——重新觀察，非本 token 的輸出就回 `ErrTargetAppeared`；writer 寫 phase 3 並以 `domain.ErrNFOWriteRejected`（原因 `nfo_target_appeared`，與 `ErrChanged` 分開）拒絕條目。
  - **已是輸出身分**（此前的 link）：要求已有 phase 1，驗位元組、同步目錄，補 phase 2。
  - **其他物件**：phase 3、拒絕條目，物件不動。
- link 後若目錄同步或核對失敗，`rollbackNFOCreate` 只在身分與位元組都相符時移除目標名稱（`ErrRolledBack`）；被改過或被換掉的物件原樣保留（`ErrChanged`，與改寫的人工處置同一條路）。
- **abort**（取消）：沒有 ready 不寫、沒有 phase 1 直接寫 phase 3 不碰檔案；有 phase 1 時持鎖觀察——不存在就寫 phase 3、身分與位元組皆相符就移除目標名稱、同步目錄後寫 phase 3、其他（原地改過或被換掉）回 `ErrChanged` 並保留。
- **恢復**（租約過期或當機）：`CommitNFOWriteEntry` 依持久狀態續作——階段從未完成就重做階段（有 checkpoint 就續用同一 attempt，名稱存在但沒有 checkpoint 就配置下一個 attempt 命名空間，既有名稱不收養）；結算依觀察到的目標決定如上。所以最後只有兩種狀態：目標以準備好的位元組只建立一次，或根本沒有目標，從未碰過外來物件。

清理：`CleanNFOWriteCommitFiles` 結果不變。create 的計畫版本為 2、目標身分為零，所以 `-original-pin` 與回滾名稱從不被視為 token 擁有（若有物件則保留並計數）；`-output` 與 `-output-pin` 身分相符才刪除。**目標與 `-output` 是同一個 inode，cleanup 只 unlink token 的名稱，目標名稱讓該物件繼續存活。**

### Windows

`os.Root.Link` 在 Windows 以 `NtSetInformationFile(FileLinkInformation)` 實作且 `ReplaceIfExists` 為 FALSE（語意與 `CreateHardLinkW` 相同：目標名稱存在時失敗，NT 狀態 `STATUS_OBJECT_NAME_COLLISION` 預期對應 `ERROR_ALREADY_EXISTS`），所以同樣不會覆蓋。程式不依賴錯誤碼：任何 link 錯誤後都重新觀察目標，名稱上不是本 token 輸出的物件一律當作外來物件。這一節來自閱讀 Go 標準庫原始碼：此環境無法執行真正的 Windows，只有 `GOOS=windows go vet`（含測試檔）通過；Windows 目錄落盤仍是既有的 stub。

## 排程

沒有新的排程或設定。create 條目走既有的 `nfo_write` 任務：`POST …/nfo/writes` 准入（庫須 read-only 並開啟寫回權限，否則 409 `nfo_write_disabled`；每庫同時一個活動任務；一批可混合 create 與改寫條目），`enableNFOWrite` 的 runner 依序處理條目，被拒絕的條目（目標出現、範圍改變）立刻以 phase 0 或 phase 3 收尾，其餘繼續，任務最後以 `nfo_write_failed` 失敗；服務關閉或租約到期後由 claim sweep 與恢復迴圈（預設每 5 秒、每輪 4 個任務，不受工作時段限制）續作或回滾，收尾後清理程序（每輪 8 個 token）依持久身分刪除 token 的名稱並釋放預留容量。取消只會移除 token 自己尚未改動的輸出，其餘情況交人工，見上。

## 降級

000090 down 在仍有 create 準備或條目、版本 2 計畫、無 rollback 的 ready 或 attempt ready、`original_bytes=0` 的預留或 `nfo_absent` claim 時拒絕（停在 89 dirty，資料保留）；空資料時依序還原九個函式的原文（`nfo_write_native_receipt_valid`、`guard_nfo_commit_plan_native`、`guard_nfo_commit_files`、`guard_nfo_ready_checkpoint`、`guard_nfo_commit_attempt`、`verify_nfo_commit_attempt`、`record_nfo_native_claims`、`guard_nfo_native_claim`、`verify_nfo_native_claims`）、刪除新增函式與觸發器、`check_nfo_commit_catalog_v87` 改名回 `check_nfo_commit_catalog`，並還原所有約束與 NOT NULL，往返後的 schema dump 與 89 逐行相同。

## 驗證

Linux 真 PG 16、race：

- 端到端：runner 建立四種種類（電影、影集 `tvshow.nfo`、季 `season.nfo`、單集 `episodedetails`）的檔案、位元組等於準備、沒有 `.jelee.bak`、job succeeded、claims 釋放、cleanup 之後只剩目標；HTTP 端到端含 201／重播 200、`backups=3` 為 400、已存在為 409，讀回建立的 NFO 並套用到條目。
- 目標出現在準備與階段之間：階段拒絕，沒有計畫、沒有 token 檔；出現在 phase 1 之後：phase 3、條目被拒、外來檔位元組與 inode 不變；link 呼叫內才出現（含懸空符號連結與目錄）：package 層以注入的 link 驗證，外來物件不變。
- 兩個 create 對同一目標（不同媒體）：第二個 `BeginNFOWriteCommit` 為 `ErrConflict`，原始 SQL 重現確切為 23505／`nfo_native_target_unresolved`，沒有留下 journal；收尾後名稱可再取得。改寫剛建立的檔案：未解決期間被同一約束拒絕，收尾後成功並備份建立的檔案。
- 取消：link 之後 phase 2 之前取消，回收移除目標、phase 1→3、job cancelled；原地改過或被換掉的目標保留，回收以 `ErrChanged` 停在 phase 1 未收尾。
- 子程序 `os.Exit`：package 層在 plan、輸出建立（寫入前）、輸出 fsync、pin、目錄同步、checkpoint、ready、phase 1、link、link 後目錄同步、phase 2 十一個點各殺一次，PG 層在 plan、checkpoint 前後、ready、phase 1、link、phase 2 殺 7 個點；每點分別用無干擾與「死亡期間外來檔出現／取代目標」續作，另有同租約續作。結果只有「建立恰一次」或「不存在」，外來物件位元組不變，之後 cleanup 只留目標（未記錄身分的孤兒名稱保留並計數，不猜測）。
- cleanup 保留建立的目標（package 層與 PG 層）；非 token 擁有的名稱保留並計數。
- 遷移：往返（含往返後再 create）、有資料拒絕降級且不寫入；schema 契約測試證明改寫列維持嚴格（空原文、create 旗標對改寫列都被拒），create 列不能用改寫形狀，ready／checkpoint／claim 的偽造被拒，`nfo_absent` 鍵與實體身分不相交。
- **反向驗證**：把原生 link 暫時換成 rename 覆蓋，「link 呼叫內才出現」與其餘 link 測試立即失敗（外來檔被覆蓋或被回滾移除）；還原後通過。

## 還沒做

- 批次「只匯出缺失」任務（3c-ii）：把資料庫欄位轉成新建請求、分批、取消與續跑。
- 準備階段不檢查未解決的實體 claim（與改寫相同）：「未解決期間不能開始改寫剛建立的檔案」落在 journal 開啟（`BeginNFOWriteCommit`）而不是準備，準備只讀。
- 大小寫不敏感的檔案系統：`nfo_absent` 鍵用確切檔名；`Movie.nfo` 與 `movie.nfo` 在 Windows 是同一個名稱，兩個 token 可各自計畫，但第二個 link 會因名稱存在而被拒絕（`ErrTargetAppeared`），不會覆蓋。
- 移除目標名稱（abort 與回滾）在核對與 `Remove` 之間仍有 TOCTOU 窗口：native 鎖只約束合作的 Jelee 程序，檔案系統沒有「身分相同才 unlink」的原語；窗口內被外來程序換掉的物件會被移除。核對失敗時一律保留。
- 外來目標（phase 1 之後被換成不明物件）的取消回收仍只能人工處理，與改寫相同。
- Windows 真 PG、完整分片回歸與 Windows 目錄落盤。
