# mkvtoolnix 與 MediaInfo（E4）

E4（[擁有者驗證清單](owner-verification-queue.md)）核准依 G30.6 引入 MediaInfo 與 mkvtoolnix：固定版本、官方來源、SHA256、授權記錄，預設不強制安裝。本文記錄引入方式、執行邊界與三項功能：MediaInfo 探測補充（G19.1）、內嵌文字字幕擷取（G15.5）、ASS 字型附件擷取（G15.7）。

## 版本、來源與校驗

全部固定在 `tools/manifest.json` 的 `matroskaTools`；`tools.MatroskaToolSpec` 從編進程式的清單讀出，不讀 PATH、不信任宿主檔案。

| 工具 | 平台 | 官方來源 | 校驗取得方式 |
| --- | --- | --- | --- |
| mkvtoolnix 102.0 | linux-amd64 | `https://mkvtoolnix.download/appimage/MKVToolNix_GUI-102.0-x86_64.AppImage` | 上游不發 AppImage 的 SHA256；以官方 `.zsync` 控制檔的 SHA-1 `ccfb9bb1…` 與長度 62746600 比對 HTTPS 下載，一致後計算並固定 SHA256 `c66345b3…` |
| mkvtoolnix 102.0 | windows-amd64 | `https://mkvtoolnix.download/windows/releases/102.0/mkvtoolnix-64-bit-102.0.zip` | 抄自官方 `sha256sums.txt`，下載後重算一致 |
| MediaInfo CLI 26.05 | linux-amd64 | `https://mediaarea.net/download/binary/mediainfo/26.05/MediaInfo_CLI_26.05_Lambda_x86_64.zip` | MediaArea 不發校驗檔；2026-10-04 HTTPS 下載後計算並固定 |
| MediaInfo CLI 26.05 | windows-amd64 | `https://mediaarea.net/download/binary/mediainfo/26.05/MediaInfo_CLI_26.05_Windows_x64.zip` | 同上 |
| Debian libstdc++6 14.2.0-19、zlib1g 1:1.3.dfsg+really1.3.1-1+b1、libgmp10 2:6.3.0+dfsg-3 | linux-amd64 | `deb.debian.org` pool | SHA256 取自 packages.debian.org 下載頁，下載後重算一致 |

**Linux 為何用 AppImage／Lambda 版。** mkvtoolnix 自 80 版起所有命令列工具都連結 Qt6Core，官方 Linux 只提供 AppImage 與各發行版套件；發行版套件要整串 Qt／ICU 依賴與系統 libc，AppImage 則自帶 22 個函式庫（Qt6Core、ICU 56、GLib、GnuTLS、Boost 1.85 等），只缺 glibc、libstdc++、zlib、GMP。MediaInfo 的官方 Lambda 版把 libmediainfo、libzen、libcurl 靜態連入，只需 glibc、libstdc++、zlib。缺的部分由既有 `mediaRuntime`（Debian 13 glibc 2.41）加上上表三個 Debian 套件補足，同一組固定 glibc 也給 ffprobe 用。

**AppImage 不執行。** `scripts/squashfs_reader.py` 以純 Python（有界）讀取 AppImage 內偏移 188392 的 SquashFS 4.0（gzip；也支援 zstd），只取清單列出的普通檔案：`usr/bin/mkvmerge`、`mkvextract`、`mkvpropedit` 與 22 個 `usr/lib/*.so*`。AppImage runtime、AppRun、FUSE 與宿主 `unsquashfs` 都不需要也不執行；連結只在映像內解析，絕對連結與跳出根目錄的連結一律拒絕。

**閉包。** 每個生產可執行檔的 `closure` 列出它的完整依賴（容器路徑）：mkvmerge／mkvextract 各 32 個（22 個 AppImage 函式庫＋libstdc++／zlib／GMP＋7 個 glibc），mediainfo 8 個。沙箱要求函式庫集合恰為閉包、SONAME 唯一、至多 32 個，多一個未用的也拒絕。

## 安裝與校驗（G51）

