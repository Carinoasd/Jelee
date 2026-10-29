# 任務排程（G13.1）

管理員可以為媒體庫或整個伺服器保存「到期就排入一個任務」的定義。遷移 000107（暫定編號，106 保留給另一分支；SchemaVersion 107）新增 `job_schedules`，並把既有的媒體庫掃描排程搬進來。

## 可排程的種類

| 種類 | 範圍 | `params` | 說明 |
| --- | --- | --- | --- |
| `inventory_scan` | 媒體庫 | `probe`、`nfo`、`ignore`（`mode`／`caseMode`） | 背景優先的庫存掃描，選項與 `PUT /api/v1/libraries/{id}/schedule` 相同 |
| `metadata_refresh` | 媒體庫 | — | 元資料刷新（[元資料刷新任務](metadata-refresh-jobs.md)），沒有設定提供者時跳過 |
| `image_refresh` | 媒體庫 | — | 圖片刷新（[圖片刷新任務](image-refresh-jobs.md)），未設定遠端抓取或圖片存放區時跳過 |
| `consistency_check` | 媒體庫 | — | 資料一致性檢查（[一致性檢查](consistency.md)） |
| `subtitle_cache_cleanup` | 伺服器 | `maxIdleDays`（0＝30 天） | 字幕擷取快取清理（[維護任務](maintenance-jobs.md)），沒有可用快取時跳過 |
| `backup_reminder` | 伺服器 | `thresholdDays`（0＝`jobs.backupReminderDays`，未設為 7） | 備份提醒（[維護任務](maintenance-jobs.md)） |

其他種類一律拒絕（`400 schedule_kind_not_allowed`），資料表的 CHECK 也只接受上表六種：

- `external_metadata_removal` 是破壞性操作，每次都要管理員帶 `confirm=true`，不能無人確認地重複執行。
- `nfo_write`、`nfo_export`、`nfo_apply`、`catalog_import`、`catalog_sync`（接受遺失）需要事先準備的請求或人工判斷。

每個種類在同一個範圍（某個媒體庫，或伺服器）只能有一個定義，所以總數不會超過「種類數 × 媒體庫數」。

## 時間、時區與窗口

`timing` 與既有掃描排程相同，另外多了 `once`：

- `interval`：`intervalSeconds` 60～31536000；下一次＝上一次派發時間＋間隔。
- `cron`：五欄 cron，依 `timezone`（明確的 IANA 時區，不接受 `Local`）解讀；夏令時間由時區資料處理。
- `once`：在 `runAt` 執行一次，執行後自動停用（`enabled=false`）。建立時 `runAt` 已過就直接存成停用。

`window`（`start`／`end`，`HH:MM`，同樣以 `timezone` 解讀，`end` 不含；`end` 早於 `start` 表示跨午夜）限制任務只在窗口內開始：

- 下一次時間落在窗口外時，往後移到窗口的下一次開啟。
- 到期時已在窗口外（例如重試延後跑出窗口），這一次不排任務，`nextDue` 移到下一次開啟，不會遺失也不會補跑多次。
- 夏令時間：不存在的分鐘（春季跳過的一小時）永遠不符合，重複的分鐘（秋季）兩次都依牆上時間判斷；窗口完全落在跳過區間的那一天直接順延到隔天。

錯過的多次到期（伺服器停機）合併成一次，跟原本的掃描排程相同。

## 系統執行身分

排程到期與「同步後自動排入」的任務以**系統身分**執行，不依賴任何管理員或工作階段：

- `domain.SystemActor`：沒有使用者、沒有工作階段，只帶來源 `system:schedule:<排程 ID>` 或 `system:catalog_sync:<同步任務 ID>`。HTTP 不會產生它，也沒有 JSON 形式。
- 任務列的 `actor_id` 為 NULL、新欄位 `jobs.system_source` 記錄來源；CHECK 只允許上表六種任務帶系統來源，所以即使有程式錯誤，系統身分也無法跑破壞性或需準備的任務。
- 稽核：系統身分寫的稽核列沒有 `actorId`，`requestId` 是來源字串（例如 `system:schedule:…`），排程排入的送件稽核另帶 `scheduleId`。
- 權限語意：儲存層把系統身分當作管理員，但應用層只在任務 worker 自己的呼叫（元資料刷新讀取條目、偏好與套用提供者值）接受它；其他所有入口（管理排程、送件、編輯）一律拒絕。也就是說系統身分只能做「排程允許的種類」本身會做的事。
- 手動送件不變：`POST /api/v1/schedules/{id}/run`、`POST /api/v1/libraries/{id}/schedule/run` 與各種類自己的送件端點都以呼叫的管理員身分送件（`manual` 優先），任務屬於那位管理員。

因為改成系統身分，舊版「排程擁有者被停用或刪除就停用排程（`owner_unavailable`）」不再發生；這個值只會出現在遷移前已被停用的掃描排程上。目錄監看（watch）仍以 `scan_schedules.owner_id` 的管理員身分排入掃描，沒有改變。

## API

全部只限管理員（權限矩陣 P22）。

