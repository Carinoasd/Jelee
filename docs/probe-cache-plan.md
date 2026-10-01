# 探测快取：本段交付与下一段

需求来源：[原文](requirements-source.md) G09.3、G13.4、G19.3–G19.5。用户要求每段完成后验证、推送并提 PR。3C2A 完成后曾暂停切换模型；2026-10-01 用户明确「继续吧」，现已恢复 3C2B。

## 已交付：3C2A 数据库契约（PR #6）

已实现的范围和限制见[契约](probe-cache.md)，实际执行证据见[验证报告](probe-cache-verification.md)：

- 新增 schema 4；000001–000003 保持原文。
- 不启动程序的安全 `Inspect`，有界 edge fingerprint。
- 固定工具/runtime/parser/argv/sandbox 身份及不可变 `tool_versions`。
- 单版本快取、正/负 TTL、库/item generation 失效、强制重建 phase。
- parent/file leases、连续 prefix 检查点、取消/恢复/历史清理与 quota 精确对帐。
- 最多32项分页、16项 hit 提交、每 parent 一项 miss lease；有界索引淘汰与回收。

3C2A 的验证范围仅限数据库契约。当前 3C2B 的接线和公开操作见[worker契约](probe-worker.md)，其执行证据单独记录；完整第3阶段仍未完成。

## 当前：3C2B 扫描 worker 与 API

以下为本段实现约束清单，实际通过与限制以验证报告为准；不能仅凭清单将需求标记完成。

### 请求与迁移

- 新增005迁移，保存每个 opt-in job 的唯一 `probe_requests`：intent、可信 identity、scope generation、固定错误。
- 扫描请求默认 `probe=false`；显式开启才执行 metadata phase。
- library/item rebuild 必须使用 Idempotency-Key。失效、请求、job入列和audit在同一短交易；相同请求重放不再次递增 generation，body/mode/item/priority 不同则 conflict。
- 新提交/重试共用 actor/key 空间，比较完整 intent；不能沿用只比较库和 priority 的旧提交检查。
- 可信 identity 与 request 在同交易注册/引用，避免启动注册后被 Sweep 回收的间隙。请求 FK 保留 identity；历史清理后才可回收。
- 公开重建在 enqueue 时递增 generation；之后 Begin 使用记录的 generation，不再次递增。未完成请求存在时005 down 拒绝，先停 workers/处理租约；失败迁移的 dirty 状态不可自动 force。

### Worker 与 runtime

- app 只依赖 `MetadataProber` 和 repository ports；platform bridge 将既有 adapter Observation 转成可信 ProbeObservation，失败仍是零结果。
- 先读持久请求/phase，再决定继续 inventory 或 probe；恢复时区分 running/done/aborted，不重新修改已冻结 inventory。
- 每个 worker 按 keyset page 协调，所有开档、Inspect、探测都在 SQL 交易之外。固定本地 process gate 在取得 DB file lease前取得；不创建每文件 goroutine/无限队列。
- 命中只提交连续 prefix；miss 一次一项。最终 Inspect 和 Observation identity/stamp 必须匹配 reserved candidate。保存负结果前再次 Inspect，并确认 parent context 尚有效。
- global lease 暂时全满是 busy/backoff；真实储存 quota 不足才是 capacity。心跳取 parent/file TTL 的较小值，间隔最多其三分之一，DB timeout 也必须更短。
- helper/runtime 故障停止该 phase，以既有 `scan_unavailable` 结束；不把全库标成 `probe_failed`，保留已提交快取。坏媒体/单项 timeout 继续下一项。
- 默认关闭探测时不注册 identity、不初始化 scratch/runner/sweeper。Windows和隔离缺失明确停用；缺工具仅使相关能力降级，不阻止账户与已存在原档直投服务。
- 能力不可用的实例不领取 opt-in probe job。诊断/能力状态可查询；readiness不每次启动ffprobe，也不降级为裸ffprobe/ffmpeg。

### API 与公开资料

- 管理员扫描 opt-in、library/item rebuild、job probe summary；每项在交易内重新验证有效 session/admin。
- HTTP只接收已登记的library/item/job ID，不接收绝对root、工具路径、argv或manifest。
- 首版 summary 只回固定进度/计数/错误；不要增加任意 cache row/raw metadata 枚举入口。
- 路由、OpenAPI、CLI、四语错误和部署文档同步。独立 invalidate 若以后公开，需有界幂等ledger，不能在请求重放时重复 bump。

## 本段的真实 1,000→0→17 验收

验收目标使用独立 PG schema、受隔离真实 ffprobe、非 root 只读生产实验容器和自己生成的 MP4 A/B；不碰用户媒体。实际结果已入[本段验证报告](probe-worker-verification.md)：181.871秒/12.034秒/14.436秒，实际metadata子程序启动1,000/0/17次。

1. 建1,000个路径的A hardlinks（不支持时副本），首次成功探测1,000、失败0、lease0，核对 metadata/cursor/实际 JSONB quota。
2. 原档/identity/TTL不变重扫：Inspect1,000，实际metadata runner增量0，hit1,000。
3. 用B替换17个 directory entries；不得原地改写共享inode。逐路径核对只有17个完整key改变，其余983的SHA/size/mtime/edges保持。
4. 再扫：实际runner增量17、hit983，metadata对应A/B，全部原档SHA保持预先准备值。
5. 实际执行次数必须对应有效 ffprobe结果；health/version调用另计。记录峰值并行、真实工具identity、hardlink/copy比例、行数/payload、耗时及filesystem。

另验坏媒体不中断、parent/child取消、租约到期、crash前后检查点、工具故障、重建幂等、撤权、默认关闭及Windows停用。合成1,000路径不等同1,000种codec或真实片库性能。未取样中段修改与同quick key替换是已记录的限制。

## 后续仍需交付

fsnotify/去抖、ignore规则、监看/排程、NFO/图片任务、MediaInfo/mkv工具、特殊codec真样本及10万/50万/24小时规模验收。完整品牌和发布门禁仍失败；不得用本段结果替代它们。