```sh
make bootstrap-matroska            # 或 python3 scripts/matroska-tools.py bootstrap [--tool mkvtoolnix|--tool mediainfo] [--offline]
python3 scripts/toolchain.py bootstrap --tool mkvtoolnix   # 同一入口，只有點名時才安裝
make tools-verify                  # 必要工具＋已安裝的可選工具；未安裝者印出「Skipped optional …」
make matroska-tools-verify         # 嚴格：兩者都必須已安裝
make matroska-toolchain-test       # 離線合成歸檔的安裝器測試（SquashFS gzip/zstd、zip、篡改、委派）
```

Windows：`scripts/make.ps1 bootstrap-matroska`、`scripts/make.ps1 matroska-tools-verify`（以 PATH 上的 Python 執行同一安裝器；只處理 zip）；`tools-verify.ps1` 在有 Python 時一併校驗已安裝的可選工具。

- 只接受無憑證 HTTPS；`JELEE_TOOLS_MIRROR` 可換 HTTPS 鏡像，檔名、大小、SHA256 不變；`--offline` 只用快取。快取不符就刪除該檔並失敗。
- 只取清單列出的成員；zip 檢查成員大小與 SHA256，未列出的成員（例如 `mkvtoolnix-gui.exe`、MediaInfo 的 `Plugin/` 目錄）不落盤。安裝到 `.tools/matroska/<工具>/<版本>/<平台>/`，先在 staging 寫入再改名發布；記錄寫在 `.tools/matroska-installed/`。
- `verify` 比對歸檔、記錄、每個檔案的 SHA256 與實際檔案集合（多一個、少一個都拒絕），再以固定 `--version`／`--Version` 確認版本字串。已安裝檔被改動時失敗並保留現場，不自動修復。
- `.bin/mkvmerge`、`.bin/mkvextract`、`.bin/mkvpropedit`、`.bin/mediainfo` 是開發用包裝，執行前重驗雜湊；不經 shell、不走 PATH。它們不是生產沙箱。

## 生產執行與沙箱（G09.2、G29.4、G42.7）

只在 linux-amd64 容器中、以 `internal/platform/mkvruntime` 註冊；Windows 與其他平台回報 `platform_unsupported`，功能停用。檔案位置：`/usr/lib/jelee/mkvtoolnix/{mkvmerge,mkvextract}`、`/usr/lib/jelee/mkvtoolnix/lib/`、`/usr/lib/jelee/mediainfo`、`/lib/x86_64-linux-gnu/{libstdc++.so.6,libz.so.1,libgmp.so.10}`，授權在 `/licenses/{mkvtoolnix,mediainfo,runtime}/`。

E1（[ADR 0001](adr/0001-external-process-start.md)）照舊：以 `os.StartProcess` 啟動 Jelee 自己的 helper（`--internal-media-tool-helper`），helper 驗證後以 `execveat` 執行已驗證的檔案物件。與 ffprobe 的 `--internal-probe-helper` 分開，ffprobe 的描述子、政策與快取身分逐位元組不變。

- **身分**：執行檔與閉包每個函式庫都以固定路徑開啟、雜湊比對清單，生產另要求檔案與每層祖先目錄為 root 擁有、群組／其他不可寫，且服務本身非 root；helper 在套用政策前再驗一次，執行的是已驗證的同一個 inode。
- **參數陣列**：三個固定模式，argv 由 helper 依模式組出，沒有 shell、沒有字串拼接：
  - `mediainfo --Output=JSON /proc/self/fd/0`
  - `mkvmerge --identify --identification-format json /proc/self/fd/0`
  - `mkvextract /proc/self/fd/0 tracks <id>:t<id>… attachments <id>:a<id>… --quiet`

  描述子只帶模式、固定路徑與整數 ID（軌道 0–127 至多 32 個、附件 1–4096 至多 64 個，嚴格遞增），ID 來自 mkvmerge 的識別結果，不來自請求；輸出檔名由 ID 推得，放在 runner 為每次執行新建的私有工作目錄。使用者輸入不會成為參數或輸出路徑。
