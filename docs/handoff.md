# 接手记录

## 当前工作与授权

2026-10-01 用户明确「继续吧」，此前切模型暂停已结束。当前分支 `feat/jelee-nfo-cache`，从3C3A的 `082a51dc2b1206e9687a70c2292d33991e068d30` 继续3C3B。用户授权每小段完成后验证、提交、推送、建立普通PR并附到聊天，然后继续下一段；没有授权合并、发布、tag或改写历史。

作者仅用命令级 `Carinoasd <46304809+Carinoasd@users.noreply.github.com>`，不设置全局身份。原需求逐字保存在 `requirements-source.md`，SHA256 `755b6b32324efe710c3e1135a0c982c45b82f337e90fcb50ab3718a20cba5d07`。

## 已交付与本段

- 基础服务和账户：[PR #1](https://github.com/MoYuanCN/Jelee/pull/1)（草稿）。
- 3A持久只读盘点：[PR #2](https://github.com/MoYuanCN/Jelee/pull/2)。
- 3B1工具、3B2程序/素材：[PR #3](https://github.com/MoYuanCN/Jelee/pull/3)、[PR #4](https://github.com/MoYuanCN/Jelee/pull/4)。
- 3C1隔离探测：[PR #5](https://github.com/MoYuanCN/Jelee/pull/5)，runtime冷下载修复后功能CI通过。
- 3C2A持久cache契约：[PR #6](https://github.com/MoYuanCN/Jelee/pull/6)，源代码 `3d8842be1e`。该PR的PG与Windows CI通过；Linux后续暴露新增identity摘要函数缺少测试，当前3C2B补测后原生sandbox门槛86.8%/零skip通过。完整品牌门禁仍失败，不降低门槛。
- 3C2B已交付[PR #7](https://github.com/MoYuanCN/Jelee/pull/7)：schema5持久probe请求、worker、重建、能力降级、API/CLI与维护。源码 `b59be8d389216c3853a0a6d18b1f1af130745a9f`；CI暴露跨parent的测试误判，`ef115c27a0`保留own-parent join并修正测试。HEAD `893432fc51` 的[CI](https://github.com/MoYuanCN/Jelee/actions/runs/36789351642) Linux/Windows/PG真实媒体验收全部通过，仅完整品牌仍失败。完整本地证据见[worker验证](probe-worker-verification.md)。
- 3C3A已交付[PR #8](https://github.com/MoYuanCN/Jelee/pull/8)，源码 `feeee03da801bb4730eca86b39d55eb5b1521783`、HEAD `082a51dc2b`：NFO完整内容指纹、来源身份核对、私有原文与独立解析，现有CLI已接入。Windows build/lint/test/真CLI与原生Linux race/真CLI/基准通过，NFO90.5%、Linux66个顶层测试零skip；Windows1个symlink权限skip，未跑Windows race。[远端Go CI](https://github.com/MoYuanCN/Jelee/actions/runs/36790288784) Linux、Windows、PG与真实媒体验收通过，完整品牌仍失败。见[NFO来源验证](nfo-source-verification.md)。矩阵仍4已完成/168部分/164阻塞。
- 3C3B已验证，源码 `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`：独立NFO库级模式/generation、安全验证摘要、schema006有界快取与parent租约/检查点。Windows全模块27包通过；原生Linux三build/vet/race、27包474顶层测试通过，PG零skip，另6个专用媒体/沙箱skip明确列出。NFO90.4%、PG80.6%、新NFO PG83.53%。见[NFO快取验证](nfo-cache-verification.md)。本段发布分支为 `feat/jelee-nfo-cache`，后续按[3C3C计划](nfo-worker-plan.md)接worker/API与图片统计。矩阵现4已完成/170部分/162阻塞。

## 不变量

1. 新binary只接受clean schema6。迁移000001–000005原文不变；006 down拒绝仍有queued/running parent的read-only NFO phase，005 down另拒绝活动probe请求，须先结束/取消并停worker。失败迁移可能dirty，不能自动force。
2. root path只来自本地CLI登记的数据库媒体根；HTTP只接收登记ID。用户原媒体/NFO/图片不写入；不启用ffmpeg生产回退或转码。
3. 默认probe关闭，disabled节点不领probe任务；缺工具仅停用相关能力。健康状态在启动时验证，readiness不每次执行工具。
4. 持久request保存可信identity和enqueue generation；重放不失效、不repin，disabled/runtime故障仍可重放保留请求。运行恢复先读phase，从连续检查点继续。
5. 最大16项hit批次；miss每parent一项。先本地gate后DB lease。正/负结果保存前finalInspect；父取消、丢租约或工具故障不被记为坏媒体。心跳≤min(parent,fileTTL)/3，DB timeout更短。
6. 每次维护最多128项/2秒，每60秒一次。关机先cancel/join worker与维护，再清自己的scratch和DB。能力与公开summary不泄漏绝对路径、raw JSON、stderr、工具身份或凭证。
7. Windows正式probe仍停用；Linux测试缺必需工具必须失败。Windowsrace尚未执行。各阶段证据不能替代完整336项、codec、规模或24小时验收。

## 本地环境与下一段

Go1.27.1与媒体工具都在项目 `.tools`；Windows通过 `scripts/run-go.ps1`/`scripts/make.ps1`，Linux通过 `.bin/go`。不安装全局工具。独立PostgreSQL只用 `jelee_test` 的自建schema；凭证只读 `.testdata/database-url`，不得回显或提交。

完成本段PR后按[3C3C接线计划](nfo-worker-plan.md)继续worker/API、图片属性统计及真实增量验收；本段无NFO运行时接线。NFO policy generation须独立于视频probe generation；现有inventory baseline仅保存路径，图片属性差异需补持久观察。来源读取的SHA描述保留字节，不能保证敌对原地写入下的原子快照。完整NFO优先级/锁合并、写回、ignore、监看、排程、图片和规模验收尚待后续交付。

本段Windows首次沙箱ACL失败经正常host token重跑通过。Linux首次专用PG退出后自动移除，确切原因未知；已恢复同名专用PG、2GiB tmpfs且保留退出日志，最终全量通过。凭证仍仅在忽略文件中。
