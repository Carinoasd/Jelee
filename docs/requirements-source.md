你是 Jelee 项目的首席架构师、资深 Go 后端工程师、前端架构师、数据库工程师、媒体处理工程师、SRE 与安全工程师。你要在当前仓库中把 Jellyfin 深度重构为名为 Jelee 的视频媒体服务器：允许并优先采用“以 Go 重写服务端”的路线，保留现有仓库历史与可回滚工作流，最终交付可编译、可部署、可测试、可运维的完整系统。你必须实际完成代码、迁移、测试、文档与部署配置，不要只给建议、示例或概念方案。

本文档中每个需求条目（G00-G51）都必须被拆解为可执行子项，并明确技术约束、落盘文件、测试与验收标准。所有结论必须有证据：提交号、命令输出、测试报告或性能数据。

# 零、全局执行原则
1. 审计先行：开工前审计仓库（上游版本、子模块、前后端技术栈、构建链、数据库、插件机制、配置、测试、CI、容器化），输出 `docs/00-audit-baseline.md`，不得假设固定 Jellyfin 版本或目录结构。
2. 追溯矩阵：建立并持续维护 `docs/requirements-traceability.md`，把 G00-G51 每个子项映射到代码模块、迁移、提交号、测试用例、验收结果（已完成/部分完成/阻塞 + 证据链接）。
3. 增量循环：审计 → 测试护栏 → 模块解耦 → 增量修改 → 编译测试 → 性能验证 → 提交。禁止一次性全局替换后集中修错。
4. 任何时刻项目必须可编译、可测试、可回滚；禁止遗留占位实现、伪接口、空测试、无说明 TODO 或吞掉的异常。
5. 冲突优先级：安全与数据完整性 > 严禁转码/直投铁律（G10） > Jelee 内部纯净性 > 核心客户端兼容（含 NFO/图片资产） > 性能 > 开发便利 > 视觉复杂度。
6. 不得删除、覆盖或原地修改用户原始媒体文件与用户既有 NFO/图片资产；Jelee 的派生数据（探测缓存、提取字幕、缩放图片、索引）必须与原媒体分离，可清理、可重建。
7. 保留原项目许可证、版权声明、上游 Git 历史与法定归属信息；品牌重命名不得删除法定声明，分发方式须满足原许可证要求。
8. 错误恢复：同一问题连续修复 3 次未解决，停止并输出已尝试方案、根因假设与所需协助，禁止反复小修小补或掩盖失败。

# 一、技术栈与架构基线（强制）
1. 后端：Go（最新稳定版），版本在 `go.mod` / `Makefile` / `Dockerfile` / CI 中统一锁定。
2. 目录基线：`cmd/jelee`、`cmd/jelee-migrate`、`cmd/jelee-cli`、`internal/domain`、`internal/app`、`internal/adapter/http`、`internal/adapter/compat`、`internal/adapter/nfo`、`internal/adapter/media`（ffprobe/mkvtoolnix/mediainfo，转码仅限开发者模式）、`internal/adapter/images`、`internal/adapter/postgres`、`internal/adapter/events`（Webhook/事件总线）、`internal/access`（客户端管控与库权限判定）、`internal/platform`（配置/日志/遥测/锁/时钟/审计）、`internal/diag`（自检、诊断包、数据一致性修复）、`web/`、`tools/`（工具清单与 `tools.go`，入库）、`scripts/`（引导与素材生成脚本，入库）、`.tools/`、`.bin/`、`.testfixtures/`、`.testdata/`（本地生成，全部忽略，见 G51）、`deploy/`、`docs/`。`internal/` 禁止被外部模块导入。
3. 依赖注入与生命周期：Wire 或 fx 二选一，禁止全局变量隐式依赖；所有阻塞操作接受 `context.Context` 并支持取消与超时；并发用 `errgroup` 并设上限；所有 goroutine 必须可退出，禁止泄漏。
4. 错误处理：`fmt.Errorf("...: %w", err)` 包装；领域错误到 HTTP 状态的映射集中在一处；错误响应不得泄露堆栈、绝对路径、数据库结构、密钥或内网信息（仅开发者模式 G45 可例外，且必须显式开启并标记）。
4b. 严禁转码铁律：生产模式下服务端不得提供任何转码、重编码或码率自适应能力；唯一例外是开发者模式（G45）下的显式调试开关，且必须在能力声明、日志与 UI 中标记非生产状态。此铁律覆盖所有接口、兼容层、任务与前端，不得以“兼容性更好”为由绕过。
5. 可观测：`log/slog` 结构化 JSON 日志 + OpenTelemetry trace/metric + Prometheus 指标；为扫描、探测、直投流式、API、DB 查询、Webhook 投递、NFO 读写、图片处理、权限判定建立可对比基线（详见 G46）。
6. 配置：环境变量优先，支持配置文件与 `.env.example`；启动校验必填项并拒绝半配置启动；禁止硬编码密钥。
7. 迁移策略：Strangler Fig 渐进替换，每个能力切换前必须有契约测试与灰度开关；若选择一次性重写，必须有等价功能矩阵与回归测试证明能力不低于现状。

# 二、命名与兼容边界（G00 前置规则）
内部包、模块、类型、配置键、数据库对象、日志分类、服务名、镜像名、前端标识与自有文档统一使用 `Jelee`，清除内部 `Jellyfin` / `Emby` / `MediaBrowser` 品牌或历史命名。
禁止盲目文本替换；建立可复查的重命名映射 `docs/branding-rename-map.md`，按模块执行，每批修改后编译 + 测试。
允许保留旧名称的位置（须集中隔离并加入扫描白名单 `tools/brand-scan/allowlist.txt`）：Jellyfin/Emby 协议兼容 DTO、JSON 字段、HTTP 路由、请求头、客户端识别字符串、NFO 标签与文件名、图片资产文件名、旧配置/旧数据库迁移器、旧忽略文件解析器、许可证与版权声明。
兼容层只做协议适配，不得让旧品牌类型进入 Jelee 核心领域模型；使用显式 Adapter/Mapper 转换。
自动门禁：~~白名单外出现 `Jellyfin`、`Emby`、`MediaBrowser` 时 CI 失败。~~（2026-10-04 需求调整，见下）

> **2026-10-04 需求调整（缩减品牌改名范围）**：取消“全仓库全面移除或替换旧品牌名称”的要求，优先功能、稳定度与真正影响执行的问题。
> - 已完成的 Jelee 命名保留（新 Go 服务、可执行文件、`JELEE_` 配置前缀、API 标题、README、既有部署配置），不改回。
> - 旧 C# 核心、命名空间、程序集、项目文件、测试与兼容协议中的旧名称可以保留；不为品牌一致性做全局替换或额外重构。
> - 用户直接看到的产品名称以 Jelee 为方向，但不为此阻挡功能开发。
> - 全仓扫描改为信息报告，既有旧名称不阻挡合并；阻挡性的增量检查只覆盖仍明确要求 Jelee 命名的新服务（`cmd/`、`internal/`、`web/`、`deploy/`）与对外产品标识（`README.md`、`Dockerfile`、`go.mod`、`.env.example`），协议边界目录可整体列入白名单。
> - LICENSE、NOTICE、上游作者与来源声明一律保留，不得为改名移除。
> - 本调整只缩减品牌范围；其他功能、安全与测试要求不变，不得借此关闭 CI。

# 三、需求逐条细分

## G00 项目品牌
- G00.1 应用标识：可执行文件 `jelee`、服务名 `jelee`、镜像 `jelee/jelee`、配置前缀 `JELEE_`、默认数据库名 `jelee`、Web 标题 `Jelee`。
- G00.2 代码层：Go module 名、包名、日志分类、指标前缀、审计事件类型统一为 `jelee`；前端 package 名、路由前缀、favicon、manifest 名称统一。
- G00.3 文档层：`README.md`、`docs/`、Docker/K8s 元数据统一；`CHANGELOG.md` 记录更名。
- G00.4 品牌扫描：`tools/brand-scan` 提供 `make brand-scan`，输出残留清单与白命中；~~CI 强制~~ 全仓扫描为 CI 信息报告，CI 只强制 `make brand-scan-incremental`（新服务与对外产品标识）（2026-10-04 调整）。
- G00.5 许可证合规：保留 LICENSE/NOTICE/上游版权；新增 `docs/LICENSE-COMPLIANCE.md` 说明派生关系、保留声明位置与分发义务。
- 验收：~~`make brand-scan` 零非白命中~~ `make brand-scan-incremental` 零非白命中（全仓 `make brand-scan` 仅报告，2026-10-04 调整）；容器启动日志与 `/api-docs` 标题均为 Jelee；许可证文件完整未被删改。

## G01 Git 规范
- G01.1 历史与远程：保留上游历史，配置 `upstream`；`docs/git-workflow.md` 说明 fork 同步、分支模型、发布标签。
- G01.2 提交规范：Conventional Commits（`feat|fix|refactor|perf|chore|docs|test|build|ci`），scope 限定模块；禁止混合目标巨型提交。
- G01.3 版本与变更：Semantic Versioning + `CHANGELOG.md`；发布脚本来自 tag。
- G01.4 忽略规则：`.gitignore` 必须覆盖并逐条注释分组：
  - 构建与依赖：`bin/`、`dist/`、`node_modules/`、Go build cache、Vite/TS 缓存；
  - 工具链与本地安装（G51）：`.tools/`、`.bin/`、`.venv/`、`.cache/`、`tools/vendor-downloads/`；
  - 测试：覆盖率与报告（`coverage.*`、`*.out`、`reports/`）、Playwright 产物、`test-results/`、生成的测试素材 `.testfixtures/`、`.testdata/`、`*.test.db`；
  - 运行数据：`data/`、`data/transcodes/`、`data/subtitles/`、`data/mediainfo/`、`data/images/`、`data/index/`、`*.log`、`logs/`；
  - 环境与密钥：`.env`、`.env.*`（保留 `.env.example`）、`*.pem`、`*.key`、`*.p12`、凭据文件；
  - 数据库：本地卷、`*.sqlite*`、dump 文件；
  - IDE/系统：` .idea/`、`.vscode/`（保留必要共享配置）、`*.swp`、`Thumbs.db`、`desktop.ini`、`DS_Store`；
  - 临时：`.tmp/`、`tmp/`、`*.bak`（NFO 备份按 G39 单独策略，不入仓库）。
  - 硬性规则：任何由 Agent/脚本下载或生成的二进制、压缩包、可执行文件、测试素材、数据库文件都不得进入 Git；新增忽略项必须同步更新本清单与 `docs/toolchain.md`。
