# NFO 寫回的清理與容量回收（schema86）

接續 [結算、收尾與 worker](nfo-commit-settlement.md)。schema59 讓每個 token 在收尾後仍保留全部準備物件，attempt reservation 也只增不減：全域 256 筆／1 GiB 用完後，所有新寫回都會被拒絕。本批加上清理協議：收尾後的 token 由清理程序刪除自己保留的物件，記錄結果後釋放 reservation。對應 [交給擁有者的開發任務](owner-dev-tasks.md) 任務一，以及 G39.8 的備份保留份數。

## 資料庫

### 清理租約 `nfo_write_commit_cleanup_leases`

每個 token 一列，不可刪除。規則（`guard_nfo_commit_cleanup_lease`）：

- 只有已收尾（`nfo_write_commit_resolutions` 已有該 job）的 token 可以取得。收尾後原租約與恢復租約都不再有效（`nfo_commit_live_lease` 回 NULL），所以清理期間不會有寫回碰這個 token 的檔案。
- 已有清理紀錄的 token 不能再取得或續約。
- 新增時 epoch 必須是 1；同 epoch 只能由仍有效的持有者續約，首次時間不變；epoch+1 只能在租約過期後接手。期限最長 1 小時。
- 與恢復租約相同，不需要 actor 仍有效：清理只刪除 token 自己的物件，不授予其他檔案權限。

### 清理紀錄 `nfo_write_commit_cleanups`

每個 token 一列，寫入後不可改刪（`guard_nfo_commit_cleanup`）：

| outcome | 意義 |
| ---: | --- |
| 1 removed | token 保留的物件已全部刪除，或本來就不存在 |
| 2 retained | 有物件的身分與持久紀錄不符或無紀錄，已保留並計入 `retained`（需人工處理） |
| 3 parent gone | 計畫中的父目錄已不存在，或路徑上已是另一個目錄；沒有刪除任何東西 |

- 寫入時必須持有有效的清理租約（owner 與 epoch 相符），並鎖住租約列，避免與接手交錯。
- 外鍵指向清理租約，所以沒有取得過租約的 token 不能有紀錄。
- 寫入前觸發 `fence_nfo_commit_attempt_quota`，與新增 reservation 使用同一個 quota fence：容量檢查不會混用釋放前後的快照。

### 容量回收

`guard_nfo_commit_attempt` 計算全域容量時，排除已有清理紀錄的 token。reservation 本身仍是不可改刪的歷史，不刪除任何列。三種 outcome 都會釋放：outcome 2 留下的是身分不符的物件，不屬於 Jelee；outcome 3 的物件已不在原路徑。

## 檔案系統（`Writer.CleanNFOWriteCommitFiles`）

1. 驗證輸入：token、計畫、檔名與相對路徑一致、各 attempt 的身分格式，以及 output／rollback 不等於原目標。
2. 媒體庫根目錄不存在時回錯誤，下一輪再試：可能只是磁碟尚未掛載，不能因此釋放容量。
3. 路徑上任一層不存在或不是目錄，或父目錄身分與計畫不同時，回 outcome 3，不刪除任何東西。
4. 持有與寫回相同的 NFO 檔案鎖，再核對一次父目錄身分。
5. 對每個已分配的 attempt 命名空間，逐一檢查五個 commit 名稱：

   | 名稱 | 必須相同的持久身分 |
   | --- | --- |
   | `-original-pin` | 計畫的原目標 |
   | `-output`、`-output-pin` | checkpoint／ready 的 output |
   | `-rollback`、`-rollback-pin` | checkpoint／ready 的 rollback |

   - 名稱不存在：視為已刪除，所以每一步都可以冪等續作。
   - 不是一般檔案（目錄、符號連結等）、身分不同，或該身分從未記錄：保留並計數。
   - 身分相同：刪除。刪除的只是一個名稱；目標檔或 `.jelee.bak` 仍連到同一個物件時，內容不受影響。
   - 讀不到身分（觀察不清楚）：回錯誤，不寫紀錄，下一輪再試。
6. 被擠出的舊備份 `-backup-evicted`：備份輪替只會把「先前某次結算連結成 `.jelee.bak` 的原文」往後推，所以只有身分等於「同一父目錄、同一檔名、已有 phase 1 結算的其他計畫的原目標」時才刪除。舊版直接寫入器產生的備份沒有這種紀錄，因此保留並計數。
7. 有刪除時同步父目錄。

不碰：目標檔、保留份數內的 `.jelee.bak` 系列、鎖檔，以及其他任何名稱。

## 排程

`jobs.Runner` 的 nfo_write 恢復迴圈每輪結束後，呼叫 `app.NFOWriteCleaner.CleanPending`（預設每輪 8 個 token，`NFOWriteOptions.CleanupBatch`）。清理租約依恢復租約的 TTL，每三分之一續約一次；續約失敗就中止，不寫紀錄。Repository 與 Committer 都實作清理介面時才啟用（正式的 `postgres.Store` 與 `nfo.Writer` 都有）。

## 降級

086 down 在有任何清理租約或紀錄時拒絕，停在 85 dirty。空資料時刪除兩表與觸發器，並以 057 的原文還原 `guard_nfo_commit_attempt`。

## 驗證（Linux 真 PG 16）

- 檔案系統（`internal/adapter/nfo/cleanup_commit_files_test.go`）：已替換、未改名、已回滾三種 token 都刪光，目標與備份不變，重跑沒有變化；被替換的 pin、記錄成其他物件的身分、無紀錄的身分、同名目錄都保留並計數；被擠出的備份只在有先前原目標紀錄時刪除；父目錄被刪或被換時回 outcome 3、不動搬走的目錄；根目錄不存在時回錯誤；非法輸入不碰檔案。
- 子程序 `os.Exit`：在四次刪除的每一次之前與之後中斷（共 8 種），中斷時只少了 token 自己的名稱，續作後結果相同。
- 反向驗證：把身分比對改成恆真後，`KeepsObjectsWithoutMatchingIdentity` 三個子測試與 `EvictedBackupNeedsEarlierOriginal/false` 失敗；還原後通過。
- 真 PG（`internal/adapter/postgres/nfo_commit_cleanups_test.go`）：真 runner 寫入兩筆後自動清理，殘留物件全刪、reservation 全部釋放；累計 256 筆後第 257 筆被拒，清理後同一筆被接受；8 個並發清理只有 1 個取得租約，外人不能續約，直接 SQL 不能接手、改刪；過期後由下一個 epoch 接手，舊持有者讀取與寫紀錄都被拒；紀錄不一致的 outcome 被拒、同持有者重放成功、改寫被拒；收尾後不能再寫結算；migration 空資料往返、有資料拒絕。
- app：跳過他人持有的 token、失敗的 token 不寫紀錄且不影響其他 token、租約遺失時中止。

## 還沒做

- outcome 2 保留的物件只記數量，沒有列出名稱的 API；目前要由管理員依 token 名稱在目錄中確認。
- Windows 的目錄落盤仍是 stub（與寫回相同）；Windows 真 PG 尚未跑。
