# NFO 工作流程、管理接口与图片比较（3C3C）

本文件描述 3C3C 的实现契约，源码 `47da27b5ff090a71e6f55aae0b2e6c00871f825b`。跨平台、真实混合库、取消恢复及关机结果见[实际验证](nfo-worker-verification.md)；此前解析器、来源与缓存证据见[NFO来源验证](nfo-source-verification.md)与[缓存验证](nfo-cache-verification.md)。本段不会写回XML、修改原媒体、下载图片或把验证摘要导入Catalog。

## 提交与执行

库的 NFO 模式默认为 `off`。管理员先将库设为 `read-only`，再显式提交 `nfo=true`；仅启用库模式不会给已有或普通扫描增加解析。

`POST /api/v1/libraries/{id}/scan` 接受严格 JSON：

```json
{"priority":"manual","probe":false,"nfo":true}
```

省略 `nfo` 与 `false` 等价。影片探测和 NFO 分别检查能力；NFO-only 可独立于 ffprobe 运行。两项均启用时，公开意图与可信身份在同一事务中保存，执行顺序为 inventory → NFO → probe → 完成任务。

提交必须有一个 `Idempotency-Key`。同一有效管理员重放相同请求会返回原任务，即使库已关闭或 reader 后来不可用；不同意图返回冲突。重放不改变身份、generation 或检查点。新建返回 202，保留重放返回 200，并带 `Idempotency-Replayed: true`；两者提供任务 `Location`。

恢复原任务使用原身份和游标；身份或根映射漂移会以固定原因失败，不在恢复时重新绑定。重试 failed/cancelled 任务会创建新任务，继承原公开意图，并重新核对当前能力和库策略。旧版 B 的 read-only 阶段没有公开提交意图，不能推断为 C 请求；这类历史任务须重新显式提交，retry 返回冲突。

所有普通提交入口也保存 frozen-off 请求。旧内部 `PrepareNFOPhase` 只能核对已有冻结内容，不能把一个已入队的 off 任务变成只读解析任务。

## 读取、缓存与资源

每个 NFO 先完整读取并计算 hash，匹配冻结 inventory 的 size/mtime 后查询缓存。命中省略 XML Parse；未命中解析本次保留的字节。提交前再次完整读取/hash，命中也一样。两次 stamp 不同则记录来源改变，不保存 XML 有效/无效结论。

XML/编码/语义问题可保存固定验证摘要；取消、超时、来源不可用和 DB 错误不变成坏 XML。源码原文、任意 metadata、绝对根路径、hash、租约及内部错误不进入 API。

- 每个进程固定两个 NFO slot；slot 覆盖初读、查询、解析、末读和提交。
- 原文默认上限 8 MiB，单次 Read/Parse context 为 30 秒。Open/Stat 的内核阻塞不保证能被 context 立即打断。
- 最多取 16 个轻量候选，一次处理与提交一个文件；不把整页原文保存在内存。
- 初读与末读的 buffer 受并发和输入大小限制；这不是整个进程 RSS 的硬上限。
- 保留原 parent heartbeat，不增加 NFO child lease。取消或关机须等待文件操作、worker 与维护退出后关闭数据库。
- 每分钟维护一次，每次最多处理 128 项，DB 时限两秒；缓存和 TTL 见[缓存契约](nfo-cache.md)。

进程内 reader 统计分别记录 ReadCalls、成功读取次数/字节、完整 hash 次数、ParseCalls 和当前/峰值调用数。失败读取可能只读了一部分字节，成功字节统计不包含这部分；峰值调用数也不等同于 slot 全生命周期。持久 job 计数只代表已提交结果，不能代替实际执行次数。

完整 hash 准确描述本次保留的字节；读取前后身份检查仍有检查点之后的竞态，不能保证并发原地写入下的文件系统原子快照。

## 管理接口

以下接口均要求当前有效的管理员会话；包括历史读取、缓存读取和保留重放。使用现有工作服务并发预算、请求时限、严格 JSON/query 与四语言固定错误。

| 方法与路径 | 输入或结果 |
| --- | --- |
| GET /api/v1/libraries/{id}/nfo/policy | libraryId、mode、generation |
| PUT /api/v1/libraries/{id}/nfo/policy | `{"mode":"read-only","expectedGeneration":1}`，另需 Idempotency-Key |
| POST /api/v1/libraries/{id}/nfo/validate | `{"priority":"manual"}`；等价 nfo=true/probe=false，不修改库模式 |
| GET /api/v1/jobs/{id}/nfo | 历史 mode、阶段、已提交计数与固定错误码 |
| GET /api/v1/jobs/{id}/images | added、changed、unchanged、missing、uncompared、comparisonComplete |
| GET /api/v1/libraries/{id}/nfo/current-validations | cursor UUID；limit 默认 20，最多 50 |
| GET /api/v1/libraries/{id}/nfo/current-validations/{observationId}/issues | offset 默认 0，上限 64；limit 默认/上限 32 |

策略更新用 expectedGeneration 比较并交换；同模式不递增，off→read-only 的再次启用产生新代。策略改变不会让影片 probe cache 失效。

