# 原资源直投与生产防护

## 当前边界

`internal/adapter/media` 只读取并传输原始文件，不启动子进程，没有转码、重编码、烧录字幕、HLS、DASH 或 Remux 实现。HTTP 层必须先验证凭据，再通过 `ServeSource` 传入不透明资源 ID。是否注册公开播放入口由应用层功能开关控制。

这是直投安全基础模块。按用户/设备的并发与带宽限制及撤销即断流、外挂字幕/音轨直投及兼容层直投见下文；第三方客户端实际起播验收、播放会话统计、ffprobe、内嵌字幕/音轨提取（mkvextract）、Remux、实际播放器验证与部署性能验收仍需各自实现和验收。本模块测试通过不能代替这些验收。

## 接口和信任边界

1. 认证适配器验证令牌、有效期和撤销状态，把 `access.Principal` 写入请求上下文。`Kind` 必须来自服务端持久化会话；`User-Agent`、`X-Client-Kind` 或其他请求字段不能覆盖它。
2. 用户 ID 和会话 ID 都必须存在。只有 `access.ClientNative` 可进入直投；Web、未知类型以及 Web 管理员全部拒绝。
3. 注入的 `media.Resolver.Resolve(ctx, principal, sourceID)` 在 SQL 查询内完成库/条目授权，返回受信任的库根目录与相对文件名。未找到和不可见资源均返回 `media.ErrNotFound`，避免暴露存在性。不能通过按 ID 查出整条记录后由浏览器隐藏实现权限。
4. 相对文件名采用 `/` 分隔；禁止绝对路径、空路径、`.`、`..`、反斜线、NUL 和 `:`。`filepath.Clean` 与 `filepath.IsLocal` 再做检查。
5. `os.OpenRoot` 加 `Root.OpenFile` 在根目录边界内打开文件，防止符号链接逃逸及检查和打开之间的替换竞态。打开只使用只读标志，只有普通文件可发送。Linux 额外使用 `O_NONBLOCK`，防止命名管道在类型检查前阻塞。
6. 库根目录及其父目录必须由可信管理员管理。根内指向根外的符号链接会被拒绝；根内链接可读取。此机制不阻止管理员创建的硬链接或挂载点，也不是对本机管理员的隔离。媒体卷建议以只读方式挂载。

Web 禁止播放的承诺基于**服务端签发时绑定的会话类型**。原生令牌被复制到浏览器后仍是原生令牌；UA 规则无法提供可靠的浏览器识别。令牌签发和设备授权流程必须保护该边界。

## HTTP 行为

`net/http.ServeContent` 负责完整响应、HEAD、单段/后缀/开放结尾/多段 Range、条件请求和 `If-Range`。传入已打开的文件句柄，不使用公开文件服务目录。所有响应移除 `Content-Disposition`，不会产生附件下载接口。成功响应（含 HEAD 与 206）另加 `Content-Security-Policy: sandbox; default-src 'none'`：即使资源被登记为 `text/html` 或 SVG 并被直接打开，也不能在本源执行脚本（CodeQL 反射型 XSS 的纵深防御，`nosniff` 与明确的 `Content-Type` 之外再加一层）；412、416 等错误响应保留进入直投前的策略。

默认使用文件的 `Last-Modified` 处理条件请求。仓储可提供正确引用的强 ETag，但必须代表当前文件内容版本；不能把未经校验的路径、大小或修改时间冒充强内容指纹。未提供 ETag 时不合成它。

每个 handler 的 `MaxConcurrent` 限制同时解析和发送的请求数。额度耗尽返回 429 和 `Retry-After: 1`。资源查询使用独立 `LookupTimeout`（默认 5 秒），超时返回 504；不会把该查询期限施加到整个媒体传输。取消请求会关闭文件、立即到期网络写截止时间，并释放额度。缓冲路径每次写入刷新 `WriteTimeout`，零拷贝路径在传输有进展时延长截止时间；两者都按写入空闲时间控制长媒体。自定义 ResponseWriter 必须支持 `http.ResponseController.SetWriteDeadline` 或透传 `Unwrap`；`httptest.ResponseRecorder` 没有该能力，仅用于功能测试。文件系统内核调用本身的停顿仍受操作系统和挂载配置约束。

读取包装器检查请求取消，缓冲复制路径用 `io.CopyBuffer` 为每个流显式分配 32 KiB 缓冲。完整响应与单段 Range 在未限速时改走零拷贝路径（见下文“零拷贝直投”）。Range 请求头最多 4096 字节、最多 16 段，超出返回 416，避免攻击者构造大量分段增加解析和 MIME 开销。不会把整部媒体读入内存，超大文件不会因文件大小分配等量缓冲。

`Options.WriteError` 必须注入统一 HTTP 错误映射。包括标准库生成的 412、416 在内，错误通过该回调输出，禁止回显底层绝对路径或数据库错误。媒体已开始发送后的连接/读取错误通过终止流处理，不能在媒体字节后追加 JSON 错误。

## 撤销即断流、并发播放与带宽上限（G07.4、G45.4）

### 撤销即断流

资源查询本身已在 SQL 内确认会话有效，但长时间的下载在开始后不会再经过认证。因此每个 GET 串流在打开文件后启动一个检查协程，每隔 `streaming.revokeCheckSeconds`（默认 5 秒，范围 1–300，不能关闭）调用 `SessionChecker.SessionActive`。正式实现是 PostgreSQL 仓储的 `Store.SessionActive`：会话未撤销、未过期，且用户未禁用、未软删除。它直接查库，所以经任何实例撤销会话（登出、管理员撤销、改密、禁用、删除、撤回原生权限）都会在一个检查间隔内切断所有实例上的串流。

查询失败不立即断流，避免一次数据库抖动中断全部播放；连续 3 次无法确认（约 3 个间隔）才按已撤销处理。查询期限沿用 `LookupTimeout`。

切断的方式与客户端取消相同：结束串流上下文，关闭文件并把网络写截止时间设为现在，阻塞中的读、写和限速等待都会立即返回。此时响应已经开始，不能再追加 JSON 错误；写截止时间保持过期，连接不会被复用，客户端看到的是提前结束的响应（短于 `Content-Length`）。HEAD 不启动检查。

### 并发播放上限

`internal/adapter/media/limits.go` 按用户计算**不同播放**的数量：同一会话对同一资源的多个请求（播放器的重叠 Range 请求与拖动）只算一个，因此不会因 seek 误判超限。超过时在开文件和取共享 I/O 配额之前拒绝：

| 错误码 | 状态 | 含义 |
| --- | --- | --- |
| `user_stream_limit` | 429 | 该用户同时播放的不同资源数已达上限 |
| `device_stream_limit` | 429 | 该设备（native 登录上报的 `deviceId`；未上报时以会话为单位）已达上限 |
| `stream_limit` | 429 | 原有的全进程 `JELEE_MAX_STREAMS` 请求上限，与以上两项相互独立 |

用户上限可由管理员经 `PUT /api/v1/users/{id}/delivery-limits` 的 `maxStreams` 覆写（省略＝跟随全局，`0`＝不限），设备上限只有全局值。HEAD 不发送媒体，不计数也不限速。计数在进程内存中：多实例部署时每个实例各自执行上限，同一用户分散到多个实例时总数可能超过设定值；撤销检查则是跨实例的。

### 带宽上限

令牌桶按用户共享（`bandwidthScope=device` 时按设备）：同一用户的全部串流共用 `maxKbpsPerUser`（千比特每秒，1 kbps＝125 字节/秒），可由管理员以 `maxKbps` 覆写。桶容量为 250 毫秒的流量（至少 16 KiB），每次网络写最多 16 KiB；预约可以透支，等待时间按透支量计算，因此并发串流按到达顺序分享速率。最后一个使用者结束后桶被释放；串流进行中改变覆写值，会在该用户下一次开始串流时生效；共享的桶随之改速，同一用户仍在进行的串流也一起改变。

未开带宽限制或用户不受限时，`streamWriter.throttle` 为 nil。这是零拷贝（sendfile）快速路径唯一可用的情形：撤销检查不依赖写入路径，它通过关闭文件与写截止时间生效。

时间来源是可注入的 `media.Clock`（`Options.Clock`，默认系统时钟），检查间隔与限速等待都经由它，单元测试以假时钟驱动。