- G01.4b 忽略校验：提供 `make gitignore-check`，枚举仓库中被忽略与未被忽略文件，断言无二进制/大文件/密钥/工具产物被跟踪；CI 强制；使用 `git check-ignore` 与体积阈值（如单文件 >1MB 需白名单说明）。
- G01.4c 例外机制：确需入库的二进制（如 favicon、示例图标）必须列入 `docs/binary-allowlist.md` 并说明用途与体积，否则 CI 失败。
- G01.5 属性与编码：`.gitattributes` 统一 LF/UTF-8，锁定媒体与二进制文件 diff 行为；`.editorconfig` 统一缩进与换行。
- G01.6 Hook 与 CI：`pre-commit` 运行 gofmt/前端 lint/品牌扫描。
- G01.7 安全：禁止提交密钥、大媒体、数据库数据、构建产物；提供密钥泄露检查说明（无授权不得重写已推送历史）。
- 验收：全新克隆后 `make init && make test` 可跑通；干净构建后 `git status` 无新增未忽略文件；提交历史可二分。

## G02 核心范围裁剪
- G02.1 保留域：Movie、Series、Season、Episode、HomeVideo/其他视频、Collection、Playlist、Library、MediaSource、UserData、PlaybackProgress。
- G02.2 删除域：Audio/Music、AudioBook、Book、Comic、Photo 及其实体、仓储、扫描器、解析器、任务、控制器、前端页面、资源、图标、翻译键、数据库表与迁移清理脚本。
- G02.3 数据层：迁移移除相关表/列，外键与约束同步；提供回滚迁移。
- G02.4 门禁：架构测试禁止领域层引用已删除媒体类型枚举；前端路由树不得出现相关页面。
- 验收：导入旧音乐/图书数据后系统忽略且不报错；已删除类型接口返回 404/明确不支持且非 500；架构测试通过。

## G03 语言与国际化
- G03.1 仅保留 `zh-CN`（默认）、`zh-TW`、`ja-JP`、`en-US`（回退）；删除其他自有语言资源。
- G03.2 资源结构：`web/src/i18n/<locale>/*.json`，服务端错误消息同样四语（`internal/platform/i18n`）。
- G03.3 回退与协商：`Accept-Language` 解析，未知语言静默回退 `en-US`；用户级语言偏好覆盖请求头。
- G03.4 门禁：i18n 检查脚本校验缺失键、冗余键、未使用键、JSON 合法性、占位符一致性；禁止组件内硬编码用户可见文本。
- G03.5 文本质量：简繁不混用；日文敬体统一；日期/数字/单位按 locale 格式化。
- 验收：四语切换无缺失键；模拟 `fr-FR` 请求返回英文且不报错；CI 中 i18n 检查失败即阻断。

## G04 PostgreSQL 主存储
- G04.1 连接：`pgx/v5` + `pgxpool`；连接串仅来自环境变量/密钥文件；TLS 可配置；连接池参数可调（见 G41 并发预算）。
- G04.2 迁移：`golang-migrate` 或 Atlas（二选一，需说明）；已发布迁移不可变；提供 up/down。
- G04.3 查询：`sqlc` 或手写类型安全仓储；禁止字符串拼接 SQL；所有查询参数化。
- G04.4 Schema：外键、唯一约束、CHECK、合适索引（部分索引/表达式索引/GIN），JSONB 仅用于确实动态数据。
- G04.5 事务与并发：用例级事务边界明确；热路径避免长事务；批量写入用 `COPY`/多值插入；死锁重试与退避。
- G04.6 迁移工具：Jellyfin/旧 Jelee SQLite → PostgreSQL 工具，支持预检、断点续传、幂等、失败回滚、行数核对、校验和与迁移报告。
- G04.7 缓存边界：Redis（可选）只做缓存/锁；失效以版本号或事件驱动；不可用时降级而非错误。
- G04.8 门禁：禁止 SQLite 生产回退；生产模式检测非 postgres 驱动即拒绝启动。
- 验收：空库迁移到最新再全量 down/up 成功；10 万条模拟数据关键查询走索引（`EXPLAIN` 入档）；迁移工具导入后行数与校验和一致。

## G05 移除 DLNA / 直播 / 录制
- G05.1 删除：DLNA/SSDP/UPnP 与发现、Live TV、EPG、Tuner、Recording、Channel 及控制器、任务、配置、依赖、前端 UI。
- G05.2 兼容声明：客户端探测返回空能力或明确不支持（`501`/空数组），不得 500，不得静默启用。
- G05.3 端口与依赖：关闭相关监听端口；移除 SSDP 多播与 NAT 依赖；防火墙文档同步。
- G05.4 清理：数据库表/列、配置项、翻译键、图标一并清理。
- 验收：局域网 SSDP 探测无 Jelee 响应；直播接口返回明确不支持；代码中无相关监听器注册。

## G06 去除下载能力
- G06.1 删除下载权限位、下载按钮、附件式下载接口与原文件导出/打包接口。
- G06.2 响应约束：播放流不得返回 `Content-Disposition: attachment`；兼容 DTO 中旧下载字段固定 `false`。
- G06.3 权限模型：所有用户默认且永久无下载权限；管理员不可授予（权限位移除而非仅 UI 隐藏）。
- G06.4 文档：`docs/security-model.md` 明确“可播放媒体理论上可能被客户端录制，系统只能移除官方显式下载能力”。
- 验收：遍历自有与兼容 API 断言无 attachment 响应头；授权测试证明任何角色无法触发下载。

## G07 用户管理
- G07.1 生命周期：创建、启用/禁用、软删除、恢复、重命名、头像、资料字段。
- G07.2 认证：密码哈希（Argon2id 或 bcrypt，参数可配置）、密码策略、改密需验证旧密码、令牌轮换与撤销。
- G07.3 防护：登录限速（IP + 用户维度）、失败锁定与解锁、可疑登录记录与通知。
- G07.4 会话与设备：会话列表、设备识别、强制下线、并发播放上限、带宽上限（按用户/设备）。
- G07.5 授权：媒体库可见性、内容分级、管理操作最小权限、隐藏用户。
- G07.6 审计：用户相关管理操作写审计日志（操作者、目标、前后值、IP、时间）。
- G07.7 数据权利：导出与删除用户数据（含播放记录），删除后不可恢复且级联处理。
- G07.8 可选：TOTP 双因素（不得破坏第三方客户端登录路径，需提供无 2FA 设备令牌流程说明）。
- 验收：权限矩阵测试覆盖全部角色 × 操作；限速、锁定、撤销令牌、并发限制均有集成测试；绕过 UI 直接调 API 也被拒。

## G08 Jelee 自有 API
- G08.1 框架：chi（或等价）路由；中间件顺序固定：request-id → 恢复 → 日志 → 追踪 → 超时 → 认证 → 授权 → 校验 → 限流。
- G08.2 规范：统一分页（cursor 优先，offset 兼容）、排序白名单、过滤白名单、字段选择、错误模型（code/message/details/traceId）。
- G08.3 校验：请求体 schema 校验，未知字段策略明确且一致；路径/查询参数强类型。
- G08.4 幂等：写接口支持 `Idempotency-Key`（至少覆盖用户创建、Webhook 创建、库扫描触发、NFO 写回）。
- G08.5 文档：OpenAPI 3.1 由代码/注解生成并 CI 校验是否过期。
- G08.6 版本：`/api/v1` 前缀 + 兼容层独立前缀；破坏性变更走 v2 与弃用头。
- G08.7 加固：限流、请求体大小上限、超时、取消传播到 DB 与子进程。
- 验收：OpenAPI 与实现一致性检查通过；错误码表测试；越权/非法输入返回 4xx；模糊测试无 panic。

## G09 本地媒体处理与工具链
- G09.1 存储：媒体、外挂字幕、外挂音轨、NFO、图片资产、封面、章节、探测缓存全部位于本地卷；目录结构在 `docs/storage-layout.md` 定义；支持多库多路径。
- G09.2 调用安全：仅用 `os/exec` 参数数组调用 ffmpeg/ffprobe/mkvpropedit/mkvmerge/mediainfo；禁止 shell 字符串拼接；禁止用户输入直接成为参数或输出路径。
- G09.3 工具管理：启动探测工具存在与版本，记录到 `tool_versions`；缺失时相关能力降级并给出可操作错误。
- G09.4 执行控制：超时、取消（进程组终止）、并发上限、输出大小上限、临时目录隔离与清理。
- G09.5 路径安全：路径规范化 + 根目录边界校验 + 符号链接逃逸防护。
- G09.6 权限：以低权限运行外部工具（容器非 root 优先）。
- 验收：恶意文件名/路径穿越/超长参数测试全部被拒；子进程取消后可回收无残留；临时目录无积累（含崩溃后清理）。

## G10 严禁播放/严禁转码下的播放体验（完整资源直投）
- G10.1 铁律：服务端只投递完整、未经改动的原始资源；禁止转码、重编码、码率自适应、分辨率/帧率/色域转换、音轨重编码、字幕烧录、HLS/DASH 切片、分片封装。
- G10.2 唯一允许的两种投递：Direct Play（原文件原样流式返回）与 Remux（仅换容器/重封装，不重编码音视频；默认关闭，需显式开启且注明会改变字节流）。二者都必须保持原始码流不被重编码。
- G10.3 请求拒绝：任何请求转码的参数（如 `maxVideoBitrate`、`videoCodec`、`audioCodec`、`TranscodeReasons`、`Segment*`、`hls` 等）一律返回明确错误码（如 `transcode_disabled`，HTTP 409/501 二选一并全局统一）与可读提示，不得静默降级为直投。
- G10.4 能力声明：兼容层与自有 API 的客户端能力协商必须声明 `Transcoding: disabled/absent`、`HLS: unsupported`、`DASH: unsupported`；相关字段统一为空数组或 false，并在 `docs/compat-matrix.md` 中记录对客户端行为的已知影响。
- G10.5 不兼容处理：无法直投的客户端/格式应返回明确“不支持直投”提示（含原因：容器/编码/字幕/音轨），不得尝试转码救场；UI 与日志需可排查。
- G10.6 启动加速：媒体探测与关键帧/时长索引缓存、moov/faststart 标记提示、首段优先、连接复用、合理缓冲参数、流打开前的权限与存在性快速校验。
- G10.7 Seek：客户端侧基于本地索引与关键帧表定位；服务端支持完整 Range（单段/多段）、`If-Range`、写超时、客户端断开快速终止；服务端不得为 Seek 重新编码。
- G10.8 流式实现：`io.Copy`/`io.CopyN` + 显式缓冲；零拷贝路径（如可用 `Sendfile`）在支持平台上启用并有回退。
- G10.9 字幕与音轨：不烧录、不重编码；仅以独立外挂资源或客户端可识别的内嵌轨形式提供；外挂字幕/音轨按 G15/G16 原样直投。
- G10.10 禁止修改原媒体：不得重写、faststart 改写、标签写回原文件；如需提示优化，只给出建议不执行。
- G10.11 开发者模式例外：仅在 G45 开启时允许临时启用转码用于调试，必须在能力声明、日志、UI 横幅中标记 `dev-transcode`，关闭即刻失效；生产模式绝不可达该代码路径（以测试断言）。
- 验收：生产模式下全部转码/HLS/DASH 参数与路由返回统一错误码；代码中转码路径在关闭态不可达（有断言测试）；Direct Play 首字节与 Seek 指标达标（见性能验收）；原媒体校验和在播放前后一致（证明未被修改）。

