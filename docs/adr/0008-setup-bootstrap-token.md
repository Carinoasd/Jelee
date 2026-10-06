# ADR 0008：初始设置向导使用一次性引导令牌，而不是来源地址白名单

- 状态：已采纳（E14，2026-10-05）
- 日期：2026-10-06（追记；实现见 `aa6b40e1f0`）
- 相关需求：G18.1–G18.5
- 相关代码：`internal/platform/runtime/setup.go`、`internal/adapter/http/setup.go`、`cmd/jelee-cli/`（CLI 设置）
- 详细流程：[初始设置](../setup-wizard.md)

## 背景

全新实例在创建第一个管理员之前，任何能访问端口的人都可能抢先完成设置。常见做法是只允许环回地址进行设置。

## 决定

1. 每次启动尚未完成设置的实例时，生成一个 32 字节随机数的一次性引导令牌（base64url）。设置了 `JELEE_SETUP_TOKEN_FILE` 时以 0600 权限写入该文件，否则输出到标准错误。
2. 除 `GET /api/v1/setup/status` 外，所有设置向导接口都要求请求头 `X-Jelee-Setup-Token`，以常数时间比较，不符返回 401 `setup_token_invalid`。
3. 设置完成前，实例对设置向导以外的请求一律返回 503 `setup_required`；设置完成后令牌失效，所有向导路径返回 410。
4. 多实例部署时，设置期间只开一个实例，或改用 CLI 完成设置。

## 理由

- **合法的设置者往往不是环回地址。** Docker 端口映射与反向代理之后，管理员的请求在服务看来来自网桥或代理地址；如果把这些地址加入白名单，就等于允许经过代理的整个互联网。
- **令牌证明“能读到服务的输出”**：只有能看到容器日志或令牌文件的人才是部署者，这与网络位置无关。
- **放在自定义请求头中**，浏览器不会自动附带，因此没有跨站请求伪造的问题。

## 代价

- 部署者需要从日志或文件中取得令牌；每次重启会换新令牌。
- 多实例在设置期间需要人工协调。

## 守门

- `internal/adapter/http/setup_test.go`：`TestSetupWizardAPIFlowTokenAndErrors`、`TestSetupGateBlocksEveryRegisteredRoute`、`TestSetupGateRechecksAndFailsClosed`。
- 真实 PostgreSQL：`TestSetupRuntimePostgresWizardOpensGate`。
- [权限矩阵](../permission-matrix.md) P02 行由守门测试对照实现。
