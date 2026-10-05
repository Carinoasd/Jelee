# 前端架構決策紀錄（G31／G27／G35）

狀態：已採用（第 15 階段 15.1、15.3、15.4；插件體系、自訂 CSS、版面與 bundle 預算見後半）。日期：2026-10-04。

## 決策

網頁端採用 Vue 3.5 + Vite 8 + TypeScript 5.9（strict）+ Pinia 3 + vue-router 4 + vue-i18n 11 + openapi-fetch；型別由 openapi-typescript 從 `api/openapi.json` 產生。開發工具為 vue-tsc、ESLint 10（typescript-eslint strict type-checked、eslint-plugin-vue、@intlify/eslint-plugin-vue-i18n）與 Vitest 5（jsdom）。不使用 UI 元件庫，元件放在 `web/src/components/ui/`，樣式全部取自設計 token。

### 為何選 Vue 3 而非 Svelte

- **生態與需求對位**：G31.1 要求的狀態管理（Pinia）、路由守衛與懶載入（vue-router）、四語 i18n（vue-i18n，含 ESLint 外掛可檢查硬編碼文字與缺鍵）都是 Vue 官方或核心團隊維護的套件，版本節奏一致；Svelte 需要自行拼湊等價方案。
- **外掛體系（G32）**：Vue 的 `app.use`、`provide/inject`、`defineAsyncComponent` 與 `onErrorCaptured` 直接對應「懶載入插件元件、錯誤邊界降級、插件拿不到令牌」的要求，不必自建執行期。
- **型別檢查**：`vue-tsc --noEmit` 對 SFC 做完整型別檢查，可以作為 G31 驗收的 `tsc --noEmit` 門禁；泛型元件（`<script setup generic>`）讓請求狀態元件保有資料型別。
- **安全預設**：模板一律轉義，唯一的原始 HTML 出口 `v-html` 可用 lint 全面禁止（`vue/no-v-html: error`），符合 G35.1。
- **體積**：Vue runtime 與 Svelte 編譯產物在本專案規模差異不大；首屏 JS 由 `web/bundle-budget.json` 設預算並在建置時斷言（見「Bundle 預算」）。

### 版本取捨

- TypeScript 固定 5.9.3：typescript-eslint 8.71 只支援 `<6.1`，openapi-typescript 7.13 的 peer 為 `^5.x`；TypeScript 6/7 待工具鏈支援後再升。
- Pinia 固定 3.0.4、vue-router 4.6.4：依技術方案使用 vue-router 4；Pinia 3 是與 vue-router 4 搭配的穩定主線。
- 所有直接依賴在 `web/package.json` 精確固定，完整解析與完整性雜湊鎖在根目錄 `package-lock.json`；`.npmrc` 設 `ignore-scripts=true`，依賴的 lifecycle 腳本永不執行。

## 目錄結構（G31.2）

```
package.json            npm workspace 根（成員 web/），固定 Node / npm 版本範圍
package-lock.json       唯一 lockfile
web/
  package.json          @jelee/web
  index.html            無行內腳本或樣式
  vite.config.ts        建置與 Vitest 設定
  eslint.config.js
  scripts/
    api-types.mjs       產生 / 檢查 src/api/schema.d.ts
    check-i18n.mjs      i18n 門禁
    check-no-playback.mjs 禁播門禁
  src/
    api/                產生的契約型別、openapi-fetch 用戶端、驗證策略、錯誤正規化、請求狀態機
    stores/             Pinia store（只放狀態與動作，不放畫面）
    features/<domain>/  各領域的 API 呼叫與頁面（auth、libraries、items、account、errors）
    components/ui/      無業務邏輯的基礎元件（按鈕、輸入、提示、請求狀態）
    theme/              設計 token（CSS 變數，含深色與 reduced-motion）與基礎樣式
    plugins/            Vue app 外掛的組合根（index.ts）與 G32 插件體系：
      sdk/              @jelee/plugin-sdk：型別化 Hook、Manifest 驗證、語意化版本、棄用表（只依賴 vue）
      host/             插件宿主：清單探索、Pinia store、受限 API、設定命名空間、ErrorBoundary、插槽元件
      official/<名稱>/   官方範例插件（manifest.json、入口、四語訊息、懶載入元件）
    features/home/      首頁（可排序與隱藏的區塊）
    features/admin/     管理頁外殼、插件管理、自訂 CSS
    i18n/<locale>/*.json 四語訊息；locales.ts 為語系協商
    router/             路由表（全部懶載入）、守衛、redirect 驗證
  bundle-budget.json    首屏 gzip 體積預算（G35.4）
  scripts/check-bundle-budget.mjs 預算門禁
```

業務規則不寫在元件內：元件呼叫 store，store 呼叫 `features/*/api.ts`，後者只透過 `api/` 的型別化用戶端存取伺服器。

### API 型別（G31.4）

`npm run api:generate` 以 openapi-typescript 從已提交的 `api/openapi.json` 產生 `web/src/api/schema.d.ts`（提交入庫）；`web-types` 先執行 `api-types.mjs --check`，內容與重新產生的結果不同即失敗。Go 端 `make openapi-check` 保證 `api/openapi.json` 與路由一致，兩道門禁串起來即可確認前端型別未過期。用戶端型別 `WebPaths` 直接剔除所有 `.../stream` 路徑，網頁端呼叫直投串流在編譯期就會失敗。

### 路由與請求狀態（G31.3、G31.5）

- 所有頁面以動態 import 懶載入；`/:pathMatch(.*)*` 對應 404，`/forbidden` 對應 403。
- 守衛：未登入導向 `/login?redirect=<原路徑>`；已登入造訪 `/login` 轉回目標；`meta.admin` 路由在非管理員時導向 403。守衛只影響體驗，真正的授權永遠由伺服器檢查（G35.2）。
- `redirect` 參數只接受以單一 `/` 開頭、無反斜線與控制字元的站內路徑，避免開放式重導。
- `api/requestState.ts` 的狀態機為 idle → loading → success／empty／error；過時的回應（被較新請求取代）一律丟棄。`RequestStatus` 元件統一呈現載入、錯誤（含 traceId 與重試）與空狀態。
- 錯誤一律正規化為 `ApiError`（伺服器錯誤碼、HTTP 狀態、traceId；網路錯誤為 `network_error`），畫面只依錯誤碼顯示本地化訊息，不直接顯示伺服器原文。

