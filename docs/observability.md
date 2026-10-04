# 日誌與追蹤串聯（G46.6）

Jelee 的追蹤以日誌為載體：每段工作（span）把 trace／span 識別碼放進 `context`，日誌處理器把它們寫進用這個 context 記錄的每一筆日誌；被採樣的 span 結束時多寫一筆 `span completed`。本版不附 trace exporter（`go.mod` 只有 OTel 指標相關套件，沒有 trace SDK／OTLP trace exporter，依規定不新增相依），所以「完整鏈路」是在日誌系統裡用 `traceId` 查出來的，不是在 Jaeger／Tempo 之類的介面上看。日後接上 OTLP 時，同一組識別碼（W3C 寬度：trace 32 位、span 16 位小寫十六進位）可以直接沿用。

實作：`internal/domain/trace.go`（識別碼與 context 傳遞）、`internal/platform/tracing`（span、採樣、背景工作關聯）、`internal/platform/logging/router.go`（寫入欄位）。

## 欄位

| 欄位 | 出現在 | 內容 |
| --- | --- | --- |
| `traceId` | 所有以帶 span 的 context 記錄的日誌 | 32 位小寫十六進位。HTTP 請求的 `traceId` 等於回應標頭 `X-Request-ID`、錯誤信封的 `traceId`、日誌的 `requestId`，以及稽核列的 `request_id` |
| `spanId` | 同上 | 16 位小寫十六進位，目前這段工作 |
| `span` | `span completed` | span 名稱，見下表 |
| `parentSpanId` | `span completed` | 上層 span；root span 沒有 |
| `durationMs` | `span completed` | span 耗時 |
| `outcome` | `span completed` | 結果（`ok`、`failed`、`cancelled`、任務狀態、Webhook 投遞狀態等），可省略 |
| `forced` | `span completed` | 這條 trace 因安全事件被強制保留 |
| `linkKind`／`linked` | 背景工作的 `span completed` | 關聯種類（`job`、`webhook_event`）；`linked=true` 表示接上了提交者的 trace，`false` 表示另起 root |

欄位名與 G46.4 一致（camelCase，與既有 `requestId`、`taskId` 同風格）；需求文字中的 trace_id／span_id 即這兩個欄位。它們都在日誌白名單裡，格式不符的值一律以 `[redacted]` 取代，與其他欄位同一套脫敏（G46.5）。記錄本身已帶 `traceId` 屬性時以該屬性為準，不重複寫入。

沒有 context 的日誌（程序啟動、背景迴圈的彙總警告等）不帶這兩個欄位；這類記錄不屬於任何單一請求或任務。

## 關鍵路徑的 span

| span | component | 起點 | 父 span |
| --- | --- | --- | --- |
| `http.request` | `http` | 每個 HTTP 請求進入邊界（`server.go` boundary） | 無（root；trace ID＝請求 ID） |
| `media.direct` | `media` | 直投（原檔、外掛字幕／音軌，含相容層的播放路徑） | `http.request` |
| `job.<kind>` | `scan`／`nfo`／`jobs` | worker 領取任務（`inventory_scan`、`nfo_write`、`catalog_import`、`catalog_sync`、`consistency_check`） | 提交請求的 span，或新 root |
| `scan.inventory` | `scan` | 盤點掃描階段 | `job.inventory_scan` |
| `nfo.write` | `nfo` | NFO 寫回 | `job.nfo_write` |
| `webhook.deliver` | `webhook` | 每次 Webhook 投遞嘗試 | 產生事件的 span，或新 root |

Webhook 分派器原本用的元件名 `webhooks` 不在 G46.2 元件清單內（輸出會被遮蔽、也無法單獨調級），本批改為 `webhook`。

## 從一個請求追到背景工作

提交端（PostgreSQL adapter）在建立任務或寫入 Webhook outbox 事件時，把當下的 span 記在程序內的關聯表（`tracing.Links`，上限 4096 筆、先進先出淘汰）。worker 領取任務或分派器投遞事件時查這張表：

- 查得到：背景工作成為提交 span 的子 span，**與請求同一個 `traceId`**，`span completed` 帶 `linked=true` 與 `parentSpanId`。重試的任務再次被領取時仍接回同一條 trace。
- 查不到（另一個副本提交、程序重啟、排程或監看觸發、或已被淘汰）：另起 root span，`linked=false`。此時用業務識別碼關聯：任務日誌一律帶 `taskId`（提交請求的回應與 `Location` 也有任務 ID），Webhook 日誌帶 `eventId`／`deliveryId`。

關聯表不寫入資料庫，因此不需要遷移；代價是跨副本與重啟後只能靠 `taskId`／`eventId` 關聯。

範例：管理員觸發掃描後，以回應的 `X-Request-ID` 查日誌：

