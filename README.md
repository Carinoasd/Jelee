# Jelee

Jelee 正在以独立 Go 服务逐步接管视频目录与原文件直投。本仓库当前交付的是**Go 基础服务与第 2 阶段账户 API**，尚未达到完整媒体服务器替代版本的验收条件。

## 当前实现

- Go 1.27.1、fx 生命周期、chi HTTP、pgx PostgreSQL，以及内嵌的 up/down 数据库迁移。
- Argon2id 密码登录、用户管理、会话轮换/撤销、登录限速/锁定与库权限 API；本地 CLI 初始化账户和注册已有视频文件。
- 带 SQL 库权限过滤的目录列表、详情和原文件流式接口；单段/多段 Range 与条件请求。
- 生产请求拒绝转码、HLS、DASH 与重封装；web 会话不能播放，native 会话类型来自数据库。
- 默认关闭目录与直投开关，默认监听 `127.0.0.1:8097`；无有效 PostgreSQL schema 时拒绝启动。
- Windows/Linux 本地 Go 引导、哈希验证、安全解压与测试入口。
- 只读 NFO adapter 与无需数据库的 `nfo validate` CLI；原文保留、字段提取和安全校验已通过 Windows/Linux 测试。

尚未交付完整管理前端、第三方协议兼容、媒体扫描/探测、图片资产处理、用户权限管理界面、完整诊断、完整工具与素材链。NFO 尚缺修改后的 XML 序列化、按库批量处理、`--fix`、任务接入及真实客户端往返验收。现有旧服务端源码仍保留，尚未完成所有功能裁剪与内部重命名。完整品牌门禁目前会失败；增量检查通过不能代替最终验收。

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
- [工具链与未完成项](docs/toolchain.md)
- [NFO 只读兼容范围](docs/nfo-compatibility.md)
- [安全模型](docs/security-model.md)
- [Git 与回滚流程](docs/git-workflow.md)
- [命名映射](docs/branding-rename-map.md)
- [许可证与来源](docs/LICENSE-COMPLIANCE.md)
- [保留的上游说明](docs/upstream-README.md)

当前工作位于 `feat/jelee-go-foundation`，每阶段验证后提交和推送至[草稿 PR #1](https://github.com/MoYuanCN/Jelee/pull/1)。第一阶段[验证记录](docs/verification-report.md)保留为历史快照；账户功能范围见[账户 API](docs/accounts-api.md)。尚未创建发布标签或正式版本。上游历史、许可证与归属资料保留，不能把该基础版本标记为 G00–G51 已完成。