### 设置与开关

每项限制单独开关（G45.4）；关闭的限制同时忽略全局值与所有用户覆写。配置文件 `streaming` 段与环境变量：

| 配置文件字段 | 环境变量 | 默认 | 范围 |
| --- | --- | --- | --- |
| `revokeCheckSeconds` | `JELEE_STREAM_REVOKE_CHECK_SECONDS` | 5 | 1–300（配置文件写 0 视为默认） |
| `enableStreamLimit` | `JELEE_STREAM_LIMIT_ENABLED` | true | 布尔 |
| `maxStreamsPerUser` | `JELEE_STREAM_MAX_PER_USER` | 4 | 0–128，0＝无全局上限 |
| `maxStreamsPerDevice` | `JELEE_STREAM_MAX_PER_DEVICE` | 0 | 0–128，0＝不限设备 |
| `enableBandwidthLimit` | `JELEE_BANDWIDTH_LIMIT_ENABLED` | true | 布尔 |
| `maxKbpsPerUser` | `JELEE_BANDWIDTH_MAX_KBPS_PER_USER` | 0 | 0–10000000，0＝无全局速率 |
| `bandwidthScope` | `JELEE_BANDWIDTH_SCOPE` | `user` | `user` 或 `device` |
| `bufferKiB` | `JELEE_STREAM_BUFFER_KIB` | 32 | 16–1024（0＝默认）；缓冲路径每路复制缓冲，见下文“首段预读与缓冲参数” |
| `prefetchKiB` | `JELEE_STREAM_PREFETCH_KIB` | 2048 | 64–65536（0＝默认）；开文件时的预读建议量 |
| `disablePrefetch` | `JELEE_STREAM_PREFETCH_DISABLED` | false | 布尔；关闭预读建议 |

默认值的含义：每个用户最多同时播放 4 个不同资源；带宽开关打开但没有全局速率，所以只有被管理员设置了 `maxKbps` 的用户会被限速。超出范围的值在启动时拒绝。用户覆写的取值范围相同，由数据库 CHECK 约束与应用层双重校验。

## 零拷贝直投（G10.8）

`internal/adapter/media/sendfile.go`。`http.ServeContent` 对完整响应和单段 Range 调用 `io.CopyN(w, 文件, 长度)`，`streamWriter.ReadFrom` 收到包着 `contextFile` 的 `*io.LimitedReader`；在以下条件**全部**满足时，把底层 `*os.File`（同一长度上限）交给原始 ResponseWriter 的 `io.ReaderFrom`，Linux 上 net/http 经 `TCPConn.ReadFrom` 调用 sendfile，媒体字节不进入用户态：

- 平台为 Linux（`sendfile_linux.go` 的 `zeroCopySupported`）；
- `streamWriter.throttle == nil`（未开带宽限制或该用户不受限）；
- 不是错误响应（412/416 等）、不是 TLS 连接（Go 不做内核 TLS，TLS 下 net/http 会退回它自己的未清零共享缓冲）；
- 原始 ResponseWriter 实现 `io.ReaderFrom`，且支持 `SetWriteDeadline`。

其余情况——多段 Range（`ServeContent` 经管道写 multipart，读取端不是文件）、限速、HTTP/2、测试用 `httptest.ResponseRecorder`、非 Linux——走缓冲复制（默认 32 KiB，`streaming.bufferKiB` 可调），行为不变。Windows 与 macOS 编译 `sendfile_other.go`，常量为 false，整个程序照常编译、运行，只走缓冲路径。Content-Length、Range、HEAD、ETag、CSP 等标头仍完全由 `ServeContent` 与 `ServeSource` 决定，零拷贝只替换正文的复制方式。进入零拷贝前先 `Flush` 发出响应头，这样 net/http 不会再为内容嗅探把正文前 512 字节复制进它的共享缓冲。

**取消、撤销与写超时。** 取消与撤销沿用同一机制：串流上下文结束时 `context.AfterFunc` 把写截止时间设为现在并关闭文件；阻塞在 sendfile 中的调用因套接字写截止时间到期立即返回。缓冲路径每写一次刷新一次 `WriteTimeout`，而一次 sendfile 调用可能持续整个下载，固定截止时间会误杀读得慢但仍在读的客户端。因此零拷贝期间有一个看门狗协程，每 `WriteTimeout/4`（至少 10 毫秒）读取文件偏移量（Linux sendfile 每次系统调用后推进偏移量）；偏移量前进就把截止时间延长到“现在＋`WriteTimeout`”，延长后若上下文已结束则立即重新到期，避免延长抵消取消。语义从“每次写入不超过 `WriteTimeout`”变为“连续无进展不超过 `WriteTimeout`（最多再加一个轮询间隔）”，对停住不读的客户端效果相同。进展的粒度受内核限制：套接字发送缓冲排空约一半后内核才唤醒写方，偏移量才前进；loopback 上以约 16 MiB/s 读取时实测两次前进之间可隔 200～300 毫秒。缓冲路径的单次 `write` 同样要等这次唤醒，所以两条路径对慢客户端的容忍度一致；`WriteTimeout` 不应设得接近这个粒度（生产为 30 秒）。非 Linux 平台偏移量要到整个调用结束才更新，看门狗无法观察进度，这是那些平台不开零拷贝的原因。

**测试**（`sendfile_test.go`，loopback TCP，非 Linux 上同样执行并验证走缓冲路径）：

| 测试 | 证明 |
| --- | --- |
| `TestZeroCopyServesFullAndSingleRangeUnchanged` | 完整、单段、后缀、5 字节 Range 的状态码、字节、`Content-Length`、`Content-Range`、ETag、CSP 不变，且确实走零拷贝；HEAD 不进入复制 |
| `TestZeroCopySkipsMultipartAndThrottledStreams` | 多段 Range 与限速串流不走零拷贝，内容正确 |
| `TestZeroCopyLoopbackCancellationRevocationAndTimeout` | 96 MiB 下载在客户端停止读取、sendfile 阻塞时：请求取消后约 0.1 毫秒结束；撤销（检查间隔 200 毫秒）后约 100 毫秒结束；停住不读的客户端在 `WriteTimeout`＋轮询间隔内被断开 |
| `TestZeroCopySlowReaderOutlivesWriteTimeout` | 以至多约 16 MiB/s 持续慢读 3.2 秒（超过两个 1.5 秒的 `WriteTimeout`）不被断开，96 MiB 内容完整 |

反向验证（临时改代码后确认测试失败，再还原）：去掉 `throttle != nil` 条件强制限速串流走零拷贝，`TestLoopbackBandwidthLimit`（3 MiB 本应约 1.32 秒，实际 5 毫秒）与 `TestZeroCopySkipsMultipartAndThrottledStreams` 失败；去掉取消回调里的写截止时间到期，取消与撤销两个子测试 2～3 秒仍未结束而失败；去掉看门狗，慢读测试在约 1.8 秒（一个 `WriteTimeout` 后）被截断；把 `zeroCopySupported` 改为 false，路径计数断言失败。

**基准**（开发机数据：2026-10-04，WSL2 Ubuntu / Linux amd64，AMD Ryzen 7 9850X3D，Go 1.27.1；`go test -p 1 ./internal/adapter/media -run '^$' -bench LoopbackDownload -benchmem -count 6`，取中位数）。基准经 loopback TCP 下载 64 MiB 完整文件与 32 MiB 单段 Range，客户端在同一进程以 `io.Copy(io.Discard, …)` 读取；`cpu-ns/op` 是进程用户态＋内核态 CPU 时间，含客户端，客户端部分两边相同，差值来自服务端。改前数据用同一基准在父提交上测得。

| 场景 | 改前 MB/s | 改后 MB/s | 改前 CPU ms/次 | 改后 CPU ms/次 | 改前 allocs/op | 改后 allocs/op |
| --- | --- | --- | --- | --- | --- | --- |
| 完整 64 MiB | 1640 | 2032 | 62.1 | 45.4 | 120 | 134 |
| 单段 Range 32 MiB | 1717 | 2008 | 30.5 | 23.1 | 131 | 143 |

