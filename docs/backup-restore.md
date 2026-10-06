# 備份與還原（G36.4）

本文定義 Jelee 的備份策略、還原步驟與演練方式，對應 G36.4，以及 G36.3（資料生命週期）裡和備份有關的部分。資產的位置與能否重建以[儲存佈局](storage-layout.md)為準，資料表之間的關係見[領域模型](domain-model.md)；部署方式見[部署](deployment.md)。

Jelee 有兩層備份，用途不同，**兩者都要做**：

| 層 | 工具 | 內容 | 主要用途 |
| --- | --- | --- | --- |
| 完整備份 | `pg_dump`／`pg_restore` | 整個 PostgreSQL：帳號、工作階段、目錄、快取、任務、稽核…… | 災難復原；回到某個時間點的完整狀態 |
| 元資料備份 | `jelee-cli metadata export`／`import` | 媒體無法重建的使用者資料（見[元資料匯出與匯入](#元資料匯出與匯入)） | 版本或主機搬遷、局部救回、完整備份不可用時的保險；也是驗證還原結果的比對基準 |

PostgreSQL 是唯一的持久狀態（服務不寫本機狀態檔），所以「備份 Jelee」等於「備份資料庫＋密鑰＋設定」；媒體本身是使用者的資產，由使用者自己備份，Jelee 只讀不寫。

## 該備份什麼

| 資產 | 備份？ | 方式 | 遺失的後果 |
| --- | --- | --- | --- |
| PostgreSQL 資料庫 | **必須** | `pg_dump`（下節）；另做元資料匯出 | 帳號、權限、播放進度、手動中繼資料、規則全部消失 |
| `JELEE_WEBHOOK_MASTER_KEY`（或 `_FILE` 指向的檔） | **必須，與資料庫分開保存** | 密碼管理器／保管庫 | 所有 webhook 端點的簽章密鑰與自訂標頭永遠無法解開，見[密鑰](#密鑰) |
| 部署設定（`.env`、Compose 覆寫檔、`JELEE_CONFIG` 指向的設定檔、反向代理設定） | 必須 | 版本控制或保管庫（含密碼的檔案只放保管庫） | 需要重新設定 |
| 媒體目錄（影片、外掛字幕與音軌、`.jeleeignore`、NFO、`.jelee.bak`） | 使用者自行備份 | 使用者既有的媒體備份 | 不屬於 Jelee；Jelee 不會改動，也無法重建 |
| `JELEE_IMAGE_STORE_ROOT` 的 `originals/` | 選用 | 檔案備份（停機或接受不一致） | 只是快取；但**鎖定的遠端圖片**原圖只存在這裡，見[本機卷](#本機卷) |
| `JELEE_IMAGE_STORE_ROOT` 的 `variants/`、`tmp/` | 不備份 | — | 會重新產生 |
| `JELEE_IMAGE_TEMP_ROOT`、`$TMPDIR/jelee-service-*`、`jelee-probe-check-*`、Compose 的 `/tmp` tmpfs | 不備份 | — | 暫存，啟動清掃會清掉殘留 |
| `.jelee-nfo-*.lock`、`.jelee-nfo-stage-*`、`.jelee-nfo-commit-*` | 不單獨備份 | 隨媒體備份即可 | 見[儲存佈局 NFO 旁車檔](storage-layout.md#nfo-旁车文件) |
| 資料庫內的探測快取、NFO 快取、盤點、任務、統計 | 隨 `pg_dump` | — | 可重建：重新掃描與探測即可（耗時） |
| `JELEE_MATROSKA_CACHE_ROOT` | 不需要 | — | 只是內嵌字幕與字型的擷取快取，刪除後下次請求重新擷取 |
| `JELEE_SUBTITLE_OCR_CACHE_ROOT` | 不需要 | — | 只是點陣字幕 OCR 結果的快取，刪除後下次查詢重新辨識（耗時，受每分鐘上限） |

## PostgreSQL 備份

### 建議參數

```sh
pg_dump --format=custom --compress=6 --no-owner --no-privileges \
  --dbname=jelee --username=jelee --file=jelee-YYYYMMDDTHHMMSSZ-s71.dump
```

- **`--format=custom`**：單一檔案、可壓縮、可用 `pg_restore --list` 檢查目錄、可選擇性還原。資料庫很大時改用 `--format=directory --jobs=4`（平行傾印；還原時也可 `--jobs`）。
- **`--no-owner --no-privileges`**：還原到不同角色名稱的資料庫時不會因 `ALTER OWNER` 失敗；Jelee 只用一個資料庫角色，沒有需要保留的授權。
- **不要排除任何資料表**（不要用 `--exclude-table-data` 省掉快取表）：快取表之間有配額列、觸發器與外鍵約束，只還原一部分會讓服務拒絕啟動或配額錯亂。
- **檔名帶上 schema 版本**（例：`-s71`）。還原時要用對應版本的二進位，見 [schema 版本相容](#schema-版本相容)。`jelee-migrate status` 會印出目前版本。
- 備份檔含密碼雜湊、工作階段權杖雜湊、封存的 webhook 密鑰與雙因素驗證器密鑰、復原碼與應用程式密碼摘要、稽核紀錄：檔案權限設為 0600，異地保存時加密。封存的密鑰需要同一把主金鑰（`JELEE_WEBHOOK_MASTER_KEY`）才能開啟，主金鑰請與資料庫備份分開保存。

### 一致性與停機

`pg_dump` 在單一 REPEATABLE READ 快照內讀取所有資料表，**Jelee 運作中也能得到一致的備份**，不需要停機：

- 快照之後才寫入的資料（例如備份進行中更新的播放進度）不在這份備份裡，這就是恢復點。
- 備份當下仍在執行的任務（掃描、探測、NFO 寫回）在還原後會以「租約過期」的狀態出現，由啟動後的恢復流程接手或重試，不需要手動清理；NFO 寫回的檔案側殘留依[儲存佈局](storage-layout.md#nfo-旁车文件)的說明處理。
- `pg_dump` 會持有 `ACCESS SHARE` 鎖，期間不要執行 `jelee-migrate`（遷移需要排他鎖，會被擋住直到備份結束）。

**建議停機備份的時機**：升級前（步驟見[部署：升級與回滾](deployment.md#升级滚动升级与回滚g374g375)）、手動大量變更前、搬遷主機前。停機時資料庫完全靜止，恢復點就是停機那一刻，也不會有進行中的任務。

不要在 PostgreSQL 運作中複製 `data/postgres` 目錄；檔案層級的冷備份只能在 PostgreSQL 停止時做，而且只能還原到**同一個 PostgreSQL 主版本**。需要比每日傾印更細的恢復點（RPO）時，改用 PostgreSQL 的 WAL 封存與時間點復原（PITR）；隨附的 Compose 沒有設定這部分。

### 容器部署的指令

在專案根目錄、以 `deploy/docker-compose.yml` 為例（服務名稱 `postgres`、`jelee`、`migrate`）：

```sh
mkdir -p backups && chmod 700 backups
stamp=$(date -u +%Y%m%dT%H%M%SZ)

# 1. 完整備份（Jelee 可以繼續運作）
docker compose -f deploy/docker-compose.yml exec -T postgres \
  pg_dump -U jelee -d jelee --format=custom --compress=6 --no-owner --no-privileges \
  > "backups/jelee-$stamp-s71.dump"

# 2. 確認備份可讀（列出目錄，不寫入任何東西）
docker compose -f deploy/docker-compose.yml exec -T postgres \
  pg_restore --list < "backups/jelee-$stamp-s71.dump" > /dev/null && echo dump-ok

# 3. 元資料備份（運作中也可以；匯出在單一快照內完成）
docker compose -f deploy/docker-compose.yml run --rm --no-deps -T --entrypoint /jelee-cli jelee \
  metadata export --out - > "backups/metadata-$stamp-s71.jsonl"

chmod 600 backups/*
```

`docker compose run` 會用 `jelee` 服務的環境變數（包含 `JELEE_DATABASE_URL`），不需要另外傳連線字串；也不要把連線字串寫進腳本或日誌。元資料匯出的摘要（筆數、SHA-256）印在標準錯誤。

## 本機卷

- **資料庫卷**（`data/postgres`）：只透過 `pg_dump` 備份，見上節。
- **媒體卷**（以 `:ro` 掛載到 `/media`）：使用者資產。Jelee 在資料庫裡只存媒體根的絕對路徑和相對路徑，所以**還原後媒體必須出現在相同的容器內路徑**（例如仍然掛載在 `/media`）。宿主機上的實際位置可以不同，只要掛載點一樣。
- **圖片持久存放區**（`JELEE_IMAGE_STORE_ROOT`）：是快取，整個目錄在停機時可以刪除。唯一的例外是**鎖定的遠端圖片**：請求路徑不會抓取遠端網址，資料庫列只記得內容的 SHA-256，原圖只存在 `originals/`。不備份 `originals/` 時，還原後這些槽位會退回下一個可用的圖片，直到管理員重新整理圖片。需要完全一致時，在停機時連同 `originals/` 一起備份（`variants/` 與 `tmp/` 不需要）。
- **暫存**：`JELEE_IMAGE_TEMP_ROOT`、`$TMPDIR` 下的服務暫存、Compose 的 `/tmp`、開發用的 `.testdata/` 都不備份；啟動清掃會處理殘留（[儲存佈局](storage-layout.md#暂存与崩溃残留)）。

## 密鑰

- **Webhook 主鑰**（`JELEE_WEBHOOK_MASTER_KEY`）：每個端點的簽章密鑰與自訂標頭值都以這把 32 位元組主鑰經 AES-256-GCM 封存，附加資料綁定端點 ID（[Webhook](webhooks.md)）。**主鑰遺失的後果**：資料庫備份完整也無法解開任何端點的密鑰；投遞器會讓事件保持 pending，唯一的出路是刪除端點、重建，並把新密鑰重新設定到每個接收端。目前沒有換主鑰的工具，所以主鑰要和資料庫備份**分開保存**（例如不同的保管庫），但用同等級的保護；只有資料庫備份外洩時，攻擊者仍拿不到 webhook 密鑰。
- **還原 webhook 的條件**：同一把主鑰、端點 ID 不變。`pg_restore` 與元資料匯入都會保留端點 ID；元資料檔裡的密鑰維持封存狀態，從不解開。
- **密碼**：`pg_dump` 一定包含密碼雜湊（argon2id）。元資料匯出**預設不含**密碼雜湊，見下節。
- **工作階段與權杖**：`pg_dump` 的還原會帶回當時仍有效的工作階段。若懷疑備份檔外洩，還原後讓所有人重新登入：停止 Jelee 後在資料庫執行 `UPDATE sessions SET revoked_at=now() WHERE revoked_at IS NULL;`。元資料匯出從不包含工作階段、權杖、使用者建立金鑰。
- **資料庫密碼**（`JELEE_POSTGRES_PASSWORD`）：不在任何備份裡；還原到新主機時可以換新密碼，只要 `JELEE_DATABASE_URL` 跟著改。

## 還原步驟（完整備份）

以下假設要把 `backups/jelee-…-s71.dump` 還原到同一台主機、同一個 Compose 專案。

```sh
dc="docker compose -f deploy/docker-compose.yml"
dump=backups/jelee-20261004T030000Z-s71.dump

# 1. 停止 Jelee（資料庫保持運作）
$dc stop jelee

# 2. 換一個空的資料庫（先確認手上的備份可讀：pg_restore --list）
$dc exec -T postgres dropdb -U jelee --if-exists jelee
$dc exec -T postgres createdb -U jelee -O jelee jelee

# 3. 還原；任何錯誤都中止並整體回滾
$dc exec -T postgres pg_restore -U jelee -d jelee --no-owner --no-privileges \
  --exit-on-error --single-transaction < "$dump"

# 4. 讓 schema 與目前的二進位一致（版本相同時什麼都不做）
$dc run --rm migrate

# 5. 啟動並等待就緒
$dc up -d jelee
```

步驟 2 會刪除現有資料庫。保守的做法是先還原到另一個名稱（`createdb -U jelee jelee_restore`，還原到它，驗證完再把 `JELEE_DATABASE_URL` 指過去），舊資料庫保留到驗證通過。資料庫很大時把 `--single-transaction` 換成 `--jobs=4`（兩者不能同時用；失敗時要自行刪掉半成品重來）。

## 元資料匯出與匯入

`jelee-cli metadata export` 匯出媒體無法重建的資料，`jelee-cli metadata import` 把它套用到同版本的資料庫。

### 內容

| 匯出 | 不匯出 |
| --- | --- |
| 帳號與設定（管理員、停用、隱藏、顯示名稱、語系、原生用戶端許可、串流與頻寬上限、分級上限、刪除標記） | 工作階段、權杖、使用者建立金鑰、登入失敗計數與鎖定 |
| 密碼雜湊——**僅在 `--include-password-hashes` 時** | 預設不匯出；匯入後這些帳號沒有密碼，用 `jelee-cli account set-password` 重設 |
| — | 雙因素驗證（封存的驗證器密鑰、復原碼摘要、登入挑戰）與應用程式密碼：屬於憑據，一律不匯出。匯入後的帳號沒有第二因素，使用者需重新啟用並重建應用程式密碼；若同時匯入了密碼雜湊，這些帳號在重新啟用前只憑密碼即可登入，請通知使用者（見 [two-factor.md](two-factor.md#元資料備份)） |
| 媒體庫與設定（NFO 模式、中繼資料語言與圖片語言、自動同步）、媒體根、媒體庫授權 | 各種世代計數器（探測、NFO、盤點） |
| 目錄識別：條目、媒體檔、劇集／季資料夾、父子關係、掃描群組與掃描來源 | 盤點、盤點快照、掃描待處理清單、外掛字幕／音軌（掃描會重建） |
| 條目中繼資料欄位與事實（含手動編輯、TMDB 與 NFO 來源）、欄位鎖、獨立 NFO 欄位鎖、修訂號 | 探測快取、NFO 快取、NFO 寫回工作與日誌 |
| **鎖定**的條目圖片（來源與內容摘要） | 未鎖定的圖片（掃描與重新整理會重建）、圖片檔本身 |
| 多版本人工決定：被合併的掃描群組別名、排除誤合併的檔案、主版本、人工放置標記（[多版本](item-versions.md)） | 版本操作紀錄與撤銷資料（只為 30 天內撤銷服務） |
| 播放進度、已播放、播放次數、音軌／字幕偏好（使用者預設、條目層、版本層） | 播放工作階段與取樣、觀看統計 |
| 內容存取政策、分級對照表、條目允許／隱藏規則、封鎖標籤 | — |
| 用戶端管控政策與規則（含 `restrict_libraries` 的媒體庫，匯入時換成目的地的媒體庫） | 命中計數、已知用戶端、命中紀錄 |
| 媒體庫網路規則（G48.5；媒體庫沒有一起匯入時略過） | 分享連結與其訪客帳號、訪客的進度與規則（G48.6：持權杖即可存取，還原等於讓舊連結復活） |
| Webhook 端點（密鑰與標頭**保持封存**） | outbox、投遞紀錄 |
| 掃描排程與監看開關 | 排程的上次執行與錯誤、監看租約 |
| — | **尚未納入、只在 `pg_dump` 裡**（2026-10-06 對照 `domain.MetadataBackupKinds` 核對）：合集與合集成員、播放清單（遷移 081）、使用者介面偏好（073）、站點外觀與外掛設定（075；可另用管理員 API `GET /api/v1/site/export`／`POST /api/v1/site/import` 搬移）、修復與一致性檢查紀錄（074、083）。只靠元資料檔搬遷時，這些資料會遺失 |
| — | 稽核紀錄（只在 `pg_dump` 裡）、初始引導狀態 |

### 格式

UTF-8 的 JSON Lines，每行一筆：

```json
{"format":"jelee.metadata","formatVersion":1,"schemaVersion":71,"createdAt":"2026-10-04T03:00:00Z","passwordHashes":false}
{"kind":"library","data":{"id":"…","name":"Movies","nfo_mode":"off","metadata_language":"zh-CN","metadata_image_languages":["zh","ja","en","null"],"metadata_preferences_revision":1,"catalog_sync_auto":false}}
{"kind":"end","records":123,"counts":{"library":1,"…":0},"sha256":"<前面所有位元組的 SHA-256>"}
```

- 第一行是標頭；最後一行是尾段，記錄筆數、各類筆數與**前面每一個位元組**的 SHA-256。沒有尾段＝截斷；摘要或筆數不符、尾段後還有資料、種類順序錯亂＝損壞。兩者都會在寫入任何一列之前被拒絕。
- 種類依固定順序排列（媒體庫 → 根 → 帳號 → 授權 → 條目 → 媒體檔 → …… → 排程），每筆只引用排在前面的種類；欄位名稱就是資料表欄位名稱，bytea 以 `\x` 十六進位字串表示，時間一律 UTC。
- 單行上限 1 MiB；匯出與匯入都一次只持有一行，記憶體不隨目錄大小成長（10 萬條目、231 MB 的檔案，匯出與匯入的 Go 堆積峰值都約 2.5 MiB，見[演練紀錄](evidence/backup-drill-2026-10-04.txt)）。
- 檔案可以再壓縮或加密；匯入從標準輸入讀取時可以直接接管線（`zstd -dc 檔案 | jelee-cli metadata import --in -`）。

### 指令

```sh
jelee-cli metadata export --out FILE|- [--include-password-hashes] [--timeout 2h]
jelee-cli metadata import --in FILE|- [--dry-run] [--skip-conflicts] [--timeout 2h]
```

- `export --out FILE` 先寫到同目錄的暫存檔（0600），成功才以原子方式取用目標名稱；**已存在的檔案絕不覆寫**，失敗不留下半成品。`--out -` 寫到標準輸出，摘要改印在標準錯誤。匯出在單一唯讀快照內完成，成功後寫一筆 `metadata.exported` 稽核（安全類別，記錄筆數、是否含密碼雜湊與檔案摘要）。
- `import` 把整個檔案串流進交易內的暫存表，驗證尾段後才開始比對與套用；**整個匯入是一個交易**，任何錯誤都完整回滾。成功時寫一筆 `metadata.imported` 稽核。
- `--dry-run` 跑完全部檢查與套用後回滾，用來預覽報告。
- 匯入期間持有與掃描、登記相同的工作鎖；建議在 Jelee 停止時匯入，至少不要同時執行掃描。
- 報告（JSON，印在標準輸出，被拒絕時也會印）：`kinds` 下每一類的 `records`、`inserted`、`updated`、`unchanged`、`skipped`（依原因分），以及 `conflictTotals` 與最多 100 筆 `conflicts` 範例（只含種類、匯出時的 ID 與原因，沒有路徑或祕密）。

錯誤以固定代碼印在標準錯誤，結束碼 1（用法錯誤為 2）：

| 代碼 | 意義 |
| --- | --- |
| `metadata_backup_corrupt` | 摘要、筆數、種類順序或 JSON 不符；尾段後還有資料 |
| `metadata_backup_truncated` | 檔案在尾段之前就結束 |
| `metadata_backup_unsupported` | 格式版本不認得，或檔案來自比目前二進位更新的 schema |
| `metadata_backup_conflict` | 預檢發現衝突（報告列出），或套用後會違反全域限制；沒有寫入任何東西 |
| `metadata_output_exists` | 匯出目標已存在 |
| `metadata_database_unavailable`、`metadata_configuration_invalid` | 無法連線或設定錯誤（不印連線字串） |
| `metadata_export_failed`、`metadata_import_failed`、`metadata_output_failed`、`metadata_input_unavailable`、`metadata_cancelled` | 其他失敗、逾時或中斷 |

容器內執行（映像沒有 shell，以標準輸入輸出傳檔最簡單）：

```sh
dc="docker compose -f deploy/docker-compose.yml"
# 預覽
$dc run --rm --no-deps -T --entrypoint /jelee-cli jelee metadata import --in - --dry-run < backups/metadata-….jsonl
# 套用
$dc run --rm --no-deps -T --entrypoint /jelee-cli jelee metadata import --in - < backups/metadata-….jsonl
```

### 匯入的語意

匯入是**合併**：只新增與更新，從不刪除目標裡多出來的資料；同一個檔案匯入第二次不會有任何變更（報告全部是 `unchanged`）。

身分比對依序：

| 種類 | 比對方式 |
| --- | --- |
| 帳號 | ID，其次名稱（不分大小寫） |
| 媒體庫 | ID，其次名稱 |
| 媒體根 | ID，其次路徑；根必須屬於對應的媒體庫、路徑必須相同 |
| 條目 | ID，其次目標裡擁有同一個媒體檔、同一個劇集／季資料夾或同一個掃描群組的條目 |
| 其他 | 保留匯出時的 ID（webhook 的封存密鑰因此仍能解開） |

- 找不到的帳號、媒體庫、根、條目會以原 ID 建立，連同媒體檔、資料夾、父子關係與掃描狀態。之後的掃描會把它們視為已知檔案（大小與時間相同就是「未變更」）。
- **已存在於目標的條目**只接收使用者資料（中繼資料、鎖、圖片鎖、進度、規則），目錄結構維持目標自己的版本。
- 中繼資料欄位、事實、欄位鎖、鎖定圖片、帳號設定、媒體庫設定、規則、webhook、排程：以檔案為準覆寫。有變更的既有條目修訂號加一；有變更的既有帳號 `auth_version` 加一並撤銷其工作階段（等同管理員修改帳號）。
- 播放進度：**較新的一方勝出**（比較 `updated_at`），所以把舊檔案匯入運作中的伺服器不會倒退進度。
- 密碼雜湊：只寫給新建的帳號與目前沒有密碼的帳號，**從不覆蓋既有密碼**。
- NFO 來源的欄位、事實與獨立 NFO 欄位鎖帶有來源檔與根的 ID；只有條目與根在目標裡是同一個 ID 時才匯入，否則以 `nfo_origin_not_portable` 略過，交給 NFO 重新整理重建。
- 目標已經為同一個圖片槽鎖定了別的來源時，保留目標的選擇（`image_slot_locked`）。
- 用戶端管控規則的命中計數從零開始；規則有變更時會提高版本，所有實例在下一個請求重新編譯。
- 目標沒有初始引導狀態而匯入後有帳號或媒體庫時，標記為已完成（adopted），和升級遷移 071 相同；**引導進行到一半的資料庫會拒絕匯入**（`setup_in_progress`）。

衝突原因（預設任何衝突都中止；`--skip-conflicts` 則略過衝突的紀錄與所有引用它的紀錄，依原因計數）：

| 原因 | 情況 |
| --- | --- |
| `user_name_taken`、`library_name_taken` | ID 對上了，但名稱已被目標裡的另一筆使用 |
| `root_path_changed`、`root_library_mismatch` | 同 ID 的根路徑不同；或同路徑的根屬於別的媒體庫 |
| `item_identity_mismatch` | 對上的條目種類或媒體庫不同 |
| `item_location_conflict`、`item_location_ambiguous` | 同 ID 的條目在目標裡位置屬於另一個條目；或位置分屬多個條目 |
| `source_id_taken` | 要新建的條目，其媒體檔或資料夾 ID 已被目標使用 |
| `identity_ambiguous` | 兩筆匯出紀錄對上目標同一列 |
| `dependency_conflict` | 所屬媒體庫、根或父條目衝突 |
| `setup_in_progress` | 目標的初始引導尚未完成 |
| `last_admin`、`watch_limit`、`client_rule_limit` | 套用後沒有可用的管理員、監看媒體庫超過上限、規則數超過上限 |

略過（不是衝突）：`unresolved_reference`（引用的帳號、條目等被略過）、`source_absent`（目標既有條目沒有這個媒體檔）、`nfo_origin_not_portable`、`image_slot_locked`。

### 典型用法

- **全新主機還原**：`jelee-migrate up` 建立空 schema → **在第一次掃描之前** `metadata import` → 啟動 Jelee → 掃描。條目保留原 ID，掃描會沿用。
- **救回部分資料**（例如誤刪手動中繼資料）：把舊的元資料檔匯入運作中的資料庫。合併語意不會刪除後來新增的資料，進度不會倒退。
- **還原驗證**：還原前後各匯出一次，比較記錄行（去掉標頭與尾段）：`diff <(sed '1d;$d' before.jsonl) <(sed '1d;$d' after.jsonl)`。輸出為空代表所有使用者資料一致。

## schema 版本相容

每個 Jelee 二進位只接受一個確切的 schema 版本（`internal/adapter/postgres` 的 `SchemaVersion`，本文更新時為 83；版本政策見 [ADR 0006](adr/0006-migration-version-policy.md)）。

| 情況 | 做法 |
| --- | --- |
| `pg_dump` 來自 schema N，要給 schema M 的二進位用，M > N | 還原後執行 `jelee-migrate up`（新版二進位附帶的）。升級遷移不刪表 |
| M < N（用舊二進位開新備份） | 不支援直接使用。改用 schema N 的二進位；或還原後用 schema N 的 `jelee-migrate down --i-understand` 逐級降版——多個版本在保留新增資料時會拒絕降版（見[部署](deployment.md)），這時只能用 N 版二進位 |
| 元資料檔 schema ≤ 二進位 schema（同格式版本 1） | 直接匯入。格式版本 1 涵蓋 schema 71 起 |
| 元資料檔 schema > 二進位 schema | 拒絕（`metadata_backup_unsupported`）：先升級二進位 |
| 格式版本不同 | 拒絕；欄位形狀改變時格式版本會提高，並在本文說明轉換方式 |
| PostgreSQL 主版本不同 | `pg_dump` 的檔案可以還原到**相同或更新**的主版本；不能往回。冷備份的資料目錄只能用相同主版本 |

每份備份都應該記錄 schema 版本（檔名帶 `-s71`）。升級前一定先做完整備份，這是降版唯一可靠的途徑。

## 驗證還原成功的檢查清單

1. `pg_restore --list` 讀得出備份目錄；還原指令沒有錯誤（`--exit-on-error`）。
2. `jelee-migrate status` 顯示 `version=71 dirty=false`（或目前二進位的版本）。
3. `jelee-cli doctor` 全部通過；`/readyz` 回 200，`data.setup` 是 `completed`。
4. 關鍵資料表筆數與備份前一致（備份前先記下）：

   ```sql
   SELECT (SELECT count(*) FROM users) users, (SELECT count(*) FROM libraries) libraries,
          (SELECT count(*) FROM items) items, (SELECT count(*) FROM media_sources) sources,
          (SELECT count(*) FROM user_item_data) progress, (SELECT count(*) FROM item_metadata_fields WHERE source='manual') manual_fields,
          (SELECT count(*) FROM client_rules) client_rules, (SELECT count(*) FROM webhooks) webhooks;
   ```

5. 還原後再做一次元資料匯出，與備份當時的元資料檔逐行比較（見[典型用法](#典型用法)）。
6. 以管理員登入；一般使用者看到的媒體庫與條目符合授權與內容規則。
7. 隨機開幾個條目：手動標題、鎖定欄位與鎖定海報都在；續播位置正確；媒體可以播放（媒體掛載點正確）。
8. 對一個 webhook 端點觸發測試投遞，接收端驗證簽章成功——這同時驗證了主鑰正確。
9. 執行一次掃描：已知檔案應全部是「未變更」，沒有大量新增或遺失。
10. 確認備份當時進行中的任務已被恢復流程處理（`jelee-cli jobs`），沒有卡住的租約。

## 備份演練

- **自動演練**：`make backup-drill`（需要 `JELEE_TEST_DATABASE_URL` 指向專用的 `jelee_test` 資料庫）。在兩個新遷移的 schema 之間：建立涵蓋每一種紀錄的資料加 2,000 集 → 匯出 → 試跑匯入（確認回滾後為空）→ 匯入 → 再匯出並逐類逐行比對 → 第二次匯入確認無變更 → 檢查工作階段未還原、引導狀態、密碼、監看狀態、稽核。另外驗證：預設不匯出密碼雜湊、已掃描目標的身分對應、衝突預檢不寫入任何東西、損壞與截斷的檔案被拒絕、CLI 端到端。紀錄寫到 `BACKUP_DRILL_REPORT`（預設 `.testdata/backup-drill.txt`），內容不含連線資訊或祕密。
- **規模演練**：`make backup-scale`：10 萬條目（80 萬筆紀錄、約 231 MB）的匯出與匯入，量測 Go 堆積峰值（上限 16 MiB）。匯入速度受條目插入觸發器限制，約每個新條目 2–3 ms。
- 最近一次實跑：[backup-drill-2026-10-04.txt](evidence/backup-drill-2026-10-04.txt)。

演練只在測試資料庫進行。**正式環境的演練必須由擁有者執行**，至少包含：

1. 在正式主機以上面的容器指令做一次 `pg_dump` 與元資料匯出，確認檔案權限 0600、異地副本已加密。
2. 把 `pg_dump` 還原到**另一台**主機（或同主機的另一個資料庫名稱），依檢查清單 1–10 驗證，並記錄所花時間（恢復時間 RTO）與備份時間點（恢復點 RPO）。
3. 在還原的實例上用正式主鑰驗證 webhook 簽章（清單第 8 項）；確認主鑰保存在與資料庫備份不同的位置，並實際從那裡取回一次。
4. 在全新 schema 上只用元資料檔還原（`jelee-migrate up` → `metadata import` → 掃描），確認使用者用 `jelee-cli account set-password` 重設密碼後可以登入、進度與手動中繼資料都在。
5. 以升級流程演練一次：備份 → 停機 → 升級遷移 → 啟動；再用備份回滾到升級前，確認舊版二進位可以啟動。
6. 把結果（日期、資料量、各步驟耗時、問題）記錄到 `docs/evidence/`，內容不得包含連線字串、主機名稱以外的內部位址或任何祕密。

## 資料生命週期中與備份相關的部分（G36.3）

- **保留期**：建議每日完整備份保留 7 份、每週保留 4 份、每月保留 3 份；元資料檔與同一天的完整備份一起保存與輪替。升級前的備份保留到確定不再回滾。
- **稽核紀錄**：稽核表只能附加，依 `audit_retention` 在資料庫內清除；但**備份檔裡的稽核紀錄不受清除影響**，會保留到該份備份被輪替掉。稽核封存的實際保留期＝資料庫保留期與備份保留期取較長者。
- **刪除使用者資料（G07.7）**：刪除帳號後，舊的備份檔仍含該使用者的資料，直到輪替期滿。對外承諾的「刪除後不可恢復」期限應寫成「刪除後最長再保留＜備份保留期＞於備份中」。還原舊備份後，要重新執行還原時間點之後的刪除請求。
- **快取與可重建資料**：探測快取、NFO 快取、圖片變體不需要也不應該另外備份；它們隨 `pg_dump` 一起保存只是因為無法安全地排除。
- **加密與存取**：備份檔與元資料檔都含個人資料（帳號名稱、觀看紀錄）。只給運維存取，異地副本加密，傳輸走加密通道。

### 與 G07.7 使用者資料匯出的關係

G07.7 的個人資料匯出與永久刪除見 [使用者資料權利](user-data-rights.md)。匯出採用自己的 NDJSON 格式（`jelee.user-data`，同樣是版本化標頭、逐行串流、以結尾紀錄確認完整），範圍是單一使用者的全部資料，與全域元資料檔無關。永久刪除後，還原早於刪除時間的完整備份或匯入早於刪除時間的元資料檔會把該使用者帶回，需依上方說明重新執行刪除。
