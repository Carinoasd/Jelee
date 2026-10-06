# API 参考

本文是 Jelee HTTP API 的总览：路由分组、认证方式、请求与响应约定、分页、错误码，以及原生 API 与兼容层 `/compat` 的区别。**逐条路由的参数、请求体、响应结构与状态码以 OpenAPI 文档为准**，本文不重复列出：

- 仓库中的参考版本：[`api/openapi.json`](../api/openapi.json)（OpenAPI 3.1，按开启全部功能的参考配置生成）。
- 运行中的实例：`GET /api/v1/openapi.json` 按实例的实际配置生成，只包含该实例注册的路由；`GET /api-docs` 是指向它的简单页面。
- 前端使用的 TypeScript 类型 `web/src/api/schema.d.ts` 由同一份文档生成。

各路由由哪些角色调用见[权限矩阵](permission-matrix.md)；兼容层逐项行为见[兼容矩阵](compat-matrix.md)。

## 维护与校验

| 操作 | 命令 | 说明 |
| --- | --- | --- |
| 重新生成 OpenAPI | `make openapi`（即 `go run ./tools/openapi`） | 由路由代码中的规格函数生成 `api/openapi.json`，键排序、两格缩进 |
| 校验 OpenAPI | `make openapi-check` | 提交的文档必须是最新的；每条注册路由都有文档、每个文档路径都有路由（逐个功能组合检查）；错误码都有描述。`make lint` 包含此项 |
| 重新生成前端类型 | `npm run api:generate --workspace @jelee/web` | CI 以 `api:check` 检查 `schema.d.ts` 是否过期 |
| 本文的错误码表 | `JELEE_API_REFERENCE_UPDATE=1 go test -run TestAPIReferenceErrorCodeTable ./internal/adapter/http/` | 表格由 `errorCodeStatuses` 与 zh-CN 消息目录生成；`TestAPIReferenceErrorCodeTableMatchesImplementation` 校验两者一致 |
| 本文的分组 | `go test -run TestAPIReferenceGroupsCoverOpenAPI ./internal/adapter/http/` | 每个 OpenAPI 路径必须属于下表某个分组，且每个分组前缀至少对应一个路径 |
| 文档链接与错误码引用 | `make doc-check`（`go run ./tools/doccheck`） | 所有文档里写成“`错误码`（状态）”或“`错误码`/状态”的引用都要与 `errorCodeStatuses` 一致 |

OpenAPI 中的 Jelee 扩展字段：

| 字段 | 含义 |
| --- | --- |
| `x-jelee-role: administrator` | 只限管理员；其他调用者在任何查询之前得到 403 `forbidden` |
| `x-jelee-role: guest` | 只限分享访客会话 |
| `x-jelee-session: native` | 只接受原生会话；网页会话得到 403 `web_playback_disabled` |
| `x-jelee-setup` | 初始设置向导相关的说明 |
| `x-jelee-limits` | 该操作的额外数量或大小上限 |
| `x-jelee-dev-only` | 只在可开启开发者模式的实例上注册 |
| `x-jelee-list` | 列表操作的统一约定：`paging`（`keyset`／`offset`／`memory`）、可选字段 `fields`、排序白名单 `sort`、方向白名单 `order`、过滤白名单 `filters`、`maxLimit`、`offsetMax`，见下文“列表约定” |
| `x-jelee-removed-features`（顶层） | 已移除的上游功能及其 501 响应 |

## 路由分组