### 当前观察与历史计数

任务 summary 保留历史计数。逐文件 API 返回当前库范围内未过期且 reader/root/policy generation 匹配的数据库观察，**不会在 GET 时重新读取文件**，也不是某个历史任务的文件报告。

新解析替换观察 UUID。缓存命中仅更新内部 last_used_at，不刷新公开 observedAt、TTL 或 observationId；过期、淘汰、关闭策略、根变动或被新观察替换的旧 observationId 返回 not-found。列表以 UUID 游标排序，跨页不保证快照。

每项只返回 root ID、已验证的相对路径、固定状态、条目数、问题计数、parse failureCode 与观察/到期时间。路径最多 1,024 UTF-8 bytes。坏 XML 可能有 failureCode 而没有 issue 行，客户端不能把空 issues 当作有效。

issues 只分页返回已保存的前 64 个固定问题；每页最多 32。issueCount 是完整计数，issuesTruncated 表示超出前缀。读完前缀后没有 nextOffset，即使 issueCount 更大；未保存的后续问题不可继续查询。响应不包含任意 XML 字段值。

## CLI

保留独立本地命令：

```text
jelee-cli nfo validate --root ABSOLUTE_ROOT --file RELATIVE_FILE
```

远端管理命令读取 stdin 中的会话令牌，令牌不放进 argv 或 URL。URL 沿用 HTTPS/loopback 限制并拒绝重定向；返回内容有大小上限，严格解码后只输出公开 DTO。

```text
jelee-cli nfo policy-get --library UUID --token-stdin
jelee-cli nfo policy-set --library UUID --mode read-only --expected-generation 1 --key KEY --token-stdin
jelee-cli nfo validate --library UUID --key KEY --token-stdin
jelee-cli nfo current-validations --library UUID --limit 20 --token-stdin
jelee-cli nfo issues --library UUID --observation UUID --offset 0 --limit 32 --token-stdin
jelee-cli nfo job --id UUID --token-stdin
jelee-cli nfo images --id UUID --token-stdin
jelee-cli jobs scan --id UUID --nfo --key KEY --token-stdin
```

可加 `--url` 指定服务。远端 validate 与本地 `--root/--file/--max-bytes` 显式互斥；validate 不自动启用库策略或影片探测。

## 图片属性与未知基线

图片比较仅基于 inventory 的类别、size、mtime。相同属性不证明图片内容相同，也不验证图片格式。

已知同路径图片属性不变为 unchanged，size/mtime 不同为 changed；没有旧路径或旧类别为非图片时为 added。已知旧图片消失或变为非图片时可计 missing。迁移前只有路径的旧行保持属性未知，同路径新图片记 uncompared，不伪造过去的类别或属性。

根增删、路径或归属变化产生独立 inventory epoch。任务在提交时捕捉 epoch，完成时锁住库并再次核对；漂移拒绝发布基线。旧 epoch 的属性也不可当作当前来源的旧观察。

只要扫描不完整、有 skipped、失败、取消或旧未知属性使比较不完整，就返回 `comparisonComplete=false, missing=0`。此时零不表示所有文件仍存在；已观察到的其他计数可能只是部分结果。一般任务原有的路径 missing/review 仍按其原规则显示。

尚未提交图片比较结果的任务，例如 release 后在 queued 状态取消、租约耗尽直接结束，按该任务已保存的图片 inventory 数量返回 uncompared。查询不使用后来可变的基线推测旧任务的 added/changed；后续任务建立新基线不会改变这些旧计数。此时所有 observed images 都表示尚未完成属性比较。

只有完整成功、无 skipped、缺失未触发 review 阈值的任务，才能原子替换已接受基线。failed/cancelled/review 保留原基线，重复扫描不能自动消除待确认的大量缺失。首次完整成功扫描可以建立已知属性供下一次比较。

当前/旧 inventory 均受明确行数上限约束，SQL 聚合只返回计数。图片比较需要扫描/连接有限库存，不能称为常数耗时；超时回滚，不扩大为无界事务。

## schema 007 与回滚

只新增 007，已发布 001–006 原文不变。新 binary 要求 clean schema 7。

升级前停旧 worker，结束或明确取消所有仍 queued/running 且带 B read-only phase 的旧任务，包括 phase 已 done/aborted 的 parent；007 up 会拒绝这些无 C request 的活动任务。不能删 phase、改成 off 或推测公开意图来绕过检查。历史 B phase 保留查询语义。

回退前停止接收新任务，结束/取消并 join 活动任务；007 down 拒绝任何 queued/running 的 C request，包括 frozen-off。回退移除本段请求、图片属性/统计、inventory epoch 和 observation UUID，保留原路径 baseline、B 的策略/cache、Catalog 与原文件。回退丢失图片属性后，下次升级仍从未知基线开始。

不支持旧 binary 与 C worker 混跑，不自动 force dirty migration。真实 up/down/up、取消与关机验证的执行范围和限制见[本段报告](nfo-worker-verification.md)。
