# 四語 HTTP 協商

四語資源裁剪後，舊 C# 的 ASP.NET 預設 header provider 對未知語系仍可能退回伺服器預設簡中。新增四語 header provider，無 header 時沿用設定（預設簡中），有 header 卻沒有有效可接受語系時靜默回退英文。

協商限制 header 長度 8,192 字元，支援四個 canonical locale、英文／日文中性語系及中文簡繁 script/region 映射。依 q 權重、語系匹配程度及原順序選擇；遵守 q=0 排除與 wildcard，拒絕非法 tag／q／重複參數。只替換原 header provider，既有 query／cookie 順序保持。協商結果同時套用 Culture 與 UICulture，既有 Content-Language 回應流程保持。

真實應用測試主機的 34 項 HTTP 驗收全通過：四語／未知／別名／權重／排除／畸形／超長及 query 優先。另取正式 host 的 localization options 驅動真實中介層，3 項英文回退皆無 logger 呼叫；最後專項 37 pass、0 fail/skip。完整 Debug 17 套件 4,150 Passed、21 NotExecuted、零失敗；其後只新增3項logger測試，正式來源沒有改動。Go i18n／HTTP 回歸均通過。

沒有新增舊 C# 使用者偏好 schema；Go 持久化帳號語系優先流程有既有 HTTP 回歸。G03 前端切換、未使用鍵／硬編碼檢查、完整文風與日期數字單位格式仍未完成。

[實際證據](evidence/ui-http-locales.json)。ASP.NET 預設 provider／middleware 行為查閱 [官方來源](https://github.com/dotnet/aspnetcore/tree/main/src/Middleware/Localization/src)，實際結果以上述本地 SDK10.0.400 測試為準。