<!-- api-groups:begin -->
| 路径前缀 | 内容 | 权限矩阵 |
| --- | --- | --- |
| `/healthz`、`/readyz`、`/api/v1/system`、`/api-docs`、`/api/v1/openapi.json` | 存活、就绪（含依赖状态码）、服务能力标志、API 文档 | P01 |
| `/api/v1/setup` | 初始设置向导，见[初始设置](setup-wizard.md) | P02 |
| `/api/v1/auth` | 网页登录、原生登录、二次验证、注销、轮换令牌、CSRF 令牌 | P03、P04 |
| `/api/v1/users`、`/api/v1/sessions` | 本人资料、偏好、两步验证、应用密码、会话、个人数据权利；用户管理；本人续播、观看统计、音轨偏好 | P05–P10、P14、P16、P19 |
| `/api/v1/access` | 全局访问策略、分级表、网络规则，见[访问控制](access-control.md) | P10 |
| `/api/v1/items`、`/api/v1/version-operations` | 条目浏览与详情、媒体源、播放信息、已播放标记、用户数据、音轨偏好、元数据编辑、版本拆分与合并、重新探测 | P11、P13、P14、P16、P20 |
| `/images` | 条目图片（缩放、ETag），见[图片资产](image-assets.md) | P12 |
| `/api/v1/sources` | 原文件直投与外挂/内嵌轨道、附件、OCR 字幕，见[直投](direct-delivery.md) | P13 |
| `/api/v1/playback` | 播放开始/进度/停止上报；全站播放会话 | P14、P15 |
| `/api/v1/collections`、`/api/v1/playlists` | 合集与播放清单，见[合集与播放清单](collections-playlists.md) | P17、P18 |
| `/api/v1/watch-stats` | 全站观看统计与导出 | P19 |
| `/api/v1/metadata` | TMDB 候选搜索、预览与图片候选 | P21 |
| `/api/v1/libraries`、`/api/v1/jobs` | 媒体库、扫描、排程、监看、目录同步、NFO 策略与校验、探测重建；任务与任务报告，见[任务 API](jobs-api.md) | P22 |
| `/api/v1/admin` | 自愈修复与回滚，见[修复](repair.md) | P23 |
| `/api/v1/webhooks` | Webhook 端点、投递记录、重放，见 [Webhook](webhooks.md) | P24 |
| `/api/v1/client-control` | 客户端管控策略、规则、命中记录、已知客户端，见[客户端管控](client-control.md) | P25 |
| `/api/v1/shares` | 分享链接管理、兑换、当前分享 | P03、P26、P27 |
| `/api/v1/site` | 站点外观、插件设置、导入导出 | P28 |
| `/metrics` | Prometheus 指标，见[指标](metrics.md) | P29 |
| `/api/v1/dev`、`/debug/pprof` | 开发者模式令牌、状态、开关与性能剖析，见[开发者模式](developer-mode.md) | P30–P32 |
<!-- api-groups:end -->

此外：

- `/compat/…` 是第三方客户端兼容层，不在 OpenAPI 中（见下文“原生 API 与兼容层”）。
- 其他不以 `/api` 开头的 GET/HEAD 请求，在配置了前端目录时由前端静态文件回应；`/api/…` 下未注册的路径一律返回原生格式的 404。
- 转码、HLS、DASH 等路径在任何路由之前返回 409 `transcode_disabled`；直播、频道、DLNA 等已移除功能返回 501 `feature_removed`。

## 认证

### 原生 API

| 方式 | 写法 | 适用 |
| --- | --- | --- |
| Bearer 令牌 | `Authorization: Bearer <43 字符令牌>` | 原生客户端与脚本；只要带了 `Authorization` 头就以它为准 |
| 会话 Cookie | `__Host-jelee_session`（HttpOnly、Secure、SameSite=Strict） | 只接受网页会话；用 Cookie 认证的 POST/PUT/PATCH/DELETE 必须带 `X-Jelee-CSRF`，否则 403 `csrf_failed`。CSRF 令牌来自登录、轮换或 `GET /api/v1/auth/csrf` |

会话由以下接口发放，令牌在数据库中只保存 SHA-256 摘要：

