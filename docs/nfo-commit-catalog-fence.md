# NFO 準備交易的 catalog scope 複核

固定的工作意圖不是持續有效的執行授權。Store 的 `BeginNFOWriteCommit`、`SaveNFOWriteCommitFilePlan` 與 `SaveNFOWriteCommitFilesReady` 現在重新解析當下 catalog scope，逐欄比較保存的 item／library／source／root ID、kind、revision、library policy generation、root path、媒體或目錄路徑與所選 NFO 路徑。

## 短交易邊界

沿既有 job／actor／租約守衛，從 job-owned entry 讀取固定 scope，再使用既有 `readItemNFOScope` 鎖定 item、library policy、source 與 root。現在仍是只讀策略下的內部檔案準備，不允許正式 NFO target 寫回。revision、generation 或任一 scope 欄位不同、來源缺失或歧義、policy 關閉都拒絕。

已有 metadata state 的 revision row 另加鎖；revision1 沒有 state row，已持有的 item FOR UPDATE 鎖透過 state 的 item 外鍵阻擋 concurrent insertion。保存 port 在 SQL 寫入及 no-op replay 後再複核 scope，鎖保持到交易結束。Begin 失敗整筆回滾新 journal；plan／ready 失敗不回部分資料、不建立新證據，既有不可變紀錄保持。

`GetNFOWriteCommitFiles` 仍可觀察 lease／actor 有效的歷史證據，供後續恢復稽核；它不因 catalog 改變刪除或隱藏固定意圖，也不能授權執行。Stage 首次準備在 plan 保存失敗時尚未建立 native lock 或 stage；ready 重開要再重放兩個保存 port，所以舊 catalog scope 不會被當成可繼續準備的權限。

## 驗證

真 PG 矩陣涵蓋 Begin、plan／ready 首次及重試，和首次原生 stage／ready重開：revision、generation、policy、root path、source path／ID、缺失／歧義來源及 kind 改變均拒絕。逐案核回傳零值與資料列數保持；原生階段核全部剩餘檔案前後完整 bytes 不變、無新 lock／stage 與配額不洩漏。原文及已保存 ready 證據保持。

真PG race兩次選測合計132個通過事件／零skipfail；主選測124、補充Series／Season目錄正例與保存後trigger變更8。最終編譯的14個TestNFOCommit根在組合中各run／pass一次，這是選測組合。Windows postgres／architecture226通過事件／811資料庫條件跳過，沒有Windows真PG結果；vet兩平台、Darwin NFO僅交叉編譯、格式／增量品牌0／339／gitignore通過。移除scope複核的隔離真PG overlay重現過期revision被保存。完整來源與範圍見[安全證據](evidence/nfo-commit-catalog-fence.json)。100份已發布 SQL、需求原文、模組與 LICENSE 保持；本批没有新 migration，不當完整 PG 回歸重新執行。

## 尚缺的提交證明

這是應用 port 的 scope 複核；schema49／50 的直接 SQL 守衛仍只核租約、actor、不可變資料與局部身分形狀，沒有新的 catalog scope SQL trigger。讀取 metadata 的邊界也不是跨檔案 Rename 的交易。

root generation 尚未獨立保存，原生 root／媒體／完整 ancestor 跨程序身分、同實體未解決排除、target 提交邊界、backup／rollback／結算及 crash 恢復仍缺。不能把路徑或 catalog ID 相等當成原生實體相等；不能只開 read-write mode 或 worker。Windows directory metadata 耐久性、部分stage處置與完整 heap／RSS驗收仍缺。正式24h來源24caf不包含本批。
