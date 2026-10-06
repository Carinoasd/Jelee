# 图片资产（G40）

本文是图片功能的总览：支持的图片类型与来源、选图顺序、存储、HTTP 接口、处理与资源上限、清理，以及尚未完成的部分。各专题的细节与验收证据见文末“相关文档”。本文内容按 2026-10-06 的代码核对（`internal/domain/item_images.go`、`internal/domain/images.go`、`internal/app/images.go`、`internal/adapter/images/`、`internal/adapter/http/images.go`、`internal/adapter/compat/images.go`、`internal/platform/config/images.go`）。

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

同一槽位有多行时，先取**锁定**的行（每个槽位最多一行锁定，由唯一索引保证），否则按 local → nfo → remote → embedded 的顺序；最多尝试 4 个候选，文件不存在或字节尚未入库就换下一个。请求处理过程中**不会**抓取远程图片，也不会现场提取内嵌封面，只读取持久存放区中已有的字节。

槽位中没有可用的行、且请求的是 Primary/0 时，退回媒体文件旁的海报：先找 `<文件基名>-poster.jpg|jpeg|png`，再找 `poster.jpg|jpeg|png`（不区分大小写）。详见[本地图片](local-images.md)。

内嵌封面提取默认关闭，用 `JELEE_ENABLE_EMBEDDED_COVERS=true` 开启；它只写 Primary 槽位的 `embedded` 行，槽位已锁定或已有其他来源时跳过，每次尝试的结果记录在 `item_embedded_cover_attempts`（迁移 080）。

## 存储（G40.5）

| 位置 | 内容 | 可否重建 |
| --- | --- | --- |
| `JELEE_IMAGE_STORE_ROOT/originals/<sha256 前两位>/<sha256>` | 原图，按内容寻址，相同内容只存一份 | 本地、NFO、内嵌来源可以重建；**锁定的远程图片原图只存在这里**，见[备份与还原](backup-restore.md) |
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

缓存与校验：

- `ETag` 是输出 JPEG 的 SHA-256（强校验）；带 `If-None-Match` 且匹配时返回 304。
- `tag` 等于原图 SHA-256 时返回 `Cache-Control: private, max-age=31536000, immutable`，否则 `private, no-cache, must-revalidate`；`Vary` 包含携带凭据的请求头。
- 不支持 Range（`Accept-Ranges: none`）。

权限：图片查询在同一条 SQL 中按用户、会话与媒体库授权过滤（[ADR 0003](adr/0003-unified-access-filter-in-sql.md)）；渲染完成后、写出响应前会再次核对可见性，缓存命中与 304 也不例外。看不到的条目与不存在的条目返回相同结果。原生接口接受任意会话类型；兼容层需要原生会话，并要求登录（[ADR 0004](adr/0004-compat-stricter-than-upstream.md)）。各角色见[权限矩阵](permission-matrix.md) P12。

错误码：`image_busy`（503，带 `Retry-After: 1`）、`image_unavailable`（404）、`image_too_large`（413）、`image_unsupported`（415）、`request_timeout`（408）。完整列表见 [API 参考](api-reference.md#错误码)。

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
- `jelee-cli repair image-variants`（或管理员接口 `POST /api/v1/admin/repairs`）以新世代整体作废现有变体，之后按需重新生成；`orphans` 修复动作清理没有对应文件的变体索引行。见[修复](repair.md)。
- 一致性检查会比对 `item_images` 的引用与存放区清单，见[一致性检查](consistency.md)。

## 尚未完成

- **没有图片管理 API**：存储层已有写入、锁定、删除图片槽位的方法（并写审计），但没有 HTTP 或 CLI 入口，管理员目前不能通过 API 上传、锁定或删除图片（[开发待办](owner-dev-tasks.md)任务二）。
- 扫描不会把媒体目录中的旁车图片写入 `item_images`；本地海报只通过上文的回退规则读取。
- 受控的远程图片抓取器已实现但尚未接线，没有图片刷新任务，因此 `remote` 行目前只来自元数据导入。
- 不支持 WebP/AVIF 输出，不支持裁剪与焦点。
- 24 小时长测尚未通过，见[图片长测](image-soak.md)。

## 相关文档

| 文档 | 内容 |
| --- | --- |
| [本地图片](local-images.md) | 本地 Primary 海报的启用、解码、缩放、缓存与资产取图 |
| [图片内存](image-memory.md) | 10 万张本地图片的内存验收与预算 |
| [大图内存回收](image-reclaim-pressure.md) | 大图处理后 RSS 不回落的根因与修复 |
| [同尺寸输出](image-same-size.md) | 同尺寸输出不再复制完整位图的回归验证 |
| [图片长测](image-soak.md)、[长测诊断](image-soak-diagnostics.md) | 24 小时长测的进度与失败诊断 |
| [TMDB 图片偏好](tmdb-image-preferences.md) | 图片语言偏好与候选列表 |
| [存储布局](storage-layout.md) | 所有本地资产的创建者、清理者与能否重建 |
| [领域模型](domain-model.md) | `item_images` 与条目、根目录的关系 |