- **輸入**：媒體以 `probe.Open`（`os.Root`、逐段 lstat、拒絕 symlink 與特殊檔）唯讀開啟後作為 stdin 傳入；工具重開 `/proc/self/fd/0`。Landlock 只對該檔案物件給 READ_FILE，其所在目錄仍不可讀。
- **Landlock／seccomp**：執行檔 READ+EXECUTE、函式庫 READ（載入器另給 EXECUTE）、輸入檔 READ；只有擷取模式對私有工作目錄給 MAKE_REG／WRITE／TRUNCATE／REMOVE_FILE／READ_DIR。seccomp 允許清單與 ffprobe 相同（無網路、無子行程、無 namespace）。
- **資源**：NOFILE 128、NPROC 128、AS 2 GiB、CORE 0；CPU 60 秒（擷取 600 秒）；FSIZE 0（擷取為每檔 64 MiB，超過即被核心終止）。runner 層：識別與 MediaInfo 並發 2、逾時 1 分鐘、stdout 4 MiB 上限（超過即終止且不回傳內容）、stderr 只計數 64 KiB；擷取並發 1、逾時 10 分鐘。大輸出一律寫私有目錄的暫存檔，不放管道記憶體。
- **清理**：每次執行的私有目錄在讀完後刪除；runner 根目錄是 `jelee-service-mkv-<pid>…`，關閉時刪除，當機殘留由啟動時的 scratch 清掃（已加入 `ServiceMKV`）回收。程序群組在結束、逾時、取消時整組 SIGKILL 並回收。
- **結束碼**：mkvtoolnix 的 1（警告、輸出完整）視為成功，2 視為此檔案無法處理；MediaInfo 非 0 視為失敗。

**mkvpropedit 不使用。** G19.1 提到以 mkvmerge／mkvpropedit 處理章節與附件，但 mkvpropedit 唯一用途是就地改寫 Matroska 檔；Jelee 不寫入原媒體（G10、G15 原檔雜湊不變）。讀取章節與附件已由 MediaInfo 與 mkvmerge／mkvextract 完成，需求中沒有必須寫入的操作，因此 mkvpropedit 只固定與校驗（`productionAllowed: false`），不進映像、不註冊、不提供任何 API。

## 探測補充：MediaInfo（G19.1）

探測對 Matroska／WebM 來源在 ffprobe 之後以同一個已開啟的檔案執行 MediaInfo，結果解析為 `MediaMetadata.matroska`（探測資料 schema 2、解析器 `media-metadata-v3`；與 G40.4 的 attached_pic 記錄合併後的版本，兩者各自開發時都用過 v2，合併後升 v3 讓任何一邊寫下的快取都重新探測一次）：

- `chapters`：第一個 edition 的章節起點與標題；
- `attachments`：附件檔名（依檔案順序，ID 即 mkvtoolnix 的 1 起附件 ID）與是否為字型；
- `tags`：容器層標籤（Title、Movie、Encoded_Application 等固定欄位與自訂標籤，名稱限識別字，值 ≤512 位元組）；
- `streamTitles`：各軌的 Matroska 軌名，對應探測串流索引。

檔案系統欄位（檔名、日期、大小）一律不保留；任何一項不符白名單就整份丟棄，不留下部分結果。MediaInfo 失敗或輸出不可用時只省略補充，ffprobe 結果照常快取；取消、逾時、忙碌與沙箱失敗則讓本次探測失敗並重試。補充的有無與 MediaInfo 執行檔、閉包、argv 一起折入探測身分的 `RuntimeSHA256`／`ArgumentsSHA256`，有、無補充的快取不會混用；版本升為 2 後既有快取以新身分重新探測一次。

## 擷取到可重建快取（G15.5、G15.7）

設定（預設關閉，需要目錄與直投）：

| 環境變數 | 設定檔 | 說明 |
| --- | --- | --- |
| `JELEE_ENABLE_MATROSKA_EXTRACTION` | `matroska.enableExtraction` | 開啟擷取路由 |
| `JELEE_MATROSKA_CACHE_ROOT` | `matroska.cacheRoot` | 既有、私有（0700）的絕對目錄，不放其他東西 |
| `JELEE_MATROSKA_CACHE_MAX_BYTES` | `matroska.cacheMaxBytes` | 快取總上限，預設 1 GiB（256 MiB–1 TiB） |

