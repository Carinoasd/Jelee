# 圖片與掃描 24 小時驗收（實作中）

本頁記錄驗收工具的進度，**目前沒有正式 24 小時結果**，G42.10 仍未完成。既有十萬圖片結果見 [image-memory.md](image-memory.md)。

## 已實作

- 測試專用 Go 採樣器：每秒讀取 worker RSS、heap、GC 累積值和正式圖片 Processor 統計，階段切換另取樣。每批最多 60 筆、佇列最多 4 批；背壓超過 5 秒即失敗。只保留目前批次，總量上限 90,000 筆、時間上限 25 小時。
- 單一 JSONL 寫入器：固定 run ID、連續序號與非遞減事件時間；只接受明確的證據型別。每行最多 64 KiB、事件總量最多 64 MiB。目的地必須支援寫入期限；寫入逾時、取消、部分寫入或序列錯誤後不可恢復成功。工作輪次結束後仍允許關閉階段採樣。
- Python 採樣判定：跨批次檢查索引、時間間隔、累積計數器與記憶體上限，只保留最後一筆和聚合值。
- Python 穩態判定：288 個五分鐘輪次分成 24 組，每組 12 個靜止狀態檢查點。以第 2～4 小時作參考，比較末 3 小時與第 2～24 小時範圍；heap 容差 32 MiB、RSS 64 MiB。末 6 小時若連續每小時增加至少 1 MiB，判未建立穩態。這些是事前工程容差，不能解讀為零洩漏證明。
- 既有冷／暖 HTTP 圖片負載抽出共同的單調時間起點；原一小時驗收繼續由原 sampler 委派，未放寬既有門檻。

採樣的 RSS 上限仍為 464 MiB；Active ≤2、reserved ≤192 MiB、每圖估算 ≤96 MiB、cache ≤128 entries／32 MiB。Go 工具僅以 `jelee_probe_tests` 編譯，沒有新增產品採樣執行緒或 API。

## 尚待整合

以上元件尚未形成完整驗收。仍須接上單一事件消費者、逐小時 GC 與 cgroup 邊界、真實掃描與圖片輪次、12 小時 session rotation、前後負例與 SIGTERM 清理，以及固定提交快照的背景控制器。控制器還需核對所有事件的身分與序號、原始檔大小與 SHA、退出狀態及自建資源清理。

正式流程將使用同一程序連續至少 86,400 秒、288 輪；smoke 固定兩輪／600 秒。合成資料、短測和單元測試都不能替代正式長跑。工作後的關閉採樣也不能計入 24 小時工作時長。

## 目前驗證

Python 22 項合成資料測試通過，包括串流處理 86,400 筆的固定保留狀態檢查；這不是實際經過一天的量測。Go Windows 選測與 Linux race 選測通過；Linux 另驗真 `os.Pipe` 寫入，Windows 略過這個平台專項。新測試已加入 `scripts/runtime_memory_contracts.py` 的 CI 契約步驟。

執行：`python -m unittest discover -s scripts -p 'test_images_soak*.py'`，以及固定 SDK 的 `go test -tags jelee_probe_tests -run '^TestImages(Soak|Memory)' ./internal/platform/runtime`。完整正式結果將另保存，不以本頁或測試通過宣稱 G42.10 達成。

## 第二批：正式流程輔助函式與 GC 判定

新增混合掃描的 HTTP 提交／狀態等待，以及 inventory、baseline、106目錄、2008檔案／bytes與active snapshot交叉檢查；原五十萬純影片驗收不改。session rotation 使用正式HTTP路由，要求舊token失效、新token可用、身分與角色不變及24h TTL；grant只保留記憶體，事件只有布林。前後負例的seed與check已分開，避免第二輪重插固定ID。另補輪次／rotation／資源typed schema。

GC判定使用25個相接的真量測邊界，逐24小時各核一次並核完整區間，沿原保守histogram桶上界、50ms與1%門檻。單小時超標即拒絕，全天平均合格不能掩蓋；缺邊界、時間異常、counter倒退與Inf有事件均拒絕。這仍須由完整controller核實時間、身分和工作量。

驗證：Linux race真Fx／HTTP／PG整合完成兩輪2008檔混合掃描、106目錄和snapshot核對；管理員／一般使用者各rotation成功，資料庫各僅一個live session。這個短整合使用微型檔案，只驗掃描與session，未代替真圖片負載。Windows選測及tagged vet通過（未提供DB的整合測試略過）；Python新22項、既有圖片控制器21項通過。

另以原1000張真JPEG／PNG／PNG16短測驗證負例拆分，案例72bd27a66a854db9a2468752cba2c610，來源摘要2be0bdf008e8a1ce7d425fb772a6a7b43d51e3af4d8e9aa523b0152b9f630899。1000冷解碼、192暖命中、ACL／負例／取消／SIGTERM／來源保持及自建資源清理通過；finalAcceptance=false。後加資源schema與GC工具不屬該短測來源，不把此結果擴大為完整長測。私人紀錄為 .testdata/image-memory-72bd27a66a854db9a2468752cba2c610/summary.json、.testdata/soak-workload-linux-race.log。

下一步仍是整體協調器：把現有sampler、writer、round和GC helper接成同一Fx程序；組合600秒smoke通過後才能提交固定快照啟動24h。
## 第三批：同程序輪次、協調器與 opt-in 入口

