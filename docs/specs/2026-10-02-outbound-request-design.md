# G11.4 服務端出站防護設計

狀態：設計；尚未實作。原始 G11.4／G12.5 授權的例行設計選擇依使用者自主接續要求執行。現況與 SDK 缺口見[盤點](../outbound-request-audit.md)。

## 方案

| 方案 | 評估 |
| --- | --- |
| 各抓取器自行檢查 URL | 容易漏掉重定向與 DNS；不採用 |
| 共用受控 client，解析後固定具體 IP 連線 | 採用，將政策與真正的抓取器依賴注入一起驗收 |
| 只靠外部代理或防火牆 | 可作第二層限制，無法替代應用內的可執行驗收 |

## 信任邊界

服務端處理外部輸入的遠端抓取，與使用者 CLI 主動連到管理服務分開。前者拒絕環回／私網／link-local，後者必須支援本機服務。不能為了統一工具，把 CLI 預設目標封鎖，也不能讓 CLI 政策成為服務端的私網例外。

受控 client 擁有私有 http.Client／Transport，不暴露可改寫欄位。正式建構使用系統解析器與預設憑證驗證；resolver／dialer 測試替身限制在同 package 的未匯出建構。禁止正式入口任意換成無保護 transport、關閉 TLS 驗證或啟用環境代理。

## 請求、解析與連線

1. 初次請求只接受 http／https 的絕對 URL；拒絕 userinfo、不明 scheme、zone、無效 authority／port，以及另外設定的 Host。端點白名單只能收窄允許範圍，不能覆寫禁用地址政策。
2. IP 字面地址先正規化 IPv4 映射，再分類。拒絕私網、環回、link-local、未指定、多播與特殊保留地址。實作前須把選定的 CIDR 清單與官方登錄核對，不能只呼叫 IsPrivate 並宣稱涵蓋全部。
3. 域名解析接受 context，建議 5 秒與最多 64 結果的預算；空結果或任何禁用答案都拒絕，包括混合公／私網答案。
4. 只向本次已驗證的具體 IP 連線，避免驗證後又由 hostname 解析一次。TLS 仍驗證原始 hostname，不把原始 hostname 換成 IP 去繞過憑證驗證。
5. DNS、TCP、TLS 與回應標頭均有超時；回應正文讀取受總期限與能力自己的大小上限約束。取消需傳到解析、連線、正文與重試。
6. Keep-Alive 有界，重新建立連線時重新檢查 DNS。既有連線保持原來已驗證的 TCP 對端；不能因保留連線而跳過 URL／端點政策。

Go 的 Transport 接管連線與代理，Client 接管重定向與總期限；設計選擇把兩層一起封裝。[Go HTTP 官方文件](https://pkg.go.dev/net/http#Transport)。context 的連線取消不代表已建立連線的正文會自動結束，需保留 Client／request 期限。[Go Dialer 官方文件](https://pkg.go.dev/net#Dialer.DialContext)。

## 重定向與錯誤

每個 Location 解析後再驗證 URL 與目標，實際連線仍走受控解析；最多 5 次重定向，拒絕 HTTPS 降級 HTTP。跨 origin 的認證、cookie 與自訂敏感標頭不轉送。Webhook 初期直接拒絕重定向，避免重送簽章／秘密；未來如需要跳轉，另驗證簽章與端點身份。

對外只返回固定分類錯誤；保留取消／超時的 errors.Is 語意，但不把原始 url.Error、DNS／socket 錯誤或 URL query 輸出到公開回應與日誌。安全事件記固定 reason、taskId／requestId；避免記地址、密鑰、完整 URL 或原始 Location。

## 可執行驗收

- 地址矩陣：環回、私網、169.254.169.254、IPv6 link-local／ULA、映射 IPv4、特殊保留地址與有效公網地址。
- DNS：公網初始答案後轉私網、混合答案、空結果、取消、超時與結果超限；檢查具體 dial 目標且不能再 hostname 解析。
- 真 HTTP：正式抓取器注入受控 client；有效回應、正文超限、慢正文取消、Keep-Alive 與資源關閉。
- 重定向：公轉私、鏈上再次變更 DNS、超限、HTTPS 降級、跨 origin 敏感標頭與 Location 秘密不外洩。
- TLS：錯誤憑證必須失敗；正常憑證驗證 hostname。環境代理不能改變受控 dial 目標。
- 負例：只移除實際連線的保護，阻擋測試須失敗；恢復後來源逐位元一致，再跑相關完整回歸。

測試可使用自有環回 listener 配合私有 fake resolver／dialer，將測試中的允許地址送到自有服務；這是可控重綁定模擬，不是公網部署證明。測試替身不能進入正式設定。

G11.4 完成需要所有已啟用抓取能力與 SDK 的實際接線、跨平台回歸及需求指定矩陣；單有 client 單元測試仍只是基礎部分。