第一次要用到某來源時，先以 mkvmerge 識別，再一次擷取該來源全部可擷取項目：`S_TEXT/UTF8`（srt）、`S_TEXT/ASS`、`S_TEXT/SSA`、`S_TEXT/WEBVTT`（vtt）文字字幕至多 32 軌；字型附件（副檔名 ttf/otf/ttc/otc/woff/woff2 或字型 MIME）每個 ≤32 MiB、合計 ≤128 MiB、至多 64 個。位圖字幕（PGS、VobSub、DVB）與 `D_WEBVTT/SUBTITLES`（WebM 形式，mkvextract 102.0 不支援）不擷取；位圖字幕仍只能隨原檔直投。開啟[字幕 OCR](subtitle-ocr.md)（G15.6）時，OCR 背景工作另以同一個 mkvextract 模式把 PGS／VobSub 軌抽到私有工作目錄、辨識成額外的 SRT 軌後即刪除，不進本快取、不取代原軌。

- **原樣**：mkvextract 依 Matroska 內儲存的內容寫出（文字字幕為 UTF-8，含 BOM），不渲染、不燒錄、不轉碼、不轉檔，也不改原媒體；快取檔與內嵌位元組一致（字型逐位元組相同，見實測）。
- **快取版本**：`<cacheRoot>/<sourceID>/<修訂>/`，修訂由來源 ID、大小與修改時間雜湊；檔案改變就是新修訂，舊修訂刪除。擷取期間來源被改寫或替換即放棄結果。
- **上限與清理**：單一來源的輸出超過 256 MiB 不入快取；總量超過上限時依最近使用時間淘汰；超過一小時的 staging 殘留在下次寫入時清除。整個快取可隨時刪除，下次請求重新擷取。非 Matroska（例如 MP4）記為空項，不重複識別。
- **同一來源只擷取一次**：並發請求共用同一次執行；請求取消會取消擷取，下一個請求重試。

### 取得方式

自有 API（僅原生會話）：

- `GET|HEAD /api/v1/sources/{id}/embedded-subtitles/{index}`：`index` 是 `subtitleTracks[].index`（探測串流索引）。
- `GET|HEAD /api/v1/sources/{id}/attachments/{attachmentId}`：`attachmentId` 是 `attachments[].id`。

`GET /api/v1/items/{id}/playback` 在擷取可用時為 `extractable` 的字幕與字型附件加上 `url`；檔案資訊（`/sources`）不含任何 URL。相容層：PlaybackInfo 中可擷取的內嵌文字字幕改為 `DeliveryMethod: External` 並帶 `DeliveryUrl`（`/Videos/{itemId}/{sourceId}/Subtitles/{index}/0/Stream.{srt|ass|ssa|vtt}`，只接受該軌自身格式），`MediaAttachments` 列出附件與字型的 `DeliveryUrl`（`/Videos/{itemId}/{sourceId}/Attachments/{index}`，`index` 為附件的探測串流索引）。

兩條自有路由與相容層都走 `media.Handler.ServeExtracted`，與外掛軌 `ServeTrack` 同一條直投路徑：生產防護（轉換參數 409）、只限原生會話（Web 會話 bearer 或 cookie 都是 403 `web_playback_disabled`，且不觸達解析器）、方法限制、Range／HEAD／條件請求、並發與頻寬上限（項目計入來源本身的播放）、撤銷即斷流、`nosniff` 與 sandbox CSP。授權先以與 `/stream` 相同的 SQL 確認會話與媒體庫權限，未授權者不會觸發擷取；不存在、看不到、不可擷取、工具未安裝的答覆一致（預設 404，G48.3 可設 403）。擷取查詢的逾時為 10 分鐘（`ExtractTimeout`），其餘查詢仍是一般逾時。Content-Type 由固定表決定：字幕依格式加 `charset=UTF-8`，字型為 `font/ttf`、`font/otf`、`font/collection`、`font/woff`、`font/woff2`。

**不可觸及編碼器（G10.11）**：`internal/adapter/media`、`http`、`compat` 只認 `media.ExtractedResolver` 介面；會啟動行程的實作在 `internal/platform/runtime`（`matroskaService`），架構測試 `TestDeliveryPackagesCannotRunEncoders` 照舊成立。

