# Webhook（G12.1–G12.6）

Jelee 在媒體、掃描、播放、登入等變更發生時，把事件以簽章過的 HTTPS POST 送到管理員設定的端點。本文說明事件清單、載荷格式、簽章驗證、重試與死信、安全設計與管理 API。

- 契約與純邏輯：`internal/domain/webhook.go`（事件、脫敏、簽章、退避）、`internal/app/webhooks.go`（埠介面與組裝步驟）。
- 管理服務與背景投遞器：`internal/app/webhook_admin.go`、`internal/app/webhook_dispatcher.go`。
- 儲存：`internal/adapter/postgres/webhooks.go`，遷移 `000068_webhooks`。
- 網路：`internal/adapter/events`（結果分類）走 `internal/platform/outbound` 的受防護客戶端（G11.4）。
- 密鑰封存：`internal/platform/secretbox`（AES-256-GCM）。

## 啟用

| 設定（環境變數 / 設定檔 `webhooks.*`） | 預設 | 說明 |
| --- | --- | --- |
| `JELEE_ENABLE_WEBHOOKS` / `enableWebhooks` | `false` | 啟用投遞器與管理 API；需要帳號功能（`JELEE_ENABLE_ACCOUNTS=true`） |
| `JELEE_WEBHOOK_MASTER_KEY` 或 `JELEE_WEBHOOK_MASTER_KEY_FILE` | 無 | **必填**。32 位元組主鑰，標準或 URL 安全 base64，或 64 個十六進位字元。只能從環境變數或檔案讀，不能寫在設定檔。沒有主鑰時啟用 webhook 會在啟動時以明確錯誤拒絕 |
| `JELEE_WEBHOOK_ALLOWED_HOSTS` / `allowedHosts` | 空 | 端點主機白名單（逗號分隔、小寫、精確比對）。空表示任何公網主機 |
| `JELEE_WEBHOOK_CA_FILE` / `caFile` | 空 | 額外信任的 PEM 根憑證（自簽 CA 的端點用），附加在系統根憑證之上 |
| `JELEE_WEBHOOK_POLL_MILLISECONDS` / `pollMilliseconds` | 1000 | 投遞器閒置時的輪詢間隔（100–60000） |
| `JELEE_WEBHOOK_BATCH` / `batch` | 50 | 每輪最多展開的事件數與領取的投遞數（1–500） |
| `JELEE_WEBHOOK_CONCURRENCY` / `concurrency` | 4 | 本實例同時進行的投遞數（1–32） |
| `JELEE_WEBHOOK_LEASE_SECONDS` / `leaseSeconds` | 120 | 投遞租約（60–3600），必須長於最長一次嘗試 |
| `JELEE_WEBHOOK_RETENTION_DAYS` / `retentionDays` | 14 | 已結束（送達或死信）的事件與投遞日誌保留天數（1–365） |

產生主鑰：`openssl rand -base64 32`。**主鑰遺失或更換後，既有端點的密鑰無法解開**：投遞器記錄錯誤並讓投遞保持 pending（不會被打成死信），需要恢復原主鑰，或刪除端點後重建。

webhook 停用時（或主鑰未設定時），產生端完全不寫 outbox，執行的仍是原本的 SQL 語句，所以不會累積事件，也不依賴遷移 068 的表。

## 事件

### 信封

每次投遞的本文是一個 JSON 物件：

```json
{
  "eventId": "5f0c6a1e9b2d4f3a8c7e6d5b4a392817",
  "type": "playback.stopped",
  "version": 1,
  "occurredAt": "2026-10-04T12:00:00.123Z",
  "subject": { "kind": "session", "id": "0b7e…" },
  "data": { "userId": "…", "itemId": "…", "state": "stopped", "positionTicks": 12000000000, "completed": false }
}
```

- `eventId`：1–64 個 `[A-Za-z0-9_-]`，**重試與手動重放都不變**，消費端以它去重（G12.6）。
- `type`：`<領域>.<動作>`，發布後名稱不變；不相容的載荷變更以 `version` 遞增表示。
- `occurredAt`：UTC、毫秒精度。**不承諾全域順序**：不同事件可能亂序或同時到達；需要順序的消費端以 `occurredAt` 排序並容忍並列與倒序。
- `subject.kind` 由事件種類決定（見下表）；`subject.id` 是 Jelee 的不透明識別碼，絕不是路徑或名稱。

### 清單與接線狀態

