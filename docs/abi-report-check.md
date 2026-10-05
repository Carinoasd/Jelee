# ABI 遷移契約檢查

> **已退役（2026-10-04）**：上游 C# 樹移出工作樹後，`ci-compat` 工作流程、`tools/abi/`、`scripts/check-abi-report.py`、`scripts/test_abi_guard.py` 與 `scripts/fixtures/abi-legacy-report.json` 一併刪除，本門禁不再執行。以下內容保留為當時的驗證紀錄，原始檔可從標籤 `upstream-csharp-final` 取回，見[許可證與來源](LICENSE-COMPLIANCE.md)。

品牌改名與退役功能刪除會改變舊 .NET 二進位 API。G00 要求內部名稱改為 Jelee，G05／G11.5 要求裁剪直播與探索，G28 將 .NET 定位為遷移期參考。G24 另外要求 HTTP／JSON／Header 客戶端相容；本門禁通過不代表 G24 已完成。

## 基準與已核准差異

保留原來八組共同祖先與 PR HEAD 的實際 DLL 比較，保存原始輸出與結束碼。`tools/abi/expected-breaks.json` 列出目前52條精確診斷，每項包含程序集比較、診斷碼、完整符號、完整訊息、原需求與來源提交；沒有診斷碼全域豁免或通配規則。

其中48條來自命名模組的程序集與namespace改名。單純承認這48條會漏掉新名稱下的API退化，因此增加第九組真實DLL比較：以已推送的 `202b813a955cfc64ab87992484bf00b8aa72221a` 建置命名專案及其正常引用，再與HEAD的命名程序集比較。該基準尚未發布為SemVer版本；不能換成每次PR HEAD。此比較不允許已核准破壞項，其餘七組仍維持共同祖先基準。

## 判讀與失敗條件

工具固定為ApiCompat `10.0.401`，安裝在CI工作目錄 `.tools/abi/10.0.401`。安裝後實際執行 `--version`，判讀器核對完整版本 `10.0.401+e34a38d2ae1fc26406a317517196e55c68ff83ab`，不把安裝請求當成二進位版本證據。Naming建置工作寫入實際checkout SHA收據。判讀器檢查兩份收據、九組完整報告與各自的原始結束碼。

工作明確設定 `DOTNET_CLI_UI_LANGUAGE=en-US`。由於實測此變數不能獨自固定Windows獨立ApiCompat程式的語系，僅在工具呼叫時另設 `DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1`，使文字診斷契約固定為英文；不修改產品或建置的全球化設定。兩個新增／調整工作均設20分鐘上限。

判讀器對非空行作精確、多重集合比較；它不會用模糊regex忽略未知行。新增診斷、已核准項不再出現、重複項、未知普通錯誤文字、缺報告／狀態／組件、非預期結束碼、空報告或不同工具版本均失敗。缺失或空DLL在呼叫工具前明確記錄125；損壞DLL的錯誤輸出也不能當成成功。只有預期相容的組合可接受工具exit 0及其完整成功標記。

原始報告、結束碼、驗證JSON與job summary均保留於14天artifact。成功摘要明示52條有意破壞，不宣稱舊ABI保持不變。任何檢查失敗仍使工作失敗，沒有continue-on-error或空介面／dummy參數。

## 官方工具限制

[官方CLI](https://learn.microsoft.com/en-us/dotnet/fundamentals/apicompat/global-tool)提供精確suppression與unused檢查選項。但10.0.401的[unused輸出](https://github.com/dotnet/sdk/blob/v10.0.401/src/Compatibility/ApiCompat/Microsoft.DotNet.ApiCompat.Shared/SuppressionFileHelper.cs#L65)呼叫普通LogError，而[退出碼](https://github.com/dotnet/sdk/blob/v10.0.401/src/Compatibility/ApiCompat/Microsoft.DotNet.ApiCompat.Shared/ValidateAssemblies.cs#L99)只檢查HasLoggedErrorSuppressions。因此本門禁自行核對精確集合，不依賴unused suppression的退出碼。

## 驗證

```sh
python3 -B scripts/test_abi_guard.py
python3 -B scripts/check-abi-report.py
```

腳本fixtures保留HEAD `533c21881d44553452b202638f13d06628bccee4` 的八組真實CI文字輸出；第九組成功文字僅供parser測試。這些測試涵蓋unexpected、stale、duplicates、缺檔、exit127、未知錯誤、版本與基準不符。它們不代表真實Naming組件已通過新基準。

本機已以SDK `10.0.400` 從固定基準與HEAD `cfba1009631a3ada2bc76b884f6616af8cb27be7` 的隔離來源快照建置真實Naming程序集，正向ApiCompat與判讀器均exit 0。將VideoResolver改為internal以移除公開型別、為IsVideoFile加入實際使用的StringComparison參數，均成功建置，工具分別報CP0001／CP0002且判讀器exit 1。還原真實歷史探索模型後，該模型比較exit 0，但已核准刪除變成stale，判讀器正確exit 1。缺失與截斷的真實DLL亦被拒絕。官方unused suppression已實際重現工具exit 0、判讀器exit 1。

上述合併判讀使用的舊八組仍是已保存的CI原始報告；本機重新執行的是Naming正向與隔離反例，並未重建或重新比較舊八組。20項輕量parser測試通過。完整版本、結果與原始日誌SHA256見[實測摘要](evidence/abi-guard-validation.json)。所有實驗只改隔離快照，結束後恢復；正式工作樹未套用反例。

推送後，提交 `0d7fb57971f139daf062ce37a28816e5c5c4577d` 的[遠端 CI](https://github.com/Carinoasd/Jelee/actions/runs/37042236978)已完成全部九組真實 DLL 比較：三個建置工作及 Difference 均成功，52 條歷史診斷完全吻合，新 Naming 基準沒有差異。原始 artifact 已下載核對，SHA256 記於實測摘要。本機八組歷史報告與這次遠端九組實際比較是不同證據。
