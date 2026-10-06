# 贡献指南

本文说明参与 Jelee 开发需要的工具链、常用 make 目标、合并前必须通过的质量门禁、提交规则，以及文档和架构决定的维护方式。命令的完整列表与工具版本以[工具链](toolchain.md)为准，各门禁的细节见[质量门禁](quality-gates.md)。

## 工具链

- 所有工具（Go 1.27.1、Node 与前端工具、golangci-lint、测试用的媒体工具）都安装在仓库内被 Git 忽略的 `.tools/` 等目录，版本、来源与 SHA-256 固定在 `tools/manifest.json`。不使用系统全局安装的 Go 或 Node。
- 首次准备：`make init`（Linux）或 `pwsh -File scripts/make.ps1 init`（Windows），然后 `make tools-verify` 校验完整性。媒体相关测试另需 `make bootstrap-media`、`make bootstrap-matroska` 等，见[工具链](toolchain.md)。
- PostgreSQL 集成测试需要一个隔离的测试数据库（多数集成测试要求数据库名为 `jelee_test`），用 `JELEE_TEST_DATABASE_URL` 指定；不要指向任何真实实例。没有数据库时这些测试报告 SKIP，不能把 SKIP 记为通过；`JELEE_REQUIRE_INTEGRATION=true` 时缺少数据库直接失败。
- 新增依赖：修改 `go.mod`、前端依赖或外部工具清单都属于需要记录的决定，必须同时更新许可证记录（[许可证与来源](LICENSE-COMPLIANCE.md)、[第三方工具](THIRD-PARTY-TOOLS.md)），见下文“架构决定”。

## 常用 make 目标

| 目标 | 用途 |
| --- | --- |
| `make build` | 生成 `bin/jelee`、`bin/jelee-cli`、`bin/jelee-migrate` |
| `make test` | `go test -count=1 ./...` |
| `make test-race` | 竞态检测（需要 C 编译器） |
| `make test-integration` | PostgreSQL 集成测试，必须设置 `JELEE_TEST_DATABASE_URL` |
| `make fmt`／`make fmt-check` | 格式化／检查 Go 源码 |
| `make lint` | `fmt-check`、`openapi-check`、`doc-check`、`go vet`、`golangci-lint` |
| `make openapi`／`make openapi-check` | 重新生成／校验 `api/openapi.json`，见 [API 参考](api-reference.md) |
| `make doc-check` | 文档链接、锚点与错误码引用检查 |
| `make coverage-check` | 按 `tools/coverage-thresholds.json` 检查覆盖率棘轮 |
| `make brand-scan-incremental` | 新增代码的品牌检查 |
| `make gitignore-check` | 检查是否误提交了生成物或禁止的文件 |
| `make migrate`、`make dev` | 迁移开发数据库、启动开发服务 |
| `make web-install web-types web-lint web-test web-build` | 前端依赖、类型（含 OpenAPI 类型是否过期）、ESLint 与四语检查、单元测试、构建与体积预算 |

Windows 使用 `pwsh -File scripts/make.ps1 <目标>`，目标名称相同（部分仅 Linux 的目标见[工具链](toolchain.md)）。

## 质量门禁

合并前 CI（`.github/workflows/jelee.yml`）必须全部通过，本机提交前至少运行与改动相关的部分：

