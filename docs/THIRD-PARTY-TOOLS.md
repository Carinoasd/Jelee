# 第三方开发与测试工具

实际下载的工具与精确平台哈希见 `tools/manifest.json`。Go 下载来源为 [Go 官方下载页](https://go.dev/dl/)；校验信息为 [官方下载元数据](https://go.dev/dl/?mode=json)。

| 名称 | 版本 | 许可证与归属 | 用途 | 分发范围 |
| --- | --- | --- | --- | --- |
| Go（包含 gofmt、vet、coverage） | 1.27.1 | BSD-3-Clause，The Go Authors；发行包内 `go/LICENSE` 与 `go/PATENTS` 保留 | 构建、检查、测试 | 仅项目本地工具；工具链不随应用二进制分发 |
| 官方 Go 容器构建镜像 | 1.27.1-alpine3.24 | Go 为 BSD-3-Clause；Alpine 各包保留各自许可证 | Docker 多阶段构建 | 构建阶段使用，最终 scratch 镜像不含该工具链 |
| Docker Engine | 29.7.2（已存在的宿主工具） | Apache-2.0，[Moby 项目](https://github.com/moby/moby) | 启动隔离测试数据库 | 引导不安装、不分发 |
| PostgreSQL 测试镜像 | 16.15 | [PostgreSQL License](https://www.postgresql.org/about/licence/)，PostgreSQL Global Development Group；基础镜像各包保留各自许可证 | 临时集成测试 | 已存在镜像，引导不拉取、不分发 |

测试镜像固定为 `postgres@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685`，来源为 [Docker Official Image](https://hub.docker.com/_/postgres)。此摘要记录本次测试依赖，不构成生产数据库部署版本建议。

宿主 PowerShell、Python、curl、GNU Make 与 C 编译器作为已有引导前提使用，没有由 Jelee 安装到系统目录。它们的宿主版本不会冒充项目已固定的下载工具。GitHub Actions 使用固定提交的 checkout 与 cache 动作。

媒体工具、浏览器驱动、Node 与其余扫描工具尚未被加入清单；不可据此表宣称 G51 工具集合已完整。完整状态见 `docs/toolchain.md`。将来新增工具须先记录来源、精确版本、平台、SHA256、许可证与归属，再允许下载。开发工具不随生产产物分发；任何实际再分发仍须带齐其对应许可证和声明。
