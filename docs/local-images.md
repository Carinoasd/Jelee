# 本地海報縮圖

狀態：本地 Primary 海報子項已實作及驗證，尚未完成十萬圖片規模驗收。

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

## API

```text
GET /images/Primary/{itemID}?width=320&height=480&quality=85&format=jpeg
HEAD /images/Primary/{itemID}?width=320&height=480
```

需要有效 Bearer session 及該媒體庫的查看權限，Web 與原生 session 都可使用。處理前與交付前各重新查驗權限及 item 的來源綁定；快取命中、HEAD、304 也必須通過。API 不接受檔案路徑或 URL。

省略尺寸時縮入 640 × 640，保持比例且不放大；只給一個尺寸時限制該方向，輸出仍受配置的最大邊長限制。明寫尺寸須為 1–2048，品質須為 1–100；實際輸出限制以配置為準。PNG 透明區域以白底合成。回應提供 `image/jpeg`、內容長度與強 ETag，支援 `If-None-Match` 的 304；快取政策為 `private, no-cache, must-revalidate`。

滿載回 503 與 `Retry-After`；沒有權限或沒有可用海報回 404；超過處理預算回 413，不支援的圖片格式回 415。只有省略的參數使用預設值，重複與未知參數均拒絕。

## 來源與快取

本段只接受具有唯一影片來源的項目，同目錄候選依固定優先序選擇 `<影片基名>-poster.jpg`、`.jpeg`、`.png`，其次為 `poster.jpg`、`.jpeg`、`.png`。檔名比對不區分大小寫，同一候選的大小寫碰撞一律拒絕，即使它的順位較低。

每個目錄最多 **10000 個條目、檔名累計 4 MiB**，每批讀取 128 個條目，只保留六個候選的位置；必須完成整個有界列舉才選圖，超限不使用部分結果。開啟與複製前後查核根目錄、父目錄、影片和候選圖片；處理後再核對來源身分、大小、時間與完整內容雜湊，拒絕已觀察到的替換及保留時間戳的內容修改。這些前後檢查不是檔案系統的原子快照。

快取只保存編碼後的小圖，採容量、筆數、LRU 與 TTL 限制。每次請求仍讀取並核對原圖來源；暖命中省下解碼與縮放。key 綁來源及變體參數，沒有公開原始路徑。回應 Body 持有准入槽直到關閉，因此慢客戶端持有已淘汰的圖也受同時處理數限制。程序重啟後按需重建快取。

取消採合作式方式：I/O 與處理邊界檢查 context，CPU 解碼或縮放結束前保留准入槽，取消後不交付圖片。標準解碼器沒有可強制中止 CPU 運算的 context API；設定的逾時不代表 CPU 指令會在該瞬間終止。停止流程會等待仍在處理的請求結束，再關閉資料庫。

縮放使用固定 [Go x/image v0.46.0](https://pkg.go.dev/golang.org/x/image@v0.46.0/draw) 的 `ApproxBiLinear`，從已解碼來源直接寫入唯一縮圖，不建立額外全尺寸 RGBA 複本。JPEG 的 RGB／CMYK 轉換及尚未驗證的子格式會拒絕；尺寸預檢須包含 progressive 係數與 PNG 16-bit／交錯工作區，不能只算寬 × 高 × 4。

## 已執行驗證

[來源雜湊與驗證證據](evidence/local-images.json)記錄本段範圍、固定依賴、失敗修正與原始日誌雜湊。測試事件包含父測試及子測試，不能相加解讀成不重複案例數。

- Windows 相關單元套件 723 個通過事件、1 個平台略過；Linux race 709 個通過事件、零略過。涵蓋 progressive JPEG、16-bit／Adam7 PNG、預檢拒絕、LRU／容量／TTL、原圖異動、取消、逾時及持有回應時的停止等待。
- HTTP Windows 整包 444 個通過事件、零略過，套件覆蓋率 89.2%；Linux race 圖片專項 44 個事件、零略過。驗證認證前准入、503、參數、錯誤、撤權、HEAD／304、寫入失敗及回應關閉。
- Linux race 真實 PostgreSQL／Fx／HTTP 10 個通過事件、零略過。兩張實際 JPEG／PNG 由 128 × 64 縮為 32 × 16，以正式預設密碼雜湊及登入、授權與撤權流程取圖；四個原檔雜湊保持，暫存清空，停止後 HTTP 關閉、lifetime 取消、資料庫連線歸零。
- 33 個 Go 檔格式、八個套件 vet、兩項架構測試、三個正式執行檔建置及模組校驗通過。90 份既有 SQL、五份直播核心、授權與需求原文保持。

第一輪 Linux 整合測試因 `t.TempDir` 建立的目錄可被其他使用者讀取而收到 404，改用測試自有私有目錄後通過，產品權限檢查保持。Windows 初次受限 token／路徑分隔符／檔案共享模式問題與原始失敗日誌均保留。Windows 的大小寫碰撞素材依平台略過，Linux 有實際執行。

905 份最終來源凍結後完成 PostgreSQL 驗收；先前單元測試的 902 份來源與最終差異只有四個整合測試素材檔。小型整合的暖回應位元組一致不單獨證明快取命中，命中計數由 processor 測試驗證；正常停止與處理中等待分別由整合及單元測試涵蓋。這輪沒有量測圖片規模 RSS。

## 尚未涵蓋

其他圖片角色與完整命名、WebP／AVIF 等輸入輸出、裁切與 EXIF 方向處理、NFO／遠端／內嵌來源、鎖定管理、持久變體目錄、相容圖片路由、tag 長快取、前端與圖片重建工作尚未完成。十萬圖片處理、混合並發與至少 24h 的驗收另行執行。本段不能據此將 G40 或 G42.10 標為完成。
