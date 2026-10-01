# 本次工作与验证报告

本文保留为**第一阶段提交前的历史验证快照**。此后 8 个提交已经推送并建立[草稿 PR #1](https://github.com/MoYuanCN/Jelee/pull/1)；当前账户功能与后续验证见[第 2 阶段报告](accounts-verification.md)。下文“尚未推送”与计数仅描述当时状态。

工作与验证日期：2026-09-30 至 2026-10-01，Asia/Taipei。基线提交 `52a680c578f1af888ebb74cefcb89b736f9c5738`，分支 `feat/jelee-go-foundation`。

## 结论与交付边界

**交付的是可构建并经过真实 PostgreSQL 和 HTTP 验证的 Go 基础增量，不是 G00–G51 完整系统。** 原需求有 336 个明确编号子项，目前 147 项部分完成、189 项阻塞；G17、G43、G44 在原文中缺失。全量状态在 [追溯矩阵](requirements-traceability.md)。现有历史、原版权与媒体文件保持不变。

新增实现：本地固定工具链；chi/fx 服务与优雅关闭；配置校验；PostgreSQL schema 和迁移；本地会话创建与只读媒体登记；SQL 库 ACL；有界目录分页；安全原文件直投；Web 会话拒绝；生产转换参数/路由拒绝；只读 NFO 解析与离线校验 CLI；四语错误；日志脱敏基础；基础 doctor；构建和 CI 入口。

用户确认提交作者为 `Carinoasd`。已通过其 GitHub 公开资料核对账号 ID `46304809`，下列 7 个阶段提交使用 `Carinoasd <46304809+Carinoasd@users.noreply.github.com>`，由提交命令级 `git -c user.name=... -c user.email=...` 指定。以下哈希均已通过本地 Git 历史核对；最终文档另行提交，没有推送到来源仓库。

## 本地提交记录

| 提交 | 实际范围 |
| --- | --- |
| `a512674643e1d259466e04c0060e272f9416c1e8` | 审计基线、原始需求、解释文档 |
| `721102c8d068490d53be2bb366a9e794fe1e01fd` | 本地 Go 工具引导/校验、忽略规则、工具入口与门禁 |
| `c77863e445a72b421a3f97db160901b3983a91b4` | 领域与应用端口、原文件传输/护栏、配置/四语/日志与核心测试；这是修订后的有效提交 |
| `632005d430393e1e6a0c1317dd4ce8f8d37f115a` | PostgreSQL schema/迁移、事务导入、SQL ACL 与真实数据库测试 |
| `403cc21b2772357e64227b70df7aa74bcfd656df` | HTTP API/合同、三个 CLI 入口、fx 生命周期 |
| `0bbd5939bb590a5448f9ec2833559da236064a6b` | 非 root 镜像配方、Compose、数据库 CI 入口与部署/许可说明 |
| `f21d15668477bd5806e7e525149bfb373d9a68bd` | 有界只读 NFO 解析、编码/字段/原文保留、离线校验 CLI、取消/隐私测试及兼容说明 |

上表归属实际修改范围。独立归档验证的源码快照为 `0bbd5939bb`；最终 Windows/Linux 测试与当前镜像实测对应 `f21d156684`。不能把这些通过结果倒推为每个中间提交都完成所有最终构建目标。README/CHANGELOG/最终证据归属单独的文档提交，NFO 完整验收仍未完成。

## 实际执行

| 项目 | 命令 / 证据 | 结果 |
| --- | --- | --- |
| 已提交源码独立复验 | `git archive 0bbd5939bb -- go.mod go.sum cmd internal tools`，在独立目录执行 `test -count=1 -v ./...` 与 `build ./...`；[日志](evidence/committed-snapshot.txt) | Windows amd64、本地 Go 1.27.1；测试及构建退出码均为 0。未复制进行中的 NFO 文件。PG 和 Linux/race 不属于此独立复验 |
| Windows 构建 | `scripts/make.ps1 build` | 三个 exe 构建成功 |
| Windows 单测 | `scripts/run-go.ps1 test -count=1 -cover -v ./...`；[日志](evidence/windows-tests.txt) | `f21d156684` 通过；未配置 PostgreSQL 的集成测试及 3 个无权限创建符号链接的案例明确跳过 |
| Windows 格式/分析 | `scripts/make.ps1 lint` | gofmt / go vet 通过 |
| Linux 完整 Go 验证 | `go test -race -count=1 -cover -v ./...`，设置隔离数据库与 REQUIRE 环境变量；[日志](evidence/linux-race.txt) | `f21d156684` 通过；数据库实际执行；media 与 NFO 的 2 个 FIFO 案例因 DrvFS 不支持命名管道跳过 |
| Linux 静态分析 | 本地 `.bin/go vet ./...` | 退出码 0 |
| PostgreSQL 16.15 | [修正后整合日志](evidence/postgres-optimized.txt) | up/status/down/up、幂等 up、令牌摘要/禁用/撤销、ACL、约束、失败导入事务回滚、取消与迁移锁测试通过 |
| 媒体 Linux race | [专项日志](evidence/media-linux-race.txt) | 原字节、Range/多段/If-Range、符号链接逃逸与替换竞态、取消阻塞写入、并发额度回收通过 |
| 媒体 fuzz | `FuzzProductionGuard -fuzztime 5s` | 86,077 次执行通过；微基准详见 direct-delivery.md |
| NFO / CLI | [Windows](evidence/windows-tests.txt)、[Linux race](evidence/linux-race.txt) 与[兼容范围](nfo-compatibility.md) | 编码/根/字段黄金样本、原文保留、资源限制、危险 XML/路径拒绝、离线 URL、摘要隐私、退出码与堵塞 stdout 取消通过；不等同于编辑写回或真实客户端往返 |
| 当前镜像构建与冒烟 | [容器日志](evidence/container-current.txt) | `f21d156684` 镜像、真实 PG 迁移/doctor、NFO 原文 hash、HTTP 字节/权限/健康、停止退出码 0 与迁移 down 通过；Compose 未运行 |
| 工具安装安全 | Windows `test-toolchain.ps1`，Linux `test_toolchain.py` | Windows 12 案例、Linux 6 测试通过；校验和不符与路径逃逸均拒绝 |
| 工具验证 | Windows/Linux tools-verify、离线 bootstrap | Go 1.27.1 版本及哈希通过；Linux SDK 首次解压借助 Windows Python，不宣称 Linux 全空目录重建已验证 |
| 增量品牌 | `brand-scan --new` | 新增服务零违规；完整仓库品牌仍有遗留 |
| 忽略检查 | `gitignore-check` | 新工具/生成目录与新增文件检查通过；历史二进制全面审计尚未完成 |
| 原 LICENSE | SHA256 | `f371b80469fb235bc500ec29e0e85b682d4a6157a158567d828ff0be544d4f1d`，未改变 |
| 最终校验与清理 | [汇总记录](evidence/final-checks.txt) | 构建/lint/增量品牌/忽略检查与来源字节核对通过；全量品牌明确失败；专用测试数据库和容器已清理 |

独立源码归档 SHA256 为 `72bf942730578e54025dfbeed6773dea219785483c614ab6b7115fd077886bb6`。该复验明确跳过未配置 PostgreSQL 的集成测试及两个缺少权限的 Windows 符号链接案例；这些跳过不能当作数据库或文件系统特性的通过。

最终 Linux race 日志的逐包覆盖率：access/app/domain/logging 为 100%，HTTP 为 81.0%，media 为 86.5%，NFO 为 89.8%，postgres 为 78.8%，i18n 为 97.6%，config 为 94.5%，jelee-cli 为 40.5%；jelee/jelee-migrate/runtime/工具入口仍为 0%。Windows PostgreSQL 因跳过为 0%，其余上述包比例相同。完整覆盖率门槛尚未达到。

## 当前容器证据与版本边界

[当前容器日志](evidence/container-current.txt)对应源码 `f21d15668477bd5806e7e525149bfb373d9a68bd`，镜像为 `sha256:17c550a0d89547b8d33f22d74bb205a2653d07017d1b1152b3600faddfe60c16`。运行使用 UID/GID `65532:65532`、只读根文件系统及移除全部 capabilities。真实 PostgreSQL 的 up/doctor/provision/import、NFO 校验且原文 SHA256 不变、native 全量/Range、伪造 UA 的 web 403、转码 409、目录与 healthy 均通过；停止后记录 `Running=false ExitCode=0 OOMKilled=false`，迁移 down 完成。

[当前镜像静态检查](evidence/container-current-inspection.txt)确认仅三个静态 ELF 程序，无 PT_INTERP/PT_DYNAMIC，无 ffmpeg、ffprobe、shell 或 Go SDK；项目 LICENSE 字节及 SHA256 不变，Go LICENSE/PATENTS 和 CA 证书存在。

Compose 尚未运行。36 字节传输 fixture 不能证明真实视频播放或第三方客户端兼容；反向代理、滚动升级、恢复与完整生产运行工具要求仍未验收。[旧容器日志](evidence/container.txt)保留首轮镜像、网络失败与重试的历史记录，不能用于替代当前镜像证据。

测试数据库为本次专用 Docker 容器，使用已有精确镜像 `postgres@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685`。数据使用 tmpfs，密码随机生成且不写入跟踪文件。每次仓储测试使用随机 schema 并清理自己的 schema，没有操作用户现有数据库。全部测试后专用数据库容器已停止并由 `--rm` 移除，tmpfs 数据及本地测试凭据文件已清理；SDK、缓存、二进制和测试镜像保留以便复验。

## 实际服务冒烟

在隔离数据库执行 Linux 三个二进制：迁移 up → doctor → 创建 native/web 会话 → 登记测试文件 → 启动服务 → 发出真实 HTTP 请求 → SIGTERM 排空 → 迁移 down。实际退出码 0，输出：

```text
Jelee schema version=1 dirty=false
configuration: valid
PostgreSQL: connected
schema: 1 clean
production restrictions: enabled
developer mode: unavailable
PASS: native whole/range bytes, web forged-UA 403, transcode 409, catalog, graceful shutdown
Jelee schema version=0 dirty=false
```

样本是 36 字节传输测试文件，不是可播放视频；此测试证明 HTTP 字节投递、权限和生命周期，不证明真实播放器解码或第三方协议兼容。真实媒体/NFO/图片/三类客户端验收仍未进行。

## 查询计划

同一环境、10,002 条 items、受限用户仅有一条可见记录。基线 SQL 将管理员与 ACL 合并在 OR 中，扫描 10,002 条 items；修正后分离管理员与按库 LATERAL 分页，使用 `items_library_id_idx`，实际只访问 1 条 items。测试对访问行数设定上界，防止只看到 Index Scan 标签便误判通过。

保存的单次 EXPLAIN 执行时间为基线 2.683ms、修正后 0.164ms；[基线](evidence/postgres-baseline.txt) 与 [修正后](evidence/postgres-optimized.txt) 可比较。没有足够重复采样、冷热控制和稳定负载，**不据此声称 P95、20% CPU 降低或完整性能目标通过**。

## 已知未达标

- 完整品牌扫描仍会失败：旧参考实现保留，未通过放宽白名单掩盖。
- 未执行旧 .NET 全套构建/测试；完整前端、密码登录/用户管理、真实第三方兼容、旧库迁移、图片、字幕/音轨、扫描、TMDB、任务/Webhook、统计、客户端策略、详细 ACL、开发者模式、OTel/Prometheus 尚未交付。
- NFO 只读 adapter 与校验 CLI 已提交并经过 Windows/Linux 测试；修改后的 XML 序列化、按库批量处理、`--fix`、任务接入及真实客户端往返仍未完成。
- OpenAPI 当前仅描述基础路由，完整模型/示例/生成类型/文档同步门禁未完成。
- 覆盖率不满足全部核心 ≥70% / 关键 ≥85% 门槛。原始日志逐包列出结果，jelee-cli 为 40.5%，主服务/迁移入口与 runtime 等仍缺包内覆盖率。
- Linux FIFO 在原生文件系统上的验证未完成；Windows race 所需本地 C 工具链未固定和验收。
- G51 全套工具、合成媒体脚本、无 Docker 嵌入式数据库回退与全新克隆完整命令未完成。
- 迁移库内部部分调用仍使用后台 context；server-side lock_timeout=5s / statement_timeout=30s 防无界等待，取消不保证立即终止当前迁移语句。
- 容器/Compose、远端 CI、性能矩阵、百万条目/十万图片/24 小时稳定性与发布演练以各自证据为准；没有运行就不能标为通过。

## 复验

使用 README / quickstart 的本地工具命令。独立 PostgreSQL 测试需库名 `jelee_test`，设置 `JELEE_TEST_DATABASE_URL` 与 `JELEE_REQUIRE_INTEGRATION=true`，执行 `make test-integration test-race`。生产数据不能用于这些测试。证据日志不含令牌、数据库密码或媒体根路径。

## G05 錄製自動啟動來源裁剪

刪除兩個錄製host及註冊，移除設定更新建立錄製媒體庫回呼。focused 3pass，舊實作反驗證3fail並逐位元恢復，完整Debug17套件4159Passed／21NotExecuted／0fail；格式通過（workspace warning）。[合同與證據](recording-startup-removal.md)。直播控制器／排程／調諧器與資料仍待裁剪，G05維持部分完成，ABI與完整品牌失敗保留。

## G05 舊 HTTP 直播與頻道入口裁剪

刪兩個控制器及兩個專用DTO，明確501／feature_removed並保留設定授權／IP限制。643專項pass，舊程序集與OpenAPI反驗證2fail並逐位元恢復，完整Debug17套件4798Passed／21NotExecuted／0fail，格式／Win/Linux四語124鍵門禁通過。[合同與證據](legacy-removed-features.md)。內部服務、排程、設定及資料仍待裁剪，G05部分完成，ABI與完整品牌門禁保留。

## G05 自動工作與提供者裁剪

刪六個實作、五個queue呼叫、兩個排程constructor相依及三個專用四語翻譯鍵。positive10pass，舊實作negative8fail並逐位元恢復，完整Debug17套件4808Passed／21NotExecuted／0fail；格式與Win/Linux四語121鍵門禁通過。[合同與證據](live-feature-actors-removal.md)。G05.4有部分交付，核心服務、設定、資料與其他資源仍待清理。

## G05 一般DTO移除直播／錄製增補相依

DTO單層解耦及兩份既有測試建構參數更新，14項回歸與完整Debug17套件4808Passed／21NotExecuted／0fail、格式通過。[合同與證據](dto-live-decoupling.md)。原七檔跨層批次操作遭自動審查拒絕且未執行，已採獲准的DTO單層替代；其他核心／視圖／DI保持，G05仍部分完成。

## G05 使用者視圖移除直播／頻道增補相依

移除使用者視圖的兩個manager相依與外抓分支。四單元及兩HTTP／六請求pass，完整Debug17套件4814Passed／21NotExecuted／0fail，最後測試行尾修正後HTTP與格式再pass。[合同與證據](user-view-live-decoupling.md)。核心、DI、存量資料與實體相依仍待處理，G05保持部分完成。

## G05 直播核心註冊分層解耦

五項核心／延遲服務註冊移除，程序集探索保留必要媒體庫元件。六項啟動與解析驗收通過；[範圍與證據](live-core-registration-removal.md)。核心程式與存量資料仍待清理。

## G05 指南核心移除

刪除指南實作／介面834行，保留來源的兩天圖片快取常數改為本地持有。七項啟動與程序集整合驗收通過，[範圍與證據](guide-core-removal.md)。反向恢復兩舊來源時兩項程序集驗收全部失敗，finally恢復刪除狀態；完整Debug17套件4821Passed／21NotExecuted／0fail，格式通過，所有測試已結束。

## G03 HTTP 翻譯鍵靜態門禁

所有 HTTP 正式Go來源AST與四語資源對照，缺少／未使用鍵即失敗；八個抽取情境與一個缺失／未使用負例覆蓋驗收。[範圍與證據](http-i18n-key-audit.md)。產品來源與待授權直播核心保持未變。
