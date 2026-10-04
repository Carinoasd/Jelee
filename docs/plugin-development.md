# 網頁端插件開發（G32）

狀態：SDK 1.0.0（第 15 階段）。日期：2026-10-04。架構與安全邊界的決策見 [前端架構決策紀錄](frontend-adr.md) 的「插件體系」一節。

Jelee 網頁端只做瀏覽與管理，不播放媒體（G27）。插件同樣不能提供任何播放、串流、投放或子母畫面入口：SDK 沒有這類 Hook，`route.register` 拒絕播放字彙的路徑，建置時的禁播門禁也會掃描插件原始碼與產物。

## 快速開始

插件是 `web/src/plugins/official/<名稱>/` 下的一個目錄，與網頁端一起建置與審查（目前不支援從遠端載入插件，見「安全邊界」）：

```
web/src/plugins/official/item-facts/
  manifest.json        清單：載入任何程式碼前先驗證
  index.ts             入口（manifest.entry），default export definePlugin(...)
  messages.ts          四語訊息（zh-CN、zh-TW、ja-JP、en-US，鍵必須一致）
  ItemFactsTab.vue     元件一律以 () => import(...) 懶載入
```

```ts
// index.ts
import { definePlugin } from "@jelee/plugin-sdk";
import { messages } from "./messages";

export default definePlugin({
  messages,
  setup(context) {
    context.register("media.detail.tabs", {
      id: "facts",
      title: "tab.title", // 插件自己的訊息鍵
      component: () => import("./ItemFactsTab.vue"),
    });
  },
});
```

```vue
<!-- ItemFactsTab.vue -->
<script setup lang="ts">
import { usePlugin, type PluginItem } from "@jelee/plugin-sdk";
const props = defineProps<{ item: PluginItem }>();
const { t, api, settings } = usePlugin();
</script>
```

插件原始碼只能 import `vue` 與 `@jelee/plugin-sdk`。ESLint（`eslint.config.js` 的插件區塊）禁止 import 宿主模組（`@/…`）、`vue-i18n`、`vue-router`、`pinia`、`openapi-fetch`，禁止 `getCurrentInstance`／`inject`／`provide`，也禁止直接使用 `fetch`、`XMLHttpRequest`、`WebSocket`、`EventSource`、`localStorage`、`sessionStorage`、`indexedDB`、`document.cookie`、`navigator.sendBeacon`。範本中的文字必須來自 `t()`（`no-raw-text`）。

`setup()` 必須同步完成登記、不得發請求；資料在元件內透過 `context.api` 讀取。`setup()` 回傳後 `register()` 即失效。

## Manifest（G32.2）

| 欄位 | 規則 |
| --- | --- |
| `id` | 小寫、點分 2–4 段，例如 `vendor.name`；同 ID 第二個插件被拒絕 |
| `name`、`description` | 四種語言都要有非空文字（名稱 ≤80、描述 ≤400 字元） |
| `version` | 嚴格語意化版本 `MAJOR.MINOR.PATCH[-pre][+build]` |
| `sdkVersion` | 版本範圍：`^1.0.0`、`~1.2.0`、`1.x`、`>=1.0.0 <2.0.0`、`a || b`、`*` |
| `minJeleeVersion` | 最低 Jelee 版本（語意化版本），與伺服器 `GET /api/v1/system` 回報的 `version` 比較（伺服器建置版本，等於 OpenAPI `info.version`）；讀不到時改比網頁端 `web/package.json` 的 `version`（兩者隨同一個映像發佈） |
| `permissions` | 不重複的權限陣列，見下表；未知權限拒絕 |
| `hooks` | 會用到的 Hook，不重複、非空；未知 Hook 拒絕；`register()` 未宣告的 Hook 會讓 setup 失敗 |
| `dependencies` | `{ "<插件 ID>": "<版本範圍>" }`；依賴必須存在、版本符合且已啟用，否則插件為「依賴未滿足」 |
| `entry` | 清單旁的模組，例如 `./index.ts`；不可跨目錄或遠端網址 |
| `author` | 選填，1–80 字元 |

