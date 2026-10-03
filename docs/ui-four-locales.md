# 四語 UI 資源與回退

依 G03 與使用者明確授權，移除既有 Core 的 101 份其他 UI 翻譯，保留 zh-CN、zh-TW、ja-JP、en-US；ja.json 改為 ja-JP.json。國家、媒體語言與分級資料保持。現有使用其他 UI 語系的設定會由 LocalizationManager 靜默回退英文；未設定時使用簡中。設定更新及啟動會先解析有效語系，避免非法 culture 導致例外。

裁剪階段四份資源各有123鍵；後續[舊 HTTP 功能裁剪](legacy-removed-features.md)加入 FeatureRemoved，目前各124鍵。新增 `make i18n-check` 與兩平台 CI 步驟，檢查僅有四語、UTF-8/JSON、重複鍵、非空字串、缺失／多餘鍵及數字占位符出現次數。門禁找到日文 LyricDownloadFailureFromForItem 的舊缺漏，補回 {0}、{1}。八種隔離正反情境通過。

LocalizationManager 回歸 148 項通過。完整 Debug solution 的 17 套件共 4,116 Passed、21 NotExecuted、零失敗；Go i18n 回歸通過。變更 C# 格式通過（工具有工作區載入警告），增量品牌／gitignore／diff 通过。完整品牌殘留 14,960→14,800，仍失敗。

這批完成既有 UI 資源裁剪與 manager 回退；G03 仍部分完成。尚無 web 前端，未使用鍵／硬編碼文案門禁與完整文風／日期數字格式未驗收。這些單元測試也不能證明舊 C# HTTP 中介層的未知 Accept-Language 或使用者偏好流程；Go 既有 HTTP 協商證據保持獨立。

[驗證證據](evidence/ui-four-locales.json)。

後續[自動工作裁剪](live-feature-actors-removal.md)刪除三個專用翻譯鍵，目前四份資源各121鍵；先前階段的124鍵與驗證證據保留為當時結果。
