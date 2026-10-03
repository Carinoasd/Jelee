> 最新進度見下方「Claude 接續進度」。以下為原交接：本文件隨使用者要求的 WIP 交接提交公開。schema56 最新 legacy 守衛尚未重測，quota fence 因果性未證明，正式 Stage 尚未接線。以下既有 green 只對應各自歷史來源。四套件編譯檢查通過，不是功能或完整驗收。Claude 請從「最重要的待修驗證問題」接續。使用者已要求先推送，因此本次先保存問題；後續功能發布仍須完成驗證。

# Jelee 交接給 Claude：schema56 WIP與後續全部階段
記錄日期：2026-10-04（Asia/Taipei）

## Claude 接續進度（2026-10-04）
工作目錄已從 Windows 轉到 WSL 的獨立 clone（同 branch／PR46）；原 Windows 目錄留給仍在跑的原 24h（run329073a5），未動。本節是最新狀態，下方原交接內容保留為歷史。

已完成並驗證（Linux 真 PG、race；私密 JSONL 只取計數）：
1. 最新 legacy 互斥守衛：`^(TestNFOCommitAttempt.*|TestNFOCommitCheckpoint.*)$` 四套件 69 PASS／0 fail／0 skip（`.testdata/claude-schema56-selected-linux-v1.jsonl`）。以上是在 Stage 接線前跑的。
2. quota fence 因果：第二候選改由正式 fixture 建在另一個 library／job／root／item，測試先查兩者不共用 job、library、root、item、source，且兩個 lease 都是 running。rows／bytes 各跑 RC／RR／Serializable，第二筆都被拒；另加 `fence-disabled/repeatable_read` 對照，只把 owned schema 的 `fence_nfo_commit_attempt_quota()` 換成 `RETURN NEW`，第二筆就被接納且超額（rows 257、bytes 上限再加一份）。8 項全 PASS（`.testdata/claude-schema56-quota-causal-linux-v1.jsonl`）。失敗標記拆為 `stale attempt capacity admitted` 與 `unrelated attempt capacity refusal`。
3. 正式 Stage 接線（`internal/adapter/nfo/stage_commit_attempts.go`）：repository 實作 `app.NFOWriteCommitAttemptStageRepository`（Store 有）時改走持久 attempt：
   - 先讀 `GetNFOWriteCommitAttempts`；legacy ready 與已分配 attempt 並存就 ErrChanged。
   - 有 ready 的 attempt：只驗證並重播，不新建、不認領、不刪除。
   - 有 checkpoint 的 attempt 固定續作；首次輸出被改過即拒絕，不輪替。
   - 沒有 checkpoint：該 namespace 五個名稱都不存在才重試同一 namespace；任一存在（或觀察不清）就保留，先 `AllocateNFOWriteCommitAttempt` 持久分配下一個，才開始動檔案。
   - 第 3 個用完回 `ErrCommitAttemptsExhausted`，不刪任何物件。
   - attempt 0 仍走原 legacy 流程（`stageLegacyNFOCommitFiles`），舊行為不變。
   - 新 namespace 的 plan callback 會重播同一 plan，並用 `ReserveNFOWriteCommitAttempts` 比對首次 reservation。
   - 測試：nfo 套件 6 個假 repository 案例；另做反向驗證，把名稱偵測關掉時 3 個會失敗。postgres 套件新增 `TestNFOCommitAttemptStageTruePG`（rotate／exhausted），用丟失首次 checkpoint 的 wrapper 模擬「已建檔、未存 checkpoint」，驗證真 PG 依序分配 1–3、上限拒絕、未知物件保留、legacy evidence 不被寫入、target 不變。
4. 所有 NFO commit／Stage 相關測試（接線後重跑，含舊 Stage 測試改走新流程）：524 PASS／0 fail／0 skip，4 package PASS（`.testdata/claude-schema56-stage-linux-v1.jsonl`）。

