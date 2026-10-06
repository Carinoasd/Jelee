# 二进制与大文件例外

G01.4b／G01.4c：`make gitignore-check`（`tools/gitignore-check`）对**全部已跟踪文件**以及未被忽略的未跟踪文件逐个检查，发现下列任一类型时，必须由本页表格中的一行放行，否则 CI 失败：

| 类型 | 判定 |
| --- | --- |
| `binary` | 前 8000 字节含 NUL（与 Git 判定二进制的方式相同） |
| `large` | 大于 1 MiB（1048576 字节） |
| `archive` | 扩展名 `.zip .gz .tgz .xz .bz2 .zst .7z .rar .tar .deb .rpm .msi .jar` |
| `media` | 扩展名 `.mp4 .mkv .webm .avi .mov .m4v .mp3 .flac .wav .m4a .ogg .opus .m2ts .iso` |
| `database` | 扩展名 `.db .sqlite .sqlite3 .dump` |
| `key` | 扩展名 `.pem .key .p12 .pfx .jks .keystore` |
| `executable` | ELF／PE／Mach-O 文件头，扩展名 `.exe .dll .so .dylib`，或带可执行位却没有 `#!` 开头的文件 |

以下情况**不能**用本表放行：位于生成目录（`.tools/`、`.bin/`、`.testdata/`、`.testfixtures/`、`.cache/`、`.gocache/`、`node_modules/`、根目录 `data/`、`bin/`、`dist/`、`tools/vendor-downloads/`）下的文件；违反 `.gitignore` 却仍被跟踪的文件（应改规则或取消跟踪）；带密钥内容的 PEM 私钥块。密钥内容另由 `make secret-scan` 检查，见[密钥泄露处理](secret-leak-response.md)。

## 表格格式与门禁规则

- 第一列是反引号包住的路径模式（Go `path.Match` 语法，`*` 不跨目录），按仓库相对路径匹配。
- 第二列是允许的类型，逗号分隔；文件的每一种类型都必须被同一行或其他行覆盖。
- 第三列是**单文件上限**（字节）；超过即视为未放行。
- 后三列（数量与体积、用途、来源与批准）不得为空；数量与体积供审阅，增删文件时同步更新。
- 某一行若不再匹配任何已跟踪文件，门禁失败，提示删除该行，避免名单过期。

新增二进制或大文件时，先确认它确实需要入库（工具、下载物、测试素材与数据库一律放在被忽略目录，见[工具链](toolchain.md#被忽略的产物)），再在下表加一行并在同一提交中说明理由。

## 当前例外

| 路径模式 | 类型 | 单文件上限（字节） | 数量与体积（2026-10-06） | 用途 | 来源与批准 |
| --- | --- | --- | --- | --- | --- |
| `web/e2e/__screenshots__/desktop/*.png` | binary | 262144 | 32 个，合计 2316130 字节，最大 122235 | 桌面视口视觉回归基线（16 个关键页面 × 亮／暗） | 本项目 Playwright 截图（G34.6），只能经 `make web-visual-update` 更新并人工确认；见[前端 ADR](frontend-adr.md) |
| `web/e2e/__screenshots__/mobile/*.png` | binary | 262144 | 32 个，合计 1482056 字节，最大 62108 | 手机视口视觉回归基线 | 同上 |
| `api/openapi.json` | large | 4194304 | 1 个，1718263 字节 | 由路由规格生成的 OpenAPI 文档，前端类型与文档测试以它为准 | `make openapi` 生成，`make openapi-check` 校验与代码一致；见 [API 参考](api-reference.md) |
| `web/src/api/schema.d.ts` | large | 4194304 | 1 个，1168778 字节 | 由 OpenAPI 生成的前端类型 | `npm run api:generate`（`web` 工作区）生成，`npm run api:check` 校验；见 [API 参考](api-reference.md) |

原仓库跟踪的图标、静态资产等随上游 C# 源码树一并移出工作树（见[许可证与来源](LICENSE-COMPLIANCE.md)），当前工作树中没有其他二进制；它们仍可从 Git 历史与回滚标签 `upstream-csharp-final` 取回，其逐文件许可证清单未补做，不能把历史中的资产记为“已完成白名单审计”。
