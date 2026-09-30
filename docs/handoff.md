# 切换模型接手记录

## 当前状态

2026-10-01 用户已恢复工作，要求“每做完一阶段就提PR，然后继续下一阶段”。第3A [PR #2](https://github.com/MoYuanCN/Jelee/pull/2)、3B1 [PR #3](https://github.com/MoYuanCN/Jelee/pull/3)、3B2 [PR #4](https://github.com/MoYuanCN/Jelee/pull/4)已推送。第3C1源码已提交 `27f66b1eb36c4d6aed0471b58371bbd6c83aaac0`，当前分支 `feat/jelee-isolated-probe` 基于3B2；验证见[隔离探测报告](probe-verification.md)。接着3C2A持久cache契约，再3C2B扫描/API整合，采用[已选方案](probe-cache-plan.md)。

- 第1/2阶段历史分支 `feat/jelee-go-foundation`，目标 `master`；[草稿 PR #1](https://github.com/MoYuanCN/Jelee/pull/1)。后续阶段采用相邻阶段分支作为 PR base，当前分支见页首。
- 第 2 阶段最终提交 `fdbd1173b53e2999ab6cb99ec1d525fef91ff0b6` 已推送；本记录与第 3 阶段接口草案另作接手提交。
- 作者始终使用命令级 `Carinoasd <46304809+Carinoasd@users.noreply.github.com>`；没有修改全局 Git 署名。禁止重写已推送历史。
- 需求原文、336 项矩阵、阶段范围和真实证据分别位于 `requirements-source.md`、`requirements-traceability.md`、`accounts-api.md`、`accounts-verification.md`。
- 第 2 阶段[远端 CI](https://github.com/MoYuanCN/Jelee/actions/runs/36747240343)：Windows、Linux 与 PostgreSQL 全部通过；完整品牌门禁仍因旧树残留而失败，不能降低门禁。

## 已完成到哪里

第 1 阶段基础服务和只读 NFO；第 2 阶段密码账户/会话/库 ACL、schema 2、HTTP/CLI、四语与测试均已提交。最终本地镜像 `jelee/jelee:codex-accounts-test` 为 `sha256:197a2e8cc68564a77d500ae57e5deaad507e4ab4a852a472fbb6e2509799cd0a`，容器验证通过。完整功能替代、媒体扫描/探测、前端和多数原需求尚未完成。

第3A段已交付：schema3、持久盘点队列、真实只读scanner、取消/恢复/fencing、HTTP/CLI/config和协调停止。Windows全套、Linux全套race+真PG、原生Linuxscanner、真服务1000files及生产只读容器实测均通过；Windowsrace因缺兼容C编译器受阻。详细证据见[第3A验证](jobs-verification.md)，后续3B/3C/3D规划见[第3阶段计划](jobs-stage3-plan.md)。

## 恢复后的起点

1. 确认阶段PR/当前分支后继续3C2，沿用每段验证/推送/提PR/继续的授权，不需要再次询问。
2. schema3 已发布；后续只增加迁移，不改写000001/000002/000003。
3. 已实现契约：Heartbeat bool=cancelRequested，Claim/Next无工作ErrNotFound；kind video/nfo/image/other；目录含根`.`。任何skipped/大量缺失均保留基准待review，尚无确认API，完全不自动删除。
4. root path 仅来自数据库中经本地 CLI 注册的媒体根，不允许 HTTP 任意传路径。批次最多 128 项，路径最多 1024 UTF-8 字节，队列/历史/目录/文件记录与并发都有硬上限。所有公共任务操作在事务内重验管理员和有效会话。
5. ffprobe/ffmpeg 已固定在 manifest，并安装至两平台项目 `.tools`；见[3B1验证](media-tools-verification.md)。3C1 已注册 Linux amd64 固定只读隔离 metadata operation，`doctor probe` 和正式实验镜像已验证；扫描尚未调用此 operation，Windows 停用。不得使用 ffmpeg 生产回退。

## 本地运行注意

Go 1.27.1 位于项目 `.tools/`，Windows 使用 `scripts/run-go.ps1`/`scripts/make.ps1`，Linux 使用 `.bin/go`。所有工具、下载、缓存与测试素材保持项目内；不安装全局工具。

本轮专用 PostgreSQL 容器停止后需要重新建立隔离测试库，不能把测试迁移指向用户数据库。生成的脚本和临时测试日志保留在被忽略的 `.testdata/`，可作本地复现实验参考；公开验证证据已脱敏入库。数据库凭据和会话令牌不能提交或回显。
