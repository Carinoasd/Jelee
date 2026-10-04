# 選配雙因素驗證與應用程式密碼（G07.8）

需求原文：「可選：TOTP 雙因素（不得破壞第三方客戶端登入路徑，需提供無 2FA 裝置權杖流程說明）」。本文件同時是該條要求的「無 2FA 裝置權杖流程說明」。

## 概要

- **選配、按帳號**：每位使用者自行在網頁「設定 → 雙因素驗證」啟用；未啟用的帳號所有登入路徑完全不變。
- **演算法**：RFC 6238 TOTP（HMAC-SHA1、6 位數、30 秒），以 RFC 4226 截斷；相容所有常見驗證器 App。密鑰 160 位元（`crypto/rand`），以標準庫自行實作，不新增相依（RFC 4226 附錄 D 與 RFC 6238 附錄 B 的 SHA1／SHA256／SHA512 測試向量全數通過，見 `internal/domain/twofactor_test.go`）。
- **時間容差與重放**：接受目前時間步前後各 1 步（±30 秒）；伺服器記錄最後一次接受的時間步（`user_totp.last_step`），之後只接受**更晚**的時間步，所以同一個碼、以及比它更早的碼都不能再用（同時登入兩個分頁時只有一個成功）。檢查與記錄在同一交易、同一列鎖下完成。
- **密鑰封存**：以伺服器主金鑰（與 Webhook 相同的 `JELEE_WEBHOOK_MASTER_KEY`／`JELEE_WEBHOOK_MASTER_KEY_FILE`，AES-256-GCM，`internal/platform/secretbox`）封存，封存情境綁定使用者 ID 與用途（`jelee-totp-secret:v1:<userId>`），複製到其他使用者或其他欄位都無法開啟。主金鑰與是否啟用 Webhook 無關：只要設定了有效主金鑰即可使用雙因素。**未設定主金鑰時無法啟用**：狀態 `available:false`、啟用回 `409 two_factor_unavailable`；主金鑰無效時伺服器照常啟動並記錄警告 `two_factor_key_invalid`。已啟用的帳號若之後主金鑰遺失或更換，驗證碼無法核對（同樣回 `409 two_factor_unavailable`，不計入失敗），仍可用復原碼完成登入，或由管理員重設。
- **復原碼**：啟用時產生 10 組（每組 80 位元，顯示為 `xxxx-xxxx-xxxx-xxxx`，不分大小寫、可省略連字號），**只顯示一次**；資料庫只存綁定使用者 ID 的 SHA-256 摘要。每組只能用一次。可用目前驗證碼重產（舊的全部作廢）。

## 啟用、停用與重設

| 方法與路徑 | 權限 | 說明 |
| --- | --- | --- |
| GET `/api/v1/users/{id}/two-factor` | 自己或管理員 | `available`、`enabled`、`enabledAt`、`pending`、`recoveryCodesRemaining` |
| POST `/api/v1/users/me/two-factor/enroll` | 自己（web 工作階段） | `{}`；產生新密鑰（取代尚未確認的舊密鑰），回 `secret`（base32）與 `uri`（`otpauth://totp/Jelee:<名稱>?…`），只回這一次。已啟用時 `409 conflict` |
| POST `/api/v1/users/me/two-factor/confirm` | 自己（web） | `{"code":"123456"}`；驗證碼正確才啟用，回 10 組復原碼；**撤銷此帳號除目前工作階段外的所有工作階段**（它們只憑密碼建立，包括原生裝置的工作階段）；稽核 `user.two_factor_enabled` |
| POST `/api/v1/users/me/two-factor/recovery-codes` | 自己（web） | `{"code"}`；重產復原碼；稽核 `user.recovery_codes_regenerated` |
| POST `/api/v1/users/me/two-factor/disable` | 自己（web） | `{"password", "code"}` 或 `{"password", "recoveryCode"}`；密碼錯 `400 invalid_password`、碼錯 `400 invalid_two_factor_code`；稽核 `user.two_factor_disabled`。應用程式密碼保留，需要時另外撤銷 |
| DELETE `/api/v1/users/{id}/two-factor` | 管理員（web） | 使用者遺失驗證器與復原碼時重設；應用程式密碼保留；稽核 `user.two_factor_reset`（沒有可重設時不寫） |

