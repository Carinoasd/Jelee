# 3D1B 忽略来源与编译缓存验证

日期2026-10-01。源码 `760e02f299ad11d04219fd19fd83b7c457622f94`，分支 `feat/jelee-ignore-source`，基于PR #11的 `32e3c85ca3a010641858007d11382557cb2fe7ac`。本报告记录本地实际执行；新PR的远端CI另行核对。

## 交付范围

新增 `internal/adapter/media/ignore`（`ignoresource`）：可信根内可达祖先的严格no-follow来源观察、两次完整读取/hash与身份核对、候选判定/来源诊断、取消回收、有界并发和编译LRU。调用合同见[组件说明](ignore-source.md)，预算与取舍见[设计](ignore-source-plan.md)。缓存只省Compile，每次仍重新观察present/absent和内容。

Windows使用held handle相对NtCreateFile、OBJ_DONT_REPARSE及reparse/type检查；身份使用64位volume和完整128位file ID。固定x/sys提供原生路径转换，但未导出对应释放函数，因此从系统ntdll解析固定的RtlFreeUnicodeString并在分配前确认入口、结束时释放。Linux使用openat2的NO_SYMLINKS/NO_MAGICLINKS，根以下再加BENEATH；不退回较弱打开方式。没有新依赖或工具安装。

本段没有启用生产扫描忽略，没有新增HTTP/CLI或数据库迁移。旧格式兼容仍待实现；规则清单持久化、generation/fencing、排除报告和库存missing保护属于3D1C。

## 冻结来源与回归

两平台的342个源码/构建配置文件SHA相同，执行后核对未变，并逐一比对上述提交的Git blob。[保留项证据](evidence/ignore-source-guards.json)同时确认go.mod/go.sum、LICENSE、原需求及001–007共14份迁移原文不变。增量品牌0违规、产物门禁0违规、diff检查通过；完整品牌清理仍未完成。

| 验证 | 实际结果 |
| --- | --- |
| Windows必需Git oracle、三build、lint/vet、全模块测试 | 29包通过；全模块测试7.695秒；[完整摘要](evidence/ignore-source-windows.json) |
| 原生Linux必需Git oracle、三build、vet、全模块race | 29包、604项顶层测试通过；race命令152.514秒；[完整摘要](evidence/ignore-source-native.json) |
| 新来源包覆盖率 | Windows378/424 = 89.1509%；Linux355/388 = 91.4948% |
| 新来源包顶层测试 | Windows33通过、1跳过；Linux37通过、0跳过 |
| 原生Linux全模块覆盖率 | 83.1%；尚非整个项目达到85% |

两平台先执行必需Git目标，全模块随后用 `-skip '^TestGitOracle$'` 排除已经单独运行的入口。Linux从自建 `/tmp` 源码快照运行，结束清理；现有Go1.27.1、GCC15.2/binutils2.46与注册hash先行核对。专用PG测试零SKIP，另6个沙箱/真实工具专用场景明确跳过，原因保留于摘要。未再次运行带tag的1,000文件容器媒体验收；现有scanner/worker/DB未变，前段远端实测见[PR #11结果](ignore-matcher-verification.md#远端-ci)。

Windows共172个SKIP事件：158个PG测试因未配置Windows DSN而跳过，另14个平台/工具场景（包括本段1个symlink权限限制）。没有运行Windows race。第一次受限环境执行在既有toolidentity的自建目录ACL设置处失败；相同定向测试及随后同一冻结源码的完整验证在正常宿主权限下通过，没有改变源码或断言。失败日志hash与原因另保存在Windows摘要，没有记成通过。

## 真实来源行为及限制

两份真实规则、前后固定长注释夹住中段规则；修改后恢复原size/mtime。两平台记录如下，详见[测试名与原始计数](evidence/ignore-source-contracts.json)：

| 调用 | 完整读取次数 | 读取bytes | Compile | 返回后owned目录/文件 |
| --- | ---: | ---: | ---: | ---: |
| 冷缓存 | 4 | 4,146 | 2 | 0 / 0 |
| 暖缓存 | 4 | 4,146 | 0 | 0 / 0 |
| 中段等长改文、mtime恢复 | 4 | 4,146 | 1 | 0 / 0 |

每个present来源每次读取后均计算完整原文SHA；表格直接计数的是读取和Compile，没有伪造独立hash指标。修改后决定与token改变，原文仅由测试夹具显式修改，观察器保持只读。另测原本absent的规则新增、规则删除、prune后不进入深处、目录不读自己的控制文件、二次观察发现新规则、截断/增长/替换/删除、读错误与close失败、真实文件handle的取消关闭和callback join。

Linux原生文件系统在非root UID下验证7种root/祖先/内外/悬空symlink、无writer FIFO、leaf/parent权限拒绝、区分大小写，以及最终重开时新出现symlink。替换根/父目录但硬连结同一规则inode仍被拒绝。Windows5种真junction全部拒绝；一般symlink建立权限不足，单列SKIP。Windows实际大小写alias与模式Case选项分开验证。Windows的held root/parent rename被OS拒绝；另用第二次返回不同真实root、但硬连结同一规则文件的私有测试入口验证身份失配，未把OS阻止改名冒充替换分支。

规则观察不是多文件原子snapshot；不承诺阻止hardlink、bind mount或最后检查后的变化。普通open/stat及停滞文件系统调用也没有硬中断保证。Windows真实ACL拒绝场景没有另做来源专用测试；固定错误映射和注入读拒绝有测，Linux权限拒绝是真实文件权限测试。

## 预算、取消与缓存边界

私有可控ports验证两项准入、第三项立即Busy、一次共用30秒/更短调用方deadline、取消后join、所有FD关闭后等待发布锁期间取消不发布缓存、错误零Observation、暖项不回退、诊断副本隔离、root/Case隔离、LRU项数/权重与淘汰后已有Program不变。129条独立祖先链的合同测试证明128来源限额不等于全库总额；这组使用可控来源，不冒充大库扫描吞吐实测。

编译累计输入32MiB恰好与加1经完整Resolver路径验证，失败不发布暂存项。单份256KiB实测读入；4MiB恰好与加1测试只隔离下层reader共享预算。冷链16×256KiB需要累计34MiB Compile输入，会先触发32MiB工作限制，所以没有声称4MiB上限可独立端到端达到。合法候选因追加控制文件basename而超长时返回Limit。快取64项/16MiB是保守记账，另有每调用暂存，不能称为整个Go进程RSS上限。

## 组件微基准

同机AMD Ryzen 7 9850X3D、两份小规则，`-benchtime=100x -count=3`；全部包含两次读取/hash、重开与资源关闭。cold为每次新建Resolver，warm为已编译缓存；没有控制操作系统页面缓存。

| 平台 | cold | warm | cold / warm Go分配 |
| --- | ---: | ---: | ---: |
| Windows | 464.460–502.855微秒 | 386.008–444.663微秒 | 10,224 / 7,088 B/op，169 / 127 allocs/op |
| Linux原生/tmp | 45.419–48.109微秒 | 41.417–45.519微秒 | 8,784 / 5,648 B/op，159 / 117 allocs/op |

[Windows原始输出](evidence/ignore-source-windows-benchmark.txt)、[Linux原始输出](evidence/ignore-source-native-benchmark.txt)。这是小来源组件微基准；没有推算跨平台磁盘性能、完整扫描吞吐、P95、OS原生分配或进程RSS。

G22.4推进为部分完成；其生产扫描失效接线仍未完成。矩阵现4已完成、173部分、159阻塞，共336项。G22.5扫描报告及安全库存比较仍待交付，本段通过不代表完整第3阶段完成。
