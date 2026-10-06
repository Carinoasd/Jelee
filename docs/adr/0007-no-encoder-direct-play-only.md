# ADR 0007：正式镜像不含 ffmpeg，服务只做原文件直投

- 状态：已采纳
- 日期：2026-10-06（追记；见 `c77863e445`、`0bbd5939bb`、`27f66b1eb3`、`3d6f1a679d`）
- 相关需求：需求总则第 4b 条、G10.1–G10.3、G10.11、G27.3、G37.1、G38.2、G51.7
- 相关代码：`Dockerfile`、`tools/runtime-image/`、`tools/manifest.json`、`internal/adapter/media/`（`direct.go`、`guard*.go`）、`internal/architecture/no_exec_test.go`、`internal/platform/runtime/selfcheck.go`
- 相关文档：[直投](../direct-delivery.md)、[部署](../deployment.md)、[外部进程启动 ADR 0001](0001-external-process-start.md)

## 需求原文

> G37.1 ……镜像含 ffprobe、mkvtoolnix、mediainfo 且版本固定可追踪；生产镜像默认不包含 ffmpeg（转码相关二进制），避免误启用转码路径；开发/测试用 ffmpeg 只存在于 `.tools/`（G51），绝不进镜像。
>
> G10.1 禁止转码……HLS/DASH 切片、分片封装。
>
> G51.7 ……不得因本地存在 ffmpeg 而在生产代码路径启用转码（以断言测试保证）。

## 决定

1. **服务只投递原始字节**：原文件、外挂字幕与音轨按原样经 HTTP Range 投递；不转码、不重新封装（remux 也不实现）、不切 HLS/DASH。转码类参数与路径在任何处理器之前返回 409 `transcode_disabled`；兼容层的 `SupportsTranscoding`、`SupportsDirectStream` 一律为 false，不提供转码地址。
2. **正式镜像只含固定版本的只读工具**：ffprobe（探测）、mkvtoolnix 的 `mkvmerge` 与 `mkvextract`（内嵌字幕与附件提取，不含 `mkvpropedit`）、MediaInfo，以及运行它们所需的库与许可证；基础镜像为 `scratch`，以非 root 用户运行，`JELEE_ENV=production`。ffmpeg 在工具清单中标为 `productionAllowed: false`，只为生成测试夹具存在于 `.tools/`。OCR（Tesseract）是另外的可选镜像层，不在默认镜像中（E16）。
3. **开发者模式也不加入编码器**：`dev-transcode` 开关刻意不接（显示 unavailable，开启返回 409 `devmode_toggle_unavailable`）。
4. **外部工具只经受控的进程启动器**运行，并有沙箱与资源限制（[ADR 0001](0001-external-process-start.md)）。

## 理由

- **需求的铁律**：生产模式不得提供任何转码、重编码或码率自适应能力。最可靠的做法是让二进制和镜像里根本没有这条路径，而不是靠开关关闭。
- **资源与攻击面**：转码是媒体服务器最主要的 CPU 消耗和最大的解析攻击面；只做直投让资源上限、并发预算和沙箱都简单得多。
- **可验证**：镜像内容按清单逐字节核对，代码路径由架构测试静态检查，两者都能在 CI 中自动失败。

## 代价

- 客户端必须能直接解码原始格式；不支持的格式只能返回 `NoCompatibleStream` 或由客户端自行处理。
- 无法提供按带宽自适应的码率；带宽限制只能限流原始字节。

## 守门

- `tools/runtime-image`：构建镜像时按精确清单校验暂存目录，多出任何文件（例如 ffmpeg）或哈希不符都会失败；`TestImageRejectsUnknownMissingBadHashAndOversize`、`TestOCRPinsStayOutOfTheDefaultImage`。
- `internal/architecture/no_exec_test.go`：`TestDeliveryPackagesCannotRunEncoders` 禁止直投、兼容层与 HTTP 包及其依赖导入 `os/exec`、调用建立进程的 API 或在字面量中出现 ffmpeg；`TestNoExecScannerDetectsViolations` 证明扫描器有效。
- `internal/adapter/media/guard_params_test.go`、`guard_test.go`：转码参数拒绝清单覆盖上游控制器的全部参数。
- 启动自检：`transcode_unreachable` 确认拒绝中间件在位；PATH 上存在 ffmpeg 时 `encoder_absent` 告警（`encoder_on_path`）但不阻止启动，因为二进制本身没有调用它的路径。