## G11 网络协议与隐私防泄露
- G11.1 URL 生成：对外 URL 一律相对地址或显式 `PublicBaseUrl`；禁止依据 `Host`/`X-Forwarded-*` 猜测公网地址。
- G11.2 代理信任：仅信任配置的 `TRUSTED_PROXIES` CIDR；未命中时忽略转发头并记录告警。
- G11.3 Host 校验：允许名单 + 拒绝非法 Host；避免 Host 头注入。
- G11.4 SSRF：所有出站请求（TMDB、图片抓取、Webhook、NFO 外链）经自定义 `http.Client`/Dialer 拦截私网/环回/link-local，含重定向二次校验与 DNS 重绑定防护。
- G11.5 隐私开关：禁用发现/UPnP/STUN/自动 NAT；默认仅监听 `127.0.0.1` 经反向代理发布；局域网监听需显式开启。
- G11.6 响应头：CSP、HSTS、X-Content-Type-Options、Referrer-Policy；不泄露版本指纹与内网 IP；错误页/重定向/WebSocket 不得回显公网 IP。
- G11.7 协议优化：HTTP/2（TLS 场景）、Keep-Alive、压缩仅用于非媒体响应、WebSocket 心跳与背压。
- G11.8 文档：`docs/network-privacy.md` 明确“客户端直连公网 IP 时该 IP 不可能对该客户端隐藏”。
- 验收：自动化扫描全部 API/响应头/日志/错误页/WebSocket 帧，断言无公网 IP 与内网地址泄露；SSRF 用例（127.0.0.1、169.254.169.254、内网段、DNS 重绑定模拟）全部被拦截。

## G12 Webhook
- G12.1 事件：媒体新增/更新/删除、扫描开始/完成/失败、播放开始/暂停/进度/停止、用户登录/失败/锁定、会话创建/结束、NFO 写回、图片抓取完成、系统告警；事件含稳定 `eventId`、`type`、`version`、`occurredAt`。
- G12.2 配置：多端点、事件订阅过滤、启用/禁用、每端点独立密钥、自定义头、超时、重试策略。
- G12.3 可靠投递：Outbox 表 + 后台投递器；至少一次投递；指数退避 + 抖动；最大重试与死信；手动重放；投递日志可查询。
- G12.4 安全：HMAC-SHA256 签名（`X-Jelee-Signature`、`X-Jelee-Timestamp`）、防重放窗口、密钥轮换、载荷脱敏（不含令牌/密码/完整本地路径）。
- G12.5 网络：目标地址经 SSRF 防护与白名单（可配）；HTTPS 证书校验不可关闭（除非显式自签 CA 配置）。
- G12.6 顺序与幂等：`eventId` 供消费方去重；不承诺全局顺序。
- 验收：故障注入（超时/5xx/慢响应）验证退避与死信；重放成功；签名校验测试；端到端事件 5s 内首次投递。

## G13 定时任务与媒体库扫描
- G13.1 调度器：cron/interval/一次性；任务定义持久化；支持启用/禁用、手动触发、历史与日志查看。
- G13.2 并发控制：Postgres advisory lock 或租约保证同一任务不重叠；实例级任务 leader 选举（见 G41）。
- G13.3 可靠性：任务可取消、可恢复、可观测（进度、处理/总数、ETA）、失败有原因与重试。
- G13.4 扫描：fsnotify 监听 + 去抖；增量扫描基于路径/大小/mtime/快速指纹；未变化文件不重复昂贵探测；批处理与检查点；删除确认阈值（防误判全库删除）。
- G13.5 性能：目录级并发、I/O 与 CPU 分离、低优先级完整校验、扫描窗口避开高峰、NFO/图片解析纳入同一增量判定。
- G13.6 其他任务：元数据刷新、NFO 写回、图片抓取/重建、探测与关键帧索引缓存清理、提取字幕清理、统计聚合、数据一致性校验（G50）、备份提醒。
- 验收：1000 文件库增量扫描仅处理变化项（有统计证据）；任务重叠被锁拒绝；中途取消重启可续跑；删除阈值保护生效。

## G14 刮削与 TMDB 优先
- G14.1 数据源：TMDB 为默认第一优先级；支持配置备用源与本地 NFO；不得网页抓取或绕过 TMDB 条款。
- G14.2 凭据：`TMDB_API_KEY` 仅来自环境变量/密钥文件；启动校验可用性与配额。
- G14.3 请求治理：限流器、并发上限、重试（含 429 `Retry-After`）、缓存（内存 + 持久化可选）、超时与取消。
- G14.4 匹配：电影/剧集按标题+年份、外部 ID（IMDB/TVDB/TMDB）；季度/单集号匹配；低置信度不自动写入并标记待人工确认。
- G14.5 语言：按用户/库语言请求，回退链 `zh-CN → zh-TW → ja-JP → en-US`；图片语言偏好可配。
- G14.6 覆盖：NFO 与人工编辑优先于自动刮削；字段级锁，锁定字段不被覆盖（与 G39 联动）。
- G14.7 合规：记录数据来源与抓取时间；提供“移除外部元数据”能力；遵守 TMDB 署名与缓存要求。
- 验收：模拟 TMDB 服务完成 100 部电影 + 20 部剧集刮削，命中率与错误率入档；429/超时/非法响应处理正确；锁定字段不被覆盖。

## G15 字幕格式与处理
- G15.1 支持：SRT、ASS/SSA、WebVTT、TTML/DFXP、SAMI/SMI、MicroDVD(.sub)、VobSub(.sub/.idx)、PGS/SUP、DVB、MKV 内嵌字幕轨道。
- G15.2 元数据：语言、标题、forced、SDH、default、编码、来源（内嵌/外挂）。
- G15.3 命名：外挂字幕命名规则（`名称.语言[.forced][.sdh][.default].ext`、同名多轨、`Subs/` 子目录）并有解析器测试。
- G15.4 编码：UTF-8/UTF-16/GBK/Shift_JIS/BIG5 检测与转换；BOM 处理；乱码回退策略。
- G15.5 提取与直投：ffprobe 识别、mkvmerge 提取内嵌文本字幕到可重建缓存并原样直投给客户端；位图字幕（VobSub/PGS/DVB）直投原轨，不烧录、不转换；明确能力边界并写入文档。
- G15.6 OCR：默认关闭，显式开启后可运行；需限流、并发受限，并说明准确率与资源开销。
- G15.7 附件：ASS 字体附件提取到缓存目录并在渲染/转换时提供。
- 验收：每种格式有最小可再分发样本与解析测试；编码检测正确率入档；外挂/内嵌优先级规则测试；原字幕文件不被修改。

## G16 外挂音轨
- G16.1 格式：MKA 及常见 AAC/M4A、AC3、EAC3、DTS/DTS-HD、TrueHD、FLAC、ALAC、Opus、Vorbis、MP3、WAV/PCM。
- G16.2 命名与目录：`名称.语言[. commentary][. default][. forced].ext`、同名规则、子目录 `Audio/`，兼容常见第三方约定。
- G16.3 元数据：语言、标题、评论音轨、默认/强制、声道数、采样率、码率（按需探测）。
- G16.4 呈现：在播放信息中作为可选音轨返回（原样直投，不重编码）；客户端无法解码时返回明确“不支持直投该音轨”说明，禁止重编码救场。
- G16.5 交互：用户可按条目保存音轨偏好；多版本各自保存偏好。
- 验收：每种格式有最小样本与探测测试；音轨元信息正确性断言；外挂音轨可被第三方客户端识别与选择；不支持格式被明确拒绝而非静默失败。

## G18 初始引导
- G18.1 向导步骤：语言 → 管理员账户（密码强度校验）→ PostgreSQL 连通性与迁移 → 媒体目录（存在/权限校验）→ TMDB 配置 → 工具链检测 → NFO 与图片策略（读取/写回/抓取）→ 网络发布模式与隐私检查 → 完成。
- G18.2 状态机：步骤状态持久化，可前进/回退/中断恢复；重入向导不重复创建管理员。
- G18.3 校验：每步实时验证并给出可操作错误（目录不可读、端口冲突、DB 版本过低）。
- G18.4 原子性：完成阶段用事务写入初始化记录；失败回滚；半初始化实例不得对外服务（中间件拦截）。
- G18.5 CLI 兜底：`jelee-cli setup --non-interactive` 支持无头部署与容器初始化。
- 验收：向导中断/重连/重复进入均不出错；未完成初始化时 API 返回明确状态而非 500；无头初始化一键部署成功。

## G19 媒体信息提取
- G19.1 探测：ffprobe JSON 为主；MediaInfo 补充（MKV 章节/附件/标签与部分编码细节）；mkvmerge/mkvpropedit 处理 Matroska 章节与附件。
- G19.2 规范化：视频编码、Profile/Level、分辨率、帧率、码率、色域、HDR10/HLG/Dolby Vision、色彩空间/传输/原色；音频编码、声道布局、采样率、位深；字幕编码；时长、容器、章节。
- G19.3 缓存：缓存键 = 路径 + 大小 + mtime + 工具版本 + 必要指纹；支持按库/条目失效与重建；缓存清理任务。
- G19.4 容错：损坏文件标记 `probe_failed` 并可重试；不阻塞整体扫描；错误信息脱敏。
- G19.5 性能：并发上限、进程池、批处理写入、避免重复探测（与 G41/G42 联动）。
- 验收：规范化字段黄金样本测试（含 DV/HDR/TrueHD/ATMOS/PGS）；缓存命中率与探测耗时入档；损坏文件不中断扫描。

## G20 同一条目多版本
- G20.1 聚合规则：同一影片/单集的不同分辨率、HDR/DV、编码、音轨、剪辑版聚合为一个逻辑条目；按文件名、目录、NFO 外部 ID、时长相近度与人工指定综合判定。
- G20.2 版本展示：版本标签（1080p/2160p/HDR/DV/导演剪辑/REMUX）、默认版本选择（设备能力、带宽、用户偏好）。
- G20.3 人工干预：合并/拆分、设置主版本、排除误合并；操作写审计。
- G20.4 数据归属：观看进度归属逻辑条目；流选择、音轨/字幕偏好可按版本保存。
- G20.5 边界：不得跨不同剧集/影片错误合并（NFO 外部 ID 优先）；误合并可一键撤销。
- 验收：构造 4K/HDR/1080p/导演剪辑多版本库，聚合与选择符合预期；人工拆分/合并与进度继承测试通过。

## G21 季度与集数识别
- G21.1 目录/文件：`Season 01`、`S01`、`第1季`、`シーズン1`、`Specials`、`Season 0`、`SP`、`OVA`、`S01E01`、`1x01`、`EP01`、多集（`S01E01-E03`）、绝对集数、`Part` 分片合并。
- G21.2 优先级：NFO/外部 ID > 明确命名 > 目录结构 > 启发式；外部 ID 存在时以元数据为准。
- G21.3 特殊：Anime 绝对编号、OVA/剧场版归类、跨季合订、日期型命名可配置。
- G21.4 测试：参数化测试集覆盖中/英/日命名，≥200 条用例；解析器改动必须补用例。
- G21.5 纠错：识别结果可在 UI 修正并锁定；锁定后不被扫描覆盖。
- 验收：测试集全绿；随机改名样本识别准确率入档；锁定项不被覆盖。

