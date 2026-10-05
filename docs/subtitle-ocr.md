# 字幕 OCR（G15.6）

G15.6 要求：「OCR：預設關閉，顯式開啟後可運行；需限流、並發受限，並說明準確率與資源開銷。」本文記錄 Jelee 的做法：把 Matroska 來源內的點陣字幕軌（PGS、VobSub）辨識成**額外的** SRT 文字字幕軌，放在可重建快取，經既有的擷取直投路徑提供給原生用戶端。

- **不取代、不改原軌**（G15.5）：點陣字幕軌照舊只能原樣直投；OCR 結果是另一條衍生軌，標題加註「(OCR)」。原媒體檔不寫入（實測原檔 SHA256 不變）。
- **不轉碼**（G10）：OCR 只讀字幕圖片，不碰影像串流；Jelee 自己解碼 PGS／VobSub 圖片（Go 標準庫 `image`，不新增相依），再交給沙箱內的 Tesseract。
- **預設關閉**：需同時開啟 Matroska 擷取（E4）並設定 OCR 自己的快取根目錄；Tesseract 是選用工具，預設不安裝、不在預設映像。
- **限流與並發**：每分鐘圖片數上限（任一滾動 60 秒內）、同時 1–4 個 Tesseract 行程、每張圖另經實例 CPU 預算（G41）、子行程輸出限流（G42.7）。

## 版本、來源與校驗（G30.6、G51）

Tesseract（Apache-2.0）依 G30.6 核准為選用外部工具（[擁有者驗證清單](owner-verification-queue.md) E16），全部固定在 `tools/manifest.json` 的 `ocrTools`；`tools.OCRToolSpec` 從編進程式的清單讀出，不讀 PATH、不信任宿主檔案。

**為何用 Debian 套件。** Tesseract 上游只發原始碼，沒有 Linux 可攜二進位（Windows 只有 UB Mannheim 的 NSIS 安裝程式，不是可攜 zip）。因此固定 Debian 13（trixie）main amd64 的套件，比照 mkvtoolnix 補函式庫的做法：

| 項目 | 版本 | 來源 | 校驗 |
| --- | --- | --- | --- |
| `tesseract-ocr`（只取 `usr/bin/tesseract`） | 5.5.0-1+b1 | `deb.debian.org` pool | SHA256 與大小抄自 `dists/trixie/main/binary-amd64/Packages.xz`（2026-10-05），HTTPS 下載後重算一致 |
| `libtesseract5`、`libleptonica6` 1.84.1-4 等 49 個函式庫套件 | 各自固定版本 | 同上 | 同上 |
| `libc6` 2.41-12+deb13u4（只取 `libresolv.so.2`） | 與 mediaRuntime 的 glibc 同一個套件版本 | 同上 | 同上 |
| `tesseract-ocr-eng`／`-chi-tra`／`-chi-sim`／`-jpn` | 1:4.1.0-2（tesseract-lang，tessdata_fast 4.1.0） | 同上 | 同上，`.traineddata` 另有逐檔 SHA256 |

- **閉包**：`usr/bin/tesseract` 的 DT_NEEDED 圖在這些套件內解析（readelf），共 57 個檔案：52 個套件函式庫（libtesseract、Leptonica 與其影像函式庫，以及 Debian 版本連結的 libcurl／libarchive 與其 TLS、Kerberos、LDAP 堆疊）、同版 libc6 的 `libresolv.so.2`，加上 mediaRuntime 的 ld.so、libc、libm、libgcc_s。Jelee 從不給 URL 或壓縮檔，沙箱也禁止網路與子行程，但這些函式庫必須能載入。閉包比其他工具大，沙箱因此只對 OCR 模式把上限從 32 放寬到 64（`sandbox.MaxOCRLibraries`），其他模式不變。
- **語言資料的大小取捨**：用 tessdata_fast（整數 LSTM）：eng 4.1 MB、chi_tra 2.4 MB、chi_sim 2.5 MB、jpn 2.5 MB，合計 11.4 MB。tessdata_best（浮點 LSTM）大數倍、也更慢；舊版 tessdata（eng 23 MB、chi_tra 57 MB）含 legacy 引擎，Jelee 只用 LSTM（`--oem 1`），不需要。字幕是橫書，直書模型（`*_vert`）不收。
- 套件在 Debian 小版本更新後會離開 pool；`snapshot.debian.org` 仍保留，`JELEE_TOOLS_MIRROR` 可指向檔名相同的 HTTPS 鏡像。

