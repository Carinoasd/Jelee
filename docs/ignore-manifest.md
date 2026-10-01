# 忽略来源证明持久化（schema 9）

## 范围与入口

`RecordIgnoreProofs` 在当前 enabled ignore 任务的有效租约下，原子保存最多128项父目录在前的证明。根和路径来自已登记的任务范围；证明本身没有授予文件系统访问权限。现有claim、普通库存、NFO/probe和成功Finish的ignore执行守卫继续关闭。

领域值 `IgnoreDirectoryProof` 包含根ID、规范相对目录、父身份、目录身份、明确的目录缺失标记，以及固定规则文件的present/identity/size/mtime/完整SHA256。目录缺失只允许用于已打开父目录下的直接child，不能为根、不能带规则，也不能再作为后代的父证明。来源组件目前只产生已打开目录记录；真实缺失目录观察仍需后续原生接口。

## 一致性与预算

- 每个job/root/directory只有一项证明。相同重送不重复记账；不同内容不会覆盖旧值，而是提交整份manifest失效标记并返回`ErrInventoryInvalidated`。同一批此前新增的前缀会回滚，原记录保持不变。失效记录不能靠重送旧内容恢复。
- 单任务最多16,384项，同时受job MaxDirectories约束；来源原文大小合计及确定性元数据记账各最多64MiB。单规则最多256KiB。数据库不保存规则原文；元数据charge不声称等于磁盘用量。
- 每项验证根属于当前库，直接父项存在、不是缺失目录且identity精确相同。禁止只按路径字符串认为父目录相同。
- job冻结根映射epoch必须非空且等于库当前epoch。库行锁、取消、owner/generation和数据库租约时间在交易中检查，最终提交前再次检查；超时不保留部分行或额度。
- SQL约束检查字段长度和present/absence形状；UPDATE触发器拒绝覆盖已记录证明。history删除父job时级联删除这些工作记录，基线不依赖这些历史行。

`FreezeIgnoreManifest` 必须看到每个已配置根的有效根证明，然后冻结新增项。冻结可重放，之后相同证明仍可重送，但不能新增；冲突仍使整份记录失效。它只冻结来源清单，不表示目录已枚举完、来源已在当前租约下重新复核或整个扫描可提交。

`ReadIgnoreProofPage` 仅读取冻结、未失效的manifest，每页最多128项，按(root UUID, directory COLLATE C)游标读取。空页是EOF，最后一项是下一页游标。重新领取任务后仍能读取相同清单，旧租约不能读写。后续verification必须重新绑定新租约，不能把frozen当作seal。

## 迁移与后续

本版binary要求clean schema9。000001–000008不改写。009添加manifest header与proof表；空manifest可9→8，任何保留的manifest（含失效或终态任务）都阻止回退，避免静默丢失证据。先停worker再迁移，不混跑schema8/9服务，不自动force dirty migration。

未交付：基线included_missing/excluded/unknown分类与合并、实际ReadDir同句柄连接、完整来源复核/租约seal、HTTP/CLI启用和过滤报告。当前记录不能单独证明媒体缺失或允许删除媒体。

## 验证状态

最终PostgreSQL race 150顶层通过、零skip；包含10项manifest回归及10,001行生产分页SQL执行计划，128项页面只访问128行且不排序剩余后缀。Windows全模块25包/521顶层通过，223个PG和14个工具或平台skip，未跑Windows race；Windows build/vet、最终Linux vet/三build通过。

schema9的1000/100真实NFO、图片、视频混合验收、取消恢复、SIGTERM、原素材保护与资源清理通过。该验收在最后manifest分页SQL索引优化前执行；库存/媒体执行代码完全相同，它没有验证enabled ignore扫描。最终来源SHA、命令与详细边界见[证据](evidence/ignore-manifest.json)。
