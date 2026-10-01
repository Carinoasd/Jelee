# 忽略规则的持久比较：C2/C3 设计

**状态：未实现计划。** 当前 C1 仅保留意图并拒绝 enabled 执行，见[持久盘点计划](ignore-inventory-plan.md)。本轮先处理 schema 8 下 general/image 的 epoch 资格不一致；本文的 manifest、三态分类和原生文件系统接线留待后续小段。新 schema 编号、API 名称和新增预算均未定案，不修改已发布迁移。

## 1. 先区分旧 scope 与本轮观察

scope 指配置根集合及其映射 epoch；原生根身份另由本轮来源证明绑定。不能只检查 job epoch 等于当前 library epoch，就对任意旧基线计 missing。

| 条件 | 本轮结果与基线处理 |
| --- | --- |
| 同 scope，完整观察且无 unknown | 精确比较，沿用现有 missing 门槛；未触发 review 才发布合并基线 |
| 旧基线 epoch 不同或无法确认旧 scope，但本轮具备全库完整观察 | `Missing=0`，建立整个当前 scope 的新基线；不沿用旧 scope 的 missing、排除记录或属性比较结果 |
| 本轮来源、目录覆盖或最终复核未知/失败 | `Missing=0`、比较未完成，整个基线保持不变；不能借 epoch reset 绕过错误 |

第二种是 **全库 authoritative epoch reset**：旧 scope 不可比不等于当前读取失败，也不产生永久 review 标记。当前所有根完整后可以建立可比较的新 scope。重置轮的旧图片属性不参与 added/changed/unchanged 推断，当前图片计入 uncompared；下一轮再正常比较。

发布前由 SQL 锁定 library，并证明：配置根集合非空；本轮根 frontier 与配置根集合双向完全相等；每个根 frontier 已完成；没有未完成目录、skipped、取消或失败；job frozen epoch 仍等于当前 epoch。仅凭一个 `complete=true` 参数、没有 pending 行或计数相等都不够。未来过滤流程还必须通过完整 manifest 的当前租约 seal。交易末尾再次检查 epoch、取消及租约期限。

`attributes_known=false` 与 path provenance unknown 分开处理：前者不能造出旧图片属性；后者不能参与 missing。旧行缺 epoch 或 epoch 不符时，不得由 SQL left join absence 推导 missing。off 扫描也走这套资格判断；enabled→off 重新比较，不继承过时排除决定。同 scope 的 protected 旧项仍按下一节保留。

## 2. 最小持久模型与分类

建议增加 library 级 baseline revision/scope epoch，以及 job 级 comparison state：冻结 baseline revision、root epoch、manifest revision、分页位置、phase、计数和 seal generation。基线另记 observed revision，区分本轮读取与保留的旧属性。它们不外键依赖可被 history trim 删除的 job。

同 scope 下，每个未见旧基线项保存一条 `(job,root,path)` 分类：

- `included_missing`：当前范围可比较、规则允许，并有完整枚举或明确缺失祖先证据。
- `excluded`：引用本轮有效规则来源、行号与匹配路径；只保留旧属性，不宣称本轮读过该文件。
- `unknown`：当前范围或证据不足；保存固定 reason，不保存 OS 错误。

设 P 为旧基线中已见于 current included 的数量，M 为 included_missing 数。missing 分母为 **P+M**；excluded 和新增项都不进分母。有任何当前 unknown 或不完整观察时不发布。完整且未触发既有 missing 门槛时：

```text
新基线 = current included UNION ALL 同 scope 下 classified excluded 的旧 rows
```

两部分必须按精确 root/path 不重叠，合并总量再次受限。excluded 的旧属性、epoch、observed revision 原样保留。图片 missing 使用相同资格；旧 image 在同路径变成 included video 时计图片 missing，不计一般文件 missing。case mode 只影响规则，库存 key 不折叠大小写；目录前缀按组件比较，不拼接未经转义的 SQL LIKE。

**分页先限原始候选，再做 anti-join。** 按 `(root_id,path COLLATE "C")` keyset 读取最多128条原始 baseline，然后筛出未见项。返回原始页尾 cursor；即使该页 unseen 为空，空分类提交也必须推进 cursor。只有原始页为空才是 EOF。这样不会为凑满128条 unseen 而无界扫描已命中的基线；精确前缀重放不重复计数。

