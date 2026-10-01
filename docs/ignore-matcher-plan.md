# 3D1A：有界忽略规则解析与匹配

状态：纯组件实现与本地验证完成，尚未接入生产扫描。前一段[NFO工作流程](nfo-worker-verification.md)已发布为[PR #10](https://github.com/MoYuanCN/Jelee/pull/10)，本段分支为 `feat/jelee-ignore-matcher`。下面保留设计与验收要求，实际命令、平台差异和结果另随验证报告保存。

## 分段与范围

G22 分成三个可独立审查的小段：

1. **3D1A**：纯 `.jeleeignore` 文本编译与匹配、固定资源限制、来源定位和真实 Git 对照。
2. **3D1B**：可信媒体根内的安全来源读取、编码/身份核对、编译缓存及失效；旧 `.ignore` 另依固定来源合同实现。
3. **3D1C**：持久 inventory、规则快照和忽略报告接线。规则排除的旧路径不能误记成文件缺失或自动覆写基线。

本段不读取磁盘、不接scanner/DB/runtime/API，不缓存、不实现旧规则，也不启动监看或排程。它交付可独立调用的纯组件；生产扫描启用须等后续接线验收。

## 组件与API

位置 `internal/platform/ignore`，只使用Go标准库纯计算依赖；不修改go.mod/go.sum。采用有工作量预算的token状态推进，避免递归回溯。`Program`不可变，多个Evaluate不共享临时状态。

```go
Compile(ctx context.Context, sources []Source, options Options) (*Program, error)
(*Program).Evaluate(ctx context.Context, path string, kind Kind) (Match, error)
(*Program).Diagnostics() Diagnostics
```

- Source只接受规范的根相对 `.jeleeignore` 路径和有界文本字节，BaseDir由路径派生；不接受另一份可能矛盾的目录参数。输入顺序不决定优先级，重复来源拒绝。
- 默认大小写敏感，跨OS一致；显式ASCII不敏感模式遵循Git的字节匹配，不默默引入Unicode/locale折叠。字符类保持Git的具体语义，不能简单把整组字符统一小写。来源目录按规范组件精确激活，不混合实际不同目录的规则。
- 结果为Unmatched/Include/Exclude三态，以及来源相对路径、物理行号、实际决定路径和ParentBlocked。未匹配不能清除祖先决定。
- 输入/Program的打印不带原文；Diagnostics只含固定错误码、来源与行号，保留最多64项并报告完整总数。返回slice不与Program共享可变数据。
- nil context、无效Program/enum/path返回固定错误；取消/超时返回标准context错误。任何错误都返回nil Program或零Match，不能当作允许扫描。

## 语义

UTF-8严格验证，可带开头BOM；UTF-16LE/BE必须有BOM且代理对合法。UTF-16是本项目扩展，Git对照使用同内容的UTF-8，编码转换另测。LF/CRLF保留物理行号；不猜编码、不用替换字符掩盖非法字节。

支持注释、转义首字符、否定、未转义尾部ASCII空格、锚定斜线、basename匹配、目录专用规则、`*`/`?`/字符类和Git规定位置的`**`。不做正则/brace/extglob扩展，不用path.Clean改变规则文本。末尾反斜线和畸形字符类按真实Git结果定案；不能因某行不匹配而把整个文件解释为忽略全部。

候选是规范根相对UTF-8路径，拒绝绝对路径、空/`.`/`..`组件、重复或末尾斜线、反斜线、冒号和控制字符；文件/目录由显式kind指定。规则内的合法锚定斜线不受候选路径规则误拒。

逐层判断祖先可达性。判断目录自身时只启用它父级以上的规则；只有目录可进入，才激活其内部规则。例如 `build/` 后跟 `!build/keep.txt` 不能越过被排除的build目录；若先以 `!build/` 恢复目录，子规则才可生效。每个文件内最后匹配获胜，更近来源覆盖较远来源的匹配。

非ASCII的`?`/字符类采用Git wildmatch的字节语义，以真实两平台对照确认；不能把Unicode rune匹配误称为Git兼容。字符类的特殊位置、ASCII POSIX集合、未知类及其他连续星号都列入差分样例。

初步真实Git探针已确认几个需要专门实现的边界：完整路径组件中的连续两个以上星号可有globstar行为；反序字符范围不一定令整条规则失效；ASCII不敏感模式下 `[A]` 与 `[A-Z]` 的行为不能用相同的小写预处理模拟。最终支持范围仍以完整差分和黄金测试结果为准。

来源激活采用跨平台纯值合同。原生Linux的Case目录来源不会用于case目录，即使core.ignoreCase开启；Windows/DrvFS可能因文件系统不区分大小写而发现同一控制文件。本组件不做来源发现，不声称精确路径值等同于每种宿主文件系统的查找行为。

## 固定硬限制

| 对象 | 上限 |
| --- | ---: |
| 来源数 | 128 |
| 单源/全部原始文本 | 256 KiB / 4 MiB |
| 单源/全部解码后UTF-8文本 | 384 KiB / 4 MiB |
| 单源/全部物理行 | 4,096 / 16,384 |
| 单行字节 | 4,096 |
| 全部规则记录 | 16,384 |
| 单规则/全部token | 4,096 / 131,072 |
| 字符类bitmap | 4,096 |
| 候选/来源路径字节、组件数 | 1,024 / 128 |
| Compile/Evaluate累计工作量 | 16,777,216 / 8,388,608 |
| 保存的diagnostics | 64，另存Total/Truncated |

所有总量与单项限制同时生效，加法防溢出。解码增长、路径/规则扫描、状态转移和祖先求值均计入预算。开始、结束、每规则及最多每1,024单位检查context；没有逐文件goroutine或全局cache。超过工作量上限返回明确错误，不假装未命中。只承诺组件输入/分配/工作量有界，不把它说成整个进程RSS硬限制。

## 真实验证要求

- 表驱动黄金例逐项验证三态、来源、行号与父目录阻挡，覆盖语法、编码、层级、兄弟目录隔离和平台一致性。
- 每项可到达的硬上限及加1、组合总量、变更调用者输入、修改返回diagnostics、并发Evaluate、取消及预算耗尽；错误必须零结果。
- 生产依赖检查防止引入文件/网络/程序执行。测试代码可以使用隔离Git oracle。
- Windows及原生Linux运行已有Git，记录完整版本、二进制hash及语料hash；不下载或改全局配置。每组短临时repo隔离HOME、system/global配置、templates及excludes；固定argv执行init/check-ignore，候选仅经NUL分隔stdin传入。
- Git stdout/stderr有上限，超时取消后Wait；按verbose NUL四字段解析每个候选的source/line/pattern/path，不能靠整个进程exit code判断单项结果。生成语料先落盘为对应文件/目录，以验证目录kind和剪枝。
- Windows普通测试与真实oracle、Linux race与真实oracle；有界fuzz与Compile/Evaluate分配/耗时基准。记录跳过和未核对边界，不由微基准推算大库扫描吞吐。

本段完成最多推进G22语法/值层的部分约束。符号链接、不完整来源发现、mtime缓存、旧格式兼容、规则变更后重扫和公开忽略报告仍须后续实际验收。
