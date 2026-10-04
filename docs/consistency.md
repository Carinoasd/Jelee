# 資料一致性檢查（G50.3）

一致性檢查比對目錄、最近一次被接受的盤點基準（`library_inventory_baseline`）、衍生資料表與實際檔案，回報彼此不一致的地方。**預設只報告、不修改**；`--fix` 只做少數安全且可逆的修正，每筆都寫入修正日誌並寫稽核，刪除類一律不自動做。

- 手動：`jelee-cli consistency check`（在服務主機上執行，直接連資料庫）。
- 排程：`consistency_check` 任務走任務系統（[任務 API](jobs-api.md)），可在任務清單觀察、以 `jobs cancel` 取消，受 worker 租約、心跳、執行時間上限與工作時窗約束。
- 觀測：每次執行的報告存在 `consistency_runs`；各檢查的最新發現數輸出為 `jelee_consistency_findings{check}`，搭配[告警](runbook.md#jeleeconsistencyfindings)。

## 指令

```
jelee-cli consistency check   [--library ID|名稱] [--json] [--fix] [--stat-budget N] [--watch-sample N] [--timeout 30m]
jelee-cli consistency report  [--library ID|名稱] [--json]
jelee-cli consistency revert  --run RUN_ID [--json]
jelee-cli consistency enqueue --library ID|名稱 [--priority manual|background]
```

- `check` 不帶 `--library` 時檢查全部媒體庫，另加兩項資料庫層級檢查。輸出為表格；`--json` 輸出下方定義的報告文件。結束碼：0 沒有未修正的發現、3 有發現（報告照常輸出）、1 失敗、2 用法錯誤、130 被中斷。
- `report` 印出最近一次涵蓋該媒體庫（或任何媒體庫）的已完成報告，不重新檢查。
- `revert` 依修正日誌把該次 `--fix` 的修正由新到舊還原；只還原仍保持修正後數值的列，其他（之後又被改過、引用的來源已刪）跳過並計數。重複執行不會重放已還原的項目。
- `enqueue` 以背景或手動優先權排一個 `consistency_check` 任務；同一媒體庫已有進行中的任務時回 `consistency_library_busy`。
- 錯誤只輸出固定代碼（`consistency_database_unavailable`、`consistency_not_found`、`consistency_check_failed` 等），不輸出連線字串或路徑。所有輸出先經過與 `doctor` 相同的敏感值掃描，命中就不輸出（`output_unsafe`）。

排程以 `JELEE_JOB_CONSISTENCY_INTERVAL_HOURS`（JSON `jobs.consistencyIntervalHours`，0–8760，預設 0＝關閉）啟用：排程迴圈在沒有到期的掃描排程時，每分鐘最多查一次，替「該間隔內沒有被檢查過、也沒有進行中工作」的一個媒體庫排一個背景 `consistency_check`。任務報告只檢查、不修正。`JELEE_JOB_CONSISTENCY_STAT_BUDGET`（預設 1000，上限 100000）與 `JELEE_JOB_CONSISTENCY_WATCH_SAMPLE`（預設 500，上限 10000）同時適用於排程與指令列，指令列旗標可覆寫。

## 檢查項目

需求原文 G50.3 列出八類；下表是對應的十項檢查。依基準比對的項目在媒體庫沒有基準（從未完成掃描）時標為 `skipped`／`no_baseline`；檢查期間基準被換掉（掃描發布新基準）時，這些項目作廢為 `incomplete`／`baseline_changed`，不留下可能誤報的發現。

| 檢查 | 需求類別 | 比對內容 | 發現代碼 | 可 `--fix` |
| --- | --- | --- | --- | --- |
| `orphan_item` | 孤兒條目（文件不存在） | 每個 `media_sources` 是否在基準中；不在的以 stat 確認 | `source_missing`、`source_missing_unconfirmed` | 否（刪除類） |
| `orphan_file` | 孤兒文件（未入库） | 基準中的影片是否有對應來源；等待審核的待定檔（`catalog_scan_pending`）只計入 `info.pendingReview` | `video_not_cataloged` | 否 |
| `version_count` | 版本计数错误 | 影片類條目（Movie／Episode／HomeVideo）至少要有一個版本（媒體來源），Series／Season 不應有；`user_item_data.last_source_id` 與播放工作階段的 `source_id` 只能指向自己條目的版本 | `video_item_without_source`、`container_item_with_source`、`user_data_foreign_source`、`session_foreign_source` | 後兩者：清除錯誤參照 |
| `watch_stats_drift` | 播放统计漂移 | 抽樣 `watch_stats_daily`，用已計入工作階段的標記重算 sessions／views／first plays／rewatches／completions／completion milli；再抽樣已計入的工作階段確認日彙總列存在 | `daily_counter_drift`、`daily_row_missing` | 前者：以重算值覆寫計數 |
| `image_file` | 图片记录与实际文件不一致 | `item_images` 的本地參照（local／nfo／embedded）是否在基準中、大小與修改時間是否與讀取時相同 | `image_source_missing`、`image_source_missing_unconfirmed`、`image_source_changed` | 否 |
| `image_variant_index` | 缓存与实际不符 | 抽樣 `image_variants` 索引列，確認圖片存放區目前世代目錄中有該變體檔 | `variant_file_missing` | 否（刪除類） |
| `nfo_state` | NFO 与条目不一致 | `item_nfo_observations` 的狀態與基準中的 NFO：觀察為有效／無效但檔案已不在、觀察為缺少但檔案已出現、大小或修改時間已變、觀察指向別的條目的來源 | `nfo_missing_but_observed`、`nfo_present_but_observed_missing`、`nfo_changed_since_observed`、`observation_foreign_source` | 否 |
| `sidecar_file` | 補充：外掛軌記錄與實際檔案 | `media_sidecar_tracks` 的外掛軌檔是否在基準中、大小與修改時間是否相同 | `sidecar_missing`、`sidecar_missing_unconfirmed`、`sidecar_changed` | 否 |
| `probe_cache_stale` | 缓存与实际不符 | 已就緒的探測快取列是否仍對應基準中同大小、同修改時間的檔案 | `probe_cache_stale`、`probe_cache_orphan` | 否 |
| `constraint_state` | 外键／唯一约束异常 | 目前 schema 中未驗證（`NOT VALID`）的約束，以及無效或未就緒的索引（例如並行建立失敗留下的唯一索引） | `constraint_not_validated`、`index_invalid` | 否 |

外鍵與唯一約束在有效時由 PostgreSQL 強制，不可能被違反，所以 `constraint_state` 檢查的是讓違反得以存在的情況：未驗證的約束與無效索引。遷移 000060 刻意以 `NOT VALID` 加入的兩個稽核約束（只約束新列）不列為發現。

### 做不到或刻意不做的部分

- **影片有效觀看時間**（`effective_ms`）不重算：它是樣本的時間聯集，樣本依保留期刪除後無法重現。只重算可由工作階段標記精確重現的計數。
- **保留期**：日彙總列比工作階段活得久。只抽樣「該日所有工作階段都還在」的日期（播放歷史保留期往前兩天的餘裕），反向抽樣只看統計保留期內的日期，避免把正常清理當成漂移。
- **NFO 快取（`nfo_cache`）** 以路徑、大小、修改時間與內容雜湊為鍵且依 TTL 淘汰，舊列只是不再命中，不列入檢查；`nfo_state` 檢查的是會影響條目的觀察結果。
- **NFO 檔名大小寫**：讀取器忽略大小寫比對檔名。檢查先查常見寫法，再在同一目錄做最多 4096 筆的不分大小寫比對；目錄更大時該筆計入 `info.unverified`，不判定為缺少。觀察為「缺少」的條目只用常見寫法判定是否出現了 NFO。
- **目錄型來源**（Series／Season 的 `item_directory_sources`）：基準只記錄檔案，不記錄目錄，因此不檢查目錄本身是否存在；其 NFO 由 `nfo_state` 檢查。
- **舊版基準列**（遷移 000007 前、沒有屬性的列）沒有種類，`orphan_file` 不會把它們當影片。
- **刪除類修正**（孤兒條目、孤兒檔案、孤兒快取或索引列）一律不自動做：目錄同步有自己的缺失確認流程（[目錄同步](catalog-sync.md)），快取與索引會被各自的淘汰與重建處理。

## 發現與處置

| 代碼 | 處置 |
| --- | --- |
| `source_missing` | 檔案確實不在。重新掃描；目錄同步會把缺失的來源標記並在確認後移除，手動匯入的來源需要人工決定。 |
| `source_missing_unconfirmed` | 基準沒有該檔，但 stat 預算用完或 stat 失敗（無法開啟根目錄、權限、穿越被拒）。提高 `--stat-budget` 或修好掛載後重跑。 |
| `video_not_cataloged` | 掃描到但未入庫的影片。執行目錄同步（`POST /api/v1/libraries/{id}/catalog-sync` 或開啟自動同步），或以 `import-video` 手動登記。 |
| `video_item_without_source`、`container_item_with_source` | 條目結構異常，多半來自手動匯入或舊版資料。人工合併、移動或刪除條目。 |
| `user_data_foreign_source`、`session_foreign_source` | 續播資料或播放紀錄指向別的條目的版本。`--fix` 清除該參照（只影響「最後使用版本」的記憶，不影響續播點與統計）。 |
| `daily_counter_drift` | 日彙總計數與工作階段標記不符。`--fix` 在彙總鎖下重算並覆寫計數。 |
| `daily_row_missing` | 已計入的工作階段沒有日彙總列。人工檢查是否有人直接刪除了統計列；不自動補列。 |
| `image_source_*` | 圖片檔已刪除或變更。重新掃描讓圖片進度更新，或在條目圖片管理中替換。 |
| `variant_file_missing` | 變體索引指向不存在的檔案；下次請求會重新產生，持續出現時檢查存放區磁碟與 `jelee_images_store_*` 指標。 |
| `nfo_*` | NFO 狀態過時。以 `jelee-cli jobs scan --id 媒體庫 --nfo …` 重新驗證 NFO。`observation_foreign_source` 表示觀察指向別的條目，重新驗證後會被取代。 |
| `sidecar_*` | 外掛軌檔已刪除或變更。重新掃描並同步。 |
| `probe_cache_stale`、`probe_cache_orphan` | 探測快取對應的檔案已變或不在；重新探測（`jobs probe-rebuild-item`／`probe-rebuild-library`），舊列會依 TTL 與配額淘汰。 |
| `constraint_not_validated`、`index_invalid` | 由資料庫管理員處理：修正違反的資料後 `ALTER TABLE … VALIDATE CONSTRAINT`，或 `REINDEX`／重建索引。 |

## 修正、日誌與還原

`--fix` 只套用上表標為可修正的三種代碼，每頁一個交易：

1. 以檢查讀到的值為前提再次比對（列已被改過就跳過，計入 `skipped`）；計數漂移在交易內持有統計彙總的 advisory lock 重新計算，不採用檢查當時的數字。
2. 每筆修正把目標、修正前與修正後的確切值寫入 `consistency_fix_journal`。
3. 每個交易寫一筆稽核 `consistency.fixed`（目標為執行 ID，內容為各代碼套用數與跳過數）。還原寫 `consistency.reverted`。排程任務的提交與結束寫 `consistency.submitted`／`consistency.finished`。

有修正日誌的執行不會被保留上限清掉，隨時可以 `consistency revert`。保留上限是 50 次已完成的執行（`ConsistencyRunRetention`）；超過一天仍未完成的執行（程序中斷）在下次開始時標為 `failed`。

## 有界執行

- 每項檢查以主鍵或既有索引分頁，每頁 500 筆、每次資料庫呼叫最多 15 秒；記憶體只保留目前頁與每項檢查最多 50 筆樣本（全部檢查合計最多 2000 筆，媒體庫多時每項自動減少）。發現數本身完整計數。
- 檔案系統只做 stat（不開檔、不讀內容），只針對基準顯示「不在」的候選，總數受 `--stat-budget` 限制；每次 stat 經由固定在媒體庫根目錄的 `os.Root`，不會沿符號連結離開根目錄。孤兒檔案完全依基準判斷，不遍歷媒體庫。
- 統計重算與變體索引為抽樣：從隨機鍵開始讀取，每個抽樣最多掃描 20 倍抽樣數的列。
- 排程任務受 worker 的租約、心跳、取消與 `JELEE_JOB_MAX_RUNTIME_SECONDS` 約束；取消時已完成的檢查照樣寫入報告（狀態 `cancelled`）。

實測（`TestConsistencyScale100000Postgres`，`JELEE_CONSISTENCY_SCALE=1`，本機 WSL2、PostgreSQL 測試庫）：10 萬條目、10 萬來源、10 萬基準列，其中 1000 個檔案不在基準中，一次完整檢查 14.0 秒，峰值 heap 3 MiB（較執行前增加 2.6 MiB），1000 次 stat 全部用於確認，得到 1000 筆 `source_missing`。

## 報告文件

`consistency check --json`、`consistency report --json` 與 `consistency_runs.report` 是同一份文件。`schema` 為 `jelee-consistency-report/v1`；新增欄位不換版本，刪除或改名欄位必須換版本。路徑只出現在樣本的 `path`，依日誌路徑模式處理：`logging.pathMode=redact`（預設）一律為 `[redacted]`，`relative` 時為相對於 `logging.pathRoots` 的路徑；絕對路徑、連線字串與其他祕密永遠不會出現。被程序中斷而沒有完整報告的執行，報告只有 `schema`、`runId`、`state`（以及排程任務的 `jobId`）。

下方 JSON Schema 由 `internal/domain/consistency_test.go` 對照實際輸出檢查。

<!-- consistency-report-schema -->
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "jelee-consistency-report/v1",
  "type": "object",
  "required": ["schema", "runId", "origin", "mode", "state", "startedAt", "finishedAt", "libraries", "global", "totals", "limits"],
  "additionalProperties": false,
  "properties": {
    "schema": {"const": "jelee-consistency-report/v1"},
    "runId": {"type": "string", "format": "uuid"},
    "origin": {"enum": ["cli", "job"]},
    "jobId": {"type": "string", "format": "uuid"},
    "libraryId": {"type": "string", "format": "uuid"},
    "mode": {"enum": ["report", "fix"]},
    "state": {"enum": ["completed", "partial", "cancelled", "failed"]},
    "startedAt": {"type": "string", "format": "date-time"},
    "finishedAt": {"type": "string", "format": "date-time"},
    "libraries": {"type": "array", "items": {"$ref": "#/$defs/library"}},
    "global": {"type": "array", "items": {"$ref": "#/$defs/check"}},
    "totals": {
      "type": "object",
      "required": ["findings", "fixable", "fixed"],
      "additionalProperties": false,
      "properties": {"findings": {"type": "integer"}, "fixable": {"type": "integer"}, "fixed": {"type": "integer"}}
    },
    "limits": {
      "type": "object",
      "required": ["statBudget", "statsUsed", "watchSample", "variantSample", "samplesPerCheck", "pageSize"],
      "additionalProperties": false,
      "properties": {
        "statBudget": {"type": "integer"}, "statsUsed": {"type": "integer"}, "watchSample": {"type": "integer"},
        "variantSample": {"type": "integer"}, "samplesPerCheck": {"type": "integer"}, "pageSize": {"type": "integer"}
      }
    }
  },
  "$defs": {
    "library": {
      "type": "object",
      "required": ["libraryId", "baseline", "checks"],
      "additionalProperties": false,
      "properties": {
        "libraryId": {"type": "string", "format": "uuid"},
        "baseline": {
          "type": "object",
          "required": ["available", "revision", "entries"],
          "additionalProperties": false,
          "properties": {"available": {"type": "boolean"}, "revision": {"type": "integer"}, "entries": {"type": "integer"}}
        },
        "checks": {"type": "array", "items": {"$ref": "#/$defs/check"}}
      }
    },
    "check": {
      "type": "object",
      "required": ["check", "status", "examined", "findings", "fixable", "fixed", "samples", "samplesTruncated"],
      "additionalProperties": false,
      "properties": {
        "check": {"enum": ["orphan_item", "orphan_file", "version_count", "watch_stats_drift", "image_file", "image_variant_index", "nfo_state", "sidecar_file", "probe_cache_stale", "constraint_state"]},
        "status": {"enum": ["ok", "findings", "skipped", "incomplete"]},
        "reason": {"enum": ["no_baseline", "baseline_changed", "image_store_unavailable", "cancelled", "verification_budget_exhausted", "check_failed"]},
        "examined": {"type": "integer"},
        "findings": {"type": "integer"},
        "fixable": {"type": "integer"},
        "fixed": {"type": "integer"},
        "info": {
          "type": "object",
          "additionalProperties": false,
          "properties": {"baselineStale": {"type": "integer"}, "unconfirmed": {"type": "integer"}, "pendingReview": {"type": "integer"}, "unverified": {"type": "integer"}, "sampled": {"type": "integer"}}
        },
        "samples": {"type": "array", "items": {"$ref": "#/$defs/finding"}},
        "samplesTruncated": {"type": "boolean"}
      }
    },
    "finding": {
      "type": "object",
      "required": ["code", "fixable"],
      "additionalProperties": false,
      "properties": {
        "code": {"type": "string"},
        "libraryId": {"type": "string", "format": "uuid"},
        "itemId": {"type": "string", "format": "uuid"},
        "sourceId": {"type": "string", "format": "uuid"},
        "rootId": {"type": "string", "format": "uuid"},
        "path": {"type": "string", "description": "root-relative per logging.pathMode, or [redacted]; never absolute"},
        "userId": {"type": "string", "format": "uuid"},
        "day": {"type": "string", "format": "date"},
        "object": {"type": "string", "description": "constraint or index (table.name), session ID, image record ID or image variant (source/key)"},
        "fixable": {"type": "boolean"},
        "expected": {"type": "object", "additionalProperties": {"type": "integer"}},
        "actual": {"type": "object", "additionalProperties": {"type": "integer"}}
      }
    }
  }
}
```

## 資料表（遷移 000074）

- `consistency_runs`：每次執行一列（來源 `cli`／`job`、模式、狀態、發現與修正數、報告 jsonb，上限 4 MiB；超過時捨棄樣本保留計數）。`job_id` 對任務唯一，任務因租約遺失重跑時沿用同一列。
- `consistency_run_checks`：每次執行 × 媒體庫 × 檢查的狀態與計數，供指標讀取各媒體庫最新結果。
- `consistency_fix_journal`：修正日誌，有日誌的執行不被清理。
- `jobs` 新增種類 `consistency_check` 與錯誤碼 `consistency_check_failed`；`job_metric_totals`／`job_metric_buckets` 增加對應的兩組（`jelee_jobs_shared_*` 的 `kind` 標籤因此多一個值）。
- `webhook_deliveries` 增加 `state='dead'` 的部分索引，供死信指標計數。

降級遷移在仍有一致性任務、執行紀錄或修正日誌時拒絕（`retained consistency check state prevents downgrade`）。
