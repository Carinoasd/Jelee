# 探测快取数据库契约：3C2A 验证

源码提交 `3d8842be1ebdc990d81f6b98d091b360120740d5`，基于[隔离探测 PR #5](https://github.com/MoYuanCN/Jelee/pull/5) 的冷下载修复 `b572b6dd7c9f51386d651ee159237a125ba806da`。日期2026-10-01，Asia/Taipei。

本段交付[domain/PG契约](probe-cache.md)、schema 4、安全 Inspect 和可信身份计算。扫描仍只盘点：worker、配置开关、HTTP/CLI 快取操作和真实1,000→0→17验收尚未交付。用户要求本小段完成、推送并提PR后停止，方便切模型。

## 来源与完整回归

两平台使用同一 Git tree `019dd65c85dc205ba0483855e53c4de58d61fc15`；archive SHA256 `5e652cef925c8493359c9d9beac49c2442a4598afa61107dfd0ae764b7bf766e`。源码提交 tree 与此一致。测试使用自建临时快照，工具/SDK仅复用项目内固定版本，测试结束清理自建目录。

| 验证 | 实际结果与限制 |
| --- | --- |
| [Windows完整快照摘要](evidence/probe-cache-windows.txt) | 三命令build、完整vet/test、required真实素材/parser、固定工具与CLI通过。66个skip实例：54个PG（未配置，含子测试）+12个symlink权限；不算PG验收。Windows race因缺兼容C编译器未执行 |
| [原生Linux完整race摘要](evidence/probe-cache-linux-race.txt) | 三build、完整vet/race/test、真实PG、required素材/固定工具/Python安全wrapper均通过。PG39.186秒，78.8%。6个skip：5个需独立kernel/protected-profile fixture的测试+1个optional ffprobe；后者随后required执行通过。前5项的独立容器实测保留在[前段证据](probe-verification.md)，本段没有重新执行该容器目标 |
| [独立PG完整日志](evidence/probe-cache-postgres.txt) | 专用PostgreSQL16.15、每fixture独立schema、Linux GCC15 race；48个顶层测试（32个probe），37.660秒、0 skip |
| [PG覆盖率与源码SHA摘要](evidence/probe-cache-postgres-summary.json) | 5个新增probe实现合计556/654 statements=85.015%；整包78.8%。实际profile SHA保存；原profile留在忽略的.testdata，没有修改计数 |
| [Windows](evidence/probe-cache-parser-fuzz-windows.txt)/[Linux](evidence/probe-cache-parser-fuzz-linux.txt) fuzz | 有界parser→domain序列化通过206559/53326次执行，实际11.243/12.323秒；实际媒体codec验收范围仍沿前段 |
| [源与门禁核对](evidence/probe-cache-checks.txt) | 31个Go文件gofmt无输出；diff、增量品牌及ignore检查通过；原始需求、LICENSE和000001–000003迁移未变 |

Windows/Linux摘要保留每个命令、package结果和全部skip，省略常规单项PASS/RUN；完整原日志的SHA在摘要中，原日志保留在本机忽略目录。Linux测试在原生临时filesystem运行，不将DrvFS mode/symlink行为当作Linux权限证明。测试不使用用户媒体或数据库。

关键现有package覆盖率：domain两平台98.8%，probe Windows93.2%/Linux93.7%。新PG probe五文件分别为cache87.619%、commit86.275%、policy86.735%、phase84.574%、maintenance81.988%；合计达85%不代表每个文件均达85%。CLI73.9%、Linux proberuntime76.2%、旧runtime52.5%及整包PG78.8%仍不足，完整G30门槛未通过。前段真实容器覆盖率基于前段源码，未拿旧计数拼接本段的新identity文件。

## 真 PostgreSQL 验证内容

- policy exact replay和有界scope/identity admission；不可变身份每个字段/摘要重新核对。
- inventory完成后才可Begin；phase运行后禁止再Save inventory；连续keyset分页、hit prefix提交、miss单项租约和cursor/revision重放防护。
- 实际1,000和8,192个cache rows验证容量/精确JSONB字节；这是数据库fixture，**不是1,000次真实ffprobe**。
- global最后一个lease槽的并发竞争，path/library隔离，scope/identity容量，parent/file reclaim、sequence ABA、取消和交易中到期的完整回滚。
- library/item首次重建与Begin重放，root/item/source mapping变化、TTL/size/mtime/edge变化在提交前重查；旧结果不能覆盖新scope。
- ready按`jsonb::text`计价。合法compact122,504 bytes在PG展开为131,344 bytes时，保存固定`probe_metadata_limit`负结果，metadata为空、释放128KiB预留、只计2KiB行成本。
- expiry及LRU查询用10,000 rows/两库的实际EXPLAIN检验索引页，不使用全表排序；一次Sweep/容量淘汰最多128，Acquire可另外清最多8个失效child。
- 坏媒体固定负结果、negative TTL重试和新工具身份miss；changed/input unavailable独立计数。runtime故障与取消不可变成坏媒体记录。
- 终态parent、租约释放/恢复/历史清理、活跃sibling保留、zero-row quota scope和tool identity回收后，精确quota与真实row总额一致。
- live admin/session撤销/过期与audit actor；撤权过程中没有generation/audit部分提交。
- 真实PG拒写、deferred COMMIT失败、资源错误、写入后取消、closed pool及存储identity不一致均安全固定分类、零部分结果；完整私有数据库快照核对回滚，恢复后retry通过。Sweep/Release/容量淘汰在quota已更改后拒删，同样完整回滚。
- 4→3→0→4；down4保留原catalog、media_sources、jobs/inventory/baseline的完整私有快照。有效running probe parent阻止down；迁移失败dirty行为见[回滚说明](probe-cache.md#升级与回滚)。

## 重现与未完成项

项目固定Go1.27.1。普通入口为Windows `scripts/make.ps1 build/test`，Linux `make build test-race test-integration`；真实PG测试须配置专用测试DSN并设`JELEE_REQUIRE_INTEGRATION=true`，不可使用用户数据库。源码快照脚本保留在本机.testdata；Windows私人ACL验收需正常用户token和足够短的临时路径，既有长路径限制仍保留在[前段报告](probe-verification.md)。

本段没有启用后台cache清理或生产probe扫描，没有进程池/跨实例worker实际执行计数，也未完成Windows隔离、特殊codec、MediaInfo/mkv、完整性能/24小时、前端和发布。下一段方案见[分段计划](probe-cache-plan.md)。

[前段PR5远端CI](https://github.com/MoYuanCN/Jelee/actions/runs/36779151565)三功能job通过，完整品牌job失败。本段PR的远端CI在推送后由GitHub执行，本报告只声明以上已执行的本地证据；不能把既有完整品牌失败改成全绿。
