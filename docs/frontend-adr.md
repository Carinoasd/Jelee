# 前端架構決策紀錄（G31／G27／G35）

狀態：已採用（第 15 階段 15.1、15.3、15.4）。日期：2026-10-04。

## 決策

網頁端採用 Vue 3.5 + Vite 8 + TypeScript 5.9（strict）+ Pinia 3 + vue-router 4 + vue-i18n 11 + openapi-fetch；型別由 openapi-typescript 從 `api/openapi.json` 產生。開發工具為 vue-tsc、ESLint 10（typescript-eslint strict type-checked、eslint-plugin-vue、@intlify/eslint-plugin-vue-i18n）與 Vitest 5（jsdom）。不使用 UI 元件庫，元件放在 `web/src/components/ui/`，樣式全部取自設計 token。

### 為何選 Vue 3 而非 Svelte

- **生態與需求對位**：G31.1 要求的狀態管理（Pinia）、路由守衛與懶載入（vue-router）、四語 i18n（vue-i18n，含 ESLint 外掛可檢查硬編碼文字與缺鍵）都是 Vue 官方或核心團隊維護的套件，版本節奏一致；Svelte 需要自行拼湊等價方案。
- **外掛體系（G32）**：Vue 的 `app.use`、`provide/inject`、`defineAsyncComponent` 與 `onErrorCaptured` 直接對應「懶載入插件元件、錯誤邊界降級、插件拿不到令牌」的要求，不必自建執行期。
- **型別檢查**：`vue-tsc --noEmit` 對 SFC 做完整型別檢查，可以作為 G31 驗收的 `tsc --noEmit` 門禁；泛型元件（`<script setup generic>`）讓請求狀態元件保有資料型別。
- **安全預設**：模板一律轉義，唯一的原始 HTML 出口 `v-html` 可用 lint 全面禁止（`vue/no-v-html: error`），符合 G35.1。
- **體積**：Vue runtime 與 Svelte 編譯產物在本專案規模差異不大；目前首頁主 chunk gzip 約 25 KB，vue-i18n chunk 約 41 KB（尚未設 bundle 預算，見「後續」）。

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
    features/<domain>/  各領域的 API 呼叫與頁面（auth、libraries、errors）
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

目前伺服器的 `POST /api/v1/auth/login` 回傳一次性的 opaque bearer 令牌，並把該 session 記為 `web` 類型（伺服器據此拒絕網頁端的直投播放，G27.3）。前端的處理：

- 令牌只存在 `api/auth.ts` 的 `createMemoryBearerAuth()` 閉包變數中，由 openapi-fetch middleware 加上 `Authorization` 標頭。不寫入 localStorage、sessionStorage、IndexedDB 或 script 可讀的 Cookie，也不放進 Pinia（devtools 看不到）。ESLint 以 `no-restricted-globals`／`no-restricted-properties` 禁止 `localStorage`、`sessionStorage`、`document.cookie`、`eval`，測試以 spy 驗證登入過程沒有任何 storage 寫入。
- 代價：重新整理頁面即登出（登入頁有說明）。
- 收到 401 時清除令牌與使用者狀態，導向 `/login?reason=expired`。
- 用戶端預設 `credentials: "same-origin"`，不對跨來源帶 Cookie。

**可替換設計**：`AuthStrategy` 介面（`authorize`／`establish`／`clear`／`hasCredential`）是唯一接觸憑證的地方。伺服器實作 httpOnly + `SameSite=Strict` session Cookie 與 CSRF 後，新增 `cookie-csrf` 策略：`establish` 不保存任何令牌、`authorize` 為非安全方法加上 CSRF 標頭（值由伺服器以非 Cookie 管道提供或 double-submit），並在 `createApiClient` 換用；呼叫端、store 與頁面不需修改。在此之前網頁端沒有 Cookie 驗證，因此也沒有 CSRF 面。

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
2. **型別層**：`WebPaths` 移除所有 `/stream` 路徑。
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
- 伺服器以 Cookie + CSRF 取代 bearer 後，換上 `cookie-csrf` 策略並移除重新整理即登出的限制。
- Windows 的 PowerShell 引導與 `make.ps1` 尚未安裝 Node（清單已有 Windows 雜湊）。
