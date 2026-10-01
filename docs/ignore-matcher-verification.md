# 3D1A 忽略规则匹配验证

日期2026-10-01。源码提交 `9afc8f15c6ca3b190bc6eafdb7e24356ad1cd33c`，分支 `feat/jelee-ignore-matcher`，基于3C3C的 `bf5af35003bae0b7916797eddc1f7f9b6184099f`。本报告记录本地实际结果；新PR的远端CI另行核对。

## 已交付范围

`internal/platform/ignore` 提供纯 `.jeleeignore` 编译/求值、UTF编码、继承/优先级、父目录剪枝、来源行号、不可变Program、取消及固定资源限制。只使用标准库计算，不读磁盘或执行Git。调用合同见[组件说明](ignore-matcher.md)，固定上限见[设计](ignore-matcher-plan.md)。

新增真实Git测试目标和两平台CI步骤，版本/二进制/语料hash随测试输出保存。旧格式只有[固定来源审计](ignore-source-audit.md)，没有把审计当成兼容解析已实现。安全来源读取、符号链接、编译缓存、持久规则快照与生产扫描报告均留给后续小段。

## 最终快照与回归

两平台的326个源码/构建配置文件SHA256完全一致，执行后再次核对未变。原生Linux在自建 `/tmp` 快照运行并在结束清理；现有Go1.27.1和GCC/binutils身份先与项目记录核对。没有安装新工具或修改全局Git设置。

| 验证 | 实际结果 |
| --- | --- |
| Windows必需Git目标、三build、lint/vet、全模块测试 | 通过，28个包；[命令/哈希/跳过](evidence/ignore-matcher-windows.json) |
| 原生Linux必需Git目标、三build、vet、全模块race | 通过，28个包、567项顶层测试；race命令142.698秒；[完整摘要](evidence/ignore-matcher-native.json) |
| 新ignore包语句覆盖率 | 两平台均454/501 = 90.6188%，超过85%门槛 |
| 原生Linux全模块覆盖率 | 82.8%；不是整个项目已达85% |
| 增量品牌、产物忽略与diff检查 | 通过；[保留项/不变量](evidence/ignore-matcher-guards.json) |

必需Git oracle先经Make/PowerShell目标运行，全模块命令随后用 `-skip '^TestGitOracle$'` 排除已单独执行的入口。ignore包其余23项顶层测试与种子检查没有跳过；不将这个排除算作第二次Git验证。

Windows全模块出现171个SKIP事件，其中158个属于未配置Windows测试DSN的PG测试，13个为平台/专用工具场景；本段没有执行Windows race。原生Linux在专用 `jelee_test` 数据库运行PG测试，PG零SKIP；另6个沙箱/真实工具专用场景明确跳过，原因保存在摘要。没有把这些跳过算成通过。

