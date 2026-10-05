# 目錄瀏覽、條目詳情與檔案資訊（G34.3）

自有 API 中給所有登入使用者（原生與網頁工作階段皆可）讀取的三組目錄路由。授權一律在同一條 SQL 內重用 `libraryVisibleSQL`（使用者未停用未刪除、管理員或 `library_acl` 授權），與 `/api/v1/items/{id}`、相容層瀏覽模組相同；看不到的媒體庫、父項與條目，答覆與不存在的完全一致。

## `GET /api/v1/items`

同一路由有兩種形式：

- **游標形式**（原有，行為不變）：只帶 `cursor`、`limit`（1–100，預設 50；空字串視為預設）。依條目 ID 排序，`pagination.nextCursor` 為空表示結束。
- **位移形式**：只要出現 `offset`、`libraryId`、`parentId`、`type`、`sort`、`order`、`q` 任一參數即切換。回應信封相同，`pagination` 另含 `offset` 與 `total`，`nextCursor` 固定為空字串。與 `cursor` 同時出現回 400 `invalid_request`。

選擇「位移＋總數」而非擴充游標的理由：依名稱、日期或年份排序後，以條目 ID 為鍵的游標無法接續；分頁 UI 需要總數；共用的 `BrowseItems` 讀取（相容層已在用）本來就是位移＋`count(*) OVER()`，位移上限 1,000,000、每頁上限 100。

| 參數 | 說明 |
| --- | --- |
| `limit` | 1–100，預設 50 |
| `offset` | 0–1,000,000，預設 0 |
| `libraryId` | 只列該媒體庫的條目（各層級）。只會縮小範圍，不會放寬授權 |
| `parentId` | 只列直接子項：媒體庫 ID → 頂層條目；影集／季 ID → 季／單集。可與 `libraryId` 併用 |
| `type` | `Movie`、`HomeVideo`、`Series`、`Season`、`Episode`；可重複參數或以逗號分隔，重複值去重 |
| `sort` | `name`（排序標題，否則標題，不分大小寫）、`premiereDate`、`productionYear`；逗號分隔最多 3 個不重複鍵，剩餘同值以 ID 決定 |
| `order` | `asc`（預設）或 `desc`，套用到所有排序鍵；沒有日期／年份的條目升冪在前、降冪在後 |
| `q` | 標題子字串，不分大小寫，最多 128 個字元；`%`、`_`、`\` 一律按字面比對 |

未知參數、格式錯誤的 UUID（含大寫）、未知類型或排序鍵、重複的單值參數皆為 400。位移形式的條目除 `id`、`libraryId`、`title`、`kind` 外，另含 `premiereDate`、`productionYear`（未知時省略）；`parentId` 只在連結到影集／季時出現，與游標形式一致。

授權：看不到或不存在的 `libraryId`／`parentId` 都回 200、`data: []`、`total: 0`，兩者回應逐位元組相同。搜尋與總數只計算呼叫者可見的條目。

## `GET /api/v1/items/{id}/details`

一般使用者可讀的顯示中繼資料。不接受查詢參數。

```json
{"data":{"id":"…","libraryId":"…","title":"Arrival","kind":"Movie","sortTitle":"…","originalTitle":"Story of Your Life","tagline":"…","overview":"…",
 "premiereDate":"2016-11-11","productionYear":2016,"genres":["Drama"],"externalIds":[{"type":"tmdb","value":"329865","default":true}],
 "nfo":{"status":"valid","readAt":"2026-09-01T08:30:00Z","fields":["overview","genres"]}}}
```

- `nfo.status`：`unread`（尚無確認過的 NFO 讀取）、`valid`、`missing`、`nfo_invalid`；`readAt` 只在已讀取時出現；`fields` 列出目前值來自 NFO 的顯示欄位。
- 不含：伺服器檔案路徑、根目錄、NFO 來源檔／根目錄 ID、摘要、指紋、供應商來源網址與抓取資訊。這些仍只在管理員 `GET /api/v1/items/{id}/metadata`，該路由維持原狀。
- 失效或無法通過驗證的事實（類型、外部 ID）會被略過；無法驗證或修訂版號超前的 NFO 觀察視為 `unread`，不讓整頁失敗。
- 媒體庫 ID、不存在或不可見的條目回 404（設定 G48.3 為 403 時回 403）。

## `GET /api/v1/items/{id}/sources`

檔案資訊，任何工作階段種類皆可讀；不接受查詢參數。重用播放資訊的同一條 SQL（`listSourcesSQL`），差別只在不限定 `client_kind='native'`：使用者、工作階段（未撤銷、未過期且屬於該使用者）、媒體庫授權與條目仍在同一語句確認。

每個來源（依版本品質分數由高到低）提供容器、內容類型、`probed`、大小、時長、位元率、版本標籤（`displayName`、`qualityScore` 等）、視訊軌（編碼、設定檔、解析度、影格率、主軌）、內嵌音軌（編碼、語言、聲道、Atmos、預設／強制）、內嵌字幕（編碼、格式、語言、預設／強制）與外掛字幕／音軌（ID、種類、格式、推定編碼、語言、標題、forced、SDH、default、評論、字元集、大小）。

刻意不提供：

- 任何絕對或相對路徑、根目錄、檔名（版本標籤只取自檔名推導的標記）。
- 任何直投網址。外掛軌的 `url` 欄位在此路由的 schema（`MediaSourceInfo`）中不存在，處理器與服務層都清空它；`/api/v1/sources/{id}/stream`、`/subtitles/…`、`/audio/…` 仍只給原生工作階段，網頁工作階段呼叫仍為 403 `web_playback_disabled`。

## 驗證

- 單元：`internal/domain/item_details_test.go`、`internal/app/catalog_browse_test.go`（詳情／檔案資訊接線與驗證）、`internal/adapter/http/items_test.go`（參數解析、舊游標形式不變、各種 400、網頁工作階段可讀詳情與檔案資訊且回應不含 `/api/v1/sources`、`/subtitles/`、`/audio/`、`/stream`、`url`、檔名）。
- 真 PG：`internal/adapter/postgres/catalog_details_test.go`，兩位網頁使用者各授權一個媒體庫：媒體庫／父項篩選、類型、年份與日期排序、位移與總數、字面萬用字元搜尋、跨授權不可見；詳情與檔案資訊不含根目錄與路徑；看不到的條目在三條路由上與不存在的回應逐位元組相同；撤銷的工作階段讀不到檔案資訊；網頁工作階段仍讀不到播放資訊。
- `access_leak_test.go` 登記 `/details`、`/sources` 為 by-ID 路由，真 PG 下 404／403 兩種設定都確認不洩漏隱藏標記。
