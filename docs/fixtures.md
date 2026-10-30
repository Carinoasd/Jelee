# 自建测试素材

Windows 执行 `scripts/make.ps1 fixtures`；Linux 执行 `make fixtures`。命令显式准备可选媒体工具、验证固定版本，然后编译只供开发测试的生成器。普通 Jelee 构建没有 `NewFixtureRunner`，配置变量不能启用它。

每次生成到全新的 `.testfixtures/media-<平台>-<随机值>/`，没有输入或输出路径参数。所有媒体 argv 都在代码中固定；FFmpeg 使用 `-n`，文件写入使用独占创建，已有文件不被替换。工具先按清单验证二进制/许可证 SHA256，再经有界 runner 校验完整供应商版本并生成媒体。项目工作区及工具文件必须由受信任的开发者控制。

## 20 个小型原创文件（外加第 10 阶段的格式样本，见下）

- 1秒 H.264/AAC MP4，320×180 和640×360两个分辨率。
- 1秒 MKV：H.264、两路 AAC、英/中文字幕与两章。
- 1秒 FLAC 音频、故意损坏的 MKV、16×16 PNG 与 16×16 JPEG 海报。
- G40.4：`cover-atom.mp4`（JPEG 海报以 MP4 封面 atom／attached_pic 串流原样复制）与 `cover-attachment.mkv`（PNG 海报作为 Matroska 图片附件，demuxer 同样呈现为 attached_pic），`fixtures.json` 记录海报 SHA256，供内嵌封面提取逐字节比对，见[本地图片](local-images.md)。
- B3：1秒 `track-labels.mkv`（H.264、三路 AAC、两路 SubRip），音轨／字幕带标题标签与 `comment`、`visual_impaired`、`hearing_impaired` 配置（Matroska 没有 captions 旗标）（disposition），供内嵌轨标签解析的真工具测试使用，见[媒体中继数据](media-metadata.md#內嵌軌標籤b3g152g163)。
- 两个原创 SRT、章节点元数据、movie/tvshow/episode NFO 与故意损坏的 NFO。
- E4：1秒 `subtitles-fonts.mkv`（H.264、SRT 與 ASS 內嵌字幕含軌名、兩章、`font/ttf` 附件）及其來源 `styled.ass`、`JeleeSyntheticSans.ttf`（自建佔位位元組，不是真字型，只作附件擷取的位元組比對）。供 mkvtoolnix／MediaInfo 真工具測試使用，見 [mkvtoolnix 與 MediaInfo](matroska-tools.md)。

`fixtures.json` 保存平台、工具版本/哈希、每个文件的字节数、SHA256及预期媒体结构。它记录本次实际生成结果；不同供应商构建的二进制哈希不要求相同。

每次工具运行最多30秒，总生成预算2分钟、工具stdout/stderr各64 KiB；生成结束的每个文件最多4 MiB，总计16 MiB。这些终检限额适用于固定合成输入，不能作为任意媒体写入的沙箱。成功结果留供测试；失败/取消只清理本次新建目录。Linux `SIGTERM` 与中断通过生成器 context 取消子程序；突然断电或强杀主程序后的孤儿清理仍需后续运维机制。

## 每格式最小样本與驗收（第 10 階段 B11，G15、G16；W10-14）

每一種外掛字幕與外掛音軌格式都有一個極小的原創樣本，同一組樣本走完整條路徑：**命名解析 → 掃描檢查（字元集、`.sub` 內容判別、指紋）→ 按需探測（沙箱同一份 ffprobe 參數）→ 播放資訊（自有 API 的 `PlaybackExternalTrack`、相容層 `PlaybackInfo`）→ 原樣投遞（位元組逐一相同，含 Range 與 HEAD）**。樣本、產生方式與期望值都在 `internal/adapter/formatsamples/`（只給測試與開發用產生器 import，沒有任何正式套件 import 它）。

- **文字字幕不入庫，由模板在記憶體產生**（`texts.go`，共 20 個樣本、約 12 KB，每個不到 1.2 KB，位元組固定）：文字檢查閘（`tools/textcheck`）只容許 UTF-8、LF 的文字檔，而這些樣本刻意是 CRLF、帶 BOM 或舊編碼，入庫會被閘擋下或被 Git 換行轉換改壞。內容為 srt／ass／ssa／vtt（另以 webvtt 副檔名投遞）／ttml（另以 dfxp）／smi（另以 sami）／MicroDVD `.sub` 的 UTF-8 版；同一段 SubRip 內容的 UTF-8 BOM、UTF-16LE、UTF-16BE、GB18030、Big5、Shift_JIS、EUC-JP、EUC-KR、windows-1252 版；以及 ass／ssa／smi／MicroDVD 搭配 GB18030、Big5、EUC-KR、Shift_JIS 的版本。`TestEncodedSamplesDecodeBackToTheirStory` 用與偵測器無關的解碼器把每個舊編碼樣本解回原文，證明樣本確實是它宣稱的編碼。
- **點陣字幕與 MP3 同樣在記憶體產生**：VobSub `.idx`＋`.sub`、PGS `.sup` 由 `bitmapsubtest` 在記憶體產生（共 2.7 KB）；MP3 因本機與固定版 ffmpeg 可能沒有 libmp3lame，改為手工寫的 40 個靜音 MPEG-1 Layer III 影格（4 KB，`formatsamples.Mp3`），ffprobe 可探測。
- **其餘音軌由 ffmpeg 內建編碼器產生**（`formatsamples.AudioSpecs` 的參數表，0.25 至 1 秒的 440 Hz 音調）：aac（ADTS）、m4a（AAC 與 ALAC 各一）、ac3、eac3、ec3、dts、thd、truehd、mlp、flac、opus、ogg、oga（Vorbis）、wav、mka（AC-3 與 FLAC 各一）。`tools/gen-fixtures` 把整套樣本（`format-` 開頭）一併寫進 `.testfixtures`；固定版 ffmpeg 若缺某個實驗性編碼器（需要 `-strict -2` 者），記入 `fixtures.json` 的 `skipped` 與原因，不假裝已產生。系統 ffmpeg 版本的測試遇到編碼器缺失同樣 `SKIP` 並印出原因。

### 格式覆蓋表

| 格式 | 樣本 | 掃描／偵測 | 探測 | 播放資訊 | 原樣投遞 |
| --- | --- | --- | --- | --- | --- |
| srt、ass、ssa、vtt、webvtt、ttml、dfxp、smi、sami | 有（UTF-8） | 命名＋字元集 UTF-8 | 不需要（`not_applicable`） | 有（vtt→webvtt、dfxp→ttml、smi→sami） | 有，位元組相同 |
| 字元集 UTF-8 BOM、UTF-16LE、UTF-16BE、GB18030、Big5、Shift_JIS、EUC-JP、EUC-KR、windows-1252 | 有（SubRip；ass／ssa／smi／MicroDVD 各含一種） | 9 種字元集全部偵測正確 | — | `charset` 欄位 | 有，`Content-Type` 只多 `charset` 參數 |
| MicroDVD `.sub` | 有（UTF-8、Shift_JIS） | 內容嗅探為 `microdvd` | ffprobe `microdvd` | `microdvd` | 有，`text/x-microdvd` |
| VobSub `.idx`＋`.sub` | 有（記憶體產生） | `.sub` 嗅探為 `vobsub`；`.idx` 與 `.sub` 成對（真 PG） | `.sub`：ffprobe `dvd_subtitle` | `.idx` 軌 `vobsub`、`.sub` 資料半邊不單列 | 有，`.sub` 為 `application/octet-stream` |
| PGS `.sup` | 有（記憶體產生） | — | ffprobe `hdmv_pgs_subtitle` | `pgs` | 有，`application/x-pgs` |
| aac、m4a(AAC)、m4a(ALAC)、ac3、eac3、ec3、dts、flac、opus、ogg、oga、wav、mka(AC-3)、mka(FLAC) | 有（ffmpeg） | 命名 | 有：codec、聲道數、取樣率、聲道佈局、時長、設定檔 | 有（探測 codec 優先於副檔名） | 有 |
| thd、truehd、mlp | 有（ffmpeg，需 1 秒才能被辨識） | 命名 | 有；**原始位元流沒有時長**（ffprobe 報 N/A） | 有 | 有 |
| mp3 | 有（手工位元流；本機 ffmpeg 無 libmp3lame） | 命名 | 有 | 有 | 有 |
| dtshd | **缺**：沒有 ffmpeg 編碼器寫出 DTS-HD（MA），也不手工偽造 | 只驗命名與 MIME（`audio/vnd.dts.hd`） | 未驗；併入擁有者項目 D42 | — | 只驗 MIME |
| alac（獨立 `.alac`） | **缺**：ALAC 沒有原始位元流容器，獨立 `.alac` 檔不存在；ALAC 以 m4a 驗 | 只驗命名與 MIME（不透明型別） | 以 m4a 驗 | — | — |

內嵌（容器裡）的音軌格式另有一個測試：同一批樣本複製進 Matroska，用正式參數探測，核對播放資訊的內嵌音軌 codec、聲道數與取樣率（`TestEmbeddedAudioFormatsAreReported`，12 種 codec）。內嵌文字字幕（srt、ass）沿用上面的 `subtitles-fonts.mkv`、`track-labels.mkv`，內嵌點陣字幕沿用 B6 的擷取測試。

### 外掛與內嵌的優先級（設計點 5）

`TestTrackPriorityLadder` 把現行規則逐階固定下來。**字幕**由強到弱：使用者指定的軌；語言符合（完全相同的標籤、同文字系統、同語言）；不是 SDH（除非要求 SDH）；檔案的 default 旗標；外掛優先於內嵌；列出順序。forced 軌不當作完整字幕。**音軌**由強到弱：指定軌；要求評論時的評論軌；語言；避開評論；default 旗標；**內嵌優先於外掛**；列出順序。

與設計建議的差異：設計點 5 寫「外掛優先於內嵌」，目前只有字幕這樣做，音軌在其他條件相同時仍選內嵌（既有行為，`SelectDefaultTracks` 的註解與 `TestSelectDefaultTracksPrefersExternalSubtitles` 明載）。這屬於產品決定，B11 不改，只固定並記錄；要改請連同相容層 `DefaultAudioStreamIndex` 一併評估。

### 驗收測試與執行方式

| 測試 | 位置 | 工具需求 |
| --- | --- | --- |
| 樣本表完整（每種辨識的格式都有樣本或缺項原因）、舊編碼解回原文、MP3 影格 | `internal/adapter/formatsamples` | 無 |
| 命名（旁邊、`Subs/`、`Audio/`）、同目錄多影片配對、字元集與 `.sub` 內容檢查 | `internal/adapter/scan/format_samples_test.go` | 無 |
| 內容型別表、原位元組、Range、HEAD | `internal/adapter/media/format_samples_test.go` | 無（有 ffmpeg 時多驗編碼樣本） |
| 優先級階梯 | `internal/domain/track_priority_test.go` | 無 |
| 掃描檢查→探測→播放資訊→相容層 `PlaybackInfo`→`DeliveryUrl` 取回位元組 | `internal/adapter/compat/format_samples_test.go` | ffprobe（探測事實）；ffmpeg（編碼樣本） |
| 沙箱 ffprobe 參數固定、每種格式的 codec／聲道數 | `internal/platform/sandbox/format_samples_test.go` | ffprobe、ffmpeg |
| 內嵌音軌格式 | `internal/adapter/probe/format_samples_test.go` | ffprobe、ffmpeg |
| 目錄同步→外掛軌探測→配對→播放資訊→直投路由（真 PG） | `internal/adapter/postgres/format_samples_test.go` | PG、ffprobe、ffmpeg |
| 固定版 ffmpeg 產生整套樣本 | `tools/gen-fixtures`（`jelee_fixture_tools`） | 固定版 ffmpeg（另有系統 ffmpeg 版本的單元測試） |

缺 ffmpeg／ffprobe 時這些測試 `SKIP`，設 `JELEE_REQUIRE_MEDIA_TOOL_TESTS=true` 則失敗；編碼器缺失一律 `SKIP` 並印出原因。

### 驗收中觀察到的事項（未改產品邏輯）

1. 已探測的 WAV 在外掛軌回報 codec `pcm_s16le`，未探測時回報 `pcm`；單聲道 WAV 沒有聲道佈局。**B12 已修**：WAV 改與 mka／m4a／ogg／oga 同列「探測前不報 codec」，探測後為 ffprobe 名稱（與內嵌 PCM 軌一致，客戶端宣告 `pcm` 照樣涵蓋）。
2. 相容層把 MicroDVD 外掛字幕的 `IsTextSubtitleStream` 報為 `false`（它是文字，但 `microdvd` 不在 `textSubtitleFormats`）。
3. 相容層外掛字幕的 `Codec` 是副檔名（`vtt`、`dfxp`、`smi`），不是播放資訊的規範名稱（`webvtt`、`ttml`、`sami`）。
4. 太短的 TrueHD／MLP 位元流（不到約 0.5 至 1 秒）ffprobe 在 `fd:` 輸入下辨識不出，外掛軌會是 `failed`；真實檔案不受影響。
5. DTS 編碼器固定約 1.4 Mbit/s，所以 0.25 秒的 DTS 樣本仍有 44 KB（只在測試時產生，不入庫）。

真實 DTS-HD MA、TrueHD Atmos、ALAC、Vorbis、多軌 MKA 的探測正確性仍由擁有者驗證項 D42 負責，本包沒有新增編號。

## 验证

`make fixtures-test` 或 `scripts/make.ps1 fixtures-test` 要求真实已固定的工具；缺失必须失败。普通 tag 测试未要求工具时会明确跳过真实工具用例。开发测试用唯读可seek的FD0探测这批自建 MP4/MKV，核对codec、分辨率、音轨/字幕/章节数、时长、大小、损坏拒绝和原文件哈希。

这些测试不开放生产媒体operation，也不证明外部媒体的OS隔离。HDR/Dolby Vision/Atmos/位图字幕/损坏容器的更多种类尚未生成和验收。所有二进制素材、工具缓存与生成器可执行文件都被Git忽略；生成脚本与测试源码入库。
