# 图片资产（G40）

本文是图片功能的总览：支持的图片类型与来源、选图顺序、存储、HTTP 接口、管理接口、处理与资源上限、清理，以及尚未完成的部分。各专题的细节与验收证据见文末“相关文档”。本文内容按 2026-10-29 的代码核对（`internal/domain/item_images.go`、`internal/domain/images.go`、`internal/app/images.go`、`internal/app/images_admin.go`、`internal/adapter/images/`、`internal/adapter/http/images.go`、`internal/adapter/http/images_admin.go`、`internal/adapter/compat/images.go`、`internal/platform/config/images.go`）。

## 类型与槽位（G40.1）

每个条目按“类型＋序号”划分槽位。支持 15 种类型：Primary、Backdrop、Logo、ClearLogo、Banner、ClearArt、Art、Disc、Thumb、Landscape、Chapter、Box、BoxRear、Menu、Profile；`Fanart` 视为 `Backdrop` 的别名。只有 Backdrop 与 Chapter 允许序号大于 0（0–9999），其他类型只有序号 0。数据库中的 CHECK 约束（`item_images`，迁移 `000058_item_images`）与领域代码使用同一份类型表。

## 来源与选图顺序（G40.3、G40.4、G40.10）

`item_images.source_kind` 记录图片来源：