| 方法與路徑 | 說明 |
| --- | --- |
| `GET /api/v1/schedules[?libraryId=]` | 列出全部或某媒體庫的排程（`{"schedules":[…]}`，伺服器層級排在前面） |
| `POST /api/v1/schedules` | 建立；`expectedRevision` 必須是 0；同種類同範圍已存在時 `409`；回 `201` 與 `Location` |
| `GET /api/v1/schedules/{id}` | 讀取 |
| `PUT /api/v1/schedules/{id}` | 以 `expectedRevision` 取代（過期時 `409`）；`kind` 與 `libraryId` 不能改（`400`） |
| `DELETE /api/v1/schedules/{id}?expectedRevision=N` | 刪除；`204` |
| `POST /api/v1/schedules/{id}/run` | 以呼叫者身分立即送件（需 `Idempotency-Key`），不推進排程；`consistency_check` 沒有管理員送件端點，回 `400 schedule_kind_not_allowed` |

```http
POST /api/v1/schedules
Authorization: Bearer SESSION_TOKEN
Content-Type: application/json

{"kind":"metadata_refresh","libraryId":"LIBRARY_ID","expectedRevision":0,"enabled":true,
 "timing":{"mode":"cron","intervalSeconds":0,"cron":"0 4 * * *","timezone":"Asia/Taipei"},
 "window":{"start":"02:00","end":"06:00"},"params":{}}
```

回應的 `lastError`：`admission_unavailable`（媒體庫忙碌或佇列已滿，30 秒後重試同一次到期）、`kind_unavailable`（這台伺服器不能跑此種類，這一次跳過、排到下一次）、`owner_unavailable`（只見於遷移前停用的掃描排程）。`lastJobId`／`lastRunAt` 是最近一次排入的任務與時間。建立、取代、刪除分別寫稽核 `schedule.created`、`schedule.updated`、`schedule.deleted`。

## 與既有掃描排程的關係

`GET`／`PUT /api/v1/libraries/{id}/schedule` 保持原樣，現在是兩張表的檢視：

- `scan_schedules` 只保留掃描選項（`probe`、`nfo`、`ignore`）、目錄監看開關與擁有者，以及這個端點的 `revision`。
- 時間、時區、`nextDue`、重試與最後任務搬到該媒體庫的 `inventory_scan` 列。
- 從 `/api/v1/schedules` 建立或修改 `inventory_scan` 時，也會把掃描選項同步寫回 `scan_schedules`（選項改變時 `revision` 加一），讓監看用相同選項。從這裡設定的窗口在 `PUT …/schedule` 之後仍保留並生效；舊端點不顯示窗口。
- 刪除 `inventory_scan` 排程不會刪掉監看設定；之後讀舊端點會看到停用、每日、UTC 的預設時間。

遷移時每一列掃描排程原封不動地搬過去（時間、時區、到期、重試、最後任務與錯誤）。降級時只要所有排程都能用舊格式表達（只有 `inventory_scan`、沒有 `once` 與窗口、選項與 `scan_schedules` 一致）而且沒有任何系統任務或 `trigger=schedule` 的刷新紀錄，就把時間寫回 `scan_schedules`；否則拒絕降級。

中繼資料備份的 `scan_schedule` 紀錄維持原本的形狀（由兩張表組合）；帶窗口或 `once` 的掃描排程與其他種類的排程不會匯出，還原後需要重新設定（與自動刷新開關相同的理由：還原後的實例不應在管理員確認前自動執行）。

## 備份提醒的環境變數

`JELEE_JOB_BACKUP_REMINDER_DAYS`（`jobs.backupReminderDays`）繼續有效：大於 0 時每 24 小時排一次備份提醒，門檻為該天數，也是 `backup_reminder` 排程 `thresholdDays=0` 時的門檻。等價的通用排程是：

```json
{"kind":"backup_reminder","expectedRevision":0,"enabled":true,
 "timing":{"mode":"interval","intervalSeconds":86400,"cron":"","timezone":"UTC"},"params":{"thresholdDays":7}}
```

兩者同時存在不會重複：環境變數的派發在 24 小時內已有任何 `backup_reminder` 任務時不再排入。建議改用通用排程（可設時區、窗口、停用與立即執行），再把環境變數設回 0。`JELEE_JOB_CONSISTENCY_INTERVAL_HOURS` 也同樣保留。

## 測試

- domain 表格測試：窗口在不同時區、跨午夜、紐約春季跳過與秋季重複的分鐘；`NextScheduleRun` 的 interval／once／窗口；輸入驗證與系統身分格式（`go test ./internal/domain/ -run 'ScheduleWindow|NextScheduleRun|ValidateJobSchedule|SystemActor'`）。
- 真 PG（`JELEE_TEST_DATABASE_URL`）：`go test -p 1 -parallel 2 -run 'TestJobSchedule|TestSchedule' ./internal/adapter/postgres/`——舊掃描排程遷移後不變（含降級往返與系統身分派發）、新種類到期觸發、窗口外不觸發並移到開啟時間、忙碌重試、不可用種類跳過、`once` 自動停用、破壞性種類被拒（應用層與 CHECK）、沒有任何工作階段時排程的元資料刷新成功而手動任務失敗、無法表達的排程拒絕降級、HTTP 端點。

長時間與 Windows 驗證見[待驗證清單](owner-verification-queue.md) B22、C40、D40。
