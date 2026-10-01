# 受控出站連線與 TMDB 啟動檢查

G11.4／G14.2 部分完成。設定 TMDB 憑據後，服務啟動在監聽及啟動工作池之前，使用正式 TMDB 適配器經受控 GET client 驗證憑據。這個 client 已有產品呼叫者；完整刮削、圖片／Webhook／NFO 外鏈及舊 SDK 的接線仍待完成。

## 設定与啟動

- 只從 `TMDB_API_KEY` 或 `TMDB_API_KEY_FILE` 載入 v3 API key，兩者不能同時指定；格式為 32 個十六進位字元。環境值不自動去空白；檔案允許尾端換行，最多 4 KiB。
- 憑據不接受 JSON 設定，也不會序列化進設定 JSON。明確提供空值、無效格式、無法讀取或超限檔案會拒絕載入；錯誤不回顯值或檔案位置。
- 逐步遷移期間，兩種來源都未設定時保持本機模式，不連 TMDB。這不代表完整刮削配置驗收。
- 已設定時呼叫官方 `GET /3/authentication`；正常 TLS 驗證、精確域名限制、4 KiB 回應、最多 15 秒並接受啟動取消。200 必須帶 success=true 與 status_code=1；401／403、重試耗盡的429／5xx、非法正文、網路／憑證錯誤或重定向都阻止監聽。
- 失敗會取消 lifetime 並關閉已建立的資料庫及工具資源；預檢 client 無論成功／失敗都關閉 idle 連線。沒有增加額外生命週期 hook，仍由原單一 hook 擁有資源。

API key 會依 v3 認證放入 HTTPS query，但不會進入錯誤字串或日誌。本輪以自有 TLS listener 模擬官方主機與憑證；沒有真實外部憑據，沒有宣稱實際帳戶剩餘配額。429 只代表當次預檢被限流。[官方驗證接口](https://developer.themoviedb.org/reference/authentication-validate-key)。

## 出站防護

初次 URL 與重定向 URL 解析皆驗證 scheme、authority、port、精確域名及憑據／fragment／zone。所有重定向直接拒絕，不送第二個請求，也不轉送 query 憑據；後續圖片能力若需重定向，必須另行接入設計中的逐跳檢查。

DNS 最多 64 答案，整批先驗證；只要包含禁用地址就拒絕，不會挑另一個公網答案繼續。5 秒解析與連線預算內，實際 dial 只用驗證後的具體 IP，沒有第二次 hostname 解析。重新建連時重新驗證 DNS；Keep-Alive 保持已驗證的對端。

IP 規則先將映射 IPv4 正規化，再拒絕私網、環回、link-local、未指定、多播与特殊用途範圍。政策保守排除特殊指派，包含一些官方登錄仍可全球使用的例外；IPv6 限於 2000::/3 並排除其中的特殊範圍。這是抓取用途的限制，不是完整路由可達性判定。[IPv4 登錄](https://www.iana.org/assignments/iana-ipv4-special-registry)、[IPv6 登錄](https://www.iana.org/assignments/iana-ipv6-special-registry)。

client 不採用環境代理；正式 client 不提供 resolver／dialer／CA／transport 替換 API，TLS 保持原 hostname 驗證。最多每域名 4 連線、16 idle、32 KiB 回應標頭；正文由呼叫能力設定上限。DNS／TLS／socket／url.Error 都轉成固定安全錯誤；取消／超時保留 errors.Is 語意。管理 CLI 的環回控制面保持既有行為。

## 驗證與邊界

- 正式 TMDB 適配器經真 TLS 測試服務：成功、401、429、非法正文、混合公私 DNS 被拒與重定向不跟隨。私有測試 seam 只編入測試 binary；生產程式沒有該符號或不安全設定入口。
- 地址矩陣、URL／白名單、混合 DNS、固定 dial、重新建連重綁定、DNS／正文取消、大小界限、錯誤憑證與 hostname、環境代理、秘密錯誤回歸通過。
- 拿掉實際 DNS 答案防護後，正式適配器的私網案例 1 leaf 失敗；拿掉啟動接線後兩項啟動案例失敗。兩檔逐位元恢復，相關回歸再通過。
- Windows 完整 Go 29 包通過，2,830 pass events（含父測試）、435 skip events；本輪四包 207 pass events、6 個既有環境 skip。略過清單完整保留於證據，不能当作通過。完整 vet 通過；Linux 四包 race 通過，摘要輸出未列 skip 次數，沒有零略過宣稱。

[機器證據](evidence/outbound-tmdb-preflight.json)包含來源 hash、測試範圍、略過與恢復 hash。這些可控 TLS／DNS 模擬不代替真 TMDB 憑據、公網部署、全量 SDK 接線或完整配額／重試／cache 驗收。專用 SSRF 安全事件日誌與其他抓取能力後續接續。

後續 HTTP 重試已接入預檢，最多三次與總期限15秒，尊重Retry-After；當前增量驗收見[重試證據](tmdb-retry.md)，上面的測試總數保留9137aaeffa階段範圍。
