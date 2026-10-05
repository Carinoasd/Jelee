# NFO 寫回的恢復租約（schema57）

## 為什麼需要
schema49 起，job 一旦有 commit journal，就不能換 owner、不能換 generation，也不能重新排隊。它只能停下來（failed／cancelled）。所以原本的 worker 當機後，這個 job 已準備好的檔案和 attempt 永遠無法續作，也無法結算。

## 做法
新增 `nfo_write_commit_recovery_leases`，每個 job 一列：
- 只能對「已停止」的 nfo_write job 取得：state 為 failed 或 cancelled、owner 為 NULL、仍有 journal，且 actor 仍是有效管理員。
- 首次取得的 epoch 為 1。持有者可以在租約有效時續約（保留首次時間）；租約過期後別人可以接手，epoch 加 1。
- 租約期限最長 1 小時；不能刪除。
- 恢復租約只續作既有 token，不會開新 journal，也不改 job 本身。

`nfo_commit_live_lease(schema, job, journal_owner, journal_generation)` 回傳某個 journal 目前有效的租約到期時間：原本 running 的 job 租約，或同一個已停止 job 的有效恢復租約；都沒有就回傳 NULL。以下守衛都改用它判斷，取代寫死的「job running 且 owner 等於 journal owner」：
- `guard_nfo_commit_files`、`verify_nfo_commit_files_lease`（plan／ready／checkpoint）
- `guard_nfo_commit_attempt`（reservation／attempt／attempt checkpoint／attempt ready）

`check_nfo_native_claim_scope`（認定自己的未結算 claim）不看租約是否有效，維持 schema54 的語意：job 的 owner 等於 journal owner，或 job 已停止且有恢復租約列，就視為自己的 token。租約是否有效由上面的守衛判斷，所以錯誤訊息和以前一樣。

已取消的 job（`cancel_requested`）在恢復模式下不能新增任何 evidence：plan、reservation、attempt、checkpoint、ready 都由觸發器拒絕。取消必須成立；恢復只能讀取，之後再用結算或回滾收尾。

worker 當機時：`ClaimJob` 的過期回收會把「租約已過期、而且有 journal」的 nfo_write job 改成 failed（`job_timeout`），已要求取消的改成 cancelled，owner 清空。這樣就能取得恢復租約，不需要人工改狀態。

Go 端：`domain.JobLease.RecoveryEpoch` 大於 0 時表示恢復租約，Owner 是持有者。
- 所有 NFO commit 交易都經 `fencedNFOCommitLease` 鎖定。
- 恢復模式下不開新 journal，不比對 journal 的原 owner，也不因 job 已取消而拒絕（恢復本來就是要收尾已取消的工作）。
- `Store.AcquireNFOWriteCommitRecovery`／`RenewNFOWriteCommitRecovery` 負責取得與續約。

## 降級
057 down 在有恢復租約或任何 journal 時拒絕，拒絕後狀態停在 56 dirty。這比 056 down 嚴格，所以只要有 journal，就到不了 056 down 的「保留 attempt 容量」檢查。這是刻意的：兩者都保護同一批資料。

## 鎖序
寫入端一律先鎖 job（`FOR UPDATE`）再鎖恢復租約列（`FOR SHARE`）。續約也先鎖 job 再更新租約列，避免依賴全域 advisory lock 才不死鎖。

## 還沒做
- 原 actor 被停用或刪除後，恢復與回滾都會被 actor 檢查擋下，需要另外設計系統層級的回滾出口。

schema59 已補上（見 [結算與收尾](nfo-commit-settlement.md)）：worker 自動取得與續約恢復租約、恢復時沿用 Stage 的完整重核、rename／備份／rollback 的持久結算，以及收尾後 job 的最終狀態（failed 且每個 entry 都已替換時改為 succeeded）。
