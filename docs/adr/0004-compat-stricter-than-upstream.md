# ADR 0004：兼容层 `/compat` 比上游更严格（隐私优先）

- 状态：已采纳（E11，2026-10-05）
- 日期：2026-10-06（追记）
- 相关需求：G24（第三方客户端兼容）、G48.3（不暴露存在性）、G27.3（网页会话不能播放）
- 相关代码：`internal/adapter/compat/`（`router.go`、`auth.go`、`users.go`、`images.go`、`library.go`）、`internal/adapter/http/server.go` 的 `newCompat`
- 逐项对照：[兼容矩阵](../compat-matrix.md)

## 背景

兼容层的目标是让现有第三方客户端能够浏览和直投播放，而不是复刻上游的全部行为。上游有几处行为会暴露账号或媒体的存在，或者允许匿名读取。[验证清单](../owner-verification-queue.md) E11 记录了这个取舍：若某个客户端依赖上游的宽松行为，是否接受不兼容。

## 决定

兼容层维持比上游严格的行为，隐私优先。具体包括（均见兼容矩阵）：

1. `GET /Users/{id}` 只允许本人或管理员，读取他人一律 403，不论该账号是否存在。
2. `GET /Users/Public` 固定返回空列表，不向未认证的调用者公开账号名。
3. 带 `Origin` 的请求一律 403，从不发送 CORS 标头，浏览器页面无法借用原生凭据。
4. 只接受原生会话；网页会话、Bearer、Cookie、分享访客令牌一律 401。
5. 条目图片需要登录（上游允许匿名），缓存头一律 `private`；条目 ID 不能当作访问凭据。
6. `{userId}` 形式的路由必须是本人或管理员，并在任何目录查询之前判定。
7. 转码相关字段一律为 false，没有可直投的媒体源时返回 `NoCompatibleStream`，不提供转码地址（见 [ADR 0007](0007-no-encoder-direct-play-only.md)）。
8. 错误响应比照上游生产环境用空响应体或固定文字，从不回传异常、路径或 SQL；登录不回传 `RemoteEndPoint`。
9. `/Items` 的 `Limit` 最大 500；开启两步验证的账号用密码登录返回 403 `app_password_required`，需改用应用密码。

## 理由

- 公开账号列表等于把登录流程刻意不确认的账号名交给任何人，削弱了限速与锁定的意义。
- 允许读取他人资料或匿名读取图片，会暴露账号和条目的存在，与 G48.3“不暴露存在性”冲突。
- 兼容层与原生 API 共用会话、权限过滤器和直投处理器（[ADR 0003](0003-unified-access-filter-in-sql.md)），宽松的兼容层会成为绕过原生 API 限制的旁路。

## 代价

- 依赖上游宽松行为的客户端可能出现功能缺失：例如登录页不显示用户头像列表、未带凭据请求图片的客户端显示不出图。兼容层尚未经过真实客户端验收（G24.5），遇到具体客户端问题时逐项评估，但不会放宽上述隐私相关的行为。

## 守门

- `internal/adapter/compat/router_test.go`：`TestCORSIsRefused`、`TestAuthentication`、`TestResponsesPublishNoAddressesOrPaths`。
- `internal/adapter/compat/users_test.go`：`TestUserByID`、`TestPublicUsersIsEmpty`。
- 真实 PostgreSQL：`TestCompatUsersPostgres`、`TestCompatImagesPostgres`、`TestCompatSessionKindsPostgres`；兼容层路由同样在 `leakRouteTable` 中分类并参与零泄漏遍历。
