# 基础服务容器

`Dockerfile` 构建三个静态 Go 可执行文件，运行镜像使用 scratch 和 UID/GID 65532，不包含 shell、转码工具或旧服务。构建阶段使用 Go 1.27.1-alpine3.24，固定镜像索引摘要 `sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414`，已记录于 `tools/manifest.json`。最终镜像包含项目 LICENSE、Go LICENSE/PATENTS 与 CA 证书。

当前支持登记后读取原媒体、离线 NFO 只读校验，以及可配置的持久只读盘点任务。ffprobe、mkvtoolnix、mediainfo、字幕/图片处理、NFO 编辑写回与其批量任务尚未接入，因此 G37 的完整镜像要求仍未完成。第3A镜像与只读扫描实测见[任务验证](jobs-verification.md)，以下保留首阶段镜像历史。

Compose 使用 PostgreSQL 16.15 精确镜像摘要、健康检查及先迁移再启服务。数据库卷在项目 `data/postgres`；媒体以只读卷挂载到 `/media`，宿主机必须给予 UID 65532 读取与父目录遍历权限。

设置 `JELEE_POSTGRES_PASSWORD`（建议随机十六进制，避免 URL 保留字符）、`JELEE_MEDIA_ROOT`（媒体目录）、`JELEE_ALLOWED_HOSTS` 后运行：

```sh
docker compose -f deploy/docker-compose.yml up --build -d
```

Compose 内部网络使用 sslmode=disable，仅用于此隔离网络；远程数据库应使用证书验证。HTTP 默认只向宿主环回发布。启用直投还需要 `JELEE_ENABLE_DIRECT=true` 与有效 native 会话，不能匿名访问媒体。令牌与媒体登记操作使用容器内 `/jelee-cli`。

## 初始化（G18）

啟用帳號（`JELEE_ENABLE_ACCOUNTS=true`）的新安裝在完成初始引導前只開放引導 API、`/healthz`、`/readyz`、探索路由與前端外殼，其餘一律回 503 `setup_required`。`/healthz` 是存活檢查；`/readyz` 在資料庫可用時即回 200，並以 `data.setup` 標示 `required`／`completed`，因此等待引導的實例仍會接收流量。完整說明見[初始引導](setup-wizard.md)。

1. 先執行遷移（`jelee-migrate up`；Compose 的 `migrate` 服務已處理）。遷移 `000071` 會把已有使用者或媒體庫的既有部署直接標為已完成，升級不會被鎖。
2. 擇一完成引導：
   - **瀏覽器**：啟動服務後，從 `docker compose logs jelee`（標準錯誤）取得一次性引導權杖，或設定 `JELEE_SETUP_TOKEN_FILE=/絕對路徑` 讓服務以 0600 寫入該檔；開啟網頁介面輸入權杖並逐步完成。每次重啟都換新權杖。
   - **無頭**：在服務第一次啟動前執行 `jelee-cli setup --non-interactive --password-stdin --admin-name NAME [--library 名稱=/media/路徑]...`，密碼從標準輸入讀。已完成時以 `setup_already_completed` 結束（exit 1），可安全放在每次啟動前。例如 `printf '%s\n' "$ADMIN_PASSWORD" | docker compose run --rm -T --entrypoint /jelee-cli jelee setup --non-interactive --password-stdin --admin-name admin --library Movies=/media --accept-degraded-tools`。
3. 多實例部署請在引導期間只啟動一個實例或改用 CLI；完成後其他實例最多 1 秒內放行。

未啟用帳號的部署沒有引導，也不受閘門影響。

## 可選的執行時記憶體設定

在基礎檔後加入 `-f deploy/docker-compose.memory.yml`，可為 `jelee` 選用 `GOGC=100`、`GOMEMLIMIT=512MiB` 與容器 768 MiB 上限；三者均可覆寫，memory 與 memory+swap 上限保持相同，因此此設定不提供 swap。PostgreSQL 與遷移服務的預算另計。本輪固定混合負載的真容器驗收已通過，`GOGC=50` 比較組也通過；使用方式、實測數據與容量限制見[執行時記憶體設定](runtime-memory.md)。

## 媒體庫語言設定的升級

