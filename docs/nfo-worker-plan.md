# 3C3C：NFO worker、当前验证结果与图片增量接线计划

**状态：未实现，未验收。** 本文以 3C3B 工作树中的 domain、摘要 reader、schema006 与 PostgreSQL 契约为基础；须在 B 的 PR 完成后另开实现阶段。这里只读核对代码，不表示已经接上服务、HTTP、CLI 或 worker。总范围与保留项见 [NFO 增量计划](nfo-incremental-plan.md)。

## 1. 最小交付与现有缺口

- `NFOReader.Read → Stamp → Parse` 已能提供完整原文 hash 与安全验证摘要；`NFORepository` 已有等待/运行/完成阶段、连续 checkpoint、正负缓存与独立配额。它不需要 ffprobe。
- 目前 `PrepareNFOPhase` 在取得 parent lease、inventory 尚未开始时捕捉当前策略，**还不是提交工作时的快照**。C 必须补入队请求；不能直接在 worker 启动时调用 Prepare 并宣称意图已经冻结。
- 现有 worker 的 `execute` 只载入 probe request/phase；probe 已完成时会直接返回，scan-only 则直接扫描。必须改成统一阶段编排，不能在这两个分支后简单追加 NFO。
- B 的缓存只保存每个 root/path 的当前观察，phase 只保存计数。C 采用“工作历史计数 + 当前库验证结果”，不建立一份无独立配额的逐工作 XML 摘要副本。
- 不做 Catalog 合并、NFO 写回、图片解码/下载、自动关联剧集或多集条目、fsnotify、调度。图片仅比较文件存在、类别、size、mtime；不声称已验证格式或内容相同。

## 2. 入队、重放与重试：显式 opt-in

现有 OpenAPI 名称是 `ScanRequest`，HTTP 内部目前是匿名 `{priority, probe}` 结构。C 新增共享 `ScanRequestBody` 时保留这两个字段，增加可选 `nfo:boolean`，默认 `false`；继续严格拒绝未知字段、重复键、错误类型。旧客户端省略 `nfo` 与显式 `false` 等价，仍只执行 inventory 及原本选择的 probe。

| 请求 | 新工作接受条件与冻结内容 |
| --- | --- |
| `nfo=false` 或省略 | 有效 NFO mode 固定为 `off`；不因库后来启用而增加工作。probe 条件维持现有行为。 |
| `nfo=true` | 新提交要求 live admin、当前库 `read-only`、本机 NFO reader 可用；库 off 返回固定 `nfo_disabled`，不自动改策略，也不接受无实际验证的 validate 工作。 |
| `probe=true,nfo=true` | 两种能力分别检查；一次事务同时保存两种意图和可信身份，不能先入队再补 NFO 请求。 |

建议新增 `nfo_job_requests`：job/library ID、原始 `requested` 布尔值、有效 mode、完整可信 NFO identity/digest、library NFO generation、固定 request error。所有新入队路径都写记录，包含 off；job 外键随现有有限历史清理。reader identity 来自本机固定构造，不接受 HTTP parser/version/hash 参数。off 记录可保存同一个本机固定 identity，但绝不因此执行解析。

入队事务先验证 live admin、查原 key，再检查新工作的能力、策略与容量；同 key 比较 library、priority、probe intent、NFO requested 及 retry parent。完全相同则返回原工作，即使当前能力已不可用或库已 off；不能重写 mode/identity/generation 或重复失效。不同 body 返回 conflict。重放也须在提交前重新检查会话。

**恢复与重试分开：**

- 同一个 job 的 lease 恢复使用原请求、原 identity、原 generation 和 cursor；任何当前漂移只允许固定原因中止，不重新绑定。
- `POST /jobs/{id}/retry` 仍只对 failed/cancelled 建立新工作。复制原公开 intent；若原来 `nfo=true`，新重试须重新满足 read-only，并捕捉当前可信 identity/generation。原 C 请求明确 `nfo=false` 的重试仍 off。原 probe scope 沿用现有“新工作采用当前可信工具”的规则。
- 只有既无 C request、又无 B read-only phase 的工作可解释为 legacy off，重试也 off。B 的 `PrepareNFOPhase` 可能已记录 read-only，不能因为缺少 C request 而覆盖这个事实；007 的升级阻挡规则见第 8 节。历史 B phase 保留 mode/状态/计数的查询语义；对缺少公开提交 intent 的 B read-only 历史工作，C 的 retry 返回 conflict，要求新提交显式 `nfo=true`，不猜测或静默降级。
- 库模式 off→on 产生新 NFO generation，不能复活旧请求。NFO 模式/缓存变化不触碰任何 probe generation。

