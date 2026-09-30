# 性能诊断结果

把管理员与普通用户查询分开，以库 ACL 作为 JOIN 入口，每个授权库先执行有界分页，再合并排序。

| 指标 | 修改前 | 修改后 |
| --- | --- | --- |
| 输入 items | 10,002 | 10,002 |
| 受限用户可见条目 | 1 | 1 |
| 实际访问 items 行数 | 10,002 | 1 |
| 单次 EXPLAIN 执行时间 | 2.683ms | 0.164ms |

查询回归测试直接 EXPLAIN 实际 `listItemsSQL`，并断言访问行数不超过 50，避免使用过期的复制 SQL。日志：[基线](evidence/postgres-baseline.txt)、[修正后](evidence/postgres-optimized.txt)。这证明本样本消除了不可见大库扫描，不证明普遍延迟比例或 P95 目标。

Direct Play 16KiB 微基准为 65,379 ns/op、250.60 MB/s、58,523 B/op、71 allocs/op。它使用本地临时文件与内存响应器，包含测试请求分配；不能替代真实网络吞吐、首字节或播放器 Seek 测试。

只读 NFO adapter 与校验 CLI 已提交并通过 Windows/Linux 测试；尚无 NFO 批量导入/导出或并发写入的性能证据。扫描、图片、TMDB、Webhook 与前端仍未实现。原需求性能场景、CPU 降低、权限开销 ≤10%、大规模内存与 24 小时稳定性均未验收。
