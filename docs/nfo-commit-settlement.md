# NFO 寫回的結算、收尾與 worker（schema59）

接續 [恢復租約](nfo-commit-recovery-lease.md)。Stage 只準備檔案；本批把「改名替換目標、備份、回滾」接成持久的結算協議，並加上 nfo_write worker 與恢復迴圈，讓 NFO 寫回可以端到端執行。對應需求 G39.8（原子寫入、寫前備份、失敗回滾）、G13.3（可取消、可恢復、可觀測）。

## 結算紀錄 `nfo_write_commit_settlements`

每個 token 只追加、不改不刪，主鍵 (token, phase)：

| phase | 意義 | 寫入時機 |
| ---: | --- | --- |
| 1 backed_up | 備份已輪替並落盤，**之後才可能 Rename** | 目標 Rename 前 |
| 2 replaced | 目標已是準備好的 output，目錄已落盤（終態） | Rename 並核對後 |
| 3 rolled_back | 目標保有原文：從未被這個 token 改名，或已用 rollback 物件還原（終態） | 回滾或放棄時 |

規則（BEFORE 觸發器 `guard_nfo_commit_settlement`）：

- 只有 ready 已存在才可寫：attempt 0 要有 legacy ready 且沒有 attempt ready；attempt n 要有該 attempt 的 ready。同一 token 所有 phase 的 attempt 必須相同。
- 只能往前：2 需要已有 1 且沒有 3；1 在終態後拒絕；3 在 2 之後拒絕。重放同一 phase 是 no-op，首次時間不變。
- 租約：與 plan／ready 相同，經 `nfo_commit_live_lease`（原租約或恢復租約）；deferred 觸發器在 commit 時再查一次。actor 必須仍是有效管理員。
- catalog：phase 1、2 走 `guard_nfo_commit_catalog`（含實體 claim 檢查）；phase 3 **不**檢查 catalog。理由：回滾只把準備好的原文放回去，catalog 已漂移（例如 item revision 變了）時更需要能回滾；若回滾也被 catalog 擋住，目標會停在 output 而永遠無法收尾。
- 取消：job 已停止且 `cancel_requested` 時，只能寫 phase 3。設計理由：取消必須成立，不能完成被取消的寫入；phase 1 會開放 Rename，phase 2 會確認替換，所以兩者都拒絕。若取消前 phase 2 已經 commit，該 token 已完成，取消不會把它倒回去。
- `jobs.files`／`jobs.skipped` 由結算列推導（已替換數／已回滾數），重放不會重算。

「沒有 phase 1 ⇒ 目標從未被這個 token 改名」是整個協議的不變式：Writer 一定先 commit phase 1，失敗就撤回備份輪替、不 Rename。

## 收尾 `nfo_write_commit_resolutions`

每個 job 一列，不可改刪。插入條件：

- 由原租約持有者（epoch 0，job running、owner 相符、租約未過期）或恢復租約持有者（epoch=恢復 epoch，job 已停止、恢復租約有效）寫入。
- job 內沒有任何 token 停在 phase 1（有 1 但沒有 2／3）。

收尾後：

- `nfo_commit_live_lease` 一律回 NULL：原租約與恢復租約都不能再寫任何 evidence 或結算。
- 觸發器刪除該 job 所有 token 的實體 claims（`guard_nfo_native_claim` 改為只允許刪除已收尾 job 的 claim），同一 NFO／媒體之後可以再寫。
- 不需要 actor 仍有效：收尾不授予任何檔案權限，避免 actor 停用後 job 永遠卡住。

有準備檔但沒有 ready、或有 ready 但沒有 phase 1 的 token 可以直接收尾：依不變式，目標未被改名，留下的準備檔屬於之後的清理協議。

## job 結束出口（`retain_nfo_write_commit_job`）

有 journal 的 job：

- running → failed／cancelled（清 owner、lease）：照舊永遠允許。停止後由恢復處理。
- → succeeded：只允許 running → succeeded，或 failed → succeeded（未取消），而且必須已有收尾，且**每個 entry** 都有 phase 2 的 token。
- 其他狀態轉換、換 owner／generation、重排、刪除：照舊拒絕。

