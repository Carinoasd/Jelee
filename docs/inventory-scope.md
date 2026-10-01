# 库存比较范围修正（3D1C2 前置）

## 问题与范围

schema 008 的一般 Missing 查询只按 `(root_id,path)` 比较；图片查询还检查旧基线的属性和根映射 `inventory_generation`。因此，修改数据库中的根路径后再完整扫描新根，旧路径可能被一般计数当成大量遗失，触发 review 并永久阻止新范围建立基线。迁移前没有来源 epoch 的基线也有同样问题。

本修正统一两者的比较资格，不增加迁移、忽略执行开关或公开接口。enabled ignore 的 claim 和直接执行入口继续关闭。完整的忽略三态、受保护旧行合并及来源 manifest 仍见[后续设计](ignore-comparison-design.md)，不算在本修正已交付范围内。

## 比较与发布

- 一般与图片查询分别用 SQL 聚合计算旧基线总数、不可比较行数及相应缺项数。任何旧行的 epoch 未知或与本轮不同，本轮一般 Missing 为 0，图片 ComparisonComplete 为 false；不能以当前 job 的 epoch 补造旧来源。
- 同范围、属性已知的完整观察维持既有缺失计数和 review 门槛。相同路径的 image→video 只增加图片 Missing，不增加一般 Missing。新文件不增加缺失百分比的旧基线分母。
- 所有当前数据库根必须有完成的根 frontier，任务不能混入其他库的根，也不能有未完成目录。缺少必要根记录时拒绝成功 Finish。目录自身记录的 skipped 与任务 skipped 任一非零，都要求 review、Missing=0、旧基线不变。
- 本轮来源或覆盖不完整时保留旧基线。旧基线 epoch 不可比，但本轮所有当前根已完整观察、没有 skipped、job frozen epoch 仍等于数据库当前 epoch 时，允许以本轮观察建立新范围基线；该轮不宣称旧路径被删除。后续正常扫描能使用这个新基线。
- 根映射 epoch 是整库范围：任一根新增、移除或路径变更都会改变整库 scope。当前修正没有逐根 revision；不能把它描述为仅重设某一个根。
- 没有 frozen epoch 的旧 job 不在 Finish 时重新绑定；不发布基线。取消、失败、超出缺失门槛、租约丢失及最后交易内的过期均沿用原子回滚与保留策略。

基线只是比较元数据。本修正不会删除或修改媒体文件，也不会删除媒体目录项目。旧图片任务摘要在发布新基线前保存，之后不按新基线重新计算。

## 验证要求

真实 PostgreSQL 回归需证明根映射改变、迁移前未知属性、完整空目录、多根缺项、混入其他根、目录 skipped 与汇总不一致、同范围遗失和类型改变、最后提交过期。保留现有 10,000 基线／9,600 当前行的 SQL 聚合执行计划检查，及 NFO、probe、ignore 守卫回归。最终结果另记验证报告；本文件不是通过声明。

## 本段实测结果

基于 `6fdfaaa2c69cc8bbb0c21f8180ddc36cad5825ef`，新增8项scope回归先在旧代码重现失败，再在修正后全部通过。完整 PostgreSQL race 140项、零skip；现有10,000基线／9,600当前项的执行计划检查也覆盖新的通用缺项SQL。Windows build/lint/全模块测试通过（25有测试package、513顶层通过；205个PG和14个工具／平台skip，未跑Windows race）；Linux vet与三个命令build通过。1000与100项真实NFO／图片／视频混合库、取消恢复与SIGTERM验证通过，原素材保护和自建资源清理通过。具体来源SHA和命令证据见[本段证据](evidence/inventory-scope.json)。

完整忽略分类与manifest仍未交付，需求矩阵状态不提前提升。本段没有新增迁移。远端基线PR #13的一轮CI另出现process后代退出测试失败；状态复读问题已单独修正，见[验证记录](process-exit-verification.md)，不能宣称全CI已通过。