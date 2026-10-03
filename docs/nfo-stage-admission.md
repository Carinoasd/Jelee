# NFO 檔案準備的去重與讀取准入

`Writer.StageCommitFiles` 現在先加入工作去重，再讀取 job-owned payload；只由 owner 取得 I/O 配額後呼叫 `GetNFOWriteTask`。讀取完成釋放 I/O，後續依序取得 CPU 重建／解析，再取得 I/O 保存計畫與準備檔案，維持 Total=1 可執行的非巢狀順序。

去重 key 使用結構化編碼的 SHA256，包含 repository 型別／實例、job ID、owner／generation、sequence、固定 journal token、私有 root／relative path、Source stamp 及 maxBytes。另沿既有 `acquireIntent` 核對 root／parent／file 的實體；相同內容但不同實體不能加入同一工作。pointer repository 在整個 flight 由 closure 保持存活；非 pointer 實作各自執行。key 不輸出私有路徑或 payload。

等待者取消只退出自己的等待；owner 取消後等待 repository 及檔案操作返回，才釋放配額與意圖引用。完成結果不快取；後來呼叫重新讀取並核對，遇到既有 stage 仍拒絕碰撞，不能猜測已提交或刪掉保留證據。

## 驗證

Windows owned fixture 的100個並行呼叫共用一次 payload 讀取、一次 plan 與 ready 保存，原 target 完整 bytes 保持。測試另覆蓋 job／owner／generation／sequence／token／repository／root／實體／stamp 不共用；讀取時持 I/O 而非 CPU，配額滿載及取消在 repository 呼叫前拒絕，沒有檔案副作用；等待者取消不釋放 owner 配額，owner 取消等待清理，完成後意圖與配額清空。

隔離 overlay 移除預讀配額，`TestStageCommitFilesReadAdmission` 確實失敗於 repository 被呼叫；正式來源保持。Windows NFO／architecture340通過事件／2既有symlink條件跳過；Linux NFO／architecture／jobs race568通過事件／1個Windows專屬跳過。真 PG 回歸範圍與來源雜湊見[安全報告](evidence/nfo-stage-admission.json)。本批沒有新 SQL；schema50 的完整434根 PG 回歸屬先前 d8c3e42 來源，不能當成本批重新跑過。

## 尚缺的准入與恢復

I/O 讀取准入限制同時讀取數量，並不限制等待 CPU／後續 I/O 時仍保留的全部 payload bytes。後續[原始 payload 生命週期配額](nfo-payload-budget.md)已按三份SQL上限在讀取前保留，跨CPU／IO等待直到清理；Source、重建副本與XML物件尚未納入，整體混合記憶體／延遲、持久 plan／ready 讀取及 stage 重開續作仍需完成。沒有新增 runtime caller、正式寫回策略、target Rename、結算或恢復；完整 root／媒體／revision／policy 跨程序授權與 Windows directory metadata 耐久性仍缺。
