# API 版本与弃用

本文记录 Jelee 自有 API（`/api/v1`）的版本策略、弃用机制与弃用时间线（G49.2）。弃用清单由
`internal/adapter/http/deprecation.go` 的 `apiDeprecations` 表驱动，本文的清单必须与该表一致：
`TestDeprecatedRoutesAreDocumented` 核对每个弃用路由已注册、在 `api/openapi.json` 中标为
`deprecated: true`，并在下方清单中以相同日期列出；反之，清单或 OpenAPI 中多出的弃用项同样失败。

## 版本策略

- 全部自有接口位于 `/api/v1`；第三方客户端兼容层（`/compat`）不在本文范围内。
- 向后兼容的变更（新增路由、新增可选参数、新增响应字段、新增错误码）直接在 `/api/v1` 发布，
  不需要弃用。客户端应忽略不认识的响应字段。
- 破坏性变更（删除或改名字段、改变字段类型或含义、收紧参数、改变状态码或错误码语义、删除路由）
  不在原路由上进行：新行为发布在 `/api/v2` 下的新路由，旧的 `/api/v1` 路由同时进入弃用清单。
- 过渡期：从弃用日期到 Sunset 日期至少 180 天，并且至少跨越一个正式版本。过渡期内旧路由行为不变；
  Sunset 日期之前不会删除。`validateDeprecations` 拒绝过渡期短于 180 天的条目，服务因此无法启动。
- 删除没有替代品的功能同样先弃用；此时 `Successor` 为空，清单的“替代”栏写“无”并说明原因。

## 弃用信号

被弃用路由的每个响应（包括错误响应）都带：

| 响应头 | 格式 | 示例 |
| --- | --- | --- |
| `Deprecation` | RFC 9745 结构化日期（`@` 加 Unix 秒） | `Deprecation: @1790812800` |
| `Sunset` | RFC 8594 HTTP-date | `Sunset: Thu, 01 Apr 2027 00:00:00 GMT` |
| `Link` | 替代路由，`rel="successor-version"` | `Link: </api/v2/system>; rel="successor-version"` |
| `Link` | 弃用说明，`rel="deprecation"`（RFC 9745） | `Link: </api-docs#deprecations>; rel="deprecation"` |

同一操作在 OpenAPI 中标为 `deprecated: true`，说明以 `Deprecated since <日期>; removal no earlier than <日期>.`
开头，并带 `x-jelee-deprecation`（`since`、`sunset`、`successor`）与上述响应头的文档。可浏览的
`/api-docs` 在操作上显示 deprecated 标记，并在“Deprecations”一节列出完整清单。

客户端建议：记录收到 `Deprecation` 头的请求路径并上报；在 Sunset 日期之前改用 `successor-version` 指向的路由。

## 公告流程

1. 新路由（通常在 `/api/v2`）随版本发布，并有自己的合同测试。
2. 同一提交在 `apiDeprecations` 加入旧路由（路由、弃用日期、Sunset 日期、替代路径、英文说明），
   在下方清单加一行，重新生成 `api/openapi.json` 与 `web/src/api/schema.d.ts`。
3. 发布说明（CHANGELOG）列出弃用项、替代路由与 Sunset 日期。
4. Sunset 日期之后的版本才可以删除旧路由，删除时同时移出 `apiDeprecations` 与本清单，并在下方“已移除”记录。

## 弃用清单

目前无弃用项目。

清单行格式如下（填写时去掉行首空格；`TestDeprecatedRoutesAreDocumented` 按此格式解析）：

```text
  | `GET /api/v1/example` | 2026-10-01 | 2027-04-01 | `/api/v2/example` | 说明 |
```

| 路由 | 弃用日期 | Sunset 日期 | 替代 | 说明 |
| --- | --- | --- | --- | --- |

## 已移除

无。