### 國際化（G03）

- 只有 `zh-CN`（預設）、`zh-TW`、`ja-JP`、`en-US`（回退）。瀏覽器未表明語言時用 zh-CN；不支援的語言靜默回退 en-US；登入後以使用者帳號的 locale 覆蓋瀏覽器偏好。協商規則與伺服器 `internal/platform/i18n` 相同（zh-Hant／TW／HK／MO → zh-TW，其餘 zh → zh-CN）。API 請求帶 `Accept-Language` 為目前介面語言。
- 命名空間預設隨入口一次載入；只在單一懶載入頁面使用的命名空間（目前 `clients`、`shares`、`networkRules`）列在 `i18n/index.ts` 的 `lazyNamespaces`，由路由的 `lazyView(namespace, import)` 在進入頁面時與元件一起載入，不計入首屏預算。
- 每個檔案 `web/src/i18n/<locale>/<namespace>.json` 只有一個與檔名相同的頂層鍵。**`core.json` 保留**給即將由伺服器端 UI 資源移入的字串：允許平面結構（載入時包在 `core` 命名空間下），並暫時豁免「未使用鍵」檢查。
- 目錄預設隨入口一次載入；**例外 `twoFactor.json`**（G07.8）：入口 bundle 預算（G35.4）只剩約 3 KB，雙因素畫面的訊息改由 `i18n/twoFactor.ts` 的 `useTwoFactorI18n()` 在登入第二步、設定頁、管理員使用者頁懶載入並合併（每個 i18n 實例一次），`index.ts` 的 eager glob 排除它，測試確認入口目錄不含這些鍵。這些畫面以外不應引用 `twoFactor.*` 鍵（四個雙因素錯誤碼只會出現在這些畫面）。
- `check-i18n.mjs`：四語目錄完全一致、命名空間檔案一致、嚴格 JSON（拒絕重複鍵）、非空字串、缺鍵／多餘鍵、`{name}`／`{0}`／`@:key` 佔位符一致、簡繁混用偵測（語言自稱 `common.localeNames.*` 除外）、原始碼引用的鍵必須存在、目錄中的鍵必須被使用。ESLint 另以 `@intlify/vue-i18n/no-raw-text` 禁止模板硬編碼文字，`no-missing-keys` 檢查模板引用的鍵。

## 驗證與令牌（G35.1）

伺服器的 `POST /api/v1/auth/login` 對 web session 設定 `__Host-jelee_session` Cookie（HttpOnly、Secure、SameSite=Strict），回應正文另含一次性 bearer 令牌與 `csrf`（見 [security-model.md](security-model.md)）。前端預設採用 `cookie-csrf` 策略（`api/auth.ts` 的 `createCookieCsrfAuth()`）：

- **令牌只在 httpOnly Cookie**：script 讀不到 session 令牌；登入回應裡的 bearer 令牌直接丟棄，不保存、也不送回。所有請求都不帶 `Authorization`，由瀏覽器自動附上同源 Cookie（`credentials: "same-origin"`，不對跨來源帶 Cookie）。
- **CSRF**：`csrf` 值只存在策略的閉包變數中（不進 Pinia、localStorage、sessionStorage、IndexedDB 或 script 可寫的 Cookie），只在非安全方法（GET／HEAD／OPTIONS／TRACE 以外）加上 `X-Jelee-CSRF`。
- **重新整理後恢復**：第一次導覽前，路由守衛等待 `auth.restore()`：`GET /api/v1/users/me` 成功就以 `GET /api/v1/auth/csrf` 取回 CSRF 並恢復登入狀態，因此深層連結在重新整理後仍停在原頁；失敗則視為未登入。
- 收到 401 時清除 CSRF 與使用者狀態，導向 `/login?reason=expired&redirect=<原路徑>`。登出、撤銷目前裝置或「在所有裝置上登出」後，伺服器清除 Cookie，前端同時清空各 store 的使用者快取（`stores/userScoped.ts`）。
- ESLint 以 `no-restricted-globals`／`no-restricted-properties` 禁止 `localStorage`、`sessionStorage`、`document.cookie`、`eval`；測試以 spy 驗證登入過程沒有任何 storage 寫入、請求沒有 `Authorization`、只有寫入請求帶 CSRF。唯一例外是 `stores/persist.ts`（見「瀏覽器儲存例外」），它只存沒有伺服器 API 的呈現狀態，並拒絕任何名稱含 token、csrf、session、cookie、password、secret、credential、bearer、auth 的鍵。
- 圖片（`/images/Primary/{id}`）以 `<img>` 同源載入，由同一個 Cookie 授權；這也是改用 Cookie 的必要條件（bearer 無法加在 `<img>` 請求上）。

`AuthStrategy` 介面（`authorize`／`establish`／`resume`／`clear`／`hasCredential`、`survivesReload`）是唯一接觸憑證的地方。`createMemoryBearerAuth()` 保留給不接受環回明文 HTTP 上 `Secure` Cookie 的開發瀏覽器（例如部分 Safari）：令牌只在記憶體，重新整理即登出，且海報圖無法載入。

## 頁面（G34.3 第一批）

| 路由 | 頁面 | API |
| --- | --- | --- |
| `/login` | 登入；欄位驗證、錯誤碼對照訊息、登入後回 `redirect` | `POST /auth/login` |
| `/libraries` | 媒體庫卡片列表、游標分頁 | `GET /libraries` |
| `/libraries/:libraryId` | 條目海報牆／列表（`?view=list`）、伺服器排序（`?sort=newest`／`?sort=year`，皆可深層連結）、「已顯示 n／共 m 項」與載入更多、骨架屏、空狀態、錯誤態 | `GET /items?parentId=…&sort=…&order=…&offset=…&limit=60` |
| `/items/:itemId` | 條目詳情：標題、類型、年份、原始標題、標語、簡介、類型標籤、外部 ID、NFO 讀取狀態與來源標記；檔案資訊（每個版本的容器、時長、解析度、編碼、位元率、大小、內嵌與外掛字幕／音軌）；「請使用原生用戶端觀看」說明 | `GET /items/{id}/details`、`GET /items/{id}/sources` |
| `/account` | 個人資料、自己的工作階段列表、單一撤銷（樂觀更新可回滾）、在所有裝置上登出 | `GET /users/me`、`GET/DELETE /users/{id}/sessions[/{sessionID}]` |

