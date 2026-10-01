# 媒體中繼資料解析

`probe.ParseJSON([]byte) (domain.MediaMetadata, error)` 將 ffprobe JSON 轉為有限欄位的資料模型。此函式不啟動程序、不開啟檔案、不寫入資料庫，也不決定是否能播放。程序隔離與安全開檔由其他元件負責；參見 [process runner](process-runner.md) 與 [probe input](probe-input.md)。

## 輸入與錯誤

輸入形狀依據 ffprobe 的 `-show_format -show_streams -show_chapters -of json` 輸出。欄位名稱、數字與字串型別參照 [ffprobe 文件](https://ffmpeg.org/ffprobe.html) 與固定版本的 [JSON 輸出實作](https://github.com/FFmpeg/FFmpeg/blob/n9.0.2/fftools/ffprobe.c)。解析器要求至少一條串流，每條都有唯一非負 `index`，且 `codec_type` 為 `video`、`audio`、`subtitle`、`data` 或 `attachment`。空物件、空串流和只有容器資訊的文件均拒絕。

錯誤固定為 `probe_metadata_invalid` 或 `probe_metadata_limit`，不含原始 JSON、路徑、標籤或解碼器訊息。失敗時回傳空模型，不提供部分結果。所有物件都拒絕重複鍵，包括未知欄位與跳脫後相同的鍵；未知欄位在下列總量限制內忽略。

| 限制 | 硬上限 |
| --- | --- |
| 輸入 UTF-8 JSON | 4 MiB，包含空白 |
| 串流 | 64 |
| 章節 | 256 |
| JSON 巢狀層級 | 根層 0，最深 16 |
| 每個物件欄位／一般陣列元素 | 1,024 |
| JSON 值總數 | 32,768，包含容器與根物件 |
| 單個鍵或解碼後字串 | 64 KiB |
| 每條視訊串流 side data 記錄 | 32 |
| `format_name` | 1,024 bytes、16 個逗號分隔名稱 |

時間以非負 `int64` 微秒儲存。十進位秒字串最多 6 位小數；章節 ticks 使用整數有理數換算，四捨五入到微秒，並檢查溢位。若 ticks 與秒字串同時存在，兩者必須一致。章節起點不可大於終點；容器時長已知時，章節不得超出時長。章節 ID 必須唯一。

整數欄位拒絕浮點、指數表示法、錯誤 JSON 型別及範圍溢位。JSON 數字也拒絕非有限值。有理數使用非負分子和正分母，約分後保存；拒絕溢位和零分母。視訊幀率的 `0/0` 是 ffprobe 未知值，特別轉為缺值。幀率 `0/1` 亦為缺值，`1/0` 仍是錯誤。

## 保留欄位

| 類別 | 保留內容 |
| --- | --- |
| 容器 | 固定允許清單中的格式名稱、時長、大小、位元率 |
| 串流共用 | index、種類、允許的 codec/profile、時長、位元率、語言、default/forced |
| 視訊 | level、寬高、有理數幀率、色彩範圍／空間／transfer／primaries、已知 HDR side data |
| 音訊 | 聲道數、允許的 channel layout、取樣率、每樣本位元數、明確 Atmos profile |
| 字幕 | codec、語言、default/forced |
| 章節 | ID、開始與結束微秒 |

未知或尚未列入允許清單的 codec、profile、格式、色彩名稱與 channel layout 會留空，不原樣傳出。optional 欄位使用指標；缺少、`null`、空字串、`N/A`、`unknown`、`unspecified` 代表缺值。缺值不代表零、false、不支援或已通過播放驗證。寬高、取樣率、聲道數、位元率和音訊位元深度的零值亦視為未知；明確 disposition `0` 則保留為 false。

寬高最多 65,535；聲道最多 256；取樣率最多 768,000 Hz；音訊位元深度最多 64；幀率最多 1,000,000。這些是拒絕異常輸入的界線，不是播放器能力聲明。語言只保留長度不超過 35、符合有限語言標籤形狀的字串，並轉為小寫；不嘗試根據標題推斷語言。

色彩空間使用 ffprobe 的 canonical 名稱；例如 RGB 對應 `gbr`。名稱取自 [FFmpeg 9 的色彩屬性定義](https://www.ffmpeg.org/doxygen/9.0/pixdesc_8c.html)，不自行轉換或推斷色彩空間。

## HDR、Dolby Vision 與 Atmos

只接受明確記錄，不根據檔名、軌道名稱、容器、codec 或位元深度推斷特性。

- `Mastering display metadata`：色度座標與亮度的有理數；座標限 0..1，亮度限 0..1,000,000，最小亮度不得超過最大亮度。
- `Content light level metadata`：MaxCLL／MaxFALL，限 0..65,535，平均不得大於峰值。
- `DOVI configuration record`：profile、level、RPU／EL／BL flags 與 compatibility ID。
- HDR10+：只有明確 `HDR Dynamic Metadata SMPTE2094-40 (HDR10+)` 記錄才設為 true。
- Atmos：只有 `eac3` 搭配 `Dolby Digital Plus + Dolby Atmos`，或 `truehd` 搭配 `Dolby TrueHD + Dolby Atmos` 才設為 true。名稱依 [FFmpeg profile 定義](https://www.ffmpeg.org/doxygen/9.0/profiles_8c_source.html)。

side data 記錄可只有部分欄位，模型保留其餘欄位為空；空的已知記錄只表示觀察到該記錄種類。這不證明完整 HDR 資料存在或可解碼。相同已知 side data 種類重複時拒絕輸入。此階段以合成 JSON 驗證這些映射；真實 HDR、Dolby Vision、Atmos 素材仍未驗收。

## 隱私與驗證界線

不保留原始 filename、任意 tags、vendor、URL、軌道標題、章節標題、字型附件名稱或未列入允許清單的值。固定錯誤不帶原始內容，模型也没有原始 JSON 欄位。

測試包含官方欄位形狀的合成 golden、隱私投影、未知值、數值邊界、重複鍵、資源上限、章節時間一致性與 fuzz seeds。這些測試驗證解析器行為；不等於已驗證每一種媒體格式、真實特殊音訊／HDR 素材、程序沙箱或客戶端播放能力。

## 真實工具輸出驗收

`tools/gen-fixtures/parser_real_test.go` 在 `jelee_fixture_tools` build tag 下，以固定開發工具產生新的一組自建素材，再讓固定 ffprobe 讀取已開啟的唯讀 FD。測試先核對編譯時嵌入的 ffprobe 與授權檔 SHA256，再核對精確 vendor version；stdout 限 4 MiB、stderr 限 64 KiB，每次程序最多 15 秒，整個測試 context 為一分鐘。解析前後比對原檔 SHA256，並清理此測試新增的目錄。

此註冊只存在於開發 tag 的 `_test.go`，僅接受此測試自建的輸入。一般生產建置没有此媒體操作。它驗證工具輸出與解析器的相容性，不能取代任意媒體需要的 OS 沙箱。

| 真實自建素材 | Windows 與 Linux 已核對內容 |
| --- | --- |
| `video-180p.mp4` | H.264 Constrained Baseline、320×180、24 fps、1 條 AAC mono 48 kHz 音軌、1,000,000 微秒 |
| `video-360p.mp4` | H.264 Constrained Baseline、640×360、24 fps、1 條 AAC mono 48 kHz 音軌、1,000,000 微秒 |
| `multi.mkv` | 320×180 H.264、2 條 AAC 音軌、2 條 SubRip 字幕（eng／zho）、2 章節（0–0.5 秒／0.5–1 秒）；容器 1,021,000 微秒，包含 AAC priming padding |
| `corrupt.mkv` | 工具失敗；不保留輸出，空輸出也無法通過 `ParseJSON` |

三個有效影片另核對容器格式、精確檔案 byte 數、正值位元率、隱私投影與來源 SHA256 不變；MKV 字幕另核對 default／forced 欄位。沒有因 H.264 或檔名推斷 HDR、Dolby Vision 或 Atmos。

本次固定版本為 Windows `9.0.2-essentials_build-www.gyan.dev` 與 Linux `n9.0.2-17-g2a571b6068-20260930`，來源與 SHA256 見 [工具 manifest](../tools/manifest.json)。真實特殊 codec、HDR、Dolby Vision、Atmos、外部媒體與客戶端播放仍未驗收。

重現純解析器測試：

```sh
.bin/go test -count=1 -cover ./internal/adapter/probe
.bin/go test -run '^$' -fuzz FuzzParseJSON -fuzztime 20s -parallel 2 ./internal/adapter/probe
```

重現真實素材解析（先安裝 manifest 固定的可選開發工具；Windows 使用 `.bin/go.cmd` 並設定相同測試環境變數）：

```sh
JELEE_REQUIRE_MEDIA_TOOL_TESTS=true .bin/go test -tags jelee_fixture_tools \
  -run TestParseActualPinnedFFprobeFixtures -count=1 -v ./tools/gen-fixtures
```

未設定 `JELEE_REQUIRE_MEDIA_TOOL_TESTS=true` 時，真實素材測試明確 skip；設定後，缺少工具或身份不符會失敗。Linux 可加入 `-race`。本次原始日誌保存在 ignored `.testdata/parser-real-windows.txt`、`parser-real-linux.txt` 與 `probe-json-*`，供階段驗收彙整。

## 後續串接

`NewAdapter(*process.IsolatedRunner, identity)` 只接受隔離 runner；沒有公開的任意 executor 或裸 ffprobe 工廠。呼叫端應先由已授權的媒體庫解析 `Source`，adapter 才以安全開啟的唯讀 FD 執行固定 `ffprobe/metadata` 操作。子程序成功後才解析 JSON。任何錯誤一律回傳空 `Observation`，不提供部分中繼資料或來源指紋。

| Adapter 失敗 | 固定分類 |
| --- | --- |
| 工具／helper 不可用、無效 descriptor、啟動或清理失敗、異常退出／訊號 | `probe_tool_unavailable` |
| 呼叫取消／逾時 | `context.Canceled`／`context.DeadlineExceeded` |
| 所有程序名額已滿 | `process_busy` |
| 子程序輸出超限 | `process_output_limit` |
| JSON 結構或解析資源限制 | `probe_metadata_invalid`／`probe_metadata_limit` |
| 子程序正常回報媒體失敗（exit 1），或未分類錯誤 | `probe_failed` |
| 觀察到來源變更 | `probe_source_changed` |
| 開檔／讀取失敗或無效來源 | `probe_input_unavailable`／`probe_invalid_input` |

來源一致性檢查包含開啟後與執行後的 regular-file 身分、大小、mtime，以及 `edge-sha256-v1` 快速指紋。該指紋將版本字串、little-endian uint64 大小與首尾各最多 64 KiB 合併後計算 SHA256；小於等於 128 KiB 的檔案只取每個 byte 一次，不重疊。`ReadAt` 不改變共用 FD 的 seek offset。最後再安全開啟授權路徑，比對 inode／檔案身分與 stat，拒絕路徑替換、刪除、symlink 替換或工具回報大小不符。

這不是完整檔案 snapshot，也不能作為內容完整性證明。另一程序若保持 inode、大小和首尾取樣不變，只改寫未取樣的中間區段並還原 mtime，仍可能通過檢查；驗證結束後也仍可再被改寫。測試明確保留此限制，沒有宣稱能偵測所有並行修改。`Observation` 只是一筆候選結果；資料庫保存前的版本比對、掃描工作串接、快取失效與 HTTP 回應仍由後續階段實作。

Adapter 測試使用僅套件內可見的 executor 替身，驗證固定操作、唯讀輸入、FD 清理、取消、錯誤分類、無效輸出及來源變更；它們不取代真實隔離程序驗收。解析成功只代表結構和允許欄位有效，不能用作任意媒體安全性、播放支援或轉碼授權的判定。

本次 adapter 加入後，Windows 全套 probe 測試與 vet 通過（package 93.8%）；Linux 原生暫存檔案系統的完整 probe race 測試與 vet 通過（package 94.4%）。adapter.go 覆蓋 74／84 statements（88.1%），解析器為 326／334（97.6%）。Windows 因主機 symlink 權限而跳過 2 項；Linux 實際執行 symlink、FIFO、檔案權限與替換測試，沒有 skip。原始日誌為 ignored `.testdata/probe-adapter-windows.txt`、`.testdata/probe-adapter-linux-native.txt`；後者先以固定 Linux Go 編譯 `-race -cover` 測試執行檔，再在本次建立且自動清理的原生目錄執行。