## 3. 必须新增或调整的内部契约

以下名称为 C 的建议接口，不是当前可调用 API；保留旧构造器和方法作为默认 NFO off 的兼容入口。

| 层 | 最小变化 |
| --- | --- |
| domain/app 入队 | `ScanIntent{Probe ProbeIntent, NFO bool}`；新增统一 `ScanJobRepository.SubmitScanWithStages / RetryScanWithStages`，一次事务处理两种请求。可信 identity 参数与公开 intent 分开。`Jobs.SubmitScan` 原签名包装为 NFO false，新方法接受完整 intent。 |
| domain/app 执行 | `NFORequest / NFOWork{Request,Phase}`；`NFOExecutionRepository` 嵌入 B 的 `NFORepository`，增加 `LoadNFOWork / BeginRequestedNFOPhase / AbortNFORequest`。Load 可返回失效请求供恢复/中止；Begin 才严格验证身份与当前 scope。 |
| PG 阶段准备 | 入队保存请求并初始化 waiting 或 frozen-off phase；内部共用 SQL helper，不伪造 `JobLease` 调 B 的 Prepare。已有请求的 Prepare/Begin 只能匹配冻结内容，不可重新读取策略后覆盖。 |
| Claim | 新增 `ClaimJobWithCapabilities(..., ScanCapabilities{Probe,NFO})`；两类请求分别过滤。更新后的旧 Claim/ClaimWithProbe 包装器把不支持的 NFO 能力传 false，避免误认领。 |
| worker | 保持 `jobs.New` 参数；`Options.NFO *NFOOptions{Repository,Reader,MaxConcurrent,FileTimeout,Available}`，生产固定并发 2、单次调用 30 秒。无 NFO options 仍需使用 capability-aware claim。 |
| runtime | `EnableJobs` 下独立构造固定 8 MiB 的 `SummaryReader`、确保 B 的 cache policy；不依赖 `EnableProbe`、tool identity 或 Linux sandbox。库 default off 加请求 opt-in 是两道执行开关；不新增 HTTP 可调解析预算。 |
| 查询 | 新增 live-admin `NFOQueryRepository`：工作 summary、当前库观察列表、单观察的固定 issues；不把内部 `NFOEntry/NFOSource` 作为公开 DTO。 |

`PrepareNFOPhase` 的 B 契约及测试仍保留；C 的生产入口使用上述提交时绑定路径。B 的 committed progress 不足以当作实际 CPU/IO 次数；另在 reader 边界提供进程内只读统计：实际 Read 调用、成功读字节/hash 数、Parse 调用、当前和峰值活动数，用于真实验收，包含被取消/重试的实际调用。不能从缓存行数推算 Parse 次数。

## 4. 阶段顺序与资源生命周期

每次 claim 后先载入两种 durable request/phase，再决定下一步：**inventory → NFO → probe → FinishJob**。NFO-only 工作在 Windows、probe 被关闭或工具缺失时仍可运行；同时请求两种能力的工作不降级成另一种工作。

- legacy/off：按第 2 节同时检查 request 与 B phase，不建立新的只读意图；inventory 后跳过 NFO。若意外遇到无 C request 的活动 B read-only phase，拒绝执行并报告固定 conflict，不按 off 处理。
- waiting：只恢复未完成目录；inventory 完整后 Begin NFO。若 NFO/probe 已 running/done，禁止重新扫描被冻结的 inventory。
- NFO running：从已提交 cursor 继续；done 跳过解析；非 off 的 aborted 固定失败，不进入 probe，也不成功替换 baseline。
- NFO done + probe waiting/running：继续 probe。probe done 也必须检查 NFO 和 inventory 状态，不能沿用现有提前成功返回。矛盾状态 fail closed。
- policy/identity 漂移、缓存容量、reader 基础故障须持久化固定阶段原因；XML 无效只是一个 invalid 文件，可继续处理。DB 错误、租约失效和取消不可变成 XML 负缓存。