- `POST /api/v1/auth/login`：网页会话，设置 Cookie。网页会话可以浏览，**不能播放**。
- `POST /api/v1/auth/login/native`：原生会话，令牌只在响应体中返回。管理员必须先为该用户开启原生登录（否则 403 `native_login_disabled`）；带 `Origin`、`Sec-Fetch-*` 等浏览器标头的请求返回 403 `forbidden`。
- 开启了两步验证的账号：登录先返回挑战，再用 `POST /api/v1/auth/login/second-factor` 提交验证码或恢复码。原生登录与兼容层登录必须使用应用密码（`POST /api/v1/users/me/app-passwords`），用账号密码会得到 403 `app_password_required`。见[两步验证](two-factor.md)。
- `POST /api/v1/shares/redeem`、`/redeem/native`：用分享令牌换取访客会话（网页或原生），见[访问控制](access-control.md)。
- `POST /api/v1/auth/rotate`：轮换令牌，保留会话类型。

登录、二次验证与兼容层登录共用同一套限速（每 IP、每用户）与账号锁定，超出返回 429 `auth_rate_limited`，见[密码安全](password-security.md)。

另外两种令牌**不是** API 凭据：初始设置向导的一次性引导令牌（请求头 `X-Jelee-Setup-Token`，见[初始设置](setup-wizard.md)），以及开发者模式的一次性令牌（只用于 `jelee-cli devmode enable`）。

### 兼容层

兼容层只接受**原生会话**令牌，按以下顺序取第一个非空值：`Authorization: MediaBrowser Token="…"`（也接受旧的 `Emby` 方案，`Authorization` 为空时读 `X-Emby-Authorization`）、`X-Emby-Token` 或 `X-MediaBrowser-Token`、查询参数 `ApiKey`、`api_key`。网页会话、Bearer、Cookie 与分享访客令牌一律 401。带 `Origin` 的请求一律 403，不发送任何 CORS 标头。

## 请求约定

- **Host 白名单**：`Host` 必须在 `allowedHosts`（默认 `localhost`、`127.0.0.1`、`::1`）中，否则 400 `invalid_host`。部署在反向代理后时见[可信代理](trusted-proxies.md)。
- **JSON 请求体**：`Content-Type` 必须是 `application/json`（字符集只能是 UTF-8），否则 415 `unsupported_media_type`；只接受一个 JSON 对象，拒绝未知字段、重复键（包括大小写变体）、`null`（个别字段在 OpenAPI 中注明可为 null）与超过 64 层的嵌套，均为 400 `invalid_request`；超过该路由的大小上限为 413 `body_too_large`。
- **查询参数**：只接受该路由声明的参数，未知或重复的参数返回 400 `invalid_request`；列表操作的参数见下文“列表约定”。
- **CORS**：原生 API 不发送任何 `Access-Control-*` 标头；前端与 API 同源部署。
- **版本与弃用**：自有接口都在 `/api/v1` 下；弃用的接口响应会带 `Deprecation`、`Sunset` 与 `Link: <替代>; rel="successor-version"` 标头，时间线、替代方案与 v2 过渡政策见 [API 弃用](api-deprecations.md)。浏览版的接口文档在服务的 `/api-docs`。

## 响应约定

- 成功的 JSON 响应顶层一律是 `{"data": …}`，`Content-Type: application/json; charset=utf-8`。少数接口返回 NDJSON（个人数据导出、观看统计导出）、CSV、`image/jpeg` 或原始媒体字节，以 OpenAPI 为准。
- 错误响应（兼容层除外）一律是：

  ```json
  {"error": {"code": "not_found", "message": "…", "details": {}, "traceId": "0123456789abcdef0123456789abcdef"}}
  ```

  `code` 是稳定的机器可读错误码（见文末错误码表）；`message` 是安全的本地化消息，不含路径、连接串或底层错误；`details` 通常为空对象，只有设置向导的校验失败与 `/readyz` 会带字段；`traceId` 等于响应头 `X-Request-ID`，也写入日志与审计（见[日志](logging.md)）。
