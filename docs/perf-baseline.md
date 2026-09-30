# 性能诊断基线

这是新 Go 实现的局部查询诊断，不是旧服务完整性能基线。

Go 1.27.1 / Linux amd64，WSL Ubuntu，PostgreSQL 16.15 容器，10,002 条 items（受限用户仅能访问 1 条）。旧查询实际访问 10,002 条 items、移除 10,001 条，保存的单次 EXPLAIN 执行时间 2.683ms。

证据：[postgres-baseline.txt](evidence/postgres-baseline.txt)。缺少稳定重复样本、冷热状态控制、机器负载基线与全部 12 种工作负载，不满足完整 G26 性能基线验收。