- 驗證碼的每次嘗試（確認、重產、停用）先消耗獨立的雙因素限速表（IP 與使用者 ID 兩維，額度沿用登入限速設定），超過回 `429 auth_rate_limited`。
- **自助變更只接受 web 工作階段**：native 工作階段（包括應用程式密碼換得的）一律 `403 forbidden`，也不能建立或撤銷自己的應用程式密碼。管理員重設同樣要求 web 工作階段，以免管理員的應用程式密碼被拿來移除管理員自己的第二因素。
- **營運者救援**：唯一的管理員也遺失驗證器時，用本機資料庫憑據執行 `jelee-cli account reset-two-factor --name NAME`（與 `set-password` 同屬受信任的本機入口；稽核 `user.two_factor_reset`，`after.local=true`）。

## 網頁登入的第二步

1. `POST /api/v1/auth/login` 密碼正確且帳號已啟用雙因素時，回 `200 {"data":{"secondFactorRequired":true,"challenge":"…","expiresAt":"…"}}`：**不簽發工作階段、不設 Cookie**。挑戰權杖為 32 位元組隨機值，資料庫只存 SHA-256；有效 5 分鐘，每位使用者最多保留 5 個未用挑戰。
2. `POST /api/v1/auth/login/second-factor`，正文 `{"challenge","code"}` 或 `{"challenge","recoveryCode"}`（二擇一）。成功即與一般登入相同：簽發 web 工作階段、設定 `__Host-jelee_session` Cookie、回 `csrf`。
3. 挑戰**單次使用**：第一個正確的碼即作廢它；錯 5 次也作廢；密碼變更或重設（`auth_version` 改變）、雙因素被停用或重設後同樣作廢。作廢、過期或不存在一律 `401 login_challenge_invalid`，網頁回到密碼步驟。
4. **失敗計數與鎖定共用**：錯誤的驗證碼或復原碼與錯誤密碼計入同一個 `failed_login`／`locked_until`（預設 5 次鎖 15 分鐘），寫安全類稽核 `login.second_factor_failed` 與 Webhook `user.login_failed`／`user.locked`。只有第二步完成才把失敗計數歸零——**密碼正確本身不歸零**，否則持有密碼的人可以無限輪替「密碼、四次猜碼」。帳號被鎖時第二步回 `401 authentication_required`。
5. **獨立限速**：第二步另有一張限速表（IP 與挑戰兩維），與密碼登入的限速表互不消耗。
6. 稽核：`login.second_factor_challenged`（密碼正確、已發挑戰）、`login.recovery_code_used`（含剩餘數量）、`session.created`。

## 第三方客戶端：無 2FA 裝置權杖流程（應用程式密碼）

原生登入（`POST /api/v1/auth/login/native`）與相容層（`POST /compat/Users/AuthenticateByName`）沒有地方輸入第二因素。為了**不破壞這些登入路徑**，啟用雙因素的帳號改用「應用程式密碼」：

