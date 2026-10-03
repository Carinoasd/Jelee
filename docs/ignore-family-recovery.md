# 合併忽略工作的取消與租約過期

正式 app 准入、真實 PostgreSQL 與原生 scanner／worker 驗證兩個情境：

- **復核中取消**：完成實際來源觀察後，在舊來源復核處放置可取消的測試屏障；透過 app.Cancel 寫入取消要求，正式 heartbeat 傳播取消。工作終態 Cancelled、Missing=0、沒有發布基線、helper Active=0，全部原始 fixture 檔案內容保持。此情境驗證復核取消；屏障本身沒有正在執行的 helper，不作活躍子程序中途取消聲明。
- **租約過期恢復**：先以舊 owner 掃描並保存正式批次，將 fixture 租約設為過期。舊 owner 的 progress、合法批次保存及發布全部回傳 ErrJobLeaseLost。新正式 runner 認領同一工作，attempts=2，成功發布正確排除結果與兩家族來源報告，helper Active=0。

首次編譯用錯 FinishFamilyIgnoreJob 參數，已按既有合同修正；首輪原生測試 1 pass／1 fail，舊 lease 保存測試使用空批次，先被輸入驗證拒絕。改用實際原生 scanner 已保存的合法批次後，專項 2 pass／0 fail／0 skip，27.588 秒。沒有放寬輸入驗證、租約 fence 或正式實作。

[原生 PostgreSQL 回歸證據](evidence/ignore-family-recovery.json)：9 項頂層 pass、0 fail、0 skip，38.811 秒，sourceUnchanged=true。包含原生儲存、正式發布、NFO 排除、主動釋放恢復、租約過期恢復、取消、來源變更、unknown 與兩組六輪重掃。Windows PostgreSQL 套件可執行測試及 vet 通過；沒有 Windows 真實 PostgreSQL 整合聲明。

CI 的既有原生 family worker 選取式涵蓋新增兩測試。完整混合負載、活躍 helper 取消的服務完整鏈、規模與其他舊格式仍需驗收，G22保持部分完成。