頁面與 API 限制（對照表）：

| 原限制 | 狀態 | 現在的做法 |
| --- | --- | --- |
| `GET /api/v1/items` 只接受 `cursor`、`limit`，前端逐頁讀全域目錄再篩媒體庫 | 已解除 | 位移形式新增 `libraryId`、`parentId`、`type`、`sort`＋`order`、`q`、`offset` 並回 `total`（見 `docs/catalog-api.md`）。條目頁一次請求一頁（每頁 60），用 `parentId=<媒體庫>` 取頂層條目，排序由伺服器完成；舊游標形式行為不變 |
| 簡介、年份、外部 ID、NFO 來源只有管理員 API 提供 | 已解除 | `GET /api/v1/items/{id}/details` 對所有可見該條目的使用者提供；詳情頁不再呼叫管理員 `/metadata`，也不再顯示「目前僅管理員可見」 |
| 沒有網頁可用的檔案資訊 API | 已解除 | `GET /api/v1/items/{id}/sources` 任何工作階段可讀，不含路徑與任何直投網址；讀取失敗只隱藏檔案資訊區塊 |
| 網頁不得取得直投路徑 | 維持 | `WebPaths` 仍移除 `/stream` 與 `/api/v1/sources/…`；檔案資訊 schema（`MediaSourceInfo`）沒有 `url` 欄位；`check-no-playback` 仍掃描原始碼路徑字串 |

## 頁面（G34.3 第二批：搜尋、統計、設定、管理）

| 路由 | 頁面 | API |
| --- | --- | --- |
| 頁首搜尋框 | `role="search"`；`/` 鍵聚焦、Esc 清除；在搜尋頁輸入時去抖 300 ms 更新 `?q=`（`router.replace`，同路徑不搶焦點），其他頁按 Enter 才前往搜尋頁 | — |
| `/search?q=&type=` | 搜尋結果（伺服器比對、依存取權過濾與計數）、類型篩選、「顯示 n 項，共 m 項」（`role="status"`）、載入更多、未輸入提示、空結果、錯誤＋重試 | `GET /items?q=…&type=…&sort=name&offset=…&limit=40` |
| `/stats` | 我的觀看統計：總計（有效時長、次數、工作階段、完成率、首看／重看）、按日／週／月／年走勢（SVG 長條，另附隱藏資料表）、Top N（連到條目詳情）、按庫／按類型（CSS 長條表格）；清除自己的觀看紀錄（兩段確認） | `GET /users/me/watch-stats?period=&top=&from=&to=`、`DELETE /users/me/playback-history` |
| `/settings` | 介面語言（立即套用並存入帳號）、主題（跟隨系統／淺色／深色，存入帳號）、個人資料（顯示名稱、在公開清單隱藏）、變更密碼（12–1024 位元組檢查、確認欄；成功後所有工作階段失效、回登入頁） | `PUT /users/me/profile`、`GET/PUT /users/me/preferences`、`PUT /users/me/password` |
| `/admin/users` | 使用者列表（游標分頁、可含已刪除）、建立使用者（`Idempotency-Key`，重送同一筆沿用同一鍵） | `GET/POST /users` |
| `/admin/users/:userId` | 帳號設定（停用需兩段確認）、原生登入權限 allowNative（撤銷需確認）、投遞上限（留空＝跟隨伺服器、0＝不限）、解鎖、軟刪除／還原、工作階段與全部撤銷、媒體庫授權勾選、內容存取（分級上限、未分級三態、標籤封鎖、條目 hide/allow 規則：以搜尋挑條目），每區附變更影響說明 | `GET/PUT/DELETE /users/{id}`、`PUT /users/{id}/native`、`GET/PUT /users/{id}/delivery-limits`、`POST /users/{id}/unlock`、`POST /users/{id}/restore`、`GET/DELETE /users/{id}/sessions`、`GET /libraries`、`GET/PUT /users/{id}/libraries`、`GET/PUT /users/{id}/content-access`、`PUT/DELETE /users/{id}/content-access/items/{itemId}`、`GET /access/parental-ratings`、`GET /items?q=` |
| `/admin/access` | 全站內容存取政策（未分級預設、管理員是否受限；變更需確認並說明影響）、分級代碼對照表、規則優先順序說明 | `GET/PUT /access/policy`、`GET /access/parental-ratings` |
| `/admin/clients` | 未知客戶端預設策略（兩段確認，提示 `jelee-cli access reset-policies`）；規則列表與新增／編輯／刪除、觀察→攔截切換（兩段確認並顯示目前命中數）；已知客戶端（重新命名、可信、加入屏蔽、踢下線）；命中統計（Top UA／IP／規則）、命中明細與匯出（fetch 後存檔，錯誤顯示本地化訊息） | `/client-control/policy`、`/client-control/rules[/{id}[/enforce\|/observe]]`、`/client-control/clients[/{id}[/block\|/kick]]`、`/client-control/stats`、`/client-control/hits[/export]` |
| `/admin/webhooks` | 端點列表、建立（事件取自伺服器 `events` 目錄）、啟用／停用、刪除、送測試事件、輪替密鑰；密鑰只顯示一次（提示框＋複製，按「我已保存」後自記憶體清除） | `GET/POST /webhooks`、`PUT/DELETE /webhooks/{id}`、`POST /webhooks/{id}/test`、`POST /webhooks/{id}/rotate-secret` |
| `/admin/webhooks/:webhookId` | 設定編輯、投遞日誌（依狀態篩選、游標分頁）、單筆嘗試歷史、死信／已送達重放（兩段確認） | `GET/PUT /webhooks/{id}`、`GET /webhooks/{id}/deliveries[/{deliveryId}]`、`POST …/replay` |
| `/admin/stats` | 全站觀看統計（同上圖表＋最活躍使用者）、目前範圍的 CSV／NDJSON 匯出（以 API client 取得後存檔；伺服器檢查角色、寫稽核、超過列數回 `stats_export_limit`，顯示為本地化訊息而不會存成檔案） | `GET /watch-stats`、`GET /watch-stats/export` |

