# 许可证与来源保留

Jelee 本仓库派生自 [Jellyfin](https://github.com/jellyfin/jellyfin)。审计基线为 `52a680c578f1af888ebb74cefcb89b736f9c5738`，原仓库历史与法定归属完整保留。新增 Go 实现不构成删除原许可证或改写原作者归属的理由。

## 已保留材料

- 根 `LICENSE`：GNU General Public License Version 2 全文，未修改。
- LICENSE SHA256：`f371b80469fb235bc500ec29e0e85b682d4a6157a158567d828ff0be544d4f1d`。
- `CONTRIBUTORS.md`（上游作者名单，未修改）。
- 原源码中的版权声明：上游 C# 树移出工作树后保存在 Git 历史与 `upstream-csharp-final` 标签；独立的声明与归属文件原文移至 `docs/legal/upstream/`，见下节。
- `docs/legal/upstream/listenbrainz-logo-NOTICE.md`（原 `MediaBrowser.Providers/Plugins/ListenBrainz/Configuration/NOTICE.md`，原文未改）。
- `docs/upstream-README.md` 保存改写主页前的上游 README。
- 30090 个基线可达 Git 提交；无历史重写或强制推送。

来源、许可证、版权、兼容协议字段中出现 Jellyfin、Emby、MediaBrowser 时，应准确保留；它们与 Jelee 核心内部命名的替换范围不同。明确例外列入 `tools/brand-scan/allowlist.txt`，没有豁免任何目录。

## 上游 C# 源码树移出工作树（2026-10-04）

依需求原文零.7（保留许可证、版权、上游 Git 历史与法定归属）、G28.1（C#/.NET 仅作迁移期参考）与当时的 G00.4（全仓品牌门禁零非白名单命中；2026-10-04 已调整为信息报告，旧名称可保留），工作树删除全部上游 C# 项目、测试、fuzz、部署模板及 .NET 专用 CI，取代 [需求澄清](requirements-clarifications.md) 中“保留原 C# 源码作历史比对与回滚”的旧决定。

- **历史与回滚**：删除前最后一个完整提交以标签 `upstream-csharp-final` 标记（由主线在合并时建立，指向本次分支起点）；原审计基线 `52a680c578f1af888ebb74cefcb89b736f9c5738` 及全部上游提交保持可达，没有改写历史。取回方式：`git show upstream-csharp-final:<路径>`、`git checkout upstream-csharp-final -- <路径>`，或 `git worktree add <目录> upstream-csharp-final`。
- **根 LICENSE 未动**：仍为 GPL v2 全文，SHA256 `f371b80469fb235bc500ec29e0e85b682d4a6157a158567d828ff0be544d4f1d`；`CONTRIBUTORS.md` 未动。
- **原文移存的声明**（`docs/legal/upstream/`，内容逐字节保留，仅改文件位置与名称）：
  - `listenbrainz-logo-NOTICE.md`：ListenBrainz 标志的 CC BY-SA 4.0 归属链。对应的 SVG 已随旧插件删除，Jelee 当前不分发该图档；声明保留作归属记录。
  - `naming-attribution.props.txt`：原 `Jelee.Naming/Attribution.props`（原套件作者 Jellyfin Contributors，`PackageLicenseExpression` 为 `GPL-3.0-only`）。
  - `naming-copyright.cs.txt`：原 `Jelee.Naming/Properties/Copyright.cs` 的 `AssemblyCopyright`。另外 14 个旧程序集的 `Properties/AssemblyInfo.cs` 带有逐字相同的版权字串，随树删除，原文仍在历史中。
  - `mit-dotnet-foundation-happy-eyeballs.txt`、`mit-gerald-barre-split-string.txt`：原 `src/Jelee.Networking/HappyEyeballs/HttpClientExtension.cs` 与 `src/Jellyfin.Extensions/SplitStringExtensions.cs` 文件头的 MIT 声明原文（文件头逐行截取）。对应程序码已删除，Jelee Go 程序不含这两段实现。
- **Go 端仍需要的资料**：四语 UI 字串由旧本地化目录移到 `web/src/i18n/<locale>/core.json`（G03.2 指定结构），只把两条字串中的旧产品名改为 Jelee 并把一个键名去品牌化；其余旧数据 Go 端未使用，未移转。

### GPL 义务在仅保留 Git 历史时的处理

以下只陈述本仓库现有事实与本文件已有的分发要求，不构成法律结论：

- 本文件“分发要求”一节不变：分发 Jelee 时保留适用许可证、版权与免责声明、标明修改，并依 GPL 条款提供相应源代码。删除旧树不改变这一要求；Jelee 自身仍在根 GPL v2 `LICENSE` 下。
- 旧 C# 程序在本仓库没有发布过二进制或容器镜像（见“分发要求”一节末段）；删除后 Jelee 构建产物也不再包含旧程序。若过去有人分发过旧程序的二进制，其源码可由本仓库 Git 历史与 `upstream-csharp-final` 标签取得。
- 工作树不再随附旧文件的逐文件版权头；这些头连同原文件在 Git 历史中完整保留。

**需要专案拥有者确认**（未确认前不得视为合规审查完成）：

1. 只以公开 Git 历史与标签提供旧 C# 源码，是否满足拥有者对上游 GPL 义务的理解（特别是仓库若改为私有、迁移或被 fork 时，历史可达性如何保证）。
2. Go 实现中是否有逐段移植自上游 C# 文件的程序（例如档名解析、忽略规则、NFO 读取的行为对照）；若有，需确认这些 Go 文件是否须带上对应上游文件的版权头。本次只核对到 Go 原始码没有引用旧树路径或复制旧程序码的标记，未做逐段比对。
3. 根 `LICENSE` 为 GPL v2，而原套件元数据（`naming-attribution.props.txt`）写的是 `GPL-3.0-only`；两者差异由来与 Jelee 对外宣告的授权版本需由拥有者决定。
4. ListenBrainz 归属声明在不再分发对应图档的情况下是否继续保留于 `docs/legal/upstream/`（目前保守保留）。


## 分发要求

分发本派生项目时保留适用许可证、版权与免责声明，标明修改，并依 GPL 条款向接收方提供相应源代码及构建所需材料。发布流程须核对实际采用的源码提供方式满足 [GPL v2](https://www.gnu.org/licenses/old-licenses/gpl-2.0.en.html)；不能只发布二进制并删除源代码获取说明。原文件存在更具体授权或第三方声明时，应分别保留和审查。

本阶段尚未发布二进制发行包或容器镜像，也没有完成全部新 Go 依赖与未来 Web 资产的分发许可证审计（旧依赖与旧 Web 资产已随 C# 树移出工作树）。发行前还需生成完整依赖与许可清单，核对各文件适用授权、源码包可重建性及 Notice。当前文件只记录已确认的来源、保留措施和待完成工作，不代表发布合规审查已结束。

开发工具单独记录在 `docs/THIRD-PARTY-TOOLS.md`；本地 Go 工具链不随应用二进制分发。新增品牌图标或其他二进制须先进入 `docs/binary-allowlist.md`。

## 舊格式 regex 執行依賴

`github.com/dlclark/regexp2 v1.12.0` 為 MIT，Copyright (c) Doug Clark；完整授權保留於 `internal/platform/legacyignorehelper/LICENSE.regexp2`，版本及內容校驗由 go.mod/go.sum 固定。此項不代表其他依賴的整體發行審計已完成。

## 排程日曆依賴

`github.com/robfig/cron/v3 v3.0.1` 的完整授權保留於 `internal/adapter/calendar/LICENSE.cron`，版本與校驗值由 go.mod/go.sum 固定。僅使用日曆解析及下次時間計算；工作執行與持久交易由 Jelee 管理。

## 目錄通知依賴

`github.com/fsnotify/fsnotify v1.10.1` 使用 BSD 三條款授權，Copyright © 2012 The Go Authors 與 Copyright © fsnotify Authors；完整聲明保留於 `internal/adapter/scan/LICENSE.fsnotify`。go.mod/go.sum 固定版本及校驗值，Linux 觀察器使用此依賴。

## 圖片縮放依賴

`golang.org/x/image v0.46.0` 使用 BSD 三條款授權，Copyright (c) 2009 The Go Authors；完整聲明保留於 `internal/adapter/images/LICENSE.x-image`，正式容器另附於 `/licenses/x-image/LICENSE`。版本與校驗值由 go.mod/go.sum 固定；本地圖片縮圖使用 `draw.ApproxBiLinear`（EXIF 方向以同一插值器的仿射 `Transform`），WebP／BMP／TIFF 解碼使用同一固定版本的 `webp`、`bmp`、`tiff` 子套件；JPEG／PNG／GIF 解碼與 JPEG 編碼使用固定 Go SDK。