新增 TestImagesSoakAcceptance，須明確提供 JELEE_IMAGES_SOAK_ACCEPTANCE=true、scope formal或smoke與32位run ID。共用原Fx／PG／帳戶／SIGTERM生命週期，formal固定288輪、smoke固定2輪，每輪300秒，不接受加速時數；第144輪rotation，末輪後仍等待滿工作時長。提前訊號會取消並join工作，再走共用清理。

collector由單一consumer排列sample／round／hour事件；每小時先flush並drain才讀全部採樣的min/peak，只保留24組聚合。Processor observer只可綁定一次，寫入失敗取消producer，正常停止join後讀GC/cgroup結束邊界。ready/final記錄與事件共用期限和大小限制，各最多一次，final不消耗事件序號。最終結果仍須外層驗證，Go返回passed本身不等於正式驗收完成。

Windows選測及vet通過；Linux race與真PG輔助整合通過（soak-collector-linux-race.log），最後ready/final變更另做對應race選測通過（soak-collector-final-linux-race.log）。Windows首次collector fixture緊迴圈遇相同時鐘tick，改測試注入嚴格遞增時鐘後通過；正式原生時鐘未更改。共用生命周期抽取已由原1000圖片smoke 8ff8521fd760435795b53dfe7cea1196驗過，後增collector/入口不屬該smoke來源。

**尚未執行完整600秒或24h入口。** 下一步是外層控制器：固定快照、單條logs-follow有界reader、完整stream/round/hour/GC重驗、ready後SIGTERM、私有status與自建資源清理。不得以本批單元或短PG整合替代長跑證據。
## 第四批：串流重播與固定預算

scripts/images_soak_acceptance.py逐行重播有界JSONL，拒絕重複JSON key、非有限數、未知事件、身分或序號錯誤、截斷及超量。逐輪核scan、cold/warm增量、quiescent資源，checkpoint須精確對上原始採樣；正式模式再核24組hour、rotation、GC邊界與穩態。最後報告不能覆蓋或補造缺少的事件。

重播只保留400筆近期採樣、288輪摘要及24小時聚合。事件64KiB、final2MiB、raw64MiB；這些與RSS／GC／趨勢門檻固定於tools/image-soak-budget.json，validator拒絕修改。提供實際Docker inspect時會另核cgroup／OOM／退出限制與媒體唯讀mount；未提供inspect的結果只有streamValidated，沒有finalAcceptance。

Windows與Linux各32項合成資料測試通過，包括完整288輪／24個小時的正向重播、缺號／截斷／final矛盾、checkpoint造假、唯讀mount破壞、門檻放寬拒絕。既有圖片控制器21項通過，新重播步驟已加入memory contracts CI。**所有上述資料為合成契約測試，沒有實際運行24小時。**

下一步外層controller仍須完成固定快照與單條logs-follow、接收時間heartbeat、真容器退出與cleanup核對；然後執行600秒smoke，正式24h尚未啟動。
## 第五批：外層日誌監控與容器生命週期

單一 logs-follow 管線持續讀取，不重新抓取累積日誌；以控制器 monotonic clock 核70秒完整事件接收心跳，部分行或任意文字不刷新心跳。raw與行長有硬上限，私有raw保留供重播；每5秒查容器、每300秒更新有界status。ready後只送一次SIGTERM，串流結束再核follower exit、worker exit/OOM。失敗/中斷取消並回收follower，容器先嘗試20秒正常停止，再清理本次UUID資源。

run_images_soak.py串接實際建置、固定fixture/預算、私有env、image ID、重播與來源/fixture/cleanup核對。Windows39通過／3平台略過，Linux42通過；包含真管線握手、心跳逾時回收和控制器故障注入。這些測試未執行Docker長測。

**尚缺固定已提交來源快照及背景啟動器。** 模組暫不提供CLI，finalAcceptance保持false；真正600秒smoke與24h仍未啟動。

## 第六批：固定來源及背景啟動器

Linux入口 `python3 -B scripts/start_images_soak.py --smoke` 從HEAD提交建立原生磁碟快照；tracked來源唯讀、逐檔SHA核對。SDK與media runtime只用本機已有的manifest pin，背景worker及imports皆來自snapshot。每輪自建專屬PG容器與volume、動態loopback port，不修改既有PG。flock由worker持有至退出，避免重複長測；registry記PID/startTicks/bootId以供接手確認。

省略--smoke可啟動formal，但必須先有同commit真smoke成功與完整清理證據。正式run固定288輪/24h，沒有任意時長override。SIGTERM/INT取消會走容器與schema補償清理，不能將中斷時段拼接。清理專屬PG、secret與snapshot後才寫launcher最終結果。

本批工具測試Windows42通過／5平台略過、Linux47通過；包含Git真提交與dirty workspace隔離、archive逃逸/link拒絕、formal短測前提。**實際600秒與24h結果需另附，不以工具測試代替。**

### 真Docker輸出期限修正
第三輪在3.43秒退出，尚未產生start事件。Docker提供的原始stdout是blocking FIFO，Go SetWriteDeadline不支援；最小容器probe重現。Linux測試入口改用O_NONBLOCK重開同FIFO，核對SameFile並預檢期限，自己的Close不關原stdout。Linux race真pipe塞滿期限與整合通過；此修正只影響opt-in驗收程式，正式產品未變。所有失敗短測證據保留，均未計入24h。