| 類型 | subject | data | 觸發時機 |
| --- | --- | --- | --- |
| `media.added` | item | `libraryId`、`kind`（Movie／Episode／Season／Series）、`title` | 目錄同步（catalog_sync）自動建立條目時，與條目同一個 savepoint 內寫入 |
| `media.updated` | item | `libraryId`、`sourceId`、`reason` | 目錄同步改動既有條目的媒體來源時，與改動同一個 savepoint／交易：`source_added`（新檔案以新版本併入既有條目；新建條目只發 `media.added`）、`source_changed`（已追蹤檔案的大小或修改時間變了；只換解析器版本的重新規劃不發）、`source_missing`（未經接受的缺失檔案被標記缺失）、`source_restored`（標記缺失的檔案再次出現）。管理員的元資料編輯、刮削、NFO 套用**不發** |
| `media.deleted` | item | `libraryId` | 接受缺失檔案後的目錄同步刪除掃描建立的條目時 |
| `scan.started` | library | `jobId`、`priority`（manual／background） | 庫存掃描（`inventory_scan`）**第一次**被領取時，與領取同一個交易。以工作的 `generation`（每次領取 +1、永不倒退）判定，所以暫停後續跑、租約逾時後重試都不再發 |
| `scan.completed` | library | `jobId`、`missing`、`reviewRequired` | 庫存掃描在 `FinishJob` 成功結束、以及 ignore 發布路徑結束時 |
| `scan.failed` | library | `jobId`、`errorCode` | `FinishJob` 以失敗結束時。租約逾時後因嘗試次數用盡而失敗的路徑**未接** |
| `playback.started` | session | `userId`、`itemId`、`sourceId`（有時）、`positionTicks`、`paused` | 開始回報**新建**播放會話時（接回既有會話不發），與 INSERT 同一個語句 |
| `playback.paused` | session | `userId`、`itemId`、`positionTicks`（暫停時的位置） | 回報從播放轉為暫停（自有 API 與相容層共用同一個進度緩衝）。在下一次批次 flush 與會話狀態同一個語句寫入；同一個 flush 週期內多次暫停合併為一個事件，取最後一次暫停的時間與位置。flush 失敗時保留到下一次 flush |
| `playback.progress` | session | `userId`、`itemId`、`positionTicks`、`paused`（一定是 false） | **節流**：播放中（未暫停）的會話每 60 秒最多一個（`ProgressOptions.ProgressEventInterval`，預設 `app.DefaultProgressEventInterval`＝1 分鐘），在間隔到期後的第一次 flush（預設每 10 秒）寫入，`occurredAt` 是該 flush 帶的最後回報時間。間隔從會話開啟時起算；暫停中不發；結束的那次 flush 不發（位置由 `playback.stopped` 帶）。flush 失敗時下一次 flush 立刻補發 |
| `playback.stopped` | session | `userId`、`itemId`、`state`（stopped／failed／timed_out）、`positionTicks`、`completed` | 批次 flush 實際把會話從 active 結束時，與 flush 同一個語句；重複的停止不會再發 |
| `user.login` | user | `clientKind`（web／native） | 密碼登入成功（自有 API 與相容層共用） |
| `user.login_failed` | user | `failedLogins` | 已存在的帳號密碼錯誤；不存在的帳號名沒有 user 可指，不發 |
| `user.locked` | user | `failedLogins`、`lockedUntil` | 造成鎖定的那次失敗；鎖定期間的嘗試會在計數前被拒，不重複發 |
| `session.created` | session | `userId`、`clientKind`、`expiresAt`、`reason` | 與建立會話同一個交易：`login`（密碼登入與兩步驟驗證第二步完成，自有 API 與相容層共用）、`rotated`（會話輪換產生的新會話）、`share`（兌換分享連結的訪客會話） |
| `session.ended` | session | `userId`、`clientKind`、`reason` | 撤銷時與撤銷同一個語句，只對**尚未到期**的會話發：`logout`（撤銷自己的目前會話）、`revoked`（撤銷別的會話或全部會話）、`rotated`、`password_changed`、`password_reset`（資料庫操作員的本機重設）、`user_changed`（停用帳號或變更管理員身分）、`user_deleted`（軟刪除帳號）、`native_access_removed`、`two_factor_enabled`（啟用兩步驟驗證時撤銷其他會話）、`app_password_revoked`、`share_revoked`、`client_rule`（force_relogin 規則）、`client_kicked`。到期：`reason` 為 `expired`，`occurredAt` 是到期時間，由背景掃描每分鐘發一次（見下） |
| `nfo.written` | item | `jobId`、`libraryId` | NFO 寫回工作成功結束時，每個條目一個事件。由恢復租約補完的工作**未接** |
| `images.fetched` | item | `libraryId`、`jobId`、`images`（本批抓到內容的圖片數） | 圖片刷新任務（`image_refresh`）把遠端圖片內容寫入時，與該批結果同一個交易；每個條目每批一個事件，圖片跨批的條目每批各一個。抓取失敗、延後、被鎖定或手動值搶先而略過的不算 |
| `system.alert` | system | `code`、`component`（有時）、`severity`（warning／error） | 程序以下列固定 `code` 記錄 WARN 以上的日誌時（見「系統告警」）；管理 API 的測試送出也用這個類型，`data` 為 `{"test": true, "message": …}` |

