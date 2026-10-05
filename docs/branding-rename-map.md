# 命名迁移映射

本文件记录从 Jellyfin / Emby / MediaBrowser 来源到 Jelee 的分阶段映射。新增 Go 服务独立装配，旧 C# 文件从未做全局文本替换；2026-10-04 起整棵上游 C# 树移出工作树，见文末“上游 C# 树移除”。

| 来源或旧标识 | 新标识 / 边界 | 状态 |
| --- | --- | --- |
| Jellyfin 服务入口与程序集 | `cmd/jelee`、`jelee` 可执行文件 | Go 基础实现已新增；旧程序集已移出工作树（标签 `upstream-csharp-final`） |
| 旧迁移入口 | `cmd/jelee-migrate` | 新 PostgreSQL up/down/status 已新增；旧 SQLite 导入未实现 |
| 旧管理 CLI | `cmd/jelee-cli` | 已提交 doctor、provision、import-video 和只读 nfo validate |
| `JELLYFIN_` 配置前缀 | `JELEE_` | 新服务只读新前缀；旧配置转换未实现 |
| 旧领域/Controller 命名 | `internal/domain`、`internal/app`、`internal/adapter` | 仅已迁移功能；旧 C# 核心已移出工作树 |
| Jellyfin/Emby 协议对象 | 未来隔离于 `internal/adapter/compat` | 兼容协议尚未实现 |
| 原 README | Jelee README；原文保存在 `docs/upstream-README.md` | 已完成主页来源分离 |
| 原法定作者、LICENSE、NOTICE | 保留原文 | 必须保留 |
| NFO 标签 | `internal/adapter/nfo` 提取字段并保留原文字节 | 只读 adapter 与校验 CLI 已提交并通过 Windows/Linux 测试；编辑序列化、批量/修复/任务与真实客户端往返未完成 |
| 旧客户端字段与资产名 | 未来由显式兼容 Mapper 保留协议写法 | 客户端协议与资产处理仍未实现 |

**2026-10-04 范围调整**：取消全仓改名要求（见[需求原文](requirements-source.md)“二、命名与兼容边界”的调整说明）。已完成的 Jelee 命名不改回；旧代码与兼容协议中的旧名称可以保留，不再为品牌一致性做全局替换或额外重构。

`make brand-scan` 扫描受跟踪与新增未忽略文本并输出报告，命中不影响退出码（只有扫描器自身错误才失败）；CI 的 “Branding report (informational)” 把清单写进 job summary。`--strict` 保留旧的阻挡行为，仅供本机自查。白名单以精确文件路径配置，结尾为 `/` 的条目整体豁免协议边界目录（目前只有 `internal/adapter/compat/`）。

`make brand-scan-incremental` 使用 `--new`，是 CI 中唯一阻挡合并的品牌检查，只覆盖仍明确要求 Jelee 命名的新服务（`cmd/`、`internal/`、`web/`、`deploy/`）与对外产品标识（`README.md`、`Dockerfile`、`go.mod`、`.env.example`）；文档、脚本、工具与 workflow 只在报告中列出。上游 C# 树移出工作树后，完整扫描为零非白名单命中（2026-10-04：14,736 → 0，白名单命中 195），但这只是当前状态，不再是验收条件。

## 倉庫管理與 OpenAPI 產物工作流程

移除原專用 commands、issue-stale、project-automation、pull-request-conflict 工作流程：它們使用上游專用 bot／token、看板 action、聯絡網站或限定來源倉庫條件，沒有本倉庫可驗證的管理服務。這些不是服務端功能契約或 CI 測試門禁。原始檔仍可由 Git 歷史恢復。

openapi-merge 保留 master／版本 push 的 OpenAPI reusable workflow 與產物生成，移除綁定上游伺服器的 SCP／SSH 發布 job。Jelee 部署與正式發布需另行驗收；未發布至其他系統。ci-tests、ci-format、ci-compat、ci-codeql-analysis、jelee 完整工作流程及 OpenAPI 生成器保持。

