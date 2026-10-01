# 忽略掃描混合工作驗收

使用既有正式探測容器：Linux amd64、UID65532、唯讀根目錄及媒體掛載、移除 capabilities、no-new-privileges、2 CPUs／768MiB／128 PIDs。外部探測仍經正式受保護 helper，沒有使用未隔離的替代程序。

重現方式：在已配置專用 `jelee_test` 資料庫與固定媒體工具的 Linux Docker 環境，設定 `JELEE_NFO_IGNORE_ACCEPTANCE=true` 後執行 `python3 scripts/test_nfo_worker.py`。資料庫連線依既有驗收方式透過環境提供；不要將憑證写入命令或文件。

## 實際結果

兩組素材（1000與100筆）各另加一份 `.jeleeignore` 與三個被排除的錯誤 video／NFO／image。HTTP提交同時啟用ignore、NFO與probe，連續驗證冷快取、暖快取、控制內容替換三輪。

| 素材組 | 各輪NFO解析次數 | 各輪外部探測啟動次數 | 修改後圖片變更數 |
| --- | --- | --- | --- |
| 1000筆 | 400、0、17 | 100、0、0 | 23 |
| 100筆 | 40、0、3 | 10、0、0 | 4 |

每輪報告均列出三筆排除、正確根目錄規則第1行與匹配路徑；被排除的素材不進NFO/probe快取或基線。各輪結束沒有活躍NFO呼叫或子程序。

兩組均通過忽略掃描中的取消、恢復與SIGTERM關閉。取消與關閉在一次真實NFO讀取後設置測試屏障，以穩定重現尚未返回的呼叫；不把這個屏障當作阻塞OS I/O的模擬。舊基線保留，工作與子程序均完成清理。控制器確認原始素材雜湊不變，只有指定NFO與圖片副本被替換，測試schema、容器與映像均已清理。

另以正式worker和真PG驗證unknown：原生inventory後，刻意在observer介面回傳來源不可用。結果為待檢視、missing=0，舊基線逐欄不變，報告保留unknown與source_unavailable。Linux PG race 1項頂層測試通過、零跳過、17.608秒。

[完整執行證據](evidence/ignore-worker-acceptance.json)。Tagged runtime vet、Windows runtime／PG／architecture測試通過；Windows未配置PG，不能作為Windows整合證據。本階段僅修改驗收測試與控制器，未修改正式程式碼或遷移。

## 尚未完成

舊格式忽略規則的兼容實作與跨格式驗收仍待完成；G22及全案不能因此標記全部完成。完整媒體庫規模效能與網路檔案系統阻塞行為亦不由本次固定素材驗收證明。
