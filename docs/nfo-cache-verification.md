# NFO 策略与持久快取：3C3B 验证

源码提交 `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`，基于 [PR #8](https://github.com/MoYuanCN/Jelee/pull/8) 的 `082a51dc2b1206e9687a70c2292d33991e068d30`。日期2026-10-01，Asia/Taipei。

本段实现默认off的库级NFO策略、固定验证摘要、schema006快取、容量治理及parent租约下的连续检查点。接口与上限见[快取契约](nfo-cache.md)。它尚未接入scan worker或库级API/CLI；真实暖扫Parse次数、图片增量与公开结果由[3C3C计划](nfo-worker-plan.md)继续交付。

## 已执行验证

| 项目 | 实际结果 |
| --- | --- |
| [Windows全模块](evidence/nfo-cache-windows.json) | 三命令build、gofmt/vet、27个包测试通过。138个明确SKIP，其中125个为未设置DB的PG用例，其余为平台或专用工具条件；未跑Windows race |
| [原生Linux全模块](evidence/nfo-cache-native.json) | 固定Go1.27.1及已登记GCC/binutils，自建原生临时源码快照；三build、vet、`test -race -json -count=1 ./...`通过，27个包、474个顶层测试；真实PG全部执行、零PG SKIP |
| [NFO PG专项](evidence/nfo-cache-postgres.json) | 30个顶层测试及141个子测试通过，零SKIP，31.520秒。这轮为普通测试；最终全模块另以race覆盖 |
| [完整性门禁](evidence/nfo-cache-guards.json) | 新服务品牌扫描零违规、gitignore检查零违规、git diff检查通过。001–005十个迁移文件、LICENSE及原始需求保持不变；244个源码/配置文件与两平台验证快照哈希一致 |

Linux全模块另有6个专用媒体/沙箱案例明确SKIP：真实固定ffprobe、instrumented child覆盖、受保护路径、真实工具profile，以及需要独立UID线程预算的两个隔离案例。本段未改媒体runner或沙箱，不把这些SKIP计为通过；先前独立验收及PR #8远端结果见[NFO来源报告](nfo-source-verification.md)。本段的真PG与新增NFO测试无跳过。

最终原生Linux语句覆盖率：NFO解析包90.4%、domain97.9%、app89.0%、PG完整包80.6%，全模块81.9%。五个新NFO PG实现文件合计507/607，即83.53%；不能把这个子集百分比当成完整PG或全项目已达到85%。严格摘要解码函数为100%。完整覆盖率要求仍未全部达成。

## 关键断言

- 读取/hash和解析分开；摘要只含固定值，问题最多64个而保留完整计数。第65个error仍使摘要invalid，未知问题码返回固定契约错误。XML包装根转为中性wrapper，原文及任意字段不进入摘要。
- 库off/read-only、相同模式、off→on、expected-generation冲突、完全重放及不同body均实测。重放也验证当前会话；撤权、禁用、伪造及交易中到期拒绝，已写入的策略与审计一起回滚。NFO策略变化不修改既有影片cache。
- parent owner/generation/租约、取消、scope及连续cursor均重新核对；旧token不重复进度，旧解析身份可加载/中止但不能继续。原始off phase可跳过NFO；read-only phase不能靠Abort伪装成off绕过完成门槛。
- 命中TTL在最终提交前再次确认。8个parent API的测试实际阻塞交易跨过租约期限，核对等待至少500ms与完整回滚；不以进入交易前就到期代替此断言。
- 正负TTL、旧schema未命中后替换、当前匹配记录严格解码、JSONB实际长度/计费、全局与单库行数/字节四个配额、scope耗尽/回收、128项淘汰及维护总预算均验证。策略幂等记录还验证每actor64/全局4096最后一个名额竞争。
- cache/checkpoint写后取消、deferred COMMIT拒绝、清理DELETE拒绝均验证零成功结果及原数据/计数回滚，重试可继续。迁移up/down/up和活动只读phase拒绝down已实测，未force dirty。

## 查询规模边界

真PG在10,000个非NFO inventory条目后放置64个NFO，生产分页SQL使用部分索引，只访问32个候选。另一组10,000个cache行覆盖两个库；四条全局/单库过期与LRU生产查询均走对应索引，返回128项时访问129项（含executor预取一项），无关闭seqscan等planner提示。Sweep后行数、字节和scope逐项精确对账。

这些是SQL有界页证据，不是1000文件真实NFO worker验收、十万条全域查询证明或端到端性能指标。来源读取仍有文件系统检查点竞态限制，不提供敌对写入下的原子快照。

## 失败记录与修正

Windows首轮因执行沙箱拒绝自建暂存目录的私有DACL而失败；保持产品代码和ACL保护不变，以正常本机token执行同一全量验证通过。品牌增量检查发现XML包装名进入domain摘要，已在adapter转为wrapper，补兼容fixture断言，再执行最终两平台验证；未扩大白名单。

Linux首轮中，专用PG容器退出码1后被原`--rm`设置自动删除，后续fixture无法连接。Docker事件未观察到OOM/kill/stop；服务日志随容器丢失，确切原因未知。恢复仅此自建测试容器，暂存上限512MiB改为2GiB并保留退出日志后，全量重跑通过。容量压力只是可能原因，不能声称已证实。[恢复记录](evidence/nfo-cache-pg-recovery.json)及[原失败日志哈希](evidence/nfo-cache-guards.json)保留失败事实。

完整品牌门禁仍受保留的旧服务端命名阻挡，未降低门槛。G39的来源优先级/字段锁合并、read-write、原子写回、客户端互操作与完整图片处理仍未交付。

## 推送后远端核对

[PR #9](https://github.com/MoYuanCN/Jelee/pull/9) HEAD `676c8edcdb0b15b34455af4c1439eee265587e49` 的[Go CI](https://github.com/MoYuanCN/Jelee/actions/runs/36794292127)已完成：Linux、Windows、PostgreSQL及真实媒体验收通过；仅完整品牌门禁失败。[公开步骤结果](evidence/nfo-cache-ci.json)记录各项结论，不能将整个workflow标为成功。
