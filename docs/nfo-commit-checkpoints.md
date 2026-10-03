# NFO 檔案 checkpoint 的持久化（schema55）

schema55 將[已保存 witness pair 的內部續作](nfo-partial-stage-recovery.md)接到 PostgreSQL 與 StageCommitFiles。完整凍結 PG 回歸與獨立核驗已通過；已提交並推送807e494f5769448ee4832ffcb7f82dfa9a0f4972至既有 PR46，遠端HEAD吻合。原 target 保持，正式 read-write 與 worker 尚未啟用。

## 首次證據與固定容量

每個既有 journal／plan token 最多兩筆不可變 checkpoint：phase1 保存首次 output 身分，phase2 保存同一 output 與首次 rollback 身分。兩阶段以自參照外鍵綁定相同 first output；phase2 缺 phase1 或換 output 都拒絕。此表不重新觀察 filesystem、不改 journal owner／generation，也不解除 schema54 的 NFO／media claims。

Stage 在完整 output＋output-pin 或完整五份準備物件核對後保存 checkpoint。原文、已保存 parent／target 身分、output／rollback bytes 與 witness、Source、native lock 及 directory sync 都須通過；保存嘗試前 retain，未知交易結果保留檔案。每次為提交後才回傳的短 PG 交易，不跨檔案階段持有資料庫交易。

保存與 no-op 重放沿用 journal token／工作租約、running state、取消、活躍管理員及 catalog／root generation／first native receipt／claims 守衛，提交時再核。原 recorded_at／lease_until 保留首次值。catalog 反向守衛納入本交易 checkpoint 的 xmin；SET CONSTRAINTS 提早 flush 後再修改 catalog 仍須拒絕。

已有任一 checkpoint 時，ready 首次及重放都必須符合完整两階段證據。沒有 checkpoint 的既有 ready-only 內部呼叫保留首次完整觀察契約；它不提供部分落檔恢復，也不授予 filesystem 寫回權限。Stage 對支持新 port 的 Store 必須使用 checkpoint 路徑。

## 重開與未知物件

GetNFOWriteCommitFiles 只讀取最高已保存 phase，不創建 journal 或 checkpoint。Stage 重建同一工作意圖並重新取得 Source、配額與現有租約守衛後，核對同一 plan 與保存證據：

- phase1：original-pin 與 output pair 全部符合首次觀察，rollback／rollback-pin 均不存在才建立缺少的 rollback pair。
- phase2：完整五份物件符合首次觀察，沿相同身分完成 ready。
- 缺 witness、換實體、bytes 變更、未知後續物件或證據不一致時保留並拒絕，不能用相同內容／mtime／名稱猜測所有權。

create 到首次 checkpoint 保存之間的中斷仍缺有界 attempt／名稱、容量及清理協議；換 owner／generation 的恢復租約也未實作。它們不能以目前 phase1／phase2 正例替代。

## 升降與歷史

升級只從已持久 ready 複製原 output／rollback 及原 recorded_at／lease_until，形成兩筆 checkpoint。已停止 job 的歷史 ready 不被當成現在的授權；plan-only 歷史不補物件身分，不新讀 filesystem。保留 checkpoint 或未解決 journal 時拒絕55→54，dirty54保持，資料不刪除。108份已發布001–054 SQL不改。

初次候選使用 generated first_phase，BEFORE UPDATE 不可變守衛使 no-op 重放誤判；owned 原生SQL診斷命中23514／immutable evidence，已改成 DEFAULT1＋CHECK，保留原失敗日誌。後續一致性 trigger 先於不可變守衛導致錯誤原因不同，已調整順序。沒有弱化守衛。

Windows 真PG中斷初次在受控重建前 ErrChanged；私有overlay只輸出固定位置及布林標記，證明只有原 root 字串與 filepath.Clean 的分隔符表示不同，其餘stamp／bytes不變。重建沿ReadSource既有OS filepath.Clean規則比較，同一原生root／catalog核對保持；增加Windows正例及不同root拒絕回歸。原失敗與overlay日誌保持。

## 驗證與未完成範圍

- Linux V3真PG選測31PASS／零skipfail，V4聯合原claims／ready回歸74PASS／零skipfail；兩者為Windows root修正前的歷史範圍。
- 修正後Windows真PG七roots／28PASS／零skipfail，含兩個actual child os.Exit91／92，從DB重讀checkpoint並重新開啟目錄可達ready；清除準備TTL仍保持first output／rollback及首次timestamp，target原文不變。owned relay finally關閉，實核child0。
- 同来源Windows三套件984PASS／1059條件skip／零fail；Windows slash root專例1PASS；vet／格式／增量brand0／339／gitignore／diff通過。條件skip不是PG成功證據。
- 已凍結1008份Go／SQL、108舊SQL保持，實際compiled PG宇宙494roots／四分片。V5 Linux同來源聯合選測74PASS；完整四片1548PASS、零skipfail、四片exit0，494根各run/pass一次，coverage及獨立核驗通過。完整runner首次在freeze未terminal時因manifest缺失退出，尚未編譯／開測；已保留此啟動失敗，freeze成功後才啟動實際四片。

仍缺完整 target Rename／backup／rollback／結算／crash recovery、Windows目錄metadata斷電耐久性、正式worker三批次／missing-NFO absence協議、完整filesystem授權及整體heap／RSS。Windows directory sync仍為stub。原24h來源24caf不含本批，不重啟；全G00–G51的7完成／198部分／131阻塞保持。

修正後同1008來源的Linux V5聯合真PG74PASS／零skipfail已終態；Linux domain＋NFO race745PASS／1Windows專屬skip／兩package pass。完整PG四片已終態通過，見[完整回歸證據](evidence/nfo-checkpoints-schema-v1.json)。原獨立checkpoint保存source／selected logs／歷史failure hashes及當時未完成狀態，保持不改，見[候選證據](evidence/nfo-checkpoints-candidate-checkpoint.json)。完整回歸不代表全G00–G51驗收。
