# 使用者資料權利：匯出與永久刪除（G07.7）

需求原文：「数据权利：导出与删除用户数据（含播放记录），删除后不可恢复且级联处理。」

## API

| 方法與路徑 | 權限 | 說明 |
| --- | --- | --- |
| GET `/api/v1/users/{id}/data-export` | 自己（任何工作階段）或管理員（任何使用者，含已軟刪除者） | 以 NDJSON 串流下載個人資料；`Content-Disposition: attachment`、`Cache-Control: no-store`；稽核 `user.data_exported`（security 類） |
| POST `/api/v1/users/me/purge` | 自己（web 工作階段） | `{"password"}`，帳號啟用雙因素時另需 `code` 或 `recoveryCode` 其中之一；成功 `204` 並清除工作階段 Cookie |
| POST `/api/v1/users/{id}/purge` | 管理員（web 工作階段） | 本文 `{}`；不能刪自己（`403`，改用 `/users/me/purge`）；可刪除使用中或已軟刪除的使用者 |

- 自助刪除的密碼與驗證碼共用雙因素限速表（同停用雙因素），超過回 `429 auth_rate_limited`；密碼錯 `400 invalid_password`、碼錯或缺碼 `400 invalid_two_factor_code`；native 工作階段（包括應用程式密碼換得的）一律 `403 forbidden`。
- 最後一位有效管理員不能被刪除（`409 last_admin`），自助或管理員路徑皆同。
- 原有的 `DELETE /api/v1/users/{id}` 仍是**可還原的軟刪除**（撤銷工作階段、刪播放資料），與本文的永久刪除分開。

## 匯出格式

每行一個 `{"type": "...", "data": {...}}`：

1. `export`：`format`（`jelee.user-data`）、`version`（1）、`userId`、`userName`、`generatedAt`（快照時間）。
2. 資料紀錄，依序：`account`、`preferences`、`trackPreference`、`playlist`、`playlistItem`（自己的播放清單與目前看得到的條目）、`blockedTag`、`blockedKeyword`、`accessWindow`（封鎖標籤、關鍵字與限制時段）、`libraryAccess`、`itemAccessRule`、`itemData`（播放進度、已播放、次數）、`playbackSession`、`playbackSample`（播放紀錄）、`watchStatsDay`、`watchStatsItem`（觀看統計）、`session`（裝置與工作階段中繼資料：裝置名稱、用戶端、建立／最後使用時間、最後 IP）、`appPassword`（名稱與日期）、`shareLink`（自己建立的分享）、`clientControlHit`、`auditEvent`（以此使用者為操作者或對象的事件：事件名、時間、類別、角色；IP 只在使用者自己是操作者時列出；不含前後狀態，因為狀態可能描述其他使用者）。
3. `end`：`records` 為資料紀錄筆數。**沒有 `end` 行代表串流中斷**；回應另有 `X-Jelee-Export-Complete` trailer。

- **秘密一律不匯出**：密碼雜湊、應用程式密碼摘要、TOTP 密鑰、復原碼摘要、工作階段／分享／登入挑戰權杖、Webhook 密鑰。SQL 只選列出的欄位，測試確認輸出不含這些值。
- **不洩漏隱藏內容（G48）**：項目只以 ID 表示、不附標題；指向項目或媒體庫的紀錄（播放進度、播放紀錄與樣本、統計、音軌偏好、項目存取規則、媒體庫授權、分享）只匯出**被匯出者目前仍看得到**的項目與媒體庫，並套用本次請求的網路／用戶端媒體庫限制。看不到的項目的紀錄不出現在匯出中，但永久刪除時一樣刪除。存取洩漏測試（`TestAccessLeakHiddenContentPostgres`）涵蓋此路由。
- **記憶體**：所有區段在同一個 `REPEATABLE READ READ ONLY` 交易快照中逐列讀取、逐行寫出，不在記憶體中累積。整個匯出上限 5 分鐘；每個執行個體同時最多 2 個匯出、每個呼叫者 1 個，超過回 `409 conflict`（附 `Retry-After`）。稽核在送出第一個位元組前寫入並提交。

## 永久刪除做了什麼

一個交易內（陳述式逾時 30 秒）：

