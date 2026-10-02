# NFO 只读解析与原文保留

## 已实现的边界

`internal/adapter/nfo` 读取、校验本地 NFO，提取元数据并保留原始字节。它不修改原 NFO、不写媒体文件、不生成新 ID、不启动外部进程，也不访问 NFO 中的 URL。CLI 可以在没有数据库的情况下使用这个模块校验文件。

当前支持**读取和原文复制**。`WriteOriginal` 不把修改后的 `Metadata` 序列化回 XML；修改提取视图后调用它，输出仍是最初读取的原文。未知标签、属性、注释、元素顺序、缩进、换行、BOM 和原编码由原始字节保留。

確認套用的單值投影會共用標題／日期別名的重複檢查，歧義內容在供應商查詢與保存前拒絕；[別名契約](nfo-alias-ambiguity.md)。

唯讀項目套用已接可信來源、人工優先、四文字欄位及其獨立鎖、NFO／TMDB同交易融合與可信缺失／解析損壞回退。第26版亦支援沒有文字、但有已知正鎖定指令的有效NFO；見[lock-only契約](nfo-lock-only.md)。按庫worker的摘要觀察與缓存已有[實際驗證](nfo-worker-verification.md)，尚未等同完整媒體階層匯入。

第27版新增排序標題（第五欄）與其獨立鎖、人工接管及同交易融合；[排序標題契約](nfo-sort-title.md)。

第28版另新增tagline、outline、mpaa與certification，合計九個文字欄位；OfficialRating保護兩種分級文字，人工九欄patch與同交易融合已接；[擴充文字契約](nfo-text-fields.md)。

尚未完成：九欄以外的套用／鎖、季集與多項目來源的正式套用、完整實體匯入及前端、編輯後的無損回寫、原子替換與備份、跨進程鎖、批量匯入／匯出、`--fix`、真實客戶端雙向互操作驗收。G39仍為部分完成。

## 上游证据与格式选择

核对快照：`52a680c578f1af888ebb74cefcb89b736f9c5738`。以下链接固定到已审计提交；旧名称只用于准确标识兼容来源。