Store 方法：

- `FinishNFOWriteJob(lease, state, code)`：只用原租約。succeeded 要求全部替換（即使取消在最後一筆替換 commit 之後才到，也回報成功，因為寫入已在取消生效前完成）。沒有 token 停在 phase 1 就在同一交易收尾，回傳 resolved=true；否則只停止，交給恢復。停成 cancelled 時同時設 `cancel_requested`，讓 SQL 保證恢復只回滾。
- `CompleteNFOWriteCommitRecovery(recovery lease)`：寫收尾；job 是 failed、未取消、且每個 entry 都已替換時改成 succeeded，否則保持停止狀態。同一持有者重放回傳目前狀態。
- 注意：failed → succeeded 會讓 job 指標同時計入一次 failed 與一次 succeeded（指標觸發器在每次狀態變化時計數）。

## Writer

`Writer.SettleCommitFiles(ctx, source, lease, record, repo)`：

1. 讀 job-owned intent、legacy plan／ready、結算紀錄（含選定 ready；這幾個讀取都不經 catalog 守衛，回滾因此不受 catalog 漂移影響）。
2. 終態直接回報（replaced → nil；rolled_back → `ErrRolledBack`），不碰檔案。
3. 觀察目標實體：
   - 仍是原文物件：以 `rebuildPrepared` 重建並比對，再做 Stage 相同的 `boundCommitCheck`（native scope、root、每個路徑元件、parent、目標 bytes）。
   - 已是 output 或 rollback 物件：必須已有 phase 1，source bytes 必須等於 replacement／original，native scope 以目前物件身分重核。
   - 其他物件：`ErrChanged`。
4. 呼叫 `settleNFOCommitFiles`，progress 在 BackedUp／Replaced 寫 phase 1／2；primitive 回 RolledBack 時寫 phase 3。
5. 租約屬於已取消 job 時改走 abort。

`Writer.AbortCommitFiles`：沒有 ready → 回 phase 0，不寫；有 ready 但沒有 phase 1 → 直接寫 phase 3、不碰檔案；有 phase 1 → 持鎖觀察目標：原文就寫 phase 3、output 就用 rollback 物件還原再寫 phase 3、已還原就補目錄落盤；外來物件拒絕（`ErrChanged`，需人工）。abort 不做任何前進 Rename，也不還原已輪替的備份（最新備份此時是未變動原文的 hardlink）。

`Writer.CommitNFOWriteEntry`：重新 `ReadSource`，未進入結算才跑 Stage，接著 Settle。無法靠重試修好的錯誤（目標或 scope 變了、文件不可用、attempt 用完、已回滾、catalog 衝突）包成 `domain.ErrNFOWriteRejected`。

## Worker（`app.NFOWriteWorker`）

- `Run(lease)`：依序處理 entry。每筆 `BeginNFOWriteCommit` → `CommitNFOWriteEntry`。被拒的 entry 立刻 abort 收尾後繼續下一筆（一個檔案被使用者改過，不該擋住整批），結束時回 `ErrNFOWritePartial`，job 以 `nfo_write_failed` 失敗。其他錯誤（資料庫、租約、未知提交結果、回滾失敗）立即停止，由恢復接手。
- `Recover(jobID)`：取得恢復租約，每三分之一 TTL 續約，續約失敗就中止。只處理既有 token：
  - 已取消（state 或 flag）：每個未終結 token 只 abort。
  - 否則：`CommitNFOWriteEntry` 續作（含未完成的 Stage）；資料庫、租約、context 錯誤保留給下一輪；其他錯誤改 abort 收尾。
  - 最後 `CompleteNFOWriteCommitRecovery`。
- `RecoverPending(limit)`：列出已停止、有 journal、未收尾、且沒有有效恢復租約的 job，逐一 Recover。

## 執行期接線與設定

