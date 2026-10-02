# 许可证与来源保留

Jelee 本仓库派生自 [Jellyfin](https://github.com/jellyfin/jellyfin)。审计基线为 `52a680c578f1af888ebb74cefcb89b736f9c5738`，原仓库历史与法定归属完整保留。新增 Go 实现不构成删除原许可证或改写原作者归属的理由。

## 已保留材料

- 根 `LICENSE`：GNU General Public License Version 2 全文，未修改。
- LICENSE SHA256：`f371b80469fb235bc500ec29e0e85b682d4a6157a158567d828ff0be544d4f1d`。
- `CONTRIBUTORS.md` 与原源码中的版权声明。
- `MediaBrowser.Providers/Plugins/ListenBrainz/Configuration/NOTICE.md`。
- `docs/upstream-README.md` 保存改写主页前的上游 README。
- 30090 个基线可达 Git 提交；无历史重写或强制推送。

来源、许可证、版权、兼容协议字段中出现 Jellyfin、Emby、MediaBrowser 时，应准确保留；它们与 Jelee 核心内部命名的替换范围不同。明确例外列入 `tools/brand-scan/allowlist.txt`，没有豁免整个旧源码树。

## 分发要求

分发本派生项目时保留适用许可证、版权与免责声明，标明修改，并依 GPL 条款向接收方提供相应源代码及构建所需材料。发布流程须核对实际采用的源码提供方式满足 [GPL v2](https://www.gnu.org/licenses/old-licenses/gpl-2.0.en.html)；不能只发布二进制并删除源代码获取说明。原文件存在更具体授权或第三方声明时，应分别保留和审查。

本阶段尚未发布二进制发行包或容器镜像，也没有完成全部旧依赖、Web 资产和新 Go 依赖的分发许可证审计。发行前还需生成完整依赖与许可清单，核对各文件适用授权、源码包可重建性及 Notice。当前文件只记录已确认的来源、保留措施和待完成工作，不代表发布合规审查已结束。

开发工具单独记录在 `docs/THIRD-PARTY-TOOLS.md`；本地 Go 工具链不随应用二进制分发。新增品牌图标或其他二进制须先进入 `docs/binary-allowlist.md`。

## 舊格式 regex 執行依賴

`github.com/dlclark/regexp2 v1.12.0` 為 MIT，Copyright (c) Doug Clark；完整授權保留於 `internal/platform/legacyignorehelper/LICENSE.regexp2`，版本及內容校驗由 go.mod/go.sum 固定。此項不代表其他依賴的整體發行審計已完成。

## 排程日曆依賴

`github.com/robfig/cron/v3 v3.0.1` 的完整授權保留於 `internal/adapter/calendar/LICENSE.cron`，版本與校驗值由 go.mod/go.sum 固定。僅使用日曆解析及下次時間計算；工作執行與持久交易由 Jelee 管理。

## 目錄通知依賴

`github.com/fsnotify/fsnotify v1.10.1` 使用 BSD 三條款授權，Copyright © 2012 The Go Authors 與 Copyright © fsnotify Authors；完整聲明保留於 `internal/adapter/scan/LICENSE.fsnotify`。go.mod/go.sum 固定版本及校驗值，Linux 觀察器使用此依賴。
