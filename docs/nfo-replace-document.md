# NFO 目錄內替換與備份基礎

私有`replaceNFODocument`在已持有、已授權的父目錄`os.Root`內替換既存NFO。它接收原文與修改後Document、保留備份份數0至16。完整XML重新驗證後取得持續旁檔鎖，核對普通檔案、完整原bytes、前後大小/時間及身分。相同內容不替換inode或輪替備份。這個基礎尚未提供正式read-write入口；庫策略、項目/root/父目錄身分、安全觀察與欄位授權仍須由正式writer接入。

新內容、回滾原文及啟用備份時的備份原文，均以隨機名加EXCL在同目錄建立、分段寫入、file.Sync後關閉。替換前重新核對原文、暫存bytes/身分與鎖旁檔。原檔由Root.Rename替換，同目錄POSIX替換使用原子rename；POSIX再同步目錄。backup名稱為`movie.nfo.jelee.bak`，較舊版本為`.1`、`.2`依序，最多16份；調低非零保留份數時刪除本介面支援的多餘舊份。0停用新備份輪替，不刪除既存備份。非普通backup及符號連結拒絕。备份轮替若中途失败，原NFO保持，但备份集合可能已经轮替一部分。

替換前失敗或取消，原文保持，釋放鎖並清理仍屬本次inode的暫存。替換後的目錄同步失敗會核對新NFO仍為本次完整內容與身分，再由已同步的原文暫存回滾、同步目錄。取消不跳過這段提交/回滾。若外部編輯器已修改新內容，拒絕覆蓋其修改；回滾失敗傳固定`nfo_rollback_failed`，保留原文恢復暫存供後續處理。成功回滾傳`nfo_replace_failed`。外部不遵守同一鎖協議的修改仍有觀察與rename之間的競態，本介面不是檔案系統快照；正式工作流程仍須處理提交結果與恢復稽核。

Windows資料檔同樣Sync後Rename，已驗證100次並行與跨程序修改沒有XML損壞。Windows沒有透過os.File.Sync提供POSIX目錄fsync，本介面未證明不同Windows檔案系統的rename原子性、目錄metadata斷電耐久性、完整ACL/owner保留；G39.8保持部分完成。

Windows nfo/architecture及vet通過，[Linux race](evidence/nfo-write-document-race-linux.txt)通過。實測範圍：未知XML/屬性/註解/CRLF保留，備份3份輪替與調低至1份、no-op身分保持、原文改變拒絕、取消與臨時檔清理；file sync、backup rename/目錄sync、target rename/目錄sync故障注入，回滾失敗保留原文恢復bytes；還原size/mtime的手動修改、回滾前外部修改均拒絕覆蓋。POSIX另驗符號連結與鎖旁檔替換拒絕。100個goroutine及4個子程序各25次的實際讀→修改title→替換→再讀，最後計數100且未知XML/註解保持。

G39.8/9/15仍未完成。後續[原文綁定writer](nfo-bound-writer.md)已接Source實體證明、受控修改來源與singleflight；正式持久read-write jobs、庫策略/項目/租約generation與独立鎖授權、缺失NFO建立/ID生成、完整欄位及真實上游客戶端互操作仍缺。上述100次僅是目錄內替換基礎，不能代替完整正式工作流程驗收；G41混合負載亦須接入真正NFO工作。
