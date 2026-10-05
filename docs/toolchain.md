# 项目本地工具链

## 当前边界

本阶段实现 Go 1.27.1 的 Windows/Linux amd64、arm64 引导、校验、离线缓存与清理。版本与 SHA256 来自 [Go 官方下载元数据](https://go.dev/dl/?mode=json)，2026-09-30 核对；清单位于 `tools/manifest.json`。

3B1 另提供显式选择的 Windows/Linux amd64 媒体开发工具。普通 `bootstrap-tools` 仍只引导 Go；媒体压缩包约为 Windows 115 MB、Linux 151 MB，必须另行运行下面的 `bootstrap-media-tools`。3C1 另有显式选择的 Linux amd64 实验运行库入口，见下文；默认 `probe=false`，工具安装本身不启用探测。

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

`test-race` 还需要现有 C 编译器。引导脚本不会安装编译器。本机 WSL Ubuntu 26.04 的 GCC `15.2.0-16ubuntu1`、其 cc1/collect2 和 binutils `2.46-3ubuntu2` 的 ld.bfd/as 已按实际 ELF 哈希、包来源和许可记录在清单的 `existingHostDependencies`。这是既有宿主的精确盘点，不是引导脚本可安装的跨机器工具包。其他宿主需要单独盘点自身编译器。macOS 当前没有清单映射，脚本明确报错；尚未宣称支持。

2026-10-01 的编译器登记晚于此前已执行的 Linux race 测试；历史结果不改写为 manifest-first。最终 Linux race 复验应在本次登记之后执行，结果另见阶段验收证据。Windows race 编译器仍不可用，普通测试不能计为 race 通过。详见 `docs/evidence/host-compiler.txt`。

## Go 下载、缓存与完整性

- 下载只允许 HTTPS，包括重定向；SHA256 不匹配立即失败并移除损坏缓存。
- Windows 使用 ZIP，Linux 使用 tar.gz；拒绝绝对路径、`..`、链接、设备文件与越界解压。解压到独立 staging 目录，成功后才放入正式安装路径。
- 解压条目数最多 100000，展开体积最多 2 GiB。ZIP 重复文件被拒绝，不覆盖已解压文件。
- 安装记录在 `.tools/.installed.json`，按平台记录版本、时间、压缩包哈希与主程序哈希。
- `tools-verify` 校验缓存压缩包、安装记录、`go version`、`go.mod` 版本，并将安装的 `go`、`gofmt` 与已校验压缩包中的对应字节比较。
- `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY` 使用宿主下载库支持的标准代理变量。`JELEE_TOOLS_MIRROR=https://mirror.example/tools` 可改下载前缀；文件名和清单 SHA256 保持固定。
- 离线重用：Windows `scripts/bootstrap-tools.ps1 -Offline`；Linux `sh scripts/bootstrap-tools --offline`。缺失缓存时明确失败。

Go 包装器将 `GOCACHE`、`GOPATH`、`GOMODCACHE`、`GOTMPDIR`、临时目录与 Go 配置放入 `.tools/cache/`；设置 `GOTOOLCHAIN=local`、`GOENV=off`。引导在该项目专用配置目录关闭 Go telemetry。Windows 在调用完成后恢复进程环境变量。不要直接调用 `.tools` 内的裸 Go 二进制，以免绕过这些设置。

## Node 与前端工具（15.1）

`tools/manifest.json` 的 `node` 条目固定 Node 24.21.0（24.x Active LTS，内含 npm 11.19.0），记录 Linux/Windows amd64、arm64 四个官方压缩包的 HTTPS URL 与 SHA256。哈希取自官方 `SHASUMS256.txt`；2026-10-04 另以 `gpgv` 对照 Node 发布者公钥环验证该文件签名（签名者指纹记录在清单），引导时只强制比对固定的 SHA256。`playwright` 条目见下方「Playwright」一节。

Linux `make bootstrap`（`sh scripts/bootstrap-tools`）先装 Go，再装 Node；`--tool node` 或 `--tool go` 可只处理其一，`--offline` 只用 `.tools/downloads/` 缓存。Node 压缩包是 tar.xz，沿用同一个安全解压器：只有清单 `skippedLinks` 列出的三个符号链接（`bin/npm`、`bin/npx`、`bin/corepack`，名称与目标都必须完全相符）被略过不建立，其他链接或特殊文件一律使整次解压失败。安装到 `.tools/node/<版本>/<平台>/`，`bin/node` 与 npm CLI 的字节和已校验压缩包逐一比对。记录写在 `.tools/.installed.json` 的 `tools.node.<平台>`，原本 Go 使用的 `platforms` 结构不变。

`.bin/node`、`.bin/npm`、`.bin/npx` 是生成的 POSIX 包装脚本：直接执行固定版本的 node，并把 npm cache 放在 `.tools/npm-cache`、prefix 放在 `.tools/npm-prefix`，使用不存在的项目内 user/global config，因此不读取也不写入使用者的 `~/.npmrc` 或全局目录；`PATH` 前置 `.bin`，让 npm script 内的 `node`/`npm` 也落在固定版本。`tools-verify` 会检查压缩包、安装记录、node/npm 版本与包装脚本内容。

根目录 `.npmrc` 设 `ignore-scripts=true`、`engine-strict=true`、`save-exact=true`；`package.json` 的 `engines` 限定 Node `>=24.21.0 <25`。依赖版本精确固定在 `package.json`，完整解析结果锁在根目录 `package-lock.json`（npm workspace，成员为 `web/`）。

| 目标 | 行为 |
| --- | --- |
| `web-install` | `npm ci --ignore-scripts`（只依 lockfile） |
| `web-types` | OpenAPI 型别过期检查 + `vue-tsc --noEmit`（应用与 Node 设定两个 tsconfig） |
| `web-lint` | ESLint（零警告）+ i18n 检查 + 禁播门禁 |
| `web-test` | `vitest run` |
| `web-build` | `vite build`，再以 `--require-dist` 扫描产物 |
| `web-e2e` / `web-visual` / `web-visual-update` | Playwright 端到端、视觉回归比对、经人工确认后更新基线（见下方「Playwright」） |

Windows：`scripts/bootstrap-tools.ps1` 目前仍只安装 Go；清单已记录 Windows Node 压缩包与哈希（`bootstrapStatus` 字段注明），PowerShell 安装流程与 `make.ps1` 的 web 目标留待后续，CI 的前端门禁目前只在 Linux 执行。

## Playwright（G27.4、G34.5、G34.6）

`tools/manifest.json` 的 `playwright` 条目（`status: active`）固定 Playwright 1.63.0：npm 开发依赖 `@playwright/test` 精确版本写在 `web/package.json`，`playwright`、`playwright-core` 由 `package-lock.json` 锁定（integrity 哈希），清单 `npmPackages` 记录三者版本。浏览器只用 `playwright-core` 的 `browsers.json` 指定的 Chrome Headless Shell（修订 1243，Chrome for Testing 153.0.8010.12）：清单记录 linux-amd64、linux-arm64、windows-amd64 三个压缩包的 HTTPS URL（Playwright 自己的 `cdn.playwright.dev`，另记 Google 存储桶镜像地址）与 SHA256。两个来源都不发布校验和，2026-10-04 从两处各下载一次、哈希相同后固定（见 [THIRD-PARTY-TOOLS](THIRD-PARTY-TOOLS.md)）。

- 下载是选用的（约 120 MB 压缩、270 MB 解压）：`make bootstrap` 不含它，`make bootstrap-playwright`（`scripts/toolchain.py bootstrap --tool playwright`）才下载；沿用同一 HTTPS 下载与 SHA256 校验，ZIP 由 `safe_extract_zip` 解压（拒绝链接、绝对路径、`..`、超过 2 GiB／100000 项），先进 staging 目录。安装到 `.tools/playwright/<版本>/<平台>/chromium_headless_shell-<修订>/`，即 Playwright 期望的目录布局（含 `INSTALLATION_COMPLETE` 标记）；下载缓存文件名带版本与修订（`downloadName`）。
- `make playwright-verify` 校验压缩包、安装记录（`.tools/.installed.json` 的 `tools.playwright.<平台>`）、执行档与 `LICENSE.headless_shell` 与压缩包逐字节相同，并核对 `web/package.json`、`package-lock.json` 与已安装 `playwright-core` 的 `browsers.json` 都和清单一致——升级 Playwright 时三处与清单必须一起改。
- `web/scripts/run-playwright.mjs` 是唯一入口：在 Playwright 载入前把 `PLAYWRIGHT_BROWSERS_PATH` 设为清单的 `.tools/playwright/<版本>/<平台>`，并设 `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1`、`PLAYWRIGHT_SKIP_BROWSER_GC=1`，因此不读取、不下载到也不清理使用者的 `~/.cache/ms-playwright`。浏览器缺失时提示执行 `make bootstrap-playwright`。
- 只选 Chromium：Firefox 压缩包约 90 MB、字体渲染与 Chromium 不同，两套基线会让图片数量翻倍；G34.6 要求的是同一浏览器下的亮暗对比，跨浏览器兼容不在本项要求内，需要时再加清单条目。完整 Chromium（headed）、WebKit 与 Playwright 的 ffmpeg（录影）都不下载，配置固定 `video: "off"`。
- 只在 Linux 执行（amd64；arm64 已有固定哈希）；Windows 哈希已记录（`bootstrapStatus`），但 `bootstrap-tools.ps1` 不装浏览器，`make.ps1` 也没有这些目标。
- 禁播门禁：Playwright 与其依赖不在播放器名单内，`check-no-playback.mjs` 未触发，门禁规则未改；`web/scripts/checks.test.ts` 断言实际 lockfile 无违规。

| 目标 | 行为 |
| --- | --- |
| `bootstrap-playwright` | 下载、校验、安装固定的 Chrome Headless Shell |
| `playwright-verify` | 校验压缩包、安装记录、执行档与 npm 版本一致 |
| `web-e2e` | `playwright test --project=e2e`：键盘主流程、两段确认、所有关键页面无播放与可及性检查 |
| `web-visual` | 关键页面亮／暗 × 桌面／手机截图与 `web/e2e/__screenshots__` 比对，只比对不写入 |
| `web-visual-update` | `--update-snapshots=changed`：只改写有差异或缺少的基线；提交前必须人工查看新图 |

测试先以 `vite build` 构建到 `.testdata/playwright/dist`，再用 `vite preview` 提供；API 全部由 `web/e2e/fixtures/api.ts` 拦截回答，不需要 Jelee 服务或数据库，预览服务器的 `/api` 代理指向不可连的端口，漏网请求不会碰到本机正在运行的服务。结果与差异图在 `.testdata/playwright/results`；CI 失败时上传为构件。设计与基线规则见 [前端 ADR](frontend-adr.md#端對端與視覺回歸g274g345g346)。

## golangci-lint（G30.1）

`tools/manifest.json` 的 `golangci-lint` 条目固定 2.14.0（官方构建，`golangci-lint version` 显示以 go1.27.0 编译），记录 Linux/Windows amd64、arm64 四个[官方 GitHub release](https://github.com/golangci/golangci-lint/releases/tag/v2.14.0) 压缩包的 HTTPS URL 与 SHA256。哈希取自同一 release 的 `golangci-lint-2.14.0-checksums.txt`；2026-10-04 另下载 linux-amd64 与 windows-amd64 压缩包重算，结果一致。许可证 GPL-3.0，仅作开发与 CI 工具执行，不链接、不随 Jelee 分发（见 [THIRD-PARTY-TOOLS](THIRD-PARTY-TOOLS.md)）。

- Linux：`make bootstrap` 在 Go、Node 之后安装（`--tool golangci-lint` 可单独处理）；沿用同一 HTTPS 下载、SHA256 与安全解压器，安装到 `.tools/golangci-lint/<版本>/<平台>/`，执行档与 `LICENSE` 和已校验压缩包逐字节比对，记录在 `.tools/.installed.json` 的 `tools.golangci-lint.<平台>`。`.bin/golangci-lint` 包装脚本经 `scripts/toolchain.py golangci-lint` 执行。
- Windows：`scripts/bootstrap-tools.ps1` 在 Go 之后安装 ZIP（同一 `Expand-SafeZip`）；`scripts/run-golangci-lint.ps1` 为执行入口，`tools-verify.ps1` 校验压缩包、安装记录、执行档、`LICENSE` 与版本输出。
- 两平台都以固定 Go 的环境执行：`PATH` 前置固定 `GOROOT/bin`（golangci-lint 通过它调用 `go list`），`GOCACHE`／`GOMODCACHE`／`GOPATH` 等沿用 `.tools/cache/`，`GOLANGCI_LINT_CACHE` 为 `.tools/cache/golangci-lint`；不写入使用者家目录或全局配置，Windows 调用后恢复进程环境变量。
- `tools-verify` 一并校验 golangci-lint；`toolchain-test` 覆盖坏缓存清除、清单布局篡改与环境隔离。

配置、基线与覆盖率／基准门禁的规则见 [质量门禁](quality-gates.md)。

## 命令

以下名称同时适用于 `make <目标>` 与 `pwsh -File scripts/make.ps1 <目标>`：

| 目标 | 行为 |
| --- | --- |
| `init` / `bootstrap` | 安装清单中的本地 Go、Node 与 golangci-lint（Linux；Windows 为 Go 与 golangci-lint） |
| `tools-verify` | 校验 Go、Node（Linux）与 golangci-lint 的固定版本与完整性 |
| `toolchain-test` | 校验和、恶意归档、边界测试 |
| `build` | 生成 `bin/jelee`、`bin/jelee-cli`、`bin/jelee-migrate`，Windows 带 `.exe` |
| `test` | `go test -count=1 ./...` |
| `test-race` | `go test -race -count=1 ./...`，需现有 C 编译器 |
| `test-integration` | 必须设置 `JELEE_TEST_DATABASE_URL`，执行 PostgreSQL Integration 测试 |
| `coverage` | 生成被忽略的 `coverage.out` |
| `fmt` / `fmt-check` | 格式化 / 检查 Go 源码 |
| `lint` | 格式检查、OpenAPI 检查、`doc-check`、`go vet` 与 `golangci-lint` 门禁 |
| `golangci-lint` | 以 `.golangci.yml` 执行 golangci-lint，再以 `tools/lintgate` 对照 `tools/lint-baseline/<goos>.json`；基线外的新问题或已修复仍留在基线的条目都失败 |
| `lint-baseline-prune` | 仅 Linux：对 linux 与 windows（交叉检查）重跑并从基线移除已修复条目，从不新增 |
| `coverage-check` | 仅 Linux：按 `tools/coverage-thresholds.json` 测量核心／关键包覆盖率，低于棘轮最低值失败 |
| `coverage-ratchet` | 仅 Linux：补测试后把最低值提高到实测值（向下取整），从不降低 |
| `bench-compare` | 仅 Linux：`BENCH_BASE_REF=<提交>`，同机交替执行基准提交与工作树的热路径基准并以 benchgate 判定 |
| `quality-gates-test` | lintgate、covergate、benchgate 单元测试（Linux 另含 bench-compare 脚本测试） |
| `brand-scan` | 全仓库品牌门禁 |
| `brand-scan-incremental` | 新增代码品牌检查 |
| `gitignore-check` | 检查被跟踪的生成物与禁止文件 |
| `migrate` | 执行 `jelee-migrate up` |
| `doctor` | 执行 `jelee-cli doctor` |
| `doc-check` | 离线文档门禁：`README.md` 与 `docs/**/*.md` 的相对链接／锚点、外部链接格式（不联网）、文档中带 HTTP 状态的错误码与错误码表一致 |
| `dev` | 以 `go run ./cmd/jelee` 启动开发服务；必须先设置 `JELEE_DATABASE_URL`（或 `_FILE`／`JELEE_CONFIG`）并执行 `migrate` |
| `nfo` | 不需数据库。设置 `NFO_ROOT`（绝对根目录）与 `NFO_FILE`（相对路径）时只读验证该文件，否则运行离线 NFO 测试 |
| `diag` | 执行 `jelee-cli diag export`，输出到 `DIAG_OUT`（默认 `.testdata/jelee-diag.zip`，已存在时拒绝覆盖） |
| `tools-clean` | 删除 `.tools/`、`.bin/`、`.testfixtures/`、`.testdata/` |

`doc-check` 的实现在 `tools/doccheck`，只用标准库。错误码只检查明确声明为 HTTP 错误的写法（如 `` `job_busy`/409 ``、`HTTP 501，`…、JSON `"code":"…"`），需求原文与追溯表不检查；`go run ./tools/doccheck -undocumented` 另列出尚无文档提及的错误码（仅提示）。目前没有已知坏链接；若将来需要分批清理，可用 `-update-baseline` 生成 `tools/doccheck/baseline.txt`，门禁只容许清单内的旧问题，已修复但仍留在清单的条目也会失败。

`tools-clean` 只接受项目内固定目录，并拒绝链接。它会删除缓存和生成的测试数据；重建需重新引导。不会操作原始媒体目录。

## 被忽略的产物

`.gitignore` 覆盖 `.tools/`、`.bin/`、`.venv/`、`.cache/`、`tools/vendor-downloads/`、`.testfixtures/`、`.testdata/`、`bin/`、`dist/`、`node_modules/`、覆盖率、报告、测试数据库、运行数据、日志、环境密钥、IDE 与临时文件。新增下载工具必须先更新清单、许可证表与忽略清单；二进制例外需记录在 `docs/binary-allowlist.md`。

## 数据库测试

本机已有 Docker 29.7.2 与 PostgreSQL 16.15 测试镜像。清单记录镜像内容摘要；引导不会安装 Docker，也不会自动拉取镜像。集成测试必须使用隔离数据库，数据库 URL 不得指向用户生产实例。

无 Docker 时的项目本地嵌入式 PostgreSQL 引导尚未实现。数据库集成测试缺少配置时应报告 SKIP；专用 `test-integration` 入口在变量缺失时直接失败，不能把跳过记为通过。

## 可选媒体开发工具（3B1）

平台仅支持 amd64。Windows 需要 PowerShell 7.2+、curl 和 Windows 10+；Linux 需要已有 Python 3.9+、glibc 2.28+ 与 Linux 4.18+。Linux 发行包静态链接 libav 等依赖，但仍依赖 glibc；放入 scratch 镜像时须另提供完整、已验证的动态库。供应商与完整版本分别固定，不能把两套构建统称为同一二进制版本：

| 平台 | 供应商版本 | 上游来源 |
| --- | --- | --- |
| Windows amd64 | `9.0.2-essentials_build-www.gyan.dev` | Gyan tag `9.0.2`，FFmpeg `946fcce07b6dcd0331c8cc609192aeff5e1924f8` |
| Linux amd64 | `n9.0.2-17-g2a571b6068-20260930` | BtbN tag `autobuild-2026-09-30-13-08`，FFmpeg `2a571b606854520cf89804d8030c8b328e621689`，即 9.0.2 后 release/9.0 分支的 17 次提交 |

```powershell
pwsh -NoProfile -File scripts/bootstrap-media-tools.ps1
pwsh -NoProfile -File scripts/media-tools-verify.ps1
pwsh -NoProfile -File scripts/test-media-tools.ps1
pwsh -NoProfile -File scripts/run-media-tool.ps1 -Tool ffmpeg -version
pwsh -NoProfile -File scripts/run-media-tool.ps1 -Tool ffprobe -version
# 已有固定压缩包时，完全离线复验安装：
pwsh -NoProfile -File scripts/bootstrap-media-tools.ps1 -Offline
```

```sh
sh scripts/bootstrap-media-tools
python3 -B scripts/media-tools.py verify
python3 -B scripts/test_media_tools.py
.bin/ffmpeg -version
.bin/ffprobe -version
sh scripts/bootstrap-media-tools --offline
```

Windows 的 `.bin/ffmpeg.cmd` 和 `.bin/ffprobe.cmd` 也可调用；含复杂引号的参数优先直接使用 PowerShell 入口。脚本自行读取最前面的 `-Tool`，后续 `-v`、`-f` 等由原生程序解释，不绑定到 PowerShell 公共参数。

- 下载前必须存在 manifest。记录精确 tag、核心源码与构建脚本修订、HTTPS URL、压缩包大小与 SHA256、两个可执行文件 SHA256、许可文件 SHA256。压缩包 SHA256 同时对照供应商校验清单与 GitHub release asset digest；可执行文件哈希从已验证的压缩包读取，写入 manifest 后才首次执行。
- 校验和、版本、许可缺失或被修改均拒绝执行，不回退系统 PATH。引导发现缓存压缩包的 SHA256 或大小不符时，按 G51.4 删除该项目内已确认路径的坏包；下载中的 partial 也会清理。不会自行替换或重新认可已修改的安装文件。安装目录受损时应先审查原因，再移除对应项目内媒体安装目录后重新引导。
- ZIP/tar.xz 使用 Go 引导共用的安全解压器，拒绝越界、绝对路径、链接、设备和重复文件，最多 100000 条目和展开 2 GiB。每次用唯一空 staging 目录；压缩包、执行文件、许可证与版本全部通过后才发布安装目录。操作系统文件锁拒绝同平台并发安装，进程退出会释放锁。
- 安装目录为 `.tools/media/<platform>/<vendor-pin>/`；独立记录为 `.tools/media-installed/<platform>.json`。下载仍位于 `.tools/downloads/`。不同平台的记录互不覆盖，旧 Go 安装记录格式不变。
- 完整 `media-tools-verify` 同时检查缓存压缩包、安装记录、文件 SHA256 与完整版本。每次包装器调用检查记录、所选可执行文件、许可哈希与版本；不会为每次调用重新读取整个压缩包。
- 固定 `-version` 诊断有 10 秒期限，stdout/stderr 各在读取时限制为 64 KiB，超限或超时关闭子进程并等待回收。子进程临时目录与 XDG 配置位于项目 `.tools/cache/media/`；去除 `FFREPORT` 和 `LD_*`，避免环境自动报告及动态加载器注入。
- 包装器以绝对文件路径、独立参数列表启动工具，没有 shell 拼接。这是显式开发工具入口：正常媒体命令仍按调用者参数读取和写入文件，不提供生产进程隔离、媒体输入沙箱或任意媒体命令超时。
- HTTPS 镜像、代理规则沿用 Go 入口；媒体镜像不允许 URL 内凭证、查询或 fragment。离线缺包、哈希错误、版本错误均失败。供应商未来删除旧资产时，应保留已验证缓存或使用同哈希的受控 HTTPS 镜像，不能改用浮动 `latest`。

安全测试覆盖离线缺包/坏包、安装记录伪造、二进制与许可证篡改、版本不匹配、实时输出洪泛、超时、并发锁及原生参数传递。Python 的合成 tar.xz 用例另验证恶意归档和符号链接父路径；共用 ZIP 解压器由 `test-toolchain.ps1` 验证。测试只使用项目内临时目录。

## 构建测试依赖与运行依赖

Go、gofmt、vet、coverage 是构建测试工具，不随服务端产物分发。ffmpeg 仅用于合成测试素材和开发调试，不得进入生产镜像或生产执行路径。清单中的 `productionAllowed` 是后续分发策略声明，媒體工具中只有 ffprobe 為 true。其他媒體運行依賴僅為按需啟用的 mkvtoolnix（mkvmerge、mkvextract 為 true；mkvpropedit 為 false，只固定與校驗，不進映像）與 mediainfo，已依 E4 固定在 `matroskaTools`，以 `make bootstrap-matroska` 或 `scripts/toolchain.py bootstrap --tool mkvtoolnix|mediainfo` 明確安裝，預設不安裝；`tools-verify` 校驗已安裝者並對未安裝者明示跳過。詳見 [mkvtoolnix 與 MediaInfo](matroska-tools.md)。供应商二进制归属与许可证说明见 [第三方工具表](THIRD-PARTY-TOOLS.md)。

## Linux amd64 实验运行库（3C1）

`tools/manifest.json` 的 `mediaRuntime` 固定 Debian 13 的 libc6 `2.41-12+deb13u4`、libgcc-s1 `14.2.0-19` 和只用于归属文件的 gcc-14-base `14.2.0-19`。此入口不安装编译器、不调用 apt/dpkg/ar/tar，也不执行下载包中的任何程序或脚本。默认 Go/media bootstrap 不会自动下载这组运行库。

```sh
sh scripts/runtime-tools bootstrap
sh scripts/runtime-tools verify --offline
sh scripts/runtime-tools sources
sh scripts/runtime-tools sources --offline
python3 -B scripts/test_runtime_tools.py
```

Windows 使用现有 WSL Ubuntu 和其中的 Python 3.9+；这是 Linux runtime 的入口，不提供 Windows 原生运行库回退，不安装 WSL 或 Python：

```powershell
pwsh -NoProfile -File scripts/runtime-tools.ps1 -Command bootstrap
pwsh -NoProfile -File scripts/runtime-tools.ps1 -Command verify -Offline
pwsh -NoProfile -File scripts/runtime-tools.ps1 -Command sources -Offline
```

其他现有 WSL 发行版可以显式传 `-Distribution`，运行端仍要求 Linux amd64。`JELEE_TOOLS_MIRROR` 的 HTTPS 镜像、HTTPS 代理与预填离线缓存可用于下载源不可达的环境；不改变原始文件名、大小或 SHA256。

三份 GNU 许可文本现在从官方 [GNU FTP 的 HTTPS 目录](https://ftp.gnu.org/gnu/Licenses/) 下载。原 `www.gnu.org` 端点在独立冷下载中约 30 秒后网络不可达；此前的预填缓存验收不能证明它可冷启动。替代端点保持相同文件名、大小及 SHA256，未更改许可内容、下载期限或验证门槛。独立空缓存 bootstrap、离线 verify 和 36 项安装器测试已经通过，详见[冷启动证据](evidence/runtime-bootstrap-cold.txt)；远端 CI 是否恢复须以后续实际结果为准。

`installed.json` 绑定完整 runtime 子清单，来源 URL 改动也会让旧安装记录失效。旧安装因此会安全拒绝，不会自动改写或重新信任旧记录。更新时先停止使用该本地 runtime，确认绝对路径确实位于本项目 `.tools/media-runtime/linux-amd64/debian13-glibc2.41-12deb13u4-gcc14.2.0-19/`，再移除或移走**仅这个生成的安装目录**，重新运行 `bootstrap` 和 `verify --offline`。保留 `.tools/downloads`、Go SDK、media 工具及其他项目内容；原有相同 SHA 的下载缓存可复用。

- 先在正式 manifest 记录包/来源/许可 URL、SHA256 与大小，才能下载。首次 `inspect` 仅报告从已校验包里抽取的候选文件哈希，不安装、不修改 manifest、不执行文件。维护者将八个 ELF 与两个包版权文件的哈希写入 manifest 后，`bootstrap` 才允许安装；未就绪或缺少文件哈希时拒绝。
- 二进制包不超过 16 MiB；ar 恰含 `debian-binary`、一个 control 和一个 data 成员，Debian 格式为 2.0。完全忽略 control 内容，不执行 maintainer scripts。data 只接受 gzip/xz，展开不超过 64 MiB，xz decoder 内存上限 64 MiB，tar 最多 10000 条目。
- 仅抽取清单允许的真实普通文件，拒绝路径越界、重复条目、特殊设备、FIFO、稀疏/扩展 tar 格式和被选中的硬/符号链接。未选中的链接从不跟随或落盘。单 ELF 最大 8 MiB、单许可 1 MiB、所有选定文件合计 32 MiB。只检查和写入明确的八个 amd64 ELF 与六份许可/归属文件。
- 下载在读取时受清单大小限制；只允许无凭证的 HTTPS，重定向同样检查。HTTP 状态列、标头和内容的每次 socket 读取检查同一 300 秒绝对期限，避免持续少量字节延长读取；平台 DNS 解析仍不保证可硬中断。partial 总会清理，已确认项目内的坏缓存按 G51.4 删除。已安装文件被修改时失败并保留现场，不自动替换或重新认可。
- 安装在项目 `.tools` 内的全新 staging 中，设置明确权限后发布。OS 文件锁拒绝并行操作，退出时释放；锁文件保留不代表仍被占用。`installed.json` 绑定 runtime 子清单摘要和每个文件哈希。`verify` 完全离线，对照归档、个别文件、安装记录和实际文件集合；额外文件、缺失文件和链接均拒绝。
- 安装过程请求 ELF 0555、许可 0444；WSL DrvFS 可能把只读许可报告为 0555。容器构建仍须明确设置最终 root 所有权及 ELF 0555/许可 0444。安装器不执行许可文本或 ELF，也不把本地权限当作生产不可变性证明。

固定安装根为 `.tools/media-runtime/linux-amd64/debian13-glibc2.41-12deb13u4-gcc14.2.0-19/`。相对映射：`lib64/ld-linux-x86-64.so.2` → `/lib64/ld-linux-x86-64.so.2`；其余七个 `.so` 在 `lib/x86_64-linux-gnu/` → `/lib/x86_64-linux-gnu/`；完整 `licenses/runtime/` → `/licenses/runtime/`。`tools.RuntimeSpec("linux-amd64")` 从嵌入清单返回固定映射与哈希，不读取系统 PATH 或动态认可宿主库。ffprobe 本身的供应商许可文件另外保留。

`sources` 在 `.tools/downloads/` 缓存并核验六份 Debian `.dsc`、上游源码和 Debian 补丁/构建规则压缩包，约 118 MB，不解压或执行。当前未验证 `.dsc` 的 OpenPGP 签名，也未收集 BtbN 全部静态依赖的对应源代码；这是本地实验容器的材料，尚不足以宣称公共镜像分发准备完成。具体范围见 [第三方工具表](THIRD-PARTY-TOOLS.md) 与 `docs/evidence/runtime-tools.txt`。

## 尚未交付的 G51 子项

| 工具/能力 | 当前状态 |
| --- | --- |
| Go / gofmt / vet / coverage | 已固定并提供入口；核心／关键包覆盖率棘轮门禁已接入 CI（[质量门禁](quality-gates.md)），PostgreSQL 包尚未纳入 |
| golangci-lint（含 errcheck、staticcheck、govet、revive、gosec、bodyclose、contextcheck） | 2.14.0 已固定、引导、校验并接入 Linux／Windows CI；既有问题以只减不增的基线管理 |
| gofumpt、漏洞扫描（govulncheck 等） | 尚未固定、引导与接入 |
| 外部 migrate/Atlas CLI、sqlc | 尚未加入工具清单；当前项目通过 golang-migrate 库提供迁移命令 |
| OpenAPI 生成器、buf（如采用 protobuf） | 尚未加入工具清单 |
| Node LTS、包管理器 | Node 24.21.0（含 npm 11.19.0）已固定、引导（Linux）并接入 CI 前端门禁 |
| Playwright 浏览器 | 1.63.0 与 Chrome Headless Shell 153.0.8010.12 已固定、按需引导（Linux）、校验并接入 Linux CI；Firefox／WebKit 不采用 |
| ffmpeg/ffprobe | Windows/Linux amd64本地引导与验证已实现；Linux amd64受保护隔离探测、持久worker和默认关闭开关已接通；Windows正式探测仍关闭 |
| mkvtoolnix、mediainfo | E4：mkvtoolnix 102.0、MediaInfo 26.05 已固定（Linux／Windows amd64，官方 HTTPS、SHA256、授權），可選引導與 `tools-verify` 校驗已實作；Linux amd64 生產沙箱與映像已接通，Windows 只作開發工具（[說明](matroska-tools.md)） |
| Tesseract OCR（G15.6） | E16：Tesseract 5.5.0（Debian 5.5.0-1+b1）與 eng／chi_tra／chi_sim／jpn 語言資料以 55 個 Debian 套件固定（版本、pool URL、SHA256、授權），`make bootstrap-ocr` 可選引導、`tools-verify` 校驗已安裝者；只支援 Linux amd64，不在預設映像（`deploy/ocr/Dockerfile`）（[說明](subtitle-ocr.md)） |
| 合成多轨媒体、章节、损坏素材、`make fixtures` | 3B2生成13个小型自建文件及SHA/结构清单，E4 增至 16 個（內嵌字幕與字型附件 MKV）；双平台真实工具和FD探测测试通过，见[素材说明](fixtures.md) |
| Testcontainers / 嵌入式 PostgreSQL 回退 | 尚未实现；当前使用已有隔离测试容器 |
| 链接检查、shellcheck、actionlint | 尚未加入工具清单 |
| 完整工具链 CI 与 G51.15 全新克隆验收 | 未完成 |

`.github/workflows/jelee.yml` 运行 Windows/Linux 基础编译测试，并使用清单与 go.sum 哈希缓存工具链。独立 PostgreSQL job 使用清单中的固定镜像摘要，设置 `JELEE_REQUIRE_INTEGRATION=true`，运行数据库集成和 race 测试；固定 `ci-only` 密码仅用于该临时隔离服务。完整品牌门禁单独保留且会阻断残留命名，当前不能宣称 CI 全绿。CI已加入fixtures/fixtures-test，远端实际结果待回填；其他尚未固定的工具仍使完整G51.14验收未完成。

## 工具身份诊断（3B2）

在项目根运行 `jelee-cli doctor tools`，无需数据库配置。诊断按嵌入可执行文件的清单检查ffprobe/许可证SHA256，再从新的私有项目目录运行已验证副本的固定`-version`。输出仅为平台、预期版本、状态和固定原因，媒体能力始终为`disabled_sandbox`。这不认证宿主动态库，也不开放媒体读取。

诊断需要项目`.testdata`写入权限并在完成后删除新副本。Linux需要真正可强制0700的文件系统；WSL共享NTFS/DrvFS可能回`temporary_unavailable`。Windows需要可保护新目录DACL的普通用户token；权限受限时安全拒绝，不修改用户系统权限或放宽目录ACL。

## 持久探测 worker 验收（3C2B）

原生 Linux 使用 `make probe-worker-test`，需要专用 `JELEE_TEST_DATABASE_URL`（数据库名必须为 `jelee_test`）、Docker、项目固定 Go/media/runtime 和已生成素材。脚本只在自己的 UUID schema、镜像、容器和暂存目录执行，结束时核对源文件及原素材 SHA256，并在失败时仍清理自己创建的资源。凭证文件在 Docker build 完成后创建，原始输出留在忽略的 `.testdata`。

这个必需目标已经接入 PostgreSQL CI；另外 `make sandbox-test` 保留原生容器的85%覆盖率门槛。CI保留两者的验收日志/摘要。当前真实结果及跳过、失败记录见[worker验证](probe-worker-verification.md)；完整品牌和发布门禁仍未通过。

## NFO 混合库验收（3C3C）

`make nfo-worker-test` 使用相同固定工具和私有测试连接，分别运行 1,000 与 100 文件混合库的冷扫、暖扫、局部修改、取消恢复和 SIGTERM 验收。两组均使用独立 UUID schema、容器、镜像和只读素材；清理只针对本次创建的资源。该目标接在 PostgreSQL CI 的 probe 验收之后，保留 `.testdata/nfo-worker-acceptance.txt` 和结构化摘要 7 天。实际范围与限制见[NFO工作流程验证](nfo-worker-verification.md)。

Windows `fmt-check` 让固定 gofmt 递归检查 cmd/internal/tools，避免长工作目录中逐文件绝对路径参数超过系统上限；退出失败或存在未格式化源码时仍拒绝通过。

## 忽略规则 Git 对照（3D1A）

`make ignore-oracle-test` 或 Windows `scripts/make.ps1 ignore-oracle-test` 运行必需的真实 Git 差分。缺少 Git、启动失败、超时或结果不同均失败；普通 `go test` 在没有 Git 时明确跳过这项对照，不将其当作兼容证据。生产 matcher 只执行纯 Go 计算，不依赖 Git。

测试默认查找宿主已有 Git；可用 `JELEE_IGNORE_ORACLE_GIT` 指定绝对可执行文件路径。本地验证使用清单中登记的现有二进制；CI 使用 runner 自带版本并在 `IGNORE_ORACLE_REPORT` 记录实际完整路径、版本、SHA256、语料 hash 与平台差异。此记录是来源盘点，不是跨机器固定 Git 分发包，也不会安装工具或修改全局 Git 配置。

测试使用项目 `.testdata` 下新建的私有目录，隔离 system/global config、templates、excludes、HOME 和环境变量，固定参数仅执行 --version、init/check-ignore。候选经 NUL 分隔 stdin 传入，输入/输出有上限；Git 单程序最多5秒，命令与 matcher 共用60秒 context。普通文件 I/O、二进制 hash 与清理不保证可被硬中断，不能把该 context 称为整个测试的硬期限。结束清理本次创建的目录。Windows 无法真实创建的语料单列，仍执行纯值黄金测试；原生 Linux 另行对照。CI 保存两平台 `.testdata/ignore-oracle.txt` 7天。

## ABI 遷移門禁的本機產物

門禁使用固定的ApiCompat `10.0.401`，只安裝在專案的 `.tools/abi/10.0.401`；實際 `--version` 輸出另與核准完整版本核對。工具版本及逐符號契約記錄於 `tools/abi/expected-breaks.json`，用途為開發／CI檢查，不隨產品分發。

根目錄 `/abi-base/`、`/abi-head/`、`/abi-naming-base/` 是下載或建置的組件，`/abi-report/` 是原始診斷、退出碼與驗證結果。四個目錄均以精確根路徑忽略，允許刪除後由CI或驗證流程重建；`scripts/fixtures/` 與 `tools/abi/` 中受審查的文字契約仍納入Git。詳見[ABI門禁](abi-report-check.md)。
