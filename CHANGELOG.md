# Changelog

## Unreleased — Go foundation

- 第3C3B新增按库默认关闭的NFO只读策略、安全验证摘要及schema6持久快取；正负TTL、行数/字节配额、连续检查点和事务租约核对均有明确上限。库级worker与API仍由3C3C接入，详见 `docs/nfo-cache.md`。

- 第3C3A为只读NFO提供完整内容指纹与独立解析来源；现有单档CLI核对读取前后和当前根/文件身份，读取中改动返回固定 `nfo_changed`。持久快取与按库任务留待后续分段。

- 第3C2B新增显式开启的持久scan/probe请求、增量快取worker、库/条目幂等重建、管理员API/CLI与能力降级；schema5保持前四份迁移不变，详见 `docs/probe-worker.md`。

- 第 2 阶段新增 Argon2id 密码登录、首次管理员初始化、用户生命周期和库 ACL 管理 API；支持限速、失败锁定、会话轮换与撤销、最后管理员保护，以及安全事务审计。
- 新增 schema 2 迁移、账户 API 开关、四语错误与用户语言偏好、严格 JSON 请求校验及账户 OpenAPI schema；详细范围见 `docs/accounts-api.md`。
- 新增独立 Jelee Go 服务、PostgreSQL schema 与迁移命令。
- 新增受会话和库权限限制的目录接口与原文件直投；生产请求拒绝转码相关能力。
- 新增本地用户/令牌创建、单文件注册和基础 doctor 命令。
- 新增 Windows/Linux 项目本地工具链、HTTP/媒体/数据库回归测试与独立 CI。
- 新增只读 NFO 解析、原文字节保留与离线校验 CLI，通过 Windows/Linux 测试。
- 记录上游审计、需求追溯、许可证与回滚边界；保留原源码和历史。

当前没有发布 SemVer 版本或标签。完整 Web、兼容协议、扫描、图片、运维与全部 G51 工具链仍未完成。NFO 修改后的 XML 序列化、按库批量处理、修复、任务接入及真实客户端往返尚未完成。旧服务端命名仍触发完整品牌门禁。
