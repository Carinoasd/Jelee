# Go 服务快速开始

此流程用于隔离评估当前 Go 实现。旧数据迁移与第三方客户端协议尚未实现；请使用新建的测试数据库和已有的小型测试视频。

## 1. 引导与构建

Windows 需要已有 PowerShell 7.2+、curl；Linux 需要已有 Python 3.9+ 与 GNU Make。下载工具只进入仓库的 `.tools/`。

```powershell
pwsh -NoProfile -File scripts/make.ps1 bootstrap
pwsh -NoProfile -File scripts/make.ps1 tools-verify
pwsh -NoProfile -File scripts/make.ps1 build
```

Linux 对应命令为 `make bootstrap tools-verify build`。Windows 程序位于 `bin/*.exe`，Linux 位于 `bin/`。

### 可选：离线验证 NFO

`nfo validate` 不加载服务配置，也不连接数据库，可在配置 PostgreSQL 前运行。它只读取指定文件，输出 JSON 验证摘要，不写回 NFO。

```powershell
.\bin\jelee-cli.exe nfo validate --root 'D:\TestMedia' --file 'Example/movie.nfo'
```

```sh
./bin/jelee-cli nfo validate --root /absolute/path/to/test-media --file Example/movie.nfo
```

`--root` 必须为绝对路径；`--file` 必须为根目录内、以 `/` 分隔的相对路径，Windows 也使用 `/`。路径逃逸与根外符号链接会被拒绝。默认读取上限为 8 MiB（8,388,608 字节），可用 `--max-bytes` 设置为 1 至 33,554,432 字节，最高 32 MiB。主流程使用 15 秒 context 期限，Ctrl+C 可取消。

普通文件读取、解析及 CLI 堵塞的 stdout 管道支持取消；网络文件系统若阻塞在 `OpenRoot`、`OpenFile` 或 `Stat` 的底层调用中，不保证立即中断，错误输出也假设 stderr 可写，因此 15 秒不是所有环境下的硬退出时限。

JSON 仅包含 `valid`、XML 根元素类型 `root`、`encoding`、`originalBytes`、`entries` 和 `issues`；不输出媒体标题、元数据正文或本地/远端路径。出现 warning 时仍可有效；出现语义 error 时 `valid` 为 false。读取或解析失败时，stderr 输出错误码，不输出成功摘要。

| 退出码 | 含义 |
| --- | --- |
| 0 | 验证有效，可能有 warning |
| 1 | 读取、编码、XML 解析或输出失败 |
| 2 | 命令用法、缺少参数或字节上限参数错误 |
| 3 | XML 已解析，但存在语义 error |
| 124 | 检测到 15 秒 context 期限已过 |
| 130 | 收到取消请求 |

此命令验证单个 NFO，不代表扫描导入、写回或完整兼容验收已经完成。

## 2. 配置独立 PostgreSQL

使用你已管理的 PostgreSQL 实例创建独立数据库与专用用户。当前测试证据使用 PostgreSQL 16.15；引导不安装数据库。配置仅接受 PostgreSQL，不会回退到 SQLite。

将连接 URL 放入权限受限的文本文件，例如 `postgres://用户名:密码@数据库主机:5432/jelee?sslmode=verify-full`；将示例值换成实际配置。默认优先使用经验证的 TLS；只有隔离的本机测试数据库才采用 `sslmode=disable`。

PowerShell：

```powershell
$env:JELEE_DATABASE_URL_FILE = 'C:\secure\jelee-database-url.txt'
$env:JELEE_LISTEN = '127.0.0.1:8097'
$env:JELEE_ALLOWED_HOSTS = 'localhost,127.0.0.1,::1'
```

Linux：

```sh
export JELEE_DATABASE_URL_FILE=/absolute/path/to/jelee-database-url.txt
export JELEE_LISTEN=127.0.0.1:8097
export JELEE_ALLOWED_HOSTS=localhost,127.0.0.1,::1
```

也可直接提供 `JELEE_DATABASE_URL`，但它与 `JELEE_DATABASE_URL_FILE` 只能选择一个。程序不会自动加载 `.env`；`.env.example` 用于列出可配置项。可选 `JELEE_CONFIG` 指向 JSON 配置文件，环境变量覆盖对应文件设置，未知 JSON 字段会报错。

## 3. 迁移与自检

```powershell
.\bin\jelee-migrate.exe up
.\bin\jelee-migrate.exe status
.\bin\jelee-cli.exe doctor
```