## 程式碼分析器模組

`src/Jellyfin.CodeAnalysis`、同名 csproj／程序集／namespace → `src/Jelee.CodeAnalysis` 與 `Jelee.CodeAnalysis`。根建置屬性的分析器載入／自引用排除、solution 專案名稱／路徑同步更新。診斷 JF0001 與原發布紀錄保持；編譯器正反例、完整 Debug 建置與模組格式檢查通過。[證據](analyzer-brand-rename.md)。

## 媒體檔名解析模組

Emby.Naming（含原套件 Jellyfin.Naming）→ Jelee.Naming；tests/Jellyfin.Naming.Tests → tests/Jelee.Naming.Tests。完整模組／引用／solution／版本腳本與 ABI base/head 路徑一起更新。原 Authors 與 AssemblyCopyright 分離到精確法律歸屬檔，內容保留；不豁免整個解析模組。命名701測試／完整17套件4098pass、21NotExecuted、0fail，源碼等價核對／Debug build／變更檔格式通過。[證據與尚未完成邊界](naming-brand-rename.md)。

## 網路模組

src/Jellyfin.Networking 與 tests/Jellyfin.Networking.Tests → src/Jelee.Networking／tests/Jelee.Networking.Tests，namespace／程序集／solution 與 consumer 引用同步；一般 log 與示例命名一併調整。舊探索 request 與舊配置 key 在精確 Compatibility/LegacyNetworkNames.cs 隔離；原 MIT 標頭保持。147 模組測試／完整4098pass、21NotExecuted通過，格式／source對應核對通過。[驗證及未完成 G05 邊界](networking-brand-rename.md)。

### 四語資源來源索引

以下角色對應四語階段的確切來源路徑；摘要保存於 `docs/evidence/ui-four-locales.json`。舊來源名稱集中在本既有改名映射文件，並未擴大程式碼或證據檔豁免。

| 證據角色 | 來源路徑 |
| --- | --- |
| localizationManager | `Emby.Server.Implementations/Localization/LocalizationManager.cs` |
| serverStartup | `Jellyfin.Server/Startup.cs` |
| serverConfiguration | `MediaBrowser.Model/Configuration/ServerConfiguration.cs` |
| localizationManagerTests | `tests/Jellyfin.Server.Implementations.Tests/Localization/LocalizationManagerTests.cs` |
| catalogGate | `scripts/check-ui-locales.py` |
| foundationWorkflow | `.github/workflows/jelee.yml` |
| makefile | `Makefile` |
| englishCatalog | `Emby.Server.Implementations/Localization/Core/en-US.json` |
| japaneseCatalog | `Emby.Server.Implementations/Localization/Core/ja-JP.json` |
| simplifiedChineseCatalog | `Emby.Server.Implementations/Localization/Core/zh-CN.json` |
| traditionalChineseCatalog | `Emby.Server.Implementations/Localization/Core/zh-TW.json` |

### 四語 HTTP 來源索引

| 證據角色 | 來源路徑 |
| --- | --- |
| startup | `Jellyfin.Server/Startup.cs` |
| headerProvider | `Jellyfin.Server/Localization/FourLocaleRequestCultureProvider.cs` |
| httpTests | `tests/Jellyfin.Server.Integration.Tests/Middleware/FourLocaleRequestCultureTests.cs` |

### 伺服器 UDP 探索裁剪來源索引

| 證據角色 | 來源路徑 |
| --- | --- |
| startup | `Jellyfin.Server/Startup.cs` |
| schemaFilter | `Jellyfin.Server/Filters/AdditionalModelFilter.cs` |
| retiredConfiguration | `MediaBrowser.Common/Net/NetworkConfiguration.cs` |
| legacyEnvironmentBoundary | `src/Jelee.Networking/Compatibility/LegacyNetworkNames.cs` |
| discoveryRemovalTests | `tests/Jellyfin.Server.Integration.Tests/DiscoveryRemovalTests.cs` |
| autoDiscoveryHost | `src/Jelee.Networking/AutoDiscoveryHost.cs` |
| discoveryResponseModel | `MediaBrowser.Model/ApiClient/ServerDiscoveryInfo.cs` |

