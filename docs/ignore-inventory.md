# 持久忽略意图（3D1C1）

本段增加 schema 008 和 repository 合同，为后续过滤扫描保存明确的启用意图。当前 HTTP/CLI 没有启用参数，worker 不领取启用忽略的任务；当前规则来源组件仍未接入生产扫描。完整接线分段见[计划](ignore-inventory-plan.md)。

## 请求与重放

内部 `ScanJobRepository.SubmitScanWithStages` 接收 `ScanIntent.Ignore`：

| mode | caseMode | 结果 |
| --- | --- | --- |
| 空 | 空 | off，兼容既有调用 |
| `jeleeignore` | `sensitive` | 保存大小写敏感意图 |
| `jeleeignore` | `ascii-insensitive` | 保存 ASCII 大小写不敏感意图 |
| 其他组合 | 任意 | `ErrInvalid` |

启用忽略只能与普通盘点或 incremental probe 组合，可同时请求 NFO。与 library/item probe rebuild 组合会被拒绝。Case 行为与宿主文件系统的查找语义不同，不能根据操作系统默认推断。

`jobs.ignore_requested` 是保留标记，启用请求另保存 job/library、mode/case、`jeleeignore-v1` 与 `jeleeignore-proof-v1`。这些版本是服务端声明的预期合同，不代表 C1 已生成或验证来源证明。请求不包含规则原文、绝对路径或文件句柄，不提供文件系统访问权限。

Job、根 frontier、NFO/probe 请求、ignore 请求和审计共用提交交易。相同管理员/key 重放同一任务；mode/case 不同为冲突。重试从原任务复制 ignore 意图及身份，包括从旧的普通任务重试入口调用，不能默默改为 off。重新提交 off 扫描要使用新的请求及 key。沿用现有 live-admin、queue/history、单库活动任务限制。

本版只接受上述 v1 身份；不能据此声称支持未来版本间的重放。未来版本迁移必须明确处理保留请求，不能用新的 DefaultIdentity 改写旧意图。

## 执行边界

当前 claim 同时检查保留 bit 和请求表，任一表示启用都不领取。传入 `ScanCapabilities.Ignore=true` 也不能绕过，因为 C1 尚无受验证的过滤执行器。已过期的任务可按既有规则恢复为 queued，但仍不能被领取执行。

直接调用 inventory Next/Save、NFO Prepare/LoadWork/执行、probe LoadWork/执行、成功 Finish 同样拒绝；不能用手工构造的有效 lease 启动未过滤扫描。取消、失败、租约释放和历史清理继续可用。失败/取消保持 Missing=0、图片比较未完成与原基线不变。

没有 request 只在 bit=false 时表示 off；bit=true 但 request 缺失的重放/重试为冲突，执行入口保持关闭。只有记录而没有 bit 也不能被当成 off。数据库损坏不会触发静默降级。

本段没有忽略命中报告、规则 generation、过滤后的 missing 统计或基线合并。单个候选路径的来源 token 不能代替全库规则快照；旧路径即使已经消失，也必须在后续段重新分类，才能判断是忽略还是缺失。

## 升级与回退

C1 binary 只接受 clean schema 8；当前版本已推进到 schema 9，见[来源清单](ignore-manifest.md)。000001–000007 保持原文；008 把既有任务标记为未请求忽略，保留原库存/图片统计。

升级和回退前均须停止全部旧 worker；不支持在同一数据库混跑 schema 7 与 8 的长期连接。启动版本检查不能撤销一个早已启动的旧进程的数据库权限。

只要保留了启用请求或标记，008 down 就拒绝，包括已结束的任务：007 的重试流程无法保留这些意图。先停 worker，并按既有保留流程处理历史；迁移不会自动删除记录、解除保护或 force dirty 状态。全部 off 的数据库可以正常 up/down/up。迁移拒绝与其他数据库错误要分开诊断。