读法：每次下载的进程 CPU 时间降低约 25%～27%，这是零拷贝的主要收益。吞吐量受同进程客户端读取限制，噪声较大：同一台机器另一轮改前测得 1920～2123 MB/s（完整）与 1827～1938 MB/s（Range），改后 2042～2097 与 1948～2113，提升约 2%～6%；不能据此推断真实网络吞吐。每次请求多出约 12 次分配（看门狗协程、计时器、通道与读取包装），与文件大小无关；字节数/op 都在 10～16 KB，没有随媒体大小增长的分配。看门狗与额外的 `Flush` 是固定开销，对很小的 Range 请求可能抵消收益，这里没有单独测量。

## 本机基线与断连泄漏回归（G10.1、G10.7、G29.5）

**本机基线**（`internal/adapter/media/latency_test.go`，build tag `perf`，不进默认测试也不进任何门禁）。`make bench-direct`（Windows：`scripts/make.ps1 bench-direct`）在真 TCP loopback 上用一个 256 MiB 文件量：

- 完整文件、随机 Seek、向前 +16 MiB、向后 −16 MiB、连续 1 MiB Range 的**首字节**延迟（请求发出到收到第一个正文字节）P50/P95/P99/max，各 100 个样本；
- 每一行分**热**（页缓存）与**冷**（每次请求前对文件 `posix_fadvise(DONTNEED)`；报告里附 `mincore` 量到的页缓存驻留率，确认 100% → 0%；需放在磁盘文件系统，`BENCH_DIRECT_DIR` 默认 `.testdata/direct-bench`，不要指到 tmpfs）；
- 1、4、16 路并发完整下载（每路 32 MiB）的总吞吐与进程 CPU（每路、每 GiB），三轮取中位数；
- 每一项分 **sendfile** 与**缓冲**两条路径。缓冲路径用只暴露 `Unwrap` 的包装 `ResponseWriter` 藏起连接的 `io.ReaderFrom`，走的是和 Windows、TLS 连接相同的 32 KiB 复制循环。

输出写到 `.testdata/direct-latency.txt`（`BENCH_DIRECT_OUT` 可改）。2026-10-30 在开发机（WSL2、16 个逻辑核心、20 GB、ext4、go1.27.1）上的结果存于 [`evidence/direct-play-latency-linux.txt`](evidence/direct-play-latency-linux.txt)：

| 项目（单路，开发机） | sendfile | 缓冲 |
| --- | --- | --- |
| 完整文件首字节 P50 / P95 / P99（热） | 0.36 / 0.67 / 0.98 ms | 0.35 / 0.54 / 0.64 ms |
| 完整文件首字节 P95（冷，已确认页缓存驻留 0%） | 0.59 ms | 0.87 ms |
| 随机 Seek 首字节 P95（热 / 冷） | 0.81 / 0.92 ms | 0.79 / 0.83 ms |
| 向前、向后、连续 Range 首字节 P95（最差一列，热 / 冷） | 0.67 / 0.82 ms | 0.77 / 0.83 ms |
| 16 路并发总吞吐 | 约 30 GB/s | 约 25 GB/s |
| 16 路并发进程 CPU，每 GiB | 约 400 ms | 约 600 ms |

读法与限制：

- **这是开发机基线，不是验收。** 客户端与服务端在同一进程，走 loopback；冷快取只代表“不在客户机页缓存”，WSL2 的虚拟磁盘仍有宿主缓存，所以冷热几乎没有差距。结果只说明伺服器自己的处理（鉴权后查询之外的部分：开文件、`ServeContent`、第一段 sendfile／复制）不引入可观的延迟，所有行 P99 都在 3 毫秒以内（第二次完整运行里个别行的 P95 到 1.8 毫秒，噪声来自共享宿主）。需求中的“首字节 P95 ≤ 500 ms、Seek P95 ≤ 1 s”要在目标硬体、真实磁盘与网络上验证，属于拥有者项目 **C42**；Windows 缓冲路径与断连测试属于 **B24**。
- 本基线不含鉴权、数据库查询（`Resolver` 是内存桩），也不含 TLS、反向代理。真实首字节 = 本基线 + 查询 + 磁盘首读 + 网络往返。
- 并发吞吐在同进程客户端消费能力内测得，数字只在 sendfile／缓冲两条路径之间比较才有意义：多路时 sendfile 的每 GiB CPU 低约三分之一；单路时两者互有高低（用户态与内核态记账在 loopback 上不稳），不据此下结论。
- 缓冲路径在所有行的首字节上与 sendfile 无可测量的差距，因为首字节取决于第一次读和第一次写，不取决于复制方式；区别在持续传输的 CPU。这一点是 B2 决定缓冲预设值的输入：首字节不是调大或调小缓冲的理由，吞吐与 CPU 才是，应在目标硬体上（C42）按 16 KiB 到 1 MiB 实测后再改预设值。

**断连泄漏回归**（`internal/adapter/media/leak_test.go`，默认 `go test` 运行，约 1.5 秒）。`TestDisconnectReleasesEveryResource` 对 sendfile 与缓冲两条路径各启动 6 路（取消场景 2 路）48 MiB 下载，让服务端停在传输中途，再用五种方式断开，每次都断言：

1. 客户端正常关闭连接；2. 客户端 RST（`SetLinger(0)`）；3. 会话被撤销但客户端仍连着不读；4. 客户端停止读取超过 `WriteTimeout`；5. 请求上下文被取消。

断言：处理函数全部返回；**处理函数一返回，原文件的打开描述符（`/proc/self/fd` 逐项 `readlink`）就为 0**，不靠垃圾回收终结器（测试刻意不调用 `runtime.GC`）；进度看门狗（`watchProgress`）、会话监视（`watchSession`）与 `deliver` 协程在 200 毫秒内消失；`slots` 播放名额回到 0；零拷贝路径计数等于预期（sendfile 路径每路一次，缓冲路径为零，证明测的确实是该路径）；随后关闭连接，进程协程数与描述符总数不超过测试前；原文件 SHA-256 前后一致。非 Linux 平台没有 `/proc`，描述符计数跳过，其余照跑。

反向验证（临时改代码，确认测试失败后还原）：去掉处理函数里的 `defer file.Close()` 与取消回调里的关闭，“描述符仍打开”在所有场景失败；去掉 `defer <-h.slots` 的名额释放，“播放名额仍被占用”失败；让看门狗不随上下文结束也不被 `stop()` 等待，“协程活过其串流”失败。

## 容器布局提示与 faststart 建议（G10.6、G10.10）

**只读、只给建议。** Jelee 不改写、不重封装、不搬移原文件中的任何东西（G10.1、G10.10）。下面的布局判定只读文件头，结果只用于在播放信息里给管理员建议，以及给内核预读建议；需要 faststart 的文件由管理员自己用工具在副本上处理（例如 `ffmpeg -i in.mp4 -c copy -movflags +faststart out.mp4`，Matroska 用 `mkvmerge -o out.mkv in.mkv` 补 Cues）。

**布局判定**（`internal/adapter/containerlayout`，纯 Go，无外部工具）。每个文件最多读 64 KiB（`domain.SourceLayoutReadLimit`，所有走访过的盒子／元素头都计入）：

- MP4／MOV／M4V：逐个读顶层盒子的 8 字节头（`size==1` 时再读 64 位长度，`size==0` 视为延伸到文件尾），按长度跳过 `mdat` 等负载，不读媒体数据；最多 4096 个顶层盒子。`moov` 在第一个 `mdat` 之前为 `moov_before_mdat`（即 faststart）；在 `mdat` 之后为 `moov_at_end`；`moov` 之后、`mdat` 之前出现 `moof` 为 `fragmented`。盒子类型不是可打印 ASCII、第一个盒子不是 `ftyp`／`styp`／`moov`／`mdat`／`free`／`skip`／`wide`／`pnot`／`uuid`／`junk`、长度小于盒子头或超出文件、重复的 `moov`、`moov` 之前的 `moof`、找不到 `moov` 都判为 `unknown`。
- Matroska／WebM：确认 EBML 头与 Segment（允许未知长度的 Segment），走访 Segment 的顶层元素直到第一个 Cluster（最多 256 个，未知长度只允许在 Segment／Cluster），解析 SeekHead（最多 16 KiB，最多再跟一层指向另一个 SeekHead 的条目），并在 SeekHead 指出的位置核对确实是 Cues 元素。Cues 在第一个 Cluster 之前为 `cues_before_clusters`，之后为 `cues_after_clusters`（多数封装器的默认输出，客户端多一次 Range 请求即可），读完第一个 Cluster 之前的全部元素仍找不到可达的 Cues 为 `no_cues`；没有 Cluster、截断或格式不符为 `unknown`。
- AVI、MPEG-TS 不判定，播放信息里不出现 `layout`。