管理頁全部掛在 `/admin` 之下，`meta.admin` 由 vue-router 合併到每個子路由，守衛對非管理員一律導向 403；頁首的「管理」入口只對管理員渲染。伺服器對每個管理 API 仍會再檢查（G35.2）。危險操作一律用 `UiConfirmButton`（第一次按下只顯示提示與確認鈕並移交焦點，Esc／取消返回觸發鈕，第二次按下才送出）。

第二批的 API 缺口與限制：

| 項目 | 狀態 | 目前做法 |
| --- | --- | --- |
| 使用者偏好（主題等）沒有伺服器 API（G33.3） | 已解除 | 遷移 `000073_user_preferences` 與 `GET/PUT /users/me/preferences`（主題 system／light／dark，預留 `density`，遷移 074 加 `layout`；PUT 須帶齊全部欄位、不寫稽核、不進元資料備份）。`stores/preferences.ts` 在登入或恢復工作階段時載入並套用，切換即存（失敗時本分頁仍套用並顯示錯誤）；未登入只存分頁記憶體，登出後沿用目前主題。從未存過主題的使用者讀到全站預設主題；全站設定的匯入／匯出 JSON 與重置見下方 G32／G33 表 |
| 密碼錯誤時 `PUT /users/me/password` 回 `401 authentication_required` | 已解除 | 伺服器改回 `400 invalid_password`（工作階段有效、只是輸入值錯，與 `invalid_request` 同屬 400 輸入錯誤；不用 403 以免和權限、CSRF、客戶端管控的 403 混淆）；改密限速、429 與「不計入登入鎖定」照舊。`api/client.ts` 的路徑豁免已移除，任何 401 都視為工作階段失效 |
| 客戶端管控 `rules/{id}/enforce`、`/observe`、`clients/{id}/block`、`/kick` 要 `{}` 卻沒宣告 requestBody | 已解除 | OpenAPI 比照 logout 宣告 `Empty`；`features/clients/api.ts` 的 `emptyBody = {} as never` 已移除，直接傳 `{}`。契約測試要求每個 POST／PUT／PATCH 都宣告 requestBody（只豁免不讀正文的 setup back／complete） |
| `KnownClient` 看不出是否已被屏蔽 | 已解除 | 回應新增 `blocked` 與 `blockRuleId`：存在與「加入屏蔽」相同識別（裝置 ID，沒有時 UA；UA 被截斷時前綴）的啟用、全域、無時間窗 `deny` 規則即為已屏蔽；其他屬性（IP、正則、標頭）的拒絕規則不反映。面板顯示「已屏蔽」標記與解除說明，已屏蔽者不再提供「加入屏蔽」 |
| 統計匯出、命中匯出用 `<a download>`，409／401 會被存成檔案 | 已解除 | 改由前端以 API client 取得 blob 再存檔（`api/download.ts`）：錯誤走一般錯誤正規化顯示本地化訊息，401 走工作階段失效流程。未採預檢端點：預檢與下載之間列數可能改變，且要多跑一次計數查詢與權限檢查。代價是整份匯出先緩衝在瀏覽器記憶體（上限由 `stats.exportMaxRows` 與命中匯出 10,000 筆約束），且讀不到 `X-Jelee-Export-Complete` trailer |
| G48.7 授權矩陣（使用者 × 庫批量勾選）、模板、變更預覽（影響條目數／使用者數） | 缺 | 只有逐一使用者的 `PUT /users/{id}/libraries` 與 content-access；畫面以文字說明影響與「立即生效、寫入稽核」，沒有數字預覽，也沒有批量或模板 |
| 使用者群組（G48.1／G47.4） | 缺 | 只能逐一使用者設定 |
| 客戶端規則的 scope（使用者／客戶端類型）與生效時間窗 | 前端未做編輯 | 表單只建立全域、無時間窗規則；編輯既有規則時原樣保留其 scope 與 window |
| 觀看統計的期間範圍 | 限制 | 依分組固定為最近 30 天／12 週／12 個月／5 年（伺服器限制按日最多 400 天、其餘 3660 天）；自訂起訖日期尚未提供 |

共用元件（`components/ui/`）：`UiConfirmButton`（兩段確認）、`UiSelectField`、`UiCheckbox`、`UiBarChart`（CSS 長條，資料保留為表格）、`UiColumnChart`（SVG 走勢，SVG `aria-hidden`、另附隱藏資料表），不新增任何圖表套件；`UiButton`（primary／secondary／danger／ghost、`pressed` 切換、`busy`）、`UiTextField`（label／hint／error 以 `aria-describedby` 連結、`aria-invalid`）、`UiSkeleton`（`aria-hidden`、固定版面尺寸、reduced-motion 時停用動畫）、`UiEmptyState`、`UiErrorState`（錯誤碼對照訊息＋traceId＋重試）、`UiAlert`、`UiBadge`、`UiToastRegion`（`aria-live`，錯誤用 `role="alert"` 且不自動消失）、`RequestStatus`（可插入骨架屏）。可及性：skip link、導覽後焦點移到頁面 `h1`、`RouterLink` 的 `aria-current`、觸控目標 44px、`:focus-visible` 外框，亮／暗色全部取自 token。

## 插件體系（G32）

詳細的開發者文件見 [plugin-development.md](plugin-development.md)。

### 決策