其他欄位一律拒絕。驗證在 `web/src/plugins/sdk/manifest.ts`；不通過的插件**不會載入任何程式碼**，管理頁以介面語言列出每一條原因（例如「需要 SDK ^2.0.0，目前為 1.0.0。」）。

## 權限

| 權限 | 開放的能力 |
| --- | --- |
| `catalog.read` | `api.item(id)`（`GET /api/v1/items/{id}/details`）、`api.itemSources(id)`（`GET /api/v1/items/{id}/sources`，只有容器、大小、時長、解析度）、`api.libraries()`（`GET /api/v1/libraries` 第一頁） |
| `user.read` | `api.me()`：名稱、顯示名稱、語言、是否管理員；沒有使用者 ID、工作階段或憑證 |
| `settings.storage` | `settings.get/set/remove/keys`：插件自己的設定命名空間 |
| `ui.routes` | 宣告 `route.register` 的前提 |
| `ui.theme` | 宣告 `theme.token` 的前提 |

`api` 只有上表這些唯讀方法，沒有通用請求、沒有寫入；回傳值是凍結的複本；錯誤只帶伺服器錯誤碼（`PluginApiError.code`/`status`）。未宣告權限就呼叫會丟出 `PluginPermissionError`，而且不發出任何請求。伺服器仍對每個請求檢查使用者權限，插件拿不到比使用者更多的資料。

## Hook（G32.1）

型別定義在 `web/src/plugins/sdk/hooks.ts`，`HookMap` 對應每個 Hook 接受的內容。`id` 與路徑段落為小寫英數與連字號（≤48 字元），同一插件同一 Hook 內不可重複。

| Hook | 內容 | 宿主呈現位置 |
| --- | --- | --- |
| `media.detail.tabs` | `{ id, title, component }`，元件收到 `item` | 條目詳情頁的「插件頁籤」區（ARIA tablist，方向鍵／Home／End 切換；只掛載選中的頁籤） |
| `metadata.panel` | `{ id, title, component }`，元件收到 `item` | 條目詳情頁的「插件面板」區（每個面板一個 h2） |
| `item.action` | `{ id, label, when?(item), run(item, ui) }` | 條目詳情頁標語下方的操作列；`run` 拋錯或 reject 時顯示錯誤提示並記入失敗次數 |
| `settings.section` | `{ id, title, component }` | 設定頁最下方，每個插件一張卡片 |
| `library.toolbar` | `{ id, component }`，元件收到 `libraryId` | 媒體庫頁工具列 |
| `theme.token` | `{ id, tokens(), darkTokens?() }` | 以構造樣式表覆寫設計 token；可覆寫的 token 見 `themeTokenNames`；值與管理員 CSS 同規則檢查（無 `url()`、`expression()`、標記、分號或大括號），不合規的值與名稱直接略過 |
| `route.register` | `{ path, title, component }` | `/x/<插件 ID>/<path>`，需登入；標題由宿主渲染為 h1；停用插件即移除路由；重新整理深層連結時等插件載入後再解析 |
| `command.palette` | `{ id, label, keywords?, run(ui) }` | 已可登記並在管理頁計數；**宿主尚無命令面板 UI** |
| `webhook.eventType` | `{ eventType, label, description? }` | 已可登記並在管理頁計數；**Webhook 頁尚未顯示插件提供的標籤**（事件目錄仍以伺服器為準） |

`PluginItem` 是條目顯示資料的凍結複本（ID、媒體庫 ID、類型、標題、原始標題、年份、是否有簡介、類型標籤、外部 ID、NFO 狀態），不含路徑或任何直投網址。

## 上下文（`usePlugin()` / `setup(context)`）

