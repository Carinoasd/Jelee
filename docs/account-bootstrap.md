# 本地账户初始化与密码恢复

这两个命令面向持有 Jelee 数据库凭据的受信任运维人员。它们直接连接数据库，不需要现有 HTTP 会话，也不创建登录令牌。日常用户改密走需要认证的 HTTP 流程。

先按[快速开始](quickstart.md)完成构建与数据库配置，再运行 `jelee-migrate up`。当前命令要求 schema 4 且迁移状态 clean；不会自动创建数据库或执行迁移。数据库 URL 通过 `JELEE_DATABASE_URL_FILE` 或 `JELEE_DATABASE_URL` 配置，二者只选其一。

## 创建首个管理员

```text
jelee-cli account bootstrap --name NAME [--display-name TEXT] [--locale zh-CN] --password-stdin
```

默认语言为 `zh-CN`，也接受 `zh-TW`、`en-US`、`ja-JP`。成功时创建启用的管理员。仅当数据库中没有启用且未删除的管理员时允许执行；该条件在数据库事务中检查，并发执行也不能绕过。

已有活动管理员时返回 `account_conflict`。第一阶段 `provision --admin` 建立的账户即使尚无密码，也属于活动管理员；应使用下节的密码恢复命令。

## 为已有账户设置或恢复密码

```text
jelee-cli account set-password --name NAME --password-stdin
```

按数据库中大小写不敏感的名称匹配未删除账户，设置新密码、清除登录失败次数与临时锁定，并撤销该用户的所有旧会话。它保留用户的管理员、禁用、显示名、语言与媒体库权限；禁用账户不会因此启用，已删除账户返回 `account_not_found`。

这是本地数据库运维恢复路径，允许在没有旧密码的情况下重设。数据库审计事件为 `user.password_reset_local`，操作者为空以标识本地运维路径，记录密码已变更、会话已撤销的布尔状态，不写入新旧密码或散列。首个管理员创建对应 `user.bootstrapped` 事件。

## 从标准输入传入密码

命令必须显式带 `--password-stdin`，不提供 `--password`、密码环境变量或文件路径参数。不要把密码字面值放进命令历史或进程参数。可由密码管理器通过管道传入 UTF-8 字节；交互输入示例如下。

PowerShell 7.2+（在启动 CLI 前完成隐藏输入）：

```powershell
$savedOutputEncoding = $OutputEncoding
$OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$accountPassword = Read-Host '新密码' -MaskInput
try {
    $accountPassword | .\bin\jelee-cli.exe account bootstrap --name admin --display-name '管理员' --password-stdin
} finally {
    $OutputEncoding = $savedOutputEncoding
    Remove-Variable accountPassword
}
```

Bash：

```bash
unset account_password
IFS= read -r -s -p '新密码: ' account_password
builtin printf '\n' >&2
builtin printf '%s' "$account_password" | ./bin/jelee-cli account bootstrap --name admin --password-stdin
unset account_password
```

恢复密码时，把示例中的 `bootstrap` 改为 `set-password`，去掉可选 `--display-name` 和 `--locale`。这些示例避免把密码放进 CLI 参数或环境变量，但 shell 和 Go 进程仍会短暂持有明文，移除变量不构成可证明的内存清零。

输入规则：

- 密码为 12–1024 个有效 UTF-8 字节；不裁剪空格，不做 Unicode 归一化。
- 一次读取整个标准输入，直到 EOF。只移除**一个**末尾 LF 或 CRLF；其余字节保持原样，包括额外换行和单独的 CR。
- 最多读取 1027 字节以区分 1024 字节密码、可选 CRLF 与超长输入。超过允许长度时，不执行散列，也不连接数据库。
- 输入格式错误仅返回固定代码，不把内容写入 stderr。

## 配置、输出与退出码

两命令加载同一服务配置，密码工作量来自 `accounts.passwordMemoryKiB`、`passwordIterations`、`passwordParallelism` 和 `passwordConcurrency`，也可由对应 `JELEE_PASSWORD_*` 环境配置覆盖。这些变量只承载工作量数字，不承载密码。即使 HTTP 账户开关关闭，本地 CLI 仍可执行，但始终校验散列参数边界。默认参数和资源限制见[密码安全说明](password-security.md)。

成功时 stdout 只输出一条 `User` JSON 摘要，包含 ID、名称、显示名、语言、管理员/禁用/隐藏状态及创建时间，不含密码、散列、数据库连接信息或会话令牌。发生错误时 stderr 输出固定代码，未知底层错误不直接回显。

| 退出码 | 含义 / 常见代码 |
| --- | --- |
| 0 | 数据库操作成功，已输出用户摘要 |
| 1 | 配置、存储或输出失败；如 `account_configuration_invalid`、`account_database_unavailable`、`account_conflict`、`account_not_found`、`account_output_failed` |
| 2 | 用法或输入错误；如固定 usage、`account_password_invalid`、`account_input_invalid` |
| 124 | `account_timeout` |
| 130 | `account_cancelled` |

取消会检查上下文；等待或执行散列的细节遵循密码模块的取消约定。阻塞的标准输入/输出如果支持关闭，会在取消时关闭以释放管道；正常完成不会关闭它们。自定义的普通 Reader/Writer 必须自行提供可取消的 I/O，stderr 也需保持可写。

数据库提交后若 stdout 写入失败或收到取消，操作可能已经完成。不要仅凭非零退出码判断事务一定未提交；先核对账户状态和审计，再决定是否重试。密码恢复会再次撤销会话，初始化重试可能返回冲突。

## 验证范围

CLI 单元测试通过可注入端口验证分派、配置映射、真正 Argon2 散列与原字节验证、长度与换行边界、错误脱敏、数据库关闭，以及取消时释放 stdin/stdout 管道。Windows 专测与 Linux race 均通过。数据库事务、最后管理员保护、审计和旧会话撤销由 PostgreSQL 集成测试另行验证；CLI 的模拟存储测试不替代这些检查。