### 錄製啟動裁剪來源索引

| 證據角色 | 來源路徑 |
| --- | --- |
| startup | `Jellyfin.Server/Startup.cs` |
| recordingsManager | `src/Jellyfin.LiveTv/Recordings/RecordingsManager.cs` |
| regressionTests | `tests/Jellyfin.Server.Integration.Tests/RecordingStartupRemovalTests.cs` |
| recordingsHost | `src/Jellyfin.LiveTv/Recordings/RecordingsHost.cs` |
| recordingNotifier | `src/Jellyfin.LiveTv/Recordings/RecordingNotifier.cs` |

### 舊 HTTP 直播與頻道入口裁剪來源索引

| 證據角色 | 來源路徑 |
| --- | --- |
| startup | `Jellyfin.Server/Startup.cs` |
| compatibilityMiddleware | `Jellyfin.Api/Compatibility/RemovedFeaturesMiddleware.cs` |
| httpTests | `tests/Jellyfin.Server.Integration.Tests/Controllers/RemovedFeaturesTests.cs` |
| englishCatalog | `Emby.Server.Implementations/Localization/Core/en-US.json` |
| japaneseCatalog | `Emby.Server.Implementations/Localization/Core/ja-JP.json` |
| simplifiedChineseCatalog | `Emby.Server.Implementations/Localization/Core/zh-CN.json` |
| traditionalChineseCatalog | `Emby.Server.Implementations/Localization/Core/zh-TW.json` |
| liveTvController | `Jellyfin.Api/Controllers/LiveTvController.cs` |
| channelsController | `Jellyfin.Api/Controllers/ChannelsController.cs` |
| programsDto | `Jellyfin.Api/Models/LiveTvDtos/GetProgramsDto.cs` |
| channelMappingDto | `Jellyfin.Api/Models/LiveTvDtos/SetChannelMappingDto.cs` |
| oldEnablingTests | `tests/Jellyfin.Server.Integration.Tests/Controllers/LiveTvControllerTests.cs` |


### 直播與頻道自動工作裁剪來源索引

| 證據角色 | 來源路徑 |
| --- | --- |
| tunerHostManager | `src/Jellyfin.LiveTv/TunerHosts/TunerHostManager.cs` |
| listingsManager | `src/Jellyfin.LiveTv/Listings/ListingsManager.cs` |
| tunerInterface | `MediaBrowser.Controller/LiveTv/ITunerHostManager.cs` |
| existingListingsTests | `tests/Jellyfin.LiveTv.Tests/Listings/ListingsManagerTests.cs` |
| actorRemovalTests | `tests/Jellyfin.Server.Integration.Tests/LiveFeatureActorsRemovalTests.cs` |
| englishCatalog | `Emby.Server.Implementations/Localization/Core/en-US.json` |
| japaneseCatalog | `Emby.Server.Implementations/Localization/Core/ja-JP.json` |
| simplifiedChineseCatalog | `Emby.Server.Implementations/Localization/Core/zh-CN.json` |
| traditionalChineseCatalog | `Emby.Server.Implementations/Localization/Core/zh-TW.json` |
| guideTask | `src/Jellyfin.LiveTv/Guide/RefreshGuideScheduledTask.cs` |
| channelTask | `src/Jellyfin.LiveTv/Channels/RefreshChannelsScheduledTask.cs` |
| channelCleanup | `src/Jellyfin.LiveTv/Channels/ChannelPostScanTask.cs` |
| liveMediaProvider | `src/Jellyfin.LiveTv/LiveTvMediaSourceProvider.cs` |
| channelMediaProvider | `src/Jellyfin.LiveTv/Channels/ChannelDynamicMediaSourceProvider.cs` |
| channelImageProvider | `src/Jellyfin.LiveTv/Channels/ChannelImageProvider.cs` |

