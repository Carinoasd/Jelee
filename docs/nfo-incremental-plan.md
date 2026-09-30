# NFO 只读增量整合：3C3 分段计划

本文记录后续范围、实现约束和待执行验收，不是完成报告。依据为[原始需求](requirements-source.md)、[第 3 阶段计划](jobs-stage3-plan.md)、现有 [NFO 解析器](nfo-compatibility.md)、[扫描任务](jobs-worker.md)与[媒体探测 worker](probe-worker.md)。所有相关完整需求仍为部分完成。

## 当前边界与推荐顺序

现有扫描器已经按扩展名识别 video、nfo、image、other；schema003 的 inventory 保存 root、相对路径、size 和纳秒 mtime，并有独立的已接受 baseline。baseline仅保存library/root/path，尚无kind/size/mtime，不能直接据它计算图片内容属性的增量差异。NFO 解析器已支持多种编码、媒体根、字段、锁和图片引用；它不访问 URL，也不修改原文件。现有 CLI 可独立于数据库校验单个 NFO。

缺口是安全来源读取的前后核对、按库模式、持久解析快取，以及任务和公开诊断整合。Catalog 当前只有基本 item/source，尚无完整 metadata 来源优先级、字段锁或剧集父子模型。不要以读取锁字段代替字段合并保护，也不要以批次验证代替 NFO 导入。

按三个可独立验证的小段交付，每段完成后单独提交 PR：

| 阶段 | 最小可用结果 | 当前状态 |
| --- | --- | --- |
| 3C3A | 稳定性核对后的 NFO 来源、完整内容指纹；现有单文件 CLI 经 `ReadFile` 直接使用 | 本小段已验证，见[实际报告](nfo-source-verification.md)；完整G39仍未完成 |
| 3C3B | 持久 read-only/off policy、有界 NFO cache、SQL fencing 与配额契约 | 计划，未实现或验收 |
| 3C3C | 既有 scan worker、按库验证 API/CLI、NFO/图片增量统计与真实规模验收 | 计划，未实现或验收 |

暂不加入 fsnotify、去抖、cron、持续监看、NFO 写回、图片下载/处理、自动 Catalog 合并。图片在 3C 的范围仍是存在、size、mtime 的识别与增量统计。

## 3C3A：安全来源与不可变读取结果

### 最小契约

新增 `nfo.ReadSource(ctx, rootAbs, relativeSlash, maxBytes)` 返回私有原始字节支持的 `Source`。`Source.Stamp()` 按值返回来源观察；`Source.Parse(ctx)` 解析这一次已经读到的字节，不重新打开文件。`ReadFile` 委派给这两个步骤，因此本段立即改善现有 CLI，而不需要数据库或未来 worker。

Stamp 至少需要 size、纳秒 mtime、完整原始字节 SHA-256，以及明确的指纹版本。SHA 应覆盖 BOM、原编码、空白和未知字段，不能由提取后的 metadata 重新生成。缓存的路径、root ID、parser/schema/policy 版本由后续层另行加入；不要把绝对路径放进公开 stamp、错误或日志。

本段已约定的 API 草案是 `SourceStamp{Size int64, ModifiedUnixNano int64, SHA256 string, FingerprintVersion string}`，`SourceFingerprintVersion = "sha256-full-v1"`。nil 或未初始化 Source 的 Parse 返回 `ErrInvalidInput`。这些签名不代表实现已通过验收。

读取失败必须返回 nil Source；不得留下可误用的部分 stamp。`Parse` 失败返回 nil Document，保留固定解析错误。来源读取成功而 XML 无效时，Source 的内容指纹仍可供未来负缓存使用；文件变化、读取不可用和取消不能成为 `nfo_invalid`。

这里的“稳定”表示通过指定检查点的核对，不是文件系统原子快照保证。

### 文件与内容不变量

