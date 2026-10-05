# NFO 跨程序原生身分觀察

新增私有原生身分觀察與48 bytes版本化紀錄，作為後續持久提交／恢復的接點。它只讀取已開啟的檔案 handle，沒有路徑、XML、size或mtime，也不建立、刪除或替換檔案。目前 Writer、準備資料與提交 journal 尚未呼叫它，不能據此宣稱正式恢復已完成。

## 格式與平台

版本1明確區分平台及普通檔案／目錄。Windows保存64-bit volume serial、完整128-bit FileIdInfo及原生creation FILETIME ticks，避免轉成Unix奈秒時溢位。Linux以statx的AT_EMPTY_PATH讀已持有descriptor，保存device major／minor、inode、birth seconds／nanoseconds；必須實際回傳TYPE、INO及BTIME mask，缺少任何一項即拒絕，沒有退回只有inode的較弱證據。

解碼只接受固定48 bytes、已知版本／平台／kind、零保留位及有效奈秒範圍，並複製資料以免呼叫者改動。Windows ID高64位不截斷。格式化輸出遮蔽身分；nil、closed、pipe或不支援的平台回固定錯誤，不包含私有路徑。Darwin目前只能編譯，這個新功能明確拒絕；原Writer行為保持。

Linux需要核對回傳mask，見[statx官方手冊](https://man7.org/linux/man-pages/man2/statx.2.html)。Windows的完整識別欄位見[FILE_ID_INFO](https://learn.microsoft.com/en-us/windows/win32/api/winbase/ns-winbase-file_id_info)；[Microsoft亦說明檔案ID可被重用](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/ns-fileapi-by_handle_file_information)。所以這是觀察紀錄，仍需保留並核對owned witness、完整意圖、root／parent／媒體與租約，不能僅憑ID或creation time授權恢復，也不能用bytes相等認定工作已提交。

## 實際驗證

Windows與原生Linux的owned暫存夾具將48-byte紀錄落檔，再啟另一個測試程序讀取及核對。驗證普通檔案、root與parent的重開；Rename與hardlink仍指向同一實體；原名稱被相同內容／size／mtime新檔案取代時不相等，保留的hardlink仍相等。原parent及root搬走後，在相同名稱建立的新目錄也不相等。內容／mtime改動保持原生物件身分，表明它不能取代完整bytes驗證。

這些hardlink是測試自有夾具，原實體保持存活；沒有證明任意檔案系統的ID重用、斷電、惡意篡改creation time、production witness建立或自動恢復。

最終Windows NFO／architecture回歸311個通過事件，2個既有symlink條件跳過；Linux同兩套件race326個通過事件，1個Windows專屬案例跳過。新原生身分案例兩平台均通過，零跳過；helper根測試沒有環境時只返回，真正比較由父測試啟動的子程序完成。兩平台vet、格式、增量品牌0／339、gitignore及diff通過；Darwin amd64僅交叉編譯通過，沒有原生Darwin結果。98份已發布SQL、模組與需求原文保持。見[來源與安全證據](evidence/nfo-native-identity.json)。

## 下一個接點

需要在檔案副作用前保存有界stage／rollback／witness計畫，持久化完整原生觀察，並於target Rename前提交已驗證的stage及witness身分。接著才能由native鎖、lease與owner工作join、外部修改拒絕及可驗證結算接上schema49 journal。新寫回准入須排除同實體未解決提交；三種正式批次操作、API／CLI／worker、Windows完整落盤與其他G00–G51仍未完成。
