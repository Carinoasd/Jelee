# 第 2 阶段：账户、会话与库权限 API

使用 schema 2，并显式设置 `JELEE_ENABLE_ACCOUNTS=true`。关闭开关后账户路由返回 404；已有目录和直投开关独立控制。先按[本地账户初始化](account-bootstrap.md)设置管理员密码，再通过 HTTP 登录。首次初始化没有公网接口。

## 请求和响应

- 路由前缀 `/api/v1`；除登录外均要求 `Authorization: Bearer <token>`。
- JSON 请求必须是单一对象，`Content-Type: application/json`，上限 64 KiB。未知字段、null 值、重复键、尾随 JSON、非法 UTF-8、未知或重复查询参数会被拒绝。无参数的 POST 仍发送 `{}`；DELETE 正文必须为空。
- 成功返回 `{"data": ...}`；无返回值的修改返回 204。错误沿用 `error.code/message/details/traceId`；详情不包含底层错误、SQL、密码或令牌。全部响应 `Cache-Control: no-store`。
- 名称、显示名、设备名上限均为 128 UTF-8 字节，不允许控制字符。名称不得为空或有首尾空格；数据库名称不区分大小写且唯一。locale 仅支持 `zh-CN/zh-TW/ja-JP/en-US`。
- 已登录用户保存的 locale 优先于 `Accept-Language`；未认证请求按请求头协商，无请求头默认简中。
- 所有权限操作在数据库事务内重新检查有效会话、账户状态和角色；HTTP 管理员预检不能替代数据库检查。

完整请求/响应 schema 随功能开关发布于 `/api/v1/openapi.json`。

| 方法与路径 | 权限 | 请求与结果 |
| --- | --- | --- |
| POST `/auth/login` | 公开 | `name,password,deviceName?`；返回 user、session、token；只能签发 web 会话 |
| POST `/auth/logout` | 自己 | `{}`；撤销当前会话 |
| POST `/auth/rotate` | 自己 | `deviceName?`；原子撤销旧令牌并返回新令牌，保留服务端会话类型 |
| GET `/users/me` | 自己 | 当前用户资料 |
| PUT `/users/me/profile` | 自己 | `displayName,locale,hidden`；不能修改角色或密码 |
| PUT `/users/me/password` | 自己 | `oldPassword,newPassword`；成功后撤销所有会话，需重新登录 |
| GET `/users` | 管理员 | `cursor?,limit=1..100,includeDeleted=true/false`；data.users 与 data.pagination |
| POST `/users` | 管理员 | `name,password,displayName?,locale?,hidden?,admin?,disabled?`；要求 Idempotency-Key；201 或回放 200 |
| GET `/users/{id}` | 自己或管理员 | 用户资料；普通用户无全体用户发现接口 |
| PUT `/users/{id}` | 管理员 | 完整替换 `name,displayName,locale,hidden,admin,disabled`；改角色或禁用时撤销会话 |
| DELETE `/users/{id}` | 管理员 | 软删除并撤销会话 |
| POST `/users/{id}/restore` | 管理员 | `{}`；恢复资料，原令牌保持撤销 |
| POST `/users/{id}/unlock` | 管理员 | `{}`；重置失败计数和锁定时间 |
| GET `/users/{id}/sessions` | 自己或管理员 | 有效会话列表；返回会话 ID 和设备标签，不返回令牌 |
| DELETE `/users/{id}/sessions` | 自己或管理员 | 撤销目标全部会话 |
| DELETE `/users/{id}/sessions/{sessionID}` | 自己或管理员 | 撤销目标单个会话 |
| GET `/users/{id}/libraries` | 自己或管理员 | 显式库授权列表；管理员实际仍可访问全部库 |
| PUT `/users/{id}/libraries` | 管理员 | `{"libraryIds":[UUID,...]}`；原子替换，空数组清除，最多 1000 个唯一 ID |