## 缺工具時（G51.10）

- 服務：工具缺、被改、平台不支援或快取根不可用時，對應能力停用並記一次 `matroska_runtime_unavailable` 類警告；路由仍在（若已設定），一律答覆為找不到；播放資訊不提供 URL；MediaInfo 缺席時探測照常、只少補充。
- `jelee-cli doctor`：新檢查 `matroska_tools` 逐一回報 mkvmerge、mkvextract、mediainfo（容器路徑與專案 `.tools` 路徑），只雜湊不執行；缺席為 warn，啟用擷取時缺 mkvmerge／mkvextract 為 fail。代碼說明見 [故障排查](troubleshooting.md)。
- 測試：真工具測試在工具未安裝時以原因跳過；`JELEE_REQUIRE_MEDIA_TOOL_TESTS=true` 時改為失敗。

## 驗證

- 安裝器：`scripts/test_matroska_tools.py`（8 項，含 mksquashfs 生成的 gzip／zstd 映像、跳出連結、截斷映像、zip 成員篡改、多餘檔案、壞快取刪除、`toolchain.py --tool` 委派）。實際下載三種 Linux 歸檔與 Windows 兩個 zip 並以清單校驗（Windows 版本檢查需在 Windows 執行）。
- 夾具：`tools/gen-fixtures` 新增 `subtitles-fonts.mkv`（SRT＋ASS 軌、軌名、章節、字型附件佔位檔）與 `styled.ass`、`JeleeSyntheticSans.ttf`，全部自建、不提交。
- 沙箱：`TestRealMatroskaToolsInSandboxWithExplicitHostRuntime`（需 `JELEE_MATROSKA_HOST_RUNTIME=true` 與 `JELEE_MATROSKA_FIXTURE`，以宿主 glibc 加專案固定檔案組成明示的開發政策）在 Landlock＋seccomp 下實跑三個工具，原檔 SHA 不變；`TestNativeToolSandboxConfinesExtraction` 以假 mkvextract 驗證只可寫私有目錄、讀不到其他檔、不能開 socket、FSIZE 上限（需專屬 UID 的執行緒預算，共用 UID 時跳過；2026-10-04 以靜態測試二進位在 `--network none`、UID 54321 的拋棄式容器中通過，ffprobe 既有的 `TestNativeSandboxDeniesAmbientAccessAndPreservesFDInput` 同場通過，確認共用沙箱重構未改變其行為）。
- 容器：[證據](evidence/matroska-runtime-image.txt)。生產映像（UID 65532、唯讀根）中三個工具 `verified`；以 `tools/matroska-smoke`（建置標籤 `jelee_matroska_tests`，只放拋棄式測試映像）從唯讀掛載的夾具擷取 SRT、ASS 與字型，字型雜湊等於原附件，原檔 SHA 不變；換掉一個函式庫後 mkvmerge／mkvextract 拒絕註冊，以 root 執行全部拒絕；`doctor --checks matroska_tools` 三項 ok；映像內無 ffmpeg、mkvpropedit、shell。
- 反向驗證：拿掉 Landlock 對輸入檔的規則後，真工具測試的 mkvmerge 與 mkvextract 失敗（讀不到媒體）；拿掉 `matroskaService` 的授權查詢後「未授權不觸達擷取」失敗；`ServeExtracted` 不覆寫 Content-Type 後路由測試失敗。

## 授權

mkvtoolnix 為 GPL-2.0（`COPYING`，映像內 `/licenses/mkvtoolnix/COPYING`），隨附元件授權列於上游 README（`/licenses/mkvtoolnix/README.md`）；MediaInfo 為 BSD-2-Clause（`/licenses/mediainfo/LICENSE`）；Debian 函式庫的 copyright 在 `/licenses/runtime/`。MKVToolNix 原始碼位置記於清單 `sourceURL`；AppImage 內第三方函式庫的完整對應原始碼尚未收集，與 BtbN ffprobe 相同，只用於本地與實驗容器，不發布公共映像。詳見 [第三方工具](THIRD-PARTY-TOOLS.md) 與 [授權合規](LICENSE-COMPLIANCE.md)。