1. 验证绝对可信 root 和 `/` 分隔的相对路径；拒绝越界、ADS、NUL 及非普通文件。root 配置不是任意 HTTP 路径输入。沿用 `os.Root` 的根边界约束，不把它说成拒绝所有根内链接或本机管理员隔离。
2. 首次 `OpenRoot` 后以 `Root.Stat(".")` 取得根目录身份，作为本次观察的起点；不承诺观察该 handle 取得之前的目录状态。打开只读安全 FD，在读取前后检查同一 FD 的文件身份、类型、size、mtime，并核对实际读取长度。原始输入仍受默认 8 MiB、最大 32 MiB 的既有上限约束，精确边界与超过一字节分别验收。
3. 读取结束重新安全解析 root/相对路径，检查 root 与叶文件身份仍对应此次打开的对象；删除、替换、路径重定向或已观察到的内容变化返回固定 `nfo_changed`。
4. 完整 SHA-256 必须由返回 Source 中相同的原始字节计算。读取中计算 hash 或随后遍历私有 buffer 均可；不得为 hash 再读另一份文件并与前一份解析结果拼接。
5. Source 不公开可写 `[]byte`、Reader 的底层切片或 FD；Stamp 为值。每次 Parse 的可变 metadata、issues 和 entries 不能修改 Source，也不能影响另一次 Parse。
6. Parse 重用私有原文，避免通过 `Read(bytes.NewReader(...))` 再完整复制一次最大 32 MiB 输入。原文可以在只读 Source 与 Document 之间共享，但所有对外复制入口都不能把共享内存变成可写别名。`WriteOriginal` 可使用固定小块 scratch，避免自定义 Writer 改动共享原文。
7. 编码转换、XML token 和字段视图仍占用额外内存。只承诺有界输入、结构和并发；不能称常量内存或把输入上限直接等同峰值 RSS。
8. 所有失败优先保留已发生的标准 context cancellation/deadline；其余错误为固定安全码。路径失联、源变化、输入超限、XML/编码错误须能区分，不返回底层路径或 XML 片段。

### 必须公开的并发限制

完整内容 hash 能识别两次完整读取之间的中段变化，即使 size 和 mtime 被还原；这比只取文件边缘的指纹更适合小型 NFO。但没有文件系统快照或能约束所有写入者的锁时，另一个进程仍可能在读取期间原地写入，并还原可见属性。此时检查可能未发现混合读取。完整 hash 只准确描述本次保留的字节，不能证明这些字节曾在磁盘上原子同时存在。

最后一次身份核对之后文件仍可变化；Source 是已经保留的历史观察，不是对后续路径的持续保证。后续 cache 提交必须另做新的来源核对和数据库 fencing，并仍保留上述并发限制。

### 跨平台注意事项

- Linux：打开 FD 后原路径被 rename/unlink，FD 仍可读；仅 `file.Stat` 无法发现路径换到另一对象。需重新安全打开路径比较对象身份。FIFO 必须在可能阻塞前受保护；不能只在打开之后检查普通文件。
- Windows：除符号链接，还要考虑目录 junction 和其他 reparse point；只检查 `ModeSymlink` 不足以概括全部。大小写差异和路径字符串相等不能代替文件身份检查。文件共享语义可能令并发替换尝试失败，测试要记录真实触发结果，不能把“没替换成功”算作检测成功。
- 固定 Go 1.27.1 的 Windows `os.Stat(path)` 可通过 GetFileAttributesEx 返回属性，延迟到首次 `os.SameFile` 才按路径获取 file ID；路径可能已换对象。因此本段不使用这种路径 Stat 作为初始身份依据，改以首次 `OpenRoot` 后的 `Root.Stat(".")` 捕捉身份。已打开文件的 Stat 也从 handle 取得身份。验证从这个明确检查点开始，不能只用 Linux 行为推定 Windows。
- 两平台：root 目录自身可能被替换；仅从最初持有的 root handle 重新开相对路径，可能仍进入旧目录。root 身份也要按既定策略复核。所有检查均是检查点，不能宣称消除敌对祖先目录替换的所有竞态。
- 可信 root 内的硬链接和挂载点不能仅靠路径约束全部排除；同 inode 的不同名称也不能只凭字符串判定为内容变化。安全边界和支持范围必须写清楚。
- 网络文件系统或底层内核调用可能卡在 OpenRoot/OpenFile/Stat；context 关闭已打开 FD 不保证所有前置调用立即中断。避免宣传硬实时截止。
- 只读访问可能由系统更新 atime；原文不变验收应检查内容 hash、size、mtime 及无主动写入，不能声称所有文件系统属性都不变。

### 3C3A 验收清单

以下清单对应的实际结果、平台差异与保留限制见[3C3A验证报告](nfo-source-verification.md)，不能据此宣称3C3B/C已完成。

