# NFO 寫回任務的持久資料契約

schema47新增`nfo_write`工作種類、`nfo_write_requests`及`nfo_write_entries`。這是正式批次寫回的儲存與觀察接點；公開准入、read-write政策與執行worker尚未接入。schema49另新增[未解決提交紀錄](nfo-write-commit-journal.md)，保留工作與完整意圖；檔案提交與恢復仍待接入。runtime仍不宣告或claim這個種類。

## 固定意圖的生命週期

每批1至100個條目，保存有序準備ID與priority的意圖摘要。內部`copyNFOWriteIntents`在既有工作建立交易中將schema46的完整受控請求、原文、完整輸出、UUID與scope/stamp複製到工作所屬資料中。只複製相同actor、library及generation、尚未到期的準備資料。正式准入仍須在同交易核對活躍管理員、最新項目/revision、policy/generation與來源關係；此複製函式不建立工作或授權。

準備ID為歷史識別，沒有指回準備表的外鍵；準備資料24小時到期清理後，工作意圖仍保留到工作生命週期結束。任務讀取結果不帶準備期限，不能重新套用準備TTL。payload保留schema46的大小、雜湊、請求與item/revision/maxBytes綁定檢查；不以catalog外鍵删除正在處理的固定原文。library/generation由工作請求外鍵綁定，條目順序唯一且每批item唯一。

request與entry內容不可更新。同交易提交時，deferred constraint trigger核工作種類、library與完整批次：必須恰有total筆、sequence連續1至total。缺request、部分批次、刪除單筆或將kind改成其他種類都拒絕。schema49已有未解決journal的工作不能刪除，history trim亦先排除這些工作；其完整意圖保留至後續可驗證的恢復結算。沒有journal的工作仍按原歷史生命週期連帶移除意圖。

意圖儲存全域最多1024條、512MiB，每工作最多128MiB，以請求/原文/輸出三份實際bytes計費。schema48在原同schema固定advisory鎖及容量檢查前加入固定列更新，防止Repeatable Read使用過期快照超量插入；交易失敗須整筆回滾。見[交易快照防護](nfo-write-quota-fences.md)。不依赖caller search_path，沒有無界過期事件表。

## 租約觀察與 worker 邊界

`GetNFOWriteTask`一次取回指定sequence的私有固定資料。讀取需當下running工作、相同owner/generation、未過期租約、未取消與仍活躍的管理員actor。保留users讀鎖至短交易結束，讀完再核租約，失敗不回部分bytes。它只觀察資料，沒有policy或檔案提交授權。

原ClaimJobWithCapabilities仍只接inventory_scan/catalog_import；過期租約回收也只處理這兩種。舊worker不能claim或回收nfo_write。通用FinishJob、Release與Pause拒絕真正的nfo_write，即使呼叫者把傳入Kind改成inventory_scan。專用寫回worker、資料庫與原生檔案的提交邊界、journal結算與恢復、媒體實體確認及read-write准入仍必須完成。

## 指標與降版

新增nfo_write的manual/background兩組累計及histogram，不重置既有epoch或其他kind。現在固定六組、六列累計與156列分桶。完整彙整讀取與exporter都要求六組；缺失/重複/未知組別仍拒絕，固定維度之外不增加labels。

47→46在任何nfo_write工作、意圖或新kind統計樣本保留時拒絕，留下migration dirty目標版本，runtime拒絕。空新kind才移除其零值組別；原有kind資料及epoch保持。001–046已發布SQL不變。

## 驗證狀態

Windows domain/app/postgres/nfo/telemetry/jobs/runtime/architecture八套件通過，1377通過事件、659條件跳過；vet通過。Linux telemetry/architecture/jobs完整race通過。真PG任務/準備資料/metrics/migration回歸及最終重驗合計115通過事件、零跳過/失敗；真PG正式runtime metrics HTTP另有1個通過事件、零跳過。

首次Windows exporter重驗發現舊四組位圖仍使用uint16；修正為六組與18個outcomes的uint32完整集合，保留缺失/重複/未知維度拒絕。首次PG回歸僅舊總列數斷言仍預期109；修正為163後重驗該案例與全部新任務案例。證據按初次完整選測及最終重驗合併，不當作完整postgres套件重跑。

schema47已驗準備列清理/重開pool後的工作bytes與固定UUID、工作意圖到原生Writer、租約/取消/actor拒絕且無部分輸出、舊worker不claim或回收、真實kind防偽通用Finish、不完整批次/不可變內容、每工作128MiB容量回滾、指標epoch保持、保留工作或清理歷史後累計值拒降版。schema47當時未獨立執行全域1024條/512MiB邊界；schema48追加驗證與快照漏洞修補另見[配額證據](nfo-write-quota-fences.md)。

詳見[安全證據](evidence/nfo-write-jobs.json)與[Linux race](evidence/nfo-write-jobs-race-linux.txt)。92份已發布SQL保持。測試使用私有SQL夾具建立完整工作，不代表公開read-write操作已啟用；正式24h仍屬9a74a8932a來源，不含本批。全案與G39/G41狀態保持部分完成。
