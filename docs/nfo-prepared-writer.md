# 持久準備資料的受控寫回

`Writer.ReplacePrepared`銜接schema46保存的原文、修改請求與完整輸出。呼叫者提供當下`ReadSource`觀察；Writer要求共用WorkBudget，核對scope路徑、讀取上限、完整stamp與原文bytes。

## 重建規則

在CPU階段重新解析當下原文，依保存的十種受控文字修改、CreateMissing、BOM與縮排選項重建修改來源證明。只接受單一條目的movie/tvshow/season/episode或episodedetails根。原文已有識別碼時保持原值；原文無ID時，從固定輸出讀取唯一jelee UUID v4，不指定default，不重新隨機生成。最後重建bytes必須與保存的完整輸出逐位元相同，再嚴格驗證原文與輸出XML。

保存的XML本身不建立修改來源證明。移除或修改未知標籤/註解、手工ID、改請求但沿用舊輸出、非預定的ID類型/default/UUID均在檔案操作前拒絕。

singleflight在完整文件複製與重建之前去重；等待者只在CPU配額內驗證請求/原文摘要與固定輸出雜湊，共用工作才保留重建文件。意圖key包含完整scope、保存及當下路徑、兩份stamp、請求摘要、輸出摘要及讀取上限，且依root/父目錄/file實體區分。NFOSource的JSON會隱藏路徑，因此key另明確編碼兩組私有路徑，再取雜湊，不把路徑寫入診斷。

重建後沿用原文綁定Writer的native鎖、source實體與全原文複核、暫存/備份/原子替換/回滾及清理。CPU與I/O階段不巢狀；呼叫者須先釋放先前工作permit。這個去重邊界不代表完整G41混合負載或heap驗收已完成。

## 正式任務接線仍需完成

這是adapter執行入口，尚未接runtime或授予read-write policy。正式worker必須從受信任repository取得固定意圖，重新核庫政策、catalog revision、generation、項目媒體身分、租約與提交授權；不能把準備資料或此方法當作授權證明。準備資料未保存跨重啟可用的root/父目錄/檔案實體識別，當下ReadSource觀察也不能代替媒體身分確認。

當下來源已經等於保存的輸出，但不等於保存的原文時，方法返回ErrChanged。不能只憑bytes相等推斷上一次工作已提交；正式job仍需提交紀錄與恢復判定。暫存同步失敗而原文保持時，可以重新觀察並消費相同保存的UUID/輸出。準備TTL不能清除執行或恢復中的job意圖。

缺失NFO建立、全部欄位、批次匯入/匯出、正式政策/任務/恢復/API/CLI、Windows完整落盤與真實客戶端互操作仍未完成。G39、G41與全案統計保持。

## 驗證

Windows nfo/postgres/architecture/jobs/runtime及vet通過；Windows PostgreSQL條件測試的跳過不計為真PG通過。Linux nfo/architecture/jobs完整race通過。真PostgreSQL的持久準備及新Writer整合選測race全部通過、無跳過；本批沒有migration變更。

測試涵蓋BOM/UTF16/多種root、手工ID與未知XML保持、固定UUID失敗重試、100個並行請求共用一次替換、外部相同大小/mtime修改保持、取消/配額拒絕與私有root差異不共用。真PostgreSQL另驗重開pool後保存資料到原生Writer的完整bytes、固定UUID與備份，及篡改未知XML在建立旁檔前拒絕。資料庫與檔案整合在私有測試夾具執行，未啟用正式read-write任務或證明提交恢復。

詳見[安全摘要](evidence/nfo-prepared-writer.json)與[Linux race](evidence/nfo-prepared-writer-race-linux.txt)。正式24h仍屬來源9a74a8932a，不含本批變更。