Linux 使用 `./bin/jelee-migrate` 和 `./bin/jelee-cli`。doctor 检查配置、PostgreSQL 连接与迁移状态、媒体库根、固定 ffprobe 哈希、磁盘与 inode、监听与可信代理、暂存／日志目录权限、隐私开关与开发者模式；任一项 fail 时结束码非 0，`--json` 输出机器可读格式。需要回报问题时用 `jelee-cli diag export --out jelee-diag.zip` 导出脱敏诊断包。错误码与修复步骤见[故障排查](troubleshooting.md)；一致性检查与自愈（G50.3–G50.4）尚未实现。

本段 binary 要求 clean schema 4；快取数据库契约已加入，扫描仍只盘点。升级与降版限制见[快取说明](probe-cache.md#升级与回滚)。

迁移锁等待上限 5 秒，数据库语句上限 30 秒；取消在迁移安全边界处理，不保证正在执行的语句立即终止。遇到 dirty 状态先检查数据库和备份，不要自动重试或手工清除标记。

## 4. 创建评估会话与登记视频

以下命令创建本地管理员和 native 会话。执行者拥有数据库凭据，属于受信任的本地运维人员。

```powershell
.\bin\jelee-cli.exe provision --name eval-admin --admin --native
.\bin\jelee-cli.exe import-video --library Test --root 'D:\TestMedia' --file 'sample.mp4' --title Example
```

修改媒体根路径和文件名为实际存在的文件。Linux 示例根路径为 `/absolute/path/to/test-media`。`--root` 必须绝对路径，`--file` 必须是根内相对路径。登记操作只写目录数据库，不修改视频；重复登记失败并回滚该事务。

`provision` 返回随机 Bearer token，只显示一次，24 小时到期；数据库仅保存 SHA256 摘要。每次命令创建新用户，名字须唯一。去掉 `--native` 会创建 web 会话，该会话不能直投。此命令保留为受信任的本地评估入口，创建的用户尚无密码。密码登录与管理 API 请使用[账户初始化指南](account-bootstrap.md)；已有账户可执行 account set-password。尚无管理 UI，该命令不能代表第三方客户端兼容认证已经完成。

保存 `import-video` 返回的 **source ID**，它用于流式 URL。目录中的 item ID 是另一种 ID。

## 5. 显式启用并启动

目录和直投默认关闭。启用直投必须同时启用目录：

```powershell
$env:JELEE_ENABLE_CATALOG = 'true'
$env:JELEE_ENABLE_DIRECT = 'true'
.\bin\jelee.exe
```

Linux：

```sh
JELEE_ENABLE_CATALOG=true JELEE_ENABLE_DIRECT=true ./bin/jelee
```

在第二个终端访问 [健康检查](http://127.0.0.1:8097/healthz)、[依赖就绪检查](http://127.0.0.1:8097/readyz)、[API 说明](http://127.0.0.1:8097/api-docs)。API 列表仅是当前已实现接口。

Windows 请求示例（令牌与 source ID 使用刚才输出的值）：

```powershell
$token = Read-Host '粘贴本次 native 会话令牌'
$sourceId = Read-Host '粘贴登记输出的 source ID'
Invoke-RestMethod -Uri 'http://127.0.0.1:8097/api/v1/items' -Headers @{ Authorization = "Bearer $token" }
Invoke-WebRequest -Method Head -Uri "http://127.0.0.1:8097/api/v1/sources/$sourceId/stream" -Headers @{ Authorization = "Bearer $token" }
```

直投只返回原文件字节；没有网页播放器。native token 的持有者仍然可以自行编写客户端获取字节，应按敏感凭据保管。

## 6. 测试与退出评估

```powershell
pwsh -NoProfile -File scripts/make.ps1 test
```

数据库集成测试另需设置 `JELEE_TEST_DATABASE_URL` 指向专用 `jelee_test` 数据库，且运行身份能够为每次测试创建独立 schema：

```powershell
$env:JELEE_REQUIRE_INTEGRATION = 'true'
pwsh -NoProfile -File scripts/make.ps1 test-integration
```

Linux 使用 `make test-integration test-race`；race 需要已有 C 编译器。缺失测试数据库不能当作数据库测试通过。

回退功能时，关闭 `JELEE_ENABLE_DIRECT` 与 `JELEE_ENABLE_CATALOG` 后重启，保留数据库与原媒体。`jelee-migrate down --i-understand` 每次降一个版本；004 down 丢失派生快取/phase/身份/配额，保留既有账户/catalog/jobs/inventory/baseline。先停止/释放 probe workers；若保护拒绝 down，迁移会保持 dirty，须检查实际状态再处理，不可自动 force。继续降003会丢失jobs/inventory/baseline。降版仅用于可丢弃的测试库，或经过备份与明确回退评审的数据库；运行binary必须匹配目标schema。该命令不会把新数据转换回旧服务端数据库。