判定结果同时记下索引范围（`moov` 盒子或 Cues 元素的起点与长度，超过 1 GiB 只分类不记范围）。读取中途文件变短（大小与扫描记录不符）判为 `unknown`；真正的 I/O 错误与取消返回错误，留给下一次同步重读。表驱动测试覆盖截断、超大长度、64 位长度溢出、二进制盒子类型、盒子数／元素数上限、SeekHead 畸形条目；`FuzzRead` 断言任何输入都不 panic、读取量不超过 64 KiB、记下的范围在文件内；`TestReadRealMuxerOutput`（build tag `jelee_fixture_tools`，需要 ffmpeg，只用来产生样本）以真实封装器输出验证除 `unknown` 外的六种判定（不加 `-movflags`→`moov_at_end`、`+faststart`→`moov_before_mdat`、`frag_keyframe+empty_moov`→`fragmented`、MOV、MKV 默认→`cues_after_clusters`、`-reserve_index_space`→`cues_before_clusters`、`-live 1`→`no_cues`），并比对读取前后的 SHA-256。

**何时读、存在哪里。** 目录同步（`catalog_sync`）在外挂轨检查之后多一步：分页（每页 64 个，每次同步最多 20000 个，其余留给下一次同步）找出布局缺失、或记录时的大小／修改时间与扫描记录不同、或判定器版本（`domain.SourceLayoutVersion`，目前 `layout-v1`）不同的资源，在事务外经库根 `os.OpenRoot` 以只读方式打开（`scan.LayoutInspector`），读前读后都核对文件戳，变了就跳过等下一次扫描；结果在带租约围栏的事务里写入 `media_source_layouts`，写入时再核对一次扫描戳。资源被删除时布局随之级联删除；这是可重建的衍生状态，不导出、降级直接删表。未变的资源重扫不会重读。

**播放信息。** 自有 API 的 `GET /api/v1/items/{id}/playback` 与 `GET /api/v1/items/{id}/sources` 在每个资源（多分片版本的每个分片）上加 `layout` 与 `layoutHints`：`moov_at_end` 给 `faststart_recommended`，`no_cues` 给 `no_seek_index`，其余没有建议时省略。只有记录时的文件戳等于最近一次扫描戳才报告，文件改过而尚未重扫时不报。相容层（`/compat`）不加任何字段（上游 `MediaSourceInfo` 没有对应成员）。网页条目页的版本面板（仅管理员可见）在对应版本下显示建议文字（四语系），只是说明，没有任何会改动文件的按钮。

## 首段预读与缓冲参数（G10.6）

**首段预读。** GET 请求打开文件后（HEAD 不做），对“第一个请求区间的起点”起 `prefetchKiB`（默认 2 MiB）发 `posix_fadvise(WILLNEED)`；请求从文件开头开始（无 Range 或 Range 起点为 0）时，若目录同步记下了索引范围、且打开的文件大小与修改时间仍等于记录时的戳，再对该范围（最多 16 MiB）发同样的建议——`moov_at_end` 的文件因此在客户端回头要 `moov` 之前就开始读文件尾。这只是给内核的预读建议：内核排入读取后即返回、不等待读完（量大时排队本身可能花几毫秒，所以索引范围上限 16 MiB），不改变发送的字节、错误一律忽略，经 `SyscallConn` 调用而不是 `Fd()`，不会把描述符改成阻塞模式，也不和取消时的关闭竞争。Range 格式异常时不发建议，留给 `ServeContent` 正常回 416。非 Linux 平台（`prefetch_other.go`）是空操作。`TestPrefetchAdviceKeepsBytes` 记录每次建议并比对完整、单段、多段、后缀与越界 Range 的响应字节与原文件 SHA-256；`disablePrefetch=true` 关闭全部建议。

**缓冲参数。** 缓冲路径（TLS、限速、多段 Range、非 Linux）每路复制缓冲由 `bufferKiB` 设定，范围 16 KiB–1 MiB，**默认维持 32 KiB**。依据是上一节的本机基线：两条路径首字节在噪声内，缓冲大小不影响首字节，影响的是持续传输的系统调用次数与 CPU；开发机的冷／热快取也看不出预读效果。所以预设值不按开发机数字调整，要在目标硬体（C42）与 Windows（B24）上按 16 KiB 到 1 MiB 实测后再定；改成更大缓冲的代价是每路常驻内存（`maxStreams` 路 × 缓冲大小，缓冲池复用并在归还前清零）。`jelee-cli doctor` 的 `config` 检查以事实列出生效值：`streamBufferKiB`、`streamPrefetchKiB`、`streamPrefetch`（`advise`／`off`／非 Linux 为 `unsupported`）。

## 外挂字幕与音轨直投（G10.9、G15.5、G16.4）

两条路由与 `/api/v1/sources/{id}/stream` 同属播放面，同样只在同时启用目录与直投时注册，GET 与 HEAD 都可用：

- `GET|HEAD /api/v1/sources/{id}/subtitles/{trackId}`
- `GET|HEAD /api/v1/sources/{id}/audio/{trackId}`

`{trackId}` 是 `media_sidecar_tracks` 的行 ID，由播放信息的 `externalTracks[].url` 给出。文件按字节原样发送：不烧录、不重编码、不 Remux，字幕也不转换字符编码（G10.9；位图字幕 `.sup`/`.idx`+`.sub` 同样只传原文件，G15.5）。Matroska 内嵌文字字幕、PGS／VobSub 点阵字幕与字型附件的擷取（E4、G15.5）走另外的路由 `/api/v1/sources/{id}/embedded-subtitles/{index}`（文字与 PGS `.sup`）、`/api/v1/sources/{id}/embedded-subtitles/{index}.idx`／`.sub`（VobSub 两半）、`/api/v1/sources/{id}/attachments/{attachmentId}` 與 `Handler.ServeExtracted`，共用本節的整條直投路徑；点阵字幕只是把原始图片串流搬成独立文件，不 OCR、不烧录、不转格式。DVB 点阵字幕、MP4 `mov_text` 与非 Matroska 容器内的字幕不擷取，只随原文件投递（`extractable=false`）。能力边界表見 [mkvtoolnix 與 MediaInfo](matroska-tools.md)。

**数据来源。** 轨道行由扫描→条目同步写入（W10-8，见 `catalog-sync.md`“外挂字幕／音轨配对”）：同步为每个扫描登记的资源配对同目录及 `Subs/`、`Audio/` 等子目录中的外挂文件，与资源在同一事务中写入；重扫时外挂文件的新增、删除、改名随之反映，视频被标记缺失或删除时轨道一并清除。字符集与边缘指纹由同一任务结束前的有界读取补上，在此之前 `charset` 为空，响应不带 `charset` 参数。

**同一条直投路径。** HTTP 层只做与 `stream` 相同的前置检查（`GuardProduction` → 不接受任何查询参数 → 两个 ID 都必须是 UUID，否则 404），然后调用 `Handler.ServeTrack`。`ServeTrack` 与 `ServeSource` 共用同一个 `serve`：会话类型（只有原生会话，Web 会话无论 bearer 还是 cookie 都是 403 `web_playback_disabled`，且不触达查库）、方法限制、Range 头上限、全进程并发额度、查库超时、并发播放与带宽上限、共享 I/O 配额、`os.OpenRoot` 打开、`ServeContent`（Range、HEAD、条件请求）、撤销即断流、零拷贝与缓冲复制路径、CSP 和错误映射全部沿用，没有另写一套串流。

**授权。** `media.Resolver` 新增 `ResolveTrack(ctx, principal, sourceID, kind, trackID)`。PostgreSQL 实现（`Store.ResolveTrack`）在一条 SQL 中同时确认：用户未停用未删除、会话为未撤销未过期的原生会话、用户是管理员或拥有该资源所在媒体库的授权、轨道的 `source_id` 等于路径中的资源 ID、轨道 `kind` 与路由一致。看不到资源、轨道属于别的资源、字幕 ID 走音轨路由、ID 不存在，答复都相同（默认 404，按 G48.3 配置为 403），不泄露存在性。仓储只返回库根目录、相对路径、字符集、设备 ID 与用户覆写上限，路径不进入响应或日志。

