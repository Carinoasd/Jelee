# Jelee 渐进替换架构

## 决策与边界

采用需求已指定的 Strangler Fig 路线：在根目录新增独立 Go module。上游 .NET 服务源码已于 2026-10-04 移出工作树，以标签 `upstream-csharp-final` 保留，供行为比对时取回（见 [Git 与回滚流程](git-workflow.md)）。Go 服务不会透传请求给旧服务，因此旧服务的转码、下载与直播不会通过新入口重新出现。仅明确实现并测试的路由可用；未接管的能力返回 404。

首个增量采用 chi 路由、fx 生命周期、pgx/v5 连接池、golang-migrate 嵌入式迁移。配置启动校验、数据库连通及 schema 版本检查先于监听。数据库不使用 SQLite 回退（[ADR 0005](adr/0005-postgresql-only-state.md)），schema 版本政策见 [ADR 0006](adr/0006-migration-version-policy.md)。Go 版本统一为官方发布的 1.27.1。

目录责任：`internal/domain` 存放无 I/O 的模型；`internal/app` 定义目录用例与仓储端口；`internal/adapter` 实现 HTTP、数据库、媒体读取及 NFO 解析；`internal/platform` 管理配置、日志和服务生命周期；`cmd` 提供程序入口。

领域与应用层不得依赖适配器，架构测试检查这条依赖方向。当前 HTTP 与 PostgreSQL 适配器都直接依赖 `internal/adapter/media` 中的资源、解析器及错误契约；HTTP 还使用其传输实现。因此，适配器尚未完全解耦。`internal/platform/runtime` 通过 fx 装配服务，CLI 入口装配各自使用的适配器。

领域对象与主要数据表见[领域模型](domain-model.md)；各功能的当前状态以[需求追溯矩阵](requirements-traceability.md)为准。

## 数据与权限

会话令牌使用密码学随机数生成，只把 SHA256 摘要存入 PostgreSQL。会话类型由服务端持久化，Web 会话不能通过更换 User-Agent 获得直投权限。目录与流查询在 SQL 中通过统一权限过滤器下推过滤；没有权限的条目与不存在条目使用相同 404（[ADR 0003](adr/0003-unified-access-filter-in-sql.md)）。各角色可调用的路由见[权限矩阵](permission-matrix.md)。

原媒体以 `os.OpenRoot` 根目录句柄进行只读访问，拒绝逃逸。原文件 Range 投递由标准 HTTP 内容服务处理，不执行外部程序，不改变字节。生产转码、HLS/DASH 和 remux 能力均不实现，正式镜像不含 ffmpeg（[ADR 0007](adr/0007-no-encoder-direct-play-only.md)）。

## 错误与生命周期

所有外部错误使用固定错误码与可本地化的安全消息，不回传路径、连接串或原始数据库错误。日志不记录请求体、令牌、查询字符串与原始底层错误。HTTP 关闭先停止接受连接，再排空请求，最后关闭连接池。后台生命周期必须可取消。

## 验证与切换

编译、单元与契约测试完成后才记录通过。真实 PostgreSQL、Windows 与 Linux race、真实客户端、前端及性能验收分别记录；跳过不能计为通过。当前保留的上游代码使全仓品牌清理尚未完成；不添加整仓白名单掩盖此状态。

完整替换与一次性重写未采用：前者必须等全部契约和迁移完成，后者无法在缺少等价回归证据时保护既有能力。详见审计和逐项追溯矩阵。

## 掃描觸發生命週期

持久排程與目錄監看由 runtime 隨既有 worker 啟停。排程在短交易內選定到期定義並提交任務；監看使用持久租約、定義版號與根目錄世代，將事件合併成待處理世代，再以同一交易提交掃描及接受世代。兩者共用既有工作容量、管理員資格與掃描意圖驗證。停止服務會取消並等待觀察器結束，再停止工作執行器。詳細契約見 [排程](scan-schedules.md)及 [監看](scan-watch.md)。

## 文档地图

| 主题 | 文档 |
| --- | --- |
| 部署与上手 | [快速开始](quickstart.md)、[部署](deployment.md)、[初始设置](setup-wizard.md)、[备份与还原](backup-restore.md)、[故障排查](troubleshooting.md) |
| 领域与数据 | [领域模型](domain-model.md)、[存储布局](storage-layout.md)、[NFO 兼容性](nfo-compatibility.md)、[图片资产](image-assets.md) |
| API 与权限 | [API 参考](api-reference.md)、[权限矩阵](permission-matrix.md)、[访问控制](access-control.md)、[兼容矩阵](compat-matrix.md)、[安全模型](security-model.md) |
| 运维与可观测性 | [日志](logging.md)、[可观测性](observability.md)、[指标](metrics.md)、[开发者模式](developer-mode.md)、[性能报告](perf-report.md) |
| 开发 | [贡献指南](contributing.md)、[工具链](toolchain.md)、[质量门禁](quality-gates.md)、[Git 与回滚流程](git-workflow.md) |

## 架构决定记录（ADR）

编号 ADR 位于 `docs/adr/`，写法见[贡献指南](contributing.md#架构决定adr)。

| 编号 | 决定 | 状态 |
| --- | --- | --- |
| [0001](adr/0001-external-process-start.md) | 外部工具以 `os.StartProcess`／Windows Job 启动，而非 `os/exec` | 已采纳（E1） |
| [0002](adr/0002-no-redis-cache-boundary.md) | 不引入 Redis；缓存留在进程内存，跨实例协调用 PostgreSQL | 已采纳（E15） |
| [0003](adr/0003-unified-access-filter-in-sql.md) | 统一权限过滤器在 SQL 中下推，隐藏与不存在同样回应 | 已采纳 |
| [0004](adr/0004-compat-stricter-than-upstream.md) | 兼容层 `/compat` 比上游更严格（隐私优先） | 已采纳（E11） |
| [0005](adr/0005-postgresql-only-state.md) | PostgreSQL 是唯一的持久状态，不提供 SQLite 回退 | 已采纳（E13） |
| [0006](adr/0006-migration-version-policy.md) | 每个版本只接受一个确切的 schema，已发布迁移不可修改 | 已采纳（不可修改门禁待建） |
| [0007](adr/0007-no-encoder-direct-play-only.md) | 正式镜像不含 ffmpeg，服务只做原文件直投 | 已采纳 |
| [0008](adr/0008-setup-bootstrap-token.md) | 初始设置向导使用一次性引导令牌 | 已采纳（E14） |

前端技术选型另见[前端架构决策](frontend-adr.md)（未编号，2026-10-04 采用）。其他已由项目决定的事项（E1–E16）记录在[验证清单](owner-verification-queue.md)的 E 节。
