# NFO 部分落檔的中斷恢復

G13.3 要求任務可恢復；G39.8 要求原子替換、寫前備份與失敗自動回滾。現有 StageCommitFiles 只有 plan 與完整 ready 的持久邊界，部分落檔仍沒有自動續作入口。本文件記錄可重現的缺口及下一步必須滿足的恢復條件；正式 read-write 與 worker 尚未啟用。

## 實際程序中斷回歸

`internal/adapter/nfo/commit_files_abrupt_test.go` 由父程序建立自有目錄、原文與替換文件，再啟動同一測試執行檔的子程序。子程序在選定同步點直接 os.Exit，繞過 defer 與記憶體清理；父程序重開目錄並核對原生身分與完整內容。callback 將 plan 同步保存至自有檔案，此夾具不使用 PostgreSQL。

| 中斷位置 | 已留下的五個準備名稱數 | 重開後目前行為 |
| --- | ---: | --- |
| plan 已同步，尚未建立檔案 | 0 | 同一 plan 可完成準備並保存 ready |
| output 已 Sync，尚未建立 output pin | 2 | ErrReplace，保留證據，沒有 ready |
| rollback 已 Sync，尚未建立 rollback pin | 4 | ErrReplace，保留證據，沒有 ready |
| directory sync callback 已返回，尚未保存 ready | 5 | ErrReplace，保留證據，沒有 ready |

三個部分落檔案例的 target 原文不變；重試不移除或替換保留物件，也不以「檔名存在」推定 ready。此回歸通過只證明上述現有邊界，部分落檔仍未自動恢復。Windows directory sync 目前是 stub，Windows 該列只驗證 callback 後的程序中斷；兩平台均不構成硬體斷電耐久性證據。

## 持久證據缺口

現有 plan 保存 journal token、parent／target 身分及 basename；output／rollback 的身分在完整 ready 才保存。EXCL create 後觀察到的物件所有權只存在記憶體，因此程序中斷後不能以 basename、相同 bytes／mtime 或新的 filesystem 觀察代替第一次持久證據。schema54 的未結算 claims 保護 NFO／media，但不提供部分 stage 所有權或回收授權。

下一步恢復協議必須同時涵蓋：

1. 在檔案副作用前保存有界 attempt／名稱計畫，綁定原 journal、first receipt、工作與恢復租約；不得改寫原 owner／generation 或解除未結算 claims。
2. 保存每個已確認自有物件的首次原生觀察與階段；保存結果未知時先重讀持久結果，不因超時清除物件。
3. 對 create 與首次觀察保存之間的中斷保留未知物件。可驗證的後續 attempt 必須使用另外已保存的名稱，不能採用或覆寫未知物件；attempt 數與保留檔案須有容量及清理協議。
4. 重新取得有效恢復租約及完整 catalog／policy／root／media 授權，核對同一持久原文、輸出、parent 及每個物件。新 native ID 觀察不能單獨提供 filesystem 授權。
5. 補齊剩餘 stage、Sync 與 witness，經完整核對及目錄耐久性後保存 ready；任何衝突、替換或未知結果保持原證據。
6. 經真 PostgreSQL 與实际子程序中斷驗證各保存前後、EXCL create 前後、每次檔案／目錄 Sync、租約競爭、取消與未知交易結果。完整 target Rename／backup／rollback／結算與 crash recovery 另需相同範圍驗收。

這些條件尚未實作，不能把此安全保留回歸作為 G13.3、G39.8 或 G39.14 完成證據。schema54 完整1520PASS仍只屬其原凍結來源，不包含新增的本回歸。

## 已驗證範圍

正式中斷選測在Windows與Linux race各5PASS、零skipfail；完整NFO套件Windows383PASS／5個symlink條件跳過，Linux race401PASS／1個Windows專屬跳過，兩者package terminal pass。vet、格式、增量品牌0／339、gitignore及diff通過。安全摘要獨立核每個中斷案例各run／pass一次、全部日誌terminal及hash，見[本批證據](evidence/nfo-partial-stage-abrupt.json)。沒有修改108份已發布SQL或schema54來源，沒有重跑完整PostgreSQL宇宙。

## 已保存 witness pair 的內部續作原型

`prepareNFOCommitFiles` 新增私有 progress callback 與 resume evidence。原有呼叫者未提供 callback 時仍走既有 plan／ready 流程；正式 StageCommitFiles／PostgreSQL repository 尚未接入。checkpoint 只可保存完整 output＋output-pin（連同 original-pin），或完整五份準備物件；保存前核對原文、首次 parent／target／output／rollback 身分與 witness，並核 source callback、native lock 和 directory sync。保存嘗試前即保留證據，回應未知不刪檔。

重開只接受同一 plan 與首次持久身分。output pair checkpoint 須核原文及 output pair，且 rollback／rollback-pin 都尚不存在；只建立缺少的 rollback pair，保留 output 身分。完整 pair checkpoint 核全部物件後沿同一身分保存 ready。任何未知 rollback 物件、缺 witness、換實體、內容變更或不一致 plan 皆拒絕；拒絕前後原 target、五個準備名稱、物件身分與 bytes digest 保持。

實際子程序在 output pair 或完整 pair callback 將紀錄 Sync 後 os.Exit；父程序重讀紀錄及重開目錄，兩條路徑均可完成 ready。這個 callback 仍是自有檔案夾具，不是真 PostgreSQL 交易，也未證明換 owner／generation 的恢復租約。建立物件後、保存首次 checkpoint 前的中斷仍需有界 attempt 協議；目前不能採用未知物件。原始三個部分落檔中斷回歸保持原範圍。

本原型沒有 target Rename、backup、rollback、結算、claim 解除或正式 worker 呼叫者，不能宣稱 G13.3／G39.8／G39.14 完成。後續必須接入持久 checkpoint schema／ports、交易首次及重放守衛、恢復租約與完整授權，再驗真正 PG 子程序中斷及未知結果。

同來源選測Windows／Linux race各12PASS（包含無操作helper一PASS）、零skipfail；兩個實際中斷正例及七個拒絕leaf各執行通過一次。完整NFO套件Windows395PASS／5symlink條件skip、Linux race413PASS／1Windows專屬skip。私有overlay僅移除重開前唯讀核對，四個拒絕案例留下新物件，Go exit1／5 test FAIL events／四個精確retained artifact marker，正式來源保持。vet、格式、增量品牌0／339及gitignore通過；見[原型安全證據](evidence/nfo-commit-progress-primitive.json)。