## 安裝與校驗

```sh
make bootstrap-ocr                 # 或 python3 scripts/ocr-tools.py bootstrap [--offline]
python3 scripts/toolchain.py bootstrap --tool tesseract   # 同一入口，只有點名時才安裝
make tools-verify                  # 必要工具＋已安裝的可選工具；未安裝者印出「Skipped optional tesseract」
make ocr-tools-verify              # 嚴格：必須已安裝
make ocr-toolchain-test            # 離線合成 .deb 的安裝器測試（6 項）
```

- 只接受無憑證 HTTPS；`--offline` 只用快取，快取不符就刪除該檔並失敗。只從 `.deb` 的 data 壓縮檔取清單列出的檔案；control 壓縮檔與維護腳本從不解開或執行。語言資料用新的 `data` 檔案種類（`runtime-tools.selected_files` 需明確允許，ELF 以外的檔案不做 ELF 檢查，大小上限與 ELF 相同）。
- 安裝到 `.tools/ocr/tesseract/5.5.0-1+b1/linux-amd64/`（`bin/`、`lib/`、`tessdata/`、`runtime/lib/x86_64-linux-gnu/libresolv.so.2`、`licenses/`），先在 staging 寫入再改名；記錄在 `.tools/ocr-installed/`。`verify` 比對每個檔案的 SHA256 與實際檔案集合（多一個、少一個都拒絕），再以固定 `--version` 確認版本字串。已安裝檔被改動時失敗並保留現場。
- `.bin/tesseract` 是開發用包裝（宿主 glibc，含宿主 libresolv），執行前重驗雜湊；不經 shell、不走 PATH；它不是生產沙箱。
- 只支援 Linux amd64；Windows 上 `--if-installed` 直接略過，正式服務回報 `platform_unsupported`。

## 映像（不在預設階段）

預設的 `Dockerfile` 完全不變。OCR 是另一層：`deploy/ocr/Dockerfile`（BuildKit 使用旁邊的 `Dockerfile.dockerignore`，只送入驗證器與 `.tools/ocr`）：

```sh
make bootstrap-media bootstrap-runtime bootstrap-matroska   # 預設映像複製的固定檔案
make bootstrap-ocr
docker build -t jelee:local .
docker build -f deploy/ocr/Dockerfile --build-arg JELEE_IMAGE=jelee:local -t jelee:ocr .
```

建置時 `tools/runtime-image -ocr` 逐位元組核對 110 個檔案（tesseract、52 個函式庫與 libresolv、4 個語言資料、52 份授權聲明），不執行任何 OCR 程式。檔案位置：`/usr/lib/jelee/tesseract/{tesseract,lib/,tessdata/}`、`/lib/x86_64-linux-gnu/libresolv.so.2`、`/licenses/tesseract/<套件>/copyright`。

## 生產執行與沙箱（G09.2、G29.4、G42.7）

只在 linux-amd64、以 `internal/platform/ocrruntime` 註冊。沿用 E4 的工具 helper（`--internal-media-tool-helper`）、身分驗證、Landlock、seccomp 允許清單、`execveat` 與程序群組管理，新增第四個固定模式 `tesseract-ocr`；ffprobe 與三個 mkvtoolnix／MediaInfo 模式的描述子、政策與探測身分逐位元組不變（`ToolPolicyVersion` 仍是 v1，OCR 另有 `OCRPolicyVersion`）。

- **身分**：執行檔、57 個閉包檔案與 4 個語言資料都以固定路徑開啟、雜湊比對清單；生產另要求 root 擁有、群組／其他不可寫、服務非 root。helper 在套用政策前再驗一次；任一檔案（含未設定的語言）被改動，整個 OCR 停用。
- **參數陣列**（helper 依描述子組出，無 shell、無字串拼接）：
  `tesseract /proc/self/fd/0 stdout --tessdata-dir /usr/lib/jelee/tesseract/tessdata -l <語言> --oem 1 --psm 6 -c thresholding_method=1`。
  描述子只帶模式、固定路徑與 1–4 個語言代碼（只限政策已固定資料的語言、不重複）；語言來自設定，不來自請求。`thresholding_method=1`（Leptonica 分塊 Otsu）在實測中消除了全域門檻對部分大號描邊中文行給出空結果的情形。