- 正常、空文件、无效 XML、各编码、上限边界：先读取/hash，不解析也能取得相同原文指纹；Parse 与现有解析结果一致。
- 同一 Source 重复或并发 Parse；修改某个 Document 的 metadata/issues 后重新 Parse 不受影响。原文复制及自定义 Writer 不得改变 Source 或下一次指纹/解析结果。
- 读取过程中可控地修改 size、mtime、内容，替换叶文件和 root，删除路径，转为链接/非普通文件；用例明确区分触发成功与环境不支持。
- Linux 在根 handle 取得后执行真实 root rename/replacement。Windows 若因持有 handle 而拒绝 rename，分别记录 OS 拒绝与原文保持，再用返回另一个真实 root 的 reopen 测试证明身份不匹配会返回 `ErrChanged`；两者不能合称 Windows 成功完成了 rename 后检测。
- 可观察到的变化固定返回 `nfo_changed` 和零结果；取消/deadline 保留标准 context 错误；解析错误与来源错误不混淆。
- 文件取得 Source 后再修改原路径，Parse 仍只解析已保留字节；对此历史观察语义作明确断言。
- 检查没有多余完整原文复制；使用最大边界输入验证限制，不以不稳定的单次 RSS 数值代替所有权检查。
- Windows 与原生 Linux 文件系统测试，Linux race；权限或文件系统不支持的案例明确 SKIP。现有 CLI 无 DB 验证路径回归、原文 hash 不变、日志无原始内容/路径。

## 3C3B：按库模式与持久快取

以下是后续实现方向，字段和接口需在该段开始时与当前 schema 再核对。

### 策略与缓存身份

- 按库保存 `off` 或 `read-only`，默认 off；`read-write` 尚未支持时明确拒绝。策略修改必须 live admin 检查、审计和幂等处理。
- 新增独立 NFO policy/source generation，不因 NFO 模式变化递增 `library.probe_generation`。NFO 改变或关闭不能迫使未变影片重新执行 ffprobe。
- 缓存身份包含 library/root、规范相对路径、size、纳秒 mtime、完整原文 SHA-256、指纹/parser/规范 DTO schema 版本和相关 scope generation。文件系统 inode 身份用于当前读取核对，不把易复用的 inode 数字当永久缓存主键。
- 相同内容的暖扫描仍需有界读取/hash；可以省略 XML 解析。必须分别统计读字节数、hash 次数、Parse 次数和命中数，不能称暖扫描零 I/O。
- 原始 XML 留在用户文件系统，不复制到数据库。新增纯 domain 规范 DTO；不直接持久化整个 adapter Document、任意 vendor 字段或无限 issue 列表。payload、条目、字段及 issue 数量都设硬上限；超限不能静默截断为完整 metadata。

### 事务、容量与失败

- 使用新 migration006；已发布001–005保持原文。不要把 NFO 塞进 video-only `probe_cache` 或借 ffprobe tool identity 授权解析。
- 复用 parent job 的 owner/generation/lease 和同库互斥；在确认无需跨 job 并行后，可先不引入第二套 child lease。任何保存与 checkpoint 必须同一短交易完成，重新核对租约、取消和 policy/scope generation。
- 读取/hash/XML 在交易外；按连续 cursor 提交有界页，不允许跳过尚未完成的前缀。已完成观察可重放而不重复计数。
- 正/负 TTL、全局与每库行数/字节配额、索引淘汰与 sweep 批量上限必须显式；NFO 上限独立于媒体 probe 配额。过期和淘汰不能清除用户文件或媒体 Catalog。
- XML 无效可成为短 TTL 的 `nfo_invalid`；来源 changed、unavailable、取消、租约丢失以及存储故障不能污染负缓存。语义校验 error 与 warning 另有明确状态，避免把有 warning 的有效文档判为损坏。
- `off` 之后不再解析或公开旧观察为当前结果。缓存保留/清理可按容量与 TTL 执行，不以关闭功能为删除用户资料的授权。

### 3C3B 验收（待执行）

真实 PG migration up/down/up、同库竞争、租约过期/ABA、取消最后提交、policy 变更、事务失败回滚、连续 checkpoint、配额精确对账、TTL/淘汰/sweep 上限；错误全部脱敏。回滚策略须说明活动阶段处理及丢失哪些生成数据，不能改旧 migration 或默默 force dirty 状态。

## 3C3C：任务、公开诊断与规模证据

