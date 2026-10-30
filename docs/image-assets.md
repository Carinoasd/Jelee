# 图片资产（G40）

本文是图片功能的总览：支持的图片类型与来源、选图顺序、存储、HTTP 接口、管理接口、图片元数据、处理与资源上限、清理，以及尚未完成的部分。各专题的细节与验收证据见文末“相关文档”。本文内容按 2026-10-29 的代码核对（`internal/domain/item_images.go`、`internal/domain/images.go`、`internal/app/images.go`、`internal/app/images_admin.go`、`internal/adapter/images/`（输出格式、裁切与取色在 `pipeline.go`）、`internal/adapter/http/images.go`、`internal/adapter/http/images_admin.go`、`internal/adapter/compat/images.go`、`internal/platform/config/images.go`、`internal/app/image_refresh_worker.go`、`internal/adapter/images/refresh.go`、`internal/platform/telemetry/images.go`）。

## 类型与槽位（G40.1）

每个条目按“类型＋序号”划分槽位。支持 15 种类型：Primary、Backdrop、Logo、ClearLogo、Banner、ClearArt、Art、Disc、Thumb、Landscape、Chapter、Box、BoxRear、Menu、Profile；`Fanart` 视为 `Backdrop` 的别名。只有 Backdrop 与 Chapter 允许序号大于 0（0–9999），其他类型只有序号 0。数据库中的 CHECK 约束（`item_images`，迁移 `000058_item_images`）与领域代码使用同一份类型表。

## 来源与选图顺序（G40.3、G40.4、G40.10）

`item_images.source_kind` 记录图片来源：