- **輸入**：每張圖寫成私有目錄內的二進位 PGM（0600），重新以唯讀開啟後作為 stdin；Tesseract 重開 `/proc/self/fd/0`。Landlock 只對 stdin 檔案物件與**本次語言**的資料檔給 READ_FILE，目錄本身不可讀；沒有任何可寫路徑（FSIZE 0）。
- **資源**：NOFILE 128、NPROC 128、AS 2 GiB、CORE 0、CPU 30 秒；`OMP_THREAD_LIMIT=1`（每個行程單執行緒，並發由行程數決定）。runner 層：同時 1–4 個（`JELEE_SUBTITLE_OCR_CONCURRENCY`）、逾時 1 分鐘、stdout 64 KiB 上限（超過即終止且不回傳內容）、stderr 只計數 64 KiB。
- **清理**：圖片檔在每次辨識後刪除；runner 與工作目錄在服務的 `jelee-service-mkv-*` scratch 內（`ocr-runs`、`ocr-pictures`、`ocr-work`），關閉時刪除，當機殘留由啟動清掃回收。
- **結束碼**：非 0 或逾時視為這張圖無法辨識（略過該句）；忙碌、取消、沙箱失敗則中止整個工作。

## 處理流程

1. **觸發**：原生會話讀播放資訊（自有 API `GET /api/v1/items/{id}/playback`、相容層 PlaybackInfo）時，對每條 Matroska 內的 PGS（`hdmv_pgs_subtitle`）或 VobSub（`dvd_subtitle`）軌，以與直投相同的授權查詢詢問 OCR 結果；沒有結果就把該來源排入佇列（不阻塞回應），有結果才列出衍生軌。直接請求衍生軌網址也會排入。Web 會話拿不到播放資訊，也碰不到這些路由。
2. **佇列**：有界（`JELEE_SUBTITLE_OCR_QUEUE_SIZE`，預設 16 個來源），同一來源修訂只排一次；滿了就丟棄並計數，下次查詢再排。一次只處理一個來源。失敗的修訂 10 分鐘內不重排，避免壞檔一直佔用。關閉服務時取消執行中的工作，staging 與工作檔一併刪除。
3. **擷取**：以既有 mkvmerge 識別與 mkvextract（同一沙箱模式）把來源的點陣字幕軌原樣抽到私有工作目錄：PGS 為 `t<id>.sup`，VobSub 為 `t<id>.idx`＋`t<id>.sub`（mkvextract 102.0 實測的命名）。每來源至多 8 軌、合計 256 MiB、每檔 64 MiB（超過即被核心以 FSIZE 終止）。文字擷取快取不受影響；mkvextract 忙碌時退避重試 4 次。DVB（`S_DVBSUB`）不處理。
4. **解碼**（`internal/adapter/bitmapsub`，純 Go、只吃 `io.Reader`／`io.ReaderAt`）：
   - PGS：PCS／WDS／PDS／ODS／END 段、epoch 物件與調色盤、多物件合成與裁切、RLE；VobSub：`.idx`（第一個 `id:` 區段、16 色調色盤、timestamp／filepos）與 MPEG-2／MPEG-1 PS、跨 PES 的 SPU、控制序列、交錯 RLE。
   - 上限：畫布與物件 ≤4096×4096、單張 ≤4096×2304 像素、每軌 ≤50000 張、單一物件 ≤16 MiB、每個 epoch ≤32 MiB／64 個物件、每軌累計解碼像素 ≤2^32、`.idx` ≤4 MiB。截斷、超大尺寸、錯誤 RLE、迴圈控制序列等都有錯誤處理：單張壞圖略過並計數，結構錯誤停在該處——之前解出的句子保留，該軌標記為截斷；從不 panic、配置前先檢查尺寸。Fuzz 測試各跑約 100 萬次以上無異常。
   - 時間：PGS 以 90 kHz PTS；VobSub 以 `.idx` 時間加 SPU 延遲（單位 1024/90000 秒，約 11.4 毫秒，所以結束時間可能多 1 毫秒）。結束時間夾在 (開始, 開始+30 秒]，不明時用 5 秒。
