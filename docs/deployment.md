# 基础服务容器

`Dockerfile` 构建三个静态 Go 可执行文件，运行镜像使用 scratch 和 UID/GID 65532，不包含 shell、转码工具或旧服务。构建阶段使用 Go 1.27.1-alpine3.24，固定镜像索引摘要 `sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414`，已记录于 `tools/manifest.json`。最终镜像包含项目 LICENSE、Go LICENSE/PATENTS 与 CA 证书。

当前只支持登记后读取原媒体；ffprobe、mkvtoolnix、mediainfo、字幕/NFO/图片处理尚未接入，因此 G37 的完整镜像要求仍未完成。

Compose 使用 PostgreSQL 16.15 精确镜像摘要、健康检查及先迁移再启服务。数据库卷在项目 `data/postgres`；媒体以只读卷挂载到 `/media`，宿主机必须给予 UID 65532 读取与父目录遍历权限。

设置 `JELEE_POSTGRES_PASSWORD`（建议随机十六进制，避免 URL 保留字符）、`JELEE_MEDIA_ROOT`（媒体目录）、`JELEE_ALLOWED_HOSTS` 后运行：

```sh
docker compose -f deploy/docker-compose.yml up --build -d
```

Compose 内部网络使用 sslmode=disable，仅用于此隔离网络；远程数据库应使用证书验证。HTTP 默认只向宿主环回发布。启用直投还需要 `JELEE_ENABLE_DIRECT=true` 与有效 native 会话，不能匿名访问媒体。令牌与媒体登记操作使用容器内 `/jelee-cli`。

## 本次验证

2026-09-30 在现有 WSL Docker 29.7.2 上构建了本地 `jelee/jelee:codex-foundation-test`，没有推送。首轮使用默认构建网络时，`proxy.golang.org` TLS 握手超时；使用 `docker build --network host --progress plain --tag jelee/jelee:codex-foundation-test .` 重试成功。该参数是本机网络问题的验证方式；部署运行仍按 Compose 的隔离网络配置。

- 镜像 ID：`sha256:d7f2da18e5590ecf616c46f04a6d3804a2f6ee64c6282f3747658fde855a7003`，Linux amd64，Docker 报告大小 24458613 字节。
- 配置确认 UID/GID 为 `65532:65532`，入口 `/jelee`，健康检查为 `/jelee-cli doctor`。
- 导出文件系统检查：三个程序均为 ELF64 且没有 PT_INTERP 动态解释器；没有 ffmpeg、ffprobe、shell、busybox 或 Go 工具链可执行文件。
- 项目 LICENSE 与源码逐字节一致，Go LICENSE/PATENTS 和 CA 证书存在。
- 无网络、只读根文件系统、移除全部 capabilities 并启用 no-new-privileges 时，CLI 用法检查返回预期退出码 2；缺少数据库配置时服务返回预期退出码 1。

原始构建与检查输出见 [container.txt](evidence/container.txt)。测试创建的检查容器已经移除；本地测试镜像保留。未启动 Compose、未创建部署数据库卷、未使用用户媒体目录；未执行该镜像连接数据库后的健康状态或流式端到端测试。反向代理、更新/回滚、多架构镜像与完整部署验收仍待完成。正式发布前须补齐媒体探测工具、完整工具清单、完整依赖许可清单、部署测试和安全加固。
