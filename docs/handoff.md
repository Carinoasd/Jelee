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