- `t(key, params?)`：插件自己的訊息，依目前介面語言（反應式），缺語言時退回 en-US，缺鍵時回傳鍵名。訊息以 `{name}` 為佔位符；插件訊息不會覆蓋宿主目錄。
- `locale()`：目前介面語言。
- `settings`：見下節。
- `ui.notify(key, tone?, params?)`：以插件訊息顯示提示。
- `api`：見「權限」。
- `id`、`manifest`、`sdkVersion`、`jeleeVersion`。

上下文物件與 `api`、`settings`、`ui` 都是凍結的閉包，沒有任何屬性指向 API 用戶端、憑證策略或 CSRF 值（測試會走訪整個物件圖確認）。

## 設定命名空間（G32.4）

`settings.get(key, fallback)` 回傳反應式的值（元件讀取後，`set()` 會觸發重繪）；型別與 fallback 不同的已存值不會交給插件。鍵為 `^[A-Za-z][A-Za-z0-9_.-]{0,63}$`，值必須是 JSON（巢狀最多 8 層，可含 `null`），單一插件最多 64 個鍵、16 KiB；伺服器以相同上限再驗一次。每個插件只能透過自己的上下文存取自己的命名空間。

命名空間分兩層：

- **全站值**：存在伺服器 `/api/v1/site/plugins` 的 `settings.<插件 ID>`，所有已登入使用者讀得到（停用中的插件除外）。管理員在設定頁呼叫 `set()`／`remove()` 時寫入這一層，對所有人生效；寫入帶版本號，被其他管理員搶先時宿主會顯示錯誤並重新載入伺服器上的值。**全站值對每個使用者可讀，絕對不要存密鑰或憑證。**
- **瀏覽器值**：一般使用者的 `set()`，以及未登入或讀不到伺服器時任何人的 `set()`，存在本瀏覽器 `localStorage` 的 `jelee.ui.v1.plugin-settings.<插件 ID>`，只在該瀏覽器覆蓋全站值。

`get()` 先看瀏覽器值，再看全站值，`keys()` 為兩者聯集。管理頁「清除插件設定」同時清除全站值與本瀏覽器的值，清除後插件會重新啟動。啟用狀態與順序同樣存在伺服器（管理員變更即對所有人生效），讀不到伺服器時沿用本瀏覽器的副本。

## 隔離與失敗（G32.3）

| 失敗 | 結果 |
| --- | --- |
| 清單不合法、SDK 範圍不含 1.0.0、Jelee 版本過舊 | 「已拒絕」，不載入程式碼 |
| 依賴缺少、版本不符或未啟用 | 「依賴未滿足」，不載入程式碼 |
| 入口載入失敗、匯出不是合法定義（四語訊息鍵不一致也算） | 「啟動失敗」 |
| `setup()` 拋錯、登記未宣告的 Hook、內容不合法（例如播放字彙路徑） | 「啟動失敗」，已登記的內容全部丟棄 |
| 元件 setup／render／watcher／事件處理拋錯、懶載入失敗或逾時（15 秒） | 該插件的區塊換成「插件『…』發生錯誤」提示與「重試」，頁面其他部分照常；失敗次數與最後錯誤顯示在管理頁 |
| `item.action` 拋錯、`when()` 拋錯、`theme.token` 拋錯 | 錯誤提示或略過該項，記入失敗次數 |

## 管理（G32.4）

管理員在「管理 → 插件」（`/admin/plugins`）可以：啟用／停用（立即生效，路由、token、頁籤同步增減）、以按鈕或 Alt+↑／Alt+↓ 排序（決定各插件內容出現的先後）、查看 ID、版本、SDK 範圍、最低 Jelee 版本、入口、作者、依賴、權限說明、Hook 與登記數、拒絕原因、依賴問題、棄用警告與攔截的錯誤，並可重新啟動或清除插件設定。官方範例中「條目資料概況」預設啟用、「強調色主題範例」預設停用。啟用狀態與順序目前只存在本瀏覽器（缺口見 ADR）。

## 官方範例（G32.5）