仍未做（不要當成完成）：
- 真 PG + 實際 child `os.Exit` 的中斷點矩陣（建檔後、phase1／2 存檔前後、unknown response），本批用 wrapper 模擬，不能替代。
- 新 freeze、完整分片 PG 回歸、Windows 真 PG、finalizer。這些交給 @MoYuanCN 在自己的環境跑，說明見 PR46 留言。
- 恢復 lease、FS grant、target Rename／settlement、worker 接線；G00–G51 狀態不變（7／198／131）。
- 原 24h 只對應來源 24caf7d4，本批沒動圖片／記憶體路徑。


## 請 MoYuanCN 執行的驗證
給 @MoYuanCN（或代跑的 Codex）照著做。只跑、只回報，不要改 Go／SQL，也不要刪改 `.testdata` 的舊 log。

前提：
- 分支 `feat/jelee-ignore-family-worker` 的最新 commit（PR46 頁面顯示的 HEAD），工作樹乾淨。
- 已跑過 `make bootstrap`；Windows 用 `scripts/bootstrap-tools.ps1`。
- 一個獨立的 PostgreSQL 16，資料庫名稱必須是 `jelee_test`；不可和正式資料或圖片長測共用。測試會在裡面建立和刪除自己的 schema。

1. Linux 完整回歸（race）
   ```
   export JELEE_TEST_DATABASE_URL='postgres://<user>:<password>@127.0.0.1:<port>/jelee_test?sslmode=disable'
   export JELEE_REQUIRE_INTEGRATION=true
   make test-race
   .bin/go test -race -count=1 -timeout=120m -tags jelee_probe_tests ./internal/adapter/postgres ./internal/adapter/nfo ./internal/platform/runtime
   ```
   - `make test-race` 是整個 repo 的基本回歸，每個套件期限 45 分鐘。第二行加上 `jelee_probe_tests` tag，補跑歷史完整回歸也有涵蓋的 probe 測試；postgres 套件若在第一行逾時，以第二行的結果為準。
   - 想要 JSON 計數的話加 `-json`，輸出導到 `.testdata/` 底下的新檔名，不要覆寫舊檔。
2. Windows 真 PG：在 PowerShell 設好同樣兩個環境變數，用 `pwsh ./scripts/run-go.ps1 test -count=1 -timeout=120m -tags jelee_probe_tests ./...` 跑一次。不加 `-race`，因為 Windows 的 race 需要 CGO 和 gcc，以前也沒在 Windows 跑過 race。目前 schema56 在 Windows 只有條件 skip 的結果。
3. 回報：在 PR46 留言，寫 commit、平台、test pass／fail／skip 數、package pass／fail 數；有失敗的話列出測試名稱。不要貼 DSN、XML 或錯誤細節原文。

這次不用跑 24h：本批沒有動圖片和記憶體路徑。之後若改到圖片或掃描，再照 [image-soak.md](image-soak.md) 用 `scripts/start_images_soak.py` 跑。

## 先讀這段
使用者要求把目前這一小段工作記錄下來，轉交 Claude 接續。初次交接只整理紀錄。使用者隨後明確要求把現有修改與問題提交推送，供 Claude 從 GitHub 接續。已直接核對工作目錄與既有測試 JSONL。以下明確區分歷史通過與目前未驗證內容。

既有工作目錄：
使用此 PR 的 feat/jelee-ignore-family-worker 分支工作目錄

既有 branch：feat/jelee-ignore-family-worker
目前基底 HEAD：53796aa1472af5c4006988162db625fb10289451
提交訊息：docs(nfo): 記錄有界attempt發布及持久接線缺口
PR：https://github.com/Carinoasd/Jelee/pull/46
遠端 HEAD／PR body 曾在 .testdata/pr46-attempt-remote-verified.json 核驗；本交接沒有重新查遠端或 CI。