保留现有固定 worker 数和单个 parent heartbeat goroutine。NFO 使用进程共享的两个 IO/CPU slot，在首次 Read 前取得，处理完本项后释放；无每文件 goroutine、无整库 Source 列表。page 只取最多 16 条轻量 inventory；初版每次处理和提交一项，符合 B 的“至多 16 个连续命中或一个新结果”契约，无需先优化批量命中。

单项流程在 SQL 事务外执行：

1. Read/hash 得到私有 Source；校验完整 identity 和 Stamp，size/mtime 必须匹配冻结 inventory。读取 changed/unavailable/too-large 分别产生零 stamp 的 changed/unavailable/rejected。
2. 用完整 SHA 的 candidate Lookup。命中不调用 Parse；miss 才 Parse 同一 Source。仅 B 白名单的固定 XML/编码错误或语义 error 可形成 invalid summary。
3. **命中与新解析都在提交前再 Read/hash**，完整比较初始和最后 Stamp；最后读取不 Parse。变更/失败清空 summary，按来源错误分类。哈希相等不消除检查点之后的竞态，也不保证敌对原地写入下的原子快照。
4. 检查 parent context 和 reader identity 后短事务 Commit；SQL 再核对 parent lease、取消、mode/generation、cursor、cache key/TTL。冲突后 Load cursor 并重新观察；不能沿用旧 stamp 重试。持久重放不重复计数。

每个 Read/Parse 有独立 30 秒 context，parent cause 优先。初始/最终读取或 Parse deadline 都记来源 unavailable，不能当成 invalid XML。context 不保证打断所有内核 Open/Stat；不启动不可回收的“超时后遗弃”goroutine。两个 slot 最多保留各两个原文 buffer，默认原文预算合计 32 MiB，另有有界 XML/编码/摘要内存；不是 RSS 硬上限。

heartbeat 继续按 parent/probe child 较短 lease 的三分之一，DB timeout 更短；NFO 不新增 child lease。停止时取消并等待文件操作、解析、heartbeat 返回后，再释放 parent/关闭 pool。Join 未完成必须报告超时，不能声称已安全停止。

## 5. 管理 API、CLI 与“当前结果”的含义

所有端点复用 jobBudget、认证、严格 JSON/query、live-admin 交易末检查；OpenAPI、CLI 严格响应解码、错误映射与四语言固定错误同步增加。禁止原 XML 中的 title/plot/people/外部 IDs/URL、绝对 root、hash、lease 或任意内部错误进入响应。

| 建议端点 | 返回/行为 |
| --- | --- |
| `GET/PUT /api/v1/libraries/{id}/nfo/policy` | B 的 mode/generation；PUT `{mode,expectedGeneration}` 和 Idempotency-Key，read-write 拒绝。 |
| `POST /api/v1/libraries/{id}/nfo/validate` | `{priority}`，等价新 scan `nfo=true,probe=false`；要求 read-only，返回 202/原重放 200，不改策略。 |
| `GET /api/v1/jobs/{id}/nfo` | 冻结 mode、阶段、B 的持久 progress、固定 error；历史 B phase 即使没有 C request 也保留这些字段，**不承诺历史逐文件 issues**。 |
| `GET /api/v1/libraries/{id}/nfo/current-validations` | keyset 分页、默认 20/上限 50；仅当前 mode/gen/reader identity/root generation/TTL 匹配的缓存观察。每项 observation ID、root ID、合法相对路径、固定 status/counts/observedAt/expiresAt；列表不带 issues 数组。 |
| `GET /api/v1/libraries/{id}/nfo/current-validations/{observationId}/issues` | 固定 code/severity/field/entry；ordinal cursor、每页≤32，总可取前缀≤64，保留完整 issueCount 和 issuesTruncated。不声称可分页取回未存储的其余 issues。 |