1. 自助路徑：在交易內重新核對已驗證密碼的憑證快照（`auth_version`、雜湊未變），帳號啟用雙因素時核對驗證碼（記錄時間步防重放）或消耗一組復原碼。
2. 最後管理員保護。
3. 此使用者擁有的掃描排程移交：管理員路徑移交給執行刪除的管理員，自助路徑移交給建立最早的其他有效管理員（沒有可接手者時 `409 last_admin`）。
4. 從使用者範圍的用戶端控制規則（G47）移除此使用者，只剩此使用者的規則直接刪除，並遞增政策版本。
5. 刪除沒有自動級聯的資料：`user_creation_keys`、`nfo_policy_requests`、`nfo_write_preparations`（冪等紀錄）、`client_control_hits`、`legacy_import_map` 中指向此使用者的對照、此使用者建立的 `share_links`（其訪客帳號隨外鍵級聯刪除）、主體是此使用者或資料含其 `userId` 的 `webhook_outbox` 事件；`known_clients.last_ip` 清空。
6. `DELETE FROM users`，外鍵級聯刪除：工作階段（含 `known_client_sessions`）、應用程式密碼、TOTP、復原碼、登入挑戰、偏好、音軌偏好、封鎖標籤、媒體庫授權、項目存取規則、播放會話與樣本、`user_item_data`、觀看統計。`ON DELETE SET NULL` 的欄位（工作、版本操作、清單接受、用戶端與網路規則的建立者、分享的撤銷者、已知用戶端的最後使用者）保留列但不再指向任何人。
7. 稽核去識別化（見下節），最後寫入 `user.purged`（不含 IP，`after` 只有計數與旗標）。

沒有任何還原路徑：`POST /users/{id}/restore` 對已永久刪除的 ID 回 `404`。舊備份仍含資料，直到輪替期滿，見 [備份與還原](backup-restore.md#資料生命週期中與備份相關的部分g363)。

## 稽核紀錄的處理（決定與理由）

**保留事件、去識別化**，不刪除：稽核表只能附加（遷移 060），刪除只能經保留期清除；為了刪帳號而刪稽核會讓「誰在何時做了什麼」無從追查，包括這次刪除本身。遷移 `000082_user_data_rights` 新增唯一的修改路徑 `redact_audit_subject(schema, subject, objects)`：

- 以此使用者為操作者的事件：`actor_ip` 設為 NULL；沒有操作者而以此使用者為對象的事件（例如登入失敗）同樣清除 IP。
- 以此使用者，或其工作階段、應用程式密碼、分享、播放會話為對象的事件：`before_state`／`after_state` 改為 `{"redacted":"user_purged"}`。
- 事件名、時間、類別、請求 ID、操作者與對象 UUID 保留。UUID 是隨機值，刪除後沒有任何表能對應回名稱，成為假名。
- 觸發器只在此函式執行中（函式範圍設定 `jelee.audit_redact`）接受 UPDATE，而且只允許：其他欄位完全不變、IP 只能變 NULL 且操作者已不是帳號、狀態只能變成標記且操作者或對象已不是帳號。對仍存在帳號的事件呼叫此函式會被拒絕；其他 UPDATE 照舊拒絕。
- 去識別化後的事件仍依原類別的保留期清除。

## 守門測試

`TestUserPurgeLeavesNoUserRows`（`internal/adapter/postgres/userdata_integration_test.go`，真 PG）：

- 從 `pg_constraint` 讀出所有指向 `users` 的外鍵，必須都列在測試的 `userReferenceColumns`（標註 cascade／set null／explicit／redacted）；從 `information_schema.columns` 讀出名稱像使用者參照（`user`、`actor`、`owner`、`*_by`、`target_user`）但沒有外鍵的 uuid 欄位，也必須列入。**日後新增表忘了處理，這個測試就會失敗。**
- 為使用者在每個能存資料的表建立資料（播放、統計、偏好、TOTP、應用程式密碼、分享與訪客、排程、工作、規則、Webhook 事件、舊系統對照…）後以管理員刪除，再逐欄確認沒有任何列指向此使用者，稽核只剩去識別化的事件，其他使用者的資料不受影響。

其他測試：自助刪除的重新驗證、雙因素、過期快照、最後管理員與排程移交（`TestUserPurgeSelfReauthenticationAndLastAdmin`）；匯出快照與秘密檢查（`TestUserDataExportSnapshotWithoutSecrets`）；遷移 down/up（`TestUserDataRightsMigrationDownUp`）；HTTP 端到端（`internal/adapter/http/userdata_integration_test.go`）；前端（`web/src/features/settings/dataRights.test.ts`）。

## 前端

設定頁「你的資料」區塊：「下載我的資料」；「永久刪除帳號」需輸入密碼、帳號啟用雙因素時輸入驗證碼或復原碼、勾選「我了解無法復原」，再按兩次（`UiConfirmButton` 的二次確認）。成功後清除本機登入狀態並回到登入頁。分享訪客不顯示此區塊。

## 已知限制

- 刪除在單一交易內完成並持有帳號管理的 advisory lock；播放樣本極多的使用者可能使其他帳號操作在刪除期間等待或逾時（30 秒上限），大量資料下的耗時尚未實測。
- 匯出由瀏覽器以 Blob 下載，資料量很大時佔用瀏覽器記憶體；伺服器端是串流。
- [舊系統匯入](legacy-import.md)（G04.6）若再次從同一個舊資料庫匯入，會把該使用者當成新帳號建立；那是新的外部資料匯入，不是還原。
