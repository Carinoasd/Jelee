# NFO 来源读取：3C3A 验证

源码提交 `feeee03da801bb4730eca86b39d55eb5b1521783`，基于 [PR #7](https://github.com/MoYuanCN/Jelee/pull/7) 的 `893432fc51312ae89ff5f40d62e2b6f4323b67fc`。日期2026-10-01，Asia/Taipei。

本段让现有单档 NFO CLI 在解析前检查来源一致性，并提供完整内容指纹。`ReadSource` 保留私有原文；`Stamp` 按值返回大小、mtime、SHA256与指纹版本；`Parse` 只解析这次保留的字节。`ReadFile` 直接使用这条路径。具体边界见[NFO契约](nfo-compatibility.md)，后续库级快取与任务见[分段计划](nfo-incremental-plan.md)。

## 已执行验证

| 项目 | 实际结果 |
| --- | --- |
| [Windows](evidence/nfo-source-windows.json) | 三命令build、全项目gofmt/vet、NFO/CLI/architecture三个包测试通过；NFO90.5%、CLI76.4%。1个symlink权限SKIP，未执行Windows race |
| [原生Linux](evidence/nfo-source-native.json) | 自建原生文件系统快照，固定Go1.27.1及登记的GCC/binutils；三build、相关包vet和race通过，66个顶层测试、零SKIP；NFO90.5%、CLI76.4%，新source.go为98/107=91.59% |
| 真CLI | Windows UTF-8/UTF-16LE/坏XML三个案例、Linux另含UTF-16BE共四个案例。有效文件exit0，坏XML为exit1及固定nfo_invalid_xml；输出无原文title、根路径或DB配置。原文SHA256不变；Linux同时核对mtime不变 |
| 清理与一致性 | Windows来源成功返回后根目录可重命名，证明没有保留阻止重命名的句柄；保留Source仍可解析。原生临时快照、CLI自建文件都已清理；224个源码/配置文件哈希与测试快照相同 |

按变更范围验证NFO、CLI及架构，不重复无变更的数据库迁移、真实ffprobe和整库扫描验收。前段完整回归与CI修正另见[worker报告](probe-worker-verification.md)；PR #7 在 `893432fc51` 的[修正后CI](https://github.com/MoYuanCN/Jelee/actions/runs/36789351642) Linux、Windows、PG含真实媒体验收全部通过，仅完整品牌门禁仍失败。本段没有新增schema、依赖或全局工具。

## 来源与不可变性断言

- 完整SHA256包括BOM、原编码、空白和未知内容；未解析的空文件/坏XML也可以先取得来源指纹。修改中段并恢复相同size/mtime后，下一次读取仍得到不同hash。
- 同一安全FD读前/读后核对size、mtime、身份与实际长度，再重新打开当前root和文件。真实rewrite、truncate、append、替换、删除返回零结果及固定nfo_changed。
- 根身份以首次OpenRoot后 `Root.Stat(".")` 取得的handle信息为起点。审查发现Windows全域os.Stat的file ID延迟加载，已移除不可靠的前置path Stat比较。
- Linux实际rename根后用同一文件hardlink替换根，验证仅比对叶文件身份仍不够；Windows持有根句柄时OS拒绝rename，单独记录这种保护，没有把未成功的rename称为替换检测。两平台另从不同真实根重开，验证root身份比较。
- 8路并行Parse及调用者修改metadata不相互污染；自定义writer改动收到的buffer也不能改变私有原文。Parse不再复制一份完整输入，WriteOriginal使用最多32 KiB隔离缓冲。
- 取消关闭自有文件并等待回调结束；close失败丢弃结果，固定安全错误。非法参数、越界/控制字符路径、不可表示的mtime、读错误与限额边界均有测试。CLI保留context取消优先，包装错误不泄漏私有路径。

原文输入仍默认8 MiB、最大32 MiB，解码和XML结构沿用既有限制。完整hash描述实际读取的字节；没有文件系统原子快照保证，刻意并发原地写入后恢复可见属性仍可能躲过检查。Source是历史观察，之后磁盘变化不会改变它；未来cache提交仍须重新观察并核对租约。

## 有界微基准

[原始基准输出](evidence/nfo-source-benchmark.txt)：原生Linux、AMD Ryzen 7 9850X3D、Go1.27.1；同一自建16 KiB plot NFO，非race，三个各1秒样本。文件在计时循环外创建。

| 操作 | 每次耗时范围 | 分配字节 | 分配次数 |
| --- | ---: | ---: | ---: |
| ReadSource：完整读取/hash/身份核对 | 36.315–38.131微秒 | 43,004 | 56 |
| ReadFile：上述步骤再解析XML | 143.338–149.343微秒 | 115,761 | 104 |

这是两个新API在同一输入下的成本对照，未对旧版ReadFile做性能改善声明，也不是P95、真实片库吞吐或已接入快取后的暖扫描结果。

## 失败与未完成项

Windows首次来源测试尝试rename仍持有句柄的根，OS拒绝；测试修正为明确验证OS保护，Linux保留实际rename。Windows整体脚本最初把坏XML案例预期exit1误当成整批失败；个别案例与包测试都已通过，最终核对依据改为明确的案例结果，原日志保留。

G13.5/G39相关项继续部分完成：3C3A范围内尚无按库NfoMode、持久解析快取、nfo_invalid数据库状态、worker/API、Catalog来源优先级/字段锁合并、写回或图片处理。完整品牌及项目整体覆盖率门禁仍未达成。

## 推送后远端核对

[PR #8](https://github.com/MoYuanCN/Jelee/pull/8) HEAD `082a51dc2b1206e9687a70c2292d33991e068d30` 的[Go CI](https://github.com/MoYuanCN/Jelee/actions/runs/36790288784) Linux、Windows、PostgreSQL与真实媒体验收全部通过，完整品牌门禁仍失败。[结果摘要](evidence/nfo-source-ci.json)保留各项结论。较早一次run `36790260752` 在runtime工具bootstrap失败，未完成真实媒体验收；该失败不计为通过，也未通过降低检查标准处理。