## 完整目標與原始材料
原目標是接續 docs/requirements-source.md 的 G00–G51 需求與驗收，逐階段驗證、提交及推送既有 PR，保留長測與品牌門禁，直到有完整完成證據。336 項仍為 7 完成／198 部分／131 阻塞，不因這批測試通過而調高。

先閱讀：
- docs/handoff.md：歷史交接，本次已在頂部補未提交 schema56 摘要。
- docs/requirements-source.md、docs/requirements-traceability.md：完整範圍與狀態。
- docs/nfo-partial-stage-recovery.md：部分落檔恢復設計及缺口。
- docs/evidence/nfo-checkpoints-schema-v1.json：已發布 schema55 完整回歸。
- docs/evidence/nfo-commit-attempt-primitive.json：已發布私有有界 attempt primitive 的驗證。
不要重寫歷史證據，或把舊來源的完整回歸套用到目前修改。

## 已發布的成果
schema55 已發布 110 份 SQL（001–055 上下行），不得修改。
其凍結來源 1008 份 Go／SQL、494 個 compiled roots、完整 PG 1548 PASS／零 skip／fail；只證明當時來源。
私有 attempt primitive 已在 f2af24361c837efe765a201550ce99d7f338c159 發布：
序號0保留 legacy names；1–3使用各自 names，先保存 reservation，再落檔；保留首次物件證據與未知結果。它尚無正式 runtime 呼叫者。詳見上述 evidence 與程式。
本批基底 53796 是接續記錄發布狀態與持久接線缺口的文件提交。

## 本批交接提交的 WIP：schema56
目前 13 個 Go／SQL 檔有新增或修改；精確內容 hash 見docs/evidence/nfo-attempt-ledger-wip-source-snapshot.json。另有 docs/handoff.md 摘要與本文件／快照。本批依使用者指示提交至既有分支供接續；未做新 freeze／完整 regression／finalizer，不能當成功驗收。

新增：
- internal/domain/nfo_commit_attempt.go
- internal/domain/nfo_commit_attempt_test.go
- internal/app/nfo_commit_attempt.go
- internal/adapter/postgres/nfo_commit_attempts.go
- internal/adapter/postgres/nfo_commit_attempts_test.go
- internal/adapter/postgres/nfo_commit_attempt_quota_test.go
- internal/adapter/postgres/migrations/000056_nfo_commit_attempts.up.sql
- internal/adapter/postgres/migrations/000056_nfo_commit_attempts.down.sql

修改：
- internal/adapter/postgres/store.go：SchemaVersion 55 → 56。
- internal/adapter/postgres/nfo_commit_files.go：SavePlan 提早取得容量鎖；舊 schema fixture 使用 optional table 查詢。
- internal/adapter/postgres/nfo_commit_checkpoints_test.go：保留 schema55 downgrade 守衛的歷史驗證。
- internal/adapter/nfo/commit_attempt.go
- internal/adapter/nfo/commit_attempt_test.go
  後兩檔改用共享 domain 上限與容量公式；已發布 primitive 文件中的 factor3 是歷史，候選已改 factor4，發布新文件時須清楚說明。

實作重點：
- NFOWriteCommitAttemptLimit=3；保留 legacy0 + 新1–3 namespace。
- 每個 reservation 保守 bytes = 4 × (3 × originalBytes + 2 × replacementBytes)，含 legacy 保留容量；每份 payload 1–32MiB。全域最多256 reservations／1GiB。
- 固定 quota fence row 以 UPDATE 防止舊 snapshot 超收容量。
- plans 自動產生 reservation／attempt0，舊 Stage 也要計入容量，不能繞過。
- attempts 0–3 以 predecessor FK／CHECK 保證不跳號；首次值不可變、重播保留首次時間。
- per-attempt checkpoints 兩階段：output pair、完整 output／rollback pair；phase2 綁定 first output。
- 一個 token 的 new-ready 選定單一 attempt；latest、跨 attempt IDs、lease／actor／catalog 即時與 deferred 守衛。
- 舊 retained plan 升級只回填 job-owned payload 與 first 時間，不能推定新的 filesystem IDs；超出歷史容量則 atomic 拒絕，不刪除歷史。
- down56 有 reservation 或 journal 時拒絕並保留資料／dirty55；空資料才恢复55。
- repository 提供 Reserve／Allocate／Get／SaveAttemptCheckpoint／SaveAttemptReady；Get 用固定3槽陣列重讀 persisted evidence，沒有新增 filesystem observation。
- 這批仍是持久儲存與 port，未接正式 Stage 自動分配／選 latest／恢復流程。