本段没有再次运行带tag的1,000文件媒体容器验收；生产scanner/worker/DB代码和已发布迁移均未改动。前段相应实际验收及远端结果见[3C3C报告](nfo-worker-verification.md#远端-ci)，它不是新版本的全场景执行记录。

## 真实 Git 差分

80组手写黄金案例检查三态、来源、物理行号、决定路径和父目录阻挡；另外60组固定Cartesian案例覆盖5种glob上下文×6种字符类/转义模式×2种大小写模式，每组60个短路径，含两层来源。组合结果直接取自Git，对照完整Match。

| 平台/实测Git | 实际组数/候选数 | 明确遗漏 |
| --- | ---: | --- |
| Windows amd64 / 2.55.0.windows.3 | 132 / 3,768 | 8组、16候选：含竖线、尾空白或空白文件名，无法正常落盘 |
| 原生Linux amd64 / 2.53.0 | 140 / 3,784 | 0 |

两者均通过，语料SHA256相同：`d8f06c60a0594120130f5cbfe1522905726f534e6521244eac798539c6e3e52a`。Windows仍执行全部80组纯值黄金测试。Git完整路径、可执行文件SHA256、各遗漏名称和日志hash见[结构化结果](evidence/ignore-matcher-oracle.json)。

Git在真实私有小仓库里读取映射为 `.gitignore` 的UTF-8文本；测试隔离配置、templates、excludes和环境。固定参数只执行版本查询、init和check-ignore；最多256条查询/批次，stdin256KiB、stdout1MiB、stderr64KiB。子程序5秒和共享context60秒并不保证文件I/O、hash及清理可被硬中断。子程序退出与输出复制会等待回收。

已实测的特殊行为包括完整组件中连续两个以上星号、转义slash、反序range保留首字节、ASCII不敏感模式的 `[A]`/`[A-Z]` 区别、POSIX字符类以及非ASCII字节匹配。UTF-16解码属于项目扩展，另测BOM、代理对、非法字节和行号，不宣称Git直接读取UTF-16。

来源激活另有canary：Linux不同大小写目录不互用来源；Windows文件系统可把两者解析到同一控制文件。Program输入是精确路径值，两个模式都不折叠来源目录；该值合同已明确说明，不能把它冒充所有文件系统的来源发现行为。

## 上限、取消和不可变性

实际边界测试涵盖来源数、原始/解码总字节、行、token、字符类、候选/来源路径长度和深度；恰好上限与可到达的加1分别检查。规则上限与总行数相等，单规则token上限亦受行长约束，测试没有声称能独立越过先行限制。

单来源decoded384KiB受raw256KiB及UTF-16 BOM支配，最大可达值为cap−3；该可达极值已测，未伪造公共输入来覆盖不可达加1分支。Compile的逆序长来源+大文本能耗尽共享预算，Evaluate的长路径/重复星号组合能耗尽求值预算，两者均返回零结果。预算恰好用完合法，下一次超额收费才失败。

真实取消测试在调用中同步到context检查点后取消并join，验证Compile无部分Program、Evaluate无部分Match，之后正常调用仍可用。并行求值、修改调用方文本/slice、修改返回diagnostics均不影响已编译程序。架构检查防止生产包引入I/O依赖。

## Fuzz 与微基准

原生Linux同版源码分别执行两个15秒配置、parallel=2的fuzz：Compile实际86,661次，Evaluate实际22,545次，均通过。[输出与日志hash](evidence/ignore-matcher-fuzz.json)保留实际耗时；进程时间还包含构建/收尾，不把配置15秒当成整个命令硬期限。fuzz限制输入尺寸，不对每个随机输入启动Git，也不构成全输入证明。

原生Linux amd64、AMD Ryzen 7 9850X3D，`-benchmem -benchtime=100x -count=3`，每例准备在计时前，命中结果在循环内核对。三次范围：

| 每次操作 | 时间范围 | 分配 |
| --- | ---: | ---: |
| Compile 1 / 100 / 1,000条规则 | 0.633–1.121 / 48.268–60.149 / 338.585–374.432微秒 | 688 / 37,648 / 441,747–441,799 B/op |
| Evaluate 1 / 100 / 1,000条规则 | 0.266–0.505 / 63.793–68.800 / 625.265–799.616微秒 | 此批短规则均0堆分配 |
| Evaluate 1,024字节路径 | 25.029–27.506微秒 | 0 B/op |
| Evaluate 128个来源、仅1个激活 | 0.303–0.320微秒 | 0 B/op |
| Evaluate 128组件路径 | 462.366–481.770微秒 | 0 B/op |

[原始基准](evidence/ignore-matcher-benchmark.txt)包含全部三次结果。这是组件微基准，不包括扫描、控制文件读取或缓存，不推算大库吞吐、P95、进程RSS或所有模式都零分配。

## 修正与未完成项

最初Git测试辅助器对目录追加尾slash，造成四个差异；这会向Git询问不同的词法路径并可能激活目录内部规则。改为规范路径加实际目录kind后，两平台全部最终对照通过；没有因此放宽matcher断言。Windows非法落盘名称显式列为遗漏并由Linux核对。没有修改已发布迁移、Go依赖、许可证或原需求文本。

G22.1与G22.3推进为部分完成，G22.2仍仅审计；G22.4缓存、G22.5扫描报告尚未接线。矩阵共336项：4已完成、172部分、160阻塞。完整品牌清理和全项目要求仍未完成，本段通过不能替代整项目验收。

## 远端 CI

[PR #11](https://github.com/MoYuanCN/Jelee/pull/11) 的[Go CI](https://github.com/MoYuanCN/Jelee/actions/runs/36801016335)已结束：Windows、Linux基础检查（含必需Git oracle）及PostgreSQL/race/真实媒体probe与NFO验收均通过。[结构化结果](evidence/ignore-matcher-ci.json)保存每项job状态。全量品牌门禁仍失败：15,278处旧名称、96处白名单命中；没有放宽门禁或将整体CI记为通过。
