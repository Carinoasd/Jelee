# Jelee 需求追溯矩阵

## 阅读与状态规则

原始授权文本逐字节保存在 [requirements-source.md](requirements-source.md)，SHA256 为 `755b6b32324efe710c3e1135a0c982c45b82f337e90fcb50ab3718a20cba5d07`。审计基线为 `52a680c578f1af888ebb74cefcb89b736f9c5738`。本文列出原文全部 336 个有编号子项与缺失的 G17/G43/G44。

每行“约束/工作”保留原文，作为必须逐条满足的执行清单；“落盘目标”和“测试计划”均为**计划映射**，不意味着文件已存在或测试已经编写/执行。同一子项中的多个动作必须全部通过才可标记已完成。章节验收在各表前列出，子项测试另有唯一计划 ID。

- **已完成**：必须链接存在的实现、实际执行报告与提交号。
- **部分完成**：有可核对交付，但原子项仍有未完成约束或未通过验收。
- **阻塞**：当前项实现/依赖/验证证据缺失；不表示一定需要用户介入。
- 本轮已建立分阶段本地提交，见[提交记录](verification-report.md#本地提交记录)。用户确认作者 Carinoasd，使用命令级 Git 身份。提交栏只归属已实现子集；阻塞项没有实现提交，最终文档另行提交。真实测试只能回填测试结果，跳过与未执行不能转成通过。
- 原始 G01.4 的八组忽略规则及硬性规则适用于 G01.4 全行，详见原文。G30.6/G30.5 保留原文顺序。
- 需求冲突、缺失编号和工程解释见 [requirements-clarifications.md](requirements-clarifications.md)。
- 所有未完成性能项均缺少同机同数据集基线，不能推算达标；第“性能与全局约束”节提供额外验收清单。

## 实际交付证据

第3D1C1见[持久意图与执行守卫验证](ignore-inventory-verification.md)，源码 `b07ad3a6f20508c43b17458cd6847451ff6d68ab`；本段未启用实际过滤，G22状态不变。第3D1B见[安全来源与缓存验证](ignore-source-verification.md)，第3D1A见[忽略匹配验证](ignore-matcher-verification.md)，第3C3C见[NFO工作流程验证](nfo-worker-verification.md)，第3C3B见[NFO快取验证](nfo-cache-verification.md)，第3C3A见[NFO来源验证](nfo-source-verification.md)，第3C2B见[worker验证](probe-worker-verification.md)，第3C2A见[快取契约验证](probe-cache-verification.md)，第3C1见[隔离探测验证](probe-verification.md)；第3A、3B1、3B2分别见[任务验证](jobs-verification.md)、[工具验证](media-tools-verification.md)、[程序/素材验证](process-verification.md)，当前编号项统计为 **4 项已完成、184 项部分完成、148 项阻塞**。第1/2阶段保留在[草稿 PR #1](https://github.com/MoYuanCN/Jelee/pull/1)，后续按用户授权每个已验证分段建立独立 PR。下面首阶段证据保留原验证范围。

| 证据 | 结果 | 范围 |
| --- | --- | --- |
| [审计基线](00-audit-baseline.md) | 已完成文件审计；基线构建/测试未执行 | 原始版本、目录、数据库、插件、忽略语义、构建、CI、许可证 |
| `git rev-list --count 52a680c578f1af888ebb74cefcb89b736f9c5738` | 30090 | 保留基线历史；新增本地提交另计 |
| `git remote -v` / `git branch --show-current` | 已配置 origin/upstream；当前 feat/jelee-go-foundation | G01.1 的历史/远程部分；尚缺发布工作流验收 |
| [架构决策](architecture.md) | 已选择渐进替换；其代码验证以运行报告为准 | G28 设计，不能代替功能等价证明 |
| [完整验证报告](verification-report.md) | Windows/Linux Go、真实 PG、HTTP 冒烟；失败与跳过单列 | 本轮增量的真实结果；不代表全部 336 项通过 |
| [Windows 日志](evidence/windows-tests.txt) / [Linux race 日志](evidence/linux-race.txt) | 实际已执行测试；覆盖率以最终日志为准 | 当前代码合同、race、已记录的跳过；不为未实现功能背书 |
| [PG 修正后日志](evidence/postgres-optimized.txt) | 隔离 schema 的真实迁移/约束/权限/撤销/回滚通过 | G04/G07/G25/G36/G48 当前已实现子集 |
| [当前容器实测](evidence/container-current.txt) | `f21d156684` 对应镜像重建，非 root/只读根/移除 capabilities，真实 PG、NFO 与 HTTP 冒烟通过 | Compose、真实视频/客户端、升级回滚及完整运行工具验收未完成 |
| [已提交源码独立复验](evidence/committed-snapshot.txt) | `0bbd5939bb` 的 source archive 在 Windows test/build 均退出 0；不含后续 NFO 提交 | 提交快照的构建证据；PG/Windows 符号链接跳过已明确，非完整系统验收 |
| [本地提交记录](verification-report.md#本地提交记录) | 表中 7 个阶段哈希已核对；最终文档另行提交，部分完成行仅映射实际实现子集 | 189 个阻塞子项没有实现提交；文档提交不冒充功能实现 |

### 实际测试与实现索引

表中的 Plan-G 编号仍是完整需求的验收计划。下列代码链接和现有测试名称用于明确哪些子集已经真实执行；没有出现在实现中的计划目录不得被视为交付文件。

| 关联子项 | 实际实现 / 测试 | 已验证内容与限制 |
| --- | --- | --- |
| G07.4/G08/G10/G11.3/G27.3/G49.1 | [HTTP 合同](../internal/adapter/http/server_test.go)：TestConstructorRejectsInvalidConfigurationAndMissingDependencies、TestPublicHealthAndHostBoundary、TestAuthenticationAndStrictCatalogQueries、TestWebCannotSpoofNativePlaybackAndNativeRangePreservesBytes、TestCentralErrorMapping | 当前路由、Host、参数、错误、会话类型与原字节；完整兼容和全部业务 API 未实现 |
| G03 | [语言测试](../internal/platform/i18n/messages_test.go)：TestLocaleNegotiation、TestCatalogKeyAndPlaceholderParity；HTTP TestErrorLanguageNegotiationAtHTTPBoundary | 服务端四语、请求头及回退；第 2 阶段已加入保存语言优先回归，UI 未实现 |
| G04/G07.2/G25.4/G36/G48 | [PG 集成](../internal/adapter/postgres/store_integration_test.go)：TestPostgresIntegration 及具名子测试；[生产 SQL](../internal/adapter/postgres/store.go) | 专用数据库中 up/down/up、哈希/禁用/撤销、SQL ACL、失败事务无残留、约束、取消、迁移锁上限；旧库导入未实现 |
| G10/G29/G42.1 | [传输测试](../internal/adapter/media/direct_test.go)：TestDirectDeliveryRangeAndSourceUnchanged、TestMultipartRange、TestSourceLookupHasIndependentDeadline、TestCancellationInterruptsBlockedNetworkWrite | Range/条件请求/取消/额度/只读 hash；真实播放器 Seek 和端到端延迟目标未验收 |
| G09.5/G10.3 | [媒体护栏](../internal/adapter/media/guard_test.go)、[Linux 文件安全](../internal/adapter/media/direct_linux_test.go) | query/form/JSON 转换拒绝、路径与符号链接竞态；FIFO 在当前 DrvFS 明确跳过 |
| G30/G46.5 | [配置](../internal/platform/config/config_test.go)、[日志](../internal/platform/logging/logging_test.go)、[架构](../internal/architecture) | 当前错误/配置/脱敏/依赖方向；完整 telemetry、日志体系和所有覆盖率门槛未满足 |
| G51 | [工具说明](toolchain.md)、[Windows 安全测试](../scripts/test-toolchain.ps1)、[Linux 安全测试](../scripts/test_toolchain.py) | 当前 Go 工具引导、hash、安全解压与离线；完整媒体/浏览器/无 Docker 回退链未实现 |
| G39/G40.3 | [NFO 解析测试](../internal/adapter/nfo/read_test.go)：TestGoldenMovieMapsFieldsAndRetainsOriginalBytes、TestSeriesSeasonEpisodeAndWrappers、TestEncodingsPreserveBytesAndText、TestArtworkReferencesNeverTriggerNetworkRequests；[CLI 测试](../cmd/jelee-cli/nfo_test.go)：TestNFOValidateOriginalUnchangedAndSummaryPrivate、TestNFOValidateCancelsUnreadOutputPipe；[兼容范围](nfo-compatibility.md) | `f21d156684` 的 Windows/Linux race 已测只读提取、原文保留、错误/资源上限、路径与取消；不含修改后序列化、批量修复/任务、真实客户端往返 |

### SQL 权限查询的修复前诊断基线

2026-09-30 在专用 PostgreSQL 16.15 测试数据库的独立随机 schema 运行 `TestPostgresIntegration`，初始版本全部子测试通过。10,002 条合成条目中，该用户只可见 1 条。初始 `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON)` 使用 `items_pkey`，但实际扫描 10,002 条、由 join filter 排除 10,001 条，单次 Execution Time 为 **4.254 ms**。这证明 SQL 权限正确，也揭示仅断言“用了索引”不足以验证扫描量。

随后将管理员与普通用户查询分开，并按已授权库使用有上限的索引分页。集成测试直接引用生产 `listItemsSQL`，复测访问 **1 条** items。持久保存的另一次旧查询单样本为 **2.683 ms**，新查询单样本为 **0.164 ms**，见[基线日志](evidence/postgres-baseline.txt)、[修正后日志](evidence/postgres-optimized.txt)与[验证报告](verification-report.md)。随机 UUID、缓存和运行噪声未受充分控制，以上不是统计性能改善比例；不能替代 P95、10 万/100 万规模或 G48 的 ≤10% 开销验收。

## G00 项目品牌

- 验收：`make brand-scan` 零非白命中；容器启动日志与 `/api-docs` 标题均为 Jelee；许可证文件完整未被删改。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G00.1** 应用标识：可执行文件 `jelee`、服务名 `jelee`、镜像 `jelee/jelee`、配置前缀 `JELEE_`、默认数据库名 `jelee`、Web 标题 `Jelee`。 | tools/brand-scan/; go.mod; README.md; web/package.json; docs/LICENSE-COMPLIANCE.md | 不适用 | Plan-G00.1：运行品牌门禁并检查可执行文件、配置前缀、Web 标题、镜像与保留版权；全仓非白名单命中为零；须单独覆盖本行全部约束 | 部分完成：Go 三个命令、JELEE_ 配置、系统名与 API 文档标题已落地并构建；Web 品牌与完整发行镜像未全部验收；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27` / `0bbd5939bb`（仅已实现子集） |
| **G00.2** 代码层：Go module 名、包名、日志分类、指标前缀、审计事件类型统一为 `jelee`；前端 package 名、路由前缀、favicon、manifest 名称统一。 | tools/brand-scan/; go.mod; README.md; web/package.json; docs/LICENSE-COMPLIANCE.md | 不适用 | Plan-G00.2：运行品牌门禁并检查可执行文件、配置前缀、Web 标题、镜像与保留版权；全仓非白名单命中为零；须单独覆盖本行全部约束 | 部分完成：新增 Go module/包/错误及日志使用 Jelee；旧树内部命名、指标体系与前端仍未完成；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27` / `0bbd5939bb`（仅已实现子集） |
| **G00.3** 文档层：`README.md`、`docs/`、Docker/K8s 元数据统一；`CHANGELOG.md` 记录更名。 | tools/brand-scan/; go.mod; README.md; web/package.json; docs/LICENSE-COMPLIANCE.md | 不适用 | Plan-G00.3：运行品牌门禁并检查可执行文件、配置前缀、Web 标题、镜像与保留版权；全仓非白名单命中为零；须单独覆盖本行全部约束 | 部分完成：README、基础部署文档与更名记录已落地；完整自有文档/Web/K8s 未全部验收；[实际证据](verification-report.md) | 文档：README/CHANGELOG/补充说明随最终文档提交 |
| **G00.4** 品牌扫描：`tools/brand-scan` 提供 `make brand-scan`，输出残留清单与白命中；CI 强制。 | tools/brand-scan/; go.mod; README.md; web/package.json; docs/LICENSE-COMPLIANCE.md | 不适用 | Plan-G00.4：运行品牌门禁并检查可执行文件、配置前缀、Web 标题、镜像与保留版权；全仓非白名单命中为零；须单独覆盖本行全部约束 | 部分完成：brand-scan --new 零违规；完整扫描仍有 14,960 个非白名单遗留命中；工作流程／分析器／檔名解析／網路模組分別减少 41／4／226／47 处，最终门禁未通过；[工作流程证据](evidence/workflow-brand-cleanup.json)／[分析器证据](analyzer-brand-rename.md)／[檔名解析证据](naming-brand-rename.md)／[網路证据](networking-brand-rename.md) | `721102c8d0`（扫描工具；全仓门禁未通过） |
| **G00.5** 许可证合规：保留 LICENSE/NOTICE/上游版权；新增 `docs/LICENSE-COMPLIANCE.md` 说明派生关系、保留声明位置与分发义务。 | tools/brand-scan/; go.mod; README.md; web/package.json; docs/LICENSE-COMPLIANCE.md | 不适用 | Plan-G00.5：运行品牌门禁并检查可执行文件、配置前缀、Web 标题、镜像与保留版权；全仓非白名单命中为零；须单独覆盖本行全部约束 | 部分完成：LICENSE 原 SHA256、版权/NOTICE/历史保留且有合规说明；完整依赖分发许可证审核待完成；[实际证据](verification-report.md) | `0bbd5939bb`（合规说明；原许可证保留） |

## G01 Git 规范

- 验收：全新克隆后 `make init && make test` 可跑通；干净构建后 `git status` 无新增未忽略文件；提交历史可二分。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G01.1** 历史与远程：保留上游历史，配置 `upstream`；`docs/git-workflow.md` 说明 fork 同步、分支模型、发布标签。 | .gitignore; .gitattributes; .editorconfig; scripts/; docs/git-workflow.md; docs/binary-allowlist.md | 不适用 | Plan-G01.1：全新克隆后 init/test；git check-ignore；检查已跟踪二进制、密钥与生成物；保持上游提交可达；须单独覆盖本行全部约束 | 部分完成：保留基线 30,090 提交、origin/upstream 与分支；git-workflow 已写，完整同步/标签流程未演练；[实际证据](verification-report.md) | `a512674643`（基线）；远程为本地配置；git-workflow 归属最终文档提交 |
| **G01.2** 提交规范：Conventional Commits（`feat\|fix\|refactor\|perf\|chore\|docs\|test\|build\|ci`），scope 限定模块；禁止混合目标巨型提交。 | .gitignore; .gitattributes; .editorconfig; scripts/; docs/git-workflow.md; docs/binary-allowlist.md | 不适用 | Plan-G01.2：全新克隆后 init/test；git check-ignore；检查已跟踪二进制、密钥与生成物；保持上游提交可达；须单独覆盖本行全部约束 | 部分完成：已创建分阶段的 Conventional Commits 本地提交；仅对当前完整 source 快照复验，不声称每个中间提交均完成全部最终构建目标；[实际证据](verification-report.md) | [阶段提交记录](verification-report.md#本地提交记录) |
| **G01.3** 版本与变更：Semantic Versioning + `CHANGELOG.md`；发布脚本来自 tag。 | .gitignore; .gitattributes; .editorconfig; scripts/; docs/git-workflow.md; docs/binary-allowlist.md | 不适用 | Plan-G01.3：全新克隆后 init/test；git check-ignore；检查已跟踪二进制、密钥与生成物；保持上游提交可达；须单独覆盖本行全部约束 | 部分完成：CHANGELOG/Unreleased 已提供；SemVer 标签生成与发布脚本演练尚未完成；[实际证据](verification-report.md) | 文档：CHANGELOG 随最终文档提交；发布流程无实现提交 |
| **G01.4** 忽略规则：`.gitignore` 必须覆盖并逐条注释分组： | .gitignore; .gitattributes; .editorconfig; scripts/; docs/git-workflow.md; docs/binary-allowlist.md | 不适用 | Plan-G01.4：全新克隆后 init/test；git check-ignore；检查已跟踪二进制、密钥与生成物；保持上游提交可达；须单独覆盖本行全部约束 | 部分完成：工具、测试、运行、密钥与缓存忽略组已补齐；新增生成物检查通过；历史文件/全部规则正反例未全面审核；[实际证据](verification-report.md) | `721102c8d0`（仅已实现子集） |
| **G01.4b** 忽略校验：提供 `make gitignore-check`，枚举仓库中被忽略与未被忽略文件，断言无二进制/大文件/密钥/工具产物被跟踪；CI 强制；使用 `git check-ignore` 与体积阈值（如单文件 >1MB 需白名单说明）。 | .gitignore; .gitattributes; .editorconfig; scripts/; docs/git-workflow.md; docs/binary-allowlist.md | 不适用 | Plan-G01.4b：全新克隆后 init/test；git check-ignore；检查已跟踪二进制、密钥与生成物；保持上游提交可达；须单独覆盖本行全部约束 | 部分完成：gitignore-check 对新增文件与工具生成目录通过；全仓历史二进制、完整大文件/密钥门禁仍未完备；[实际证据](verification-report.md) | `721102c8d0`（仅已实现子集） |
| **G01.4c** 例外机制：确需入库的二进制（如 favicon、示例图标）必须列入 `docs/binary-allowlist.md` 并说明用途与体积，否则 CI 失败。 | .gitignore; .gitattributes; .editorconfig; scripts/; docs/git-workflow.md; docs/binary-allowlist.md | 不适用 | Plan-G01.4c：全新克隆后 init/test；git check-ignore；检查已跟踪二进制、密钥与生成物；保持上游提交可达；须单独覆盖本行全部约束 | 部分完成：binary-allowlist 说明已提供；历史二进制逐项用途与体积核对尚未完成；[实际证据](verification-report.md) | 文档：binary-allowlist 随最终文档提交 |
| **G01.5** 属性与编码：`.gitattributes` 统一 LF/UTF-8，锁定媒体与二进制文件 diff 行为；`.editorconfig` 统一缩进与换行。 | .gitignore; .gitattributes; .editorconfig; scripts/; docs/git-workflow.md; docs/binary-allowlist.md | 不适用 | Plan-G01.5：全新克隆后 init/test；git check-ignore；检查已跟踪二进制、密钥与生成物；保持上游提交可达；须单独覆盖本行全部约束 | 部分完成：保留既有 .gitattributes 并更新 .editorconfig；全部文件 LF/UTF-8 和媒体 diff 行为未全面验证；[实际证据](verification-report.md) | `721102c8d0`（仅已实现子集） |
| **G01.6** Hook 与 CI：`pre-commit` 运行 gofmt/前端 lint/品牌扫描。 | .gitignore; .gitattributes; .editorconfig; scripts/; docs/git-workflow.md; docs/binary-allowlist.md | 不适用 | Plan-G01.6：全新克隆后 init/test；git check-ignore；检查已跟踪二进制、密钥与生成物；保持上游提交可达；须单独覆盖本行全部约束 | 部分完成：本地 fmt/vet/增量品牌和 CI 入口已提供；前端 lint、完整 pre-commit 与远端强制门禁未完成；[实际证据](verification-report.md) | `721102c8d0`（仅已实现子集） |
| **G01.7** 安全：禁止提交密钥、大媒体、数据库数据、构建产物；提供密钥泄露检查说明（无授权不得重写已推送历史）。 | .gitignore; .gitattributes; .editorconfig; scripts/; docs/git-workflow.md; docs/binary-allowlist.md | 不适用 | Plan-G01.7：全新克隆后 init/test；git check-ignore；检查已跟踪二进制、密钥与生成物；保持上游提交可达；须单独覆盖本行全部约束 | 部分完成：项目忽略配置与本地敏感材料保护已验证；全历史泄露扫描与完整安全发布流程未完成；[实际证据](verification-report.md) | `721102c8d0`（仅已实现子集） |

## G02 核心范围裁剪

- 验收：导入旧音乐/图书数据后系统忽略且不报错；已删除类型接口返回 404/明确不支持且非 500；架构测试通过。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G02.1** 保留域：Movie、Series、Season、Episode、HomeVideo/其他视频、Collection、Playlist、Library、MediaSource、UserData、PlaybackProgress。 | internal/domain/; internal/adapter/postgres/; internal/adapter/compat/; web/src/router/ | 移除非视频领域迁移及 down | Plan-G02.1：架构测试拒绝已删域；旧数据导入不失败；不支持 API 返回明确 4xx/501；须单独覆盖本行全部约束 | 部分完成：新 domain/schema 有基础视频 Item、Library、MediaSource；Collection/Playlist/UserData/PlaybackProgress 等未全部实现；[实际证据](verification-report.md) | `c77863e445` / `632005d430`（仅已实现子集） |
| **G02.2** 删除域：Audio/Music、AudioBook、Book、Comic、Photo 及其实体、仓储、扫描器、解析器、任务、控制器、前端页面、资源、图标、翻译键、数据库表与迁移清理脚本。 | internal/domain/; internal/adapter/postgres/; internal/adapter/compat/; web/src/router/ | 移除非视频领域迁移及 down | Plan-G02.2：架构测试拒绝已删域；旧数据导入不失败；不支持 API 返回明确 4xx/501；须单独覆盖本行全部约束 | 部分完成：新 schema CHECK 拒绝 Audio，新增 API 未暴露被裁剪能力；保留的旧实体、控制器、任务、资源仍未清理；[实际证据](verification-report.md) | `c77863e445` / `632005d430`（仅已实现子集） |
| **G02.3** 数据层：迁移移除相关表/列，外键与约束同步；提供回滚迁移。 | internal/domain/; internal/adapter/postgres/; internal/adapter/compat/; web/src/router/ | 移除非视频领域迁移及 down | Plan-G02.3：架构测试拒绝已删域；旧数据导入不失败；不支持 API 返回明确 4xx/501；须单独覆盖本行全部约束 | 部分完成：新基础 schema up/down/up 通过；旧库裁剪迁移与原数据回滚尚未实现；[实际证据](verification-report.md) | `c77863e445` / `632005d430`（仅已实现子集） |
| **G02.4** 门禁：架构测试禁止领域层引用已删除媒体类型枚举；前端路由树不得出现相关页面。 | internal/domain/; internal/adapter/postgres/; internal/adapter/compat/; web/src/router/ | 移除非视频领域迁移及 down | Plan-G02.4：架构测试拒绝已删域；旧数据导入不失败；不支持 API 返回明确 4xx/501；须单独覆盖本行全部约束 | 部分完成：已有新 Go 分层与约束测试；旧域全范围枚举门禁及前端路由测试未完成；[实际证据](verification-report.md) | `c77863e445` / `632005d430`（仅已实现子集） |

## G03 语言与国际化

- 验收：四语切换无缺失键；模拟 `fr-FR` 请求返回英文且不报错；CI 中 i18n 检查失败即阻断。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G03.1** 仅保留 `zh-CN`（默认）、`zh-TW`、`ja-JP`、`en-US`（回退）；删除其他自有语言资源。 | internal/platform/i18n/; web/src/i18n/; tools/i18n-check/ | 用户语言设置 | Plan-G03.1：四语资源键与占位符检查；fr-FR 回退 en-US；用户偏好覆盖请求头；须单独覆盖本行全部约束 | 部分完成：服务端错误与旧树 Core UI 仅提供四语并默认 zh-CN，其他101份 UI 已按授权删除；前端未实现；[四语资源证据](ui-four-locales.md)；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G03.2** 资源结构：`web/src/i18n/<locale>/*.json`，服务端错误消息同样四语（`internal/platform/i18n`）。 | internal/platform/i18n/; web/src/i18n/; tools/i18n-check/ | 用户语言设置 | Plan-G03.2：四语资源键与占位符检查；fr-FR 回退 en-US；用户偏好覆盖请求头；须单独覆盖本行全部约束 | 部分完成：internal/platform/i18n 已实现；web/src/i18n 尚未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G03.3** 回退与协商：`Accept-Language` 解析，未知语言静默回退 `en-US`；用户级语言偏好覆盖请求头。 | internal/platform/i18n/; web/src/i18n/; tools/i18n-check/ | 用户语言设置 | Plan-G03.3：四语资源键与占位符检查；fr-FR 回退 en-US；用户偏好覆盖请求头；须单独覆盖本行全部约束 | 部分完成：Go 服务端协商与保存语言优先已实现并有 HTTP 回归；旧 C# 四语 header 协商／静默英文回退另有37项验证；前端语言切换尚未交付；[四语HTTP证据](ui-http-locales.md)；[账户验证](accounts-verification.md) | `d906f9183e` / `0beb2f17dc` |
| **G03.4** 门禁：i18n 检查脚本校验缺失键、冗余键、未使用键、JSON 合法性、占位符一致性；禁止组件内硬编码用户可见文本。 | internal/platform/i18n/; web/src/i18n/; tools/i18n-check/ | 用户语言设置 | Plan-G03.4：四语资源键与占位符检查；fr-FR 回退 en-US；用户偏好覆盖请求头；须单独覆盖本行全部约束 | 部分完成：服务端翻译键/占位符及 HTTP 错误码覆盖测试通过；旧 Core 四语 JSON/键/占位符 CI 门禁已实现；前端未使用键/硬编码文案门禁未实现；[四语资源证据](ui-four-locales.md)；[实际证据](verification-report.md) ；Go HTTP已新增所有正式來源AST缺失／未使用鍵門禁，[證據](http-i18n-key-audit.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G03.5** 文本质量：简繁不混用；日文敬体统一；日期/数字/单位按 locale 格式化。 | internal/platform/i18n/; web/src/i18n/; tools/i18n-check/ | 用户语言设置 | Plan-G03.5：四语资源键与占位符检查；fr-FR 回退 en-US；用户偏好覆盖请求头；须单独覆盖本行全部约束 | 部分完成：四语错误文本已有回归；完整 UI 日语文风及日期/数字/单位本地化未验收；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |

## G04 PostgreSQL 主存储

- 验收：空库迁移到最新再全量 down/up 成功；10 万条模拟数据关键查询走索引（`EXPLAIN` 入档）；迁移工具导入后行数与校验和一致。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G04.1** 连接：`pgx/v5` + `pgxpool`；连接串仅来自环境变量/密钥文件；TLS 可配置；连接池参数可调（见 G41 并发预算）。 | internal/adapter/postgres/; cmd/jelee-migrate/; internal/adapter/compat/migration/ | 版本化 PostgreSQL up/down；SQLite 导入映射 | Plan-G04.1：真实 PostgreSQL up/down/up；10 万条 EXPLAIN；旧库导入行数/校验和；池取消与死锁恢复；须单独覆盖本行全部约束 | 部分完成：pgxpool、环境/密钥文件与 MaxConns 已实现且隔离库连通通过；TLS 可由 DSN 配置，真实 TLS 与全部池参数验收未完成；[实际证据](verification-report.md) | `632005d430` / `c77863e445`（仅已实现子集） |
| **G04.2** 迁移：`golang-migrate` 或 Atlas（二选一，需说明）；已发布迁移不可变；提供 up/down。 | internal/adapter/postgres/; cmd/jelee-migrate/; internal/adapter/compat/migration/ | 版本化 PostgreSQL up/down；SQLite 导入映射 | Plan-G04.2：真实 PostgreSQL up/down/up；10 万条 EXPLAIN；旧库导入行数/校验和；池取消与死锁恢复；须单独覆盖本行全部约束 | 部分完成：golang-migrate迁移链至schema6；001–005原文不变，006 up/down/up、活动只读phase拒绝down已验证；完整业务迁移链未完成；[3C3B实际证据](nfo-cache-verification.md) | `632005d430` / `c77863e445`（仅已实现子集） / `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`（第3C3B子集） |
| **G04.3** 查询：`sqlc` 或手写类型安全仓储；禁止字符串拼接 SQL；所有查询参数化。 | internal/adapter/postgres/; cmd/jelee-migrate/; internal/adapter/compat/migration/ | 版本化 PostgreSQL up/down；SQLite 导入映射 | Plan-G04.3：真实 PostgreSQL up/down/up；10 万条 EXPLAIN；旧库导入行数/校验和；池取消与死锁恢复；须单独覆盖本行全部约束 | 部分完成：当前仓储使用参数化 SQL 和类型化 Go 返回值，真实集成通过；完整业务仓储尚未实现；[实际证据](verification-report.md) | `632005d430` / `c77863e445`（仅已实现子集） |
| **G04.4** Schema：外键、唯一约束、CHECK、合适索引（部分索引/表达式索引/GIN），JSONB 仅用于确实动态数据。 | internal/adapter/postgres/; cmd/jelee-migrate/; internal/adapter/compat/migration/ | 版本化 PostgreSQL up/down；SQLite 导入映射 | Plan-G04.4：真实 PostgreSQL up/down/up；10 万条 EXPLAIN；旧库导入行数/校验和；池取消与死锁恢复；须单独覆盖本行全部约束 | 部分完成：基础约束和跨库root外键已测；006新增NFO JSONB长度/状态/计费约束，10,000条NFO inventory/cache索引页实测；完整G36 schema及10万条全部关键查询仍未验收；[3C3B实际证据](nfo-cache-verification.md) | `632005d430` / `c77863e445`（仅已实现子集） / `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`（第3C3B子集） |
| **G04.5** 事务与并发：用例级事务边界明确；热路径避免长事务；批量写入用 `COPY`/多值插入；死锁重试与退避。 | internal/adapter/postgres/; cmd/jelee-migrate/; internal/adapter/compat/migration/ | 版本化 PostgreSQL up/down；SQLite 导入映射 | Plan-G04.5：真实 PostgreSQL up/down/up；10 万条 EXPLAIN；旧库导入行数/校验和；池取消与死锁恢复；须单独覆盖本行全部约束 | 部分完成：基础Provision/ImportVideo事务已测；NFO cache/配额/checkpoint原子提交，写后取消、租约途中到期、deferred COMMIT/清理拒绝完整回滚已实测；COPY、死锁退避及全用例事务未完成；[3C3B实际证据](nfo-cache-verification.md) | `632005d430` / `c77863e445`（仅已实现子集） / `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`（第3C3B子集） |
| **G04.6** 迁移工具：Jellyfin/旧 Jelee SQLite → PostgreSQL 工具，支持预检、断点续传、幂等、失败回滚、行数核对、校验和与迁移报告。 | internal/adapter/postgres/; cmd/jelee-migrate/; internal/adapter/compat/migration/ | 版本化 PostgreSQL up/down；SQLite 导入映射 | Plan-G04.6：真实 PostgreSQL up/down/up；10 万条 EXPLAIN；旧库导入行数/校验和；池取消与死锁恢复；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G04.7** 缓存边界：Redis（可选）只做缓存/锁；失效以版本号或事件驱动；不可用时降级而非错误。 | internal/adapter/postgres/; cmd/jelee-migrate/; internal/adapter/compat/migration/ | 版本化 PostgreSQL up/down；SQLite 导入映射 | Plan-G04.7：真实 PostgreSQL up/down/up；10 万条 EXPLAIN；旧库导入行数/校验和；池取消与死锁恢复；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G04.8** 门禁：禁止 SQLite 生产回退；生产模式检测非 postgres 驱动即拒绝启动。 | internal/adapter/postgres/; cmd/jelee-migrate/; internal/adapter/compat/migration/ | 版本化 PostgreSQL up/down；SQLite 导入映射 | Plan-G04.8：真实 PostgreSQL up/down/up；10 万条 EXPLAIN；旧库导入行数/校验和；池取消与死锁恢复；须单独覆盖本行全部约束 | 部分完成：生产配置拒绝非 PostgreSQL 且无 SQLite 回退；现有路径测试通过，完整上线系统尚未交付；[实际证据](verification-report.md) | `632005d430` / `c77863e445`（仅已实现子集） |

## G05 移除 DLNA / 直播 / 录制

- 验收：局域网 SSDP 探测无 Jelee 响应；直播接口返回明确不支持；代码中无相关监听器注册。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G05.1** 删除：DLNA/SSDP/UPnP 与发现、Live TV、EPG、Tuner、Recording、Channel 及控制器、任务、配置、依赖、前端 UI。 | internal/adapter/http/; internal/adapter/compat/; deploy/; web/src/ | 删除发现/直播相关表列并提供恢复策略 | Plan-G05.1：SSDP 无响应；直播探测明确不支持；无相关端口与监听器注册；须单独覆盖本行全部约束 | 部分完成：新入口不注册发现/直播/录制；旧 C# 伺服器UDP7359探索host／payload／schema已删并有正反回归；錄製啟動／通知host與設定更新建立媒體庫回呼已刪並有正反回歸；舊直播／Channel控制器與專用DTO也已刪並有正式HTTP501回歸；EPG／Channel排程、掃描後清理及三個提供者也已刪，移除五個queue呼叫；一般DTO的直播／錄製建構與增補相依也已刪，14項既有回歸及完整C#通過；使用者視圖的直播／Channel建構與外抓相依也已刪，四單元與新舊HTTP回歸通過；其餘DI／調諧器／核心服務等相依仍保留；[視圖解耦](user-view-live-decoupling.md)；四個核心與延遲註冊已移除，六項啟動回歸通過，[註冊解耦](live-core-registration-removal.md)；指南核心實作／介面已裁剪，[指南核心](guide-core-removal.md)；[DTO解耦](dto-live-decoupling.md)；[自動工作裁剪](live-feature-actors-removal.md)；[舊HTTP裁剪](legacy-removed-features.md)；[錄製啟動證據](recording-startup-removal.md)；[探索裁剪证据](server-discovery-removal.md)；[实际证据](verification-report.md) | `403cc21b27`（仅已实现子集） |
| **G05.2** 兼容声明：客户端探测返回空能力或明确不支持（`501`/空数组），不得 500，不得静默启用。 | internal/adapter/http/; internal/adapter/compat/; deploy/; web/src/ | 删除发现/直播相关表列并提供恢复策略 | Plan-G05.2：SSDP 无响应；直播探测明确不支持；无相关端口与监听器注册；须单独覆盖本行全部约束 | 部分完成：新 Go 三类探测根路径明确501/feature_removed，系统相关能力全false，四语／HEAD／守卫优先／无后端操作回归通过；舊C#直播／Channel控制器與專用DTO已刪，正式host回501並保留設定授權／IP限制、設定不變／OpenAPI／四語回歸；其餘內部服務與真實第三方握手仍未完成；[舊HTTP合同](legacy-removed-features.md)；[探测合同与证据](compat-matrix.md)；[实际证据](verification-report.md) | `403cc21b27`（仅已实现子集） |
| **G05.3** 端口与依赖：关闭相关监听端口；移除 SSDP 多播与 NAT 依赖；防火墙文档同步。 | internal/adapter/http/; internal/adapter/compat/; deploy/; web/src/ | 删除发现/直播相关表列并提供恢复策略 | Plan-G05.3：SSDP 无响应；直播探测明确不支持；无相关端口与监听器注册；须单独覆盖本行全部约束 | 部分完成：Go 运行时只开配置 HTTP 监听；旧 C# 伺服器探索监听已删，HTTP及探索埠防火墙文档已补；其余调谐器UDP／依赖清理与SSDP实测未完成；[探索裁剪证据](server-discovery-removal.md)；[实际证据](verification-report.md) | `403cc21b27`（仅已实现子集） |
| **G05.4** 清理：数据库表/列、配置项、翻译键、图标一并清理。 | internal/adapter/http/; internal/adapter/compat/; deploy/; web/src/ | 删除发现/直播相关表列并提供恢复策略 | Plan-G05.4：SSDP 无响应；直播探测明确不支持；无相关端口与监听器注册；须单独覆盖本行全部约束 | 部分完成：刪除已退休Channel排程專用的三個四語翻譯鍵，四資源121鍵門禁與完整回歸通過；資料表／欄位、設定、其他翻譯與圖示仍待清理；[清理證據](live-feature-actors-removal.md) | 本批來源摘要見[證據](evidence/live-feature-actors-removal.json) |

## G06 去除下载能力

- 验收：遍历自有与兼容 API 断言无 attachment 响应头；授权测试证明任何角色无法触发下载。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G06.1** 删除下载权限位、下载按钮、附件式下载接口与原文件导出/打包接口。 | internal/domain/; internal/adapter/http/; internal/adapter/compat/; docs/security-model.md | 移除 download 权限字段 | Plan-G06.1：全角色调用下载路径被拒；播放响应无 attachment；兼容 DTO 永久 false；须单独覆盖本行全部约束 | 部分完成：新 API 没有下载接口/权限且只读传输；保留旧树/未来 UI 的全面裁剪未完成；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G06.2** 响应约束：播放流不得返回 `Content-Disposition: attachment`；兼容 DTO 中旧下载字段固定 `false`。 | internal/domain/; internal/adapter/http/; internal/adapter/compat/; docs/security-model.md | 移除 download 权限字段 | Plan-G06.2：全角色调用下载路径被拒；播放响应无 attachment；兼容 DTO 永久 false；须单独覆盖本行全部约束 | 部分完成：HTTP/媒体合同断言 direct 与 Range 无 attachment；兼容 DTO 旧下载字段尚未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G06.3** 权限模型：所有用户默认且永久无下载权限；管理员不可授予（权限位移除而非仅 UI 隐藏）。 | internal/domain/; internal/adapter/http/; internal/adapter/compat/; docs/security-model.md | 移除 download 权限字段 | Plan-G06.3：全角色调用下载路径被拒；播放响应无 attachment；兼容 DTO 永久 false；须单独覆盖本行全部约束 | 部分完成：新权限 schema 不含可授予下载位；完整角色/管理员和兼容接口矩阵尚未交付；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G06.4** 文档：`docs/security-model.md` 明确“可播放媒体理论上可能被客户端录制，系统只能移除官方显式下载能力”。 | internal/domain/; internal/adapter/http/; internal/adapter/compat/; docs/security-model.md | 移除 download 权限字段 | Plan-G06.4：全角色调用下载路径被拒；播放响应无 attachment；兼容 DTO 永久 false；须单独覆盖本行全部约束 | 部分完成：security-model 已说明可播放内容无法阻止客户端录制；完整安全模型随剩余模块补充；[实际证据](verification-report.md) | 文档：security-model 随最终文档提交 |

## G07 用户管理

- 验收：权限矩阵测试覆盖全部角色 × 操作；限速、锁定、撤销令牌、并发限制均有集成测试；绕过 UI 直接调 API 也被拒。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G07.1** 生命周期：创建、启用/禁用、软删除、恢复、重命名、头像、资料字段。 | internal/app/; internal/access/; internal/adapter/http/; internal/adapter/postgres/; web/src/features/users/ | users/sessions/devices/user_data/audit_logs | Plan-G07.1：角色×操作授权矩阵；密码/令牌/锁定/撤销/并发集成；直接 API 绕过 UI 仍被拒；须单独覆盖本行全部约束 | 部分完成：创建/启停/软删除/恢复/重命名/资料 API 与 CLI 已验证；头像与管理 UI 未交付；[账户验证](accounts-verification.md) | `d906f9183e` / `0beb2f17dc` |
| **G07.2** 认证：密码哈希（Argon2id 或 bcrypt，参数可配置）、密码策略、改密需验证旧密码、令牌轮换与撤销。 | internal/app/; internal/access/; internal/adapter/http/; internal/adapter/postgres/; web/src/features/users/ | users/sessions/devices/user_data/audit_logs | Plan-G07.2：角色×操作授权矩阵；密码/令牌/锁定/撤销/并发集成；直接 API 绕过 UI 仍被拒；须单独覆盖本行全部约束 | 已完成：可配置 Argon2id、密码策略、验证旧密码改密、轮换及撤销均有单元/真实数据库/HTTP/执行档证据；[账户验证](accounts-verification.md) | `d906f9183e` / `0beb2f17dc` |
| **G07.3** 防护：登录限速（IP + 用户维度）、失败锁定与解锁、可疑登录记录与通知。 | internal/app/; internal/access/; internal/adapter/http/; internal/adapter/postgres/; web/src/features/users/ | users/sessions/devices/user_data/audit_logs | Plan-G07.3：角色×操作授权矩阵；密码/令牌/锁定/撤销/并发集成；直接 API 绕过 UI 仍被拒；须单独覆盖本行全部约束 | 部分完成：有界 IP/名称限速、持久锁定/解锁与已知账户失败审计已验证；可疑登录通知与分布式防护尚未实现；[账户验证](accounts-verification.md) | `d906f9183e` / `0beb2f17dc` |
| **G07.4** 会话与设备：会话列表、设备识别、强制下线、并发播放上限、带宽上限（按用户/设备）。 | internal/app/; internal/access/; internal/adapter/http/; internal/adapter/postgres/; web/src/features/users/ | users/sessions/devices/user_data/audit_logs | Plan-G07.4：角色×操作授权矩阵；密码/令牌/锁定/撤销/并发集成；直接 API 绕过 UI 仍被拒；须单独覆盖本行全部约束 | 部分完成：有效会话列表、设备标签、强制下线及有效会话数上限已验证；可信设备识别和按用户/设备播放/带宽上限未完成；[账户验证](accounts-verification.md) | `d906f9183e` / `0beb2f17dc` |
| **G07.5** 授权：媒体库可见性、内容分级、管理操作最小权限、隐藏用户。 | internal/app/; internal/access/; internal/adapter/http/; internal/adapter/postgres/; web/src/features/users/ | users/sessions/devices/user_data/audit_logs | Plan-G07.5：角色×操作授权矩阵；密码/令牌/锁定/撤销/并发集成；直接 API 绕过 UI 仍被拒；须单独覆盖本行全部约束 | 部分完成：库 ACL 管理、事务内角色/会话重验与隐藏字段已验证，普通用户无用户发现接口；内容分级及细分管理权限未完成；[账户验证](accounts-verification.md) | `d906f9183e` / `0beb2f17dc` |
| **G07.6** 审计：用户相关管理操作写审计日志（操作者、目标、前后值、IP、时间）。 | internal/app/; internal/access/; internal/adapter/http/; internal/adapter/postgres/; web/src/features/users/ | users/sessions/devices/user_data/audit_logs | Plan-G07.6：角色×操作授权矩阵；密码/令牌/锁定/撤销/并发集成；直接 API 绕过 UI 仍被拒；须单独覆盖本行全部约束 | 部分完成：账户操作有目标、可用操作者/IP、时间与安全前后值的事务审计；本地运维操作者身份及完整审计查询/保留策略未完成；[账户验证](accounts-verification.md) | `d906f9183e` / `0beb2f17dc` |
| **G07.7** 数据权利：导出与删除用户数据（含播放记录），删除后不可恢复且级联处理。 | internal/app/; internal/access/; internal/adapter/http/; internal/adapter/postgres/; web/src/features/users/ | users/sessions/devices/user_data/audit_logs | Plan-G07.7：角色×操作授权矩阵；密码/令牌/锁定/撤销/并发集成；直接 API 绕过 UI 仍被拒；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G07.8** 可选：TOTP 双因素（不得破坏第三方客户端登录路径，需提供无 2FA 设备令牌流程说明）。 | internal/app/; internal/access/; internal/adapter/http/; internal/adapter/postgres/; web/src/features/users/ | users/sessions/devices/user_data/audit_logs | Plan-G07.8：角色×操作授权矩阵；密码/令牌/锁定/撤销/并发集成；直接 API 绕过 UI 仍被拒；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G08 Jelee 自有 API

- 验收：OpenAPI 与实现一致性检查通过；错误码表测试；越权/非法输入返回 4xx；模糊测试无 panic。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G08.1** 框架：chi（或等价）路由；中间件顺序固定：request-id → 恢复 → 日志 → 追踪 → 超时 → 认证 → 授权 → 校验 → 限流。 | internal/adapter/http/; internal/platform/; api/openapi.json; tools/openapi/ | idempotency_keys | Plan-G08.1：路由与 OpenAPI 合同；未知字段/参数/分页/错误映射；幂等重放；取消传播；fuzz 无 panic；须单独覆盖本行全部约束 | 部分完成：chi、边界恢复/请求 ID/安全日志/认证已测试；完整规定中间件顺序、OTel/限流尚未完成；[实际证据](verification-report.md) | `403cc21b27`（仅已实现子集） |
| **G08.2** 规范：统一分页（cursor 优先，offset 兼容）、排序白名单、过滤白名单、字段选择、错误模型（code/message/details/traceId）。 | internal/adapter/http/; internal/platform/; api/openapi.json; tools/openapi/ | idempotency_keys | Plan-G08.2：路由与 OpenAPI 合同；未知字段/参数/分页/错误映射；幂等重放；取消传播；fuzz 无 panic；须单独覆盖本行全部约束 | 部分完成：基础 cursor/limit、严格 query 和统一 error 已测试；offset、排序/过滤/字段选择未完成；[实际证据](verification-report.md) | `403cc21b27`（仅已实现子集） |
| **G08.3** 校验：请求体 schema 校验，未知字段策略明确且一致；路径/查询参数强类型。 | internal/adapter/http/; internal/platform/; api/openapi.json; tools/openapi/ | idempotency_keys | Plan-G08.3：路由与 OpenAPI 合同；未知字段/参数/分页/错误映射；幂等重放；取消传播；fuzz 无 panic；须单独覆盖本行全部约束 | 部分完成：基础 UUID、查询白名单/重复键/非法编码有合同测试；全部写接口 schema 校验未实现；[实际证据](verification-report.md) | `403cc21b27`（仅已实现子集） |
| **G08.4** 幂等：写接口支持 `Idempotency-Key`（至少覆盖用户创建、Webhook 创建、库扫描触发、NFO 写回）。 | internal/adapter/http/; internal/platform/; api/openapi.json; tools/openapi/ | idempotency_keys | Plan-G08.4：路由与 OpenAPI 合同；未知字段/参数/分页/错误映射；幂等重放；取消传播；fuzz 无 panic；须单独覆盖本行全部约束 | 部分完成：用户创建与盘点触发/重试支持 actor/key 重放；Webhook 与 NFO 写回仍待交付；[第3A实际证据](jobs-verification.md) | `45d3fbfcb8`（本段已实现子集） |
| **G08.5** 文档：OpenAPI 3.1 由代码/注解生成并 CI 校验是否过期。 | internal/adapter/http/; internal/platform/; api/openapi.json; tools/openapi/ | idempotency_keys | Plan-G08.5：路由与 OpenAPI 合同；未知字段/参数/分页/错误映射；幂等重放；取消传播；fuzz 无 panic；须单独覆盖本行全部约束 | 部分完成：OpenAPI 3.1 基础路由文档存在；完整模型/示例/从代码生成及过期门禁未完成；[实际证据](verification-report.md) | `403cc21b27`（仅已实现子集） |
| **G08.6** 版本：`/api/v1` 前缀 + 兼容层独立前缀；破坏性变更走 v2 与弃用头。 | internal/adapter/http/; internal/platform/; api/openapi.json; tools/openapi/ | idempotency_keys | Plan-G08.6：路由与 OpenAPI 合同；未知字段/参数/分页/错误映射；幂等重放；取消传播；fuzz 无 panic；须单独覆盖本行全部约束 | 部分完成：原生 /api/v1 已实现；独立兼容层与版本弃用机制尚未实现；[实际证据](verification-report.md) | `403cc21b27`（仅已实现子集） |
| **G08.7** 加固：限流、请求体大小上限、超时、取消传播到 DB 与子进程。 | internal/adapter/http/; internal/platform/; api/openapi.json; tools/openapi/ | idempotency_keys | Plan-G08.7：路由与 OpenAPI 合同；未知字段/参数/分页/错误映射；幂等重放；取消传播；fuzz 无 panic；须单独覆盖本行全部约束 | 部分完成：已实现接口的 context/期限/大小限制覆盖任务 API 与 DB；未来接口及子进程仍待交付；[第3A实际证据](jobs-verification.md) | `403cc21b27`（仅已实现子集）；`45d3fbfcb8`（第3A子集） |

## G09 本地媒体处理与工具链

- 验收：恶意文件名/路径穿越/超长参数测试全部被拒；子进程取消后可回收无残留；临时目录无积累（含崩溃后清理）。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G09.1** 存储：媒体、外挂字幕、外挂音轨、NFO、图片资产、封面、章节、探测缓存全部位于本地卷；目录结构在 `docs/storage-layout.md` 定义；支持多库多路径。 | internal/adapter/media/; internal/platform/; docs/storage-layout.md | tool_versions/probe_cache | Plan-G09.1：恶意文件名、路径越界、symlink、取消、输出上限与崩溃清理；工具版本与缺失降级；须单独覆盖本行全部约束 | 部分完成：CLI 登记本地根与相对路径；派生资产/多库完整扫描与 storage-layout 文档未完成；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G09.2** 调用安全：仅用 `os/exec` 参数数组调用 ffmpeg/ffprobe/mkvpropedit/mkvmerge/mediainfo；禁止 shell 字符串拼接；禁止用户输入直接成为参数或输出路径。 | internal/adapter/media/; internal/platform/; docs/storage-layout.md | tool_versions/probe_cache | Plan-G09.2：恶意文件名、路径越界、symlink、取消、输出上限与崩溃清理；工具版本与缺失降级；须单独覆盖本行全部约束 | 部分完成：固定argv/受信registry/no-shell/no用户参数已验证；Linux用os.StartProcess、Windows用原生原子Job启动，尚未统一原文os/exec入口及全部工具，见[3B2验证](process-verification.md) | `03a559524c`（第3B2子集） |
| **G09.3** 工具管理：启动探测工具存在与版本，记录到 `tool_versions`；缺失时相关能力降级并给出可操作错误。 | internal/adapter/media/; internal/platform/; docs/storage-layout.md | tool_versions/probe_cache | Plan-G09.3：恶意文件名、路径越界、symlink、取消、输出上限与崩溃清理；工具版本与缺失降级；须单独覆盖本行全部约束 | 部分完成：固定工具/runtime身份及tool_versions已接入启动健康检查与持久请求；缺失或运行故障降级、默认关闭、Windows停用和真服务system capability已验证，见[worker验证](probe-worker-verification.md)；MediaInfo/mkv等后续工具尚未交付 | `03a559524c`（第3B2子集）  / `27f66b1eb3` / `3d8842be1e`（第3C2A子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） |
| **G09.4** 执行控制：超时、取消（进程组终止）、并发上限、输出大小上限、临时目录隔离与清理。 | internal/adapter/media/; internal/platform/; docs/storage-layout.md | tool_versions/probe_cache | Plan-G09.4：恶意文件名、路径越界、symlink、取消、输出上限与崩溃清理；工具版本与缺失降级；须单独覆盖本行全部约束 | 部分完成：双平台timeout/cancel/树终止/并发和输出限额/正常清理已验证；生产媒体隔离与崩溃残留清理待后续，见[3B2验证](process-verification.md) | `03a559524c`（第3B2子集） |
| **G09.5** 路径安全：路径规范化 + 根目录边界校验 + 符号链接逃逸防护。 | internal/adapter/media/; internal/platform/; docs/storage-layout.md | tool_versions/probe_cache | Plan-G09.5：恶意文件名、路径越界、symlink、取消、输出上限与崩溃清理；工具版本与缺失降级；须单独覆盖本行全部约束 | 部分完成：os.OpenRoot、相对路径与 symlink 逃逸/替换竞态测试通过；NFO/图片/字幕等全部文件面尚未实现；[实际证据](verification-report.md)；3C3A新增NFO held/current root与file身份核对、变化拒绝及句柄清理，见[NFO来源验证](nfo-source-verification.md) | `c77863e445` / `403cc21b27`（仅已实现子集） / `feeee03da801bb4730eca86b39d55eb5b1521783`（第3C3A） |
| **G09.6** 权限：以低权限运行外部工具（容器非 root 优先）。 | internal/adapter/media/; internal/platform/; docs/storage-layout.md | tool_versions/probe_cache | Plan-G09.6：恶意文件名、路径越界、symlink、取消、输出上限与崩溃清理；工具版本与缺失降级；须单独覆盖本行全部约束 | 部分完成：Linux正式profile的ffprobe/helper/8个库在UID65532只读容器实测，root/inode/祖先与不可写检查通过；Windows隔离/其他工具尚未实现，见[3C1验证](probe-verification.md) | `27f66b1eb3`（第3C1子集） |

## G10 严禁播放/严禁转码下的播放体验（完整资源直投）

- 验收：生产模式下全部转码/HLS/DASH 参数与路由返回统一错误码；代码中转码路径在关闭态不可达（有断言测试）；Direct Play 首字节与 Seek 指标达标（见性能验收）；原媒体校验和在播放前后一致（证明未被修改）。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G10.1** 铁律：服务端只投递完整、未经改动的原始资源；禁止转码、重编码、码率自适应、分辨率/帧率/色域转换、音轨重编码、字幕烧录、HLS/DASH 切片、分片封装。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.1：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 部分完成：新服务仅只读原字节直投，转换参数/路由拒绝及原文件 SHA256 不变已测试；完整迁移能力仍待验收；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G10.2** 唯一允许的两种投递：Direct Play（原文件原样流式返回）与 Remux（仅换容器/重封装，不重编码音视频；默认关闭，需显式开启且注明会改变字节流）。二者都必须保持原始码流不被重编码。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.2：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 部分完成：Direct Play 已实现，Remux 关闭且未实现；不把 Remux 缺失计为支持；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G10.3** 请求拒绝：任何请求转码的参数（如 `maxVideoBitrate`、`videoCodec`、`audioCodec`、`TranscodeReasons`、`Segment*`、`hls` 等）一律返回明确错误码（如 `transcode_disabled`，HTTP 409/501 二选一并全局统一）与可读提示，不得静默降级为直投。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.3：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 部分完成：当前播放面 query/JSON/form/编码路由禁止转换，统一 409 transcode_disabled；完整兼容 API 合同待实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G10.4** 能力声明：兼容层与自有 API 的客户端能力协商必须声明 `Transcoding: disabled/absent`、`HLS: unsupported`、`DASH: unsupported`；相关字段统一为空数组或 false，并在 `docs/compat-matrix.md` 中记录对客户端行为的已知影响。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.4：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 部分完成：自有 system 明示 transcoding/hls/dash/remux=false；兼容能力 DTO 与真实客户端影响记录未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G10.5** 不兼容处理：无法直投的客户端/格式应返回明确“不支持直投”提示（含原因：容器/编码/字幕/音轨），不得尝试转码救场；UI 与日志需可排查。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.5：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G10.6** 启动加速：媒体探测与关键帧/时长索引缓存、moov/faststart 标记提示、首段优先、连接复用、合理缓冲参数、流打开前的权限与存在性快速校验。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.6：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G10.7** Seek：客户端侧基于本地索引与关键帧表定位；服务端支持完整 Range（单段/多段）、`If-Range`、写超时、客户端断开快速终止；服务端不得为 Seek 重新编码。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.7：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 部分完成：单段/多段 Range、If-Range、写期限和取消实测；客户端关键帧索引与 Seek P95 未验收；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G10.8** 流式实现：`io.Copy`/`io.CopyN` + 显式缓冲；零拷贝路径（如可用 `Sendfile`）在支持平台上启用并有回退。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.8：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 部分完成：标准 ServeContent 的有界原文件传输已测；显式 io.Copy 缓冲与零拷贝路径测量未完整实施；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G10.9** 字幕与音轨：不烧录、不重编码；仅以独立外挂资源或客户端可识别的内嵌轨形式提供；外挂字幕/音轨按 G15/G16 原样直投。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.9：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G10.10** 禁止修改原媒体：不得重写、faststart 改写、标签写回原文件；如需提示优化，只给出建议不执行。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.10：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 部分完成：当前登记/传输无原媒体写入，前后 hash 一致；后续扫描与资产处理仍须维持该合同；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G10.11** 开发者模式例外：仅在 G45 开启时允许临时启用转码用于调试，必须在能力声明、日志、UI 横幅中标记 `dev-transcode`，关闭即刻失效；生产模式绝不可达该代码路径（以测试断言）。 | internal/adapter/http/; internal/adapter/media/; internal/adapter/compat/; internal/platform/ | media_sources/probe_cache | Plan-G10.11：转码/HLS/DASH 全路由与参数一致拒绝；Range/If-Range 字节验证；原媒体 hash 不变；首字节/Seek P95；须单独覆盖本行全部约束 | 部分完成：生产没有 dev-transcode 路径且 dev 开启请求拒绝；完整开发者模式例外未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |

## G11 网络协议与隐私防泄露

- 验收：自动化扫描全部 API/响应头/日志/错误页/WebSocket 帧，断言无公网 IP 与内网地址泄露；SSRF 用例（127.0.0.1、169.254.169.254、内网段、DNS 重绑定模拟）全部被拦截。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G11.1** URL 生成：对外 URL 一律相对地址或显式 `PublicBaseUrl`；禁止依据 `Host`/`X-Forwarded-*` 猜测公网地址。 | internal/platform/network/; internal/adapter/http/; docs/network-privacy.md; deploy/ | 网络设置 | Plan-G11.1：Host/代理注入、SSRF 私网/重定向/DNS 重绑定回归；响应/日志/WS 地址泄漏扫描；须单独覆盖本行全部约束 | 部分完成：当前 API 只使用相对文档链接且不由 Host/forwarding 推导 URL；原文允许相对地址，PublicBaseUrl属于可选替代；共用网络Plan中的SSRF／WS／完整部署矩阵尚未完成；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G11.2** 代理信任：仅信任配置的 `TRUSTED_PROXIES` CIDR；未命中时忽略转发头并记录告警。 | internal/platform/network/; internal/adapter/http/; docs/network-privacy.md; deploy/ | 网络设置 | Plan-G11.2：Host/代理注入、SSRF 私网/重定向/DNS 重绑定回归；响应/日志/WS 地址泄漏扫描；须单独覆盖本行全部约束 | 部分完成：Go可信CIDR與嚴格XFF鏈已接登入限流／稽核，未可信及非法鏈固定告警不含原值，真HTTP與正反回歸通過；完整公網代理部署矩陣仍待驗收；[證據](trusted-proxies.md)；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G11.3** Host 校验：允许名单 + 拒绝非法 Host；避免 Host 头注入。 | internal/platform/network/; internal/adapter/http/; docs/network-privacy.md; deploy/ | 网络设置 | Plan-G11.3：Host/代理注入、SSRF 私网/重定向/DNS 重绑定回归；响应/日志/WS 地址泄漏扫描；须单独覆盖本行全部约束 | 部分完成：Host 允许名单、端口和括号语法及伪造转发头回归通过；完整代理发布验收待完成；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G11.4** SSRF：所有出站请求（TMDB、图片抓取、Webhook、NFO 外链）经自定义 `http.Client`/Dialer 拦截私网/环回/link-local，含重定向二次校验与 DNS 重绑定防护。 | internal/platform/network/; internal/adapter/http/; docs/network-privacy.md; deploy/ | 网络设置 | Plan-G11.4：Host/代理注入、SSRF 私网/重定向/DNS 重绑定回归；响应/日志/WS 地址泄漏扫描；须单独覆盖本行全部约束 | 部分完成：受控GET已接TMDB憑據啟動預檢，私網／混合DNS／固定IP／重建連線重綁定／重定向拒絕／TLS與錯誤安全正反回歸通過；其他抓取與舊SDK、完整漏洩矩陣未完成；[證據](outbound-tmdb-preflight.md) | [來源與執行證據](evidence/outbound-tmdb-preflight.json)；只屬已實作子集 |
| **G11.5** 隐私开关：禁用发现/UPnP/STUN/自动 NAT；默认仅监听 `127.0.0.1` 经反向代理发布；局域网监听需显式开启。 | internal/platform/network/; internal/adapter/http/; docs/network-privacy.md; deploy/ | 网络设置 | Plan-G11.5：Host/代理注入、SSRF 私网/重定向/DNS 重绑定回归；响应/日志/WS 地址泄漏扫描；须单独覆盖本行全部约束 | 部分完成：默认 127.0.0.1 且新服务无发现/NAT/STUN；全部部署模式隐私验证未完成；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G11.6** 响应头：CSP、HSTS、X-Content-Type-Options、Referrer-Policy；不泄露版本指纹与内网 IP；错误页/重定向/WebSocket 不得回显公网 IP。 | internal/platform/network/; internal/adapter/http/; docs/network-privacy.md; deploy/ | 网络设置 | Plan-G11.6：Host/代理注入、SSRF 私网/重定向/DNS 重绑定回归；响应/日志/WS 地址泄漏扫描；须单独覆盖本行全部约束 | 部分完成：CSP/nosniff/no-referrer/no-store、TLS 场景 HSTS 与安全错误已实现；全部兼容/WS/日志地址面未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G11.7** 协议优化：HTTP/2（TLS 场景）、Keep-Alive、压缩仅用于非媒体响应、WebSocket 心跳与背压。 | internal/platform/network/; internal/adapter/http/; docs/network-privacy.md; deploy/ | 网络设置 | Plan-G11.7：Host/代理注入、SSRF 私网/重定向/DNS 重绑定回归；响应/日志/WS 地址泄漏扫描；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G11.8** 文档：`docs/network-privacy.md` 明确“客户端直连公网 IP 时该 IP 不可能对该客户端隐藏”。 | internal/platform/network/; internal/adapter/http/; docs/network-privacy.md; deploy/ | 网络设置 | Plan-G11.8：Host/代理注入、SSRF 私网/重定向/DNS 重绑定回归；响应/日志/WS 地址泄漏扫描；须单独覆盖本行全部约束 | 部分完成：[網路隱私文件](network-privacy.md)已明確說明直連公網IP不可能隱藏，三項既有边界回歸通過；共用網路驗收中的SSRF、WS與完整部署矩陣尚未完成 | [文件與來源證據](evidence/network-privacy-documentation.json)；本階段無產品變更 |

## G12 Webhook

- 验收：故障注入（超时/5xx/慢响应）验证退避与死信；重放成功；签名校验测试；端到端事件 5s 内首次投递。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G12.1** 事件：媒体新增/更新/删除、扫描开始/完成/失败、播放开始/暂停/进度/停止、用户登录/失败/锁定、会话创建/结束、NFO 写回、图片抓取完成、系统告警；事件含稳定 `eventId`、`type`、`version`、`occurredAt`。 | internal/adapter/events/; internal/app/; internal/platform/network/; web/src/features/webhooks/ | outbox/webhooks/webhook_deliveries | Plan-G12.1：超时/5xx/慢响应故障注入；退避死信重放；HMAC 与脱敏；首次投递 5 秒以内；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G12.2** 配置：多端点、事件订阅过滤、启用/禁用、每端点独立密钥、自定义头、超时、重试策略。 | internal/adapter/events/; internal/app/; internal/platform/network/; web/src/features/webhooks/ | outbox/webhooks/webhook_deliveries | Plan-G12.2：超时/5xx/慢响应故障注入；退避死信重放；HMAC 与脱敏；首次投递 5 秒以内；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G12.3** 可靠投递：Outbox 表 + 后台投递器；至少一次投递；指数退避 + 抖动；最大重试与死信；手动重放；投递日志可查询。 | internal/adapter/events/; internal/app/; internal/platform/network/; web/src/features/webhooks/ | outbox/webhooks/webhook_deliveries | Plan-G12.3：超时/5xx/慢响应故障注入；退避死信重放；HMAC 与脱敏；首次投递 5 秒以内；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G12.4** 安全：HMAC-SHA256 签名（`X-Jelee-Signature`、`X-Jelee-Timestamp`）、防重放窗口、密钥轮换、载荷脱敏（不含令牌/密码/完整本地路径）。 | internal/adapter/events/; internal/app/; internal/platform/network/; web/src/features/webhooks/ | outbox/webhooks/webhook_deliveries | Plan-G12.4：超时/5xx/慢响应故障注入；退避死信重放；HMAC 与脱敏；首次投递 5 秒以内；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G12.5** 网络：目标地址经 SSRF 防护与白名单（可配）；HTTPS 证书校验不可关闭（除非显式自签 CA 配置）。 | internal/adapter/events/; internal/app/; internal/platform/network/; web/src/features/webhooks/ | outbox/webhooks/webhook_deliveries | Plan-G12.5：超时/5xx/慢响应故障注入；退避死信重放；HMAC 与脱敏；首次投递 5 秒以内；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G12.6** 顺序与幂等：`eventId` 供消费方去重；不承诺全局顺序。 | internal/adapter/events/; internal/app/; internal/platform/network/; web/src/features/webhooks/ | outbox/webhooks/webhook_deliveries | Plan-G12.6：超时/5xx/慢响应故障注入；退避死信重放；HMAC 与脱敏；首次投递 5 秒以内；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G13 定时任务与媒体库扫描

- 验收：1000 文件库增量扫描仅处理变化项（有统计证据）；任务重叠被锁拒绝；中途取消重启可续跑；删除阈值保护生效。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G13.1** 调度器：cron/interval/一次性；任务定义持久化；支持启用/禁用、手动触发、历史与日志查看。 | internal/app/jobs/; internal/app/scan/; internal/adapter/postgres/; web/src/features/jobs/ | jobs/job_runs/job_locks/scan_checkpoints | Plan-G13.1：1000 文件变化计数；advisory lock 拒绝重叠；取消重启续跑；误删阈值保护；须单独覆盖本行全部约束 | 部分完成：一次性持久盘点、手动/背景优先与历史 API；schema41 已接持久 cron／interval、時區、定義啟停、手動觸發與交易防重；管理 UI 等仍待交付；[排程證據](evidence/scan-schedules.json)；[第3A实际证据](jobs-verification.md)  schema40的影片批次匯入已接同一佇列／worker，100筆、取消恢復、舊租約拒絕、逐筆回滾與來源保留經真PG及TLS驗收；[批次任務](catalog-import-jobs.md)； | `45d3fbfcb8`（本段已实现子集） |
| **G13.2** 并发控制：Postgres advisory lock 或租约保证同一任务不重叠；实例级任务 leader 选举（见 G41）。 | internal/app/jobs/; internal/app/scan/; internal/adapter/postgres/; web/src/features/jobs/ | jobs/job_runs/job_locks/scan_checkpoints | Plan-G13.2：1000 文件变化计数；advisory lock 拒绝重叠；取消重启续跑；误删阈值保护；须单独覆盖本行全部约束 | 部分完成：真实 PG 同库排他、跨 owner 租约恢复/fencing；排程 leader 尚未实现；[第3A实际证据](jobs-verification.md)  schema40的影片批次匯入已接同一佇列／worker，100筆、取消恢復、舊租約拒絕、逐筆回滾與來源保留經真PG及TLS驗收；[批次任務](catalog-import-jobs.md)； | `45d3fbfcb8`（本段已实现子集） |
| **G13.3** 可靠性：任务可取消、可恢复、可观测（进度、处理/总数、ETA）、失败有原因与重试。 | internal/app/jobs/; internal/app/scan/; internal/adapter/postgres/; web/src/features/jobs/ | jobs/job_runs/job_locks/scan_checkpoints | Plan-G13.3：1000 文件变化计数；advisory lock 拒绝重叠；取消重启续跑；误删阈值保护；须单独覆盖本行全部约束 | 部分完成：取消、检查点恢复、尝试上限、固定失败码和真实计数；3C3C增加NFO阶段恢复及真实取消后图片基线保护/重扫恢复。未知总数/ETA不猜测，完整可观测界面待交付；[第3A证据](jobs-verification.md)、[3C3C证据](nfo-worker-verification.md)  schema40的影片批次匯入已接同一佇列／worker，100筆、取消恢復、舊租約拒絕、逐筆回滾與來源保留經真PG及TLS驗收；[批次任務](catalog-import-jobs.md)； | `45d3fbfcb8`（本段已实现子集） / `47da27b5ff090a71e6f55aae0b2e6c00871f825b`（第3C3C子集） |
| **G13.4** 扫描：fsnotify 监听 + 去抖；增量扫描基于路径/大小/mtime/快速指纹；未变化文件不重复昂贵探测；批处理与检查点；删除确认阈值（防误判全库删除）。 | internal/app/jobs/; internal/app/scan/; internal/adapter/postgres/; web/src/features/jobs/ | jobs/job_runs/job_locks/scan_checkpoints | Plan-G13.4：1000 文件变化计数；advisory lock 拒绝重叠；取消重启续跑；误删阈值保护；须单独覆盖本行全部约束 | 部分完成：盘点、缺失保护、租约/批次检查点、probe与NFO缓存均接真实worker；1,000与100混合库冷/暖/局部修改的实际读取/解析/子程序次数已验，图片属性纳入比较；已补根映射epoch不可比与完整根覆盖的缺失保护，见[库存范围验证](inventory-scope.md)；schema42 已接 Linux fsnotify／Windows 控制代碼監看、去抖、重建及啟動核對；201 檔突發與重啟真測通過，完整規模與穩態仍待驗收；[監看證據](evidence/scan-watch.json)；[probe验收](probe-worker-verification.md)、[3C3C验收](nfo-worker-verification.md) ；清單批次 SQL 往返已降為 13 次，一萬檔案有實測；十萬檔案基準發布逾時，完整規模未驗收，見[批次證據](evidence/scan-batch.json) | `45d3fbfcb8`（本段已实现子集） / `3d8842be1e`（第3C2A子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） / `47da27b5ff090a71e6f55aae0b2e6c00871f825b`（第3C3C子集） |
| **G13.5** 性能：目录级并发、I/O 与 CPU 分离、低优先级完整校验、扫描窗口避开高峰、NFO/图片解析纳入同一增量判定。 | internal/app/jobs/; internal/app/scan/; internal/adapter/postgres/; web/src/features/jobs/ | jobs/job_runs/job_locks/scan_checkpoints | Plan-G13.5：1000 文件变化计数；advisory lock 拒绝重叠；取消重启续跑；误删阈值保护；须单独覆盖本行全部约束 | 部分完成：固定有界worker、两级优先、两个NFO处理slot、暖扫省解析及图片kind/size/mtime增量已接线，真实1,000与100混合库通过。目录并发、CPU/I/O分额、扫描窗口和图片解码待交付；[3C3C实际证据](nfo-worker-verification.md) | `45d3fbfcb8`（本段已实现子集） / `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`（第3C3B子集） / `47da27b5ff090a71e6f55aae0b2e6c00871f825b`（第3C3C子集） |
| **G13.6** 其他任务：元数据刷新、NFO 写回、图片抓取/重建、探测与关键帧索引缓存清理、提取字幕清理、统计聚合、数据一致性校验（G50）、备份提醒。 | internal/app/jobs/; internal/app/scan/; internal/adapter/postgres/; web/src/features/jobs/ | jobs/job_runs/job_locks/scan_checkpoints | Plan-G13.6：1000 文件变化计数；advisory lock 拒绝重叠；取消重启续跑；误删阈值保护；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G14 刮削与 TMDB 优先

- 验收：模拟 TMDB 服务完成 100 部电影 + 20 部剧集刮削，命中率与错误率入档；429/超时/非法响应处理正确；锁定字段不被覆盖。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G14.1** 数据源：TMDB 为默认第一优先级；支持配置备用源与本地 NFO；不得网页抓取或绕过 TMDB 条款。 | internal/adapter/metadata/; internal/app/; web/src/features/metadata/ | metadata_providers/metadata_locks | Plan-G14.1：模拟 100 电影/20 剧集；429 Retry-After/超时/非法响应；字段锁与来源优先级；须单独覆盖本行全部约束 | 部分完成：正式Go管理員API依TMDB ID取得電影／劇集／季／集候選資料，經受控出站與共享治理、有限快取、保留來源時間；電影名稱／年份與劇集首播年份候選搜尋已接線；季／集依確定號碼取得已驗證；自动刮削、备用源／本地NFO優先與完整條款／前端矩陣尚未完成；[電影](tmdb-movie-preview.md)；[劇集](tmdb-series-preview.md)；[季與集](tmdb-season-episode-preview.md) | [來源與執行證據](evidence/tmdb-movie-preview.json)；只屬已實作子集 |
| **G14.2** 凭据：`TMDB_API_KEY` 仅来自环境变量/密钥文件；启动校验可用性与配额。 | internal/adapter/metadata/; internal/app/; web/src/features/metadata/ | metadata_providers/metadata_locks | Plan-G14.2：模拟 100 电影/20 剧集；429 Retry-After/超时/非法响应；字段锁与来源优先级；须单独覆盖本行全部约束 | 部分完成：API key只從環境／秘密檔載入，配置後監聽前經受控TMDB authentication驗證，401／429／錯誤正文／取消拒絕啟動；真外部憑據、剩餘配額與完整刮削矩陣尚未驗收；[證據](outbound-tmdb-preflight.md) | [來源與執行證據](evidence/outbound-tmdb-preflight.json)；只屬已實作子集 |
| **G14.3** 请求治理：限流器、并发上限、重试（含 429 `Retry-After`）、缓存（内存 + 持久化可选）、超时与取消。 | internal/adapter/metadata/; internal/app/; web/src/features/metadata/ | metadata_providers/metadata_locks | Plan-G14.3：模拟 100 电影/20 剧集；429 Retry-After/超时/非法响应；字段锁与来源优先级；须单独覆盖本行全部约束 | 部分完成：正式TMDB預檢有250ms啟動间隔／4容量、429及503共享冷卻，三次重試／Retry-After／總期限與取消，真TLS與接線負例通過；電影與劇集各256筆／24h語言分區LRU快取與runtime/API接線已驗證，季16筆／單集256筆、完整身份＋語言鍵與集陣列複製已驗證；圖片共16筆完整資源／ID／有序語言鍵快取、陣列隔離與24h到期已驗證；認證與錯誤不cache；並行miss合併、可配置預算、多實例及完整刮削矩陣未完成；[電影快取](tmdb-movie-preview.md)；[劇集快取](tmdb-series-preview.md)；[季／集快取](tmdb-season-episode-preview.md)；[治理](tmdb-governor.md)；[重試](tmdb-retry.md) | [治理執行證據](evidence/tmdb-governor.json)；只屬已實作子集 |
| **G14.4** 匹配：电影/剧集按标题+年份、外部 ID（IMDB/TVDB/TMDB）；季度/单集号匹配；低置信度不自动写入并标记待人工确认。 | internal/adapter/metadata/; internal/app/; web/src/features/metadata/ | metadata_providers/metadata_locks | Plan-G14.4：模拟 100 电影/20 剧集；429 Retry-After/超时/非法响应；字段锁与来源优先级；须单独覆盖本行全部约束 | 部分完成：管理員電影名稱＋選填上映年份／劇集首播年份查詢、TMDB ID電影／劇集詳細取得、標題／年份比較及全部候選待確認已接正式API，真TLS与100合成名稱契約通過；確定季／集號及上游身份核對已驗證；檔名解析與自動配對、IMDB／TVDB、完整置信度及寫入矩陣尚未完成；[電影](tmdb-movie-search.md)；[劇集](tmdb-series-preview.md)；[季／集](tmdb-season-episode-preview.md) | [電影證據](evidence/tmdb-movie-search.json)；[劇集證據](evidence/tmdb-series-preview.json)；[季／集證據](evidence/tmdb-season-episode-preview.json)；只屬已實作子集 |
| **G14.5** 语言：按用户/库语言请求，回退链 `zh-CN → zh-TW → ja-JP → en-US`；图片语言偏好可配。 | internal/adapter/metadata/; internal/app/; web/src/features/metadata/ | metadata_providers/metadata_locks | Plan-G14.5：模拟 100 电影/20 剧集；429 Retry-After/超时/非法响应；字段锁与来源优先级；须单独覆盖本行全部约束 | 部分完成：六個正式管理員API採已認證使用者四語偏好及明確覆蓋，詳細簡介缺失與搜尋空頁按既定鏈回退；欄位請求語言與時間、身份及快取隔離、共用期限與終止錯誤經實際TLS／正反驗收；媒體庫持久化四語設定、版本衝突與稽核已接六路由庫偏好選擇；有序圖片偏好已持久化並接電影／劇集圖片API，真HTTP／PG及TLS正反驗收通過；其他文字、季／集圖片與完整前端／矩陣尚缺；[圖片偏好](tmdb-image-preferences.md)；[語言回退](tmdb-language-fallback.md)；[媒體庫設定](tmdb-library-language.md) | [來源與執行證據](evidence/tmdb-language-fallback.json)；只屬已實作子集 |
| **G14.6** 覆盖：NFO 与人工编辑优先于自动刮削；字段级锁，锁定字段不被覆盖（与 G39 联动）。 | internal/adapter/metadata/; internal/app/; web/src/features/metadata/ | metadata_providers/metadata_locks | Plan-G14.6：模拟 100 电影/20 剧集；429 Retry-After/超时/非法响应；字段锁与来源优先级；须单独覆盖本行全部约束 | 部分完成：永久item人工欄位／來源／鎖定已持久化，管理員HTTP／app／PG與catalog標題同步、版本衝突、撤銷session、原子回滾及升降版保護經正反驗收；明確確認TMDB後的真HTTP／TLS／PG寫入已驗人工與鎖保護，100電影／20劇集合成矩陣通過；可信NFO四欄抽取／來源歸屬／短交易及人工空值與NFO鎖保護已經真檔案HTTP／PG正反驗收；唯讀庫與TMDB同交易融合已經真HTTP／TLS／NFO／PG正反驗收，僅一次版本／稽核；唯讀庫可信缺失／損壞回退與安全歷史狀態亦保存於同次交易，人工空值與既有NFO來源保持；[回退驗證](nfo-tmdb-fallback.md)；實體匯入、完整前端仍缺；[人工元資料](item-metadata.md)；[確認套用](tmdb-metadata-apply.md)；[NFO套用](nfo-item-metadata.md)；[同次融合](nfo-tmdb-fusion.md) 第25版補上缺少文字值時的獨立NFO鎖定證明；人工文字接管同交易解除、人工鎖開關不能繞過、讀取off後仍保護；[缺值鎖定](nfo-field-lock-intent.md)； 第26版支援有效lock-only專用投影：正鎖／有效觀察同交易保存，原文字來源與分類保持，指定鎖允許其他TMDB欄位，查詢期間指令變更拒409；[lock-only契約](nfo-lock-only.md)； 第27版新增sortTitle正式套用／來源／獨立鎖與人工空值接管，NFO-only及TMDB同交易融合，別名歧義拒絕與保留資料降版保護；[排序標題](nfo-sort-title.md)； 第28版新增tagline／outline／mpaa／certification正式套用，九文字來源／獨立鎖／人工patch與同交易融合，OfficialRating無值鎖不虛構來源，舊投影集合保持；[九文字契約](nfo-text-fields.md)； | [人工證據](evidence/item-metadata.json)；[套用證據](evidence/tmdb-metadata-apply.json)；[NFO證據](evidence/nfo-item-metadata.json)；只屬已實作子集 |
| **G14.7** 合规：记录数据来源与抓取时间；提供“移除外部元数据”能力；遵守 TMDB 署名与缓存要求。 | internal/adapter/metadata/; internal/app/; web/src/features/metadata/ | metadata_providers/metadata_locks | Plan-G14.7：模拟 100 电影/20 剧集；429 Retry-After/超时/非法响应；字段锁与来源优先级；须单独覆盖本行全部约束 | 部分完成：正式搜尋／詳細候選含TMDB來源、標準頁面與UTC取得時間，確認套用後欄位持久保存ID／來源／時間，簡介回退保存自身來源；API docs Credits有官方標誌／聲明；[套用](tmdb-metadata-apply.md)；移除外部元資料、完整前端及TMDB條款／快取要求尚未验收；[候選](tmdb-movie-preview.md)；[搜尋](tmdb-movie-search.md) | [候選證據](evidence/tmdb-movie-preview.json)；[搜尋證據](evidence/tmdb-movie-search.json)；只屬已實作子集 |

## G15 字幕格式与处理

- 验收：每种格式有最小可再分发样本与解析测试；编码检测正确率入档；外挂/内嵌优先级规则测试；原字幕文件不被修改。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G15.1** 支持：SRT、ASS/SSA、WebVTT、TTML/DFXP、SAMI/SMI、MicroDVD(.sub)、VobSub(.sub/.idx)、PGS/SUP、DVB、MKV 内嵌字幕轨道。 | internal/adapter/media/; internal/adapter/compat/; docs/subtitles.md | media_streams/subtitle_preferences | Plan-G15.1：每类格式/编码合成样本；外挂/内嵌优先级；原字幕 hash 不变；提取能力实测；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G15.2** 元数据：语言、标题、forced、SDH、default、编码、来源（内嵌/外挂）。 | internal/adapter/media/; internal/adapter/compat/; docs/subtitles.md | media_streams/subtitle_preferences | Plan-G15.2：每类格式/编码合成样本；外挂/内嵌优先级；原字幕 hash 不变；提取能力实测；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G15.3** 命名：外挂字幕命名规则（`名称.语言[.forced][.sdh][.default].ext`、同名多轨、`Subs/` 子目录）并有解析器测试。 | internal/adapter/media/; internal/adapter/compat/; docs/subtitles.md | media_streams/subtitle_preferences | Plan-G15.3：每类格式/编码合成样本；外挂/内嵌优先级；原字幕 hash 不变；提取能力实测；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G15.4** 编码：UTF-8/UTF-16/GBK/Shift_JIS/BIG5 检测与转换；BOM 处理；乱码回退策略。 | internal/adapter/media/; internal/adapter/compat/; docs/subtitles.md | media_streams/subtitle_preferences | Plan-G15.4：每类格式/编码合成样本；外挂/内嵌优先级；原字幕 hash 不变；提取能力实测；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G15.5** 提取与直投：ffprobe 识别、mkvmerge 提取内嵌文本字幕到可重建缓存并原样直投给客户端；位图字幕（VobSub/PGS/DVB）直投原轨，不烧录、不转换；明确能力边界并写入文档。 | internal/adapter/media/; internal/adapter/compat/; docs/subtitles.md | media_streams/subtitle_preferences | Plan-G15.5：每类格式/编码合成样本；外挂/内嵌优先级；原字幕 hash 不变；提取能力实测；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G15.6** OCR：默认关闭，显式开启后可运行；需限流、并发受限，并说明准确率与资源开销。 | internal/adapter/media/; internal/adapter/compat/; docs/subtitles.md | media_streams/subtitle_preferences | Plan-G15.6：每类格式/编码合成样本；外挂/内嵌优先级；原字幕 hash 不变；提取能力实测；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G15.7** 附件：ASS 字体附件提取到缓存目录并在渲染/转换时提供。 | internal/adapter/media/; internal/adapter/compat/; docs/subtitles.md | media_streams/subtitle_preferences | Plan-G15.7：每类格式/编码合成样本；外挂/内嵌优先级；原字幕 hash 不变；提取能力实测；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G16 外挂音轨

- 验收：每种格式有最小样本与探测测试；音轨元信息正确性断言；外挂音轨可被第三方客户端识别与选择；不支持格式被明确拒绝而非静默失败。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G16.1** 格式：MKA 及常见 AAC/M4A、AC3、EAC3、DTS/DTS-HD、TrueHD、FLAC、ALAC、Opus、Vorbis、MP3、WAV/PCM。 | internal/adapter/media/; internal/adapter/compat/; web/src/features/media/ | media_streams/track_preferences | Plan-G16.1：各格式探测与命名样例；第三方选择音轨；无法解码明确拒绝；不重编码；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G16.2** 命名与目录：`名称.语言[. commentary][. default][. forced].ext`、同名规则、子目录 `Audio/`，兼容常见第三方约定。 | internal/adapter/media/; internal/adapter/compat/; web/src/features/media/ | media_streams/track_preferences | Plan-G16.2：各格式探测与命名样例；第三方选择音轨；无法解码明确拒绝；不重编码；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G16.3** 元数据：语言、标题、评论音轨、默认/强制、声道数、采样率、码率（按需探测）。 | internal/adapter/media/; internal/adapter/compat/; web/src/features/media/ | media_streams/track_preferences | Plan-G16.3：各格式探测与命名样例；第三方选择音轨；无法解码明确拒绝；不重编码；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G16.4** 呈现：在播放信息中作为可选音轨返回（原样直投，不重编码）；客户端无法解码时返回明确“不支持直投该音轨”说明，禁止重编码救场。 | internal/adapter/media/; internal/adapter/compat/; web/src/features/media/ | media_streams/track_preferences | Plan-G16.4：各格式探测与命名样例；第三方选择音轨；无法解码明确拒绝；不重编码；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G16.5** 交互：用户可按条目保存音轨偏好；多版本各自保存偏好。 | internal/adapter/media/; internal/adapter/compat/; web/src/features/media/ | media_streams/track_preferences | Plan-G16.5：各格式探测与命名样例；第三方选择音轨；无法解码明确拒绝；不重编码；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G18 初始引导

- 验收：向导中断/重连/重复进入均不出错；未完成初始化时 API 返回明确状态而非 500；无头初始化一键部署成功。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G18.1** 向导步骤：语言 → 管理员账户（密码强度校验）→ PostgreSQL 连通性与迁移 → 媒体目录（存在/权限校验）→ TMDB 配置 → 工具链检测 → NFO 与图片策略（读取/写回/抓取）→ 网络发布模式与隐私检查 → 完成。 | internal/app/setup/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/setup/ | setup_state/settings/users | Plan-G18.1：中断/重复向导恢复；事务回滚；半初始化 API 拒绝；CLI 无头引导；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G18.2** 状态机：步骤状态持久化，可前进/回退/中断恢复；重入向导不重复创建管理员。 | internal/app/setup/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/setup/ | setup_state/settings/users | Plan-G18.2：中断/重复向导恢复；事务回滚；半初始化 API 拒绝；CLI 无头引导；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G18.3** 校验：每步实时验证并给出可操作错误（目录不可读、端口冲突、DB 版本过低）。 | internal/app/setup/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/setup/ | setup_state/settings/users | Plan-G18.3：中断/重复向导恢复；事务回滚；半初始化 API 拒绝；CLI 无头引导；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G18.4** 原子性：完成阶段用事务写入初始化记录；失败回滚；半初始化实例不得对外服务（中间件拦截）。 | internal/app/setup/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/setup/ | setup_state/settings/users | Plan-G18.4：中断/重复向导恢复；事务回滚；半初始化 API 拒绝；CLI 无头引导；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G18.5** CLI 兜底：`jelee-cli setup --non-interactive` 支持无头部署与容器初始化。 | internal/app/setup/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/setup/ | setup_state/settings/users | Plan-G18.5：中断/重复向导恢复；事务回滚；半初始化 API 拒绝；CLI 无头引导；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G19 媒体信息提取

- 验收：规范化字段黄金样本测试（含 DV/HDR/TrueHD/ATMOS/PGS）；缓存命中率与探测耗时入档；损坏文件不中断扫描。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G19.1** 探测：ffprobe JSON 为主；MediaInfo 补充（MKV 章节/附件/标签与部分编码细节）；mkvmerge/mkvpropedit 处理 Matroska 章节与附件。 | internal/adapter/media/; internal/domain/; internal/app/scan/ | probe_cache/media_sources/media_streams/chapters | Plan-G19.1：DV/HDR/TrueHD/ATMOS/PGS 黄金字段；工具版本变更失效；损坏文件不中断；缓存计数；须单独覆盖本行全部约束 | 部分完成：正式Linux隔离ffprobe JSON主探测已通过原创MP4/MKV；MediaInfo/mkv工具、附件/标签及业务整合待后续，见[3C1验证](probe-verification.md) | `27f66b1eb3`（第3C1子集） |
| **G19.2** 规范化：视频编码、Profile/Level、分辨率、帧率、码率、色域、HDR10/HLG/Dolby Vision、色彩空间/传输/原色；音频编码、声道布局、采样率、位深；字幕编码；时长、容器、章节。 | internal/adapter/media/; internal/domain/; internal/app/scan/ | probe_cache/media_sources/media_streams/chapters | Plan-G19.2：DV/HDR/TrueHD/ATMOS/PGS 黄金字段；工具版本变更失效；损坏文件不中断；缓存计数；须单独覆盖本行全部约束 | 部分完成：有界白名单模型/数字校验及H.264/AAC/SubRip/尺寸/帧率/时长/容器/章节真素材验证通过；HDR/DV/Atmos等只有合成JSON契约，真实特殊codec未验收，见[3C1验证](probe-verification.md) | `27f66b1eb3`（第3C1子集） |
| **G19.3** 缓存：缓存键 = 路径 + 大小 + mtime + 工具版本 + 必要指纹；支持按库/条目失效与重建；缓存清理任务。 | internal/adapter/media/; internal/domain/; internal/app/scan/ | probe_cache/media_sources/media_streams/chapters | Plan-G19.3：完整有效性键变化；库/item重建幂等；周期清理/配额；真PG竞争回滚与1000→0→17实际子程序计数 | 已完成：路径/size/mtime/完整工具身份/必要edge指纹有效性键、库/item失效重建、周期有界清理及真实1000→0→17验收通过；实现见[worker契约](probe-worker.md)，测试及Linux amd64启用边界见[worker验证](probe-worker-verification.md) | `3d8842be1e`（第3C2A契约子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） |
| **G19.4** 容错：损坏文件标记 `probe_failed` 并可重试；不阻塞整体扫描；错误信息脱敏。 | internal/adapter/media/; internal/domain/; internal/app/scan/ | probe_cache/media_sources/media_streams/chapters | Plan-G19.4：真实坏媒体固定失败码；负TTL后重试；混合好坏文件父任务完成；公开摘要与日志脱敏 | 已完成：adapter/worker/真PG验证固定probe_failed、负TTL后重试、取消和工具错误分类；真服务两好一坏仍完成，次轮正命中2/负命中1，公开错误与日志脱敏通过，见[worker验证](probe-worker-verification.md) | `27f66b1eb3`（第3C1子集） / `3d8842be1e`（第3C2A子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） |
| **G19.5** 性能：并发上限、进程池、批处理写入、避免重复探测（与 G41/G42 联动）。 | internal/adapter/media/; internal/domain/; internal/app/scan/ | probe_cache/media_sources/media_streams/chapters | Plan-G19.5：DV/HDR/TrueHD/ATMOS/PGS 黄金字段；工具版本变更失效；损坏文件不中断；缓存计数；须单独覆盖本行全部约束 | 部分完成：worker本地gate、全局file lease、连续hit批次、有界背压与真实重复探测计数通过，见[worker验证](probe-worker-verification.md)；常驻进程池、跨实例实际负载和完整性能尚未交付 | `27f66b1eb3`（第3C1子集） / `3d8842be1e`（第3C2A子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） |

## G20 同一条目多版本

- 验收：构造 4K/HDR/1080p/导演剪辑多版本库，聚合与选择符合预期；人工拆分/合并与进度继承测试通过。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G20.1** 聚合规则：同一影片/单集的不同分辨率、HDR/DV、编码、音轨、剪辑版聚合为一个逻辑条目；按文件名、目录、NFO 外部 ID、时长相近度与人工指定综合判定。 | internal/domain/; internal/app/versions/; internal/adapter/http/; web/src/features/media/ | items/item_versions/media_sources/track_preferences | Plan-G20.1：4K/HDR/1080p/剪辑版聚合；防跨影片误合并；拆分撤销；逻辑进度继承；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G20.2** 版本展示：版本标签（1080p/2160p/HDR/DV/导演剪辑/REMUX）、默认版本选择（设备能力、带宽、用户偏好）。 | internal/domain/; internal/app/versions/; internal/adapter/http/; web/src/features/media/ | items/item_versions/media_sources/track_preferences | Plan-G20.2：4K/HDR/1080p/剪辑版聚合；防跨影片误合并；拆分撤销；逻辑进度继承；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G20.3** 人工干预：合并/拆分、设置主版本、排除误合并；操作写审计。 | internal/domain/; internal/app/versions/; internal/adapter/http/; web/src/features/media/ | items/item_versions/media_sources/track_preferences | Plan-G20.3：4K/HDR/1080p/剪辑版聚合；防跨影片误合并；拆分撤销；逻辑进度继承；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G20.4** 数据归属：观看进度归属逻辑条目；流选择、音轨/字幕偏好可按版本保存。 | internal/domain/; internal/app/versions/; internal/adapter/http/; web/src/features/media/ | items/item_versions/media_sources/track_preferences | Plan-G20.4：4K/HDR/1080p/剪辑版聚合；防跨影片误合并；拆分撤销；逻辑进度继承；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G20.5** 边界：不得跨不同剧集/影片错误合并（NFO 外部 ID 优先）；误合并可一键撤销。 | internal/domain/; internal/app/versions/; internal/adapter/http/; web/src/features/media/ | items/item_versions/media_sources/track_preferences | Plan-G20.5：4K/HDR/1080p/剪辑版聚合；防跨影片误合并；拆分撤销；逻辑进度继承；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G21 季度与集数识别

- 验收：测试集全绿；随机改名样本识别准确率入档；锁定项不被覆盖。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G21.1** 目录/文件：`Season 01`、`S01`、`第1季`、`シーズン1`、`Specials`、`Season 0`、`SP`、`OVA`、`S01E01`、`1x01`、`EP01`、多集（`S01E01-E03`）、绝对集数、`Part` 分片合并。 | internal/adapter/media/naming/; internal/app/scan/; web/src/features/media/ | metadata_locks/episode_numbers | Plan-G21.1：≥200 中英日命名参数化用例；绝对集数/日期/多集；人工锁定不被覆盖；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G21.2** 优先级：NFO/外部 ID > 明确命名 > 目录结构 > 启发式；外部 ID 存在时以元数据为准。 | internal/adapter/media/naming/; internal/app/scan/; web/src/features/media/ | metadata_locks/episode_numbers | Plan-G21.2：≥200 中英日命名参数化用例；绝对集数/日期/多集；人工锁定不被覆盖；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G21.3** 特殊：Anime 绝对编号、OVA/剧场版归类、跨季合订、日期型命名可配置。 | internal/adapter/media/naming/; internal/app/scan/; web/src/features/media/ | metadata_locks/episode_numbers | Plan-G21.3：≥200 中英日命名参数化用例；绝对集数/日期/多集；人工锁定不被覆盖；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G21.4** 测试：参数化测试集覆盖中/英/日命名，≥200 条用例；解析器改动必须补用例。 | internal/adapter/media/naming/; internal/app/scan/; web/src/features/media/ | metadata_locks/episode_numbers | Plan-G21.4：≥200 中英日命名参数化用例；绝对集数/日期/多集；人工锁定不被覆盖；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G21.5** 纠错：识别结果可在 UI 修正并锁定；锁定后不被扫描覆盖。 | internal/adapter/media/naming/; internal/app/scan/; web/src/features/media/ | metadata_locks/episode_numbers | Plan-G21.5：≥200 中英日命名参数化用例；绝对集数/日期/多集；人工锁定不被覆盖；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G22 忽略规则

- 验收：契约测试覆盖上游文档化行为 + 边界用例；忽略命中可追溯；规则修改后重新扫描生效。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G22.1** 自有：`.jeleeignore`，语法与 `.gitignore` 兼容（通配、`**`、否定 `!`、锚定 `/`、注释、转义）。 | internal/platform/ignore/; internal/adapter/media/ignore/; internal/adapter/compat/ignore/（后续）; docs/ignore-source-audit.md | 无；扫描规则缓存可重建 | Plan-G22.1：纯matcher契约/语义/边界/编码/取消/并发/fuzz及真实Git差分已执行；3D1B安全来源与编译缓存另有实际验证；兼容解析和生产重扫/报告后续单验 | 部分完成：纯 .jeleeignore 有界编译/匹配、通配/锚定/否定/注释/转义、80组黄金及60组组合语料的真实Windows/Linux Git对照已通过；3D1B已补安全来源发现与缓存，正式worker已接線（PR25）；公開API/CLI與報告見[使用說明](ignore-api.md)，仍待規則修改後重掃完整驗收。[3D1A证据](ignore-matcher-verification.md)、[3D1B证据](ignore-source-verification.md) | `9afc8f15c6ca3b190bc6eafdb7e24356ad1cd33c`（第3D1A子集；G22.2仅来源审计） |
| **G22.2** 兼容：先从对应上游版本源码确认 `.jellyfinignore`/`.embyignore`/`.ignore` 的确切语义（记录来源与版本），再实现；不得凭印象。 | internal/platform/ignore/; internal/adapter/media/ignore/; internal/adapter/compat/ignore/（后续）; docs/ignore-source-audit.md | 无；扫描规则缓存可重建 | Plan-G22.2：纯matcher契约/语义/边界/编码/取消/并发/fuzz及真实Git差分已执行；3D1B安全来源与编译缓存另有实际验证；兼容解析和生产重扫/报告后续单验 | 部分完成：固定包裝器與 Ignore 0.2.1 依賴來源，未修改 C# 來源實測 253 組；新增規則及 UTF-16 轉換器與獨立引擎差分工具（PR29，63eb3c101d）。有界相容執行層已交付 PR30，安全最近來源觀察已交付 PR31；schema13 來源保存與凍結驗證見[保存合同](ignore-legacy-storage.md)，同根 1–129 次查詢已支援共享祖先去重與原子批次保存；schema14 增加綁定租約與固定期限的[查詢復核進度](ignore-legacy-verification.md)。已新增[來源解碼與批次匹配](ignore-legacy-matching.md)，包含 47 組 ReadAllText 對照與兩平台原生子程序。已新增[合併掃描 adapter](ignore-family-scan.md)，保留兩種來源證據與排除原因。已接入 schema15 的[原子掃描保存](ignore-family-storage.md)，另已新增[缺失父目錄的來源觀察與匹配](ignore-legacy-baseline.md)，schema16保存原始查詢與缺失邊界，支援凍結分頁還原及單次原生復查；schema17新增[基線查詢的任務級復核](ignore-legacy-baseline-verification.md)。已新增[遵守祖先排除的合併基線分類](ignore-family-baseline.md)，保留兩種來源證據及命中祖先；schema18新增[分類與來源原子保存](ignore-family-baseline-storage.md)，並已加入[三路共同復核與封存](ignore-family-verification.md)及[合併基線發布](ignore-family-publication.md)、[worker 整合](ignore-family-worker.md)，正式worker與公開入口仍待接線。正式 worker／公開入口及全量相容驗收未完成；另兩種格式仍缺對應來源。[固定來源審計](ignore-source-audit.md)、[相容實測](ignore-legacy-audit.md) | `9afc8f15c6ca3b190bc6eafdb7e24356ad1cd33c`（第3D1A子集；G22.2仅来源审计） |
| **G22.3** 行为：目录级继承与就近优先、大小写敏感策略可配、UTF-8/UTF-16 BOM 处理、路径穿越防护、符号链接不穿越。 | internal/platform/ignore/; internal/adapter/media/ignore/; internal/adapter/compat/ignore/（后续）; docs/ignore-source-audit.md | 无；扫描规则缓存可重建 | Plan-G22.3：纯matcher契约/语义/边界/编码/取消/并发/fuzz及真实Git差分已执行；3D1B安全来源与编译缓存另有实际验证；兼容解析和生产重扫/报告后续单验 | 部分完成：纯值层继承/就近优先、父目录剪枝、显式ASCII大小写、UTF-8/UTF-16 BOM与规范路径拒绝已通过；来源目录精确激活的跨平台合同已注明。已增加schema9有界持久来源清单、父链与冲突失效，见[清单合同](ignore-manifest.md)；最終復核、seal與正式worker已接線（PR19–25），API明確提供caseMode；跨情境完整驗收仍待完成。[3D1A证据](ignore-matcher-verification.md)；3D1B增加两平台严格no-follow、身份和完整内容复查，[实际证据](ignore-source-verification.md) | `760e02f299ad11d04219fd19fd83b7c457622f94`（安全来源子集） / `9afc8f15c6ca3b190bc6eafdb7e24356ad1cd33c`（第3D1A子集；G22.2仅来源审计） |
| **G22.4** 性能：规则编译缓存（按目录 + 文件 mtime 失效）。 | internal/adapter/media/ignore/; internal/platform/ignore/ | 内存编译LRU；无DB迁移 | 冷/暖完整读取、同size/mtime中段改文、新增/删除、失败无旧决定、root/Case隔离、64项/16MiB及共享预算；正式 worker 六輪規則修改重掃已驗證，壓力長穩仍待驗收 | 部分完成：每次两轮完整读/hash和身份检查，只有完整成功观察才能发布有界编译缓存；Windows/Linux实测见[3D1B报告](ignore-source-verification.md)；正式worker已接線（PR25），六輪真實 PG／原生 worker 重掃涵蓋兩家族同 size／mtime 修改、包含覆蓋、歷史排除保留及移除規則檔；見[重掃驗收](ignore-family-rescan.md)。其他歷史格式與壓力長穩尚未完成 | `760e02f299ad11d04219fd19fd83b7c457622f94`（第3D1B子集） |
| **G22.5** 可观测：扫描报告列出被忽略条目与命中规则来源。 | internal/adapter/postgres/ignore_report.go; internal/adapter/http/ignore_openapi.go; cmd/jelee-cli/ignore_jobs.go; docs/ignore-api.md | schema9–18既有來源清單、兩家族排除與基線分類；本段未改遷移 | Plan-G22.5：HTTP→PG→正式worker→報告分頁已驗證；live admin/session、相對路徑、規則行、unknown與深頁generic/custom索引計畫已通過；合併模式報告專項8項真PG race通過，含133筆分頁與兩萬筆generic/custom索引；規則修改後的報告來源已由六輪重掃驗收；其他舊格式與壓力長穩仍待驗收 | 部分完成：原模式與合併模式報告列出scan排除與baseline三態、規則家族及命中來源；空白/無效來源省略行號，unknown不歸屬規則；API/CLI有界分頁；[使用說明](ignore-api.md)。尚不能代表舊格式與完整G22驗收 | 本API/報告階段提交；正式worker基底73df751081（PR25） |

## G23 观看统计

- 验收：并发进度上报不造成写放大（有 QPS/写入次数证据）；统计口径文档化并有单元测试；清除与导出测试通过。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G23.1** 采集：播放会话（开始/暂停/恢复/停止/失败）、客户端、设备、条目、版本、音轨/字幕、投递方式（direct/remux）、失败原因（含 `transcode_disabled`、`codec_unsupported`、`client_blocked`、`permission_denied`）。 | internal/app/playback/; internal/adapter/postgres/; web/src/features/stats/ | playback_sessions/playback_progress/watch_stats | Plan-G23.1：节流去重批量写 QPS；观看时长口径；清除与导出；本人/管理员隔离；汇总查询计划；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G23.2** 写入：进度事件客户端节流 + 服务端批量写入（缓冲与 flush 间隔）；会话 ID 去重；断连后会话超时关闭。 | internal/app/playback/; internal/adapter/postgres/; web/src/features/stats/ | playback_sessions/playback_progress/watch_stats | Plan-G23.2：节流去重批量写 QPS；观看时长口径；清除与导出；本人/管理员隔离；汇总查询计划；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G23.3** 指标：有效观看时长（扣除快进/空转，规则可配）、完成率、续播点、观看次数、首播/重看、按日/周/月/年、按库/类型/条目、Top N。 | internal/app/playback/; internal/adapter/postgres/; web/src/features/stats/ | playback_sessions/playback_progress/watch_stats | Plan-G23.3：节流去重批量写 QPS；观看时长口径；清除与导出；本人/管理员隔离；汇总查询计划；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G23.4** 隐私：保留期可配、用户可清除自身历史、管理员可导出聚合数据；个人统计仅本人/管理员可见。 | internal/app/playback/; internal/adapter/postgres/; web/src/features/stats/ | playback_sessions/playback_progress/watch_stats | Plan-G23.4：节流去重批量写 QPS；观看时长口径；清除与导出；本人/管理员隔离；汇总查询计划；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G23.5** 性能：聚合走 SQL 汇总或物化视图；避免每次请求全表扫描。 | internal/app/playback/; internal/adapter/postgres/; web/src/features/stats/ | playback_sessions/playback_progress/watch_stats | Plan-G23.5：节流去重批量写 QPS；观看时长口径；清除与导出；本人/管理员隔离；汇总查询计划；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G24 第三方客户端兼容

- 验收：黄金文件对比零差异；客户端可登录、浏览、搜索、查看详情、起播、上报进度；不支持能力探测不报错。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G24.1** 兼容矩阵：`docs/compat-matrix.md` 列出目标客户端与所需接口、能力声明、已知限制。 | internal/adapter/compat/; docs/compat-matrix.md; tests/contracts/ | 使用原生 users/items/sessions 映射；禁止旧类型入 domain | Plan-G24.1：黄金 JSON 合同；≥3 类真实客户端版本/步骤/截图；登录至进度端到端；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G24.2** 覆盖：认证（含 API Key/Header 形态）、用户、系统公开信息、媒体库、Items 查询与过滤、详情、图片、播放信息（仅直投信息）、会话、进度上报、收藏、字幕/音轨选择、视频流（Range/Remux）、能力协商（明确 transcoding 与 HLS 不支持）。 | internal/adapter/compat/; docs/compat-matrix.md; tests/contracts/ | 使用原生 users/items/sessions 映射；禁止旧类型入 domain | Plan-G24.2：黄金 JSON 合同；≥3 类真实客户端版本/步骤/截图；登录至进度端到端；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G24.3** 实现：兼容 DTO 与路由独立目录；Adapter/Mapper 单向转换；旧 JSON 字段、ID 形态、大小写与必要请求头保持一致。 | internal/adapter/compat/; docs/compat-matrix.md; tests/contracts/ | 使用原生 users/items/sessions 映射；禁止旧类型入 domain | Plan-G24.3：黄金 JSON 合同；≥3 类真实客户端版本/步骤/截图；登录至进度端到端；须单独覆盖本行全部约束 | 部分完成：当前新领域不导入旧品牌对象；实际兼容 DTO、路由与 Mapper 尚未实现；[实际证据](verification-report.md) | `c77863e445`（仅新领域边界；兼容 Adapter 未实现） |
| **G24.4** 移除能力：DLNA/直播/下载/音乐等通过能力声明隐藏；探测接口返回明确不支持而非 500。 | internal/adapter/compat/; docs/compat-matrix.md; tests/contracts/ | 使用原生 users/items/sessions 映射；禁止旧类型入 domain | Plan-G24.4：黄金 JSON 合同；≥3 类真实客户端版本/步骤/截图；登录至进度端到端；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G24.5** 测试：契约测试（黄金 JSON 文件）+ 至少三类真实客户端手动验证记录（版本、步骤、截图/日志）。 | internal/adapter/compat/; docs/compat-matrix.md; tests/contracts/ | 使用原生 users/items/sessions 映射；禁止旧类型入 domain | Plan-G24.5：黄金 JSON 合同；≥3 类真实客户端版本/步骤/截图；登录至进度端到端；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G25 自有接口性能优化

- 验收：1 万条数据下热路径 P95 ≤ 200ms；单请求 SQL 语句计数断言纳入测试。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G25.1** 查询：投影查询、批量加载（dataloader 或显式 batch）、避免 N+1、稳定分页键。 | internal/adapter/postgres/; internal/adapter/http/; internal/platform/cache/ | 热查询索引与分页键 | Plan-G25.1：1 万元数据热路径 P95≤200ms；SQL 次数；ETag/缓存用户隔离；取消和集合上限；须单独覆盖本行全部约束 | 部分完成：列表已分 admin/ACL 分支并做稳定有界索引分页；其他热查询/批量加载未实现；[实际证据](verification-report.md) | `632005d430` / `403cc21b27`（仅已实现子集） |
| **G25.2** 传输：轻量 DTO、字段选择、gzip/brotli 仅用于非媒体响应、ETag/`If-None-Match`、Cache-Control。 | internal/adapter/postgres/; internal/adapter/http/; internal/platform/cache/ | 热查询索引与分页键 | Plan-G25.2：1 万元数据热路径 P95≤200ms；SQL 次数；ETag/缓存用户隔离；取消和集合上限；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G25.3** 缓存：热点只读数据缓存 + 失效策略；用户态数据不共享缓存。 | internal/adapter/postgres/; internal/adapter/http/; internal/platform/cache/ | 热查询索引与分页键 | Plan-G25.3：1 万元数据热路径 P95≤200ms；SQL 次数；ETag/缓存用户隔离；取消和集合上限；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G25.4** 约束：禁止无界集合返回（默认上限 + 显式分页）、禁止循环内访问 DB、禁止先全表加载再内存过滤。 | internal/adapter/postgres/; internal/adapter/http/; internal/platform/cache/ | 热查询索引与分页键 | Plan-G25.4：1 万元数据热路径 P95≤200ms；SQL 次数；ETag/缓存用户隔离；取消和集合上限；须单独覆盖本行全部约束 | 部分完成：当前 catalog 限 1..100 且 ACL 在 SQL 执行，10,002 条样本实际仅访问 1 条；全部 API 还未实现；[实际证据](verification-report.md) | `632005d430` / `403cc21b27`（仅已实现子集） |
| **G25.5** 取消：请求取消传播到 DB 查询与子进程。 | internal/adapter/postgres/; internal/adapter/http/; internal/platform/cache/ | 热查询索引与分页键 | Plan-G25.5：1 万元数据热路径 P95≤200ms；SQL 次数；ETag/缓存用户隔离；取消和集合上限；须单独覆盖本行全部约束 | 部分完成：目录 DB 取消和媒体 lookup 独立期限已测；外部工具取消尚未实现；[实际证据](verification-report.md) | `632005d430` / `403cc21b27`（仅已实现子集） |

## G26 CPU 性能优化

- 验收：典型非转码负载 CPU 不高于基线，目标降低 ≥20%；所有优化有可复现命令与数据。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G26.1** 方法：先建基线（扫描、探测、直投流式、API、序列化、DB、NFO 解析、图片处理、权限判定），再用 pprof/trace 定位，禁止凭直觉优化。 | internal/diag/; tests/bench/; docs/perf-baseline.md; docs/perf-report.md | 仅按实测添加索引 | Plan-G26.1：同机同集 CPU profile+benchstat 对比；典型负载不高于基线、目标降低≥20%；正确性不回退；须单独覆盖本行全部约束 | 部分完成：保留 SQL 修正前后 EXPLAIN 与媒体微基准；扫描/探测/NFO/图片/完整 pprof 基线缺失；[实际证据](verification-report.md) | `632005d430` / `c77863e445`（仅已实现子集） |
| **G26.2** 重点：重复探测、重复哈希、正则回溯、对象分配、JSON 反序列化、锁竞争、goroutine 调度、无界并发、字符串拼接、高频 time/rand 调用。 | internal/diag/; tests/bench/; docs/perf-baseline.md; docs/perf-report.md | 仅按实测添加索引 | Plan-G26.2：同机同集 CPU profile+benchstat 对比；典型负载不高于基线、目标降低≥20%；正确性不回退；须单独覆盖本行全部约束 | 部分完成：修正权限查询扫描整个隐藏大库的问题；其他 CPU 热点须真实工作负载再诊断；[实际证据](verification-report.md) | `632005d430` / `c77863e445`（仅已实现子集） |
| **G26.3** 手段：`sync.Pool`、预分配切片、编译期正则、流式解析、批处理、索引优化、并发上限、避免热路径反射。 | internal/diag/; tests/bench/; docs/perf-baseline.md; docs/perf-report.md | 仅按实测添加索引 | Plan-G26.3：同机同集 CPU profile+benchstat 对比；典型负载不高于基线、目标降低≥20%；正确性不回退；须单独覆盖本行全部约束 | 部分完成：已使用 per-library 有界索引分页与流并发上限；其他优化手段未以 profile 证明；[实际证据](verification-report.md) | `632005d430` / `c77863e445`（仅已实现子集） |
| **G26.4** 门禁：每个优化提交附 benchstat 前后数据；关键基准回归超过阈值即 CI 失败。 | internal/diag/; tests/bench/; docs/perf-baseline.md; docs/perf-report.md | 仅按实测添加索引 | Plan-G26.4：同机同集 CPU profile+benchstat 对比；典型负载不高于基线、目标降低≥20%；正确性不回退；须单独覆盖本行全部约束 | 部分完成：实际 SQL 访问行数有回归断言；统一 benchstat/性能阈值 CI 门禁尚未完成；[实际证据](verification-report.md) | `632005d430` / `c77863e445`（仅已实现子集） |
| **G26.5** 红线：不得牺牲正确性、画质、安全换取性能数字。 | internal/diag/; tests/bench/; docs/perf-baseline.md; docs/perf-report.md | 仅按实测添加索引 | Plan-G26.5：同机同集 CPU profile+benchstat 对比；典型负载不高于基线、目标降低≥20%；正确性不回退；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G27 网页端去播放化

- 验收：Web 端全量点击遍历无播放入口；Web 客户端调用播放接口被拒且有清晰提示；第三方客户端仍可播放。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G27.1** 前端删除：播放器组件、播放路由、播放按钮/入口、播放状态机、媒体会话、HLS/DASH 播放依赖、画中画、投屏、播放快捷键与相关翻译键/资源。 | web/src/; web/package.json; internal/access/; internal/adapter/http/ | sessions 的服务端客户端类型与播放资格 | Plan-G27.1：静态无播放依赖/入口；E2E 全点击；Web 会话换 UA 仍拒绝；第三方授权直投；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G27.2** 保留：登录、浏览、搜索、详情、图片、观看记录/统计、个人资料、安全与偏好设置；管理员的用户、媒体库、任务、Webhook、NFO/图片策略、系统页面。 | web/src/; web/package.json; internal/access/; internal/adapter/http/ | sessions 的服务端客户端类型与播放资格 | Plan-G27.2：静态无播放依赖/入口；E2E 全点击；Web 会话换 UA 仍拒绝；第三方授权直投；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G27.3** 服务端：播放/流接口保留给授权第三方客户端；Web 会话不得具备播放能力（服务端显式拒绝来自 Web 客户端的播放请求）。 | web/src/; web/package.json; internal/access/; internal/adapter/http/ | sessions 的服务端客户端类型与播放资格 | Plan-G27.3：静态无播放依赖/入口；E2E 全点击；Web 会话换 UA 仍拒绝；第三方授权直投；须单独覆盖本行全部约束 | 部分完成：服务器持久化会话类型，Web 伪造 native UA/头仍 403，native 可原字节传输；完整第三方兼容未完成；[实际证据](verification-report.md) | `c77863e445` / `632005d430` / `403cc21b27`（仅已实现子集） |
| **G27.4** 门禁：前端构建期断言无播放依赖与播放路由；E2E 断言 Web 端无播放入口。 | web/src/; web/package.json; internal/access/; internal/adapter/http/ | sessions 的服务端客户端类型与播放资格 | Plan-G27.4：静态无播放依赖/入口；E2E 全点击；Web 会话换 UA 仍拒绝；第三方授权直投；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G28 Go 服务端重写

- 验收：`go build ./...` 通过；架构测试通过；诊断命令输出版本/工具/DB/配置/隐私自检结果。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G28.1** 边界：Go 承载 HTTP 服务、媒体扫描、NFO、元数据、图片资产、用户权限、库访问管控、任务调度、事件与 Webhook、播放信息、直投流式、字幕/音轨、DB 访问、CLI 与诊断；C#/.NET 仅作迁移期参考或兼容旁路。 | cmd/jelee/; cmd/jelee-cli/; cmd/jelee-migrate/; internal/; docs/architecture.md | 由 G04/G36 管理 | Plan-G28.1：go build；依赖方向架构检查；fx 生命周期与取消；每阶段开关/合同/回滚实测；须单独覆盖本行全部约束 | 部分完成：Go 已承载基础 HTTP/目录/ACL/直投/迁移/CLI；扫描/NFO/元数据/任务等余下域未完成；[实际证据](verification-report.md) | `c77863e445` / `632005d430` / `403cc21b27`（仅已实现子集） |
| **G28.2** 分层：`domain`（纯领域，无 I/O）→ `app`（用例，依赖接口）→ `adapter`（HTTP/DB/NFO/media/images/compat）→ `platform`；依赖方向单向，架构测试强制。 | cmd/jelee/; cmd/jelee-cli/; cmd/jelee-migrate/; internal/; docs/architecture.md | 由 G04/G36 管理 | Plan-G28.2：go build；依赖方向架构检查；fx 生命周期与取消；每阶段开关/合同/回滚实测；须单独覆盖本行全部约束 | 部分完成：分层目录与架构测试通过；余下域尚未接管，不能称完成整个重写；[实际证据](verification-report.md) | `c77863e445` / `632005d430` / `403cc21b27`（仅已实现子集） |
| **G28.3** HTTP：chi 路由；中间件统一；JSON 编解码明确；响应流式化避免大对象全量入内存。 | cmd/jelee/; cmd/jelee-cli/; cmd/jelee-migrate/; internal/; docs/architecture.md | 由 G04/G36 管理 | Plan-G28.3：go build；依赖方向架构检查；fx 生命周期与取消；每阶段开关/合同/回滚实测；须单独覆盖本行全部约束 | 部分完成：chi 与安全 JSON/error/流响应已实测；完整业务中间件/接口集合未完成；[实际证据](verification-report.md) | `c77863e445` / `632005d430` / `403cc21b27`（仅已实现子集） |
| **G28.4** 配置与启动：Wire/fx 组装；优雅启动（迁移可选、工具探测、初始化判断）与优雅关闭（draining、停任务、终止子进程）。 | cmd/jelee/; cmd/jelee-cli/; cmd/jelee-migrate/; internal/; docs/architecture.md | 由 G04/G36 管理 | Plan-G28.4：go build；依赖方向架构检查；fx 生命周期与取消；每阶段开关/合同/回滚实测；须单独覆盖本行全部约束 | 部分完成：fx/schema5启动检查、工具健康与能力降级、worker/维护取消join、真HTTP及带child lease的SIGTERM后零租约通过，见[worker验证](probe-worker-verification.md)；后续业务组件的完整生命周期仍待交付 | `c77863e445` / `632005d430` / `403cc21b27`（仅已实现子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） |
| **G28.5** CLI：`jelee`（serve）、`jelee-migrate`（up/down/status/verify）、`jelee-cli`（用户管理、扫描触发、NFO 导入/导出/校验、图片重建、缓存清理、诊断报告）。 | cmd/jelee/; cmd/jelee-cli/; cmd/jelee-migrate/; internal/; docs/architecture.md | 由 G04/G36 管理 | Plan-G28.5：go build；依赖方向架构检查；fx 生命周期与取消；每阶段开关/合同/回滚实测；须单独覆盖本行全部约束 | 部分完成：三个Go命令可构建；现有账户/盘点CLI及新增probe扫描、库/item重建、摘要和保留意图重试已验证，见[worker验证](probe-worker-verification.md)；图片重建、完整NFO/诊断和其他CLI仍待交付 | `c77863e445` / `632005d430` / `403cc21b27`（仅已实现子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） |
| **G28.6** 迁移：Strangler Fig 阶段化，每阶段有开关 + 契约测试 + 回滚路径。 | cmd/jelee/; cmd/jelee-cli/; cmd/jelee-migrate/; internal/; docs/architecture.md | 由 G04/G36 管理 | Plan-G28.6：go build；依赖方向架构检查；fx 生命周期与取消；每阶段开关/合同/回滚实测；须单独覆盖本行全部约束 | 部分完成：渐进替换、目录/直投默认关闭开关及回滚文档已落地；所有后续阶段合同与回滚尚未完成；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（设计与当前开关）；后续阶段未实现 |

## G29 Go 流式与并发

- 验收：取消/断连场景下 goroutine 数回归基线；无残留子进程与临时文件；race 检测无告警。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G29.1** 流式：`io.Copy`/`io.CopyN` + 显式缓冲；Range/多段 Range、`If-Range`、写超时、客户端断开快速终止。 | internal/adapter/http/; internal/adapter/media/; internal/platform/workers/ | 无；任务持久化见 G41 | Plan-G29.1：Range/If-Range；race 混合并发；客户端断连后 goroutine 回归；进程树与临时文件回收；须单独覆盖本行全部约束 | 部分完成：Range/If-Range、阻塞写取消及期限实测；跨平台显式零拷贝/缓冲目标未完整验证；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G29.2** 并发：`errgroup` + 上限；worker pool 处理探测/扫描/索引构建/NFO/图片/Remux（开发者模式）；channel 必须设容量或明确无界理由 + 背压。 | internal/adapter/http/; internal/adapter/media/; internal/platform/workers/ | 无；任务持久化见 G41 | Plan-G29.2：Range/If-Range；race 混合并发；客户端断连后 goroutine 回归；进程树与临时文件回收；须单独覆盖本行全部约束 | 部分完成：有界盘点 worker/HTTP 准入/队列和同步批次背压；未来 probe/索引/写回 worker 待交付；[第3A实际证据](jobs-verification.md) | `45d3fbfcb8`（本段已实现子集） |
| **G29.3** 生命周期：所有 goroutine 绑定 context；禁止泄漏、无超时锁、忙等；提供泄漏检测测试。 | internal/adapter/http/; internal/adapter/media/; internal/platform/workers/ | 无；任务持久化见 G41 | Plan-G29.3：Range/If-Range；race 混合并发；客户端断连后 goroutine 回归；进程树与临时文件回收；须单独覆盖本行全部约束 | 部分完成：HTTP/direct 与盘点生命周期、timer/heartbeat join、Fx 停止逾时先取消有 race 回归；底层 FS 硬中断及所有未来 goroutine 待验收；[第3A实际证据](jobs-verification.md) | `c77863e445` / `403cc21b27`（仅已实现子集）；`45d3fbfcb8`（第3A子集） |
| **G29.4** 子进程：进程组管理与终止；stdout/stderr 限流读取；僵尸进程回收；临时文件清理（含崩溃清理）。 | internal/adapter/http/; internal/adapter/media/; internal/platform/workers/ | 无；任务持久化见 G41 | Plan-G29.4：Range/If-Range；race 混合并发；客户端断连后 goroutine 回归；进程树与临时文件回收；须单独覆盖本行全部约束 | 部分完成：双平台子孙终止/输出限额/回收/取消清理和资源稳定实测通过；崩溃清理与不可中断OS调用限制仍在，见[3B2验证](process-verification.md) | `03a559524c`（第3B2子集） |
| **G29.5** 测试：`go test -race` 覆盖流式、取消、并发扫描、NFO 并发写、并发直投与客户端断连；压力测试观察 goroutine 数稳定。 | internal/adapter/http/; internal/adapter/media/; internal/platform/workers/ | 无；任务持久化见 G41 | Plan-G29.5：Range/If-Range；race 混合并发；客户端断连后 goroutine 回归；进程树与临时文件回收；须单独覆盖本行全部约束 | 部分完成：Linux race 覆盖当前 API/媒体/PostgreSQL；扫描/NFO混合压测缺失，Windows race/C 工具链未验收；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |

## G30 Go 工程质量门禁

- 验收：CI 全绿且可复现；覆盖率达标；容器非 root 启动并通过健康检查。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G30.1** 工具链：`gofmt`、`go vet`、`golangci-lint`（errcheck、staticcheck、govet、revive、gosec、bodyclose、contextcheck）、`go test -race`、覆盖率门槛（核心包 ≥70%，关键包 ≥85%）。 | Makefile; scripts/make.ps1; .golangci.yml; .github/workflows/; Dockerfile | 无 | Plan-G30.1：fmt/vet/lint/race；核心≥70%、关键≥85%覆盖率；非 root 容器健康检查；工具来源门禁；须单独覆盖本行全部约束 | 部分完成：fmt/vet/Linux race 与逐包覆盖率已记录；golangci 全 linter、安全工具与全部覆盖阈值未满足；[实际证据](verification-report.md) | `721102c8d0` / `c77863e445` / `403cc21b27` / `0bbd5939bb`（仅已实现子集） |
| **G30.2** 规范：错误包装统一、日志字段统一（traceId/userId/itemId）、panic 恢复仅限边界并记录、资源 defer 释放、time/clock 可注入。 | Makefile; scripts/make.ps1; .golangci.yml; .github/workflows/; Dockerfile | 无 | Plan-G30.2：fmt/vet/lint/race；核心≥70%、关键≥85%覆盖率；非 root 容器健康检查；工具来源门禁；须单独覆盖本行全部约束 | 部分完成：当前安全错误包装、边界 panic 恢复与资源释放已测；完整统一上下文字段/注入时钟未完成；[实际证据](verification-report.md) | `721102c8d0` / `c77863e445` / `403cc21b27` / `0bbd5939bb`（仅已实现子集） |
| **G30.3** 运行：liveness/readiness、优雅关闭、信号处理、`/metrics`、`/healthz`。 | Makefile; scripts/make.ps1; .golangci.yml; .github/workflows/; Dockerfile | 无 | Plan-G30.3：fmt/vet/lint/race；核心≥70%、关键≥85%覆盖率；非 root 容器健康检查；工具来源门禁；须单独覆盖本行全部约束 | 部分完成：healthz/readyz 与 SIGTERM 排空已实测；Prometheus /metrics 尚未实现；[实际证据](verification-report.md) | `721102c8d0` / `c77863e445` / `403cc21b27` / `0bbd5939bb`（仅已实现子集） |
| **G30.4** 交付：`Makefile` 统一入口（`bootstrap`、`tools-verify`、`tools-clean`、`fixtures`、`gitignore-check`、`dev`、`build`、`test`、`test-integration`、`lint`、`fmt`、`bench`、`migrate`、`brand-scan`、`nfo`、`doctor`、`diag`）；Makefile 内所有工具调用优先使用 `.bin/` 本地固定版本（`PATH := $(CURDIR)/.bin:$(PATH)`）；Windows 提供等价 PowerShell 入口（G51.13）；Dockerfile 多阶段构建，非 root 运行。 | Makefile; scripts/make.ps1; .golangci.yml; .github/workflows/; Dockerfile | 无 | Plan-G30.4：fmt/vet/lint/race；核心≥70%、关键≥85%覆盖率；非 root 容器健康检查；工具来源门禁；须单独覆盖本行全部约束 | 部分完成：主要 Make/PowerShell 构建测试入口和多阶段 Dockerfile 已提供；全部 nfo/diag/bench/fixtures 等功能入口未完成；[实际证据](verification-report.md) | `721102c8d0` / `c77863e445` / `403cc21b27` / `0bbd5939bb`（仅已实现子集） |
| **G30.6** 工具来源纪律：禁止在测试或构建中依赖未纳入 `tools/manifest.toml` 的工具；禁止写入用户全局环境；新工具必须先入清单再使用（G51）。 | Makefile; scripts/make.ps1; .golangci.yml; .github/workflows/; Dockerfile | 无 | Plan-G30.6：fmt/vet/lint/race；核心≥70%、关键≥85%覆盖率；非 root 容器健康检查；工具来源门禁；须单独覆盖本行全部约束 | 部分完成：Go 清单与项目缓存来源已固定；全部构建/测试工具清单化范围仍未完成；[实际证据](verification-report.md) | `721102c8d0` / `c77863e445` / `403cc21b27` / `0bbd5939bb`（仅已实现子集） |
| **G30.5** 禁止：不得通过关闭 lint、跳过测试、降低安全设置让 CI 通过。 | Makefile; scripts/make.ps1; .golangci.yml; .github/workflows/; Dockerfile | 无 | Plan-G30.5：fmt/vet/lint/race；核心≥70%、关键≥85%覆盖率；非 root 容器健康检查；工具来源门禁；须单独覆盖本行全部约束 | 部分完成：保留全品牌失败、覆盖率不足和跳过事实；完整 CI 质量门禁尚未实现，未冒充通过；[实际证据](verification-report.md) | `721102c8d0` / `c77863e445` / `403cc21b27` / `0bbd5939bb`（仅已实现子集） |

## G31 前端技术选型与架构

- 验收：`tsc --noEmit` 与 lint 零错误；路由权限正确；OpenAPI 类型同步检查通过。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G31.1** 选型：Vite + TypeScript strict；框架 Vue 3 或 Svelte（二选一并在 `docs/frontend-adr.md` 记录理由）；Pinia 或等价状态管理。 | web/package.json; web/tsconfig.json; web/src/; docs/frontend-adr.md | 用户 UI 状态/偏好 | Plan-G31.1：tsc strict/lint；路由权限/深链；OpenAPI 类型同步；请求状态与乐观回滚；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G31.2** 分层：`api/`、`stores/`、`features/<domain>/`、`components/ui/`、`theme/`、`plugins/`、`i18n/`、`router/`；业务不散落在组件内。 | web/package.json; web/tsconfig.json; web/src/; docs/frontend-adr.md | 用户 UI 状态/偏好 | Plan-G31.2：tsc strict/lint；路由权限/深链；OpenAPI 类型同步；请求状态与乐观回滚；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G31.3** 路由：懒加载、守卫、服务端权限校验、404/403、深链可用。 | web/package.json; web/tsconfig.json; web/src/; docs/frontend-adr.md | 用户 UI 状态/偏好 | Plan-G31.3：tsc strict/lint；路由权限/深链；OpenAPI 类型同步；请求状态与乐观回滚；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G31.4** 类型：由 OpenAPI 生成 TS 类型，CI 校验过期；禁止 `any` 泛滥。 | web/package.json; web/tsconfig.json; web/src/; docs/frontend-adr.md | 用户 UI 状态/偏好 | Plan-G31.4：tsc strict/lint；路由权限/深链；OpenAPI 类型同步；请求状态与乐观回滚；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G31.5** 状态：请求状态机（idle/loading/success/error）、错误统一处理、乐观更新可回滚。 | web/package.json; web/tsconfig.json; web/src/; docs/frontend-adr.md | 用户 UI 状态/偏好 | Plan-G31.5：tsc strict/lint；路由权限/深链；OpenAPI 类型同步；请求状态与乐观回滚；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G32 前端可扩展性（插件体系）

- 验收：故意抛错插件不影响主应用；启停即时生效；manifest 非法被拒绝且有清晰错误。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G32.1** SDK：`@jelee/plugin-sdk` 暴露类型化 Hook：`media.detail.tabs`、`item.action`、`settings.section`、`library.toolbar`、`theme.token`、`route.register`、`command.palette`、`webhook.eventType`、`metadata.panel`。 | web/src/plugins/; web/packages/plugin-sdk/; docs/plugins.md | plugin_configs | Plan-G32.1：错误插件隔离；manifest 版本/权限拒绝；启停排序即时；两个真实样例插件；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G32.2** Manifest：含 id、name、version、sdkVersion（范围）、权限声明、依赖、入口、minimal Jelee 版本；加载前校验，不兼容则拒绝并提示。 | web/src/plugins/; web/packages/plugin-sdk/; docs/plugins.md | plugin_configs | Plan-G32.2：错误插件隔离；manifest 版本/权限拒绝；启停排序即时；两个真实样例插件；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G32.3** 隔离：插件组件懒加载；单插件渲染错误被 ErrorBoundary 捕获并降级，不得白屏；插件不可直接访问令牌或绕过权限。 | web/src/plugins/; web/packages/plugin-sdk/; docs/plugins.md | plugin_configs | Plan-G32.3：错误插件隔离；manifest 版本/权限拒绝；启停排序即时；两个真实样例插件；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G32.4** 管理：管理员可启用/禁用/排序/查看插件信息与权限；插件设置独立存储命名空间。 | web/src/plugins/; web/packages/plugin-sdk/; docs/plugins.md | plugin_configs | Plan-G32.4：错误插件隔离；manifest 版本/权限拒绝；启停排序即时；两个真实样例插件；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G32.5** 内置样例：提供 2 个官方示例插件与插件开发文档。 | web/src/plugins/; web/packages/plugin-sdk/; docs/plugins.md | plugin_configs | Plan-G32.5：错误插件隔离；manifest 版本/权限拒绝；启停排序即时；两个真实样例插件；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G32.6** 版本：SDK 语义化版本与弃用策略；破坏性变更需迁移说明。 | web/src/plugins/; web/packages/plugin-sdk/; docs/plugins.md | plugin_configs | Plan-G32.6：错误插件隔离；manifest 版本/权限拒绝；启停排序即时；两个真实样例插件；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G33 前端可定制性（主题与布局）

- 验收：主题切换无闪烁与布局跳动；自定义 CSS XSS 样例被拦截；导入导出往返一致。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G33.1** Token：设计 token 单一来源（颜色、间距、圆角、阴影、字号、层级、动效时长），编译为 CSS 变量，运行时可覆盖。 | web/src/theme/; web/src/features/settings/; internal/adapter/http/ | theme_configs | Plan-G33.1：三套主题亮暗/system；CSS XSS 防护；配置 JSON 往返；触控目标与对比度；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G33.2** 主题：light/dark/system + 至少 3 套预设；管理员可配主色、强调色、圆角、密度、字体、背景、海报比例、列表密度、卡片/列表视图、首页版块。 | web/src/theme/; web/src/features/settings/; internal/adapter/http/ | theme_configs | Plan-G33.2：三套主题亮暗/system；CSS XSS 防护；配置 JSON 往返；触控目标与对比度；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G33.3** 持久化：用户级与全局级配置分别存服务端；支持导入/导出 JSON；可重置默认。 | web/src/theme/; web/src/features/settings/; internal/adapter/http/ | theme_configs | Plan-G33.3：三套主题亮暗/system；CSS XSS 防护；配置 JSON 往返；触控目标与对比度；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G33.4** 自定义 CSS：允许管理员注入，必须转义与限制（禁止 `<script>`、`expression`、`javascript:`、外部字体默认禁用可开白名单），防 XSS。 | web/src/theme/; web/src/features/settings/; internal/adapter/http/ | theme_configs | Plan-G33.4：三套主题亮暗/system；CSS XSS 防护；配置 JSON 往返；触控目标与对比度；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G33.5** 布局：首页版块拖拽排序与显隐；详情页面板可配置；布局预设保存/切换。 | web/src/theme/; web/src/features/settings/; internal/adapter/http/ | theme_configs | Plan-G33.5：三套主题亮暗/system；CSS XSS 防护；配置 JSON 往返；触控目标与对比度；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G33.6** 可访问性：主题切换保证对比度达标；高密度模式不破坏触控目标尺寸。 | web/src/theme/; web/src/features/settings/; internal/adapter/http/ | theme_configs | Plan-G33.6：三套主题亮暗/system；CSS XSS 防护；配置 JSON 往返；触控目标与对比度；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G34 前端美观与体验

- 验收：axe 扫描零严重问题；视觉回归基线通过；关键页面亮暗截图入档。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G34.1** 设计基础：4/8pt 栅格、排版层级、色彩语义、间距节奏、统一圆角与阴影层级。 | web/src/components/ui/; web/src/features/; web/tests/visual/ | 无 | Plan-G34.1：响应式/键盘/ARIA/axe 无严重问题；亮暗截图；减少动效；人工视觉确认；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G34.2** 组件库：按钮、输入、选择器、弹层、抽屉、表格、标签、头像、评分、进度、骨架屏、空状态、错误态、Toast、Tooltip、分页、虚拟列表。 | web/src/components/ui/; web/src/features/; web/tests/visual/ | 无 | Plan-G34.2：响应式/键盘/ARIA/axe 无严重问题；亮暗截图；减少动效；人工视觉确认；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G34.3** 页面：海报墙（多尺寸/悬停信息）、列表视图、详情页（演职员、版本、字幕音轨、章节、文件信息、NFO 来源标记）、搜索（筛选/排序/即时反馈）、设置页分区。 | web/src/components/ui/; web/src/features/; web/tests/visual/ | 无 | Plan-G34.3：响应式/键盘/ARIA/axe 无严重问题；亮暗截图；减少动效；人工视觉确认；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G34.4** 动效：默认 ≤250ms、统一缓动、尊重 `prefers-reduced-motion`；骨架屏避免布局抖动；避免滥用毛玻璃影响性能。 | web/src/components/ui/; web/src/features/; web/tests/visual/ | 无 | Plan-G34.4：响应式/键盘/ARIA/axe 无严重问题；亮暗截图；减少动效；人工视觉确认；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G34.5** 响应式与可达性：移动/平板/桌面断点；键盘导航、焦点可见、ARIA、对比度达 WCAG 2.1 AA。 | web/src/components/ui/; web/src/features/; web/tests/visual/ | 无 | Plan-G34.5：响应式/键盘/ARIA/axe 无严重问题；亮暗截图；减少动效；人工视觉确认；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G34.6** 视觉回归：关键页面亮/暗截图对比 + Playwright 视觉回归；变更需人工确认。 | web/src/components/ui/; web/src/features/; web/tests/visual/ | 无 | Plan-G34.6：响应式/键盘/ARIA/axe 无严重问题；亮暗截图；减少动效；人工视觉确认；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G35 前端安全与性能

- 验收：依赖审计无高危；bundle 预算门禁通过；XSS/CSRF 样例测试通过。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G35.1** 安全：用户内容转义（禁止 `v-html`/等价除非白名单 sanitize）；令牌仅 httpOnly Cookie 或受控内存，禁止 localStorage 存令牌；CSRF 防护；CSP；依赖漏洞扫描。 | web/src/; web/package.json; internal/adapter/http/; .github/workflows/ | 无 | Plan-G35.1：XSS/CSRF 样例与依赖漏洞；token 不进 localStorage；bundle gzip 预算；TTI P95≤1.5s；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G35.2** 权限：路由守卫仅为体验，真校验在服务端；管理入口按权限渲染且服务端二次校验。 | web/src/; web/package.json; internal/adapter/http/; .github/workflows/ | 无 | Plan-G35.2：XSS/CSRF 样例与依赖漏洞；token 不进 localStorage；bundle gzip 预算；TTI P95≤1.5s；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G35.3** 性能：代码分割、路由懒加载、虚拟列表、图片懒加载与尺寸适配（配合 G40 图片服务）、请求去重与缓存。 | web/src/; web/package.json; internal/adapter/http/; .github/workflows/ | 无 | Plan-G35.3：XSS/CSRF 样例与依赖漏洞；token 不进 localStorage；bundle gzip 预算；TTI P95≤1.5s；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G35.4** 预算：主 bundle gzip 体积预算 CI 断言；首屏可交互 P95 ≤1.5s。 | web/src/; web/package.json; internal/adapter/http/; .github/workflows/ | 无 | Plan-G35.4：XSS/CSRF 样例与依赖漏洞；token 不进 localStorage；bundle gzip 预算；TTI P95≤1.5s；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G35.5** 禁项：不得引入会拉回播放能力的依赖。 | web/src/; web/package.json; internal/adapter/http/; .github/workflows/ | 无 | Plan-G35.5：XSS/CSRF 样例与依赖漏洞；token 不进 localStorage；bundle gzip 预算；TTI P95≤1.5s；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G36 数据库设计与迁移工程

- 验收：迁移 up/down/up 往返成功；模拟 100 万条 items 的查询计划入档；备份恢复演练成功。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G36.1** Schema：完整 ER 设计文档，覆盖 users、sessions、devices、client_policies、client_blocks、library_acl、library_acl_overrides、libraries、library_roots、items、item_versions、media_sources、media_streams、chapters、people、genres、tags、studios、collections、playlists、user_data、playback_sessions、playback_progress、watch_stats、nfo_documents、nfo_field_locks、images、image_variants、metadata_providers、metadata_locks、probe_cache、transcode_jobs、jobs、job_runs、job_locks、webhooks、webhook_deliveries、audit_logs、settings、plugin_configs、theme_configs、schema_migrations。 | internal/adapter/postgres/; cmd/jelee-migrate/; cmd/jelee-cli/; docs/domain-model.md; docs/backup-restore.md | 完整 schema ER、不可变 up/down、数据生命周期 | Plan-G36.1：up/down/up；100 万 items EXPLAIN；真实备份恢复演练；生产无转码任务路径；须单独覆盖本行全部约束 | 部分完成：schema 1/2 加不可改写的 000003 任务/盘点/基准；完整领域表待交付；[第3A实际证据](jobs-verification.md) | `632005d430`（仅已实现子集）；`45d3fbfcb8`（第3A子集） |
| **G36.2** 索引策略：列出每条热查询与其索引；全表扫描必须有理由与数据量上限。 | internal/adapter/postgres/; cmd/jelee-migrate/; cmd/jelee-cli/; docs/domain-model.md; docs/backup-restore.md | 完整 schema ER、不可变 up/down、数据生命周期 | Plan-G36.2：up/down/up；100 万 items EXPLAIN；真实备份恢复演练；生产无转码任务路径；须单独覆盖本行全部约束 | 部分完成：PG 参数化短交易、任务租约及批次回滚；完整领域约束与大规模性能仍待验收；[第3A实际证据](jobs-verification.md) | `632005d430`（仅已实现子集）；`45d3fbfcb8`（第3A子集） |
| **G36.3** 数据生命周期：播放历史保留期、探测与索引缓存清理、提取字幕清理、图片变体清理、日志轮转与归档、审计归档策略与任务。 | internal/adapter/postgres/; cmd/jelee-migrate/; cmd/jelee-cli/; docs/domain-model.md; docs/backup-restore.md | 完整 schema ER、不可变 up/down、数据生命周期 | Plan-G36.3：up/down/up；100 万 items EXPLAIN；真实备份恢复演练；生产无转码任务路径；须单独覆盖本行全部约束 | 部分完成：探测cache每60秒进行最多128项/2秒的回收，TTL/容量淘汰、精确quota及维护停止通过真实PG/runtime测试，见[worker验证](probe-worker-verification.md)；播放历史、索引、字幕、图片和日志/审计归档任务尚未完成 | `45d3fbfcb8`（本段已实现子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） |
| **G36.4** 备份恢复：`docs/backup-restore.md` + `jelee-cli` 元数据导出/导入；备份演练记录。 | internal/adapter/postgres/; cmd/jelee-migrate/; cmd/jelee-cli/; docs/domain-model.md; docs/backup-restore.md | 完整 schema ER、不可变 up/down、数据生命周期 | Plan-G36.4：up/down/up；100 万 items EXPLAIN；真实备份恢复演练；生产无转码任务路径；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G36.5** 迁移纪律：迁移文件不可变、可回滚、向前兼容、大表变更在线策略。 | internal/adapter/postgres/; cmd/jelee-migrate/; cmd/jelee-cli/; docs/domain-model.md; docs/backup-restore.md | 完整 schema ER、不可变 up/down、数据生命周期 | Plan-G36.5：up/down/up；100 万 items EXPLAIN；真实备份恢复演练；生产无转码任务路径；须单独覆盖本行全部约束 | 部分完成：001–005原文不变，schema6 up/down/up、活动NFO阶段拒绝down、回滚保留旧数据已验证；完整生产备份、在线大表迁移与后续迁移仍待交付；[3C3B实际证据](nfo-cache-verification.md) | `632005d430`（仅已实现子集）；`45d3fbfcb8`（第3A子集） / `3d8842be1e`（第3C2A子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） / `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`（第3C3B子集） |

## G37 部署与运维

- 验收：全新环境一条命令起栈并通过健康检查；反向代理下播放与 Range 正常；升级/回滚演练成功。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G37.1** 容器：Dockerfile 多阶段、非 root、最小基础镜像、健康检查；镜像含 ffprobe、mkvtoolnix、mediainfo 且版本固定可追踪；生产镜像默认不包含 ffmpeg（转码相关二进制），避免误启用转码路径；开发/测试用 ffmpeg 只存在于 `.tools/`（G51），绝不进镜像。 | Dockerfile; deploy/docker-compose.yml; deploy/nginx.conf; deploy/Caddyfile; docs/quickstart.md | 部署 PostgreSQL 卷与迁移版本 | Plan-G37.1：干净环境起栈；非 root；反代 Range；固定运行工具；升级回滚；生产无 ffmpeg；须单独覆盖本行全部约束 | 部分完成：f21d156684 镜像实测非 root/只读根/移除 capabilities、健康状态与优雅停止；静态检查无 ffmpeg/shell/Go SDK；mkvtoolnix、mediainfo 与完整生产工具验收未完成；[实际证据](verification-report.md) ；第3C1正式Linux隔离profile/固定runtime镜像/无DB诊断已有[实测](probe-verification.md)，完整子项仍部分完成 | `0bbd5939bb` / `403cc21b27` / `f21d156684`（基础镜像与已验证服务子集）  / `27f66b1eb3` |
| **G37.2** 编排：`deploy/docker-compose.yml`（Jelee + PostgreSQL + 本地卷）、环境变量示例、卷权限说明（宿主机 UID/GID 映射）。 | Dockerfile; deploy/docker-compose.yml; deploy/nginx.conf; deploy/Caddyfile; docs/quickstart.md | 部署 PostgreSQL 卷与迁移版本 | Plan-G37.2：干净环境起栈；非 root；反代 Range；固定运行工具；升级回滚；生产无 ffmpeg；须单独覆盖本行全部约束 | 部分完成：Compose 与环境/卷说明已提供；完整起栈及 UID/GID 场景验收未全部执行；[实际证据](verification-report.md) | `0bbd5939bb` / `403cc21b27`（仅已实现子集） |
| **G37.3** 反向代理：Nginx/Caddy 参考配置（TLS、HTTP/2、超时、缓冲调优、大文件与 Range 支持）。 | Dockerfile; deploy/docker-compose.yml; deploy/nginx.conf; deploy/Caddyfile; docs/quickstart.md | 部署 PostgreSQL 卷与迁移版本 | Plan-G37.3：干净环境起栈；非 root；反代 Range；固定运行工具；升级回滚；生产无 ffmpeg；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G37.4** 运行：优雅关闭与滚动升级、`/healthz`、`/readyz`、`/metrics`；日志轮转与脱敏。 | Dockerfile; deploy/docker-compose.yml; deploy/nginx.conf; deploy/Caddyfile; docs/quickstart.md | 部署 PostgreSQL 卷与迁移版本 | Plan-G37.4：干净环境起栈；非 root；反代 Range；固定运行工具；升级回滚；生产无 ffmpeg；须单独覆盖本行全部约束 | 部分完成：当前容器真实 PostgreSQL、健康/就绪、HTTP 原字节/权限与停止退出码 0 已测；滚动升级、metrics、完整日志轮转尚未实现；[实际证据](verification-report.md) | `0bbd5939bb` / `403cc21b27`（仅已实现子集） |
| **G37.5** 文档：安装、升级、回滚、故障排查（播放卡顿、扫描异常、DB 连接、NFO 权限、图片抓取失败）手册。 | Dockerfile; deploy/docker-compose.yml; deploy/nginx.conf; deploy/Caddyfile; docs/quickstart.md | 部署 PostgreSQL 卷与迁移版本 | Plan-G37.5：干净环境起栈；非 root；反代 Range；固定运行工具；升级回滚；生产无 ffmpeg；须单独覆盖本行全部约束 | 部分完成：当前基础 quickstart/deployment 与回滚说明已写；全部媒体/资产业务故障手册尚未完成；[实际证据](verification-report.md) | `0bbd5939bb`（deployment）；quickstart 归属最终文档提交 |

## G38 交付、CI 与发布

- 验收：CI 可复现全绿；发布流程演练一次；最终报告齐全无笼统“全部完成”表述。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G38.1** CI：环境准备（`make bootstrap` + `make tools-verify` + `make fixtures`，缓存键基于 `tools/manifest.toml`）→ lint → build → unit → integration（Postgres service 或 G51.9 嵌入式回退）→ contract（含 NFO/图片/兼容层黄金文件）→ 权限矩阵与客户端管控测试 → 严禁转码断言 → 开发者模式不可达断言 → 日志脱敏扫描 → E2E 冒烟 → 容器构建 → 品牌扫描 → 依赖与漏洞审计 → 文档链接与示例校验 → `make gitignore-check` → 性能门禁。 | .github/workflows/; scripts/release/; CHANGELOG.md; docs/requirements-traceability.md | CI 临时 PostgreSQL 和回滚环境 | Plan-G38.1：完整 CI 阶段实际日志；发行演练、hash/签名；未执行与失败不得记绿；须单独覆盖本行全部约束 | 部分完成：本地 Windows/Linux 构建测试与隔离 PG 通过，CI 入口已提供；完整媒体/前端/品牌/漏洞/性能及远端 CI 未通过；[实际证据](verification-report.md) | `721102c8d0` / `0bbd5939bb`（仅已实现子集） |
| **G38.2** 发布：SemVer tag、CHANGELOG 自动生成、制品（二进制/镜像）、校验和/签名（条件允许）；生产镜像不得包含 dev 开关与转码二进制。 | .github/workflows/; scripts/release/; CHANGELOG.md; docs/requirements-traceability.md | CI 临时 PostgreSQL 和回滚环境 | Plan-G38.2：完整 CI 阶段实际日志；发行演练、hash/签名；未执行与失败不得记绿；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G38.3** 文档：README、架构 ADR 集、API 文档（G49）、插件/主题开发文档、NFO 兼容说明、图片资产说明、权限矩阵、客户端兼容矩阵、开发者模式说明、日志与故障排查、备份恢复、性能报告、安全说明、许可证合规。 | .github/workflows/; scripts/release/; CHANGELOG.md; docs/requirements-traceability.md | CI 临时 PostgreSQL 和回滚环境 | Plan-G38.3：完整 CI 阶段实际日志；发行演练、hash/签名；未执行与失败不得记绿；须单独覆盖本行全部约束 | 部分完成：审计/架构/快速开始/直投/NFO/安全/工具/许可/验证文档已有；原文全部文档体系未完成；[实际证据](verification-report.md) | `a512674643` / `721102c8d0` / `c77863e445` / `0bbd5939bb` / `f21d156684`（已提交文档）；其余归属最终文档提交 |
| **G38.4** 报告：最终报告按 G00-G51 逐项“已完成/部分完成/阻塞”，附提交号、测试与性能证据。 | .github/workflows/; scripts/release/; CHANGELOG.md; docs/requirements-traceability.md | CI 临时 PostgreSQL 和回滚环境 | Plan-G38.4：完整 CI 阶段实际日志；发行演练、hash/签名；未执行与失败不得记绿；须单独覆盖本行全部约束 | 部分完成：336 子项与缺失编号已逐项记录，真实测试/计划区别明确；阶段提交已按实际子集归属，最终文档另行提交；其余功能未完成；[实际证据](verification-report.md) | 文档：本矩阵与报告的提交号以文件 Git 历史为准 |

## G39 NFO 兼容、读取与保持（Emby / Jellyfin 双向）

只读 NFO adapter 与离线校验 CLI 已提交为 `f21d15668477bd5806e7e525149bfb373d9a68bd`。[Windows](evidence/windows-tests.txt) 与 [Linux race](evidence/linux-race.txt) 最终日志均标明该源码提交，NFO 包覆盖率 89.8%；平台跳过单列于验证报告。尚未实现修改后的 XML 序列化、按库批量处理、`--fix`、任务接入及真实客户端往返；下列部分完成仅指已验证的只读子集。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G39.1** 文件识别：支持 `movie.nfo`、`<视频文件名>.nfo`、`tvshow.nfo`、`season.nfo`、`<剧集文件名>.nfo`；大小写不敏感；支持 UTF-8/UTF-8 BOM/UTF-16/GBK（带 BOM 与探测回退）。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.1：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：只讀解析涵蓋UTF-8/BOM、UTF-16、GBK與回退；正式CLI／HTTP以真實影片及影集／季資料夾選擇同名NFO、movie.nfo、tvshow.nfo及season.nfo，大小寫比對、歧義拒絕及有界目錄觀察通過。全庫自動辨識與真實舊客戶端樣本仍待；[解析契約](nfo-compatibility.md)、[資料夾來源](directory-nfo-sources.md)、[單集資料](nfo-episode-details.md) | `f21d156684`（仅只读子集） |
| **G39.2** 根元素：兼容 `<movie>`、`<tvshow>`、`<episode>`、`<season>`（存在时）、以及 Emby/Jellyfin 常见包装（`<root>`、`<Item>`、`<MediaBrowser>` 派生结构）；未知根元素记录告警而非崩溃。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.2：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：movie/tvshow/season/episode/episodedetails、包装与未知根告警已通过合成合同；多单集根保留源顺序；真实客户端格式互操作仍未验收；[NFO范围与测试](nfo-compatibility.md)、[最终日志](evidence/linux-race.txt) | `f21d156684`（仅只读子集） |
| **G39.3** 电影字段：title、originaltitle、sorttitle、plot/outline、tagline、year、premiered/releasedate、rating、ratings（多来源嵌套）、mpaa、certification、runtime、genre（多值）、tag（多值）、studio（多值）、country、language、director/writer（多值）、actor（name/role/thumb/order）、producer、trailer、thumb/aspect（多值）、fanart（多值）、`art` 结构（poster/fanart/banner/clearart/clearlogo/thumb/landscape）、uniqueid（type 属性，IMDB/TMDB/TVDB）、imdbid/tmdbid/tvdbid、ratings、userrating、lockdata、dateadded、collection/set 与 `<set>` 结构。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.3：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：主要文本/数值/日期、多值、人物、评分、ID、锁及图片字段已提取并通过黄金测试；提供商扩展和完整字段等价性未全部验收；[NFO范围与测试](nfo-compatibility.md)、[最终日志](evidence/linux-race.txt) 正式投影對標題／日期別名共用單值重複檢查，歧義有效XML拒503且無provider／保存，單別名及巢狀actor names保持；[別名契約](nfo-alias-ambiguity.md)； 第27版新增sortTitle正式套用／來源／獨立鎖與人工空值接管，NFO-only及TMDB同交易融合，別名歧義拒絕與保留資料降版保護；[排序標題](nfo-sort-title.md)； 第28版新增tagline／outline／mpaa／certification正式套用，九文字來源／獨立鎖／人工patch與同交易融合，OfficialRating無值鎖不虛構來源，舊投影集合保持；[九文字契約](nfo-text-fields.md)； 確認套用拒絕year／runtime／rating／userrating重複，communityrating共用評分目的欄位；巢狀多來源保持，該守衛階段尚未接入數值保存，後續年份見下方契約；[數值單值守衛](nfo-numeric-ambiguity.md)； 第29版年份以JSON整數正式保存，人工null清除與獨立鎖同交易，其他數值尚未套用；[年份契約](nfo-year-fact.md)；  第30版補上片長／一般與使用者評分，四數值與九文字／獨立鎖共交易，人工零值與null優先，舊投影保持；[數值保存](nfo-numeric-facts.md)；  第31版八種字串多值以有順序JSON陣列正式保存，來源／獨立鎖／人工null與空陣列清除共交易，128值與UTF-8界限及新資料降版保護；NFO-only／供應商融合經實際HTTP與PG驗收；[多值保存](nfo-string-lists.md)；  第32版演員name／role／thumb／可省略order以有順序物件正式保存，人工清除／獨立鎖與同交易融合、歧義／界限及舊資料往返／保留新資料拒降經實際HTTP與PG驗收；[演員結構](nfo-actors.md)；  第33版識別碼type／value／default與來源順序正式保存，供應商衝突拒絕，人工清除／獨立鎖與交易回滾、舊演員往返及新資料拒降經實際HTTP／PG驗收；[識別碼契約](nfo-identifiers.md)；  第34版多來源評分name／value／max／votes／default、原尺度與缺省保持，來源／獨立鎖及人工清除共交易；歧義、可空值、回滾與降版經實際HTTP及PG驗收；[多來源評分](nfo-ratings.md)；  第35版合集name／overview及文本別名、人工null／獨立鎖共交易，重複／缺名／混合內容與Unicode空白拒絕，舊評分往返及新資料拒降經實際HTTP／PG驗收；[合集結構](nfo-collection.md)；  第36版新增日期原表示、預告片／圖片有序參照與季數，人工清除／獨立鎖共交易；新讀取全域鎖涵蓋28欄，歧義／界限／回滾與降版經實際HTTP／PG驗收；[電影附加資料](nfo-movie-extras.md)； | `f21d156684`（仅只读子集） |
| **G39.4** 剧集字段：tvshow 级（同电影字段 + `season` / `episode` 计数、status、airs 相关）、season.nfo（seasonnumber、title、plot、poster 等）、episode 级（title、plot、season、episode、displayseason/displayepisode、aired、rating、actor、director、writer、thumb、uniqueid、lockdata）。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.4：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：剧集/季度/单集根、seasonnumber、显示季集号、aired、人物/评分/ID/锁等已测；airs 细分结构、计数语义及真实客户端字段等价性未完成；[NFO范围与测试](nfo-compatibility.md)、[最终日志](evidence/linux-race.txt)  第37版tvshow五欄正式保存：季數／集數保留未知-1、零與缺省，狀態及播出星期／時間保留原文，人工清除／獨立鎖共交易；33欄全域鎖、回滾及降版經實際HTTP／PG驗收；[劇集資料](nfo-series-details.md)；  第38版單集綁定真實影片同名NFO，六欄保留缺省／零、人工清除與獨立鎖，34欄全域鎖、回滾／降版經HTTP及PG驗收；[單集資料](nfo-episode-details.md)；  已接正式CLI Movie／Episode匯入到同名NFO套用，資料夾階層仍缺；[匯入種類](import-video-kinds.md)；  第39版已接真實影集／季資料夾與同庫父子關聯，CLI與HTTP套用tvshow／season、季數及29欄獨立鎖、回滾／降版驗收通過；[資料夾来源](directory-nfo-sources.md)； | `f21d156684`（仅只读子集） |
| **G39.5** 多值与分隔符：genre/tag/studio/director/writer/country 支持多元素与斜杠分隔两种形式；写入时使用可配置分隔符（默认与读取库策略一致，默认保留多元素形式以免破坏第三方兼容）。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.5：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：多元素与斜杠分隔读取已测并保留输入顺序；可配置分隔符与编辑后的 XML 写入未实现；[NFO范围与测试](nfo-compatibility.md)、[最终日志](evidence/linux-race.txt) | `f21d156684`（仅只读子集） |
| **G39.6** 读取策略：NFO 存在时优先级高于自动刮削；按库配置 `NfoMode`（`read-only` / `read-write` / `off`）；字段级锁（`lockdata=true` 或 Jelee 字段锁）阻止覆盖。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.6：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：按库off/read-only、獨立generation、live-admin幂等策略和审计已接worker/API/CLI；显式入队冻结意图，默认off，read-write拒绝。明確確認項目的同名NFO四欄已接可信來源與真HTTP／PG、獨立NFO鎖及人工優先；既存NFO來源於讀取關閉後仍高於TMDB，負例已驗；唯讀庫同次TMDB融合已接正式入口並經正反驗收；讀者持有跨查詢實體身分觀察，根／父目錄／媒體／NFO在相同內容與戳下替換也拒409且無更新／audit，經完整HTTP／TLS／NFO／PG負例與復原驗收；[實體觀察](nfo-item-observation.md)；讀者已區分有效／可信缺失／解析損壞，重核原bytes戳與狀態轉換；[三態觀察](nfo-item-state-observation.md)；第24版安全觀察與可信缺失／損壞TMDB回退已接同交易，明確狀態／原bytes戳與單版本／稽核，人工歷史及NFO優先保持，經實際HTTP／TLS／NFO／PG正反驗收；[回退與保存](nfo-tmdb-fallback.md)；同名／慣用檔名來源選擇已經正反驗收；其他NFO欄位、季／集／多項目來源與read-write仍缺；[NFO項目套用](nfo-item-metadata.md)；[同次融合](nfo-tmdb-fusion.md)；[證據](evidence/nfo-item-metadata.json)；[3C3C实际证据](nfo-worker-verification.md) 第25版補上缺少文字值時的獨立NFO鎖定證明；人工文字接管同交易解除、人工鎖開關不能繞過、讀取off後仍保護；[缺值鎖定](nfo-field-lock-intent.md)； 第26版支援有效lock-only專用投影：正鎖／有效觀察同交易保存，原文字來源與分類保持，指定鎖允許其他TMDB欄位，查詢期間指令變更拒409；[lock-only契約](nfo-lock-only.md)； 正式投影對標題／日期別名共用單值重複檢查，歧義有效XML拒503且無provider／保存，單別名及巢狀actor names保持；[別名契約](nfo-alias-ambiguity.md)； 第27版新增sortTitle正式套用／來源／獨立鎖與人工空值接管，NFO-only及TMDB同交易融合，別名歧義拒絕與保留資料降版保護；[排序標題](nfo-sort-title.md)； 第28版新增tagline／outline／mpaa／certification正式套用，九文字來源／獨立鎖／人工patch與同交易融合，OfficialRating無值鎖不虛構來源，舊投影集合保持；[九文字契約](nfo-text-fields.md)； 確認套用拒絕year／runtime／rating／userrating重複，communityrating共用評分目的欄位；巢狀多來源保持，該守衛階段尚未接入數值保存，後續年份見下方契約；[數值單值守衛](nfo-numeric-ambiguity.md)； 第29版年份以JSON整數正式保存，人工null清除與獨立鎖同交易，其他數值尚未套用；[年份契約](nfo-year-fact.md)；  第30版補上片長／一般與使用者評分，四數值與九文字／獨立鎖共交易，人工零值與null優先，舊投影保持；[數值保存](nfo-numeric-facts.md)；  第31版八種字串多值以有順序JSON陣列正式保存，來源／獨立鎖／人工null與空陣列清除共交易，128值與UTF-8界限及新資料降版保護；NFO-only／供應商融合經實際HTTP與PG驗收；[多值保存](nfo-string-lists.md)；  第32版演員name／role／thumb／可省略order以有順序物件正式保存，人工清除／獨立鎖與同交易融合、歧義／界限及舊資料往返／保留新資料拒降經實際HTTP與PG驗收；[演員結構](nfo-actors.md)；  第33版識別碼type／value／default與來源順序正式保存，供應商衝突拒絕，人工清除／獨立鎖與交易回滾、舊演員往返及新資料拒降經實際HTTP／PG驗收；[識別碼契約](nfo-identifiers.md)；  第34版多來源評分name／value／max／votes／default、原尺度與缺省保持，來源／獨立鎖及人工清除共交易；歧義、可空值、回滾與降版經實際HTTP及PG驗收；[多來源評分](nfo-ratings.md)；  第35版合集name／overview及文本別名、人工null／獨立鎖共交易，重複／缺名／混合內容與Unicode空白拒絕，舊評分往返及新資料拒降經實際HTTP／PG驗收；[合集結構](nfo-collection.md)；  第36版新增日期原表示、預告片／圖片有序參照與季數，人工清除／獨立鎖共交易；新讀取全域鎖涵蓋28欄，歧義／界限／回滾與降版經實際HTTP／PG驗收；[電影附加資料](nfo-movie-extras.md)；  第37版tvshow五欄正式保存：季數／集數保留未知-1、零與缺省，狀態及播出星期／時間保留原文，人工清除／獨立鎖共交易；33欄全域鎖、回滾及降版經實際HTTP／PG驗收；[劇集資料](nfo-series-details.md)；  第38版單集綁定真實影片同名NFO，六欄保留缺省／零、人工清除與獨立鎖，34欄全域鎖、回滾／降版經HTTP及PG驗收；[單集資料](nfo-episode-details.md)；  第39版已接真實影集／季資料夾與同庫父子關聯，CLI與HTTP套用tvshow／season、季數及29欄獨立鎖、回滾／降版驗收通過；[資料夾来源](directory-nfo-sources.md)； | `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`（第3C3B子集） / `47da27b5ff090a71e6f55aae0b2e6c00871f825b`（第3C3C子集） |
| **G39.7** 写入与保持：写回必须保留未知/未识别标签与属性（解析保留原始 XML 子树并在序列化时回写），保留注释与缩进风格可配置，保持元素顺序稳定；UTF-8 带 BOM 策略可配（默认无 BOM 或沿用原文件策略）。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.7：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：原文复制逐字节保留未知标签、属性、注释、顺序、缩进与编码/BOM；WriteOriginal 不序列化修改后的 Metadata；可编辑无损写回与输出风格配置未实现；[NFO范围与测试](nfo-compatibility.md)、[最终日志](evidence/linux-race.txt) | `f21d156684`（仅只读子集） |
| **G39.8** 原子写入：临时文件 → `fsync` → `os.Rename` 原子替换；写前备份（可配置保留 N 份 `.nfo.jelee.bak`）；失败自动回滚。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.8：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G39.9** 并发与锁：跨进程写 NFO 使用文件锁（POSIX `flock` / Windows `LockFileEx`）与进程内 `singleflight` 去重；同一条目并发写不得产生截断或交错内容。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.9：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G39.10** 一致性：写回时若条目无 ID 则生成并写入（元素名与策略在 `docs/nfo-compatibility.md` 中明确，且不得覆盖已有 ID）；不得擅自改写用户手工填写的 ID。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.10：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G39.11** 校验与修复：`jelee-cli nfo validate --library X` 输出问题清单（XML 错误、缺失字段、非法路径、编码问题）；`--fix` 仅做可安全修复项并先备份。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.11：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：保留单文件只读CLI；新增--library验证入队及current-validations/issues分页命令。validate返回job，不等于单命令等待并输出完整问题清单；--fix和修复备份未实现。当前缓存观察也不是历史逐文件报告；[3C3C接口](nfo-worker.md)、[实际证据](nfo-worker-verification.md) | `f21d156684`（仅只读子集） / `feeee03da801bb4730eca86b39d55eb5b1521783`（第3C3A） / `47da27b5ff090a71e6f55aae0b2e6c00871f825b`（第3C3C子集） |
| **G39.12** 损坏容忍：XML 非法时记录 `nfo_invalid` 状态、保留原文件、回退到其他数据源，不得删除或覆盖用户 NFO。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.12：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：只读来源核对保持原文；XML/编码错误与语义error可持久保存为固定invalid摘要及短TTL，来源变动/取消/存储错误不成为负缓存；電影／影集項目明確確認的可信缺失／解析損壞TMDB回退已接，nfo_invalid／原bytes戳和字段／版本／稽核同次保存，實際HTTP／TLS／NFO／PG正反驗原文保持及來源變更拒絕；[項目回退](nfo-tmdb-fallback.md)；scan worker壞項隔離、其他項目種類及完整欄位仍待接入；[3C3B实际证据](nfo-cache-verification.md) | `f21d156684`（仅只读子集） / `feeee03da801bb4730eca86b39d55eb5b1521783`（第3C3A） / `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`（第3C3B子集） |
| **G39.13** 安全：NFO 中的外链 URL（thumb/fanart/actor thumb）下载必须经 G11 SSRF 防护与域名白名单；NFO 文本作为用户输入处理，前端展示必须转义。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.13：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：离线解析不请求 URL，图片引用语法检查拒绝危险形式，DTD/外部实体拒绝；未来下载 SSRF/域名白名单及前端转义未实现；[NFO范围与测试](nfo-compatibility.md)、[最终日志](evidence/linux-race.txt) | `f21d156684`（仅只读子集） |
| **G39.14** 开关与迁移：提供“从 NFO 全量导入”“导出全部条目为 NFO”“仅导出缺失 NFO”三种批量操作，全部走任务系统（可取消、可观测、批处理）。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.14：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G39.15** 验收：黄金文件集覆盖 Emby/Jellyfin 典型 NFO（电影、剧集、季度、单集、带 art 结构、带锁、UTF-16、损坏文件），解析后字段映射零差异；往返测试（读 → 改 → 写 → 再读）证明未知标签与注释不丢失；并发写 100 次无损坏；Jellyfin/Emby 可读取 Jelee 写出的 NFO（提供验证记录）。 | internal/adapter/nfo/; internal/app/; cmd/jelee-cli/; docs/nfo-compatibility.md | nfo_documents/nfo_field_locks/metadata_locks | Plan-G39.15：各编码和结构黄金集；未知标签属性注释往返；100 并发写；备份失败回滚；双向真实旧客户端验证；须单独覆盖本行全部约束 | 部分完成：合成黄金样本、原文字节复制、编码/损坏/锁/图片与 Windows/Linux race 已测；缺真实客户端零差异和双向记录、修改写回及 100 并发写验收；[NFO范围与测试](nfo-compatibility.md)、[最终日志](evidence/linux-race.txt) | `f21d156684`（仅只读子集） |

## G40 图片资产：海报 / 背景图 / Logo / 单集缩略图等

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G40.1** 类型覆盖：Primary（海报）、Backdrop/Fanart（背景图）、Logo、Banner、ClearArt、ClearLogo、Thumb（单集缩略图/人物头像）、Art/Disc、Chapter 图（可提取或生成）、Landscape、Box/BoxRear、Menu、Profile 图；人物（Actor）头像与演职员图。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.1：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.2** 文件命名兼容：同级 `poster.*`、`folder.*`、`cover.*`、`movie.*`、`<文件名>-poster.*`、`backdrop.*`、`fanart.*`、`fanart1..n.*`、`logo.*`、`clearlogo.*`、`banner.*`、`clearart.*`、`landscape.*`、`thumb.*`、`<剧集文件名>-thumb.*`、`<文件名>.episode-thumb.*`、`season01-poster.*`、`season01-thumb.*`、`series-poster.*`；大小写不敏感；扩展名支持 jpg/jpeg/png/webp/avif/gif/bmp/tiff。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.2：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.3** NFO 内图片：读取 NFO 中 `<thumb>`、`<fanart>`、`<art>` 结构（含 `aspect`、`season`、`type` 属性）以及 NFO 内指向的远程 URL；远程 URL 需下载到本地缓存并记录来源。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.3：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 部分完成：读取 thumb/fanart/art 与 aspect/type/season/preview、保留 URL 字符串并检查引用语法；不下载图片，本地缓存与来源持久化未实现；[NFO范围与测试](nfo-compatibility.md)、[最终日志](evidence/linux-race.txt) | `f21d156684`（仅只读子集） |
| **G40.4** 内嵌封面提取：可选通过 ffprobe/ffmpeg（如可用）提取视频内嵌封面/附件图片（mjpeg/png）到缓存，默认关闭且可配置。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.4：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.5** 存储与缓存：原图与派生变体分离；变体目录可清理可重建；缓存键含 源路径/大小/mtime/目标尺寸/格式/质量；使用内容寻址或稳定哈希文件名避免重复存储。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.5：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.6** 处理管线：按需缩放/裁剪/格式转换（WebP/AVIF/JPEG 可配）、质量参数可配、保持宽高比、支持 focal point 或智能裁剪（可关闭）；处理并发受限（见 G41）并有超时与取消。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.6：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.7** 元数据：记录宽高、格式、主色调（可选）、平均色（可选）、文件大小、来源（本地/远程/NFO/内嵌）、抓取时间、是否锁定。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.7：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.8** API 与前端：提供 `/images/{type}/{id}` 与兼容层旧图片路由；支持宽度/高度/质量/格式/标签参数；返回 ETag 与长缓存头；支持 `If-None-Match` 返回 304；前端请求按容器尺寸取图并懒加载。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.8：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.9** 抓取：来自 TMDB 等外部源的图片下载需限流、并发受限、SSRF 防护、失败重试与记录；可配置“仅本地图片”模式。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.9：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.10** 锁定与优先级：本地图片 > NFO 指定 > 外部抓取；用户锁定图片后不被刷新覆盖；提供“重建图片缓存/重新抓取”任务。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.10：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.11** 删除与保留：更换/删除图片时默认保留原本地图片文件（不删用户资产），删除操作需确认并写审计；仅清理 Jelee 生成的变体。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.11：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.12** 性能与内存：禁止把整张原图读入内存后多份复制；使用流式解码与尺寸预检；大图处理有内存上限与拒绝策略；解码并发受 G41 预算约束。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.12：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G40.13** 验收：每种类型与命名方式各有样本并被正确识别；单集缩略图同时支持文件名约定、NFO 指定与外部抓取；缩放请求命中缓存（有命中率数据）；ETag/304 生效；10 万张图片库下内存占用不超预算（见 G42）；外部图片失败不影响扫描。 | internal/adapter/images/; internal/adapter/nfo/; internal/adapter/http/; web/src/; docs/image-assets.md | images/image_variants | Plan-G40.13：全部类型/命名；ETag/304；本地优先/锁定；10 万图片内存；失败隔离；用户原图不删除；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G41 并发架构与吞吐

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G41.1** 任务模型：所有异步工作（扫描、探测、索引构建、NFO 解析/写回、图片处理/抓取、字幕提取、统计聚合、一致性校验、Webhook 投递）统一走任务队列与 worker pool，禁止各自散乱起 goroutine。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.1：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 部分完成：inventory/NFO/probe统一使用持久parent队列、固定worker和阶段检查点，未为NFO另起逐文件goroutine；写回/图片处理等其余异步工作尚待接入；[3C3C实际证据](nfo-worker-verification.md)  schema40的影片批次匯入已接同一佇列／worker，100筆、取消恢復、舊租約拒絕、逐筆回滾與來源保留經真PG及TLS驗收；[批次任務](catalog-import-jobs.md)； | `45d3fbfcb8`（本段已实现子集） / `47da27b5ff090a71e6f55aae0b2e6c00871f825b`（第3C3C子集） |
| **G41.2** 队列与优先级：至少两级优先级（用户触发/交互相关 > 后台例行）；支持公平调度防止大库扫描饿死交互任务；任务可取消、可超时、可重试。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.2：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 部分完成：manual/background 两级 3:1 优先、fallback 和有界队列；完整混合任务公平性待验收；[第3A实际证据](jobs-verification.md) | `45d3fbfcb8`（本段已实现子集） |
| **G41.3** 并发预算：CPU 密集型（探测、索引构建、图片处理）并发 ≈ CPU 核数（可配系数）；I/O 密集型（目录遍历、下载、NFO 读写、直投流式）并发按独立上限；总并发受统一限额器约束；所有限额可通过配置调整并有默认值。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.3：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 部分完成：worker/队列/文件/目录/历史/HTTP 硬限；未来全部 CPU/I/O/外部程序与全局资源配额待交付；[第3A实际证据](jobs-verification.md) | `c77863e445` / `632005d430`（仅已实现子集）；`45d3fbfcb8`（第3A子集） |
| **G41.4** 背压：有界队列 + 明确满载策略（阻塞/丢弃/降级）；禁止无界 channel 与无界 goroutine；队列深度指标暴露。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.4：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 部分完成：持久queue拒绝超容量，probe本地有界gate和DB busy/backoff、不创建每文件goroutine、取消退出已验证，见[worker验证](probe-worker-verification.md)；统一队列深度遥测和完整混合负载仍待交付 | `c77863e445` / `632005d430`（仅已实现子集）；`45d3fbfcb8`（第3A子集） / `b59be8d389216c3853a0a6d18b1f1af130745a9f`（第3C2B） |
| **G41.5** 数据库并发：连接池大小与 worker 数匹配并文档化；避免连接饥饿（提供等待队列与超时）；批量写入合并；advisory lock 防任务重入；leader 选举用于单例任务。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.5：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 部分完成：要求 pool 至少 workers+2；各任务与 HTTP DB 操作有期限；完整混合负载容量验收待交付；[第3A实际证据](jobs-verification.md) | `c77863e445` / `632005d430`（仅已实现子集）；`45d3fbfcb8`（第3A子集） |
| **G41.6** 分布式与多实例：明确多实例部署下的任务归属（leader 或分片）；至少保证单实例正确性，多实例需有锁与心跳续约。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.6：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 部分完成：PG 租约心跳、owner/generation、过期及交易中到期 fencing 真测通过；排程使用短交易選主，六路並行與進度失敗回滾已驗證；schema42 監看另用租約、設定與根目錄世代守衛，雙實例與交易中到期回滾已驗證；[監看證據](evidence/scan-watch.json)；[排程證據](evidence/scan-schedules.json)；[第3A实际证据](jobs-verification.md) | `45d3fbfcb8`（本段已实现子集） |
| **G41.7** 自适应：可选根据系统负载（load average、cgroup CPU quota、内存压力）动态下调并发；下调与恢复有滞后避免抖动。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.7：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G41.8** 可观测：暴露队列深度、在跑任务数、等待时长、任务耗时分布、取消/失败计数、goroutine 数、DB 池使用率。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.8：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G41.9** 测试：`-race` 下并发压测（混合扫描 + 探测 + NFO 写 + 图片处理 + API 请求）；断言无死锁、无 goroutine 泄漏、无连接耗尽、无数据竞争。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.9：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 部分完成：当前媒体/HTTP/PG 的 Linux race 通过；扫描/探测/NFO/图片混合压力尚未实现；[实际证据](verification-report.md) | `c77863e445` / `632005d430`（仅已实现子集） |
| **G41.10** 验收：给出并发参数调优基准表（不同核数/内存下的推荐值与实测吞吐）；满载时系统响应仍可用（API P95 不崩溃）；压测期间 goroutine 与内存曲线平稳。 | internal/platform/workers/; internal/app/jobs/; internal/adapter/postgres/; internal/platform/telemetry/ | job_locks/leases/heartbeats | Plan-G41.10：race 混合负载；公平/背压/取消；多实例租约；不同核数吞吐；goroutine/内存曲线；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G42 内存占用优化

- 验收：默认构建下 dev 路由与开关全部不可达（自动化断言）；开启后横幅/响应头/日志三处同时生效；关闭或到期后所有限制立即恢复；审计记录完整。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G42.1** 禁止模式：禁止把整个媒体文件读入内存；禁止大列表全量加载后过滤（改为数据库分页/游标）；禁止响应体一次性构造超大 JSON（改流式编码）；禁止无界缓存。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.1：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 部分完成：当前媒体不整文件读内存，SQL 有界权限分页；全部后续资产/列表/缓存路径未实现；[实际证据](verification-report.md) | `c77863e445` / `632005d430`（仅已实现子集） |
| **G42.2** 流式：文件与图片处理使用流式读写；HTTP 响应流式编码；大 NFO/大量 XML 使用流式解析而非整体 DOM（若需保留未知标签，采用流式 + 子树缓冲策略并说明取舍）。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.2：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 部分完成：原媒体按流读取已实测；图片/大 NFO 流式解析未实现；[实际证据](verification-report.md) | `c77863e445` / `632005d430`（仅已实现子集） |
| **G42.3** 扫描内存：目录遍历与文件条目处理流式化，批处理大小有上限；完整库扫描内存占用不随条目数线性增长（提供 1 万 / 10 万 / 50 万条目三档实测曲线）。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.3：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 部分完成：单目录 ReadDir128 与同步 checkpoint 有界；真实1000文件及原生 Linux race 通过，1万/10万/50万曲线未验收；[第3A实际证据](jobs-verification.md) ；清單批次 SQL 往返已降為 13 次，一萬檔案有實測；十萬檔案基準發布逾時，完整規模未驗收，見[批次證據](evidence/scan-batch.json) | `45d3fbfcb8`（本段已实现子集） |
| **G42.4** 缓存治理：所有缓存（探测结果、图片变体索引、元数据、TMDB 响应、翻译资源）必须有容量上限与淘汰策略（LRU/LFU/TTL 组合）与命中率指标；支持按内存压力主动收缩。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.4：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 部分完成：NFO缓存的全局/单库行数/字节上限、正负TTL、索引LRU/过期清理、每轮128项维护已接runtime；真实冷/暖扫描核对配额和命中。其余cache统一治理、运行指标、内存压力收缩及24小时规模验收未完成；[3C3B契约](nfo-cache-verification.md)、[3C3C实际证据](nfo-worker-verification.md) | `f26888aa27f6ca9a80ad406cfac1e1f8e6ff0c02`（第3C3B子集） / `47da27b5ff090a71e6f55aae0b2e6c00871f825b`（第3C3C子集） |
| **G42.5** 对象复用：热路径使用 `sync.Pool` 复用缓冲与编码器；预分配切片容量；避免高频小对象分配；避免热路径反射与重复序列化。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.5：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G42.6** 图片内存：解码并发与单图内存上限可配；超大图按尺寸预检拒绝或降采样；禁止同时驻留多份全尺寸位图。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.6：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G42.7** 子进程：外部工具（ffprobe/mkvmerge/mediainfo）stdout/stderr 限流读取与截断，防止输出灌爆内存；大输出走临时文件而非管道内存缓冲。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.7：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 部分完成：stdout有界保留、stderr仅计数、超限终止/错误不返回raw内容已验证；大输出暂存策略与其他工具待实现，见[3B2验证](process-verification.md) | `03a559524c`（第3B2子集） |
| **G42.8** 运行时：合理设置 `GOGC` 与 `GOMEMLIMIT`（可配），并记录调优依据；容器内存限制下验证不 OOM。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.8：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G42.9** 监控与门禁：暴露 Go 运行时指标（heap、goroutine、GC 暂停、alloc rate）；设定内存预算（如典型 4C8G 环境常驻 ≤ 阈值，具体阈值以基线为准）并在 CI/基准中校验。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.9：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G42.10** 验收：提供 pprof heap/inuse_space 前后对比；50 万条目完整扫描与 10 万图片处理下内存不超预算且 GC 暂停可控；长时间运行（≥24h 模拟）无内存单调增长（或有证据的缓存稳态）。 | internal/platform/cache/; internal/adapter/media/; internal/adapter/images/; internal/diag/; docs/perf-report.md | 无；缓存可重建 | Plan-G42.10：1万/10万/50万扫描 heap 曲线；10万图片；pprof 前后；容器预算；≥24h 无单调增长；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G45 开发者模式（特殊方式开启，用于关闭限制与调试）

- 验收：默认构建下 dev 路由与开关全部不可达（自动化断言）；开启后横幅/响应头/日志三处同时生效；关闭或到期后所有限制立即恢复；审计记录完整。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G45.1** 开启方式（需多重门槛，不得单一开关）：环境变量 `JELEE_DEV_MODE=true` + 配置文件 `dev.enabled: true` + CLI 子命令 `jelee-cli devmode enable --token <一次性令牌>` + 特殊入口（仅本地环回可访问的魔法路径或专用端口）。默认全部关闭。 | internal/platform/devmode/; cmd/jelee-cli/; internal/adapter/http/; web/src/; docs/developer-mode.md | 一次性令牌、短期调试状态及审计 | Plan-G45.1：多重门槛/production 强制拒绝；过期恢复；危险操作确认；默认关闭下 dev 路由不可达；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G45.2** 防误开：非环回地址访问管理入口时禁止通过 URL 方式开启；开启需审计记录（时间、来源、配置 diff）；容器镜像生产标签默认禁用；`JELEE_ENV=production` 时强制忽略 dev 配置并告警。 | internal/platform/devmode/; cmd/jelee-cli/; internal/adapter/http/; web/src/; docs/developer-mode.md | 一次性令牌、短期调试状态及审计 | Plan-G45.2：多重门槛/production 强制拒绝；过期恢复；危险操作确认；默认关闭下 dev 路由不可达；须单独覆盖本行全部约束 | 部分完成：此生产构建拒绝开发者模式配置；完整组合门槛、审计与生产标签机制未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G45.3** 持续可见：开发者模式开启时，UI 顶部常驻醒目横幅、API 响应头 `X-Jelee-Dev-Mode: true`、系统信息接口返回 `devMode: true`、启动与周期性日志 WARN 提醒。 | internal/platform/devmode/; cmd/jelee-cli/; internal/adapter/http/; web/src/; docs/developer-mode.md | 一次性令牌、短期调试状态及审计 | Plan-G45.3：多重门槛/production 强制拒绝；过期恢复；危险操作确认；默认关闭下 dev 路由不可达；须单独覆盖本行全部约束 | 部分完成：当前系统与响应头明确 dev=false；开启态 UI 横幅/周期 WARN 尚未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G45.4** 可关闭的限制（逐项开关，非全开）：登录限速、API 限流、并发播放上限、带宽上限、忽略文件规则、NFO 只读保护、图片锁定、权限矩阵严格模式、Host 校验严格模式、SSRF 严格拦截、公网 IP 隐藏约束、客户端 UA 屏蔽。 | internal/platform/devmode/; cmd/jelee-cli/; internal/adapter/http/; web/src/; docs/developer-mode.md | 一次性令牌、短期调试状态及审计 | Plan-G45.4：多重门槛/production 强制拒绝；过期恢复；危险操作确认；默认关闭下 dev 路由不可达；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G45.5** 可开启的调试选项：详细日志（DEBUG/TRACE）、SQL 语句与耗时日志、请求/响应体日志（脱敏后）、pprof 端点（`/debug/pprof/*`）、OpenAPI 原始与内部 API 暴露、内部错误堆栈返回（仅 dev）、模拟客户端能力（可伪造 UA/设备/编解码能力）、转码调试开关（`dev-transcode`，违反 G10 铁律但仅限 dev）、mock 外部服务（TMDB/图片源）、种子数据生成、强制任务立即执行。 | internal/platform/devmode/; cmd/jelee-cli/; internal/adapter/http/; web/src/; docs/developer-mode.md | 一次性令牌、短期调试状态及审计 | Plan-G45.5：多重门槛/production 强制拒绝；过期恢复；危险操作确认；默认关闭下 dev 路由不可达；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G45.6** 危险操作二次确认：删除全部数据、重建库、清空缓存、关闭鉴权、导入不受信 NFO 等必须 `--i-understand` 或 UI 二次确认，并写审计。 | internal/platform/devmode/; cmd/jelee-cli/; internal/adapter/http/; web/src/; docs/developer-mode.md | 一次性令牌、短期调试状态及审计 | Plan-G45.6：多重门槛/production 强制拒绝；过期恢复；危险操作确认；默认关闭下 dev 路由不可达；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G45.7** 自动降级：开发者模式设置最长有效期（可配，默认如 12 小时）到期自动关闭并恢复生产限制；进程重启默认不继承（除非显式持久化并告警）。 | internal/platform/devmode/; cmd/jelee-cli/; internal/adapter/http/; web/src/; docs/developer-mode.md | 一次性令牌、短期调试状态及审计 | Plan-G45.7：多重门槛/production 强制拒绝；过期恢复；危险操作确认；默认关闭下 dev 路由不可达；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G45.8** 测试隔离：所有测试默认在开发者模式关闭态运行；必须有断言证明生产态下 dev 专属路由、端点、开关不可达（返回 404/403）。 | internal/platform/devmode/; cmd/jelee-cli/; internal/adapter/http/; web/src/; docs/developer-mode.md | 一次性令牌、短期调试状态及审计 | Plan-G45.8：多重门槛/production 强制拒绝；过期恢复；危险操作确认；默认关闭下 dev 路由不可达；须单独覆盖本行全部约束 | 部分完成：默认关闭与 debug 路由不可达合同通过；完整 dev 选项矩阵尚未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（仅已实现子集） |
| **G45.9** 文档：`docs/developer-mode.md` 列出全部开关、风险、默认值、恢复方式；README 显著位置警告不得在生产启用。 | internal/platform/devmode/; cmd/jelee-cli/; internal/adapter/http/; web/src/; docs/developer-mode.md | 一次性令牌、短期调试状态及审计 | Plan-G45.9：多重门槛/production 强制拒绝；过期恢复；危险操作确认；默认关闭下 dev 路由不可达；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G46 日志系统（全面、分层、可诊断）

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G46.1** 输出：`log/slog` JSON 结构化为主，支持控制台人类可读模式（dev）、文件轮转（大小/时间/保留份数）、可选 stdout-only 容器模式、可选 syslog/Loki/OTLP 转发。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.1：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 部分完成：slog JSON 输出已使用并实测脱敏；轮转、dev 控制台与转发适配未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27` / `632005d430`（仅已实现子集） |
| **G46.2** 级别与组件：全局级别 + 按组件级别（`http`、`auth`、`access`、`scan`、`probe`、`nfo`、`images`、`jobs`、`webhook`、`compat`、`media`、`db`、`gc`）独立可配，运行时可热调整（管理员或 CLI）。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.2：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G46.3** 分类日志：访问日志（含 method/path/status/耗时/大小/traceId/userId/clientId，媒体请求单独标记）、审计日志（管理操作与权限变更，不可被普通日志级别关闭）、安全日志（登录失败、锁定、被屏蔽客户端、越权尝试、SSRF 拦截、dev 模式变更）、任务日志（扫描/索引/NFO/图片/Webhook，带 taskId 与进度）、媒体日志（探测、直投、Seek、断连、失败原因）、DB 慢查询日志（阈值可配）、panic/崩溃日志与堆栈归档。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.3：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 部分完成：已实现 HTTP/媒体/账户/任务 component；其余组件与热调待交付；[第3A实际证据](jobs-verification.md) | `c77863e445` / `403cc21b27` / `632005d430`（仅已实现子集）；`45d3fbfcb8`（第3A子集） |
| **G46.4** 上下文字段：`traceId`、`spanId`、`requestId`、`userId`、`deviceId`、`clientId`、`itemId`、`libraryId`、`taskId`、`jobRunId`；中间件自动注入，跨 goroutine 传递。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.4：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 部分完成：requestId 和经 UUID 校验的 taskId；完整 OTel/跨模块上下文待交付；[第3A实际证据](jobs-verification.md) | `c77863e445` / `403cc21b27` / `632005d430`（仅已实现子集）；`45d3fbfcb8`（第3A子集） |
| **G46.5** 脱敏：令牌、密码、API Key、Webhook Secret、Cookie、Authorization 头、数据库连接串、完整本地绝对路径（可配是否保留相对路径）、IP（可配掩码）必须脱敏；提供脱敏规则单测与扫描测试（在日志中搜索敏感样例必须为空）。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.5：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 部分完成：字段白名单、任务 ID/state/code 校验与真服务隐私检查；全部后续日志路径仍待验收；[第3A实际证据](jobs-verification.md) | `c77863e445` / `403cc21b27` / `632005d430` / `f21d156684`（仅已实现子集）；`45d3fbfcb8`（第3A子集） |
| **G46.6** 与追踪联动：日志携带 trace/span ID；关键路径（直投、扫描、NFO 写、Webhook 投递）串起完整链路；采样率可配且安全事件强制采样。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.6：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G46.7** 查询与可视化：日志字段规范化便于检索；提供常见查询示例（Loki/Grafana/ELK 示例在文档中）；`jelee-cli logs tail/filter/export`。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.7：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G46.8** 性能：异步/缓冲写避免阻塞请求路径；背压与丢弃策略明确（丢弃时计数并告警）；高 QPS 下日志不得成为瓶颈（有基准数据）；DEBUG 级别不得在生产默认开启。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.8：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G46.9** 保留与合规：保留期与容量上限可配；轮转与压缩；审计与安全日志保留期独立于普通日志；用户数据删除时相关日志处置策略写入文档。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.9：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G46.10** 验收：各类日志均有可复现用例与断言；脱敏扫描零命中；日志级别热调整生效；高 QPS 压测下 P95 无显著劣化；审计日志无法被关闭（尝试关闭有告警记录）。 | internal/platform/logging/; internal/platform/telemetry/; cmd/jelee-cli/; docs/logging.md | audit_logs/log_retention_settings | Plan-G46.10：敏感样例扫描零命中；组件级热调；审计不可关闭；轮转背压；INFO/DEBUG P95 对比；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G47 客户端管控：UA 屏蔽、标识屏蔽与访问策略

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G47.1** 识别维度：User-Agent、客户端应用名/版本（兼容层与自有 API 上报）、设备 ID、设备名、设备类型、IP/CIDR、API Key、请求头特征、可选 TLS/JA3 指纹（若实现需说明依赖与局限）。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.1：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G47.2** 规则模型：白名单/黑名单/优先级；精确匹配、前缀、通配、正则（正则需 ReDoS 审查与超时保护）、大小写策略；规则支持备注、生效时间窗、命中动作与命中计数。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.2：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G47.3** 命中动作：拒绝（返回明确错误码如 `client_blocked`，HTTP 403）、只读、限制库访问、限速、强制重新认证、仅记录不拦截（观察模式）、影子记录。观察模式需可在 UI 评估影响后再切拦截。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.3：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G47.4** 粒度：全局、按用户/用户组、按库、按客户端类型分别配置；规则冲突时优先级与合并策略明确（文档化并测试）。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.4：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G47.5** 已知客户端管理：识别到的客户端列表（名称、版本、UA、设备、最后活跃、最后 IP）可在管理页查看；支持重命名、标记可信、加入屏蔽、踢下线。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.5：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G47.6** 伪装与绕过：明确 UA 可伪造，规则应结合设备 ID/API Key/令牌；文档说明防护边界；提供“未知客户端默认策略”（允许/只读/拒绝/需管理员批准）。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.6：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G47.7** 管理员保护：管理员自身会话与本地环回诊断默认不受屏蔽影响（可配置）；防止规则误配导致全员无法登录（提供紧急恢复 CLI：`jelee-cli access reset-policies`）。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.7：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G47.8** 可观测：命中记录写入安全日志与统计（命中次数、Top UA、Top IP）；可导出命中明细；提供告警规则（异常 UA 暴增、批量被拒）。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.8：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G47.9** 隐私：被屏蔽请求的日志需脱敏；不得因屏蔽逻辑泄露其他用户信息或完整路径。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.9：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G47.10** 验收：每种匹配方式与动作均有测试；观察模式→拦截切换可评估；误配后紧急恢复成功；管理页可查看与操作；性能上规则匹配有缓存/编译，热路径开销可测（有基准）。 | internal/access/; internal/adapter/http/; cmd/jelee-cli/; web/src/features/access/ | client_policies/client_blocks/devices/audit_logs | Plan-G47.10：全部匹配/动作/作用域；观察转拦截；CLI 紧急恢复；UA 可伪造边界；匹配基准；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G48 媒体库访问与查看权限管理

- 验收：OpenAPI 与路由清单一致性检查通过；错误码表全覆盖且无重复/冲突；每类接口有契约测试；文档链接与关键示例 CI 通过；新功能未同步文档视为未完成。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G48.1** 授权模型：用户/用户组 ↔ 库（可见/不可见）；支持库级、目录根级、条目级（显式隐藏/允许）、分级（Parental Rating）、标签/类型级规则；规则优先级与冲突合并策略文档化。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.1：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 部分完成：基础用户↔库 ACL 和管理员分支已实现；组/目录根/条目覆写/分级/标签规则未实现；[实际证据](verification-report.md) | `632005d430` / `403cc21b27`（仅已实现子集） |
| **G48.2** 服务端强制：所有列表、搜索、详情、图片、字幕/音轨、播放信息、统计、Webhook 载荷都必须经过统一权限过滤器；禁止先查询再在展示层过滤。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.2：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 部分完成：当前列表/详情/媒体源在 SQL 强制过滤；图片/字幕/统计/Webhook 等接口尚未实现；[实际证据](verification-report.md) | `632005d430` / `403cc21b27`（仅已实现子集） |
| **G48.3** 隐藏语义：不可见条目不得出现在任何响应、搜索建议、最近添加、继续观看、合集、Playlist、图片 URL 与统计中；直接按 ID 访问返回 404（而非 403 暴露存在性，策略需可配并默认 404）。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.3：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 部分完成：当前隐藏条目和 source 都用 404，SQL 过滤测试通过；全部建议/合集/统计/图片响应面未实现；[实际证据](verification-report.md) | `632005d430` / `403cc21b27`（仅已实现子集） |
| **G48.4** 分级与内容控制：按分级、标签、关键字屏蔽；支持时间窗（如限制时段）；不同用户可不同分级上限；分级缺失时的默认策略可配。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.4：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G48.5** 设备与网络维度：可按设备类型、IP/CIDR、是否局域网限制库访问；与 G47 规则协同（明确优先级顺序）。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.5：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G48.6** 共享与来宾：支持受限共享链接/来宾用户（可设过期时间、只读、指定库、并发上限），所有共享访问可撤销并审计。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.6：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G48.7** 管理体验：库授权矩阵页面（用户 × 库批量勾选）、批量应用、模板（如“成人库”“儿童库”）、变更预览（影响多少条目/用户）、变更写审计。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.7：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G48.8** 性能：权限过滤必须在 SQL 层下推（索引、JOIN/EXISTS 条件），禁止全表捞取后内存过滤；提供查询计划证据与单请求 SQL 计数断言。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.8：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 部分完成：真实 10,002 条样本 ACL 用 items_library_id_idx，仅访问 1 条；完整接口 SQL 次数断言与规模/开销目标未完成；[实际证据](verification-report.md) | `632005d430` / `403cc21b27`（仅已实现子集） |
| **G48.9** 与开发者模式：dev 模式可临时关闭严格权限用于调试，但必须标记与自动恢复（G45.4/G45.7）。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.9：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G48.10** 验收：权限矩阵测试覆盖 用户 × 库 × 条目 × 分级 × 设备 × IP；越权直接访问返回 404；隐藏内容在全部接口零泄露（自动化遍历断言）；过滤下推有 `EXPLAIN` 证据；批量变更与撤销审计完整。 | internal/access/; internal/adapter/postgres/; internal/adapter/http/; web/src/features/access/ | library_acl/library_acl_overrides/groups/guest_grants | Plan-G48.10：用户×库×条目×分级×设备×IP；隐藏内容遍历零泄漏；SQL EXPLAIN/计数；批量撤销审计；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G49 API 与文档完善

- 验收：OpenAPI 与路由清单一致性检查通过；错误码表全覆盖且无重复/冲突；每类接口有契约测试；文档链接与关键示例 CI 通过；新功能未同步文档视为未完成。

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G49.1** 规范统一：全部自有 API 使用统一响应信封（数据/分页/错误/元信息）、统一错误码表（全局唯一、带 HTTP 映射与说明）、统一分页（cursor 优先 + offset 兼容）、统一排序/过滤白名单、统一时间与 ID 表示（RFC3339、字符串 ID）。 | internal/adapter/http/; api/; tools/openapi/; web/src/api/; docs/ | 各能力同源 schema；不重复定义接口数据 | Plan-G49.1：OpenAPI/路由/错误码同步；全部接口合同；TS/Go/curl 示例；文档链接；简中管理员文档；须单独覆盖本行全部约束 | 部分完成：基础列表/详情/error/page 使用统一 JSON 与 string UUID；全部能力信封、offset/排序/过滤尚未实现；[实际证据](verification-report.md) | `403cc21b27` / `c77863e445`（仅已实现子集） |
| **G49.2** 版本与弃用：`/api/v1`；弃用头与公告；`docs/api-deprecations.md` 记录时间线与替代方案；破坏性变更走 v2 并保留过渡期。 | internal/adapter/http/; api/; tools/openapi/; web/src/api/; docs/ | 各能力同源 schema；不重复定义接口数据 | Plan-G49.2：OpenAPI/路由/错误码同步；全部接口合同；TS/Go/curl 示例；文档链接；简中管理员文档；须单独覆盖本行全部约束 | 部分完成：基础 /api/v1 已实现；弃用头、时间线与 v2 过渡尚未实现；[实际证据](verification-report.md) | `403cc21b27` / `c77863e445`（仅已实现子集） |
| **G49.3** OpenAPI：3.1 规范由代码生成，CI 校验与实现一致；提供可浏览文档页与导出文件；示例请求/响应齐全（含错误示例）。 | internal/adapter/http/; api/; tools/openapi/; web/src/api/; docs/ | 各能力同源 schema；不重复定义接口数据 | Plan-G49.3：OpenAPI/路由/错误码同步；全部接口合同；TS/Go/curl 示例；文档链接；简中管理员文档；须单独覆盖本行全部约束 | 部分完成：有基础 OpenAPI 3.1 与浏览页；完整 schema/示例/生成同步门禁未完成；[实际证据](verification-report.md) | `403cc21b27` / `c77863e445`（仅已实现子集） |
| **G49.4** 调试台：开发者模式下提供 API 控制台（构造请求、查看响应、复制 cURL、查看 traceId），生产模式不可用。 | internal/adapter/http/; api/; tools/openapi/; web/src/api/; docs/ | 各能力同源 schema；不重复定义接口数据 | Plan-G49.4：OpenAPI/路由/错误码同步；全部接口合同；TS/Go/curl 示例；文档链接；简中管理员文档；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G49.5** SDK 与示例：生成/维护至少 TypeScript 客户端类型与一个 Go 示例；提供 curl 示例集；所有示例有 CI 校验（关键示例可跑通）。 | internal/adapter/http/; api/; tools/openapi/; web/src/api/; docs/ | 各能力同源 schema；不重复定义接口数据 | Plan-G49.5：OpenAPI/路由/错误码同步；全部接口合同；TS/Go/curl 示例；文档链接；简中管理员文档；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G49.6** 覆盖面：补齐 G00-G48 引入的全部能力接口——系统信息、用户与权限、库与条目、NFO 读写与校验、图片资产、字幕/音轨、播放信息（直投）、流式、会话与进度、统计、任务、Webhook、客户端策略、库 ACL、开发者模式状态、诊断与日志查询。所有接口必须有契约测试。 | internal/adapter/http/; api/; tools/openapi/; web/src/api/; docs/ | 各能力同源 schema；不重复定义接口数据 | Plan-G49.6：OpenAPI/路由/错误码同步；全部接口合同；TS/Go/curl 示例；文档链接；简中管理员文档；须单独覆盖本行全部约束 | 部分完成：当前 catalog/stream/system/health 合同测试通过；原文全部能力接口仍未实现；[实际证据](verification-report.md) | `403cc21b27` / `c77863e445`（仅已实现子集） |
| **G49.7** 文档体系：`README.md`、`docs/quickstart.md`、`docs/architecture.md` + ADR 集、`docs/domain-model.md`、`docs/nfo-compatibility.md`、`docs/image-assets.md`、`docs/api-reference.md`、`docs/permission-matrix.md`、`docs/compat-matrix.md`、`docs/developer-mode.md`、`docs/logging.md`、`docs/troubleshooting.md`、`docs/backup-restore.md`、`docs/perf-report.md`、`docs/security-model.md`、`docs/contributing.md`。 | internal/adapter/http/; api/; tools/openapi/; web/src/api/; docs/ | 各能力同源 schema；不重复定义接口数据 | Plan-G49.7：OpenAPI/路由/错误码同步；全部接口合同；TS/Go/curl 示例；文档链接；简中管理员文档；须单独覆盖本行全部约束 | 部分完成：现有审计/架构/快速开始/NFO/安全/性能/验证文档已落盘；全部要求文档与 ADR 集尚未齐全；[实际证据](verification-report.md) | `a512674643` / `721102c8d0` / `c77863e445` / `0bbd5939bb` / `f21d156684`（部分文档）；其余归属最终文档提交 |
| **G49.8** 文档质量门禁：链接检查、代码块可编译/可运行检查（关键片段）、中英日术语一致、与实现不一致即为 bug（CI 校验关键文档片段，如错误码表与路由清单）。 | internal/adapter/http/; api/; tools/openapi/; web/src/api/; docs/ | 各能力同源 schema；不重复定义接口数据 | Plan-G49.8：OpenAPI/路由/错误码同步；全部接口合同；TS/Go/curl 示例；文档链接；简中管理员文档；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G49.9** i18n 文档：面向管理员的关键文档至少提供简体中文；UI 四语齐全（G03）。 | internal/adapter/http/; api/; tools/openapi/; web/src/api/; docs/ | 各能力同源 schema；不重复定义接口数据 | Plan-G49.9：OpenAPI/路由/错误码同步；全部接口合同；TS/Go/curl 示例；文档链接；简中管理员文档；须单独覆盖本行全部约束 | 部分完成：当前管理员文档为简体中文，服务端四语错误已测；UI 四语尚未实现；[实际证据](verification-report.md) | `c77863e445` / `403cc21b27`（服务端四语）；管理员文档另行提交 |

## G50 诊断、自检与自愈

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G50.1** 自检：`jelee-cli doctor` 一键检查配置合法性、DB 连通与版本、迁移状态、库路径存在与权限、外部工具版本、磁盘空间与 inode、网络监听与代理头配置、隐私开关状态、开发者模式状态、日志与缓存目录可写、外部源连通性；输出可读报告与建议修复命令。 | internal/diag/; cmd/jelee-cli/; internal/adapter/http/; deploy/alerts/; docs/runbooks/ | 一致性问题、修复任务及不可关闭审计 | Plan-G50.1：10 类 doctor 故障；5 类数据问题；诊断脱敏；所有修复 dry-run/重跑/回滚；readyz；须单独覆盖本行全部约束 | 部分完成：doctor 配置/PG/schema3 实测；完整路径/工具/磁盘/网络诊断待交付；[第3A实际证据](jobs-verification.md)；第3B2固定工具身份诊断/开发tag不可达/素材CI入口有实际证据，完整子项仍待后续，见[3B2验证](process-verification.md) ；第3C1正式Linux隔离profile/固定runtime镜像/无DB诊断已有[实测](probe-verification.md)，完整子项仍部分完成 | `403cc21b27` / `632005d430`（仅已实现子集）；`45d3fbfcb8`（第3A子集） / `03a559524c`  / `27f66b1eb3` |
| **G50.2** 诊断包：`jelee-cli diag export` 导出脱敏诊断包（配置快照脱敏、最近日志片段、pprof、DB 统计与慢查询、任务状态、客户端与规则命中统计、库统计），可指定时间窗与大小上限。 | internal/diag/; cmd/jelee-cli/; internal/adapter/http/; deploy/alerts/; docs/runbooks/ | 一致性问题、修复任务及不可关闭审计 | Plan-G50.2：10 类 doctor 故障；5 类数据问题；诊断脱敏；所有修复 dry-run/重跑/回滚；readyz；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G50.3** 数据一致性检查器：定期或手动检测孤儿条目（文件不存在）、孤儿文件（未入库）、版本计数错误、播放统计漂移、图片记录与实际文件不一致、NFO 与条目不一致、外键/唯一约束异常、缓存与实际不符。 | internal/diag/; cmd/jelee-cli/; internal/adapter/http/; deploy/alerts/; docs/runbooks/ | 一致性问题、修复任务及不可关闭审计 | Plan-G50.3：10 类 doctor 故障；5 类数据问题；诊断脱敏；所有修复 dry-run/重跑/回滚；readyz；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G50.4** 自愈动作：重建条目、重建图片变体、重建探测与索引缓存、重算统计、清理孤儿记录、重新同步 NFO、修复计数；所有动作支持 `--dry-run` 预演（输出将影响的对象与数量），确认后执行并写审计。 | internal/diag/; cmd/jelee-cli/; internal/adapter/http/; deploy/alerts/; docs/runbooks/ | 一致性问题、修复任务及不可关闭审计 | Plan-G50.4：10 类 doctor 故障；5 类数据问题；诊断脱敏；所有修复 dry-run/重跑/回滚；readyz；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G50.5** 运行时自检：启动时校验关键不变量（如“转码路径不可达”“dev 模式状态”“权限过滤器已装配”），不满足则拒绝启动或明确告警；提供 `/readyz` 包含关键依赖状态。 | internal/diag/; cmd/jelee-cli/; internal/adapter/http/; deploy/alerts/; docs/runbooks/ | 一致性问题、修复任务及不可关闭审计 | Plan-G50.5：10 类 doctor 故障；5 类数据问题；诊断脱敏；所有修复 dry-run/重跑/回滚；readyz；须单独覆盖本行全部约束 | 部分完成：schema/配置/开发禁用/构造器依赖验证及 readyz 已测；全部运行不变量与未来模块装配未完成；[实际证据](verification-report.md) | `403cc21b27` / `632005d430`（仅已实现子集） |
| **G50.6** 告警与 runbook：为关键指标与事件提供默认告警规则与 runbook（磁盘满、DB 不可达、扫描连续失败、Webhook 死信堆积、被屏蔽客户端暴增、内存超阈值、dev 模式未关闭）。 | internal/diag/; cmd/jelee-cli/; internal/adapter/http/; deploy/alerts/; docs/runbooks/ | 一致性问题、修复任务及不可关闭审计 | Plan-G50.6：10 类 doctor 故障；5 类数据问题；诊断脱敏；所有修复 dry-run/重跑/回滚；readyz；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |
| **G50.7** 验收：`doctor` 在各类人为故障下给出正确诊断（注入 10 种故障）；`diag export` 产物脱敏（敏感扫描零命中）；一致性检查能发现注入的 5 类数据问题；所有自愈动作 dry-run 准确且执行后可回滚/可重跑。 | internal/diag/; cmd/jelee-cli/; internal/adapter/http/; deploy/alerts/; docs/runbooks/ | 一致性问题、修复任务及不可关闭审计 | Plan-G50.7：10 类 doctor 故障；5 类数据问题；诊断脱敏；所有修复 dry-run/重跑/回滚；readyz；须单独覆盖本行全部约束 | 阻塞：尚无本项完整实现及执行证据 | 未实现或未验证；无实现提交 |

## G51 工具链自安装与测试素材（允许 Agent 在项目目录下安装并使用工具）

| 子项 / 约束与工作 | 计划落盘文件 | 迁移 / 数据 | 测试计划与验收 | 状态 / 证据 | 实现提交 |
| --- | --- | --- | --- | --- | --- |
| **G51.1** 总原则：允许 Agent 在项目目录内自行安装开发/测试所需工具，但必须“清单化、可复现、可清理、不入 Git”；禁止污染用户系统环境（禁止全局 `npm i -g`、`go install` 写入用户 GOPATH/bin、系统级包管理器安装）；禁止安装到项目目录之外。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.1：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：Go SDK 和缓存限项目 .tools/.bin，安装安全测试通过；完整工具清单与全空环境重建未完成；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0`（仅已实现子集） / `3b9be6922c`（媒体开发工具子集） |
| **G51.2** 安装根目录：所有下载与解压落在 `.tools/`（按 `工具名/版本/平台-架构/` 分层），可执行文件与包装脚本链接到 `.bin/`；两者均被 `.gitignore` 忽略（G01.4）。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.2：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：当前固定 Go 按版本/平台安装到 .tools，包装器在 .bin 且被忽略；其余工具未实现；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0`（仅已实现子集） / `3b9be6922c`（媒体开发工具子集） |
| **G51.3** 清单文件：提交 `tools/manifest.toml`（或等价），逐条记录工具：名称、用途、版本（精确固定，禁止 latest）、来源 URL、平台/架构映射、SHA256 校验和、解压方式、相对安装路径、是否必需、许可证标识、归属说明。新增工具必须先改清单，禁止临时随手下载。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.3：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：manifest.json 固定 Go 版本/URL/SHA256/许可/路径；原文全部适用工具清单尚未齐全；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0`（仅已实现子集） / `3b9be6922c`（媒体开发工具子集） |
| **G51.4** 引导脚本：提供 `scripts/bootstrap-tools`（POSIX）与 `scripts/bootstrap-tools.ps1`（Windows），或统一 `make bootstrap`；实现：检测平台架构 → 读取清单 → 下载（仅 HTTPS，支持代理与镜像变量）→ 校验 SHA256（不符立即失败并清理）→ 解压到 `.tools/` → 生成 `.bin/` 包装脚本 → 写入 `.tools/.installed.json`（版本/时间/校验和）。脚本必须幂等、可离线复用缓存、失败可重跑。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.4：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：Windows/Linux bootstrap、hash、解压边界、幂等与离线已测；Linux 全空目录初次重建仍未完整证明；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0`（仅已实现子集） / `3b9be6922c`（媒体开发工具子集） |
| **G51.5** 版本固定与复现：Go 工具类优先用 `go run <module>@<version>` 配合 `tools/tools.go`（`//go:build tools`）纳入 `go.mod` 固定版本，避免散装安装；非 Go 工具走清单下载；所有命令在 Makefile 中通过 `PATH=$(PWD)/.bin:$(PATH)` 调用，保证优先使用本地固定版本。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.5：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：Go module 锁定依赖，Make/PS 包装本地 SDK 和缓存；完整辅助 Go 工具集未纳入；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0`（仅已实现子集） / `3b9be6922c`（媒体开发工具子集） |
| **G51.6** 工具清单（至少覆盖）：Go 工具链校验、`golangci-lint`、`gofumpt`/`gofmt`、`go-testcoverage` 或等价、`migrate`（golang-migrate）或 Atlas CLI、`sqlc`（如采用）、`swag`/OpenAPI 生成器、`buf`（若用 protobuf）、Node LTS 与包管理器（项目本地安装）、Playwright（浏览器驱动下载到 `.tools/`）、Testcontainers 依赖（若采用）、`ffmpeg`/`ffprobe`（仅测试素材生成与开发调试）、`mkvtoolnix`（mkvmerge/mkvpropedit）、`mediainfo`、`gosec`、`trivy` 或等价漏洞扫描、`lychee`/等价链接检查、`shellcheck`、`actionlint`（若用 GitHub Actions）。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.6：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：当前清单只完成 Go 与其 fmt/vet/test 能力；媒体/Node/Playwright/迁移 CLI/安全扫描等完整清单缺失；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0`（仅已实现子集） / `3b9be6922c`（媒体开发工具子集） |
| **G51.7** 与严禁转码的关系：开发工具（ffmpeg 等）属于**开发/测试期依赖**，不得被 Jelee 运行时依赖，不得进入生产镜像；`docs/toolchain.md` 明确区分“构建测试依赖”与“运行依赖”（运行依赖仅 ffprobe、mkvtoolnix 可选、mediainfo），并不得因本地存在 ffmpeg 而在生产代码路径启用转码（以断言测试保证）。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.7：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：新生产路径不调用 ffmpeg，当前镜像静态检查确认无转码工具/SDK；完整媒体运行工具清单与其他开发工具隔离仍未实现；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力；第3B2固定工具身份诊断/开发tag不可达/素材CI入口有实际证据，完整子项仍待后续，见[3B2验证](process-verification.md) ；第3C1正式Linux隔离profile/固定runtime镜像/无DB诊断已有[实测](probe-verification.md)，完整子项仍部分完成 | `c77863e445` / `0bbd5939bb`（原字节路径与镜像配方） / `3b9be6922c`（媒体开发工具子集） / `03a559524c`  / `27f66b1eb3` |
| **G51.8** 测试素材：禁止向仓库提交受版权保护媒体与大体量二进制；提供 `make fixtures` 用清单中的 ffmpeg 生成最小合成素材（短视频含音轨、多字幕轨、章节、多版本分辨率样本、损坏文件样本、极小图片、示例 NFO 集）到 `.testfixtures/`（被忽略）。素材生成脚本本身入库（`scripts/gen-fixtures`），生成产物不入库。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.8：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 已完成：双平台make/PowerShell生成13个原创小型短片/双分辨率/双音轨双字幕/章节/损坏媒体/小图/NFO集，SHA清单、原档不变与取消清理通过，产物全部忽略，见[素材](fixtures.md)和[3B2验证](process-verification.md) | `03a559524c`（第3B2子集） |
| **G51.9** 集成测试环境：PostgreSQL 优先用 Testcontainers（检测 Docker 可用时）；不可用时回退到清单中固定的嵌入式 PostgreSQL 二进制（安装到 `.tools/`，数据目录放 `.testdata/`，被忽略），保证无 Docker 环境也能跑集成测试；两种模式都要有明确日志说明处于哪种模式。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.9：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：专用 PostgreSQL 16.15 Docker 实测且仅操作自有 schema；Testcontainers 自动选择/固定嵌入式无 Docker 回退未实现；[实际证据](verification-report.md) | `632005d430`（集成测试）；Docker 环境为本次测试配置 |
| **G51.10** 缺失降级：外部工具缺失时相关测试必须**显式跳过并输出原因**，禁止静默通过或把跳过当作成功；CI 关键分支必须使用“完整工具链”模式并校验所有工具版本与清单一致（`make tools-verify`）。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.10：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：数据库无配置可见跳过，required 模式缺配置失败；Windows symlink/Linux FIFO 跳过单列；完整 CI 工具模式未完成；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力；第3B2固定工具身份诊断/开发tag不可达/素材CI入口有实际证据，完整子项仍待后续，见[3B2验证](process-verification.md) | `721102c8d0` / `632005d430` / `c77863e445`（入口与显式跳过） / `3b9be6922c`（媒体开发工具子集） / `03a559524c` |
| **G51.11** 许可证与合规：`tools/manifest.toml` 记录每个工具的许可证标识；`docs/THIRD-PARTY-TOOLS.md` 汇总工具名称、版本、许可证、用途（区分构建测试/运行）、上游链接与归属；分发说明中明确开发工具不随产物分发。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.11：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：已记录 Go 工具许可与用途；完整第三方工具表与全分发依赖审核尚未完成；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0` / `0bbd5939bb`（工具/许可文档） / `3b9be6922c`（媒体开发工具子集） |
| **G51.12** 安全：仅 HTTPS 下载并校验 SHA256；禁止执行来自下载包的安装脚本（仅解压取用二进制）；解压路径限制在 `.tools/` 内并防路径穿越（zip slip 防护）；不得将工具加入系统 PATH 或写入用户 shell 配置；卸载用 `make tools-clean`（删除 `.tools/`、`.bin/`、`.testfixtures/`、`.testdata/`）。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.12：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：当前 Go 下载 HTTPS/hash、防越界/恶意归档及本地清理测试通过；完整工具集卸载重建未验收；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0`（仅已实现子集） / `3b9be6922c`（媒体开发工具子集） |
| **G51.13** 平台支持：优先保证 Linux 与 Windows（当前开发环境为 Windows）可用，macOS 尽力；Windows 下需处理可执行文件扩展名、长路径、无 `make` 时的替代入口（提供 `scripts/make.ps1` 或等价 PowerShell 入口），并在文档中说明。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.13：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：Windows/Linux Go 引导与构建已实际运行；其他工具跨平台/macOS、Windows race 未完成；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0`（仅已实现子集） / `3b9be6922c`（媒体开发工具子集） |
| **G51.14** CI 集成：CI 步骤为 `make bootstrap` → `make tools-verify` → `make fixtures` → lint/build/test；CI 使用缓存键基于 `tools/manifest.toml` 哈希，避免每次重下；缓存失效时能完整重建。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.14：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：已提供基于清单缓存的 CI 基础入口；完整 fixtures→全 lint/test 与远端 CI 未执行；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力；第3B2固定工具身份诊断/开发tag不可达/素材CI入口有实际证据，完整子项仍待后续，见[3B2验证](process-verification.md) | `721102c8d0` / `0bbd5939bb`（引导与 CI 入口） / `3b9be6922c`（媒体开发工具子集） / `03a559524c` |
| **G51.15** 验收：全新克隆执行 `make bootstrap && make tools-verify && make fixtures && make test` 成功；`.tools/`、`.bin/`、`.testfixtures/`、`.testdata/` 全部未被 Git 跟踪（`make gitignore-check` 通过）；删除这些目录后能一键重建；校验和错误时安装失败而非静默继续；工具缺失时测试显式跳过而非假通过。 | tools/manifest.json; tools/tools.go; scripts/bootstrap-tools*; scripts/gen-fixtures*; Makefile; .gitignore; docs/toolchain.md | 仅 .testdata 本地测试数据库，不入 Git | Plan-G51.15：干净克隆 bootstrap/verify/fixtures/test；错 hash 拒绝；解压越界拒绝；缺工具显式跳过；Linux+Windows；所有本地产物未跟踪；须单独覆盖本行全部约束 | 部分完成：当前 Go 引导/verify/test/ignore 部分流程通过；全新 clone 完整 fixtures/媒体/浏览器/无 Docker 回退链未完成；[实际证据](verification-report.md)；第3B1双平台固定媒体工具、安装/版本/哈希/限额安全测试已通过，见[工具验证](media-tools-verification.md)，尚未覆盖全部工具与运行能力 | `721102c8d0`（仅已实现子集） / `3b9be6922c`（媒体开发工具子集） |

## G17 / G43 / G44 缺失原文

| 编号 | 落盘 | 测试 / 验收 | 状态 | 提交 |
| --- | --- | --- | --- | --- |
| G17 | requirements-clarifications.md | 取得原需求定义后才能拆分与验收 | 阻塞：原文没有该章节/子项，禁止虚构 | 无需求定义；无实现提交 |
| G43 | requirements-clarifications.md | 同上 | 阻塞：原文没有该章节/子项，禁止虚构 | 无需求定义；无实现提交 |
| G44 | requirements-clarifications.md | 同上 | 阻塞：原文没有该章节/子项，禁止虚构 | 无需求定义；无实现提交 |

## G01.4 逐组忽略执行清单

这些组为 G01.4 的独立检查单元，均由 gitignore-check 检查“没有被跟踪”和 git check-ignore 两种情况，规则文件存在不能代替实际结果。

| 子单元 | 必须覆盖 | 状态 / 验收 |
| --- | --- | --- |
| G01.4.build | bin、dist、node_modules、Go cache、Vite/TS cache | 部分完成：新增 Go 构建/缓存产物检查通过；前端产物与全历史待验证 |
| G01.4.tools | .tools、.bin、.venv、.cache、tools/vendor-downloads | 部分完成：本地 Go bootstrap 后新增工具目录未跟踪；完整工具集待验证 |
| G01.4.tests | coverage、out、reports、Playwright、test-results、.testfixtures、.testdata、test.db | 部分完成：当前覆盖率/测试临时目录检查通过；完整媒体/Playwright 素材链待实现 |
| G01.4.runtime | data 全部派生目录、log/logs | 部分完成：当前运行日志/测试 DB 目录已忽略；完整派生资产待实现，不得与用户媒体混置 |
| G01.4.secrets | .env/.env.*，保留 .env.example；pem/key/p12/credentials | 部分完成：新增密钥文件及 .env.example 例外规则已有；全历史扫描仍待完成 |
| G01.4.database | 本地卷、sqlite、dump | 部分完成：专用测试 DB 配置/生成物未跟踪；全部本地卷/备份路径待审核 |
| G01.4.editor | IDE、swap、Thumbs.db、desktop.ini、DS_Store；共享配置显式例外 | 部分完成：保留既有规则并补充说明；完整正反例和共享配置例外待核对 |
| G01.4.temp | .tmp/tmp/bak；NFO 备份单独策略 | 部分完成：NFO 备份忽略规则存在；NFO 写回与实际备份场景未实现 |
| G01.4.generated | 全部 Agent 下载/生成二进制、压缩包、素材、数据库 | 部分完成：新增本地生成物检查通过；历史二进制清单及完整素材待审核 |

## 性能与全局约束（不遗漏无 G 编号的原文要求）

| 验收 ID | 计划证据文件 | 工作与成功标准 | 状态 |
| --- | --- | --- | --- |
| GLOBAL.audit | 00-audit-baseline.md | 先审计再实施，保留精确 commit 与实际命令结果 | 已完成文件审计；基线可执行验证未执行 |
| GLOBAL.priority | requirements-clarifications.md / architecture.md | 安全数据完整性、禁转码优先；禁止写原媒体；保留版权、历史、回滚 | 部分完成：原字节/权限/禁转码/历史保留在当前增量已验证；全系统未验收 |
| GLOBAL.arch | architecture.md / internal/ / go.mod | Go 版本统一；fx 或 Wire；context/errgroup/有界并发；错误集中映射；环境优先配置；OTel+Prometheus | 部分完成：当前 Go/chi/fx/pgx 构建和真实服务已验证；OTel/Prometheus 与全模块未完成 |
| GLOBAL.security | security-model.md / 测试报告 | 所有文件路径安全；参数数组执行；密钥保护；SQL 参数化；NFO/XML/XSS；管理授权与审计 | 部分完成：当前路径/Host/转码/SQL ACL/错误脱敏已回归；全部模块安全回归未完成 |
| PERF.01 | perf-baseline.md / perf-report.md | 同机器/同配置 1 万视频冷/热列表，热 API P95≤200ms | 部分完成：10,002 行查询计划和单样本时间已有；冷热/P95 未验收 |
| PERF.02 | perf-baseline.md / perf-report.md | 100 文件增量与全库扫描，未变化项探测计数为零 | 阻塞：尚无实测 |
| PERF.03 | perf-baseline.md / perf-report.md | 冷/热详情延迟与缓存行为 | 阻塞：尚无实测 |
| PERF.04 | perf-baseline.md / perf-report.md | Direct Play 首字节 P95≤500ms，并发吞吐和原文件 hash | 部分完成：真实 HTTP 字节/hash 与微基准已有；TTFB P95/真实并发吞吐未验收 |
| PERF.05 | perf-baseline.md / perf-report.md | 前/后/连续 Seek P95≤1s，客户端关键帧定位+服务端 Range | 部分完成：Range/If-Range 合同通过；真实客户端 Seek 与延迟未验收 |
| PERF.06 | perf-baseline.md / perf-report.md | 100 并发进度，写入次数和吞吐 | 阻塞：尚无实测 |
| PERF.07 | perf-baseline.md / perf-report.md | 1 万 NFO 导入/导出、100 并发写及资产不损坏 | 阻塞：尚无实测 |
| PERF.08 | perf-baseline.md / perf-report.md | 10 万图片识别与缩放缓存命中率 | 阻塞：尚无实测 |
| PERF.09 | perf-baseline.md / perf-report.md | 扫描/探测/NFO/图片/直投/API 混合负载，无泄漏/竞争/死锁 | 部分完成：当前媒体/API/PG race 通过；完整混合压力尚未实现 |
| PERF.10 | perf-baseline.md / perf-report.md | Web 首屏 TTI P95≤1.5s，页面切换，bundle gzip 明确预算 | 阻塞：尚无实测及预算 |
| PERF.11 | perf-baseline.md / perf-report.md | 客户端匹配+SQL ACL 相比无规则基线开销≤10% | 部分完成：当前 SQL ACL 查询访问量已修正并验证；规则匹配/无规则对照/10% 门槛未验收 |
| PERF.12 | perf-baseline.md / perf-report.md | INFO/DEBUG 日志 P95 对比、无显著劣化；需预定“显著”阈值 | 阻塞：尚无实测及阈值 |
| PERF.cpu | perf-baseline.md / perf-report.md | 典型无转码负载 CPU 不高于基线，目标降低≥20% | 阻塞：尚无实测 |
| PERF.memory | perf-baseline.md / perf-report.md | 1万/10万/50万扫描、10万图片与≥24h 稳态曲线；明确部署内存预算 | 阻塞：尚无实测及预算 |
| DELIVERY.history | git-workflow.md / CHANGELOG.md | 单目标 Conventional Commits，可二分；不重写上游历史；作者 Carinoasd 已由用户确认 | 部分完成：保留完整基线历史；已记录分阶段提交并推送至草稿 PR；尚无发布标签与完整二分验收 |
| DELIVERY.release | requirements-traceability.md / 验证报告 | 原文完成判定全部满足，三类客户端记录、完整 CI、发行演练与精确提交 | 阻塞：当前不是完整产品交付 |

### G22 合併模式准入交易驗證

對應 G22.2／G22.3／G22.5：新增明確家族准入port、server固定身份與各自可用性；保留授權重送、父任務重試及新任務原子提交。正式app→准入→原生worker→來源報告五情境通過，完整PG race 252項頂層通過、零略過。[合同與證據](ignore-family-admission.md)。公開HTTP/CLI掃描及正式服務啟動仍待接線，需求維持部分完成。報告版本已提交e9f019f3f2（PR46）。

### G22 合併模式正式服務驗證

對應 G22.2／G22.3／G22.5：HTTP／CLI、預設關閉功能開關、固定 helper 健康檢查、動態准入與認領、停止後暫存清理已接線。正式Fx→真實登入→HTTP→PG→原生worker→來源報告及重啟關閉後保留重送已驗證，五項頂層通過、零略過。[服務合同與證據](ignore-family-runtime.md)。其他忽略格式、完整混合負載和規模穩定性仍待驗收，G22維持部分完成。

### G22 合併模式重掃驗收

正式 app→PG→原生 scanner→worker 六輪重掃已驗證，涵蓋固定mtime與同長內容變更、兩家族優先與來源報告、歷史排除保留及包含刷新。規則檔移除達50%門檻時完整保留基線；100%設定下正常發布並僅移除缺失控制檔記錄。原生回歸7項頂層通過、零略過。[合同與證據](ignore-family-rescan.md)。其他格式、取消與租約失效、混合負載及規模仍待驗收，G22維持部分完成。

### G22 合併模式取消與租約過期

正式准入→PG→原生scanner→worker驗證復核途中取消及舊租約失效後同任務接續。舊owner不能讀取進度、保存合法批次或發布；新owner attempts2成功，取消不更新基線，原fixture保持與childActive0。原生回歸9項頂層通過、零略過。[合同與證據](ignore-family-recovery.md)。活躍helper取消的正式服務完整鏈、混合負載與規模仍待驗收。

### G22 合併模式混合掃描

1,000與100媒體受保護容器、正式HTTP/app/PG/worker、真實原生probe與忽略helper混合NFO/影片/圖片已驗證。包含六輪cold/warm/受控替換、兩家族優先與来源報告、惡意排除無cache/baseline、取消恢復及SIGTERM join；原素材保持與自有測試資源清理。[合同與實測](ignore-family-mixed.md)。暖掃描仍完整兩次讀/hash納入NFO，零重新解析。其他歷史格式、活躍helper服務取消及壓力穩定性仍待驗收，G22維持部分完成。

### G22／G08.7／G09.4／G29 活躍忽略 helper 取消與停止

固定原生helper Active1時服务context取消後join且可重用；Close撤回可用性後等待active Evaluate，再清理。正式HTTP提交→真PG→正式worker觀察活躍child後Fx.Stop，無child/temp/owner/baseline發布殘留，jobqueued與原fixture保持。真PG/runtime7頂層通過、零略過，另Windows原生與相關Linux race通過。[合同與證據](ignore-family-active-child.md)。這是各需求的已驗證子集；HTTP工作取消旗標的活躍helper完整鏈、其他子程序／並發壓力及所有格式仍待驗收。

### G08.7／G09.4／G13.3／G22 HTTP 取消的本機通知

真實HTTP取消在active原生helper期間，原有DB旗標會擋發布但未中斷helper。新增授權提交後的選用本機通知port與runner有界世代註冊，保持DB/lease/finalpublish權威與跨實例原有fallback。預設設定下69ms完成，取消控制分支1／逾時0，服務健康、child/baseline/owner無殘留；8項真PG/runtime專項通過。[合同與證據](ignore-family-http-cancel.md)。完整PG回歸仍執行；跨實例即時通知、其他格式、壓力長穩與各需求全範圍未完成。

HTTP 本機取消修復完整回歸已完成：[完整 PG race 證據](evidence/ignore-local-cancellation-postgres.json) 252 頂層 pass、0 fail／skip、344.936 秒、來源保持。Windows 全 Go／probe tag、Linux 相關 race／vet／build亦通過；首次600秒逾時與期限校正時的人工中止證據保留，未省略測試。僅證明本次修復回歸，不代表 G22 或全案完成。

### G22 原生併發飽和驗收

正式兩額度服務8輪滿載，256次額外請求全部busy且未啟動子程序；16個活躍child取消、每輪重用結果保持、Peak2／TimedOut0、最後Active0與暫存清空。Windows／probe tag及Linux全部原生runtime9頂層pass。[合同與證據](ignore-family-saturation.md)。只證明短週期有界飽和，長穩與其他格式尚未完成。

### G08.7／G09.4／G13.3 跨實例工作取消

新增租約受保護的只讀旗標port，既有monitor一秒讀取、不增加心跳寫入／goroutine。兩個獨立正式runtime／HTTP／PG／native helper驗收，取消897ms、Cancelled1／TimedOut0、baseline0／owner0、來源與暫存保持；10原生頂層pass。完整PG race254頂層pass；相同素材上一runner反向失敗、恢復後全通過。[合同與證據](ignore-family-remote-cancel.md)。僅完成本子集，其他需求與G22長穩／歷史格式尚未完成。

### G09.4／G22.4 原生服務五分鐘穩定性

同一服務固定五分鐘連續兩額度飽和／busy／活躍child取消／正常重用／temp清理，Linux5043輪、Windows9399輪，0fail／skip／timeout／active，來源保持，goroutine2→2。父Go heap抽樣3.3MiB以下，非RSS。新增必要CI步驟與證據artifact，不能縮短時長或只跑空等待。[合同與證據](ignore-family-sustained.md)。混合媒體的測試建構接縫已有五分鐘證據；公開 New 全圖的持續負載與其他歷史格式仍待驗收，G22部分完成。

### G09.4／G22.4 混合媒體持續暖掃描

受保護的 1,000／100 檔容器各完成 303.903／300.392 秒連續暖掃描，共 20／170 輪；每輪完整 NFO read/hash、零重新解析／新增 probe、圖片 unchanged、來源與 quota 核對。取消恢復／SIGTERM join、原素材 hash、sourceDigest 與 owned cleanup 通過。[合同與逐輪證據](ignore-family-mixed-sustained.md)。沿用正式 worker 的測試建構接縫，公開 New 全圖持續負載仍待驗收；G22 其他格式未證明，維持部分完成。