5. **圖片前處理**：只裁切可見像素外框、四周補 16 px 白邊，比較「外圈像素」與「內部像素」的亮度決定極性（白字黑框、黃字黑框、黑字白框都輸出白底深字），裁切高度不到 24 px 放大 3 倍、不到 48 px 放大 2 倍（雙線性）。同一軌內與先前完全相同的圖片只辨識一次。
6. **辨識與限流**：每張圖依序等待每分鐘上限（`JELEE_SUBTITLE_OCR_PICTURES_PER_MINUTE`，任一滾動 60 秒內的上限，不會因突發超過）、實例 CPU 預算（G41 `WorkCPU`），再由 1–N 個工作者交給 Tesseract。語言依軌的語言標記選擇設定中相符者（`chi`／`zho` → chi_tra／chi_sim、`jpn` → jpn、`eng` → eng；中日文軌若有設定 eng 會加為第二語言），不相符或沒有標記時用全部設定的語言。
7. **輸出**：辨識文字只做清理（移除控制字元、合併空白、至多 4 行、每行 200 字），寫成 UTF-8 SRT（每軌 ≤8 MiB，超過即截斷並標記）。空白結果不成句。整個來源的所有軌一次發布到 `<cacheRoot>/<sourceID>/<修訂>/s<探測串流索引>.srt`；修訂由來源 ID、大小、修改時間、辨識器身分（執行檔、閉包、語言資料雜湊、argv 與語言）與解碼器版本雜湊而成，任一改變就是新修訂、舊修訂刪除；期間來源被改寫即放棄結果。總量超過 `JELEE_SUBTITLE_OCR_CACHE_MAX_BYTES` 依最近使用淘汰；整個快取可隨時刪除，下次查詢重做。

## 設定

| 環境變數 | 設定檔 | 預設 | 說明 |
| --- | --- | --- | --- |
| `JELEE_ENABLE_SUBTITLE_OCR` | `subtitleOcr.enable` | `false` | 開啟 OCR；需 `JELEE_ENABLE_MATROSKA_EXTRACTION=true`（因此也需 catalog 與直投） |
| `JELEE_SUBTITLE_OCR_CACHE_ROOT` | `subtitleOcr.cacheRoot` | — | 既有、私有（0700）的絕對目錄，不放其他東西；不可與 Matroska 快取相同或互相包含 |
| `JELEE_SUBTITLE_OCR_CACHE_MAX_BYTES` | `subtitleOcr.cacheMaxBytes` | 256 MiB | 16 MiB–1 TiB |
| `JELEE_SUBTITLE_OCR_LANGUAGES` | `subtitleOcr.languages` | `eng` | 逗號分隔，1–4 個：`eng`、`chi_tra`、`chi_sim`、`jpn`（依優先序） |
| `JELEE_SUBTITLE_OCR_PICTURES_PER_MINUTE` | `subtitleOcr.picturesPerMinute` | 60 | 1–6000；任一滾動 60 秒內最多辨識的圖片數 |
| `JELEE_SUBTITLE_OCR_CONCURRENCY` | `subtitleOcr.concurrency` | 1 | 1–4 個同時執行的 Tesseract 行程 |
| `JELEE_SUBTITLE_OCR_QUEUE_SIZE` | `subtitleOcr.queueSize` | 16 | 1–256 個等待中的來源 |

工具缺、被改、平台不支援、設定的快取根不可用時，OCR 停用並記一次 `subtitle_ocr_*_unavailable` 警告；Matroska 擷取照常。路由（若已開啟）仍在，衍生軌一律答覆為找不到，播放資訊不列出。

## 取得方式

- **自有 API**（僅原生會話）：`GET|HEAD /api/v1/sources/{id}/ocr-subtitles/{index}`，`index` 是點陣字幕軌的探測串流索引。播放資訊中該軌在 OCR 完成後多一個 `ocr` 物件：`{"format":"srt","title":"<軌名或語言> (OCR)","url":"/api/v1/sources/{id}/ocr-subtitles/{index}"}`；點陣軌本身的欄位不變。
- **相容層**：PlaybackInfo 在外掛字幕之後，為每條點陣字幕候選軌依探測順序編一個固定的串流索引（不論是否已完成，編號不會在 PlaybackInfo 與串流請求之間移動）；已完成者列為 `IsExternal`、`DeliveryMethod: External`、`Codec: srt`、`Title: "... (OCR)"`，`DeliveryUrl` 為 `/Videos/{itemId}/{sourceId}/Subtitles/{index}/0/Stream.srt`（只接受 srt）。原點陣串流照舊列出、照舊只能原樣直投。
- 兩者都走 `media.Handler.ServeExtracted`（新種類 `ocr_subtitle`）：生產防護（轉換參數 409）、只限原生會話（Web 會話 403 `web_playback_disabled`，且不觸達解析器）、授權先於任何 OCR 查詢、Range／HEAD／條件請求、並發與頻寬上限（計入來源本身的播放）、撤銷即斷流、`nosniff`、sandbox CSP；Content-Type 固定為 `application/x-subrip; charset=UTF-8`。不存在、看不到、還在排隊、沒有文字的答覆一致（預設 404，G48.3 可設 403）。
- `internal/adapter/media`、`http`、`compat` 只認 `media.ExtractedResolver` 介面；會啟動行程的實作在 `internal/platform/runtime`，架構測試 `TestDeliveryPackagesCannotRunEncoders` 照舊成立。