最後一筆程式修改尚未測試：
- 舊 ready 存在時，禁止分配新 attempt。
- 分配新 attempt 後，禁止寫 legacy ready／schema55 checkpoint。
- 新 checkpoint 不得沿用 legacy checkpoint 的物件 IDs。
- 新 TestNFOCommitAttemptLegacyEvidenceExclusive 三個案例。
請先驗證這些最新 bytes，不能拿前面的 green 當成最後版本通過。

## 測試進度與失敗歷史
本交接安全解析既有 JSONL，只讀事件 counts，不輸出私密 Output：

1. .testdata/nfo-attempt-ledger-pg-linux-v1.jsonl
   12 test PASS／0 fail／0 skip，1 package PASS。
   Ledger replay／bounds、immutability、catalog after constraint flush（三隔離）、lease／actor、legacy migration capacity。
   這份是在最後 legacy 互斥修改之前，且不是完整 PG regression。

2. .testdata/nfo-attempt-ledger-quota-linux-v4.jsonl
   7 test PASS／0 fail／0 skip，1 package PASS。
   rows／bytes × RC／RR／Serializable 六個 leaf，驗到256筆與精確1GiB上限；失敗的第二筆 plan 不得殘留。
   同樣早於最後 legacy 互斥修改。

3. .testdata/nfo-attempt-ledger-windows-conditional-v3.jsonl
   21 test PASS／0 fail／16 skip，3 packages PASS。
   條件 skip 不能當 Windows 真 PG 驗證；目前 schema56 沒有完整 Windows 真 PG 通過證據。

保留以下失敗與 runner，不覆寫或刪除：
- windows-conditional-v2：quota 測試 Go else 換行語法 compile fail，後來修正。
- quota-linux-v1／v2：clone entry 沒有相符 preparation，被既有 guard 拒絕。
- quota-linux-v3：matching preparation 修正後，PG jsonb 正規化 request 與 Go canonical request digest 不同，GetTask 拒絕。
- v4 改用 Go json.Marshal(domain.CloneRequest) 產生 canonical request，才通過。
- .testdata/run-nfo-attempt-ledger-pg-linux-v1.py
- .testdata/run-nfo-attempt-ledger-quota-linux-v1.py 至 v4.py
- 其他 primitive 歷史失敗詳見已發布 evidence／docs/handoff.md。

之前的測試 handles 59604、6259、60987、90784、7536 都已終態，不要再 poll 或當成 live。
本交接沒有啟動完整測試；沒有 schema56 新的完整回歸程序待接管。交接發布前僅追加四套件 test -run ^$ 編譯檢查，全部 exit0／no tests to run；不證明新 SQL 守衛通過。原24h長測是另一個持續程序，見下方。

## 最重要的待修驗證問題：quota 因果性
目前 quota test 的兩個競爭 plan 來自同一 job／library。RR／Serializable 即使移除 quota fence，也可能因既有 job generation UPDATE 發生40001，因此目前 green 證明整體容量限制，不能證明全域 quota fence 本身必要。

