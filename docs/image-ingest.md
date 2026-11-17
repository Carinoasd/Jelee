# 圖片掃描入庫（G40.2、G40.3）

每次 catalog sync 完成後，Jelee 把媒體庫內每個條目的圖片引用寫進 `item_images`：條目目錄裡依 G40.2 命名的圖片檔記為 `local` 來源，NFO 的 `<thumb>`／`<fanart>`／`<art>` 指向 root 內檔案者記為 `nfo` 來源，指向網址者記為 `remote` 來源（只記錄，不抓取）。這是[擁有者任務二](owner-dev-tasks.md)的第 1 項；遠端抓取（`image_refresh`）與管理 API 是第 2、3 項，不在本文範圍。

## 掛載點與理由

掃描 → 發布 → 條目同步的現有流程：

1. `inventory_scan` 任務走訪媒體根目錄，把每個檔案（含 `kind='image'` 的圖片、`nfo`、`video`）寫進 `job_inventory`，成功後發布成媒體庫的盤點基準（`library_inventory_baseline_data`，以 snapshot／revision／generation 標識）。這一步不碰條目。
2. `catalog_sync` 任務（自動同步或管理員接受缺失後觸發）以租約分批推進 `publish`→`sources`→`missing`→`pending`→`done` 各階段，建立／更新條目、媒體來源與外掛字幕；全部階段提交後，runner 再跑外掛軌檢查與（選用的）內嵌封面兩段收尾。

入庫掛在第 2 步的收尾：`done` 之後、內嵌封面之前，由 `internal/platform/jobs` 的 `ingestItemImages` 逐頁呼叫 `Store.IngestItemImages`。理由：

- **掃描與 catalog 發布都會經過這裡。** 掃描只產生盤點；條目是 catalog sync 建立的，圖片必須掛到條目上，所以最早只能在條目同步完成後做。自動同步與「接受缺失」兩條發布路徑都會以 `catalog_sync` 結束。
- **只讀資料庫。** 圖片檔名、大小、mtime 已經在發布的基準裡；條目目錄來自 `media_sources`（電影、單集）與 `item_directory_sources`（劇集、季）；NFO art 已在 `item_metadata_facts`。因此入庫不開檔、不連網，每頁是一個只讀寫資料庫的交易，不受檔案系統或外部服務影響。
- **自己的 lease fence。** 每頁在一個交易內先以 `catalogSyncLease` 核對租約（取消即停），最後以 `guardedJobUpdate` 做租約圍欄後提交；過期或被取代的 worker 寫的東西全部回滾。這與外掛軌檢查、內嵌封面兩段收尾相同。
- **排在內嵌封面之前。** local／nfo 列優先於內嵌封面，內嵌封面那一段會跳過已被這些列占用的 Primary 槽。
- **不做成新的 catalog sync 階段。** 新階段需要改 `catalog_sync_requests.phase` 的 CHECK、加游標欄、改 `FinishCatalogSync` 的完成條件；入庫是冪等的，租約換手後從頭重跑即可，不需要持久游標。

每次 catalog sync 都會走訪整個媒體庫（每頁 64 個條目，`domain.ItemImageIngestBatch`），而不只看變動的目錄：一個條目的圖片可能在影片沒變時新增或消失，NFO art 也可能被另一個任務改動。沒有變化的列不寫入，所以穩定狀態下每頁只有固定幾句查詢。

## 讀取什麼

每頁以一次查詢各取：

| 資料 | 來源 |
| --- | --- |
| 條目、種類、父條目、季號 | `items`、`item_parent_links`、`item_directory_sources`；季號取 `catalog_scan_items.season`，沒有時取 `seasonNumber` 事實 |
| 電影／單集的影片 | `media_sources`（同一條目多版本時依路徑排序，前面的版本先取槽） |
| 目錄內的圖片檔 | 發布基準中 `kind='image'`、`attributes_known`、與目標基準同 generation 且被發布該基準的掃描觀察到（`observed_revision`）的列；被忽略規則排除而保留舊觀察的檔案不算。以新函數 `inventory_parent_directory(path)` 與部分索引按目錄查詢 |
| 電影目錄的影片數 | 發布基準中直接位於該目錄的影片，按所屬條目去重（未入庫的影片各算一部） |
| NFO art | `item_metadata_facts` 中 `field='art'`、`source='nfo'` 的值；NFO 所在目錄由 `nfo_origin.sourceId`（媒體來源或目錄來源）與 `rootId` 決定 |
| NFO 指向的檔案 | 發布基準中該相對路徑的圖片列（主鍵查詢） |