除 `system.alert` 與到期的 `session.ended` 之外，事件都在產生變更的同一個 PostgreSQL 交易（或同一個語句）內寫入 `webhook_outbox`：變更提交才有事件，變更回滾事件也一起消失。另外，只有在有**啟用中的端點訂閱該類型**時才會寫入，避免沒人訂閱的事件佔空間。

### 會話到期

到期不改任何資料列，所以 `session.ended`（`reason: expired`）由背景掃描產生：webhook 投遞器執行時，每分鐘依 `(expires_at, id)` 順序找出水位之後已到期的會話（撤銷時間晚於到期時間的也算，因為它先到期），每個語句最多 500 個並在同一個交易推進水位（遷移 000112 的單列表 `session_expiry_watermark`，以列鎖讓多個實例不重複發）。升級時水位設在遷移當下，之前已到期的會話不補發；webhook 停用超過 24 小時期間到期的會話也不補發（只回補 24 小時內）。在到期前已撤銷的會話只會有撤銷那一次 `session.ended`。

### 系統告警

`system.alert` 來自程序日誌：執行期把日誌處理器包一層，凡是 WARN 或 ERROR 且帶有下列 `code` 的紀錄（不論設定的日誌等級是否輸出它）就收一筆告警。載荷只放 `code`、`component`（固定標籤才放）與 `severity`，**不放訊息文字或其他屬性**，所以不會帶出路徑或位址。

| code | 情境 |
| --- | --- |
| `probe_runtime_unavailable` | 媒體探測已開啟但隔離執行環境不可用（啟動時）或探測暫存清理失敗 |
| `matroska_*` | Matroska 擷取不可用（工具缺席、暫存不可用、平台不支援等固定原因）或其暫存清理失敗 |
| `subtitle_ocr_*` | 字幕 OCR 已開啟但不可用 |
| `embedded_cover_*`、`chapter_image_*` | 內嵌封面／章節圖擷取已開啟但缺少探測執行環境或圖片存放區 |
| `subtitle_utf8_cache_unavailable` | 衍生 UTF-8 字幕快取不可用 |
| `two_factor_key_invalid` | 主鑰無效導致兩步驟驗證不可用 |
| `probe_maintenance_failed`、`nfo_maintenance_failed` | 探測／NFO 快取的背景維護失敗 |
| `schedule_dispatch_failed` | 任務排程派送失敗 |
| `watch_unavailable` | 目錄監看停止 |
| `log_prune_failed` | 過期日誌檔無法刪除 |

同一個 `code` 每小時最多一個告警（`app.DefaultSystemAlertInterval`），待寫入的告警最多 64 筆；啟動期間（投遞器開始前）的告警會先保留，投遞器開始後寫入 outbox。webhook 停用時不寫入。資料庫或請求層級的錯誤（例如 `scan_unavailable`、`db_slow_query`、開發者模式的 `devmode_*`、客戶端控制）**不**發告警。

### 載荷脫敏（G12.4）

`domain.RedactWebhookData` 在寫入前與每次投遞前各檢查一次：

- 鍵名含 password、token、secret、api key、cookie、authorization、hash、salt、device id、IP 等的值一律換成 `"[redacted]"`；
- 字串中夾帶憑證（`token=`、`Bearer `、URL 的 `user:pass@`、PEM 區塊）整串換成 `"[redacted]"`；
- 本機路徑（POSIX 絕對路徑、Windows 磁碟／UNC 路徑、`~/`、`file:`）以及路徑類鍵名（`path`、`file`、`directory`…）的值只保留最後一段檔名；
- 字串最長 2048 位元組、清單 256 項、物件 64 個鍵、巢狀 4 層。