## G22 忽略规则
- G22.1 自有：`.jeleeignore`，语法与 `.gitignore` 兼容（通配、`**`、否定 `!`、锚定 `/`、注释、转义）。
- G22.2 兼容：先从对应上游版本源码确认 `.jellyfinignore`/`.embyignore`/`.ignore` 的确切语义（记录来源与版本），再实现；不得凭印象。
- G22.3 行为：目录级继承与就近优先、大小写敏感策略可配、UTF-8/UTF-16 BOM 处理、路径穿越防护、符号链接不穿越。
- G22.4 性能：规则编译缓存（按目录 + 文件 mtime 失效）。
- G22.5 可观测：扫描报告列出被忽略条目与命中规则来源。
- 验收：契约测试覆盖上游文档化行为 + 边界用例；忽略命中可追溯；规则修改后重新扫描生效。

## G23 观看统计
- G23.1 采集：播放会话（开始/暂停/恢复/停止/失败）、客户端、设备、条目、版本、音轨/字幕、投递方式（direct/remux）、失败原因（含 `transcode_disabled`、`codec_unsupported`、`client_blocked`、`permission_denied`）。
- G23.2 写入：进度事件客户端节流 + 服务端批量写入（缓冲与 flush 间隔）；会话 ID 去重；断连后会话超时关闭。
- G23.3 指标：有效观看时长（扣除快进/空转，规则可配）、完成率、续播点、观看次数、首播/重看、按日/周/月/年、按库/类型/条目、Top N。
- G23.4 隐私：保留期可配、用户可清除自身历史、管理员可导出聚合数据；个人统计仅本人/管理员可见。
- G23.5 性能：聚合走 SQL 汇总或物化视图；避免每次请求全表扫描。
- 验收：并发进度上报不造成写放大（有 QPS/写入次数证据）；统计口径文档化并有单元测试；清除与导出测试通过。

## G24 第三方客户端兼容
- G24.1 兼容矩阵：`docs/compat-matrix.md` 列出目标客户端与所需接口、能力声明、已知限制。
- G24.2 覆盖：认证（含 API Key/Header 形态）、用户、系统公开信息、媒体库、Items 查询与过滤、详情、图片、播放信息（仅直投信息）、会话、进度上报、收藏、字幕/音轨选择、视频流（Range/Remux）、能力协商（明确 transcoding 与 HLS 不支持）。
- G24.3 实现：兼容 DTO 与路由独立目录；Adapter/Mapper 单向转换；旧 JSON 字段、ID 形态、大小写与必要请求头保持一致。
- G24.4 移除能力：DLNA/直播/下载/音乐等通过能力声明隐藏；探测接口返回明确不支持而非 500。
- G24.5 测试：契约测试（黄金 JSON 文件）+ 至少三类真实客户端手动验证记录（版本、步骤、截图/日志）。
- 验收：黄金文件对比零差异；客户端可登录、浏览、搜索、查看详情、起播、上报进度；不支持能力探测不报错。

## G25 自有接口性能优化
- G25.1 查询：投影查询、批量加载（dataloader 或显式 batch）、避免 N+1、稳定分页键。
- G25.2 传输：轻量 DTO、字段选择、gzip/brotli 仅用于非媒体响应、ETag/`If-None-Match`、Cache-Control。
- G25.3 缓存：热点只读数据缓存 + 失效策略；用户态数据不共享缓存。
- G25.4 约束：禁止无界集合返回（默认上限 + 显式分页）、禁止循环内访问 DB、禁止先全表加载再内存过滤。
- G25.5 取消：请求取消传播到 DB 查询与子进程。
- 验收：1 万条数据下热路径 P95 ≤ 200ms；单请求 SQL 语句计数断言纳入测试。

## G26 CPU 性能优化
- G26.1 方法：先建基线（扫描、探测、直投流式、API、序列化、DB、NFO 解析、图片处理、权限判定），再用 pprof/trace 定位，禁止凭直觉优化。
- G26.2 重点：重复探测、重复哈希、正则回溯、对象分配、JSON 反序列化、锁竞争、goroutine 调度、无界并发、字符串拼接、高频 time/rand 调用。
- G26.3 手段：`sync.Pool`、预分配切片、编译期正则、流式解析、批处理、索引优化、并发上限、避免热路径反射。
- G26.4 门禁：每个优化提交附 benchstat 前后数据；关键基准回归超过阈值即 CI 失败。
- G26.5 红线：不得牺牲正确性、画质、安全换取性能数字。
- 验收：典型非转码负载 CPU 不高于基线，目标降低 ≥20%；所有优化有可复现命令与数据。

## G27 网页端去播放化
- G27.1 前端删除：播放器组件、播放路由、播放按钮/入口、播放状态机、媒体会话、HLS/DASH 播放依赖、画中画、投屏、播放快捷键与相关翻译键/资源。
- G27.2 保留：登录、浏览、搜索、详情、图片、观看记录/统计、个人资料、安全与偏好设置；管理员的用户、媒体库、任务、Webhook、NFO/图片策略、系统页面。
- G27.3 服务端：播放/流接口保留给授权第三方客户端；Web 会话不得具备播放能力（服务端显式拒绝来自 Web 客户端的播放请求）。
- G27.4 门禁：前端构建期断言无播放依赖与播放路由；E2E 断言 Web 端无播放入口。
- 验收：Web 端全量点击遍历无播放入口；Web 客户端调用播放接口被拒且有清晰提示；第三方客户端仍可播放。

## G28 Go 服务端重写
- G28.1 边界：Go 承载 HTTP 服务、媒体扫描、NFO、元数据、图片资产、用户权限、库访问管控、任务调度、事件与 Webhook、播放信息、直投流式、字幕/音轨、DB 访问、CLI 与诊断；C#/.NET 仅作迁移期参考或兼容旁路。
- G28.2 分层：`domain`（纯领域，无 I/O）→ `app`（用例，依赖接口）→ `adapter`（HTTP/DB/NFO/media/images/compat）→ `platform`；依赖方向单向，架构测试强制。
- G28.3 HTTP：chi 路由；中间件统一；JSON 编解码明确；响应流式化避免大对象全量入内存。
- G28.4 配置与启动：Wire/fx 组装；优雅启动（迁移可选、工具探测、初始化判断）与优雅关闭（draining、停任务、终止子进程）。
- G28.5 CLI：`jelee`（serve）、`jelee-migrate`（up/down/status/verify）、`jelee-cli`（用户管理、扫描触发、NFO 导入/导出/校验、图片重建、缓存清理、诊断报告）。
- G28.6 迁移：Strangler Fig 阶段化，每阶段有开关 + 契约测试 + 回滚路径。
- 验收：`go build ./...` 通过；架构测试通过；诊断命令输出版本/工具/DB/配置/隐私自检结果。

## G29 Go 流式与并发
- G29.1 流式：`io.Copy`/`io.CopyN` + 显式缓冲；Range/多段 Range、`If-Range`、写超时、客户端断开快速终止。
- G29.2 并发：`errgroup` + 上限；worker pool 处理探测/扫描/索引构建/NFO/图片/Remux（开发者模式）；channel 必须设容量或明确无界理由 + 背压。
- G29.3 生命周期：所有 goroutine 绑定 context；禁止泄漏、无超时锁、忙等；提供泄漏检测测试。
- G29.4 子进程：进程组管理与终止；stdout/stderr 限流读取；僵尸进程回收；临时文件清理（含崩溃清理）。
- G29.5 测试：`go test -race` 覆盖流式、取消、并发扫描、NFO 并发写、并发直投与客户端断连；压力测试观察 goroutine 数稳定。
- 验收：取消/断连场景下 goroutine 数回归基线；无残留子进程与临时文件；race 检测无告警。

## G30 Go 工程质量门禁
- G30.1 工具链：`gofmt`、`go vet`、`golangci-lint`（errcheck、staticcheck、govet、revive、gosec、bodyclose、contextcheck）、`go test -race`、覆盖率门槛（核心包 ≥70%，关键包 ≥85%）。
- G30.2 规范：错误包装统一、日志字段统一（traceId/userId/itemId）、panic 恢复仅限边界并记录、资源 defer 释放、time/clock 可注入。
- G30.3 运行：liveness/readiness、优雅关闭、信号处理、`/metrics`、`/healthz`。
- G30.4 交付：`Makefile` 统一入口（`bootstrap`、`tools-verify`、`tools-clean`、`fixtures`、`gitignore-check`、`dev`、`build`、`test`、`test-integration`、`lint`、`fmt`、`bench`、`migrate`、`brand-scan`、`nfo`、`doctor`、`diag`）；Makefile 内所有工具调用优先使用 `.bin/` 本地固定版本（`PATH := $(CURDIR)/.bin:$(PATH)`）；Windows 提供等价 PowerShell 入口（G51.13）；Dockerfile 多阶段构建，非 root 运行。
- G30.6 工具来源纪律：禁止在测试或构建中依赖未纳入 `tools/manifest.toml` 的工具；禁止写入用户全局环境；新工具必须先入清单再使用（G51）。
- G30.5 禁止：不得通过关闭 lint、跳过测试、降低安全设置让 CI 通过。
- 验收：CI 全绿且可复现；覆盖率达标；容器非 root 启动并通过健康检查。

## G31 前端技术选型与架构
- G31.1 选型：Vite + TypeScript strict；框架 Vue 3 或 Svelte（二选一并在 `docs/frontend-adr.md` 记录理由）；Pinia 或等价状态管理。
- G31.2 分层：`api/`、`stores/`、`features/<domain>/`、`components/ui/`、`theme/`、`plugins/`、`i18n/`、`router/`；业务不散落在组件内。
- G31.3 路由：懒加载、守卫、服务端权限校验、404/403、深链可用。
- G31.4 类型：由 OpenAPI 生成 TS 类型，CI 校验过期；禁止 `any` 泛滥。
- G31.5 状态：请求状态机（idle/loading/success/error）、错误统一处理、乐观更新可回滚。
- 验收：`tsc --noEmit` 与 lint 零错误；路由权限正确；OpenAPI 类型同步检查通过。

## G32 前端可扩展性（插件体系）
- G32.1 SDK：`@jelee/plugin-sdk` 暴露类型化 Hook：`media.detail.tabs`、`item.action`、`settings.section`、`library.toolbar`、`theme.token`、`route.register`、`command.palette`、`webhook.eventType`、`metadata.panel`。
- G32.2 Manifest：含 id、name、version、sdkVersion（范围）、权限声明、依赖、入口、minimal Jelee 版本；加载前校验，不兼容则拒绝并提示。
- G32.3 隔离：插件组件懒加载；单插件渲染错误被 ErrorBoundary 捕获并降级，不得白屏；插件不可直接访问令牌或绕过权限。
- G32.4 管理：管理员可启用/禁用/排序/查看插件信息与权限；插件设置独立存储命名空间。
- G32.5 内置样例：提供 2 个官方示例插件与插件开发文档。
- G32.6 版本：SDK 语义化版本与弃用策略；破坏性变更需迁移说明。
- 验收：故意抛错插件不影响主应用；启停即时生效；manifest 非法被拒绝且有清晰错误。

