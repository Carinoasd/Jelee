# 持久扫描与探测 worker：3C2B 验证

源码提交 `b59be8d389216c3853a0a6d18b1f1af130745a9f`，基于 [PR #6](https://github.com/MoYuanCN/Jelee/pull/6) 的 `9f5e98aab6055e774866cc272e8017ff55b6ef93`。日期2026-10-01，Asia/Taipei。实现契约见 [probe-worker.md](probe-worker.md)。

本段接通 schema5 持久请求、真实扫描/探测 worker、管理员 API/CLI、能力降级和周期清理。探测默认关闭；Linux amd64 受保护容器显式启用。用户已恢复工作，要求每个小段验证、推送、建立普通 PR 后继续。

## 真实 1,000→0→17 次探测

[验收摘要](evidence/probe-worker-acceptance.json)由正式 `make probe-worker-test` 生成。测试使用生产镜像的工具与隔离配置、专用 PostgreSQL schema、真实扫描/worker/ffprobe，以及正式 HTTP handler 的 httptest 入口。

| 场景 | 实际 metadata 子程序启动 | 成功探测 | 正快取命中 | 耗时 | 快取行数 / 计费字节 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1,000 个路径首次扫描 | 1,000 | 1,000 | 0 | 181.871秒 | 1,000 / 2,784,000 |
| 原档与工具身份不变 | 0 | 0 | 1,000 | 12.034秒 | 1,000 / 2,784,000 |
| 原子替换其中17个目录项 | 17 | 17 | 983 | 14.436秒 | 1,000 / 2,784,051 |

三轮 failed、negativeHits、changed、unavailable 都为0。计数来自实际成功 `Start` 的 metadata 子程序，与 prober 调用次数和成功结果交叉核对；health 使用独立 runner。每轮结束 active child lifecycle 和数据库 file lease 都为0，JSONB 实际计费与 quota 相等。

1,000 个路径是同一自建 MP4 的硬链接；替换时使用另一个自建 MP4，并逐路径核对 SHA256、大小、mtime 和文件身份，确认只有17个改变。所有结果核对 H264/AAC 与320×180/640×360分辨率，原始 fixture SHA256 未变。逻辑媒体字节84,806,000，初始单一 seed 为84,806字节；这不是1,000种独立媒体或物理数据库大小的测量。

进程统计的 Peak 表示已经取得额度、尚未完成清理的调用生命周期，包括启动失败；它是实际存活子程序的上界，不是操作系统采样值。此次单库顺序 miss 的累计 Peak=1，配置上限=2，未将此结果冒充双库并发压力证明。

源内容摘要 `be09a3cfd52ac5b04301fb44bc795a94ca12c732bdeefe2f1a50b7e1f869fe85` 覆盖生产 Go、runtime 测试、迁移、manifest、Dockerfile 与构建/验收脚本；执行结束再次核对相同。验收容器、镜像、自建 schema 和暂存目录已清理。生产镜像 ID、可信工具身份与 seed hashes 均保存在 JSON 摘要。

## 真服务 HTTP 与停止流程

独立[真服务摘要](evidence/probe-worker-http.json)使用正式 `/jelee` 入口、Fx 和真实监听端口，非 root、只读根/媒体、无 capabilities、no-new-privileges，内存768 MiB、pids128、暂存64 MiB。

- 实际 migrate 到 clean5，通过 stdin 引导管理员及 CLI 登记只读测试库；healthz、readyz、system capability 正常。
- 两个正常文件和一个坏文件全部处理完成，父任务成功：succeeded=2、failed=1；下一轮 hits=2、negativeHits=1，未重新启动坏文件探测。
- 整库重建期间观察到真实 child lease 后发送 SIGTERM：服务 exit0、OOM=false，HTTP 关闭，parent/file leases 与 active-lease quota 都为0。
- 原 fixture 和复制媒体的 SHA256 未变；服务日志不含测试密码、token 或媒体根。自建容器、镜像、schema 和暂存资料已清理。

真实服务镜像与1,000路径验收镜像单独保存 ID，不混称同一镜像。坏文件固定 `probe_failed`、负 TTL 后可重试、工具身份变更重探测，另由真实 PG 和 worker/adapter 测试核对。

## 完整回归与门禁

| 验证 | 已观察结果及边界 |
| --- | --- |
| [Windows 完整测试](evidence/probe-worker-windows.json) | 三命令 build、gofmt/vet、27包 test 通过。89个skip实例：76个PG（未配置DSN，含子测试）、12个平台/symlink限制、1个runtime-image；原因逐项保留。Windows race 未执行 |
| [原生 Linux 完整 race](evidence/probe-worker-native.json) | 自建原生 `/tmp` 源快照，固定Go1.27.1、登记过的GCC；三build、完整vet和27包race测试通过，含required真PG。6个skip：5个需独立保护配置的测试、1个optional固定工具；后者随后required运行通过，前者由本段独立sandbox目标验证 |
| Linux required工具/素材 | 真实生成素材与metadata、受限fixture命令、固定ffprobe身份三个目标全部通过、零skip。原生快照源码哈希再次核对，结束后清理 |
| [真实 PG](evidence/probe-worker-postgres.json) | 独立58个顶层测试47.552秒、零skip；补充15个请求/故障目标14.219秒、零skip；最终完整Linux运行PG52.856秒、79.7% |
| [原生 sandbox](evidence/probe-worker-sandbox.txt) | 必需非root只读容器：零skip；实际父进程65.2%、child60.8%，合并86.8%通过原85%门槛；process89.7% |
| [来源及检查摘要](evidence/probe-worker-checks.txt) | 冻结源码与两平台证据对应；diff、增量品牌、gitignore检查通过；需求原文、LICENSE和000001–000004未变 |

关键整包覆盖率：domain98.9%、app89.0%、config95.9%、worker87.9%、HTTP88.7%、CLI76.3%、Linux process89.7%、probe94.0%、runtime55.5%、PG79.7%。PG两个新增实现文件分别为145/178=81.46%、92/109=84.40%。不能以局部测试通过宣称全项目85%或90%达标。

## 失败记录与修正

1. 前段 PR #6 的 Linux CI run `36784081954` 及本地 sandbox 首次失败于84.7%覆盖率。新增 `MetadataArgumentsDigest` 的固定摘要与返回参数切片隔离测试后，实际合并覆盖率86.8%；未改生产 sandbox、测试选择器或85%门槛。
2. 首次完整 Linux 测试在 Windows DrvFS 路径运行，私有目录权限与纳秒mtime行为不满足测试契约。原失败日志保留在忽略的 `.testdata/probe-worker-linux.jsonl`；后续将相同源码解包到原生 Linux 文件系统，完整重跑通过。没有修改生产代码来放宽权限/mtime断言。
3. 1,000路径验收初试的测试密码成本低于应用下限，尚未启动任务即拒绝；修正测试参数。随后一次运行在加强外层清理与凭证文件创建时机时主动中止，实际核对自建schema清理完成；第三次正式全程通过。未把前两次计入成功耗时。
4. 真服务烟测最初错误预期schema4，修正脚本为schema5后重新从空schema完整运行通过。原失败摘要仍保留于本机忽略目录。

原始输出保留在 `.testdata`，公开摘要只含结果、跳过原因与原输出 SHA256，不发布凭证或原始媒体路径。提交检查时仅删除验收Python脚本末尾多余空行，去除末尾CR/LF后文本完全相同；测试时与提交时脚本SHA256分别记录。其余程序、测试和配置对应冻结源码，文件证据另行提交。

## 重现与未完成范围

Windows 使用 `scripts/make.ps1 build`、`scripts/make.ps1 lint` 与 `scripts/run-go.ps1 test -count=1 ./...`。原生 Linux 使用项目固定SDK，运行 `make build lint test-race`，专用 `JELEE_TEST_DATABASE_URL` 指向 `jelee_test` 并设 `JELEE_REQUIRE_INTEGRATION=true`。需先按[工具链](toolchain.md)引导固定媒体/runtime并生成fixture，再运行 `make sandbox-test probe-worker-test`。测试凭证从私有环境注入。

本段完成 G19.3 快取/重建/清理及 G19.4 损坏媒体容错。G13.4 的监听与去抖、G19.5 进程池、NFO/图片任务、完整catalog导入、MediaInfo/mkv补充、特殊codec、Windows正式隔离、10万/50万/24小时及跨实例实际负载仍未完成。edge 指纹无法发现保持size/mtime/edge的中段修改，文件系统阻塞open/stat也不保证硬取消。

完整品牌门禁仍有遗留失败；增量门禁通过不能替代全仓通过。

## PR #7 的 CI 回归修正

[PR #7](https://github.com/MoYuanCN/Jelee/pull/7) 首次 [CI](https://github.com/MoYuanCN/Jelee/actions/runs/36788523456) 的Windows通过；Linux在双worker停止测试出现偶发失败。原断言使用全局active计数，误把「B只在等gate，取消后释放B」当成「A还没join却释放A」。生产代码按各自parent执行与join，没有要求不同parent同时释放。

测试修正提交 `ef115c27a00c1286f482ca7ba0ed39b945123502` 记录active child所属parent，并用channel强制B在A仍active时释放：保留A自己的join要求，也明确验证B不必等待A。旧断言在这个固定顺序下必然失败；修正后Windows和原生Linux race各100次目标运行、完整worker测试及vet通过。Windows整包87.9%。[原失败与重现/修正证据](evidence/probe-worker-ci-correction.json)保留真实SHA256；只改测试，不改生产流程或降低门槛。补推后CI结果须另行核对。