## 可觀測

- 指標（`/metrics`，[指標說明](metrics.md)）：`jelee_subtitle_ocr_queued`、`_running`，以及 `_jobs_completed_total`、`_jobs_failed_total`、`_jobs_dropped_total`、`_pictures_total`、`_recognized_total`、`_reused_total`、`_rejected_total`、`_skipped_total`、`_throttled_total`。
- 日誌：每個工作結束記一行（軌數、圖片數、句數、耗時；不含路徑、檔名或字幕文字），失敗記固定代碼（`subtitle_ocr_source_changed`、`_too_large`、`_busy`、`_bitmap_invalid`、`_unavailable`）。
- `jelee-cli doctor`：檢查 `subtitle_ocr` 逐一回報 tesseract 與 4 個語言資料（容器路徑與專案 `.tools` 路徑），只雜湊不執行；缺席為 warn，啟用 OCR 時缺執行檔或缺所設定語言的資料為 fail。另列出是否啟用、語言、每分鐘上限與並發。代碼見[故障排查](troubleshooting.md)。

## 準確率與資源開銷

**方法**（`TestRealOCRAccuracyAndCost`，`JELEE_OCR_HOST_RUNTIME=true` 時執行）：以 `bitmapsubtest.RenderText` 把已知文字畫成廣播字幕樣式（填色＋描邊、透明底），編成真正的 PGS（1920×1080）或 VobSub（720×480）軌，經 `bitmapsub` 解碼與前處理，再由**沙箱內**的固定 Tesseract 辨識（與生產相同的 helper、Landlock、seccomp，只有 glibc 改用宿主的明示開發政策）。字元正確率 = 1 − 編輯距離／參考字元數；比較前合併空白（中日文去除空白）、彎引號視同直引號、全形 ASCII 標點視同半形。英文用 x/image 內附的 Go 字型；Arial、Times、微軟正黑體（msjh）、細明體（mingliu）、微軟雅黑（msyh）、游ゴシック由 `JELEE_OCR_FONT_DIR` 指向本機字型目錄讀入，不提交。

**實測**（2026-10-05，AMD Ryzen 7 9850X3D、WSL2，每個 Tesseract 行程單執行緒，`-run` 逐案依序執行）：

| 案例 | 格式 | 語言 | 圖片 | 字元正確率（錯／總） | 整句正確 | 平均 ms／張 | p95 ms | 前處理圖平均 KiB |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Go Regular 48 px 白字黑框 | PGS | eng | 12 | 100.00%（0／376） | 12／12 | 145 | 171 | 86 |
| Go Bold 48 px 黃字黑框 | PGS | eng | 12 | 100.00%（0／376） | 12／12 | 136 | 147 | 90 |
| Go Italic 48 px（斜體） | PGS | eng | 12 | 100.00%（0／376） | 12／12 | 139 | 154 | 88 |
| Go Regular 48 px 黑字白框 | PGS | eng | 12 | 100.00%（0／376） | 12／12 | 136 | 152 | 86 |
| Go Regular 48 px 深色底框 | PGS | eng | 12 | 99.73%（1／376） | 11／12 | 149 | 166 | 69 |
| Go Regular 48 px 兩行一張 | PGS | eng | 6 | 100.00%（0／382） | 6／6 | 160 | 191 | 104 |
| Go Regular 64 px | PGS | eng | 12 | 100.00%（0／376） | 12／12 | 142 | 159 | 91 |
| Go Regular 24 px 細框（DVD） | VobSub | eng | 12 | 100.00%（0／376） | 12／12 | 132 | 139 | 100 |
| Go Bold 30 px（DVD） | VobSub | eng | 12 | 100.00%（0／376） | 12／12 | 149 | 171 | 127 |
| Go Regular 16 px 細框（DVD 極小） | VobSub | eng | 12 | 99.73%（1／376） | 11／12 | 140 | 165 | 113 |
| Arial 48 px | PGS | eng | 12 | 99.20%（3／376） | 10／12 | 150 | 169 | 83 |
| Times Italic 44 px（襯線斜體） | PGS | eng | 12 | 100.00%（0／376） | 12／12 | 152 | 180 | 182 |
| 微軟正黑體 52 px | PGS | chi_tra+eng | 12 | 98.31%（2／118） | 10／12 | 224 | 260 | 45 |
| 微軟正黑體 68 px | PGS | chi_tra+eng | 12 | 100.00%（0／118） | 12／12 | 247 | 281 | 67 |
| 微軟正黑體 32 px（DVD） | VobSub | chi_tra+eng | 12 | 98.31%（2／118） | 10／12 | 224 | 261 | 91 |
| 細明體 52 px（明體） | PGS | chi_tra+eng | 12 | 95.76%（5／118） | 10／12 | 214 | 226 | 46 |
| 微軟雅黑 52 px | PGS | chi_sim+eng | 12 | 96.61%（4／118） | 10／12 | 212 | 234 | 47 |
| 游ゴシック 52 px | PGS | jpn+eng | 12 | 99.26%（1／135） | 11／12 | 207 | 239 | 51 |