给缓存增加生成的 observation UUID 和 `(library_id,observation_id)` 索引；新解析替换行时更换 UUID，命中只更新 last-used。issues 查询绑定 library/observation ID，再做完整当前作用域和 TTL 检查；替换/淘汰/过期返回安全 not-found，不把新观察伪装成旧观察。off 不公开旧验证结果。

“current”仅指数据库中**尚未过期且作用域有效的最近观察**，不是 HTTP GET 时重新读过文件，也不是某历史 job 的快照。分页期间新观察可出现或旧观察可消失；API 不保证跨页快照。路径限已验证的 root-relative inventory 名称，JSON 转义，CLI 校验后输出；不带绝对路径。需要历史逐文件报告时，应另立有容量与保留期的结果模型，不能借当前缓存完成。

保留无 DB 的 `nfo validate --root ... --file ...`。新增互斥的 `nfo validate --library UUID --token-stdin --key ... [--url ...]`；复用现有 HTTP CLI 的 token stdin、HTTPS/loopback、无重定向和响应白名单。另提供 policy/current-validations/issues 与 job NFO summary 子命令；token 不进 argv/env。CLI 的 library validate 不调用 ffprobe、不自动启用 NFO。

## 6. schema007：图片属性基线与迁移后的未知比较

只新增 007，不改已发布 001–006。除请求和 observation ID 外，扩充 `library_inventory_baseline` 的可空 kind/size/modifiedUnixNano、明确 attributes-known 状态，以及每 job 的固定图片统计（added/changed/unchanged/missing/uncompared、comparisonComplete）。

另外增加独立的 inventory root-mapping epoch：库内 root 增删、path 或归属改变时递增，不借 NFO mode 或 probe rebuild generation。新 job 入队时连同 root 集合捕捉本轮 epoch，恢复不得重新绑定；原 legacy job 若无可靠快照则不能发布新的图片属性基线，须明确标记不可比较。Finish 发布统计和替换 baseline 前，在同一交易锁住当前映射并重新比较 epoch，拒绝不匹配的发布，保留原基线。旧属性观察也记录对应 epoch；发生映射变化时先令它们不可比较，不能仅凭相同 root UUID/path 认为来源相同。这是数据库配置映射检查，不是文件系统原子快照保证。

- 迁移前的 baseline 只有路径。旧行保持属性未知，不从文件扩展名推测旧 kind，也不从当前 inventory 伪造过去 size/mtime。
- 当前图片与已知旧图片按 root/path 比较 size/mtime：相同为 unchanged，不同为 changed；没有旧路径才 added。旧已知图片不在当前 inventory 为 missing；旧未知且消失的路径无法分类成图片缺失，只进入既有 missing/review 机制，并令图片 comparisonComplete=false。
- 当前图片遇到同路径旧未知属性为 uncompared；类别从已知非图片变成图片计 added，反向计 missing。固定 DTO 注明这些是属性/类别观察，不是图片内容 hash 判断。旧库首次升级扫描不能把全部同路径图片报新增或改动。
- inventory 未完成、有 skipped，或工作 failed/cancelled 时，图片 `missing=0`、`comparisonComplete=false`，保留基线；零表示此次不能确认缺失，不表示已证明全部存在。已观察到的其他计数只能标为部分结果，禁止用未遍历目录作反向缺失统计。
- 比较与属性 baseline 接受保持同一 fenced FinishJob 交易：仅完整成功、无 skipped、missing 未触 review 阈值时替换；failed/cancelled/review 保留旧属性和路径 baseline。重复 mass-missing 不得自动清零。下一次成功完整扫描才能消除未知属性。
- 复用库存的每 job 条数上限（默认 100,000，硬上限 500,000）、相对路径 1,024 UTF-8 bytes 和固定数值字段；按 library/job/root/path 索引连接，在 SQL 聚合，Go 不载入整库结果。旧 baseline 也须有同等明确行数上限。交易超时完整回滚，不能宣称最终聚合是常数耗时；若实际大库不能在 DB 时限内完成，应另做有 checkpoint 的准备与原子发布，不能放大为无界交易。

