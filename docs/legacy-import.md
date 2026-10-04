# 舊庫遷移工具（G04.6）

`jelee-cli legacy-import` 把 Jellyfin（或改寫前的 C# 版 Jelee）的 SQLite 資料庫匯入 Jelee 的 PostgreSQL：帳號與權限、媒體庫與根路徑、條目對應、播放進度與已播放狀態。工具支援預檢、斷點續傳、冪等重跑、失敗回滾當前批次、行數核對、來源校驗和與 JSON 遷移報告，並寫入稽核。

選擇放在 `jelee-cli` 而不是 `jelee-migrate`：`jelee-migrate` 只管 Jelee 自己的 schema（up／down／status），而舊庫匯入是會建立帳號、需要路徑對照與媒體庫掃描配合的維運作業，和 `metadata import` 同屬一類。SQLite 驅動（純 Go 的 `modernc.org/sqlite`，決定記錄在[擁有者核對清單](owner-verification-queue.md) E13）只連結進 `jelee-cli`；伺服器執行檔 `jelee` 不含任何 SQLite 程式碼（G04.8，由 `internal/architecture` 的測試把關）。

## 相容範圍

| 來源 | 支援 | 說明 |
| --- | --- | --- |
| Jellyfin 10.11 起的 `jellyfin.db`（EF Core schema，含遷移 `20241020103111_LibraryDbMigration`） | 是 | 依 `upstream-csharp-final` 標籤的 EF Core 模型撰寫，測到遷移 `20260815063607_RemoveOrphanedUserPermissionsAndPreferences` |
| 改寫前的 C# Jelee | 是 | 與上列同一份 schema（同一棵 EF Core 模型） |
| 比測試版本更新的 `jellyfin.db` | 有條件 | 必要的表與欄位都在就照常讀取，報告 `source.tested=false`；欄位改名或刪除會在開檔時拒絕 |
| Jellyfin 10.10 以前（條目在 `library.db` 的 `TypedBaseItems`） | 否 | 拒絕並回報 `library_db_before_10_11`。先用 Jellyfin 10.11 開一次讓它完成升級，再匯入升級後的 `jellyfin.db` |