建議先做：
1. 將第二個 candidate 建在不同 job、library、root／item；不要停止第一 candidate 的 live lease。
2. synthetic native receipts 僅用於 storage contract fixture，不能宣稱 filesystem 授權或真 native writer。
3. 確認兩筆不共用 catalog／job 行；檢查其他全域 metrics fence 是否仍可能掩蓋原因。
4. 私有 overlay 只將 owned schema 的 fence_nfo_commit_attempt_quota() 變成 RETURN NEW，跑 RR rows／bytes。
5. 必須精確觀察第二 plan 被錯誤接納／超額，而非 unrelated failure；保留 red log。不要改正式110份舊 SQL。
6. 將目前含糊的「stale attempt capacity exceeded or unrelated refusal」拆成 admitted 與 unrelated refusal 的固定標記。

fixture helper makeAttemptQuotaBatch：
原job先 stopped，同library每批100項，總共跨多批到上限；clone item/media/preparation，以精確現有 copyNFOWriteIntents SQL 建 entry，入 entry 後删除暫存 preparation 以避免 prep quota 干擾；request bytes 必須用 Go canonical marshal。新增不同library版本要同步 job/request/item/media/preparation/entry 的 scope，receipt ordinal 要避免碰撞。不要只改 job UUID 就假定隔離。

## 後續接續順序
1. 核對 snapshot、git status、最新 legacy 互斥守衛，再跑 fresh selected log。
2. 完成不同 job／library quota 測試與單一 fence 的紅測因果控制。
3. 審查 deferred／同 TX 提前 flush／首次 output FK／expired actor／migration limits，補必要驗證。
4. 接持久 attempt 到正式 Stage：
   - 在 filesystem 副作用前取得 durable allocation。
   - saved phase1／2 核驗首次 ID，再同 attempt 續作。
   - 無 first checkpoint 但 names 存在時保留未知物件，取得新 namespace。
   - 不可把任意 ErrChanged 當成可輪替；已保存 first output 被更動必須拒絕。
   - 達3個新 attempt 上限有界拒絕；不能擅自刪未知物件。
   - 舊 ready 與新 attempt 相容／互斥按新守衛處理。
5. 真 PG + actual child os.Exit 驗 create-before-first-checkpoint、phase1／2 save 前後及 unknown response。舊 file-backed crash probe 不能替代 PG 接線。
6. 最終來源穩定後再新 freeze、實際 compiled roots、分片完整 PG、Windows 驗證、獨立 finalizer／hash／coverage。凍結測試活躍時不改 Go／SQL。
7. 通過後更新方法／trace／handoff／新 evidence，核精確提交清單，再提交推送既有 branch／PR46；核遠端 HEAD／body／CI。
不要只完成 storage contract 就宣稱 G00–G51 完成。

## 仍缺的整體驗收
完整 target Rename／backup／rollback／settlement／crash recovery、新 owner recovery lease、完整 filesystem grant、Windows directory metadata durability、正式 worker／API／CLI 的三種 G39.14 批次與 missing-NFO absence、完整 heap／RSS。正式 worker 仍 disabled。
完整品牌門禁歷史有兩項 FAIL；增量 brand0／339 不等於完整 branding 通過。
本回合未核新 CI，不宣稱全綠。

原24h：
.testdata/images-soak-active.json
安全核查腳本：.testdata/check-original-images-soak-identity.py
原 run329073a5d193446383327ab217aba147，PID1026300，startTicks33072456，boot4a5d9c5c-4482-4c3e-8978-30156b1ce92f。
來源24caf7d45fb96390689dcc03685033242b7bbfea，不含55／56。上次核仍 running／OOMfalse；本交接未刷新，不可說目前已完成。先核身分，禁止重啟以抹去長測。scale PG 過去 OOM 歷史亦要保留。

