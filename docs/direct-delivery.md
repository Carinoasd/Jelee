# 原资源直投与生产防护

## 当前边界

`internal/adapter/media` 只读取并传输原始文件，不启动子进程，没有转码、重编码、烧录字幕、HLS、DASH 或 Remux 实现。HTTP 层必须先验证凭据，再通过 `ServeSource` 传入不透明资源 ID。是否注册公开播放入口由应用层功能开关控制。

这是直投安全基础模块。第三方客户端兼容协商、按用户/设备限制、播放会话统计、ffprobe、字幕/音轨提取、Remux、实际播放器验证与部署性能验收仍需各自实现和验收。本模块测试通过不能代替这些验收。

## 接口和信任边界

1. 认证适配器验证令牌、有效期和撤销状态，把 `access.Principal` 写入请求上下文。`Kind` 必须来自服务端持久化会话；`User-Agent`、`X-Client-Kind` 或其他请求字段不能覆盖它。
2. 用户 ID 和会话 ID 都必须存在。只有 `access.ClientNative` 可进入直投；Web、未知类型以及 Web 管理员全部拒绝。
3. 注入的 `media.Resolver.Resolve(ctx, principal, sourceID)` 在 SQL 查询内完成库/条目授权，返回受信任的库根目录与相对文件名。未找到和不可见资源均返回 `media.ErrNotFound`，避免暴露存在性。不能通过按 ID 查出整条记录后由浏览器隐藏实现权限。
4. 相对文件名采用 `/` 分隔；禁止绝对路径、空路径、`.`、`..`、反斜线、NUL 和 `:`。`filepath.Clean` 与 `filepath.IsLocal` 再做检查。
5. `os.OpenRoot` 加 `Root.OpenFile` 在根目录边界内打开文件，防止符号链接逃逸及检查和打开之间的替换竞态。打开只使用只读标志，只有普通文件可发送。Linux 额外使用 `O_NONBLOCK`，防止命名管道在类型检查前阻塞。
6. 库根目录及其父目录必须由可信管理员管理。根内指向根外的符号链接会被拒绝；根内链接可读取。此机制不阻止管理员创建的硬链接或挂载点，也不是对本机管理员的隔离。媒体卷建议以只读方式挂载。

Web 禁止播放的承诺基于**服务端签发时绑定的会话类型**。原生令牌被复制到浏览器后仍是原生令牌；UA 规则无法提供可靠的浏览器识别。令牌签发和设备授权流程必须保护该边界。

## HTTP 行为

`net/http.ServeContent` 负责完整响应、HEAD、单段/后缀/开放结尾/多段 Range、条件请求和 `If-Range`。传入已打开的文件句柄，不使用公开文件服务目录。所有响应移除 `Content-Disposition`，不会产生附件下载接口。

默认使用文件的 `Last-Modified` 处理条件请求。仓储可提供正确引用的强 ETag，但必须代表当前文件内容版本；不能把未经校验的路径、大小或修改时间冒充强内容指纹。未提供 ETag 时不合成它。

每个 handler 的 `MaxConcurrent` 限制同时解析和发送的请求数。额度耗尽返回 429 和 `Retry-After: 1`。资源查询使用独立 `LookupTimeout`（默认 5 秒），超时返回 504；不会把该查询期限施加到整个媒体传输。取消请求会关闭文件、立即到期网络写截止时间，并释放额度。每次写入刷新 `WriteTimeout`，长媒体按写入空闲时间控制。自定义 ResponseWriter 必须支持 `http.ResponseController.SetWriteDeadline` 或透传 `Unwrap`；`httptest.ResponseRecorder` 没有该能力，仅用于功能测试。文件系统内核调用本身的停顿仍受操作系统和挂载配置约束。

读取包装器检查请求取消，`io.CopyBuffer` 为每个流显式分配 32 KiB 缓冲。Range 请求头最多 4096 字节、最多 16 段，超出返回 416，避免攻击者构造大量分段增加解析和 MIME 开销。当前未声明 sendfile 零拷贝路径，也没有上传整部媒体到内存。超大文件不会因文件大小分配等量缓冲。

`Options.WriteError` 必须注入统一 HTTP 错误映射。包括标准库生成的 412、416 在内，错误通过该回调输出，禁止回显底层绝对路径或数据库错误。媒体已开始发送后的连接/读取错误通过终止流处理，不能在媒体字节后追加 JSON 错误。

## 生产模式转换请求拦截

`IsForbiddenDeliveryRoute` 可用于路由前检查，覆盖 `hls`、上游已有 `hls1`、`dash`、`transcode`、`transcoding`、分段路由以及 `.m3u8`、`.mpd`、`.m4s`。大小写、百分号编码及多次编码会归一化。直接传输原始 `.ts` 文件仍可接受。

`GuardProduction` **只在播放/播放信息接口上调用**。它检查查询参数以及最多 64 KiB 的 JSON、表单正文；正文通过后会原样恢复。它拒绝视频/音频编码、码率、缩放/帧率请求、分片参数、非空 `TranscodingProfiles`、HLS/DASH 协议与字幕烧录。JSON 重复键与嵌套字段同样检查；不能用后一个同名键覆盖隐藏前面的请求。未知正文类型、语法错误、过深嵌套和过大正文都会拒绝。

空转码能力数组、明确 `EnableTranscoding=false`、外部字幕方式和 `Static=true` 可通过。转换参数不会被悄悄忽略后转成直投。不要把参数拦截器挂在全局元数据接口上：搜索中的编码筛选字段不代表请求编码媒体。未知原生接口参数仍应由 HTTP 路由自己的 schema 校验。

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
