# NFO 寫回的持久準備資料

schema46新增`nfo_write_preparations`。application服務`NFOWritePreparations.Prepare`將受控文字修改與缺ID生成的完整輸出保存到PostgreSQL，供後續正式寫回任務使用。目前是內部application入口；HTTP、CLI、runtime與持久寫回worker尚未接入。庫政策仍只接受off/read-only；準備資料不授予檔案修改權限。

## 準備與重播

請求包含itemId、預期metadata revision、十種已支援文字欄位的有序修改、缺欄位新增選項、BOM/縮排、原文/輸出上限及備份份數。拒絕重複欄位、ID欄位修改、非法選項與超量請求；不接受XML或路徑。完整請求以SHA256綁定冪等鍵，順序與選項也納入雜湊。

repository每次準備及重播都重新驗證活躍session與管理員。首次準備從資料庫解析item/source/root關係，預設選相鄰同名NFO；影集/季沿用固定資料夾NFO。WritePreparer以共用I/O讀取、CPU解析/受控修改/缺ID生成/嚴格驗證、I/O再次觀察三階段執行。total1不巢狀；保留原文BOM/未知XML/註解/排版與手工ID，缺ID以UUID v4生成。檔案或root/父目錄換實體、bytes或stamp改變拒絕；不建立鎖旁檔、暫存或備份，不寫原NFO。

檔案操作完成後，短資料庫交易再次核對metadata revision、庫policy generation、source/root及全部解析範圍。固定ID保存在完整replacement bytes內。交易同時保存準備資料與`nfo.write_prepared`稽核，失敗全回滾；稽核只含itemId、revision及generation，不含XML、文字值或路徑。沒有資料庫交易跨越檔案讀取。

同actor/key與相同完整請求取回第一次保存的原文/完整輸出/ID，包括並行首次準備時選出的唯一結果。不同請求回Conflict。重播是歷史準備資料：即使檔案之後消失或庫策略改變，仍可查看先前結果；這不表示可以執行。後續worker必须另核read-write策略、租約/generation、catalog/media/檔案實體與原文，不能把準備時的權限視為永久授權。

## 保存界限與 migration

- 每份原文及輸出最多32MiB；請求編碼最多32MiB。驗證先計算JSON跳脫後長度，再配置完整編碼；超量文字不先配置大型JSON。以實際三份bytes總數計費。
- 全域最多256列、256MiB；每個actor最多32列；每個庫最多128MiB。
- 保存24小時，createdAt/expiry均有限；新準備最多清除256列過期資料，與全域列界限相同。重播不延長期限，不再次稽核。
- SQL trigger在INSERT取得同schema固定advisory鎖並檢查容量；不依賴caller search_path。完整SHA256與大小、请求/項目/版本綁定、跨庫item外鍵均由SQL約束檢查。列內容不可更新。
- 46→45只允許空準備表；保留任何準備資料即拒降版並保持資料，golang-migrate留下dirty目標版本，runtime拒絕。45之前已發布的90份SQL保持。

`NFOWritePreparation`的JSON為空物件，String/GoString遮蔽全部私有資料；application與repository結果各自擁有bytes與請求副本。[受控Writer入口](nfo-prepared-writer.md)可以依保存請求及固定UUID重建來源證明，並逐位元比對完整輸出。目前沒有可執行jobs引用這些列。未來提交worker時必須將固定完整意圖保存到工作生命週期中，不能讓24小時準備期限清掉正在執行或待恢復的輸出。

## 驗證範圍

Windows完整Go套件與最終受影響套件驗證通過；images/toolidentity的owned fixture ACL先受sandbox拒絕，僅這兩個套件在允許原生ACL操作後重驗通過。Linux domain/app/nfo/architecture race及受影響套件vet通過。

真PostgreSQL完整race回歸最初有四個舊夾具失敗：三個HTTP入口漏填預設Resources，通用降版測試漏列46→45。修正夾具後，四個入口與全部新準備測試重新通過；證據由完整回歸及最終重驗組成。包含並行冪等、重新開pool後無來源檔仍重播固定UUID/完整輸出、來源範圍及session變更拒絕、稽核失敗回滾、SQL不可變/容量/雜湊/到期限制與保留資料降版拒絕。已發布的90份SQL均核對保持。

runtime真PostgreSQL metrics整合通過；production NFO worker驗收因未配置nonroot production profile而未執行，不計為通過。詳見[安全摘要](evidence/nfo-write-preparations.json)及[Linux race](evidence/nfo-write-preparations-race-linux.txt)。正式24h的來源9a74a8932a不含本批變更。

新schema為正式持久寫回的準備步驟，G39與G41狀態保持部分完成。缺失NFO建立、全部欄位、read-write政策/批次job/租約與能力准入/API/CLI/worker/提交恢復、Windows完整落盤與真實客戶端仍未完成。