| 证据 | 确认的行为 | 本模块处理 |
| --- | --- | --- |
| [电影保存器](https://github.com/MoYuanCN/Jelee/blob/52a680c578f1af888ebb74cefcb89b736f9c5738/MediaBrowser.XbmcMetadata/Savers/MovieNfoSaver.cs#L41) | `movie.nfo` 或视频同名 `.nfo`；电影根为 `movie` | 识别 `.nfo` 文件名，读取 `movie`；不实现光盘目录发现 |
| [剧集保存器](https://github.com/MoYuanCN/Jelee/blob/52a680c578f1af888ebb74cefcb89b736f9c5738/MediaBrowser.XbmcMetadata/Savers/SeriesNfoSaver.cs#L41) | `tvshow.nfo` 与 `tvshow` 根 | 支持 |
| [季度保存器](https://github.com/MoYuanCN/Jelee/blob/52a680c578f1af888ebb74cefcb89b736f9c5738/MediaBrowser.XbmcMetadata/Savers/SeasonNfoSaver.cs#L40) | `season.nfo` 与 `season` 根 | 支持，包含 `seasonnumber` / `seasonname` |
| [单集保存器](https://github.com/MoYuanCN/Jelee/blob/52a680c578f1af888ebb74cefcb89b736f9c5738/MediaBrowser.XbmcMetadata/Savers/EpisodeNfoSaver.cs#L41) | 视频同名 `.nfo` 与 `episodedetails` 根 | 支持；另接受需求指定的 `episode` |
| [多集解析器](https://github.com/MoYuanCN/Jelee/blob/52a680c578f1af888ebb74cefcb89b736f9c5738/MediaBrowser.XbmcMetadata/Parsers/EpisodeNfoParser.cs#L49) | 一个文件可连续出现多个 `episodedetails`，上游会排序后合并 | 接受多个单集根，`Entries` 保留源顺序，暂不合并 |
| [公共字段解析器](https://github.com/MoYuanCN/Jelee/blob/52a680c578f1af888ebb74cefcb89b736f9c5738/MediaBrowser.XbmcMetadata/Parsers/BaseNfoParser.cs#L278) | `name/title/localtitle`、锁、ID、多值、图片、评分等字段 | 提取表中列出的字段；保留其余原文 |

`root`、`Item`、`MediaBrowser` 包装是 G39.2 指定的兼容扩展；没有把它们宣称为该快照保存器的输出格式。包装内可嵌套已支持的媒体根。包装本身直接带字段时会提取，并产生 `nfo_kind_unknown` 警告；包装同时带直接字段与媒体子根时，采用媒体子根，并报告直接字段被忽略。未知根仍可读取，报告 `nfo_unknown_root`。

该上游公共解析器还处理纯提供商 URL 与 XML 后附 URL。本阶段要求结构化 XML，因此这些形式返回 `nfo_invalid_xml`，不会抓取链接。多个电影根同样拒绝；多根例外仅限单集格式。

## 提取字段

| 类别 | 字段 |
| --- | --- |
| 文本 | title/name/localtitle/seasonname、originaltitle、sorttitle/sortname、plot、outline、tagline、showtitle、status、mpaa、certification |
| 数字 | year、season/seasonnumber、episode、displayseason、displayepisode、runtime（分钟） |
| 日期 | premiered/releasedate、aired、dateadded；校验 `YYYY-MM-DD`、`YYYY-MM-DD HH:mm:ss`、RFC3339 |
| 多值 | genre、tag/style、studio、country、language、director、writer/credits、producer；多元素与 `/` 分隔均保留输入顺序 |
| 人员 | actor/name、role、thumb、order |
| 标识 | uniqueid/type/default、imdbid、tmdbid、tvdbid、id（映射为 imdb）；冲突值同时保留并告警 |
| 评分 | rating/communityrating、userrating、ratings/rating 的 name/max/default/value/votes |
| 锁 | lockdata、以 `\|` 分隔的 lockedfields；保留原鎖資訊；已知九個文字欄位鎖已接唯讀套用與TMDB融合，其他欄位尚未支援 |
| 合集 | 文本 set/collection 或 set/name、set/overview |
| 图片 | thumb/aspect/type/season/preview、fanart/thumb、多种 art 子元素、poster/banner/clearart/clearlogo/landscape |
| 其他 | trailer 原始文本列表 |

只有直接元数据字段会被映射；未知子树内的同名 `title` 不会覆盖条目名称。有命名空间的扩展字段保持原文，不当成无命名空间的标准字段。重复标量字段采用最后一个值；原文保留所有重复项。文本仍是不可信数据，显示时必须转义；CDATA 中的 HTML 不执行也不净化为可信 HTML。

未支持的字段（例如 airs 细分结构和部分提供商专用字段）只在原文中保留。本阶段没有完整 G39 字段等价性的结论。

## 编码与资源限制

- UTF-8，以及 UTF-8 BOM。
- 带 BOM 的 UTF-16LE / UTF-16BE；无 BOM 且以 XML `<` 的字节模式开头时可识别，并产生编码推测警告。
- 显式 GBK、CP936、GB2312 声明按 GBK 解码；无声明且 UTF-8 无效时尝试 GBK，并报告 `nfo_encoding_guessed`。GBK 没有本模块支持的标准 BOM。
- 编码猜测不能保证区分其他旧编码。原始字节始终保留，推测结果应人工复核。无效 UTF-16 代理项、无效 GBK、声明与实际 BOM 冲突、未支持的编码均拒绝。
- XML declaration 最多 1024 字节；长 declaration 不能绕过编码一致性校验。解码器请求的字符集也必须匹配已经识别的实际编码。
- `maxBytes` 限制原始字节数，默认 8 MiB、最大允许 32 MiB；精确达到上限可读取，超过一字节即拒绝。
- 最深 64 层、50,000 个元素、200,000 个 token、每元素 64 个属性、最多 128 个媒体条目。未知子树仍检查 XML 与这些限制，但不会构造成保留树。
- DTD、实体声明、外部实体、XML stylesheet 等处理指令被拒绝；标准 XML 字符实体与数字引用由严格解码器处理。

转换后的编码缓冲和已识别字段会增加内存使用；这里提供明确上限，未声称常量内存。已识别字段的混合文本按顺序提取，避免每层重复复制整棵文本子树。

## 文件边界与取消

`ReadFile` 接受管理员配置的绝对根目录和 `/` 分隔的相对文件名，使用 `os.OpenRoot` 与只读 `Root.OpenFile`。拒绝绝对相对参数、父目录穿越、反斜线、NUL、Windows ADS 冒号、符号链接逃逸和非普通文件。Linux 加 `O_NONBLOCK`，避免 FIFO 在普通文件检查前挂起。仅大小写不敏感的**文件名识别**由 `IsNFOName` 提供；Linux 文件打开仍使用实际文件名。

上下文取消会关闭 `ReadFile` 自己打开的文件。通用 `Read(io.Reader)` / `WriteOriginal(io.Writer)` 在读写块之间检查取消；自定义阻塞 Reader/Writer 必须自己提供期限或关闭机制。底层文件系统内核调用的挂起仍受挂载环境约束。

`os.Root` 不是对本机管理员的隔离；可信根内的硬链接和挂载点不由此阻止。所有公开错误均为固定代码，不包含传入绝对路径、XML 片段或底层异常文本。

图片引用只做语法检查：例如本地 `../`、绝对路径、凭据 URL、`file:` 和 `javascript:` 会形成问题项。合法 HTTP/HTTPS 的语法不等于允许下载；未来下载器仍必须经过 SSRF 防护、域名和地址检查。当前解析器完全离线。

## API 与校验结果

`Read(ctx, reader, maxBytes)` 和 `ReadFile(ctx, rootAbs, relativeSlash, maxBytes)` 返回 `*Document`。`Document.Metadata` 是第一个条目的提取视图，`Entries` 是所有条目；`Encoding`、`OriginalSize` 和 `Root` 描述输入。

### 3C3A：先读取来源，再选择解析

`ReadSource(ctx, rootAbs, relativeSlash, maxBytes)` 返回只读 `Source`。它通过安全文件描述符读取有界原文，核对读取前后文件大小、mtime与身份，再从配置根重新打开当前路径，核对根与文件仍是同一个对象。观察到改动、替换或删除时返回固定 `nfo_changed` 和空结果；取消优先返回context错误。

`Source.Stamp()` 返回值拷贝，包含大小、纳秒mtime、完整原文字节SHA256和 `sha256-full-v1` 指纹版本；不含路径或原文。`Source.Parse(ctx)` 才进行XML解析。3C3C worker 在读完/hash后先查询持久快取，再决定是否解析；命中仍执行末次读取/hash，暖扫不能声称零I/O。未解析的来源不保证XML有效。

现有 `ReadFile` 使用这条路径后立即解析，所以单档CLI也具备来源一致性核对。CLI遇到读取中改动输出 `nfo_changed`、退出1，不把这类变化误称为坏XML。`Source` 保留独立于后续磁盘改动的原文字节；多次解析和修改提取视图不会改写这些字节，复制原文时也不把内部缓冲直接交给调用者的writer。

上述检查可发现可观察到的改动，不能提供文件系统原子快照：并发原地写入后刻意恢复同size/mtime仍可能躲过检查。SHA256只标识实际读取的字节，不证明它们在某一瞬间同时存在。根内硬链接、挂载点和已有root内符号链接行为保持原有信任边界。后续持久快取必须在提交前重新观察来源。

实际执行结果见[3C3A验证报告](nfo-source-verification.md)，后续分段及验收见[NFO增量计划](nfo-incremental-plan.md)。本段不新增库级模式、schema、批次任务、目录关联、Catalog合并或写回，相关需求仍为部分完成。

### 3C3B：验证摘要与快取边界

`NewSummaryReader` 使用固定解析/指纹/摘要版本，将来源读取和摘要解析分开。摘要只含固定编码、根类别、条目数和最多64个固定问题码；完整warning/error计数不因截断而丢失。包装根映射为中性 `wrapper`，未知及命名空间根保持固定告警；不将第三方根名或任意XML内容传入核心摘要。

五类固定XML/编码/复杂度错误可形成invalid摘要，语义error同样无效，warning本身不使文档无效。来源变化、超大输入、取消、未知问题码和读写故障不能形成负缓存。schema006提供独立NFO库策略、正负TTL、配额及连续检查点，详见[快取契约](nfo-cache.md)。3C3C/schema007 接入scan worker与按库CLI/API，见[工作流程](nfo-worker.md)与[实际验证](nfo-worker-verification.md)；Catalog来源合并仍未实现。

`Validate()` 返回读取时问题列表的副本；它不重新校验调用者事后修改的 `Metadata`。每项包含固定 `Code`、`Severity`（`warning` 或 `error`）、字段名和零起算 `Entry`（文档级为 -1）。无标题、未知根、编码猜测、ID 冲突为警告；非法数值、日期、布尔值、路径引用等为错误问题项。语法/编码/安全/大小限制失败会直接返回错误，且不提供部分成功的文档。

`WriteOriginal(ctx, writer)` 只提供给内部受信任调用者复制原文。调用者必须选择独立目标，不能把 writer 指向原 NFO；该 API 不创建文件、不提供公开下载接口。短写、写入失败和取消均返回错误。

## 可复现验证

```powershell
./scripts/run-go.ps1 test ./internal/adapter/nfo -count=1 -cover
./scripts/run-go.ps1 vet ./internal/adapter/nfo
./scripts/run-go.ps1 test ./internal/adapter/nfo -run '^$' -fuzz FuzzReadRetainsAcceptedOriginal -fuzztime 5s
```

```sh
.bin/go test -race -count=1 -v ./internal/adapter/nfo
```

测试使用源代码内的小型 XML 字符串与临时文件，没有提交媒体或生成的二进制素材。黄金测试覆盖电影、剧集、季度、单集、多集、包装、人物/评分/锁/图片/ID、多值、未知内容、编码、原文字节一致性，以及错误、取消和文件边界。网络回归测试证明读取图片引用不会发出请求。

2026-09-30 Windows / Go 1.27.1 验证：单元测试通过，包语句覆盖率 89.8%，`go vet` 通过；5 秒 fuzz 完成 265,385 次执行并通过。Windows 创建符号链接权限不足时相关子测试明确跳过，不能作为该能力通过的证据。Linux 的 FIFO 测试在不支持命名管道的 DrvFS 上也明确跳过，需原生 Linux 文件系统复验。

首段只读解析的提交为 `f21d15668477bd5806e7e525149bfb373d9a68bd`。包含 CLI 的完整 [Windows 测试](evidence/windows-tests.txt)与 [Linux race 测试](evidence/linux-race.txt)均通过；Linux 实际执行 NFO 符号链接逃逸与替换测试，NFO 覆盖率 89.8%。日志中列出的跳过仍保留上述限制。

同日 WSL Ubuntu / Go 1.27.1 的 `-race -count=1` 专项通过，文件符号链接逃逸和并发替换用例均实际执行通过。完整输出保存在被忽略的 `.testdata/nfo-linux-race.txt`；其中 FIFO 一项按上述文件系统限制明确跳过。
