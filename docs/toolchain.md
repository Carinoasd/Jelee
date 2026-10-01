# 项目本地工具链

## 当前边界

本阶段实现 Go 1.27.1 的 Windows/Linux amd64、arm64 引导、校验、离线缓存与清理。版本与 SHA256 来自 [Go 官方下载元数据](https://go.dev/dl/?mode=json)，2026-09-30 核对；清单位于 `tools/manifest.json`。

显式执行 Docker 构建时，固定的 Go 构建镜像由已有 Docker daemon 管理缓存，独立记录在清单的 `containerBuildDependencies`；它不向宿主系统安装 Go，也不进入最终 scratch 运行镜像。构建证据见 `docs/deployment.md`。

G51 尚未全部完成。现有工具足以编译和测试本阶段 Go 代码，不代表媒体素材、浏览器、嵌入式 PostgreSQL 或全部静态扫描工具已经具备。完整品牌扫描仍会报告保留的旧服务端内部命名；`brand-scan-incremental` 只检查当前新增代码，不能代替完整验收。

## Windows

使用现有 PowerShell 7.2+ 和 Windows 自带 curl；不安装系统级软件。无需 make。

```powershell
pwsh -NoProfile -File scripts/bootstrap-tools.ps1
pwsh -NoProfile -File scripts/make.ps1 tools-verify
pwsh -NoProfile -File scripts/make.ps1 toolchain-test
pwsh -NoProfile -File scripts/make.ps1 lint
pwsh -NoProfile -File scripts/make.ps1 build
pwsh -NoProfile -File scripts/make.ps1 test
```

直接运行固定版本 Go：

```powershell
pwsh -NoProfile -File scripts/run-go.ps1 version
pwsh -NoProfile -File scripts/run-go.ps1 test -count=1 ./...
```

`.bin/go.cmd` 同样转发至该入口；包装脚本不会修改系统 PATH 或 shell 配置。所有路径均按绝对路径处理，支持空格与 Unicode。Go 或 Windows 的路径长度限制仍然适用；遇到明确的路径长度错误时，将仓库放在较短的路径后重新引导。

## Linux

使用现有 Python 3.9+、POSIX shell；Makefile 入口另需 GNU Make。引导使用 Python 标准库，不运行下载包中的安装脚本。

```sh
make bootstrap tools-verify toolchain-test
make lint build test
make test-race
```

无需 make 的入口：

```sh
sh scripts/bootstrap-tools
python3 scripts/toolchain.py verify
.bin/go test -count=1 ./...
```

`test-race` 还需要现有 C 编译器。引导脚本不会安装编译器。macOS 当前没有清单映射，脚本明确报错；尚未宣称支持。

## 下载、缓存与完整性

- 下载只允许 HTTPS，包括重定向；SHA256 不匹配立即失败并移除损坏缓存。
- Windows 使用 ZIP，Linux 使用 tar.gz；拒绝绝对路径、`..`、链接、设备文件与越界解压。解压到独立 staging 目录，成功后才放入正式安装路径。
- 解压条目数最多 100000，展开体积最多 2 GiB。ZIP 重复文件被拒绝，不覆盖已解压文件。
- 安装记录在 `.tools/.installed.json`，按平台记录版本、时间、压缩包哈希与主程序哈希。
- `tools-verify` 校验缓存压缩包、安装记录、`go version`、`go.mod` 版本，并将安装的 `go`、`gofmt` 与已校验压缩包中的对应字节比较。
- `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY` 使用宿主下载库支持的标准代理变量。`JELEE_TOOLS_MIRROR=https://mirror.example/tools` 可改下载前缀；文件名和清单 SHA256 保持固定。
- 离线重用：Windows `scripts/bootstrap-tools.ps1 -Offline`；Linux `sh scripts/bootstrap-tools --offline`。缺失缓存时明确失败。

Go 包装器将 `GOCACHE`、`GOPATH`、`GOMODCACHE`、`GOTMPDIR`、临时目录与 Go 配置放入 `.tools/cache/`；设置 `GOTOOLCHAIN=local`、`GOENV=off`。引导在该项目专用配置目录关闭 Go telemetry。Windows 在调用完成后恢复进程环境变量。不要直接调用 `.tools` 内的裸 Go 二进制，以免绕过这些设置。

## 命令

以下名称同时适用于 `make <目标>` 与 `pwsh -File scripts/make.ps1 <目标>`：

| 目标 | 行为 |
| --- | --- |
| `init` / `bootstrap` | 安装清单中的本地 Go |
| `tools-verify` | 校验固定版本与完整性 |
| `toolchain-test` | 校验和、恶意归档、边界测试 |
| `build` | 生成 `bin/jelee`、`bin/jelee-cli`、`bin/jelee-migrate`，Windows 带 `.exe` |
| `test` | `go test -count=1 ./...` |
| `test-race` | `go test -race -count=1 ./...`，需现有 C 编译器 |
| `test-integration` | 必须设置 `JELEE_TEST_DATABASE_URL`，执行 PostgreSQL Integration 测试 |
| `coverage` | 生成被忽略的 `coverage.out` |
| `fmt` / `fmt-check` | 格式化 / 检查 Go 源码 |
| `lint` | 格式检查与 `go vet` |
| `brand-scan` | 全仓库品牌门禁 |
| `brand-scan-incremental` | 新增代码品牌检查 |
| `gitignore-check` | 检查被跟踪的生成物与禁止文件 |
| `migrate` | 执行 `jelee-migrate up` |
| `doctor` | 执行 `jelee-cli doctor` |
| `tools-clean` | 删除 `.tools/`、`.bin/`、`.testfixtures/`、`.testdata/` |

`tools-clean` 只接受项目内固定目录，并拒绝链接。它会删除缓存和生成的测试数据；重建需重新引导。不会操作原始媒体目录。

## 被忽略的产物

`.gitignore` 覆盖 `.tools/`、`.bin/`、`.venv/`、`.cache/`、`tools/vendor-downloads/`、`.testfixtures/`、`.testdata/`、`bin/`、`dist/`、`node_modules/`、覆盖率、报告、测试数据库、运行数据、日志、环境密钥、IDE 与临时文件。新增下载工具必须先更新清单、许可证表与忽略清单；二进制例外需记录在 `docs/binary-allowlist.md`。

## 数据库测试

本机已有 Docker 29.7.2 与 PostgreSQL 16.15 测试镜像。清单记录镜像内容摘要；引导不会安装 Docker，也不会自动拉取镜像。集成测试必须使用隔离数据库，数据库 URL 不得指向用户生产实例。

无 Docker 时的项目本地嵌入式 PostgreSQL 引导尚未实现。数据库集成测试缺少配置时应报告 SKIP；专用 `test-integration` 入口在变量缺失时直接失败，不能把跳过记为通过。

## 构建测试依赖与运行依赖

Go、gofmt、vet、coverage 是构建测试工具，不随服务端产物分发。未来 ffmpeg 仅用于合成测试素材和开发调试，不得进入生产镜像或生产执行路径。允许的媒体运行依赖仅为探测用途的 ffprobe，以及按需启用的 mkvtoolnix、mediainfo；当前引导尚未安装这些工具。

## 尚未交付的 G51 子项

| 工具/能力 | 当前状态 |
| --- | --- |
| Go / gofmt / vet / coverage | 已固定并提供入口；尚未设置全项目覆盖率阈值 |
| 独立 golangci-lint、gofumpt、gosec、漏洞扫描 | 尚未固定、引导与接入 |
| 外部 migrate/Atlas CLI、sqlc | 尚未加入工具清单；当前项目通过 golang-migrate 库提供迁移命令 |
| OpenAPI 生成器、buf（如采用 protobuf） | 尚未加入工具清单 |
| Node LTS、包管理器、Playwright 浏览器 | 尚未加入工具清单 |
| ffmpeg/ffprobe、mkvtoolnix、mediainfo | 尚未加入工具清单 |
| 合成多轨媒体、章节、损坏素材、`make fixtures` | 尚未实现，不提供假成功入口 |
| Testcontainers / 嵌入式 PostgreSQL 回退 | 尚未实现；当前使用已有隔离测试容器 |
| 链接检查、shellcheck、actionlint | 尚未加入工具清单 |
| 完整工具链 CI 与 G51.15 全新克隆验收 | 未完成 |

`.github/workflows/jelee.yml` 运行 Windows/Linux 基础编译测试，并使用清单与 go.sum 哈希缓存工具链。独立 PostgreSQL job 使用清单中的固定镜像摘要，设置 `JELEE_REQUIRE_INTEGRATION=true`，运行数据库集成和 race 测试；固定 `ci-only` 密码仅用于该临时隔离服务。完整品牌门禁单独保留且会阻断残留命名，当前不能宣称 CI 全绿。该 CI 尚未满足 G51.14 的 fixtures 与完整工具链要求。