- **SDK 位置**：`@jelee/plugin-sdk` 是樹內套件 `web/src/plugins/sdk/`，以 Vite alias 與 `tsconfig.app.json` 的 `paths` 解析成穩定的 import 名稱；不另建 npm workspace 成員，避免改動 lockfile 與安裝流程。它只依賴 `vue`（ESLint 禁止它 import 宿主），之後要獨立發佈時只需加上 `package.json`。版本為 `SDK_VERSION = 1.0.0`，語意化版本與棄用策略見開發文件。
- **Hook**：需求列出的九個 Hook 全部有型別（`HookMap`）。宿主已接上 `media.detail.tabs`、`metadata.panel`、`item.action`（條目詳情頁）、`settings.section`（設定頁）、`library.toolbar`（媒體庫頁）、`theme.token`（構造樣式表）、`route.register`（`/x/<插件 ID>/…`）；`command.palette` 與 `webhook.eventType` 可登記、會計數，但宿主尚無命令面板，Webhook 頁也還沒顯示插件標籤。
- **載入順序**：Manifest（JSON，小）隨主程式讀入並在載入程式碼前驗證；入口與元件全部是獨立 chunk，只有啟用且依賴滿足的插件會被 import，元件在首次顯示時才載入（`defineAsyncComponent`，15 秒逾時）。插件宿主與 CSS 清洗器本身也在 `plugins/index.ts` 以動態 import 啟動，不佔主 bundle。
- **隔離**：每個插件的每個區塊外包 `PluginBoundary.vue`（`onErrorCaptured` 回傳 `false`），元件 setup／render／watcher／事件處理與懶載入失敗都只把該區塊換成提示與「重試」；`setup()` 拋錯或登記不合法內容時整個插件標為失敗、已登記內容全部丟棄；操作與 token 函式拋錯另行攔截。失敗次數與最後錯誤顯示在管理頁。
- **受限客戶端**：插件拿到的 `api` 是凍結的閉包物件，只有四個固定的 GET 方法（條目詳情、檔案摘要、媒體庫、自己的帳號摘要），各自需要 Manifest 權限；沒有通用請求、沒有寫入，回傳凍結複本，錯誤只帶錯誤碼。API 用戶端、`AuthStrategy` 與 CSRF 值都不是插件可走訪物件圖上的任何屬性（測試以 `Reflect.ownKeys` 走訪整個上下文驗證），請求本身也不帶 CSRF 或 `Authorization`。
- **靜態約束**：`src/plugins/official/**` 的 ESLint 規則只允許 import `vue` 與 `@jelee/plugin-sdk`，禁止 `getCurrentInstance`／`inject`／`provide`、宿主模組、`vue-i18n`／`vue-router`／`pinia`／`openapi-fetch`，以及 `fetch`、`XMLHttpRequest`、`WebSocket`、`EventSource`、各種瀏覽器儲存、`document.cookie`、`navigator.sendBeacon`。插件訊息放在插件自己的四語目錄，由宿主的小型格式器處理，不併入 vue-i18n 目錄，所以插件不能覆蓋宿主字串。
- **禁播**：`route.register` 拒絕 `play`、`stream`、`watch`、`pip`、`cast`、`audio`、`video` 等路徑段；插件原始碼同樣經過 `check-no-playback`。

### 前端做得到與做不到的邊界

插件與網頁端同源、同 realm。上述措施保證「守規矩、經審查的插件」只能用到宣告的能力，並且讓意外的錯誤不會白屏；但前端無法阻止刻意惡意的同源程式碼：它可以直接 `fetch('/api/v1/auth/csrf')` 取得 CSRF 值，或經由 Vue 內部結構找到宿主物件。因此目前只支援與網頁端一起建置、審查的插件，不支援遠端或使用者上傳的插件。真正的隔離需要 iframe sandbox／獨立來源與 postMessage 協定，或伺服器端為插件簽發範圍受限的權杖，列為後續。伺服器對每個請求的授權（G35.2）不受插件影響。

## 外觀自訂：自訂 CSS（G33.4）與版面（G33.5）

### 自訂 CSS

- 管理頁 `/admin/appearance`：輸入 CSS、外部字型開關與主機白名單、「檢查」列出每一處被移除的內容與原因、「套用並儲存」、兩段確認的「清除」。
- 清洗器 `theme/customCss.ts` 先把輸入解析成規則與宣告，再只序列化通過檢查的部分，所以套用的文字一定是檢查過的文字：
  - **整段拒絕**：任何 `<`（不可能出現 `<script>` 或 `</style>`）、反斜線跳脫（可拼出被禁字詞）、控制字元與 U+2028/2029、未閉合的註解、大括號、括號或引號、超過 64 KiB。註解先換成空白，`expr/**/ession(` 無法重新拼回。
  - **逐條移除**：`@import`（任何形式）與 `@media`／`@supports`／`@container`／`@layer`／`@font-face`／`@keyframes` 以外的 at 規則；`expression(`；`javascript:`／`vbscript:`；`behavior`、`-moz-binding`；`image-set()`、`-webkit-image-set()`、`src()`、`element()`、`paint()` 與當作網址的 `attr()`；所有不是本站路徑（`/…`，不含 `//`）或 `#片段` 的 `url()`；巢狀規則；不合法的選擇器與宣告；超過三層的巢狀 at 規則。
  - **外部字型**：預設禁用；開啟後只有 `@font-face` 的 `src` 可以指向白名單主機（完全相符，最多 10 個）的 `https` 網址，不得帶帳密或連接埠。
- **套用方式**：以 `CSSStyleSheet.replaceSync` 建立構造樣式表並加入 `document.adoptedStyleSheets`（`theme/styleSheets.ts`）。CSSOM 不受 CSP `style-src` 管轄，所以在目前的 `style-src 'self'` 下不需要 `'unsafe-inline'`，也不產生任何 `<style>` 元素；伺服器的 `style-src` 因此維持 `'self'`，不放寬。不支援構造樣式表的瀏覽器不套用（行內 `<style>` 反正會被 CSP 擋），管理頁顯示提示。順序為：打包的樣式 → 插件 token（`plugin-tokens`）→ 全站 token 覆寫（`site-tokens`）→ 管理員 CSS（`custom-css`，最後套用、優先）。
- **伺服器端清洗**：`internal/domain/customcss.go` 是 `theme/customCss.ts` 的移植（JavaScript 字串語意：UTF-16 長度、ECMAScript 空白、`toLowerCase` 的 U+0130），兩邊的測試讀同一份 `web/src/theme/customCss.cases.json`（88 筆 CSS、23 筆 token、14 筆主機），結果必須逐筆相同。`PUT /api/v1/site/appearance` 與匯入遇到結構問題（`<`、反斜線、控制字元、未閉合結構、過長）整份拒絕為 `400 custom_css_rejected`；逐條移除的部分照存原文，管理員讀 `/site/appearance/config` 看到 `cssIssues`；其他使用者的 `GET /site/appearance` 只拿到**每次讀取時重新清洗**的 `css`，看不到原文。字型主機在伺服器也只接受純 DNS 名稱（最後一段不可全為數字，所以拒絕 IP），前端 `normalizeFontHost` 同步改為相同規則。
- **CSP font-src**：外部字型開啟且白名單非空時，伺服器在網頁端回應的 CSP 末尾加上 `font-src 'self' https://<主機>…`（主機再驗一次、最多 10 個），其他指令逐字不變；未設定時維持原 CSP。結果在每個實例快取 30 秒，本實例寫入後立即失效，讀不到設定時沿用上次的值（從未讀到則不加 font-src）。API 回應的 CSP 不受影響。
- 存放：已登入時以伺服器全站設定為準（`/api/v1/site/appearance`，對所有使用者與裝置生效）；未登入或讀不到伺服器時，沿用管理員先前存在本瀏覽器 `localStorage` 的副本，每次載入都重新清洗。

