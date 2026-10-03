# NFO 未解決提交的持久紀錄

schema49 新增 `nfo_write_commit_journal`，先保留提交嘗試與恢復所需的完整意圖。這是未解決紀錄；目前沒有正式檔案提交、結算或自動恢復入口。worker 尚未接入，read-write 政策與 capability 仍未啟用。

## 記錄與交易邊界

每筆紀錄綁定工作、sequence、首次記錄的 owner／generation、固定 UUID token，以及資料庫觀察的時間與租約期限。每個工作條目／generation 唯一；同一個有效租約重試或重開連線會讀回同一筆紀錄。JSON 與文字格式化不輸出私有資料。

私有 `recordNFOWriteCommit` 只在呼叫者的短工作交易內記錄，不回傳已提交的授權。呼叫者必須先成功 commit 才能使用持久紀錄；它仍不授予檔案寫入權限。SQL 同時核真實 nfo_write 種類、running 狀態、owner／generation、未過期且未取消租約、活躍管理員、條目外鍵及工作嘗試上限。deferred trigger 在提交交易時再次核租約、取消及管理員；失敗整笔回滾。

SQL 插入 journal 時也更新工作列的 MVCC 版本。只取得工作讀寫鎖不足以刷新 Repeatable Read／Serializable 的舊快照：舊交易可能看不到新紀錄而轉移租約。這個同步更新讓舊工作變更回報40001。資料表與函式按 trigger 所屬 schema 明確定位，採 invoker 權限及固定 pg_catalog search_path。

紀錄不可改動或刪除，每個工作條目最多10筆，並由工作意圖最多1024條的配額限制。沒有額外保存 XML 副本：外鍵指向完整且不可變的工作意圖。準備資料TTL清理不會刪除這些意圖。

## 未解決資料的保留

存在 journal 的工作不能刪除、改種類／library／actor／generation、換owner、重排或宣告成功。running 可以停止為 failed／cancelled 並清空owner與租約，但紀錄及原文／輸出仍保留；停止不證明檔案的最終狀態。取消旗標與同owner的心跳仍可記錄。

history trim 在套用保留筆數前排除這些工作，普通工作的保留筆數保持原規則。直接刪工作、意圖或其request，亦由SQL trigger與外鍵拒絕；不能先刪journal解除保護。通用Release／Pause依資料庫真實kind拒絕nfo_write，即使傳入kind偽裝成掃描，或尚未記錄journal。

乾淨的歷史schema仍可供migration夾具驗證旧契約；當前schema或dirty狀態若缺journal表，trim報錯並回滾，不退回忽略journal的清理。runtime仍須通過當前版本Ready檢查。

49→48在有任何未解決紀錄時拒絕，留下dirty目標48，Ready拒絕；空journal才可移除其表與守衛。001–048共96份已發布SQL保持。

## 恢復仍需完成的接點

目前沒有解除保留或接受「已提交」的捷徑。正式worker啟用前仍須保存原生staging／恢復檔案實體、跨重啟的root／父目錄／媒體／NFO證明，接入native檔案鎖與租約提交邊界，並由可驗證的檔案結果新增結算紀錄。不能以輸出bytes相等推斷本工作已提交，不能只憑這筆journal直接Rename，也不能讓租約接管者與尚未join的旧writer同時修改檔案。

這些接點完成後，結算才能解除對歷史清理與後續寫回的保留；新寫回准入亦須檢查同一實體的未解決提交。公開API／CLI、取消／恢復worker與共用CPU／IO配額仍待完整接線。

## 驗證狀態

初次journal選測已通過。另新增舊快照測試，直接SQL插入journal後，重現Repeatable Read與Serializable仍能轉移owner；同步更新工作版本後，全部journal真PG race選測通過，包含三隔離層級、16並行重試、reopen／TTL清理後固定token與bytes、租約／actor／取消拒絕、末端expiry／disable／cancel整筆回滾、不可變內容、普通history trim及未解決資料保留、空降升與拒降版。

完整PostgreSQL race回歸以四個平衡分片執行同一個已編譯套件，424個root各執行並通過一次，合計1133個通過事件，零跳過／失敗；四個程序皆退出0。946份Go／SQL／模組來源在執行期間保持凍結。Windows八個相關套件1557個通過事件／705個條件跳過，vet、Linux三套件race及runtime真PG metrics亦通過。格式、增量品牌0違規／339白名單、gitignore與diff檢查通過；完整品牌門禁仍保留。

初次完整PG回歸因舊inventory夾具在deferred trigger未執行前ALTER ENABLE而失敗（55006）。僅在夾具UPDATE與ENABLE之間執行SET CONSTRAINTS ALL IMMEDIATE，保留原legacy／baseline／缺失斷言；最終完整回歸已包含修正案例，96份已發布SQL未改。初次失敗與最終完整分片結果分別記錄。

Windows第一次重驗的圖片失敗診斷夾具曾間歇失敗；診斷overlay證實本地HTTP請求可短於時鐘tick，開始與結束讀值都為0，統計及503仍保留。未改凍結來源重跑八套件通過；等待時鐘前進的夾具修復已用overlay連跑100次通過，將另行提交，不把這項修復歸入本批凍結來源。

詳見[安全證據](evidence/nfo-write-commit-journal.json)。每條目10筆journal容量邊界尚未獨立驗證；原生提交／結算／恢復、正式寫回准入與worker仍未完成。正式24h仍屬來源9a74a8932a，未涵蓋本批，全G00–G51未完成。
