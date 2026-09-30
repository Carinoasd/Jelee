# 第 3A 段验证记录

2026-10-01。实现提交 `45d3fbfcb82f4e555aac30b013dec77a3eda88f9`，分支 `feat/jelee-jobs-scan`，作者 Carinoasd。首个任务分段交付持久一次性盘点、真实目录枚举、显式取消、检查点与租约恢复、管理员 API/CLI、schema 3 和协调停止。详细合同见[任务 API](jobs-api.md)、[worker](jobs-worker.md)、[扫描器](scanning-filesystem.md)。

| 检查 | 实际结果与证据 |
| --- | --- |
| Windows 全套构建/fmt/vet/Go测试 | 通过；[日志](evidence/jobs-windows.txt)。Windows 未配置 PG，数据库集成明确跳过；符号链接权限用例亦有跳过 |
| Linux 全套 race、真实 PG、vet | 通过；[日志](evidence/jobs-linux-race.txt)。所有 PG 测试实际执行；DrvFS 特殊文件/名称/权限限制单独列 skip |
| 真 PG 任务仓储 | [独立日志](evidence/jobs-postgres.txt)通过；完整版后续补上保留缺失基准回归，最终全套日志为准 |
| 原生 Linux scanner race | [受限容器 tmpfs 实测](evidence/jobs-scan-linux-native.txt)：14 项全部通过，零跳过，补齐真实 FIFO/非法 UTF8/chmod 与符号链接竞争 |
| 真服务/API/CLI | [执行档证据](evidence/jobs-service.txt)：1000 文件、11 目录、字节数/分类/分页准确；重放、重扫、连续大量缺失、根失联、retry/cancel、正常关闭与全部原文件 hash 检查通过 |
| 生产容器 | [实测](evidence/jobs-container.txt)：256 文件/5 目录/10130 bytes、分类和分页正确；UID65532、只读、cap-drop、healthy；退出0无OOM；migrate3→2→1→0；镜像无ffmpeg/ffprobe/shell/GoSDK |
| Windows race | 未执行测试：cgo 构建缺 GCC，现有 MSVC 不兼容；[受阻记录](evidence/jobs-windows-race-unavailable.txt)。普通 Windows 测试不能记作 race 通过 |
| 保护检查 | Go模块 checksum、忽略规则、增量品牌、diff-check 通过；LICENSE 与需求原文 hash 保持原值；000001/000002 无修改 |
| 完整品牌门禁 | 仍有 15278 个保留旧树命中，失败；未增加例外或降低门禁 |

最终 Linux 覆盖率：HTTP88.6%、app89.1%、worker93.0%、scan89.3%、PG75.4%、CLI70.7%、config95.7%、runtime52.5%。这些结果不代表所有核心模块达到原需求覆盖率，也不把真实执行档/容器流程换算成额外百分比。

生产容器镜像 `jelee/jelee:codex-jobs-test` ID为 `sha256:e0d0c9267a67c1eff580c0ba3465b9159b900089132ddc2c950c3a5c625d1f00`；构建输入 hash 在验收前后相同，对应实现提交。镜像只在本地构建，没有发布。测试使用随机凭据、独立 schema 与自建素材；本次服务/容器/schema/敏感文件已清理，共享临时 PG 保留用于下一段测试。

## 回归覆盖

真 PG 包含：同 actor/key 并发唯一与改请求冲突、queue满载竞争、同库排他、两级 claim、queued/running cancel、过期恢复及尝试上限、所有过时 lease 操作拒绝、batch/Finish 交易中 lease 到期全量回滚、未完成目录重读/去除消失局部条目、UUID 分页索引、限额/整数溢出回滚、skipped/失败/大量缺失不覆盖基准、历史清理保留基准、低于阈值才更新基准、migration 往返且账户/媒体根保留。实际1000行分页面只走100条索引资料。

worker 测试覆盖固定并发、3:1选择/fallback、timer与heartbeat清理、取消、过期 fencing、运行期限、checkpoint、释放与恢复、scanner panic不泄露、成功结束和最后取消竞争。真实 Fx+TCP 回归确认 HTTP drain 耗尽 Stop 期限时已取消 worker，底层 worker未退出时不会提前关池；建构/监听/启动失败也回收。HTTP/CLI 覆盖管理员预检查、事务 live 权限、严格JSON/query/key、分页、准入拒绝、错误四语、安全日志，以及空/不完整成功响应拒绝。

曾遇到的验证问题已修正：首次 Linux 全套恰逢源文件修改造成 import graph 过时，冻结后完整重跑通过；旧真实 TCP 准入测试缺响应屏障，在 Windows 偶发提前释放，补屏障后仍验证真实写超时释放；冒烟脚本首次用了不存在的 migration 命令，修正后完整重跑通过。未把失败日志或前次结果当最终验收。

## 范围与复现

本段实现有界观测，不执行 probe/NFO解析/增量指纹/cron/fsnotify，不删除原媒体或 catalog。尚无接受缺失基准的确认 API，完整 G13/G19/G41/G42 与全功能替代仍未完成。单目录批次最多128，root相对路径1024 UTF8字节，深度128；最大500k配置未做规模性能验收。当前每项多条SQL与全局任务事务锁的吞吐仍须后续实测，底层网络文件系统调用不保证硬取消。

```powershell
./scripts/make.ps1 lint
./scripts/make.ps1 build
./scripts/run-go.ps1 test -count=1 -cover ./...
```

```sh
# 只给专用 jelee_test 提供 JELEE_TEST_DATABASE_URL，绝不能指向生产库。
export JELEE_REQUIRE_INTEGRATION=true
.bin/go test -race -count=1 -cover -v ./...
.bin/go vet ./...
```

原矩阵共336项，当前为1已完成/159部分/176阻塞；本段仅将有实证的子集回填，剩余部分不冒充完成。提交后独立 PR 以阶段1/2分支为基底，避免重复包含前阶段 diff；后续 PR 依次堆叠，合并策略由仓库维护者决定。
