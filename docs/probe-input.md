# 媒体探测的只读输入

## 当前交付边界

`internal/adapter/probe` 提供已验证的普通文件描述符与固定 ffprobe 参数。它不启动工具，不注册媒体探测 operation，不写探测缓存，也不导入媒体目录。当前媒体执行能力保持关闭，直到文件与网络沙箱有实际验证；这是尚待实现的能力，不需要用户放宽安全约束。

工具版本诊断和进程超时、输出上限、进程树终止由其他模块处理。工具不存在、版本不符合或缺少必要能力时不能回退到任意 PATH 工具、`file:` 输入、普通管道或 ffmpeg。

## API 与文件生命周期

```go
input, err := probe.Open(ctx, probe.Source{
    RootPath:     authorizedRoot,
    RelativePath: authorizedRelativePath,
})
if err != nil {
    return err
}
defer input.Close()

metadata := input.Metadata() // Size, ModifiedUnixNano；没有路径
stdin := input.Stdin()      // *os.File，供已隔离的内部 runner 使用
```

`Source` 来自已完成 ACL 检查的目录解析器。输入不是命令行参数，不能让 HTTP 调用者提供本地绝对路径。固定错误为 `probe_invalid_input`、`probe_input_unavailable`，或上下文取消/超时；不包含文件名或底层错误。

根必须是有效 UTF-8 的绝对目录；根相对路径使用 `/`，最多 1024 字节和 128 个组件，拒绝空组件、`.` / `..`、绝对路径、反斜线、冒号、控制字符和无效 UTF-8。保留正常空格和 Unicode 名称。

打开流程通过 `os.OpenRoot` 固定根，检查各级路径类型与身份，再以只读方式打开文件，检查实际 handle 的类型、身份、大小和时间，最后验证 seek。仅接受普通文件。Linux 使用 `O_NONBLOCK`，避免竞争中换入 FIFO 后阻塞在只读打开。不会读取整个文件、复制到 RAM、创建暂存副本、重命名或修改源文件。