### DTO直播相依解耦來源索引

| 證據角色 | 來源路徑 |
| --- | --- |
| generalDto | `Emby.Server.Implementations/Dto/DtoService.cs` |
| dtoTests | `tests/Jellyfin.Server.Implementations.Tests/Dto/DtoServiceTests.cs` |
| imageInheritanceTests | `tests/Jellyfin.Server.Implementations.Tests/Dto/DtoServiceImageInheritanceTests.cs` |
| viewManager | `Emby.Server.Implementations/Library/UserViewManager.cs` |
| dependencyRegistration | `Emby.Server.Implementations/ApplicationHost.cs` |
| partsAssembly | `Jellyfin.Server/CoreAppHost.cs` |
| featureRegistry | `src/Jellyfin.LiveTv/Extensions/LiveTvServiceCollectionExtensions.cs` |

### 使用者視圖直播相依解耦來源索引

| 證據角色 | 來源路徑 |
| --- | --- |
| viewManager | `Emby.Server.Implementations/Library/UserViewManager.cs` |
| viewApi | `Jellyfin.Api/Controllers/UserViewsController.cs` |
| localViewTests | `tests/Jellyfin.Server.Implementations.Tests/Library/UserViewManagerRetirementTests.cs` |
| httpViewTests | `tests/Jellyfin.Server.Integration.Tests/Controllers/UserViewRetirementTests.cs` |
| dependencyRegistration | `Emby.Server.Implementations/ApplicationHost.cs` |
| partsAssembly | `Jellyfin.Server/CoreAppHost.cs` |
| featureRegistry | `src/Jellyfin.LiveTv/Extensions/LiveTvServiceCollectionExtensions.cs` |

## G05 核心註冊解耦來源索引

- dependencyRegistration: `Emby.Server.Implementations/ApplicationHost.cs`
- partsAssembly: `Jellyfin.Server/CoreAppHost.cs`
- featureRegistry: `src/Jellyfin.LiveTv/Extensions/LiveTvServiceCollectionExtensions.cs`
- startupTests: `tests/Jellyfin.Server.Integration.Tests/LiveCoreRegistrationRemovalTests.cs`

## G05 指南核心裁剪來源索引

- guideImplementation: `src/Jellyfin.LiveTv/Guide/GuideManager.cs`
- guideInterface: `MediaBrowser.Controller/LiveTv/IGuideManager.cs`
- retainedListingSource: `src/Jellyfin.LiveTv/Listings/SchedulesDirect.cs`
- etagEncodingComment: `src/Jellyfin.LiveTv/Listings/XmlTvProgramEtag.cs`
- etagRegressionComment: `tests/Jellyfin.LiveTv.Tests/Listings/XmlTvProgramEtagTests.cs`
- compiledCoreAndStartupTests: `tests/Jellyfin.Server.Integration.Tests/LiveCoreRegistrationRemovalTests.cs`

## G11 出站請求來源索引

- namedHttpRegistration: `Jellyfin.Server/Startup.cs`
- hostnameSocketConnect: `src/Jelee.Networking/HappyEyeballs/HttpClientExtension.cs`
- sdkMetadataClient: `MediaBrowser.Providers/Plugins/Tmdb/TmdbClientManager.cs`
- imageDownloader: `MediaBrowser.Providers/Manager/ProviderManager.cs`
- packageDownloader: `Emby.Server.Implementations/Updates/InstallationManager.cs`
- localTunerClient: `src/Jellyfin.LiveTv/TunerHosts/HdHomerun/HdHomerunHost.cs`
- playlistClient: `src/Jellyfin.LiveTv/TunerHosts/M3uParser.cs`
- jobsControlClient: `cmd/jelee-cli/jobs.go`
- nfoControlClient: `cmd/jelee-cli/nfo_jobs.go`
- dependencyVersions: `Directory.Packages.props`

