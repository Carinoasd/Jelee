# 3D1C1 持久忽略意图验证

日期：2026-10-01。源码提交 `b07ad3a6f20508c43b17458cd6847451ff6d68ab`；分支 `feat/jelee-ignore-inventory`，基于 PR #12 的 `e9aea71540ea4add86b2ebeabfb81f7e764ba1b1`。本报告记录本地实际结果，新 PR 的远端 CI 另行核对。

## 交付范围

schema 008 保存明确的忽略 mode/case、固定 program/proof 合同版本和独立 parent 标记；同 key 重放比较原意图，三个重试入口均复制父任务设置。任务/请求/frontier/NFO/probe/审计同交易提交。数据库约束保护不可变意图和 job/library 关联；008 down 拒绝仍保留的 enabled 意图，包括 terminal 和不一致记录。

当前 worker 不领取 enabled ignore 任务，直接 inventory、NFO、probe 执行和成功 Finish 入口也关闭。HTTP/CLI 尚无启用参数。取消、失败、过期恢复和普通 off 扫描继续可用，未启用过滤不会产生永久库级 review 标记。详见[合同](ignore-inventory.md)及[后续计划](ignore-inventory-plan.md)。

本段没有实现过滤扫描、持久规则来源证明、忽略命中报告或过滤后的基线合并。固定 proof 版本是预期合同标识，不代表已验证文件系统证明。

## 同一源码的两平台结果

固定 Go 1.27.1，使用既有项目工具，无新增依赖或全局安装。

| 验证 | 实际结果 |
| --- | --- |
| Windows | 必需 Git oracle、三个命令 build、格式/vet、完整模块测试通过，29 包；模块测试 7.9725 秒。未运行 Windows race。 |
| Windows skip | 202 个 skip 事件：188 为未配置本机 PG 的数据库测试及子测试，14 为平台/专用工具条件；不计数据库验证通过。 |
| 原生 Linux | `/tmp` 独立来源快照，必需 Git oracle、三个 build、vet、完整模块 `-race -count=1` 通过；29 包、627 个顶层测试，race 测试 155.696 秒。 |
| PostgreSQL | 专用 `jelee_test` 中各测试创建独立 schema；原生整包包含 131 个 PG 顶层测试，零 skip。包含 12 个新请求测试和 5 个新迁移测试。 |
| Linux skip | 6 项既有专用工具/沙箱测试：显式 instrumented child、真实 pinned ffprobe、隔离容器线程预算、root-owned fixture、显式 developer profile；逐项理由保留在 JSON。 |
| 源码一致性 | 两平台验证的 350 个 source/config SHA 一致，且全部与源码提交中的 Git blob 一致。 |
| 保留材料 | LICENSE、原需求、go.mod/go.sum、migration 001–007，共 18 个文件与基线相同。 |
| 产物与命名 | 固定工具 hash 通过；gitignore guard 零违规；增量品牌零违规，完整品牌要求仍未满足。 |

证据：[Windows](evidence/ignore-inventory-windows.json)、[原生 Linux](evidence/ignore-inventory-native.json)、[提交与保留材料](evidence/ignore-inventory-guards.json)。源码快照和详细原始输出在忽略的 `.testdata`；公开 JSON 保存测试、命令、skip 原因与日志 SHA，不包含数据库凭证或宿主用户名路径。

Windows 定向 PG 尝试未连通专用 WSL 数据库，未作为数据库验证。最终 PG 验证在原生 Linux 运行。原生测试需要正常宿主权限启动 WSL，未通过修改测试或降低权限断言掩盖环境限制。

## 有效断言

- 8 个并发同 key 调用只产生一个 job/request/提交审计；普通用户与已撤销管理员不能提交或重放。
- mode/case/off 改变为冲突；普通 job、probe 和 scan-stage 三个重试入口保留 intent/identity。缺 request、单边 marker 损坏均不会变成 off。
- 5 种 claim 调用（包括 `Ignore=true`）跳过 enabled，仍可领取另一个库的 off 工作；跳过不增加 attempts。
- 构造真实有效 lease 后，14 个直接执行入口返回固定错误与零结果，所有持久状态保持一致。
- 插入拒绝回滚 job、frontier、请求、工具身份、quota、基线与审计；逾时测试用非事务 sequence 证明已进入 INSERT trigger，随后确认全部业务写入回滚。
- queued cancel、running cancel/failed/release，以及 expired requeue/attempt-exhausted/cancelled 保持基线和零 missing。旧 lease 不能修改已恢复任务；后续普通 off 扫描可正常完成。
- schema 8→0→8 与历史 migration 目标回归；真实 schema 7 的任务、NFO 缓存、库存、图片统计和审计在 008 升降后保持一致，旧 off 任务可继续领取。
- clean 7、dirty 8、future schema 均拒绝 readiness；008 down 的拒绝必须是固定 `55000` 与预期消息，不能把任意 SQL 错误当作保护成功。
- queued/running/三种 terminal、marker-only/row-only/orphan 都阻止 down；正常历史清理级联移除 retained 请求后可回退。独立验证 immutable trigger、CHECK、NOT NULL 和 composite FK。

## 覆盖率与限制

原生 statement coverage：`domain/ignore.go` 14/14（100%），`domain/scan_intent.go` 5/5（100%），新增 PG helper `ignore_requests.go` 21/25（84%）。三者合计 40/44（90.91%）；这不是所有修改行或整个 PG 包的覆盖率。全模块 **83.2%**，仍未达到原需求的全项目 85% 门槛，不能用新文件统计替代它。

本段没有重跑 tagged 100/1000 文件真实媒体验收或测量新吞吐/P95。PR #12 的远端真实媒体和 NFO 验收通过属于其已提交版本，不能冒充本段 schema 8 的结果。C1 的实际过滤和目录句柄证明仍未启用，C2/C3 必须分别验证。

追溯矩阵仍为 4 已完成、173 部分、159 阻塞，共 336 项。第 3 阶段与完整项目均未完成。