- 消息语言按 `Accept-Language` 在 zh-CN、zh-TW、ja-JP、en-US 中选择：没有该标头时用 zh-CN，无法匹配时用 en-US；已登录且设置了界面语言的用户以其设置为准。响应带 `Content-Language` 与 `Vary: Accept-Language`。
- 每个响应都带 `X-Request-ID`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`、严格的 `Content-Security-Policy`、`X-Jelee-Dev-Mode`，默认 `Cache-Control: no-store`；HTTPS 连接另加 HSTS。条件请求与缓存只在图片（`ETag`、`If-None-Match`、304）、原文件直投（`ETag`、`Range`、`If-Range`、412、416）与前端静态文件（`Last-Modified`、`Range`）上提供。
- **压缩（G11.7）**：请求带 `Accept-Encoding: gzip`（或 q 值不为 0 的 `*`）时，JSON、NDJSON、HTML、CSS、JavaScript、纯文本与 CSV 响应在响应体达到门槛（默认 1 KiB）后以 gzip 压缩，带 `Content-Encoding: gzip` 并去掉 `Content-Length`；这些类型的响应无论是否压缩都带 `Vary: Accept-Encoding`。**从不压缩**：原文件直投、外挂／内嵌轨道、附件、OCR 字幕与图片（按内容类型白名单判断，字幕、图片、音视频与字体类型都不在其中）；任何带 `Range` 的请求；带 `Content-Range` 或 `Accept-Ranges: bytes` 的响应（前端静态文件除外，它只在请求不带 `Range` 时压缩，压缩后去掉 `Accept-Ranges`）；HEAD、204、206、304；已带 `Content-Encoding` 或 `Cache-Control: no-transform` 的响应。
- **压缩与 ETag**：压缩后的字节与原表示不同，因此压缩响应中的强 `ETag` 改为弱形式（`"x"` → `W/"x"`），304 不带响应体、不压缩。`If-None-Match` 按 RFC 9110 用弱比较，`W/"x"` 与 `"x"` 都能得到 304；以后新增的条件请求处理器也必须用弱比较（`http.ServeContent` 即如此）。目前带 `ETag` 的图片与直投从不压缩，不受影响。开关、级别与门槛见[部署](deployment.md#响应压缩g117)。

## 列表约定

所有原生列表操作遵守同一套约定（G08.2、G49.1），由 `internal/adapter/http/list_contract.go` 的列表契约统一解析，OpenAPI 中逐个操作列出参数并以 `x-jelee-list` 记录契约。`TestOpenAPIListOperationsDeclareListContract` 要求每个列表操作都有契约（或列入下文的豁免表）、声明全部列表参数、其余查询参数恰好是过滤白名单，且可选字段恰好是行 schema 的成员。

| 参数 | 含义 |
| --- | --- |
| `cursor` | 不透明游标，取自上一页的 `nextCursor`；`nextCursor` 为空字符串表示没有下一页。游标绑定该查询，不能跨查询使用。与 `offset` 同时出现返回 400 `invalid_request` |
| `offset` | 兼容用的偏移分页：从头跳过的行数 |
| `limit` | 每页行数，默认值与上限以 OpenAPI 为准（常见默认 50、上限 100） |
| `sort`、`order` | 排序键与方向，只接受白名单中的值 |
| `fields` | 逗号分隔的行成员，只返回这些成员；标识成员（通常是 `id`）总会返回 |
| 过滤参数 | 各操作自己的过滤白名单（例如 `state`、`includeDeleted`、`ruleId`），值由该操作校验 |

未知参数、重复参数、白名单外的排序键或方向、未知或空的字段名、超出范围的 `limit`／`offset`、格式不对的游标，一律 400 `invalid_request`（与其他严格查询一致）。

按分页方式分三类（`x-jelee-list.paging`）：

- **keyset**（存储按游标分页，大多数列表）：`cursor` 优先。存储只按一个固定顺序读取，因此排序白名单只有这一个键（例如用户、任务按 `id` 升序，投递记录、命中记录按时间倒序），显式传入相同的值是允许的。`offset` 是兼容用法：服务端从头按游标逐页（每页不超过该列表的 `limit` 上限）跳过，因此最多 1000，响应仍给出继续用的 `nextCursor`；大量翻页请用游标。`GET /api/v1/items` 另有原有的偏移形式（`offset`、`libraryId`、`parentId`、`type`、`sort`（可逗号分隔多个键）、`order`、`q`，响应含 `offset` 与 `total`），行为不变。
- **offset**（存储按偏移分页：`GET /api/v1/users/me/resume`）：`cursor` 是不透明的位置值（形如 `o.50`），与对应的 `offset` 等价；响应另含 `nextCursor`。
- **memory**（整体读取的小列表：本人与他人的会话、应用密码、媒体库授权、分享、网络规则、客户端规则、播放会话、Webhook）：不带 `limit` 时照旧返回全部行；带 `sort` 时在内存中按白名单字段排序（缺失或 null 排最前，同值保持存储顺序，`order` 必须与 `sort` 一起使用），不带 `sort` 时保持存储顺序；`cursor` 为位置值。响应增加 `pagination`：`nextCursor`、`offset`、`total`，以及给了 `limit` 时的 `limit`。行本身是数组的列表（如 `GET /api/v1/shares`）放在顶层 `pagination`，其余（`GET /api/v1/webhooks`）放在 `data.pagination`。

字段选择只作用于已经套用全部可见性规则与遮罩之后的响应，只会删除成员，不会补回被遮罩或省略的成员；选择后 schema 中的其他必填成员可能缺失。

分页对象的位置沿用各接口原有形状：多数为 `data.pagination`（或行数组旁的顶层 `pagination`）；合集、播放清单、NFO 校验结果与忽略报告的 `nextCursor` 与列表字段同层。

豁免（看起来像列表、但不接受列表参数，原因见 `listExemptions`）：

| 操作 | 原因 |
| --- | --- |
| `GET /api/v1/access/library-grants` | 授权矩阵（G48.7）作为一份文件：全部媒体库与前 1000 个账号的授权，矩阵页整份读取 |
| `GET /api/v1/access/parental-ratings` | 分级代码表作为一份文件：至多 500 个代码，由 PUT 整表替换（G48.4），不是可逐一寻址的资源集合 |
| `GET /api/v1/client-control/hits/export` | 导出下载：按过滤条件一次给出全部（受命中记录保留期限制）并附 `count` |
| `GET /api/v1/watch-stats/export` | NDJSON／CSV 导出下载；`limit` 只是调低导出行数上限 |
| `GET /api/v1/libraries/{id}/nfo/current-validations/{observationId}/issues` | 单个观察结果的问题：最多保留 64 条、没有标识，只按偏移分页 |
| `GET /api/v1/site/plugins`、`GET /api/v1/site/plugins/config` | 站点设置中的插件开关（固定的小映射），不是集合 |
| `GET /api/v1/collections/{id}` | 单个合集及其成员：资源视图，内嵌列表受合集大小限制 |
| `GET /api/v1/items/{id}/sources`、`GET /api/v1/items/{id}/playback` | 单个条目的媒体源（版本）与播放信息，属于条目资源本身 |

## 限流与重试

返回 429 或 503 的错误中，`client_rate_limited`、`job_queue_full`、`playback_busy`、`image_busy` 会带 `Retry-After`。并发播放上限（`stream_limit`、`user_stream_limit`、`device_stream_limit`）按实例计数，多实例部署时的含义见 [ADR 0002](adr/0002-no-redis-cache-boundary.md)。

## 原生 API 与兼容层

| 方面 | 原生 API（`/api/v1`） | 兼容层（`/compat`） |
| --- | --- | --- |
| 用途 | Jelee 前端、原生客户端、脚本 | 只为让现有第三方客户端能浏览与直投播放 |
| 默认 | 开启 | **关闭**；`JELEE_COMPAT_ENABLED=true` 或配置 `enableCompat` 开启，可用 `JELEE_COMPAT_SERVER_ID` 固定服务器 ID |
| 契约 | OpenAPI 完整描述，CI 校验 | 不在 OpenAPI 中；以[兼容矩阵](compat-matrix.md)与上游对照测试为准，尚未经真实客户端验收 |
| 认证 | Bearer 或网页 Cookie＋CSRF | 只接受原生会话令牌，写法见上文 |
| 字段风格 | camelCase，固定的 `data`/`error` 信封 | PascalCase，省略 null 成员 |
| 错误 | 统一错误信封 | 比照上游生产行为：401/403/404/405/413/415/503 空响应体，400/500 为纯文本；只有 409 `transcode_disabled` 与 501 沿用原生信封 |
| 路径匹配 | 大小写敏感 | 大小写不敏感 |
| 范围 | 全部功能 | System、Users、UserViews/Items、PlaybackInfo、视频流与字幕、播放上报、UserData、图片；其余返回 404 |
| 隐私 | — | 比上游严格（[ADR 0004](adr/0004-compat-stricter-than-upstream.md)） |

两者共用同一套会话、权限过滤器、直投处理器、限流与客户端管控，兼容层没有独立的账号或查询逻辑。

## 错误码

下表由 `internal/adapter/http/openapi_contract.go` 的 `errorCodeStatuses` 与服务端消息目录（`internal/platform/i18n`）生成，OpenAPI 的 `ErrorCode` 枚举与之相同。一个错误码可能对应多个 HTTP 状态时用“、”分隔。客户端应以 `code` 而非消息文字判断错误。运维诊断用的错误码（`jelee-cli doctor`）是另一套，见[故障排查](troubleshooting.md)。

<!-- error-codes:begin -->
| 错误码 | HTTP 状态 | 默认消息（zh-CN） |
| --- | --- | --- |
| `account_busy` | 503 | 账户服务繁忙，请稍后重试。 |
| `app_password_required` | 403 | 此账户已启用双因素验证。请在此设备上使用在网页端创建的应用专用密码登录。 |
| `auth_rate_limited` | 429 | 身份验证尝试过于频繁，请稍后重试。 |
| `authentication_required` | 401 | 请先登录。 |
| `body_too_large` | 413 | 请求正文超出大小限制。 |
| `client_blocked` | 403 | 此客户端不允许访问服务器。 |
| `client_pending_approval` | 403 | 此客户端正在等待管理员批准。 |
| `client_rate_limited` | 429 | 此客户端的请求过于频繁，请稍后重试。 |
| `client_read_only` | 403 | 此客户端只能读取。 |
| `confirmation_required` | 400 | 这是危险操作，需要明确确认。 |
| `conflict` | 409 | 资源与现有状态冲突。 |
| `csrf_failed` | 403 | 安全令牌缺失或无效，请重新加载页面后再试。 |
| `custom_css_rejected` | 400 | 自定义 CSS 被拒绝：其中含有标记、转义、控制字符或未闭合的区块，或超出长度上限。 |
| `device_stream_limit` | 429 | 此设备的同时播放数量已达上限。 |
| `devmode_inactive` | 409 | 开发者模式未开启。 |
| `devmode_toggle_unavailable` | 409 | 此构建不提供该开发者模式选项。 |
| `feature_removed` | 501 | 不支持设备发现、直播电视、录制和频道。 |
| `forbidden` | 403 | 不允许执行此操作。 |
| `ignore_unavailable` | 503 | 忽略规则扫描暂时不可用。 |
| `image_busy` | 503 | 图片处理繁忙，请稍后重试。 |
| `image_too_large` | 413 | 图片超过处理上限。 |
| `image_unavailable` | 404 | 图片暂时不可用。 |
| `image_unsupported` | 415 | 不支持此图片格式。 |
| `internal_error` | 500 | 无法完成请求。 |
| `invalid_host` | 400 | 不允许使用此主机名。 |
| `invalid_password` | 400 | 当前密码不正确。 |
| `invalid_range` | 416 | 无法提供请求的字节范围。 |
| `invalid_request` | 400 | 请求无效。 |
| `invalid_two_factor_code` | 400 | 验证码或恢复码不正确，或已被使用。 |
| `job_busy` | 409 | 此媒体库已有正在等待或执行的任务。 |
| `job_queue_full` | 429 | 任务队列已满，请稍后重试。 |
| `jobs_busy` | 503 | 任务服务繁忙，请稍后重试。 |
| `last_admin` | 409 | 必须保留一名启用的管理员。 |
| `login_challenge_invalid` | 401 | 登录步骤已过期或已被使用，请重新输入密码登录。 |
| `lookup_timeout` | 504 | 媒体查询超时。 |
| `metadata_unavailable` | 503 | 元数据来源暂时不可用，请稍后重试。 |
| `method_not_allowed` | 405 | 不支持此请求方法。 |
| `metrics_busy` | 503 | 指标服务繁忙，请稍后重试。 |
| `native_login_disabled` | 403 | 此账户未启用原生设备登录。 |
| `nfo_cache_capacity` | 409 | NFO缓存容量已达上限。 |
| `nfo_disabled` | 409 | 此媒体库尚未启用NFO校验。 |
| `nfo_identity_mismatch` | 409 | NFO校验版本已改变，请重试任务。 |
| `nfo_invalidated` | 409 | NFO校验范围已改变，请重试任务。 |
| `nfo_reader_unavailable` | 503 | NFO校验暂时不可用。 |
| `not_found` | 404 | 找不到该资源。 |
| `not_ready` | 503 | 服务尚未就绪。 |
| `playback_busy` | 503 | 播放进度上报繁忙，请稍后再试。 |
| `precondition_failed` | 412 | 请求的前置条件未满足。 |
| `probe_cache_capacity` | 409 | 探测缓存容量已达上限。 |
| `probe_disabled` | 409 | 媒体探测尚未启用。 |
| `probe_identity_mismatch` | 409 | 探测工具身份已改变，请重试任务。 |
| `probe_invalidated` | 409 | 探测范围已改变，请重试任务。 |
| `probe_runtime_unavailable` | 503 | 媒体探测不可用，请检查隔离运行环境。 |
| `request_timeout` | 408 | 请求已取消或超时。 |
| `scan_limit` | 409 | 扫描资源数量已达上限。 |
| `scan_unavailable` | 503 | 无法访问扫描根目录。 |
| `session_limit` | 429 | 有效会话数量已达上限。 |
| `setup_completed` | 410 | 初始引导已完成。 |
| `setup_required` | 503 | 尚未完成初始引导。 |
| `setup_step_order` | 409 | 该引导步骤不是当前步骤。 |
| `setup_token_invalid` | 401 | 引导令牌缺失或无效。 |
| `setup_validation_failed` | 400 | 引导输入需要修正。 |
| `share_forbidden` | 403 | 分享链接不允许此操作。 |
| `share_playback_disabled` | 403 | 此分享链接不允许播放。 |
| `share_read_only` | 403 | 此分享链接为只读。 |
| `share_unavailable` | 404 | 此分享链接不存在、已撤销或已过期。 |
| `stats_export_limit` | 409 | 导出超过行数上限，请缩小范围。 |
| `stream_limit` | 429 | 同时播放数量已达上限。 |
| `transcode_disabled` | 409 | 仅支持原始文件直投。 |
| `two_factor_unavailable` | 409 | 双因素验证不可用：服务器未配置主密钥。 |
| `unsupported_media_type` | 415 | 不支持此请求内容类型。 |
| `user_stream_limit` | 429 | 此账户的同时播放数量已达上限。 |
| `version_identity_conflict` | 409 | 条目指向不同作品：外部 ID 或集数不一致。 |
| `version_item_busy` | 409 | 条目正在播放或处理中，请稍后再试。 |
| `version_merge_incompatible` | 409 | 条目无法合并：类型、媒体库或剧集不同，含有子条目，或该版本是唯一版本。 |
| `version_undo_unavailable` | 409 | 此操作已无法撤销，或须先撤销之后的操作。 |
| `web_playback_disabled` | 403 | 此会话不能播放媒体。 |
| `webhook_target_denied` | 400 | Webhook 地址必须是指向允许的公网主机的 HTTPS 地址。 |
<!-- error-codes:end -->