## ABI 遷移契約的精確來源身份

`tools/abi/expected-breaks.json` 只記錄舊程序集與公開符號在遷移比較中的精確身份，以及G00／G05／G11.5／G28對應；`scripts/fixtures/abi-legacy-report.json` 保留來源CI輸出作門禁反例。兩者按精確檔名加入品牌掃描白名單，不豁免任何實作目錄。

命名模組增加固定的真實Jelee基準：`202b813a955cfc64ab87992484bf00b8aa72221a`，原八組比較與52條歷史差異仍完整保留。後續未核准的Jelee命名API差異依新組件比較失敗；詳見[ABI門禁](abi-report-check.md)。

## 上游 C# 樹移除（2026-10-04）

依需求原文零.7／G28.1／G00.4，取代“保留原 C# 源碼”的舊決定（理由見[需求澄清](requirements-clarifications.md)）。刪除前最後完整提交以標籤 `upstream-csharp-final` 標記，回滾與比對一律從該標籤取回。

| 來源 | 處理 |
| --- | --- |
| 根目錄 14 個舊程序集目錄、`src/`、`tests/`、`fuzz/`、`deployment/` | 刪除（2,472 個跟蹤檔） |
| 舊 solution、`Directory.Build.props`／`Directory.Packages.props`、`global.json`、`nuget.config`、`stylecop.json`、`BannedSymbols.txt`、`SharedVersion.cs`、舊 code-workspace、`.config/dotnet-tools.json`、`bump_version` | 刪除 |
| 舊本地化目錄四語 `Core/<locale>.json` | 移至 `web/src/i18n/<locale>/core.json`；`StartupEmbyServerIsLoading` → `StartupServerIsLoading`，`NewVersionIsAvailable` 與該鍵字串中的舊產品名 → Jelee |
| `scripts/check-ui-locales.py` | 改讀 `web/src/i18n/<locale>/*.json` |
| ListenBrainz `NOTICE.md`、命名模組 `Attribution.props`／`Properties/Copyright.cs`、兩段 MIT 檔頭 | 原文移至 `docs/legal/upstream/`，精確檔名列入品牌白名單 |
| `src/Jelee.Networking/Compatibility/LegacyNetworkNames.cs`、`tools/abi/expected-breaks.json`、`scripts/fixtures/abi-legacy-report.json` | 隨樹／ABI 門禁刪除，自白名單移除 |
| ABI 門禁（`tools/abi/`、`scripts/check-abi-report.py`、`scripts/test_abi_guard.py`、`ci-compat`） | 刪除；[紀錄](abi-report-check.md)標為已退役 |
| `.github/workflows` 的 ci-tests、ci-format、ci-compat、openapi-generate／merge／pull-request／workflow-run、issue-template-check、pull-request-stale、release-bump-version | 刪除（.NET 建置或上游專用 bot／token）；Go 的 OpenAPI 門禁已在 `make lint`（`openapi-check`） |
| `ci-codeql-analysis` | 語言 csharp → go，以 `make bootstrap build` 手動建置 |
| `.github/CODEOWNERS`、`renovate.json` | 刪除（上游個人／組織與 dotnet 預設） |
| Issue／PR 範本 | 改寫為 Jelee 中性內容 |
| `.devcontainer/`、`.vscode` 的 launch／tasks／settings | 刪除（.NET 開發容器與偵錯設定）；`extensions.json` 改推薦 Go 擴充 |
| `.gitignore`、`.dockerignore`、`.editorconfig` | 移除 .NET／舊產品專用規則；`.tools` 改為不帶斜線以涵蓋 worktree 的符號連結 |
| `docs/owner-verification-queue.md` E2 | 舊忽略檔名改寫為「G22.2 所列兩種上游舊品牌忽略檔」 |