產生端也只放識別碼與計數：不放 IP、裝置識別碼、帳號名稱、根目錄路徑。資料庫裡的舊事件若不再通過目前的脫敏規則，投遞器會以 `invalid` 直接打成死信，不會送出。

## 請求與簽章（G12.4）

每次嘗試都是一個 `POST`，不跟隨重新導向（3xx 視為失敗），標頭：

| 標頭 | 內容 |
| --- | --- |
| `Content-Type` | `application/json` |
| `User-Agent` | `Jelee-Webhook` |
| `X-Jelee-Event-Id` | 與本文 `eventId` 相同，方便在解析前去重 |
| `X-Jelee-Timestamp` | 本次嘗試的 Unix 秒數（十進位） |
| `X-Jelee-Signature` | `v1=<hex>`；輪換寬限期內是 `v1=<新>, v1=<舊>` |
| 自訂標頭 | 端點設定的標頭（值以主鑰封存，從不回傳） |

簽章是 `HMAC-SHA256(secret, "<X-Jelee-Timestamp>.<原始本文位元組>")` 的十六進位。**密鑰是建立或輪換時回傳的整個字串（含 `whsec_` 前綴）的 UTF-8 位元組**。每次重試、手動重放都用嘗試當下的時間重新簽，所以舊事件重送仍會落在接收端的時間窗內，而不變的 `eventId` 讓接收端去重。

接收端的驗證步驟：

1. 用**原始本文位元組**（不要先解析再序列化）計算 HMAC；與標頭中任一個 `v1=` 項目以常數時間比較，任一相符即可。不認識的 scheme 前綴忽略。
2. 檢查 `X-Jelee-Timestamp` 與本機時間相差不超過防重放窗口（Jelee 自己的驗證預設 5 分鐘）。
3. 以 `eventId` 去重：已處理過的回 2xx 但不再處理（回錯誤會讓 Jelee 繼續重試）。至少保留兩個窗口長度的 `eventId`。

Go（直接用 Jelee 的實作）：

```go
err := domain.VerifyWebhookSignature(
	[]domain.WebhookSecret{domain.WebhookSecret(secret)},
	domain.WebhookSignedHeaders{Timestamp: r.Header.Get("X-Jelee-Timestamp"), Signature: r.Header.Get("X-Jelee-Signature")},
	body, time.Now(), 5*time.Minute)
```

Python：

```python
import hashlib, hmac, time

def verify(secret: str, headers, body: bytes, window: int = 300) -> bool:
    ts = headers["X-Jelee-Timestamp"]
    if not ts.isdigit() or abs(time.time() - int(ts)) > window:
        return False
    want = hmac.new(secret.encode(), ts.encode() + b"." + body, hashlib.sha256).hexdigest()
    for part in headers["X-Jelee-Signature"].split(","):
        part = part.strip()
        if part.startswith("v1=") and hmac.compare_digest(part[3:], want):
            return True
    return False
```

Node.js：

```js
import { createHmac, timingSafeEqual } from "node:crypto";

export function verify(secret, headers, rawBody, windowSeconds = 300) {
  const ts = headers["x-jelee-timestamp"];
  if (!/^\d+$/.test(ts) || Math.abs(Date.now() / 1000 - Number(ts)) > windowSeconds) return false;
  const want = createHmac("sha256", secret).update(`${ts}.`).update(rawBody).digest();
  return headers["x-jelee-signature"].split(",").some((part) => {
    part = part.trim();
    if (!part.startsWith("v1=")) return false;
    const got = Buffer.from(part.slice(3), "hex");
    return got.length === want.length && timingSafeEqual(got, want);
  });
}
```

### 密鑰輪換

`POST /api/v1/webhooks/{id}/rotate-secret` 產生新密鑰並只回傳這一次。`graceSeconds`（預設 86400，最多 604800）期間新舊密鑰都簽，`X-Jelee-Signature` 帶兩個 `v1=`；消費端可以先加入新密鑰、確認後再移除舊的。`graceSeconds: 0` 立即停用舊密鑰。

## 可靠投遞（G12.3、G12.6）

```
產生端交易 ──INSERT──▶ webhook_outbox ──展開──▶ webhook_deliveries（每個訂閱端點一列）
                                                │ claim（租約 + lease_token）
                                                ▼
                                   簽章 → outbound POST → 分類結果
                                                │ RecordAttempt（同一交易：更新狀態 + 寫嘗試日誌）
                                                ▼
                                pending（下次時間）／delivered／dead
```