| 插件 | 示範內容 |
| --- | --- |
| `jelee.item-facts`「條目資料概況」 | `media.detail.tabs`（元資料完整度、外部資料庫連結、按需讀取檔案摘要）、`item.action`（複製條目 ID，剪貼簿不可用時由宿主顯示錯誤）、`settings.section`（外部連結開關，預設關閉）；權限 `catalog.read`、`settings.storage`。外部連結只接受格式正確的 TMDB／IMDb ID，`rel="noopener noreferrer"` 並以隱藏文字標示「在新視窗開啟」 |
| `jelee.accent-tokens`「強調色主題範例」 | `theme.token`（三套對比度達 WCAG AA 的強調色，亮暗各一組，依插件設定反應式更新）、`settings.section`（選色與圓角）、`route.register`（`/x/jelee.accent-tokens/preview` token 預覽頁）；權限 `ui.theme`、`ui.routes`、`settings.storage` |

## 版本與棄用策略（G32.6）

- SDK 版本為 `web/src/plugins/sdk/version.ts` 的 `SDK_VERSION`（目前 `1.0.0`），遵守語意化版本：
  - **PATCH**：只修正錯誤，不改型別或行為約定。
  - **MINOR**：只新增 Hook、權限、上下文 API 或選填欄位；既有插件不需修改。
  - **MAJOR**：可以移除或改變 Hook、權限與上下文 API；必須在本文件「遷移說明」新增一節，逐項寫明舊用法、新用法與替代方案。
- 宿主同時只支援一個 SDK 主版本；`sdkVersion` 範圍不含目前版本的插件在載入前被拒絕，管理頁顯示需要與目前的版本。
- 棄用：要移除的 Hook 先列入 `deprecatedHooks`（`since`、`removedIn`、`replacement`）。宣告了已棄用 Hook 的插件仍正常載入，管理頁顯示警告與替代方案。棄用項目至少保留一個 MINOR 版本且不少於六個月，只在下一個 MAJOR 移除；移除後宣告它的插件會因「未知 Hook」被拒絕。
- 插件自身版本（`version`）同樣使用語意化版本，供其他插件以 `dependencies` 範圍引用。

### 遷移說明

SDK 1.0.0 是第一個版本，尚無遷移項目。之後每個 MAJOR 版本在此新增「1.x → 2.0」一節。

## 安全邊界與限制

- 插件與網頁端在同一個 JavaScript 執行環境（同源、同 realm）。SDK 的受限客戶端、凍結的上下文與 ESLint 規則構成「能力邊界」：守規矩的插件只能拿到上述能力，拿不到令牌（令牌只在 HttpOnly Cookie）、CSRF 值（只在 API 層閉包）或 API 用戶端。
- 這個邊界**擋不住刻意惡意的同源程式碼**：同源腳本仍可直接 `fetch('/api/v1/auth/csrf')` 取得 CSRF 值並以使用者身分呼叫 API，或透過 Vue 內部結構找到宿主物件。因此目前只接受與網頁端一起建置、經過審查的插件，不支援遠端載入或使用者上傳插件；真正的隔離需要 iframe sandbox／獨立來源加 postMessage 協定，或伺服器端的插件權限範圍（列為後續，見 ADR）。
- 伺服器對每個請求都重新檢查使用者權限（G35.2），插件無法取得使用者本來看不到的資料。

## 測試

- `web/src/plugins/sdk/sdk.test.ts`：語意化版本與範圍、Manifest 每種拒絕原因、棄用警告、路由路徑。
- `web/src/plugins/host/host.test.ts`：拒絕不載入、重複 ID、啟停與排序即時生效並保存、依賴、載入／定義／setup／未宣告 Hook 失敗隔離、`register()` 關閉、上下文物件圖不含用戶端／憑證／CSRF、受限客戶端只發固定 GET 且無 CSRF／Authorization 標頭、未授權不發請求、錯誤只帶錯誤碼、設定命名空間隔離、渲染錯誤只降級該插件、頁籤鍵盤操作、操作失敗提示。
- `web/src/plugins/official/official.test.ts`：兩個官方插件的實際行為、token 覆寫與惡意 token 值被略過、路由註冊與深層連結。