## G33 前端可定制性（主题与布局）
- G33.1 Token：设计 token 单一来源（颜色、间距、圆角、阴影、字号、层级、动效时长），编译为 CSS 变量，运行时可覆盖。
- G33.2 主题：light/dark/system + 至少 3 套预设；管理员可配主色、强调色、圆角、密度、字体、背景、海报比例、列表密度、卡片/列表视图、首页版块。
- G33.3 持久化：用户级与全局级配置分别存服务端；支持导入/导出 JSON；可重置默认。
- G33.4 自定义 CSS：允许管理员注入，必须转义与限制（禁止 `<script>`、`expression`、`javascript:`、外部字体默认禁用可开白名单），防 XSS。
- G33.5 布局：首页版块拖拽排序与显隐；详情页面板可配置；布局预设保存/切换。
- G33.6 可访问性：主题切换保证对比度达标；高密度模式不破坏触控目标尺寸。
- 验收：主题切换无闪烁与布局跳动；自定义 CSS XSS 样例被拦截；导入导出往返一致。

## G34 前端美观与体验
- G34.1 设计基础：4/8pt 栅格、排版层级、色彩语义、间距节奏、统一圆角与阴影层级。
- G34.2 组件库：按钮、输入、选择器、弹层、抽屉、表格、标签、头像、评分、进度、骨架屏、空状态、错误态、Toast、Tooltip、分页、虚拟列表。
- G34.3 页面：海报墙（多尺寸/悬停信息）、列表视图、详情页（演职员、版本、字幕音轨、章节、文件信息、NFO 来源标记）、搜索（筛选/排序/即时反馈）、设置页分区。
- G34.4 动效：默认 ≤250ms、统一缓动、尊重 `prefers-reduced-motion`；骨架屏避免布局抖动；避免滥用毛玻璃影响性能。
- G34.5 响应式与可达性：移动/平板/桌面断点；键盘导航、焦点可见、ARIA、对比度达 WCAG 2.1 AA。
- G34.6 视觉回归：关键页面亮/暗截图对比 + Playwright 视觉回归；变更需人工确认。
- 验收：axe 扫描零严重问题；视觉回归基线通过；关键页面亮暗截图入档。

## G35 前端安全与性能
- G35.1 安全：用户内容转义（禁止 `v-html`/等价除非白名单 sanitize）；令牌仅 httpOnly Cookie 或受控内存，禁止 localStorage 存令牌；CSRF 防护；CSP；依赖漏洞扫描。
- G35.2 权限：路由守卫仅为体验，真校验在服务端；管理入口按权限渲染且服务端二次校验。
- G35.3 性能：代码分割、路由懒加载、虚拟列表、图片懒加载与尺寸适配（配合 G40 图片服务）、请求去重与缓存。
- G35.4 预算：主 bundle gzip 体积预算 CI 断言；首屏可交互 P95 ≤1.5s。
- G35.5 禁项：不得引入会拉回播放能力的依赖。
- 验收：依赖审计无高危；bundle 预算门禁通过；XSS/CSRF 样例测试通过。

## G36 数据库设计与迁移工程
- G36.1 Schema：完整 ER 设计文档，覆盖 users、sessions、devices、client_policies、client_blocks、library_acl、library_acl_overrides、libraries、library_roots、items、item_versions、media_sources、media_streams、chapters、people、genres、tags、studios、collections、playlists、user_data、playback_sessions、playback_progress、watch_stats、nfo_documents、nfo_field_locks、images、image_variants、metadata_providers、metadata_locks、probe_cache、transcode_jobs、jobs、job_runs、job_locks、webhooks、webhook_deliveries、audit_logs、settings、plugin_configs、theme_configs、schema_migrations。
- G36.2 索引策略：列出每条热查询与其索引；全表扫描必须有理由与数据量上限。
- G36.3 数据生命周期：播放历史保留期、探测与索引缓存清理、提取字幕清理、图片变体清理、日志轮转与归档、审计归档策略与任务。
- G36.4 备份恢复：`docs/backup-restore.md` + `jelee-cli` 元数据导出/导入；备份演练记录。
- G36.5 迁移纪律：迁移文件不可变、可回滚、向前兼容、大表变更在线策略。
- 验收：迁移 up/down/up 往返成功；模拟 100 万条 items 的查询计划入档；备份恢复演练成功。

## G37 部署与运维
- G37.1 容器：Dockerfile 多阶段、非 root、最小基础镜像、健康检查；镜像含 ffprobe、mkvtoolnix、mediainfo 且版本固定可追踪；生产镜像默认不包含 ffmpeg（转码相关二进制），避免误启用转码路径；开发/测试用 ffmpeg 只存在于 `.tools/`（G51），绝不进镜像。
- G37.2 编排：`deploy/docker-compose.yml`（Jelee + PostgreSQL + 本地卷）、环境变量示例、卷权限说明（宿主机 UID/GID 映射）。
- G37.3 反向代理：Nginx/Caddy 参考配置（TLS、HTTP/2、超时、缓冲调优、大文件与 Range 支持）。
- G37.4 运行：优雅关闭与滚动升级、`/healthz`、`/readyz`、`/metrics`；日志轮转与脱敏。
- G37.5 文档：安装、升级、回滚、故障排查（播放卡顿、扫描异常、DB 连接、NFO 权限、图片抓取失败）手册。
- 验收：全新环境一条命令起栈并通过健康检查；反向代理下播放与 Range 正常；升级/回滚演练成功。

## G38 交付、CI 与发布
- G38.1 CI：环境准备（`make bootstrap` + `make tools-verify` + `make fixtures`，缓存键基于 `tools/manifest.toml`）→ lint → build → unit → integration（Postgres service 或 G51.9 嵌入式回退）→ contract（含 NFO/图片/兼容层黄金文件）→ 权限矩阵与客户端管控测试 → 严禁转码断言 → 开发者模式不可达断言 → 日志脱敏扫描 → E2E 冒烟 → 容器构建 → 品牌扫描 → 依赖与漏洞审计 → 文档链接与示例校验 → `make gitignore-check` → 性能门禁。
- G38.2 发布：SemVer tag、CHANGELOG 自动生成、制品（二进制/镜像）、校验和/签名（条件允许）；生产镜像不得包含 dev 开关与转码二进制。
- G38.3 文档：README、架构 ADR 集、API 文档（G49）、插件/主题开发文档、NFO 兼容说明、图片资产说明、权限矩阵、客户端兼容矩阵、开发者模式说明、日志与故障排查、备份恢复、性能报告、安全说明、许可证合规。
- G38.4 报告：最终报告按 G00-G51 逐项“已完成/部分完成/阻塞”，附提交号、测试与性能证据。
- 验收：CI 可复现全绿；发布流程演练一次；最终报告齐全无笼统“全部完成”表述。

## G39 NFO 兼容、读取与保持（Emby / Jellyfin 双向）
- G39.1 文件识别：支持 `movie.nfo`、`<视频文件名>.nfo`、`tvshow.nfo`、`season.nfo`、`<剧集文件名>.nfo`；大小写不敏感；支持 UTF-8/UTF-8 BOM/UTF-16/GBK（带 BOM 与探测回退）。
- G39.2 根元素：兼容 `<movie>`、`<tvshow>`、`<episode>`、`<season>`（存在时）、以及 Emby/Jellyfin 常见包装（`<root>`、`<Item>`、`<MediaBrowser>` 派生结构）；未知根元素记录告警而非崩溃。
- G39.3 电影字段：title、originaltitle、sorttitle、plot/outline、tagline、year、premiered/releasedate、rating、ratings（多来源嵌套）、mpaa、certification、runtime、genre（多值）、tag（多值）、studio（多值）、country、language、director/writer（多值）、actor（name/role/thumb/order）、producer、trailer、thumb/aspect（多值）、fanart（多值）、`art` 结构（poster/fanart/banner/clearart/clearlogo/thumb/landscape）、uniqueid（type 属性，IMDB/TMDB/TVDB）、imdbid/tmdbid/tvdbid、ratings、userrating、lockdata、dateadded、collection/set 与 `<set>` 结构。
- G39.4 剧集字段：tvshow 级（同电影字段 + `season` / `episode` 计数、status、airs 相关）、season.nfo（seasonnumber、title、plot、poster 等）、episode 级（title、plot、season、episode、displayseason/displayepisode、aired、rating、actor、director、writer、thumb、uniqueid、lockdata）。
- G39.5 多值与分隔符：genre/tag/studio/director/writer/country 支持多元素与斜杠分隔两种形式；写入时使用可配置分隔符（默认与读取库策略一致，默认保留多元素形式以免破坏第三方兼容）。
- G39.6 读取策略：NFO 存在时优先级高于自动刮削；按库配置 `NfoMode`（`read-only` / `read-write` / `off`）；字段级锁（`lockdata=true` 或 Jelee 字段锁）阻止覆盖。
- G39.7 写入与保持：写回必须保留未知/未识别标签与属性（解析保留原始 XML 子树并在序列化时回写），保留注释与缩进风格可配置，保持元素顺序稳定；UTF-8 带 BOM 策略可配（默认无 BOM 或沿用原文件策略）。
- G39.8 原子写入：临时文件 → `fsync` → `os.Rename` 原子替换；写前备份（可配置保留 N 份 `.nfo.jelee.bak`）；失败自动回滚。
- G39.9 并发与锁：跨进程写 NFO 使用文件锁（POSIX `flock` / Windows `LockFileEx`）与进程内 `singleflight` 去重；同一条目并发写不得产生截断或交错内容。
- G39.10 一致性：写回时若条目无 ID 则生成并写入（元素名与策略在 `docs/nfo-compatibility.md` 中明确，且不得覆盖已有 ID）；不得擅自改写用户手工填写的 ID。
- G39.11 校验与修复：`jelee-cli nfo validate --library X` 输出问题清单（XML 错误、缺失字段、非法路径、编码问题）；`--fix` 仅做可安全修复项并先备份。
- G39.12 损坏容忍：XML 非法时记录 `nfo_invalid` 状态、保留原文件、回退到其他数据源，不得删除或覆盖用户 NFO。
- G39.13 安全：NFO 中的外链 URL（thumb/fanart/actor thumb）下载必须经 G11 SSRF 防护与域名白名单；NFO 文本作为用户输入处理，前端展示必须转义。
- G39.14 开关与迁移：提供“从 NFO 全量导入”“导出全部条目为 NFO”“仅导出缺失 NFO”三种批量操作，全部走任务系统（可取消、可观测、批处理）。
- G39.15 验收：黄金文件集覆盖 Emby/Jellyfin 典型 NFO（电影、剧集、季度、单集、带 art 结构、带锁、UTF-16、损坏文件），解析后字段映射零差异；往返测试（读 → 改 → 写 → 再读）证明未知标签与注释不丢失；并发写 100 次无损坏；Jellyfin/Emby 可读取 Jelee 写出的 NFO（提供验证记录）。

