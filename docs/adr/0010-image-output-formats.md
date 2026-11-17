# ADR 0010：圖片輸出格式（WebP 用純 Go 無損編碼、AVIF 不提供）與裁切

- 狀態：已採納
- 日期：2026-10-29
- 相關需求：G40.6（並推進 G40.7、G40.8）
- 相關程式：`internal/adapter/images/pipeline.go`（`planOutput`、`smartCrop`、`imageColors`、`encodeImage`、`webpEncodeBytes`）、`internal/adapter/images/processor.go`、`internal/adapter/http/images.go`（`imageFormat`）、`internal/adapter/compat/images.go`、`internal/platform/config/images.go`、遷移 `000097_item_image_dominant_color`（暫定編號）
- 詳細規則：[圖片資產](../image-assets.md#处理与资源上限g406g4012)

## 需求原文

> G40.6 處理管線：按需縮放/裁剪/格式轉換（WebP/AVIF/JPEG 可配）、質量參數可配、保持寬高比、支持 focal point 或智能裁剪（可關閉）；處理並發受限（見 G41）並有超時與取消。

## 決定

1. **新增一個第三方相依：`github.com/HugoSmits86/nativewebp v1.3.0`（MIT）**，只用它的 `Encode`，只做 WebP **無損（VP8L）** 輸出。解碼仍用既有的 `golang.org/x/image/webp`。
2. **AVIF 不提供輸出**：`format=avif` 回 415 `image_unsupported`，設定 `JELEE_IMAGE_OUTPUT_FORMATS` 列 `avif` 啟動即失敗。AVIF 解碼也維持不支援（原本就是 415）。
3. **輸出格式可設定**：`JELEE_IMAGE_OUTPUT_FORMATS`（預設 `jpeg,webp`）、`JELEE_IMAGE_DEFAULT_FORMAT`（預設 `jpeg`，必須在清單內）、既有的 `JELEE_IMAGE_DEFAULT_QUALITY`（只影響 JPEG）。原生接口要了沒開放的格式回 415；相容層改用預設格式（上游客戶端只是表達偏好）。
4. **預設行為不變**：不帶 `format` 的請求仍是 JPEG，與升級前的位元組相同；JPEG 等比縮放的變體鍵（`fit-v1/max=…`、`jpeg`）沒有改，持久存放區裡既有的變體繼續有效。
5. **不做 `Accept` 協商**：瀏覽器的 `Accept` 幾乎都含 `image/webp`，協商等於把預設格式改成無損 WebP（檔案大好幾倍，見下），還要加 `Vary: Accept`。要 WebP 的呼叫端明確帶 `format=webp`。
6. **裁切是「填滿裁切」**：原生 `fillWidth`＋`fillHeight`（兩者都要、不可再帶 `width`／`height`），相容層 `FillWidth`＋`FillHeight` 同時出現時改為裁切語意。輸出比例＝框的比例，取原圖中該比例的最大區域，絕不放大（原圖不夠大時整個框等比縮小）。
7. **焦點用「智慧裁切」，不存焦點座標**：`JELEE_IMAGE_CROP=smart`（預設）在解碼後的位圖上取 96×48 的取樣格，以相鄰樣本的亮度差當細節量，沿可滑動的那一軸找細節最多的視窗；平坦圖片等同置中。`center` 永遠置中，`off` 把填滿請求改回等比縮放（等於關閉裁切）。不在 `item_images` 存焦點座標，因為那需要新的管理接口與前端操作，現階段沒有人會去設定它。
8. **快取鍵與 ETag 含格式與裁切**：記憶體快取鍵與持久變體鍵都綁定格式；填滿裁切的變體鍵另含裁切模式（`fill-v1/<mode>/max=…`）。ETag 是輸出位元組的 SHA-256，不同格式的位元組必然不同（檔頭就不同），所以 ETag 也不同。WebP 沒有品質參數，變體鍵裡的品質固定為 0，避免同一張圖因 `quality` 不同存好幾份相同位元組。
9. **G40.7 顏色**：平均色與主色調都從解碼後的位圖取最多 64×64 個等距樣本（透明疊在白底上，與 JPEG 輸出一致）；主色調是每色版 16 階直方圖中樣本最多那一格的平均。在**讀到內容的地方**計算：管理員上傳（本來就完整解碼一次）、內嵌封面（存入後以 `DescribeStoredImage` 再驗一次，失敗只是不記顏色）、圖片刷新（`image_refresh` 可呼叫同一個 `DescribeStoredImage`）。掃描入庫不讀檔（只寫路徑），所以本地／NFO 列的內容欄仍是空的，直到有程式讀取它的內容。

## 理由

### WebP 套件的評估

條件：純 Go、無 cgo、授權寬鬆（MIT／BSD／Apache）、能放進現有的記憶體預估。

| 候選 | 授權 | 編碼 | 評估 |
| --- | --- | --- | --- |
| `golang.org/x/image/webp` | BSD-3 | 只有解碼 | 已在用，不能編碼 |
| `github.com/chai2010/webp`、`github.com/kolesa-team/go-webp` | BSD／MIT | 有損＋無損 | 包 libwebp，需要 cgo，違反 Windows／`CGO_ENABLED=0` 建置要求，排除 |
| `github.com/gen2brain/webp` | MIT | 有損＋無損 | 把 libwebp 編成 WASM 再用 wazero 執行：純 Go 但會在程序內編譯並執行 WASM，WASM 的線性記憶體不在 Go 堆積上、不受現有預估約束，也多出一整個執行環境的相依樹；排除 |
| `github.com/deepteams/webp` v1.2.8 | MIT | 有損 VP8＋無損 | libwebp 的純 Go 移植，約 3.5 萬行，含 `unsafe` 與 amd64／arm64 組合語言，2025 年才出現、版本更新頻繁；有損編碼器是本專案最難審查的部分，暫不採用 |
| **`github.com/HugoSmits86/nativewebp` v1.3.0** | **MIT** | **只有無損 VP8L** | 約 1,400 行、沒有 `unsafe`、沒有組合語言，唯一相依是已在用的 `golang.org/x/image`（要求 ≥ v0.24.0，本專案 v0.46.0）；記憶體用量可以量測並寫進預估。**採用** |

結論：目前沒有夠可靠、可審查的純 Go **有損** VP8 編碼器，所以用無損 VP8L。

### 無損的代價

開發機量測（nativewebp v1.3.0，預設壓縮等級）：

| 圖片 | JPEG q85 | WebP 無損 | 倍數 |
| --- | --- | --- | --- |
| 640×640 合成照片（漸層＋雜訊，最壞情況） | 48 KB | 787 KB | 16× |
| 1024×1024 同上 | 119 KB | 2,028 KB | 17× |
| 640×147 介面截圖 | 29 KB | 51 KB | 1.8× |
| 640×360 插圖 | 40 KB | 267 KB | 6.6× |

- 照片類素材用無損 WebP 一定比 JPEG 大，所以預設格式維持 JPEG。WebP 的實際用處是**保留透明度**（Logo、ClearLogo、ClearArt 等去背圖：JPEG 只能疊白底），以及只接受 WebP 的客戶端。
- 輸出超過 `JELEE_IMAGE_MAX_OUTPUT_BYTES`（預設 2 MiB）時回 413 `image_too_large`，與 JPEG 的規則相同；大尺寸雜訊多的照片要 WebP 時可能遇到。
- **記憶體**：編碼器自己會複製 NRGBA、展平像素、建 match chain 與符號陣列，實測約 73 B／像素（含 2 MiB 雜湊表頭）。預估按 **80 B／像素＋4 MiB** 加進同一份單圖預算（G40.12），解碼前就拒絕：預設 96 MiB 預算下，1024×1024 的 WebP 要約 84 MiB 加上解碼，原圖很大時會 413；640×640（預設尺寸）約 36 MiB。
- **取消**：nativewebp 的 `Encode` 不看 context，編碼開始後不能中途取消；它只受輸出像素上限（`JELEE_IMAGE_MAX_OUTPUT_DIMENSION`²）約束，開發機上 1024² 不到 1 秒。逾時在編碼結束後照常生效，准入槽與預算一直持有到它返回。
- 它忽略寫入錯誤、並可能對意外輸入 panic：Jelee 用會記住第一個錯誤的有界寫入器並以 `recover` 包住，兩者都變成這次渲染失敗。

### AVIF 延後

AVIF 編碼需要 AV1 編碼器（libaom／SVT-AV1／rav1e），目前沒有可用於正式環境的純 Go 實作；包 C 函式庫需要 cgo，WASM 方案的問題與上表 `gen2brain/webp` 相同，而且 AV1 編碼的 CPU 與記憶體成本遠高於 WebP。因此 AVIF 維持 415，等有可靠的純 Go 編碼器再評估。

## 影響

- `go.mod` 新增 `github.com/HugoSmits86/nativewebp v1.3.0`；授權全文保存在 `internal/adapter/images/LICENSE.nativewebp`，正式容器另附於 `/licenses/nativewebp/LICENSE`，見 [授權合規](../LICENSE-COMPLIANCE.md#圖片縮放依賴)。
- 遷移 000097（暫定）新增 `item_images.dominant_color`，與平均色一樣只在有內容時存在；降級只刪欄位（顏色可由原圖重算）。中繼資料備份包含這一欄，舊備份沒有這一欄時匯入為未知。
- 長時間負載、Windows 與真實客戶端驗證見[擁有者核對清單](../owner-verification-queue.md) B9、C26、D27。
