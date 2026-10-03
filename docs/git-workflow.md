# Git 与回滚流程

## 本次工作状态

- origin：`https://github.com/MoYuanCN/Jelee.git`。
- upstream：`https://github.com/jellyfin/jellyfin.git`，用于追踪来源。
- 基线：`52a680c578f1af888ebb74cefcb89b736f9c5738`，保留 30090 个可达提交。
- 阶段1/2工作分支：`feat/jelee-go-foundation`；第3A分支：`feat/jelee-jobs-scan`，以后者对前者建立独立PR。
- 用户授权作者 `Carinoasd <46304809+Carinoasd@users.noreply.github.com>`，命令级指定；阶段1/2保留[草稿 PR #1](https://github.com/MoYuanCN/Jelee/pull/1)。2026-10-01用户要求每个完成分段都推送并建立PR，再继续；后续PR以已推送前阶段分支为base，保持逐段diff。未创建发布标签或重写已推送历史。

已核对的本地顺序为 `a512674643`（审计）→ `721102c8d0`（工具链）→ `c77863e445`（媒体/核心）→ `632005d430`（PostgreSQL）→ `403cc21b27`（API/CLI）→ `0bbd5939bb`（部署）→ `f21d156684`（只读 NFO）。媒体/核心使用修订后的 `c77863e445`。完整哈希与文件范围见[验证报告](verification-report.md#本地提交记录)。

上述阶段提交通过命令级 Git 配置指定用户授权身份；README 与最终矩阵/证据文档另行提交。不能给阻塞需求附上不相关模块提交以冒充实现。独立快照复验确认 `0bbd5939bb` 的已提交源码可构建/测试；最终 Windows/Linux 日志及当前容器实测另对应 `f21d156684`。这些证据不替代全部中间提交目标的逐一复验。

## 后续提交

使用用户授权的身份，按单一目标分别提交审计、工具链、Go 基础、数据迁移、直投、权限及文档。使用 `feat|fix|refactor|perf|chore|docs|test|build|ci` 和模块 scope，例如 `feat(media): add bounded original-file delivery`。提交前运行相应格式化、测试和数据库回归，记录真实输出。

上游同步先 `git fetch upstream`，在独立分支审阅差异，再按团队选定的 merge/rebase 策略整合；已推送历史不得未经授权重写。不要硬重置、删除用户分支、强制推送或批量覆盖未提交文件。

## 回滚层级

1. 关闭对应的 `JELEE_ENABLE_ACCOUNTS`、`JELEE_ENABLE_DIRECT` 与 `JELEE_ENABLE_CATALOG` 后重启，停止已接管的功能；不删除数据。
2. 已提交版本通过审阅后的 revert 或切回已验证发行版本回滚应用。保留数据库备份与迁移兼容性检查。
3. `jelee-migrate down --i-understand` 是破坏性 schema 回退，不能代替应用开关，也不会把数据迁回原 SQLite。仅在可丢弃测试库或已完成备份/恢复评审后使用。

旧 .NET 服务源码已于 2026-10-04 移出工作树，最后一个完整提交以标签 `upstream-csharp-final` 标记（由主线合并时建立），来源审计与比对改用 `git show`／`git worktree add` 从该标签取回，见[许可证与来源](LICENSE-COMPLIANCE.md)。新旧服务从未代理串联，也没有证明可以无损互换新旧数据库。不要把切换二进制描述为自动数据回滚。

## 发布

当前版本为 Unreleased。未来使用 SemVer 标签，发布产物来自明确 tag；禁止把工作区快照当成正式版本。发行前须满足追溯矩阵、完整品牌门禁、兼容/媒体回归、许可证与性能验收。目前持续推送工作分支，尚未创建 release。