典型錯誤：Arial 的大寫 `I` 讀成 `|`；直引號讀成彎單引號；「火」讀成「痰」、「桌」讀成「昌」、「就」讀成「融」；明體的一句被拆成多行並夾雜英文字母；日文「じゃない」多出「や」。

**調整紀錄**：同一組樣本上，原本「高度 <40 放大 3 倍、<80 放大 2 倍」使 50–80 px 的中日文行準確率降到 87–93%；改為 <24／<48 後中日文回到 96–100%，英文不變或更好。全域門檻（Tesseract 預設）對部分大號描邊中文行給出空結果或整句英文亂碼，改用 `thresholding_method=1` 後消失。Go 字型案例的下限（95–97%）寫在測試裡，任何解碼、極性或前處理的退步都會讓測試失敗。

**資源開銷**：

- **每張圖一個行程**：英文約 130–160 ms、中日文約 210–250 ms（大部分是行程啟動、沙箱驗證與載入語言模型；Tesseract 內單執行緒）。一部約 1500 句的電影，在預設每分鐘 60 張下約 25 分鐘完成（重複圖片只辨識一次），純 CPU 時間約 3.5 分鐘（英文）／5.5 分鐘（中日文）；提高每分鐘上限與並發可縮短，CPU 也同比例增加。
- **記憶體**：單一 Tesseract 行程峰值常駐約 100–175 MiB（`RUSAGE_CHILDREN` 的最大值，中日文較高），受 2 GiB 位址空間上限約束；並發 N 時最多 N 倍。Jelee 端每張進行中的圖片只保留裁切後的灰階圖（平均 45–180 KiB），解碼一張 PGS／VobSub 圖片 0.02–0.2 ms，不配置整個畫布。
- **磁碟**：抽出的點陣軌只在工作期間存在工作目錄（每來源 ≤256 MiB），結束即刪；SRT 快取通常每軌數十 KiB。

## 限制

- **只處理 Matroska 內的 PGS 與 VobSub。** DVB 點陣字幕（`S_DVBSUB`、MPEG-TS）的區塊／物件／CLUT 結構較複雜，列為後續；外掛 `.sup`／`.idx` 檔與 MP4 內的點陣字幕也尚未處理（它們仍可原樣直投）。
- **字型與樣式**：無襯線、粗體、斜體的英文表現最好；Arial 的 `I`／`|` 易混淆；明體（襯線）中文、簡體中文比黑體繁體稍差。手寫體、藝術字、描邊極細或無描邊且顏色接近背景的字、漸層與陰影效果未測。
- **語言**：只固定 eng、chi_tra、chi_sim、jpn 四種；其他語言軌會用設定的全部語言辨識，結果通常不可用。直書、注音／振假名（ruby）、上下兩種語言同時出現的雙語字幕會降低準確率。
- **背景與極性**：點陣字幕本身是透明底；帶不透明底框的字幕（部分 DVD）實測仍可讀。極性靠外圈與內部亮度比較，填色與描邊亮度接近、或只有陰影沒有描邊時可能判錯。
- **結構**：PGS 的 window 裁切、淡入淡出、同一畫面內位置不同的兩個物件會合成一張圖（由上而下讀）；VobSub 的 `delay:`、自訂顏色忽略，`.sub` 不做 start code 重新同步；PTS 32 位元回繞（約 13 小時）未處理；強制字幕（forced）旗標不另外標示。
- **單軌上限**：每軌 64 MiB（超過時 mkvextract 被終止、該來源不做 OCR），每軌 50000 張圖、8 MiB SRT。
- **結果需要人工判讀**：OCR 文字可能有錯，所以只作為額外軌並在標題標明「(OCR)」，從不取代原點陣軌。
- **真實片源未測**：以上數字來自合成樣本；真實藍光／DVD 字幕（各種字型、抗鋸齒、壓縮雜訊）的準確率列為 [擁有者驗證清單](owner-verification-queue.md) D25。