```text
# Loki
{app="jelee"} | json | traceId="5f0c…e21a"
# 沒接上同一條 trace 時，改用任務 ID
{app="jelee"} | json | taskId="0b6d…"
```

```json
{"msg":"request completed","component":"http","requestId":"5f0c…e21a","traceId":"5f0c…e21a","spanId":"a1…","method":"POST","durationMs":12}
{"msg":"job started","component":"jobs","taskId":"0b6d…","traceId":"5f0c…e21a","spanId":"c3…"}
{"msg":"span completed","component":"scan","span":"scan.inventory","traceId":"5f0c…e21a","spanId":"d4…","parentSpanId":"c3…","durationMs":840,"outcome":"ok","forced":false}
{"msg":"inventory job completed","component":"jobs","taskId":"0b6d…","state":"succeeded","traceId":"5f0c…e21a","spanId":"c3…"}
{"msg":"span completed","component":"scan","span":"job.inventory_scan","traceId":"5f0c…e21a","spanId":"c3…","parentSpanId":"a1…","linkKind":"job","linked":true,"outcome":"succeeded"}
```

失敗的登入同樣可由 `traceId`（＝`X-Request-ID`）對到 `audit_logs.request_id` 的安全列。

## 採樣

- 設定：`logging.traceSampleRate`（JSON）或 `JELEE_TRACE_SAMPLE_RATE`，0～1，預設 `0.1`。採樣在 root span 建立時決定（head sampling），子 span 與接上的背景工作沿用同一決定。
- 採樣**只**影響 `span completed` 記錄。一般日誌（存取、任務、錯誤、稽核相關警告）永遠不被採樣丟棄，也永遠帶 `traceId`／`spanId`；未被採樣的 trace 一樣可以用 `traceId` 查到它的日誌，只是沒有 span 耗時記錄。
- **安全事件強制採樣**：以下事件把整條 trace 標為保留（`forced=true`），此後該 trace 所有 span 的 `span completed` 都會寫出，不受採樣率影響（包括 0）：
  - 寫入任何 `security` 類稽核列（`login.failed`、`login.second_factor_failed`、`login.native_denied`、開發者模式變更、2FA／應用程式密碼變更等，見 `internal/adapter/postgres/audit.go`）；
  - 原生與相容層的登入失敗、登入速率限制拒絕；
  - 客戶端控制規則屏蔽請求（`client_blocked`）。
  安全事件本身的紀錄（稽核列、`client requests blocked` 彙總日誌）原本就不經採樣。
- `span completed` 為 INFO，仍受元件級別控制（例如把 `scan` 調到 WARN 會隱藏掃描 span 記錄）；這是操作者的明示選擇，與採樣無關。
- 不信任請求帶來的 `traceparent`：客戶端不得決定用來關聯安全紀錄的識別碼。Webhook 對外請求也不送出 `traceparent`，避免把內部識別碼交給第三方。

## 效能（G46.8）

`go test -run '^$' -bench . ./internal/platform/logging/ ./internal/platform/tracing/`（AMD Ryzen 7 9850X3D，-count 5／3 取典型值）：

| 基準 | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| 存取日誌，無 span | ≈1170 | 48 | 3 |
| 存取日誌，帶 span（多兩個欄位） | ≈1350 | 96 | 4 |
| 被級別過濾的記錄，帶 span | ≈3.4 | 0 | 0 |
| HTTP root span，未採樣 | ≈273 | 436 | 6 |
| HTTP root span，採樣（含寫 `span completed`） | ≈1890 | 699 | 12 |

每筆記錄約多 180 ns 與一次配置；識別碼的十六進位字串在 span 建立時算好，記錄時不再編碼。被過濾的記錄不受影響。以預設採樣 0.1 計，每個請求的追蹤額外成本約 0.4 µs。

## 測試

- `internal/platform/logging/trace_test.go`：欄位寫入（含衍生 logger、鍵名守衛、主控台格式）、白名單、基準。
- `internal/platform/tracing/tracing_test.go`：請求 ID 即 trace ID、子 span、採樣與強制採樣、背景工作接回提交 trace、關聯表上限。
- `internal/adapter/postgres/trace_integration_test.go`（真 PostgreSQL）：`TestTraceHTTPScanJobPostgres` 以 HTTP 提交掃描、由 worker 執行，斷言存取日誌、任務日誌、`job.inventory_scan` 與 `scan.inventory` span 記錄都帶同一個 `traceId`（＝`X-Request-ID`）；`TestTraceSecurityEventsBypassSamplingPostgres` 在採樣率 0 下斷言一般請求沒有 span 記錄、登入失敗的請求有 `forced=true` 的記錄。
- `internal/app/webhook_dispatcher_test.go`：`TestWebhookDispatcherDeliverySpans`；`internal/adapter/media/trace_test.go`：`TestDirectDeliverySpan`。
