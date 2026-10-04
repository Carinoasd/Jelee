# 第 2 阶段：账户、会话与库权限 API

账户字段由 schema 2 引入，当前 binary 要求 schema 4 clean，并显式设置 `JELEE_ENABLE_ACCOUNTS=true`。关闭开关后账户路由返回 404；已有目录和直投开关独立控制。先按[本地账户初始化](account-bootstrap.md)设置管理员密码，再通过 HTTP 登录。首次初始化没有公网接口。

## 请求和响应

- 路由前缀 `/api/v1`；除登录外均要求 `Authorization: Bearer <token>`，或（仅 web 会话）登录时下发的 `__Host-jelee_session` Cookie。经 Cookie 认证的 POST/PUT/PATCH/DELETE 必须带 `X-Jelee-CSRF`，否则 `403 csrf_failed`；Bearer 不需要。规则见[安全模型](security-model.md#网页会话-cookie-与-csrfg351)。
- JSON 请求必须是单一对象，`Content-Type: application/json`，上限 64 KiB。未知字段、null 值、重复键、尾随 JSON、非法 UTF-8、未知或重复查询参数会被拒绝。无参数的 POST 仍发送 `{}`；DELETE 正文必须为空。
- 成功返回 `{"data": ...}`；无返回值的修改返回 204。错误沿用 `error.code/message/details/traceId`；详情不包含底层错误、SQL、密码或令牌。全部响应 `Cache-Control: no-store`。
- 名称、显示名、设备名上限均为 128 UTF-8 字节，不允许控制字符。名称不得为空或有首尾空格；数据库名称不区分大小写且唯一。locale 仅支持 `zh-CN/zh-TW/ja-JP/en-US`。
- 已登录用户保存的 locale 优先于 `Accept-Language`；未认证请求按请求头协商，无请求头默认简中。
- 所有权限操作在数据库事务内重新检查有效会话、账户状态和角色；HTTP 管理员预检不能替代数据库检查。

完整请求/响应 schema 随功能开关发布于 `/api/v1/openapi.json`。

| 方法与路径 | 权限 | 请求与结果 |
| --- | --- | --- |
| POST `/auth/login` | 公开 | `name,password,deviceName?`；返回 user、session、token、csrf；只能签发 web 会话，并设置 HttpOnly 会话 Cookie |
| POST `/auth/login/native` | 公开（仅非浏览器） | `name,password,client,deviceId,device?,version?`；返回 user、session、token；仅当管理员为该用户开启 `allowNative` 时签发可直投的 native 会话，否则在密码验证通过后返回 `403 native_login_disabled`。不设 Cookie、不返回 csrf；带 `Origin`、`Sec-Fetch-Site` 或 `Sec-Fetch-Mode` 的请求一律 `403 forbidden`。详见[原生设备登录](#原生设备登录g074g242) |
| POST `/auth/logout` | 自己 | `{}`；撤销当前会话；请求携带的会话 Cookie 被清除 |
| POST `/auth/rotate` | 自己 | `deviceName?`；原子撤销旧令牌并返回新令牌，保留服务端会话类型；web 会话同时替换 Cookie 并返回新 csrf |
| GET `/auth/csrf` | 自己 | 返回当前凭据对应的 `csrf`，供页面重载后取回 |
| GET `/users/me` | 自己 | 当前用户资料 |
| PUT `/users/me/profile` | 自己 | `displayName,locale,hidden`；不能修改角色或密码 |
| GET/PUT `/users/me/preferences` | 自己 | 界面偏好 `theme`（system/light/dark）、`density`（comfortable/compact，预留）；从未保存时读到默认值；PUT 必须带齐全部字段（迁移 000073 `user_preferences`）；只影响显示，不写审计、不进元数据备份，降级时直接丢弃 |
| PUT `/users/me/password` | 自己 | `oldPassword,newPassword`；成功后撤销所有会话，需重新登录；旧密码错误返回 `400 invalid_password`（会话仍有效，与会话失效的 401 区分） |
| GET `/users` | 管理员 | `cursor?,limit=1..100,includeDeleted=true/false`；data.users 与 data.pagination |
| POST `/users` | 管理员 | `name,password,displayName?,locale?,hidden?,admin?,disabled?`；要求 Idempotency-Key；201 或回放 200 |
| GET `/users/{id}` | 自己或管理员 | 用户资料；普通用户无全体用户发现接口 |
| PUT `/users/{id}` | 管理员 | 完整替换 `name,displayName,locale,hidden,admin,disabled`；改角色或禁用时撤销会话 |
| DELETE `/users/{id}` | 管理员 | 软删除并撤销会话 |
| POST `/users/{id}/restore` | 管理员 | `{}`；恢复资料，原令牌保持撤销 |
| POST `/users/{id}/unlock` | 管理员 | `{}`；重置失败计数和锁定时间 |
| PUT `/users/{id}/native` | 管理员 | `{"allowNative":true}` 或 `false`；开启或撤回原生设备登录；撤回时同时撤销该用户全部有效 native 会话；写审计 `user.native_access_changed`；值未变时不写审计 |
| GET `/users/{id}/delivery-limits` | 管理员 | 该用户的直投覆写值；省略的字段表示跟随全局设置 |
| PUT `/users/{id}/delivery-limits` | 管理员 | `{"maxStreams":0..128,"maxKbps":0..10000000}`，两项皆可省略；省略即恢复跟随全局，`0` 表示该用户不受此项限制；只影响之后开始的串流；写审计 `user.delivery_limits_changed`（前后值含 null）；值未变时不写审计。详见[直投限制](direct-delivery.md#撤销即断流并发播放与带宽上限g074g454) |
| GET `/sessions` | 管理员 | 全部用户的有效会话；`cursor?,limit=1..100`，按会话 ID 游标分页；data.sessions 与 data.pagination |
| GET `/users/{id}/sessions` | 自己或管理员 | 有效会话列表；返回会话 ID、类型、设备名、client/deviceId/version、最后使用时间与地址，不返回令牌 |
| DELETE `/users/{id}/sessions` | 自己或管理员 | 撤销目标全部会话 |
| DELETE `/users/{id}/sessions/{sessionID}` | 自己或管理员 | 撤销目标单个会话 |
| GET `/users/{id}/libraries` | 自己或管理员 | 显式库授权列表；管理员实际仍可访问全部库 |
| PUT `/users/{id}/libraries` | 管理员 | `{"libraryIds":[UUID,...]}`；原子替换，空数组清除，最多 1000 个唯一 ID |
| GET/PUT `/users/{id}/content-access` | 管理员 | 分级上限、未分级覆写、封锁标签（PUT 整组替换，`blockedTags` 必填）；GET 另含条目规则；写审计 `user.content_access_changed`。详见[存取控制](access-control.md) |
| PUT/DELETE `/users/{id}/content-access/items/{itemId}` | 管理员 | `{"effect":"allow"\|"hide"}`；条目及其子树的显式规则；写审计 `user.item_access_rule_set`／`user.item_access_rule_removed` |
| GET/PUT `/access/policy`、GET `/access/parental-ratings` | 管理员 | 全域策略 `restrictAdmins`、`blockUnrated`（写审计 `access.policy_changed`）；可辨识分级代码表 |

PUT 中遗漏的可选字符串/布尔字段会重置为空/false；它不是 PATCH。创建用户默认 locale 为 `zh-CN`。会话及库授权读取最多返回 1000 条，超量明确返回 409，避免悄悄截断管理结果。用户列表采用 UUID 游标，每页最多 100 条。

`Idempotency-Key` 为 1–128 个可打印 ASCII 非空白字符，按操作者隔离。相同操作者与 key 始终返回第一次成功创建的用户，即使之后提供不同正文，也不会重设密码；每次回放仍需有效管理员会话和合规请求。首次 201，回放 200 并带 `Idempotency-Replayed: true`。密钥不保存密码指纹。

## 登录防护与密码

新密码必须为 12–1024 UTF-8 字节；不裁剪、不规范化、不要求固定字符类别。Argon2id 成本、并发预算与取消边界见[密码安全](password-security.md)。数据库仅存 PHC 密码哈希和 SHA256 会话令牌摘要。登录与轮换的令牌仅在当前成功响应中返回。

默认同 IP 每分钟 60 次、同名称每分钟 10 次；内存最多 10000 个维度桶，容量满时拒绝新桶，返回 429 与 Retry-After。仅使用 TCP 对端 IP，忽略转发头。多个反向代理用户会共享代理 IP 的预算；限速当前只作用于单进程，重启会清空。账户失败计数和锁定持久保存在 PostgreSQL，默认连续 5 次失败锁定 15 分钟。未知、密码错误、禁用、软删除和锁定登录统一返回 401；未知和旧无密码账户仍执行正常成本的虚拟验证。

全部账户路由的同时处理请求数限制为密码并发数的 4 倍，默认 8 个，包含读取正文和等待密码计算的请求。准入时不排队；满时返回 `503 account_busy` 与 `Retry-After: 1`。这使密码工作队列有固定上限，请求期限同时限制等待时间；已经开始的 Argon2 计算仍需完成后才能返回取消。

改密验证另有独立的 IP/用户 ID 限速表，沿用相同额度和容量上限。它采用认证后的不可变用户 ID，改名或伪造转发头不能绕过。错误旧密码返回 `400 invalid_password` 而非 401：会话本身有效，客户端不应把它当作登出；每次尝试（无论对错）照旧先消耗改密限速表，额度与 429 行为不变。错误旧密码不增加登录锁定计数，也不消耗登录限速表，避免持有被盗会话的人阻止合法用户登录并撤销该会话。响应写入同样有期限，停止读取响应的客户端不能无限占用账户名额。

默认每个用户最多 8 个有效会话、24 小时有效期。达到上限返回 `429 session_limit`；这与并发播放预算是不同限制。并发登录在用户行锁内计数。密码验证在数据库事务外运行，签发令牌前以密码哈希与版本再次比较；改密、禁用、删除和角色变化不会让过时验证结果绕过新状态。最后一名启用管理员不能被禁用、降权或删除，并发修改使用事务锁保护。

账户管理、授权修改、已知账户失败登录与会话变化写入 PostgreSQL 审计表，包含可用的操作者、目标、IP、时间和安全前后值；不写密码、哈希或令牌。设备名称是用户提供的标签，不是可信设备认证。

## 原生设备登录（G07.4、G24.2）

选择独立路由 `POST /api/v1/auth/login/native`，而不是在 `/auth/login` 加参数：web 登录契约（Cookie、csrf、浏览器调用）保持原样，原生路由的“拒绝浏览器、不发 Cookie”规则也不会与之混在一起。两者共用同一张登录限速表（IP 与名称两个维度）、同一套失败计数与锁定、同一套审计；换路由不会多出尝试次数。

- **权限**：用户字段 `allowNative`（schema 63 起 `users.allow_native`）默认 false，包括管理员本人；只能由管理员经 `PUT /users/{id}/native` 修改，`PUT /users/{id}` 不接受也不重置它。未开启时，密码正确的原生登录返回 `403 native_login_disabled` 并写安全类审计 `login.native_denied`，不签发任何会话、不改失败计数；密码错误、未知账户、锁定等仍统一返回 401，因此未认证的调用者无法探知某账户是否开启了原生登录。撤回权限会立即撤销该用户的全部有效 native 会话（含 CLI `provision --native` 签发的），web 会话不受影响。`jelee-cli provision --native` 是受信任的本地运维入口，行为不变，不检查此开关。
- **请求字段**：`client`（客户端名，必填，1–128 字节）、`deviceId`（设备标识，必填，1–256 字节）、`device`（设备名，选填，≤128 字节，存为会话的 `deviceName`）、`version`（客户端版本，选填，≤64 字节）。全部要求合法 UTF-8、不得含控制字符；必填项不得有首尾空格。超限或非法在密码计算前返回 400。这些值是客户端自报的标签，用于会话列表与后续 G47.1 客户端识别规则，不是设备可信证明。
- **响应**：`data.token` 是唯一凭据；native 凭据从不进入 Cookie，也没有 csrf。轮换（`/auth/rotate`）保留会话类型与 client/deviceId/version，只允许更换设备名。
- **会话使用记录**：每次认证成功时记录 `lastSeenAt` 与 `lastIp`（按可信代理设置得出的客户端地址），同一会话 60 秒内最多写一次；记录失败或会话行正被账户事务锁住时跳过，不影响请求本身。

## 迁移、回滚与剩余范围

000001–000003 已发布迁移保持不变。000002 添加账户字段、大小写唯一索引、会话元数据、审计字段与幂等键表。旧库若存在只差大小写的名称，迁移失败并保留原名，不自动合并用户；应由操作员先处理冲突，再按 dirty 状态恢复流程处理。当前 binary 只接受 clean schema 4；升级和回滚都必须配合相同 schema 的 binary，见[快取回滚](probe-cache.md#升级与回滚)。

关闭账户开关并不撤销已经签发的会话。正常回退优先关闭功能开关；数据库 down 会丢失密码、资料、软删除信息、锁定计数、设备标签、扩展审计与幂等记录，并将软删除用户保留为禁用。需要恢复这些字段时必须使用备份。切勿把 down 当作无损操作。

schema 63（`000063_native_session_devices`）为 users 增加 `allow_native`，为 sessions 增加可空且有长度/字符约束的 `device_id`、`client_name`、`client_version`、`last_seen_at`、`last_ip`。down 删除这些列：原生登录权限随之收回（失败即关闭），已签发的 native 会话在 schema 62 下仍按 CLI 签发的 native 会话一样有效且可撤销，只丢失客户端标签与最后使用记录；再次 up 后所有用户的 `allowNative` 回到 false。

schema 64（`000064_user_delivery_limits`）为 users 增加可空的 `max_streams`（0–128）与 `max_kbps`（0–10000000）。NULL 表示跟随全局设置。down 删除这两列，所有用户回到全局限制；再次 up 后覆写值均为 NULL，需要时重新设置。

本阶段未实现头像、内容分级、可疑登录通知、永久删除与个人数据导出、管理 UI、MFA、分布式限速及完整安全验收。web 会话依旧禁止播放；第三方原生客户端协议适配在后续阶段完成。