若發布的基準已不是媒體庫目前的基準（之後又發布了新的盤點），該頁不寫入、回報 `Stale`，整段結束；新基準自己的 catalog sync 會重新入庫。讀取期間以 `FOR SHARE` 鎖住媒體庫列，發布新基準要等這一頁提交。

## 命名對應（G40.2）

目錄清單交給 `images.RecognizeImageNames`（`internal/adapter/images/naming.go`），大小寫不敏感，副檔名 jpg/jpeg/png/webp/avif/gif/bmp/tiff。結果中的類型直接是 `item_images` 的類型；`fanart` 存為 `Backdrop`，`fanart1..n` 是 Backdrop 的 index 1..n。

| 條目 | 讀的目錄 | 規則 |
| --- | --- | --- |
| Movie、HomeVideo | 每個影片所在目錄 | 電影規則：`<影片名>-poster` 優先於 `poster`／`folder`／`cover`／`movie`，以及 backdrop、fanart、logo、clearlogo、banner、clearart、landscape、thumb、disc |
| Episode | 每個影片所在目錄 | 只認 `<影片名>-thumb` 與 `<影片名>.episode-thumb`，存成 `Thumb` |
| Series | 登記的劇集目錄 | 劇集規則（`series-poster` 優先），`seasonNN-*` 等季圖不掛在劇集上 |
| Season | 自己的季目錄，以及父劇集目錄 | 順序：季目錄自己的檔案 → 劇集目錄的 `seasonNN-*`（季 0 也認 `season-specials-*`）→ `season-all-*`。每個槽取第一個 |

- **season-all 展開。** 劇集目錄的 `season-all-poster` 等會掛到該劇集下**每一個**季條目，前提是該季沒有更具體的同槽圖片。季號未知的季只拿 `season-all-*`。
- **一個目錄多部電影。** 目錄中直接有兩部以上電影（以條目計，多版本算一部）時，`poster.jpg` 這類描述整個目錄的共用名稱一律不採用，只採用以各自影片名開頭的 `<影片名>-poster`；只有一部時才採用共用名稱。
- **無法使用的清單。** 目錄圖片超過 10,000 個或名稱總長超過 4 MiB 時，`RecognizeImageNames` 拒絕整份清單，該條目的 local 列這一輪全部保持不動（不新增也不移除）。同一槽出現只差大小寫的同名檔（例如 `logo.png` 與 `LOGO.png`）時，該槽視為不明確，既有列保持不動。影片名本身不能作為命名輸入時，電影仍採用目錄共用名稱；單集則略過。

## NFO art（G40.3）

NFO 讀取器把 `<thumb>`、`<fanart>`、`<art>` 存成 art 事實（`kind`、`location`、`season`）。入庫依序處理每個項目：

- **類型。** poster → Primary，fanart／backdrop → Backdrop，banner、clearart、clearlogo、logo、landscape、thumb 對應同名類型，disc／discart → Disc；其他（例如 keyart）略過。單集 NFO 的 `<thumb>`（讀取器存成 poster）是單集縮圖，存成 `Thumb`，與 `<影片名>-thumb` 檔同槽。
- **季。** 劇集 NFO 中帶 `season` 屬性的項目只掛到該季號的季條目，不掛在劇集上。
- **網址。** `https://` 網址寫成 `remote` 列，不在這裡抓取（抓取是 `image_refresh` 的工作），因此外部服務失敗不會影響掃描與發布。`http://` 網址不符合 `item_images` 的網址規則，略過。
- **相對路徑。** 先做百分比解碼並把 `\` 換成 `/`，以 NFO 所在目錄為基準組合；結果必須仍在同一 root 內（`../` 可以，但不能超出 root）、不是絕對路徑、不含冒號、是圖片副檔名，而且該檔在發布基準中存在，才寫成 `nfo` 列並記下大小與 mtime。找不到的檔案這一輪略過，檔案出現後的下一次掃描會補上。`smb://` 等其他協定略過。
- **順序。** 每個槽取第一個可用的項目；fanart 依 NFO 順序（只算可用的項目）編 Backdrop 的 index 0、1、2…，檔案與網址共用同一個編號序列。
- **來源已不存在。** NFO 的媒體來源或目錄來源已刪除時，相對路徑無從解析，只保留網址項目。

## 寫入規則

寫入一律透過 `upsertItemImage(ctx, tx, domain.Actor{}, in)`（非手動、不寫稽核），與既有內嵌封面相同。

