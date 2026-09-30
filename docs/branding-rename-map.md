# 命名迁移映射

本文件记录从 Jellyfin / Emby / MediaBrowser 来源到 Jelee 的分阶段映射。当前新增 Go 服务独立装配，旧 C# 文件未进行全局文本替换，也未批量删除。

| 来源或旧标识 | 新标识 / 边界 | 状态 |
| --- | --- | --- |
| Jellyfin 服务入口与程序集 | `cmd/jelee`、`jelee` 可执行文件 | Go 基础实现已新增，旧程序集保留 |
| 旧迁移入口 | `cmd/jelee-migrate` | 新 PostgreSQL up/down/status 已新增；旧 SQLite 导入未实现 |
| 旧管理 CLI | `cmd/jelee-cli` | 已提交 doctor、provision、import-video 和只读 nfo validate |
| `JELLYFIN_` 配置前缀 | `JELEE_` | 新服务只读新前缀；旧配置转换未实现 |
| 旧领域/Controller 命名 | `internal/domain`、`internal/app`、`internal/adapter` | 仅已迁移功能；旧 C# 核心仍含旧名称 |
| Jellyfin/Emby 协议对象 | 未来隔离于 `internal/adapter/compat` | 兼容协议尚未实现 |
| 原 README | Jelee README；原文保存在 `docs/upstream-README.md` | 已完成主页来源分离 |
| 原法定作者、LICENSE、NOTICE | 保留原文 | 必须保留 |
| NFO 标签 | `internal/adapter/nfo` 提取字段并保留原文字节 | 只读 adapter 与校验 CLI 已提交并通过 Windows/Linux 测试；编辑序列化、批量/修复/任务与真实客户端往返未完成 |
| 旧客户端字段与资产名 | 未来由显式兼容 Mapper 保留协议写法 | 客户端协议与资产处理仍未实现 |

`make brand-scan` 扫描受跟踪与新增未忽略文本，白名单以精确文件路径配置。许可证/来源文档和扫描器规则本身有明确例外；没有给整个旧项目目录豁免。

`make brand-scan-incremental` 使用 `--new`，只检查新服务目录及其文档，适合作为增量修改的检查。**完整扫描目前仍会失败，G00 的全仓库纯净性未完成。** 后续每个模块需单独替换、编译和回归后再更新该映射。