PUT 中遗漏的可选字符串/布尔字段会重置为空/false；它不是 PATCH。创建用户默认 locale 为 `zh-CN`。会话及库授权读取最多返回 1000 条，超量明确返回 409，避免悄悄截断管理结果。用户列表采用 UUID 游标，每页最多 100 条。

`Idempotency-Key` 为 1–128 个可打印 ASCII 非空白字符，按操作者隔离。相同操作者与 key 始终返回第一次成功创建的用户，即使之后提供不同正文，也不会重设密码；每次回放仍需有效管理员会话和合规请求。首次 201，回放 200 并带 `Idempotency-Replayed: true`。密钥不保存密码指纹。

## 登录防护与密码

新密码必须为 12–1024 UTF-8 字节；不裁剪、不规范化、不要求固定字符类别。Argon2id 成本、并发预算与取消边界见[密码安全](password-security.md)。数据库仅存 PHC 密码哈希和 SHA256 会话令牌摘要。登录与轮换的令牌仅在当前成功响应中返回。

默认同 IP 每分钟 60 次、同名称每分钟 10 次；内存最多 10000 个维度桶，容量满时拒绝新桶，返回 429 与 Retry-After。仅使用 TCP 对端 IP，忽略转发头。多个反向代理用户会共享代理 IP 的预算；限速当前只作用于单进程，重启会清空。账户失败计数和锁定持久保存在 PostgreSQL，默认连续 5 次失败锁定 15 分钟。未知、密码错误、禁用、软删除和锁定登录统一返回 401；未知和旧无密码账户仍执行正常成本的虚拟验证。

全部账户路由的同时处理请求数限制为密码并发数的 4 倍，默认 8 个，包含读取正文和等待密码计算的请求。准入时不排队；满时返回 `503 account_busy` 与 `Retry-After: 1`。这使密码工作队列有固定上限，请求期限同时限制等待时间；已经开始的 Argon2 计算仍需完成后才能返回取消。

改密验证另有独立的 IP/用户 ID 限速表，沿用相同额度和容量上限。它采用认证后的不可变用户 ID，改名或伪造转发头不能绕过。错误旧密码不增加登录锁定计数，也不消耗登录限速表，避免持有被盗会话的人阻止合法用户登录并撤销该会话。响应写入同样有期限，停止读取响应的客户端不能无限占用账户名额。

默认每个用户最多 8 个有效会话、24 小时有效期。达到上限返回 `429 session_limit`；这与并发播放预算是不同限制。并发登录在用户行锁内计数。密码验证在数据库事务外运行，签发令牌前以密码哈希与版本再次比较；改密、禁用、删除和角色变化不会让过时验证结果绕过新状态。最后一名启用管理员不能被禁用、降权或删除，并发修改使用事务锁保护。

账户管理、授权修改、已知账户失败登录与会话变化写入 PostgreSQL 审计表，包含可用的操作者、目标、IP、时间和安全前后值；不写密码、哈希或令牌。设备名称是用户提供的标签，不是可信设备认证。

## 迁移、回滚与剩余范围

000001 已发布迁移保持不变。000002 添加账户字段、大小写唯一索引、会话元数据、审计字段与幂等键表。旧库若存在只差大小写的名称，迁移失败并保留原名，不自动合并用户；应由操作员先处理冲突，再按 dirty 状态恢复流程处理。schema 2 二进制遇到 schema 1 会拒绝启动。

关闭账户开关并不撤销已经签发的会话。正常回退优先关闭功能开关；数据库 down 会丢失密码、资料、软删除信息、锁定计数、设备标签、扩展审计与幂等记录，并将软删除用户保留为禁用。需要恢复这些字段时必须使用备份。切勿把 down 当作无损操作。

本阶段未实现头像、内容分级、可疑登录通知、用户/设备带宽与播放并发预算、永久删除与个人数据导出、管理 UI、MFA、分布式限速及完整安全验收。web 会话依旧禁止播放；第三方原生客户端协议适配在后续阶段完成。