- **手動列。** 遷移 000094 為 `item_images` 加上 `manual` 欄：管理員透過 `UpsertItemImage`（`Manual=true`）寫入的列標記為手動。`upsertItemImage` 現在把手動列與鎖定列同樣對待：非手動輸入一律 `Skipped`，不改內容、來源或鎖定。遷移時既有的非 embedded 列（只可能來自人工寫入或元資料匯入）全部標為手動，掃描不會接手它們。
- **冪等。** 計畫中的列與既有列來源相同（root、路徑、網址、大小、mtime）時不寫入，`updated_at` 不變。檔案大小或 mtime 改變時更新該列並清掉已存的內容摘要，等之後重新讀取。
- **消失移除。** 掃描寫入的 `local`、`nfo` 列（非鎖定、非手動），若這一輪的計畫不再需要（檔案消失、改名、NFO 不再引用），刪除該列；刪除前先鎖條目，`DELETE` 本身再次以 `NOT locked AND NOT manual` 為條件，與手動寫入競爭時手動一方獲勝。只刪資料庫引用，**從不刪除或修改使用者的圖片檔**。
- **remote 列不移除。** 遠端引用之後也會由其他寫入者（例如刷新任務或元資料來源）產生，資料表無法區分掃描寫的網址列，因此掃描只新增或更新 remote 列，不刪除。NFO 刪掉某網址後，該 remote 列會留到管理員刪除為止。
- **鎖定。** 鎖定列永遠不動，即使檔案已消失。
- **併發。** 條目在同一頁中被刪除時略過該條目；其他錯誤讓整頁回滾、任務失敗（與外掛軌檢查一致）。

## 遷移 000094（暫定編號）

- `item_images.manual boolean NOT NULL DEFAULT false`；既有非 embedded 列設為 `true`。
- `inventory_parent_directory(text)`：檔案的父目錄（root 為 `''`），與 Go 的 `parentDirectory` 一致（測試對照）。
- 部分索引 `library_inventory_image_directory_idx (library_id, snapshot_id, root_id, inventory_parent_directory(path)) WHERE kind='image'`：只有圖片列負擔這個索引。
- 降級移除索引、函數與 `manual` 欄，保留所有 `item_images` 列（掃描寫入的列成為一般的未鎖定引用，舊版程式照常使用）。

## 驗證

- 真 PG 端到端（`TestCatalogSyncIngestsItemImages`，真實掃描器＋runner）：電影（`<影片名>-poster` 勝 `poster`、fanart 圖庫、clearlogo）、同目錄兩部電影、root 目錄的電影、劇集、三個季（季目錄 `folder.jpg` 勝 `season01-poster`、`season02-*`、`season-specials-poster`、`season-all-banner` 展開到兩季）、單集縮圖三種來源（同名 `-thumb` 檔、NFO 相對路徑、NFO 網址）；NFO 的 root 內路徑、`../../` 逃逸被拒、網址不抓取；無變化重掃所有列（id、`updated_at`）不變；刪檔後掃描列移除、季橫幅退回 season-all（同一列更新）、鎖定列與手動列不變；所有圖片檔前後 SHA-256 一致；分頁 API 的參數檢查、未完成同步拒絕、基準被取代時 `Stale` 且不寫入。
- 遷移往返（`TestItemImageIngestMigrationRoundTrip`）：既有列的手動分類、SQL 父目錄函數與 Go 一致、非手動輸入不改手動列、降級保留所有列。
- 規劃單元測試（`TestPlanItemImages*`）：多電影目錄、無法使用的影片名、超長清單、大小寫衝突、季圖優先順序、NFO 路徑與網址的各種拒絕情形。
- Runner 單元測試（`TestItemImageIngestPass`）：游標分頁、Stale 停止、錯誤視為儲存錯誤、取消。
- 反向驗證：拿掉消失移除的 `DELETE`、拿掉多電影目錄過濾、拿掉手動列保護，端到端測試各自失敗。

## 限制

- 只認同一目錄的檔案；`extrafanart/` 等子目錄只有被 NFO 引用時才會入庫。
- NFO 的 `season="-1"`（所有季）在讀取器中沒有保留季號，會被當成劇集本身的圖片。
- 管理員手動編輯的 art 事實（`source='manual'`）不入庫，因為沒有 NFO 目錄可解析相對路徑。
- 每次 catalog sync 走訪整個媒體庫；十萬條目規模約 1,600 頁，尚未做長時間或大規模量測。
