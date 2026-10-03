# 当前安全模型与边界

## 身份与权限

新服务接受服务端生成的 Bearer 会话令牌。32 字节随机值编码为 token，数据库保存 SHA256 摘要。密码登录使用可配置 Argon2id，首次管理员经本地 stdin 初始化；令牌在登录、轮换或本地 provision 的成功结果中仅返回一次。默认 24 小时过期。数据库查询同时校验用户是否禁用、软删除，会话是否撤销及有效期。

会话的 `web` / `native` 类型来自数据库，不能由 User-Agent、转发头或请求参数升级。web 会话直投返回 `403 web_playback_disabled`；native 会话仍需库权限。native 令牌不是设备可信证明，任何持有者均能编写客户端使用，应按敏感凭据管理。

目录列表、详情和媒体解析通过参数化 SQL 限制到用户获准访问的库；管理员具有所有库访问权。无权直接访问条目、来源或图片默认返回 404（`JELEE_HIDDEN_CONTENT_STATUS=403` 或配置文件 `access.hiddenStatus` 可改为 403；不存在与不可见的 ID 始终得到相同响应，不暴露存在性）。`internal/adapter/http/access_leak_test.go` 以 `chi.Walk` 遍历全部已注册路由，对隐藏条目/库/来源/图片断言零泄漏；新增路由未登记即失败。打开媒体前再次查询有效会话和库权限。账户 API 支持库 ACL 替换及自己/管理员撤销会话，并在事务中重新核对操作者；尚无用户组、内容分级、目录/条目级规则、客户端策略编辑或 ACL 管理 UI。

## 原生设备登录与浏览器隔离（G07.4、G24.2）

native 会话可以直投，web 会话不能（G27.3）。`POST /api/v1/auth/login/native` 是第一个能用密码换取 native 会话的公开入口，因此必须保证浏览器页面无法经它拿到可播放的令牌，否则“网页不能播放”只剩前端自律：

- **只对管理员显式开启的用户签发**（`allowNative`，默认 false）。开关检查放在密码验证之后、同一用户行锁事务内，未认证者无法借此探测账户设置；撤回开关立即撤销该用户的 native 会话。
- **凡带 `Origin` 的请求一律 `403 forbidden`，`Sec-Fetch-Site`、`Sec-Fetch-Mode` 同样处理，且在读取正文和密码计算之前拒绝。** 理由：浏览器对每个跨源请求和每个 POST（含同源 fetch 与表单提交）都会附加 `Origin`，现代浏览器还会附加 Fetch Metadata 头；这些都是禁止由页面脚本设置或删除的请求头。原生客户端的 HTTP 栈不会发送它们。于是无论页面来自任何源（包括本站前端、被注入的脚本或第三方站点），都不能调用此入口；只靠 CORS 不够，因为 CORS 只限制读取响应，不阻止同源页面，而同源页面恰恰是 web 前端本身。服务端本就不返回任何 CORS 允许头，这一规则是在此之外的明确拒绝。
- **不设 Cookie、不发 csrf**：native 令牌只出现在响应正文；`authenticate` 本来就拒绝经 Cookie 出示的 native 令牌。
- **限制**：内嵌浏览器内核的“客户端”（Electron、WebView、以网页为界面的桌面播放器等）发出的请求同样带 `Origin`，无法使用此入口；这类客户端需由管理员用 `jelee-cli provision --native` 签发令牌，或等待后续第三方协议适配层。持有 native 令牌的人仍可自行编写任意客户端使用它；开关限制的是谁能用密码换取它，不是设备可信证明。
- 会话的 client、deviceId、version、设备名均为客户端自报标签；`lastSeenAt`/`lastIp` 是服务端观测值（同一会话 60 秒内最多写一次）。它们供会话列表与 G47.1 客户端识别使用，不参与授权判断。

## 网页会话 Cookie 与 CSRF（G35.1）

`POST /api/v1/auth/login` 在原有 JSON 之外，仅对 web 会话设置 `__Host-jelee_session` Cookie：HttpOnly、Secure、SameSite=Strict、Path=/、不设 Domain，Max-Age 等于会话剩余有效期。native 会话从不写入 Cookie。`authenticate` 只在请求没有 `Authorization` 头时读取该 Cookie；有头时完全按原 Bearer 规则处理，无效头不会回退到 Cookie。经 Cookie 认证的会话必须是数据库中的 `client_kind='web'`，native 令牌放进 Cookie 一律 401；同名 Cookie 出现多个、格式不符或会话已失效时返回 401 并下发过期 Cookie。Web 会话即使经 Cookie 访问直投仍得到 `403 web_playback_disabled`（G27.3）。

CSRF 令牌为 `HMAC-SHA256(key=SHA256(会话令牌), "csrf")`，即以数据库保存的令牌摘要为密钥，服务端每次按请求凭据重算，不新增存储或迁移；轮换后随新令牌改变，会话撤销后随之失效。登录与轮换响应的 `data.csrf` 返回该值，页面重载后可经 `GET /api/v1/auth/csrf` 取回。凡经 Cookie 认证的非安全方法（GET/HEAD/OPTIONS/TRACE 以外，含 POST/PUT/PATCH/DELETE）必须带唯一一个 `X-Jelee-CSRF` 头且常量时间比对一致，否则 `403 csrf_failed`，请求不会进入业务处理。Bearer 请求不需要 CSRF。登录本身不经 Cookie 认证，严格 JSON 的 `Content-Type: application/json` 要求使跨站表单无法直接提交。