第19／20版新增以下偏好；當前版本要求乾淨schema29，見[lock-only NFO](nfo-lock-only.md)。先執行資料庫遷移，再啟動新的服務；001–019保持原樣。第19版新增媒體庫文字語言及更新版本，第20版新增有序圖片語言清單。設定有更新時降版會拒絕丟失偏好；介面與回復限制見[媒體庫語言](tmdb-library-language.md)及[圖片語言](tmdb-image-preferences.md)。以下仍是首階段容器的歷史驗證。

## 首階段容器验证

2026-09-30 至 2026-10-01 在现有 WSL Docker 29.7.2 上构建并验证本地 `jelee/jelee:codex-current-test`，源码提交为 `f21d15668477bd5806e7e525149bfb373d9a68bd`，没有推送。构建使用 `docker build --network host`；这仅用于处理本机下载网络问题，不能据此改变部署网络边界。

- 镜像 ID：`sha256:17c550a0d89547b8d33f22d74bb205a2653d07017d1b1152b3600faddfe60c16`，Linux amd64。
- 配置确认 UID/GID 为 `65532:65532`，入口 `/jelee`，健康检查为 `/jelee-cli doctor`。（2026-10-04 起改为 `/jelee-cli doctor --checks config,database,migrations`，范围与当时的 doctor 相同；完整 doctor 见[故障排查](troubleshooting.md)。）
- 导出文件系统检查：三个程序均为静态 ELF64，没有 PT_INTERP 或 PT_DYNAMIC；没有 ffmpeg、ffprobe、shell、busybox 或 Go 工具链可执行文件。
- 项目 LICENSE 与源码逐字节一致，Go LICENSE/PATENTS 和 CA 证书存在。
- 使用非 root、只读根文件系统与移除全部 capabilities 运行，连接专用 PostgreSQL 完成 up、doctor、会话创建和测试资源登记。
- NFO 校验成功且原文 SHA256 不变；native 原字节/Range、伪装 UA 的 web 403、转码 409、目录和 healthy 均通过。停止后为 `Running=false ExitCode=0 OOMKilled=false`，迁移 down 成功。

当前证据见[构建与运行日志](evidence/container-current.txt)和[静态镜像检查](evidence/container-current-inspection.txt)。[旧容器日志](evidence/container.txt)保留首轮下载超时、重试和早期镜像的历史记录。静态检查容器已移除；本地测试镜像保留。

未启动 Compose、未创建部署数据库卷、未使用用户媒体目录。HTTP 样本仅为 36 字节传输 fixture，不能证明真实影片可播放或第三方客户端兼容。反向代理、更新/回滚、多架构镜像与完整部署验收仍待完成。正式发布前须补齐媒体探测工具、完整工具清单、完整依赖许可清单及其余部署验证。

## 探索埠與防火牆

現有 Go 容器僅需發布設定的 HTTP 埠，PostgreSQL 保持內部網路；不發布 UDP 1900／7359，不需 SSDP 多播或路由器自動開埠。反向代理連至設定的 HTTP listener，客戶端自行輸入服務網址。防火牆僅允許實際使用的 HTTP／HTTPS 入口；不要為服務新增探索埠規則。

舊 C# 入口的伺服器 UDP 7359 探索 host 已從實作、啟動圖與探索回應模型移除，[驗證](server-discovery-removal.md)包含舊設定true時的正式host／OpenAPI驗收。其餘直播／調諧器 UDP socket factory仍在參考樹，尚未完成全部G05依賴移除或LAN SSDP封包驗收，不應將此階段當成所有舊網路能力都已刪除。

## 網路隱私與公開入口

公開位址、代理、Host、登入限流與HSTS的現況見[網路隱私](network-privacy.md)。客戶端直接連線所使用的公網IP無法對該客戶端隱藏；完整可信代理與公網部署驗收仍待完成。

第21版新增[人工元資料與欄位鎖](item-metadata.md)，保留已有001–020遷移。第21版有人工欄位狀態時降版拒刪；當前binary要求乾淨schema29。第24版新增項目NFO確認觀察，保留紀錄時也拒絕降版；001–023保持，先遷移再啟動。

第25版新增[獨立NFO欄位鎖](nfo-field-lock-intent.md)，缺值欄位亦受保護；001–024保持不變，保留任何獨立鎖時拒絕25→24降版。啟動新binary前先遷移。

第26版擴充[lock-only NFO投影](nfo-lock-only.md)；001–025保持，保留新投影鎖定證明時拒絕26→25降版，既有四文字鎖保持。

