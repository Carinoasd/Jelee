# 3D1C：忽略规则与持久盘点

## 分段与当前边界

3D1A 已交付纯 matcher，3D1B 已交付安全来源观察和编译缓存。来源 token 只覆盖一次候选路径的祖先链，不能作为整个库的规则版本，也不能证明另一次打开的目录枚举属于同一目录句柄。

本段 **3D1C1** 只保存扫描的忽略意图，并在数据库入口阻止尚未支持忽略规则的执行路径。它是后续执行流程的持久合同；HTTP、CLI、worker 尚不能启用过滤。没有过滤结果、规则原文、文件系统授权或成功比较证据入库。

- `ScanIntent.Ignore` 的零值为空 mode/case，表示 off；启用值为 `jeleeignore`，大小写为 `sensitive` 或 `ascii-insensitive`。off 携带 case 或其他值均拒绝。
- 忽略仅可配合普通盘点或 incremental probe；允许同时请求 NFO。不与只重建 probe 的请求组合。
- schema 008 增加 `jobs.ignore_requested`，既有任务为 false；启用任务保存独立 `job_ignore_requests`，绑定 job/library、mode/case 与编译时固定的 program/proof 版本。版本不由 HTTP 输入；一个任务最多一条记录，沿用队列及历史容量限制。
- 入队、根 frontier、NFO/probe 请求和审计在同一交易提交。相同 key 的重放比较原始意图；case/mode 不同为冲突。重试从父任务复制，旧重试入口也不能把 enabled 降为 off。
- enabled bit 或 request 任一存在就不能被当前 worker 领取；即使调用者填写 `ScanCapabilities.Ignore=true` 也不能启用尚未实现的执行器。直接调用 inventory、NFO、probe 的执行入口以及成功 Finish 也必须拒绝，不能只依赖 claim 过滤。
- 取消、失败、释放租约与历史清理仍可用。失败/取消保持 Missing=0、图片比较未完成和基线不变；没有启用过的 off 扫描继续沿用既有行为。
- 本段不设置永久库级 review 标记，因为 enabled 任务尚不能产生观察或发布基线。bit=true 时缺少请求，或 bit/row 不一致，均不得推断为 off；bit=false 且没有请求是本版及升级旧任务的合法 off 表示。
- 008 down 拒绝仍保留任意 enabled 任务，包括 terminal，避免回到 007 后重试丢失意图；不自动取消、删除历史或 force dirty migration。纯 off 的 up/down/up 可用，001–007 原文不变。

## 后续比较合同（C1 尚未实现）

3D1C2 接持久规则观察、目录批次与基线比较；3D1C3 接文件系统、worker、报告和 API/CLI。只有相应验证通过，才让 claim 接受忽略能力。

1. 每个未出现在当前库存的旧基线路径都要分类为可比较的缺失、已排除或未知。只记录当前看见的 excluded children 不够：已经消失的旧文件也可能命中新规则。
2. 本轮来源或覆盖未知、未完成比较不得把旧路径记为 missing，不替换基线。旧基线 scope 不可比但当前所有根完整且稳定时，允许 Missing=0 建立当前 scope 基线；不能把旧 scope 不可比当成永久 review。被排除的旧项保留旧属性，不宣称本轮已读取；成功发布合并当前 included 观察与受保护的旧 excluded 项，合并总量受限。
3. Missing 数和百分比分母使用同一比较范围；图片比较复用同一资格条件。目录前缀以路径组件判断，不能用未转义的 SQL LIKE 处理 `%`、`_` 等合法文件名。
4. 冻结来源包含存在与缺失、根/目录/规则身份、完整 hash、size/mtime、版本、case 和数据库根映射 epoch。重放不能丢弃已完成兄弟目录的冻结规则；规则变化必须中止或重新开始整个 run。
5. 最终来源复核绑定当前租约 generation；恢复不能复用旧 owner 的验证标记。最终交易仍检查根 epoch、取消、租约和规则版本，过期则观察、基线、图片统计和审计一起回滚。
6. 目录枚举与规则观察必须由可核对的 held directory 身份连接。DB 中相等的字节不是文件系统授权；C3 需要真正的受控句柄实现和替换攻击验证。
7. enabled→off 重新判断基线，不沿用陈旧排除记录。历史清理不能删除仍需保护的基线。来源、证明、批次、排除记录与合并后的库存各有固定预算。

## C1 验证

领域非法组合；真实 PG 并发重放、冲突、重试保留、事务回滚、管理员权限、全部 claim 包装器、直接执行绕过、bit/row 损坏、取消/失败/恢复、旧 off 正常比较；008 拒绝丢失意图及旧版升级兼容。运行两平台构建/静态检查/模块测试、原生 Linux PG race 与迁移回归。C1 测试不能替代 C2/C3 的实际过滤、目录句柄、规则变化或规模验收。