## G40 图片资产：海报 / 背景图 / Logo / 单集缩略图等
- G40.1 类型覆盖：Primary（海报）、Backdrop/Fanart（背景图）、Logo、Banner、ClearArt、ClearLogo、Thumb（单集缩略图/人物头像）、Art/Disc、Chapter 图（可提取或生成）、Landscape、Box/BoxRear、Menu、Profile 图；人物（Actor）头像与演职员图。
- G40.2 文件命名兼容：同级 `poster.*`、`folder.*`、`cover.*`、`movie.*`、`<文件名>-poster.*`、`backdrop.*`、`fanart.*`、`fanart1..n.*`、`logo.*`、`clearlogo.*`、`banner.*`、`clearart.*`、`landscape.*`、`thumb.*`、`<剧集文件名>-thumb.*`、`<文件名>.episode-thumb.*`、`season01-poster.*`、`season01-thumb.*`、`series-poster.*`；大小写不敏感；扩展名支持 jpg/jpeg/png/webp/avif/gif/bmp/tiff。
- G40.3 NFO 内图片：读取 NFO 中 `<thumb>`、`<fanart>`、`<art>` 结构（含 `aspect`、`season`、`type` 属性）以及 NFO 内指向的远程 URL；远程 URL 需下载到本地缓存并记录来源。
- G40.4 内嵌封面提取：可选通过 ffprobe/ffmpeg（如可用）提取视频内嵌封面/附件图片（mjpeg/png）到缓存，默认关闭且可配置。
- G40.5 存储与缓存：原图与派生变体分离；变体目录可清理可重建；缓存键含 源路径/大小/mtime/目标尺寸/格式/质量；使用内容寻址或稳定哈希文件名避免重复存储。
- G40.6 处理管线：按需缩放/裁剪/格式转换（WebP/AVIF/JPEG 可配）、质量参数可配、保持宽高比、支持 focal point 或智能裁剪（可关闭）；处理并发受限（见 G41）并有超时与取消。
- G40.7 元数据：记录宽高、格式、主色调（可选）、平均色（可选）、文件大小、来源（本地/远程/NFO/内嵌）、抓取时间、是否锁定。
- G40.8 API 与前端：提供 `/images/{type}/{id}` 与兼容层旧图片路由；支持宽度/高度/质量/格式/标签参数；返回 ETag 与长缓存头；支持 `If-None-Match` 返回 304；前端请求按容器尺寸取图并懒加载。
- G40.9 抓取：来自 TMDB 等外部源的图片下载需限流、并发受限、SSRF 防护、失败重试与记录；可配置“仅本地图片”模式。
- G40.10 锁定与优先级：本地图片 > NFO 指定 > 外部抓取；用户锁定图片后不被刷新覆盖；提供“重建图片缓存/重新抓取”任务。
- G40.11 删除与保留：更换/删除图片时默认保留原本地图片文件（不删用户资产），删除操作需确认并写审计；仅清理 Jelee 生成的变体。
- G40.12 性能与内存：禁止把整张原图读入内存后多份复制；使用流式解码与尺寸预检；大图处理有内存上限与拒绝策略；解码并发受 G41 预算约束。
- G40.13 验收：每种类型与命名方式各有样本并被正确识别；单集缩略图同时支持文件名约定、NFO 指定与外部抓取；缩放请求命中缓存（有命中率数据）；ETag/304 生效；10 万张图片库下内存占用不超预算（见 G42）；外部图片失败不影响扫描。

## G41 并发架构与吞吐
- G41.1 任务模型：所有异步工作（扫描、探测、索引构建、NFO 解析/写回、图片处理/抓取、字幕提取、统计聚合、一致性校验、Webhook 投递）统一走任务队列与 worker pool，禁止各自散乱起 goroutine。
- G41.2 队列与优先级：至少两级优先级（用户触发/交互相关 > 后台例行）；支持公平调度防止大库扫描饿死交互任务；任务可取消、可超时、可重试。
- G41.3 并发预算：CPU 密集型（探测、索引构建、图片处理）并发 ≈ CPU 核数（可配系数）；I/O 密集型（目录遍历、下载、NFO 读写、直投流式）并发按独立上限；总并发受统一限额器约束；所有限额可通过配置调整并有默认值。
- G41.4 背压：有界队列 + 明确满载策略（阻塞/丢弃/降级）；禁止无界 channel 与无界 goroutine；队列深度指标暴露。
- G41.5 数据库并发：连接池大小与 worker 数匹配并文档化；避免连接饥饿（提供等待队列与超时）；批量写入合并；advisory lock 防任务重入；leader 选举用于单例任务。
- G41.6 分布式与多实例：明确多实例部署下的任务归属（leader 或分片）；至少保证单实例正确性，多实例需有锁与心跳续约。
- G41.7 自适应：可选根据系统负载（load average、cgroup CPU quota、内存压力）动态下调并发；下调与恢复有滞后避免抖动。
- G41.8 可观测：暴露队列深度、在跑任务数、等待时长、任务耗时分布、取消/失败计数、goroutine 数、DB 池使用率。
- G41.9 测试：`-race` 下并发压测（混合扫描 + 探测 + NFO 写 + 图片处理 + API 请求）；断言无死锁、无 goroutine 泄漏、无连接耗尽、无数据竞争。
- G41.10 验收：给出并发参数调优基准表（不同核数/内存下的推荐值与实测吞吐）；满载时系统响应仍可用（API P95 不崩溃）；压测期间 goroutine 与内存曲线平稳。

## G42 内存占用优化
- G42.1 禁止模式：禁止把整个媒体文件读入内存；禁止大列表全量加载后过滤（改为数据库分页/游标）；禁止响应体一次性构造超大 JSON（改流式编码）；禁止无界缓存。
- G42.2 流式：文件与图片处理使用流式读写；HTTP 响应流式编码；大 NFO/大量 XML 使用流式解析而非整体 DOM（若需保留未知标签，采用流式 + 子树缓冲策略并说明取舍）。
- G42.3 扫描内存：目录遍历与文件条目处理流式化，批处理大小有上限；完整库扫描内存占用不随条目数线性增长（提供 1 万 / 10 万 / 50 万条目三档实测曲线）。
- G42.4 缓存治理：所有缓存（探测结果、图片变体索引、元数据、TMDB 响应、翻译资源）必须有容量上限与淘汰策略（LRU/LFU/TTL 组合）与命中率指标；支持按内存压力主动收缩。
- G42.5 对象复用：热路径使用 `sync.Pool` 复用缓冲与编码器；预分配切片容量；避免高频小对象分配；避免热路径反射与重复序列化。
- G42.6 图片内存：解码并发与单图内存上限可配；超大图按尺寸预检拒绝或降采样；禁止同时驻留多份全尺寸位图。
- G42.7 子进程：外部工具（ffprobe/mkvmerge/mediainfo）stdout/stderr 限流读取与截断，防止输出灌爆内存；大输出走临时文件而非管道内存缓冲。
- G42.8 运行时：合理设置 `GOGC` 与 `GOMEMLIMIT`（可配），并记录调优依据；容器内存限制下验证不 OOM。
- G42.9 监控与门禁：暴露 Go 运行时指标（heap、goroutine、GC 暂停、alloc rate）；设定内存预算（如典型 4C8G 环境常驻 ≤ 阈值，具体阈值以基线为准）并在 CI/基准中校验。
- G42.10 验收：提供 pprof heap/inuse_space 前后对比；50 万条目完整扫描与 10 万图片处理下内存不超预算且 GC 暂停可控；长时间运行（≥24h 模拟）无内存单调增长（或有证据的缓存稳态）。

## G45 开发者模式（特殊方式开启，用于关闭限制与调试）
- G45.1 开启方式（需多重门槛，不得单一开关）：环境变量 `JELEE_DEV_MODE=true` + 配置文件 `dev.enabled: true` + CLI 子命令 `jelee-cli devmode enable --token <一次性令牌>` + 特殊入口（仅本地环回可访问的魔法路径或专用端口）。默认全部关闭。
- G45.2 防误开：非环回地址访问管理入口时禁止通过 URL 方式开启；开启需审计记录（时间、来源、配置 diff）；容器镜像生产标签默认禁用；`JELEE_ENV=production` 时强制忽略 dev 配置并告警。
- G45.3 持续可见：开发者模式开启时，UI 顶部常驻醒目横幅、API 响应头 `X-Jelee-Dev-Mode: true`、系统信息接口返回 `devMode: true`、启动与周期性日志 WARN 提醒。
- G45.4 可关闭的限制（逐项开关，非全开）：登录限速、API 限流、并发播放上限、带宽上限、忽略文件规则、NFO 只读保护、图片锁定、权限矩阵严格模式、Host 校验严格模式、SSRF 严格拦截、公网 IP 隐藏约束、客户端 UA 屏蔽。
- G45.5 可开启的调试选项：详细日志（DEBUG/TRACE）、SQL 语句与耗时日志、请求/响应体日志（脱敏后）、pprof 端点（`/debug/pprof/*`）、OpenAPI 原始与内部 API 暴露、内部错误堆栈返回（仅 dev）、模拟客户端能力（可伪造 UA/设备/编解码能力）、转码调试开关（`dev-transcode`，违反 G10 铁律但仅限 dev）、mock 外部服务（TMDB/图片源）、种子数据生成、强制任务立即执行。
- G45.6 危险操作二次确认：删除全部数据、重建库、清空缓存、关闭鉴权、导入不受信 NFO 等必须 `--i-understand` 或 UI 二次确认，并写审计。
- G45.7 自动降级：开发者模式设置最长有效期（可配，默认如 12 小时）到期自动关闭并恢复生产限制；进程重启默认不继承（除非显式持久化并告警）。
- G45.8 测试隔离：所有测试默认在开发者模式关闭态运行；必须有断言证明生产态下 dev 专属路由、端点、开关不可达（返回 404/403）。
- G45.9 文档：`docs/developer-mode.md` 列出全部开关、风险、默认值、恢复方式；README 显著位置警告不得在生产启用。
- 验收：默认构建下 dev 路由与开关全部不可达（自动化断言）；开启后横幅/响应头/日志三处同时生效；关闭或到期后所有限制立即恢复；审计记录完整。