`Input.Stdin()` 必须作为 `*os.File` 直接交给 runner；不能重开 `Name()`，也不能包进一般 `io.Reader` 后由管道复制。Go 的 `Cmd.Stdin` 对 `*os.File` 直接使用文件描述符；其他 Reader 才使用复制 goroutine。[Go Cmd 文档](https://pkg.go.dev/os/exec#Cmd)

Windows 使用继承的标准输入 HANDLE，同样保留原对象和访问权限。[Microsoft handle inheritance](https://learn.microsoft.com/en-us/windows/win32/procthread/inheritance)。本地 Go 1.27.1 的 `os/exec/exec.go`、`syscall/exec_windows.go` 与 `os/root_windows.go` 已核对：标准句柄通过明确的继承列表传递，Root 使用基于目录 handle 的打开。实际 Go 子进程测试已验证 Linux 和 Windows 的只读继承与 seek；这不等于选定 ffprobe 构建已经验证完成。

同一 Input 只用于一个子进程，父子共享文件位置。上下文取消会关闭父进程 handle，`Close` 可重复和并发调用，且等待已开始的关闭回调。父进程关闭描述符不会收回已经继承给子进程的描述符，因此 runner 仍须取消并等待整个子进程树退出，再结束 Input 生命周期。

## 路径边界的实际含义

静态可见的内部、外部符号链接和根叶节点链接会被拒绝。打开前后的身份检查不能证明极短窗口里从未经过指向同一对象的内部链接；根外逃逸仍由 `os.Root` 的解析边界阻止。根及其祖先由本地管理员管理，根内硬链接、挂载点不是 Root 能排除的对象。[Go Root](https://pkg.go.dev/os#Root)

一旦打开，来源路径被替换不会改变该 handle 指向的对象。其他写入者仍可修改同一个 inode；元数据不是不可变快照。后续缓存层需要另外处理扫描期间内容变化，不能只把本次大小和时间视为永久真实性保证。网络文件系统的打开/stat/seek 可能卡在内核中，上下文检查不提供任意挂载故障下的硬中断保证。普通读取也可能引起文件系统访问时间更新。

## 固定 ffprobe 参数

`FFprobeFDArguments()` 每次返回独立参数数组，没有可插入的文件名、输出路径或用户选项：

- 输入为 `fd:`，只允许 `fd` 协定，未允许 `file`、`pipe`、HTTP、TCP、UDP、concat、crypto、data 等协议。
- demuxer 白名单仅包含 Matroska/WebM、MOV/MP4 及其别名、AVI、MPEG-TS/PS/视频流、FLV 和 Ogg。HLS、DASH、concat、图片序列不在其中；容器格式以内容探测，不依赖扩展名。
- 显式保持 MOV 外部数据引用 `enable_drefs=0`。
- 每次内存分配上限 32 MiB、探测字节 1 MiB、分析时间 2 秒、64 条流、2500 个探测包和单解码线程。这些工具选项不是总内存、总 CPU、读取总字节或进程墙钟期限；runner 与后续 OS 沙箱另设整体上限。
- 只请求 JSON 的容器、流与章节信息；参数没有转码、输出文件或素材修改操作。输出仍是不可信数据，后续解析需字段和大小限制，不直接写入 API 或日志。

官方 `fd` 协议支持普通文件 seek，读取默认使用 FD 0，不接受把描述符号码藏在 URL 里。[FFmpeg fd 文档](https://ffmpeg.org/ffmpeg-protocols.html#fd)。已查阅 FFmpeg `n9.0.2` 的 [`file.c`](https://github.com/FFmpeg/FFmpeg/blob/n9.0.2/libavformat/file.c)：`fd_open` 对普通文件保留 seek，并复制 FD；Windows 对复制后的 FD 设置二进制模式。`pipe:` 不提供等价 seek 能力。

格式/协议白名单定义见 [`options_table.h`](https://github.com/FFmpeg/FFmpeg/blob/n9.0.2/libavformat/options_table.h)；MOV 外部轨道的默认关闭及检查分支见 [`mov.c`](https://github.com/FFmpeg/FFmpeg/blob/n9.0.2/libavformat/mov.c)。这些是所选版本的官方源码依据；供应商实际 binary 的 `-protocols`、帮助输出和真实 FD 样本仍需逐项验证，不以源码阅读代替执行证据。

**参数白名单不是 OS 沙箱。** 媒体格式可能要求额外输入；解析器漏洞还可能绕开库的正常协议流程。私有工作目录、只读输入 FD、较低权限和进程组/Job Object 都不能单独证明禁止读取其他可访问文件或访问网络。因此本模块不会把这些参数注册成可运行的媒体能力。

## 后续 Linux 沙箱方案：可行，但未实现

建议使用独立的 Jelee 内部 helper 进程，不在服务进程中修改线程安全策略，也不安装 C helper：

1. 服务只继承只读媒体 FD、受限 stdout/stderr 和必要的控制通道。helper 的程序与依赖由项目工具清单固定；工具路径、参数和规则不来自媒体内容。
2. helper 在处理媒体前调用 `runtime.LockOSThread`，保持线程锁定直到 `execve` 或退出。设置 `PR_SET_NO_NEW_PRIVS`，创建并应用 Landlock 文件规则，只授予选定 executable/动态加载器/确切依赖的必要执行与读取权限；默认拒绝其他文件内容访问与修改。
3. 应用按架构检查的 seccomp 过滤，再在同一受限线程执行固定工具。过滤须拒绝网络创建/通信及绕过入口，包含 socket 家族、`io_uring`、ptrace、跨进程 FD 获取等；amd64 还要拒绝 x32 调用约定。不能只阻止 `connect`，也不能对过滤器未认识的架构默认放行。
4. 配置缺失、ABI 不足、策略安装失败、工具依赖不匹配均返回能力不可用。在执行前以明确的 FD 白名单配合 `close_range` 等机制处理多余 FD，不让服务密钥文件或网络 socket 继承给工具。普通 Go 打开默认设置 CLOEXEC，但 runner 不能据此保证其他代码通过原始 syscall 打开的 FD 也正确设置了该标志。固定路径工具的身份及执行时替换风险也必须单独验证。

这是设计建议，不是已完成的安全保证。Landlock 官方说明其限制作用于调用线程及后续子进程，并且既有打开 FD 不受新的文件访问限制；这使预开媒体 FD 可用，也要求严格控制所有继承 FD。不同 ABI 覆盖的权限不同，旧 ABI 的网络限制不足以替代完整网络隔离。[Linux Landlock](https://docs.kernel.org/userspace-api/landlock.html)

seccomp 官方要求检查系统调用架构，过滤策略在允许的 fork/exec 后继承，并明确指出单独的系统调用过滤不是完整沙箱。[Linux seccomp BPF](https://docs.kernel.org/userspace-api/seccomp_filter.html)。文件规则、调用过滤、继承句柄、执行身份、进程树和资源限制需要共同验证。

本地 `golang.org/x/sys v0.48.0` 已有 `LandlockRulesetAttr`、`LandlockPathBeneathAttr`、Landlock syscall 号码、`Prctl` 与 seccomp 常量，但没有直接使用的完整沙箱封装。固定 Go 1.27.1 的 `syscall.Exec` 在 Linux 调用 `execve`；线程锁定、失败路径立即退出和限制安装后的 Go runtime 行为仍需专门测试。不能将 Landlock 应用于一个 goroutine 后再让它迁移到另一线程执行工具。

## 后续 Windows 沙箱：媒体执行继续关闭

Windows 的 `PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES` 可以让 `CreateProcess` 建立 AppContainer 进程，`SECURITY_CAPABILITIES` 指定 SID 与 capabilities；空网络 capability 是候选隔离策略的一部分。[UpdateProcThreadAttribute](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute)、[AppContainer isolation](https://learn.microsoft.com/en-us/windows/win32/secauthz/appcontainer-isolation)

但官方 `CreateAppContainerProfile` 会创建用户范围的目录与注册表存储，稍后删除并不等于从未改变项目外状态。[CreateAppContainerProfile](https://learn.microsoft.com/en-us/windows/win32/api/userenv/nf-userenv-createappcontainerprofile)。官方启动示例也使用 profile；目前没有足够官方保证或本地执行证据证明“仅导出临时 SID、不创建 profile”可满足这里所需的完整隔离。[Microsoft 启动说明](https://learn.microsoft.com/en-us/windows/win32/secauthz/implementing-an-appcontainer)

后续若验证不建立全局 profile 的方式，仍需检查继承输入 HANDLE、Job Object、受限 token、工具执行 ACL、网络拒绝和旁路读取；ACL 改动只能作用于项目私有工具副本，并按原值恢复。当前不创建 profile、不修改注册表、不修改文件 ACL，也不把 Windows 媒体执行标为可用。低权限暂存副本若仍通过普通 `file:` 路径打开，不能替代这些隔离要求。

## 已有测试证据

- Windows：包测试通过，覆盖率 85.9%；真实子进程从继承的只读 stdin seek 到 64 MiB 稀疏文件尾部；路径替换、源 SHA256 不变、固定错误、取消与并发关闭均通过。符号链接创建因当前 Windows 权限跳过。
- 原生 Linux tmpfs：固定 Go 1.27.1 静态 race 测试程序，在非 root、无网络、只读根、移除全部 capabilities 的容器内运行，全部 12 项顶层测试通过，覆盖率 88.9%，零跳过。实际 FIFO、mode 000 权限、内部/外部符号链接与 345 次来源/外部链接替换通过；测试数据只在容器项目目录 `/project/.testdata`。
- `go vet ./internal/adapter/probe` 通过。这些测试验证文件输入 primitive；没有声称完成真实 ffprobe、恶意媒体解析、OS 沙箱、规范化或探测缓存验收。

```powershell
./scripts/run-go.ps1 test ./internal/adapter/probe -count=1 -cover -timeout=40s
./scripts/run-go.ps1 vet ./internal/adapter/probe
```

```sh
.bin/go test -race -count=1 ./internal/adapter/probe
```

直接在 WSL DrvFS 执行最后一条命令仍可能有权限/FIFO相关跳过；原生 tmpfs 容器验证另行保留本地测试输出。
