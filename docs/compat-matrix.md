# 客戶端探測與已移除能力

本表描述目前 Go 入口的實際合同。尚未驗收完整第三方客戶端握手與 UI 行為，不能將本表當成全部舊協定相容。

| 探測表面 | Go 回應 | 行為 |
| --- | --- | --- |
| `/LiveTv` 及其子路徑 | HTTP 501，`feature_removed` | 直播、EPG、調諧器、錄製、計時器均不啟用 |
| `/Channels` 及其子路徑 | HTTP 501，`feature_removed` | 頻道功能不啟用 |
| `/Dlna` 及其子路徑 | HTTP 501，`feature_removed` | 不提供 DLNA HTTP 功能 |
| `/api/v1/system` | HTTP 200 | dlna、discovery、liveTv、epg、tuners、recordings、channels 全為 false |
| 伺服器 UDP 7359 探索 | 舊 C# 探索 host 已刪除 | 客戶端輸入服務網址連線；LAN 封包驗收仍待完成 |

三類 HTTP 根路徑大小寫不敏感；只比對完整第一段，不影響相似名稱或其他根路徑。所有方法及子路徑都拒絕啟用。既有 Host 驗證仍先回400，轉換／HLS／DASH路徑仍先回409，debug路徑仍回404。

這些是公開的能力拒絕探測，不查驗帳號、不查資料庫、不開啟媒體、不建立工作。錯誤 envelope 包含 code/message/details/traceId，依 Accept-Language 提供四語、預設簡中／未知英文。HEAD 回應仍為501；HTTP傳輸層依HEAD規則不傳送正文。持久化使用者語系優先仍由已驗證的正式驗證流程處理，此公開探測不查詢使用者設定。

OpenAPI 的 `x-jelee-removed-features` 擴充欄位列出三個根路徑、狀態與錯誤碼；其值另有HTTP一致性回歸。17條路徑×6方法×4語系×2開關狀態，共816請求通過，沒有後端／媒體呼叫。另驗證Host／轉碼／debug優先、相似與不同根路徑404、HEAD與OpenAPI；證據見[探測驗證](evidence/removed-feature-http.json)。

G05仍部分完成：舊C#的直播與Channel控制器已刪除並提供[明確拒絕入口](legacy-removed-features.md)；其餘調諧器、EPG、錄製、Channel服務與排程仍待移除，相關資料、設定、翻譯鍵與圖示也未全部清理。沒有將舊C#內部全部功能與排程宣稱為已關閉。防火牆與探索裁剪詳[部署](deployment.md)、[探索裁剪](server-discovery-removal.md)。

另外以真正loopback TCP/HTTP驗證HEAD：501、正文長度0、未知語系回en-US，沒有後端／媒體呼叫；Windows與Linux race回歸通過。

## 舊 C# HTTP 入口

直播／頻道控制器與專用 DTO 已刪除，三類根路徑回501／feature_removed，四語／HEAD／設定不變／OpenAPI已驗證。直播設定仍先驗證授權，受限IP先回既有503；[完整合同與證據](legacy-removed-features.md)。內部服務與排程仍待移除。