第27版新增[排序標題](nfo-sort-title.md)的文字、來源與鎖；001–026保持。含排序標題欄位或新版投影證明時拒絕27→26，人工清空也不能藉降版刪除。

第28版新增[四種文字欄位](nfo-text-fields.md)，九欄來源／獨立鎖／人工patch與同交易套用；001–027保持。保留新欄位或新版投影證明時拒絕28→27，人工空值也保護。

第29版新增[年份有型別保存](nfo-year-fact.md)，年份來源／鎖／人工null清除與文字同交易。001–028保持；保留年份資料或新版proof時拒絕29→28。

## 反向代理（G37.3）

参考配置：[`deploy/nginx/jelee.conf`](../deploy/nginx/jelee.conf)（nginx ≥ 1.25.1）与 [`deploy/caddy/Caddyfile`](../deploy/caddy/Caddyfile)（Caddy 2.7+）。**两份配置都只是参考，尚未在真实公网部署、真实客户端或真实影片上验收**；上线前必须在自己的环境按本节末尾的核对清单验证。

两份配置共同遵守的约定：

| 项目 | 做法 | 对应 Jelee 行为 |
| --- | --- | --- |
| Host | 只为配置的公开域名转发，并把该域名作为 `Host` 传给 Jelee；其他 Host／裸 IP 直接断开（nginx `444`、`ssl_reject_handshake`；Caddy `abort`、无证书） | `JELEE_ALLOWED_HOSTS` 必须包含该域名，否则返回 `invalid_host`（400） |
| 客户端地址 | 边缘代理用真实对端覆盖 `X-Forwarded-For`，并删除客户端带来的 `Forwarded`、`X-Real-IP`、`X-Forwarded-Host`、`X-Forwarded-Proto` | 只有命中 `JELEE_TRUSTED_PROXIES` 的对端才采用 XFF，见[可信代理](trusted-proxies.md) |
| Range／直投 | 原样转发 `Range`、`If-Range`；关闭响应缓冲（nginx `proxy_buffering off`、`proxy_max_temp_file_size 0`；Caddy `flush_interval -1`）；不缓存、不压缩 | Jelee 自己处理单段／多段 Range 与条件请求 |
| 超时 | 连接 5 秒；上游读取／客户端发送超过 Jelee 的 30 秒单次写入超时 | 暂停播放的客户端由 Jelee 断开后以 Range 续传 |
| 请求体 | 上限 4 MiB，略高于 Jelee 最大 JSON 请求体 2 MiB | 精确上限由 Jelee 各路由执行，超限返回 `body_too_large` |
| TLS／HSTS | TLS 1.2/1.3、HTTP/2，代理端加 `Strict-Transport-Security` | Jelee 只在直接 TLS 时发 HSTS |
| 隐私 | 隐藏代理版本与 `Server`／`Via`／`X-Powered-By`；`/metrics` 只允许本机访问；访问日志含客户端地址，需缩短保留期并限制读取权限 | Jelee 使用相对 URL，不依据转发头生成公开地址 |

`JELEE_TRUSTED_PROXIES` 必须与代理**连到 Jelee 时的源地址**一致：代理与 Jelee 同机且 Jelee 监听 `127.0.0.1:8097` 时为 `127.0.0.1/32,::1/128`；使用随附 Compose 时，Jelee 看到的通常是 Docker 网桥网关地址而不是 `127.0.0.1`，应填能匹配它的最小 CIDR。不要把客户端可以直连的网段列为可信。代理前面还有 CDN／负载均衡时，nginx 改用 `$proxy_add_x_forwarded_for` 并配置 `set_real_ip_from`，Caddy 配置全局 `trusted_proxies`。

本仓库内只做过以下离线检查：nginx 1.29.8 容器 `nginx -t` 与 Caddy 2.11.6 容器 `caddy validate`／`caddy fmt` 通过；在本机用替身上游（非 Jelee）确认 206 Range 透传、伪造 XFF 被替换、`Forwarded`／`X-Real-IP` 被删除、未知 Host 被断开。这不能代替真实部署验收。

上线核对清单：