## 7. 必须实际执行的验收

1. **真实 1,000 文件混合库**建议固定 100 video / 400 NFO / 500 image。NFO 包含有效、语义 error、warning、多集、UTF-8/UTF-16/GBK、固定坏 XML；均在输入上限内。冷扫实际 Parse=400、影片 helper launches=100；图片 added=500。
2. 同输入/identity/TTL 暖扫：Parse=0，影片 launches=0，图片 unchanged=500。完整初读和最终读/hash 仍执行；按本方案成功 NFO 每项两次完整读取，实际 bytes/hash/call 统计如实记录。invalid 样本在负 TTL 内验 negative hit。
3. 替换 17 个 NFO 目录项、改变 23 张图片的 size/mtime，再提交两项都启用的普通增量 scan：实际 Parse=17、NFO 命中=383、影片 launches=0，图片 changed=23/unchanged=477。逐路径核对观察；共享 hardlink 必须替换目录项，不能原地更改共享 inode。
4. 另做 **100 文件**冷/暖/仅改 NFO/图片基准，报告构成、字节、运行环境、缓存状态、耗时与真实调用次数；不以 1,000 文件证据代替原需求的 100 文件案例，也不由单次样本声称 P95。
5. 单元 mock 仅验证调度/边界；规模证据必须由真实 reader/parser、真实 PG、生产 worker 和实际媒体 runner 得到。保存原素材 hash/size/mtime、source hash、配置和脱敏命令，不把 committed Parsed 推算成实际 Parse 调用。
6. 真正覆盖 queued intent、旧 job/retry、mode off→on、同 key 变 body、当前身份漂移、bad XML 隔离、finalRead 变化、cache expiry/eviction、配额失败、取消/停机恢复、租约 ABA、撤权后 GET/重放、历史计数与 current-results 区分、旧图片属性未知。Windows 与原生 Linux/race 分别记录，网络文件系统硬取消限制保留。
7. 图片边界用真实目录与 PG 验证：先建立完整基线，再令目录不可读或中途取消，断言 `missing=0`、`comparisonComplete=false` 且基线不变；恢复权限后重扫，确认正常比较。另在最后一批 Save 完成后修改数据库 root 映射，再调用 Finish，必须拒绝发布且原 baseline/统计不被部分更新；恢复采用新 job 的新 epoch，不能改旧快照绕过。
8. 007 upgrade fixture：分别建立无 C request 的 B read-only waiting/running/done phase，并保持其 parent queued/running，升级必须拒绝且数据不变；完成或取消 parent 后才可升级，历史 mode/计数仍可查询。另验证真正无 NFO phase 的旧工作维持 off，不因当前库 read-only 被启用。

## 8. 默认与回滚

库默认 off；客户端省略 NFO 为 false，但既有 B read-only phase 不受这个默认值覆盖。现有单文件 CLI 不变。C 不扩大普通用户权限，不改人工 metadata 或原文件，不把 NFO/图片变化转成 probe rebuild。

007 升级前停止旧 worker，并检查 B 的 `nfo_job_state`：任何没有 C request、mode=read-only 且 parent 仍 queued/running 的记录都使升级明确失败，包括 phase 本身已 done/aborted 的记录；须先完成或明确取消 parent，再重试迁移。禁止为绕过检查把这些记录改成 off、删除 phase 或从当前策略重新推断意图。历史 phase 原样保留并用于历史查询。升级并启用 C 后不支持旧二进制与 C worker 混跑；仅更新新代码的 claim 包装器不能限制一个 SQL 不认识 NFO 请求的旧二进制。

回退前先停接收新 NFO 工作，完成或明确取消并 join 活动工作，核对没有残留 parent/probe child lease；007 down 必须拒绝仍 queued/running 的 C 工作，不能删除请求使它们悄悄变成 scan-only。回退删除的仅是新增生成请求/图片属性统计和 observation 标识；路径 baseline、Catalog、原 NFO/媒体与 B 的策略/cache 保留。再回退 006 依其独立 guard 和生成数据损失说明执行，禁止 force dirty 或改旧 migration。所有回退行为须真 PG up/down/up 验证。
