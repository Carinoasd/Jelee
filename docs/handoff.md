# 接手记录

## 当前接续（优先于以下历史记录）

使用者要求所有後續PR標題與說明使用繁體中文；PR18已改為繁中。用户再次明确每阶段验证后提交/push普通PR，持续下一阶段直到全部完成，不需阶段间确认。作者仍仅命令级Carinoasd，禁止merge/release/force push和改写旧迁移。

已发布库存scope PR #14（780eee710d）、process观察修正PR #15（37eab90bdd）、来源逐项证明PR #16（d77fb0ecfb）。PR15最终Go CI run36810183721及所有其他PR检查均结束，只有Full branding gate失败：15,278违规/96合法保留；Windows/Linux、PG/race、真probe与NFO全部成功。PR16的全部检查也仅品牌失败。此结论已经向用户说明，不关闭门禁。

schema009来源清单已提交 `3189c4b83960b9e08072089c9ab83a60c462f251`，推送普通 [PR #17](https://github.com/MoYuanCN/Jelee/pull/17)，base `feat/jelee-ignore-observations`，head `feat/jelee-ignore-manifest`。已实现domain proof shape、父链/缺失目录、原子128批次、16,384行和64MiB预算、冲突持久失效且回滚新增前缀、freeze全根验证、冻结128项keyset分页；enabled执行仍关闭。原迁移001–008不改写，009有保留manifest就拒绝down。当前binary schema9，旧测试补充9→8步骤。

本段最终PG race150顶层零skip、Windows25包/521顶层、Linux vet/三build通过；manifest分页10,001行只访问128行/128页。1000/100真NFO混合验收通过，执行在最后仅manifest分页索引优化前，详细边界见docs/evidence/ignore-manifest.json。全部验证命令已结束。已新建接续分支 `feat/jelee-ignore-baseline`，从PR17继续基线三态分类/合并、来源复核seal和实际枚举接线；后续实现见下方基线分类阶段。用户明确要求持续到全部完成，已建立active goal，不在每个PR后等待用户确认。需求状态不提前提高。
### 基线分类存储阶段（2026-10-01）

基线分类已提交21b543cefc1895856fbb9e8f7df8454d126f1fb4并推送普通PR #18（https://github.com/MoYuanCN/Jelee/pull/18），已附聊天，base为feat/jelee-ignore-manifest。当前接续分支feat/jelee-ignore-verification从该提交开始，后续生产代码尚未修改。schema010 已实现基线观察版本、持久三态分类、先取128条原始页再筛未见项、精确前缀提交和可跨重新领取的回执、库存冻结触发器、祖先证明验证及历史清理/降级保护。成功普通扫描推进观察版本；失败/取消保持原样。001–009未改。enabled领取和成功执行仍关闭。

本段完整 PG race 157顶层零skip通过，Linux vet/三build通过，Windows domain/PG包测试及vet通过（Windows PG未配置，不能当集成证据）。源码验证期间不变。首轮154通过、3顶层失败，是旧测试未区分新增版本字段；修正后完整重跑通过。证据见 docs/evidence/ignore-baseline.json，合同见 docs/ignore-baseline.md。没有本地命令待轮询。

这批持久分类已作为独立可审查阶段发布PR #18，后续继续保护旧排除项的合并、图片同资格统计、来源复核和租约seal，再接原生枚举/worker/API/CLI。领域比较计算器尚未用于发布，不把分类完成当作实际扫描完成。全部336项需求状态不提前提高，active goal继续。

PR17全部检查已经结束：push与PR两轮Go Windows/Linux/PG成功，其他实际执行的C#/ABI/格式等也成功；仅两个Full branding gate失败，条件不符的工作流跳过。仍有15,278违规/96合法保留，不关闭检查。
### 來源復核實作（最新接續）

來源復核已提交82a70b1a7b，推送繁中普通PR #19（https://github.com/MoYuanCN/Jelee/pull/19）並附聊天；base是feat/jelee-ignore-baseline。目前接續分支feat/jelee-ignore-publication，尚未修改發布階段程式。已交付 schema011、領域頁面/租約 token、Begin/Next/Commit/Seal repository 流程。固定120秒或原始租約較短期限、seal最多30秒、不重複延長、新世代從頭復核、來源變更整份失效、逐欄摘要鏈、最後寫入後再核對期限。尚未接保護合併或原生 worker，enabled 執行守衛仍關閉。歷史遷移測試補11→10，001–010未改。

第一輪5項PG專項race通過、零skip。後續補完整/unknown/reset與scope/cancel/lease測試；Windows兩套件測試及vet通過。最終Linux全模組vet及三build通過，sourceUnchanged=true，證據在.testdata/verification-final-native-checks-summary.json。完整PG race 164項頂層測試通過、零skip，來源未變。全部本地程序已结束，證據見docs/evidence/ignore-verification.json；PR19已發布，接續實作保護合併和圖片統計。

PR18標題/說明已按使用者要求改為繁中，後續PR都必須繁中。最新遠端檢查：Windows/Linux foundation與C#/ABI等成功，一輪PG成功、另一輪PG仍執行；只有品牌檢查已確認失敗。未把執行中當作失敗。
### 保護合併與發布（最新）

保護合併已提交725d740639並推送繁中普通PR #20（https://github.com/MoYuanCN/Jelee/pull/20），已附聊天，base為feat/jelee-ignore-verification。現接續分支feat/jelee-ignore-native-scan，尚未修改原生掃描碼。此段新增 ignore_publication.go 與四項整合測試；NFO完成檢查抽取內部helper，原有requireIgnoreOff仍保留。FinishIgnoreJob核對完整分類、scope/revision、有效seal和既有NFO/probe完成狀態，原子完成圖片統計、保護舊excluded行、觀察版本推進、當前庫存、terminal/audit/history，最後核對租約/epoch/seal。unknown可結束為review但Missing0、圖片uncompared且不發布；無unknown的發布必須seal。同scope只保留excluded舊觀察版本；新scope完整重置不保留舊項。一般FinishJob/claim守衛未開放。沒有新增migration。

首輪3頂層通過、容量測試因max_entries=1低於合法最小100而失敗；已改101筆合併對100上限，增加ErrScanLimit明確斷言。Windows兩包test/vet通過，Linux全模組vet/三build通過，來源雜湊不變。完整PG race 168項頂層測試通過、零skip、sourceUnchanged=true。全部本地程序已結束，證據docs/evidence/ignore-publication.json。PR20已發布；接續原生同句柄ReadDir/absence/重新觀察與worker接線。

原生來源實作在internal/adapter/media/ignore（package ignoresource），不是adapter/ignoresource；directory介面只有Stat/OpenDirectory/OpenRule/Close，尚無ReadDir。Linux nativeFile已持有嚴格openat2句柄，OpenDirectory現有absent不允許。普通scan另用os.Root，不能用兩個獨立開檔冒充同句柄枚舉；下階段須增加實際同handle enumerator與明確child absence能力。
### 原生同句柄列舉（最新接續）

原生列舉已提交4eb2d53995並推送繁中普通PR #21（https://github.com/MoYuanCN/Jelee/pull/21），已附聊天，base為feat/jelee-ignore-publication。現接續分支feat/jelee-ignore-reobserve，尚未新增重新觀察程式。已新增Resolver.ScanDirectory：按目錄編譯祖先規則，以提供最終目錄proof的同句柄列舉，128項批次，每項附Match；來源重開/完整hash核對及所有句柄關閉後才Done。callback錯誤原樣傳回、取消關閉reader、兩slots/30秒/累積編譯限制沿用。DirectoryProof回呼副本不能變更內部chain。尚未接worker/PG/API。

新增私有scanDirectory/ReadEntries/OpenDirectoryOrAbsent。Linux用Readdirnames避免os.NewFile隱藏name導致DirEntry.Info路徑查詢，子項用同FD fstatat NOFOLLOW。Windows ReadDir metadata實作已查本地pinned Go源碼，Info直接回傳handle列舉屬性。Absence只接受已開父的合法單一child且原生ENOENT/NAME_NOT_FOUND；unsafe/closed/non-directory不可降級。

Windows來源+architecture測試及來源vet通過；原生Linux /tmp快照48頂層race+vet通過，0 skip，87.2%覆蓋率，sourceUnchanged。證據docs/evidence/ignore-native-scan.json。Windows最初root replacement測試嘗試移動持有child handle的祖先而OS拒絕（升權亦同）；已改直接列舉/替換root自身，nested directory另測，兩者通過。所有執行命令結束，沒有待輪詢session。

PR21已發布，接續加入public舊基準祖先缺失與重新觀察接口，把同句柄批次保存到PG（proof+kept inventory/excluded provenance），最後接worker/claim/API/CLI及真實素材驗收。Private absence primitive尚不能當公開缺失proof。PR20最新功能checks已過Windows/Linux/C#/ABI，但兩輪PG仍live，只有branding已確認失敗，勿將pending當失败。
### 來源重新觀察（最新）

來源重新觀察已提交f8620d5cf8並推送繁中普通PR #22（https://github.com/MoYuanCN/Jelee/pull/22），已附聊天，base為feat/jelee-ignore-native-scan。現在接續feat/jelee-ignore-scan-batches，尚未新增批次程式。新增EvaluateBaseline：共用原Evaluate的有界流程，允許嚴格native父句柄的確定child absence，重開整鏈後再次確認absence，回傳追加MissingDirectory proof；缺失路徑與父身份納入token。一般Evaluate仍嚴格拒絕缺失父目錄。DirectoryProof新增MissingDirectory，所有舊觀察預設false。

新增ReobserveDirectory：不依規則排除來隱藏指定目錄，兩次完整讀/hash身份/proof，第一個缺失祖先終止；不同長度/欄位或來源出現都ErrChanged且nil結果。保留兩slot/30秒/路徑和來源預算，close錯誤清空結果。

Windows來源+architecture測試及來源vet通過；原生Linux /tmp快照53頂層race與vet通過，0skip，86.8%覆蓋率，sourceUnchanged。證據docs/evidence/ignore-reobserve.json。所有程序結束，無待輪詢handle。

PR22已發布；接續實作PG同句柄filtered scan batch（目前SaveScanBatch仍拒enabled）、持久被排除項與來源行報告、再接worker：原生ScanDirectory→證明/包含庫存/排除項；EvaluateBaseline→精確基線分類；ReobserveDirectory→復核頁；Seal→FinishIgnoreJob。全部完成並驗收前不打開enabled claim/API。PR21目前Go雙平台已通過、PG和部分C#仍執行，只有branding已確認失敗。
## 2026-10-01 切换思考强度历史接续点

用户说明当前 ultra，并问「现在我可以降低吗」。已告知可改 medium，修改已保存，切换后说「继续」接着验证和推送。此前按小段验证／push／普通 PR 的授权仍保留。

本轮优先修复 PR #13 真媒体验收暴露的死锁，C2 生产实现尚未开始。当前分支 `feat/jelee-ignore-comparison`，HEAD 仍为 `89362b5a4569197c5d728dfbd4d4d85442dd0c08`，**新修正未提交／未推送**。

- 根因：authorizedJobs 先锁 actor user/session 再等 jobs advisory；heartbeat 第二次 UPDATE 会重新检查 actor FK，反向等待 user。官方 PostgreSQL 实现支持此原因，受控 PG 交错测试已重现 GET storage unavailable。
- 修正已落盘：accounts.go 抽取交易内 actor 验证；jobs.go 改为 account advisory → jobs advisory → actor rows。保留 live session/admin 条件、FOR UPDATE、rollback，不增重试或放宽时限。
- 新增 `internal/adapter/postgres/jobs_lock_order_test.go`。使用 own-schema pg_locks waiter barrier，与实际 GetJob 并行，验证 heartbeat 两次 UPDATE 和 reader 均成功、reader join。
- RED：`.testdata/inventory-lock-red-postgres.jsonl`／summary，旧生产码失败、0 skip。GREEN：相同 Linux pinned Go race 测试通过、1顶层、0 skip；sourceUnchanged=true。完整 PG/race 已启动session `26771`（`.testdata/run-inventory-scope-pg.py fullpg` → `.testdata/inventory-fullpg-postgres{.jsonl,-summary.json}`）；Linux vet/build 已启动session `13029`（`.testdata/run-jobs-lock-native-checks.py` → `.testdata/jobs-lock-native-checks{.txt,-summary.json}`）。接续只读最终输出/summary，不要重跑已完成命令。
- Windows normal-host：三build、lint/vet、全模块 test命令退出0。25个有测试package通过、4个无测试package；510顶层测试通过；189个PG skip（Windows无DSN）及14个工具/平台skip。日志 `.testdata/jobs-lock-windows-{build,lint}.txt` 与 `jobs-lock-windows-tests.jsonl`。不声称 Windows PG/race通过。
- media_guardrails 未启动实际1000媒体验收：automatic approval review 在执行前拒绝 WSL PostgreSQL/Docker 写入，理由是子代理上下文仍被判定read-only。没有session，也未重试或绕过；用户既有完整验证授权和root后续明确测试委派应在接续时用于审查该拒绝，不要默认另索授权。ignored orchestration 已有 `.testdata/run-lock-order-media-acceptance.py`，需先审查限定自建schema/命名Docker资源/只读fixture后再执行。现有脚本为 `scripts/test_probe_worker.py`、`scripts/test_nfo_worker.py`，凭证只读 `.testdata/database-url`，不能回显。
- `docs/jobs-lock-order-verification.md` 是初稿，尚待按上述及最终实测更新。它记录失败PR run36806931736、同HEAD成功push run36806927198的区别；两轮完整品牌均失败。不能用另一轮通过掩盖死锁。
- 后续先完成hotfix验证、选择性提交 accounts.go/jobs.go/jobs_lock_order_test.go及相应证据文档，更新PR #13的 `feat/jelee-ignore-inventory`（保持fast-forward），再继续C2。当前README/handoff与两份C2计划未提交，勿误混进源码hotfix。
- C2候选测试已保存 `.testdata/pending/inventory_scope_test.go`，SHA256 `ab9df7742f1579dd3991002840f1965e7b0efea30e8d64ef217a4e7402fea758`；未跑且预期当前会失败。不要纳入hotfix测试套件，修复CI后恢复到 postgres 包开展 RED。
- C2计划在 `docs/ignore-comparison-design.md` 与 `docs/inventory-scope.md`，明确为未实现。完整三态／manifest／FS worker均未完成，不新增schema009。
## 当前工作与授权

2026-10-01 用户明确「继续吧」，此前切模型暂停已结束；「回报进度」及「项目中最大的是3阶段吗」是状态查询，未撤销继续授权。用户授权每小段验证、提交、推送、建立普通PR并附到聊天，然后继续下一段；没有授权合并、发布、tag或改写历史。

3D1B已推送普通[PR #12](https://github.com/MoYuanCN/Jelee/pull/12)并附聊天，HEAD `e9aea71540ea4add86b2ebeabfb81f7e764ba1b1`，源码 `760e02f299ad11d04219fd19fd83b7c457622f94`，发布分支 `feat/jelee-ignore-source`，基于PR #11的 `32e3c85ca3a010641858007d11382557cb2fe7ac`。Windows29包通过，新来源包89.1509%；原生Linux29包604顶层race通过、PG零skip，新包91.4948%；342个已提交blob与两平台验证SHA一致。详见[来源与缓存验证](ignore-source-verification.md)。矩阵4已完成/173部分/159阻塞；仍未接扫描，下一段3D1C先做持久合同，再接worker/report。[PR #12 Go CI](https://github.com/MoYuanCN/Jelee/actions/runs/36804175667)功能门禁均成功，完整品牌仍为15,278残留/96允许而失败，已保存CI证据。当前分支 `feat/jelee-ignore-inventory` 从该HEAD完成3D1C1持久意图与执行守卫，源码 `b07ad3a6f20508c43b17458cd6847451ff6d68ab`，schema008；Windows29包、原生Linux29包627顶层race（PG131顶层零skip）通过，350源blob一致，报告见[本段验证](ignore-inventory-verification.md)。已推送普通[PR #13](https://github.com/MoYuanCN/Jelee/pull/13)并附聊天，HEAD `89362b5a4569197c5d728dfbd4d4d85442dd0c08`，base为 `feat/jelee-ignore-source`。[Go PR CI](https://github.com/MoYuanCN/Jelee/actions/runs/36806931736)已结束：Windows/Linux foundation成功，PostgreSQL步骤中的integration/race成功，但真实probe worker验收失败（待根因调查），后续NFO验收未执行；完整品牌门禁也失败。下一段生产修改暂缓，先调查worker验收artifact。新分支 `feat/jelee-ignore-comparison` 从该HEAD开始C2三态基线比较，尚未修改C2生产代码/schema；C3才接FS/worker。

3D1A已推送普通[PR #11](https://github.com/MoYuanCN/Jelee/pull/11)并附聊天；[Go CI](https://github.com/MoYuanCN/Jelee/actions/runs/36801016335)的Windows/Linux/PG、真实媒体验收均成功，完整品牌门禁仍失败。纯matcher源码 `9afc8f15c6ca3b190bc6eafdb7e24356ad1cd33c`，匹配合同和Git差分见[报告](ignore-matcher-verification.md)。

3C3C已发布普通[PR #10](https://github.com/MoYuanCN/Jelee/pull/10)并附聊天；源码 `47da27b5ff090a71e6f55aae0b2e6c00871f825b`。[Go CI](https://github.com/MoYuanCN/Jelee/actions/runs/36798696038)的Windows/Linux、PG集成与真实媒体/NFO混合库均通过，完整品牌检查仍失败；[CI证据](evidence/nfo-worker-ci.json)已保存。

作者仅用命令级 `Carinoasd <46304809+Carinoasd@users.noreply.github.com>`，不设置全局身份。原需求逐字保存在 `requirements-source.md`，SHA256 `755b6b32324efe710c3e1135a0c982c45b82f337e90fcb50ab3718a20cba5d07`。

## 已交付与本段

- 基础服务和账户：[PR #1](https://github.com/MoYuanCN/Jelee/pull/1)（草稿）。
- 3A持久只读盘点：[PR #2](https://github.com/MoYuanCN/Jelee/pull/2)。
- 3B1工具、3B2程序/素材：[PR #3](https://github.com/MoYuanCN/Jelee/pull/3)、[PR #4](https://github.com/MoYuanCN/Jelee/pull/4)。
- 3C1隔离探测：[PR #5](https://github.com/MoYuanCN/Jelee/pull/5)，runtime冷下载修复后功能CI通过。
- 3C2A持久cache契约：[PR #6](https://github.com/MoYuanCN/Jelee/pull/6)，源代码 `3d8842be1e`。该PR的PG与Windows CI通过；Linux后续暴露新增identity摘要函数缺少测试，当前3C2B补测后原生sandbox门槛86.8%/零skip通过。完整品牌门禁仍失败，不降低门槛。
- 3C2B已交付[PR #7](https://github.com/MoYuanCN/Jelee/pull/7)：schema5持久probe请求、worker、重建、能力降级、API/CLI与维护。源码 `b59be8d389216c3853a0a6d18b1f1af130745a9f`；CI暴露跨parent的测试误判，`ef115c27a0`保留own-parent join并修正测试。HEAD `893432fc51` 的[CI](https://github.com/MoYuanCN/Jelee/actions/runs/36789351642) Linux/Windows/PG真实媒体验收全部通过，仅完整品牌仍失败。完整本地证据见[worker验证](probe-worker-verification.md)。
- 3C3A已交付[PR #8](https://github.com/MoYuanCN/Jelee/pull/8)，源码 `feeee03da801bb4730eca86b39d55eb5b1521783`、HEAD `082a51dc2b`：NFO完整内容指纹、来源身份核对、私有原文与独立解析，现有CLI已接入。Windows build/lint/test/真CLI与原生Linux race/真CLI/基准通过，NFO90.5%、Linux66个顶层测试零skip；Windows1个symlink权限skip，未跑Windows race。[远端Go CI](https://github.com/MoYuanCN/Jelee/actions/runs/36790288784) Linux、Windows、PG与真实媒体验收通过，完整品牌仍失败。见[NFO来源验证](nfo-source-verification.md)。矩阵仍4已完成/168部分/164阻塞。
- 3C3B已验证，源码 `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`：独立NFO库级模式/generation、安全验证摘要、schema006有界快取与parent租约/检查点。Windows全模块27包通过；原生Linux三build/vet/race、27包474顶层测试通过，PG零skip，另6个专用媒体/沙箱skip明确列出。NFO90.4%、PG80.6%、新NFO PG83.53%。见[NFO快取验证](nfo-cache-verification.md)。本段发布分支为 `feat/jelee-nfo-cache`，后续按[3C3C计划](nfo-worker-plan.md)接worker/API与图片统计。矩阵现4已完成/170部分/162阻塞。

## 不变量

3C3C 已交付的源码包含schema007、NFO入队/worker/API/CLI、当前观察与图片属性比较。[实际验证](nfo-worker-verification.md)：Windows三build/lint/test 27包通过；原生Linux三build/vet/race 27包544顶层测试，PG零skip，另6个专用环境skip。1,000与100混合库、真实取消/图片基线保护/恢复、SIGTERM清理以及同版1,000影片回归全部通过。矩阵仍4已完成/170部分/162阻塞；完整品牌和全项目覆盖率尚未达标。

1. 当前binary只接受clean schema10；C1历史版接受schema8；3C3C为schema7，3C3B为schema6。迁移000001–000007原文不变；008 down拒绝任何仍保留的enabled ignore intent（包括terminal），先停所有旧worker，不支持schema7/8进程混跑。007 up拒绝活动的无请求B read-only phase，down拒绝所有活动C request。006/005另有NFO/probe回退保护，须先结束/取消并停worker。失败迁移可能dirty，不能自动force。
2. root path只来自本地CLI登记的数据库媒体根；HTTP只接收登记ID。用户原媒体/NFO/图片不写入；不启用ffmpeg生产回退或转码。
3. 默认probe关闭，disabled节点不领probe任务；缺工具仅停用相关能力。健康状态在启动时验证，readiness不每次执行工具。
4. 持久request保存可信identity和enqueue generation；重放不失效、不repin，disabled/runtime故障仍可重放保留请求。运行恢复先读phase，从连续检查点继续。
5. 最大16项hit批次；miss每parent一项。先本地gate后DB lease。正/负结果保存前finalInspect；父取消、丢租约或工具故障不被记为坏媒体。心跳≤min(parent,fileTTL)/3，DB timeout更短。
6. 每次维护最多128项/2秒，每60秒一次。关机先cancel/join worker与维护，再清自己的scratch和DB。能力与公开summary不泄漏绝对路径、raw JSON、stderr、工具身份或凭证。
7. Windows正式probe仍停用；Linux测试缺必需工具必须失败。Windowsrace尚未执行。各阶段证据不能替代完整336项、codec、规模或24小时验收。

## 本地环境与下一段

Go1.27.1与媒体工具都在项目 `.tools`；Windows通过 `scripts/run-go.ps1`/`scripts/make.ps1`，Linux通过 `.bin/go`。不安装全局工具。独立PostgreSQL只用 `jelee_test` 的自建schema；凭证只读 `.testdata/database-url`，不得回显或提交。

3C3C契约见[NFO工作流程](nfo-worker.md)。NFO policy generation独立于视频probe generation；inventory epoch核对根映射，迁移前图片属性保持未知。来源SHA只描述保留字节，不能保证敌对原地写入下的原子快照。3D1A的[纯matcher](ignore-matcher.md)已经验证；[旧规则来源审计](ignore-source-audit.md)与自有语法分开。3D1B接安全来源/cache，3D1C接持久扫描，不能把过滤掉的旧路径误计为缺失。完整NFO优先级/锁合并、写回、监看、排程、图片处理和完整规模验收尚待后续交付。

3C3B曾发生Windows沙箱ACL失败与Linux专用PG退出后自动移除，原因和恢复记录在旧报告。3C3C的Windows fmt-check超过长命令行上限，改为目录递归后通过；第一次native全套因验证期间这项脚本变化而拒当最终快照，冻结后全量重跑通过。PG容器保持2GiB tmpfs且退出不自动删除；凭证仍仅在忽略文件中。

### 忽略掃描批次（最新接續）

分支 feat/jelee-ignore-scan-batches 接續 PR22，新增 schema012 與 NextIgnoreScanDirectory/SaveIgnoreScanBatch。同交易保存祖先證明、包含庫存與排除來源；重送、部分目錄重掃、容量和租約回滾均驗證。比較後凍結排除紀錄。初輪歷史 DELETE jobs 被目錄外鍵阻擋，未發布的012改為 ON DELETE CASCADE；四項聚焦測試通過，再新增凍結測試。

最終完整 PG race 173 頂層通過、零 skip、223.432 秒、sourceUnchanged=true。Linux 全模組 vet/三 build 與 Windows domain/postgres test/vet 通過；Windows 未配置 PG 不作整合證據。全部本地驗證程序已結束。證據 docs/evidence/ignore-scan-batches.json，介面合同 docs/ignore-scan-batches.md。原遷移001–011未修改。

PR22 最新遠端功能檢查全數通過，僅兩輪 Full branding gate 失敗；門禁保留。本階段已提交 1ad4ac9063 並推送普通繁中 PR #23（https://github.com/MoYuanCN/Jelee/pull/23），已附聊天。接續分支 feat/jelee-ignore-worker-bridge，串接 native source→domain 批次/分類/重新觀察橋接與 runner。enabled claim/API 仍未開放；全案需求狀態不提前提高。

接續分支已新增（未提交）app.IgnoreInventoryScanner 及 scan.IgnoreScanner，使用 native Resolver 將同句柄 proof、包含 metadata、排除來源轉成 domain.IgnoreScanBatch；沿用普通掃描副檔名分類，callback 錯誤原樣回傳，來源變動/容量錯誤轉成 domain 錯誤。Windows scan+architecture 測試通過，新增真目錄測試驗證祖先與本地規則來源、排除目錄不排隊、保留大小/類型、callback error。尚需 Linux 原生驗證、分類/重新觀察橋接、runner/claim/API 接線與完整端到端測試；不可把此初步橋接宣稱階段完成。沒有執行中的本地測試程序。

### 原生工作介面橋接（最新）

新增 IgnoreBaselineObserver 與 EvaluateIgnoreBaseline/ReobserveIgnoreProof，包含判定仍須 PG unseen/coverage 核對，讀取錯誤不轉成缺失；較早祖先缺失不偽造目標 proof。scan.IgnoreScanner 沿用原生列舉，同句柄 proofs 映射成 domain，包含/排除 metadata 保留。

Windows 四相關套件 test 與 scan/app vet 通過；Linux 原生 scan race 16 頂層、0 skip、86.3% coverage、vet 通過。新增 TestIgnoreNativeBridgePublication 真目錄→PG保存→原生重新觀察→seal→發布的 stable/source-change 兩子案例，race 通過，1頂層、0skip、17.359秒。來源變動拒絕發布。測試使用 manufactured lease，正式 claim/runner/API 未完成；不可當正式排程端到端驗收。全部程序已結束。證據 docs/evidence/ignore-worker-bridge.json。

下一階段串接 runner 的 ignore inventory/baseline/verification/publication；需補租約核對的 request/root lookup，以及 NFO/probe 對已完成過濾庫存的安全准入。普通 off 路徑與能力不足 worker 不得誤領 enabled。完成後再開放 API/CLI，持續全案 G00–G51。

工作介面橋接已提交 592c98f5c7 並推送繁中普通 PR #24（https://github.com/MoYuanCN/Jelee/pull/24），base feat/jelee-ignore-scan-batches，已附聊天。接續分支 feat/jelee-ignore-runner，正式 runner 接線尚未修改。

runner 接續已新增未提交的 app.IgnoreExecutionRepository，以及 Store.ReadIgnoreRequest/ReadIgnoreRoot。所有讀取使用 jobTransaction/fencedJob，取消拒絕，回傳前 guardedJobUpdate；root 額外 ignoreManifestFence/epoch 與 library_id 限定。TestIgnoreExecutionPrivateReadsFenced 最終使用同一 DB schema 第二媒體庫，驗證正常讀取、跨庫與舊 generation 拒絕；Linux PG race 1頂層/零skip/16.208秒通過（execution-read-isolation 日誌），sourceUnchanged=true。先前 execution-read-first 測試通過但跨库用另 schema，已補強，不以舊證據取代最終。全部測試程序已結束。

正式 runner 尚未修改。入口 internal/platform/jobs/runner.go：loop 在171附近 capability claim，run 在251附近啟動heartbeat再 execute，終止時 FinishJob；executeInventory 在365附近。NFO/probe admission requireIgnoreOff 仍封鎖 enabled，需精確改成驗證已完成的 filtered inventory，而非直接刪守衛。ReadIgnoreRequest 可支援 off nil，但缺失或不一致 enabled request 必須 error，不能 fallback。

PR23最新功能CI：Windows/Linux與C#/ABI通過，一輪PG通過、另一輪pending；只有branding已確認失敗。PR24剛發布，尚未確認最終CI。

### 正式 runner 接線進行中（尚未提交）

本輪新增 jobs.IgnoreOptions 與 ignore.go：execute 先從具 IgnoreExecutionRepository 的主 repository 讀取請求，off 維持原流程；enabled 驗證請求/選項後，依 ReadIgnoreProgress 決定是否重掃，再分類原始頁、保存新來源證明、執行 metadata stages、重新讀持久 unknown、復核各頁及 seal。讀取錯誤僅 ErrIgnoreUnavailable 分類為 unknown；來源變動/超限等中止。新增 IgnoreExecutionProgress/ReadIgnoreProgress 供恢復使用。

nfo.go 原 execute 改 executeStages(...inventoryDone)，probe.go 增 executeProbeStages(...inventoryDone) 保留原 wrapper；filtered inventory 完成後不走普通掃描。runner.run 首次成功終止改 r.finishJob，會依 ReadIgnoreRequest 分流 FinishIgnoreJob；失敗/取消仍原 FinishJob。Options.Ignore 做基本 nil 驗證/值複製。

Windows jobs 全包測試、相關PG/architecture編譯測試、jobs/PG vet通過。新增三頂層 worker 測試：持久unknown恢復/不重掃、保存錯誤即使scanner吞掉仍回傳、missing/duplicate Done、成功/失敗終止分流。尚未跑 Linux runner race 或完整PG，不能聲稱此段可正式啟用。

下一步：NFO/probe 存取目前仍 requireIgnoreOff，需新增只允許 complete comparison+valid manifest+current epoch 的 metadata 准入；普通 FinishJob 成功必須繼續拒enabled。完成此檢查及測試後再開 capability claim，cmd 初始化 IgnoreOptions、公開intent與API/CLI。另需補真正 runner+PG+native E2E，包括重新領取、取消、來源變動、unknown review、混合 NFO/probe。正式 claim 仍關閉，所有上述修改未提交，沒有活躍測試程序。不要提早發 PR 或提高全案需求完成狀態。

### metadata 准入與正式領取已接線（仍未提交）

新增 requireMetadataInventory/requireMetadataNFOFinished：enabled 必須 comparison completed、manifest未invalidated、inventory epoch與baseline revision吻合才允許 NFO/probe；off 原樣，損壞ignore request拒絕。替换 NFO read/execute 與 probe read/execute 的 guard；普通 FinishJob 仍使用 requireNFOFinished→requireIgnoreOff，不允許繞seal發布。

ClaimJobWithCapabilities 現在按 Ignore bool 開放合法 enabled request：jobs marker與row須同時存在、library吻合、mode/case合法、program/proof版本吻合。無能力 wrapper仍不領取；缺失marker/row仍不領取。舊C1測試中原本advertise true仍期待永久不執行的案例改成ignore=false，保留無能力/重試/恢復測試；損壞row測試仍使用true。runner claim宣告options.Ignore是否存在；New在Ignore配置時要求主repo實作IgnoreExecutionRepository。

新增 TestIgnoreRunnerNativeClaimAndPublish：真正 Start→capability claim→native臨時根掃描→PG→復核→seal→發布，檢查排除項不進基線，無manufactured lease；Linux race通過。TestIgnoreMetadataAdmission驗證未完成拒絕/valid允許/invalidated/epoch/revision拒絕/ordinary success禁止。第一次唯一失敗是epoch案例預期錯誤順序錯（既有FinishJob更早回ErrInventoryInvalidated），修正預期保留guard。最終runner-admission-first：14頂層通過/0skip/30.637秒。Windows jobs/PG/architecture測試通過。所有程序結束。

仍待：mixed NFO/probe真worker整合、租約重新領取/取消/unknown/source-change等worker整合、完整PG regression、Linux jobs race、全模組vet/build。runtime.New 尚未配置IgnoreOptions，因此正式程式尚未啟用；公開API/CLI與報告也待做。上述修改保留在feat/jelee-ignore-runner未提交，不要提早宣稱此段完成或新建PR。

### 正式 worker 階段驗證完成（最新）

Windows/Linux runtime 已配置 IgnoreOptions。新增真worker測試涵蓋正式能力領取與發布、真NFO讀取且排除錯誤NFO、Release後重新領取已開始比對、重新觀察前規則變動拒絕發布。未新增migration，schema仍12。

最終完整PG race 180頂層/零skip/220.613秒/sourceUnchanged=true；Linux jobs race通過，全模組vet/三build通過且來源不變。Windows runtime/jobs/PG/architecture test與相關vet通過，Windows PG無DSN非整合證據。原LICENSE/需求/go.mod/go.sum雜湊保持。證據docs/evidence/ignore-runner.json，合同docs/ignore-runner.md。全部命令結束，無待輪詢handle。

本段準備提交普通繁中PR，後續仍須API/CLI忽略intent與排除報告、混合外部probe真媒體驗收、正式取消與unknown review端到端情境。不能把worker階段當3D1或全案完成。PR24目前所有功能CI通過，僅品牌gate失敗，門禁保留。

正式worker已提交73df751081，推送繁中普通PR #25（https://github.com/MoYuanCN/Jelee/pull/25），base feat/jelee-ignore-worker-bridge，已附聊天。接續分支feat/jelee-ignore-api。先檢視 app.Jobs.SubmitScanStages（internal/app/jobs_nfo.go）與 HTTP scan request，再加明確 IgnoreIntent，保留off預設/嚴格JSON/重試凍結。API開放前需處理執行能力不可用時新請求與舊重送的差別；不要使用使用者輸入的解析器身份。後續CLI/OpenAPI/排除報告與真probe驗收仍待做。

### API/CLI 忽略請求進行中（未提交）

新增 app.IgnoreAdmissionRepository 與 PG SubmitScanWithIgnoreCapability/RetryScanWithIgnoreCapability；原 submitScanJob 保留wrapper，轉新submitScanJobWithIgnore(...available)。能力檢查位於授權既有replay與parent intent載入之後，新請求不可用拒絕、同key重送仍允許、重試沿用父ignore且新run需能力。TestIgnoreAdmissionAvailabilityReplayAndRetry Linux PG race 1頂層/零skip/16.86秒通過（api-admission-first），無活躍程序。

app.ScanServices/Jobs 增IgnoreAvailable，SubmitScanOptions接IgnoreIntent，原SubmitScanStages包裝off。支援新port時submit/retry使用capability方法；不支援時enabled拒絕。runtime依Windows/Linux提供能力函式。HTTP ScanRequestBody新增可選ignore物件(mode/caseMode)，非空物件須jeleeignore及明確case；未知欄位仍嚴格JSON拒絕。OpenAPI IgnoreIntent兩欄required。ErrIgnoreUnavailable映射503 ignore_unavailable，補en-US/zh-CN/zh-TW/ja-JP翻譯（首輪HTTP測試抓缺翻譯，補後全過）。新增HTTPtest檢查意圖、能力下降仍送repo做replay、invalid/unknown欄位不進repo。

CLI jobs scan新增 --ignore jeleeignore --ignore-case sensitive|ascii-insensitive，必須兩者都有效，off預設仍不帶欄位。既有authenticated body測試新增ignore案例。最終Windows CLI/HTTP/app test通過，前一輪runtime/i18n也通過，相關vet通過。未跑此階段Linux完整回歸/全PG/三build；CLI invalid flags/no-network與HTTP授權/503/大小寫/空值邊界仍可補強。JSON ignore:null現等同omit，OpenAPI尚未明確此行為，需決定並對齊。

還需API實際PG串接驗證、排除報告（授權/分頁/來源行/未知結果/限制），及混合外部probe真素材验收。全案需求不提高。本分支feat/jelee-ignore-api未提交；最新已發PR25，還未查最終CI。全部測試程序已結束。

### API/報告補強進度（未提交）

本輪確認 strict_json.go 會統一拒null；先前handoff所述ignore:null等同omit不正確（只看Go pointer推論），現已加HTTP測試證實400，OpenAPI仍ref不可null。省略ignore才off。HTTP補無token401/非admin403不進repo、ASCII case傳遞、不可用503；CLI補不完整/錯誤ignore flags在讀token前拒絕。Windows HTTP/CLI/app/runtime全部通過。

新增 domain.IgnoreReport/Entry 與 GetIgnoreReport(parent,actor,id,limit,cursor)。僅terminal jobs可查（queued/running ErrConflict），authorizedJobs及結尾probeAdminStillLive。scan exclusions與baseline decisions分別標source，各取limit+1、union排序有界；opaque cursor canonical base64url JSON綁job/source/root/path，limit1..100。報告含狀態、enabled/review/invalidated、排除file/dir計數、unknown與來源行/匹配路徑。無絕對根路徑。取出entry再ValidateIgnoreReportEntry以拒絕損壞/非法相對路徑。

report-first Linux PG race 2頂層/0skip/18.061秒通過：terminal分頁/來源、跨job cursor、匿名actor/limit拒絕、unknown review原因保留。該次通過在最後新增entry validator之前，需針對最終版本再跑。新增app.IgnoreReportRepository/Jobs.IgnoreReport與GET /api/v1/jobs/{id}/ignore，沿用admin與jobPageQuery。新HTTP報告route尚未加專用測試/OpenAPI/schema/CLI輸出校验，也未做大資料query plan與撤銷session測試。全部程序結束。

接續優先完成報告HTTP/OpenAPI/CLI與實際PG串接，再Linux/fullPG回歸、證據與繁中PR；目前整批feat/jelee-ignore-api未提交，最新PR仍25。

### 排除報告 HTTP 與 OpenAPI 驗證（仍未提交）

新增 ignore_openapi.go，由 nfoSpecification 在 jobs 啟用時接入：IgnoreReport/IgnoreReportEntry、terminal-only GET /api/v1/jobs/{id}/ignore、admin 授權、opaque cursor 4096 字元、limit 1..100、預設50、來源行與相對路徑、失敗/取消可能僅部分觀察、各 source 計數含義與歷史清理限制。

新增 TestIgnoreHTTPReportAuthorizationAndPaging 與 TestIgnoreReportOpenAPIRolloutAndContract。首次HTTP測試查到 route 使用 accountEndpoint(true,false) 會拒絕分頁query；改成(true,true)，仍由 strictQuery 拒絕未知或重複欄位。涵蓋無登入/非admin不進repo、預設/opaque分頁、limit邊界、repo撤銷授權錯誤映射、queued conflict、能力不可用及關閉jobs時404。

Windows HTTP/app/CLI test 通過，相關 vet 與 diff --check 通過。最後 entry validator 的 Linux PostgreSQL race 已補跑：report-validated，2頂層/零skip/18.113秒/sourceUnchanged=true，證據 .testdata/inventory-report-validated-postgres-summary.json。全部程序已結束。

已查 PR25：所有功能CI通過，Full branding gate仍失敗，未取消門禁。CLI報告命令仍未實作；大資料query plan、真PG撤銷session測試、API實际PG串接、全PG回歸與本階段證據/PR仍待做。下一步讀 cmd/jelee-cli/jobs.go 加 jobs ignore --id --cursor --limit 與白名單回應校驗；勿把目前HTTP完成視為整階段完成。

### CLI 排除報告與真實授權撤銷已驗證（仍未提交）

新增 cmd/jelee-cli/ignore_jobs.go：jobs ignore --id --limit --cursor 經管理員 HTTP 讀報告；cursor為opaque，只檢查長度4096及base64url傳輸字元，不依賴PG私有編碼格式。回應先白名單投影header，再逐entry投影並ValidateIgnoreReportEntry；拒非terminal、負數、null、超頁大小、非法路徑/規則來源/理由、重複欄位；輸出不帶未知欄位或大小寫別名。下一頁cursor非空時要求滿頁。

新增 ignore_jobs_test.go 覆蓋固定GET路由、bearer、query、公開輸出、惡意回應與錯誤flags在讀token前拒絕。Windows CLI/HTTP/app test與vet通過；Linux同三包 race -count=1 通過（2.121/3.332/1.069秒）。diff --check通過。新增 docs/ignore-api.md 提供請求與報告使用說明。

PG報告測試補真實users降權與sessions撤銷：已取得的nextCursor不得繼續讀。report-live-auth Linux PG race 2頂層/零skip/17.008秒/sourceUnchanged=true，證據 .testdata/inventory-report-live-auth-postgres-summary.json。全部程序結束，無待輪詢handle。

接續需大資料query plan、API到真PG整合（現HTTPfake與PG測試分開）、完整PG回歸、Linux全vet/三build與證據整理，再提交此階段繁中PR。所有API/CLI/報告改動仍未提交於feat/jelee-ignore-api；最新公開PR25。後續混合外部probe真媒體與worker取消/unknown端到端仍待做，整體3D1未完成。

### 報告深頁預備計畫效能修正（仍未提交）

新增 ignore_report_plan_test.go：隔離schema合成baseline與scan各10,000筆，EXPLAIN ANALYZE驗證首頁/兩種來源深頁的資料讀取量，並以force_generic_plan+PREPARE驗證重用計畫。首次一般計畫通過，但generic測試證實原OR條件掃描9,102/9,052筆才能回傳51筆。

已修正 ignoreReportPageSQL：以CASE選擇各來源的(root_id,path)索引下界，取代source OR cursor，無新增或修改migration。最終report-plan-fixed Linux PG race涵蓋全部報告測試：3頂層/零skip/18.663秒/sourceUnchanged=true。generic與custom計畫均最多兩來源各51筆，返回51筆；授權撤銷/未知分類/頁面內容回歸也通過。Windows PG vet、diff --check通過。保留失敗與修正證據於.testdata/inventory-report-plan-{generic,fixed}-postgres*；最後僅加SQL原因註解，無執行邏輯改動。所有程序結束。

下一步優先API到真PG整合與完整回歸/證據/普通繁中PR。目前repo的PG測試還未引入httpapi；可檢查包依賴，若引入造成循環應在適合的整合測試包接線，不能移除架構門禁。不要把計畫效能測試說成完整大規模掃描验收。

### API/報告階段最終回歸完成

新增 ignore_http_test.go（Windows/Linux）：真Store登入token→HTTP提交→正式worker原生掃描臨時檔→PG→HTTP規則行/分頁報告→撤銷session拒絕。另驗證queued報告409、能力下降同key重送200、新key503。api-http-first Linux race通過。

最終api-full完整PG race：185頂層/零skip/224.669秒/sourceUnchanged=true。Linux全vet與jelee/jelee-cli/jelee-migrate建置通過且sourceUnchanged。Windows CLI/HTTP/app/runtime/i18n/architecture測試通過；PG/architecture編譯與vet通過，Windows無DSN並非整合證據。LICENSE/requirements-source/go.mod/go.sum雜湊保持，migration未改。證據 docs/evidence/ignore-api.json；使用說明 docs/ignore-api.md。全部測試handle結束。

需求矩陣更新G22已交付部分，G22.5由阻塞改部分完成，統計4完成/174部分/158阻塞。未宣稱整段G22完成。準備以此狀態提交普通繁中PR，base feat/jelee-ignore-runner，head feat/jelee-ignore-api。接續規則修改後重掃驗收、混合外部probe真媒體、worker取消/unknown端到端，以及既有上游審計基礎上的legacy忽略格式兼容；不得提前關閉全案goal。

已提交698c7d91f0，推送普通繁中PR26（https://github.com/MoYuanCN/Jelee/pull/26），已附聊天。目前分支feat/jelee-ignore-rescan，自PR26提交接續；僅本handoff更新未提交。PR26 CI尚未查看最終結果。下一小段優先同一正式worker/同一來源快取修改規則後重掃：先排除→解除排除→重新排除，確認報告與baseline保留/更新語義，包含相同size/mtime內容變更。可沿用ignore_http_test.go的真HTTP/PG/worker fixture或抽共享helper，保留production guards，不改已發布migration。所有測試handle均已結束。

### 規則修改與移除重掃驗收完成

新增 ignore_rescan_test.go：同一正式runner/scanner/cache四次執行，a排除→b排除→a排除→刪除規則。前三次固定相同6 bytes與mtime，均讀到新內容；報告scan/baseline來源與規則行正確。排除的舊媒體基線保留size/revision，重新納入則更新revision。刪除規則後排除歸零，missing=1只對應規則檔，不誤報媒體消失。

rescan-first三次Linux PG race通過；最終rescan-removal四次Linux PG race通過，1頂層/零skip/18.761秒/sourceUnchanged=true。Windows PG編譯test與vet通過（無DSN非整合證據）；diff --check通過。僅測試與文件改動，不需重跑前階段完整185項；無production或migration改動。證據docs/evidence/ignore-rescan.json，說明docs/ignore-rescan.md。全部handle結束。PR26目前品牌fail，部分功能CI已過、其餘仍pending，未看到功能失敗。

本小段準備普通繁中PR，base feat/jelee-ignore-api。下一段繼續mixed external probe真媒體與worker取消/unknown端到端；legacy兼容需沿既有來源審計，不得猜測語義。

重掃驗收已提交68a1c8f627，推送普通繁中PR27（https://github.com/MoYuanCN/Jelee/pull/27），已附聊天。當前feat/jelee-ignore-worker-acceptance基於PR27；只有本handoff更新未提交。下一步查既有probe真媒體測試工具與runner options，完成ignore+NFO+外部probe同一工作與排除素材不進解析器；也需正式取消與unknown review測試。PR26功能CI仍需待最終結果，PR27尚未查。無活躍命令handle。

### 混合正式探測與取消/unknown驗收完成

scripts/test_nfo_worker.py增加JELEE_NFO_IGNORE_ACCEPTANCE=true模式，使用獨立ignore-worker證據檔，保留原NFO模式。每組1000/100素材加規則與三個排除錯誤素材。runtime/nfo_acceptance_test.go在該模式配正式IgnoreOptions與HTTP意圖，驗證三輪報告/來源與排除素材不進cache/baseline；取消/恢復/關閉也走ignore+nfo scan。

受保護Docker實際兩組均通過：NFO解析400/0/17與40/0/3；外部probe100/0/0與10/0/0；圖片變更23/4；取消恢復、SIGTERM與active calls/children/leases清理通過。原素材/程式來源雜湊不變，只有控制器授權副本替換。schema/container/image均清理。可用.testdata/run-ignore-worker.py重現（會拒覆蓋既有證據）。外部case非race；PG unknown另race。

ignore_runner_test.go新增unknown模式：先建立旧基線，正式native掃描後在observer port刻意回ErrIgnoreUnavailable，驗證review、missing0、舊基線json逐欄不變與unknown報告。明確只是介面故障注入，不冒充blocked filesystem I/O。worker-unknown-first Linux PG race 1頂層/零skip/17.608秒。Tagged runtime vet與Windows runtime/PG/architecture測試通過。證據docs/evidence/ignore-worker-acceptance.json，說明docs/ignore-worker-acceptance.md。所有handle結束，未改production/migration。

準備普通繁中PR接PR27；下一步讀docs/ignore-source-audit.md與requirements-source的G22.2，接legacy格式精確語義實作。PR26最後檢查無功能失敗，但PG CI仍pending；品牌fail。PR27未查。全案仍active。

本段已提交8761c76791，推送普通繁中PR28（https://github.com/MoYuanCN/Jelee/pull/28），已附聊天。目前feat/jelee-ignore-legacy基於PR28，只有本handoff更新未提交。已讀docs/ignore-source-audit.md：固定基底僅找到.ignore入口；Ignore依賴0.2.1庫源码尚未核對，不能直接把自有git matcher套為兼容。需要查明G22.2專用格式A/G22.2專用格式B對應上游版本或不存在的證據，保留no-follow與庫根邊界差異。接續先讀原始G22.2及固定C# blobs/依賴來源，再寫精確相容合同與測試，不凭名稱假設語義。無活躍handle。

### legacy來源與依賴實測進度（未提交）

已從NuGet 0.2.1 nuspec確認Ignore來源f7c6f07d66d0e1043d901a2ab2f58daca1862066（不是猜tag），下載四個C#檔與MIT LICENSE至.testdata/ignore-upstream；Add-Type以未修改source且無NET8 define編譯，13固定案例實測.NET10.0.11/zh-TW。發現完整/相對路徑锚定不同、ASCII ignorecase、regex group/alternation可匹配、invalid [拋例外。不可直接重用自有matcher宣稱兼容。scripts/test_ignore_upstream.ps1執行前驗四個git blob，輸出docs/evidence/ignore-upstream-semantics.json現已保存；尚未做Go移植。

核對上游A v10.11.0固定877251bcaec3780d44b7657c54684dc28646b1c3的DotIgnore包裝器與本基底不同（目錄只看空白全文、無Trim/逐行異常略過）。目前code search無G22.2專用格式A不是所有歷史不存在證明。上游B官方文件4.8 .ignore和4.9 G22.2專用格式B不同；公開上游B HEAD仍2018 3.5.3，缺4.9對應source，不能猜。詳docs/ignore-legacy-audit.md與來源連結。

下一步可先完成已知本基底.ignore精確合同/差分/獨立matcher，其他兩格式保持未驗證；不要因其來源不足停全案或把需求刪掉。四個固定C#檔已讀，可用本地oracle，不須重下載。無活躍handle，本輪新增audit/evidence/script與handoff皆未提交。

### legacy純轉換器與205組差分（未提交）

scripts/test_ignore_upstream.ps1新增以反射取得IgnoreRule私有parsedRegex/Negate，並增24模式×8路徑，加原13共205組。固定來源先驗blob，C#執行結果已更新docs/evidence/ignore-upstream-semantics.json及internal/platform/legacyignore/testdata/ignore-021.json。

新增legacyignore/translate.go，依固定Ignore0.2.1順序轉換regex文字，特別保留QuestionMark負向lookahead實際會替換escaped ?、NoSlash插prefix先於single-star、lookbehind middle **/的原始位置語義。只做轉換，未提供production matcher，未接scanner。4096bytes/UTF8/NUL限制，package內附原MIT LICENSE.ignore。translate_test逐組比較原始regex/否定/註解與205樣本匹配結果，Windows test/vet、Linux race 1.104秒、architecture通過。全部handle結束。

尚需擴大.NET regex不相容處理（lookaround/backreference/文化Unicode/regex錯誤不能錯誤降級）、包裝器最近來源及Trim/空白/例外政策，再持久來源family/API/scanner接線。不得把Go regexp compile error都視為上游RegexParseException；目前測試只限定205已知案例，Translate註解已明示此限制。本分支所有audit/script/新package未提交，尚未新PR。下一步應先把不支援語法分類與合理錯誤合同定清楚，保留完整G22要求。

### 241組引擎邊界與CI最終狀態（未提交）

2026-10-01本輪重新核對PR26/27/28：功能CI全部完成成功，僅Full branding gate失敗；GitHub顯示26/27已MERGED、28 OPEN（本agent未執行merge）。無pending功能工作。

Oracle增加36 engineCases，總241；fixture/evidence同步。Go新增TestEngineOracleTranslation，核對上游成功編譯的engineCases轉換文字；Windows package test/vet通過。v2.8.1差异確認為SimpleFold/PCRE額外語法/UTF16，不宜直接採用。隔離.testdata/legacy-eval-v1使用v1.12.0：原始輸入2差異，pattern非BMP改surrogate escapes + MatchRunes UTF16輸入後241零差異。結果保存docs/evidence/ignore-engine-evaluation.json，包含oracle SHA。正式go.mod/go.sum未變。v1並無v2的OptionMaxBacktrackingStackSize，不可因例子通過忽略資源上限。

下一步擴展跳脫非BMP與regex語法位置的差分；驗證compile/回溯/取消/timeout生命週期與文化版本合同，再決定受限執行方式。尚未正式matcher或wrapper來源讀取、持久family/API/scanner。所有命令結束，無待輪詢handle。本分支全部legacy內容仍未提交，最新公開PR28。

### legacy來源與轉換器小階段驗證完成

Oracle253組（205+48）；新增UTF16Pattern處理奇數backslash與非BMP，修正跳脫emoji差分。獨立scripts/ignore-engine-eval工具模組可重現兩候選（主go.mod/go.sum不變）：v1原始8差異、UTF16零；v2原始20、UTF16仍13。docs/evidence/ignore-engine-evaluation.json更新來源雜湊及結果。Windows legacyignore/architecture test、legacyignore vet通過；Linux race同兩包1.118/1.132秒。未接正式matcher或scanner。

v1 runner無回溯stack硬上限、compile無取消、MatchTimeout無context且error洩露輸入，背景clock有延後清理。下一小階段必須補真正有界的執行層（可評估現有process/sandbox或受維護的受限engine改造）；不要只goroutine提前返回。此來源審計與轉換器小階段可獨立PR，base feat/jelee-ignore-worker-acceptance，明示G22.2仍部分未完成。沒有活躍handle。

已提交63eb3c101d並推送普通繁中PR29（https://github.com/MoYuanCN/Jelee/pull/29），已附聊天。現在feat/jelee-ignore-executor自PR29接續；本段handoff與G22.2矩陣更新未提交，保持部分完成。

下一段執行層已檢查現有process/sandbox：process.New正式僅接受ffprobe，NewIsolatedFFprobe由私有Launcher封閉建構；不可解除allowlist去啟動任意程式。Windows Job目前只KILL_ON_JOB_CLOSE，並非memory cap；sandbox限定Linux amd64且在exec ffprobe前套政策，不能直接把.NET/Go matcher塞進後宣稱跨平台資源隔離。需要新增獨立固定helper合同與兩平台資源限制，或維護有取消/分配限額的engine實作；須考量batch/cache效能。不得降低已存在ffprobe邊界。PR29 CI尚未查看，無活躍命令handle。

### executor批次輸入與包裝器前處理（未提交）

新增legacyignore/batch.go：固定JIG1二進位frame，decoded source≤384KiB、1..128 paths各≤4096bytes、總frame≤1MiB；UTF8/NUL/長度/尾資料嚴格拒絕，讀長度先驗再配置，解碼複製字串防原buffer修改，String/GoString不洩內容。純值context驗證不冒充I/O/regex取消；尚無helper接線。沒有argv/env/executable/資源覆寫欄位。

新增source.go：依固定基底包裝器Split換行、TrimEntries、RemoveEmptyEntries，保留原始1-based行號與註解（註解Add成功影響全invalid政策）；Blank獨立表示全空白排除。最多4096來源行、規則4096bytes。未判定regex有效性，未猜all-invalid。

Windows legacyignore/architecture test與vet通過；batch版本Linux race 1.127秒與5秒fuzz 208771次通過（session65869已exit0）。source.go在該Linux驗證後新增，需後續重跑Linux。PR29最近CI功能仍running，branding已fail，尚未見功能失敗。当前分支feat/jelee-ignore-executor；batch/source四檔及矩陣/handoff未提交，尚未新增PR。

下一步固定helper與batch結果合同、編譯/匹配兩平台真正資源限制；可沿process私有runner新增專屬self helper構造，不能開放任意exe、不能解除ffprobe guards。Linux RLIMIT_AS與Windows Job memory cap需實際驗證（尚未實作），Go記憶體softlimit不能冒充hard cap。注意worker逐檔呼叫的批次/cache效能；規則錯誤不等於執行timeout，後者必須unknown而非exclude/include。

### helper本體與OS配置上限進度（未提交）

新增legacyignore/result.go及測試：JIR1結果codec，必須回覆每條path；invalid line唯一遞增且不得是註解；matched行存在、非invalid且include/exclude對應negation。空白全排除與all-invalid全排除分開；執行error不可包成成功decision，錯誤返回時無partial results。context逐規則检查。

新增legacyignorehelper：主依賴正式新增regexp2 v1.12.0（go.mod/go.sum已變，不能再宣稱原雜湊不變）；cmd/jelee在config之前接固定--internal-ignore-helper，不接受額外args。讀input前設定OS limits，再限長ReadAll；以既有Translate/UTF16Pattern + MatchRunes批次執行。50ms regex MatchTimeout僅次級保護，整體walltimeout仍需parent。Go GC target128MiB是soft；Linux RLIMIT_AS2GiB為virtual address cap、CORE0；Windows Job PROCESS_MEMORY2GiB為commit cap，handle由子程序持有到exit。不是完整filesystem/network sandbox。

Windows helper實際子程序/超額VirtualAlloc拒絕/否定provenance/惡意回溯timeout不回partial測試通過；Linux helper實際子程序及超額PROT_NONE mmap拒絕通過（最終0.335s）。pure legacyignore Linux race1.149s，FuzzResultDecoder5秒203502次通過，session60537 exit0。helper subprocess測試在race build明確skip，因race預留虛擬位址超過production cap；必須保留無race兩平台驗收。其他純helper測試仍可race。Windows最新三包test及vet通過。尚未接parent runner、worker或API，不能聲稱正式啟用。

下一步新增process專屬固定self helper構造及輸入臨時檔所有權/取消/timeout/reap測試，不得開放任意exe或鬆ffprobe限制；批次source/cache成本需驗證。source wrapper的compile error分類仍要持續和.NET差分，不可把任意引擎unsupported當上游invalid；現有253只是固定已知語料。此分支所有helper/protocol及主mod改動仍未提交。無活躍handle。

### 父程序取消、逾時與清理已接線（未提交）

新增process.IgnoreRunner：os.Executable固定self helper，不接受程式路徑/argv；1..2 slots且≤10秒，slots涵蓋輸入暫存建立；0600 request檔close後readonly重開，持有到process.join+result驗證後關閉/刪除。獨立錯誤分類；任何錯誤不回partial decisions。原New/ffprobe限制未改。

Windows process/legacyignorehelper/legacyignore/architecture全部測試與vet通過。Linux process無race全套26.113秒、helper0.334秒通過，含真正helper啟動後取消、100ms父deadline、busy、slot重用、目錄清空與Active0。當前Linux race同四套件仍執行：session94982，需輪詢不可重跑。race test裡兩項需production address space的子程序驗收明確skip；新增CI Linux獨立無race步驟及輸出artifact，Windows原本全套無race涵蓋。workflow尚未遠端驗證。

用戶詢問第3階段多久；已重新讀docs/jobs-stage3-plan.md 3D：除ignore還有fsnotify事件/overflow、cron/interval持久時區及leader/missed-run、1萬/10萬/50萬規模和24h穩態。已明確回報24h是驗收時間、不含开发，不能承諾幾小時全階段完成。沒有縮減原階段。

Linux race session94982已exit0：process67.610s/helper1.364s/legacyignore1.148s/architecture1.149s（特定child資源測試race skip原因見docs/ignore-executor.md，已有無race真實驗證）。PR29 CI已結束，foundation Linux/Windows失敗均是新增來源文件9處legacy名稱；程式測試成功。已將精確來源段移入既有00-audit-baseline來源索引，其他文件引用；沒有改allowlist/scanner。new brand scan0 violations/100 allowed。將隨本階段PR修正，PR29歷史run本身仍紅。

Windows服務build已過；Linux全vet/build當前session48936仍需輪詢。新增docs/ignore-executor.md記錄已完成合同、測試及未接scanner限制。此階段準備提交，base feat/jelee-ignore-legacy；主go.mod/go.sum新增v1.12.0，不再使用先前原mod雜湊結論。

Linux全模組vet與正式服務build session48936 exit0。所有handle結束。保留regexp2 MIT全文於helper/LICENSE.regexp2並更新授權索引，原LICENSE與需求原文不變。執行層本小階段驗證已完成，準備普通繁中PR；正式legacy來源/worker/API仍未接。下段應直接接family合同，並持續確認相容regex語法，不再將純executor完成當整段G22完成。

本小階段已提交4e418c6a7c，推送普通繁中PR30（https://github.com/MoYuanCN/Jelee/pull/30），已附聊天。当前feat/jelee-ignore-family从PR30接續；本handoff更新未提交。PR30尚待CI；PR29歷史foundation紅由本PR修正新文件brand命中。下一步先讀domain.IgnoreIntent與source Resolver/cache/PG持久版本，加入明確legacy family能力及来源证明；不可改已發布migration，不可把自有模式原intent解釋成新regex語義。需要控制每檔起child的效能，優先批次/規則缓存合同。沒有活躍handle。

### legacy固定來源入口（未提交）

feat/jelee-ignore-family新增nativeFile.OpenLegacyRule固定.ignore，不開放任意檔名；共用現有native open的readonly/no-follow/absence分類。readLegacyRule透過窄ruleReader重用held file Stat/size budget/cancel/close/hash流程，缺乏legacy capability的reader回Unavailable而不是代讀自有檔案或假absence。未改現有job intent/proof/migration/worker，因此仍未啟用legacy掃描。

新增native來源測試：同目录兩family分離、不同fileidentity、readonly、missing、directory、closed/cancel/budget；同size/mtime改文hash變。Windows全來源package及vet通過，新增3項無skip；實際junction拒絕。Linux新增3項race無skip1.140秒，實際正常/dangling symlink拒絕。首輪Linux在WSL DrvFS上因mtime精度前提失敗；改為寫入前後皆固定Unix1700000000整秒，保留metadata完全相等/hash不同斷言後通過；不能把本次DrvFS測試稱原生/tmp snapshot。

PR30最近check仍running，只有Full branding gate已fail；尚未看到功能失敗。所有命令結束。下一步沿此fixed legacy reader建立nearest來源鏈與內容proof，再新family identity/persistence/API/worker；兩family組合需明確合同，既有自有模式不可默默改語義。此處尚未提交/新PR。

### 最近來源雙觀察（未提交）

新增Resolver.ObserveLegacy(root,relativeDirectory)及私有openRoot測試port。先只開完整目錄鏈/身份，再從最深目錄向root讀固定.ignore，遇第一份present即停止，不讀被遮蔽祖先；空檔仍是present。整條native鏈重新開一次，逐項比對directoryidentity、checked/absent及來源stamp/hash；更近來源在第二次出現回ErrChanged且無部分結果。root以外不搜尋。token含legacy專用version、trusted root/full relative context、所有身份及checked/presence/hash；Bytes getter複製資料。兩slot/30秒與所有句柄close錯誤失效沿用；這還不是持久family proof或worker capability。

Windows全來源package1.047秒及vet通過，Linux WSL DrvFS6項legacy race零skip1.288秒。覆蓋近層空檔覆蓋、影子祖先不讀、庫外忽略、兩次間新近層、getter副本、穿越拒絕。首輪兩Windows測試把未Clean的t.TempDir傳入嚴格canonical-root API而被拒；依既有合同清理測試root後通過，未放寬production驗證。

下一步LegacyObservation的proof公開投影與持久family version、解碼/批次matcher、native scanner/report接線。要支援baseline祖先確定缺失另需明確合同，當前ObserveLegacy只處理存在目錄。所有handle結束。本分支仍只有native reader/observer及handoff未提交，沒有新PR。

### legacy來源proof投影與原生Linux全套已驗證

新增LegacyDirectoryProof（版本legacy-nearest-source-v1）及DirectoryProofs getter，明確Checked=false影子祖先、Checked=true+absent近層缺失、Checked=true+present最近來源。保留directory identity/parent chain與rule identity/size/mtime/hash，不含rawbytes或absolute root；獨立type防混淆原自有proof。Projection副本/JSON String脱敏測試通過。LegacyObservation持有privatechain，公開fields也加json:"-"。

Windows全來源test1.074秒與vet通過。Linux固定Go1.27.1 direct binary、GOENVoff、TMPDIR=/tmp原生來源全套race10.496秒，architecture1.097秒；session36450 exit0。新brand scan0/100allowed，沒有改allowlist。docs/ignore-legacy-source.md保存合同。PR30最近檢查只有完整brand fail，兩PG仍pending，其餘功能完成成功。

本來源觀察小階段準備提交PR，base feat/jelee-ignore-executor；未改domain/PG/migration/API。後續priority為持久family（checked與absent不同，不可塞入舊proof）及解碼/matcher/scanner接線，baseline missing另補明確合同。所有handle結束。

來源觀察已提交7df70da（程式）/7d7e891119（文件尾空行），推送普通繁中PR31（https://github.com/MoYuanCN/Jelee/pull/31），已附聊天。当前feat/jelee-ignore-family-storage自PR31接續，本handoff未提交。下一步family持久合同，檢查schema12的source manifest主鍵與mode/version守衛，再新增schema13（如需要）；不能改001–012，不能提前開API/worker capability。PR31 CI未查，PR30最後兩PG仍pending。沒有活躍handle。

### 持久family領域合同（未提交）

新增domain.LegacyIgnoreObservation及LegacyIgnoreDirectoryProof，版本legacy-nearest-source-v1，validator要求完整root→query目錄父鏈、同root、身份、最多129項，明確unchecked祖先前綴／最近present／已查absence後綴。沒有來源時必须查到root，拒缺失目錄（後續baseline缺失另做）。不改ValidateIgnoreIntent/Identity或既有模式能力，尚未新增schema13。

新增LegacyIgnoreProofsCompatible：同一目錄跨query的unchecked→checked可相容，但directoryidentity/parent/root必須一致；兩次已查的absence/presence/bytes/meta必須全相同。這避免把某query影子祖先視為absence而誤判另一query來源，亦不能容忍真正source變更。Windows domain/architecture test及vet通過。

下一步資料模型需要同時保留query目標／選定來源與可合併的目錄來源觀察，不可只在原job_ignore_proofs(rule_present)上塞checked=false。原schema9表不支援新狀態；所有既有遷移保持。當前新增兩個domain檔及handoff未提交。無活躍handle。

### schema13 舊格式來源保存已驗證，準備提交

新增完整最近來源觀察領域合同、schema13 三表與不可逆證據守衛、RecordLegacyIgnoreObservation、FreezeLegacyIgnoreManifest、ReadLegacyIgnoreProofPage。保留 query 選定來源與合併目錄證據，未查可補查；來源衝突只保存失效標記。預算、重播、凍結、分頁、租約／取消／世代均驗證。舊模式與公開 claim/API 未啟用新模式。

Linux 真實 PostgreSQL 全套 race：193 項頂層測試通過、0 失敗、0 跳過，耗時 240.316 秒，測試前後來源雜湊一致（本機證據 `.testdata/inventory-legacy-storage-fixed-postgres-summary.json`）。首輪曾有 6 個舊降版測試漏掉 13→12 步驟，補齊後全套通過。Linux 領域／架構 race 通過（1.102／1.130 秒）；Windows 全套測試除 toolidentity 的環境 ACL 限制外通過，該套件在允許 ACL 的環境重跑通過（0.296 秒）。Windows 全模組 vet、三個命令建置、增量品牌掃描（0 違規／100 合法命中）、gitignore-check 通過。

PR31 功能 CI 全部通過，完整品牌 gate 仍失敗。PR31 已由外部合併至其基底，但 HEAD 尚非 origin/master 祖先；本 PR 延續 feat/jelee-ignore-family 為 base，不自行 merge。後續仍需查詢觀察還原與來源最終復核、消失目錄、組合／解碼／批次 matcher／worker／API 接線。所有測試 handle 已結束。

### PR32 已推送；接續觀察還原

來源保存提交 60cab5c3b5，普通繁中 PR32：https://github.com/MoYuanCN/Jelee/pull/32，已附聊天，CI 尚未檢查。當前 feat/jelee-ignore-family-recheck 接續，新增 RestoreLegacyIgnoreObservation 與測試：依保留的選定來源還原 query 邊界，遮蔽在其他 query 補查的祖先，拒絕未查／缺父鏈／不符選定來源，不修改 ledger。Windows domain/architecture test 與 domain vet 通過。此還原變更尚未提交；下一步接 PG 查詢分頁／還原及最終來源復核，不能把單純保存或還原當作真正來源驗證。無活躍命令。

### 查詢分頁還原與單次原生復查已驗證

新增 PG ReadLegacyIgnoreObservationPage，每頁16次查詢，合併讀取最多2064祖先證據，按保留的選定來源還原 Checked 邊界。新增 scan.ObserveLegacyIgnore／ReobserveLegacyIgnore，重新兩輪觀察並比對全鏈。相同metadata改文、近層空檔、來源刪除會失效；影子祖先不影響query；目錄缺失不偽造absence。

Windows domain/scan/postgres/architecture、全vet、三命令build通過。Linux原生/tmp domain/scan/architecture race 1.050/1.169/1.086秒通過。PG TestLegacyIgnore* race 8頂層零skip，23.422秒；證據 .testdata/inventory-legacy-query-native-postgres-summary.json，sourceUnchanged true。schema13不變，未啟用API/worker。準備本還原/復查小階段PR，base feat/jelee-ignore-family-storage。下一步持久verification checkpoint，需防取消/租約generation改變後重用舊復查，並在正式發布前復核；不能沿用自有模式舊proof版本。PR32僅品牌fail，兩PG尚pending，其餘功能pass。無活躍handle。

還原/單次復查已提交 49722eda70，普通繁中 PR33：https://github.com/MoYuanCN/Jelee/pull/33，已附聊天。現為 feat/jelee-ignore-family-verification；僅此 handoff 未提交。已讀既有 domain/ignore_verification.go、schema11、postgres/ignore_verification.go：既有驗證依賴自有比較表與舊proof，不能直接重用。下一步新增獨立 legacy query 復核進度（generation、固定deadline、sequence、cursor、query count、digest、complete），分頁還原helper需抽為共交易函式；同代Begin不可續期，新代由首query開始。須新增schema14而非改已發布13，更新各歷史降版測試；發布仍待真正組合掃描與baseline接線。PR33 CI尚未查；PR32最近僅Full branding fail、兩PG pending、其餘功能pass。無活躍handle。

### schema14 舊格式任務復核進度已驗證

新增獨立 LegacyIgnoreVerificationToken/Page、schema14 job_ignore_legacy_verifications、Begin/Next/CommitLegacyIgnoreVerificationPage。來源清單需已凍結且有效；同代Begin不續期、新代從首query重來，期限min(lease,120秒)。提交比較全query證據，來源差異提交失效標記，截斷或舊token拒絕；EOF與總query計數一致才complete。commit前重查期限與epoch/lease，pg_sleep注入延遲驗證逾時完全回滾。未加入publication seal或正式worker，不可宣稱完整G22。

Linux 真實 PostgreSQL 全套 race：198 項頂層測試通過、0 失敗、0 跳過，247.412 秒；測試前後來源雜湊一致（本機證據 `.testdata/inventory-legacy-verification-full-postgres-summary.json`）。Windows domain／postgres／architecture 測試、全模組 vet 與三個命令建置通過；Linux domain／architecture race 1.101／1.122 秒。增量品牌掃描 0 違規／100 合法命中，gitignore-check 通過。

既有001–013遷移未變；各歷史降版測試新增14→13。新的ReadLegacyIgnoreObservationPage內部讀取抽成同交易helper legacyObservationPage。PR32功能CI全部通過、僅Full branding fail；PR33最近兩PG pending、其餘功能通過。準備本階段PR，base feat/jelee-ignore-family-recheck。下一步組合模式的matcher/scanner與baseline接口整合，以及發布交易與復核期限守衛接線；missing目錄和另兩格式仍未做。所有handle結束。

任務級復核已提交43bed278d1，普通繁中PR34：https://github.com/MoYuanCN/Jelee/pull/34，已附聊天。現在feat/jelee-ignore-family-matching，僅此handoff未提交。下一步來源解碼與批次匹配及兩family組合；已讀legacyignore/source.go與process/ignore.go，helper只接受UTF8 Batch（source≤384KiB），native來源raw≤256KiB，需BOM解碼但必須核對固定wrapper讀檔行為，不能直接借自有模式並宣稱完整相容。自有internal/platform/ignore/decode.go已有嚴格UTF8/UTF16實作可評估共用；尚未修改。PR34 CI尚未查，PR33最後兩PG pending、其餘功能pass，PR32功能全pass，品牌仍fail。無活躍handle。

### PR34 遠端 race 套件總逾時

PR34 run36836943917/job110286518071 的 PG 工作在 go test 預設10分鐘到期，當下 TestProbeFaultReservationWriteFailureReleasesProvisionalQuota 僅執行1秒；無此前斷言失敗。另一個PG工作110286536026已pass（12m4s工作總時間）。本機日誌.testdata/pr34-pg-failure.log。Makefile及PowerShell test-race同步明確指定20分鐘套件總期限，各fixture90秒及SQL/租約/資源期限保持。此修正隨本匹配PR帶上，遠端新run通過前不能稱該歷史run已綠。

### 解碼與匹配接線已驗證，準備提交

新增legacyignore.DecodeSource，依固定wrapper File.ReadAllText 的47組.NET10.0.11對照，支援UTF8/UTF16/UTF32 BOM与replacement fallback，raw256KiB/decoded384KiB。scripts/test_ignore_decode.ps1可產生fixture，testdata/decode.json入庫純文字。實測固定Ignore0.2.1接受NUL規則：(NUL|a).mkv只匹配a，故來源/轉換允許NUL、路徑仍拒絕；request升JIG2拒絕JIG1。

新增scan.MatchLegacyIgnore，最多128同query候選、完整路徑、目錄自己查規則、缺來源NoMatch與空來源BlankExclude分開；匹配後再Observe比較token，來源變動整批失效。使用實際IgnoreRunner驗證UTF16LE/NUL來源、2候選1child、Active0、暫存清空。scan TestMain新增helper dispatch，native test用!race buildtag；CI Linux無racehelper步驟新增測試/scanpackage。正式worker/兩family組合/基線接線仍未完成。

Windows scan/legacyignore/helper/process/architecture與全vet/build通過。Linux原生/tmp race legacyignore1.114/helper1.375/scan1.260/arch1.222；無race原生 process.113/helper.062/scan.007秒；JIG2 fuzz 5秒231768次通過。brand new0/100、gitignore-check過。未改schema14或go.mod。CI修正Makefile/scripts make明確20m套件deadline另做一個commit，歷史PR34紅run本身不假稱綠。所有handle結束。

匹配接線已提交c92ef01c5f（CI期限）與b2408e9a60（解碼/匹配），普通繁中PR35：https://github.com/MoYuanCN/Jelee/pull/35，已附聊天。現為feat/jelee-ignore-family-scan，僅此handoff未提交。已讀media/ignore/scan.go和domain/ignore_scan.go：現有ScanBatch只保存自有規則proof與RuleLine≥1類型的命中來源，legacy空檔/all-invalid的行號0、來源family、nearest query證據需明確接線；不能直接冒用舊IgnoreScanExclusion/版本。需要保留held directory identity與最終Done前重新觀察來源的合同。正式組合優先順序先核對G22原文與固定上游，不猜。native匹配已可供掃描內部调用但尚未正式啟用mode。PR35 CI尚未查；PR34其中一PG race套件10m超時，另一PGpass，本PR修正尚待遠端驗證。無活躍handle。

### 合併掃描 adapter 已驗證

新增 FamilyIgnoreScanner 與獨立領域批次合同。自有明確 Include/Exclude 優先，Unmatched 查最近 .ignore；此為明確記錄的新組合政策。保留兩來源目錄身份鏈、子目錄候選身份、來源證據與排除 family/reason，空/all-invalid 行號0。兩個整次掃描名額及分離 resolver 防止巢狀耗盡；共享30秒期限傳到helper。callback錯誤保留，來源變更及候選替換拒絕，取消不發批次並釋放名額。

Windows scan/media-ignore/domain/architecture 通過（scan1.613秒、source1.080秒），相關 vet 通過。Linux 原生 /tmp race：source10.297、scan1.190、domain1.059、architecture1.092秒；真實非race helper0.012秒。增量品牌0違規/100合法命中，gitignore-check與diff-check通過。CI新增真實FamilyIgnoreNativeHelper項目。

PR35兩PG及其餘功能CI已全部通過，僅Full branding gate失敗；20分鐘套件時限修改已有遠端通過證據。schema14未變；新掃描adapter尚未公開/worker啟用。下一步組合批次的原子保存與排除表（需新增遷移，不能改既有），再接基線/發布/worker/API。保存需批次合併legacy觀察避免DB N+1，保留失效標記且回滾不完整批次。詳見 docs/ignore-family-scan.md。無活躍測試handle。

合併掃描已提交 d7974d5e2b，普通繁中 PR36：https://github.com/MoYuanCN/Jelee/pull/36，已附聊天。現為 feat/jelee-ignore-family-batches，接續組合批次保存。已檢查 ignore_legacy_manifest.go 與 ignore_scan.go：recordLegacyObservation 每個 query 都鎖 manifest、讀 ownership／proof／query，不能在128個child上直接迴圈呼叫。下一步先把同root多個觀察去重合併（checked升級、selected query保持），一次讀取既有proof/query，再管線寫入；單觀察入口應共用此實作避免兩套語義。組合保存需額外驗證排除family/reason與對應query/來源，並用交易savepoint保證來源衝突只保留失效標記，不能留下先寫入的自有proof。尚未新增schema或修改DB實作。PR36 CI尚未查；無活躍測試handle。

### 舊來源批次保存已驗證

RecordLegacyIgnoreObservations 接受1–129次同根query，原單筆入口共用批次實作。prepareLegacyObservations先合併共享祖先、保留每query選定來源；資料庫兩次集合讀取取得舊proof/query，再統一比較並管線寫入。批次內或既有衝突只提交invalidated，無accepted prefix；checked升級／影子query還原與凍結逆序重播驗證，預算錯誤回滾全部。

Linux真PG全套race 203項頂層通過、零失敗零skip、249.770秒、sourceUnchanged=true（.testdata/inventory-legacy-batch-full-postgres-summary.json）。首輪14項舊格式測試30.237秒通過；之後新增升級/預算測試已含全套。Windows postgres/architecture測試、全vet、三命令build、增量brand0違規/100合法、gitignore-check通過。schema14保持；下一步schema15與SaveFamilyIgnoreScanBatch/NextFamilyIgnoreScanDirectory，兩family證據需要共同savepoint回滾，排除family/reason與query須驗證。已讀schema12排除表與舊保存邏輯，舊表RuleLine>=1無法承接legacy空/allinvalid。

PR36目前兩PG工作pending（110298262595、110298246488），其他功能全pass，品牌fail。所有本機測試handle結束。

批次來源保存已提交 a891c03e77，普通繁中 PR37：https://github.com/MoYuanCN/Jelee/pull/37，已附聊天。現為 feat/jelee-ignore-family-inventory，下一步新增 schema15 組合排除資料與兩family原子掃描保存。不可修改已發布001–014；舊資料表只允許行號>=1，應新增保存family/reason的表，保留kind/parent索引、刪除cascade與凍結守衛。先建立validFamilyIgnoreScanBatch驗證：自有fullchain與held identity，legacy同root且query僅目前目錄/候選子目錄、兩family身份鏈一致，排除來源需對應family及query選定來源，blank/invalid行號0，rule行號1–4096，禁止overlap。保存兩family證據時用共同savepoint；任何来源衝突回滾本批兩family新增證據後標記兩manifest失效，再commit lease/epoch guard。新模式維持公開關閉直到baseline/verification/publication/worker完成。

### schema15 合併掃描原子保存已驗證

新增 family 排除表（family/reason、rule行號1–4096、blank/invalid行號0）、DB凍結守衛、NextFamilyIgnoreScanDirectory/SaveFamilyIgnoreScanBatch及完整批次驗證。共同savepoint使兩family來源衝突回滾兩邊新增證據，只保留失效標記。排除批次集合讀取、管線寫入，重播/重啟/預算/取消/舊模式隔離均驗證。正式worker/public mode仍未開放。

真PG首輪找到多query選同ancestor造成job刪除cascade被selected FK阻擋；在新schema15將該FK改DEFERRABLE INITIALLY DEFERRED，down還原，001–014不改。首輪budget測試把max_entries設1觸發既有DB下限，改為合法max_directories=2並驗證inventory+excluded合計超限回滾。修正後4項核心PG測試23.823秒通過。新增真實native→受限helper→PG驗收，16.710秒1項零skip通過，CI新增非race步驟。

全套PG race：207頂層通過、零skip，260.134秒，唯一失敗TestPostgresIntegration舊測試漏15→14；補測試步驟後該項單獨重跑23.825秒通過，正式碼未再修改。不要聲稱單次全套208通過；原失敗記錄與修正證據均保留（family-scan-full / family-scan-migration-fixed）。sourceUnchanged皆true。Windows postgres/architecture、全vet、三命令build、brand-new0/allowed100、gitignore-check、diff-check通過，LICENSE/requirements原文雜湊不變。

PR36功能CI已全pass，品牌fail；PR37一PGpass（110301421298）、一PGpending（110301590550），其餘功能pass，品牌fail。下一步基線分類/來源最終復核/發布與worker接線，尤其消失父目錄的legacy來源觀察尚不支援，不能以讀不到規則偽造absence。無活躍handle。

合併掃描保存已提交 7f35116d24，普通繁中 PR38：https://github.com/MoYuanCN/Jelee/pull/38，已附聊天。現為 feat/jelee-ignore-family-baseline，準備基線分類。PR38尚未檢查CI。需要先讀 scan/ignore.go 的 ClassifyIgnoreCandidate 與 media/ignore 的 missing-directory 原生實作，對照 legacy ObserveLegacy；目前LegacyObservation禁止MissingDirectory且schema13 identity非零，不能直接把舊absence合同塞入舊格式。應先确定查不到父目錄時對固定nearest-source語義的正確觀察/證據，再接分類与保存，不以unknown全面替代應可證明的missing/excluded。既有ignore baseline表與domain決策只容自有rule_line>=1，合併版本需保留family/reason與來源查詢，不能冒用旧合同。整個任務的自有/legacy復核都必須通過後才發布。所有本機handle已結束。

### 缺失父目錄的舊格式基線來源已驗證

核對固定wrapper blob023c1e891532e5baa022ef0e48b7179e991f59e3 FindIgnoreFileCached冷查找，DirectoryInfo.Parent不要求起始存在；不要採用暫存區的10.11.0版本包裝器（不同版本）。新增ObserveLegacyBaseline與獨立LegacyIgnoreBaselineObservation版本legacy-baseline-source-v1，將existing來源鏈與首個missing child分開，無虛構更深身份；兩輪native核對chain/source/缺失邊界。原ObserveLegacy仍拒絕missing目錄。

MatchLegacyIgnoreBaseline最多128共lookup候選，用共用evaluateLegacySource與legacyCandidatePaths避免改變原解碼/完整路徑/helper語義。匹配前後核對缺失邊界，父目錄重現即失效；領域合同驗證邊界緊鄰來源鏈尾且lookup位於其下。基線decision/missing分類尚未啟用；schema15保持，新的missing證據需持久保存與最終復查。

Windows source/scan/domain/architecture 1.132/1.656/.188/.170秒通過；全vet/三build通過。Linux原生/tmp race10.205/1.185/1.055/1.092秒，真實helper非race .007秒通過，CI加入LegacyBaselineNativeHelper。brand-new0/allowed100、gitignore/diff通過。所有handle結束。PR38兩PG pending（110307015980、110306834346），其他功能pass、品牌fail。

下一步基線觀察保存：existing Source可沿用schema13表，missing邊界要獨立保存（不能把identity0塞既有legacyproof），lookup→existingquery+boundary也需保留且freeze/verification覆盖；新schema而非改已發布15。再接自有明確include/exclude優先的基線分類、批次提交及最終發布。原庫baseline只有自有rule行號>=1的格式，需獨立family/reason合同。

缺失父目錄來源與基線匹配已提交6bfdd688f6，普通繁中PR39：https://github.com/MoYuanCN/Jelee/pull/39，已附聊天。現為feat/jelee-ignore-baseline-evidence；schema15不變，接續missing邊界/query持久化與復核。PR39尚未查CI；所有測試handle結束。

### schema16 基線來源查詢保存與還原已驗證

新增job_ignore_legacy_baseline_queries及manifest.baseline_queries計數，existing來源仍存schema13鏈；lookup/source-directory/missing-boundary分開保留，最多128批次、16384筆，共用64MiB charge。來源和boundary共savepoint，任何衝突只保留invalidated而無來源prefix。recordLegacyObservations也檢查新增present目錄是否撞到既有missing邊界；相反順序和同批都測試。來源restore抽共交易restoreLegacyQueries集合讀取，baseline page16筆；adapter Observe/ReobserveLegacyIgnoreBaseline比對完整source與邊界。

全真PG race213頂層通過、0失敗0skip、278.475秒、sourceUnchanged=true（.testdata/inventory-legacy-baseline-full-postgres-summary.json）。首輪20項36.099秒pass；Windows postgres/scan/architecture、全vet、三build、brand-new0/allowed100、gitignore/diff pass。001–015未改，所有舊降版序列已加入16→15。

PR39先出現foundation增量brand失敗，原因是最後追加handoff含非必要舊品牌檔名；已文件commit a8f59b2809並快轉推回feat/jelee-ignore-family-baseline，沒有放寬allowlist。新run36844948666的Windows/Linux foundation已pass，另一runWindowspass/Linux仍pending，兩PG待結果，完整品牌fail。不能把舊run改稱pass。查過PR39仍OPEN，base feat/jelee-ignore-family-inventory。

下一步缺失邊界的任務級持久復核。現有schema14 legacyverification只覆蓋existing query/source，不能涵蓋baseline缺失boundary；需要獨立token/cursor/count/digest/固定deadline/lease generation及EOF計數覆蓋新查詢。ReadLegacyIgnoreBaselinePage目前只是凍結讀取，Reobserve只是單次原生驗證。正式family baseline分類、publication/worker仍未開放，不宣稱完成G22。所有handle已結束。

schema16基線證據已提交7b1f6b57d3，普通繁中PR40：https://github.com/MoYuanCN/Jelee/pull/40，已附聊天。現為feat/jelee-ignore-baseline-verification，接續任務級缺失邊界復核。PR40尚未查CI；所有本機handle結束。

### schema17 基線來源與缺失邊界任務級復核已驗證

新增獨立LegacyIgnoreBaselineVerificationToken/Page、job_ignore_legacy_baseline_verifications以及Begin/Next/CommitLegacyIgnoreBaselineVerificationPage。共用legacyVerificationFence但不共用source查詢游標；按原始LookupDirectory分頁，摘要包含lookup/source全鏈/missing boundary。固定min(lease,120秒)期限，同代不續期、新代重啟、EOF總數對baseline_queries。提交前再查deadline/lease/epoch；來源或缺失邊界改變只標invalidated且無進度。

全真PG race219頂層通過、0失敗0skip、288.142秒、sourceUnchanged=true（.testdata/inventory-baseline-verification-full-postgres-summary.json）。首輪5项23.725秒pass。18次同source不同lookup跨頁、截斷/舊token、換代/取消/過期、邊界移動、PG延遲300ms對deadline150ms整批回滾，以及原生observe→保存→freeze→reobserve→commit→EOF皆通過。Windows postgres/domain/architecture、全vet、三build；Linux domain/architecture race1.056/1.092秒通過。001–016不改，歷史降版序列加入17→16。brand-new0/allowed100、gitignore/diff通過。

PR40最新兩PGpending（110314880445、110315039488），其他功能全pass，完整品牌fail。所有本機handle結束。本機全PG harness外層目前300秒，本輪288秒已接近，後續新增測試若增加整套時長須合理調整外層觀察期限，不縮減fixture/SQL/lease期限或移除測試。

下一步合併基線分類：新FamilyIgnoreBaselineDecision需family/reason、ancestor MatchedPath和可選legacybaseline觀察，不能冒用僅自有行號>=1合同。必須保留父目錄剪枝語義；不可只查最終檔案的nearest來源，因掃描可能已被祖先legacy空檔/規則排除。自有明确Include/Exclude優先，每個仍需判定的祖先目錄與最終檔案應按相同組合政策，缺失目錄觀察用已實作的邊界。兩family身份链需一致，資料庫完整基線分類/兩來源復核/發布/worker仍未接線，不代表G22或階段3完成。

schema17任務級基線復核已提交7b280acece，普通繁中PR41：https://github.com/MoYuanCN/Jelee/pull/41，已附聊天。現為feat/jelee-ignore-family-classification，接續遵守祖先剪枝的合併基線分類。PR41尚未查CI，所有本機handle結束。

### 合併基線分類進行中（未提交）

新增 FamilyIgnoreBaselineDecision 與驗證：保留 family/reason、祖先 MatchedPath；legacy 可由該目錄自己的來源排除目錄，但最終檔案不能冒充來源目錄；custom 僅容嚴格祖先來源。blank/invalid 行號0，rule行號1–4096；missing/unknown不攜帶family或規則出處，未知原因限既有固定值，序列化與格式化遮蔽私有資料。

新增 FamilyIgnoreScanner.EvaluateFamilyIgnoreBaseline：逐層祖先再最終檔案，自有明確Include/Exclude優先，Unmatched才查legacybaseline；祖先排除即停止。共用兩slot與30秒ctx；合併custom完整proof、legacy checked相容proof及兩family身份/缺失邊界，衝突返回空結果+invalidated。返回仍是暫定分類，不能證明清單absence或發布；尚未串接DB與worker。

Windows scan/domain全套測試1.560/.191秒通過，兩package vet與diff-check通過。新增針對祖先空來源禁止後代復活、明確include繞過legacy、缺失父目錄僅保留首個absence、不接受祖先之間custom來源變更的測試。尚待Linux race與真實helper驗收、更多跨family身份與邊界變更/取消/資源限額測試、架構和完整門禁；不可宣稱此小階段完成，四個新Go檔未提交。下一步先完成上述驗證和必要修正，再文件/traceability、繁中PR；schema17維持不變。

PR41最新查詢：兩套foundation Windows/Linux皆pass，兩PG與既有平台run-tests仍pending，完整品牌fail。查詢成功需require_escalated網路；預設sandbox代理127.0.0.1:9失敗。無活躍本機測試handle。

### 合併基線分類 adapter 已驗證

Windows scan/domain/architecture全套1.688/.174/.159秒，全vet/三build通過；Linux原生/tmp race1.176/1.051/1.095秒；真實helper非race .022秒通過，Windows同項在scan全套。補足真實空/allinvalid/缺失祖先/自有優先、祖先禁止復活、兩名額/第三拒絕/取消釋放/共享deadline與兩family來源變更測試。CI非race步驟新增FamilyBaselineNativeHelper。詳見ignore-family-baseline.md；schema17未改。尚未串接DB分類保存、最終發布及worker，不代表G22完成。

PR41最新三平台run-tests全pass，foundation兩平台兩套皆pass，兩PG仍pending（110318639596、110318814009），完整品牌fail。下一步合併基線保存：原基線表只容自有rule_line>=1，需新family/reason來源關聯合同，綁定exact pending page與兩family共同原子保存；先讀既有ignore_baseline與ignore_family_scan保存流程，不能把adapter分類直接作發布權限。所有本機handle已結束。

合併基線分類已提交1304bd82d5，普通繁中PR42：https://github.com/MoYuanCN/Jelee/pull/42，已附聊天。現為feat/jelee-ignore-family-baseline-storage。已讀ignore_baseline.go前175行及ignore_family_scan.go前100行：舊Begin先用ignoreComparisonFence、inventoryCoverage與root proof覆蓋檢查，凍結inventory但不凍結source；Next頁128筆raw baseline含seen標誌、exact token。後續需讀CommitIgnoreBaselinePage與來源綁定、既有migration10–12及schema15守衛，判斷可共用comparison游標但新family決策需獨立表與digest版本，不能漏legacy fence。遷移實際目錄internal/adapter/postgres/migrations，根目錄migrations不存在。PR42尚未查CI；無活躍handle。

### 合併基線保存進行中：共用證據合同（未提交）

新增domain.FamilyBaselineEvaluation及ValidateFamilyBaselineEvaluation，scan使用alias並在返回前驗證。非unknown要求完整custom祖先鏈或首個missing邊界；legacy查詢只准可達祖先/最終父目錄/命中祖先自身，重複查詢必須相同，checked來源相容，兩family目錄身份/缺失狀態一致。custom排除必須有source proof；legacy排除必須有對應lookup選中的現存來源。unknown只能無證據，來源錯誤仍由呼叫端決定unknown。Windowsdomain/scan/architecture .176/1.719/.165秒通過，相關vet/diff通過。新反例涵蓋漏鏈、不同身份、未選來源、跨family矛盾absence、進入已排除祖先；未做Linux或DB驗證。

重要：舊BeginIgnoreBaselineComparison不能直接給family模式使用。ignoreComparisonFence→ignoreManifestFence→loadIgnoreRequest→ValidateIgnoreRequest拒絕reserved family模式；需以legacyManifestFence建立新的family comparison fence（查custom/legacy invalidation和epoch/revision），不得放寬公開ValidateIgnoreIntent。既有comparison cursor/count/raw128分頁和inventory凍結表可評估共用，decision表不行。舊Commit逐筆ignoreDecisionProofs查自有來源+最後既存parent已枚舉，family需保留此覆蓋檢查並另核對legacy selected lookup。原Begin先inventoryCoverage/rootsCovered/unknown attributes與epoch，unknown舊scope直接completed的語義需保留。

仍需新schema18family決策表/receipt digest版本、family Begin/Next/Commit、兩family共同savepoint（參照recordFamilyScanEvidence及recordLegacyBaselineObservations，後者不可傳空slice），綁定exact pending page、最後lease/epoch/revision fence、真PG測試。沒有新增DB方法或迁移，未提交；目前保留已測試的共用domain合同和scan接線，不能宣稱保存完成。沒有活躍本機handle。

### schema18 與合併基線 Begin/Next 已驗證（整段未提交）

新增 BeginFamilyIgnoreBaselineComparison/NextFamilyIgnoreBaselinePage；舊入口共用private begin/next，comparisonModeFence在family使用legacyManifestFence並查兩manifest invalidated/epoch和baseline revision。Begin額外驗證每root有獨立legacy query、checked rootproof且identity與custom一致，保持原inventoryCoverage、roots、unknown旧scope與raw128分頁。舊Begin/Next/Commit仍拒絕family模式；未放寬公開模式。

新增未發布schema18：job_ignore_family_decisions保留family/reason/祖先matched_path、非空rule來源、blank/invalid line0與rule1..4096、legacy可自身目錄命中但file不可。intent guard、immutable update、兩manifest凍結/invalid guard；family scan exclusions新增comparison凍結trigger（原schema15沒有comparison模式的凍結，現在Begin可啟用故需補）。down遇保留family comparison拒絕，jobs history cascade允許。SchemaVersion18，所有已找到的降版序列補18→17；001–017未改。

真PG race首輪family-comparison-first 10頂層pass、0fail0skip、33.043秒；schema新增後family-comparison-schema 12頂層pass、0fail0skip、39.957秒；兩者sourceUnchanged=true。後者包括新Begin/Next/isolation、revision、兩manifest invalidation、未完成掃描拒絕、SQL形狀/immutable、comparison凍結排除表、阻止有資料降版、cascade及全schema上下往返；同時跑原IgnoreBaseline全部。Windowspostgres/domain/scan/architecture .070/.183/1.718/.165秒，全vet/diff通過。尚未跑全PG回歸與Linux新增domain合同race。

下一步仍必須實作CommitFamilyIgnoreBaselinePage：新的family digest需涵蓋decision和保留證據（exact replay不能冒用舊摘要），exact pending raw頁；共同savepoint保存custom與legacybaseline、任何衝突回滾本頁兩邊證據並只保留兩manifest invalidated；完整custom/legacy proof與已完成coverage綁定、未知分類不攜證據。receipt/count/EOF整批提交，最終lease/epoch/revision guard。目前新決策表僅測試直接插入，沒有正式提交方法，不能宣稱基線保存完成或建立完成PR。

PR42最新：兩套Windows/Linux foundation與三平台run-tests全部pass，兩PG仍pending（110323096556、110323257387），完整品牌fail。PR41功能CI已全部pass，完整品牌fail。所有本機handle結束，未提交變更保留於feat/jelee-ignore-family-baseline-storage。

### 合併基線整頁提交已實作、局部真PG通過（未提交）

新增ignore_family_commit.go：CommitFamilyIgnoreBaselinePage驗證token+每筆domain證據、exact next raw128 unseen順序與總數。familyBaselineDigest以新domain seed綁定token、decision全部欄位/family、每筆custom/legacy數量、完整proof和legacybaseline lookup/source/missing；exact replay查receipt，不重複計數。新表存三態/出處，coverage沿用ignoreDecisionProofs最後既存parent已done/skipped0，legacy排除轉為MatchedPath parent覆蓋檢查（不錯查custom rule）；來源出處由domain合同與同交易保存綁定。EOF必須已處理baseline_count，固定revision最後查FOR SHARE，再commitIgnoreManifest重驗lease/epoch。

recordFamilyBaselineEvidence使用整頁共同savepoint，任一source conflict回滾兩family本頁所有新增，僅標兩manifestinvalidated。先按root去重custom directory与legacy lookup，重複必須完全一致；custom依canonical path親先子後一次record，legacy每128query批次。全部保存後集合比對當前root custom/legacy identity及legacymissing/custompresent矛盾，涵蓋前頁證據。舊record helpers的预算仍生效，不把重複祖先份數當manifest唯一行數而拒絕合法深頁。

真PG race family-commit-first：6頂層pass、0fail0skip24.574秒；去重後family-commit-dedup：6頂層pass、0fail0skip22.571秒；sourceUnchanged皆true。包含130筆跨頁、排除/未知/missing計數、截斷拒絕、exact replay與改證據replay拒絕、EOF、來源衝突無分類/基線來源query/計數prefix、兩manifest失效。Windowspostgres/domain/scan/architecture .069/.172/1.679/.157秒，全vet/diff通過。

還需做：來源衝突已存在於DB時回滾之前成功寫入的另一family證據（目前新增case在去重階段即可偵測，不足以單獨證明寫入後rollback）、跨頁兩familyidentity/absence矛盾、lease reclaim/cancel/epoch/不足coverage、真實native adapter→helper→PG分類驗收；Linuxdomain合同race與全PG回歸（harness外層300秒已接近，可合理加到600秒，不改fixture/SQL/lease期限）。三命令build、完整增量門禁、文件/traceability後才能提交繁中PR。schema18和全部變更仍未提交。所有本機handle結束。

PR42功能CI已全pass（兩PG14m47s/13m44s、三平台tests及foundation），完整品牌仍fail。PR41功能也全pass。沒有新增PR，當前feat/jelee-ignore-family-baseline-storage。

### schema18 合併基線保存完整驗證

補足真實寫入後rollback：先寫custom missing proof，再遇DB既有legacy source變更或legacy已存在同目錄，整頁回滾且兩manifest失效；新proof不保留。新增epoch/cancel/generation與receipt插入延遲300ms、lease150ms晚失效，確認實際進入延遲且分類/legacybaselinequery全回滾。擴充既有TestFamilyIgnoreNativeStorage，真native scan→helper→Save→Begin→Evaluate→Commit→EOF，6筆基線Observed1/Missing1/Excluded4，無存活helper。CI原有非race同名步驟已涵蓋。

專項PG race8頂層通過26.680秒；非race native1通過17.318秒。全PG race227頂層pass、0fail0skip299.821秒、sourceUnchanged=true（.testdata/inventory-family-baseline-full-postgres-summary.json）。外層harness改600秒，因上一輪288秒已接近300；fixture/SQL/lease期限未改。Windowsdomain/scan/architecture .176/2.224/.157秒，全vet/三build通過；Linux原生race scan/domain/architecture1.181/1.053/1.096秒。LICENSE與requirements原文雜湊保持不變。所有handle已結束。schema001–017未改，僅新增18，歷史降版步驟补18→17。

下一段：合併模式最終復核/封存/發布。已讀ignore_publication.go、ignore_verification.go；舊verificationComparison及FinishIgnoreJob走ignoreComparisonFence，仍拒絕family。應接family自有proof復核、schema14來源query復核、schema17baseline缺失邊界復核，再共同檢查generation/deadline/total/manifest frozen/noninvalid/seal後發布；不能只沿用custom seal。saveIgnoreImageProgress舊SQL讀job_ignore_decisions，family要對應新表；NFO/probe/coverage/threshold/late expiry與unknown review-only語義要保留。runner在internal/platform/jobs/ignore.go，ports在internal/app/ignore_jobs_ports.go，尚未修改。公開模式仍關閉，G22與第3階段未完成。

schema18合併基線保存已提交5d67083251，普通繁中PR43：https://github.com/MoYuanCN/Jelee/pull/43，已附聊天。現為feat/jelee-ignore-family-final-verification，接續兩種來源與缺失邊界的共同最終復核/封存。名稱feat/jelee-ignore-family-verification已存在於43bed278d1（舊PR34），故保留該分支並採新名稱。PR43尚未查CI，所有本機handle已結束。下一步先讀既有Begin/Next/Commit/SealIgnoreVerification和legacy兩種verification，設計共同generation/固定期限及封存守衛；不能放寬舊模式或只重用custom seal。

### 共同最終復核啟動進行中（未提交）

新增ignore_family_verification.go：familyVerificationComparison以family comparison fence查兩manifest/epoch/revision與completed/unknown0；BeginFamilyIgnoreVerification同交易凍結兩manifest、建立custom/source-query/baseline-query三checkpoint，custom checkpoint作協調anchor。期限三者完全相同，取min(lease,now+120s,本generation既有兩legacy期限)，防止獨立legacy復核加入協調後被續期。同generation已有anchor時只驗證共同lifetime、不重置；新generation三者sequence/cursor/count/digest/completed重設且清customseal。commitFamilyVerification最後guardedJobUpdate、inventory epoch、三代數/期限相等/未過期/既有seal有效後commit。lifetime刻意不查invalidated，供未來來源錯誤保留失效標記；正常流程入口另查兩manifest。

新增三個真PG race測試：同代已完成一頁legacy後重試不續期/不清進度、generation重啟且舊lease拒絕；先獨立legacy再共同啟動保持既有20秒期限、共同expired拒絕續期；未完成comparison無checkpoint。family-verification-begin 3頂層pass、0fail0skip19.764秒、sourceUnchanged=true。Windowspostgres編譯/純測試.064秒、相關vet/diff通過。沒有修改schema18或既有verification實作。

下一步新增family custom Next/Commit與三路完整seal：既有custom verificationComparison仍走舊模式，需private mode-aware重用核心但保留舊入口拒絕family；family路徑commit須驗共同lifetime。legacy兩路現有方法可操作由共同Begin建立的checkpoint，但seal需再次共同檢查兩manifest frozen/noninvalid、所有completed/counts正確、同generation、相同固定deadline與短seal期限，最終發布仍待接線。補unknown comparison、共同啟動晚leaseexpiry/部分checkpoint/已過期獨立legacy、全三路原生復查與seal測試，再完整回歸/文件/PR。當前只有共同Begin已驗證，不可稱共同復核完成。所有handle結束。

PR43最新：一套foundation Linux/Windows及另一套Windows pass，另一Linux與三平台run-tests、兩PG仍pending（110331537726、110331379398），完整品牌fail。目前feat/jelee-ignore-family-final-verification，兩個新Go檔與handoff未提交。

### 三路復核分頁與共同封存已接線（未提交）

新增NextFamilyIgnoreVerificationPage/CommitFamilyIgnoreVerificationPage，舊custom Next/Commit共用private mode-aware核心；舊入口仍拒絕family。family verificationModeComparison檢查比較完整/known、兩manifest有效與frozen、三checkpoint共同lifetime，提交使用commitFamilyVerification最後重驗共同期限。custom changed proof只提交custom invalidated，下一步共同入口即因任一manifest失效拒絕。

新增SealFamilyIgnoreVerification：三路completed、custom verified_rows==manifest.rows、legacy verified_queries==queries、baseline verified_queries==baseline_queries、兩manifestfrozen/noninvalid、共同generation/deadline全部成立才設定custom sealed_until=min(deadline,now+30s)，COALESCE不續期；最後再次共同lifetime確認。仍未接發布。現有legacy兩路分頁可使用共同Begin建立的checkpoint，最終共同seal不接受僅custom或漏baseline邊界的復核。

專項第一次family-verification-pages：10頂層pass但新changed測試失敗，原因預期ErrConflict後未清err就做檢查，正式碼未改。測試修正後family-verification-pages-fixed：11頂層pass、0fail0skip31.388秒、sourceUnchanged=true，含所有原IgnoreVerification回歸。新case覆蓋changed marker可提交、期限不一致、缺checkpoint、legacyinvalid、截斷拒絕、舊入口隔離。原生非racefamily-verification-native：1pass、0skip15.655秒；擴充TestFamilyIgnoreNativeStorage完成native custom/legacy/sourcebaseline逐頁Reobserve→Commit→EOF，只有三路全完才seal，兩次提早seal均拒絕。沒有存活helper。Windowspostgres編譯/純測試.068秒、相關vet/diff通過。

尚需共同seal不續期/到期/錯計數/晚DB寫入、unknown比較拒絕、獨立legacy過期導致Begin整批回滾/缺checkpoint等充分邊界驗證，再全PG回歸與其他完整門禁/文件/PR；目前schema18未變。所有本機handle已結束，當前feat/jelee-ignore-family-final-verification。PR43兩PG仍pending110331537726/110331379398，其餘功能已全pass，完整品牌fail。

### 共同最終復核與封存完整驗證

新增ignore_family_seal_test.go實際走三路Next/Commit到EOF，驗證Seal重試不續期/過期拒絕、三路各自計數錯誤拒絕、DB最後jobs寫入延遲300ms與共同deadline150ms後封存整筆回滾且測試確認進入延遲；既有獨立legacy過期時共同Begin不保留custom/baseline checkpoint也不凍結custom；unknown比較完成仍拒絕復核。family-seal-boundaries真PG8頂層pass、0fail0skip28.234秒。全PG race family-verification-full 235頂層pass、0fail0skip309.436秒、sourceUnchanged=true。原生三路Reobserve/EOF/Seal於上輪已pass15.655秒，正式碼此輪未變；Windowspostgres.067秒、domain/scan/architecture .192/1.798/.163秒，全vet/三build通過。schema18保持，沒有遷移改動。

PR43功能CI已全pass（兩PG15m6s/13m37s），完整品牌fail。所有本機handle結束。接續發布：FinishIgnoreJob可抽private mode-aware；family comparison用comparisonModeFence true，發布前後共同complete/count/lifetime/frozen/noninvalid + custom sealed_until有效。saveIgnoreImageProgress的missing SQL與保留excluded baseline SQL都要改成可信常數選表job_ignore_family_decisions，不能遺漏其中一處。保留NFO/probephase、coverage/skipped、missing thresholds、review-only unknown、MaxEntries含歷史excluded、guarded finish/audit/historytrim/late guards語義；舊FinishIgnoreJob與ordinaryFinishJob仍拒絕family成功發布。ports/runner/public admission後續獨立驗證。

三路共同復核與封存已提交f092c9e97f，普通繁中PR44：https://github.com/MoYuanCN/Jelee/pull/44，已附聊天。現為feat/jelee-ignore-family-publication，接續最終發布；PR44尚未查CI，所有本機handle結束。按照上段出版守衛/兩個decision SQL選表與既有phase/閾值/rollback合同繼續，schema18不變。

### 合併模式最終發布進行中（未提交）

ignore_publication.go新增FinishFamilyIgnoreJob，兩入口共用private finishIgnoreJob(family bool)。comparisonModeFence選正確mode；guardIgnorePublicationSeal在family同時驗共同三路complete/count/lifetime/frozen/noninvalid及custom seal，發布前後各一次。ignoreDecisionTable只回傳兩個可信常數，圖片missing SQL與保留historical excluded SQL皆選family表；原NFO/probephase、coverage/skipped、unknown review-only、missing門檻、MaxEntries合計excluded、audit/history/epoch/late guard流程保留。舊FinishIgnoreJob仍拒family，public admission/worker尚未開放。

新增ignore_family_publication_test.go，classifyFamilyForPublication與familyExcluded helpers；從舊familySealFixture抽finishFamilyVerification，復核測試行為保持。新測試涵蓋observed/missing/excluded混合發布、被排除image歷史屬性/observed_revision保持、family圖片missing=1、舊入口拒絕、unknown不發布/不宣稱缺失、未seal/sourcecount錯/baseline deadline過期/終態寫入延遲300ms跨seal150ms整筆rollback。另驗大量excluded不稀釋missing分母、incomparable scope reset、合併歷史excluded計入MaxEntries。

真PG family-publication-first 7頂層pass、0fail0skip29.884秒；補邊界後family-publication-boundaries 8頂層pass、0fail0skip32.794秒，包含全部舊IgnorePublication回歸，sourceUnchanged皆true。原生非race family-publication-native 1pass、0skip15.658秒：TestFamilyIgnoreNativeStorage在原三路封存後設測試policy missing_percent_limit100並真FinishFamilyIgnoreJob，Missing1/noReview/succeeded，4筆被排除基線保持歷史revision，總數=新inventory+4。公開worker仍未串接，不能把repository原生鏈稱正式worker完整驗收。

Windows postgres編譯/純測試.067秒、相關vet/diff通過。尚需完整PG回歸、完整vet/build/其他相關門禁、文件/traceability才提交PR；schema18未改。本機所有handle結束。PR44兩PGpending110337371605/110337549493、macOS/Windowsrun-tests仍pending，其餘功能pass，完整品牌fail。當前feat/jelee-ignore-family-publication。

### 合併模式發布完整回歸通過

family-publication-full 真實 PostgreSQL race 239 頂層通過、0 fail、0 skip，327.366 秒，sourceUnchanged=true，證據 .testdata/inventory-family-publication-full-postgres-summary.json。Windows domain/scan/architecture、全 vet 與三命令 build 已通過；LICENSE 與需求原文雜湊未變。當前發布實作尚未提交，接續增量品牌、gitignore、diff 門禁後提交繁中 PR；正式 worker 與公開入口仍未完成。PR44 最新三平台 run-tests 與 foundation 通過，兩 PostgreSQL CI 仍執行中，完整品牌失敗。

### 主分支整合與 worker 讀取接續

使用者明確要求直接合入主分支，已透過普通繁中 PR45 合入 master，合併提交 1021996de3927413b0be664c7f861a11c6829ca3。主分支合併未改變 b7e7ee0165 已驗證的檔案樹。新分支 feat/jelee-ignore-family-worker 從 origin/master 接續。使用者正在討論倉庫歸屬與 token 成本，尚未同意搬移；目前只做本機工作，未新增遠端寫入。

新增 ReadFamilyIgnoreRoot / ReadFamilyIgnoreProgress，與舊入口共用私有讀取實作。family 路徑使用 comparisonModeFence 檢查模式、租約、取消、epoch 與兩份 manifest 有效性，最後仍以 commitIgnoreManifest 重驗。舊入口保持拒絕 family。真實 PostgreSQL race family-execution-reads：3 頂層通過，0 fail / 0 skip，23.075 秒，sourceUnchanged=true；包含初始/比較進度、外庫 root 不可讀、舊入口隔離、stale generation、取消、epoch、兩 manifest invalidation。Windows postgres 編譯與純測試通過。尚未做全套回歸，尚未提交。

下一步：request 讀取仍由 loadIgnoreRequest 限制為原模式，應設計明確的 worker 模式辨識及完整保留 request 身份驗證，不可盲目放寬公開入口。接續 family app ports、runner dispatch/claim capability、三路 observer 復核、NFO/probe 階段守衛與正式 worker 原生驗收。

### 開發倉庫搬移

使用者要求改由自己的帳號管理開發，已建立公開倉庫 https://github.com/Carinoasd/Jelee 。本機 origin 改指向該倉庫；原朋友倉庫保留為 friend，上游保留為 upstream。後續 PR 與推送使用 Carinoasd/Jelee。保留完整主分支歷史、LICENSE 與原有歸屬；搬移不轉移既有著作權。朋友倉庫不刪除、不撤回、不改寫。此提交包含已通過真實 PG 專項與 vet 的 worker 私有讀取進度，完整 worker 尚未完成。

### 原倉庫所有權轉移完成（取代上段搬移安排）

使用者刪除剛建立的獨立倉庫後，原 MoYuanCN/Jelee 已正式轉移為 Carinoasd/Jelee。已查證 Carinoasd 為 ADMIN、MoYuanCN 為 WRITE。origin 現在指向轉入的原倉庫，原 PR/歷史保留；先前獨立倉庫的 PR1 已不存在，不可當作本次工作連結。主分支仍為 1021996de3，最新私有讀取提交 c7b8e21e5b 比主分支多一個提交，完整備份保留於 .testdata/jelee-before-repository-delete.bundle。接續將該分支補回轉入的倉庫。
