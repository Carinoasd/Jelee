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
