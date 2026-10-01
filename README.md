# Jelee

Jelee 正在以独立 Go 服务逐步接管视频目录与原文件直投。本仓库已实现 Go 基础服务、账户 API、持久只读盘点、固定工具与素材、Linux 隔离探测与扫描快取，以及按库 NFO 验证和图片属性比较；尚未达到完整媒体服务器替代版本的验收条件。

## 当前实现

- Go 1.27.1、fx 生命周期、chi HTTP、pgx PostgreSQL，以及内嵌的 up/down 数据库迁移。
- Argon2id 密码登录、用户管理、会话轮换/撤销、登录限速/锁定与库权限 API；本地 CLI 初始化账户和注册已有视频文件。
- 带 SQL 库权限过滤的目录列表、详情和原文件流式接口；单段/多段 Range 与条件请求。
- 生产请求拒绝转码、HLS、DASH 与重封装；web 会话不能播放，native 会话类型来自数据库。
- 默认关闭目录与直投开关，默认监听 `127.0.0.1:8097`；无有效 PostgreSQL schema 时拒绝启动。
- Windows/Linux 本地 Go 引导、哈希验证、安全解压与测试入口。
- 只读 NFO adapter 与无需数据库的 `nfo validate` CLI；原文保留、字段提取和安全校验已通过 Windows/Linux 测试。
- 持久盘点任务、两级队列、管理员 API/CLI、取消、租约恢复和分批检查点；盘点只观察路径、大小和 mtime，不修改原文件或自动删除目录数据。
- Linux amd64 固定 ffprobe／8 个动态库、Landlock／seccomp helper、唯读 FD 输入与有界 JSON 解析；`jelee-cli doctor probe` 实测隔离能力。扫描可显式启用探测，Windows 正式 probe 仍停用。
- 持久 probe 意图、不可变工具身份、库/item失效、parent/file租约、批次检查点、TTL/容量及有界回收；已接 worker、管理员 API/CLI 和关闭回收，见[探测工作流程](docs/probe-worker.md)。
- schema 7 的独立 NFO off/read-only 策略、有界缓存、入队意图和 parent 检查点；管理员显式提交后按 inventory → NFO → probe 执行，提供历史计数、当前验证结果与图片 kind/size/mtime 比较，见[NFO 工作流程](docs/nfo-worker.md)和[验证报告](docs/nfo-worker-verification.md)。
- 纯 `.jeleeignore` 编译/匹配组件：来源行号、目录继承、父目录剪枝、固定预算及真实 Windows/Linux Git 对照；尚未接入生产扫描，见[合同](docs/ignore-matcher.md)与[验证](docs/ignore-matcher-verification.md)。
- 忽略来源观察与编译缓存：Windows/Linux严格拒绝链接，重新读/hash与核对身份后才发布有界缓存；见[合同](docs/ignore-source.md)和[验证](docs/ignore-source-verification.md)。生产扫描、持久规则快照和忽略报告待后续接线。

尚未交付完整管理前端、第三方协议兼容、完整 metadata 增量导入、持续监看/排程、图片资产处理、用户权限管理界面、完整诊断、完整工具与素材链。NFO 尚缺修改后的 XML 序列化、Catalog 来源优先级/锁合并、`--fix` 及真实客户端往返验收。现有旧服务端源码仍保留，尚未完成所有功能裁剪与内部重命名。完整品牌门禁目前会失败；增量检查通过不能代替最终验收。

## 开始使用

Windows PowerShell 7.2+：

```powershell
pwsh -NoProfile -File scripts/make.ps1 bootstrap
pwsh -NoProfile -File scripts/make.ps1 tools-verify
pwsh -NoProfile -File scripts/make.ps1 build
pwsh -NoProfile -File scripts/make.ps1 test
```

Linux（已有 Python 3.9+、GNU Make）：

```sh
make bootstrap tools-verify build test
```

数据库、令牌、灰度开关与启动步骤见 [快速开始](docs/quickstart.md)。`test` 不会替你创建数据库；数据库测试缺少连接配置时明确跳过，必须另外运行 `test-integration` 才能验证数据库行为。

## 文档与状态

- [仓库审计基线](docs/00-audit-baseline.md)
- [需求追溯](docs/requirements-traceability.md)
- [账户初始化与恢复](docs/account-bootstrap.md)
- [账户 API 与权限](docs/accounts-api.md)
- [第 2 阶段验证](docs/accounts-verification.md)
- [只读盘点 API 与配置](docs/jobs-api.md)
- [第 3A 段验证](docs/jobs-verification.md)
- [第 3C1 段隔离探测验证](docs/probe-verification.md)
- [探测快取数据库契约与验证](docs/probe-cache-verification.md)
- [探测 worker 验证](docs/probe-worker-verification.md)
- [NFO 来源验证](docs/nfo-source-verification.md)
- [NFO 快取验证](docs/nfo-cache-verification.md)
- [NFO 工作流程、API/CLI 与图片比较](docs/nfo-worker.md)
- [NFO 工作流程实际验证](docs/nfo-worker-verification.md)
- [忽略规则匹配实际验证](docs/ignore-matcher-verification.md)
- [工具链与未完成项](docs/toolchain.md)
- [NFO 只读兼容范围](docs/nfo-compatibility.md)
- [安全模型](docs/security-model.md)
- [Git 与回滚流程](docs/git-workflow.md)
- [命名映射](docs/branding-rename-map.md)
- [许可证与来源](docs/LICENSE-COMPLIANCE.md)
- [保留的上游说明](docs/upstream-README.md)

各段验证后分别推送并提 PR：[基础与账户 #1](https://github.com/MoYuanCN/Jelee/pull/1)、[持久盘点 #2](https://github.com/MoYuanCN/Jelee/pull/2)、[固定媒体工具 #3](https://github.com/MoYuanCN/Jelee/pull/3)、[执行器与素材 #4](https://github.com/MoYuanCN/Jelee/pull/4)、[Linux 隔离探测 #5](https://github.com/MoYuanCN/Jelee/pull/5)、[探测快取 #6](https://github.com/MoYuanCN/Jelee/pull/6)、[探测 worker #7](https://github.com/MoYuanCN/Jelee/pull/7)、[NFO 来源 #8](https://github.com/MoYuanCN/Jelee/pull/8)、[NFO 快取 #9](https://github.com/MoYuanCN/Jelee/pull/9)、[NFO worker与图片比较 #10](https://github.com/MoYuanCN/Jelee/pull/10)、[忽略规则匹配 #11](https://github.com/MoYuanCN/Jelee/pull/11)。第一阶段[验证记录](docs/verification-report.md)保留为历史快照。尚未创建发布标签或正式版本。上游历史、许可证与归属资料保留，不能把当前版本标记为 G00–G51 已完成。

### Linux 实验运行时验证

```sh
make bootstrap tools-verify bootstrap-media media-tools-verify bootstrap-runtime runtime-tools-verify
make fixtures fixtures-test runtime-toolchain-test sandbox-test probe-runtime-test
```

后两个测试需要已有 Docker 与支持 Landlock ABI≥3 的 Linux 内核；缺工具或隔离时必须失败，不以 skip 通过。Dockerfile 需要上述固定本地工具包，建置时校验全部 16 个文件哈希，仅纳入 ffprobe／动态库／许可，不纳入 ffmpeg。对应源码材料和公开分发准备仍未完成；详细范围见 [Linux 隔离](docs/media-sandbox.md)与[规范化](docs/media-metadata.md)。
