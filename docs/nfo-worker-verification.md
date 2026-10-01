# NFO 工作流程与图片比较：3C3C 验证

源码提交 `47da27b5ff090a71e6f55aae0b2e6c00871f825b`，基于 [PR #9](https://github.com/MoYuanCN/Jelee/pull/9) 的 `676c8edcdb0b15b34455af4c1439eee265587e49`。日期 2026-10-01，Asia/Taipei。本报告记录本地实际验证；推送后的远端 CI 须另行核对。

实现范围是显式 NFO 入队意图、inventory → NFO → probe 的工作顺序、管理 API/CLI、当前验证结果以及图片属性比较。库默认 off，请求默认 nfo=false。接口、资源上限与回滚见 [工作流程](nfo-worker.md)。本段不交付 Catalog 来源合并、XML 写回、图片解码/下载或持续监看。

## 跨平台与数据库

| 项目 | 实际结果 |
| --- | --- |
| [Windows全模块](evidence/nfo-worker-windows.json) | 三命令build、gofmt/vet、27包测试通过；171个明确SKIP，其中158个为未配置DB的PG实例，其余为平台/专用环境条件；未跑Windows race |
| [原生Linux全模块](evidence/nfo-worker-native.json) | 固定Go1.27.1与已登记GCC/binutils，原生临时源码快照；三build、vet、全模块race通过，27包、544个顶层测试，PG零SKIP；测试144.943秒，其中PG124.310秒 |
| [普通PG专项](evidence/nfo-worker-postgres.json) | 114顶层测试、268子测试，零SKIP，114.580秒；这一轮不带race |
| [源码与门禁](evidence/nfo-worker-guards.json) | 两平台314个源码/配置文件哈希一致；001–006十二个迁移、LICENSE及原始需求保持不变；增量品牌、gitignore和diff检查通过 |

原生Linux的6个SKIP为专用工具/沙箱条件：instrumented child覆盖、固定ffprobe显式开关、root-owned保护路径、真实工具profile及两个需要隔离UID线程预算的案例。没有把它们计为通过；生产helper的实际调用由下述独立容器验收覆盖，但不冒称它替代每个专用测试。

最终原生Linux语句覆盖率：domain98.36%、app88.56%、NFO90.78%、HTTP90.21%、PG79.77%、jobs84.98%、runtime64.95%、CLI81.03%，全模块82.4%。真实容器验收不带coverage，不将其调用额外计入这些百分比；完整项目覆盖率门槛尚未满足。

关键断言包括：

- NFO/probe 意图与可信身份原子入队；省略与显式 false 等价。停用能力后保留请求仍可重放，不能重新绑定身份。B 的只读历史缺少公开意图时，retry 和提交重放均返回冲突。
- 历史 summary 与当前观察分开；live-admin 查询、策略修改和重放在等待数据库锁后仍检查会话到期。旧 observation 被替换、过期或作用域改变后不可读取。
- worker 的冷/暖/负缓存、末次读取变动、取消、并发两个 slot、能力失效、阶段恢复及 Finish 根映射漂移；Windows 的真实文件 reader → worker 在无 probe 时处理有效和坏 XML。
- 图片旧属性保持未知；失败、取消、skipped 不报告已确认缺失，不替换已接受基线。无最终比较的历史任务只按自己的 inventory 返回 uncompared，后续基线变化不改写旧计数。
- 007 真 up/down/up、四种活动 B phase 拒绝升级、活动 C frozen-off 也拒绝回退；真正 legacy-off 不因当前库策略而被暗中启用。001–006 保持不变。
- HTTP 与 CLI 覆盖严格输入/响应、管理员认证、分页上限、受限问题前缀、固定错误和四语言映射。当前观察只表示有效缓存，GET 不重新读文件。

## 真实混合库

执行 `python3 scripts/test_nfo_worker.py`，独立测试数据库连接通过私有环境提供。两组分别创建隔离 schema、容器和生成素材，使用真实 reader/parser、PostgreSQL、生产 worker/Fx 生命周期及固定隔离 ffprobe helper。素材只读挂载；控制器只替换本轮指定的自建测试文件。

[最终结构化报告](evidence/nfo-worker-acceptance.json)中的两组均通过，零SKIP，临时schema/容器/镜像/素材均已清理。[媒体源码核对](evidence/nfo-worker-media-source.json)与全模块快照对应。固定构成为：

- 1,000 文件：400 NFO、100 影片、500 图片；NFO 中各有 10 个坏 XML、语义错误、warning 和多集样例。
- 100 文件：40 NFO、10 影片、50 图片；上述特殊样例各一个。
- 两组均含带中文的 UTF-8、UTF-16LE、UTF-16BE 和 GBK；SQL 核对摘要、编码、问题计数、多集、配额和每份来源完整 hash。
- 暖扫仍逐份执行初读与末读/hash；只省去 XML Parse。图片比较采用 kind/size/mtime，不证明像素或文件内容相同。

| 库/轮次 | 实际Parse | 完整Read / Hash | 成功读字节 | 实际ffprobe子程序启动 | 图片统计 | 单次耗时 |
| --- | ---: | ---: | ---: | ---: | --- | ---: |
| 1,000 冷扫 | 400 | 800 / 800 | 165,800 | 100 | added500 | 46.400秒 |
| 1,000 暖扫 | 0 | 800 / 800 | 165,800 | 0 | unchanged500 | 13.444秒 |
| 1,000 改17NFO/23图片 | 17 | 800 / 800 | 165,800 | 0 | changed23/unchanged477 | 14.120秒 |
| 100 冷扫 | 40 | 80 / 80 | 16,580 | 10 | added50 | 3.224秒 |
| 100 暖扫 | 0 | 80 / 80 | 16,580 | 0 | unchanged50 | 1.454秒 |
| 100 改3NFO/4图片 | 3 | 80 / 80 | 16,580 | 0 | changed4/unchanged46 | 1.475秒 |

大组每轮380 valid/20 invalid；暖扫380正命中/20负命中，修改后363正命中/20负命中/17解析。小组分别38 valid/2 invalid，暖扫38/2命中，修改后35/2命中/3解析。操作计数来自实际reader调用和OS子程序启动，不从已提交job计数反推。成功字节不包含失败读取中未知的部分字节。两组串行库内的API/child lifecycle峰值均为1；上限2的并发约束另有单元测试，不把lifecycle峰值冒称精确同时存活OS进程数。

两组各在完成真实inventory和一次真实NFO读取后，通过HTTP取消在途工作。任务最终cancelled，图片missing=0/comparisonComplete=false，完整已接受baseline JSON保持不变。随后创建新NFO-only工作，所有图片恢复unchanged且比较完整；实际Parse和ffprobe启动均为0，仍执行每份NFO两次完整读取/hash。该测试覆盖实际取消路径；不可读目录的图片保护由真PG手工批次边界测试另验，未将其冒称真实权限故障注入。

关机验收运行在测试可执行文件中，使用真实 HTTP listener 与生产 Fx hooks，不冒称该文件就是发布的 main。控制器发送真实 SIGTERM，验证 HTTP 关闭、工作/维护退出、reader 与子程序活动数及数据库租约归零。测试屏障在真实 Read 完成后等待；这不证明可硬中断阻塞的文件系统 syscall。

容器限制为 Linux amd64、UID 65532、只读根/媒体、移除 capabilities、no-new-privileges、2 CPU、768 MiB、128 PID。专用验收 binary 的 CGO 关闭，不具 race detector。最终样本与其他验证并行运行；时间包括扫描、数据库、阶段执行和完成轮询，不是独占机器的性能基准、P95或硬件无关性能承诺。

## 既有影片回归

既有[1,000影片回归](evidence/nfo-worker-probe-regression.json)也在相同源码快照通过，零SKIP：冷/暖/修改17个文件的真实子程序次数为1,000/0/17，修改后983命中；耗时187.518/12.031/14.626秒。原素材hash、停止后活动数/租约归零以及自建资源清理均核对。这组与混合库分开记录，未用NFO验收代替原probe回归。

## 独立 100 NFO 微基准

Windows amd64、AMD Ryzen 7 9850X3D，固定 Go；每次处理 100 个不同的约 16 KiB 文件。素材准备在计时前，逐份核对完整 hash，原始字节在计时后复核。命令：`scripts/run-go.ps1 test -run '^$' -bench BenchmarkObservedHundredFiles -benchtime 2x -count 3 ./internal/adapter/nfo`。

| 每次操作 | 三次样本范围 | 分配字节范围 | 实际 Read / Hash / Parse |
| --- | ---: | ---: | ---: |
| 全读并 hash | 23.209–23.565 ms | 4,577,092–4,577,140 | 100 / 100 / 0 |
| 全读、hash、Parse | 36.887–42.031 ms | 15,611,496–15,615,940 | 100 / 100 / 100 |

[原始微基准输出](evidence/nfo-worker-benchmark.txt)不含 DB、worker 的末次读取、素材准备或最终原文检查；它独立于上面的 100 文件混合库验收。

## 查询规模

当前观察查询的真 PG 样例包含两个库共 10,000 个缓存行，使用实际生产 SQL。匹配当前作用域的第一页返回 21 个候选（公开 20 项加 continuation 判断），访问 21 行并使用范围游标索引。过期或根失效数据可能使扫描量增加，仍受容量与 DB 时限约束；不声称任何分布都是常数查询。

图片聚合样例含 10,000 旧基线和 9,600 当前条目。结果为 added=100、changed=1,000、unchanged=8,500、missing=500；每条聚合 SQL 访问合计 19,600 个关系行，仅返回一个计数结果。没有关闭 seqscan 等 planner 提示。这是有界库存聚合证据，不能替代 100,000/500,000 规模和 24 小时验收。

## 失败、修复与限制

Windows 最终 lint 首轮未能启动 gofmt：263 个绝对文件路径的参数合计 34,142 字符，超过 Windows 命令行上限，PowerShell 随后报告 StandardOutputEncoding 错误。改为 pinned gofmt 递归检查 cmd/internal/tools，仍禁止未格式化源码通过。直接 lint 和重新执行的完整 Windows 检查通过，没有放宽门槛。

第一轮原生 Linux build/vet/race 测试通过后，源码一致性校验发现上述 Windows 脚本在测试期间修改，拒绝将该轮标为最终证据；[原日志哈希与原因](evidence/nfo-worker-reverification.json)保留，最终冻结后重跑通过。测试 fixture 曾尝试写入非法 frozen-off progress，被既有 CHECK 正确拒绝；修正测试为先断言约束，再仅在私有 schema 移除该 CHECK 验证读取防守，未更改已发布迁移。

完整品牌门禁仍受保留的旧服务端命名阻挡。新增服务的增量扫描不替代完整门禁，覆盖率与未执行平台/专用场景按最终记录单列。全项目 336 项需求尚未全部完成。

## 远端 CI

[PR #10](https://github.com/MoYuanCN/Jelee/pull/10) 的 HEAD `bf5af35003bae0b7916797eddc1f7f9b6184099f` 在[Go CI](https://github.com/MoYuanCN/Jelee/actions/runs/36798696038)完成 Windows/Linux foundation、PG migration/repository integration、race、真实影片 probe 和真实 NFO/视频/图片混合验收，以上均通过。完整品牌检查失败，故整个 workflow 的结论仍为 failure；没有放宽门禁。[逐项公开结果](evidence/nfo-worker-ci.json)随下一小段保存。
