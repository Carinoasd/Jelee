# ADR 0013：不提供 Remux（正式與開發者模式皆然），以容器內 seek 索引代替關鍵幀表

- 狀態：已採納
- 日期：2026-10-31（第 10 階段 B12；擁有者清單 E17 記為「已決定」）
- 相關需求：G10.2、G10.6、G10.7、G10.10、G10.11、G13.6、G23.1、G24.2、G29.2
- 相關程式：`internal/adapter/media/`（`direct.go`、`guard*.go`、`prefetch_*.go`）、`internal/adapter/containerlayout/`、`internal/adapter/compat/playback.go`、`internal/architecture/no_exec_test.go`、遷移 `000110_media_source_layouts`
- 相關決定：[ADR 0007](0007-no-encoder-direct-play-only.md)（正式映像不含 ffmpeg、只做原檔直投；其「後續」各段記錄了第 9、10 階段在同一原則下的延伸）、[ADR 0001](0001-external-process-start.md)
- 相關文件：[直投](../direct-delivery.md)、[相容層](../compat-matrix.md)、[維護任務](../maintenance-jobs.md#g136-其餘項目的對照)

## 背景

需求裡有兩處讓人以為「伺服器應該做」的項目：

> G10.2 唯一允许的两种投递：Direct Play（原文件原样流式返回）与 Remux（仅换容器/重封装，不重编码音视频；默认关闭，需显式开启且注明会改变字节流）。
>
> G10.6 启动加速：媒体探测与关键帧/时长索引缓存、moov/faststart 标记提示……
>
> G10.7 Seek：客户端侧基于本地索引与关键帧表定位……

另外 G29.2 的 worker pool 列了「索引构建」與「Remux（开发者模式）」，G13.6 列了「关键帧索引缓存清理」，G23.1 的投遞方式列了 `remux`，G24.2 的視訊串流寫「Range/Remux」。ADR 0007 第 1 條已決定「不重新封裝（remux 也不實現）」，但沒有寫出理由，也沒有處理「開發者模式下能不能開」與「關鍵幀表」。第 10 階段補齊直投（B1–B11）時需要把這兩點定下來。

## 決定

1. **不提供 Remux，正式模式與開發者模式都一樣。** 投遞只有 Direct Play：原檔、外掛字幕與外掛音軌按原位元組經 HTTP Range 送出。要求換容器的請求（相容層路徑副檔名或 `Container` 與原檔容器不同、`videoCodec=copy` 等）照舊在任何處理器之前回 409 `transcode_disabled`；能力宣告 `remux=false`，相容層 `SupportsDirectStream=false`。開發者模式不新增 `dev-remux` 之類的開關（`dev-transcode` 已依 ADR 0007 第 3 條顯示 unavailable）。G29.2 的「Remux worker」因此不存在。
2. **不建完整的關鍵幀表，也沒有關鍵幀索引快取。** G10.6 的「關鍵幀／時長索引快取」由兩樣既有東西實作：
   - **時長**：已在探測快取（`probe_cache`）。
   - **索引**：容器自帶的 seek 索引是否存在、在檔頭還是檔尾、位置與長度——MP4／MOV 的 `moov`（含 `stss` 等樣本表），Matroska／WebM 經 SeekHead 可達的 Cues。由 B2 的純 Go 有界讀取器（`internal/adapter/containerlayout`，每檔最多讀 64 KiB 的盒子／元素頭）判定，記在 `media_source_layouts`（遷移 000110），用於 `layoutHints`（`faststart_recommended`、`no_seek_index`，只給建議）與直投時對索引範圍的 `posix_fadvise(WILLNEED)` 預讀。

   G10.7 的「關鍵幀表定位」是客戶端的事：客戶端用容器裡的索引決定要 Range 哪一段，伺服器只保證 Range、`If-Range`、多段與斷線即停。G13.6 的「關鍵幀索引快取清理」因此標為不適用；`media_source_layouts` 是可重建的衍生狀態，資源刪除時級聯清除，判定器版本或檔案戳變了就重讀，不需要另設清理任務。
3. **只偵測、只建議，不改原檔。** 沒有 seek 索引或 `moov` 在檔尾的檔案，Jelee 只在播放資訊與網頁版本面板給建議文字，管理員自己在副本上處理（G10.10）；Jelee 不改寫、不搬移 `moov`、不補 Cues。

## 理由

### Remux

- **需求是上限，不是義務。** G10.2 寫「唯一允許的兩種」，並要求 Remux「默认关闭，需显式开启」。不實作是「預設關閉」最嚴格的形式，不違反任何一條。
- **真正有用的 Remux 需要編碼器程式庫。** 客戶端需要 Remux 的情況幾乎都是「不支援 MKV，要 MP4」。把 Matroska 重新封裝成 MP4 實務上只能用 ffmpeg 的 libavformat（`-c copy`）；ffmpeg 不在正式映像（ADR 0007 第 2 條、`tools/manifest.json` 的 `productionAllowed: false`），`TestDeliveryPackagesCannotRunEncoders` 也禁止投遞路徑出現它。用 Go 自寫 MP4 封裝器等於把一整套新的容器寫入程式碼放進投遞路徑，攻擊面與正確性風險都遠大於收益，而且要在串流中途產生 `moov`，必須先讀完整個檔案或做分段封裝（`moof`），後者就是 G10.1 禁止的「分片封裝」。
- **正式映像裡的 mkvmerge 幫不上忙。** mkvmerge 只能輸出 Matroska，MKV→MKV 對不支援 MKV 的客戶端沒有意義；它在映像裡只用來識別軌道（`mkvmerge --identify`）與配合 mkvextract 擷取字幕與字型。
- **改變位元組就失去直投的保證。** 原檔直投可以用 SHA-256 驗證「送出的就是原檔」，Range、`If-Range`、ETag 與快取都建立在這上面；Remux 的輸出每次都是新產生的位元組，Range 要對應到動態產生的流，需要暫存檔或重新計算偏移，會引入新的磁碟用量與一致性問題。
- **開發者模式也不例外。** 開發者模式的開關在執行期切換，只要二進位裡有 Remux 路徑，生產模式「不可達」就只能靠開關而不是靠程式不存在；這正是 ADR 0007 拒絕 `dev-transcode` 的同一個理由。

### 關鍵幀表

- **沒有消費者。** 伺服器端的關鍵幀表在上游只供 HLS 切片對齊用；Jelee 不切片、不轉碼（G10.1），沒有任何程式需要知道每個關鍵幀的位置。
- **成本高。** 完整關鍵幀表要讀過整個檔案（或至少整個樣本表與每個 Cluster 頭），對 20 GB 以上的檔案是整檔 I/O；探測與掃描都刻意只讀有界的頭部資料。
- **容器自帶的索引就是給客戶端用的。** 客戶端 seek 時本來就讀 `moov` 或 Cues；伺服器能做的、有意義的事是知道它在不在、在哪裡：在檔尾時提早預讀、沒有時提醒管理員。這兩件事 B2 已經做了，並以表驅動測試、`FuzzRead` 與真實封裝器輸出（`TestReadRealMuxerOutput`）驗證。

## 代價

- 不支援 Matroska 的客戶端（多數瀏覽器、部分電視盒）無法播放 MKV 來源，只會得到 `NoCompatibleStream` 與原因碼（`container_unsupported`），伺服器不救場。管理員若要服務這類客戶端，需要自己準備 MP4 版本（多版本會自動聚合，G20）。
- 沒有 Cues 的 MKV 與 `moov` 在檔尾的 MP4 起播或拖動可能較慢；伺服器只能預讀與建議，不能修。
- G23.1 的投遞方式永遠是 `direct`；G24.2 的「視訊串流（Range/Remux）」只有 Range。

## 守門

- `internal/architecture/no_exec_test.go`：`TestDeliveryPackagesCannotRunEncoders`（投遞、相容層、HTTP 套件不可達任何建立程序的 API，也不得出現 `ffmpeg` 字面量）。
- `internal/adapter/media/guard_params_test.go`、`guard_test.go`：換容器與 `copy` 類參數在拒絕清單內；相容層 `TestPlaybackInfoDeclarationsAreFixed` 鎖定 PlaybackInfo 可接受的宣告成員。
- 相容層串流路由：路徑副檔名或 `Container` 與原檔容器不同即 409（[相容層](../compat-matrix.md)）。
- `internal/adapter/containerlayout`：讀取上限 64 KiB、`FuzzRead`、讀取前後 SHA-256 不變。

## 何時重新評估

- 擁有者在 E17 表示要另一種做法（例如接受一個與預設映像分離、受隔離的選用 Remux 映像層，類似 OCR 層），且同意為它補齊沙箱、資源上限與「投遞套件不可達」的守門測試；屆時需要新的 ADR 修訂本決定與 ADR 0007。
- 出現伺服器端確實需要關鍵幀位置的功能（例如 G40.1 的章節圖「生成」若被核准），這時也只建該功能所需的有界索引，不做通用關鍵幀表。