**并发计数。** 轨道计入它所属资源的播放：并发上限的计数键是“会话＋资源 ID”，所以同一会话同时拉视频、外挂音轨和字幕只占一个播放名额；换成另一个资源仍按上限拒绝。带宽桶本来就按用户（或设备）共享，轨道与视频一起受限。

**外挂轨探测与 `.idx`＋`.sub`（B4，schema 109）。** 音轨、`.sub`、`.sup` 由目录同步在检查之后按需探测（见 `catalog-sync.md`），结果只是描述，不改变投递：`externalTracks[]` 新增 `probeState`（`pending`／`ready`／`failed`／`not_applicable`），`ready` 时音轨另有 `profile`、`channels`、`channelLayout`、`sampleRate`、`bitRate`、`durationMicros` 与 `atmos`（沿用内嵌轨规则：E-AC-3／TrueHD 的 profile 标明 Dolby Atmos），字幕有 `durationMicros`。`codec` 以探测结果为准（例如 `.dts` 里其实是 AC-3 就报 `ac3`；`mka`／`m4a`／`ogg`／`oga` 探测后不再是空的），未探测时照旧由扩展名决定，`.sub` 由内容判别给 `microdvd` 或 `vobsub`；探测失败或尚未探测的轨照常列出，只是事实未知，判定仍是 `track_not_probed` 或依扩展名的 codec，播放信息从不等待探测。VobSub 的 `.sub` 数据半边不单独列为轨，同名 `.idx` 带 `pairedTrackId`，客户端用它经同一条字幕路由取 `.sub` 原文件。

探测走媒体探测同一个沙箱 runtime 的新封闭操作 `sidecar`：helper 描述符 `{"version":1,"mode":"sidecar",…}`（Linux）或 Windows 直接启动的固定 argv，与 `metadata` 注册在同一个 `IsolatedRunner`，共用进程并发上限、逾时与输出上限；argv 与 `metadata` 完全相同，只有 `-format_whitelist` 换成 `matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,ogg,mpeg,aac,ac3,eac3,dts,truehd,mlp,flac,mp3,wav,sup,microdvd`（仍只允许 `fd` 协议，没有网络、图片序列、`concat`、`vobsub`（会按名称打开另一个文件）或纯文字字幕 demuxer）。文件仍以 `os.OpenRoot` 只读打开后经 stdin 交给子进程，前后比对大小、mtime 与边缘指纹。新操作不改变探测身份（`ArgumentsSHA256` 只涵盖 `metadata` argv），探测结果存在外挂轨行而不进探测缓存。

**Content-Type。** 由固定对照表按扩展名决定，忽略仓储返回的类型，也不查询系统 MIME 数据库（避免主机配置把字幕映射成文档类型）：

| 扩展名 | Content-Type |
| --- | --- |
| `srt` | `application/x-subrip` |
| `ass`、`ssa` | `text/x-ssa` |
| `vtt`、`webvtt` | `text/vtt` |
| `ttml`、`dfxp` | `application/ttml+xml` |
| `smi`、`sami` | `application/x-sami` |
| `idx` | `text/plain` |
| `sub` | 以扫描的内容判别（`subtitle_kind`）为准：VobSub 数据一律 `application/octet-stream`；MicroDVD 文本为 `text/x-microdvd`（有字符集时带 `charset`）；尚未判别时沿用旧规则（有字符集为 `text/x-microdvd`，否则 `application/octet-stream`） |
| `sup` | `application/x-pgs` |
| `mka` | `audio/x-matroska` |
| `ac3` | `audio/ac3` |
| `eac3`、`ec3` | `audio/eac3` |
| `dts` / `dtshd` | `audio/vnd.dts` / `audio/vnd.dts.hd` |
| `thd`、`truehd`、`mlp` | `audio/vnd.dolby.mlp` |
| `flac` | `audio/flac` |
| `aac` | `audio/aac` |
| `m4a` | `audio/mp4` |
| `opus` | `audio/opus` |
| `ogg`、`oga` | `audio/ogg` |
| `mp3` | `audio/mpeg` |
| `wav` | `audio/wav` |
| 其他（含 `alac`） | `application/octet-stream` |

文本字幕若扫描记录了字符集（`UTF-8`、`UTF-16LE`、`UTF-16BE`、`GB18030`、`Big5`、`Shift_JIS`、`EUC-JP`、`EUC-KR`、`windows-1252` 之一），只作为 `charset` 参数报告，例如 `application/x-subrip; charset=Shift_JIS`；字节不转码。名单外的值、音轨和位图字幕不带 `charset`。所有成功响应都有 `X-Content-Type-Options: nosniff` 与 `Content-Security-Policy: sandbox; default-src 'none'`，没有 `Content-Disposition`。

**ETag。** 规则与资源相同：只有经过校验、代表当前内容的强 ETag 才能发送。`media_sidecar_tracks.fingerprint` 可能是边缘指纹，且扫描后文件可能已变，因此目前不发 ETag，条件请求按 `Last-Modified` 处理；`ServeTrack` 本身支持仓储提供的有效 ETag（单元测试覆盖 `If-Range`、`If-None-Match`）。

**Web 前端门禁。** 这两条是服务端路由，Web 前端不得引用：`web/src/api/client.ts` 的 `WebPaths` 去掉全部 `/api/v1/sources/...` 路径（调用即编译失败）；`web/scripts/check-no-playback.mjs` 的路径段规则加入 `subtitles`、`audio`，源码中出现这类路径字面量即构建失败（生成的 `schema.d.ts` 仍然豁免）。

**测试。** `internal/adapter/media/tracks_test.go`：完整/单段/后缀/开放结尾 Range、HEAD（含 HEAD+Range）、ETag 的 `If-Range` 与 304、日期 `If-Range`、原文件 SHA-256 不变、忽略仓储给的 `text/html`；匿名 401、Web 管理员与未知会话类型 403、转换参数 409、POST 405、非法类型/空 ID/错类型/别的资源/不存在均 404 且前四种不触达查库、文件消失 404；Content-Type 对照表（含 CRLF 注入的字符集被丢弃）；轨道与视频共用名额、撤销后断流并释放名额；Linux 上完整与单段 Range 走 sendfile。`internal/adapter/http/tracks_test.go`：路由层 206/HEAD、字节不变、Web bearer 与 cookie 403、未认证 401、409、多余查询参数 400、非法路径 404、开关关闭时不注册。`internal/adapter/postgres/sidecar_delivery_test.go`（真 PG）：两个用户分别授权不同媒体库，只能取得自己库的轨道；轨道配错资源、错类型、Web 会话（即使是管理员）、混用会话、撤回授权、停用用户、撤销会话、删除资源后都是 `ErrNotFound`。`access_leak_test.go` 的 `leakRouteTable` 登记了四条路由，真 PG 遍历确认隐藏库的轨道在 404/403 两种配置下与不存在的答复一致且不泄露标记。

反向验证（临时改代码后确认测试失败，再还原）：SQL 去掉 `t.source_id=$3` 条件，“别的资源的轨道”断言失败；去掉 `t.kind=$5`，“字幕当音轨”断言失败；去掉媒体库授权条件，真 PG 的 ACL 测试与泄漏遍历失败；`ServeTrack` 不再覆盖 Content-Type，`text/html` 断言失败；轨道改为单独计数，“同一播放被拒 429”失败；门禁去掉 `subtitles|audio`，两条门禁用例失败；关闭原生会话检查，路由层与 media 层的 Web 拒绝测试都失败。

## 外挂文本字幕的 UTF-8 衍生副本（G15.4，第 10 阶段 B5）

外挂文本字幕默认仍按原样投递（上一节），字符集只作为 `charset` 参数报告。对扫描已有把握地记录了**非 UTF-8** 字符集的文本字幕，服务另外提供一个 UTF-8 副本：

