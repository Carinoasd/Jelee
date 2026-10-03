# NFO 有界 attempt 的內部準備流程

已提交並推送f2af24361c837efe765a201550ce99d7f338c159至既有PR46。

本段在schema55已保存witness pair續作之外，增加另一個預先保存的名稱空間，以便保留create到first checkpoint之間的未知物件。`prepareNFOCommitAttempt`仍是私有準備流程；正式Stage／PostgreSQL沒有attempt配置或呼叫者，不提供自動恢復租約及filesystem授權。

## 名稱、首次意圖及容量

同一journal token的新attempt序號只接受1至3；每次各有original-pin、output、output-pin、rollback、rollback-pin五個名稱。序號加入固定basename；舊schema55的序號0名稱保持不變。三個新attempt共15個名稱，連同舊名稱共20個固定名稱，不因重試產生無限nonce。

每次建立檔案前，reserve callback須保存plan、attempt序號、原文／替換長度及SHA256、容量上限，並返回相同首次值。容量保守按每個planned name独立計費，即三個新attempt共`3 × (3 × originalBytes + 2 × replacementBytes)`；即使hardlink共用bytes仍按完整大小預留。callback須持久保存及計費、核目前授權；首次值改變或結果未知則不建立檔案，後續須重讀持久結果。全域計費、既有legacy物件的容量與清理尚未接入。

prepare不檢查或採用其他attempt的物件，也不移除或覆寫它們；EXCL碰撞仍拒絕。既有parent／target、完整原文、Source callback、native lock、witness及directory sync核對保持。已保存checkpoint只可在同一attempt的plan及first IDs重開，不能把另一attempt當成舊checkpoint；新attempt亦不能改寫schema55既有output身分。

## 實際程序中斷驗證

自有子程序先把reservation同步到獨立檔案，再於output Sync、rollback Sync、output directory callback或完整pair directory callback直接os.Exit101，繞過defer。父程序重開自有root，將attempt2的reservation同步保存後完成準備；核前一attempt物件原生身分及bytes digest與target原文保持，兩套名稱完全不重疊。

同一attempt的output pair及完整pair未知保存回應仍保留證據，重新讀取同一first record可完成ready；不同attempt拒絕採用該proof。序號0／4、未知reservation回應、不同容量／first hash／序號都在副作用前拒絕。String／GoString只顯示redacted固定字串。

初次Windows選測編譯失敗是舊abrupt test仍使用位置初始化，新增attempt欄位後缺一欄；改為具名欄位，原失敗日誌保留。格式脚本初次在Windows拒絕POSIX平台，改於Linux執行成功，不將shell最後exit0當成前一步成功。

## 尚未完成

callback的reservation是file-backed測試契約，progress保存亦不是此批新PG attempt schema。尚缺持久allocator／一次性全域容量fence、latest attempt選擇與未知交易結果重讀、放棄紀錄／安全清理、換owner／generation恢復租約及完整FS授權。legacy未知物件的容量也須由後續全域reservation涵蓋。到達三次上限須保留證據並回報固定失敗，不能沿新的nonce繞過容量。

本段沒有target Rename／backup／rollback／結算／claims解除／formal worker。Windows directory sync仍為stub。schema55完整1548PASS只屬原1008來源，本段不把它當1010來源的完整PG證據；全G00–G51仍為7完成／198部分／131阻塞。原24h與完整品牌門禁保持。

## 本批驗證

Windows完整NFO413PASS／5個symlink條件skip；Linux domain＋NFO race762PASS／1個Windows專屬skip；既有Linux真PG聯合23roots／74PASS、Windows真PG七roots／28PASS皆零skipfail，所有程序terminal0。兩平台四個新actual child中斷leaf及同attempt的兩個resume leaf各通過一次。Windows PG owned relay finally關閉。

vet、Linux格式、增量brand0／339、gitignore與diff通過。1010份目前Go／SQL雜湊已保存，110份已發布SQL及requirements／trace／模組／LICENSE不變；獨立核當前8source、四份terminal logs、root coverage及歷史失敗後寫入[安全證據](evidence/nfo-commit-attempt-primitive.json)。私有finalizer初次生成CR／LF字串時SyntaxError，發生在驗證前，原script／metadata保留；修正生成方式後check-only及正式寫入terminal成功，未改formal source。
