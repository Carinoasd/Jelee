# ADR 0005：PostgreSQL 是唯一的持久状态，不提供 SQLite 回退

- 状态：已采纳
- 日期：2026-10-06（追记；决定见 `632005d430`、`c77863e445`，SQLite 边界见 `a5971ee74a` 与 E13）
- 相关需求：G04.1、G04.6、G04.8（另见 G04.7 与 [ADR 0002](0002-no-redis-cache-boundary.md)）
- 相关代码：`internal/adapter/postgres/`、`internal/platform/config/config.go`（数据库 URL 校验）、`internal/adapter/legacydb/`、`cmd/jelee-cli/`
- 相关文档：[领域模型](../domain-model.md)、[存储布局](../storage-layout.md)、[备份与还原](../backup-restore.md)、[旧库迁移](../legacy-import.md)

## 需求原文

> G04.8 门禁：禁止 SQLite 生产回退；生产模式检测非 postgres 驱动即拒绝启动。
>
> G04.6 迁移工具：Jellyfin/旧 Jelee SQLite → PostgreSQL 工具，支持预检、断点续传、幂等、失败回滚、行数核对、校验和与迁移报告。

## 决定

1. **所有权威状态都在 PostgreSQL**：账号、会话、目录、元数据、权限规则、进度、任务、审计、设置。服务不写本机状态文件；本机目录只存放可以重建的缓存（图片变体、内嵌字幕与 OCR 缓存、临时文件），各自的创建者与清理者登记在[存储布局](../storage-layout.md)。
2. **数据库 URL 只接受 `postgres://` 或 `postgresql://`**，并且必须有主机与数据库名；其他 scheme（包括 `sqlite://`）在配置校验阶段就拒绝启动，不会尝试连接。
3. **SQLite 只用于读取旧库。** 驱动 `modernc.org/sqlite`（纯 Go、BSD 授权，E13 核准）只能由 `internal/adapter/legacydb` 引入，只链接进 `jelee-cli legacy-import`；服务端 `cmd/jelee` 与迁移工具 `cmd/jelee-migrate` 的任何导入链都不能到达它。
4. **跨实例协调也只用 PostgreSQL**（advisory lock、带 generation 的租约、版本列），见 [ADR 0002](0002-no-redis-cache-boundary.md)。

## 理由

- **一个数据源，一种备份。** 备份 Jelee 等于备份数据库、密钥与配置；还原后不需要再对齐本机文件。
- **约束由数据库保证。** 外键、CHECK、唯一约束与事务保证了目录、权限与进度的一致性（例如媒体库内一致的复合外键，见[领域模型](../domain-model.md)），统一权限过滤器也依赖 SQL 下推（[ADR 0003](0003-unified-access-filter-in-sql.md)）。为 SQLite 保留第二套实现会让这些保证分叉。
- **回退会掩盖配置错误。** 配错数据库 URL 时静默改用本机 SQLite，会让服务看似正常、数据却写到别处；直接拒绝启动更安全。

## 代价

- 单机试用也需要运行 PostgreSQL（官方 Compose 文件已包含）。
- 旧库迁移工具需要额外的依赖，因此把它限制在 CLI 中，并用架构测试守住边界。

## 守门

- `internal/platform/config/config_test.go`：`TestConfigurationFailsClosed` 包含 `sqlite:///` 等非 PostgreSQL URL 的拒绝用例。
- `internal/architecture/sqlite_boundary_test.go`：`TestSQLiteStaysOutOfTheServer` 检查只有 legacydb 能导入 SQLite 驱动、服务端与迁移工具的导入链不到达它，同时断言 `jelee-cli` 确实能到达，防止扫描器失效时静默通过。
- `TestDomainModelDocumentMatchesSchema` 在真实 PostgreSQL 上核对[领域模型](../domain-model.md)中的表与外键。