- **适用范围。** SubRip、ASS、SSA、WebVTT、SAMI 与 MicroDVD（`.sub` 经内容判别为文本）——这些格式本身不声明编码，换成 UTF-8 后仍是同一份字幕。TTML／DFXP 是可在 XML 声明里写编码的文档，副本会与声明矛盾，不提供；VobSub 的 `.idx`／`.sub`、PGS 与音轨不适用。
- **播放信息。** `GET /api/v1/items/{id}/playback` 的外挂文本字幕新增 `charsetState`：`pending`（尚未有界读取）、`undetermined`（已读取但检测没有把握，只给原文件）、`utf8`、`convertible`（检测到其他字符集）。`convertible` 且字幕缓存可用时另给 `utf8Url`（`/api/v1/sources/{id}/subtitles/{trackId}/utf8`）；默认 `url` 仍是原文件。
- **路由。** `GET`／`HEAD /api/v1/sources/{id}/subtitles/{trackId}/utf8`：只在 `matroska.enableExtraction`（字幕缓存开关）开启时注册，与原文件路由共用生产防护、仅原生会话（Web 403 `web_playback_disabled`）、授权先于任何转换、不接受查询参数、Range／HEAD／限流／撤销断流。没有副本时（字符集未知或已是 UTF-8、格式不适用、转换否定了字符集）答复与不存在的轨道相同。`Content-Type` 为格式固定类型加 `charset=UTF-8`，不发 ETag。
- **转换与回退。** 首次请求时在当前字节上重新检测字符集：必须与记录的一致且不是低信心，否则不转换（`charset_mismatch`、`charset_low_confidence`）；转换（`subtitles.ToUTF8`，去掉开头 BOM、不写 BOM、不动换行）后若无法解码的字节超过输入的 1%，视为误判（`replacement_ratio`）。这几种结果记成拒绝标记，之后的请求不再重复转换；不自动尝试第二候选字符集。每次不提供副本都记一条日志：字段只有轨道 ID 与固定原因码（`charset_unknown`、`already_utf8`、`format_not_convertible` 为 Debug，其余为 Info），不含文件名与路径。
- **缓存。** 副本写在字幕缓存根（`matroska.cacheRoot`，私有 0700）下的 `utf8/<sourceId>/<trackId>-<key>.<ext>`，先写暂存文件、确认原文件在转换期间大小与修改时间未变才改名发布；键包含轨道 ID、库内相对路径、原文件当前大小与修改时间、字符集和转换器版本，原文件一变就是新键，同一轨道的旧副本随即删除。大小上限为 `matroska.cacheMaxBytes` 的 1/16（Matroska 提取缓存相应少 1/16，两者合计不超过配置值），超出按最近最少使用淘汰；单个原文件超过 64 MiB 不转换。`subtitle_cache_cleanup` 任务同时清理两种缓存：已删除资源的整个目录、闲置超过 `maxIdleDays` 的副本与拒绝标记、超过一小时的暂存文件，最后执行大小上限。删掉整个缓存只会让下次请求重新转换。
- **兼容层。** `/compat/Videos/{itemId}/{sourceId}/Subtitles/{index}/Stream.{format}` 对 `convertible` 的外挂字幕直接投递 UTF-8 副本（上游客户端预期 UTF-8），`Codec` 与 `DeliveryUrl` 不变；`undetermined` 或副本被拒绝时回退原文件并记日志；`utf8` 的字幕仍走原文件路由。
- **为何不违反 G10。** 这是对字幕文本的字符集转换，不是媒体转码或重新封装：不经过任何编码器或外部进程（纯 Go 的 `golang.org/x/text` 解码器，`TestDeliveryPackagesCannotRunEncoders` 照常成立），原文件只以只读方式打开、永不改写，原文件路由永远可用且仍是默认；副本只存在于可整体删除重建的缓存。见 [ADR 0007](adr/0007-no-encoder-direct-play-only.md) 的后续段落。
- **测试。** `internal/adapter/subtitles/derived_test.go`（GBK、Big5、Shift_JIS、带 BOM 的 UTF-16LE 逐字节等于预期且原文件 SHA-256 不变、16 个并发首次请求只转换一次、各种拒绝原因与拒绝标记、超过 1% 替换率回退、原文件变化换新副本、转换期间原文件变化不缓存、清理与 LRU）；`internal/adapter/media/derived_test.go`（UTF-8 类型、无副本时原生 404／兼容层回退、Web 403）；`internal/adapter/http/utf8_subtitles_test.go`（路由、开关、查询参数、`utf8Url` 只给 `convertible`）；`internal/adapter/compat/utf8_test.go`；`internal/platform/runtime/subtitles_utf8_test.go`（先授权后转换、日志不含路径、两种缓存合并清理）；`access_leak_test.go` 登记两条新路由（真 PG 遍历）；`postgres/playback_test.go`（真 PG）验证 `charsetState` 随字符集与指纹变化。

## 播放信息与直投判定（G10.4、G10.5、G15.2、G16.3）

两条自有 API 路由与 `/api/v1/sources/{id}/stream` 同属播放面，仅在同时启用目录与直投时注册：

- `GET /api/v1/items/{id}/playback`：列出条目的全部原始资源。
- `POST /api/v1/items/{id}/playback/check`：请求体为客户端能力声明，逐个资源返回直投判定。

处理顺序固定：

1. `GuardProduction`：查询串与请求体中任何转换参数一律 409 `transcode_disabled`，对 Web 会话同样适用。
2. 会话类型：只有原生会话可用，Web 会话 403 `web_playback_disabled`。
3. 查询参数：两条路由都不接受任何查询参数，出现即 400 `invalid_request`。
4. 查库：仓储在同一条 SQL 中重新确认用户未停用、会话为未撤销未过期的原生会话、库授权与条目存在；不可见与不存在的条目答复相同（默认 404，按 G48.3 配置可为 403）。响应只含文件名推导的版本标签，不含库根目录、目录名或相对路径。

网页会话可改用 `GET /api/v1/items/{id}/sources`（见 `docs/catalog-api.md`）读取同样的资源描述：同一条 SQL 只去掉原生会话限定，响应 schema 为 `MediaSourceInfo`，外挂轨没有 `url`，也不带投递声明。

### 资源描述

容器布局与 faststart 建议（`layout`、`layoutHints`）见上文“容器布局提示与 faststart 建议”。

每个资源给出 `id`、由存储的内容类型得出的容器（`mp4`、`mkv`、`webm`、`mov`、`avi`、`mpegts`）、大小、时长、码率、G20.2 版本标签，以及探测得到的视频轨（不含封面图流；`primary` 标出判定所用的主视频流）、内嵌音轨（编码、语言、声道、采样率、码率、默认/强制、Atmos）、内嵌字幕轨（编码与规范化格式、语言、默认/强制），和外挂字幕/音轨（语言、标题、forced、SDH、default、评论音轨、字符集、大小、由扩展名确定的格式，以及原文件的直投地址 `url`，见上一节）。多个资源按版本质量分数从高到低排列，同分按 ID。

