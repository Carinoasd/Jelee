# 忽略规则来源观察

`internal/adapter/media/ignore`（包名 `ignoresource`）为一个候选读取可达祖先的 `.jeleeignore`，调用 `internal/platform/ignore`。本组件尚未接入扫描器、HTTP、任务或数据库；接线属于3D1C。实现与预算依据[3D1B设计](ignore-source-plan.md)，实际验证另记报告。

```go
resolver := ignoresource.NewResolver()
observation, err := resolver.Evaluate(ctx, trustedRoot, "movies/example.mkv", ignore.File, ignore.Options{})
```

根必须来自可信本地配置或已经授权的catalog记录。不能把HTTP传入的路径直接交给本接口。候选为规范根相对slash路径；不会先清理非法路径再接受。候选本身无需存在，本接口读取的是其祖先控制文件；kind由上层可靠盘点结果提供。

## 旧品牌忽略文件别名（G22.2，决定 E2）

除 `.jeleeignore` 外，同一目录的两种上游旧品牌忽略文件名（`.jellyfinignore`、`.embyignore`）被视为 `.jeleeignore` 的**别名**：语法与语义完全相同（gitignore 语法、否定、锚定、注释、转义、BOM、目录继承与就近优先），不另做旧版语义推测。每个目录只取一份本层来源，优先顺序固定为：

1. `.jeleeignore`
2. `.jellyfinignore`
3. `.embyignore`

同一目录同时存在时只读最前面那份，其余整份忽略（不合并）；不同目录各自按此顺序选一份，祖先与子目录的规则照常继承。只有在已安全打开的父目录里确认较前的文件名**不存在**时才看下一个；较前的名字若是目录、链接、不可读或超限，结果是相应错误，不会退而使用别名。选中的文件身份、大小、mtime 与 SHA256 就是该目录的来源证明，因此之后出现 `.jeleeignore`、删除或修改所选别名，都会像修改规则一样使已保留的证明失效并重新判断。

入口是 `nativeFile.OpenRule`（`internal/adapter/media/ignore/rule_names.go`），Evaluate、EvaluateBaseline、ScanDirectory 与复核都经由它读取，所以 `jeleeignore` 与 `jeleeignore-legacy-v1`（custom+legacy family）两种任务模式都生效；来源标签与报告中的规则目录仍以 `<目录>/.jeleeignore` 表示该目录的 custom 来源，不另存文件名。别名与 legacy family 的 `.ignore` 无关：`.ignore` 仍只由 [旧格式最近来源观察](ignore-legacy-source.md) 读取，别名文件也不会被当成 `.ignore`。两个上游文件名只出现在 `rule_names.go`，该文件已登记在品牌扫描 allowlist（协议兼容文件名，与 compat 目录同性质）。`ignoresource.RuleFileNames()` 回传顺序副本供测试与诊断使用。

## 结果与错误

Observation包括本候选Match、diagnostics副本、`Token() [32]byte` 和 `DirectoryProofs()` 返回的独立目录证明副本。没有原文、可共享文件句柄或可误用于其他候选的部分Program。返回错误时Observation全部清零。日志格式化Observation只显示静态脱敏文字；Match的来源为根相对规则文件与行号。

Token覆盖版本、模式大小写选项、观察到的目录身份和顺序、规则存在状态、文件身份、size/mtime与完整原文SHA256。它用于区分本次规则观察，不是持久规则版本、租约、扫描generation或磁盘原子快照；不能单凭相同token提交库存删除。

| 错误 | 含义 |
| --- | --- |
| `ErrInvalid` | 参数/编码不合法，nil context或未初始化Resolver |
| `ErrUnsafe` | 链接、reparse或不允许的文件类型 |
| `ErrUnavailable` | 本机没有可用的严格原生打开能力 |
| `ErrRead` | 读取、权限、关闭或其他来源操作失败 |
| `ErrChanged` | 两次观察的身份、内容或存在状态不同 |
| `ErrLimit` | 来源大小、路径或纯matcher静态上限 |
| `ErrWorkLimit` | 编译/求值工作预算耗尽 |
| `ErrBusy` | 两个处理名额已占用；不排无界等待队列 |
| 标准context错误 | 调用被取消或共用deadline到期 |

只有安全打开父目录之后，固定leaf `.jeleeignore` 及其两个别名（见上节）都确实不存在，才表示本层没有规则。权限错误、父目录缺失、链接或坏规则不能降级成absence，也不回退旧快取结果。错误文本不含OS路径或原文。

## 读取和缓存

按祖先顺序读规则并判断下一层目录；被排除就停止进入该子树。目录本身的规则不参与该目录是否进入的判断。每次对所有可达且存在的来源完整读/hash，再重开根和整条观察链重复读/hash、确认缺失项仍缺失。所有owned资源成功关闭，最后context检查通过，才发布此次新编译项。

暖缓存也重复来源I/O与两次完整hash，所以同size/mtime的改文、新增规则、删除规则会在下一次调用重新判断。缓存只减少Compile，未提供基于mtime跳过读取的路径。共享与每调用待发布LRU各最多64项、16MiB保守记账；条目超过单项权重时仍可计算结果，但不缓存。细项预算见设计，记账权重不等于进程RSS。

Windows从held handle读取完整身份并拒绝所有reparse；Linux使用openat2拒绝链接。Windows文件系统可能接受不同大小写拼写，但规则匹配的Case选项独立，来源标签仍使用调用方规范相对路径。非Linux/Windows平台安全返回unavailable。

检查不构成多文件原子快照，也不阻止hardlink或bind mount。最后检查后的磁盘变化仍需扫描提交层处理。取消会关闭正在读的owned file并等待callback结束；普通open/stat或受阻网络文件系统调用不能保证立即被中断。

## 逐项来源证明

`DirectoryProofs()` 按根到叶顺序返回本候选真正观察并复核过的目录。根路径为 `.`，非根保存直接父目录身份；每项含目录身份和 `.jeleeignore` 的存在标记、文件身份、大小、纳秒mtime及完整原文SHA256。空规则仍为present，其SHA是空字节SHA；缺少规则的字段全零。没有目录缺失证明，也不会为排除子树制造记录。

返回slice和数组由调用方独占，修改不影响Observation或缓存。证明不含原文和绝对路径，JSON与常规格式化均脱敏。观察最多128层，返回证明使用相同边界。失败、取消、复核变化或关闭失败不会发布证明。该接口没有执行额外I/O，也没有改变cache和Token算法。

这些记录用于后续持久化适配，当前尚未写数据库。它们只证明本次来源观察，不能证明另一次ReadDir的目录句柄相同，也不代表整库规则已冻结。验证见[来源证明输出](ignore-observations-verification.md)。