### 版面

- 首頁 `/`（原本直接轉到媒體庫）改為區塊頁：歡迎與快速連結、媒體庫、最新首映（`GET /api/v1/items?sort=premiereDate&order=desc&limit=12`）。隱藏的區塊不渲染也不發請求。頁內「自訂首頁」可直接排序與顯隱。
- 條目詳情頁的面板（簡介、類型、外部 ID、NFO 來源、檔案資訊、插件面板、插件頁籤）可排序與顯隱。
- 版面預設：內建「標準」「精簡」「元資料檢查」三套，可把目前版面存成最多 10 個自訂預設、切換、刪除與恢復預設；設定頁「版面」區塊集中管理。
- 排序元件 `components/ui/UiReorderList.vue`：每列有「上移／下移」按鈕（`aria-label` 含項目名稱、`aria-keyshortcuts`），焦點在列內任一控制項時 Alt+↑／Alt+↓ 也能移動；移動後焦點留在被移動的列（到頂或到底時移到另一個仍可用的按鈕），`role="status"` 朗讀新位置；滑鼠拖曳只是額外方式。插件管理頁的排序也用同一元件。
- 存放：已登入時版面與自訂預設存在伺服器偏好的 `layout`（`{current, presets}`，16 KiB、每區最多 32 個區塊、最多 10 個預設；PUT 與主題一起帶齊全部欄位），跟著帳號到其他裝置；尚未自訂（`layout: null`）時套用管理員的全站預設版面，再沒有就用內建「標準」。每次變更也寫入本瀏覽器的按帳號副本，未登入或讀不到伺服器時用它；讀回時修復未知或缺少的區塊。「恢復預設版面」恢復全站預設版面。

### 瀏覽器儲存例外

`stores/persist.ts` 是唯一碰 `localStorage` 的模組（ESLint 只對它解除限制），鍵一律以 `jelee.ui.v1.` 開頭，存放呈現狀態的瀏覽器副本：插件啟用與順序（`plugin-host.state`）、插件設定（`plugin-settings.<插件 ID>`）、管理員 CSS（`admin-css.custom`）、版面（`page-layout.<使用者 ID>`）。伺服器已有對應 API（G32／G33 表），這些副本只在未登入、讀不到伺服器，或一般使用者自己覆蓋插件設定時生效。固定鍵名不得含憑證相關字詞、範圍部分只接受 ID 字元，值為 JSON 且有大小上限；讀回的內容一律當成不可信輸入處理。憑證、CSRF、工作階段資料仍然只在記憶體或 HttpOnly Cookie（G35.1 不變）。只在使用者實際變更時才寫入，登入流程依舊沒有任何 storage 寫入。

## Bundle 預算（G35.4）

- `web/scripts/check-bundle-budget.mjs` 讀 `dist/index.html`，量測首屏必須下載的檔案（module 入口、它的 `modulepreload` 與 stylesheet），以 gzip level 9 計算：`entryJsGzipBytes`（入口）、`initialJsGzipBytes`（入口加預載）、`initialCssGzipBytes`。懶載入 chunk 不計入。
- 預算檔 `web/bundle-budget.json` 進版控，任一項超過即失敗並列出超出位元組數；放寬預算是對該檔的審查變更，理由寫在提交訊息。
- `npm run build`（即 `make web-build`）在 Vite 建置與禁播掃描後執行此門禁；`make web-budget` 可對現有 `dist` 重跑。CI 的 Linux「Web frontend gates」步驟執行 `make … web-build web-budget`。
- 2026-10-04 量測值：入口 121,154 B、首屏 JS 145,313 B、首屏 CSS 2,313 B；預算分別為 127,000、152,000、2,450 B（約多 5%）。插件工作前的首屏 JS 約 142,900 B：插件宿主、SDK 與 CSS 清洗器都在懶載入 chunk，首屏只多了首頁路由與動態啟動碼。
- 「首屏可交互 P95 ≤1.5s」需要真實瀏覽器與網路條件量測，尚未做。

## G32／G33.4／G33.5 的後端缺口

前端插件體系與外觀自訂完成時記錄的後端缺口，遷移 `000075_site_settings` 起逐項處理如下：

