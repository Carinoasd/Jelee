# NFO 首次原生身分證據（schema53）

schema53保存準備階段觀察到的 root、media、NFO 與全部相關祖先身分，並在後續準備複核、工作意圖複製及 Stage 使用同一份證據。首批完整 PostgreSQL 回歸因測試容量 overlay 引發資料庫 OOM 中斷而失敗，V2另因兩處容量夾具不匹配失敗。修正後V3完整四片均exit0：996份來源／472個測試根各run及pass一次、1478PASS／零skip及fail，獨立彙整核對通過，見[安全回歸摘要](evidence/nfo-write-native-receipt.json)。新增測試後V4完整回歸亦已通過；完整功能驗收仍未完成。

本輪另將全域entry容量舊快照及job完整批次隔離案例升為正式測試，各正式選測4PASS／零skipfail／terminal0。上述V3完整證據屬新增這兩個測試前的凍結來源；新增後V4凍結996份來源、實際編譯474個測試根的完整四片回歸均exit0，各root run／pass一次，1486PASS／零skipfail，見[最新安全摘要](evidence/nfo-write-native-receipt-v4.json)。Windows V4三套件953PASS／1008條件skip／零fail；另以短暫loopback／stdio轉送連真PG，原生preparation保存／repository reopen及job copy／TTL清理兩個root各PASS、零skipfail，轉送關閉且Linux子程序0。首次直接連線在fixture連線前失敗，原log保留；兩個選測不證完整Windows PG套件。

## 首次觀察

準備階段先取得 IO permit，從 filesystem／volume root 沿保留的目錄 handle 開啟相關祖先，再開啟媒體及 NFO。NFO 的身分來自讀取原始 XML 的同一個 handle。讀後核對仍保留的 handle 及目前目錄 entry，所有 handle 反向關閉後釋放 IO permit。

CPU 等待及 XML 編輯之後，再取得 IO permit 重新觀察，核對首次 receipt、root／parent／NFO、stamp 及原始 bytes。同 bytes／mtime 的新媒體物件、configured root 外的祖先替換、缺失或取消都須拒絕。CPU 等待期間沒有保留首輪 IO permit 或 filesystem handle。

Movie／HomeVideo／Episode／Series legacy file 使用媒體檔案身分；Series／Season directory 使用目錄身分，支援 configured root 自身。路徑計畫固定先 absolute 祖先、後相對祖先，按路徑去重，最後紀錄必須是 NFO 父目錄。

Windows 使用 volume／完整 file ID／原始 creation time；Linux 使用 statx device／inode／birth time，缺少必要欄位拒絕。receipt 最多128個祖先，固定48-byte records，8-byte header，總長200至6296bytes；保留欄位、平台及種類皆須符合 canonical 格式。建構、clone 與 getter 不共享可變祖先陣列，JSON與診斷輸出遮蔽私人證據。

## 保存及後續使用

schema53在 preparation／entry 新增 nullable `native_receipt`，歷史NULL保持NULL。讀取歷史資料先查欄位 metadata；不將整份私人 XML row 轉成 JSON，也不以目前 filesystem 觀察回填首次證據。

新 Save 拒絕缺證。job entry 複製同一 receipt，SQL BEFORE核對有效 source preparation、actor／library、首次 scope、root generation、request digest、stamp及payload hashes；deferred核對不依賴TTL清除後的 preparation。preparation／entry 的應用及 SQL容量計算都包含 receipt bytes。原有 immutable及quota fence保留。

Begin／plan／ready／Stage拒絕缺證。plan 的父目錄身分必須等於 receipt 最後一個祖先，target 必須等於首次讀取原始 XML 的 NFO 身分。應用在 INSERT 前核對；SQL BEFORE直接核NEW，deferred再核，catalog wrapper核已保存的plan，涵蓋重放。

Stage及ready resume核對同一首次 receipt，另核原始 XML bytes。身分複核不另配置一整份 XML，也不能將後來的新媒體當成首次觀察。仍有 native proof 或 journal 時，schema53降版拒絕；空及歷史NULL資料可降版，原始證據保留。

## 已取得的證據

- 真 PostgreSQL race 擴充回歸：29個測試根、130PASS，零skip／fail；涵蓋native receipt、commit file與root generation。
- 直接SQL：canonical但不同的parent／target由即時及deferred守衛拒絕，合法首次plan可保存、no-op重放及提交；既有16併發replay與repository reopen通過。
- 私有overlay只在owned rollback交易停用新BEFORE守衛，兩個錯誤plan負例使Go真正exit1；正式來源不變。另有copy及Stage equality守衛停用的負向控制。
- Windows V3凍結三套件951PASS／1002條件skip／零fail；條件skip不證Windows真PG。兩平台path plan測試明確核最後ancestor=NFO parent，包括root自身及最大深度。
- 995份Go／SQL凍結，實際編譯468個PostgreSQL測試根，四片均已exit1，468 roots各run一次，但沒有各pass一次；790PASS／558FAIL tests、零skip。104份001–052已發布SQL及原需求／授權保持。

