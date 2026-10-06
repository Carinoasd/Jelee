# ADR 0003：统一权限过滤器在 SQL 中下推，隐藏与不存在同样回应

- 状态：已采纳
- 日期：2026-10-06（追记；决定随 `632005d430`、`294cf76ced`、`cbdd2f4250`、`dd42a2933e` 逐步落地）
- 相关需求：G48.2、G48.3、G48.8、G48.10（另涉及 G47 客户端管控、G48.5 网络限制、G48.6 分享、G48.9 开发者模式）
- 相关代码：`internal/adapter/postgres/visibility.go`（唯一的述词来源）、`internal/access/`、`internal/platform/config/access.go`
- 详细规则：[访问控制](../access-control.md)；各角色可调用的路由：[权限矩阵](../permission-matrix.md)

## 需求原文

> G48.2 所有列表、搜索、详情、图片、字幕/音轨、播放信息、统计、Webhook 载荷都必须经过统一权限过滤器；禁止先查询再在展示层过滤。
>
> G48.3 ……直接按 ID 访问返回 404（而非 403 暴露存在性，策略需可配并默认 404）。
>
> G48.8 权限过滤必须在 SQL 层下推（索引、JOIN/EXISTS 条件），禁止全表捞取后内存过滤；提供查询计划证据与单请求 SQL 计数断言。

## 决定

1. **只有一处述词来源。** 所有面向用户的目录读取，都通过 `visibility.go` 中的 SQL 述词（`libraryVisibleSQL`、`itemVisibleSQL`、`contentVisibleSQL`、`requestHiddenSQL`、`networkHiddenSQL` 等）在同一条语句里把行绑定到调用者。新的规则只能加在这个文件里。
2. **优先顺序固定**：请求限制（网络规则、客户端管控的 `restrict_libraries`）→ 媒体库授权（管理员看全部；分享访客只看分享范围）→ 最近的条目允许/拒绝规则 → 标签屏蔽 → 分级上限。下层规则不能放宽上层。
3. **每请求只多一个参数。** 请求相关的部分（客户端地址属性、客户端管控留下的媒体库集合）作为一个 jsonb 参数传入，PostgreSQL 每条语句只计算一次。
4. **隐藏与不存在同样回应。** 看不到的条目与不存在的条目返回相同的状态码和响应体，默认 404，可通过 `JELEE_HIDDEN_CONTENT_STATUS`（或配置 `access.hiddenStatus`）改为 403。管理员专属路由在查询之前就拒绝普通用户（403），所以存在与否同样不可区分。
5. **兼容层没有自己的查询**，经由同一个目录服务读取，因此自动受同一个过滤器约束。

## 理由

- **展示层过滤会泄漏。** 先查后滤会把隐藏条目的数量、分页位置、排序结果暴露给调用者，也容易在新接口里漏掉过滤。在 SQL 里下推后，看不到的行从一开始就不会被读出。
- **性能可预期。** 条件用 JOIN/EXISTS 和索引表达，不会全表读取再在内存中过滤；单请求的 SQL 条数固定。
- **一处修改、处处生效。** 规则只写一次，守门测试保证没有第二份副本，新增接口不需要记得“再过滤一次”。
- **默认 404 不暴露存在性**，与需求一致；需要显式 403 的部署可以改配置，但两种情况仍然不可区分。

## 代价

- 每个目录查询都必须接入述词，写 SQL 时比在 Go 里过滤更繁琐。
- 管理员默认不受条目规则、分级、标签限制（E12），需要时开启 `restrict_admins`；开发者模式的 `relax_permission_strict` 只能让这类管理员恢复“看得到一切”，不影响非管理员（G48.9）。
- 备份、旧库迁移、版本合并、个人数据导出等不面向浏览的代码会直接读授权表，作为整文件例外登记在守门测试中。

## 守门

- `internal/adapter/postgres/visibility_guard_test.go`：`TestVisibilityPredicateHasOneSource` 扫描非测试源码，授权与规则表只能出现在白名单位置；`TestVisibilityGuardDetectsCopiedPredicate` 证明守门本身能发现复制的述词。
- `internal/adapter/http/access_leak_test.go`：`TestAccessLeakRouteTableIsComplete` 要求每条注册路由都分类；`TestAccessLeakHiddenContentPostgres` 在真实 PostgreSQL 上，对每种隐藏机制、默认 404／显式 404／配置 403 三种模式遍历全部路由，断言零泄漏。
- `content_access_plan_test.go`：`TestContentAccessPlanPostgres`（EXPLAIN 证据）与 `TestContentAccessStatementCountPostgres`（单请求 SQL 条数）。
- 启动自检缺少过滤器时报 `access_filter_missing`（`TestSelfCheckRefusesAMissingGate`）。
- [权限矩阵](../permission-matrix.md) 的守门测试保证文档中的角色表与路由实现一致。
