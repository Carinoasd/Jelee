# 权限矩阵

本文按能力列出各角色能否调用对应的路由。表中“未登录”“普通用户”“分享访客”“管理员”“会话类型”五列，以及文末每条路由归属哪一行，都由守门测试 `TestPermissionMatrixDocumentMatchesRoutes`（`internal/adapter/http/permission_matrix_doc_test.go`）对照实现逐项核对（G49.8：文档与实现不一致即为缺陷）：

- **路由完整**：遍历开启全部功能（含兼容层与可开启开发者模式的实例）时注册的每条路由，必须恰好归属一行；归属块里不再对应任何路由的行同样报错。
- **未登录**：对每条路由真实发出不带凭据的请求。返回 401 的需要登录；只有环回地址能得到非 404 响应的记为“仅本机”。
- **管理员专属**：来自 OpenAPI 的 `x-jelee-role: administrator` 与泄漏遍历表 `leakRouteTable` 的 `leakAdmin` 分类；`TestPermissionMatrixAdministratorSourcesAgree` 要求两处一致。泄漏遍历测试 `TestAccessLeakHiddenContentPostgres` 在真实 PostgreSQL 上验证这些路由对普通用户在查询之前就返回 403。
- **分享访客**：来自 `internal/adapter/http/shares.go` 的 `guestRoutes`，其他路由一律 403 `share_forbidden`。
- **会话类型**：来自 OpenAPI 的 `x-jelee-session: native`；兼容层只接受原生会话（`internal/adapter/compat/router.go`）。

单元格以符号开头，后面括号里的文字是补充说明：`✓` 该角色可调用本行全部路由；`✗` 全部不可调用；`部分` 只能调用其中一部分；`仅本机` 只接受环回地址且不带转发标头的请求。会话类型：`任意` 网页会话与原生会话都可以；`原生` 只接受原生会话；`部分原生` 本行部分路由只接受原生会话；`—` 本行不需要登录。

“可调用”只表示通过了角色检查。能看到哪些媒体，还要再经过统一权限过滤器：媒体库授权、条目允许/拒绝规则、分级上限、标签屏蔽、网络规则、客户端管控限制的媒体库、分享范围（见[访问控制](access-control.md)与 [ADR 0003](adr/0003-unified-access-filter-in-sql.md)）。看不到的条目与不存在的条目返回相同结果，默认 404。

## 角色

