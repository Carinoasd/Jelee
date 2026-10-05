# NFO 文字修改與 XML 保留

Document.WithText(ctx, entry, field, value, maxBytes)從保留原文重新解析，選取source-order條目，修改既有標量文字並返回新Document。公開Metadata/Entries視圖的變動不能改寫原文或繞過原文lockdata/lockedfields；Overview與Name/SortName等鎖別名照對應欄位保護。

目前欄位為title、originaltitle、sorttitle、plot、outline、tagline、showtitle、status、mpaa、certification。title及sorttitle的既有別名保留原本tag名。未知根/只有直接欄位的wrapper、重複/別名衝突以及被修改欄位含巢狀元素皆明確拒絕，避免刪除原有擴展內容。條目的其他未知子樹與屬性保持；使用lexical spans保留既有順序、註解、引號及縮排，只替換文字token。被修改文字中的CDATA可改為escaped text，內部註解保留。

WithText預設拒絕缺欄位。WithTextOptions的TextEditOptions.CreateMissing=true可在指定條目尾部新增文字欄位，依原本行尾與第一個子元素縮排插入；無可推斷的縮排時使用相對兩空格。自閉合根展開時保留根名與屬性。Indent可指定最多八個空格或tab作為新增多行欄位的相對縮排；它不重排既有欄位。新增同樣受原文lockdata/lockedfields限制。

輸出採UTF-8，XML declaration只改encoding value。BOM選項零值或preserve保留原文有無BOM策略：原文有UTF-8或UTF-16 BOM時輸出UTF-8 BOM；无BOM時輸出無BOM。include強制有BOM，omit強制無BOM。UTF-16與GBK先經既有嚴格解碼；未知XML的字符與lexical格式保持，編碼位元組不保持。輸入原文、文字及escaped輸出受maxBytes限制（1至32MiB），上限包含輸出BOM；原文安全/複雜度門檻沿用。禁止XML非法字元，取消保持context錯誤。原文有semantic error也不改寫。本段不是修復器。

Windows nfo/architecture及vet通過；[Linux nfo/architecture race](evidence/nfo-edit-text-race-linux.txt)通過。測試核精確UTF-8原文差異、未知命名空間子樹與屬性、內外註解、CDATA、CRLF/tab、wrapper及多根episode的指定條目、自閉合與空欄位、UTF-16雙端序/GBK/BOM、別名與巢狀拒絕、原文鎖防繞過、取消及escaped輸出上限。另核新增欄位六種排版、八種BOM來源/策略組合、缺欄位鎖及非法排版選項。100個並行純Document修改驗證原文不變；它不能作為G39.9的100次跨程序檔案寫入證據。

G39.5–10/14/15保持未完成範圍：多值/嵌套結構/完整欄位、完整序列化style配置、ID生成及既有ID不覆蓋、安全來源複核、備份/fsync/原子rename/失敗回滾、flock/LockFileEx/singleflight、正式read-write策略與持久job、[兩種上游客戶端](nfo-compatibility.md)的真實互操作均需接續。本介面不寫檔案、不啟用read-write模式；完整混合race仍須真的NFO寫入。