| 来源 | 含义 |
| --- | --- |
| `local` | 媒体目录中的本地图片文件 |
| `nfo` | NFO 中引用的图片 |
| `remote` | 外部图片（NFO 中的网址、TMDB），由[图片刷新任务](image-refresh-jobs.md)抓取后字节保存在持久存放区 |
| `embedded` | 从视频文件中复制的内嵌封面（Primary）或章节图（Chapter） |
| `upload` | 管理员通过[管理接口](#管理接口g4010g4011)上传的图片，字节只保存在持久存放区（迁移 000095，暂定编号） |

同一槽位有多行时，先取**锁定**的行（每个槽位最多一行锁定，由唯一索引保证），否则按 local → nfo → remote → embedded → upload 的顺序；最多尝试 5 个候选（每种来源至多一行），文件不存在或字节尚未入库就换下一个。上传接口总是锁定上传的图片，所以它会胜过其他来源；解锁后它排在最后，作为后备。请求处理过程中**不会**抓取远程图片，也不会现场提取内嵌封面，只读取持久存放区中已有的字节。

槽位中没有可用的行、且请求的是 Primary/0 时，退回媒体文件旁的海报：先找 `<文件基名>-poster.jpg|jpeg|png`，再找 `poster.jpg|jpeg|png`（不区分大小写）。详见[本地图片](local-images.md)。

扫描入库（G40.2、G40.3）已实现：每次 catalog sync 完成后，条目目录中按 G40.2 命名的图片写成 `local` 行，NFO art 中 root 内的相对路径写成 `nfo` 行、`https://` 网址写成 `remote` 行（只记录，不抓取）；季图挂到季条目，`season-all-*` 展开到各季，一个目录有多部电影时不采用 `poster.jpg` 这类共用名称。重扫幂等；文件消失时扫描写入的非锁定、非手动 `local`／`nfo` 行被移除，锁定或手动（`item_images.manual`，迁移 000094）的行从不改动，用户的图片文件从不修改或删除。设计与验证见[图片扫描入库](image-ingest.md)。

远程抓取（G40.9）已实现：`image_refresh` 任务把媒体库中未锁定、非手动、尚无内容的 `remote` 行抓进持久存放区并回填内容字段；锁定与手动行从不被选取或改写，每行失败单独记录原因与重试时间（不影响其他行），可取消、可续跑，重跑幂等。默认是“仅本地图片”模式（`JELEE_ENABLE_IMAGE_FETCH=false`），不发出任何图片请求、刷新任务不能提交。设计与验证见[图片刷新任务](image-refresh-jobs.md)。

内嵌封面提取默认关闭，用 `JELEE_ENABLE_EMBEDDED_COVERS=true` 开启；它只写 Primary 槽位的 `embedded` 行，槽位已锁定或已有其他来源时跳过，每次尝试的结果记录在 `item_embedded_cover_attempts`（迁移 080）。

## 章节图（G40.1）

需求是“Chapter 图（可提取或生成）”。目前实现**提取**，**生成延后**。

**提取。** 默认关闭，用 `JELEE_ENABLE_CHAPTER_IMAGES=true`（设定档 `enableChapterImages`）开启，前置条件与内嵌封面相同（探测、图片、`JELEE_IMAGE_STORE_ROOT`）。每次 catalog sync 在内嵌封面之后多跑一段：对只有单一媒体文件、探测快取为 ready、metadata 记有章节且有 `attachedPic` 的 JPEG/PNG 视频串流、格式属于 QuickTime 家族（mov/mp4/m4a/3gp/3g2/mj2）的条目，用固定哈希的 ffprobe 在既有沙箱（Landlock/seccomp/rlimit、只读 FD、`fd` 协议白名单）里执行新增的密封 `chapters` 描述子（每个视频序号一个，0–15）：`-select_streams v:N -read_intervals %+#65` 读出该串流至多 65 个封包的原始字节（`-show_data`、`-show_data_hash SHA256`、`pts_time`），连同串流的 `timed_thumbnails` 标记与章节起止时间。只有 FFmpeg 标为 `timed_thumbnails` 的串流（MP4/MOV 的 QuickTime 章节图轨，`tref chap` 指向图片轨）才算章节图；不是的串流换下一个（每个文件至多试 4 个）。每个封包依时间放进 `[开始, 结束)` 包含它的章节，章节按开始时间编号即为 Chapter 槽位序号；同一章节取第一张（解复用器排在最前的 attached picture 复本与第一个样本相同），没有时间或落在所有章节外的封包忽略。每张都核对 ffprobe 自算的 SHA-256、JPEG/PNG 魔术字节与标头尺寸，原样存入内容寻址存放区，`item_images` 写 `image_type='Chapter'`、`source_kind='embedded'`。全程不解码、不转码、不调用 ffmpeg。

**Matroska。** Matroska 没有标准的章节缩图：图片附件在 FFmpeg 中只是 `attached_pic`（封面，由内嵌封面处理），章节元素本身不带图。所以 MKV 不做章节图提取；没有可依据的约定时不按附件文件名猜测。

**锁定、手动与优先序。** 记录时在同一交易内重锁条目、重验媒体文件与探测戳记（不符回 `changed`，什么都不写）。逐个槽位：有锁定行、或已有可用的更高优先来源（local、NFO、已抓取的 remote）时跳过（Skipped）；该槽位的 embedded 行本身是手动或锁定时也不改。之后，该条目其余**非锁定、非手动**的 embedded Chapter 行都删除——它们是文件已不再提供、或已被更高优先来源取代的旧图，所以文件改变后不会留下过期的章节图；确定性的拒绝（`absent`、`invalid`、`too_large`）会删除全部这类行。用户的图片文件从不修改。

**失败隔离与记忆。** 结果 `stored`、`absent`、`invalid`、`too_large` 记在 `item_chapter_image_attempts`（迁移 000098，暂定编号），以（根、相对路径、大小、mtime、指纹）记住“这个文件已处理”，文件不变就不再启动子进程。逾时、工具忙碌、文件变动、存放区写入失败都不记住，下次 catalog sync 再试，也不让任务失败；工具不可用时本段立即结束。只有租约与数据库错误会让任务失败。

**资源上限。** 子进程同时 1 个、30 秒逾时、stdout 16 MiB（进程启动器的上限）、stderr 64 KiB，沿用沙箱的 CPU 秒数与内存限制；单张 512 KiB、每个文件合计 3 MiB、至多 64 张；每个文件（含所有串流尝试）60 秒；每次 catalog sync 至多读 256 个文件，每页 16 个，一次只处理一个文件且占用一个 IO 工作许可。单个文件的尖峰内存约为 stdout 上限加上解析后的字节，约 40 MiB 以内。

**生成延后。** 从影片画面截取章节图需要解码视频帧并编码图片，只有 ffmpeg 做得到；[ADR 0007](adr/0007-no-encoder-direct-play-only.md) 规定正式镜像不含 ffmpeg（工具清单 `productionAllowed: false`，G37.1、G51.7），架构测试也禁止服务路径调用编码器。用 Go 自行解码视频同样等于在服务内加入解码器，违反同一决定，所以不做生成。若将来要做，须先修订 ADR 0007（例如另设受隔离、只截单帧的可选镜像层），并补齐资源与沙箱守门。

**未包含。** 探测 metadata 不记录 `timed_thumbnails`（不升探测 parser，避免整个媒体库重新探测），所以候选按“有章节＋有图片串流”挑选，由沙箱读取确认；没有章节图的 MP4 封面文件会被读一次并记成 `absent`。内嵌封面（G40.4）仍按最小序号的 `attached_pic` 选封面；如果 QuickTime 章节图轨的序号比封面小，封面段会取到第一张章节图。

## 存储（G40.5）

| 位置 | 内容 | 可否重建 |
| --- | --- | --- |
| `JELEE_IMAGE_STORE_ROOT/originals/<sha256 前两位>/<sha256>` | 原图，按内容寻址，相同内容只存一份 | 本地、NFO、内嵌来源可以重建；**锁定的远程图片与管理员上传的图片原图只存在这里**，见[备份与还原](backup-restore.md) |
| `JELEE_IMAGE_STORE_ROOT/variants/<世代>/<原图 sha256>/<变体键>` | 缩放后的变体，只有最新世代有效 | 可以重建 |
| `JELEE_IMAGE_STORE_ROOT/tmp/` | 写入暂存，fsync 后原子重命名 | 不需要备份 |
| `JELEE_IMAGE_TEMP_ROOT` | 处理过程中的临时文件 | 不需要备份，启动时清扫 |
| 数据库 `item_images`、`image_variants`、`item_embedded_cover_attempts`、`item_chapter_image_attempts` | 槽位与来源、变体索引（含 LRU 时间）、内嵌封面与章节图尝试记录 | 随 `pg_dump` 备份；元数据导出只包含锁定的图片行 |

未设置 `JELEE_IMAGE_STORE_ROOT` 时不启用持久存放区，只使用内存缓存。存放区目录不能与媒体根目录或 `JELEE_IMAGE_TEMP_ROOT` 重叠；存放区不认识的文件只计数，不读、不移动、不删除。各目录的创建者与清理者见[存储布局](storage-layout.md)。

## HTTP 接口（G40.8）

| 接口 | 说明 |
| --- | --- |
| `GET`／`HEAD /images/{type}/{id}` | 原生接口（挂在根路径，不在 `/api/v1` 下）。查询参数只接受 `width`、`height`（1–2048）、`fillWidth`＋`fillHeight`（1–2048，填满裁切，两者都要且不能再带 `width`／`height`）、`quality`（1–100）、`format`（`jpeg` 或 `webp`，只能是设置开放的格式）、`index`、`tag`，其他参数 400。都不给时按 640×640 等比缩放；不给 `format` 用设置的默认格式 |
| `GET`／`HEAD /compat/Items/{itemId}/Images/{imageType}[/{imageIndex}]` | 兼容层接口，见[兼容矩阵](compat-matrix.md)。`Width`／`MaxWidth`／`FillWidth` 等取最小值，上限 2048；`FillWidth` 与 `FillHeight` 同时给时是填满裁切，只给一个时仍只当上限；`Format=Webp` 输出 WebP、`Jpg` 输出 JPEG，其他上游名称与未开放的格式用默认格式；上游的 Logo、Art、Thumb 映射到对应的 Jelee 槽位 |
| `GET /api/v1/metadata/tmdb/{movies\|series}/{id}/images` | 管理员查看 TMDB 图片候选，按媒体库的图片语言偏好排序，不下载图片，见 [TMDB 图片偏好](tmdb-image-preferences.md) |
| `POST /api/v1/libraries/{id}/images/refresh` | 管理员提交[图片刷新任务](image-refresh-jobs.md)；未开启远程抓取或没有存放区时 404 `image_unavailable` |
| `GET /api/v1/jobs/{id}/image-refresh` | 刷新任务的游标与计数（fetched、failed、deferred、skipped） |
| `GET /api/v1/jobs/{id}/images` | 扫描任务的图片清单统计（新增、变化、未变、缺失），不是图片管理接口 |
| `GET /api/v1/items/{id}/images` | 管理员列出条目的全部图片行，见[管理接口](#管理接口g4010g4011) |
| `POST /api/v1/items/{id}/images/{type}` | 管理员上传或更换一个槽位的图片（`index` 查询参数同 `/images`；槽位已有图片时需 `confirm=true`） |
| `DELETE /api/v1/items/{id}/images/{type}` | 管理员删除一个槽位的图片引用（`source` 可选，`confirm=true` 必填） |
| `POST /api/v1/items/{id}/images/{type}/lock`、`/unlock` | 管理员锁定或解锁槽位中某个来源（`source` 必填，请求体 `{}`） |

缓存与校验：

- `ETag` 是输出字节（JPEG 或 WebP）的 SHA-256（强校验），所以每种格式、尺寸与裁切各有自己的 ETag；带 `If-None-Match` 且匹配时返回 304。`Content-Type` 是 `image/jpeg` 或 `image/webp`。
- `tag` 等于原图 SHA-256 时返回 `Cache-Control: private, max-age=31536000, immutable`，否则 `private, no-cache, must-revalidate`；`Vary` 包含携带凭据的请求头。
- 不支持 Range（`Accept-Ranges: none`）。

权限：图片查询在同一条 SQL 中按用户、会话与媒体库授权过滤（[ADR 0003](adr/0003-unified-access-filter-in-sql.md)）；渲染完成后、写出响应前会再次核对可见性，缓存命中与 304 也不例外。看不到的条目与不存在的条目返回相同结果。原生接口接受任意会话类型；兼容层需要原生会话，并要求登录（[ADR 0004](adr/0004-compat-stricter-than-upstream.md)）。各角色见[权限矩阵](permission-matrix.md) P12。

错误码：`image_busy`（503，带 `Retry-After: 1`）、`image_unavailable`（404）、`image_too_large`（413）、`image_unsupported`（415）、`request_timeout`（408）。完整列表见 [API 参考](api-reference.md#错误码)。

## 管理接口（G40.10、G40.11）

五个接口都只限管理员（权限矩阵 P20）：非管理员在查找条目之前就得到 403 `forbidden`，看得到与看不到的条目结果相同。管理员本身也经过统一权限过滤（[ADR 0003](adr/0003-unified-access-filter-in-sql.md)），被内容规则隐藏的条目与不存在的条目一样返回 404 `not_found`。槽位用路径中的 `type`（`Fanart` 是 `Backdrop` 的别名）与查询参数 `index` 指定，规则与 `/images` 相同；其他查询参数一律 400。响应中的图片行只有 `reference`（`file`、`url` 或 `store`），从不返回媒体库路径或网址。

| 接口 | 行为 |
| --- | --- |
| `GET /api/v1/items/{id}/images` | 每个槽位的全部行，锁定的在前，其余按选图顺序；已读取内容的行带[图片元数据](#图片元数据g407) |
| `POST /api/v1/items/{id}/images/{type}` | 请求体就是图片本身（`Content-Type` 为 `image/*` 或 `application/octet-stream`，必须有 `Content-Length`）。槽位为空返回 201；槽位已有任何来源的行时是“更换”，必须带 `confirm=true`，否则 400 `confirmation_required`，此时不读取也不保存请求体；更换成功返回 200。上传成为该槽位的 `upload` 行并锁定，锁从其他来源移交过来，其他来源的行保留作为后备 |
| `DELETE /api/v1/items/{id}/images/{type}` | 必须带 `confirm=true`，否则 400 `confirmation_required`。带 `source` 只删除该来源的行，不带则删除槽位全部行；没有可删的行返回 404。成功 204 |
| `POST /api/v1/items/{id}/images/{type}/lock`、`/unlock` | `source` 指定哪一行；锁定时移交槽位的锁；扫描与刷新从不改写锁定的行；状态未变时原样返回 |

上传内容不被信任：先按 `JELEE_IMAGE_MAX_SOURCE_BYTES` 检查声明长度（超出 413 `image_too_large`，不读取），再在图片处理槽内读取（槽满 503 `image_busy`）、按魔数识别格式、检查头部尺寸与内存预估（像素炸弹 413），最后完整解码一次，通过后才写入内容寻址的持久存放区。接受 JPEG、PNG、WebP、GIF、BMP、TIFF；AVIF、SVG 等其他内容返回 415 `image_unsupported`。没有配置 `JELEE_IMAGE_STORE_ROOT` 时上传返回 404 `image_unavailable`，删除与锁定仍可用。

手动与扫描行（`item_images.manual`，迁移 000094）：上传与其他手动写入的行 `manual=true`（`upload` 行由约束保证必为手动），扫描入库从不改写或移除手动行；锁定与解锁只改 `locked`，不改变一行是手动还是扫描写入的。删除对两种行一样，只删除引用：

- 删除扫描写入的 `local`／`nfo`／`remote` 行后，只要文件仍在且命名不变（或 NFO 仍引用它），下次扫描会把它重新加回来，成为普通的扫描行（未锁定、非手动）。想让槽位不再显示某个库内文件，应上传或锁定另一个来源，而不是删除。
- 删除手动行（包括上传）后不会自动恢复；同一槽位同一来源若有扫描能识别的文件，下次扫描会以扫描行的身份加入。
- 删除不会影响同一槽位的其他来源；删除上传后，槽位按选图顺序退回扫描行。

删除与更换只动 `item_images` 的引用（G40.11）：媒体目录中的用户原图从不写入、移动或删除，持久存放区中的原图也保留（由存放区自己的淘汰处理）。不再被任何行引用的内容，Jelee 生成的变体会被清除（变体索引行与存放区文件），其他内容的变体不受影响；本地与 NFO 图片的行被删除后，下次扫描会重新发现它们。

审计：上传写 `image.added`（该槽位此前没有上传行）或 `image.replaced`（替换之前的上传），锁的移交写 `image.unlocked`，锁定与解锁写 `image.locked`／`image.unlocked`，删除每行写一条 `image.deleted`。审计状态只含行 ID、槽位、来源、`reference`、锁与内容摘要，不含路径或网址。

上传的行是用户的手动选择：仍有 `upload` 行时迁移 000095 拒绝降级。元数据导出包含锁定的行，所以也包含上传的行，但字节在持久存放区，备份时需要一并保留，见[备份与还原](backup-restore.md)。

## 图片元数据（G40.7）

`item_images` 每行记录来源（`source_kind`）、是否锁定（`locked`）、抓取或读取时间（`fetched_at`），以及读取到内容后才有的内容栏：原图 SHA-256、宽高（按 EXIF 方向的显示尺寸）、格式（`jpeg`、`png`、`webp`、`gif`、`bmp`、`tiff`）、字节数、平均色（`average_color`）与主色调（`dominant_color`，迁移 000097，暂定编号）。内容栏全有或全无：没有内容时颜色也必须为空（数据库约束）。

| 栏位 | 计算方式 |
| --- | --- |
| 平均色 | 解码后的位图上最多 64×64 个等距样本的 RGB 平均，透明部分叠在白底上（与 JPEG 输出一致） |
| 主色调 | 同一批样本放进每色版 16 阶（共 4096 格）的直方图，取样本最多那一格的平均色；同样多时取编号小的格 |

取样只读解码后的位图，不另外复制原图，成本与图片大小无关。颜色在**读到内容的地方**计算并写入：

- 管理员上传：上传本来就完整解码一次，颜色随同写入 `upload` 行。
- 内嵌封面（`JELEE_ENABLE_EMBEDDED_COVERS`）：存入存放区后以 `DescribeStoredImage` 再检查一次并取色；处理槽满或失败只是不记颜色，不影响封面本身。
- 图片刷新（`image_refresh`）抓到远程图片后可调用同一个 `DescribeStoredImage`。
- 扫描入库不读文件（只写路径与文件属性），所以 `local`／`nfo` 行在被读取前没有内容栏。

管理接口的列表以 `averageColor`、`dominantColor`（`#rrggbb`）、`format`、`width`、`height`、`bytes`、`fetchedAt` 返回，未知的栏位省略。元数据导出包含主色调；没有这一栏的旧备份导入后主色调为未知。

## 处理与资源上限（G40.6、G40.12）

- 解码：JPEG、PNG、WebP、GIF（只取第一帧）、BMP、TIFF；AVIF/HEIF 返回 `image_unsupported`。每边最多 65535 像素。
- 输出：JPEG（默认）或无损 WebP（`format=webp`）。WebP 保留透明度，适合 Logo、ClearLogo 等去背图；照片类素材的无损 WebP 比 JPEG 大很多（开发机抽样 1.8–17 倍），超过 `JELEE_IMAGE_MAX_OUTPUT_BYTES` 时 413。WebP 没有质量参数，`quality` 对它无效。AVIF 没有可靠的纯 Go 编码器，`format=avif` 返回 415。选择理由见 [ADR 0010](adr/0010-image-output-formats.md)。
- 缩放：等比缩放（`width`／`height`）只缩小不放大，保持比例；填满裁切（`fillWidth`＋`fillHeight`）输出这个框，取原图中该比例的最大区域，同样不放大（原图不够大时整个框等比缩小），并受 `JELEE_IMAGE_MAX_OUTPUT_DIMENSION` 约束。
- 裁切位置（`JELEE_IMAGE_CROP`）：`smart`（默认）在 96×48 的取样格上比较相邻样本的亮度差，取细节最多的窗口，平坦图片等同置中；`center` 永远置中；`off` 把填满请求改回等比缩放。EXIF 方向在裁切前就生效，裁切、旋转与缩放由同一个仿射变换一次完成，没有额外的整图位图。
- 缓存键：内存缓存与持久变体都按格式区分，填满裁切另含裁切模式；JPEG 等比缩放的变体键与升级前相同，已存的变体继续有效。
- 防止像素炸弹：先读取头部得到尺寸，再按目标尺寸估算内存，超过单图预算返回 413。WebP 输出另外预留编码器的工作内存（每输出像素 80 字节加 4 MiB，实测约 73 字节）；它在编码开始后不能中途取消，只受输出像素上限约束。
- 并发：`JELEE_IMAGE_MAX_CONCURRENT` 个处理槽，不排队，满了直接 503。兼容层与原生接口共用这些槽。
- 内存：处理大图后按条件把内存归还操作系统，原因与修复见[大图内存回收](image-reclaim-pressure.md)；10 万张图片的内存验收见[图片内存](image-memory.md)，预算文件 `tools/image-memory-budget.json`。

| 环境变量 | 默认值 | 范围 |
| --- | --- | --- |
| `JELEE_ENABLE_IMAGES` | — | 开启图片接口 |
| `JELEE_IMAGE_TEMP_ROOT` | 无，开启图片时必填 | 绝对路径的私有目录 |
| `JELEE_IMAGE_STORE_ROOT` | 空（不启用持久存放区） | 绝对路径 |
| `JELEE_IMAGE_MAX_CONCURRENT` | 2 | 1–8 |
| `JELEE_IMAGE_MAX_WORKING_BYTES` | 96 MiB | 16–256 MiB |
| `JELEE_IMAGE_MAX_SOURCE_BYTES` | 16 MiB | 最大 64 MiB |
| `JELEE_IMAGE_MAX_OUTPUT_BYTES` | 2 MiB | 64 KiB–8 MiB |
| `JELEE_IMAGE_MAX_OUTPUT_DIMENSION` | 1024 | 16–2048 |
| `JELEE_IMAGE_CACHE_BYTES`／`_ENTRIES` | 32 MiB／128 | 最大 256 MiB／4096 |
| `JELEE_IMAGE_CACHE_TTL_SECONDS` | 300 | 1–86400 |
| `JELEE_IMAGE_TIMEOUT_SECONDS` | 15 | 1–120 |
| `JELEE_IMAGE_DEFAULT_QUALITY` | 85 | 1–100（只影响 JPEG） |
| `JELEE_IMAGE_OUTPUT_FORMATS` | `jpeg,webp` | `jpeg`、`webp` 的组合；列 `avif` 启动失败 |
| `JELEE_IMAGE_DEFAULT_FORMAT` | `jpeg` | 必须在 `JELEE_IMAGE_OUTPUT_FORMATS` 中 |
| `JELEE_IMAGE_CROP` | `smart` | `smart`、`center`、`off` |
| `JELEE_IMAGE_STORE_ORIGINAL_BYTES` | 4 GiB | 64 MiB–4 TiB |
| `JELEE_IMAGE_STORE_VARIANT_BYTES` | 1 GiB | 16 MiB–1 TiB |
| `JELEE_IMAGE_STORE_ENTRIES` | 131072（每类） | 1024–2097152 |
| `JELEE_ENABLE_EMBEDDED_COVERS` | `false` | 布尔值 |
| `JELEE_ENABLE_CHAPTER_IMAGES` | `false` | 布尔值；需要探测、图片与 `JELEE_IMAGE_STORE_ROOT` |
| `JELEE_ENABLE_IMAGE_FETCH` | `false`（仅本地图片） | 布尔值；需要 jobs、图片与 `JELEE_IMAGE_STORE_ROOT` |

配置校验要求“并发数 × 单图预算 + 缓存”不超过 1 GiB。

### 缓存命中率（G40.13）

`/images` 与兼容层的缩放请求走同一条管线、共用同一个内存变体缓存与持久存放区。每个准入的请求恰好落在下列三者之一（失败的请求另计 `jelee_images_requests_failed_total`）：

1. 内存变体缓存命中：`jelee_images_cache_hits_total`；
2. 内存未命中（`jelee_images_cache_misses_total`），但持久存放区有验证过的变体：`jelee_images_store_served_total`；
3. 两者都没有，从原图解码后生成新变体：`jelee_images_variants_generated_total`（生成后写入内存缓存与存放区）。

用 Prometheus 计算（5 分钟窗口为例）：

```promql
# 内存缓存命中率
rate(jelee_images_cache_hits_total[5m])
  / (rate(jelee_images_cache_hits_total[5m]) + rate(jelee_images_cache_misses_total[5m]))
# 整体命中率（内存或存放区，不必重新生成）
(rate(jelee_images_cache_hits_total[5m]) + rate(jelee_images_store_served_total[5m]))
  / (rate(jelee_images_cache_hits_total[5m]) + rate(jelee_images_cache_misses_total[5m]))
# 生成比例
rate(jelee_images_variants_generated_total[5m])
  / (rate(jelee_images_cache_hits_total[5m]) + rate(jelee_images_cache_misses_total[5m]))
```

没有持久存放区时 `jelee_images_store_*` 不输出，整体命中率就等于内存命中率。304（`If-None-Match` 相符）也会先经过管线取得 ETag，所以同样计入命中。计数是进程启动后的累计值，重启归零；指标定义见[指标](metrics.md#圖片快取與存放區指標g4013g424)。`TestImageVariantCountersShowRepeatedRequestsHit`（重复请求命中、重启后由存放区供应且不再解码）与真 PG 的 `TestImageResizeRequestsHitCachePostgres`（经 `/images` 与兼容层各重复 5 次，只有每种尺寸的第一次生成）是证明。

## 清理与修复（G40.11、G36.3、G50.4）

- 变体按 LRU 淘汰：变体字节数或条目数达到 `JELEE_IMAGE_STORE_VARIANT_BYTES`／`JELEE_IMAGE_STORE_ENTRIES` 的 90% 时，后台循环通过数据库索引删除最久未用的变体，直到降到 80%；期间被访问过的变体会保留。
- 通过[管理接口](#管理接口g4010g4011)删除或更换图片后，不再被引用的内容的变体立即清除；原图不删除。
- `jelee-cli repair image-variants`（或管理员接口 `POST /api/v1/admin/repairs`）以新世代整体作废现有变体，之后按需重新生成；`orphans` 修复动作清理没有对应文件的变体索引行。见[修复](repair.md)。
- 一致性检查会比对 `item_images` 的引用与存放区清单，见[一致性检查](consistency.md)。

## 尚未完成

- 图片管理只有 HTTP 接口，没有 CLI 命令，前端条目页也还没有操作入口。删除扫描写入的行只删除引用，文件仍在时下次扫描会重新加入；没有“不再采用此文件”的记录。
- 手动或锁定、但还没有内容的 `remote` 行不会被刷新任务抓取，请求时不可用；图片刷新没有自动排程，也没有 CLI 命令。
- 不支持 AVIF 输出与解码；WebP 只有无损输出（没有可靠的纯 Go 有损编码器，见 [ADR 0010](adr/0010-image-output-formats.md)），也不按 `Accept` 自动协商。
- 焦点只有自动的智能裁切，不能为单张图片手动指定焦点坐标。
- 扫描入库不读文件，`local`／`nfo` 行要等有程序读取内容（例如图片刷新）后才有宽高、格式与颜色。
- WebP 输出与裁切的负载、Windows 与真实客户端验证见[拥有者核对清单](owner-verification-queue.md) B9、C26、D27。
- 章节图只能从 MP4/MOV 章节图轨提取，不从视频帧生成（ADR 0007），Matroska 不提取；正式受保护镜像内的真实 ffprobe 实测尚未执行。
- 24 小时长测尚未通过，见[图片长测](image-soak.md)。

## 相关文档

| 文档 | 内容 |
| --- | --- |
| [本地图片](local-images.md) | 本地 Primary 海报的启用、解码、缩放、缓存与资产取图 |
| [图片刷新任务](image-refresh-jobs.md) | `image_refresh` 远程抓取的启用、选行规则、失败隔离与重试、取消与续跑、验证 |
| [图片扫描入库](image-ingest.md) | catalog sync 把命名图片与 NFO art 写入 `item_images` 的挂载点、规则与验证 |
| [图片内存](image-memory.md) | 10 万张本地图片的内存验收与预算 |
| [大图内存回收](image-reclaim-pressure.md) | 大图处理后 RSS 不回落的根因与修复 |
| [同尺寸输出](image-same-size.md) | 同尺寸输出不再复制完整位图的回归验证 |
| [图片长测](image-soak.md)、[长测诊断](image-soak-diagnostics.md) | 24 小时长测的进度与失败诊断 |
| [TMDB 图片偏好](tmdb-image-preferences.md) | 图片语言偏好与候选列表 |
| [存储布局](storage-layout.md) | 所有本地资产的创建者、清理者与能否重建 |
| [领域模型](domain-model.md) | `item_images` 与条目、根目录的关系 |