| 缺口 | 狀態 | 現在的做法 |
| --- | --- | --- |
| 沒有插件設定 API（全域啟用清單、順序、插件設定） | 已解除 | `site_plugins` 單列表與 `GET /api/v1/site/plugins`（任何已登入使用者：清單、順序、未停用插件的設定）、`GET /site/plugins/config`（管理員，含 `revision`）、`PUT /site/plugins`（管理員，帶齊欄位與 `revision`，過期為 `409 conflict`）、`POST /site/plugins/reset`。設定依插件 ID 分命名空間（最多 64 個插件、每個 64 鍵 16 KiB、巢狀 8 層），寫入稽核 `site.plugins_changed`（只記清單與每個命名空間的摘要，不抄設定內容）。`plugins/host/store.ts` 已登入時以伺服器為準，管理員的變更依序寫回並帶最新版本號，失敗時提示並重新載入；未登入或讀不到時沿用本瀏覽器副本 |
| 沒有全域外觀設定 API | 已解除 | `site_appearance` 單列表與 `GET /api/v1/site/appearance`（任何已登入使用者：預設主題、token 覆寫、清洗後的 CSS、生效的字型主機、預設版面，不含原文與版本）、`GET /site/appearance/config`（管理員：原文、`cssIssues`、`revision`）、`PUT /site/appearance`、`POST /site/appearance/reset`。伺服器端清洗與前端同級（共用案例檔，見上方「伺服器端清洗」）；token 只接受 SDK 的 14 個名稱與安全值；寫入稽核 `site.appearance_changed`（安全類，CSS 以位元組數與 SHA-256 記錄）。匯出／匯入：`GET /api/v1/site/export`、`POST /api/v1/site/import`（`format: jelee.site-settings`、`version: 1`，外觀與插件同一交易取代，不檢查版本號）；管理頁 `/admin/appearance` 新增全站預設（主題、token、版面）、設定檔匯出／匯入與「恢復預設外觀」 |
| `frontendCSP` 沒有 `font-src` 白名單 | 已解除 | 見上方「CSP font-src」：只在開啟外部字型時加入 `font-src 'self' https://<主機>…`，其他指令不變；`style-src 'self'` 不放寬 |
| `UserPreferences` 沒有版面欄位 | 已解除 | 偏好加 `layout`（可為 `null`），PUT 仍須帶齊全部欄位；見上方「版面」的存放說明 |
| 伺服器不回報版本 | 已解除 | `GET /api/v1/system` 回傳 `version`（`internal/platform/buildinfo`：發行建置以 `-ldflags -X …buildinfo.version=` 或 Dockerfile `--build-arg JELEE_VERSION` 指定，未指定或格式不對時為與 `web/package.json` 相同的預設版本，測試確保兩者一致），與 OpenAPI `info.version` 相同；插件宿主啟動前讀取並用它比較 `minJeleeVersion`，讀不到時才用網頁端版本 |
| 沒有插件專屬的伺服器授權範圍 | 缺 | 插件的請求等同使用者本人的請求；受限客戶端只開放固定唯讀端點。若要支援第三方插件：插件權杖或 iframe 隔離加伺服器端範圍 |
| 未登入頁面（登入頁）不套用全站外觀 | 限制 | 全站外觀與插件設定的讀取都需要工作階段，登入頁只用打包樣式與本瀏覽器副本 |

## CSP 規劃（G35.1）

目標標頭（由伺服器提供網頁資產時設定，屬後續伺服器整合工作）：

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:;
connect-src 'self'; object-src 'none'; media-src 'none'; frame-src 'none';
base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

伺服器目前實際送出的是 `internal/adapter/http/webapp.go` 的 `frontendCSP`；全站外觀開啟外部字型時再於末尾加上 `font-src 'self' https://<白名單主機>…`（其他指令不變，測試逐條比對）。

前端為此做的配合：`index.html` 無行內腳本與樣式；管理員 CSS、全站 token 覆寫與插件 token 以構造樣式表（CSSOM）套用，不需要放寬 `style-src`；Vite 關閉 modulepreload polyfill（避免行內腳本）、`assetsInlineLimit: 0`、不輸出 sourcemap；Vue 使用 runtime-only 建置（SFC 預先編譯，不需 `unsafe-eval`）；vue-i18n 11 預設以 JIT/AST 解譯訊息而非 `new Function`；ESLint 禁止 `eval` 與 `new Function`。`media-src 'none'` 同時是禁播的瀏覽器層防線。`img-src` 未來配合 G40 影像服務再調整。開發伺服器（`npm run dev`）把 `/api` 代理到 `JELEE_DEV_API`（預設 `http://127.0.0.1:8097`）。

## 禁播策略（G27、G35.5）

1. **不提供入口**：路由表、頁面與翻譯中沒有任何播放、播放器、串流、投放或子母畫面路由／按鈕／字串。
2. **型別層**：`WebPaths` 移除所有 `/stream` 與 `/api/v1/sources/…` 路徑；網頁可讀的檔案資訊（`GET /api/v1/items/{id}/sources`）的 schema 本身不含直投網址。
3. **伺服器層**：web session 呼叫直投端點會得到 `403 web_playback_disabled`（既有實作）。
4. **建置門禁** `web/scripts/check-no-playback.mjs`（接在 `web-lint`，`web-build` 以 `--require-dist` 再掃一次產物）：
   - 根目錄與 `web/` 的 `package.json` 各依賴欄位、`package-lock.json` 任一層的套件，不得出現 hls.js、dashjs、shaka-player、video.js、plyr、mpegts.js、flv.js、media-chrome、vidstack、artplayer、xgplayer、dplayer、clappr 等播放器，以及 `@videojs/`、`videojs-`、`@vidstack/`、`@mux/` 等前綴。
   - `web/src`、`index.html` 與 `web/dist` 不得出現 `<video`、`<audio`、`MediaSource`、`HTMLMediaElement` 類型、`createElement("video")`、`requestPictureInPicture`、`mediaSession`／`MediaMetadata`、`RemotePlayback`／`PresentationRequest`、EME。
   - 原始碼中的路徑字串不得含 `play`、`player`、`playback`、`stream`、`cast`、`pip` 等區段（產生的 `schema.d.ts` 只豁免此項）；翻譯鍵不得含播放字彙。
   - 測試檔（`*.test.ts`、`src/test/`）不掃描，因為它們需要寫出這些字樣來斷言不存在；實際出貨的內容由 dist 掃描涵蓋。
5. **測試**：整合測試在登入後的頁面斷言沒有 `<video>`／`<audio>`、沒有播放相關路由；Playwright 端對端測試在真實瀏覽器中對 16 個關鍵頁面（亮／暗）斷言 DOM（含開放的 shadow root）沒有 `<video>`／`<audio>`、沒有指向播放的連結，且每個測試全程沒有對播放、串流、字幕／音軌遞送路徑或媒體資源的請求（見下節）。尚未做的是「全點擊遍歷」：目前只走固定流程與頁面載入，不自動點遍每個控制項。

## 端對端與視覺回歸（G27.4、G34.5、G34.6）