登出、轮换、撤销当前会话、撤销自己全部会话、修改自己密码和删除自己账户成功后，若请求携带的 Cookie 正是被撤销的凭据，响应会清除（或在轮换时替换）Cookie；其他会话的 Cookie 不受影响。每个请求都重新查询会话行，撤销立即生效；残留 Cookie 下一次请求得到 401 并被清除。图片响应的 `Vary` 改为 `Authorization, Cookie`。

**开发环境不提供降级。** `__Host-` 前缀要求 Secure，服务端始终设置 Secure，没有任何开关去掉它或改名。理由：降级开关一旦被误配到生产就会让 Cookie 经明文传输，且 G11.5 默认只监听环回、经反向代理发布 TLS。浏览器把 `http://localhost`、`127.0.0.1` 和 `[::1]` 视为安全上下文，较新的 Chromium 系与 Firefox 会在这些环回源上接受 Secure 与 `__Host-` Cookie（尚待在目标浏览器上实测确认），因此本地开发（含 Vite 开发服务器在 `localhost` 代理 `/api`）可直接使用；不符合这一行为的浏览器（例如部分 Safari 版本）请在开发时使用 Bearer 或经本地 TLS 访问。非环回地址的明文 HTTP 下 Cookie 不会被浏览器保存，这是有意的失败方式。

## 前端静态文件与 CSP

`JELEE_WEB_DIR`（配置文件 `webDir`）为空时不提供前端，非 API 路径仍返回 JSON 404。设置时必须是干净的绝对路径，启动时目录必须可打开且含普通文件 `index.html`，否则拒绝启动；预期指向 `web/dist`。不使用 `go:embed`。

- 前端没有自己的路由：只有未被任何 API 路由匹配的 GET/HEAD、且路径不是 `/api` 或 `/api/` 开头时才由前端处理；其他方法与 `/api` 下的未知路径保持 JSON 404。与现有非 `/api` 路由（`/healthz`、`/readyz`、`/api-docs`、`/metrics`、`/images/...`）同名的前端路由会被 API 占用，前端不得使用这些路径。
- 每个请求经 `os.Root` 打开目录，拒绝绝对路径、`..` 与指向目录外的符号链接；此外 URL 中的 `.`/`..`/空段、以 `.` 开头的隐藏名、反斜杠、冒号与控制字符直接 404，不做“清洗后另找文件”。只服务普通文件。
- `assets/` 下带内容哈希的文件返回 `Cache-Control: public, max-age=31536000, immutable`；`index.html`（含回退）返回 `no-store`；其他文件 `no-cache`。`assets/` 下缺失的文件返回 404，不回退到 HTML；其余缺失路径与目录回退到 `index.html`，以支持深链。
- 仅前端响应使用 `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`；API 响应保持 `default-src 'none'; frame-ancestors 'none'; base-uri 'none'`。

`jelee-cli provision --admin` 与 `import-video` 使用本地数据库凭据，属于受信任运维入口。能读取这些凭据的操作员可以管理数据；不要把 CLI 访问权限授予普通客户端。

## 原文件与转码限制

媒体通过受根目录约束的文件打开方式读取，仅接受普通文件并在打开时处理逃逸风险。直投支持 Range/条件请求，输出原字节，不写回媒体，不运行 ffmpeg，不创建 HLS/DASH 或重封装。生产转码类请求返回 `409 transcode_disabled`。开发者模式目前不可用，尝试启用会拒绝启动。

流式并发数可配置且有上限；查询与写入超时有界，客户端断开会取消后续处理。该版本未完成所有性能预算验证。

系统移除官方显式下载能力，流响应不使用 `Content-Disposition: attachment`。**可播放媒体理论上可能被客户端录制；系统只能移除官方显式下载能力。** Bearer 令牌持有者接收到原字节后，服务端无法阻止其自行保存。

## 网络与错误

默认监听 `127.0.0.1:8097`，Host 必须匹配配置白名单。不依据 Host 或 X-Forwarded-* 构造公网地址或授予权限。反向代理、TLS 终止与生产网络拓扑仍需单独验证；当前不宣称完整代理信任规则已实现。

错误响应采用稳定 code 与 request/trace ID，不直接返回数据库错误、堆栈、媒体绝对路径或凭据。日志记录经过约束的字段；token、URL 查询串和认证头不作为 HTTP 日志内容。账户操作有事务审计，已保存用户语言优先于请求头；完整系统审计、日志轮换、OTel、指标与告警仍未完成。

## 默认拒绝与已知未完成项

没有有效 PostgreSQL 连接和 clean schema 2 时拒绝启动。账户、目录与直投开关默认关闭，直投要求目录同时开启。现有生产路径没有转码、下载归档、直播或后台媒体处理入口。

登录防护已有 IP/名称双维限速、持久锁定、固定成本的未知账户验证和有界密码工作。所有账户请求共用非阻塞准入限制（密码并发数的 4 倍，默认 8 个），满时返回 503，避免无界等待。详情与限制见[账户 API](accounts-api.md)和[密码安全](password-security.md)。分布式登录防护、可疑登录通知、CORS 管理（Cookie 会话的 CSRF 见上文）、多因素认证、完整诊断脱敏和灾难恢复尚未完成。此阶段应在隔离环境评估，不应据部分安全回归结果宣称全部 G00–G51 已满足。

工具下载与解压规则见 `docs/toolchain.md`。原媒体、NFO 与图片不应交给清理脚本；`tools-clean` 只处理专门的生成目录。

## 網路地址與代理信任

參見[網路隱私與公開入口](network-privacy.md)，包含目前傳輸對端限流與容器／宿主监听邊界。
