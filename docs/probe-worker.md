# 扫描后的持久媒体探测（3C2B）

本段把[快取契约](probe-cache.md)接入真实扫描、后台 worker、管理员 API 和 CLI。schema 5 新增 `probe_requests`；000001–000004 保持原文。完整第3阶段仍包含监看、排程、ignore、NFO/图片和规模验收。

## 启用与能力

默认 `JELEE_ENABLE_PROBE=false`。开启需要账户、任务和已验证的 Linux amd64 生产镜像：

```text
JELEE_ENABLE_ACCOUNTS=true
JELEE_ENABLE_JOBS=true
JELEE_ENABLE_PROBE=true
```

使用只读媒体挂载及有容量限制的私有暂存空间；Compose 已配置64 MiB `/tmp` tmpfs。默认2个任务 worker、最多2个本地探测和2个数据库文件租约。数据库操作超时必须短于 parent/file 租约心跳间隔；当前探测租约30秒，DB timeout必须小于10秒。

启用时验证固定工具与依赖闭包，并实际运行隔离 health helper。工具缺失、平台不支持或隔离不健康时，`GET /api/v1/system` 的 `data.probe` 返回 `enabled=true, available=false, state=unavailable` 和固定原因。账户与已存在原档服务继续运行。readiness只检查核心存储，不在每次请求中运行工具。

默认关闭时不创建 probe runner、暂存目录、policy 或后台清理任务。不具探测能力的实例不会领取含 `probe_requests` 的任务。运行期间工具故障立即停用该实例后续探测；修好部署后重新启动实例，再由管理员重试失败任务。没有裸 ffprobe 或 ffmpeg 回退。

## 管理员操作

所有路由使用 Bearer 会话；事务内重查管理员和有效会话。请求只接受已登记 ID，不接受文件根、工具路径、argv 或工具身份。

| 操作 | 方法与路径 | 请求体 |
| --- | --- | --- |
| 一般盘点 | `POST /api/v1/libraries/{id}/scan` | `{}` 或 `{"probe":false}` |
| 盘点后增量探测 | 同上 | `{"probe":true}` |
| 整库重建 | `POST /api/v1/libraries/{id}/probe/rebuild` | `{"iUnderstand":true}`（G45.6 危险操作确认，缺少时 400 `confirmation_required`） |
| 单条目重建 | `POST /api/v1/items/{id}/probe/rebuild` | `{}` |
| 探测进度 | `GET /api/v1/jobs/{id}/probe` | 无 |
| 取消 / 重试 | `POST /api/v1/jobs/{id}/cancel` 或 `/retry` | `{}` |

提交、重建和重试要求 `Idempotency-Key`。提交和重建可选 `priority: "manual" | "background"`，默认 manual。JSON对象最多64 KiB，拒绝未知、重复、大小写别名及null字段。新的成功提交返回202与Location；保留历史中的相同请求返回200及 `Idempotency-Replayed: true`。

相同actor/key的完整意图必须一致。重建在一个短事务中失效generation、固定身份、入列及审计；重送不再次失效。运行能力关闭或故障后，已保留的相同请求仍能重放；新的probe提交分别返回409 `probe_disabled` 或503 `probe_runtime_unavailable`。库繁忙、队列满、缓存容量不足有独立固定错误。

重试创建新任务，保留原probe scope/target，使用该实例当前可信工具。旧任务的身份与请求不可改写。幂等范围受任务历史保留限制；清理历史后不能保证旧key仍能重放。

单条目重建仍需扫描整个库，然后只探测数据库已映射到该条目的sources；本段不把全部inventory自动导入正式catalog。

CLI通过相同HTTP API操作，token从stdin传入：

```text
jelee-cli jobs scan --id <library-id> --key scan-1 --probe --token-stdin
jelee-cli jobs probe-rebuild-library --id <library-id> --key rebuild-1 --i-understand --token-stdin
jelee-cli jobs probe-rebuild-item --id <item-id> --key rebuild-2 --token-stdin
jelee-cli jobs probe --id <job-id> --token-stdin
jelee-cli jobs retry --id <job-id> --key retry-1 --token-stdin
```

远端服务可增加 `--url`。CLI输出仅保留公开字段，并校验状态、固定错误、UUID、必填字段及计数总和；响应中的额外路径或身份字段不会写入输出。

## 进度与恢复

公开summary包含 `jobId/libraryId/enabled/scope/targetItemId/phase`，以及 processed、hits、negativeHits、succeeded、failed、changed、unavailable。processed等于后六项总和，来自已提交检查点，最大500,000。公开摘要不返回工具身份、绝对路径、原始JSON、stderr或任意cache枚举。

phase为 disabled、waiting_scan、running、done、aborted、cancelled；未知总数与ETA不估算。单项坏媒体可以记入负快取并继续；工具故障中止phase，父job以既有 `scan_unavailable` 结束，probe摘要保留具体固定错误。

worker先读取持久请求/phase。running phase从连续UUID检查点继续，done不重复盘点，aborted保持原失败。文件打开、指纹和子程序均在SQL事务之外。命中按最多16项连续前缀提交；miss一次一项，先取得本地并发额度再申请数据库文件租约。保存正/负结果前重新Inspect并检查完整身份与stamp；父取消和心跳丢失不形成负快取。

每实例每60秒发起一次最多128项的cache回收，DB context最多2秒。多实例共享数据库配额锁；维护错误使用固定日志code。停止时先取消worker/维护，等待退出，再清理自己创建的暂存目录并关闭数据库。

## 升级与回滚

1. 保存数据库备份；停止旧服务，运行 `jelee-migrate up` 到clean schema5，再启动本段binary。schema4和5的服务不能混跑。
2. down5之前停止入列，完成或取消所有含probe请求的queued/running任务，并等待worker释放租约与退出。
3. down5只删除新增请求表及其trigger函数，保留schema4快取/identity/phase和既有业务资料；恢复schema4 binary前确认迁移为clean4。请求历史丢失是此回滚的明确代价。
4. 保护条件拒绝down时，golang-migrate可能留下dirty。人工检查实际schema和活动任务后决定恢复流程；不得自动force或改已发布迁移。

## 验收与限制

本段实际结果见[验证报告](probe-worker-verification.md)，含完整两平台回归、真实1,000→0→17以及正式HTTP/Fx/SIGTERM证据。

`make probe-worker-test` 要求专用 `JELEE_TEST_DATABASE_URL`（数据库名 jelee_test）、本地固定工具和生成的Linux素材，以及Docker。它构建当前生产镜像与受保护测试入口，以UID65532、只读根/媒体、无capabilities、no-new-privileges运行真实HTTP→PG→scan→worker→隔离ffprobe。此专用测试入口通过httptest运行正式HTTP handler，使用真实worker/数据库/隔离工具；不将它作为正式HTTP listener/Fx启停验收。自己生成的1,000个路径依次验证1,000→0→17次metadata调用；health调用单独执行。坏媒体、取消/恢复、重建幂等与权限竞争另由相关测试验证。

该验收的1,000个路径复用两个小MP4，不能代表1,000种codec、实际片库吞吐、10万/50万规模或24小时稳定性。快取采用有界edge指纹，无法发现保持同size/mtime/edge的中段修改；Inspect也不是文件系统快照。OS阻塞open/stat不保证被context硬中断。Windows正式隔离探测仍关闭。