## 執行注意
- 使用既有 repo，不新建 clone／worktree／thread；沒有授權 merge／release／force push。
- Windows shell 為 PowerShell；每個 repo 指令指定正確 workdir。
- Go：./scripts/run-go.ps1；gofmt：.tools/go/1.27.1/windows-amd64/go/bin/gofmt.exe。
- Linux runner 使用 wsl -d Ubuntu --cd <repo> -- python3 -B .testdata/<新runner>；CGO／GCC15 沿用既有 scripts。
- scripts/check-format.py 要在 Linux 跑，Windows POSIX 不相容失敗已保留。
- 私密 .testdata 不提交、不輸出 DSN、原 XML／JSONL／SQL payload／錯誤 detail。只輸出 counts、固定錯誤標記、SQLSTATE／constraint、hash。
- 已發布 go.mod／go.sum／requirements-source／LICENSE 和001–055 SQL 保持。
- 不改寫既有 log／evidence；新測試採新 prefix。
- 使用者已授權必要修正、驗證與推送既有 PR；不反覆詢問一般可逆步驟。
- 本次交接只是暫停接續工作以交給 Claude，不代表目標完成或阻塞。

## NFO 之後的完整階段路線（原需求第六節）
原始順序見 [requirements-source.md](requirements-source.md)「六、階段與提交紀律」。下面沿用原始20階段中的第8–20階段，補上接續任務；是待完成路線，不是完成宣告。前7階段也不能因為目前進入 NFO 工作就推定全部驗收完成；branding／feature-removal／PostgreSQL／禁止轉碼等既有缺口仍須回補。

每阶段開始前，先讀 [requirements-traceability.md](requirements-traceability.md) 对应所有行，逐條確認已有實作與證據，再補剩餘約束；本文摘要不能取代原始需求。依原始順序處理依賴，同時保留既有長測。

