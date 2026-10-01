# ABI 報告與檢查

PR #46 的 c728e71bc9 工作流程已完成組件比較，但留言步驟因缺少原上游 JF_BOT_TOKEN 失敗。命名組件／命名空間變更也確實產生 CP0001、CP0004 差異，不能宣稱維持二進位相容。

本次將完整輸出保存為 abi-report artifact（14 天）與 job summary，移除留言步驟與 PR 寫入權限。每組比較檢查兩側 DLL，保留工具結束碼；任何缺檔或非零結束碼都讓工作失敗。沒有加入差異抑制，也沒有吞掉工具錯誤。

本地從 YAML 擷取實際 Bash 程式，以隔離暫存目錄及模擬工具驗證：相容 exit 0；差異、工具 exit 127、缺檔均 exit 1。百分比文字完整保留，summary 與 artifact 報告一致。這是工作流程測試，實際 DLL 比較結果仍以新 CI 為準。

工具介面依據：[Microsoft API 相容性工具](https://learn.microsoft.com/zh-tw/dotnet/fundamentals/apicompat/global-tool)。
