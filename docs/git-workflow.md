# Git 与回滚流程

本文对应需求 G01（Git 规范）：远程与上游同步（G01.1）、提交规范（G01.2）、版本与发布标签（G01.3）、忽略规则与编码（G01.4／G01.5，详见[工具链](toolchain.md#被忽略的产物)与[二进制例外](binary-allowlist.md)）、本地钩子（G01.6）和密钥安全（G01.7，详见[密钥泄露处理](secret-leak-response.md)）。

## 远程与上游

| 远程 | 地址 | 用途 |
| --- | --- | --- |
| `origin` | `https://github.com/Carinoasd/Jelee.git` | 本项目仓库，默认分支 `master`；工作分支与 PR 都在这里 |
| `upstream` | `https://github.com/jellyfin/jellyfin.git` | 上游 Jellyfin，只读，用于追踪来源与对照 |

仓库已从 `MoYuanCN/Jelee` 转移到 `Carinoasd/Jelee`；Go 模块路径 `github.com/MoYuanCN/Jelee` 暂不改动（改模块路径会改动全部导入，需单独决定）。

全新克隆只有 `origin`。需要对照上游时由开发者自己添加 `upstream`（脚本与 make 目标不会修改远程配置）：

```sh
git remote add upstream https://github.com/jellyfin/jellyfin.git
git remote set-url --push upstream DISABLED   # 防止误推到上游
git fetch upstream --tags --no-recurse-submodules
```

上游历史完整保留：基线 `52a680c578f1af888ebb74cefcb89b736f9c5738` 可达，标签 `upstream-csharp-final` 标记旧 C# 树移出前的最后一个完整提交，上游 `v10.x` 等标签也在库中（它们不是 Jelee 版本，见下文“发布标签流程”）。

### 与上游同步

Jelee 已用 Go 重写，上游 C# 代码不再合并进工作树；“同步”指跟踪上游的修复与协议变化，再在 Jelee 中重新实现：

1. `git fetch upstream`，在独立分支或 `git worktree add ../jelee-upstream upstream/master` 中审阅差异，不在工作分支上直接合并上游。
2. 需要借鉴的修复按 Jelee 的模块重新实现，提交信息注明来源（例如 `fix(compat): 对齐上游 #12345 的 PlaybackInfo 字段`），许可证义务见[许可证与来源](LICENSE-COMPLIANCE.md)。
3. 已推送的历史不得未经拥有者授权重写；不要硬重置、强制推送、删除他人分支或批量覆盖未提交文件。

## 分支模型

- `master` 是集成分支，只通过 PR 合并。
- 每个工作分段在独立分支上完成（`feat/<主题>`、`fix/<主题>`、`docs/<主题>` 等），一个分支对应一个目标。
- **疊接 PR**：2026-10-01 起，每个完成的分段先推送并建立 PR，下一个分段以上一个已推送的分支为 base 继续（例如本分支 `feat/repohygiene` 基于 `feat/access2`），PR 的 base 也指向前一个分支，使每个 PR 只显示本分段的 diff。前一个 PR 合并后，把后续 PR 的 base 改为 `master`（GitHub 会自动处理已合并的 base），必要时 `git rebase --onto` 仅限尚未被他人基于的本地分支。
- 并行工作使用 `git worktree`，每个 worktree 一个分支；共享的 `.tools` 可以链接到同一目录（见[工具链](toolchain.md)）。
- 合并前 CI 必须全部通过（[贡献指南](contributing.md#质量门禁)）。
- 作者身份统一为 `Carinoasd`，用命令级配置指定：`git -c user.name=Carinoasd -c user.email=<GitHub noreply 地址> commit`。

## 提交规范

提交信息使用 [Conventional Commits](https://www.conventionalcommits.org/zh-hans/v1.0.0/)，类型限定为 G01.2 的九种：

```text
<类型>[(<范围>[,<范围>])][!]: <描述>

[正文]

[脚注]
```

- 类型：`feat|fix|refactor|perf|chore|docs|test|build|ci`。
- 范围：小写模块名（字母、数字、`.`、`_`、`/`、`-`），多个模块用逗号分隔，例如 `feat(access)`、`fix(webhook,stats)`、`docs(traceability)`。
- `!` 或脚注 `BREAKING CHANGE: …` 表示破坏性变更。
- 描述可以使用中文，冒号为半角 `:` 加一个空格，例如 `feat(access): 关键字封锁与限制时段（G48.4）`。
- 主题行与正文之间空一行。
- Git 自己生成的 `Merge …`、`Revert "…"`、`Reapply "…"` 信息直接接受；`fixup!`／`squash!`／`amend!` 只在本地钩子中接受，推送前须 `git rebase -i --autosquash` 压缩。

`tools/commitlint` 检查上述格式：本地由 commit-msg 钩子检查单条信息；CI 的 `commit-hygiene` 作业只检查本次 push 或 PR **新增**的提交（PR 以 base 提交为界，push 以推送前的提交为界，新分支以与默认分支的分叉点为界），2026-10-06 之前的历史不追溯。

**禁止混合目标的巨型提交。** 每个提交只做一件事。`commitlint` 统计每个非合并提交改动的文件数与行数（不计 `api/openapi.json`、`web/src/api/schema.d.ts`、`package-lock.json`、`web/e2e/__screenshots__/`、`docs/evidence/`、`tools/lint-baseline/` 等生成物）；超过 **80 个文件或 8000 行**时，要么拆分为多个提交，要么在信息脚注写明为何是单一目标：

```text
Large-Change: 迁移 000084 与其权限守门、备份、前端页面必须同时落地
```

缺少该脚注时 CI 以警告（GitHub 注解）提示，不阻止合并；`COMMIT_LINT_FLAGS="-size-mode=fail"` 可改为失败，`-max-files`／`-max-lines` 调整门槛。之所以默认只警告：判断“是否混合目标”需要人工审阅，而本项目的单一功能（迁移＋API＋前端＋文档）常常超过机械门槛；警告与脚注让审阅者看到规模与理由。

本地检查任意范围：`make commit-lint COMMIT_RANGE=origin/master..HEAD`。

## 本地钩子

`make hooks`（Windows：`pwsh -File scripts/make.ps1 hooks`）把本克隆的 `core.hooksPath` 设为 `.githooks`；只有开发者主动运行时才设置，撤销用 `git config --unset core.hooksPath`。钩子只使用项目工具链（`.bin/go` 或 Windows 的 `scripts/run-go.ps1`），不需要额外安装工具：

| 钩子 | 检查 |
| --- | --- |
| `pre-commit` | 暂存的 Go 文件 `gofmt -l`；`textcheck -staged`（LF、UTF-8 无 BOM、二进制属性）；`secretscan -staged`；`gitignore-check`；`brand-scan --new`；暂存了 `web/` 文件且已安装前端依赖时运行 `web` 工作区的 `npm run lint`（ESLint、四语、禁播检查） |
| `commit-msg` | `commitlint -message-file`（上文提交规范） |

钩子与 CI 门禁相同，`git commit --no-verify` 只会把失败推迟到 CI。

多个 `git worktree` 共用同一个 `.git/config`，`make hooks` 会对该仓库的全部 worktree 生效；只想在单个 worktree 启用时，先 `git config extensions.worktreeConfig true`，再在该 worktree 中执行 `git config --worktree core.hooksPath .githooks`。在 worktree 中 `.tools` 为链接时 `.bin/go` 会拒绝运行（见[工具链](toolchain.md)），钩子因此要求本克隆自己完成 `make init`。

## 发布标签流程

版本遵循 [Semantic Versioning 2.0.0](https://semver.org/lang/zh-CN/)，标签格式 `vMAJOR.MINOR.PATCH`（可带 `-rc.1` 等预发布后缀）。库中的 `v10.x` 等是上游 Jellyfin 的标签，不是 Jelee 版本；Jelee 的第一个版本号从 `buildinfo.DefaultVersion`（当前 `0.1.0`）起算。发布产物只来自明确的 tag，工作区快照不能当作正式版本。

1. **准备发布提交**（普通 PR）：把 `CHANGELOG.md` 的 `## Unreleased` 条目移到新段落 `## [X.Y.Z] - YYYY-MM-DD`，同时把 `web/package.json` 的 `version` 与 `internal/platform/buildinfo` 的 `DefaultVersion` 改为 `X.Y.Z`（二者由 `TestDefaultVersionMatchesWebClient` 保持一致）。发行前须满足追溯矩阵、品牌门禁、兼容/媒体回归、许可证与性能验收。
2. **本机演练**：`make release-check RELEASE_TAG=vX.Y.Z` 校验标签、CHANGELOG 段落与版本号；`make release-dry-run` 不需要 tag，按当前版本构建全部归档到被忽略的 `.testdata/release-dry-run/`（CHANGELOG 没有该版本段落时用 `Unreleased` 段落作说明）。
3. **打标签**（由拥有者执行，脚本与代理不会推送标签）：在已合并到 `master` 的发布提交上 `git tag -a vX.Y.Z -m "Jelee X.Y.Z"`，然后 `git push origin vX.Y.Z`。
4. **发布工作流** `.github/workflows/release.yml` 由 `v*.*.*` 标签触发：校验 SemVer、CHANGELOG 段落与版本一致（上游 `v10.x` 标签在这一步即失败）→ 运行仓库门禁与单元测试 → 构建前端 → `scripts/release.py build` 交叉编译 `jelee`、`jelee-cli`、`jelee-migrate`（linux/amd64、linux/arm64、windows/amd64，CGO 关闭，`-trimpath`，注入 `buildinfo.version`），每个平台一个归档（附 LICENSE、README、CHANGELOG、许可证说明），另有 `jelee-web-X.Y.Z.tar.gz` → 生成 `SHA256SUMS` 并 `sha256sum -c` 复验 → 以 CHANGELOG 段落生成说明，创建 **草稿** Release（带 `-` 的版本标为预发布）。
5. **人工发布**：拥有者审阅草稿说明与附件后在 GitHub 上发布。撤回未发布的草稿只需删除草稿；已发布的版本不删除标签，改为发布新的修订版本。

归档内的时间戳取自标签提交时间，条目排序与属主固定，同一输入重复构建得到相同的归档字节。

## 回滚层级

1. 关闭对应的 `JELEE_ENABLE_ACCOUNTS`、`JELEE_ENABLE_DIRECT` 与 `JELEE_ENABLE_CATALOG` 后重启，停止已接管的功能；不删除数据。
2. 已提交版本通过审阅后的 revert 或切回已验证发行版本回滚应用。保留数据库备份与迁移兼容性检查。
3. `jelee-migrate down --i-understand` 是破坏性 schema 回退，不能代替应用开关，也不会把数据迁回原 SQLite。仅在可丢弃测试库或已完成备份/恢复评审后使用。

旧 .NET 服务源码已于 2026-10-04 移出工作树，最后一个完整提交以标签 `upstream-csharp-final` 标记（由主线合并时建立），来源审计与比对改用 `git show`／`git worktree add` 从该标签取回，见[许可证与来源](LICENSE-COMPLIANCE.md)。新旧服务从未代理串联，也没有证明可以无损互换新旧数据库。不要把切换二进制描述为自动数据回滚。

## 早期阶段记录

阶段 1/2 的本地顺序为 `a512674643`（审计）→ `721102c8d0`（工具链）→ `c77863e445`（媒体/核心）→ `632005d430`（PostgreSQL）→ `403cc21b27`（API/CLI）→ `0bbd5939bb`（部署）→ `f21d156684`（只读 NFO），保留[草稿 PR #1](https://github.com/MoYuanCN/Jelee/pull/1)。完整哈希与文件范围见[验证报告](verification-report.md#本地提交记录)。这些阶段提交通过命令级 Git 配置指定用户授权身份；不能给阻塞需求附上不相关模块提交以冒充实现。2026-09 的部分提交使用“功能：”“修正：”等中文前缀，早于提交规范门禁，不追溯修改。
