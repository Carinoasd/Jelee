# 原资源直投与生产防护

## 当前边界

`internal/adapter/media` 只读取并传输原始文件，不启动子进程，没有转码、重编码、烧录字幕、HLS、DASH 或 Remux 实现。HTTP 层必须先验证凭据，再通过 `ServeSource` 传入不透明资源 ID。是否注册公开播放入口由应用层功能开关控制。

这是直投安全基础模块。按用户/设备的并发与带宽限制及撤销即断流见下文；第三方客户端兼容协商、播放会话统计、ffprobe、字幕/音轨提取、Remux、实际播放器验证与部署性能验收仍需各自实现和验收。本模块测试通过不能代替这些验收。

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

默认值的含义：每个用户最多同时播放 4 个不同资源；带宽开关打开但没有全局速率，所以只有被管理员设置了 `maxKbps` 的用户会被限速。超出范围的值在启动时拒绝。用户覆写的取值范围相同，由数据库 CHECK 约束与应用层双重校验。

## 零拷贝直投（G10.8）

`internal/adapter/media/sendfile.go`。`http.ServeContent` 对完整响应和单段 Range 调用 `io.CopyN(w, 文件, 长度)`，`streamWriter.ReadFrom` 收到包着 `contextFile` 的 `*io.LimitedReader`；在以下条件**全部**满足时，把底层 `*os.File`（同一长度上限）交给原始 ResponseWriter 的 `io.ReaderFrom`，Linux 上 net/http 经 `TCPConn.ReadFrom` 调用 sendfile，媒体字节不进入用户态：

- 平台为 Linux（`sendfile_linux.go` 的 `zeroCopySupported`）；
- `streamWriter.throttle == nil`（未开带宽限制或该用户不受限）；
- 不是错误响应（412/416 等）、不是 TLS 连接（Go 不做内核 TLS，TLS 下 net/http 会退回它自己的未清零共享缓冲）；
- 原始 ResponseWriter 实现 `io.ReaderFrom`，且支持 `SetWriteDeadline`。

其余情况——多段 Range（`ServeContent` 经管道写 multipart，读取端不是文件）、限速、HTTP/2、测试用 `httptest.ResponseRecorder`、非 Linux——维持原有 32 KiB 缓冲复制，行为不变。Windows 与 macOS 编译 `sendfile_other.go`，常量为 false，整个程序照常编译、运行，只走缓冲路径。Content-Length、Range、HEAD、ETag、CSP 等标头仍完全由 `ServeContent` 与 `ServeSource` 决定，零拷贝只替换正文的复制方式。进入零拷贝前先 `Flush` 发出响应头，这样 net/http 不会再为内容嗅探把正文前 512 字节复制进它的共享缓冲。

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

## 播放信息与直投判定（G10.4、G10.5、G15.2、G16.3）

两条自有 API 路由与 `/api/v1/sources/{id}/stream` 同属播放面，仅在同时启用目录与直投时注册：

- `GET /api/v1/items/{id}/playback`：列出条目的全部原始资源。
- `POST /api/v1/items/{id}/playback/check`：请求体为客户端能力声明，逐个资源返回直投判定。

处理顺序固定：

1. `GuardProduction`：查询串与请求体中任何转换参数一律 409 `transcode_disabled`，对 Web 会话同样适用。
2. 会话类型：只有原生会话可用，Web 会话 403 `web_playback_disabled`。
3. 查询参数：两条路由都不接受任何查询参数，出现即 400 `invalid_request`。
4. 查库：仓储在同一条 SQL 中重新确认用户未停用、会话为未撤销未过期的原生会话、库授权与条目存在；不可见与不存在的条目答复相同（默认 404，按 G48.3 配置可为 403）。响应只含文件名推导的版本标签，不含库根目录、目录名或相对路径。

### 资源描述

每个资源给出 `id`、由存储的内容类型得出的容器（`mp4`、`mkv`、`webm`、`mov`、`avi`、`mpegts`）、大小、时长、码率、G20.2 版本标签，以及探测得到的视频轨（不含封面图流；`primary` 标出判定所用的主视频流）、内嵌音轨（编码、语言、声道、采样率、码率、默认/强制、Atmos）、内嵌字幕轨（编码与规范化格式、语言、默认/强制），和外挂字幕/音轨（语言、标题、forced、SDH、default、评论音轨、字符集、大小、由扩展名确定的格式）。多个资源按版本质量分数从高到低排列，同分按 ID。

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

字幕和外挂音轨永不改变资源判定，而是在 `tracks` 中逐条报告：内嵌与外挂音轨不支持时为 `audio_codec_unsupported`，字幕为 `subtitle_format_unsupported`；扩展名不能唯一确定编码的外挂文件（`mka`、`m4a`、`ogg`、`oga` 与可能是 MicroDVD 也可能是 VobSub 的 `.sub`）为 `track_not_probed`。

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

已知影响：上游客户端在 PlaybackInfo 中常带 `MaxStreamingBitrate`、`MaxAudioChannels`，其设备能力 `DirectPlayProfiles` 中也含 `VideoCodec`/`AudioCodec` 字段；通用音频地址常带 `transcodingContainer`/`transcodingProtocol`。这些请求目前一律按 G10.3 拒绝；若兼容层需要接受它们，必须在兼容层把“能力声明”与“转换请求”分开解析，不能放宽本拦截器。

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
