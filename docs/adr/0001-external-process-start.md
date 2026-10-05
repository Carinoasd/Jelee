# ADR 0001：外部工具以 `os.StartProcess`／Windows 原生 Job 启动，而非 `os/exec`

- 状态：已采纳（等待需求方确认对 G09.2 字面要求的偏离）
- 日期：2026-10-04
- 相关需求：G09.2、G09.4、G09.6
- 相关代码：`internal/platform/process/`（`runner.go`、`child_linux.go`、`child_windows.go`、`isolated.go`、`ignore.go`）、`internal/platform/sandbox/`

## 需求原文

> G09.2 调用安全：仅用 `os/exec` 参数数组调用 ffmpeg/ffprobe/mkvpropedit/mkvmerge/mediainfo；禁止 shell 字符串拼接；禁止用户输入直接成为参数或输出路径。

原文包含三条约束：(1) 用参数数组而非命令字符串；(2) 不经过 shell；(3) 用户输入不能成为参数或输出路径。另外它字面指定了 `os/exec` 这个包。

## 决定

所有外部程序（目前是正式的隔离 ffprobe helper、legacy ignore helper，以及只在 `jelee_fixture_tools` 构建标签下才有的 ffmpeg fixture runner）一律只能经由 `internal/platform/process.Runner` 启动。Runner 内部不用 `os/exec`：

- Linux：`os.StartProcess(path, argv, &os.ProcAttr{Dir, Env, Files: {stdin, stdout, stderr}, Sys: {Setpgid: true}})`，然后用 `waitid(P_PID, WEXITED|WNOWAIT)` 观察退出、`kill(-pgid, SIGKILL)` 终止整个进程组，最后才 `Wait` 回收。
- Windows：`windows.CreateProcess` 搭配 `STARTUPINFOEX`，以 `PROC_THREAD_ATTRIBUTE_JOB_LIST` 在子进程执行第一条指令前就把它放进设置了 `KILL_ON_JOB_CLOSE` 的 Job Object，`PROC_THREAD_ATTRIBUTE_HANDLE_LIST` 只放 stdin/stdout/stderr，加 `CREATE_NO_WINDOW`；终止时用 `TerminateJobObject`，并等到 `ActiveProcesses=0`。

仓库中除测试外没有任何 `os/exec` import；`os.StartProcess`／`CreateProcess` 也只出现在 `child_linux.go`、`child_windows.go`。

## 为什么不用 `os/exec`

1. **Linux 进程组终止的 PID 重用竞态。** `exec.Cmd.Wait` 会一次性回收（reap）子进程。回收以后，leader 的 PID／PGID 可以被重新分配；这时再对 `-pgid` 发 `SIGKILL` 就可能打到无关的新进程组。如果反过来先杀组再 `Wait`，又无法分辨“已正常退出”和“被我们终止”。Runner 改用 `waitid(WNOWAIT)`：只观察退出、不回收，leader 保持 zombie 状态，PGID 在整个终止过程中都不会被重用；等组内都终止以后才回收。`exec.Cmd` 没有提供“观察但不回收”的接口（`Cmd.Cancel`／`WaitDelay` 都建立在 `Wait` 之上）。
2. **Windows 无法原子地放入 Job。** `os/exec` 底层的 `syscall.StartProcess` 不支持 `PROC_THREAD_ATTRIBUTE_JOB_LIST`。只能先启动、再 `AssignProcessToJobObject`，这中间子进程已经在执行，可能先派生孙进程，脱离 Job 的统一终止和资源控制。以 `CREATE_SUSPENDED` 绕过也做不到，因为 `os/exec` 不会交出主线程 handle 让我们 resume。
3. **环境与 handle 必须完全由我们决定。** `os/exec` 在 Windows 上会自动补 `SYSTEMROOT`，在所有平台上还会对 `Env` 去重、改写。Runner 需要逐项固定环境（固定 locale、本次工作目录的 `TMPDIR`／`TMP`／`TEMP`，Windows 只再加 `SystemRoot`），并且只继承三个标准 handle。直接调用底层 API，就不会有隐式行为。
4. **少一层可变语义。** `exec.Command` 会处理 `LookPath`／`PATH` 搜索、Windows 扩展名补全、`Cmd.Err` 延迟报错等。Runner 只接受注册时验证过的绝对路径，这些功能不需要；不用它们，就不会因为 Go 版本升级而改变行为。