1. 使用者在網頁登入（完成雙因素第二步）後，到「設定 → 應用程式密碼」為裝置建立一組，填一個名稱（例如「客廳電視」）。伺服器回傳一次 `password`（160 位元，八組四字元，例如 `abcd-efgh-…`），**只顯示這一次**；資料庫只存綁定使用者 ID 的 SHA-256 摘要。每人最多 20 組。
2. 在原生 App 或第三方客戶端的登入畫面，**使用者名稱照舊，密碼欄填應用程式密碼**（可含或不含連字號、不分大小寫）。登入流程其餘部分不變：同一張登入限速表、同一套失敗計數與鎖定、仍需管理員開啟 `allowNative`、同時工作階段上限照舊。簽發的 native 工作階段記住是哪一組應用程式密碼簽發的（`sessions.app_password_id`，輪換後的新權杖也保留）。
3. 第三方客戶端日後重新登入（例如工作階段過期）時繼續使用同一組應用程式密碼；它不會因為使用而失效，直到被撤銷。
4. 撤銷：在同一頁刪除該組（`DELETE /api/v1/users/{id}/app-passwords/{appPasswordId}`，本人 web 工作階段或管理員），**由它簽發的所有工作階段立即撤銷**，該裝置下次請求即被登出。稽核 `user.app_password_created`、`user.app_password_revoked`、`login.app_password_used`；清單顯示最後使用時間。

**以帳號密碼走原生／相容層登入一律拒絕**（只在密碼驗證通過後判斷，不洩漏帳號狀態給未持有密碼的人；不計入失敗、不改計數）：

- 原生登入：`403 app_password_required`（四語訊息），稽核 `login.app_password_required`。
- 相容層：上游格式只有狀態碼，因此回 `403`、標頭 `X-Jelee-Error: app_password_required`，並附純文字說明（會顯示錯誤內容的客戶端可直接讓使用者看到「請改用應用程式密碼」）。錯誤密碼仍是與以往相同的空白 `401`。

其他細節：

- 應用程式密碼**不能**用於網頁登入（網頁登入只比對帳號密碼），也不能取代第二步。
- 應用程式密碼對未啟用雙因素的帳號同樣有效，方便使用者先替裝置換好密碼再啟用雙因素。
- 修改帳號密碼不影響應用程式密碼（它們是獨立憑據，改密只撤銷工作階段）；需要時請一併撤銷。
- `jelee-cli provision --native` 仍是受信任的本機入口，不受此規則影響。

## 資料表、遷移與降級

遷移 `000077_two_factor`：`user_totp`（封存密鑰、`enabled_at`、`last_step`）、`user_recovery_codes`（摘要、`used_at`）、`login_challenges`（摘要、`auth_version`、到期、嘗試次數、`used_at`）、`app_passwords`（名稱、摘要、建立與最後使用時間），以及 `sessions.app_password_id`（`ON DELETE SET NULL`）。全部以 `ON DELETE CASCADE` 跟隨使用者。

**降級**：仍有任何**已啟用**的雙因素時，down 遷移拒絕（`reset two-factor enrollments before downgrade`），因為前一版 schema 不認得第二因素，直接丟棄會讓這些帳號只憑密碼即可登入。先由管理員重設或使用者停用後再降級。未確認的啟用、挑戰與應用程式密碼直接丟棄；由應用程式密碼簽發的工作階段在前一版 schema 下如同其他工作階段，照常有效直到到期或撤銷。

## 元資料備份

**不匯出**雙因素與應用程式密碼的任何資料（封存密鑰、復原碼摘要、挑戰、應用程式密碼摘要）：它們屬於憑據，與工作階段、權杖同類（元資料備份本來就不含工作階段與權杖）。匯入後的帳號**沒有**第二因素，使用者需重新啟用並重建應用程式密碼；若備份含密碼雜湊（`--include-password-hashes`），匯入後的帳號暫時只憑密碼即可登入，營運者應通知已啟用雙因素的使用者在匯入後立即重新設定。

## 網頁介面

- 登入頁：密碼正確後切換到第二步（6 位數驗證碼，可改用復原碼）；挑戰失效時回到密碼步驟。
- 設定頁「雙因素驗證」：狀態、啟用（QR code、otpauth URI 與密鑰文字、輸入驗證碼確認、復原碼只顯示一次並可下載 .txt）、重產復原碼、停用；「應用程式密碼」：建立（只顯示一次）、清單與撤銷。
- 管理頁使用者詳細：雙因素狀態與「重設雙因素」。
- QR code 由前端自行以純 TypeScript 產生並以內嵌 SVG 呈現（不新增相依）。