## 3. Manifest 的 typed 证明与 C3 连接点

建议以 `(job,root,directory)` 为主键；根 directory 为 `.`。每行只包含：

```text
RootID, Directory, ParentIdentity[32], DirectoryState, DirectoryIdentity[32]
RuleState, RuleIdentity[32], RuleSize, RuleModifiedNano, RuleSHA256[32]
```

- 根只能 `opened`；非根必须连接已 opened 的直接父 proof，父 identity 精确匹配。
- opened 目录的固定 leaf `.jeleeignore` 可以 `present` 或 `absent`。present 保存完整摘要；absent 只代表在该已开父句柄下确认 leaf 不存在，规则字段全零。
- 目录 `absent` 只代表在已开父句柄下确认直接 child 不存在；目录 identity 为零，RuleState=`not_applicable`，不得再插入后代 proof。不可读、unsafe、timeout 不得转成 absent。
- 既有 key 只能完全一致重放；根、父链或来源变化中止本轮，不能局部刷新已完成兄弟目录的上下文。

C3 的每批枚举须附 `rootID + directory + heldDirectoryIdentity`，与该 manifest 链匹配。identity 必须来自真正执行 ReadDir 的同一个 held handle。缺失祖先亦须原生 opener 给出明确 absence；B 现有单链 token、一般 ErrRead 或数据库中没有记录都不能替代证明。

## 4. Repository 流程与有界复核

建议接口按职责分开，名称待实现前核定：

```go
SaveIgnoreScanBatch(ctx, lease, directory, batch)
BeginBaselineComparison(ctx, lease)
NextUnseenBaselinePage(ctx, lease, limit)
CommitBaselineClassifications(ctx, lease, token, batch)
BeginIgnoreVerification(ctx, lease)
ReadIgnoreManifestPage(ctx, lease, token, limit)
VerifyIgnoreManifestPage(ctx, lease, token, observedRows)
SealIgnoreComparison(ctx, lease, token)
```

批次写入原子保存 kept/excluded children、必要 proof 和枚举身份。分类开始后冻结 inventory；分类可补充旧路径所需 proof。进入 verifying 后冻结 manifest。每次 Read/Verify 都使用 PG 保存的精确 next-prefix；桥接层重新观察后回送 typed rows，PG 逐字段比对，再推进 cursor、计数和32-byte累积 hash。采用固定 framing 的逐列 hash chain，不保存 Go 私有 hash 状态，不一次加载完整 manifest，也不接受调用者单独宣称的总 digest。

Seal 只在 EOF、列数一致、同一 live lease generation 下成立。reclaim 后从头复核；重复 Begin 或 heartbeat 不延长旧证据期限。最终基线、图片统计、terminal job 与 audit 同交易发布，末尾 guard 失败则全部回滚。普通 Save 对 enabled 仍拒绝；新路径完成验证前也不能执行，C3 验证前不开放 enabled claim。

以下是**待核定的建议预算**，并非已实现配置：页/批各128条及1MiB编码数据；manifest 16,384列、64MiB记账，唯一 present 规则原始大小总和64MiB；完整复核120秒、seal有效30秒，继承更短父期限并使用 DB clock。沿用 A/B 的路径、深度、每链来源和原文限制。旧基线、分类及合并库存仍须受现有 policy 和500,000硬上限约束；记账值不声称等于磁盘或进程实际用量。

## 5. 分段验收

C2 真 PG 验证字段 shape、父链、absence 冲突、限额、空 unseen 页推进、乱序/重放、取消、到期、reclaim、epoch reset 的全根覆盖，以及原子合并和 history trim。覆盖旧 scope→新 scope、旧属性未知、同 scope protected、off 切换、type/case 变化、`%`/`_` 目录和被删祖先。

C2 不能证明保存的 identity/hash/absence 来自真实文件系统。C3 才验证严格开档、同句柄枚举、原文只读、目录/根替换、真实缺失祖先和最终复核。两阶段都不提供文件系统原子 snapshot 保证，不用 B 每候选重复读取的成本推算整库吞吐；规模数据须接线后实测。
