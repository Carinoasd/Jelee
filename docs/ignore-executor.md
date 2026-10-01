# 舊格式匹配子程序

此階段提供固定 helper 與父程序執行器；後續來源解碼與批次匹配見[匹配接線](ignore-legacy-matching.md)，正式掃描 worker 與公開舊格式 API 尚未啟用。相容來源及253組引擎案例見[來源核對](ignore-legacy-audit.md)。

## 執行合同

- `process.NewIgnoreRunner`只使用目前執行檔及固定`--internal-ignore-helper`。呼叫者不能提供程式路徑、參數、環境或資源覆寫；既有ffprobe入口限制保持。
- 每個runner最多2個批次，包含建立輸入檔的期間；忙碌立即回錯。整批最長10秒。取消／逾時經既有程序組或Job管理終止、join、reap後才返回。
- JIG2輸入最多1MiB，已解碼来源384KiB、128條路徑各4096 bytes。每個請求建立私有目錄與0600檔案，關閉writer後readonly重開；持有至child回收與結果驗證，然後關閉及刪除。磁碟I/O本身不保證阻塞核心呼叫可立即中断。
- JIR1結果只能是未匹配、正規則排除、否定納入、空白全文排除、全無效規則排除；必须逐path回覆。父程序核對原始行號、否定方向、無效行與註解政策。執行錯誤沒有partial result，未來worker必須記unknown，不能視為未匹配。

## 子程序限制

helper在讀取資料前設定限制，且不初始化服務配置或DB。Linux將RLIMIT_AS硬上限限制到2GiB（若既有上限更低則保持較低值）並關閉core dump；Windows將子程序加入PROCESS_MEMORY 2GiB的Job。前者是虛擬位址空間、後者是commit限制，不能宣稱兩者均為相同RSS上限。Go GC target128MiB只屬輔助。

引擎固定regexp2 v1.12.0，主go.mod/go.sum已增加依賴。每次regex匹配50ms逾時是次級保護，父程序整批deadline包含編譯。沒有獨立檔案系統／網路沙箱的聲明；helper只處理傳入文字，不依路徑開媒體，也不執行額外程式。

## 驗證

- Windows：process全套2.029秒、helper0.578秒、純協議0.199秒、架構0.168秒；相關vet及服務build通過。
- Linux無race：process全套26.113秒、helper0.334秒。實際child啟動後取消、100ms父timeout、busy、slot重用、Active0、暫存清空通過。
- Linux race：process67.610秒、helper1.364秒、協議1.148秒、架構1.149秒。兩項真正child資源驗收明確不在race build執行，因race預留位址與生產cap不相容；前項無race證據及新增Linux CI步驟負責此範圍。
- Windows超額VirtualAlloc及Linux超額PROT_NONE mmap均在獨立child被拒絕；未碰觸數GiB實體記憶體。
- 輸入協議5秒fuzz208771次、結果協議5秒fuzz203502次通過。引擎惡意回溯逾時不回部分結果。

PR29的foundation失敗原因是文件新增9處來源名稱，程式測試本身通過。已把精確名稱／來源連結集中至既有來源索引，其他文件引用；沒有改brand allowlist或掃描器。新增範圍brand scan現在0違規，完整歷史品牌門禁仍未通過。

下一步仍是來源家族、持久合同、scanner/report接線與效能／重掃驗收；不是完整G22或第3階段完成。