- **至少一次**：投遞器先領取租約再送出，送出後才記錄。若在送出後、記錄前當機（或被停止），租約到期後同一筆投遞會以**同一個 `eventId`** 再送一次。記錄時以 `lease_token` 防護：過期租約的記錄會被拒，不會覆蓋新的領取。
- 多個實例可同時運行：展開以 `FOR UPDATE SKIP LOCKED` 鎖定事件、`UNIQUE(outbox_id, webhook_id)` 保證每個端點一列；領取以 `SKIP LOCKED` 分配。
- **結果分類**：2xx＝送達。408、425、429、5xx、逾時、網路錯誤＝可重試。其他 4xx、3xx、被 SSRF／白名單拒絕（`blocked`）、憑證驗證失敗（`tls`）、事件或端點不再有效（`invalid`）＝立即死信，修正原因後可手動重放。
- **退避**：第 n 次失敗後等待 `min(maxDelay, baseDelay × 2^(n-1))`，乘上 `[1-jitter, 1+jitter]` 的均勻隨機因子後再以 `maxDelay` 封頂、最少 1 秒。429／503 的 `Retry-After`（秒數或 HTTP 日期）只會延長、不會縮短等待，同樣以 `maxDelay` 封頂。
- **預設策略**：`maxAttempts` 8、`baseDelaySeconds` 10、`maxDelaySeconds` 3600、`jitter` 0.2；每個端點可自訂（`maxAttempts` 1–20，延遲 1 秒–24 小時，`jitter` 0–1），逾時 1–30 秒（預設 10）。
- **死信**：用完 `maxAttempts` 或不可重試的結果進入 `dead`，保留在投遞日誌中。
- **手動重放**：`POST …/deliveries/{deliveryId}/replay` 把 dead 或 delivered 的投遞改回 pending、立即到期、嘗試次數歸零（新一輪 `round`），`eventId` 不變。pending 的投遞重放回 409。
- **停用端點**：不再接收新事件；既有的 pending 投遞保留，重新啟用後繼續。刪除端點會一併刪除它的投遞與日誌。
- **清理**：每小時一次，分批刪除建立超過保留天數、且沒有 pending 投遞的事件（投遞與嘗試日誌級聯刪除）。沒有任何端點承接的事件在展開時就刪除。
- **關閉**：投遞器在停止時不再領取新工作；進行中的嘗試有 5 秒寬限完成並記錄，超過則取消（租約到期後重送）。HTTP 排空後、關閉連線池之前等待它結束。

## 安全（G12.4、G12.5）

- **密鑰封存**：每端點獨立的簽章密鑰（`whsec_` + 32 個隨機位元組）與自訂標頭的值，以環境主鑰經 AES-256-GCM 封存後才寫入資料庫。附加資料綁定端點 ID 與用途，複製到別的列或欄位無法解開。資料庫、API 回應、稽核紀錄與日誌都不含明文；API 只在建立與輪換時回傳一次密鑰，列表只顯示標頭名稱。
- **SSRF**：端點 URL 必須是 HTTPS、不含帳密與片段；字面位址必須是公網位址；設定白名單時主機必須在名單內。每次連線都重新解析 DNS 並逐一檢查所有位址（私網、環回、link-local、CGNAT、文件保留段等一律拒絕），實際撥號只連到檢查過的位址，所以 DNS 重綁定無效。不跟隨重新導向。被拒的投遞記為 `blocked` 並立即死信。目前**不支援**私網端點（例如區網內的 Home Assistant）；若要支援，需要另行設計顯式放行清單。
- **TLS**：一律驗證憑證與主機名，沒有任何關閉驗證的開關。自簽 CA 的端點以 `JELEE_WEBHOOK_CA_FILE` 顯式加入信任；驗證失敗記為 `tls`。
- **稽核**：`webhook.created`、`webhook.updated`、`webhook.deleted`、`webhook.secret_rotated`、`webhook.delivery_replayed` 與變更同一個交易寫入稽核紀錄；端點狀態只記 URL 的主機（查詢字串可能含憑證），不記密鑰與標頭值。
- **管理權限**：所有管理路由只限管理員；一般使用者在任何查詢之前就得到 403（已登記在 access leak 路由表）。

## 管理 API

全部需要管理員，回應包在 `data` 中。

