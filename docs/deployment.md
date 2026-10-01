# 基础服务容器

`Dockerfile` 构建三个静态 Go 可执行文件，运行镜像使用 scratch 和 UID/GID 65532，不包含 shell、转码工具或旧服务。构建阶段使用 Go 1.27.1-alpine3.24，固定镜像索引摘要 `sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414`，已记录于 `tools/manifest.json`。最终镜像包含项目 LICENSE、Go LICENSE/PATENTS 与 CA 证书。

当前支持登记后读取原媒体、离线 NFO 只读校验，以及可配置的持久只读盘点任务。ffprobe、mkvtoolnix、mediainfo、字幕/图片处理、NFO 编辑写回与其批量任务尚未接入，因此 G37 的完整镜像要求仍未完成。第3A镜像与只读扫描实测见[任务验证](jobs-verification.md)，以下保留首阶段镜像历史。

Compose 使用 PostgreSQL 16.15 精确镜像摘要、健康检查及先迁移再启服务。数据库卷在项目 `data/postgres`；媒体以只读卷挂载到 `/media`，宿主机必须给予 UID 65532 读取与父目录遍历权限。

设置 `JELEE_POSTGRES_PASSWORD`（建议随机十六进制，避免 URL 保留字符）、`JELEE_MEDIA_ROOT`（媒体目录）、`JELEE_ALLOWED_HOSTS` 后运行：

```sh
docker compose -f deploy/docker-compose.yml up --build -d
```

Compose 内部网络使用 sslmode=disable，仅用于此隔离网络；远程数据库应使用证书验证。HTTP 默认只向宿主环回发布。启用直投还需要 `JELEE_ENABLE_DIRECT=true` 与有效 native 会话，不能匿名访问媒体。令牌与媒体登记操作使用容器内 `/jelee-cli`。

## 媒體庫語言設定的升級

目前版本要求乾淨schema20。先執行資料庫遷移，再啟動新的服務；001–019保持原樣。第19版新增媒體庫文字語言及更新版本，第20版新增有序圖片語言清單。設定有更新時降版會拒絕丟失偏好；介面與回復限制見[媒體庫語言](tmdb-library-language.md)及[圖片語言](tmdb-image-preferences.md)。以下仍是首階段容器的歷史驗證。

## 首階段容器验证

2026-09-30 至 2026-10-01 在现有 WSL Docker 29.7.2 上构建并验证本地 `jelee/jelee:codex-current-test`，源码提交为 `f21d15668477bd5806e7e525149bfb373d9a68bd`，没有推送。构建使用 `docker build --network host`；这仅用于处理本机下载网络问题，不能据此改变部署网络边界。

- 镜像 ID：`sha256:17c550a0d89547b8d33f22d74bb205a2653d07017d1b1152b3600faddfe60c16`，Linux amd64。
- 配置确认 UID/GID 为 `65532:65532`，入口 `/jelee`，健康检查为 `/jelee-cli doctor`。
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
