> 本文件隨使用者要求的 WIP 交接提交公開。schema56 最新 legacy 守衛尚未重測，quota fence 因果性未證明，正式 Stage 尚未接線。以下既有 green 只對應各自歷史來源。四套件編譯檢查通過，不是功能或完整驗收。Claude 請從「最重要的待修驗證問題」接續。使用者已要求先推送，因此本次先保存問題；後續功能發布仍須完成驗證。

# Jelee 交接給 Claude：schema 56 未提交工作
記錄日期：2026-10-04（Asia/Taipei）

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

## Suggested skills
- handoff：接續前讀取本交接，需要再次交接時使用。
- verification-before-completion：發布／宣稱完成前獨立核當前來源、終態、範圍與证據。
- diagnosing-bugs 或 engineering:debug：quota causality／migration／snapshot 失敗時使用。
- engineering:code-review：schema56 guard／交易鎖序／正式 Stage 接線完成後審查。
讀取當前環境中對應 SKILL.md；遵循使用者既有授權，不因技能的推測額外要求確認。