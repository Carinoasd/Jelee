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

## 后续：章节图只提取、不生成（2026-10-29，G40.1）

G40.1 要求“Chapter 图（可提取或生成）”。**生成**（从视频帧截一张图）必须解码视频并编码图片，只有 ffmpeg 能做；本 ADR 第 2 条让正式镜像不含 ffmpeg（`tools/manifest.json` 中 `productionAllowed: false`），第 1 条与 `TestDeliveryPackagesCannotRunEncoders` 不允许服务路径出现编码器，在 Go 内自行解码视频帧同样等于把解码器加进服务。因此章节图生成**延后**，不以开关或例外方式加入。

实现的是**提取**：沿用第 4 条的受控启动器，在既有 ffprobe 沙箱新增密封的 `chapters` 描述子（每个视频序号一个），只读出 MP4/MOV 章节图轨（FFmpeg 标为 `timed_thumbnails`）已存的 JPEG/PNG 封包原始字节与章节时间，与内嵌封面（G40.4）的 `cover` 描述子同样不解码、不转码。描述子的参数清单由 `TestChapterArgumentsCopyOnly` 守住（不含 `-c`、`-vf`、`-f`、`-map`、`-show_frames` 等）。若将来要生成章节图，须先修订本 ADR，例如另设与默认镜像分离、受隔离且只截单帧的可选镜像层（类似 OCR 层），并补齐资源上限与沙箱守门。见[图片资产：章节图](../image-assets.md#章节图g401)。

## 守门

- `tools/runtime-image`：构建镜像时按精确清单校验暂存目录，多出任何文件（例如 ffmpeg）或哈希不符都会失败；`TestImageRejectsUnknownMissingBadHashAndOversize`、`TestOCRPinsStayOutOfTheDefaultImage`。
- `internal/architecture/no_exec_test.go`：`TestDeliveryPackagesCannotRunEncoders` 禁止直投、兼容层与 HTTP 包及其依赖导入 `os/exec`、调用建立进程的 API 或在字面量中出现 ffmpeg；`TestNoExecScannerDetectsViolations` 证明扫描器有效。
- `internal/adapter/media/guard_params_test.go`、`guard_test.go`：转码参数拒绝清单覆盖上游控制器的全部参数。
- 启动自检：`transcode_unreachable` 确认拒绝中间件在位；PATH 上存在 ffmpeg 时 `encoder_absent` 告警（`encoder_on_path`）但不阻止启动，因为二进制本身没有调用它的路径。

## 后续：外挂字幕的 UTF-8 衍生副本（2026-10-31，G15.4）

G15.4 要求外挂字幕的编码检测与转换；G10.1 禁止转码，G10.9 要求字幕原样直投。两者不冲突，决定如下：对扫描已有把握地检测为非 UTF-8 的文本字幕（SubRip、ASS、SSA、WebVTT、SAMI、MicroDVD），按需生成一份 UTF-8 **副本**，另设路由 `/api/v1/sources/{id}/subtitles/{trackId}/utf8`；兼容层的字幕路由对这类字幕投递副本（上游客户端预期 UTF-8）。

为何不违反本 ADR 与 G10：

1. **不是媒体转换**：只是把字幕文本从一种字符集解码再写成 UTF-8，用纯 Go 的 `golang.org/x/text` 解码器完成，不涉及任何编码器、外部进程或容器重新封装；`TestDeliveryPackagesCannotRunEncoders` 不变，转换代码在 `internal/adapter/subtitles`，投递包只拿到缓存里的文件。
2. **不改原文件**：原文件只读打开；原文件路由（自有 API 的默认 `url`）永远可用、逐字节不变。
3. **可重建**：副本只在字幕缓存根下，键含原文件的大小、修改时间、字符集与转换器版本，原文件一变即失效；整个缓存可随时删除，由 `subtitle_cache_cleanup` 与大小上限维护。
4. **怀疑就回退**：字符集未知、重新检测不一致或低信心、替换字符超过 1% 时不提供副本，只给原文件并记录原因码，不猜第二候选。

TTML／DFXP 等可自带编码声明的格式不提供副本，以免副本与声明矛盾。细节见[直投：外挂文本字幕的 UTF-8 衍生副本](../direct-delivery.md#外挂文本字幕的-utf-8-衍生副本g154第-10-阶段-b5)。

## 后续：外挂点阵字幕的 OCR 衍生文字轨（2026-10-31，G15.6 延伸）

G15.6 的 OCR 原本只处理 Matroska 内嵌的 PGS／VobSub。第 10 阶段把外挂 `.sup`（PGS）与成对的 `.idx`＋`.sub`（VobSub）也纳入同一条管线：只读外挂文件、用同一组纯 Go 解码器拆出图片，经同一个沙箱 Tesseract 识别，结果写成**额外的** SRT 轨，另设路由 `/api/v1/sources/{id}/subtitles/{trackId}/ocr`；兼容层在既有 OCR 字幕编号之后为它们编号。

为何不违反本 ADR 与 G10：

1. **不是媒体转换**：只读字幕图片做文字识别，不碰视频或音频流，不重新封装容器；外挂文件根本不经 mkvextract，直接由 `internal/adapter/bitmapsub` 读取。会启动进程的只有既有的 Tesseract 沙箱模式（`internal/platform/ocrruntime`），投递包只拿到缓存里的 SRT，`TestDeliveryPackagesCannotRunEncoders` 不变。
2. **不改原文件、原文件永远可取**：外挂 `.sup`／`.idx`／`.sub` 与媒体文件只读打开；原文件路由 `/api/v1/sources/{id}/subtitles/{trackId}`（兼容层的原字幕编号）逐字节不变、照旧可用。OCR 轨只是另一条轨，标题加注 “(OCR)”。
3. **可重建**：结果在 OCR 自己的缓存根下（按轨 ID 分目录），修订键含源与轨 ID、格式、语言、每个文件的路径、大小与修改时间、识别器身份与解码器版本，任一改变即新修订、旧修订删除；整个缓存可随时删除，下次查询重做。
4. **有界**：每个外挂文件 ≤256 MiB（超过不排队），沿用每分钟图片数、并发 1–4、实例 CPU 预算与每张图的进程上限；新增每个工作 4 小时的识别时限（到时保留已识别部分、标为截断），内嵌与外挂一律适用。

预设关闭（需同时开启 Matroska 抽取与 OCR）；Windows 仍为 `platform_unsupported`。细节见[字幕 OCR](../subtitle-ocr.md#外掛點陣字幕g156-延伸第-10-階段-b10)。
