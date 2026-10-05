# NFO 提交前的檔案計畫與保留證據

本頁保留 c7dda1d 批次的私有 callback 驗證範圍。後續 schema50 已加入真實 Store 持久化及內部 StageCommitFiles 接線，見[持久化方法與限制](nfo-commit-files-persistence.md)；下列歷史測試數字不代表後續來源。

新增私有 `prepareNFOCommitFiles` 與只讀 `verifyNFOCommitFiles`，準備跨程序可核對的輸出、回滾副本與 hardlink witness。這是既有目標的準備步驟；沒有正式 Writer／worker 呼叫，沒有新 migration 或資料庫儲存實作，也不授權 target Rename、結算或自動恢復。

## 副作用與保存順序

1. 驗證原文／輸出 XML，核對持有父目錄與目標的完整原文及原生身分。
2. 以外部已保存的固定16-byte非零token推導五個有界名稱：原文pin、output、output pin、rollback、rollback pin。版本1計畫包含target filename及parent／target原生身分。必須先呼叫plan persistence port，成功後才建立native lock sidecar、stage或hardlink。
3. 持native target鎖重核來源。以EXCL建立完整輸出與独立原文回滾副本，分32KiB寫入並file.Sync；分別建立hardlink pin，原NFO另有pin。既有碰撞回固定失敗，不覆寫或刪除碰撞檔。
4. 對全部五個名稱及原target核對完整bytes与原生身分；bytes觀察的FileInfo必須與接著讀取原生身分的handle相同，避免把兩個實體的觀察拼接。核native lock及directory sync，再呼叫ready persistence port保存完整準備證據。

所有步驟同步完成並釋放自有handle及native鎖；不修改原target、不輪替備份。兩個port必須由後續應用實作短資料庫交易並在commit後才返回；目前測試用私有callback，**未驗證真正資料庫持久化**。固定名稱先保存，也不表示其中任意既有物件屬於本工作。

ready保存一旦開始，所有五個名稱保持。回報錯誤可能是資料庫已提交但回應遺失；不能因此刪除witness。取消發生在ready成功後，也返回保留的準備證據與取消錯誤。較早故障只清除已觀察為本次建立且仍相同實體的stage／link；無法確認建立身分的名稱保持，不依名稱直接刪除。native lock sidecar沿既有契約不刪除。計畫仍需由後續恢復機制處理，不可略過未解決journal。

## 只讀核對與限制

重新開啟目錄後可用保存的版本、token、target及原生身分重新核對所有準備檔。移除output名稱或將它Rename到target後，準備核對會失敗；它不把「stage已消失」或「target bytes等於輸出」推論為本工作已提交。真正post-commit辨識與結算尚未實作。

原文pin維持原inode存活，但外部原地寫入會同時修改target與其pin，所以另保存獨立rollback copy及其pin。所有原生身分仍是[觀察證據](nfo-native-identity.md)，不能單憑ID／creation time解除未解決紀錄或授權寫入。

這裡只涵蓋已持有parent及既有target；完整root／媒體／catalog revision／policy generation／actor／lease核對、跨工作同實體排除、共享資源准入與持久SQL計畫仍缺。備份輪替、缺失NFO建立、正式Rename／rollback／結算／恢復另需接線。Windows沿既有directory sync stub，斷電metadata耐久性未證明，不能將lab準備當成Windows正式落盤驗收。

## 驗證

owned暫存夾具驗plan callback前只有原movie.nfo；成功後所有五個名稱保持，將準備紀錄落至私有測試檔，再啟子程序重開並核對。故障矩陣涵蓋plan失敗、建立前取消、output／rollback file sync失敗、directory sync失敗、ready未知結果與ready後取消；ready未知結果的檔案也由子程序重新核對。

另驗EXCL碰撞保持、plan後target換實體、ready後同bytes witness換實體、原文原地外部修改而rollback copy保持，以及bytes／原生觀察之間換檔拒絕。測試直接Rename owned夾具確認準備判定不猜提交；沒有production target提交。

最終Windows NFO／architecture327個通過事件，2個既有symlink條件跳過；Linux同兩套件race342個通過事件，1個Windows專屬案例跳過。新增準備案例两平台16個通過事件、零跳過／失敗；helper無環境時只返回，跨程序比較由父測試啟動。兩平台vet、格式、增量品牌0／339、gitignore、diff與Darwin amd64交叉編譯通過；沒有Darwin原生結果。98份已發布SQL與模組／需求原文／LICENSE保持，未重跑完整PG。見[安全證據與來源](evidence/nfo-commit-files.json)。正式24h仍屬24caf凍結來源，不包含本批。