内嵌轨标签（B3）：音轨另给 `title`、`commentary`（`comment` 旗标）、`visualImpaired`（`visual_impaired` 或 `descriptions`），字幕轨另给 `sdh`（`hearing_impaired` 或 `captions`）与 `charset`（内嵌文字轨固定 `UTF-8`，点阵与隐藏式字幕不报）；标题先取 ffprobe 读到的串流标题，没有才取 Matroska 补充的轨道名称。这些值来自探测，旧探测缓存需重探才有（parser `media-metadata-v5`），详见 [媒体中继数据](media-metadata.md#內嵌軌標籤b3g152g163)。选轨偏好对内嵌轨同样适用 SDH 与评论音轨规则。

只有当前有效的探测结果才会被使用：探测缓存状态为 ready、未过期，且若该资源由目录同步登记，探测时的大小与修改时间必须与最近一次扫描一致。否则资源仍会列出，但 `probed=false`，流列表为空，版本标签只来自文件名，大小取扫描记录。缓存中无法再通过白名单校验的文档同样按未探测处理。没有码率字段时以 大小×8÷时长 推算。

两个响应都带固定的投递声明 `delivery`：`directPlay=true`，`transcoding`、`hls`、`dash`、`remux` 均为 `false`（G10.4）。

### 能力声明与转换请求分开解析

请求体字段为 `containers`、`videoCodecs`、`audioCodecs`、`subtitleFormats`（每项最多 32 个、每个 1–32 个字符 `[A-Za-z0-9._-]`）与 `maxBitrate`（比特每秒，0 或省略表示不声明上限）。它们只描述客户端能解什么，服务端不据此改变任何字节。字段名刻意避开上游的转换参数：请求体先经 `GuardProduction` 完整检查，其中出现 `videoCodec`、`audioCodec`、`maxStreamingBitrate`、`TranscodingProfiles`、`subtitleMethod=Encode` 等（包括嵌套）仍然 409；之后再用严格 JSON 解码，未知字段、`null`、非法标记和负码率为 400。拦截器本身没有任何放宽。

标记不区分大小写，并接受常见别名：`matroska`→`mkv`、`ts`/`m2ts`→`mpegts`、`h265`/`hvc1`/`x265`→`hevc`、`avc`/`x264`→`h264`、`ac-3`→`ac3`、`ec3`/`e-ac-3`→`eac3`、`dca`→`dts`、`subrip`→`srt`、`vtt`→`webvtt`、`sup`→`pgs`、`idx`→`vobsub`。音频声明 `pcm` 覆盖所有 PCM 采样格式。空列表表示什么都不支持。

### 判定规则

资源可直投当且仅当：容器在声明中；资源已探测；主视频流的编码在声明中（无视频流时不检查）；资源有音轨时，至少一条内嵌音轨的编码在声明中；已知码率不大于 `maxBitrate`（等于上限可通过，未知码率不报告）。

不可直投时 `directPlay=false`、`code` 为 `direct_play_unsupported`，`reasons` 按以下固定顺序列出全部原因：

| 原因 | 含义 |
| --- | --- |
| `container_unsupported` | 容器未声明，或内容类型无法映射到容器 |
| `source_not_probed` | 没有当前有效的探测结果，编码未知，无法确认 |
| `video_codec_unsupported` | 主视频流编码未声明或未知 |
| `audio_codec_unsupported` | 没有任何一条内嵌音轨的编码被声明 |
| `bitrate_exceeds_client` | 已知码率严格大于声明的上限 |

字幕和外挂音轨永不改变资源判定，而是在 `tracks` 中逐条报告：内嵌与外挂音轨不支持时为 `audio_codec_unsupported`，字幕为 `subtitle_format_unsupported`；扩展名不能唯一确定编码的外挂文件（`mka`、`m4a`、`ogg`、`oga` 与可能是 MicroDVD 也可能是 VobSub 的 `.sub`）为 `track_not_probed`。不支持的轨道同时带 `code=direct_play_unsupported`，表示“不支持直投该轨道”，服务端不提供任何转码替代（G16.4）；外挂轨道无论是否支持都带原文件的 `url`，客户端可自行决定是否尝试。

判定结果本身不是错误：即使没有任何资源可直投，响应也是 200，`directPlayable=false`。服务端从不建议也不尝试转码、Remux 或烧录字幕救场。存在不可直投资源时记录一条 `direct play unsupported` 日志，只含请求 ID、资源数、不可直投数和原因代码，不含名称或路径（G10.5 可排查）。

## 生产模式转换请求拦截

`IsForbiddenDeliveryRoute` 可用于路由前检查，覆盖 `hls`、上游已有 `hls1`、`dash`、`transcode`、`transcoding`、分段路由以及 `.m3u8`、`.mpd`、`.m4s`。大小写、百分号编码及多次编码会归一化。直接传输原始 `.ts` 文件仍可接受。

`GuardProduction` **只在播放/播放信息接口上调用**。它检查查询参数以及最多 64 KiB 的 JSON、表单正文；正文通过后会原样恢复。JSON 重复键与嵌套字段同样检查；不能用后一个同名键覆盖隐藏前面的请求。未知正文类型、语法错误、过深嵌套和过大正文都会拒绝。转换参数不会被悄悄忽略后转成直投。不要把参数拦截器挂在全局元数据接口上：搜索中的编码筛选字段不代表请求编码媒体。未知原生接口参数仍应由 HTTP 路由自己的 schema 校验。

### 拒绝清单来源（G10.3）

清单逐项取自仓库内上游 C# 源码中的上游 API 控制器：视频流、音频流、动态 HLS、媒体信息（PlaybackInfo 与打开直播流）、通用音频五个控制器的查询参数；以及它们绑定的流请求 DTO、视频请求 DTO、PlaybackInfo/OpenLiveStream 请求体、编码任务选项基类与转码配置模型的属性。`guard.go` 中的表格保留上游拼写，便于与上游源码逐项比对。参数名比较不区分大小写，并忽略 `-`、`_` 与空格；查询串、表单与 JSON（含嵌套）使用同一规则。

| 类别 | 参数（上游拼写） | 判定 |
| --- | --- | --- |
| 编码选择 | `videoCodec`、`audioCodec`、`subtitleCodec` | 出现即拒绝（含 `copy`：那是 Remux，默认关闭） |
| 码率 | `maxVideoBitrate`、`maxAudioBitrate`、`maxStreamingBitrate`、`videoBitRate`、`audioBitRate` | 出现即拒绝 |
| 画面与视频编码约束 | `width`、`height`、`maxWidth`、`maxHeight`、`framerate`、`maxFramerate`、`profile`、`level`、`videoProfile`、`videoLevel`、`videoRangeType`、`codecTag`、`rotation`、`maxRefFrames`、`maxVideoBitDepth`、`videoBitDepth`、`requireAvc`、`requireNonAnamorphic`、`deInterlace` | 出现即拒绝 |
| 音频编码约束 | `audioSampleRate`、`maxAudioSampleRate`、`audioChannels`、`maxAudioChannels`、`maxAudioBitDepth`、`audioBitDepth`、`transcodingMaxAudioChannels`、`transcodingAudioChannels`、`enableAudioVbrEncoding` | 出现即拒绝 |
| 封装与时间戳 | `copyTimestamps`、`breakOnNonKeyFrames`、`enableMpegtsM2TsMode`、`estimateContentLength`、`cpuCoreLimit`、`params`（以 `;` 分隔的旧式编码参数） | 出现即拒绝 |
| 分片与自适应 | `minSegments`、`actualSegmentLengthTicks`、`enableAdaptiveBitrateStreaming`、`enableSubtitlesInManifest`、`alwaysBurnInSubtitleWhenTranscoding`，以及前缀 `segment*`、`hls*`、`dash*`、`transcode*`、`transcoding*`（如 `segmentLength`、`segmentContainer`、`transcodingProtocol`、`transcodingContainer`、`transcodeReasons`） | 出现即拒绝 |
| 编码限定流选项 | `<编码>-profile`、`-level`、`-rangeType`、`-codecTag`、`-rotation`、`-maxRefFrames`、`-videoBitDepth`、`-audioBitDepth`、`-audioChannels`、`-deInterlace`（上游把小写开头的未知查询键转交编码器） | 出现即拒绝 |
| 直投开关（上游默认开） | `enableDirectPlay`、`enableDirectStream`、`allowVideoStreamCopy`、`allowAudioStreamCopy`、`enableAutoStreamCopy` | `true`、空值或 JSON `null` 可通过；`false` 及其他值拒绝 |
| `static`（上游默认关） | `static` | 仅明确 `true` 可通过；`false`、空值、`null` 拒绝 |
| `enableTranscoding`（上游默认开） | `enableTranscoding` | 仅明确 `false` 可通过 |
| 字幕方式 | `subtitleMethod` | `External`、`Embed`（及枚举值 1、2）可通过；`Encode`、`BurnIn`、`Hls`、`Drop` 及其他值拒绝 |
| 协议/容器 | `protocol`、`streamingProtocol`、`container` | 值为 `hls`、`dash`、`m3u8`、`mpd` 时拒绝 |
| 设备能力 | `TranscodingProfiles` | 非空数组拒绝；空数组可通过 |

明确可通过、只表示直投或定位的参数：`static=true`、`enableDirectPlay=true`、`enableDirectStream=true`、`enableTranscoding=false`、流复制开关为 `true`、`subtitleMethod=External/Embed`、`mediaSourceId`、`startTimeTicks`、`audioStreamIndex`、`subtitleStreamIndex`、`videoStreamIndex`、`liveStreamId`、`playSessionId`、`deviceId`、`userId`、`tag`、`context`、`enableRedirection`、`enableRemoteMedia`、`autoOpenLiveStream`。

已知影响：上游客户端在 PlaybackInfo 中常带 `MaxStreamingBitrate`、`MaxAudioChannels`，其设备能力 `DirectPlayProfiles` 中也含 `VideoCodec`/`AudioCodec` 字段；通用音频地址常带 `transcodingContainer`/`transcodingProtocol`。这些请求在本拦截器上一律按 G10.3 拒绝。兼容层的 PlaybackInfo 改用下一节的 `GuardPlaybackInfo` 把能力声明与转换请求分开解析，本拦截器没有任何放宽。

### 兼容层（`/compat`）的播放信息与直投

兼容层（[兼容矩阵](compat-matrix.md)“播放模組”）不另写串流：

- `GuardPlaybackInfo` 只挂在 `/compat/Items/{itemId}/PlaybackInfo`。它与 `GuardProduction` 共用同一套检查（路径、查询串、64 KiB 表单/JSON、重复键、嵌套、深度），差别只有：上游 PlaybackInfo 文档化的声明成员（`UserId`、`MediaSourceId`、`LiveStreamId`、`AutoOpenLiveStream`、`StartTimeTicks`、`AudioStreamIndex`、`SubtitleStreamIndex`、`MaxStreamingBitrate`、`MaxAudioChannels`、`EnableDirectPlay`、`EnableDirectStream`、`EnableTranscoding`、`AllowVideoStreamCopy`、`AllowAudioStreamCopy`、`AlwaysBurnInSubtitleWhenTranscoding`、`DeviceProfile`）在请求顶层出现时不当转换参数；`DeviceProfile` 子树只做语法、深度与大小检查。其他任何参数，包括这些成员之下嵌套的非 DeviceProfile 值，仍按上表拒绝。声明清单由 `TestPlaybackInfoDeclarationsAreFixed` 锁定。这些成员只决定是否列出原始资源，响应从不含 `TranscodingUrl` 等字段；没有可直投资源时回 `ErrorCode=NoCompatibleStream`，不提供转码替代。
- DeviceProfile 只在守卫之后、作为能力声明解析（`compat/profile.go` → `domain.DeviceProfileReasons`）：`DirectPlayProfiles`、`ContainerProfiles`、`CodecProfiles` 的条件只用来**排除**原始资源，永远不会让转换变成可接受，也不会触发任何转换；`SubtitleProfiles` 的 `Encode`／`Hls` 方法视为“不能原样投递”，不是转换请求；`TranscodingProfiles`、`MaxAudioChannels` 只读不用。全部来源被排除时回应附 `JeleeUnsupportedReasons`（上游 `TranscodeReason` 名称，只报告、不转码），并以 info 日志记录来源 ID 与原因码（`container_unsupported`、`video_codec_unsupported`、`audio_codec_unsupported`、`container_condition_unmet:<属性>`、`codec_condition_unmet:<属性>`、`bitrate_exceeds_client`、`direct_play_disabled`）；字幕无可用方式时记 `subtitle_format_unsupported`。完整规则见[兼容矩阵](compat-matrix.md)。
- `/compat/Videos/{itemId}/stream[.{container}]` 与外挂字幕路由仍走未修改的 `GuardProduction`，先以调用者会话通过目录查出条目的资源（库授权在 SQL 内），再调用同一个 `media.Handler` 的 `ServeSource`/`ServeTrack`。因此本文的 Range、HEAD、CSP、零拷贝、并发与带宽上限、撤销即断流全部适用且共用额度；`serve` 进入前会再执行一次 `GuardProduction`。另外两条兼容层规则：未带 `static=true` 时只接受不改变字节的标识参数（`MediaSourceId`、`DeviceId`、`PlaySessionId`、`Tag`、`Container`、`api_key`/`ApiKey`），其他一律 409；路径扩展名或 `Container` 与原始容器不同即 remux 请求，409。字幕请求格式必须是原文件格式（或同格式别名），时间偏移、结束位置、VTT 时间映射与内嵌字幕都是 409。外挂音轨（第 10 阶段 B8）经 Jelee 扩充路由 `/compat/Videos/{itemId}/{mediaSourceId}/Audio/{index}/Stream.{format}` 以 `ServeTrack` 原样直投：上游只能经转码把外挂音轨合流，这里不合流、不 remux、不转码，客户端自行同步；`format` 必须等于原文件扩展名，起始偏移与内嵌音轨索引都是 409。
- 凭据：兼容层只接受原生会话；`api_key`/`ApiKey` 查询参数可携带令牌，但仍是同一个原生会话查询，Web 会话 401。

## 生产路径不可达编码器（G10.11）

`internal/architecture` 的 `TestDeliveryPackagesCannotRunEncoders` 静态断言 `internal/adapter/media`、`internal/adapter/http`、`internal/adapter/compat` 及其子包不能启动外部进程：

1. 用 `go/parser` 解析仓库 `internal/` 与 `cmd/` 下全部非测试 `.go` 文件（忽略 build tag，平台专属文件一并检查），按目录建立模块内包的导入图。
2. 从上述三个根出发做传递闭包；可达的每个模块内包都不得导入 `os/exec`、`plugin` 或 cgo（`"C"`），也不得引用 `os.StartProcess`、`syscall.Exec/ForkExec/StartProcess/CreateProcess`、`golang.org/x/sys/unix.Exec` 与 `golang.org/x/sys/windows` 的进程创建函数（按导入别名解析选择器表达式）。失败信息列出导入链。
3. 三个根及其子包中任何字符串常量或字面量都不得包含 `ffmpeg`（不区分大小写）。`ffprobe` 不在此列：探测器位于独立包，且不可从这三个根到达。
4. `TestNoExecScannerDetectsViolations` 用一个含违规代码的临时文件确认扫描器本身不会因解析回退而静默通过。

限制：测试文件不在扫描范围（测试二进制不属于生产路径）；标准库与第三方依赖不做源码扫描，新增第三方依赖时需另行审查。G45 的 `dev-transcode` 若将来实现，必须放在这三个根不可达的独立包中。

## 测试和证据

Windows：

```powershell
./scripts/run-go.ps1 test ./internal/access ./internal/adapter/media -count=1 -v
./scripts/run-go.ps1 test ./internal/adapter/media -run '^$' -bench BenchmarkOriginalRange -benchmem
```

Linux（完成本地工具引导后）：

```sh
.bin/go test -race ./internal/access ./internal/adapter/media -count=1 -v
```

测试包含原文件 SHA-256 前后相等、精确 Range 字节和多段 MIME 内容、日期及 ETag 的 If-Range、匿名/Web/未知会话、伪造客户端头、目录穿越/Windows ADS、符号链接与替换竞态、Linux FIFO、取消阻塞写入、并发额度回收、统一错误与生产转换防护。

Windows 无创建符号链接权限时两项链接测试明确跳过；不能把跳过计为通过。Linux 专属用例需在 Linux 实际执行。基准是临时本地文件与内存响应器微基准，不能证明实际网络 P95、100 用户负载或真实播放器 Seek 性能。

2026-09-30 的 Windows/amd64 本地验证使用 Go 1.27.1、AMD Ryzen 7 9850X3D：上述两个包的单元测试通过（符号链接两项按权限明确跳过）；`go vet` 通过；`FuzzProductionGuard -fuzztime 5s` 完成 86,077 次执行并通过。`BenchmarkOriginalRange` 的 16 KiB 响应测得 65,379 ns/op、250.60 MB/s、58,523 B/op、71 allocs/op。每轮包含请求和内存响应器分配，因此总分配高于 32 KiB 的流复制缓冲；该结果没有基线对照，不能推导 CPU 降低比例。

同日 WSL Ubuntu / Linux amd64 使用本地 Go 1.27.1 执行 `-race -count=1`：media、access、i18n 包通过；符号链接逃逸和并发替换测试实际执行通过，含截止时间刷新与取消交错的回归用例。日志见[媒体 Linux race 证据](evidence/media-linux-race.txt)。FIFO 用例在 `/mnt/c` 的 DrvFS 上因不支持命名管道明确跳过；这项必须在原生 Linux 文件系统复验，当前不能计为通过。

实现依据：[Go os.Root 文档](https://pkg.go.dev/os#Root)、[Go ServeContent 文档](https://pkg.go.dev/net/http#ServeContent)、[ResponseController 文档](https://pkg.go.dev/net/http#ResponseController)。