首批資料庫自行恢復，未重啟；失敗statement屬容量overlay以JSON配置兩份32MiB payload。夾具現改為明確欄位傳遞大payload，保存合法owned item及matching source preparation／receipt，兩根5PASS／零skipfail含三隔離；先通過容量再重新跑完整四片。失敗紀錄見[中斷證據](evidence/nfo-native-receipt-pg-interruption.json)。新增三個receipt-byte exact boundary均通過：準備全域256MiB、job128MiB、entry全域512MiB；同既有library128MiB邊界，分別確認不計receipt時仍可容納新增資料。私有SQL容量函式停用receipt contribution後，三案均真正red、正式來源不變。全quota正式八根29PASS，含generic global preparation及entry rows三隔離；native preparation精確最後slot舊快照三隔離4PASS，停用fence的repeatable-read反例真正red。native特有job／entry global stale snapshot矩陣仍待審查補齊。上述專項不能取代新完整四片的終態及來源核對。

## 完整回歸證據門檻

V3彙整工具從實際編譯清單重建472個測試根，逐片核對分組、run／pass各一次及成功package終態，另核四片exit0、零skip／fail。996份Go／SQL必須同時符合凍結清單的成員與內容；已發布104份SQL、原需求、go.mod／go.sum與LICENSE必須保持。完整coverage尚未產生時，工具拒絕寫出公開成功摘要。pending拒絕及成功分支均已實核，成功摘要只適用該996份凍結來源。

公開摘要只記錄計數、測試名稱、來源及log雜湊，不帶XML、SQL payload或資料庫連線字串。Windows條件skip、負向控制的預期失敗、先前中斷及cleanup限制各自保留；完整PG通過也不能推導品牌、worker、target提交、恢復或記憶體驗收完成。

## 容量與交易快照案例的範圍

`copyNFOWriteIntents`在同一admission交易建立request及完整entry batch，鎖定job，總數及intent不可變。job128MiB三隔離正式案例已各建立自己的三筆batch，前兩筆恰exact inclusive cap，第三筆由精確capacity23514拒絕；rollback後沒有job／request／entry殘留，已刪source preparations恢復、新增small preparation消失，共4PASS。

該案例在同一交易複製entry後清除source preparations，再配置第三份，避免同為128MiB的library preparation限制先拒絕。這是owned SQL儲存守衛案例，不宣稱runtime准入或新filesystem觀察，也不提交不完整batch。private READ COMMITTED反例僅在owned交易移除intent容量函式的native byte contribution；超限完整batch確實提交，Go exit1／3FAIL events／零skip，正式來源bytes保持。job容量屬每個新batch，不以同一已提交job跨交易追加entry作為合法模型。

全域entry512MiB私有候選已通過三隔離4PASS：兩份source preparation在快照前建立，兩個完整job競爭最後byte空間，第一筆恰cap，不計native bytes時兩批均可容納；第二筆拒絕並核零partial jobs／requests／entries。READ COMMITTED要求entry階段精確capacity23514；REPEATABLE READ要求entry階段23514或40001。Serializable可在job建立時收到40001，此證明整筆准入回滾，不宣稱entry fence因果。

候選V1對Serializable拒絕位置過度限縮而失敗，原log保留。V2私有反例只停用owned entry fence，Repeatable Read確實接納第二個完整batch，Go exit1／3FAIL events／零skip，正式來源未變；這才證明entry fence的作用。案例已升正式來源，正式選測4PASS／零skipfail／terminal0；新增後完整V4已通過，原V3證據仍保留其凍結範圍。

## 後續範圍

receipt是有界checkpoint觀察，不能證明atomic filesystem snapshot；native ID可能重用，birth／creation time不構成不可偽造身分。canonical SQL格式不認證實體ownership。OS open／stat受阻時，context checkpoint不提供硬性取消期限。

完整heap／RSS尚未證明：既有96MiB原始payload reservation不涵蓋receipt clones、預載Source、XML物件或GC。Windows held目錄handle可能阻擋讀取期間的rename，該測試記錄沒有實際替換；跨CPU替換測試則確實替換並拒絕。

同實體未解決journal排除、partial stage處置、backup／target Rename／rollback／成功結算／crash恢復、Windows目錄耐久性及正式worker／API／CLI尚未完成。缺失NFO導出需要支援absence的新協議；現有recipe要求原始NFO及其檔案身分，不能完成G39.14的三種批次操作。read-write runtime仍未准入，原24h圖片長測來源也不包含本候選。