## 驗證

- 解碼：`internal/adapter/bitmapsub` 26 個測試＋2 個 fuzz（往返、多物件、裁切、epoch 物件重用、跨 PES 的 SPU、交錯行、所有截斷前綴、超大尺寸、錯誤 RLE、ODS 超限、PDS 長度不符、`.idx` 超大、filepos 越界、控制序列迴圈、事件數與像素預算），覆蓋率 99.3%。
- 服務：`internal/adapter/subtitleocr` 的假時鐘限流（任一 60 秒最多 N 張、隨機到達的性質測試）、並發峰值等於設定值、佇列上限與丟棄、失敗退避、取消後 staging 與工作檔清空、損壞軌保留已解出的句子、重複圖片只辨識一次、CPU 預算、修訂更替、原檔 SHA 不變。
- 沙箱：`TestRealTesseractInSandboxWithExplicitHostRuntime`（`JELEE_OCR_HOST_RUNTIME=true`）在 Landlock＋seccomp 下實際辨識、不寫工作目錄、輸入不變、未授權語言與被改的語言資料被拒；描述子與政策上限的單元測試。
- 全流程：`TestRealOCRPipelineFromMatroska`（另需 `JELEE_MATROSKA_HOST_RUNTIME=true`）以固定的 mkvmerge 把合成 PGS 與 VobSub 封裝成 MKV，再經沙箱內的 mkvmerge 識別、mkvextract 擷取、解碼與 Tesseract 產出兩條 SRT，原檔 SHA 不變、文字擷取快取未被寫入。
- 路由與相容層：原生會話取得 SRT、Web 會話 403 且不觸達解析器、轉換參數 409、畸形索引 404、未完成與不存在相同答覆、OCR 未開時沒有路由；相容層的固定編號、只列已完成者、只接受 srt；`leakRouteTable` 登記新路由（真 PG 遍歷）。
- 容器：[證據](evidence/ocr-runtime-image.txt)。以 `deploy/ocr/Dockerfile` 疊在含固定 glibc 與 `tools/ocr-smoke`（建置標籤 `jelee_ocr_tests`）的拋棄式基底上：`runtime-image -ocr` 通過；UID 65532、唯讀根、`--network none`、Docker 預設 seccomp 下 doctor 狀態全部 `verified`，經服務辨識出三句（其中一句重複只辨識一次）；以 root 執行、改一個語言資料、改一個函式庫，註冊全部被拒。
- 反向驗證：拿掉 Landlock 對語言資料的授權後真 Tesseract 失敗（`Could not initialize tesseract`）；把 OCR 查詢移到授權之前後「未授權不觸達 OCR」測試失敗；讓限流器直接放行後三個限流測試失敗。

## 授權

Tesseract 與 tessdata_fast 為 Apache-2.0（`/licenses/tesseract/tesseract-ocr/copyright` 等）；Leptonica 為 BSD-2-Clause；其餘函式庫保留各自授權，各 Debian 套件的 `copyright` 隨 OCR 層放在 `/licenses/tesseract/<套件>/`（libstdc++、libgomp 的聲明即既有 gcc-14-base copyright，libresolv 的即既有 libc6 copyright）。Debian 原始碼套件可由各套件的 `sourcePackage`／`sourceVersion` 取得，尚未收存；與其他可選工具相同，Linux 檔案只用於本地與實驗容器，不發布公共映像。詳見 [第三方工具](THIRD-PARTY-TOOLS.md) 與 [授權合規](LICENSE-COMPLIANCE.md)。