- 既有 scan job 的持久请求包含 NFO 意图及提交时策略；重放比较公开意图，不重复失效；重试按明确规则保存/更新版本，不能默受当前配置漂移。
- inventory 完成后运行 NFO 阶段，再按请求处理其他阶段。恢复时识别阶段状态，不重新扫描已冻结 inventory。NFO 能力独立于 ffprobe：Windows、禁用 probe 或工具缺失仍可只读验证 NFO。
- 固定 worker/IO 预算处理文件，禁止每文件无界 goroutine。单项 XML 损坏不中断全库；DB/基础运行故障与单项错误分别处理。
- 提供 live-admin 检查的分页摘要/问题列表，以及 `nfo validate --library UUID` 任务入口；保留现有无 DB 单文件命令。首次只返回固定状态和计数，不公开任意原文、绝对 root 或未授权 artwork URL。
- 多集 NFO 保留全部 Entries，关联不明确时记录 unresolved；Linux 上 `movie.nfo` 与 `MOVIE.NFO` 同时存在不能任意取一个。现有 adapter 的第一个 Metadata 视图不能作为自动归属规则。
- 图片使用inventory中的属性统计新增、变动和未变；当前baseline只有路径，3C3C须新增有界图片观察或通过新迁移补足属性基线，不能把现表称为已具备完整增量统计。不打开大图做像素解码。副档名只代表候选类别，不代表已验证文件格式；具体图片角色、宽高、缩放、远程获取和图片 API 留待独立阶段。
- 不自动覆写 Catalog 的人工 title 或其他值。保留 `lockdata`、`lockedfields` 和来源信息，只是为后续合并策略提供输入；未实现优先级与锁决策之前不得宣称 G39.6 完成。

### 3C3C 真实验收（待执行）

1. 自建 1,000 文件混合库，记录 video/NFO/image 数量；初扫实际解析 M 个 NFO，原文与已有影片 metadata 对应。
2. 输入和版本不变重扫：NFO XML Parse 为 0；完整读取/hash 计数如实报告；影片 metadata child starts 为 0。
3. 替换 K 个 NFO 的目录项，再扫只解析 K 个；仅改 NFO 和图片时影片 metadata child starts 仍为 0。图片变更统计应与输入集合完全一致。
4. 如果使用 hardlinks，修改某一路径必须替换该目录项，不能原地改共享 inode 却声称只有一个文件改变。逐路径核对 size/mtime/hash，保存素材生成方法。
5. 纳入坏 XML、编码、锁、多集、图片引用、文件变化和上限样本；验证坏项不阻断其他文件，无外连，原文/媒体内容不变。
6. 取消、进程停止/恢复、到期租约、容量耗尽、撤权、幂等重放、默认 off；分别核对 checkpoint、计数和残留资源。
7. Windows 与 Linux 有意义测试及 Linux race，独立 PG schema；真实规模结果与模拟 repository 的单元测试分开报告。
8. 原始性能要求另有 100 文件增量案例，单独记录；1,000 条路径不代表 1,000 种编码/媒体格式或十万文件规模。

## 精确需求对应与保留项

| 需求 | 3C3 可覆盖的部分 | 仍需后续交付 |
| --- | --- | --- |
| G13.4/G13.5 | NFO 指纹、增量解析、checkpoint；图片存在/size/mtime 的增量统计 | fsnotify、扫描窗口、调度与更完整图片解析 |
| G39.1–5 | 复用现有只读解析并接入来源和库任务 | 未支持字段、写入分隔符及完整字段等价性 |
| G39.6 | 按库 read-only/off，保存锁输入 | read-write、来源优先级、字段锁合并决策 |
| G39.11 | 单文件安全读取，后续按库问题清单 | `--fix` 与备份 |
| G39.12 | `nfo_invalid`、原文保持、坏项隔离 | 完整多来源 metadata fallback |
| G39.13 | 离线解析，文本/图片引用保持不可信 | 实际下载的 SSRF/白名单链路 |
| G39.14/G39.15 | 可取消批次验证及部分读取 golden | 批次导入/导出、读改写往返、真实客户端互操作 |
| G40.2/G40.7/G40.11 | 图片候选存在/size/mtime；不更改原图 | 角色关联、格式/尺寸、锁、派生图与生命周期 |
| G41/G42.2/G42.4 | 固定 worker、有界 NFO 输入/结构、容量与 TTL | 完整内存预算、大规模长时间验收；不称常量内存 |
| G48 | 管理员诊断权限和库隔离 | 未来用户 metadata/image 查询的完整 ACL |

完成任一小段都不表示完整第 3 阶段、G39 或 G40 已完成。实际结果、覆盖率、SKIP 和限制应在各段验证报告中记录；本文的验收条目本身不构成执行证据。
