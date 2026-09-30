# 仓库审计基线

审计日期：2026-09-30（Asia/Taipei）。本文件记录新增 Go 实现前读取的上游快照；后续实现不能倒填为上游现有能力。文中旧项目名称只用于准确记录来源、文件名与法定归属。

## 1. 版本、历史与初始状态

| 项目 | 命令或文件证据 | 实际结果 |
| --- | --- | --- |
| 来源 | `git remote -v` | `origin https://github.com/MoYuanCN/Jelee.git`，读取时尚无 upstream |
| 基线提交 | `git log -1 --format='%H%n%s%n%cI'` | `52a680c578f1af888ebb74cefcb89b736f9c5738`，2026-09-29T18:12:51Z，Japanese translation update |
| 最近标签描述 | `git describe --tags --always` | `v12.0-rc7-207-g52a680c578`；不能当作已发布版本 |
| 程序程序集版本 | `SharedVersion.cs:3-4` | AssemblyVersion / AssemblyFileVersion 均为 `13.0.0` |
| 完整历史 | `git rev-list --count HEAD` | `30090` 个可达提交 |
| 初始工作区 | `git status --short` | 无输出，初始干净 |
| 子模块 | `git ls-tree HEAD .gitmodules`、目录清单 | 无 `.gitmodules`；`git submodule status` 在当前 Windows 沙箱因 Git sh 的 signal pipe 权限失败，不能以该命令声称验证成功 |

保留历史与 LICENSE；后续替换采用独立 Go 服务逐步接管。基线 C# 代码暂留不能被视为内部品牌纯净性已经完成，更不能用全仓库白名单隐藏欠账。

## 2. 技术栈与目录

| 范围 | 证据 | 结论 |
| --- | --- | --- |
| 后端构建 | `global.json`、`Jellyfin.Server/Jellyfin.Server.csproj` | C# / ASP.NET Core；SDK 基线 `10.0.0`、rollForward `latestMinor`；服务器目标 `net10.0`；输出文件名仍为 jellyfin |
| 依赖 | `Directory.Packages.props` | NuGet 中央版本管理；EF Core / Sqlite `10.0.11`、Serilog、Swashbuckle `10.2.3`、prometheus-net `8.2.1`、SkiaSharp、TMDbLib 等 |
| 服务入口 | `Jellyfin.Server/Program.cs`、`Jellyfin.Server/Startup.cs` | 宿主装配、ASP.NET 中间件、配置及服务生命周期 |
| API / 领域 | `Jellyfin.Api/`、`MediaBrowser.Controller/`、`MediaBrowser.Model/`、`Jellyfin.Data/` | 控制器、领域对象及 DTO 存在历史品牌耦合；兼容对象需显式映射到新的领域类型 |
| 应用服务 | `Emby.Server.Implementations/`、`Jellyfin.Server.Implementations/` | 扫描、会话、用户、任务与数据库配置等服务 |
| 媒体 / 元数据 | `MediaBrowser.MediaEncoding/`、`MediaBrowser.Providers/`、`MediaBrowser.XbmcMetadata/`、`Emby.Naming/` | 编码、探测、命名解析、NFO 读写；现有转码能力不能直接暴露给生产 Go 服务 |
| 可裁剪部分 | `Emby.Photos/`、`src/Jellyfin.LiveTv/`、`src/Jellyfin.MediaEncoding.Hls/` | 照片、直播和 HLS 仍是基线代码，服务器项目直接引用后两者 |
| 图片 / 网络 | `src/Jellyfin.Drawing*/`、`src/Jellyfin.Networking/` | Skia 图像实现与网络配置 |
| Web | `README.md` 的 Installing the Web Client | 主 Web 客户端由另一个仓库提供，本仓库没有 package.json 或完整 Web 源码；仅有服务器设置页和 API 文档静态资产。不能宣称已经从此仓库移除现成播放器 |

## 3. 数据、配置、插件与资产

- 数据库：`src/Jellyfin.Database/Jellyfin.Database.Providers.Sqlite/SqliteDatabaseProvider.cs:64-82` 配置 SQLite 与 EF Core；`src/Jellyfin.Database/readme.md` 记录 EF migration 命令。尚无 Go/pgx 或 PostgreSQL 迁移链。旧库迁移必须先针对此提交的 schema 做预检，不能假设所有上游版本相同。
- 配置：`Jellyfin.Server/Program.cs:363-388` 创建配置并读取 `JELLYFIN_` 环境前缀；`Helpers/StartupHelpers.cs` 分开解析 data/config/cache/web/log 路径。后续环境前缀应为 JELEE_，旧配置迁移应局限在迁移适配器。
- 插件：`Emby.Server.Implementations/Plugins/PluginManager.cs` 使用 CLR 插件实例、manifest、版本和启用/禁用状态；`MediaBrowser.Common/Plugins/` 提供接口。不能把现有 .NET 插件视为 Go 二进制兼容；需要单独的兼容范围或替代协议。
- NFO：`MediaBrowser.XbmcMetadata/Parsers/`、`Savers/`；XML 标签兼容是协议边界。原 NFO 无损更新与用户资产保护必须独立回归，不能仅通过新 XML parser 单测代替。
- 日志/指标：服务器项目引用 Serilog Console/File/Async/Graylog、Prometheus；尚不能证明 Go 所需 slog / OTel、脱敏及审计独立性。