## G46 日志系统（全面、分层、可诊断）
- G46.1 输出：`log/slog` JSON 结构化为主，支持控制台人类可读模式（dev）、文件轮转（大小/时间/保留份数）、可选 stdout-only 容器模式、可选 syslog/Loki/OTLP 转发。
- G46.2 级别与组件：全局级别 + 按组件级别（`http`、`auth`、`access`、`scan`、`probe`、`nfo`、`images`、`jobs`、`webhook`、`compat`、`media`、`db`、`gc`）独立可配，运行时可热调整（管理员或 CLI）。
- G46.3 分类日志：访问日志（含 method/path/status/耗时/大小/traceId/userId/clientId，媒体请求单独标记）、审计日志（管理操作与权限变更，不可被普通日志级别关闭）、安全日志（登录失败、锁定、被屏蔽客户端、越权尝试、SSRF 拦截、dev 模式变更）、任务日志（扫描/索引/NFO/图片/Webhook，带 taskId 与进度）、媒体日志（探测、直投、Seek、断连、失败原因）、DB 慢查询日志（阈值可配）、panic/崩溃日志与堆栈归档。
- G46.4 上下文字段：`traceId`、`spanId`、`requestId`、`userId`、`deviceId`、`clientId`、`itemId`、`libraryId`、`taskId`、`jobRunId`；中间件自动注入，跨 goroutine 传递。
- G46.5 脱敏：令牌、密码、API Key、Webhook Secret、Cookie、Authorization 头、数据库连接串、完整本地绝对路径（可配是否保留相对路径）、IP（可配掩码）必须脱敏；提供脱敏规则单测与扫描测试（在日志中搜索敏感样例必须为空）。
- G46.6 与追踪联动：日志携带 trace/span ID；关键路径（直投、扫描、NFO 写、Webhook 投递）串起完整链路；采样率可配且安全事件强制采样。
- G46.7 查询与可视化：日志字段规范化便于检索；提供常见查询示例（Loki/Grafana/ELK 示例在文档中）；`jelee-cli logs tail/filter/export`。
- G46.8 性能：异步/缓冲写避免阻塞请求路径；背压与丢弃策略明确（丢弃时计数并告警）；高 QPS 下日志不得成为瓶颈（有基准数据）；DEBUG 级别不得在生产默认开启。
- G46.9 保留与合规：保留期与容量上限可配；轮转与压缩；审计与安全日志保留期独立于普通日志；用户数据删除时相关日志处置策略写入文档。
- G46.10 验收：各类日志均有可复现用例与断言；脱敏扫描零命中；日志级别热调整生效；高 QPS 压测下 P95 无显著劣化；审计日志无法被关闭（尝试关闭有告警记录）。

## G47 客户端管控：UA 屏蔽、标识屏蔽与访问策略
- G47.1 识别维度：User-Agent、客户端应用名/版本（兼容层与自有 API 上报）、设备 ID、设备名、设备类型、IP/CIDR、API Key、请求头特征、可选 TLS/JA3 指纹（若实现需说明依赖与局限）。
- G47.2 规则模型：白名单/黑名单/优先级；精确匹配、前缀、通配、正则（正则需 ReDoS 审查与超时保护）、大小写策略；规则支持备注、生效时间窗、命中动作与命中计数。
- G47.3 命中动作：拒绝（返回明确错误码如 `client_blocked`，HTTP 403）、只读、限制库访问、限速、强制重新认证、仅记录不拦截（观察模式）、影子记录。观察模式需可在 UI 评估影响后再切拦截。
- G47.4 粒度：全局、按用户/用户组、按库、按客户端类型分别配置；规则冲突时优先级与合并策略明确（文档化并测试）。
- G47.5 已知客户端管理：识别到的客户端列表（名称、版本、UA、设备、最后活跃、最后 IP）可在管理页查看；支持重命名、标记可信、加入屏蔽、踢下线。
- G47.6 伪装与绕过：明确 UA 可伪造，规则应结合设备 ID/API Key/令牌；文档说明防护边界；提供“未知客户端默认策略”（允许/只读/拒绝/需管理员批准）。
- G47.7 管理员保护：管理员自身会话与本地环回诊断默认不受屏蔽影响（可配置）；防止规则误配导致全员无法登录（提供紧急恢复 CLI：`jelee-cli access reset-policies`）。
- G47.8 可观测：命中记录写入安全日志与统计（命中次数、Top UA、Top IP）；可导出命中明细；提供告警规则（异常 UA 暴增、批量被拒）。
- G47.9 隐私：被屏蔽请求的日志需脱敏；不得因屏蔽逻辑泄露其他用户信息或完整路径。
- G47.10 验收：每种匹配方式与动作均有测试；观察模式→拦截切换可评估；误配后紧急恢复成功；管理页可查看与操作；性能上规则匹配有缓存/编译，热路径开销可测（有基准）。

## G48 媒体库访问与查看权限管理
- G48.1 授权模型：用户/用户组 ↔ 库（可见/不可见）；支持库级、目录根级、条目级（显式隐藏/允许）、分级（Parental Rating）、标签/类型级规则；规则优先级与冲突合并策略文档化。
- G48.2 服务端强制：所有列表、搜索、详情、图片、字幕/音轨、播放信息、统计、Webhook 载荷都必须经过统一权限过滤器；禁止先查询再在展示层过滤。
- G48.3 隐藏语义：不可见条目不得出现在任何响应、搜索建议、最近添加、继续观看、合集、Playlist、图片 URL 与统计中；直接按 ID 访问返回 404（而非 403 暴露存在性，策略需可配并默认 404）。
- G48.4 分级与内容控制：按分级、标签、关键字屏蔽；支持时间窗（如限制时段）；不同用户可不同分级上限；分级缺失时的默认策略可配。
- G48.5 设备与网络维度：可按设备类型、IP/CIDR、是否局域网限制库访问；与 G47 规则协同（明确优先级顺序）。
- G48.6 共享与来宾：支持受限共享链接/来宾用户（可设过期时间、只读、指定库、并发上限），所有共享访问可撤销并审计。
- G48.7 管理体验：库授权矩阵页面（用户 × 库批量勾选）、批量应用、模板（如“成人库”“儿童库”）、变更预览（影响多少条目/用户）、变更写审计。
- G48.8 性能：权限过滤必须在 SQL 层下推（索引、JOIN/EXISTS 条件），禁止全表捞取后内存过滤；提供查询计划证据与单请求 SQL 计数断言。
- G48.9 与开发者模式：dev 模式可临时关闭严格权限用于调试，但必须标记与自动恢复（G45.4/G45.7）。
- G48.10 验收：权限矩阵测试覆盖 用户 × 库 × 条目 × 分级 × 设备 × IP；越权直接访问返回 404；隐藏内容在全部接口零泄露（自动化遍历断言）；过滤下推有 `EXPLAIN` 证据；批量变更与撤销审计完整。

## G49 API 与文档完善
- G49.1 规范统一：全部自有 API 使用统一响应信封（数据/分页/错误/元信息）、统一错误码表（全局唯一、带 HTTP 映射与说明）、统一分页（cursor 优先 + offset 兼容）、统一排序/过滤白名单、统一时间与 ID 表示（RFC3339、字符串 ID）。
- G49.2 版本与弃用：`/api/v1`；弃用头与公告；`docs/api-deprecations.md` 记录时间线与替代方案；破坏性变更走 v2 并保留过渡期。
- G49.3 OpenAPI：3.1 规范由代码生成，CI 校验与实现一致；提供可浏览文档页与导出文件；示例请求/响应齐全（含错误示例）。
- G49.4 调试台：开发者模式下提供 API 控制台（构造请求、查看响应、复制 cURL、查看 traceId），生产模式不可用。
- G49.5 SDK 与示例：生成/维护至少 TypeScript 客户端类型与一个 Go 示例；提供 curl 示例集；所有示例有 CI 校验（关键示例可跑通）。
- G49.6 覆盖面：补齐 G00-G48 引入的全部能力接口——系统信息、用户与权限、库与条目、NFO 读写与校验、图片资产、字幕/音轨、播放信息（直投）、流式、会话与进度、统计、任务、Webhook、客户端策略、库 ACL、开发者模式状态、诊断与日志查询。所有接口必须有契约测试。
- G49.7 文档体系：`README.md`、`docs/quickstart.md`、`docs/architecture.md` + ADR 集、`docs/domain-model.md`、`docs/nfo-compatibility.md`、`docs/image-assets.md`、`docs/api-reference.md`、`docs/permission-matrix.md`、`docs/compat-matrix.md`、`docs/developer-mode.md`、`docs/logging.md`、`docs/troubleshooting.md`、`docs/backup-restore.md`、`docs/perf-report.md`、`docs/security-model.md`、`docs/contributing.md`。
- G49.8 文档质量门禁：链接检查、代码块可编译/可运行检查（关键片段）、中英日术语一致、与实现不一致即为 bug（CI 校验关键文档片段，如错误码表与路由清单）。
- G49.9 i18n 文档：面向管理员的关键文档至少提供简体中文；UI 四语齐全（G03）。
- 验收：OpenAPI 与路由清单一致性检查通过；错误码表全覆盖且无重复/冲突；每类接口有契约测试；文档链接与关键示例 CI 通过；新功能未同步文档视为未完成。

## G50 诊断、自检与自愈
- G50.1 自检：`jelee-cli doctor` 一键检查配置合法性、DB 连通与版本、迁移状态、库路径存在与权限、外部工具版本、磁盘空间与 inode、网络监听与代理头配置、隐私开关状态、开发者模式状态、日志与缓存目录可写、外部源连通性；输出可读报告与建议修复命令。
- G50.2 诊断包：`jelee-cli diag export` 导出脱敏诊断包（配置快照脱敏、最近日志片段、pprof、DB 统计与慢查询、任务状态、客户端与规则命中统计、库统计），可指定时间窗与大小上限。
- G50.3 数据一致性检查器：定期或手动检测孤儿条目（文件不存在）、孤儿文件（未入库）、版本计数错误、播放统计漂移、图片记录与实际文件不一致、NFO 与条目不一致、外键/唯一约束异常、缓存与实际不符。
- G50.4 自愈动作：重建条目、重建图片变体、重建探测与索引缓存、重算统计、清理孤儿记录、重新同步 NFO、修复计数；所有动作支持 `--dry-run` 预演（输出将影响的对象与数量），确认后执行并写审计。
- G50.5 运行时自检：启动时校验关键不变量（如“转码路径不可达”“dev 模式状态”“权限过滤器已装配”），不满足则拒绝启动或明确告警；提供 `/readyz` 包含关键依赖状态。
- G50.6 告警与 runbook：为关键指标与事件提供默认告警规则与 runbook（磁盘满、DB 不可达、扫描连续失败、Webhook 死信堆积、被屏蔽客户端暴增、内存超阈值、dev 模式未关闭）。
- G50.7 验收：`doctor` 在各类人为故障下给出正确诊断（注入 10 种故障）；`diag export` 产物脱敏（敏感扫描零命中）；一致性检查能发现注入的 5 类数据问题；所有自愈动作 dry-run 准确且执行后可回滚/可重跑。