1. 从外网请求 `https://域名/healthz` 返回 200；用错误 Host、裸 IP 请求不会到达 Jelee。
2. 对直投地址发送 `Range: bytes=0-1023` 与末尾区段请求，确认 206、`Content-Range` 正确；在真实客户端拖动进度条与长时间暂停后续播。
3. 播放大文件时观察代理所在主机的磁盘：不应出现代理临时文件增长。
4. 登录限流与帐号稽核记录的是客户端地址而不是代理地址；Jelee 日志没有 `untrusted_peer`／`invalid_chain` 告警。
5. 检查响应头、错误页与重定向中没有内网地址、上游端口或版本号；外网无法绕过代理直连 Jelee 端口。

## 升级、滚动升级与回滚（G37.4、G37.5）

每个 Jelee 版本只接受一个确切的 schema 版本（`internal/adapter/postgres` 的 `SchemaVersion`），新旧版本不能同时对同一数据库提供服务。因此：

- **需要迁移的升级不能零停机滚动。** 步骤：备份 PostgreSQL → 停止旧服务（Compose `stop_grace_period: 20s`，服务先停止接受连接，再排空请求与工作程序）→ 运行 `jelee-migrate up` → 启动新版本 → 等待 `/readyz` 返回 200 后再开放流量。
- **不改变 schema 的版本**可以蓝绿切换：在另一端口启动新实例，`/readyz` 为 200 后把代理上游改到新端口并平滑重载（`nginx -s reload` 或 `caddy reload`），再停止旧实例。旧实例上的长时间直投连接会在排空期限后断开，客户端需以 Range 续传。
- **回滚**：先停服务；如果升级执行过迁移，用旧版本对应的 `jelee-migrate down --i-understand` 逐级回退，或直接恢复升级前的备份。多个版本在保留新增数据时会拒绝降版（见上文各版本说明），此时只能恢复备份。
- `/healthz` 只表示进程存活；`/readyz` 会检查数据库等依赖，代理健康检查与切换判断应使用 `/readyz`。容器内置健康检查使用 `jelee-cli doctor`。

以上步骤由现有关闭与迁移行为推导，尚未做过完整的升级／回滚演练（G37 验收项仍待完成）。

## 故障排查（G37.5）

先运行 `make diag`（Windows：`scripts/make.ps1 diag`）。完整的 `jelee-cli diag export` 诊断包（G50.2）尚未实现，该入口目前依次执行 `jelee-cli doctor tools`、`doctor probe` 与 `doctor`，并在全部输出后汇总失败。

| 现象 | 优先检查 |
| --- | --- |
| 播放卡顿、无法拖动 | 代理是否关闭了响应缓冲、是否原样转发 `Range`；代理或 CDN 是否压缩／缓存媒体；`JELEE_MAX_STREAMS` 是否过小（返回 `stream_limit`，429）；是否触及每用户／每设备播放上限（`user_stream_limit`／`device_stream_limit`，429）或该用户被设置了带宽上限，见[直投限制](direct-delivery.md#撤销即断流并发播放与带宽上限g074g454)；客户端是否为 native 会话（web 会话播放返回 `web_playback_disabled`，403）。Jelee 不转码，客户端不支持的编码无法靠服务端解决 |
| 所有请求 400 | `invalid_host`：把代理传入的公开域名加入 `JELEE_ALLOWED_HOSTS` |
| 登录限流把所有用户算作同一人 | `JELEE_TRUSTED_PROXIES` 未覆盖代理实际源地址；日志出现 `untrusted_peer` 即为此原因 |
| 代理 502／504 | Jelee 是否在运行、代理连接的地址与 `JELEE_LISTEN` 是否一致；Compose 只向宿主环回发布 8097 |
| 扫描异常 | 用 `jelee-cli jobs` 查看任务状态与错误码；媒体卷需要 UID 65532 可读、父目录可遍历；详见[任务 API](jobs-api.md)与[文件清单扫描](scanning-filesystem.md) |
| 数据库连接失败 | `jelee-cli doctor` 与 `jelee-migrate status`；服务在 schema 不匹配时拒绝启动，先执行迁移；远程数据库使用证书验证的 `sslmode` |
| NFO 权限或读取失败 | NFO 目前只读，不需要写权限；用 `make nfo NFO_ROOT=<绝对根目录> NFO_FILE=<相对路径>` 离线验证单个文件；详见 [NFO 工作流程](nfo-worker.md) |
| 图片取得失败 | 本地图片见[本地海报](local-images.md)；外部抓取受 SSRF 策略限制，私网／环回地址会被拦截，见[出站请求盘点](outbound-request-audit.md)与 [TMDB 重试](tmdb-retry.md) |
