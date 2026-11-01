# 日誌與追蹤串聯（G46.6）

Jelee 的追蹤以日誌為載體：每段工作（span）把 trace／span 識別碼放進 `context`，日誌處理器把它們寫進用這個 context 記錄的每一筆日誌；被採樣的 span 結束時多寫一筆 `span completed`。預設不匯出 trace，「完整鏈路」在日誌系統裡用 `traceId` 查出來；設定 `tracing.exporter=otlp` 後，同一批 span 以 OTLP/HTTP 送到 OpenTelemetry Collector，可在 Jaeger、Tempo 之類的介面上看，識別碼與日誌完全相同（見 [OTLP 匯出](#otlp-匯出)）。

實作：`internal/domain/trace.go`（識別碼與 context 傳遞）、`internal/platform/tracing`（span、採樣、背景工作關聯）、`internal/platform/logging/router.go`（寫入欄位）、`internal/platform/tracing/otlpexport`（OTLP 匯出）。

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
| `job.<kind>` | `scan`／`nfo`／`images`／`jobs` | worker 領取任務（`inventory_scan`、`nfo_write`、`catalog_import`、`catalog_sync`、`consistency_check`、`nfo_apply`、`nfo_export`、`image_refresh`、`metadata_refresh`） | 提交請求的 span，或新 root |
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

## OTLP 匯出

預設關閉。開啟後，每個寫出 `span completed` 記錄的 span 同時交給匯出器，在背景以 OTLP/HTTP（protobuf、gzip）批次送到 Collector。

### 設定

| 環境變數 | 設定檔 | 預設 | 說明 |
| --- | --- | --- | --- |
| `JELEE_TRACE_EXPORTER` | `tracing.exporter` | `none` | `none` 或 `otlp` |
| `JELEE_TRACE_OTLP_ENDPOINT` | `tracing.otlp.endpoint` | — | Collector 的完整網址；沒有路徑時用 `/v1/traces`。不得帶帳密、查詢字串或片段 |
| `JELEE_TRACE_OTLP_HEADERS` | `tracing.otlp.headers` | — | 每次匯出附帶的標頭（認證、租戶）。環境變數寫成 `名稱=值,名稱=值`，值以百分比編碼（同 `OTEL_EXPORTER_OTLP_HEADERS`）；最多 16 個、合計 8 KiB；不可覆寫 `Content-Type`、`Content-Encoding`、`Host` 等由匯出器設定的標頭 |
| `JELEE_TRACE_OTLP_INSECURE` | `tracing.otlp.insecure` | `false` | 允許明文 `http://`，**只限環回 IP 字面值**（`127.0.0.0/8`、`[::1]`，不接受 `localhost`）；其餘一律 `https://` |
| `JELEE_TRACE_OTLP_TIMEOUT_SECONDS` | `tracing.otlp.timeoutSeconds` | 10 | 1–60，每批匯出（含重試）的期限 |
| `JELEE_TRACE_OTLP_BATCH_SIZE` | `tracing.otlp.batchSize` | 512 | 1–4096，每批最多 span 數 |
| `JELEE_TRACE_OTLP_BATCH_INTERVAL_MS` | `tracing.otlp.batchIntervalMs` | 5000 | 100–60000，未滿一批時最長等待 |
| `JELEE_TRACE_OTLP_QUEUE_SIZE` | `tracing.otlp.queueSize` | 2048 | 1–65536，等待匯出的 span 上限 |

啟動校驗拒絕半套設定：`exporter=otlp` 沒有 `endpoint`、`exporter` 不是 `otlp` 卻設了任何 `tracing.otlp.*`、遠端明文 http、`https` 加 `insecure`、標頭不合法或數值越界，伺服器都不啟動，錯誤訊息不含端點與標頭內容。標頭多半是權杖，設定檔請比照資料庫網址以 0600 保存。

```json
{
  "logging": { "traceSampleRate": 0.1 },
  "tracing": {
    "exporter": "otlp",
    "otlp": {
      "endpoint": "https://otel-collector.internal:4318/v1/traces",
      "headers": { "Authorization": "Bearer <collector-token>" },
      "timeoutSeconds": 10
    }
  }
}
```

Collector 範例（`otelcol-contrib`，接收 OTLP/HTTP 並驗 Bearer 權杖，轉送到 Tempo；Jaeger 1.35 起也直接收 OTLP）：

```yaml
receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
        tls:
          cert_file: /etc/otelcol/tls/server.crt
          key_file: /etc/otelcol/tls/server.key
        auth:
          authenticator: bearertokenauth
extensions:
  bearertokenauth:
    token: ${env:JELEE_COLLECTOR_TOKEN}
processors:
  batch: {}
exporters:
  otlphttp/tempo:
    endpoint: http://tempo:4318
service:
  extensions: [bearertokenauth]
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlphttp/tempo]
```

同機測試可以不設 TLS：Collector 只聽 `127.0.0.1:4318`，Jelee 設 `JELEE_TRACE_OTLP_ENDPOINT=http://127.0.0.1:4318`、`JELEE_TRACE_OTLP_INSECURE=true`。

### 匯出內容

- **識別碼沿用**：trace ID、span ID、父 span 就是日誌的 `traceId`、`spanId`、`parentSpanId`。HTTP 請求的 trace ID 等於 `X-Request-ID`，所以在追蹤介面用回應標頭或錯誤信封的 `traceId` 直接查得到；反過來，追蹤介面上的 trace ID 可以直接拿去查日誌與稽核列。
- **背景工作**：接上提交者的任務與 Webhook 投遞是提交 span 的子 span（同一條 trace），另帶一個指向提交 span 的 OTel Link（屬性 `jelee.linkKind`）；接不上的另起 root、沒有 Link，`jelee.linked=false`，仍以 `taskId`／`eventId` 在日誌關聯。
- **屬性白名單**：只有 `jelee.component`、`jelee.outcome`、`jelee.forced`、`jelee.linkKind`、`jelee.linked` 五個，取自同一筆 `span completed` 記錄，值再經日誌脫敏規則（`internal/platform/logging/redact.go` 的白名單）檢查，不合格的值寫成 `[redacted]`；span 名稱不符合日誌 `span` 欄位規則時改為 `redacted`。路徑、IP、使用者、裝置、查詢參數等一律不匯出。Resource 只有 `service.name=jelee` 與 `service.version`，不讀 `OTEL_RESOURCE_ATTRIBUTES`、主機名稱或程序資訊。
- **種類與狀態**：`http.request` 為 server、`webhook.deliver` 為 client、`job.*` 為 consumer，其餘 internal。結果為 `panic`、`failed`、`lease_lost`、`dead` 或 Webhook 嘗試留在 `pending`（將重試）時狀態為 Error，其餘不設。

### 與日誌採樣一致

匯出不另做採樣：只有寫出 `span completed` 記錄的 span 才會匯出，條件就是上面的 head sampling（`logging.traceSampleRate`）加安全事件強制採樣，兩邊的 span 集合相同（`TestExporterReceivesExactlyTheLoggedSpans`、`TestExportFollowsLogSampling`）。唯一差別是元件級別：把某元件調到 WARN 會隱藏它的 `span completed` 日誌，但不影響匯出，因為那是日誌輸出的選擇，不是採樣。強制採樣只對事件之後才結束的 span 生效，已經結束而未被採樣的 span 兩邊都沒有。

### 丟棄、失敗與關機

- 結束 span 的請求或任務只做一次不阻塞的佇列寫入；佇列滿就丟棄並計數，請求不等 Collector。
- 每批匯出在期限內重試（0.5 秒起、最長間隔 2 秒，總長不超過 `timeoutSeconds`）；仍失敗就丟棄該批並計數。服務日誌每分鐘最多一筆 `trace export failed; spans dropped`，不寫錯誤內容（可能含端點與 Collector 回應）。
- 計數在 `/metrics`（只在開啟匯出時出現）：`jelee_tracing_spans_exported_total`、`jelee_tracing_spans_dropped_total{reason="queue_full|export_failed|shutdown"}`、`jelee_tracing_export_failures_total`；告警 [`JeleeTraceExportFailing`](runbook.md#jeleetraceexportfailing)。
- 關機時在 HTTP、worker、Webhook 投遞都停止後才清空佇列，期限 5 秒；逾時就中止進行中的匯出，剩下的計入 `reason="shutdown"`，不影響正常結束的結束碼。

### 連線與安全

- 只連到設定的端點：不使用 `HTTP_PROXY` 等環境代理、不跟隨重新導向（避免把標頭裡的權杖帶到沒設定過的位址）、TLS 1.2 以上並使用系統根憑證，不提供略過憑證驗證的選項。自簽憑證的 Collector 請把 CA 加入系統信任庫，或在 Linux 以 `SSL_CERT_FILE`／`SSL_CERT_DIR` 指定。
- `OTEL_EXPORTER_OTLP_*` 環境變數不能改變端點、標頭、憑證或逾時，也不需要設定（格式錯誤的值會被上游函式庫原樣印到 stderr，請不要把權杖放在這些變數裡）；整個行程也不註冊全域 OTel trace provider。
- 不信任入站 `traceparent` 的規則不變；Webhook 對外請求仍不送 `traceparent`。

### 相依

新增 `go.opentelemetry.io/otel/exporters/otlp/otlptrace` 與 `…/otlptracehttp` v1.47.0（與既有 OTel v1.47.0 對齊，Apache-2.0）；`go.opentelemetry.io/otel/sdk` v1.47.0 原本就在 go.mod，這裡用它的 `sdk/trace`。OTLP 的 protobuf 定義套件連帶把 gRPC、grpc-gateway 等模組連結進伺服器執行檔（約多 3.3 MB，`jelee` 50.4 → 53.7 MB），授權見 [LICENSE-COMPLIANCE](LICENSE-COMPLIANCE.md#otlp-追蹤匯出的依賴)。設定檢查放在不依賴 OTLP 套件的 `internal/platform/tracing`，`jelee-cli` 與 `jelee-migrate` 不連結這些模組。其中 `golang.org/x/net/trace` 在初始化時會在 `http.DefaultServeMux` 登記 `/debug/requests`；Jelee 從不提供 `DefaultServeMux`，這兩個路徑不會對外。

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

OTLP 匯出（2026-10-31，同一台機器，`-count 3`／`5`）：

| 基準 | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| HTTP root span，採樣，未開匯出 | ≈2250 | 603 | 10 |
| HTTP root span，採樣，開匯出（`BenchmarkRequestSpanExport`） | ≈2700 | ≈3800 | 29 |
| `BenchmarkBoundaryRequest`（未開匯出，門禁 72 allocs／6100 B） | ≈7900 | 5885 | 69 |
| `BenchmarkBoundaryRequestTraceExport`（採樣 1、開匯出、本機 Collector；受背景匯出排程影響，波動大，不列入百分比門禁） | ≈11000–18000 | ≈10200 | 95 |

未開匯出時 `BenchmarkBoundaryRequest` 與改動前相同（69 allocs、5885 B）；span 結束時只多一次 nil 檢查，屬性切片只在要匯出時才複製，未匯出時仍留在堆疊上。開匯出的兩個基準把背景匯出（轉換、protobuf 編碼、gzip、HTTP）的配置也算進去，請求路徑上實際多的是一次屬性複製與一次佇列寫入。

## 測試

- `internal/platform/logging/trace_test.go`：欄位寫入（含衍生 logger、鍵名守衛、主控台格式）、白名單、基準。
- `internal/platform/tracing/tracing_test.go`：請求 ID 即 trace ID、子 span、採樣與強制採樣、背景工作接回提交 trace、關聯表上限。
- `internal/adapter/postgres/trace_integration_test.go`（真 PostgreSQL）：`TestTraceHTTPScanJobPostgres` 以 HTTP 提交掃描、由 worker 執行，斷言存取日誌、任務日誌、`job.inventory_scan` 與 `scan.inventory` span 記錄都帶同一個 `traceId`（＝`X-Request-ID`）；`TestTraceSecurityEventsBypassSamplingPostgres` 在採樣率 0 下斷言一般請求沒有 span 記錄、登入失敗的請求有 `forced=true` 的記錄。
- `internal/app/webhook_dispatcher_test.go`：`TestWebhookDispatcherDeliverySpans`；`internal/adapter/media/trace_test.go`：`TestDirectDeliverySpan`。
- `internal/platform/tracing/otlpexport/export_test.go`（本機 httptest 假 OTLP 接收端，解 protobuf）：`TestExportKeepsLogIdentifiersAndLinks`（trace ID＝請求 ID、span／父 span／Link 與日誌相同、標頭與預設路徑）、`TestExportAttributesFollowLogWhitelist`、`TestExportFollowsLogSampling`、`TestEnvironmentCannotRedirectExport`、`TestRedirectIsNotFollowed`、`TestExportFailureIsCountedAndWarnedWithoutDetails`、`TestFullQueueDropsWithoutBlocking`、`TestShutdownDeadlineDropsQueuedSpans`、`TestNewRefusesUnsafeSettings`；`internal/platform/tracing/tracing_test.go`：`TestExporterReceivesExactlyTheLoggedSpans`；`internal/platform/config/tracing_test.go`；`internal/platform/telemetry/tracing_test.go`；`internal/platform/runtime/trace_export_test.go`（關機清空）。