| 原始階段 | 接續任務 | 主要驗收 |
| --- | --- | --- |
| 8 image-assets | 補齊 G40 本地／遠端海報、背景、Logo、單集縮圖的識別、優先順序、保存、更新與按需縮放／快取；與 NFO／掃描整合。 | 圖片黃金素材、路徑與不可信輸入安全、10萬張識別、快取命中率與完整更新矩陣；既有本地圖片子集與長測不能外推整階段。 |
| 9 media-scan-metadata | 補 G09／G13／G14／G19–G22 的工具、掃描、增量判定、TMDB融合、媒體探測、多版本、季集識別、忽略規則及關窗／取消／續跑整合。 | 100變動檔與完整庫掃描、未變更不重探測計數、命名參數矩陣、NFO＋圖片同次增量、錯誤／取消／重啟與誤刪保護。 |
| 10 playback-direct | 完成 G10 的第三方客戶端原資源直投、Range、Seek、外掛字幕／音軌及進度協作，回補 G15／G16。 | 真客戶端起播、前後／連續Seek、原媒體checksum保持；首字節P95≤500ms、Seek P95≤1s；轉碼／HLS／DASH路徑不可達，Web播放拒絕。 |
| 11 users-api-compat | 補 G07／G08／G18／G24：使用者、初始引導、Jelee自有API、第三方客戶端相容登入／瀏覽／搜尋／詳情／進度。 | API契約與錯誤碼、真客戶端E2E、100並發進度上報、登入／session撤銷、舊能力探測與關閉功能不可重新調用。 |
| 12 access-control | 補 G47／G48：UA／標識管控、媒體庫查看與存取權限，所有自有及相容接口强制执行。 | 權限矩陣、列表／搜尋／詳情／圖片／直投等隐藏内容零洩漏、誤配置緊急恢復；SQL／匹配下推，與無規則基線相比開銷≤10%。 |
| 13 logging-devmode | 補 G46／G45：分層結構化日誌、熱調整／稽核／脫敏及特殊開啟的開發者模式、可見狀態與自動恢復。 | 默認关闭與生产誤開拒絕、開啟／关闭／到期矩陣、限制恢復、稽核不可关闭、敏感資訊零洩漏、DEBUG／INFO對P95影響。 |
| 14 concurrency-memory | 補 G25／G26／G29／G41／G42：CPU／IO分額、有界併發與背壓、取消釋放、熱路徑效能、整體heap／RSS。 | 掃描＋探測＋NFO＋圖片＋直投＋API混合壓測、race／死鎖／洩漏、1萬條列表／冷熱詳情；API熱路徑P95≤200ms，CPU對同機基線目標降≥20%，完整掃描記憶體不隨條目數線性成長。 |
| 15 frontend-platform | 補 G03／G27／G31／G34／G35：前端架構、語言、無播放的管理／媒體瀏覽／工作介面、可用性、安全與效能。 | 關鍵頁面E2E、前端輸出轉義／授權、Web無播放入口或依賴；首屏可互動P95≤1.5s、關鍵頁切換與bundle預算。 |
| 16 theming-extensibility | 補 G32／G33 的插件、主題及布局擴充，遵循原始能力與安全邊界。 | 插件／主題／布局載入、配置與切換、異常隔離、兼容及安全／效能回歸；不得經擴充重新開啟移除能力或Web播放。 |
| 17 network-webhooks-jobs | 補 G11／G12／G13：網路／代理／隱私、Webhook、排程時區窗口、工作取消／恢復／可觀測完整整合。 | Host／代理／SSRF／DNS重綁及日誌／WS地址洩漏矩陣、Webhook簽章／重試／錯誤处理、跨實例取消、排程与窗口各处理阶段回歸。 |
| 18 stats-web | 補 G23 觀看統計与Web管理呈現，串接进度、使用者与库权限；仍遵循G27無Web播放。 | 統計一致性与并發上报、权限过滤、页面E2E／效能；沒有播放入口或依賴。 |
| 19 diagnostics-docs | 補 G49／G50及G51工具文件：API／OpenAPI／操作／遷移／效能文件、doctor、自檢自癒、告警runbook與工具來源／许可证清單。 | 文件链接与示例CI、doctor正常／故障／恢复矩陣、自癒可觀測、故障診斷与runbook、工具验证及可重建。 |
| 20 hardening-release | 依 G30／G36–G38／G51 与原需求第七節，收斂全部336項與跨阶段安全／部署／交付缺口。 | 空PG初始化／升降迁移、全新clone bootstrap→tools-verify→fixtures→test、完整品牌零非白命中、许可、Linux／Windows／容器／真客户端、安全E2E、全部性能与长稳证据、文件与实现一致；发布动作遵循用户授权。 |

### 各階段的完成與提交規則
- 原始需求第七節「完成判定」是最終門檻；全部 G00–G51 與無G編號的安全／性能要求都要追溯。
- 先記錄該階段未完成行與驗收清單，再交付可解釋子目標；適用編譯／測試通過、來源及終態證據吻合後，更新矩陣、方法／證據、交接與既有PR。
- 本次schema56 WIP先推送是使用者明確要求的交接保存；不改變後續功能完成判定。
- 原24h只對應原來源，後續新來源需要自己的適用驗證；保留原run及所有失敗，完整branding歷史FAIL不得用增量掃描取代。
- 不把已存在的局部功能重標整階段完成。缺失編號 G17／G43／G44 依 requirements-clarifications.md 與追溯矩陣處理，不自行補造需求。
- 不自動merge、tag、release或force-push；完成所有適用證據後再按用户明确授权执行发布。

## Suggested skills
- handoff：接續前讀取本交接，需要再次交接時使用。
- verification-before-completion：發布／宣稱完成前獨立核當前來源、終態、範圍與证據。
- diagnosing-bugs 或 engineering:debug：quota causality／migration／snapshot 失敗時使用。
- engineering:code-review：schema56 guard／交易鎖序／正式 Stage 接線完成後審查。
讀取當前環境中對應 SKILL.md；遵循使用者既有授權，不因技能的推測額外要求確認。