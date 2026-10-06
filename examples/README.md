# API 示例

本目录是 Jelee 自有 API（`/api/v1`）的可运行示例（G49.5）。接口合同以运行实例的
`/api/v1/openapi.json`（仓库中为 `api/openapi.json`）为准，可浏览版本见实例的 `/api-docs`。
所有示例只从环境变量读取地址与凭据，不包含任何密码或令牌。

| 变量 | 用途 |
| --- | --- |
| `JELEE_URL` | 服务根地址，例如 `http://127.0.0.1:8097` |
| `JELEE_USER`、`JELEE_PASSWORD` | 登录账号与密码（Go 示例与 `curl/login.sh`） |
| `JELEE_TOKEN` | `curl/login.sh` 输出的 bearer 令牌（其余 curl 示例） |
| `JELEE_LIBRARY_ID`、`JELEE_ITEM_ID` | `curl/items.sh`、`curl/item-details.sh` 的目标 |

## Go

`go/walkthrough` 只用标准库：登录 → 列出可见媒体库（管理员用 `GET /api/v1/libraries`，其他账号读自己的
库授权）→ 列出第一个库的条目 → 读取第一个条目的详情 → 登出。`go/main.go` 是读取环境变量的薄入口：

```sh
export JELEE_URL=http://127.0.0.1:8097 JELEE_USER=viewer
# 交互输入密码，不写进命令历史（bash）
read -r -s -p 'Password: ' JELEE_PASSWORD; echo; export JELEE_PASSWORD
go run ./examples/go
```

启用第二因素的账号会得到 `ErrSecondFactor`，示例不实现第二步。

## curl

`curl/*.sh` 需要 POSIX sh、curl（7.76+）与 jq。密码经 jq 从环境变量读入并由 stdin 传给 curl，
令牌经 `--header @-` 由 stdin 传入，两者都不会出现在进程参数中。

```sh
# JELEE_URL、JELEE_USER、JELEE_PASSWORD 按上一节设置
JELEE_TOKEN=$(sh examples/curl/login.sh); export JELEE_TOKEN
sh examples/curl/library-grants.sh
JELEE_LIBRARY_ID=<库 ID> sh examples/curl/items.sh
JELEE_ITEM_ID=<条目 ID> sh examples/curl/item-details.sh
sh examples/curl/logout.sh
```

## CI 校验

- `internal/adapter/http` 的 `TestGoExampleWalkthroughPostgres` 在真实 PostgreSQL 上启动测试服务器，
  以普通账号与管理员分别运行 Go 示例，并核对隐藏库内容不出现、会话已撤销；环境有 sh、curl、jq 时
  同一测试也逐个运行 curl 示例。
- `TestCurlExamplesUseDocumentedOperations` 解析每个 curl 调用的方法与路径，必须是 OpenAPI 中的操作。
- `TestExamplesCarryNoCredentials` 拒绝示例中出现字面密码或令牌。
- `internal/architecture` 的 `TestExamplesUseOnlyTheStandardLibrary` 限制 Go 示例只引用标准库；
  `go vet` 与 golangci-lint 以 `./...` 覆盖本目录。
