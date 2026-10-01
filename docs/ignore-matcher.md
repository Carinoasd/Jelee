# 纯忽略规则组件

3D1A 的 `.jeleeignore` 编译器位于 `internal/platform/ignore`，版本为 `jeleeignore-v1`。调用方提供规则文件的相对路径与完整文本，得到不可变的 Program，再逐个判断规范的相对候选路径。固定限制和后续接线范围见[设计](ignore-matcher-plan.md)。

## 调用与结果

```go
program, err := ignore.Compile(ctx, []ignore.Source{
    {Path: ".jeleeignore", Text: []byte("*.tmp\n!keep.tmp\ncache/\n")},
}, ignore.Options{Case: ignore.CaseSensitive})
if err != nil {
    return err
}
decision, err := program.Evaluate(ctx, "cache/movie.mkv", ignore.File)
if err != nil {
    return err
}
```

该候选被第3行的 `cache/` 排除，结果为 Exclude、Source=`.jeleeignore`、Line=3、MatchedPath=`cache`、ParentBlocked=true。目录内的反选规则不能跨过已被排除的父目录。

| Outcome | 含义 |
| --- | --- |
| Unmatched | 当前规则集合没有决定该候选；来源和行号为空 |
| Include | 最终命中一条否定规则；仍须调用方执行权限、媒体类型及其他规则检查 |
| Exclude | 候选或它的祖先目录被最终规则排除 |

错误只返回零 Match 或 nil Program，不提供可以继续使用的部分决定。ErrInvalid、ErrLimit、ErrWorkLimit 分别表示输入无效、尺寸限制、计算预算耗尽；取消与超时保留标准 context 错误。未来扫描器必须处理这些错误，不能把它们当作无规则。

同一个 Program 可并发 Evaluate。每次求值独立分配状态和预算，取消一个调用不改变其他调用或 Program。Compile 返回后修改来源 slice/文本不会改变已编译结果；调用期间不得并发修改调用者提供的输入。Diagnostics 返回副本，最多保留64条并另报总数。

## 文本与路径

- UTF-8 严格验证并接受开头 BOM；UTF-16LE/BE 需要 BOM 与合法代理对。UTF-16 是本项目扩展，Git 对照仅使用等价 UTF-8 内容。
- 保留物理行号，接受 LF/CRLF，拒绝 NUL、非法编码和孤立 CR。只裁掉未转义的尾部 ASCII 空格；行首空格仍有意义。
- 候选和来源必须是规范的根相对路径，使用 `/`。拒绝绝对路径、空组件、`.`、`..`、反斜线、冒号和控制字符。来源 basename 固定为 `.jeleeignore`。
- 文件与目录由 Kind 指定。判断目录自身时，其内部规则尚未激活；只有能进入目录，才使用该目录内的规则判断后代。
- 默认大小写敏感；CaseASCIIInsensitive 仅改变模式匹配的 ASCII 行为。来源目录始终按精确组件激活。实际文件系统上如何发现控制文件，由后续读取层负责。

规则按 Git 风格处理注释、转义、否定、锚定、basename、目录专用模式、通配符与字符类。靠近候选的来源覆盖更高层的命中；同一来源最后命中获胜。UTF-8 中的 `?` 匹配一个字节，字符类也按字节判断，不能将其解释为一个 Unicode 字符。

无效且永不匹配的模式产生固定 `invalid_pattern` 诊断，不输出原文，也不会把整个文件变为忽略全部。Source/Program 的常规打印隐藏文本；本组件不写日志。Match/Diagnostic 中的相对路径仍可能包含私人名称，后续公开 API 需要自行执行授权和输出策略。

## 资源与接线边界

编译和求值有固定字节、行、token、字符类、路径深度与累计工作量上限。匹配使用有界状态推进，没有递归回溯、全局缓存或生产子程序调用。尺寸合法但计算量过大的组合仍可能返回 ErrWorkLimit。微基准只能描述组件的分配和时间，不代表扫描吞吐或整个进程内存上限。

本组件尚未接入生产扫描，也不读取磁盘、发现控制文件、判断符号链接、缓存编译结果、保存规则快照或产生扫描报告。它只判断调用方给出的规则集合，不保证来源完整。后续读取层必须确保可信根、来源身份与完整性；后续持久化必须区分规则排除和媒体缺失。

旧 `.ignore` 有不同的发现、空文件和优先级行为，见[固定来源审计](ignore-source-audit.md)。本组件没有接管该格式。