## 4. 必须准确保留的旧忽略语义

证据主文件：`Emby.Server.Implementations/Library/DotIgnoreIgnoreRule.cs`；现有回归：`tests/Jellyfin.Server.Implementations.Tests/Library/DotIgnoreIgnoreRuleTest.cs`。基线实际使用 **`.ignore`**，未发现 `.jellyfinignore` 为扫描入口。

| 行号 | 基线行为 | 迁移测试要求 |
| --- | --- | --- |
| 59-89 | 对目录从其自身查找，对文件从父目录查找；未找到返回不忽略；空文件忽略所有 | 空文件、空白文件、无文件、文件与目录分别测试 |
| 148-218 | 向父目录遍历，采用最先找到的 `.ignore`；缓存结果。不会合并多个祖先文件 | 最近祖先优先、根边界、缓存清除测试；新的库边界策略须文档说明 |
| 224-245 | 规则缓存以文件修改时间与长度失效；文件消失时移除缓存 | 修改、删除、重建与扫描开始清缓存测试 |
| 248-306 | 解析符号链接目标、按行 Trim；由 `Ignore` 库解析规则，非法正则跳过；全部非法时按空文件忽略所有 | symlink 与逃逸规则必须明确；非法模式不能改变既有可见性而不报告 |
| 309-324 | Windows 反斜线规范化；目录添加结尾 `/`；向 Ignore 库传完整路径 | Windows 与 POSIX、目录通配、否定规则、空白与注释样例 |

`Directory.Packages.props:24` 锁定 `Ignore` 为 `0.2.1`。`LibraryManager.cs:1006-1007` 组合 resolver ignore rules；`:3429-3430` 对资产子项也应用 `.ignore`；`IO/LibraryMonitor.cs:383-389` 监听器同样检查内建模式与 `.ignore`。其他内建模式在 `Library/IgnorePatterns.cs`，不得只迁移点文件规则而遗漏它们。

## 5. 测试、CI、容器与许可证

- `git ls-files '*.csproj'` 列出 43 个项目，其中 `tests/` 有 17 个测试项目，`fuzz/` 有 2 个 fuzz 项目。主要使用 xUnit v3、Moq、AutoFixture、ASP.NET MVC Testing、Coverlet、SharpFuzz。
- `.github/workflows/ci-tests.yml` 在 Linux/macOS/Windows 上使用 SDK `10.0.x` 执行 `dotnet test Jellyfin.sln --configuration Release --collect:"XPlat Code Coverage" --settings tests/coverletArgs.runsettings --verbosity minimal`。另外存在 format、compat、CodeQL、OpenAPI 生成与 release bump 工作流。
- `.devcontainer/devcontainer.json` 与 `install-ffmpeg.sh` 是开发容器配置；`deployment/unraid/docker-templates/jellyfin.xml` 是已有部署模板。基线没有受跟踪的生产 Dockerfile，不能假设已有满足 G51 的 Jelee 镜像。
- 根 `LICENSE` 是 GNU GPL Version 2 全文；`CONTRIBUTORS.md` 和源码版权必须保留。另有 `MediaBrowser.Providers/Plugins/ListenBrainz/Configuration/NOTICE.md`。此处仅记录许可证材料，不替代发行前的完整依赖许可证审核。

## 6. 当前机器与验证边界

实际执行 `dotnet --list-sdks` 得到 `10.0.400 [C:\Program Files\dotnet\sdk]`。`Get-Command dotnet,go,docker,node,git -ErrorAction SilentlyContinue` 找到 dotnet、node、git，未找到当前 PATH 下的 go 或 docker；不推断其他安装路径不存在。

本审计仅运行上述只读 Git/文件/工具检测命令。**未运行基线 .NET 构建或测试，未启动数据库/容器，未进行性能采样，不能宣称这些项目通过。** 后续每个模块的真实执行结果应落到单独测试与性能报告，并回填追溯矩阵。网络依赖还原、工具校验与本地安装应遵守 G51。

## 7. 首阶段边界与未解决事项

1. 在保留此历史快照的前提下新增 Go 服务和合同测试，以显式功能开关接管；原服务的转码与下载能力不能被代理透传。
2. 优先建立只读媒体访问、禁止转码、Web 播放拒绝、权限过滤、NFO/图片资产保护的护栏。
3. 本地项目工具必须固定版本、HTTPS、SHA256、被 Git 忽略；现有系统 SDK 只读使用不等于项目工具链已可复现。
4. 原需求缺少 G17、G43、G44，编号保留为阻塞，不擅自补写；完整原文和解释见 requirements-source.md、requirements-clarifications.md。
5. 旧服务完整能力、真实第三方客户端互操作、旧 SQLite 数据迁移、长时间压力测试和发行证据在验证前均不标记完成。