| 方法與路徑 | 說明 |
| --- | --- |
| `GET /api/v1/webhooks` | 端點清單（最多 64 個）與事件目錄 |
| `POST /api/v1/webhooks` | 建立；回傳 `{webhook, secret}`（201）。URL 不符政策回 400 `webhook_target_denied` |
| `GET /api/v1/webhooks/{id}` | 讀取單一端點，含 pending 與 dead 數 |
| `PUT /api/v1/webhooks/{id}` | 取代設定（含 `enabled`）；省略 `headers` 保留原值，`{}` 清除 |
| `DELETE /api/v1/webhooks/{id}` | 刪除端點與其投遞紀錄（204） |
| `POST /api/v1/webhooks/{id}/rotate-secret` | `{graceSeconds}`；回傳 `{webhook, secret}` |
| `POST /api/v1/webhooks/{id}/test` | 直接送出一個 `system.alert` 測試事件並回傳結果（不經 outbox、不重試、不寫投遞日誌），停用中的端點也可測 |
| `GET /api/v1/webhooks/{id}/deliveries?state=&cursor=&limit=` | 投遞日誌，新到舊；以 `pagination.nextCursor` 翻頁 |
| `GET /api/v1/webhooks/{id}/deliveries/{deliveryId}` | 單筆投遞與最近 100 次嘗試 |
| `POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/replay` | 手動重放（202） |

建立範例：

```json
{
  "name": "Ops",
  "url": "https://hooks.example.com/jelee",
  "events": ["media.added", "scan.failed"],
  "headers": { "Authorization": "Bearer consumer-token" },
  "timeoutSeconds": 10,
  "retry": { "maxAttempts": 8, "baseDelaySeconds": 10, "maxDelaySeconds": 3600, "jitter": 0.2 }
}
```

`events` 省略或空陣列表示訂閱全部事件。保留的標頭名稱（`Content-Type`、`Host`、`Cookie`、`User-Agent`、`X-Jelee-*`、逐跳標頭等）會被拒絕。

## 資料表（遷移 000068）

| 表 | 內容 |
| --- | --- |
| `webhooks` | 端點設定、封存的密鑰（含輪換中的舊密鑰與期限）、封存的標頭值、明文標頭名稱 |
| `webhook_outbox` | 事件（`event_id` 唯一）、類型、版本、發生時間、subject、data；`planned_at` 表示已展開 |
| `webhook_deliveries` | 每個（事件、端點）一列：狀態、本輪嘗試次數、`round`、下次時間、租約、最後結果 |
| `webhook_delivery_attempts` | 每次嘗試：輪次、序號、開始／結束時間、結果、狀態碼、排定的下次時間 |

降級：仍有設定的端點時 000068 的 down 拒絕執行（`55000`），需要先刪除端點；佇列中的事件與投遞日誌屬暫存資料，隨表刪除。

遷移 000112新增單列表 `session_expiry_watermark`（到期掃描的水位）與索引 `sessions_expiry_idx(expires_at,id)`；水位只決定事件，不導出備份，降級直接刪除。

## 測試

- 單元：`internal/domain/webhook_test.go`（簽章、退避、脫敏）、`internal/app/webhook_dispatcher_test.go`（假時鐘下的重試排程、死信、取消後重送、主鑰不符時等待、優雅停止）、`internal/app/webhook_admin_test.go`、`internal/platform/secretbox`、`internal/platform/config/webhooks_test.go`、`internal/platform/outbound/webhook_test.go`、`internal/adapter/events`。
- 真 PostgreSQL：`internal/adapter/postgres/webhooks_test.go`（同交易 outbox 與回滾、租約與崩潰後重送同 `eventId`、死信與重放、密鑰封存、播放／掃描／媒體事件、遷移 up/down/up）、`internal/adapter/http/webhooks_test.go`（管理 API 與稽核）。
- 第 11 階段接線：`internal/app/progress_webhook_test.go`（暫停合併、progress 節流、flush 失敗後補發）、`internal/app/webhook_producers_test.go`（告警白名單、每 code 節流、佇列上限、寫入失敗重試、到期掃描分頁）、`internal/platform/runtime/webhook_alerts_test.go`（日誌處理器只取 code／component）、`internal/adapter/postgres/webhook_wiring_test.go`（真 PostgreSQL：`TestWebhookWired*` 涵蓋 media.updated、scan.started、playback.paused／progress、session.created／ended 與到期、images.fetched、system.alert）。
- 端到端：`internal/platform/outbound/webhook_integration_test.go`，以 httptest HTTPS 消費端驗證簽章與去重，含解析到私網位址被拒的情境與輪換期間的雙簽章。
