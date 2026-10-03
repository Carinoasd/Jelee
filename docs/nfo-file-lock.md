# NFO 跨程序檔案鎖基礎

`lockNFOFile`在已持有的父目錄`os.Root`中取得固定旁檔的獨占鎖。支援的POSIX平台使用`flock(LOCK_EX|LOCK_NB)`；Windows使用`LockFileEx`的立即嘗試獨占模式。等待每25毫秒重新嘗試，context取消或逾時即停止，無額外goroutine。錯誤只傳固定原因碼或context錯誤，不包含路徑。其他平台拒絕取得鎖。

旁檔名為`.jelee-nfo-<檔名SHA-256>.lock`，Windows先對檔名轉小寫。檔案保持零長度，POSIX建立權限0600。取得前與取得後檢查普通零長度檔案、旁檔目前身分與開啟的檔案相同；POSIX使用NOFOLLOW/NONBLOCK，Windows拒絕reparse point。鎖旁檔不能在釋放時刪除：刪除再建立會讓既有等待者與新寫入者鎖住不同inode。NFO本身的原子替換不會更換鎖旁檔。

Windows測試及vet通過，[Linux race](evidence/nfo-file-lock-race-linux.txt)通過。測試包括同程序互斥與取消恢復、獨立目標、重複Close、100個並行磁碟計數臨界區、子程序等待逾時與釋放後成功、非法輸入、非空旁檔與目錄拒絕；Windows另核檔名大小寫別名，POSIX另核符號連結拒絕且目標不變。100次臨界區測試不是完整NFO並發寫入驗收。

G39.9仍部分完成。這個私有基礎尚未接入原子writer或正式read-write jobs；進程內singleflight必須以完整寫入意圖去重，不能合併不同內容的寫入。寫入仍須安全來源與父目錄複核、備份、fsync、原子替換與失敗回滾。跨程序100次完整NFO寫回、實際上游客戶端互操作尚未驗證；旁檔不保護不遵守同一鎖協議的外部編輯器，也不構成檔案系統快照。
