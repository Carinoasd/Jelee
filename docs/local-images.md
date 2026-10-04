# 本地海報縮圖

狀態：本地 Primary 海報子項已實作及驗證；圖片資產表（G40.1 全部類型與 index）、G40.10 選圖與持久存放區已接入請求（見[資產取圖](#資產取圖g401g405g408g4010)）；十萬圖片規模已有[獨立來源的實測證據](image-memory.md)，同尺寸副本另有[修正驗證](image-same-size.md)。完整圖片功能與至少24小時穩態仍待完成。

本段提供已入庫影片項目的本地 `Primary` 海報：JPEG／PNG 解碼、等比例縮小、JPEG 輸出與有界記憶體快取。原圖和影片維持唯讀。圖片處理不需要啟用影片探測、NFO 或掃描 worker。

## 啟用

啟用帳號、媒體目錄及圖片功能，另外指定已存在的私有暫存目錄：

```sh
JELEE_ENABLE_ACCOUNTS=true
JELEE_ENABLE_CATALOG=true
JELEE_ENABLE_IMAGES=true
JELEE_IMAGE_TEMP_ROOT=/var/lib/jelee/image-work
```

`image-work` 必須是 Jelee 可寫、已存在的私有絕對目錄，不能位於媒體根目錄內，也不能是媒體根目錄的祖先。暫存目錄的符號連結別名會被拒絕；Linux 需由目前程序使用者擁有，權限不得開放給群組或其他使用者。Windows 以已開啟的目錄與檔案 handle 檢查 owner／DACL，僅允許目前程序使用者、SYSTEM 及系統管理員群組的授權；其他帳號的授權、缺失或無限制 DACL 均拒絕，且不會替使用者更改 ACL。其他平台目前拒絕此功能。解碼前將原圖串流複製至有大小上限的私有暫存檔，預檢及解碼讀取同一份內容；請求完成或失敗後清理該檔。它不是永久圖片儲存目錄。

| 設定 | 預設 |
| --- | ---: |
| `JELEE_IMAGE_MAX_CONCURRENT` | 2 |
| `JELEE_IMAGE_MAX_WORKING_BYTES` | 100663296（96 MiB／筆） |
| `JELEE_IMAGE_MAX_SOURCE_BYTES` | 16777216（16 MiB） |
| `JELEE_IMAGE_MAX_OUTPUT_BYTES` | 2097152（2 MiB） |
| `JELEE_IMAGE_MAX_OUTPUT_DIMENSION` | 1024 |
| `JELEE_IMAGE_CACHE_BYTES` | 33554432（32 MiB） |
| `JELEE_IMAGE_CACHE_ENTRIES` | 128 |
| `JELEE_IMAGE_CACHE_TTL_SECONDS` | 300 |
| `JELEE_IMAGE_TIMEOUT_SECONDS` | 15 |
| `JELEE_IMAGE_DEFAULT_QUALITY` | 85 |

總配置另限制「並發數 × 每筆預算 ＋ 快取」不超過 1 GiB。每筆預算是依固定解碼器與縮放器配置路徑作尺寸預檢的保守估計，包含壓縮來源、像素、解碼工作區及輸出；它不是作業系統 RSS 硬限制。Go 執行期、其他功能及 GC 尚未回收的頁面仍須由容器設定與實際負載驗收檢查。

### 持久原圖／變體存放區

`internal/adapter/images/store.go` 提供內容定址存放區。`JELEE_IMAGE_STORE_ROOT` 有設定才啟用；未設定時維持只有記憶體快取的行為。啟用後的請求流程見[資產取圖](#資產取圖g401g405g408g4010)。

| 設定 | 預設 |
| --- | ---: |
| `JELEE_IMAGE_STORE_ROOT` | 空（停用） |
| `JELEE_IMAGE_STORE_ORIGINAL_BYTES` | 4294967296（4 GiB，64 MiB–4 TiB） |
| `JELEE_IMAGE_STORE_VARIANT_BYTES` | 1073741824（1 GiB，16 MiB–1 TiB） |
| `JELEE_IMAGE_STORE_ENTRIES` | 131072（每類索引筆數，1024–2097152） |

存放區根目錄的規則與 `image-work` 相同：已存在、私有、無符號連結別名、不在媒體根目錄內也不是其祖先，且不得與 `image-work` 重疊。版面為 `originals/<sha256 前兩碼>/<sha256>`、`variants/<世代>/<來源 sha256>/<變體 key>`（世代為 16 位十六進位）與私有 `tmp/`；寫入先寫 `tmp`、fsync 後原子 rename。原圖與變體各自有位元組與筆數上限，以記憶體 LRU 淘汰，最近使用時間以檔案 mtime（至多每分鐘更新一次）保存，重啟時以固定批次讀目錄重建索引。讀取一律核對大小，變體另核對標頭內的 SHA-256，原圖可選擇重算雜湊；損壞視為未命中並只刪除該檔。`variants` 可整個清空並按需重建：`ClearVariants` 不改名任何目錄（Windows 不允許改名含開啟中檔案的目錄），而是建立下一個世代目錄作為持久提交點、同時切換現役世代並清空索引，再逐檔刪除舊世代中自己命名的檔案。正被讀取的變體不受影響（Linux 與 NTFS 上讀取者仍讀到完整舊內容；`os.Root` 以 FILE_SHARE_DELETE 開檔、以 POSIX 語意刪除）。一時刪不掉的檔案（他程序以不共享刪除的方式開著、或檔案系統不支援 POSIX 刪除）留在舊世代，由之後的清除與每次啟動重試；重啟時只有編號最大的世代會被收錄，舊世代與舊版無世代的 `variants/<來源 sha256>/` 一律視為已清除，絕不重新收錄。單一變體的淘汰只刪檔不改名目錄，開啟中的檔案在 NTFS 上同樣可刪。存放區只刪除自己命名格式的檔案，其他檔案只計數不處理，也從不碰媒體根目錄。

### 圖片資產表（schema 58）

`item_images` 記錄每個 item 的圖片引用：類型依 G40.1（Primary、Backdrop、Logo、ClearLogo、Banner、ClearArt、Art、Disc、Thumb、Landscape、Chapter、Box、BoxRear、Menu、Profile；Fanart 存成 Backdrop），只有 Backdrop／Chapter 可用 index 1–9999。來源分 `local`、`nfo`、`remote`、`embedded`：local／embedded 必須是同媒體庫的 root＋相對路徑（外鍵綁 item 所屬媒體庫，路徑不得含 `..`、絕對路徑、反斜線、冒號或控制字元；local 與 NFO 本地圖須為圖片副檔名），remote 只能是不含帳密、最長 2048 位元組的 `https://` URL，nfo 二擇一。內容欄（SHA-256、寬高、格式、位元組數、平均色、抓取時間）全有或全無，NULL 表示尚未讀取。每個 (item, 類型, index, 來源) 一列，每個槽最多一列鎖定。

選圖順序：鎖定者優先，其次 local > nfo > remote > embedded；尚未抓取內容的 URL 引用不參與。非手動（掃描、刷新）寫入不會改動已鎖定的列，也不能設定鎖定；來源身分不變而未帶內容時保留已存內容。讀取在同一句 SQL 內重驗 session 與媒體庫授權，無權與不存在一律回 not found；寫入僅限管理員，手動新增／更換、鎖定變更及刪除寫入 `audit_logs`（不含路徑或 URL）。刪除只移除資料庫引用，從不刪使用者檔案；item 刪除時級聯。`image_variants` 是變體存放區的資料庫索引（內容雜湊＋變體 key、大小、最近使用時間，觸碰至多每分鐘一次，淘汰時以列出時的時間做條件刪除）。降級時有 `item_images` 資料即拒絕；變體索引可重建，直接移除。

## API

```text
GET /images/Primary/{itemID}?width=320&height=480&quality=85&format=jpeg
HEAD /images/Primary/{itemID}?width=320&height=480
GET /images/Backdrop/{itemID}?index=2&width=1280&tag=<原圖 SHA-256>
```

需要有效 Bearer session 及該媒體庫的查看權限，Web 與原生 session 都可使用。處理前與交付前各重新查驗權限及 item 的來源綁定；快取命中、HEAD、304 也必須通過。API 不接受檔案路徑或 URL。

省略尺寸時縮入 640 × 640，保持比例且不放大；只給一個尺寸時限制該方向，輸出仍受配置的最大邊長限制。明寫尺寸須為 1–2048，品質須為 1–100；實際輸出限制以配置為準。PNG 透明區域以白底合成。回應提供 `image/jpeg`、內容長度與強 ETag，支援 `If-None-Match` 的 304；快取政策為 `private, no-cache, must-revalidate`。

滿載回 503 與 `Retry-After`；沒有權限或沒有可用海報回 404；超過處理預算回 413，不支援的圖片格式回 415。只有省略的參數使用預設值，重複與未知參數均拒絕。

## 資產取圖（G40.1／G40.5／G40.8／G40.10）

`{type}` 接受 G40.1 全部類型：Primary、Backdrop（別名 Fanart）、Logo、ClearLogo、Banner、ClearArt、Art、Disc、Thumb、Landscape、Chapter、Box、BoxRear、Menu、Profile。`index` 為查詢參數（0–9999，預設 0），只有 Backdrop／Chapter 可用非零值；其他類型帶非零 index、未知類型、非十進位或前置零均回 400。輸出格式維持 JPEG；WebP／AVIF 輸出延後（`format` 只接受 `jpeg`）。

選圖：app 層以 `ResolveItemImageSources` 在同一句 SQL 內重驗 session 與媒體庫授權，取得該槽可用的列（鎖定優先，其次 local > nfo > remote > embedded；尚未抓取內容的 URL 不列入），依序嘗試：

- local／nfo 且指向媒體庫檔案：以 root＋相對路徑經 `probe.Open`（拒絕符號連結元件、os.Root 邊界）複製到私有暫存區，處理後重新開啟並重算完整雜湊，與 Primary 海報相同。
- remote／embedded 或 NFO URL：只從持久存放區讀取已存在的原圖（開啟時重算雜湊）。**請求路徑不會發出外部請求，也不會抽取內嵌圖**；存放區沒有內容或未啟用存放區時視為不可用，換下一個來源。遠端抓取由之後的 image_refresh 任務負責；內嵌封面由 catalog sync 的[擷取段](#內嵌封面擷取g404schema-79)寫入存放區。
- 檔案不存在或來源異動（not found／unavailable）時換下一個來源；過大、格式不支援、忙碌等真正的處理結果直接回應，不再嘗試其他來源。
- 交付前（含快取命中、HEAD、304）再查一次該槽，產生圖片的那一列必須仍可見且綁定相同（列 ID、來源類別、root、路徑、URL、內容雜湊）；否則回 404。
- 該槽完全沒有可用列、或全部不可用時，Primary／index 0 回退到原本「媒體檔旁海報」的行為；其他類型回 404。

`tag`：1–128 位十六進位字元。等於原圖內容 SHA-256（大小寫不拘）時，回應改為 `Cache-Control: private, max-age=31536000, immutable`；不相符則維持 `private, no-cache, must-revalidate`，不會因此拒絕。兩種情況都保留強 ETag、`If-None-Match` 304 與 `Vary: Authorization, Cookie`（網頁端以 Cookie 驗證時同樣按憑證區分快取）。長快取用 `private`：圖片需要登入才能取得，共用快取（代理、CDN）不得保存；只有使用者自己的瀏覽器會保存一年。撤權後，該使用者瀏覽器裡已快取的副本無法收回，這是長快取的取捨。

持久存放區（`JELEE_IMAGE_STORE_ROOT`）：啟動時列出現有媒體庫 root 做重疊檢查（未有媒體庫亦可啟動），之後每個讀取媒體庫檔案的請求再以 `CheckMediaRoot` 檢查該 root，重疊即回 unavailable。查詢順序為記憶體快取 → 存放區變體 → 解碼。變體 key 綁定管線版本、輸出上限、格式、寬高與品質。解碼後：原圖以內容雜湊入庫（已存在則略過）、變體寫入存放區，再 `PutImageVariant` 更新 `image_variants`；命中變體時 `TouchImageVariant`，索引缺列（寫入失敗或存放區重建）則補回。存放區或索引寫入失敗只計數，不影響已驗證的回應。

淘汰：變體用量達上限 90%（位元組或筆數）時喚醒單一背景工作，依 `ListOldestImageVariants` 由舊到新，`DeleteImageVariant` 回 true（列出後未被使用）才刪除對應檔案，降到 80% 停止；被觸碰過的列保留，檔案已不存在的過期列一併刪除。列刪除後即使停止中也完成刪檔。存放區自身的 LRU 仍是硬上限保險；索引缺列的孤兒檔由它處理。原圖不在此淘汰範圍內。停止時先等此工作結束，再關閉存放區與資料庫連線池。

## 相容層圖片路由（G24.2／G40.8）

第三方客戶端相容層的 `GET`／`HEAD /compat/Items/{itemId}/Images/{imageType}[/{imageIndex}]` 與本頁 `/images` 共用同一條管線，沒有第二套解碼或縮放：相容層只把上游參數（`Width`／`MaxWidth`／`FillWidth` 等取最小值當上限、超過 2048 降為 2048，`Quality`，輸出固定 JPEG）轉成 `domain.ImageRequest`，再由伺服器以同一個准入槽、同一個逾時、`app.Images.Get`（處理前後重驗權限與來源綁定）、同一套輸出檢查與 `ETag`／304／`Cache-Control` 規則回應。差別只有：驗證方式是相容層的 native 工作階段（含 `api_key`），錯誤用相容層格式（空主體、隱藏狀態），`Vary` 列出相容層的憑證標頭而不是 Cookie。上游類型名稱對應、參數細節與驗證取捨見[相容性矩陣](compat-matrix.md#圖片模組g242g408g483)。

相容層的影音轉碼守衛不因此放寬：只有圖片路由的 GET／HEAD 改用 `GuardImage`，它只把上游圖片 API 的尺寸／品質／格式成員當圖片參數，其他轉換參數照樣 409；串流路由上的 `Width`／`MaxWidth` 仍是轉換請求。

列表的圖片 tag 由 `app.Images.Summaries` 提供：`ItemImageSummaries` 以一條 SQL 對一頁條目選出每個槽的 G40.10 勝出列（不含 `Chapter`，背景圖只取 index 0–31），授權與停用帳號檢查在同一句內。tag 是原圖內容 SHA-256（已讀取時，等同 `/images` 的 `tag` 長快取條件），否則是該列 ID、更新時間與檔案屬性的摘要；不含路徑或 URL。只有寫入 `item_images` 的圖片會有 tag。

## 來源與快取

本段只接受具有唯一影片來源的項目，同目錄候選依固定優先序選擇 `<影片基名>-poster.jpg`、`.jpeg`、`.png`，其次為 `poster.jpg`、`.jpeg`、`.png`。檔名比對不區分大小寫，同一候選的大小寫碰撞一律拒絕，即使它的順位較低。

每個目錄最多 **10000 個條目、檔名累計 4 MiB**，每批讀取 128 個條目，只保留六個候選的位置；必須完成整個有界列舉才選圖，超限不使用部分結果。開啟與複製前後查核根目錄、父目錄、影片和候選圖片；處理後再核對來源身分、大小、時間與完整內容雜湊，拒絕已觀察到的替換及保留時間戳的內容修改。這些前後檢查不是檔案系統的原子快照。

快取只保存編碼後的小圖，採容量、筆數、LRU 與 TTL 限制。每次請求仍讀取並核對原圖來源；暖命中省下解碼與縮放。key 綁來源及變體參數，沒有公開原始路徑。回應 Body 持有准入槽直到關閉，因此慢客戶端持有已淘汰的圖也受同時處理數限制。程序重啟後按需重建快取。

取消採合作式方式：I/O 與處理邊界檢查 context，CPU 解碼或縮放結束前保留准入槽，取消後不交付圖片。標準解碼器沒有可強制中止 CPU 運算的 context API；設定的逾時不代表 CPU 指令會在該瞬間終止。停止流程會等待仍在處理的請求結束，再關閉資料庫。

縮放使用固定 [Go x/image v0.46.0](https://pkg.go.dev/golang.org/x/image@v0.46.0/draw) 的 `ApproxBiLinear`，從已解碼來源直接寫入唯一輸出 RGBA。真正縮小時不建立額外全尺寸 RGBA；同尺寸時改為直接編碼或在私有解碼緩衝內合成白底，亦不建立完整副本，見[同尺寸修正及證據](image-same-size.md)。JPEG 的 RGB／CMYK 轉換及尚未驗證的子格式會拒絕；尺寸預檢須包含 progressive 係數與 PNG 16-bit／交錯工作區，不能只算寬 × 高 × 4。

## 內嵌封面擷取（G40.4，schema 79）

預設關閉。`JELEE_ENABLE_EMBEDDED_COVERS=true`（或設定檔 `enableEmbeddedCovers`）後，每次 catalog sync 在批次與外部字幕檢查之後多跑一段：把已探測媒體檔裡**容器本來就存放的封面**（`attached_pic` 串流：MP4 的封面 atom、Matroska 的圖片附件，JPEG 或 PNG）原封不動複製到持久原圖存放區，並在 `item_images` 以 `embedded` 來源記錄。需要同時啟用探測（`JELEE_ENABLE_PROBE`）、圖片與 `JELEE_IMAGE_STORE_ROOT`，否則設定驗證失敗；探測能力不可用、存放區未開啟或沙箱註冊失敗時只記一筆 `embedded_cover_*_unavailable` 警告並關閉這一段，不影響啟動。

**這是擷取，不是轉碼（G10）。** 不呼叫 ffmpeg，也不解碼或重新編碼任何影像：沿用已出貨、已固定雜湊的 ffprobe 與同一套隔離 helper（Landlock、seccomp、rlimit、清空環境、唯讀 FD 0 輸入、僅 `fd:` 協定與既有 demuxer 白名單），新增一個密封的「cover」描述子模式，argv 由程式固定（G09.2 參數陣列）：`-select_streams v:N -read_intervals %+#1 -show_entries …packet=stream_index,size,data,data_hash -show_data -show_data_hash SHA256`。也就是只讀出該串流的第一個封包（attached picture 只有這一個封包），以十六進位傾印加上 ffprobe 自己算的 SHA-256 輸出。N 是該封面在影片串流中的序號，每個 N（0–15）是一個獨立封裝的描述子，不接受呼叫端參數；正式 helper 只接受 `/usr/lib/jelee/ffprobe` 的標準編碼描述子。解析端要求恰好一個串流且其 index、`codec_type=video`、`attached_pic=1`、codec 為 `mjpeg`／`png` 都符合探測結果，恰好一個封包，傾印的偏移連續、欄寬固定、總長等於宣告大小，解出的位元組 SHA-256 必須等於 ffprobe 的 `data_hash`；再檢查 JPEG／PNG 魔術位元組，並只讀影像標頭做尺寸預檢（每邊 ≤ 16384、總像素 ≤ 64 Mi）。實測斷言存放區裡的原圖與嵌入前的圖檔逐位元組相同。

**不需要 ffmpeg（G37.1）。** 正式映像依 G37.1 只含固定雜湊的 ffprobe，預設不含 ffmpeg；本功能因此只用 ffprobe 的沙箱 cover 讀取，Dockerfile 與正式映像都沒有為它加入 ffmpeg，也不依賴尚未合併的 mkvtoolnix（mkvextract）。原文「ffprobe/ffmpeg（如可用）」在這裡就是 ffprobe：工具不存在或探測能力不可用時，服務照常啟動、這一段保持關閉並記 WARN（`embedded_cover_prerequisite_unavailable`／`embedded_cover_runtime_unavailable`）；`jelee-cli doctor` 的 tools 檢查在開關開啟時回報 `embedded_covers_ready` 或 `embedded_covers_tool_missing`（fail），見[故障排查](troubleshooting.md)。開發與測試用的 ffmpeg 只在 `.tools/` 產生夾具（`tools/gen-fixtures` 的 `cover-atom.mp4`、`cover-attachment.mkv`），從不進映像。

**界限（G29.4／G42.7／G40.12）。** 專用子行程准入：同時最多 1 個、每次 20 秒逾時、stdout 上限 16 MiB（十六進位傾印約為原圖的 4.3 倍）、stderr 64 KiB 且不保留；暫存在私有 scratch 目錄。單張封面上限 3 MiB（`domain.EmbeddedCoverMaxBytes`），宣告超過或輸出超限都視為過大；記憶體上限約為一份傾印加一份原圖。每次擷取另受作業 IO 工作預算與 30 秒單檔逾時，每個 catalog sync 最多啟動 512 次擷取，每頁 16 個候選。

**何時做、何時不做。** 候選是：只有單一媒體檔的條目、該檔的探測快取為 ready 且 metadata 記有 `attachedPic`（探測 parser 版本因此升為 `media-metadata-v2`，舊快取會在下次探測時重算）、Primary 槽沒有鎖定列，也沒有可用的更高優先來源（local、NFO、已抓取內容的 remote）。擷取前後都在同一個唯讀 FD 上核對探測時的大小、mtime 與邊緣指紋，並重開註冊路徑確認未被替換；不符就放棄（不記錄，下次再試）。記錄時在同一交易內重鎖 item、重驗媒體檔與探測戳記、鎖定與優先序：鎖定列存在回 `skipped_locked`，出現更高優先圖片回 `skipped_priority`，戳記不同回 `changed`，三者都不寫入。成功（`stored`）與確定性的拒絕（`absent`、`invalid`、`too_large`）寫入 `item_embedded_cover_attempts`，以 (根、相對路徑、大小、mtime、指紋) 記住「這個檔案已處理」；檔案沒變就不再啟動子行程，檔案改變（探測戳記改變）才重新擷取並更新同一列。逾時、工具忙碌或不可用、檔案變動等暫時性結果不記住；工具不可用時本段立即結束。

**優先序（G40.10）。** 內嵌封面放在最後：鎖定 > 本機旁車圖片（local）> NFO > 外部抓取（remote）> embedded。理由：它是媒體檔附帶的圖，品質與比例不保證，使用者另外放的旁車圖片或 NFO 指定圖都應優先；remote 只有在已抓取內容後才參與選圖。內嵌列一律非鎖定寫入，不會改動已鎖定的列，也不會覆蓋其他來源的列（每個來源各自一列）。

**資產（G40.5／G40.11）。** 原圖以內容 SHA-256 存入 `originals/`，與可清理重建的變體分離；`embedded` 列的 root＋相對路徑指向媒體檔本身（僅作來源身分，從不讀成圖片、從不修改或刪除）。請求路徑只從存放區讀已存在的原圖，不會在請求時擷取。降級到 schema 78 只移除擷取紀錄表，已存的 `item_images` 內嵌列保留；之後重新升級會把有封面的條目重新列為候選。

## 已執行驗證

[來源雜湊與驗證證據](evidence/local-images.json)記錄本段範圍、固定依賴、失敗修正與原始日誌雜湊。測試事件包含父測試及子測試，不能相加解讀成不重複案例數。

- Windows 相關單元套件 723 個通過事件、1 個平台略過；Linux race 709 個通過事件、零略過。涵蓋 progressive JPEG、16-bit／Adam7 PNG、預檢拒絕、LRU／容量／TTL、原圖異動、取消、逾時及持有回應時的停止等待。
- HTTP Windows 整包 444 個通過事件、零略過，套件覆蓋率 89.2%；Linux race 圖片專項 44 個事件、零略過。驗證認證前准入、503、參數、錯誤、撤權、HEAD／304、寫入失敗及回應關閉。
- Linux race 真實 PostgreSQL／Fx／HTTP 10 個通過事件、零略過。兩張實際 JPEG／PNG 由 128 × 64 縮為 32 × 16，以正式預設密碼雜湊及登入、授權與撤權流程取圖；四個原檔雜湊保持，暫存清空，停止後 HTTP 關閉、lifetime 取消、資料庫連線歸零。
- 33 個 Go 檔格式、八個套件 vet、兩項架構測試、三個正式執行檔建置及模組校驗通過。90 份既有 SQL、五份直播核心、授權與需求原文保持。

第一輪 Linux 整合測試因 `t.TempDir` 建立的目錄可被其他使用者讀取而收到 404，改用測試自有私有目錄後通過，產品權限檢查保持。Windows 初次受限 token／路徑分隔符／檔案共享模式問題與原始失敗日誌均保留。Windows 的大小寫碰撞素材依平台略過，Linux 有實際執行。

905 份最終來源凍結後完成 PostgreSQL 驗收；先前單元測試的 902 份來源與最終差異只有四個整合測試素材檔。小型整合的暖回應位元組一致不單獨證明快取命中，命中計數由 processor 測試驗證；正常停止與處理中等待分別由整合及單元測試涵蓋。這輪沒有量測圖片規模 RSS。

## 尚未涵蓋

掃描入庫（命名辨識寫入 `item_images`）、遠端抓取的 image_refresh 任務、鎖定／更換的管理 API、WebP／AVIF 輸出、裁切（相容層的 `FillWidth`／`FillHeight` 因此只當上限）、相容層的舊式長路由（`/Items/{id}/Images/{type}/{index}/{tag}/{format}/…`）與人物／類型／工作室圖片、播放進度疊加與模糊效果、前端與圖片重建工作尚未完成。相容圖片路由已提供，但尚未用真實客戶端驗證。十萬圖片處理、混合並發與至少 24h 的驗收另行執行。本段不能據此將 G40 或 G42.10 標為完成。