| 门禁 | 要求 |
| --- | --- |
| 格式与静态检查 | `gofmt` 无差异；`go vet` 通过；golangci-lint 在 Linux 与 `GOOS=windows` 下都通过 `tools/lintgate`：基线 `tools/lint-baseline/` 只能减少，新代码不得有新发现 |
| 测试 | `make test`、相关包的 `-race`；改到 PostgreSQL 的代码还要运行对应的集成测试 |
| 覆盖率 | `make coverage-check`，不得低于棘轮最低值；补测试后可用 `make coverage-ratchet` 提高 |
| 基准 | 热路径基准由 CI 在同一机器上对比基线提交，刻意的回归需要登记在接受清单（见[质量门禁](quality-gates.md)） |
| 架构 | `go test ./internal/architecture/...`：`internal/domain`、`internal/app` 的非测试文件不得导入 `os`、`net`、`database/*` 或第三方包；直投与 HTTP 包不得出现创建进程的代码（[ADR 0007](adr/0007-no-encoder-direct-play-only.md)）；只有旧库读取器能导入 SQLite（[ADR 0005](adr/0005-postgresql-only-state.md)） |
| API 契约 | 改动 HTTP 路由或错误码时运行 `make openapi` 并提交 `api/openapi.json` 与前端类型 `web/src/api/schema.d.ts`；在 `internal/adapter/http/access_leak_test.go` 的 `leakRouteTable` 登记新路由；把新路由归入[权限矩阵](permission-matrix.md)的某一行 |
| 文档 | `make doc-check`；领域表变化时更新[领域模型](domain-model.md)并运行 `TestDomainModelDocument*`；错误码变化时重新生成 [API 参考](api-reference.md#错误码)的错误码表 |
| 品牌与忽略文件 | `make brand-scan-incremental` 零违规；`make gitignore-check` |
| 前端 | 四语资源键一致（`scripts/check-ui-locales.py`、`npm run i18n:check`），不允许在前端包中出现播放代码，主包体积在预算内 |

改动之后至少做一次**反向验证**：故意破坏被测试的行为，确认对应测试会失败，再恢复。

### 测试的资源限制

本机内存有限时，`go test` 加 `-p 1`；竞态测试只针对改动的包；PostgreSQL 测试加 `-parallel 2` 并用 `-run` 限定范围，避开 GB 级的配额测试。迁移相关测试不要假设某个迁移是最新版本，按名称用 `migrationVersion`／`downgradeAboveMigration` 定位（[ADR 0006](adr/0006-migration-version-policy.md)）。

## 提交规则

- 提交信息格式：`类型(范围): 摘要`，类型为 `feat|fix|refactor|perf|chore|docs|test|build|ci`，范围是模块名，例如 `feat(media): …`、`docs(adr): …`。摘要说明做了什么以及对应的需求编号（如 `G48.3`）。
- 一个提交只做一件事；需求追溯矩阵引用提交号，不要把不相关的提交挂到某个需求上。
- 不重写已推送的历史，不强制推送，不删除他人的分支。
- 作者署名统一为 `Carinoasd`（版权行写 `(C) 2026 Carinoasd`）。提交时用命令级配置指定身份，例如：

  ```sh
  git -c user.name=Carinoasd -c user.email=<GitHub noreply 地址> commit
  ```

  提交、文档与代码中不写真实姓名、个人邮箱或其他联系方式；不要从文件路径或系统账号推断作者名。
- 不提交密钥、测试数据库地址、账号密码或本机生成物；这些放在被忽略的 `.testdata/` 等目录。
- 遵循 [Git 与回滚流程](git-workflow.md)：上游同步在独立分支审阅后整合。

## 代码约定

- 代码注释使用英文，风格与周围代码一致。
- 所有外部错误使用固定错误码和可本地化的安全消息，不回传路径、连接串或底层错误；新错误码登记在 `errorCodeStatuses` 并提供四语消息。
- 日志只写白名单字段（见[日志](logging.md)），不记录令牌、请求体、查询字符串或完整路径。
- 新的目录查询必须通过统一权限过滤器（[ADR 0003](adr/0003-unified-access-filter-in-sql.md)）；新缓存要在[缓存边界](cache-boundaries.md)登记（[ADR 0002](adr/0002-no-redis-cache-boundary.md)）。
- 外部程序只能经由 `internal/platform/process.Runner` 启动（[ADR 0001](adr/0001-external-process-start.md)）。

## 文档

- 面向管理员的文档至少提供简体中文（G49.9）；新文档使用简体中文。
- 文档与实现不一致视为缺陷（G49.8）。关键文档由测试对照实现：[权限矩阵](permission-matrix.md)、[领域模型](domain-model.md)、[API 参考](api-reference.md)的分组与错误码表、[故障排查](troubleshooting.md)的诊断错误码。
- 新增文档后在相关文档中互相链接，并更新[需求追溯矩阵](requirements-traceability.md)对应行；修改追溯矩阵后运行 `python3 scripts/traceability_stats.py` 并用 `--check` 校验。
- 需求以[需求原文](requirements-source.md)为准。

## 架构决定（ADR）

影响多个模块、难以撤销、或偏离需求字面要求的决定，写成编号 ADR 放在 `docs/adr/`，并在[架构](architecture.md#架构决定记录adr)的索引中登记。ADR 包含：状态与日期、相关需求原文、决定、理由、代价、守门测试；需要修改已采纳的决定时，新写一篇 ADR 取代旧的，并在旧 ADR 的状态中注明。