工具與來源紀律見 [工具鏈](toolchain.md#playwrightg274g345g346)：Playwright 1.63.0 與清單固定的 Chrome Headless Shell，經 `web/scripts/run-playwright.mjs` 執行，瀏覽器只放在 `.tools/playwright`。

**測試對象**：`vite build` 的產物由 `vite preview` 提供，不用開發伺服器，測到的就是出貨的程式碼。後端以 Playwright 的請求攔截回答（`web/e2e/fixtures/api.ts`），假資料在 `fixtures/data.ts`，型別取自產生的 `src/api/schema.d.ts`，契約改變時 `web-types` 會先失敗。假伺服器沒有回答的 API 請求會被記錄並讓測試失敗，所以頁面開始呼叫新端點時不會默默拍到錯誤畫面；預覽伺服器的 `/api` 代理指向不可連的埠，不會碰到本機正在跑的 Jelee。封面圖由攔截產生固定的 SVG。

**頁面清單**（`fixtures/pages.ts`，視覺與端對端共用）：登入、初始引導（權杖步驟）、媒體庫列表、條目列表（海報牆與列表兩種）、條目詳情、搜尋、我的觀看統計、設定、管理頁（使用者、內容存取、客戶端管控、Webhook、外觀〔自訂 CSS〕、插件）、開發者模式橫幅。

**穩定性**：固定瀏覽器時間（`page.clock.setFixedTime`，與假資料同一時刻）、`timezoneId: UTC`、`locale: en-US`（假資料只用 ASCII，畫面不依賴 CJK 字型）、`reducedMotion: reduce` 並以注入樣式關閉所有轉場、動畫與游標閃爍；字型以注入樣式固定為 DejaVu Sans（`system-ui` 在不同發行版解析不同，DejaVu Sans 隨 fontconfig 出現在所有 Debian／Ubuntu 映像，CI 步驟先確認它存在）。專案不內附字型檔，以免新增需要授權登記的資產。`deviceScaleFactor: 1`、只截可視區域（桌面 1280×800、手機 390×844），基線維持在合理大小。

**視覺回歸**：每頁 × 亮／暗 × 桌面／手機，共 64 張基線，存於 `web/e2e/__screenshots__/<desktop|mobile>/`。比對容忍度：逐像素顏色門檻 0.2（吸收反鋸齒），超過門檻的像素最多 10 個。2026-10-04 實測：只把 `--jl-radius-md` 從 8px 改成 0，每張圖就有 18 到 370 個像素不同，64 張全部失敗；未改動時連續三次全數通過。

**變更需人工確認**：設定檔 `updateSnapshots: "none"`，`make web-visual` 與 CI 只比對、從不寫入，缺少基線也算失敗。更新基線只能明確執行 `make web-visual-update`（`--update-snapshots=changed`，只改寫有差異或缺少的圖），由人看過新圖後連同造成變化的程式一起提交；審查者在 PR 的圖片差異中再確認一次。CI 失敗時上傳實際圖、基線與差異圖（`.testdata/playwright`）供比對。基線只在 Linux 產生；若 CI 主機的渲染與本機不同，應從 CI 構件取得實際圖、人工確認後提交，而不是放寬容忍度。

**2026-10-05 基線改以 CI 為準**：第一次 CI 實跑時 64 張全部有約 1% 像素差異（327–1808 像素），逐張看過差異圖，只集中在三處：原生 `<select>`（語言選單）與 `<input>` 佔位文字的字型、以及斜體文字（CI 的 ubuntu-latest 沒有 DejaVu 真斜體，瀏覽器以合成斜體繪製）；版面、顏色、內容完全相同。因此改以 CI 實際圖（run 37230864409 的 `web-e2e` 構件）為基線，容忍度不變。結果：**基線的權威環境是 CI 的 ubuntu-latest**；本機（例如 WSL Debian）字型不同時，本機 `make web-visual` 會出現同樣的小差異，不代表回歸，也**不得**用本機 `web-visual-update` 的結果覆蓋基線——要更新基線，請從 CI 失敗時上傳的構件取實際圖，人工確認後提交。

**端對端**（`e2e/flows.spec.ts`）：
- 鍵盤主流程：登入（Tab 到欄位、輸入、Enter）→ 媒體庫 → 條目列表 → 條目詳情 → 以麵包屑返回 → 瀏覽器返回；檢查焦點指示可見、導覽後焦點移到 `h1`。跳到主內容連結。
- 兩段確認：刪除 Webhook 第一次按只出現提示並把焦點移到確認鈕，Escape 與「取消」都退回並把焦點還給觸發鈕，期間沒有任何 DELETE；第二次確認才送出請求。
- 每個關鍵頁面（亮／暗）：無 `<video>`／`<audio>`、無播放連結；不依賴 axe 的基本可及性掃描（`html[lang]`、主內容恰一個 `h1`、表單控制項有標籤、按鈕與連結有名稱、圖片有 `alt`、無重複 `id`）；文字對比度依 WCAG 2.1 AA（一般文字 4.5:1、大字 3:1，背景沿祖先疊色計算，略過圖片／漸層背景與停用元件）。
- axe-core 沒有加入：它是新的 npm 相依，需另行核准並登記；上面的掃描涵蓋其中最常見的幾項規則，但不能取代 axe（例如 ARIA 屬性合法性、地標結構）。

**已知缺口（測試標為預期失敗）**：`App.vue` 在導覽後一個 tick 把焦點移到 `#main h1`，但條目詳情的標題要等資料載入後才出現，所以從條目列表進入詳情時焦點留在文件上。`known gaps` 測試以 `test.fail()` 記錄這點，修正後 Playwright 會回報意外通過，屆時移除標記。

## 後續

- G33／G34：主題預設、更多 UI 元件、響應式斷點、axe 掃描（需核准新相依）、條目詳情載入後的焦點；視覺基線的人工確認紀錄（D14；基線已改為 CI 實際圖，擁有者仍需看過）。
- G27.4：E2E 全點擊遍歷。
- G35.4：預算門禁已上線；評估 vue-i18n 預編譯訊息以縮小首屏 JS；首屏可交互 P95 需要真實瀏覽器量測。
- G32／G33：上表的後端缺口；命令面板 UI 與 Webhook 頁使用插件標籤；第三方插件的真正隔離（iframe／獨立來源）。
- 伺服器為 `GET /api/v1/items` 加上媒體庫篩選與排序後，移除前端逐頁篩選；網頁可用的檔案資訊 API。
- 第二批管理頁的 API 缺口（授權矩陣與變更預覽、群組）見上方表格；使用者偏好、密碼錯誤碼、客戶端管控請求正文、已屏蔽狀態與匯出下載已解除。
- Windows 的 PowerShell 引導與 `make.ps1` 尚未安裝 Node（清單已有 Windows 雜湊）。
