# 移除伺服器 UDP 探索

依 G05，刪除 UDP 7359 的伺服器探索實作與啟動註冊，連同探索 request 常數、回應模型及 OpenAPI 額外 schema 登記。客戶端須輸入伺服器網址連線。這是公開模型／schema 的有意裁剪，ABI 檢查仍會揭露差異，沒有加抑制。

舊 AutoDiscovery 設定仍由既有不可變遷移契約保存，但已沒有可啟用的探索服務；設定註解標明此值不開啟監聽。未改既有遷移、授權文件、原始媒體／NFO／圖片。

三項驗收使用正式測試主機：舊 flag 為 true 時 hosted services 沒有探索 host、編譯網路程序集沒有該實作；模型程序集及實際 OpenAPI 均沒有探索回應模型。三項全通過；反向暫用先前五個來源檔，同樣三項全失敗。恢復時逐位元確認所有來源，再完整 Debug 17 套件回歸：4,156 Passed、21 NotExecuted、零失敗，格式驗證通過（工作區載入警告）。

G05 保持部分完成。一般 UDP socket factory 仍被調諧器使用，直播／EPG／錄製／Channel 的服務、控制器、配置及資料尚待裁剪；未宣稱全部 SSDP／NAT／LAN 封包驗收完成。防火牆邊界見 [部署文件](deployment.md)。

[實際證據](evidence/server-discovery-removal.json)。
