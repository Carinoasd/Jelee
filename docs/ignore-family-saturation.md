# 原生忽略服務的併發飽和與多輪重用

使用正式 prepareProductionFamilyIgnore、固定 helper 和兩個執行額度，沒有替换 evaluator、程序結果或時間。每輪先觀察兩個重規則子程序同時 Active=2，再提出 32 次額外請求；必須全部回傳 ErrBusy、沒有部分結果、沒有新增子程序，服務仍可用。

8 輪合計拒絕 256 次請求。每輪取消兩個正在執行的子程序，必須走取消分支、join、移除輸入暫存，服務仍保持可用。隨後以正常來源再次核對 include／exclude 的行號與結果。最後 Started=25（健康檢查 1、重規則 16、重用 8）、Peak=2、Cancelled=16、TimedOut=0、Active=0；Close 撤回能力並移除整個服務暫存目錄。

Windows 專項通過（0.402 秒），probe 標籤的全部服務情境通過（0.466 秒），runtime vet 與 diff 檢查通過。[Linux 完整原生 runtime 證據](evidence/ignore-family-saturation.json)為 9 項頂層 pass、零 fail／skip、18.263 秒、sourceUnchanged=true，包含真實登入／HTTP／PostgreSQL／正式 worker、活躍 child 停止及 HTTP 取消。正式 helper 的 2GiB 地址空間限制保持，不以 race 子程序替代此驗收。

本次只增加驗收測試。正式取消修復的完整 PostgreSQL race 252 項證據見[取消合同](ignore-family-http-cancel.md)，本段沒有修改該正式實作或資料庫來源。這是有界的短週期飽和驗收，尚未證明長時間穩定性、跨實例取消即時性或所有歷史忽略格式；G22 與全案仍未完成。
