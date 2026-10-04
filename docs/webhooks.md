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

| 類型 | subject | data | 狀態 |
| --- | --- | --- | --- |
| `media.added` | item | `libraryId`、`kind`（Movie／Episode／Season／Series）、`title` | 已接：目錄同步（catalog_sync）自動建立條目時，與條目同一個 savepoint 內寫入 |
| `media.updated` | item | — | 未接 |
| `media.deleted` | item | `libraryId` | 已接：接受缺失檔案後的目錄同步刪除掃描建立的條目時 |
| `scan.started` | library | — | 未接 |
| `scan.completed` | library | `jobId`、`missing`、`reviewRequired` | 已接：庫存掃描在 `FinishJob` 成功結束、以及 ignore 發布路徑結束時 |
| `scan.failed` | library | `jobId`、`errorCode` | 已接：`FinishJob` 以失敗結束時。租約逾時後因嘗試次數用盡而失敗的路徑**未接** |
| `playback.started` | session | `userId`、`itemId`、`sourceId`（有時）、`positionTicks`、`paused` | 已接：開始回報**新建**播放會話時（接回既有會話不發），與 INSERT 同一個語句 |
| `playback.paused` | session | — | 未接 |
| `playback.progress` | session | — | 未接（進度上報經記憶體緩衝批次寫入，逐筆事件會造成寫放大） |
| `playback.stopped` | session | `userId`、`itemId`、`state`（stopped／failed／timed_out）、`positionTicks`、`completed` | 已接：批次 flush 實際把會話從 active 結束時，與 flush 同一個語句；重複的停止不會再發 |
| `user.login` | user | `clientKind`（web／native） | 已接：密碼登入成功（自有 API 與相容層共用） |
| `user.login_failed` | user | `failedLogins` | 已接：已存在的帳號密碼錯誤；不存在的帳號名沒有 user 可指，不發 |
| `user.locked` | user | `failedLogins`、`lockedUntil` | 已接：造成鎖定的那次失敗；鎖定期間的嘗試會在計數前被拒，不重複發 |
| `session.created` / `session.ended` | session | — | 未接 |
| `nfo.written` | item | `jobId`、`libraryId` | 已接：NFO 寫回工作成功結束時，每個條目一個事件。由恢復租約補完的工作**未接** |
| `images.fetched` | item | — | 未接 |
| `system.alert` | system | — | 只用於管理 API 的測試送出（`data` 為 `{"test": true, "message": …}`） |

「已接」的事件都在產生變更的同一個 PostgreSQL 交易（或同一個語句）內寫入 `webhook_outbox`：變更提交才有事件，變更回滾事件也一起消失。另外，只有在有**啟用中的端點訂閱該類型**時才會寫入，避免沒人訂閱的事件佔空間。

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

## 測試

- 單元：`internal/domain/webhook_test.go`（簽章、退避、脫敏）、`internal/app/webhook_dispatcher_test.go`（假時鐘下的重試排程、死信、取消後重送、主鑰不符時等待、優雅停止）、`internal/app/webhook_admin_test.go`、`internal/platform/secretbox`、`internal/platform/config/webhooks_test.go`、`internal/platform/outbound/webhook_test.go`、`internal/adapter/events`。
- 真 PostgreSQL：`internal/adapter/postgres/webhooks_test.go`（同交易 outbox 與回滾、租約與崩潰後重送同 `eventId`、死信與重放、密鑰封存、播放／掃描／媒體事件、遷移 up/down/up）、`internal/adapter/http/webhooks_test.go`（管理 API 與稽核）。
- 端到端：`internal/platform/outbound/webhook_integration_test.go`，以 httptest HTTPS 消費端驗證簽章與去重，含解析到私網位址被拒的情境與輪換期間的雙簽章。
