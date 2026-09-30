# 第三方开发与测试工具

实际下载的工具与精确平台哈希见 `tools/manifest.json`。Go 下载来源为 [Go 官方下载页](https://go.dev/dl/)；校验信息为 [官方下载元数据](https://go.dev/dl/?mode=json)。

| 名称 | 版本 | 许可证与归属 | 用途 | 分发范围 |
| --- | --- | --- | --- | --- |
| Go（包含 gofmt、vet、coverage） | 1.27.1 | BSD-3-Clause，The Go Authors；发行包内 `go/LICENSE` 与 `go/PATENTS` 保留 | 构建、检查、测试 | 仅项目本地工具；工具链不随应用二进制分发 |
| Gyan Windows amd64 ffmpeg / ffprobe | `9.0.2-essentials_build-www.gyan.dev` | GPL-3.0-or-later，FFmpeg developers、Gyan Doshi 与所链接依赖作者；保留发行包 `LICENSE`、README 与文档 | 可选开发工具；ffmpeg 仅用于合成测试素材/调试，ffprobe 用于开发验证 | 本地被忽略目录，不进入本阶段生产镜像 |
| BtbN Linux amd64 ffmpeg / ffprobe | `n9.0.2-17-g2a571b6068-20260930` | GPL-3.0-or-later，FFmpeg developers、BtbN 与所链接依赖作者；保留发行包 `LICENSE.txt` 及文档 | 同上；glibc 2.28+、Linux 4.18+ | 本地被忽略目录，不进入本阶段生产镜像 |
| Debian libc6 amd64（七个 ELF） | `2.41-12+deb13u4` | LGPL-2.1-or-later 与文件级条款；完整包版权文件保留，glibc contributors / Free Software Foundation / Debian GNU Libc Maintainers | 实验 Linux ffprobe 的加载器与 glibc 闭包 | 仅本地实验镜像；未发布公共镜像 |
| Debian libgcc-s1 / gcc-14-base amd64 | `14.2.0-19` | libgcc 为 GPL-3.0-or-later WITH GCC-exception-3.1；保留 gcc-14-base 完整版权文件与组件条款，GCC contributors / Free Software Foundation / Debian GCC Maintainers | 只取 libgcc_s.so.1 及归属材料，不抽取 GCC 编译器 | 同上 |
| 官方 Go 容器构建镜像 | 1.27.1-alpine3.24 | Go 为 BSD-3-Clause；Alpine 各包保留各自许可证 | Docker 多阶段构建 | 构建阶段使用，最终 scratch 镜像不含该工具链 |
| Docker Engine | 29.7.2（已存在的宿主工具） | Apache-2.0，[Moby 项目](https://github.com/moby/moby) | 启动隔离测试数据库 | 引导不安装、不分发 |
| PostgreSQL 测试镜像 | 16.15 | [PostgreSQL License](https://www.postgresql.org/about/licence/)，PostgreSQL Global Development Group；基础镜像各包保留各自许可证 | 临时集成测试 | 已存在镜像，引导不拉取、不分发 |
| WSL GCC / cc1 / collect2 | Ubuntu `15.2.0-16ubuntu1` | GPL-3.0-or-later；运行库组件另含 GCC Runtime Library Exception 3.1，完整组件条款见宿主 `gcc-15-base/copyright` | Go race/cgo 的 SDK 外部编译与链接 | 既有 Ubuntu 26.04 amd64 工具，只盘点，不安装、不分发 |
| WSL GNU binutils ld.bfd / as | Ubuntu `2.46-3ubuntu2` | GPL-3.0-or-later，Free Software Foundation；文档适用 GFDL-1.3-or-later | 上述 GCC 调用的链接器与汇编器 | 同上 |

测试镜像固定为 `postgres@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685`，来源为 [Docker Official Image](https://hub.docker.com/_/postgres)。此摘要记录本次测试依赖，不构成生产数据库部署版本建议。

宿主 PowerShell、Python、curl 与 GNU Make 作为已有引导前提使用，没有由 Jelee 安装到系统目录。WSL GCC 与 binutils 的实际 ELF 路径、SHA256、Ubuntu 二进制/源码包版本和本机版权文件哈希已登记在 `existingHostDependencies`；这是宿主盘点，不是可移植引导包。GitHub Actions 使用固定提交的 checkout 与 cache 动作。

此前 Linux race 测试的实际结果保留，但当时宿主 C 编译器尚未登记；2026-10-01 补登记后，再进行最终 Linux race 复验。不得将历史测试描述为已满足 manifest-first。Windows 未找到可用 race 编译器的结果仍为不可用，没有安装新编译器。盘点证据见 `docs/evidence/host-compiler.txt`。

浏览器驱动、Node、mkvtoolnix、mediainfo 与其余扫描工具尚未加入清单；不可据此表宣称 G51 工具集合已完整。完整状态见 `docs/toolchain.md`。将来新增工具须先记录来源、精确版本、平台、SHA256、许可证与归属，再允许下载。

## 媒体构建来源与许可

2026-10-01 对照 [FFmpeg 官方下载页](https://www.ffmpeg.org/download.html) 核验当时的稳定源代码版本为 9.0.2（2026-09-18）。FFmpeg 项目自身发布源代码，页面链接 Gyan 和 BtbN 提供的构建；这些下载是供应商二进制。

- Windows：[Gyan 9.0.2 release](https://github.com/GyanD/codexffmpeg/releases/tag/9.0.2)，核心 FFmpeg 源码修订为 [`946fcce07b6dcd0331c8cc609192aeff5e1924f8`](https://github.com/FFmpeg/FFmpeg/commit/946fcce07b6dcd0331c8cc609192aeff5e1924f8)。使用 essentials ZIP，SHA256 `60f467265b1e312373dbcd92200c2618a74850f98d3d078e94296bb3fa2047ba`；与 [Gyan 校验文件](https://www.gyan.dev/ffmpeg/builds/packages/ffmpeg-9.0.2-essentials_build.zip.sha256) 及 release asset digest 一致。[供应商说明](https://www.gyan.dev/ffmpeg/builds/) 标明其静态构建为 GPLv3。
- Linux：[BtbN 固定月末 release](https://github.com/BtbN/FFmpeg-Builds/releases/tag/autobuild-2026-09-30-13-08)，核心 FFmpeg 源码修订为 [`2a571b606854520cf89804d8030c8b328e621689`](https://github.com/FFmpeg/FFmpeg/commit/2a571b606854520cf89804d8030c8b328e621689)。使用 `linux64-gpl-9.0.tar.xz`，SHA256 `68ee646831adaae2495618346f3bba94ff207ff83bbd34d643e7004730d66269`；与该 release 的 `checksums.sha256` 及 asset digest 一致。构建脚本固定于 [`6c9aec5fc9a72ec3abedd1fa84db141fa18cf52b`](https://github.com/BtbN/FFmpeg-Builds/tree/6c9aec5fc9a72ec3abedd1fa84db141fa18cf52b)，[GPL 构建选项](https://github.com/BtbN/FFmpeg-Builds/blob/6c9aec5fc9a72ec3abedd1fa84db141fa18cf52b/variants/defaults-gpl.sh) 为 `--enable-gpl --enable-version3`。构建脚本仓库的 MIT 许可不替代生成二进制的 GPL 许可。

两个平台安装包均保留完整 GPLv3 文本，哈希 `8ceb4b9ee5adedde47b31e975c1d90c73ad27b6b165a1dcd80c7c545eb65b903`。所有完整下载 URL、独立 ffmpeg/ffprobe 哈希、许可文件路径及实际验证版本均记录在 manifest；不会下载浮动 `latest`。

[FFmpeg 许可说明](https://www.ffmpeg.org/legal.html) 解释了启用 GPL 组件对构建许可的影响。目前只提供本地工具和本地实验镜像材料，尚未发布公共镜像，也未声称仅链接 FFmpeg 核心源码便覆盖所有依赖的对应源代码义务。未来若分发 ffprobe，仍须核对完整构建配置、全部对应源代码、依赖许可证和声明，提供符合适用许可的材料；ffmpeg 不得进入生产分发。

## Debian 运行库材料（3C1）

包来源为 [Debian libc6](https://packages.debian.org/trixie/amd64/libc6/download)、[libgcc-s1](https://packages.debian.org/trixie/amd64/libgcc-s1/download) 与 [gcc-14-base](https://packages.debian.org/trixie/amd64/gcc-14-base/download)。完整 HTTPS URL、包 SHA256/大小、选定 ELF 和版权文件 SHA256 均在 `mediaRuntime` 中。包哈希先登记后下载；个别文件哈希从已校验包读出，并在首次安装/执行前写回清单。

安装后的 `licenses/runtime/` 保留 libc6 与 gcc-14-base 包内完整 `copyright`，另有完整 LGPL-2.1、GPL-2.0、GPL-3.0 和 GCC Runtime Library Exception 3.1 文本。不能仅用表格概括替代这些文件；ffprobe 的 GPL 文本及供应商归属也须另随实验镜像保留。

对应 Debian 源码材料已实际下载到项目缓存并校验：glibc `2.41-12+deb13u4` 的 `.dsc`、`glibc_2.41.orig.tar.xz`、Debian 补丁/规则压缩包；gcc-14 `14.2.0-19` 的 `.dsc`、上游源包及 Debian 补丁/规则压缩包。固定下载与校验项来自官方 `.dsc`，当前没有完成 OpenPGP 签名验证。`scripts/runtime-tools sources --offline` 可以复核该材料集合，不会执行构建脚本。

这只覆盖选定 Debian 运行库的来源材料。BtbN ffprobe 静态链接的所有第三方组件版本、补丁、构建配置及完整对应源码仍未收集，不可据此宣称整个 ffprobe 分发材料完整。公共镜像发布仍在范围外；现阶段只验证本地实验镜像。