## 安全性对等（G09.2 的三条约束仍然成立）

| 约束 | `os/exec` 的保障 | Runner 的保障 |
| --- | --- | --- |
| 参数数组 | `exec.Command(name, args...)` | `Tool.Operations[op]` 是注册时深拷贝的固定 argv；Linux 直接把 argv 数组交给 `execve`（`os/exec` 内部同样调用 `os.StartProcess`）；Windows 用 `windows.ComposeCommandLine`，与 `syscall` 给 `os/exec` 用的转义规则相同 |
| 不经 shell | 不会自动调用 shell | 同样不调用；Windows 正式路径只接受 `.exe`（`executableAllowed`），不会经 `cmd.exe` 解释 `.bat`／`.cmd` |
| 用户输入不成为参数／输出路径 | 需要调用方自己保证 | 由类型强制保证：`Request` 只有工具 ID、操作名和一个借用的只读 `*os.File`。HTTP、任务、数据库内容都无法提供 argv、程序路径、环境变量或输出路径；输出只经由大小受限的 stdout 管道读回；工作目录是 Runner 自己建立的私有 `run-*` |

`os/exec` 能做到的，这里都做到了，另外还多了：进程组／Job 的原子终止、`WNOWAIT` 防止 PID 重用、固定环境、标准 handle 白名单、stdin 必须是只读可 seek 的普通文件（Linux 检查 `F_GETFL`，Windows 检查 `FileAccessInformation`）、并发与输出上限。

## 差异与代价

- **字面偏离。** G09.2 写的是 `os/exec`，这里没用。本 ADR 主张原文的意图是“Go 标准进程 API + 参数数组 + 无 shell + 无用户参数”，而本实现以同一层标准库原语（`os.StartProcess`），加上 `golang.org/x/sys/windows` 的官方绑定，在更严格的条件下满足了这个意图。需求方若坚持字面要求，`requirements-traceability.md` 的 G09.2 行就保持“部分完成”。
- **平台代码自己维护。** Windows 的 `CreateProcess`／Job／属性列表，以及 Linux 的 `waitid` 回收顺序，都要自己维护和测试（见 `docs/process-runner.md`、`docs/process-verification.md`、`docs/process-exit-verification.md`）。Go 标准库的修正不会自动作用到这条路径。
- **平台门槛。** Windows 需要 Windows 10／Server 2016 以上（`JOB_LIST`）；缺少 API 时返回 `process_platform_unsupported`，不会降级成“只杀父进程”。其他非 Linux／Windows 平台没有子进程实现。
- **沙箱 helper 是另一层。** 隔离 ffprobe 的流程是：Runner 先启动本程序的 helper 模式（`--internal-probe-helper`）；helper 在子进程内套上 Landlock、seccomp、rlimit，再用 `execveat` 执行已验证的 ffprobe inode。这一步本来就不能用 `os/exec` 表达（它需要在 exec 之前，在子进程自己内部套用策略），详见 `docs/media-sandbox.md`。

## 后果与守门

- 新的外部工具（mkvpropedit、mkvmerge、mediainfo、ffmpeg）也必须用固定 operations 注册到 Runner，不能在其他套件 import `os/exec` 或另行调用 `os.StartProcess`。代码审查时可以用 `grep -rn '"os/exec"\|StartProcess\|CreateProcess' --include=*.go internal cmd | grep -v _test.go` 确认结果只有 `internal/platform/process/child_*.go`。
- 暂存目录的隔离与清理见 `docs/storage-layout.md`：每次执行有私有 `run-*`，服务级根目录名称带有拥有者 PID 和启动标识，启动时会清扫崩溃残留。

## 参考

- [Go `os/exec`](https://pkg.go.dev/os/exec)、[Go `os.StartProcess`](https://pkg.go.dev/os#StartProcess)
- [Linux `waitid(2)`](https://man7.org/linux/man-pages/man2/waitid.2.html)：`WNOWAIT`
- [Microsoft：UpdateProcThreadAttribute](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute)：`PROC_THREAD_ATTRIBUTE_JOB_LIST`、`PROC_THREAD_ATTRIBUTE_HANDLE_LIST`
- [Microsoft：Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)
