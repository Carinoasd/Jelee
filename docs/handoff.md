# 接手记录

## 当前工作与授权

2026-10-01 用户明确「继续吧」，此前切模型暂停已结束。当前分支 `feat/jelee-probe-worker`，从 `feat/jelee-probe-cache` 的 `9f5e98aab6055e774866cc272e8017ff55b6ef93` 继续第3C2B段。用户授权每小段完成后验证、提交、推送、建立普通PR并附到聊天，然后继续下一段；没有授权合并、发布、tag或改写历史。

作者仅用命令级 `Carinoasd <46304809+Carinoasd@users.noreply.github.com>`，不设置全局身份。原需求逐字保存在 `requirements-source.md`，SHA256 `755b6b32324efe710c3e1135a0c982c45b82f337e90fcb50ab3718a20cba5d07`。

## 已交付与本段

- 基础服务和账户：[PR #1](https://github.com/MoYuanCN/Jelee/pull/1)（草稿）。
- 3A持久只读盘点：[PR #2](https://github.com/MoYuanCN/Jelee/pull/2)。
- 3B1工具、3B2程序/素材：[PR #3](https://github.com/MoYuanCN/Jelee/pull/3)、[PR #4](https://github.com/MoYuanCN/Jelee/pull/4)。
- 3C1隔离探测：[PR #5](https://github.com/MoYuanCN/Jelee/pull/5)，runtime冷下载修复后功能CI通过。
- 3C2A持久cache契约：[PR #6](https://github.com/MoYuanCN/Jelee/pull/6)，源代码 `3d8842be1e`。该PR的PG与Windows CI通过；Linux后续暴露新增identity摘要函数缺少测试，当前3C2B补测后原生sandbox门槛86.8%/零skip通过。完整品牌门禁仍失败，不降低门槛。
- 当前3C2B：schema5持久probe请求、worker、幂等重建、能力降级、API/CLI、清理维护。源码 `b59be8d389216c3853a0a6d18b1f1af130745a9f`，见[接线契约](probe-worker.md)与[验证报告](probe-worker-verification.md)。Windows完整测试、Linux完整race/真PG、原生sandbox86.8%/零skip、1000→0→17实际子程序、真HTTP与SIGTERM零租约均已通过。矩阵4已完成/168部分/164阻塞，完整第3阶段仍未完成。

## 不变量

1. 新binary只接受clean schema5。迁移000001–000004原文不变；005 down拒绝仍有queued/running probe请求，须先结束/取消并停worker。失败迁移可能dirty，不能自动force。
2. root path只来自本地CLI登记的数据库媒体根；HTTP只接收登记ID。用户原媒体/NFO/图片不写入；不启用ffmpeg生产回退或转码。
3. 默认probe关闭，disabled节点不领probe任务；缺工具仅停用相关能力。健康状态在启动时验证，readiness不每次执行工具。
4. 持久request保存可信identity和enqueue generation；重放不失效、不repin，disabled/runtime故障仍可重放保留请求。运行恢复先读phase，从连续检查点继续。
5. 最大16项hit批次；miss每parent一项。先本地gate后DB lease。正/负结果保存前finalInspect；父取消、丢租约或工具故障不被记为坏媒体。心跳≤min(parent,fileTTL)/3，DB timeout更短。
6. 每次维护最多128项/2秒，每60秒一次。关机先cancel/join worker与维护，再清自己的scratch和DB。能力与公开summary不泄漏绝对路径、raw JSON、stderr、工具身份或凭证。
7. Windows正式probe仍停用；Linux测试缺必需工具必须失败。Windowsrace尚未执行。各阶段证据不能替代完整336项、codec、规模或24小时验收。

## 本地环境与下一段

Go1.27.1与媒体工具都在项目 `.tools`；Windows通过 `scripts/run-go.ps1`/`scripts/make.ps1`，Linux通过 `.bin/go`。不安装全局工具。独立PostgreSQL只用 `jelee_test` 的自建schema；凭证只读 `.testdata/database-url`，不得回显或提交。

3C2B之后先调研3C3只读NFO增量整合的最小交付，继续补完3C。后续仍需ignore规则/增量监看与去抖、排程、图片任务、完整catalog导入与更大规模验证。维持各段可回滚及独立PR。完整阶段3和原文多数功能尚未完成，矩阵仍需按实际子项证据更新。