| 来源 | 含义 |
| --- | --- |
| `local` | 媒体目录中的本地图片文件 |
| `nfo` | NFO 中引用的图片 |
| `remote` | 外部（TMDB）图片，字节保存在持久存放区 |
| `embedded` | 从视频文件中提取的内嵌封面 |
| `upload` | 管理员通过[管理接口](#管理接口g4010g4011)上传的图片，字节只保存在持久存放区（迁移 000095，暂定编号） |

同一槽位有多行时，先取**锁定**的行（每个槽位最多一行锁定，由唯一索引保证），否则按 local → nfo → remote → embedded → upload 的顺序；最多尝试 5 个候选（每种来源至多一行），文件不存在或字节尚未入库就换下一个。上传接口总是锁定上传的图片，所以它会胜过其他来源；解锁后它排在最后，作为后备。请求处理过程中**不会**抓取远程图片，也不会现场提取内嵌封面，只读取持久存放区中已有的字节。

槽位中没有可用的行、且请求的是 Primary/0 时，退回媒体文件旁的海报：先找 `<文件基名>-poster.jpg|jpeg|png`，再找 `poster.jpg|jpeg|png`（不区分大小写）。详见[本地图片](local-images.md)。

扫描入库（G40.2、G40.3）已实现：每次 catalog sync 完成后，条目目录中按 G40.2 命名的图片写成 `local` 行，NFO art 中 root 内的相对路径写成 `nfo` 行、`https://` 网址写成 `remote` 行（只记录，不抓取）；季图挂到季条目，`season-all-*` 展开到各季，一个目录有多部电影时不采用 `poster.jpg` 这类共用名称。重扫幂等；文件消失时扫描写入的非锁定、非手动 `local`／`nfo` 行被移除，锁定或手动（`item_images.manual`，迁移 000094）的行从不改动，用户的图片文件从不修改或删除。设计与验证见[图片扫描入库](image-ingest.md)。

内嵌封面提取默认关闭，用 `JELEE_ENABLE_EMBEDDED_COVERS=true` 开启；它只写 Primary 槽位的 `embedded` 行，槽位已锁定或已有其他来源时跳过，每次尝试的结果记录在 `item_embedded_cover_attempts`（迁移 080）。

## 存储（G40.5）

| 位置 | 内容 | 可否重建 |
| --- | --- | --- |
| `JELEE_IMAGE_STORE_ROOT/originals/<sha256 前两位>/<sha256>` | 原图，按内容寻址，相同内容只存一份 | 本地、NFO、内嵌来源可以重建；**锁定的远程图片与管理员上传的图片原图只存在这里**，见[备份与还原](backup-restore.md) |
| `JELEE_IMAGE_STORE_ROOT/variants/<世代>/<原图 sha256>/<变体键>` | 缩放后的变体，只有最新世代有效 | 可以重建 |
| `JELEE_IMAGE_STORE_ROOT/tmp/` | 写入暂存，fsync 后原子重命名 | 不需要备份 |
| `JELEE_IMAGE_TEMP_ROOT` | 处理过程中的临时文件 | 不需要备份，启动时清扫 |
| 数据库 `item_images`、`image_variants`、`item_embedded_cover_attempts` | 槽位与来源、变体索引（含 LRU 时间）、内嵌封面尝试记录 | 随 `pg_dump` 备份；元数据导出只包含锁定的图片行 |

未设置 `JELEE_IMAGE_STORE_ROOT` 时不启用持久存放区，只使用内存缓存。存放区目录不能与媒体根目录或 `JELEE_IMAGE_TEMP_ROOT` 重叠；存放区不认识的文件只计数，不读、不移动、不删除。各目录的创建者与清理者见[存储布局](storage-layout.md)。

## HTTP 接口（G40.8）

| 接口 | 说明 |
| --- | --- |
| `GET`／`HEAD /images/{type}/{id}` | 原生接口（挂在根路径，不在 `/api/v1` 下）。查询参数只接受 `width`、`height`（0–2048）、`quality`（0–100）、`format`（只能是 `jpeg`）、`index`、`tag`，其他参数 400。都不给时按 640×640 等比缩放 |
| `GET`／`HEAD /compat/Items/{itemId}/Images/{imageType}[/{imageIndex}]` | 兼容层接口，见[兼容矩阵](compat-matrix.md)。`Width`／`MaxWidth`／`FillWidth` 等取最小值，上限 2048；`format` 接受上游的名称，但输出一律是 JPEG；上游的 Logo、Art、Thumb 映射到对应的 Jelee 槽位 |
| `GET /api/v1/metadata/tmdb/{movies\|series}/{id}/images` | 管理员查看 TMDB 图片候选，按媒体库的图片语言偏好排序，不下载图片，见 [TMDB 图片偏好](tmdb-image-preferences.md) |
| `GET /api/v1/jobs/{id}/images` | 扫描任务的图片清单统计（新增、变化、未变、缺失），不是图片管理接口 |
| `GET /api/v1/items/{id}/images` | 管理员列出条目的全部图片行，见[管理接口](#管理接口g4010g4011) |
| `POST /api/v1/items/{id}/images/{type}` | 管理员上传或更换一个槽位的图片（`index` 查询参数同 `/images`；槽位已有图片时需 `confirm=true`） |
| `DELETE /api/v1/items/{id}/images/{type}` | 管理员删除一个槽位的图片引用（`source` 可选，`confirm=true` 必填） |
| `POST /api/v1/items/{id}/images/{type}/lock`、`/unlock` | 管理员锁定或解锁槽位中某个来源（`source` 必填，请求体 `{}`） |

缓存与校验：

- `ETag` 是输出 JPEG 的 SHA-256（强校验）；带 `If-None-Match` 且匹配时返回 304。
- `tag` 等于原图 SHA-256 时返回 `Cache-Control: private, max-age=31536000, immutable`，否则 `private, no-cache, must-revalidate`；`Vary` 包含携带凭据的请求头。
- 不支持 Range（`Accept-Ranges: none`）。

权限：图片查询在同一条 SQL 中按用户、会话与媒体库授权过滤（[ADR 0003](adr/0003-unified-access-filter-in-sql.md)）；渲染完成后、写出响应前会再次核对可见性，缓存命中与 304 也不例外。看不到的条目与不存在的条目返回相同结果。原生接口接受任意会话类型；兼容层需要原生会话，并要求登录（[ADR 0004](adr/0004-compat-stricter-than-upstream.md)）。各角色见[权限矩阵](permission-matrix.md) P12。

错误码：`image_busy`（503，带 `Retry-After: 1`）、`image_unavailable`（404）、`image_too_large`（413）、`image_unsupported`（415）、`request_timeout`（408）。完整列表见 [API 参考](api-reference.md#错误码)。

## 管理接口（G40.10、G40.11）

五个接口都只限管理员（权限矩阵 P20）：非管理员在查找条目之前就得到 403 `forbidden`，看得到与看不到的条目结果相同。管理员本身也经过统一权限过滤（[ADR 0003](adr/0003-unified-access-filter-in-sql.md)），被内容规则隐藏的条目与不存在的条目一样返回 404 `not_found`。槽位用路径中的 `type`（`Fanart` 是 `Backdrop` 的别名）与查询参数 `index` 指定，规则与 `/images` 相同；其他查询参数一律 400。响应中的图片行只有 `reference`（`file`、`url` 或 `store`），从不返回媒体库路径或网址。

| 接口 | 行为 |
| --- | --- |
| `GET /api/v1/items/{id}/images` | 每个槽位的全部行，锁定的在前，其余按选图顺序 |
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

## 处理与资源上限（G40.6、G40.12）

- 解码：JPEG、PNG、WebP、GIF（只取第一帧）、BMP、TIFF；AVIF/HEIF 返回 `image_unsupported`。每边最多 65535 像素。
- 输出：只有 JPEG；只缩小不放大，保持比例，不裁剪。
- 防止像素炸弹：先读取头部得到尺寸，再按目标尺寸估算内存，超过单图预算返回 413。
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
| `JELEE_IMAGE_DEFAULT_QUALITY` | 85 | 1–100 |
| `JELEE_IMAGE_STORE_ORIGINAL_BYTES` | 4 GiB | 64 MiB–4 TiB |
| `JELEE_IMAGE_STORE_VARIANT_BYTES` | 1 GiB | 16 MiB–1 TiB |
| `JELEE_IMAGE_STORE_ENTRIES` | 131072（每类） | 1024–2097152 |
| `JELEE_ENABLE_EMBEDDED_COVERS` | `false` | 布尔值 |

配置校验要求“并发数 × 单图预算 + 缓存”不超过 1 GiB。

## 清理与修复（G40.11、G36.3、G50.4）

- 变体按 LRU 淘汰：变体字节数或条目数达到 `JELEE_IMAGE_STORE_VARIANT_BYTES`／`JELEE_IMAGE_STORE_ENTRIES` 的 90% 时，后台循环通过数据库索引删除最久未用的变体，直到降到 80%；期间被访问过的变体会保留。
- 通过[管理接口](#管理接口g4010g4011)删除或更换图片后，不再被引用的内容的变体立即清除；原图不删除。
- `jelee-cli repair image-variants`（或管理员接口 `POST /api/v1/admin/repairs`）以新世代整体作废现有变体，之后按需重新生成；`orphans` 修复动作清理没有对应文件的变体索引行。见[修复](repair.md)。
- 一致性检查会比对 `item_images` 的引用与存放区清单，见[一致性检查](consistency.md)。

## 尚未完成

- 图片管理只有 HTTP 接口，没有 CLI 命令，前端条目页也还没有操作入口。删除扫描写入的行只删除引用，文件仍在时下次扫描会重新加入；没有“不再采用此文件”的记录。
- 受控的远程图片抓取器已实现但尚未接线，没有图片刷新任务，因此 `remote` 行（来自 NFO 扫描入库与元数据导入）还没有内容，请求时不可用。
- 不支持 WebP/AVIF 输出，不支持裁剪与焦点。
- 24 小时长测尚未通过，见[图片长测](image-soak.md)。

## 相关文档

| 文档 | 内容 |
| --- | --- |
| [本地图片](local-images.md) | 本地 Primary 海报的启用、解码、缩放、缓存与资产取图 |
| [图片扫描入库](image-ingest.md) | catalog sync 把命名图片与 NFO art 写入 `item_images` 的挂载点、规则与验证 |
| [图片内存](image-memory.md) | 10 万张本地图片的内存验收与预算 |
| [大图内存回收](image-reclaim-pressure.md) | 大图处理后 RSS 不回落的根因与修复 |
| [同尺寸输出](image-same-size.md) | 同尺寸输出不再复制完整位图的回归验证 |
| [图片长测](image-soak.md)、[长测诊断](image-soak-diagnostics.md) | 24 小时长测的进度与失败诊断 |
| [TMDB 图片偏好](tmdb-image-preferences.md) | 图片语言偏好与候选列表 |
| [存储布局](storage-layout.md) | 所有本地资产的创建者、清理者与能否重建 |
| [领域模型](domain-model.md) | `item_images` 与条目、根目录的关系 |
