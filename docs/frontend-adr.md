# 前端架構決策紀錄（G31／G27／G35）

狀態：已採用（第 15 階段 15.1、15.3、15.4）。日期：2026-10-04。

## 決策

網頁端採用 Vue 3.5 + Vite 8 + TypeScript 5.9（strict）+ Pinia 3 + vue-router 4 + vue-i18n 11 + openapi-fetch；型別由 openapi-typescript 從 `api/openapi.json` 產生。開發工具為 vue-tsc、ESLint 10（typescript-eslint strict type-checked、eslint-plugin-vue、@intlify/eslint-plugin-vue-i18n）與 Vitest 5（jsdom）。不使用 UI 元件庫，元件放在 `web/src/components/ui/`，樣式全部取自設計 token。

### 為何選 Vue 3 而非 Svelte

- **生態與需求對位**：G31.1 要求的狀態管理（Pinia）、路由守衛與懶載入（vue-router）、四語 i18n（vue-i18n，含 ESLint 外掛可檢查硬編碼文字與缺鍵）都是 Vue 官方或核心團隊維護的套件，版本節奏一致；Svelte 需要自行拼湊等價方案。
- **外掛體系（G32）**：Vue 的 `app.use`、`provide/inject`、`defineAsyncComponent` 與 `onErrorCaptured` 直接對應「懶載入插件元件、錯誤邊界降級、插件拿不到令牌」的要求，不必自建執行期。
- **型別檢查**：`vue-tsc --noEmit` 對 SFC 做完整型別檢查，可以作為 G31 驗收的 `tsc --noEmit` 門禁；泛型元件（`<script setup generic>`）讓請求狀態元件保有資料型別。
- **安全預設**：模板一律轉義，唯一的原始 HTML 出口 `v-html` 可用 lint 全面禁止（`vue/no-v-html: error`），符合 G35.1。
- **體積**：Vue runtime 與 Svelte 編譯產物在本專案規模差異不大；第一批頁面後主 chunk gzip 約 37 KB（含四語訊息目錄），vue-i18n chunk 約 41 KB（尚未設 bundle 預算，見「後續」）。

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
    plugins/            Vue app 外掛的組合根；之後 G32 插件 SDK 的宿主也在這裡註冊
    i18n/<locale>/*.json 四語訊息；locales.ts 為語系協商
    router/             路由表（全部懶載入）、守衛、redirect 驗證
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
- 每個檔案 `web/src/i18n/<locale>/<namespace>.json` 只有一個與檔名相同的頂層鍵。**`core.json` 保留**給即將由伺服器端 UI 資源移入的字串：允許平面結構（載入時包在 `core` 命名空間下），並暫時豁免「未使用鍵」檢查。
- `check-i18n.mjs`：四語目錄完全一致、命名空間檔案一致、嚴格 JSON（拒絕重複鍵）、非空字串、缺鍵／多餘鍵、`{name}`／`{0}`／`@:key` 佔位符一致、簡繁混用偵測（語言自稱 `common.localeNames.*` 除外）、原始碼引用的鍵必須存在、目錄中的鍵必須被使用。ESLint 另以 `@intlify/vue-i18n/no-raw-text` 禁止模板硬編碼文字，`no-missing-keys` 檢查模板引用的鍵。

## 驗證與令牌（G35.1）

伺服器的 `POST /api/v1/auth/login` 對 web session 設定 `__Host-jelee_session` Cookie（HttpOnly、Secure、SameSite=Strict），回應正文另含一次性 bearer 令牌與 `csrf`（見 [security-model.md](security-model.md)）。前端預設採用 `cookie-csrf` 策略（`api/auth.ts` 的 `createCookieCsrfAuth()`）：

- **令牌只在 httpOnly Cookie**：script 讀不到 session 令牌；登入回應裡的 bearer 令牌直接丟棄，不保存、也不送回。所有請求都不帶 `Authorization`，由瀏覽器自動附上同源 Cookie（`credentials: "same-origin"`，不對跨來源帶 Cookie）。
- **CSRF**：`csrf` 值只存在策略的閉包變數中（不進 Pinia、localStorage、sessionStorage、IndexedDB 或 script 可寫的 Cookie），只在非安全方法（GET／HEAD／OPTIONS／TRACE 以外）加上 `X-Jelee-CSRF`。
- **重新整理後恢復**：第一次導覽前，路由守衛等待 `auth.restore()`：`GET /api/v1/users/me` 成功就以 `GET /api/v1/auth/csrf` 取回 CSRF 並恢復登入狀態，因此深層連結在重新整理後仍停在原頁；失敗則視為未登入。
- 收到 401 時清除 CSRF 與使用者狀態，導向 `/login?reason=expired&redirect=<原路徑>`。登出、撤銷目前裝置或「在所有裝置上登出」後，伺服器清除 Cookie，前端同時清空各 store 的使用者快取（`stores/userScoped.ts`）。
- ESLint 以 `no-restricted-globals`／`no-restricted-properties` 禁止 `localStorage`、`sessionStorage`、`document.cookie`、`eval`；測試以 spy 驗證登入過程沒有任何 storage 寫入、請求沒有 `Authorization`、只有寫入請求帶 CSRF。
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
| 使用者偏好（主題等）沒有伺服器 API（G33.3） | 已解除 | 遷移 `000073_user_preferences` 與 `GET/PUT /users/me/preferences`（主題 system／light／dark，預留 `density`；PUT 須帶齊全部欄位、不寫稽核、不進元資料備份）。`stores/preferences.ts` 在登入或恢復工作階段時載入並套用，切換即存（失敗時本分頁仍套用並顯示錯誤）；未登入只存分頁記憶體，登出後沿用目前主題。全域級主題、匯入／匯出 JSON 仍缺 |
| 密碼錯誤時 `PUT /users/me/password` 回 `401 authentication_required` | 已解除 | 伺服器改回 `400 invalid_password`（工作階段有效、只是輸入值錯，與 `invalid_request` 同屬 400 輸入錯誤；不用 403 以免和權限、CSRF、客戶端管控的 403 混淆）；改密限速、429 與「不計入登入鎖定」照舊。`api/client.ts` 的路徑豁免已移除，任何 401 都視為工作階段失效 |
| 客戶端管控 `rules/{id}/enforce`、`/observe`、`clients/{id}/block`、`/kick` 要 `{}` 卻沒宣告 requestBody | 已解除 | OpenAPI 比照 logout 宣告 `Empty`；`features/clients/api.ts` 的 `emptyBody = {} as never` 已移除，直接傳 `{}`。契約測試要求每個 POST／PUT／PATCH 都宣告 requestBody（只豁免不讀正文的 setup back／complete） |
| `KnownClient` 看不出是否已被屏蔽 | 已解除 | 回應新增 `blocked` 與 `blockRuleId`：存在與「加入屏蔽」相同識別（裝置 ID，沒有時 UA；UA 被截斷時前綴）的啟用、全域、無時間窗 `deny` 規則即為已屏蔽；其他屬性（IP、正則、標頭）的拒絕規則不反映。面板顯示「已屏蔽」標記與解除說明，已屏蔽者不再提供「加入屏蔽」 |
| 統計匯出、命中匯出用 `<a download>`，409／401 會被存成檔案 | 已解除 | 改由前端以 API client 取得 blob 再存檔（`api/download.ts`）：錯誤走一般錯誤正規化顯示本地化訊息，401 走工作階段失效流程。未採預檢端點：預檢與下載之間列數可能改變，且要多跑一次計數查詢與權限檢查。代價是整份匯出先緩衝在瀏覽器記憶體（上限由 `stats.exportMaxRows` 與命中匯出 10,000 筆約束），且讀不到 `X-Jelee-Export-Complete` trailer |
| G48.7 授權矩陣（使用者 × 庫批量勾選）、模板、變更預覽（影響條目數／使用者數） | 缺 | 只有逐一使用者的 `PUT /users/{id}/libraries` 與 content-access；畫面以文字說明影響與「立即生效、寫入稽核」，沒有數字預覽，也沒有批量或模板 |
| 使用者群組（G48.1／G47.4） | 缺 | 只能逐一使用者設定 |
| 客戶端規則的 scope（使用者／客戶端類型）與生效時間窗 | 前端未做編輯 | 表單只建立全域、無時間窗規則；編輯既有規則時原樣保留其 scope 與 window |
| 觀看統計的期間範圍 | 限制 | 依分組固定為最近 30 天／12 週／12 個月／5 年（伺服器限制按日最多 400 天、其餘 3660 天）；自訂起訖日期尚未提供 |

共用元件（`components/ui/`）：`UiConfirmButton`（兩段確認）、`UiSelectField`、`UiCheckbox`、`UiBarChart`（CSS 長條，資料保留為表格）、`UiColumnChart`（SVG 走勢，SVG `aria-hidden`、另附隱藏資料表），不新增任何圖表套件；`UiButton`（primary／secondary／danger／ghost、`pressed` 切換、`busy`）、`UiTextField`（label／hint／error 以 `aria-describedby` 連結、`aria-invalid`）、`UiSkeleton`（`aria-hidden`、固定版面尺寸、reduced-motion 時停用動畫）、`UiEmptyState`、`UiErrorState`（錯誤碼對照訊息＋traceId＋重試）、`UiAlert`、`UiBadge`、`UiToastRegion`（`aria-live`，錯誤用 `role="alert"` 且不自動消失）、`RequestStatus`（可插入骨架屏）。可及性：skip link、導覽後焦點移到頁面 `h1`、`RouterLink` 的 `aria-current`、觸控目標 44px、`:focus-visible` 外框，亮／暗色全部取自 token。

## CSP 規劃（G35.1）

目標標頭（由伺服器提供網頁資產時設定，屬後續伺服器整合工作）：

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:;
connect-src 'self'; object-src 'none'; media-src 'none'; frame-src 'none';
base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

前端為此做的配合：`index.html` 無行內腳本與樣式；Vite 關閉 modulepreload polyfill（避免行內腳本）、`assetsInlineLimit: 0`、不輸出 sourcemap；Vue 使用 runtime-only 建置（SFC 預先編譯，不需 `unsafe-eval`）；vue-i18n 11 預設以 JIT/AST 解譯訊息而非 `new Function`；ESLint 禁止 `eval` 與 `new Function`。`media-src 'none'` 同時是禁播的瀏覽器層防線。`img-src` 未來配合 G40 影像服務再調整。開發伺服器（`npm run dev`）把 `/api` 代理到 `JELEE_DEV_API`（預設 `http://127.0.0.1:8097`）。

## 禁播策略（G27、G35.5）

1. **不提供入口**：路由表、頁面與翻譯中沒有任何播放、播放器、串流、投放或子母畫面路由／按鈕／字串。
2. **型別層**：`WebPaths` 移除所有 `/stream` 與 `/api/v1/sources/…` 路徑；網頁可讀的檔案資訊（`GET /api/v1/items/{id}/sources`）的 schema 本身不含直投網址。
3. **伺服器層**：web session 呼叫直投端點會得到 `403 web_playback_disabled`（既有實作）。
4. **建置門禁** `web/scripts/check-no-playback.mjs`（接在 `web-lint`，`web-build` 以 `--require-dist` 再掃一次產物）：
   - 根目錄與 `web/` 的 `package.json` 各依賴欄位、`package-lock.json` 任一層的套件，不得出現 hls.js、dashjs、shaka-player、video.js、plyr、mpegts.js、flv.js、media-chrome、vidstack、artplayer、xgplayer、dplayer、clappr 等播放器，以及 `@videojs/`、`videojs-`、`@vidstack/`、`@mux/` 等前綴。
   - `web/src`、`index.html` 與 `web/dist` 不得出現 `<video`、`<audio`、`MediaSource`、`HTMLMediaElement` 類型、`createElement("video")`、`requestPictureInPicture`、`mediaSession`／`MediaMetadata`、`RemotePlayback`／`PresentationRequest`、EME。
   - 原始碼中的路徑字串不得含 `play`、`player`、`playback`、`stream`、`cast`、`pip` 等區段（產生的 `schema.d.ts` 只豁免此項）；翻譯鍵不得含播放字彙。
   - 測試檔（`*.test.ts`、`src/test/`）不掃描，因為它們需要寫出這些字樣來斷言不存在；實際出貨的內容由 dist 掃描涵蓋。
5. **測試**：整合測試在登入後的頁面斷言沒有 `<video>`／`<audio>`、沒有播放相關路由；E2E 全點擊遍歷待 Playwright 啟用後補上（G27.4）。

## 後續

- G33／G34：主題預設、更多 UI 元件、響應式與 axe 檢查、Playwright 視覺回歸（manifest 已預留 Playwright 1.63.0）。
- G35.4：主 bundle gzip 預算門禁；評估 vue-i18n 預編譯訊息以縮小 chunk。
- 伺服器為 `GET /api/v1/items` 加上媒體庫篩選與排序後，移除前端逐頁篩選；網頁可用的檔案資訊 API。
- 第二批管理頁的 API 缺口（授權矩陣與變更預覽、群組）見上方表格；使用者偏好、密碼錯誤碼、客戶端管控請求正文、已屏蔽狀態與匯出下載已解除。
- Windows 的 PowerShell 引導與 `make.ps1` 尚未安裝 Node（清單已有 Windows 雜湊）。