讀取的上游表：`__EFMigrationsHistory`、`Users`、`Permissions`、`Preferences`、`BaseItems`（含 CollectionFolder 的 `Data` JSON）、`UserData`。`MediaStreamInfos`、`Chapters`、`BaseItemImageInfos`、`ItemValues`、`Peoples` 等不匯入：Jelee 掃描與探測會從媒體檔重建串流、章節與圖片，中繼資料以 NFO／TMDb 流程為準（見[已知限制](#已知限制)）。

## 使用步驟

1. **停止 Jellyfin**，把資料目錄裡的 `jellyfin.db`（以及同目錄的 `jellyfin.db-wal`，如果有）放到 Jelee 主機上。工具從不以 SQLite 開啟來源檔：它先把來源與 `-wal`／`-journal` 一起複製到工作目錄、同時計算 SHA-256，只查詢複本；複製期間來源的大小或修改時間變了就拒絕（`legacy_source_changed`）。
2. Jelee 已完成[初始引導](setup-wizard.md)或尚未開始都可以；**引導進行到一半時拒絕**（`setup_in_progress`）。全新部署匯入後，若已有帳號或媒體庫，引導視為完成。
3. 決定[路徑對照](#路徑對照)並預檢：

   ```sh
   jelee-cli legacy-import --source /srv/old/jellyfin.db --path-map '/media=/srv/media' --preflight
   ```

   預檢讀完所有帳號與媒體庫並比對 Jelee，不寫入任何東西；有衝突時結束碼 1 並列出（見[身分比對與衝突](#身分比對與衝突)）。
4. 匯入：

   ```sh
   jelee-cli legacy-import --source /srv/old/jellyfin.db --path-map '/media=/srv/media' [--merge-users] --report legacy-report.json
   ```

   第一次匯入時 Jelee 通常還沒掃描過媒體庫，所以條目都會是「待對應」（`not_scanned`），它們的播放進度也一樣待對應；帳號、權限與媒體庫此時已經建好。
5. 在 Jelee 掃描匯入的媒體庫（目錄同步或匯入工作，見[目錄同步](catalog-sync.md)）。
6. **用同一個命令再跑一次**。這是一次新的、冪等的執行：已匯入的帳號與媒體庫全部是 `unchanged`，條目改以路徑對應到掃描產生的 Jelee 條目，待對應的播放進度在這次匯入。只要還有待對應條目，可以重複 5–6。
7. 為匯入的帳號設定密碼（見[密碼處理](#密碼處理)）：`jelee-cli account set-password --name NAME --password-stdin`。

### 參數

| 參數 | 說明 |
| --- | --- |
| `--source FILE` | 上游 `jellyfin.db`（必填） |
| `--path-map FROM=TO` | 路徑前綴對照，可重複；`TO` 必須是本機絕對路徑 |
| `--preflight` | 只預檢，不寫入 |
| `--merge-users` | 名稱（不分大小寫）與 Jelee 現有帳號相同的上游帳號對應到該帳號，而不是衝突 |
| `--skip-conflicts` | 有衝突也繼續：衝突的帳號、媒體庫、根路徑略過並計入報告 |
| `--restart` | 有另一個來源或另一組選項的未完成執行時，放棄它（標為 `abandoned`）並重新開始 |
| `--batch-size N` | 每個交易處理的列數（1–10000，預設 1000） |
| `--max-batches N` | 處理 N 批後暫停（結束碼 3）；再跑同一命令從檢查點繼續 |
| `--work-dir DIR` | 放來源複本的目錄（預設系統暫存目錄；容器裡的 `/tmp` 只有 64 MiB，請掛載可寫目錄） |
| `--report FILE` | 另把 JSON 報告寫到新檔（0600；已存在就拒絕） |
| `--json` | 標準輸出印 JSON 報告而不是摘要 |
| `--timeout D` | 整體時限（預設 12h）；逾時或 Ctrl-C 只回滾當前批次 |

容器內執行（映像沒有 shell）：

```sh
dc="docker compose -f deploy/docker-compose.yml"
$dc run --rm --no-deps -v /srv/old:/legacy:ro -v /srv/legacy-work:/work --entrypoint /jelee-cli jelee \
  legacy-import --source /legacy/jellyfin.db --work-dir /work --path-map '/media=/srv/media' --preflight
```

## 路徑對照

新部署的掛載點通常和舊伺服器不同，所以每條上游路徑（媒體庫位置與每個檔案）都先經過對照：

- 規則 `FROM=TO`：第一個 `=` 分開兩邊，結尾的分隔符號會去掉。可以給多條，**最長的 `FROM` 優先**；只在路徑元件邊界比對（`/media` 不會比到 `/media2`）。
- Windows 來源路徑（`D:\Media\…`、`\\nas\share\…`）比對時不分大小寫，剩下的部分把 `\` 換成本機分隔符號：`--path-map 'D:\Media=/srv/media'` 會把 `D:\Media\電影\a.mkv` 變成 `/srv/media/電影/a.mkv`。
- 沒有規則比到的路徑原樣使用，但必須已經是本機形式的絕對路徑；Linux 上的 Windows 路徑因此一律需要規則。結果含 `.`／`..` 元件、以 `%` 開頭（Jellyfin 的虛擬路徑，例如 `%AppDataPath%`）或無法成為絕對路徑時視為「無法對照」：媒體庫位置記為衝突 `root_path_unmapped`，條目記為略過 `path_unmapped`。
- 對照後的媒體庫位置直接成為 Jelee 的媒體庫根路徑；條目路徑落在哪個根（最深者優先）之下，就以「根＋相對路徑」比對 Jelee 掃描產生的媒體檔。Unicode 檔名逐位元組比對，不做正規化（見已知限制）。
- 路徑對照是執行的一部分：未完成的執行換了規則會被拒絕（`legacy_import_run_mismatch`），要用 `--restart`。

## 密碼處理

**不匯入上游密碼雜湊；所有匯入的帳號都沒有密碼，必須由管理員設定後才能登入。**

理由：上游存的是 PBKDF2-SHA512（`$PBKDF2-SHA512$iterations=…`），Jelee 的帳號表只接受 Argon2id PHC 字串（資料庫 CHECK 約束，見[密碼安全](password-security.md)）。「以相容方式驗證一次再升級」需要在登入路徑長期保留第二套較弱的 KDF 與額外欄位，且 Jellyfin 常見沒有密碼的帳號，匯入後會變成任何人都能登入。因此選擇最保守的作法：帳號建立時 `password_hash` 為 NULL（登入一律失敗，且不洩漏帳號是否存在），報告的 `passwordResetRequired` 列出需要設定密碼的帳號數，以 `jelee-cli account set-password --name NAME --password-stdin` 或管理介面設定。用 `--merge-users` 對應到既有帳號時，既有帳號的密碼與所有設定都不變。

## 匯入內容對照

| 上游 | Jelee | 說明 |
| --- | --- | --- |
| `Users.Username` | `users.name` | 保留原名（含 Unicode）；1–128 位元組、前後無空白、無控制字元，不符合為衝突 `name_invalid` |
| 帳號 GUID | `users.id` | 該 UUID 在 Jelee 未被占用時沿用，否則產生新的；對應關係記在帳本 |
| `Permissions` IsAdministrator／IsDisabled／IsHidden | `is_admin`／`disabled`／`hidden` | |
| `Permissions` EnableAllFolders ＋ `Preferences` EnabledFolders | `library_acl` | 授權只給本次匯入或對應的媒體庫；指向未匯入媒體庫（例如音樂）的授權略過 `library_not_imported` |
| `Permissions` EnableContentDownloading | — | 略過 `download_removed`（G06 已移除下載權限） |
| `Permissions` EnableLiveTvAccess／EnableLiveTvManagement | — | 略過 `removed_domain`（G05） |
| 其他權限（遙控、轉碼、遠端存取、播放等） | — | 略過 `not_applicable`：Jelee 沒有對應設定 |
| `MaxParentalRatingScore` | `parental_rating_max`（夾在 0–21） | 同時設 `content_filtered`；`Preferences` BlockUnratedItems 非空時 `block_unrated=true` |
| `Preferences` BlockedTags | `user_blocked_tags`（小寫、去空白） | |
| `MaxActiveSessions`（>0） | `max_streams`（上限 128） | |
| `RemoteClientBitrateLimit`（bps，>0） | `max_kbps`（÷1000） | |
| CollectionFolder（`movies`、`tvshows`、`homevideos`、`mixed`、未指定） | `libraries` ＋ `library_roots` | 名稱沿用；位置來自 `Data` JSON 的 `PhysicalLocationsList`，經路徑對照 |
| CollectionFolder（`music`、`musicvideos`、`books`、`audiobooks`、`photos`、`livetv`） | — | 略過 `removed_domain`（G02.2、G05.4） |
| CollectionFolder（`boxsets`、`playlists`、`folders`、`trailers`） | — | 略過 `unsupported_collection_type` |
| `BaseItems` Movie／Episode／Video | 以檔案路徑對應既有條目 | 不建立條目：條目由 Jelee 掃描產生 |
| `BaseItems` Series／Season | 以資料夾對應已登記的劇集／季資料夾（`item_directory_sources`） | 對不到為 `folder_unmatched` |
| `UserData` | `user_item_data` | 續播位置、已播放、播放次數、最後播放時間；見下 |

條目分類（略過原因）：音樂、有聲書、書、相片、音樂錄影帶、直播頻道與節目、錄影為 `removed_domain`；預告片、合集、播放清單為 `unsupported_kind`；資料夾、檢視、人物、類型、工作室、年份與上游的佔位條目為 `structural`；缺集等虛擬條目 `virtual_item`；附加影片（花絮等）`extra`；沒有路徑 `no_path`。

播放資料：

- 同一使用者與條目在上游可能有多列（`CustomDataKey`），合併為最後播放的那列（同時間取已播放、再取位置較大者），多出的列計入 `merged_duplicate`；多個上游版本對應到同一個 Jelee 條目時也只留最新的，其餘計入 `merged_version`。
- **較新者勝**：Jelee 已有的列只有在上游的最後播放時間更新時才被覆寫，所以把舊資料庫匯入使用中的伺服器不會倒退任何人的進度；上游沒有最後播放時間的列視為最舊（`updated_at` 記為 1970-01-01）。
- 沒有任何播放狀態的列略過 `empty`；只有收藏的列略過 `favorite_unsupported`（Jelee 沒有收藏，報告 `favoritesDropped` 另計有進度又被收藏的列）；上游的佔位條目（已脫離原條目的資料）`item_detached`；條目已不存在 `item_missing`；使用者已在上游刪除 `user_missing`；使用者沒有匯入（衝突、略過）`user_not_imported`；條目待對應 `item_pending`（之後的執行會再試）；條目對應不到也不在待對應 `item_not_mapped`。

## 身分比對與衝突

| 類別 | 比對 | 衝突 |
| --- | --- | --- |
| 帳號 | 帳本 → 名稱（不分大小寫） | `name_taken`（同名帳號存在，未用 `--merge-users`）、`name_taken_deleted`（同名帳號已在 Jelee 刪除；不會合併或復活）、`name_duplicate`（兩個上游帳號在 Jelee 規則下同名，或同名帳號已由另一個上游帳號匯入）、`name_invalid` |
| 媒體庫 | 帳本 → 名稱 | 同名媒體庫已持有**全部**對照後的位置時視為同一個（`matched`）；否則 `name_taken`。位置已屬於另一個媒體庫為 `root_path_taken`；所有位置都無法對照為 `root_path_unmapped`；兩個上游媒體庫同名為 `name_duplicate` |
| 根路徑 | 路徑 | `root_path_unmapped`（該位置無法對照）、`root_path_taken`；同一媒體庫重複的位置略過 `duplicate_location` |
| 條目 | 路徑 | 不會衝突；對不到為待對應或略過 |

預檢與正式執行用同一套判斷。沒有 `--skip-conflicts` 時任何衝突都在寫入第一列前拒絕（`legacy_import_conflict`），連執行紀錄都不建立。

`--merge-users` 對應到的既有帳號**完全不改**：名稱、密碼、管理員與停用旗標、分級、封鎖標籤、媒體庫授權都保持 Jelee 的設定，只匯入播放資料。同理，媒體庫授權與封鎖標籤只套用在**本次執行建立**的帳號上；之前的執行建立的帳號在重跑時不會被重新授權，管理員事後收回的權限不會被加回來。

## 斷點續傳、冪等與回滾

- **批次**：五個階段依序執行：`users` → `libraries` → `access`（授權與封鎖標籤）→ `items` → `user_data`。每批（`--batch-size` 列）在**一個交易**內完成：套用、更新帳本、更新該階段的檢查點（游標、批數、計數、摘要鏈）。交易內也取得與掃描、目錄匯入相同的工作鎖，批次期間目錄不會變動。
- **失敗回滾**：任何錯誤（資料庫錯誤、逾時、Ctrl-C、程序被終止）都只回滾正在進行的那一批；之前已提交的批次與檢查點保留。執行紀錄記下 `last_error` 並維持 `running`。
- **斷點續傳**：同一個來源檔（SHA-256 相同）與同一組選項再跑一次，就從各階段的檢查點繼續（報告 `resumed=true`）；批次大小可以不同。來源或選項不同時拒絕（`legacy_import_run_mismatch`），除非加 `--restart`。同一時間只允許一個匯入（PostgreSQL 工作階段鎖；第二個得到 `legacy_import_busy`）。
- **冪等**：帳本 `legacy_import_map` 記錄每個上游帳號、媒體庫、條目與播放資料變成了哪一列 Jelee 資料。完成後再跑同一個來源是一次新的執行，但不會再建立任何東西：帳號與媒體庫是 `unchanged`，播放資料相同所以不更新。Jelee 裡事後被刪除的帳號不會被復活（`target_deleted`）。
- **待對應**：對不到的條目記在 `legacy_import_pending`（原因與對照後路徑），之後的執行重試；對到後移除。

## 行數核對與校驗和

- 來源檔（以及非空的 `-wal`／`-journal`）在複製時計算 **SHA-256**，寫入報告 `source.sha256`／`source.walSha256`、執行紀錄與稽核（`target_ref = sha256:<摘要>`）。
- 每個階段把讀到的每一批資料以正規化 JSON 計算 SHA-256，並串成摘要鏈 `digest = SHA-256(前一個 digest ‖ SHA-256(本批))`；續傳沿用同一條鏈，報告 `categories.<類別>.digest`。同一來源、同一批次大小的兩次完整執行得到相同的摘要。
- **行數核對**：每個類別的 `source`（來源表的列數）必須等於 `inserted + updated + matched + unchanged + 略過 + 衝突 + 待對應` 的總和（`verification.rowsBalanced`）。`library_access` 是由帳號推導出的授權，`derived=true`，不參與這條等式。孤兒權限／偏好列（帳號已不存在）計為 `user_missing`。
- **反向核對**：依帳本回查 Jelee，計算本次執行觸及的帳號、媒體庫、條目與播放資料在目標中實際存在的列數（`verification.targetRows`），必須等於報告的套用數（`verification.targetMatch`）。任一不符時執行仍標為完成並保存報告，但以 `legacy_import_unbalanced` 結束（結束碼 1），表示需要調查。

## 報告

摘要印在標準輸出（`--json` 改印 JSON）；`--report FILE` 另寫一份 JSON。完成的報告也存在 `legacy_import_runs.report`。報告不含路徑或任何祕密：衝突與待對應只列上游 ID 與原因（各最多 100 筆，總數在各類別計數）。

稽核：開始時寫 `legacy.import_started`（目標為執行 ID，記錄來源摘要、遷移版本與選項），完成時寫 `legacy.imported`（目標 `sha256:<來源摘要>`，記錄各類別計數與核對結果）；兩者都是安全類別。

JSON 結構（本區塊由 `internal/domain` 的測試逐欄對照，未記載的欄位或值會讓測試失敗）：

<!-- legacy-import-report-schema -->
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "jelee.legacy-import-report/v1",
  "type": "object",
  "required": ["schema", "state", "resumed", "targetSchemaVersion", "source", "pathMap", "mergeUsers", "skipConflicts", "batchSize", "batches", "phases", "categories",
    "passwordResetRequired", "favoritesDropped", "conflicts", "pending", "startedAt"],
  "properties": {
    "schema": {"const": "jelee.legacy-import-report/v1"},
    "runId": {"type": "string"},
    "state": {"enum": ["preflight", "refused", "paused", "failed", "completed"]},
    "resumed": {"type": "boolean"},
    "targetSchemaVersion": {"type": "integer"},
    "source": {
      "type": "object",
      "required": ["file", "sha256", "size", "latestMigration", "migrations", "tested", "tables"],
      "properties": {
        "file": {"type": "string"},
        "sha256": {"type": "string"},
        "size": {"type": "integer"},
        "walSha256": {"type": "string"},
        "latestMigration": {"type": "string"},
        "migrations": {"type": "integer"},
        "tested": {"type": "boolean"},
        "tables": {"type": "object", "properties": {}, "additionalProperties": {"type": "integer"}}
      }
    },
    "pathMap": {"type": "array", "items": {"type": "object", "required": ["from", "to"], "properties": {"from": {"type": "string"}, "to": {"type": "string"}}}},
    "mergeUsers": {"type": "boolean"},
    "skipConflicts": {"type": "boolean"},
    "batchSize": {"type": "integer"},
    "batches": {"type": "integer"},
    "phases": {
      "type": "object",
      "properties": {
        "users": {"$ref": "#/$defs/phase"},
        "libraries": {"$ref": "#/$defs/phase"},
        "access": {"$ref": "#/$defs/phase"},
        "items": {"$ref": "#/$defs/phase"},
        "user_data": {"$ref": "#/$defs/phase"}
      }
    },
    "categories": {
      "type": "object",
      "properties": {
        "users": {"$ref": "#/$defs/category"},
        "permissions": {"$ref": "#/$defs/category"},
        "preferences": {"$ref": "#/$defs/category"},
        "libraries": {"$ref": "#/$defs/category"},
        "library_roots": {"$ref": "#/$defs/category"},
        "library_access": {"$ref": "#/$defs/category"},
        "items": {"$ref": "#/$defs/category"},
        "user_data": {"$ref": "#/$defs/category"}
      }
    },
    "passwordResetRequired": {"type": "integer"},
    "favoritesDropped": {"type": "integer"},
    "conflicts": {"type": "array", "items": {"$ref": "#/$defs/sample"}},
    "pending": {"type": "array", "items": {"$ref": "#/$defs/sample"}},
    "verification": {
      "type": "object",
      "required": ["rowsBalanced", "targetRows", "targetMatch"],
      "properties": {
        "rowsBalanced": {"type": "boolean"},
        "targetRows": {"type": "object", "properties": {}, "additionalProperties": {"type": "integer"}},
        "targetMatch": {"type": "boolean"}
      }
    },
    "error": {"enum": ["batch_failed", "cancelled", "legacy_source_unsupported"]},
    "startedAt": {"type": "string"},
    "finishedAt": {"type": "string"}
  },
  "$defs": {
    "phase": {"enum": ["pending", "partial", "done"]},
    "category": {
      "type": "object",
      "required": ["source", "inserted", "updated", "matched", "unchanged"],
      "properties": {
        "source": {"type": "integer"},
        "derived": {"type": "boolean"},
        "inserted": {"type": "integer"},
        "updated": {"type": "integer"},
        "matched": {"type": "integer"},
        "unchanged": {"type": "integer"},
        "skipped": {"$ref": "#/$defs/reasons"},
        "conflicts": {"$ref": "#/$defs/reasons"},
        "pending": {"$ref": "#/$defs/reasons"},
        "digest": {"type": "string"}
      }
    },
    "reasons": {"type": "object", "properties": {}, "additionalProperties": {"type": "integer"}},
    "sample": {
      "type": "object",
      "required": ["category", "sourceId", "reason"],
      "properties": {"category": {"type": "string"}, "sourceId": {"type": "string"}, "reason": {"type": "string"}}
    }
  }
}
```

- `state`：`preflight`（只預檢）、`refused`（衝突或執行不符，未寫入）、`paused`（`--max-batches` 停下，可續傳）、`failed`（某批失敗並已回滾，可續傳）、`completed`。
- `inserted`：新建立的列；`updated`：覆寫的播放資料；`matched`：對應到 Jelee 既有的列（合併的帳號、同名同位置的媒體庫、路徑對到的條目）；`unchanged`：先前的執行已匯入，這次沒有變更。
- `skipped`／`conflicts`／`pending` 的鍵是本文各節列出的原因代碼；值為列數。

## 錯誤碼與結束碼

結束碼：0 完成或預檢無衝突；1 失敗、拒絕或核對不符；2 用法錯誤；3 因 `--max-batches` 暫停。錯誤以固定代碼印在標準錯誤，不含連線字串或路徑：

| 代碼 | 意義 |
| --- | --- |
| `legacy_source_unsupported:<原因>` | 來源無法使用：`source_unreadable`、`not_sqlite`、`library_db_before_10_11`、`schema_before_10_11`、`missing_table_<表>`、`missing_column_<表>_<欄>`、`non_text_identifiers`、`read_failed` |
| `legacy_source_changed` | 複製期間來源檔改變（Jellyfin 還在執行？） |
| `legacy_import_conflict` | 預檢發現衝突（報告列出），或引導進行中；沒有寫入 |
| `legacy_import_run_mismatch` | 有另一個來源或另一組選項的未完成執行；續傳它或加 `--restart` |
| `legacy_import_busy` | 另一個匯入正在執行 |
| `legacy_import_schema` | 目標資料庫不在此執行檔的 schema 版本（先 `jelee-migrate up`） |
| `legacy_import_unbalanced` | 行數或反向核對不符（報告的 `verification`） |
| `legacy_report_exists`、`legacy_output_failed` | `--report` 目標已存在、寫入失敗 |
| `legacy_database_unavailable`、`legacy_configuration_invalid` | 無法連線或設定錯誤 |
| `legacy_import_failed`、`legacy_cancelled` | 其他失敗、逾時或中斷（已提交的批次保留，可續傳） |

## 資料表（遷移 000075）

- `legacy_import_runs`：每次執行一列（來源 SHA-256 與大小、選項摘要、狀態 `running`／`completed`／`abandoned`、來源列數、完成時的報告、最後錯誤）。同時最多一個 `running`。
- `legacy_import_checkpoints`：每次執行每個階段一列（游標、批數、是否完成、累計計數、摘要鏈）。
- `legacy_import_map`：帳本（種類、上游鍵 → Jelee ID；播放資料另記使用者；`created`、首次與最後一次執行）。目標不是外鍵：Jelee 裡刪除的列會被察覺、不會被復活。
- `legacy_import_pending`：待對應條目（原因、對照後路徑）。

降版（`down`）在有任何執行紀錄或帳本時拒絕：舊版執行檔看不懂它們，而丟掉帳本會讓之後的匯入失去冪等性。確定不再需要時先刪除 `legacy_import_runs`、`legacy_import_map`、`legacy_import_pending` 的內容。

## 已知限制

- **密碼**不匯入，見上。
- **收藏、評分、喜歡**：Jelee 沒有對應功能，不匯入（`favorite_unsupported`、`favoritesDropped`）。
- **合集、播放清單、預告片**：Jelee 尚無對應資料表，略過；`DisplayPreferences`、首頁區塊、語言與字幕偏好、裝置、API 金鑰、活動紀錄不匯入。
- **條目只對應不建立**：中繼資料、圖片、章節、媒體串流由 Jelee 掃描、探測與 NFO／TMDb 流程重建；匯入只帶過使用者資料。劇集與季只在 Jelee 登記了資料夾來源時對應得到（掃描以檔名歸組的劇集沒有資料夾來源，它們的劇集層級資料略過 `folder_unmatched`；集數本身照常對應）。
- **Unicode 正規化**：路徑逐位元組比對。從 macOS（NFD 檔名）搬到 Linux（NFC）時同一個檔名可能對不到；這種條目會留在待對應。
- **Windows 目標**：根路徑比對區分大小寫，與 Jelee 登記的根路徑字串完全一致才算同一個根。
- **時間**：上游 `DateTime` 視為 UTC；PostgreSQL 精度到微秒，100 奈秒的尾數捨去。
- 權限中 Jelee 沒有對應設定的項目（例如 EnableMediaPlayback=false、遠端存取、轉碼權限）不轉換；匯入後請在管理介面確認需要限制的帳號。匯入的帳號 `allowNative` 一律為預設的關閉。
- 上游已刪除的帳號留下的播放資料與權限列不匯入（`user_missing`）。

## 需要以真實資料庫驗證的項目

測試資料庫全部在測試中依上游 EF Core schema 子集**生成**，不含任何真實使用者資料。下列項目需要用真實的 Jellyfin 資料庫確認（[擁有者核對清單](owner-verification-queue.md) D22）：

1. Jellyfin 10.11.x 實際寫出的 GUID 文字格式（大小寫）、`DateTime` 格式與 `PhysicalLocationsList`／`CollectionType` 在 `Data` JSON 的形式。
2. 從 10.10 升級到 10.11 的資料庫（`library.db` 移入 `jellyfin.db` 之後）的 `UserData`：`CustomDataKey` 多列的分布、`RetentionDate` 與佔位條目的實際使用情形。
3. Windows 主機上的 Jellyfin（磁碟機代號與 UNC 路徑）搬到 Linux 的路徑對照，以及 NAS 掛載點改變的情境。
4. 大型實際資料庫（10 萬條目以上、多使用者、WAL 模式）的匯入時間與記憶體。
5. 多版本電影、分段檔（stacking）、外掛字幕與附加影片在真實資料中的分類結果，與 Jelee 掃描結果的對應率。
6. Jellyfin 之外的衍生版本（Emby 不支援；改寫前 C# Jelee 的實際資料庫）。
