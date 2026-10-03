# NFO 提交前檔案證據的持久化

schema50 將[提交前檔案計畫](nfo-commit-files.md)接到 schema49 未解決 journal。新增 Store 的短交易與內部 `Writer.StageCommitFiles`，保存計畫及 output／rollback 原生身分；runtime 尚無呼叫者，原 target 保持。正式寫回准入、提交與恢復仍待完成。

## 儲存與租約

`nfo_write_commit_file_plans` 以 journal 的固定 token 為主鍵及外鍵，保存版本1、target basename、parent／target 的48-byte原生觀察。`nfo_write_commit_files_ready` 以同 token 綁計畫，保存不同於原 target、彼此不同的 output／rollback 身分。兩表不可修改或刪除；相同資料重試取回第一次紀錄，衝突返回錯誤。身分 codec 的形狀檢查不證明檔案所有權。

每次保存核真實 nfo_write 工作、running state、owner／generation、有效租約、取消狀態與活躍管理員；basename 必須符合工作 entry。鎖定工作與 actor，更新工作 MVCC 版本，並在交易提交時再次核租約及權限。重試透過 no-op UPDATE 同樣觸發 deferred 檢查；歷史 recorded_at／lease_until 保持第一次值，不當成現在的租約授權。

`BeginNFOWriteCommit` 先提交固定 journal；plan 保存成功才建立 native lock sidecar、stage 或 hardlink。任一 plan／ready 保留列拒絕50→49降版，維持 dirty 狀態並使 Ready 拒絕。空表可降升，原指標 epoch 保持；001–049 已發布 SQL 不變。

## 原生檔案接線

`StageCommitFiles` 依 lease 與 journal sequence 從 repository 取得工作所屬完整意圖，重建受控修改及已保存 UUID，再與持有 Source 比較。呼叫者不能提供另一工作的 preparation 代替。XML 重建與解析使用共用 CPU 配額；檔案階段持共用 I/O 配額，使用既有 native 鎖、完整 bytes／實體複核、EXCL stage、副本 Sync 與 hardlink witness。

plan／ready port 都是提交後才回傳的短資料庫交易，不跨完整檔案階段持有資料庫交易。ready 嘗試開始即保留五個準備名稱，錯誤或取消可能代表保存結果未知；不能據此刪掉證據。較早故障沿既有契約只清除仍可確認為本次自有實體的物件。

`VerifyCommitFiles` 是只讀觀察，從保存資料重新核對 held parent、原 target、五個準備名稱與完整原文／輸出。stage 缺失或 target 等於輸出仍返回變更，不能據此認定本工作已提交。

## 驗證與限制

真 Linux PostgreSQL 夾具從工作意圖經 StageCommitFiles 保存實際檔案證據；清除準備 TTL 資料後，另一子程序從資料庫重讀並開啟 owned 目錄，以原生身分與完整 bytes 核對所有準備檔。另驗相同 basename 的其他來源拒絕、並行／重開 pool、首次及重試交易的到期／停用／取消／回滾、不可變／降版保留、SQL／Go codec 一致性。

完整 PostgreSQL race 的434根測試分四個獨立程序，各根執行及通過恰一次，零跳過／失敗。Windows 八套件1,586通過事件／743條件跳過；Linux六套件race1,169通過事件／1個Windows專屬跳過。Windows實際原生＋PG選測在連線既有資料庫時失敗，未到檔案夾具，不計通過。Darwin只交叉編譯，沒有原生執行。初次 PG 選測的衝突夾具誤使 output 等於 rollback，已修正測試資料並重驗，原失敗證據保留。詳見[本批安全證據](evidence/nfo-commit-files-persistence.json)。

仍缺跨重啟完整 root／媒體／catalog revision／policy generation 授權、同實體未解決工作排除、target Rename／backup／rollback／結算及可驗證恢復。後續[去重與讀取准入](nfo-stage-admission.md)已接 singleflight 及 GetTask 前的 I/O 配額，整體 payload bytes 配額仍需補齊；已存在的 stage 重試會碰撞並保留，沒有重開續作入口。不能啟用正式 read-write 或 worker。Windows directory sync 沿既有 stub，斷電 metadata 耐久性未證明。正式24h是24caf凍結來源，不包含本批。