| 角色 | 判定方式 | 说明 |
| --- | --- | --- |
| 未登录 | 请求不带 `Authorization: Bearer`，也不带会话 Cookie `__Host-jelee_session` | 只能使用公开路由。兼容层的凭据写法见 [API 参考](api-reference.md#认证) |
| 普通用户 | 已登录，`users.is_admin = false` | “本人”类路由（`/users/{id}/…`）只允许 `{id}` 是自己，否则 403 |
| 分享访客 | 通过 `POST /api/v1/shares/redeem` 或 `/redeem/native` 兑换分享令牌得到的会话（隐藏的访客账号，`users.share_id` 非空） | 只能使用 `guestRoutes` 列出的路由；只读分享拒绝写入（403 `share_read_only`）；兼容层把访客凭据当作未知凭据。每次访问按会话、路由、分钟写审计，审计写不进去时拒绝请求 |
| 管理员 | 已登录，`users.is_admin = true` | 管理员默认不受条目规则、分级、标签限制，可用 `restrict_admins` 打开限制（E12）；媒体库授权从不限制管理员 |
| 开发者模式 | 不是角色，而是实例状态：环境变量、配置文件与一次性令牌同时满足后开启的有时限会话 | 只改变下表“开发者模式下”一列所述的行为，**不提升任何角色的权限**；生产环境（`JELEE_ENV=production`）一律忽略。见[开发者模式](developer-mode.md) |

会话类型：网页登录（`POST /api/v1/auth/login`）发放网页会话，只能浏览不能播放；原生登录（`/auth/login/native`，需要管理员为该用户开启 `allowNative`）发放原生会话，可以直投播放。会话类型由服务端保存，客户端无法通过更换 User-Agent 改变。

## 矩阵

| 编号 | 能力 | 未登录 | 普通用户 | 分享访客 | 管理员 | 会话类型 | 开发者模式下 | 说明 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| P01 | 服务状态与 API 文档 | ✓ | ✓ | ✓ | ✓ | — | `/api/v1/system` 显示 `devMode` 与到期时间 | `/readyz` 只返回固定状态码，不含版本或地址 |
| P02 | 初始设置向导 | ✓（需一次性引导令牌） | ✓ | ✓ | ✓ | — | 无变化 | 除 `GET /api/v1/setup/status` 外都要 `X-Jelee-Setup-Token`；设置完成后一律 410（[ADR 0008](adr/0008-setup-bootstrap-token.md)） |
| P03 | 登录、二次验证、兑换分享令牌 | ✓ | ✓ | ✓ | ✓ | — | `relax_login_rate_limit` 放宽登录限速 | 共用登录限速与账号锁定；原生登录需管理员开启 `allowNative` |
| P04 | 当前会话：注销、轮换令牌、取 CSRF 令牌 | ✗ | ✓ | 部分（可注销与取 CSRF，不能轮换） | ✓ | 任意 | 无变化 | 网页会话的写请求必须带 `X-Jelee-CSRF` |
| P05 | 本人资料与界面偏好 | ✗ | ✓（只限本人） | 部分（只读本人资料与偏好） | ✓ | 任意 | 无变化 | `GET /api/v1/users/{id}` 允许本人或管理员 |
| P06 | 两步验证与应用密码 | ✗ | ✓（只限本人） | ✗ | ✓ | 任意 | 无变化 | 启用两步验证后，原生登录与兼容层必须使用应用密码 |
| P07 | 本人会话列表与撤销 | ✗ | ✓（只限本人） | ✗ | ✓（任意用户） | 任意 | 无变化 | — |
| P08 | 个人数据导出、清除播放历史、永久删除本人账号 | ✗ | ✓（只限本人） | ✗ | ✓ | 任意 | 无变化 | 见[用户数据权利](user-data-rights.md) |
| P09 | 用户管理 | ✗ | ✗ | ✗ | ✓ | 任意 | 无变化 | 创建、修改、停用、恢复、解锁、开启原生登录、播放限额、重置两步验证、永久删除、全部会话列表；不能移除最后一个管理员（409 `last_admin`） |
| P10 | 媒体库授权与内容规则 | ✗ | 部分（只能读本人的媒体库授权） | ✗ | ✓ | 任意 | `relax_permission_strict` 只让开启了 `restrict_admins` 的管理员恢复“看得到一切”，非管理员不受影响 | 分级上限、标签屏蔽、条目规则、网络规则、全局访问策略 |
| P11 | 浏览媒体库与条目 | ✗ | ✓ | ✓（限分享范围） | ✓ | 任意 | 同 P10 | 结果经统一权限过滤器 |
| P12 | 条目图片 | ✗ | ✓ | ✓（限分享范围） | ✓ | 任意 | 无变化 | 见[图片资产](image-assets.md) |
| P13 | 播放信息与原文件直投（流、外挂与内嵌字幕、音轨、附件、OCR 字幕） | ✗ | ✓ | ✓（需分享允许播放） | ✓ | 原生 | 无变化（`dev-transcode` 刻意不接） | 网页会话请求流返回 403 `web_playback_disabled`；不转码（[ADR 0007](adr/0007-no-encoder-direct-play-only.md)） |
| P14 | 播放上报、已播放标记、续播列表 | ✗ | ✓ | ✓（只读分享拒绝写入） | ✓ | 部分原生（播放上报只给原生会话） | 无变化 | 见[播放会话与进度](playback-progress.md) |
| P15 | 全站播放会话监控 | ✗ | ✗ | ✗ | ✓ | 任意 | 无变化 | — |
| P16 | 音轨与字幕偏好 | ✗ | ✓（只限本人） | ✗ | ✓ | 任意 | 无变化 | 用户默认偏好与条目级偏好 |
| P17 | 合集 | ✗ | 部分（只读） | ✗ | ✓ | 任意 | 无变化 | 维护合集只限管理员；没有可见成员的合集对非管理员视同不存在 |
| P18 | 播放清单 | ✗ | ✓（本人清单与他人公开清单） | ✗ | ✓ | 任意 | 无变化 | 见[合集与播放清单](collections-playlists.md) |
| P19 | 观看统计 | ✗ | 部分（只能读本人统计） | ✗ | ✓ | 任意 | 无变化 | 全站统计与导出只限管理员，见[观看统计](watch-statistics.md) |
| P20 | 条目元数据、NFO 应用、版本拆分与合并、重新探测 | ✗ | ✗ | ✗ | ✓ | 任意 | 无变化 | 见[条目元数据](item-metadata.md)、[条目版本](item-versions.md) |
| P21 | TMDB 候选搜索与预览 | ✗ | ✗ | ✗ | ✓ | 任意 | 无变化 | 只在配置 TMDB 密钥时注册 |
| P22 | 媒体库、扫描、排程、目录同步、NFO 策略、任务 | ✗ | ✗ | ✗ | ✓ | 任意 | `relax_permission_strict` 同 P10 | 见[任务 API](jobs-api.md) |
| P23 | 自愈修复 | ✗ | ✗ | ✗ | ✓ | 任意 | 无变化 | 见[修复](repair.md) |
| P24 | Webhook | ✗ | ✗ | ✗ | ✓ | 任意 | 无变化 | 见 [Webhook](webhooks.md) |
| P25 | 客户端管控 | ✗ | ✗ | ✗ | ✓ | 任意 | 无变化 | 见[客户端管控](client-control.md) |
| P26 | 分享链接管理 | ✗ | ✗ | ✗ | ✓ | 任意 | 无变化 | 只有管理员能创建分享，避免用户把自己的授权转给他人 |
| P27 | 当前分享信息 | ✗ | ✗ | ✓ | ✗ | 任意 | 无变化 | 非访客会话返回 404 |
| P28 | 站点外观与插件设置 | ✗ | 部分（只读已清理的外观与插件顺序） | 部分（同普通用户） | ✓ | 任意 | 无变化 | 原始 CSS 与配置只限管理员 |
| P29 | Prometheus 指标 | ✗ | ✗ | ✗ | ✓ | 任意 | 无变化 | 见[指标](metrics.md) |
| P30 | 申请开发者模式一次性令牌 | 仅本机 | 仅本机 | 仅本机 | 仅本机 | — | 只在可开启开发者模式的实例上注册 | 远程请求（含经过反向代理）一律 404 |
| P31 | 开发者模式状态与开关 | ✗ | ✗ | ✗ | ✓ | 任意 | 只在可开启开发者模式的实例上注册 | 危险开关需要 `"iUnderstand": true` |
| P32 | 运行时性能剖析（pprof） | ✗ | ✗ | ✗ | ✗ | — | 有开发者模式会话且开启 `debug_pprof` 时，环回请求或管理员可用 | 其他情况一律 404 |
| P33 | 兼容层公开接口 | ✓ | ✓ | ✓ | ✓ | — | 无变化 | 服务器公开信息、Ping、空的公开用户列表、用户名密码登录 |
| P34 | 兼容层（已登录） | ✗ | ✓ | ✗ | ✓ | 原生 | 无变化 | 比上游严格，见 [ADR 0004](adr/0004-compat-stricter-than-upstream.md) 与[兼容矩阵](compat-matrix.md)；`/compat/Playlists` 的写入只限清单拥有者（管理员也不例外），与 P18 相同 |

## 路由归属

每行格式为 `编号 方法 路径`。方法 `*` 表示任意方法，多个方法用逗号分隔；路径以 `**` 结尾表示前缀匹配。一条路由同时匹配多行时，精确路径优先，其次是较长的前缀。

```text permission-routes
P01 GET /healthz
P01 GET /readyz
P01 GET /api/v1/system
P01 GET /api-docs
P01 GET /api/v1/openapi.json
P02 * /api/v1/setup**
P03 POST /api/v1/auth/login
P03 POST /api/v1/auth/login/native
P03 POST /api/v1/auth/login/second-factor
P03 POST /api/v1/shares/redeem
P03 POST /api/v1/shares/redeem/native
P04 POST /api/v1/auth/logout
P04 POST /api/v1/auth/rotate
P04 GET /api/v1/auth/csrf
P05 GET /api/v1/users/me
P05 GET,PUT /api/v1/users/me/preferences
P05 PUT /api/v1/users/me/profile
P05 PUT /api/v1/users/me/password
P05 GET /api/v1/users/{id}
P06 * /api/v1/users/me/two-factor/**
P06 GET /api/v1/users/{id}/two-factor
P06 * /api/v1/users/me/app-passwords
P06 * /api/v1/users/{id}/app-passwords**
P07 * /api/v1/users/{id}/sessions**
P08 GET /api/v1/users/{id}/data-export
P08 POST /api/v1/users/me/purge
P08 DELETE /api/v1/users/me/playback-history
P09 GET,POST /api/v1/users
P09 PUT,DELETE /api/v1/users/{id}
P09 POST /api/v1/users/{id}/restore
P09 POST /api/v1/users/{id}/unlock
P09 PUT /api/v1/users/{id}/native
P09 * /api/v1/users/{id}/delivery-limits
P09 DELETE /api/v1/users/{id}/two-factor
P09 POST /api/v1/users/{id}/purge
P09 GET /api/v1/sessions
P10 * /api/v1/users/{id}/libraries
P10 * /api/v1/users/{id}/content-access**
P10 * /api/v1/access/**
P11 GET /api/v1/items
P11 GET /api/v1/items/{id}
P11 GET /api/v1/items/{id}/details
P11 GET /api/v1/items/{id}/sources
P12 GET,HEAD /images/{type}/{id}
P13 GET /api/v1/items/{id}/playback
P13 POST /api/v1/items/{id}/playback/check
P13 * /api/v1/sources/**
P14 POST /api/v1/playback/start
P14 POST /api/v1/playback/progress
P14 POST /api/v1/playback/stop
P14 PUT,DELETE /api/v1/items/{id}/played
P14 GET /api/v1/items/{id}/user-data
P14 GET /api/v1/users/me/resume
P15 GET /api/v1/playback/sessions
P16 GET,PUT /api/v1/items/{id}/track-preferences
P16 GET,PUT /api/v1/users/me/track-preferences
P17 * /api/v1/collections**
P18 * /api/v1/playlists**
P19 GET /api/v1/users/me/watch-stats
P19 GET /api/v1/users/{id}/watch-stats
P19 * /api/v1/watch-stats**
P20 * /api/v1/items/{id}/metadata**
P20 * /api/v1/items/{id}/versions**
P20 POST /api/v1/version-operations/{id}/undo
P20 POST /api/v1/items/{id}/probe/rebuild
P21 * /api/v1/metadata/tmdb/**
P22 * /api/v1/libraries**
P22 * /api/v1/jobs**
P23 * /api/v1/admin/repairs**
P24 * /api/v1/webhooks**
P25 * /api/v1/client-control/**
P26 GET,POST /api/v1/shares
P26 * /api/v1/shares/{id}**
P27 GET /api/v1/shares/current
P28 * /api/v1/site/**
P29 GET /metrics
P30 POST /api/v1/dev/token
P31 * /api/v1/dev**
P32 * /debug/pprof/**
P33 GET /compat/System/Info/Public
P33 GET,POST /compat/System/Ping
P33 GET /compat/Users/Public
P33 POST /compat/Users/AuthenticateByName
P34 * /compat/**
```

## 修改路由时

新增或修改路由时，除了更新 OpenAPI（`go run ./tools/openapi`）与 `leakRouteTable`，还要把路由归入上表某一行，必要时新增一行，然后运行：

```sh
go test -p 1 -count=1 -run 'TestPermissionMatrix|TestAccessLeakRouteTableIsComplete' ./internal/adapter/http/
```

测试不需要数据库。设置环境变量 `JELEE_PERMISSION_MATRIX_DUMP=1` 再运行 `TestPermissionMatrixDump`，会列出每条路由按实现推导出的各角色结果，便于编辑本文。
