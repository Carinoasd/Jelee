# 密钥泄露检查与处理

本文对应 G01.7：禁止提交密钥、大媒体、数据库数据与构建产物，并说明如何检查泄露、发现后如何处理。原则：**一旦推送，就当作已经泄露**——先轮换，再清理；未经拥有者授权不得重写已推送的历史。

## 防线

| 层 | 工具 | 范围 |
| --- | --- | --- |
| 忽略规则 | `.gitignore` 第 5、6 组 | `.env*`（保留 `.env.example`）、`*.pem`、`*.key`、`*.p12`、`*.pfx`、`*.jks`、`credentials.json`、`service-account*.json`、`.netrc`、数据库文件，见[工具链](toolchain.md#被忽略的产物) |
| 文件类型 | `make gitignore-check` | 已跟踪与未忽略文件中的密钥扩展名、带内容的 PEM 私钥块、数据库与归档，见[二进制例外](binary-allowlist.md) |
| 内容扫描 | `make secret-scan`（`tools/secretscan`，`make lint` 的一部分） | 全部已跟踪文件 |
| 提交前 | `.githooks/pre-commit` 中的 `secretscan -staged` | 暂存区内容（`make hooks` 启用） |
| CI 新提交 | `commit-hygiene` 作业的 `make secret-scan-range` | 本次 push／PR 新增提交的每一行新增内容 |
| 全历史 | `make secret-scan-history` | 所有引用可达的全部提交（手动运行） |

### 检测内容

`tools/secretscan` 只用 Go 标准库实现，检测：PEM 私钥（含 JSON 中转义的私钥，如 Google Cloud 服务账号文件）、AWS 访问密钥 ID 与 Secret、GitHub（经典与细粒度）、GitLab、Slack 令牌与 Webhook、Google API Key、Stripe live key、npm、Telegram Bot、Anthropic 与 OpenAI API Key、JWT、URL 中内嵌的密码（`scheme://user:password@host`），以及赋给 `secret`／`password`／`token`／`api_key`／`access_key`／`private_key`／`credential` 等名称的高熵值（香农熵 ≥ 3.5 位/字符、至少两类字符且含数字、仅可打印 ASCII，排除占位符、标识符、路径与文件名）。

输出只有 `路径:行号: 规则 [指纹]`，**从不打印匹配到的值**，因此日志与 CI 输出可以安全保存。指纹是规则名与值的 SHA-256 前 16 位十六进制，不随路径、行号变化。

### 允许清单

测试必须使用假值时（例如脱敏测试故意输入假的数据库地址），把指纹登记在 `tools/secretscan/allowlist.txt`，每行：

```text
<指纹|*> <规则|*> <路径 glob> <理由>
```

- 优先登记具体指纹；`*` 指纹只用于“目的就是输入假密钥”的测试文件（例如日志脱敏测试），并写明理由；不允许 `* *`。
- `make secret-scan` 对不再匹配任何内容的条目报错，名单不会过期。
- `tools/secretscan/history-allowlist.txt` 只由 `-history` 读取，登记已经无法修改的旧历史中的发现及保留理由。2026-10-06 全历史扫描（24838 个非合并提交）发现的 27 处全部位于分叉基线 `52a680c578` 之前的上游 Jellyfin／Emby 提交：上游客户端代码内置的元数据服务 API Key（TMDb、TheTVDB、Fanart.tv、Last.fm、Rotten Tomatoes、TheAudioDB，由上游持有与轮换）和 SharpCifs 注释中的示例 URL；这些文件已不在 Jelee 工作树中，Jelee 也不使用这些密钥。
- **真实密钥永远不能加入允许清单**，只能轮换。

## 发现泄露后的处理步骤

1. **停止扩散**：不要把含密钥的分支继续推送、合并或开 PR；已经开的 PR 先转为草稿。不要在 issue、PR 评论、聊天或日志中贴出密钥值，引用时只用 `secretscan` 的路径、行号与指纹。
2. **立即轮换**：在密钥的签发方撤销旧值并生成新值，更新部署环境（环境变量或 `*_FILE` 指向的文件，见[部署](deployment.md)）。常见项目：
   - 数据库密码：`ALTER ROLE … PASSWORD …`，更新 `JELEE_DATABASE_URL`／`JELEE_DATABASE_URL_FILE`，重启服务。
   - Webhook 主密钥 `JELEE_WEBHOOK_MASTER_KEY`：它封存 Webhook 密钥与 TOTP 种子，轮换前按[Webhook](webhooks.md)与[双因素验证](two-factor.md)的说明重新封存或让用户重新登记。
   - 用户令牌、应用程序密码：管理员撤销对应会话或应用程序密码。
   - 第三方 API 令牌（GitHub、云服务等）：在对应控制台撤销，并检查审计日志中泄露期间的使用记录。
3. **评估影响**：确认密钥从何时可见（`git log -S` 或 `secretscan -range` 找到引入提交，`git branch -r --contains <提交>` 看到达了哪些分支），公开仓库要假定已被抓取。检查服务端审计与访问日志。
4. **清理工作树**：在新提交中删除密钥，改为从环境变量或被忽略的本地文件（`.env`、`.testdata/`）读取；如是测试需要，改用明显的假值。确认 `make secret-scan` 通过。
5. **历史处理需要授权**：已推送的历史**不得未经拥有者明确授权重写**（不做 `git filter-repo`、强制推送）。轮换之后旧值已经失效，通常不需要重写历史；若拥有者决定重写，须先通知所有协作者暂停推送，记录受影响的分支与提交，完成后让各克隆重新获取，并联系 GitHub 支持清除缓存视图。未推送的本地提交可以自行用 `git commit --amend` 或交互式变基移除。
6. **记录**：在 PR 或内部记录中写明发现时间、影响范围、轮换完成时间与后续措施（新增检测规则、补充允许清单理由等），不写密钥值。

## 本机命令

```sh
make secret-scan                                   # 全部已跟踪文件
make secret-scan-range COMMIT_RANGE=origin/master..HEAD   # 尚未合并的提交
make secret-scan-history                           # 全部历史（数十秒）
go run ./tools/secretscan -staged                  # 暂存区
```
