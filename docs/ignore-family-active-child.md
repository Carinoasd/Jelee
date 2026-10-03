# 活躍忽略子程序的取消與正式服務停止

使用固定正式原生 helper 與 4,000 條有界的昂貴規則，不插入人工 sleep 或替換匹配結果。先完成真正的健康檢查，再觀察 process.Stats 的 Started 增加且 Active=1，之後才取消或停止。

## 服務邊界

- 呼叫 Evaluate 的 context 取消後，不回傳部分 decisions；三秒內完成 join，Active=0。使用者取消不關閉健康服務，下一個實際 helper 請求成功，slot 可重用。
- Evaluate 仍持有服務讀鎖時呼叫 Close，Available 先變為 false，但 Close 不提前返回或移除仍使用中的自有暫存樹。取消並 join 後 Close 才清理，根暫存目錄為空，服務不可用。這兩條路徑在 Windows 與 Linux 原生 helper 上驗證。

## 正式 HTTP／PostgreSQL／Fx 流程

TestFamilyIgnoreProductionRuntimeActiveChildStop 使用同一正式 runtime 建構圖、真實密碼登入、實際監聽的 HTTP scan 准入與真實 PostgreSQL。掃描 helper Active=1 時停止 Fx，五秒停止期限內完成：

- helper Active=0、服务不可用且 Close 成功。
- HTTP listener 關閉、自有暫存根為空。
- 沒有發布掃描基線，工作沒有 owner，釋放回 queued 供接續。
- 所有自有 fixture 的原始規則、媒體與 NFO 內容保持。

為觀察正式圖內的實際 helper，公開 New 維持原有簽名並委派私有 newWithLifetime，lifetime 保留其已有服務的私有引用；沒有注入假 helper、結果或 repository。原公開建構、停止順序、helper 限額與設定合同保持。

本段證明服務 context 取消與 Fx 停止的活躍子程序回收。HTTP 的取消工作旗標在活躍 helper 期間的完整路徑、長時間壓力與其他歷史格式仍待補齊，不把它們合併宣稱完成。

## 證據

[真實原生 runtime 與 PostgreSQL 證據](evidence/ignore-family-active-child.json)：7 項頂層通過、零失敗／略過，31.625 秒，sourceUnchanged=true；包括既有公開入口／功能開關／重送／重啟、活躍 child 停止與本次兩條服務取消情境。先前停止專項 6 項頂層通過，38.184 秒。新增兩情境的 Windows 原生驗收通過，0.652 秒；probe tag 下服務原生驗收亦通過，0.501 秒。

Linux runtime／jobs／architecture race 通過，依序 1.097／1.357／1.405 秒；全 vet、runtime 最新 vet、三命令 build 通過。首次編輯重送位置時誤插入重啟區塊，編譯指出 job 作用域錯誤；移除該多餘測試區塊後驗證通過，正式 fence 與安全限制未放寬。CI 既有 ^TestFamilyIgnore 原生 runtime 步驟會執行新增驗收。

G22、第 3 階段與全案保持未完成。