## G51 工具链自安装与测试素材（允许 Agent 在项目目录下安装并使用工具）
- G51.1 总原则：允许 Agent 在项目目录内自行安装开发/测试所需工具，但必须“清单化、可复现、可清理、不入 Git”；禁止污染用户系统环境（禁止全局 `npm i -g`、`go install` 写入用户 GOPATH/bin、系统级包管理器安装）；禁止安装到项目目录之外。
- G51.2 安装根目录：所有下载与解压落在 `.tools/`（按 `工具名/版本/平台-架构/` 分层），可执行文件与包装脚本链接到 `.bin/`；两者均被 `.gitignore` 忽略（G01.4）。
- G51.3 清单文件：提交 `tools/manifest.toml`（或等价），逐条记录工具：名称、用途、版本（精确固定，禁止 latest）、来源 URL、平台/架构映射、SHA256 校验和、解压方式、相对安装路径、是否必需、许可证标识、归属说明。新增工具必须先改清单，禁止临时随手下载。
- G51.4 引导脚本：提供 `scripts/bootstrap-tools`（POSIX）与 `scripts/bootstrap-tools.ps1`（Windows），或统一 `make bootstrap`；实现：检测平台架构 → 读取清单 → 下载（仅 HTTPS，支持代理与镜像变量）→ 校验 SHA256（不符立即失败并清理）→ 解压到 `.tools/` → 生成 `.bin/` 包装脚本 → 写入 `.tools/.installed.json`（版本/时间/校验和）。脚本必须幂等、可离线复用缓存、失败可重跑。
- G51.5 版本固定与复现：Go 工具类优先用 `go run <module>@<version>` 配合 `tools/tools.go`（`//go:build tools`）纳入 `go.mod` 固定版本，避免散装安装；非 Go 工具走清单下载；所有命令在 Makefile 中通过 `PATH=$(PWD)/.bin:$(PATH)` 调用，保证优先使用本地固定版本。
- G51.6 工具清单（至少覆盖）：Go 工具链校验、`golangci-lint`、`gofumpt`/`gofmt`、`go-testcoverage` 或等价、`migrate`（golang-migrate）或 Atlas CLI、`sqlc`（如采用）、`swag`/OpenAPI 生成器、`buf`（若用 protobuf）、Node LTS 与包管理器（项目本地安装）、Playwright（浏览器驱动下载到 `.tools/`）、Testcontainers 依赖（若采用）、`ffmpeg`/`ffprobe`（仅测试素材生成与开发调试）、`mkvtoolnix`（mkvmerge/mkvpropedit）、`mediainfo`、`gosec`、`trivy` 或等价漏洞扫描、`lychee`/等价链接检查、`shellcheck`、`actionlint`（若用 GitHub Actions）。
- G51.7 与严禁转码的关系：开发工具（ffmpeg 等）属于**开发/测试期依赖**，不得被 Jelee 运行时依赖，不得进入生产镜像；`docs/toolchain.md` 明确区分“构建测试依赖”与“运行依赖”（运行依赖仅 ffprobe、mkvtoolnix 可选、mediainfo），并不得因本地存在 ffmpeg 而在生产代码路径启用转码（以断言测试保证）。
- G51.8 测试素材：禁止向仓库提交受版权保护媒体与大体量二进制；提供 `make fixtures` 用清单中的 ffmpeg 生成最小合成素材（短视频含音轨、多字幕轨、章节、多版本分辨率样本、损坏文件样本、极小图片、示例 NFO 集）到 `.testfixtures/`（被忽略）。素材生成脚本本身入库（`scripts/gen-fixtures`），生成产物不入库。
- G51.9 集成测试环境：PostgreSQL 优先用 Testcontainers（检测 Docker 可用时）；不可用时回退到清单中固定的嵌入式 PostgreSQL 二进制（安装到 `.tools/`，数据目录放 `.testdata/`，被忽略），保证无 Docker 环境也能跑集成测试；两种模式都要有明确日志说明处于哪种模式。
- G51.10 缺失降级：外部工具缺失时相关测试必须**显式跳过并输出原因**，禁止静默通过或把跳过当作成功；CI 关键分支必须使用“完整工具链”模式并校验所有工具版本与清单一致（`make tools-verify`）。
- G51.11 许可证与合规：`tools/manifest.toml` 记录每个工具的许可证标识；`docs/THIRD-PARTY-TOOLS.md` 汇总工具名称、版本、许可证、用途（区分构建测试/运行）、上游链接与归属；分发说明中明确开发工具不随产物分发。
- G51.12 安全：仅 HTTPS 下载并校验 SHA256；禁止执行来自下载包的安装脚本（仅解压取用二进制）；解压路径限制在 `.tools/` 内并防路径穿越（zip slip 防护）；不得将工具加入系统 PATH 或写入用户 shell 配置；卸载用 `make tools-clean`（删除 `.tools/`、`.bin/`、`.testfixtures/`、`.testdata/`）。
- G51.13 平台支持：优先保证 Linux 与 Windows（当前开发环境为 Windows）可用，macOS 尽力；Windows 下需处理可执行文件扩展名、长路径、无 `make` 时的替代入口（提供 `scripts/make.ps1` 或等价 PowerShell 入口），并在文档中说明。
- G51.14 CI 集成：CI 步骤为 `make bootstrap` → `make tools-verify` → `make fixtures` → lint/build/test；CI 使用缓存键基于 `tools/manifest.toml` 哈希，避免每次重下；缓存失效时能完整重建。
- G51.15 验收：全新克隆执行 `make bootstrap && make tools-verify && make fixtures && make test` 成功；`.tools/`、`.bin/`、`.testfixtures/`、`.testdata/` 全部未被 Git 跟踪（`make gitignore-check` 通过）；删除这些目录后能一键重建；校验和错误时安装失败而非静默继续；工具缺失时测试显式跳过而非假通过。

# 四、安全与工程质量（贯穿全部需求）
1. 路径安全：所有文件（媒体、NFO、图片、字幕、音轨）操作做 `filepath.Clean` + 根目录边界校验 + 符号链接逃逸防护。
2. 命令安全：外部工具仅参数数组调用；用户输入不得成为参数、过滤表达式或输出路径。
3. 密钥：令牌、TMDB Key、Webhook Secret、DB 密码只来自环境变量或受保护密钥文件；日志脱敏；支持轮换。
4. 注入与输出：DB 参数化；NFO/图片元数据作为不可信输入处理，前端输出转义；管理操作授权 + 审计。
5. 测试：单元测试、PostgreSQL 集成测试、API 契约测试、媒体命名参数化测试、NFO 黄金文件测试、图片资产识别测试、迁移往返测试、权限矩阵测试、客户端管控规则测试、严禁转码不可达断言、开发者模式关闭态断言、日志脱敏扫描测试、安全回归测试、关键 E2E；素材使用可再分发小型合成文件（由 `make fixtures` 生成，见 G51.8）与自建示例 NFO/图片，不提交受版权保护媒体，也不提交大体积二进制。
6. 可观测与告警：结构化日志（G46）、指标、关键告警规则与 runbook（G50.6）。
7. 每完成一个模块立即运行适用的格式化、静态分析、编译、测试与冒烟；失败先定位根因再修复。

# 五、性能验收（可执行基准）
在同一机器、同一数据集、同一配置下记录基线（`docs/perf-baseline.md`）与结果（`docs/perf-report.md`），覆盖：
1. 1 万条视频元数据列表查询（冷/热缓存）。
2. 100 个文件增量扫描与一次完整库扫描（含 NFO 与图片资产识别）。
3. 冷/热媒体详情请求。
4. Direct Play 首字节（原文件直投）与并发直投吞吐。
5. 前向/后向 Seek 与连续 Seek。
6. 100 并发进度上报。
7. NFO 全量导入 / 全量导出（1 万条目）与 100 次并发写回。
8. 图片资产识别（10 万张）与按需缩放缓存命中率。
9. 混合并发压测（扫描 + 探测 + NFO + 图片 + 直投 + API）。
10. 前端首屏可交互与关键页面切换。
11. 客户端管控规则匹配与库权限过滤在高 QPS 下的开销（对比无规则基线）。
12. 日志系统在 DEBUG 与 INFO 两档下对 P95 的影响。
目标：
- 核心 API 热路径 P95 ≤ 200ms。
- 本地 Direct Play 首字节 P95 ≤ 500ms（原文件直投，无转码）。
- 关键帧附近 Seek（客户端侧定位 + 服务端 Range）P95 ≤ 1s。
- 增量扫描不得重新探测未变化文件（以计数证明）。
- 典型负载 CPU 不高于基线，目标降低 ≥20%（无转码，基线不含转码）。
- 完整库扫描内存占用不随条目数线性增长；典型部署内存常驻在预算内。
- 权限过滤与客户端规则在 SQL/匹配层下推，开销 ≤ 基线 10%（有对比数据）。
- 前端首屏可交互 P95 ≤ 1.5s，主 bundle gzip 后不超过预算。
受硬件或媒体限制无法达标时，必须提供可复现数据与瓶颈证据，不得伪造通过。

# 六、阶段与提交纪律
阶段提交顺序：`audit/tests` → `branding` → `go-skeleton` → `feature-removal` → `postgresql` → `no-transcode-direct-delivery`（G10/G27 铁律） → `nfo-compat` → `image-assets` → `media-scan-metadata` → `playback-direct` → `users-api-compat` → `access-control`（G47/G48） → `logging-devmode`（G46/G45） → `concurrency-memory` → `frontend-platform` → `theming-extensibility` → `network-webhooks-jobs` → `stats-web` → `diagnostics-docs`（G49/G50） → `hardening-release`。
每个提交只完成一个可解释目标；提交前必须编译与测试通过；提交信息说明“做了什么、为什么、如何验证”。
不得改写已推送历史，不得无授权执行强制推送、硬重置或删除用户分支；不要替用户设置 Git 姓名与邮箱。若工作区已有用户改动，必须保护并避免覆盖。

# 七、完成判定
只有在以下条件全部成立时项目才算完成：
1. Go 后端可构建、可迁移、可启动；PostgreSQL 从空库初始化成功。
2. 第三方客户端可登录、浏览、搜索、查看详情、直投起播与上报进度；图片（海报/背景/Logo/缩略图）正常显示。
3. 既有 Emby/Jellyfin NFO 可被正确读取；Jelee 写出的 NFO 可被 Emby/Jellyfin 读取；未知标签与用户手工内容不丢失；原 NFO 与本地图片不被破坏。
4. 严禁转码铁律成立：生产模式下任何转码/HLS/DASH 请求返回统一错误码，能力声明明确不支持，代码中转码路径不可达；原媒体校验和播放前后一致。
5. 网页端无任何播放入口与播放依赖；Web 客户端播放请求被服务端拒绝。
6. DLNA/直播/录制/下载/音乐/图书/照片能力无法被重新调用，兼容探测不报错。
7. 开发者模式默认关闭且生产环境不可误开；开启后限制解除与调试选项生效并全程可见；关闭/到期后自动恢复。
8. 日志系统分层齐全、可热调整、敏感信息零泄露、审计日志不可关闭。
9. 客户端 UA/标识屏蔽与库访问权限在服务端强制生效；隐藏内容在任何接口零泄露；误配可紧急恢复。
10. 并发压测无死锁/泄漏/数据竞争；内存占用符合预算并有曲线证据。
11. 原媒体文件未被修改，派生数据可清理可重建。
12. 品牌扫描零非白命中；许可证与归属声明完整（含第三方工具清单 G51.11）。
13. 全新克隆执行 `make bootstrap && make tools-verify && make fixtures && make test` 成功；`.tools/`、`.bin/`、`.testfixtures/`、`.testdata/` 未被 Git 跟踪且可一键重建。
13. 文档体系齐全且与实现一致（链接与关键示例 CI 通过）；`doctor` 与自愈工具可用。
14. G00-G51 全部有追溯记录、测试与性能证据；未达标项明确说明根因、影响与下一步。
