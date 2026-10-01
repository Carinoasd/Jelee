# 舊 HTTP 直播與頻道入口裁剪

依 G05，刪除直播及 Channel 控制器、兩個僅供控制器使用的 DTO。相關根路徑所有方法改由正式 HTTP 中介層回 501／feature_removed，拒絕回應包含 error/code/message/details/traceId、no-store 與四語 Content-Language；HEAD 沒有正文。前綴只比對完整路徑段，大小寫不敏感，不讀取 query。四語訊息加入既有 Core 資源，目前各124鍵，未在產品程式硬編碼翻譯。

中介層位於既有語系、HTTPS、驗證授權及 IP 檢查之後。直播設定路徑仍保留原授權限制：未登入 GET／POST 先401，通過授權後501；受限遠端 IP 保持既有503。登入與否都不能透過這些入口啟用調諧器或錄製。一般驗證流程可能查帳號，未宣稱舊 C# 請求完全不查後端。

正式測試 host 的15條路徑×7方法×6語系共630請求通過，每次核對直播設定未變；設定矩陣帶正式登入憑證。另有登入寫入、未登入設定、3個遠端封鎖、相似前綴及 query、程序集刪除與真 OpenAPI，共643項全部通過。OpenAPI 不再刊載直播／Channel 功能或專用 DTO。暫換回舊五個正式來源，程序集與 OpenAPI 兩項反向測試全部失敗；未執行舊功能的寫入矩陣。最後逐位元恢復來源，再完整 Debug 回歸：17套件4,798 Passed、21 NotExecuted、零失敗；格式及 Windows／Linux 四語門禁通過。

G05保持部分完成：核心服務、EPG／Channel 排程、調諧器、動態媒體來源、設定工廠與存量資料仍待裁剪；沒有宣稱全功能退役或真客戶端／LAN封包驗收完成。公開型別與路由的刪除會形成真實 ABI／OpenAPI 差異，檢查保留。

[實際證據](evidence/legacy-removed-features.json)。
