# ADR 0006：迁移版本政策——每个版本只接受一个确切的 schema，已发布迁移不可修改

- 状态：已采纳（2026-10-06 加入迁移锁门禁）
- 日期：2026-10-06（追记；迁移框架见 `632005d430`）
- 相关需求：G04.2、G36.5、G37.4
- 相关代码：`internal/adapter/postgres/migrations/checksums.txt`（迁移锁）、`internal/platform/migrationlock/`、`tools/migrationlock/`、`internal/adapter/postgres/migrate.go`、`internal/adapter/postgres/store.go`（`SchemaVersion`、`Ready`）、`internal/adapter/postgres/readiness.go`、`cmd/jelee-migrate/`、`internal/adapter/postgres/migrations/`
- 运维步骤：[部署](../deployment.md)（“升级、滚动升级与回滚”一节）、[备份与还原](../backup-restore.md#schema-版本相容)

## 需求原文

> G04.2 迁移：`golang-migrate` 或 Atlas（二选一，需说明）；已发布迁移不可变；提供 up/down。
>
> G36.5 迁移纪律：迁移文件不可变、可回滚、向前兼容、大表变更在线策略。

## 决定

1. **工具**：golang-migrate，迁移以 `embed` 内嵌在二进制中，文件名为 `NNNNNN_<名称>.up.sql`／`.down.sql`，每个编号都有 up 与 down。
2. **确切版本匹配**：每个二进制只接受一个干净的 schema 版本（`SchemaVersion`）。版本较低、较高或处于 dirty 状态时，服务拒绝启动；`/readyz` 返回 `schema` 状态 `migration_required`、`newer` 或 `dirty`；`jelee-cli doctor` 报 `db_schema_newer`、`db_migration_dirty`。
3. **升级要停机迁移**：备份 → 停旧服务 → `jelee-migrate up` → 启动新版本。需要迁移的升级不支持新旧版本同时服务（不做零停机滚动）。
4. **降级是破坏性操作**：`jelee-migrate down --i-understand` 一次只退一级。多数 down 迁移在新增的数据仍然存在时拒绝执行（`RAISE EXCEPTION`），操作者需要先清除这些数据或改为恢复备份。升级前的完整备份是降级唯一可靠的途径。
5. **已发布的迁移不可修改**：修改 schema 只能新增编号更大的迁移。测试不得假设某个迁移是最新版，一律用 `migrationVersion`／`downgradeAboveMigration` 按名称定位。
6. **迁移锁**：“已发布”以迁移锁 `internal/adapter/postgres/migrations/checksums.txt` 为准，它按文件名记录每个迁移文件（up 与 down）的 SHA-256（`.gitattributes` 保证所有平台都是 LF，按原始字节计算）。锁定的文件不得修改、删除、改名或重新编号；新迁移的编号必须大于所有已锁定的编号，并用 `make migration-lock`（`go run ./tools/migrationlock -update`）追加，该命令只追加、从不改写已有条目，发现已锁定文件变化时拒绝执行。分支合并时只能重新编号**尚未进入目标分支锁文件**的迁移（同时改它自己的锁条目）。门禁有三层：`make lint` 中的 `migration-lock-check`、不需要数据库的 `TestMigrationLockCoversEmbeddedMigrations`（普通 `go test` 就会运行），以及 CI 的 `migration-lock` 作业——它以 PR 基准（推送时为推送前的提交）为 `-base`，要求基准锁文件的每个条目原样保留，因此同时改迁移和锁条目的 PR 也会失败。

## 理由

- **缓存与任务的生命周期契约随 schema 变化。** 探测缓存、NFO 写回、任务租约等表的语义在相邻版本之间并不兼容，让两个版本同时服务同一个数据库会互相破坏数据。只接受确切版本，把“不兼容”变成启动时就能发现的错误。
- **保留 down 但让它拒绝丢数据。** 需求要求可回滚；但静默丢弃新版本写入的数据比拒绝降级更糟，所以 down 在有数据时失败，并在[部署](../deployment.md)中说明以备份为准。
- **按名称而不是编号定位迁移**，让并行分支在合并时可以调整编号，而不需要改测试。

## 代价

- 需要迁移的升级必须停机；多实例部署要先全部停止。
- “向前兼容”（G36.5）不成立：旧二进制不能在新 schema 上运行。

## 未完成

- 迁移锁从 000001–000083 开始锁定（2026-10-06）。此前合并时曾重新编号（`5665a90f6c` 把目录同步迁移重编为 061），那些历史无法追溯校验。
- 完整的升级／回滚演练尚未进行（见[部署](../deployment.md)）。

## 守门

- 迁移锁：`make migration-lock-check`（`make lint` 的一部分）、`TestMigrationLockCoversEmbeddedMigrations`、CI `migration-lock` 作业（带基准提交）；工具自身由 `tools/migrationlock` 与 `internal/platform/migrationlock` 的测试覆盖（修改、删除、重新编号、插入较小编号、只追加）。

- `Store.Ready` 与启动流程：版本不符时关闭连接池并返回错误；`TestDiagnosticsIntegration` 覆盖未迁移、当前与 dirty 等情况。
- 各迁移的保留数据降级测试（`refuseRetainedDowngrade` 等）验证 down 在有数据时拒绝执行。
- `TestDomainModelDocumentMatchesSchema` 在迁移到最新版本后核对[领域模型](../domain-model.md)。