- `jobs.Runner` 的 `NFOWriteOptions`：claim 時多帶 `ScanCapabilities.NFOWrite`；`execute` 分派 nfo_write 到 `Run`；`finishJob` 走 `FinishNFOWriteJob`；另起一個服務生命週期的恢復迴圈（預設每 5 秒、每輪 4 個 job），不受工作時段限制，因為它負責安全收尾。
- 心跳與取消沿用 runner 的 monitor：取消時工作 context 被取消，job 停成 cancelled，必要的回滾由恢復迴圈完成。
- 服務關閉或時段結束時，nfo_write 不走通用的 `ReleaseJob`／`PauseJob`（仍回 `ErrInvalid`），租約到期後由 claim sweep 處理：有 journal 停成 failed（`job_timeout`）交給恢復；沒有 journal 照一般規則放回佇列或在次數用完時失敗（行為變更：以前會一直停在 running）。
- 設定：`enableNFOWrite`／`JELEE_ENABLE_NFO_WRITE`，預設 false，需要 `enableJobs`，連線數至少 workers+3。
- 送件：`Store.SubmitNFOWriteJob`（同一 actor 的未過期 preparations，1–100 筆，冪等 key）。尚未接 HTTP／CLI。

## 降級

059 down 在有任何結算、收尾或 journal 時拒絕，停在 58 dirty（與 057 同範圍）。空資料時刪除兩表與新函式，並以原文還原 049 的 `retain_nfo_write_commit_job`、054 的 `guard_nfo_native_claim`、057 的 `nfo_commit_live_lease`。

## 驗證（Linux 真 PG、race）

- 結算儲存：只能前進／首次不可變／不可改刪、租約過期／取消／actor 停用／非管理員／外來 owner／catalog 漂移（回滾仍可）／直接 SQL、已取消恢復只回滾、job 結束出口與收尾、恢復完成三種結果、migration 空資料往返與有資料拒絕。
- Writer（nfo 套件假 repository＋真檔案）：成功替換與備份輪替、Rename 後目錄落盤失敗自動回滾、三種中斷點續作、恢復租約續作、取消只回滾、外來目標拒絕與 abort。
- 真 PG＋子程序 `os.Exit`：在 phase 1 commit 後、Rename 後 phase 2 commit 前、phase 2 commit 後退出；以新 Store 走恢復租約（三種）或同一租約（兩種）續作，target 為 replacement、最新備份為原文、job succeeded、已收尾且 claims 釋放。
- worker：runner 端到端兩筆、單筆被拒其餘照寫、取消在 Rename 後生效時回滾、worker 當機後新 runner 的 claim sweep＋恢復迴圈接手。

回歸：`^(TestNFOCommit.*|TestNFOWrite.*|TestStage.*|TestJob.*|TestJobs.*|.*Migrat.*|TestSettle.*|TestCommitNFOWrite.*)$` 於 postgres／nfo／app 套件，`-p 1 -parallel 2`，排除寫入 GB 級資料的配額測試（`TestNFOCommitAttemptGlobalQuotaSnapshots`、`TestNFOCommitAttemptMigrationRefusesHistoricalExcess`、`TestNFOWriteNativeReceipt*ExactBytes*`、`TestNFOQuota*`）：828 PASS；唯一失敗 `TestNFOWriteNativeReceiptPlanSQLBinding/deferred/*` 在基底 eaa0b32473 同樣失敗（catalog v53 觸發器先拒絕），與本批無關。

## 還沒做

- 清理協議：結算後保留的 pin／rollback／output-pin 檔與 `-backup-evicted` 尚未移除；attempt reservation 全域 256 筆／1GiB 只增不減，累計 256 個 token 後所有新寫回都會被拒。這是大量匯出（G39.14）前必須先做的。
- 外來目標（phase 1 之後目標被換成不明物件）只能人工處理；actor 停用後恢復租約取不到（057 已知缺口）。
- 恢復只續作既有 token；當機前未開始的 entry 不會被執行，job 最終保持 failed，需要重新送件。
- Windows 目錄落盤仍是 stub；Windows 真 PG、完整分片回歸與大批量長測尚未跑。
- G39.14 三種批次操作見 [requirements-traceability](requirements-traceability.md) G39.14 列與 handoff。
