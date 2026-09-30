# 第 2 阶段验证记录

2026-10-01，分支 `feat/jelee-go-foundation`，作者 `Carinoasd`。本阶段在第一阶段 8 个已推送提交上增加密码账户、会话管理和库 ACL API；[草稿 PR #1](https://github.com/MoYuanCN/Jelee/pull/1)以 `master` 为目标。完整需求仍以[336 项追溯矩阵](requirements-traceability.md)为准。

## 实现与边界

- Argon2id 随机盐、严格 PHC 解析、12–1024 字节密码策略、可配置计算预算；进程共享密码执行配额，HTTP 准入限制等待请求数。
- schema 2 与事务仓储：用户生命周期、自己修改资料/密码、令牌轮换和撤销、持久锁定、最多有效会话数、库 ACL 原子替换、最后管理员保护及安全审计。
- HTTP 严格 JSON、IP/名称限速、22 个四语错误码、用户保存语言优先、账户功能开关、OpenAPI 请求/响应 schema。
- 受信任本地 CLI 通过 stdin 初始化管理员或恢复密码，不将密码放入 argv 或配置。

未交付头像、内容分级、可疑登录通知、用户/设备播放及带宽预算、个人数据导出/永久删除、管理 UI、第三方协议认证或分布式限速。账户事务使用 schema 内的 advisory 锁串行检查权限与最后管理员；性能预算尚未验收。密码模块的微基准不代表整站 P95。

## 实际执行结果

独立 PostgreSQL 测试使用 PostgreSQL 16.15、tmpfs、随机测试凭据与独立 schema。Windows 无法直接连接 WSL loopback 的测试服务，因此数据库集成结果以 Linux 实际执行为准；Windows 未配置数据库的跳过结果不计通过。

| 检查 | 实际结果与证据 |
| --- | --- |
| Windows 构建、fmt/vet、全套 Go 测试 | 通过；[详细日志](evidence/accounts-windows.txt)。3 个符号链接子测试因系统权限跳过；4 个 PostgreSQL 测试因没有 Windows 侧 DSN 跳过 |
| Linux 全套 race、真实 PostgreSQL、vet | 通过；[详细日志](evidence/accounts-linux-race.txt)。媒体与 NFO 的 2 个 FIFO 测试因 DrvFS 不支持而跳过；数据库没有跳过 |
| 最终 null 输入合约修正 | Windows 完整 HTTP 专测通过，覆盖率 90.5%；[Linux HTTP race 复验](evidence/accounts-http-final.txt)通过，覆盖率 90.5%。该小修正发生在全套日志之后，因此保留独立复验记录 |
| PostgreSQL 权限与并发 | [仓储独立 race](evidence/accounts-postgres.txt)通过，覆盖率 74.5%；所有账户事务与迁移测试实际连接 PostgreSQL |
| 真执行档 HTTP/CLI 流程 | [端到端记录](evidence/accounts-service.txt)通过；临时服务和 schema 已清理，密码/令牌/连接串未进入证据 |
| 最终容器 | [构建与实测](evidence/accounts-container.txt)通过：UID/GID 65532、只读文件系统、移除 capabilities、初始化/登录/登出、目录/Range/web 拒播、原 NFO hash 不变、healthy、正常关闭且无 OOM、schema 2→1→0 |
| 模块校验与保护门禁 | [校验记录](evidence/accounts-checks.txt)：模块 checksum、增量品牌和忽略规则通过；LICENSE 与需求原文 hash 未变 |
| 完整品牌门禁 | 仍失败，15278 个原树非白名单命中；没有关闭或降低该门禁 |

最终模块覆盖率：app 85.4%、password 95.9%、config 95.7%、HTTP 90.5%、PostgreSQL 74.5%、CLI 65.7%。真实执行档/容器测试没有注入 Go coverage，不能把它们换算成额外百分比；尚未达到原需求全部关键路径覆盖率与性能验收。

交叉审阅补上响应写入期限、按认证用户 ID/IP 的独立改密限速、CLI 显示名 128 字节边界及 null 值拒绝。真 TCP 回归使用 8 个不读取响应的客户端：第 9 个请求先收到 503，8 个写入均实际超时并释放名额，后续请求返回 200；Windows 和 Linux race 都通过。OpenAPI 与实际 chi 路由精确比对覆盖 18 个账户方法/路径。

已执行的真实数据库回归包括：同 key 的 12 路并发创建、用户名大小写冲突、撤销/降权后操作拒绝、自己与其他用户的权限界限、12 路失败登录计数/锁定、过时密码快照拒绝、12 路会话上限、8 路令牌轮换、改密撤销、ACL 替换失败原子回滚、删除关闭目录及媒体访问、恢复不恢复旧令牌、最后两个管理员并发降权、扩展审计无凭据泄漏、超过 1000 条结果明确失败，以及 schema 回退保持软删除用户禁用。

## 可复现入口

```powershell
./scripts/make.ps1 lint
./scripts/make.ps1 build
./scripts/run-go.ps1 test -count=1 -cover ./...
```

Linux 在已有本地 Go 工具链及隔离 PostgreSQL 下运行：

```sh
export JELEE_REQUIRE_INTEGRATION=true
# JELEE_TEST_DATABASE_URL 由受限测试配置提供；不能指向生产库。
.bin/go test -race -count=1 -cover -v ./...
.bin/go vet ./...
```

日常 CI 保留 Windows/Linux、真实 PostgreSQL 和完整品牌门禁。原树的品牌残留仍会使完整品牌门禁失败，本阶段不将其豁免。

## 提交记录

首阶段已经推送的 HEAD 为 `1f9426db96d90a7eb55ca571faf1d0f80523ac4a`，GitHub 首次 CI 的 Windows、Linux 与 PostgreSQL job 已通过；完整品牌 job 仍失败。相关[首阶段 CI](https://github.com/MoYuanCN/Jelee/actions/runs/36742894214)与[历史验证快照](verification-report.md)保留，不把旧镜像或旧测试结果冒充本阶段验证。

| 提交 | 范围 |
| --- | --- |
| `d906f9183ed01cd753e8274603ecc5714bb432a3` | 密码、配置、领域与应用用例、schema 2、PostgreSQL 账户/会话/ACL 与对应测试 |
| `0beb2f17dca167ce6e51614f6010e9a983aab77c` | HTTP、严格 JSON/准入/限速、四语、OpenAPI、runtime、账户 CLI 与文档/测试 |

验证记录与需求矩阵作为后续独立文档提交。以上源码和完整文档在同一阶段推送，沿用命令级 Git 作者设置；没有修改全局署名，也没有发布版本标签。
