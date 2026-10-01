# 当前存储布局

PostgreSQL 是唯一持久数据库。schema 1 存账户/目录/媒体根/媒体来源/ACL 与审计，schema 2 扩展账户和会话，schema 3 新增：

| 表 | 内容与上限 |
| --- | --- |
| jobs | run、状态、policy 快照、计数、private owner/generation/lease；全局 queued+running 限额与终态历史限额 |
| job_directories | 每 run 有界目录 frontier、完成标志及 skipped；根 `.` 计入目录限额 |
| job_inventory | 每 run 有界普通文件观测；root UUID + 相对路径，分页索引 |
| library_inventory_baseline | 每库最后接受的完整盘点路径，独立于 run 历史；skipped/大量缺失不替换 |

schema 4 新增持久探测契约：

| 表 | 内容与上限 |
| --- | --- |
| tool_versions | 不可变的嵌入工具/runtime/parser/policy 身份；默认16、硬上限32 |
| probe_cache | root UUID + 相对路径的单版本规范 metadata/固定错误码/TTL/租约；默认全域100,000 rows，每份metadata最多128 KiB |
| probe_job_state | 每个保留 job 的 probe cursor、互斥计数及身份；history 清理时连同该 job 清除 |
| probe_cache_quota / probe_library_quota | 全域和每库的 row、payload预留、active lease计数；最多1,024库scope |

绝对媒体根仅在既有 library_roots，HTTP 不提供根路径。任务观测的文件/NFO/图片始终留在原本地目录，服务不覆写、不自动删除；盘点不是完整媒体导入。快取仍未接入扫描 worker，默认盘点不会启动探测或写 cache；衍生资产运行目录待后续。

开发工具、下载及 Go 缓存在 `.tools/`、`.cache/`，wrapper 在 `.bin/`；生成素材 `.testfixtures/`、临时数据库凭据与脚本 `.testdata/`，均被 Git 忽略。真实证据经过脱敏后保存在 `docs/evidence/`。Compose 数据卷仍在项目 `data/postgres`，媒体只读挂载 `/media`。libraries 与 audit_logs 的总量保留策略留待后续阶段。

当前 binary 只接受 clean schema 4。000001–000003 不改写；004 down 丢失探测快取/phase/身份/quota，保留原账户/库/inventory/jobs/baseline，详见[快取回滚](probe-cache.md#升级与回滚)。
