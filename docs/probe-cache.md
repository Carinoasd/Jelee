# 持久探测快取契约（3C2A）

本段建立 domain、PostgreSQL repository、schema 4 和不启动程序的 `Inspect`。本文保留3C2A数据库层契约。3C2B已将其接入显式开启的worker、配置和HTTP/CLI，见[接线契约](probe-worker.md)。默认扫描仍只盘点；公开工作流与原始数据库接口的区别如下。

## 有效性键与身份

每个 `(root_id, relative_path)` 只保留当前一份 observation。命中同时要求 size、signed int64 纳秒 mtime、`edge-sha256-v1`、工具身份、库/root/item generation 和 TTL 一致。不同路径、不同库不做内容去重；未关联 catalog item 的文件不捏造 item ID。

`Inspect` 安全只读开档，通过同次 FD 的 stat→首尾最多128 KiB→stat，并重开当前路径核对文件身份；失败回传零 stamp，不启动 ffprobe。快速键无法证明全内容，也无法证明跨调用的对象身份：相同 size/mtime/edges 的替换和未取样中段修改仍可能命中。不能将它称为原子快照。

可信 runtime 从嵌入 pins 计算身份：platform、vendor/upstream/source revision、ffprobe SHA、按固定 container path 排序的8个 runtime ELF（含 loader）SHA、实际固定 argv 摘要、parser/schema/fingerprint 与 sandbox policy 版本。`tool_versions` 保存不可变身份；任何 DB 字段都不能选择可执行文件、参数或授权降级。读取身份不代表执行能力可用，仍须通过受保护 runtime 工厂与实际诊断。

## Phase、检查点与租约

保留 `inventory_scan` kind，在所有目录 done 后才可由可信 worker 建立 probe phase；未启用时不会自动建立。phase 开始后不再修改 inventory。每次读最多32项；命中结果可连续提交最多16项，miss 一次只取得并提交 cursor 后第一项，每个 parent 最多一个 file lease。

`library_rebuild`/`item_rebuild` 只在首次 Begin 分别递增库/目标 item generation；相同 Begin 重放不再次递增，已有 phase 仍校验当前 scope。3C2B公开重建改用持久request：enqueue时失效和入列同交易，BeginRequested只使用保存的generation，不再次失效。直接数据库Begin接口保留原契约。

读取/取得/提交 phase 的操作验证 parent 的 owner/generation、DB 时钟下的有效租约和取消旗标；Release 与回收允许取消后的清理。file lease 的 generation 来自 `NO CYCLE` bigint sequence；删除并重新插入 cache row 不重用旧 fence。提交重查 phase cursor、inventory 连续前缀、完整 stamp、当前 generation 和 file fence，最后再核对 parent/child 到期时间。结果、容量差额、计数和 cursor 同交易提交，已推进 cursor 的 Commit 重放返回 conflict；Begin/Finish 的同一 phase 可幂等返回。

每实例程序并发与全域 DB file lease 是不同上限。parent heartbeat 仅延长尚未到期的 child，不复活旧 lease，且不超过 parent 期限。取消/释放/恢复/结束先清理 child，再沿既有 job 生命周期处理；history 清理不能透过 cascade 删除正在工作的 file lease。已启用的 phase 未完成时，job 不能宣告成功。

## 容量、过期与失效

policy 首次写入后，其他实例必须逐字段相同，不能默默提高额度。默认全域100,000 rows/1 GiB、每库50,000 rows/256 MiB、最多1,024 quota scopes、16 tool identities、2 active file leases；硬上限由 domain 和 SQL CHECK 同时限制。

每 row 计2 KiB allowance，active lease 另预留128 KiB 最大 metadata；ready 按 PostgreSQL `jsonb::text` 的实际字节计费。所有 official repository 插入/替换/删除和租约变化在短交易内调整全域与每库计数。一次 Sweep 或容量淘汰最多128 cache rows；Acquire 另可先清理最多8个失效 child leases，因此整次 Acquire 最多清136 rows。工具/库 scope 另有32/1,024个硬上限。容量不足时只做有界淘汰，仍不足就拒绝取得新 lease。

这些数字限制受管理的 row、序列化 payload 与预留额度，不是 PostgreSQL 实体磁碟 quota；索引、TOAST、MVCC/WAL/备份另占空间。受信 DB owner 直接修改 row/计数不属于 repository 的记帐保证。

正缓存默认30日，负缓存默认15分钟；hit 不延长硬 TTL，hit 的 last_used 最多每小时更新一次。连续失败计数饱和到10，表示计数有界；TTL 到期仍可重试，不承诺终身最多10次执行。库/item 失效递增单行 generation，立即使旧 row 不能命中或提交。root path 和 source mapping 的 trigger 保护 generation，官方 writer 先取得共同 jobs lock。

## 失败与边界

只保存通过 domain 白名单和大小检查后重新序列化的 metadata；不保存原始 ffprobe JSON、任意 tags、stderr 或本机绝对 root。存储 compact JSON 在 `jsonb` 表示下超过128 KiB时，记录固定 `probe_metadata_limit` 负结果，保留相同 fence/checkpoint 保证。

单项失败只接受固定媒体错误码：`probe_failed`、metadata invalid/limit、output limit、单项 timeout。changed/input unavailable 是独立计数；helper/runtime 故障、取消和 DB 失败不能伪装成媒体负缓存。未来 worker 保存负结果前还必须再次 Inspect，并确认 parent context 有效。

公共失效 repository 在交易内重新核对有效 session 和管理员。此阶段没有公开 HTTP 路由；重建入列、读取 summary/metadata、错误本地化与 worker 整合将在3C2B交付。

## 升级与回滚

新 binary 只接受 clean schema 4，旧 schema 3 binary 不能与它混跑。000001–000003 不修改；004 down 保留账户、库/root/item、inventory/jobs/baseline，丢失 probe cache、phase、identity 和 quota。回滚前须停止/释放 probe workers；migration 拒绝仍有有效 running parent 的 probe phase。

golang-migrate 在执行 down 前写入 dirty 标记。若上述保护拒绝迁移，数据库会保持 dirty，必须先检查实际 schema 和 worker 状态，再按既有迁移故障流程处理；不可自动重试或用 force 猜测版本。不得把测试迁移指向用户数据库。
